package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// revokeTestServer records the token of every /v1/oauth/revoke call.
type revokeTestServer struct {
	revoked atomic.Value
	srv     *httptest.Server
}

func newRevokeTestServer(t *testing.T) *revokeTestServer {
	t.Helper()
	r := &revokeTestServer{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, rq *http.Request) {
		if rq.URL.Path == "/v1/oauth/revoke" {
			var body map[string]any
			_ = json.NewDecoder(rq.Body).Decode(&body)
			r.revoked.Store(body["token"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(r.srv.Close)
	t.Setenv("SFERENCE_REMOTE_URL", r.srv.URL)
	return r
}

// TestAuthLogoutAdminOwnGrant: an own-file device grant is revoked
// server-side, removed from disk, and the gateway live-reloads to
// signed-out.
func TestAuthLogoutAdminOwnGrant(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	rev := newRevokeTestServer(t)

	cfg := testConfig(t, upstream.URL, upstream.URL)
	authFile := os.Getenv("SFERENCE_SWITCH_AUTH_FILE")
	_ = os.WriteFile(authFile, []byte(`{
		"access_token": "at-x", "refresh_token": "rt-x",
		"expires_at": 4102444800, "token_type": "Bearer"
	}`), 0o600)

	g, adminL, _ := newGateway(t, cfg, resolvedAnthropicSference(t))
	defer adminL.Close()
	stop := start(t, g)
	defer stop()

	resp := postAdmin(t, g, "/v1/admin/auth/logout")
	if resp["ok"] != true {
		t.Fatalf("logout = %v, want ok", resp)
	}
	if got, _ := rev.revoked.Load().(string); got != "rt-x" {
		t.Fatalf("revoked token = %q, want rt-x", got)
	}
	if _, err := os.Stat(authFile); !os.IsNotExist(err) {
		t.Fatalf("auth file still present after logout: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(authFile), "signed-out")); err != nil {
		t.Fatalf("logout must stamp the sign-out marker: %v", err)
	}
	block := adminAuthBlock(t, g)
	if block["signed_in"] != false {
		t.Fatalf("auth after logout = %+v, want signed_in false", block)
	}
}

// TestAuthLogoutAdminSharedCredentialSuppressed: when the served
// credential IS the shared CLI file, logout neither revokes nor deletes
// it — the sign-out marker suppresses the fallback and the reload comes
// back signed out.
func TestAuthLogoutAdminSharedCredentialSuppressed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	rev := newRevokeTestServer(t)

	home := t.TempDir()
	t.Setenv("HOME", home)
	sharedDir := filepath.Join(home, ".sference")
	if err := os.MkdirAll(sharedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sharedPath := filepath.Join(sharedDir, "credentials.json")
	_ = os.WriteFile(sharedPath, []byte(`{
		"access_token": "at-sh", "refresh_token": "rt-sh",
		"expires_at": 4102444800, "token_type": "Bearer"
	}`), 0o600)

	cfg := testConfig(t, upstream.URL, upstream.URL)
	// Undo the isolation override: the shared fallback is consulted only
	// when SFERENCE_SWITCH_AUTH_FILE is unset, and with no own file the
	// shared grant is what Load serves.
	t.Setenv("SFERENCE_SWITCH_AUTH_FILE", "")

	g, adminL, _ := newGateway(t, cfg, resolvedAnthropicSference(t))
	defer adminL.Close()
	stop := start(t, g)
	defer stop()

	if block := adminAuthBlock(t, g); block["signed_in"] != true {
		t.Fatalf("setup: want signed in from the shared grant, got %+v", block)
	}

	resp := postAdmin(t, g, "/v1/admin/auth/logout")
	if resp["ok"] != true {
		t.Fatalf("logout = %v, want ok", resp)
	}
	if got := rev.revoked.Load(); got != nil {
		t.Fatalf("a shared grant must not be revoked: %v", got)
	}
	if _, err := os.Stat(sharedPath); err != nil {
		t.Fatalf("shared file must survive a switch sign-out: %v", err)
	}
	marker := filepath.Join(home, ".sference", "switch", "signed-out")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("sign-out marker missing: %v", err)
	}
	block := adminAuthBlock(t, g)
	if block["signed_in"] != false {
		t.Fatalf("auth after logout = %+v, want signed_in false", block)
	}
}

// TestAuthLogoutAdminStaticKey: an own-file static key is removed
// without a revoke round trip (there is no grant to revoke).
func TestAuthLogoutAdminStaticKey(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	rev := newRevokeTestServer(t)

	cfg := testConfig(t, upstream.URL, upstream.URL) // own file: {"token":"sk-test-key"}
	authFile := os.Getenv("SFERENCE_SWITCH_AUTH_FILE")

	g, adminL, _ := newGateway(t, cfg, resolvedAnthropicSference(t))
	defer adminL.Close()
	stop := start(t, g)
	defer stop()

	resp := postAdmin(t, g, "/v1/admin/auth/logout")
	if resp["ok"] != true {
		t.Fatalf("logout = %v, want ok", resp)
	}
	if got := rev.revoked.Load(); got != nil {
		t.Fatalf("revoke called for a static key: %v", got)
	}
	if _, err := os.Stat(authFile); !os.IsNotExist(err) {
		t.Fatalf("auth file still present after logout: %v", err)
	}
}

// TestAuthLogoutAdminEnvRefused: an SFERENCE_API_KEY credential cannot
// be removed from the admin API — the file must survive.
func TestAuthLogoutAdminEnvRefused(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, upstream.URL)
	authFile := os.Getenv("SFERENCE_SWITCH_AUTH_FILE")
	t.Setenv("SFERENCE_API_KEY", "sk-env-key")

	g, adminL, _ := newGateway(t, cfg, resolvedAnthropicSference(t))
	defer adminL.Close()
	stop := start(t, g)
	defer stop()

	resp := postAdmin(t, g, "/v1/admin/auth/logout")
	if resp["ok"] != false {
		t.Fatalf("logout = %v, want ok:false", resp)
	}
	if resp["error"] == "" || resp["error"] == nil {
		t.Fatalf("refused logout must carry the reason: %v", resp)
	}
	if _, err := os.Stat(authFile); err != nil {
		t.Fatalf("auth file must survive a refused logout: %v", err)
	}
}

// TestAuthLogoutAdminMethodNotAllowed pins the method contract.
func TestAuthLogoutAdminMethodNotAllowed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, upstream.URL)
	g, adminL, _ := newGateway(t, cfg, resolvedAnthropicSference(t))
	defer adminL.Close()
	stop := start(t, g)
	defer stop()

	resp, err := http.Get(adminURL(g, "/v1/admin/auth/logout"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 405 {
		t.Fatalf("GET logout = %d, want 405", resp.StatusCode)
	}
}
