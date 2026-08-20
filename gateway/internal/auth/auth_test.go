package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// writeOwn writes content to the switch's own auth file (env override) and
// returns its path.
func writeOwn(t *testing.T, content string) string {
	t.Helper()
	// The dev shell may export a real SFERENCE_API_KEY; it outranks every
	// file, so blank it for hermetic resolution.
	t.Setenv("SFERENCE_API_KEY", "")
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SFERENCE_SWITCH_AUTH_FILE", path)
	return path
}

// writeShared writes content to the shared CLI credentials file by
// pointing HOME at a temp dir, and returns its path.
func writeShared(t *testing.T, content string) string {
	t.Helper()
	t.Setenv("SFERENCE_API_KEY", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".sference")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "credentials.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func v2JSON(expiresAt time.Time) string {
	return fmt.Sprintf(`{"access_token":"at-1","refresh_token":"rt-1","expires_at":%d}`, expiresAt.Unix())
}

// --- Load precedence -------------------------------------------------------

func TestLoadEnvWins(t *testing.T) {
	writeOwn(t, `{"token":"sk-file"}`)
	t.Setenv("SFERENCE_API_KEY", "sk-env")
	tok, _, err := Load("")
	if err != nil || tok == nil {
		t.Fatalf("Load = %v, %v", tok, err)
	}
	if tok.AccessToken != "sk-env" || tok.Kind != KindEnv {
		t.Fatalf("tok = %+v, want env key", tok)
	}
}

func TestLoadOwnDeviceGrant(t *testing.T) {
	exp := time.Now().Add(time.Hour)
	writeOwn(t, v2JSON(exp))
	tok, _, err := Load("")
	if err != nil || tok == nil {
		t.Fatalf("Load = %v, %v", tok, err)
	}
	if tok.Kind != KindDevice || tok.AccessToken != "at-1" || tok.RefreshToken != "rt-1" {
		t.Fatalf("tok = %+v, want device grant", tok)
	}
	if tok.Expiry.Unix() != exp.Unix() {
		t.Fatalf("expiry = %v, want %v", tok.Expiry, exp)
	}
}

func TestLoadOwnStaticKey(t *testing.T) {
	writeOwn(t, `{"token":"sk-own"}`)
	tok, _, _ := Load("")
	if tok == nil || tok.Kind != KindStatic || tok.AccessToken != "sk-own" {
		t.Fatalf("tok = %+v, want own static key", tok)
	}
}

func TestLoadSharedDeviceGrant(t *testing.T) {
	// No own file: point the override at a nonexistent path... but that
	// would skip the shared fallback entirely. Instead leave the override
	// unset and use a HOME with only the shared file.
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeShared(t, v2JSON(time.Now().Add(time.Hour)))
	tok, _, _ := Load("")
	if tok == nil || tok.Kind != KindSharedDevice {
		t.Fatalf("tok = %+v, want shared device grant", tok)
	}
	if tok.Kind.Refreshable() {
		t.Fatalf("shared grant must not be refreshable")
	}
}

func TestLoadSharedDeviceGrantExpiredIsSignedOut(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeShared(t, v2JSON(time.Now().Add(-time.Hour)))
	tok, _, _ := Load("")
	if tok != nil {
		t.Fatalf("tok = %+v, want nil (expired shared grant)", tok)
	}
}

func TestLoadSharedStaticKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeShared(t, `{"token":"sk-shared"}`)
	tok, _, _ := Load("")
	if tok == nil || tok.Kind != KindSharedStatic || tok.AccessToken != "sk-shared" {
		t.Fatalf("tok = %+v, want shared static key", tok)
	}
}

func TestLoadOwnFileBeatsShared(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeShared(t, `{"token":"sk-shared"}`)
	writeOwn(t, `{"token":"sk-own"}`)
	tok, _, _ := Load("")
	if tok == nil || tok.AccessToken != "sk-own" {
		t.Fatalf("tok = %+v, want own file to win", tok)
	}
}

func TestLoadAuthFileOverrideSkipsShared(t *testing.T) {
	// An explicit SFERENCE_SWITCH_AUTH_FILE means full isolation: the
	// shared file must not be consulted (preview runtime).
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeShared(t, `{"token":"sk-shared"}`)
	writeOwn(t, `{"token":""}`) // own file present but empty
	tok, _, _ := Load("")
	if tok != nil {
		t.Fatalf("tok = %+v, want nil (shared skipped under override)", tok)
	}
}

func TestLoadNothingIsNil(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeOwn(t, `{"token":""}`)
	tok, _, err := Load("")
	if err != nil || tok != nil {
		t.Fatalf("Load = %v, %v; want nil, nil", tok, err)
	}
}

// --- Save / Delete ---------------------------------------------------------

func TestSaveDeviceGrantShape(t *testing.T) {
	path := writeOwn(t, `{"token":""}`)
	exp := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	err := Save(nil, &StoredToken{
		AccessToken:  "at-new",
		RefreshToken: "rt-new",
		Expiry:       exp,
	})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var got credentialsFileV2
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("saved file not v2: %v\n%s", err, data)
	}
	if got.AccessToken != "at-new" || got.RefreshToken != "rt-new" || int64(got.ExpiresAt) != exp.Unix() {
		t.Fatalf("saved = %+v", got)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("perms = %o, want 600", info.Mode().Perm())
	}
}

func TestSaveStaticKeyShape(t *testing.T) {
	path := writeOwn(t, `{"token":""}`)
	if err := Save(nil, &StoredToken{AccessToken: "sk-1"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var got credentialsFileLegacy
	if err := json.Unmarshal(data, &got); err != nil || got.Token != "sk-1" {
		t.Fatalf("saved = %q (%v), want legacy shape", data, err)
	}
}

func TestDeleteRemovesOwnFileOnly(t *testing.T) {
	path := writeOwn(t, `{"token":"sk-1"}`)
	if err := Delete(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("own file still present: %v", err)
	}
	// Missing file is not an error.
	if err := Delete(nil); err != nil {
		t.Fatal(err)
	}
}

// --- transport -------------------------------------------------------------

// stubRoundTripper records the Authorization header it saw.
type stubRoundTripper struct {
	lastAuth string
	calls    int
}

func (s *stubRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	s.calls++
	s.lastAuth = req.Header.Get("Authorization")
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader("ok")),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

// refreshServer fakes the platform token endpoint. The handler sees every
// refresh attempt; rotate returns fresh at-N/rt-N pairs.
type refreshServer struct {
	t       *testing.T
	mu      sync.Mutex
	calls   int
	handler func(payload map[string]string) (int, any)
}

func newRefreshServer(t *testing.T) (*refreshServer, string) {
	rs := &refreshServer{t: t}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		rs.mu.Lock()
		rs.calls++
		h := rs.handler
		rs.mu.Unlock()
		var payload map[string]string
		_ = json.NewDecoder(r.Body).Decode(&payload)
		status, body := h(payload)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return rs, srv.URL
}

func (rs *refreshServer) callCount() int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.calls
}

func deviceToken(path, baseURL string, expiry time.Time) *StoredToken {
	return &StoredToken{
		AccessToken:  "at-old",
		RefreshToken: "rt-old",
		Expiry:       expiry,
		TokenType:    "Bearer",
		RemoteURL:    baseURL,
		Kind:         KindDevice,
		Path:         path,
	}
}

func TestTransportFreshTokenNoRefresh(t *testing.T) {
	rs, baseURL := newRefreshServer(t)
	rs.handler = func(map[string]string) (int, any) {
		t.Error("refresh must not be called for a fresh token")
		return 500, nil
	}
	stub := &stubRoundTripper{}
	tr := &tokenTransport{
		base:     stub,
		baseURL:  baseURL,
		tok:      deviceToken("", baseURL, time.Now().Add(time.Hour)),
		savePath: filepath.Join(t.TempDir(), "credentials.json"),
	}
	req, _ := http.NewRequest(http.MethodGet, "http://upstream", nil)
	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if stub.lastAuth != "Bearer at-old" {
		t.Fatalf("auth header = %q", stub.lastAuth)
	}
	if rs.callCount() != 0 {
		t.Fatalf("refresh calls = %d, want 0", rs.callCount())
	}
}

func TestTransportStaleGrantRefreshesAndPersists(t *testing.T) {
	rs, baseURL := newRefreshServer(t)
	rs.handler = func(payload map[string]string) (int, any) {
		if payload["grant_type"] != grantTypeRefreshToken {
			t.Errorf("grant_type = %q", payload["grant_type"])
		}
		if payload["client_id"] != ClientID {
			t.Errorf("client_id = %q", payload["client_id"])
		}
		if payload["refresh_token"] != "rt-old" {
			t.Errorf("refresh_token = %q", payload["refresh_token"])
		}
		return 200, map[string]any{
			"access_token": "at-new", "refresh_token": "rt-new",
			"token_type": "Bearer", "expires_in": 86400,
		}
	}
	savePath := filepath.Join(t.TempDir(), "credentials.json")
	stub := &stubRoundTripper{}
	var notified error
	tr := &tokenTransport{
		base:     stub,
		baseURL:  baseURL,
		tok:      deviceToken(savePath, baseURL, time.Now().Add(-time.Hour)), // stale
		savePath: savePath,
		notify:   func(_ string, err error) { notified = err },
	}
	req, _ := http.NewRequest(http.MethodGet, "http://upstream", nil)
	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if stub.lastAuth != "Bearer at-new" {
		t.Fatalf("auth header = %q, want rotated token", stub.lastAuth)
	}
	if notified != nil {
		t.Fatalf("notify err = %v", notified)
	}
	// The rotated pair must be on disk — a restart presenting rt-old would
	// trip reuse detection and revoke the grant.
	tok := readCredentialsFile(savePath)
	if tok == nil || tok.AccessToken != "at-new" || tok.RefreshToken != "rt-new" {
		t.Fatalf("persisted = %+v, want rotated pair", tok)
	}
}

func TestTransportTerminalRefreshFailureSticks(t *testing.T) {
	rs, baseURL := newRefreshServer(t)
	rs.handler = func(map[string]string) (int, any) {
		return 400, map[string]any{"error": "invalid_grant", "error_description": "This grant was revoked — sign in again"}
	}
	stub := &stubRoundTripper{}
	tr := &tokenTransport{
		base:     stub,
		baseURL:  baseURL,
		tok:      deviceToken("", baseURL, time.Now().Add(-time.Hour)),
		savePath: filepath.Join(t.TempDir(), "credentials.json"),
	}
	req, _ := http.NewRequest(http.MethodGet, "http://upstream", nil)
	_, err := tr.RoundTrip(req)
	if err == nil || !strings.Contains(err.Error(), "sference-switch auth login") {
		t.Fatalf("err = %v, want re-login message", err)
	}
	if !IsTerminalAuthError(tr.terminal) || RefreshErrorCode(tr.terminal) != "invalid_grant" {
		t.Fatalf("terminal error not classifiable: %v (code %q)", tr.terminal, RefreshErrorCode(tr.terminal))
	}
	// Second request fails fast without another token round trip.
	_, err = tr.RoundTrip(req)
	if err == nil {
		t.Fatal("second request must fail fast")
	}
	if rs.callCount() != 1 {
		t.Fatalf("refresh calls = %d, want 1 (terminal sticks)", rs.callCount())
	}
	if stub.calls != 0 {
		t.Fatalf("upstream must not see requests with a dead grant (got %d)", stub.calls)
	}
}

func TestTransportTransientFailureServesStaleAndThrottles(t *testing.T) {
	rs, baseURL := newRefreshServer(t)
	rs.handler = func(map[string]string) (int, any) {
		return 500, map[string]any{"error": "server_error"}
	}
	stub := &stubRoundTripper{}
	tr := &tokenTransport{
		base:     stub,
		baseURL:  baseURL,
		tok:      deviceToken("", baseURL, time.Now().Add(-time.Hour)),
		savePath: filepath.Join(t.TempDir(), "credentials.json"),
	}
	req, _ := http.NewRequest(http.MethodGet, "http://upstream", nil)
	for i := 0; i < 3; i++ {
		if _, err := tr.RoundTrip(req); err != nil {
			t.Fatal(err)
		}
	}
	if stub.lastAuth != "Bearer at-old" {
		t.Fatalf("auth header = %q, want stale token served", stub.lastAuth)
	}
	if rs.callCount() != 1 {
		t.Fatalf("refresh calls = %d, want 1 (throttled)", rs.callCount())
	}
}

func TestTransportRefreshIsSingleFlight(t *testing.T) {
	var refreshCalls atomic.Int32
	rs, baseURL := newRefreshServer(t)
	release := make(chan struct{})
	rs.handler = func(map[string]string) (int, any) {
		refreshCalls.Add(1)
		<-release // hold concurrent refreshes to expose races
		return 200, map[string]any{
			"access_token": "at-new", "refresh_token": "rt-new",
			"token_type": "Bearer", "expires_in": 86400,
		}
	}
	savePath := filepath.Join(t.TempDir(), "credentials.json")
	stub := &stubRoundTripper{}
	tr := &tokenTransport{
		base:     stub,
		baseURL:  baseURL,
		tok:      deviceToken(savePath, baseURL, time.Now().Add(-time.Hour)),
		savePath: savePath,
	}
	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodGet, "http://upstream", nil)
			_, _ = tr.RoundTrip(req)
		}()
	}
	// Let the first refresh start, then release it.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if got := refreshCalls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want exactly 1 (concurrent refreshes read as token reuse server-side)", got)
	}
}

func TestTransportSharedGrantRereadsFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sharedPath := writeShared(t, v2JSON(time.Now().Add(-time.Hour))) // stale on disk
	stub := &stubRoundTripper{}
	tr := &tokenTransport{
		base:       stub,
		tok:        &StoredToken{AccessToken: "at-stale", RefreshToken: "rt-x", Expiry: time.Now().Add(-time.Hour), Kind: KindSharedDevice, Path: sharedPath},
		sharedPath: sharedPath,
	}
	// The CLI refreshes its grant: the file gets a fresh pair.
	if err := os.WriteFile(sharedPath, []byte(
		fmt.Sprintf(`{"access_token":"at-cli-new","refresh_token":"rt-cli-new","expires_at":%d}`, time.Now().Add(time.Hour).Unix())), 0o600); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "http://upstream", nil)
	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if stub.lastAuth != "Bearer at-cli-new" {
		t.Fatalf("auth header = %q, want the CLI's refreshed token", stub.lastAuth)
	}
}

func TestTransportStripsIncomingAuthHeaders(t *testing.T) {
	stub := &stubRoundTripper{}
	tr := &tokenTransport{
		base: stub,
		tok:  &StoredToken{AccessToken: "sk-ours"},
	}
	req, _ := http.NewRequest(http.MethodGet, "http://upstream", nil)
	req.Header.Set("Authorization", "Bearer harness-key")
	req.Header.Set("X-Api-Key", "harness-key")
	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if stub.lastAuth != "Bearer sk-ours" {
		t.Fatalf("auth header = %q", stub.lastAuth)
	}
}

// --- HTTPClientWithNotify ---------------------------------------------------

func TestHTTPClientWithNotifySignedOut(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeOwn(t, `{"token":""}`)
	_, _, _, err := HTTPClientWithNotify(context.Background(), "", "", nil)
	if !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("err = %v, want ErrNotSignedIn", err)
	}
}

func TestHTTPClientWithNotifyStaticKey(t *testing.T) {
	writeOwn(t, `{"token":"sk-static"}`)
	client, tick, fp, err := HTTPClientWithNotify(context.Background(), "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if fp != CredFingerprint("sk-static") {
		t.Fatalf("fp = %q", fp)
	}
	if err := tick(); err != nil {
		t.Fatalf("tick = %v (static key: no-op)", err)
	}
	if client.Timeout != 0 {
		t.Fatalf("timeout = %v, want 0 (streaming)", client.Timeout)
	}
}
