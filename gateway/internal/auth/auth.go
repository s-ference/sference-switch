// Package auth reads the Sference API key from the shared credentials file
// (~/.sference/credentials.json, the same file the sference CLI writes) or
// the SFERENCE_API_KEY environment variable, and builds an http.Client that
// injects it as "Authorization: Bearer <key>" on every upstream request.
//
// There is no OAuth device flow, no refresh token, and no keyring: the
// credential is a static API key. Credential health is therefore binary —
// present ("ok") or absent ("signed_out") — with no "refresh_failed" state.
// A SIGHUP re-reads the file so `sference auth login` + reload picks up a
// new key immediately.
package auth

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultHost is the Sference API origin used for identity calls (whoami)
// and as the upstream for routed inference traffic.
const DefaultHost = "https://api.sference.com"

// ErrNotSignedIn is returned when no API key is found in the environment
// or the credentials file.
var ErrNotSignedIn = errors.New("sference-switch: not signed in. Run 'sference auth login --api-key sk_...'")

// credentialsPath returns the shared credentials file path, honoring the
// SFERENCE_SWITCH_AUTH_FILE test override.
func credentialsPath() string {
	if v := os.Getenv("SFERENCE_SWITCH_AUTH_FILE"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".sference", "credentials.json")
	}
	return filepath.Join(home, ".sference", "credentials.json")
}

// StoredToken is the in-memory representation of a loaded API key.
// AccessToken holds the key value; the other fields are always zero and
// exist only for struct-level compatibility with callers that read them.
type StoredToken struct {
	AccessToken  string
	RefreshToken string    // always ""
	Expiry       time.Time // always zero
	TokenType    string    // always ""
	RemoteURL    string    // always ""
}

// SaveLocator identifies where a credential was loaded from so it can be
// persisted back. For the file-based store this is just the path.
type SaveLocator struct {
	Path string
}

// Load resolves the API key from $SFERENCE_API_KEY (env) or
// ~/.sference/credentials.json (file, JSON: {"token": "sk_..."}).
// Returns (nil, nil, nil) when nothing is found. The profile parameter is
// accepted for caller compatibility but ignored — there is one credential.
func Load(_ string) (*StoredToken, *SaveLocator, error) {
	if key := os.Getenv("SFERENCE_API_KEY"); key != "" {
		key = strings.TrimSpace(key)
		if key != "" {
			return &StoredToken{AccessToken: key}, &SaveLocator{Path: "(env)"}, nil
		}
	}
	path := credentialsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, nil
	}
	var payload struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(data, &payload) != nil {
		return nil, nil, nil
	}
	key := strings.TrimSpace(payload.Token)
	if key == "" {
		return nil, nil, nil
	}
	return &StoredToken{AccessToken: key}, &SaveLocator{Path: path}, nil
}

// Save writes the API key to ~/.sference/credentials.json with 0600 perms
// inside a 0700 directory, matching the sference CLI's format exactly.
func Save(_ *SaveLocator, tok *StoredToken) error {
	if tok == nil {
		return errors.New("sference-switch: cannot save nil token")
	}
	path := credentialsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(map[string]string{"token": tok.AccessToken})
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return nil
}

// Delete removes the credentials file. Best-effort: a missing file is not
// an error.
func Delete(_ *SaveLocator) error {
	path := credentialsPath()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// CredFingerprint returns a non-reversible identity for the key, used only
// to detect that the credential changed across a reload. Kept for caller
// compatibility; never logged alongside the key itself.
func CredFingerprint(key string) string {
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}

// RefreshErrorCode classifies a token-refresh error. With a static API key
// there are no refresh round trips, so this always returns "".
func RefreshErrorCode(err error) string { return "" }

// bearerTransport wraps an http.Transport and injects the API key as a
// Bearer token on every request, stripping any incoming Authorization or
// X-Api-Key header so the harness credential never leaks upstream.
type bearerTransport struct {
	base http.RoundTripper
	key  string
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Del("Authorization")
	req.Header.Del("X-Api-Key")
	req.Header.Set("Authorization", "Bearer "+t.key)
	return t.base.RoundTrip(req)
}

// buildClient returns an http.Client that injects the API key as Bearer on
// every request. Timeout is 0 (unbounded) so streaming responses are never
// truncated. On macOS, Go's bundled CA roots may not include the system
// Keychain CAs, so we explicitly load the system cert pool. When
// SFERENCE_SWITCH_INSECURE_TLS is set (dev only), TLS verification is
// skipped entirely — use when the upstream CA is not in the local trust
// store.
func buildClient(key string) *http.Client {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.MaxIdleConns = 100
	base.MaxIdleConnsPerHost = 16
	if os.Getenv("SFERENCE_SWITCH_INSECURE_TLS") != "" {
		base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	} else if pool, err := x509.SystemCertPool(); err == nil && pool != nil {
		base.TLSClientConfig = &tls.Config{RootCAs: pool}
	}
	base.MaxIdleConnsPerHost = 16
	return &http.Client{
		Transport: &bearerTransport{base: base, key: key},
		Timeout:   0,
	}
}

// HTTPClient builds an oauth2-style client for the stored API key.
// Returns ErrNotSignedIn when no credential is found.
func HTTPClient(ctx context.Context, _ string, _ string) (*http.Client, error) {
	tok, _, err := Load("")
	if err != nil {
		return nil, err
	}
	if tok == nil {
		return nil, ErrNotSignedIn
	}
	return buildClient(tok.AccessToken), nil
}

// HTTPClientForceRefresh is HTTPClient with the cached token treated as
// expired. With a static key there is nothing to refresh, so this is
// identical to HTTPClient; it exists for caller compatibility.
func HTTPClientForceRefresh(ctx context.Context, _ string, _ string) (*http.Client, error) {
	return HTTPClient(ctx, "", "")
}

// HTTPClientWithNotify is HTTPClient with a refresh-outcome callback.
// With a static key there are no refresh round trips, so notify is never
// called and tick is a no-op. credFP is the CredFingerprint of the loaded
// key so the gateway can detect a changed key across reloads.
func HTTPClientWithNotify(_ context.Context, _ string, _ string, _ func(fp string, err error)) (client *http.Client, tick func() error, credFP string, err error) {
	tok, _, loadErr := Load("")
	if loadErr != nil {
		return nil, nil, "", loadErr
	}
	if tok == nil {
		return nil, nil, "", ErrNotSignedIn
	}
	return buildClient(tok.AccessToken), func() error { return nil }, CredFingerprint(tok.AccessToken), nil
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
