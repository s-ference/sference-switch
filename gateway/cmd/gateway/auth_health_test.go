package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// adminAuthBlock reads /v1/admin/auth/status and returns the JSON body.
func adminAuthBlock(t *testing.T, g *Gateway) map[string]any {
	t.Helper()
	resp, err := http.Get(adminURL(g, "/v1/admin/auth/status"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestAuthHealthSignedIn verifies the auth status reports ok when
// a credentials file with a token is present.
func TestAuthHealthSignedIn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	cfg := testConfig(t, srv.URL, srv.URL)
	g, adminL, _ := newGateway(t, cfg, resolvedAnthropicSference(t))
	defer adminL.Close()
	stop := start(t, g)
	defer stop()

	block := adminAuthBlock(t, g)
	if block["signed_in"] != true {
		t.Fatalf("signed_in = %v, want true: %+v", block["signed_in"], block)
	}
	if block["health"] != "ok" {
		t.Fatalf("health = %v, want ok: %+v", block["health"], block)
	}
}

// TestAuthHealthSignedOut verifies the auth status reports signed_out
// when no credentials are present.
func TestAuthHealthSignedOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	cfg := testConfig(t, srv.URL, srv.URL)
	// Clear credentials.
	emptyAuth := filepath.Join(t.TempDir(), "empty-creds.json")
	_ = os.WriteFile(emptyAuth, []byte(`{"token":""}`), 0o600)
	t.Setenv("SFERENCE_SWITCH_AUTH_FILE", emptyAuth)

	g, adminL, _ := newGateway(t, cfg, resolvedAnthropicSference(t))
	defer adminL.Close()
	stop := start(t, g)
	defer stop()

	block := adminAuthBlock(t, g)
	if block["signed_in"] != false {
		t.Fatalf("signed_in = %v, want false: %+v", block["signed_in"], block)
	}
	if block["health"] != "signed_out" {
		t.Fatalf("health = %v, want signed_out: %+v", block["health"], block)
	}
}

// TestAuthHealthReloginPicksUpNewKey verifies that after rewriting the
// credentials file with a new key, SIGHUP picks it up.
func TestAuthHealthReloginPicksUpNewKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	cfg := testConfig(t, srv.URL, srv.URL)
	g, adminL, _ := newGateway(t, cfg, resolvedAnthropicSference(t))
	defer adminL.Close()
	stop := start(t, g)
	defer stop()

	// Initially signed in.
	block := adminAuthBlock(t, g)
	if block["signed_in"] != true {
		t.Fatalf("initial signed_in = %v, want true", block["signed_in"])
	}

	// Clear credentials.
	authFile := os.Getenv("SFERENCE_SWITCH_AUTH_FILE")
	_ = os.WriteFile(authFile, []byte(`{"token":""}`), 0o600)

	// Trigger a re-read (simulates SIGHUP).
	g.refreshAuth()

	// Should now be signed out.
	block = adminAuthBlock(t, g)
	if block["signed_in"] != false {
		t.Fatalf("after clearing, signed_in = %v, want false", block["signed_in"])
	}

	// Write a new key.
	_ = os.WriteFile(authFile, []byte(`{"token":"sk-new-key-12345"}`), 0o600)
	g.refreshAuth()

	// Should be signed in again.
	block = adminAuthBlock(t, g)
	if block["signed_in"] != true {
		t.Fatalf("after relogin, signed_in = %v, want true", block["signed_in"])
	}
	if block["health"] != "ok" {
		t.Fatalf("after relogin, health = %v, want ok", block["health"])
	}
}

// TestAuthDeadCredentialEngagesFallbackRoute verifies that when not signed in
// (no API key), a Sference-routed request returns 503 (needs login) when no
// fallback is configured.
func TestAuthDeadCredentialEngagesFallbackRoute(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message"}`))
	}))
	defer upstream.Close()

	cfg := testConfig(t, upstream.URL, upstream.URL)
	cfg.APIKeyFallback = false
	cfg.SferenceKey = ""
	// Clear credentials.
	emptyAuth := filepath.Join(t.TempDir(), "empty-creds.json")
	_ = os.WriteFile(emptyAuth, []byte(`{"token":""}`), 0o600)
	t.Setenv("SFERENCE_SWITCH_AUTH_FILE", emptyAuth)

	g, adminL, _ := newGateway(t, cfg, resolvedAnthropicSference(t))
	defer adminL.Close()
	stop := start(t, g)
	defer stop()

	body := []byte(`{"model":"claude-opus-4-8","stream":false,"messages":[{"role":"user","content":"ping"}]}`)
	req, _ := http.NewRequest("POST", clientURL(g, "claude-code", "/v1/messages"), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Fatalf("got %d want 503 (needs login, no fallback)", resp.StatusCode)
	}
	_ = time.Now // keep time import
}

// TestAuthHealthRefreshFailedOnRevokedGrant verifies that when the stored
// device grant is terminally rejected (invalid_grant — revoked/expired/
// reuse-detected), the first request through the oauth client flips auth
// health to "refresh_failed" and records the error for the app to render
// as "reauthentication required".
func TestAuthHealthRefreshFailedOnRevokedGrant(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	// The token endpoint rejects the refresh as a dead grant.
	oauthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"This grant was revoked — sign in again"}`))
	}))
	defer oauthSrv.Close()
	t.Setenv("SFERENCE_REMOTE_URL", oauthSrv.URL)

	cfg := testConfig(t, upstream.URL, upstream.URL)
	// Replace the static key with an expired device grant.
	_ = os.WriteFile(os.Getenv("SFERENCE_SWITCH_AUTH_FILE"),
		[]byte(`{"access_token":"at-dead","refresh_token":"rt-dead","expires_at":1}`), 0o600)

	g, adminL, _ := newGateway(t, cfg, resolvedAnthropicSference(t))
	defer adminL.Close()
	stop := start(t, g)
	defer stop()

	// Credential present: signed in. (Health may already be
	// refresh_failed here — reading the admin status fetches the account
	// email through the oauth client, which discovers the dead grant.)
	block := adminAuthBlock(t, g)
	if block["signed_in"] != true {
		t.Fatalf("initial signed_in = %v, want true: %+v", block["signed_in"], block)
	}

	// Drive a request through the oauth client: the transport refreshes
	// the stale grant, gets invalid_grant, and the notify callback flips
	// the health.
	_, client, _ := g.sferenceAuthClient()
	if client == nil {
		t.Fatal("no oauth client")
	}
	req, _ := http.NewRequest(http.MethodGet, upstream.URL, nil)
	resp, err := client.Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("request with a dead grant must fail")
	}

	block = adminAuthBlock(t, g)
	if block["health"] != "refresh_failed" {
		t.Fatalf("health = %v, want refresh_failed: %+v", block["health"], block)
	}
	if block["last_refresh_error"] == "" || block["last_refresh_error"] == nil {
		t.Fatalf("last_refresh_error not recorded: %+v", block)
	}
}

// TestAuthHealthTransientRefreshErrorKeepsOK verifies a transient refresh
// failure (5xx from the token endpoint) is recorded but does NOT flip
// health off ok — the app deliberately alarms only on terminal failures.
func TestAuthHealthTransientRefreshErrorKeepsOK(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	oauthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"server_error"}`))
	}))
	defer oauthSrv.Close()
	t.Setenv("SFERENCE_REMOTE_URL", oauthSrv.URL)

	cfg := testConfig(t, upstream.URL, upstream.URL)
	_ = os.WriteFile(os.Getenv("SFERENCE_SWITCH_AUTH_FILE"),
		[]byte(`{"access_token":"at-stale","refresh_token":"rt-stale","expires_at":1}`), 0o600)

	g, adminL, _ := newGateway(t, cfg, resolvedAnthropicSference(t))
	defer adminL.Close()
	stop := start(t, g)
	defer stop()

	_, client, _ := g.sferenceAuthClient()
	req, _ := http.NewRequest(http.MethodGet, upstream.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("transient refresh failure should serve the stale token, got %v", err)
	}
	resp.Body.Close()

	block := adminAuthBlock(t, g)
	if block["health"] != "ok" {
		t.Fatalf("health = %v, want ok (transient): %+v", block["health"], block)
	}
	if block["last_refresh_error"] == "" || block["last_refresh_error"] == nil {
		t.Fatalf("transient error should be recorded: %+v", block)
	}
}
