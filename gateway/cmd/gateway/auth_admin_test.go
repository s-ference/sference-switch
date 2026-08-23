package gateway

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// TestAuthStatusServesEmail: the account card's identity comes from the
// platform's /v1/auth/me (the email rides the username field), resolved
// through the signed-in client and cached for 60s.
func TestAuthStatusServesEmail(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	meCalls := 0
	meSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/auth/me" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		meCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"username":"user@example.com","role":"user"}`))
	}))
	defer meSrv.Close()

	cfg := testConfig(t, upstream.URL, upstream.URL) // own file: {"token":"sk-test-key"}
	cfg.OAuthHost = meSrv.URL

	g, adminL, _ := newGateway(t, cfg, resolvedAnthropicSference(t))
	defer adminL.Close()
	stop := start(t, g)
	defer stop()

	block := adminAuthBlock(t, g)
	if block["signed_in"] != true {
		t.Fatalf("setup: want signed in, got %+v", block)
	}
	if block["email"] != "user@example.com" {
		t.Fatalf("email = %v, want user@example.com", block["email"])
	}

	// A second read inside the 60s window must not re-hit the platform.
	_ = adminAuthBlock(t, g)
	if meCalls != 1 {
		t.Fatalf("/v1/auth/me calls = %d, want 1 (cached)", meCalls)
	}
}

// TestAuthStatusEmailEmptySignedOut: no credential → no identity lookup,
// empty email.
func TestAuthStatusEmailEmptySignedOut(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	meSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("signed-out gateway must not call /v1/auth/me")
		w.WriteHeader(http.StatusNotFound)
	}))
	defer meSrv.Close()

	cfg := testConfig(t, upstream.URL, upstream.URL)
	cfg.OAuthHost = meSrv.URL
	authFile := os.Getenv("SFERENCE_SWITCH_AUTH_FILE")
	_ = os.WriteFile(authFile, []byte(`{"token":""}`), 0o600) // signed out

	g, adminL, _ := newGateway(t, cfg, resolvedAnthropicSference(t))
	defer adminL.Close()
	stop := start(t, g)
	defer stop()

	block := adminAuthBlock(t, g)
	if block["signed_in"] != false || block["email"] != "" {
		t.Fatalf("signed-out block = %+v, want signed_in false, empty email", block)
	}
}
