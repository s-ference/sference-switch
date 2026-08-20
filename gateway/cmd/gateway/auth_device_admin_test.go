package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// deviceLoginTestServer serves the device-flow endpoints: device_code
// returns a fixed code; token approves after approveAfter polls.
type deviceLoginTestServer struct {
	polls atomic.Int32
	srv   *httptest.Server
}

func newDeviceLoginTestServer(t *testing.T, approveAfter int32) *deviceLoginTestServer {
	t.Helper()
	d := &deviceLoginTestServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/oauth/device_code", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code": "dc-admin", "user_code": "WXYZ-1234",
			"verification_uri": "https://app.sference.com/device",
			"expires_in":       600, "interval": 1,
		})
	})
	mux.HandleFunc("/v1/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if d.polls.Add(1) < approveAfter {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "authorization_pending"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at-admin", "refresh_token": "rt-admin",
			"token_type": "Bearer", "expires_in": 86400,
		})
	})
	d.srv = httptest.NewServer(mux)
	t.Cleanup(d.srv.Close)
	t.Setenv("SFERENCE_REMOTE_URL", d.srv.URL)
	return d
}

func postAdmin(t *testing.T, g *Gateway, path string) map[string]any {
	t.Helper()
	resp, err := http.Post(adminURL(g, path), "application/json", nil)
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

// awaitDeviceLoginState polls the status endpoint until the state matches
// or the deadline passes.
func awaitDeviceLoginState(t *testing.T, g *Gateway, want string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(adminURL(g, "/v1/admin/auth/device/status"))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&m)
		resp.Body.Close()
		if m["state"] == want {
			return m
		}
		time.Sleep(100 * time.Millisecond)
	}
	m := postAdmin(t, g, "/v1/admin/auth/device/cancel")
	t.Fatalf("device login never reached %q (last: %v)", want, m)
	return nil
}

// TestDeviceLoginAdminFlow exercises the in-UI sign-in end to end: start →
// pending with code → approval → grant persisted + gateway reloaded.
func TestDeviceLoginAdminFlow(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	newDeviceLoginTestServer(t, 2) // approve on the second poll

	cfg := testConfig(t, upstream.URL, upstream.URL)
	authFile := os.Getenv("SFERENCE_SWITCH_AUTH_FILE")
	_ = os.WriteFile(authFile, []byte(`{"token":""}`), 0o600) // signed out

	g, adminL, _ := newGateway(t, cfg, resolvedAnthropicSference(t))
	defer adminL.Close()
	stop := start(t, g)
	defer stop()

	// Reset any flow state leaked by another test.
	postAdmin(t, g, "/v1/admin/auth/device/cancel")

	startResp := postAdmin(t, g, "/v1/admin/auth/device/start")
	if startResp["state"] != "pending" {
		t.Fatalf("start = %v, want pending", startResp)
	}
	if startResp["user_code"] != "WXYZ-1234" || startResp["verification_uri"] == "" {
		t.Fatalf("start missing code/URI: %v", startResp)
	}

	// A second start while pending rejoins the same flow (same code).
	again := postAdmin(t, g, "/v1/admin/auth/device/start")
	if again["user_code"] != "WXYZ-1234" {
		t.Fatalf("rejoin returned a different code: %v", again)
	}

	final := awaitDeviceLoginState(t, g, deviceLoginApproved)

	// The grant must be on disk in v2 shape...
	data, err := os.ReadFile(authFile)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(data, &saved); err != nil || saved.AccessToken != "at-admin" || saved.RefreshToken != "rt-admin" {
		t.Fatalf("saved = %s (%v)", data, err)
	}
	// ...and the gateway must have live-reloaded it.
	block := adminAuthBlock(t, g)
	if block["signed_in"] != true || block["health"] != "ok" {
		t.Fatalf("auth after approval = %+v, want signed_in/ok", block)
	}
	_ = final
}

// TestDeviceLoginAdminCancel verifies cancel returns the flow to idle.
func TestDeviceLoginAdminCancel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	newDeviceLoginTestServer(t, 1<<30) // never approve

	cfg := testConfig(t, upstream.URL, upstream.URL)
	g, adminL, _ := newGateway(t, cfg, resolvedAnthropicSference(t))
	defer adminL.Close()
	stop := start(t, g)
	defer stop()

	postAdmin(t, g, "/v1/admin/auth/device/cancel")
	startResp := postAdmin(t, g, "/v1/admin/auth/device/start")
	if startResp["state"] != "pending" {
		t.Fatalf("start = %v, want pending", startResp)
	}
	cancelResp := postAdmin(t, g, "/v1/admin/auth/device/cancel")
	if cancelResp["state"] != "idle" {
		t.Fatalf("cancel = %v, want idle", cancelResp)
	}
}

// TestDeviceLoginAdminStartFailure verifies a device_code rejection (e.g.
// unknown client_id against a platform that predates registration) lands
// in the failed state with the error surfaced.
func TestDeviceLoginAdminStartFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "invalid_client", "error_description": "Unknown client_id",
		})
	}))
	defer srv.Close()
	t.Setenv("SFERENCE_REMOTE_URL", srv.URL)

	cfg := testConfig(t, upstream.URL, upstream.URL)
	g, adminL, _ := newGateway(t, cfg, resolvedAnthropicSference(t))
	defer adminL.Close()
	stop := start(t, g)
	defer stop()

	postAdmin(t, g, "/v1/admin/auth/device/cancel")
	resp := postAdmin(t, g, "/v1/admin/auth/device/start")
	if resp["state"] != "failed" {
		t.Fatalf("start = %v, want failed", resp)
	}
	if resp["error"] == "" || resp["error"] == nil {
		t.Fatalf("failed start must carry the error: %v", resp)
	}
	if got := fmt.Sprint(resp["error"]); got == "" {
		t.Fatalf("empty error: %v", resp)
	}
}

// TestDeviceLoginAdminMethodNotAllowed pins the method contract.

// TestDeviceLoginAdminMethodNotAllowed pins the method contract.
func TestDeviceLoginAdminMethodNotAllowed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	cfg := testConfig(t, upstream.URL, upstream.URL)
	g, adminL, _ := newGateway(t, cfg, resolvedAnthropicSference(t))
	defer adminL.Close()
	stop := start(t, g)
	defer stop()

	resp, err := http.Get(adminURL(g, "/v1/admin/auth/device/start"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 405 {
		t.Fatalf("GET start = %d, want 405", resp.StatusCode)
	}
}
