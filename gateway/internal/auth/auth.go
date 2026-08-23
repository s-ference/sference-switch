// Package auth resolves the Sference credential and builds an http.Client
// that injects it as "Authorization: Bearer <token>" on every upstream
// request.
//
// Credential sources, in precedence order:
//
//  1. SFERENCE_API_KEY env — static API key.
//  2. The switch's own auth file (SFERENCE_SWITCH_AUTH_FILE override, else
//     ~/.sference/switch/credentials.json) — either a device-flow grant
//     (v2: {access_token, refresh_token, expires_at}) written by
//     `sference-switch auth login`, or a static key ({"token": "sk_..."})
//     written by `sference-switch auth login --api-key`.
//  3. The shared file ~/.sference/credentials.json (skipped when
//     SFERENCE_SWITCH_AUTH_FILE is set, so preview/test runtimes stay
//     isolated) — READ-ONLY. A v2 grant here belongs to the sference CLI:
//     the switch serves its current access token but never refreshes it
//     (two clients refreshing one grant trips reuse detection and revokes
//     the chain). A legacy {"token"} key is served as-is.
//
// Refresh: only the switch's own v2 grant is refreshed, lazily inside the
// transport, single-flight under a mutex (concurrent refreshes of one
// grant look like token reuse to the server). Rotated pairs are persisted
// atomically before use. A terminal refresh failure (invalid_grant — the
// grant was revoked, expired, or reuse-detected) sticks: requests fail
// fast with a re-login message and the gateway health flips to
// "refresh_failed" via the notify callback.
//
// A SIGHUP re-reads the files so `sference-switch auth login` (or a CLI
// login to the shared file) is picked up without a restart.
package auth

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultHost is the Sference API origin used for identity calls (whoami),
// device-flow round trips, and as the upstream for routed inference
// traffic.
const DefaultHost = "https://api.sference.com"

// ErrNotSignedIn is returned when no credential is found in the
// environment or any credentials file.
var ErrNotSignedIn = errors.New("sference-switch: not signed in. Run 'sference-switch auth login' (device flow) or 'sference-switch auth login --api-key sk_...'")

// Kind identifies where a credential came from and how it may be used.
type Kind string

const (
	// KindEnv is a static key from SFERENCE_API_KEY.
	KindEnv Kind = "env"
	// KindDevice is the switch's own device-flow grant (refreshable).
	KindDevice Kind = "device"
	// KindStatic is a static API key from the switch's own auth file.
	KindStatic Kind = "static"
	// KindSharedDevice is the sference CLI's device grant in the shared
	// file — served read-only, refreshed only by picking up the CLI's
	// rewrites, never via the token endpoint.
	KindSharedDevice Kind = "shared-device"
	// KindSharedStatic is a static API key from the shared file.
	KindSharedStatic Kind = "shared-static"
)

// Refreshable reports whether this credential kind is refreshed via the
// OAuth token endpoint (only the switch's own grant chain).
func (k Kind) Refreshable() bool { return k == KindDevice }

// ownCredentialsPath returns the switch's own auth file path, honoring the
// SFERENCE_SWITCH_AUTH_FILE override (preview runtime, tests).
func ownCredentialsPath() string {
	if v := os.Getenv("SFERENCE_SWITCH_AUTH_FILE"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".sference", "switch", "credentials.json")
	}
	return filepath.Join(home, ".sference", "switch", "credentials.json")
}

// OwnCredentialsPathForDisplay returns the switch's own auth file path
// for user-facing messages (login/logout output).
func OwnCredentialsPathForDisplay() string { return ownCredentialsPath() }

// sharedCredentialsPath returns the file the sference CLI writes. The
// switch reads it as a fallback but never writes it.
func sharedCredentialsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".sference", "credentials.json")
	}
	return filepath.Join(home, ".sference", "credentials.json")
}

// StoredToken is the in-memory representation of a loaded credential.
// For device grants, RefreshToken and Expiry are populated (Expiry is the
// absolute access-token expiry); for static keys they are zero.
type StoredToken struct {
	AccessToken  string
	RefreshToken string
	Expiry       time.Time
	TokenType    string
	RemoteURL    string
	// Kind records the credential source; Path the file it was loaded
	// from ("(env)" for environment credentials). Both are informational.
	Kind Kind
	Path string
}

// expired reports whether the access token is within the refresh skew of
// its recorded expiry. Static keys (zero Expiry) never expire.
func (t *StoredToken) expired() bool {
	if t.Expiry.IsZero() {
		return false
	}
	return time.Now().After(t.Expiry.Add(-ExpirySkew))
}

// SaveLocator identifies where a credential was loaded from so it can be
// persisted back. For the file-based store this is just the path.
type SaveLocator struct {
	Path string
}

// credentialsFileV2 is the on-disk shape of a device-flow grant. expires_at
// is an absolute unix timestamp so readers never have to know when the
// file was written.
type credentialsFileV2 struct {
	AccessToken  string  `json:"access_token"`
	RefreshToken string  `json:"refresh_token"`
	ExpiresAt    float64 `json:"expires_at"`
}

// credentialsFileLegacy is the on-disk shape of a static API key (the
// format the sference CLI introduced and the switch has always read).
type credentialsFileLegacy struct {
	Token string `json:"token"`
}

// readCredentialsFile parses path as either credential shape. Returns nil
// when the file is missing, unreadable, or malformed — a credential that
// cannot be parsed is treated as absent, matching the historical behavior.
func readCredentialsFile(path string) *StoredToken {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var v2 credentialsFileV2
	if err := json.Unmarshal(data, &v2); err == nil &&
		strings.TrimSpace(v2.AccessToken) != "" && strings.TrimSpace(v2.RefreshToken) != "" && v2.ExpiresAt > 0 {
		sec := int64(v2.ExpiresAt)
		return &StoredToken{
			AccessToken:  strings.TrimSpace(v2.AccessToken),
			RefreshToken: strings.TrimSpace(v2.RefreshToken),
			Expiry:       time.Unix(sec, 0),
			TokenType:    "Bearer",
		}
	}
	var legacy credentialsFileLegacy
	if err := json.Unmarshal(data, &legacy); err != nil {
		return nil
	}
	key := strings.TrimSpace(legacy.Token)
	if key == "" {
		return nil
	}
	return &StoredToken{AccessToken: key}
}

// Load resolves the credential per the package-doc precedence. Returns
// (nil, nil, nil) when nothing is found. Load performs no network I/O: an
// expired own-grant is returned as-is and refreshed lazily by the
// transport; an expired shared grant is skipped (the switch cannot
// refresh it, and an expired JWT is certainly rejected upstream). The
// profile parameter is accepted for caller compatibility but ignored —
// there is one credential chain.
func Load(_ string) (*StoredToken, *SaveLocator, error) {
	if key := strings.TrimSpace(os.Getenv("SFERENCE_API_KEY")); key != "" {
		return &StoredToken{AccessToken: key, Kind: KindEnv, Path: "(env)"}, &SaveLocator{Path: "(env)"}, nil
	}
	ownPath := ownCredentialsPath()
	if tok := readCredentialsFile(ownPath); tok != nil {
		if tok.RefreshToken != "" {
			tok.Kind = KindDevice
			tok.RemoteURL = DefaultHostFunc()
		} else {
			tok.Kind = KindStatic
		}
		tok.Path = ownPath
		return tok, &SaveLocator{Path: ownPath}, nil
	}
	// The shared file is consulted only when the own-file override is
	// unset — an explicit SFERENCE_SWITCH_AUTH_FILE means full isolation
	// (preview runtime, tests).
	if os.Getenv("SFERENCE_SWITCH_AUTH_FILE") == "" {
		sharedPath := sharedCredentialsPath()
		if tok := readCredentialsFile(sharedPath); tok != nil {
			if sharedSuppressedBySignOut(sharedPath) {
				// Explicit sign-out: stay signed out until the CLI
				// rewrites its grant (login or refresh).
				return nil, nil, nil
			}
			if tok.RefreshToken != "" {
				if tok.expired() {
					// The CLI owns this grant chain; an expired access
					// token here means the CLI has not run recently.
					// Treat as signed out rather than serving a token
					// that is certainly rejected.
					return nil, nil, nil
				}
				tok.Kind = KindSharedDevice
			} else {
				tok.Kind = KindSharedStatic
			}
			tok.Path = sharedPath
			return tok, &SaveLocator{Path: sharedPath}, nil
		}
	}
	return nil, nil, nil
}

// Save persists the credential to the switch's own auth file (0600 inside
// a 0700 directory). A token with a RefreshToken is written in the v2
// device-grant shape; otherwise the legacy {"token": ...} shape. The
// shared CLI file is never written. A successful save lifts any explicit
// sign-out suppression (see signOutMarkerPath).
func Save(_ *SaveLocator, tok *StoredToken) error {
	if tok == nil {
		return errors.New("sference-switch: cannot save nil token")
	}
	path := ownCredentialsPath()
	if err := writeCredentialsFile(path, tok); err != nil {
		return err
	}
	// A login is the explicit undo of a sign-out.
	if err := os.Remove(signOutMarkerPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// signOutMarkerPath is a sentinel next to the switch's own auth file
// recording an explicit sign-out. While it exists and is not older than
// the shared CLI credentials file, Load skips the shared fallback —
// without it, deleting the own file would immediately re-sign the switch
// in from the CLI's grant, making "sign out" look like a no-op. The
// suppression lifts when the shared file is rewritten after the sign-out
// (a CLI login or refresh is a newer session the switch may ride again)
// and is removed outright by Save.
func signOutMarkerPath() string {
	return filepath.Join(filepath.Dir(ownCredentialsPath()), "signed-out")
}

// sharedSuppressedBySignOut reports whether an explicit sign-out
// suppresses the shared credential at sharedPath right now.
func sharedSuppressedBySignOut(sharedPath string) bool {
	marker, err := os.Stat(signOutMarkerPath())
	if err != nil {
		return false
	}
	shared, err := os.Stat(sharedPath)
	if err != nil {
		return false
	}
	return !shared.ModTime().After(marker.ModTime())
}

// writeCredentialsFile atomically writes tok to path: temp file in the
// same directory + rename, so a crash mid-write never leaves a truncated
// credential (a truncated v2 file would present a rotated-out refresh
// token on next start, which the server reads as token reuse and revokes
// the whole grant).
func writeCredentialsFile(path string, tok *StoredToken) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var data []byte
	var err error
	if tok.RefreshToken != "" {
		expiry := tok.Expiry
		if expiry.IsZero() {
			expiry = time.Now().Add(24 * time.Hour)
		}
		data, err = json.Marshal(credentialsFileV2{
			AccessToken:  tok.AccessToken,
			RefreshToken: tok.RefreshToken,
			ExpiresAt:    float64(expiry.Unix()),
		})
	} else {
		data, err = json.Marshal(credentialsFileLegacy{Token: tok.AccessToken})
	}
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".credentials-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// Delete removes the switch's own auth file and records the explicit
// sign-out (signOutMarkerPath) so the shared CLI fallback does not
// immediately re-sign the switch in. Best-effort on the own file: a
// missing file is not an error. The shared CLI file is never removed.
func Delete(_ *SaveLocator) error {
	path := ownCredentialsPath()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	marker := signOutMarkerPath()
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		return err
	}
	// Write (not O_CREATE) so a repeat sign-out refreshes the mtime —
	// the marker must be newer than the shared file to suppress it.
	return os.WriteFile(marker, []byte("signed out\n"), 0o600)
}

// CredFingerprint returns a non-reversible identity for the credential,
// used only to detect that it changed across a reload. Never logged
// alongside the token itself.
func CredFingerprint(key string) string {
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}

// RefreshErrorCode classifies a token-refresh error: the RFC 6749 code for
// server rejections ("invalid_grant", …), a local label for transport
// failures ("network_error"), or "" for other errors.
func RefreshErrorCode(err error) string {
	var derr *DeviceAuthError
	if errors.As(err, &derr) {
		return derr.Code
	}
	return ""
}

// IsTerminalAuthError reports whether err means the grant is
// unrecoverable (revoked, expired, reuse-detected) and the user must sign
// in again.
func IsTerminalAuthError(err error) bool {
	var derr *DeviceAuthError
	return errors.As(err, &derr) && derr.IsTerminal()
}

// transientRetryInterval throttles refresh retries after a transient
// failure so a down network does not turn every request into a token
// round trip.
const transientRetryInterval = 30 * time.Second

// tokenTransport wraps an http.RoundTripper and injects the credential as
// a Bearer token on every request, stripping any incoming Authorization or
// X-Api-Key header so the harness credential never leaks upstream.
//
// For the switch's own device grant it refreshes lazily: when the access
// token is within ExpirySkew of expiry, a single-flight refresh (mutex —
// concurrent refreshes of one grant read as token reuse server-side)
// rotates the pair, persists it atomically, and the request proceeds with
// the fresh token. Transient refresh failures serve the stale token (the
// upstream 401 is the honest signal) and throttle retries; terminal
// failures (grant revoked) stick and fail requests fast with a re-login
// message.
//
// For the CLI's shared grant it never calls the token endpoint; a stale
// token triggers a re-read of the shared file instead, picking up the
// CLI's latest rewrite.
type tokenTransport struct {
	base    http.RoundTripper
	notify  func(fp string, err error)
	baseURL string // token-endpoint host for refreshable grants

	mu         sync.Mutex
	tok        *StoredToken
	savePath   string // own-grant persist path ("" = read-only credential)
	sharedPath string // shared-grant re-read path ("" = not a shared grant)
	terminal   error
	lastFailAt time.Time
}

func (t *tokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := t.currentToken(req.Context())
	if err != nil {
		return nil, err
	}
	req.Header.Del("Authorization")
	req.Header.Del("X-Api-Key")
	req.Header.Set("Authorization", "Bearer "+token)
	return t.base.RoundTrip(req)
}

// currentToken returns the access token to use, refreshing or re-reading
// when stale. The mutex makes refresh single-flight; the double-check
// after locking avoids a second refresh right behind the first.
func (t *tokenTransport) currentToken(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.terminal != nil {
		return "", t.terminal
	}
	if !t.tok.expired() {
		return t.tok.AccessToken, nil
	}
	// Shared grant: re-read the file for the CLI's latest rewrite.
	if t.sharedPath != "" {
		if fresh := readCredentialsFile(t.sharedPath); fresh != nil && !fresh.expired() {
			fresh.Kind = KindSharedDevice
			fresh.Path = t.sharedPath
			t.tok = fresh
		}
		return t.tok.AccessToken, nil
	}
	// Static key or nothing to refresh with.
	if t.tok.RefreshToken == "" || t.savePath == "" {
		return t.tok.AccessToken, nil
	}
	// Throttle retries after a transient failure.
	if !t.lastFailAt.IsZero() && time.Since(t.lastFailAt) < transientRetryInterval {
		return t.tok.AccessToken, nil
	}
	resp, err := RefreshTokens(ctx, t.baseURL, t.tok.RefreshToken)
	if err != nil {
		if IsTerminalAuthError(err) {
			// %w keeps the DeviceAuthError classifiable for the notify
			// consumer (gateway health) while the message stays
			// user-actionable.
			t.terminal = fmt.Errorf("sference-switch: stored login was rejected — run 'sference-switch auth login' to sign in again: %w", err)
			t.notifyLocked(t.terminal)
			return "", t.terminal
		}
		t.lastFailAt = time.Now()
		t.notifyLocked(err)
		return t.tok.AccessToken, nil
	}
	t.tok = &StoredToken{
		AccessToken:  resp.AccessToken,
		RefreshToken: resp.RefreshToken,
		Expiry:       time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second),
		TokenType:    "Bearer",
		RemoteURL:    t.baseURL,
		Kind:         KindDevice,
		Path:         t.savePath,
	}
	// Persist the rotated pair. The server has already rotated, so the old
	// refresh token is dead either way; a failed write is reported but the
	// in-memory pair stays in use (the next refresh retries the write).
	if err := writeCredentialsFile(t.savePath, t.tok); err != nil {
		t.notifyLocked(fmt.Errorf("sference-switch: refreshed token could not be persisted: %w", err))
		return t.tok.AccessToken, nil
	}
	t.lastFailAt = time.Time{}
	t.notifyLocked(nil)
	return t.tok.AccessToken, nil
}

func (t *tokenTransport) notifyLocked(err error) {
	if t.notify != nil {
		t.notify(CredFingerprint(t.tok.AccessToken), err)
	}
}

// buildClient returns an http.Client that injects the credential as Bearer
// on every request. Timeout is 0 (unbounded) so streaming responses are
// never truncated. On macOS, Go's bundled CA roots may not include the
// system Keychain CAs, so we explicitly load the system cert pool. When
// SFERENCE_SWITCH_INSECURE_TLS is set (dev only), TLS verification is
// skipped entirely — use when the upstream CA is not in the local trust
// store.
func buildClient(tok *StoredToken, notify func(fp string, err error)) *http.Client {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.MaxIdleConns = 100
	base.MaxIdleConnsPerHost = 16
	if os.Getenv("SFERENCE_SWITCH_INSECURE_TLS") != "" {
		base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	} else if pool, err := x509.SystemCertPool(); err == nil && pool != nil {
		base.TLSClientConfig = &tls.Config{RootCAs: pool}
	}
	transport := &tokenTransport{
		base:    base,
		notify:  notify,
		baseURL: tok.RemoteURL,
		tok:     tok,
	}
	switch tok.Kind {
	case KindDevice:
		transport.savePath = tok.Path
		if transport.baseURL == "" {
			transport.baseURL = DefaultHostFunc()
		}
	case KindSharedDevice:
		transport.sharedPath = tok.Path
	}
	return &http.Client{
		Transport: transport,
		Timeout:   0,
	}
}

// HTTPClient builds a client for the stored credential. Returns
// ErrNotSignedIn when no credential is found.
func HTTPClient(ctx context.Context, _ string, _ string) (*http.Client, error) {
	client, _, _, err := HTTPClientWithNotify(ctx, "", "", nil)
	return client, err
}

// HTTPClientForceRefresh is HTTPClient with the cached token treated as
// expired. Refresh happens lazily inside the transport, so this is
// identical to HTTPClient; it exists for caller compatibility.
func HTTPClientForceRefresh(ctx context.Context, _ string, _ string) (*http.Client, error) {
	return HTTPClient(ctx, "", "")
}

// HTTPClientWithNotify builds a client whose transport refreshes the
// switch's own device grant lazily and reports every refresh outcome
// through notify (nil error = success). tick proactively refreshes when
// the token is within the expiry skew; callers may invoke it periodically
// or ignore it (the transport covers staleness on demand). credFP is the
// CredFingerprint of the loaded credential so the gateway can detect a
// changed credential across reloads.
func HTTPClientWithNotify(ctx context.Context, _ string, _ string, notify func(fp string, err error)) (client *http.Client, tick func() error, credFP string, err error) {
	tok, _, loadErr := Load("")
	if loadErr != nil {
		return nil, nil, "", loadErr
	}
	if tok == nil {
		return nil, nil, "", ErrNotSignedIn
	}
	c := buildClient(tok, notify)
	tick = func() error {
		transport, ok := c.Transport.(*tokenTransport)
		if !ok {
			return nil
		}
		_, err := transport.currentToken(ctx)
		return err
	}
	return c, tick, CredFingerprint(tok.AccessToken), nil
}

// DefaultHostFunc returns the API host, honoring SFERENCE_REMOTE_URL for
// test overrides. Kept as a function (not the const) for callers that
// need the env override.
func DefaultHostFunc() string {
	if v := os.Getenv("SFERENCE_REMOTE_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return DefaultHost
}
