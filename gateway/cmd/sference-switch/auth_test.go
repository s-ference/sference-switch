package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sference/sference-switch/gateway/internal/auth"
)

func writeAuthJSON(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SFERENCE_SWITCH_AUTH_FILE", path)
}

func captureStdout(t *testing.T, fn func() int) (string, int) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	code := fn()
	w.Close()
	os.Stdout = old
	b, _ := io.ReadAll(r)
	r.Close()
	return string(b), code
}

// TestWhoamiWithAPIKey verifies whoami reads the API key from the credentials
// file and prints a masked key + fingerprint.
func TestWhoamiWithAPIKey(t *testing.T) {
	writeAuthJSON(t, `{"token":"sk-test-1234567890abcdef"}`)
	out, code := captureStdout(t, func() int {
		return cmdWhoami(nil)
	})
	if code != 0 {
		t.Fatalf("cmdWhoami = %d, want 0; output:\n%s", code, out)
	}
	if !strings.Contains(out, "Signed in with API key") {
		t.Fatalf("output should report API key sign-in:\n%s", out)
	}
	if !strings.Contains(out, "sk-t...cdef") {
		t.Fatalf("output should show masked key:\n%s", out)
	}
	if !strings.Contains(out, "Fingerprint") {
		t.Fatalf("output should show fingerprint:\n%s", out)
	}
}

// TestWhoamiWithEnvKey verifies whoami reads the API key from SFERENCE_API_KEY.
func TestWhoamiWithEnvKey(t *testing.T) {
	t.Setenv("SFERENCE_API_KEY", "sk-env-test-key-12345")
	writeAuthJSON(t, `{"token":""}`)
	out, code := captureStdout(t, func() int {
		return cmdWhoami(nil)
	})
	if code != 0 {
		t.Fatalf("cmdWhoami = %d, want 0; output:\n%s", code, out)
	}
	if !strings.Contains(out, "Signed in with API key") {
		t.Fatalf("output should report API key sign-in:\n%s", out)
	}
	if !strings.Contains(out, "sk-e...2345") {
		t.Fatalf("output should show masked env key:\n%s", out)
	}
}

// TestWhoamiNotSignedIn verifies whoami exits 3 when no key is found.
func TestWhoamiNotSignedIn(t *testing.T) {
	writeAuthJSON(t, `{"token":""}`)
	out, code := captureStdout(t, func() int {
		return cmdWhoami(nil)
	})
	if code != 3 {
		t.Fatalf("cmdWhoami = %d, want 3; output:\n%s", code, out)
	}
	// The 'not signed in' message goes to stderr; stdout is empty.
	// The exit code (3) is the assertion.
}

// TestWhoamiProfileFlagAccepted verifies --profile is accepted for compat
// but ignored (one credential).
func TestWhoamiProfileFlagAccepted(t *testing.T) {
	writeAuthJSON(t, `{"token":"sk-test-1234567890abcdef"}`)
	out, code := captureStdout(t, func() int {
		return cmdWhoami([]string{"--profile", "anything"})
	})
	if code != 0 {
		t.Fatalf("cmdWhoami = %d, want 0; output:\n%s", code, out)
	}
	if !strings.Contains(out, "Signed in with API key") {
		t.Fatalf("output should report API key sign-in:\n%s", out)
	}
}

// --- device-flow login -----------------------------------------------------

// stubBrowserOpen replaces the package-level browser opener for the test
// (a real `open` would fire a browser on the dev machine) and records
// URLs.
func stubBrowserOpen(t *testing.T) *[]string {
	t.Helper()
	opened := &[]string{}
	old := openBrowser
	openBrowser = func(url string) { *opened = append(*opened, url) }
	t.Cleanup(func() { openBrowser = old })
	return opened
}

// deviceFlowTestServer serves the device-flow endpoints: device_code
// returns a fixed code; token approves on first poll; revoke records.
func deviceFlowTestServer(t *testing.T) (revoked *bool) {
	t.Helper()
	wasRevoked := false
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/oauth/device_code", func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]string
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload["client_id"] != auth.ClientID {
			t.Errorf("client_id = %q, want %q", payload["client_id"], auth.ClientID)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code": "dc-test", "user_code": "ABCD-EFGH",
			"verification_uri": "https://app.sference.com/device",
			"expires_in":       600, "interval": 1,
		})
	})
	mux.HandleFunc("/v1/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at-device", "refresh_token": "rt-device",
			"token_type": "Bearer", "expires_in": 86400,
		})
	})
	mux.HandleFunc("/v1/oauth/revoke", func(w http.ResponseWriter, r *http.Request) {
		wasRevoked = true
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("SFERENCE_REMOTE_URL", srv.URL)
	return &wasRevoked
}

// TestAuthLoginDeviceFlow verifies the no-flag login: device code → poll →
// v2 grant written to the switch's own auth file.
func TestAuthLoginDeviceFlow(t *testing.T) {
	t.Setenv("SFERENCE_API_KEY", "")
	deviceFlowTestServer(t)
	opened := stubBrowserOpen(t)
	authPath := writeAuthJSONReturnPath(t, `{"token":""}`)
	out, code := captureStdout(t, func() int {
		return cmdAuth([]string{"login"})
	})
	if code != 0 {
		t.Fatalf("auth login = %d, want 0; output:\n%s", code, out)
	}
	if !strings.Contains(out, "ABCD-EFGH") {
		t.Fatalf("output should show the user code:\n%s", out)
	}
	if len(*opened) != 1 || (*opened)[0] != "https://app.sference.com/device" {
		t.Fatalf("browser opens = %v", *opened)
	}
	data, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		AccessToken  string  `json:"access_token"`
		RefreshToken string  `json:"refresh_token"`
		ExpiresAt    float64 `json:"expires_at"`
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("saved file not v2 shape: %v\n%s", err, data)
	}
	if saved.AccessToken != "at-device" || saved.RefreshToken != "rt-device" || saved.ExpiresAt == 0 {
		t.Fatalf("saved = %+v", saved)
	}
}

// TestAuthLoginAPIKeyWritesOwnFile verifies --api-key persists the legacy
// shape to the switch's own file (never the shared CLI file).
func TestAuthLoginAPIKeyWritesOwnFile(t *testing.T) {
	t.Setenv("SFERENCE_API_KEY", "")
	authPath := writeAuthJSONReturnPath(t, `{"token":""}`)
	out, code := captureStdout(t, func() int {
		return cmdAuth([]string{"login", "--api-key", "sk-static-1"})
	})
	if code != 0 {
		t.Fatalf("auth login = %d, want 0; output:\n%s", code, out)
	}
	data, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &saved); err != nil || saved.Token != "sk-static-1" {
		t.Fatalf("saved = %s (%v), want legacy shape", data, err)
	}
}

// TestAuthLogoutRevokesAndRemoves verifies logout revokes a device grant
// server-side (best-effort) and removes the own auth file only.
func TestAuthLogoutRevokesAndRemoves(t *testing.T) {
	t.Setenv("SFERENCE_API_KEY", "")
	revoked := deviceFlowTestServer(t)
	authPath := writeAuthJSONReturnPath(t,
		`{"access_token":"at-device","refresh_token":"rt-device","expires_at":9999999999}`)
	out, code := captureStdout(t, func() int {
		return cmdAuth([]string{"logout"})
	})
	if code != 0 {
		t.Fatalf("auth logout = %d, want 0; output:\n%s", code, out)
	}
	if !*revoked {
		t.Fatal("logout should revoke the grant server-side")
	}
	if _, err := os.Stat(authPath); !os.IsNotExist(err) {
		t.Fatalf("own auth file still present: %v", err)
	}
}

// TestWhoamiDeviceGrant verifies whoami renders a device grant with its
// expiry and source.
func TestWhoamiDeviceGrant(t *testing.T) {
	t.Setenv("SFERENCE_API_KEY", "")
	writeAuthJSON(t, `{"access_token":"at-device-token-1234","refresh_token":"rt-x","expires_at":9999999999}`)
	out, code := captureStdout(t, func() int {
		return cmdWhoami(nil)
	})
	if code != 0 {
		t.Fatalf("cmdWhoami = %d, want 0; output:\n%s", code, out)
	}
	if !strings.Contains(out, "Signed in with device grant") {
		t.Fatalf("output should report device grant:\n%s", out)
	}
	if !strings.Contains(out, "Access token expires:") {
		t.Fatalf("output should show expiry:\n%s", out)
	}
}

// writeAuthJSONReturnPath is writeAuthJSON but also returns the path so
// the test can inspect what login wrote. It also points the gateway
// pidfile at a temp path so a login's SIGHUP can never reach a real
// router running on the dev machine.
func writeAuthJSONReturnPath(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SFERENCE_SWITCH_AUTH_FILE", path)
	t.Setenv("SFERENCE_SWITCH_GATEWAY_PIDFILE", filepath.Join(t.TempDir(), "gateway.pid"))
	return path
}
