package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// oauthTestServer routes the three device-flow endpoints to per-test
// handlers.
type oauthTestServer struct {
	mu         sync.Mutex
	deviceCode func(payload map[string]string) (int, any)
	token      func(payload map[string]string) (int, any)
	revoke     func(payload map[string]string) (int, any)
}

func newOAuthTestServer(t *testing.T) (*oauthTestServer, string) {
	s := &oauthTestServer{}
	mux := http.NewServeMux()
	handle := func(fn func(*oauthTestServer) func(map[string]string) (int, any)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var payload map[string]string
			_ = json.NewDecoder(r.Body).Decode(&payload)
			s.mu.Lock()
			h := fn(s)
			s.mu.Unlock()
			status, body := 200, any(map[string]any{})
			if h != nil {
				status, body = h(payload)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(body)
		}
	}
	mux.HandleFunc("/v1/oauth/device_code", handle(func(s *oauthTestServer) func(map[string]string) (int, any) { return s.deviceCode }))
	mux.HandleFunc("/v1/oauth/token", handle(func(s *oauthTestServer) func(map[string]string) (int, any) { return s.token }))
	mux.HandleFunc("/v1/oauth/revoke", handle(func(s *oauthTestServer) func(map[string]string) (int, any) { return s.revoke }))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return s, srv.URL
}

func TestVerificationURIComplete(t *testing.T) {
	cases := []struct {
		uri, code, want string
	}{
		{"https://app.sference.com/device", "WXYZ-1234",
			"https://app.sference.com/device?code=WXYZ1234"},
		// An existing query string keeps its parameters.
		{"https://app.sference.com/device?next=%2F", "WXYZ-1234",
			"https://app.sference.com/device?next=%2F&code=WXYZ1234"},
		// Missing parts degrade to the plain URI.
		{"https://app.sference.com/device", "", "https://app.sference.com/device"},
		{"", "WXYZ-1234", ""},
	}
	for _, c := range cases {
		if got := VerificationURIComplete(c.uri, c.code); got != c.want {
			t.Errorf("VerificationURIComplete(%q, %q) = %q, want %q",
				c.uri, c.code, got, c.want)
		}
	}
}

func TestStartDeviceLogin(t *testing.T) {
	s, baseURL := newOAuthTestServer(t)
	s.deviceCode = func(payload map[string]string) (int, any) {
		if payload["client_id"] != ClientID {
			t.Errorf("client_id = %q", payload["client_id"])
		}
		if payload["device_label"] != "my-mac" {
			t.Errorf("device_label = %q", payload["device_label"])
		}
		return 200, map[string]any{
			"device_code": "dc-1", "user_code": "ABCD-EFGH",
			"verification_uri": "https://app.sference.com/device",
			"expires_in":       600, "interval": 5,
		}
	}
	dc, err := StartDeviceLogin(context.Background(), baseURL, "my-mac")
	if err != nil {
		t.Fatal(err)
	}
	if dc.DeviceCode != "dc-1" || dc.UserCode != "ABCD-EFGH" || dc.Interval != 5 || dc.ExpiresIn != 600 {
		t.Fatalf("dc = %+v", dc)
	}
}

func TestStartDeviceLoginUnknownClient(t *testing.T) {
	s, baseURL := newOAuthTestServer(t)
	s.deviceCode = func(map[string]string) (int, any) {
		return 400, map[string]any{"error": "invalid_client", "error_description": "Unknown client_id"}
	}
	_, err := StartDeviceLogin(context.Background(), baseURL, "")
	var derr *DeviceAuthError
	if !errors.As(err, &derr) || derr.Code != "invalid_client" {
		t.Fatalf("err = %v, want invalid_client", err)
	}
}

func TestStartDeviceLoginMalformed(t *testing.T) {
	s, baseURL := newOAuthTestServer(t)
	s.deviceCode = func(map[string]string) (int, any) {
		return 200, map[string]any{"device_code": "dc-1"} // missing everything else
	}
	if _, err := StartDeviceLogin(context.Background(), baseURL, ""); err == nil {
		t.Fatal("want malformed-response error")
	}
}

// fakeClock collects sleeps instead of performing them.
type fakeClock struct {
	sleeps []time.Duration
}

func (f *fakeClock) sleep(d time.Duration) { f.sleeps = append(f.sleeps, d) }

func TestPollForTokensPendingThenApproved(t *testing.T) {
	s, baseURL := newOAuthTestServer(t)
	polls := 0
	s.token = func(payload map[string]string) (int, any) {
		if payload["grant_type"] != grantTypeDeviceCode || payload["device_code"] != "dc-1" {
			t.Errorf("payload = %v", payload)
		}
		polls++
		if polls < 3 {
			return 400, map[string]any{"error": "authorization_pending"}
		}
		return 200, map[string]any{
			"access_token": "at-1", "refresh_token": "rt-1",
			"token_type": "Bearer", "expires_in": 86400,
		}
	}
	clock := &fakeClock{}
	tokens, err := PollForTokens(context.Background(), baseURL, "dc-1", 5*time.Second, 10*time.Minute, clock.sleep)
	if err != nil {
		t.Fatal(err)
	}
	if tokens.AccessToken != "at-1" || tokens.RefreshToken != "rt-1" {
		t.Fatalf("tokens = %+v", tokens)
	}
	if len(clock.sleeps) != 2 {
		t.Fatalf("sleeps = %v, want 2 interval waits", clock.sleeps)
	}
	for _, d := range clock.sleeps {
		if d != 5*time.Second {
			t.Fatalf("sleep = %v, want the server interval (5s)", d)
		}
	}
}

func TestPollForTokensSlowDownAddsFiveSeconds(t *testing.T) {
	s, baseURL := newOAuthTestServer(t)
	polls := 0
	s.token = func(map[string]string) (int, any) {
		polls++
		switch polls {
		case 1:
			return 400, map[string]any{"error": "authorization_pending"}
		case 2:
			return 400, map[string]any{"error": "slow_down"}
		default:
			return 200, map[string]any{
				"access_token": "at-1", "refresh_token": "rt-1",
				"token_type": "Bearer", "expires_in": 86400,
			}
		}
	}
	clock := &fakeClock{}
	if _, err := PollForTokens(context.Background(), baseURL, "dc-1", 5*time.Second, 10*time.Minute, clock.sleep); err != nil {
		t.Fatal(err)
	}
	if len(clock.sleeps) != 2 || clock.sleeps[0] != 5*time.Second || clock.sleeps[1] != 10*time.Second {
		t.Fatalf("sleeps = %v, want [5s 10s] (slow_down adds 5s)", clock.sleeps)
	}
}

func TestPollForTokensExpiredCode(t *testing.T) {
	s, baseURL := newOAuthTestServer(t)
	s.token = func(map[string]string) (int, any) {
		return 400, map[string]any{"error": "expired_token", "error_description": "device_code expired"}
	}
	clock := &fakeClock{}
	_, err := PollForTokens(context.Background(), baseURL, "dc-1", 5*time.Second, 10*time.Minute, clock.sleep)
	var derr *DeviceAuthError
	if !errors.As(err, &derr) || derr.Code != "expired_token" {
		t.Fatalf("err = %v, want expired_token", err)
	}
	if len(clock.sleeps) != 0 {
		t.Fatalf("sleeps = %v, want none (terminal error returns immediately)", clock.sleeps)
	}
}

func TestPollForTokensPendingPastDeadline(t *testing.T) {
	s, baseURL := newOAuthTestServer(t)
	s.token = func(map[string]string) (int, any) {
		return 400, map[string]any{"error": "authorization_pending"}
	}
	clock := &fakeClock{}
	// A 1ns expires_in means the first pending response already blows the
	// deadline — no sleep, immediate expiry error.
	_, err := PollForTokens(context.Background(), baseURL, "dc-1", 5*time.Second, time.Nanosecond, clock.sleep)
	var derr *DeviceAuthError
	if !errors.As(err, &derr) || derr.Code != "expired_token" {
		t.Fatalf("err = %v, want expired_token", err)
	}
	if len(clock.sleeps) != 0 {
		t.Fatalf("sleeps = %v, want none past the deadline", clock.sleeps)
	}
}

func TestRefreshTokensRotatedPair(t *testing.T) {
	s, baseURL := newOAuthTestServer(t)
	s.token = func(payload map[string]string) (int, any) {
		if payload["grant_type"] != grantTypeRefreshToken || payload["refresh_token"] != "rt-old" {
			t.Errorf("payload = %v", payload)
		}
		return 200, map[string]any{
			"access_token": "at-new", "refresh_token": "rt-new",
			"token_type": "Bearer", "expires_in": 86400,
		}
	}
	tokens, err := RefreshTokens(context.Background(), baseURL, "rt-old")
	if err != nil {
		t.Fatal(err)
	}
	if tokens.AccessToken != "at-new" || tokens.RefreshToken != "rt-new" || tokens.ExpiresIn != 86400 {
		t.Fatalf("tokens = %+v", tokens)
	}
}

func TestRefreshTokensRevokedIsTerminal(t *testing.T) {
	s, baseURL := newOAuthTestServer(t)
	s.token = func(map[string]string) (int, any) {
		return 400, map[string]any{"error": "invalid_grant", "error_description": "This grant was revoked — sign in again"}
	}
	_, err := RefreshTokens(context.Background(), baseURL, "rt-old")
	var derr *DeviceAuthError
	if !errors.As(err, &derr) || derr.Code != "invalid_grant" || !derr.IsTerminal() {
		t.Fatalf("err = %v, want terminal invalid_grant", err)
	}
	if !IsTerminalAuthError(err) {
		t.Fatal("IsTerminalAuthError = false, want true")
	}
	if RefreshErrorCode(err) != "invalid_grant" {
		t.Fatalf("RefreshErrorCode = %q", RefreshErrorCode(err))
	}
}

func TestRefreshTokensNetworkErrorIsNotTerminal(t *testing.T) {
	// Nothing listening on this port.
	_, err := RefreshTokens(context.Background(), "http://127.0.0.1:1", "rt-old")
	var derr *DeviceAuthError
	if !errors.As(err, &derr) || derr.Code != "network_error" {
		t.Fatalf("err = %v, want network_error", err)
	}
	if derr.IsTerminal() || IsTerminalAuthError(err) {
		t.Fatal("network errors must not be terminal")
	}
}

func TestRevokeTokenBestEffort(t *testing.T) {
	s, baseURL := newOAuthTestServer(t)
	called := false
	s.revoke = func(payload map[string]string) (int, any) {
		called = true
		if payload["token"] != "rt-1" || payload["client_id"] != ClientID {
			t.Errorf("payload = %v", payload)
		}
		return 200, map[string]any{}
	}
	if err := RevokeToken(context.Background(), baseURL, "rt-1"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("revoke endpoint not called")
	}
}
