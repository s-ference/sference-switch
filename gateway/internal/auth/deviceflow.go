// deviceflow.go is the RFC 8628 device-flow client for
// `sference-switch auth login` and for gateway token refresh. It mirrors
// the sference CLI's device_auth.py (same wire shapes, same pacing rules)
// against the platform endpoints:
//
//	POST /v1/oauth/device_code -> {device_code, user_code, verification_uri, expires_in, interval}
//	POST /v1/oauth/token       -> {access_token, token_type, expires_in, refresh_token}
//	                              or 400 {error, error_description}   (RFC 6749 shape)
//	POST /v1/oauth/revoke      -> 200 (always, even for unknown tokens)
package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ClientID is registered on the platform (KNOWN_CLIENT_IDS in
// device_flow.py); unknown client_ids get invalid_client. The switch has
// its own client_id so its refresh-grant chain is independent of the
// sference CLI's — two clients refreshing one shared grant would trip
// reuse detection and revoke the whole chain.
const ClientID = "sference-switch"

const (
	grantTypeDeviceCode   = "urn:ietf:params:oauth:grant-type:device_code"
	grantTypeRefreshToken = "refresh_token"
)

// ExpirySkew is how far ahead of expiry a token is refreshed, so a token
// never dies mid-request. Matches the CLI's EXPIRY_SKEW_SECONDS.
const ExpirySkew = 60 * time.Second

// DeviceAuthError is a failed device-flow call. Code is the RFC 6749 error
// when the server sent one (invalid_grant, expired_token, …), else a local
// label (network_error, http_<status>).
type DeviceAuthError struct {
	Code        string
	Description string
}

func (e *DeviceAuthError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Description)
}

// IsTerminal reports whether the grant is unrecoverable — revoked, expired,
// or reuse-detected — and the user must sign in again. Transient failures
// (network, 5xx) are worth retrying; terminal ones are not.
func (e *DeviceAuthError) IsTerminal() bool {
	switch e.Code {
	case "invalid_grant", "expired_token", "invalid_client", "access_denied":
		return true
	}
	return false
}

// DeviceCodeResponse is the parsed POST /v1/oauth/device_code payload.
type DeviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// TokenResponse is the parsed POST /v1/oauth/token payload.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
}

// oauthHTTPClient is used for device-flow round trips only (short timeouts,
// no auth headers — these endpoints are pre-auth).
var oauthHTTPClient = &http.Client{Timeout: 15 * time.Second}

// postOAuth POSTs a JSON body and returns the parsed body for ANY status:
// the device-flow endpoints signal pending/slow_down/expired via 400
// bodies, so HTTP errors are data, not failures. Network failures return a
// DeviceAuthError — the caller cannot distinguish statuses it never
// received.
func postOAuth(ctx context.Context, url string, payload map[string]string) (int, map[string]any, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return 0, nil, &DeviceAuthError{Code: "network_error", Description: err.Error()}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	parsed := map[string]any{}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &parsed); err != nil {
			return resp.StatusCode, map[string]any{}, nil
		}
	}
	return resp.StatusCode, parsed, nil
}

func oauthError(status int, body map[string]any, fallback string) *DeviceAuthError {
	code, _ := body["error"].(string)
	if code == "" {
		code = fmt.Sprintf("http_%d", status)
	}
	desc, _ := body["error_description"].(string)
	if desc == "" {
		desc = fmt.Sprintf("%s (%d)", fallback, status)
	}
	return &DeviceAuthError{Code: code, Description: desc}
}

// StartDeviceLogin POSTs /v1/oauth/device_code and returns the parsed
// response. deviceLabel is shown on the console's devices page.
func StartDeviceLogin(ctx context.Context, baseURL, deviceLabel string) (*DeviceCodeResponse, error) {
	payload := map[string]string{"client_id": ClientID}
	if deviceLabel != "" {
		payload["device_label"] = deviceLabel
	}
	status, body, err := postOAuth(ctx, baseURL+"/v1/oauth/device_code", payload)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, oauthError(status, body, "device_code request failed")
	}
	raw, _ := json.Marshal(body)
	var out DeviceCodeResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("device_code response malformed: %w", err)
	}
	if out.DeviceCode == "" || out.UserCode == "" || out.Interval <= 0 || out.ExpiresIn <= 0 {
		return nil, fmt.Errorf("device_code response malformed: missing fields")
	}
	return &out, nil
}

// PollForTokens polls /v1/oauth/token until the user approves; returns the
// token response. Honors the RFC 8628 pacing contract:
// authorization_pending waits `interval`; slow_down adds 5s to it (the
// server also enforces pacing — a client that ignores slow_down gets 400s).
// sleep is injectable for tests.
func PollForTokens(ctx context.Context, baseURL, deviceCode string, interval, expiresIn time.Duration, sleep func(time.Duration)) (*TokenResponse, error) {
	if sleep == nil {
		sleep = time.Sleep
	}
	url := baseURL + "/v1/oauth/token"
	deadline := time.Now().Add(expiresIn)
	wait := interval
	for {
		status, body, err := postOAuth(ctx, url, map[string]string{
			"grant_type":  grantTypeDeviceCode,
			"client_id":   ClientID,
			"device_code": deviceCode,
		})
		if err != nil {
			return nil, err
		}
		if status == http.StatusOK {
			raw, _ := json.Marshal(body)
			var out TokenResponse
			if err := json.Unmarshal(raw, &out); err != nil {
				return nil, fmt.Errorf("token response malformed: %w", err)
			}
			if out.AccessToken == "" || out.RefreshToken == "" {
				return nil, fmt.Errorf("token response malformed: missing fields")
			}
			return &out, nil
		}
		oerr := oauthError(status, body, "token poll failed")
		if oerr.Code == "authorization_pending" || oerr.Code == "slow_down" {
			if oerr.Code == "slow_down" {
				wait += 5 * time.Second
			}
			if time.Now().Add(wait).After(deadline) {
				return nil, &DeviceAuthError{Code: "expired_token", Description: "device code expired before approval"}
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
			sleep(wait)
			continue
		}
		return nil, oerr
	}
}

// RefreshTokens POSTs /v1/oauth/token with grant_type=refresh_token.
//
// The server rotates on every refresh: the returned refresh_token REPLACES
// the presented one, and presenting a rotated-out token revokes the whole
// grant (reuse detection). Callers must persist the new pair atomically and
// must single-flight refreshes — two concurrent refreshes of one grant look
// like reuse.
func RefreshTokens(ctx context.Context, baseURL, refreshToken string) (*TokenResponse, error) {
	status, body, err := postOAuth(ctx, baseURL+"/v1/oauth/token", map[string]string{
		"grant_type":    grantTypeRefreshToken,
		"client_id":     ClientID,
		"refresh_token": refreshToken,
	})
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, oauthError(status, body, "refresh failed")
	}
	raw, _ := json.Marshal(body)
	var out TokenResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("token response malformed: %w", err)
	}
	if out.AccessToken == "" || out.RefreshToken == "" {
		return nil, fmt.Errorf("token response malformed: missing fields")
	}
	return &out, nil
}

// RevokeToken best-effort revokes a refresh token (RFC 7009). The endpoint
// is 200 even for unknown tokens, so any error here is a network/local
// failure; callers treat revocation as best-effort.
func RevokeToken(ctx context.Context, baseURL, refreshToken string) error {
	_, _, err := postOAuth(ctx, baseURL+"/v1/oauth/revoke", map[string]string{
		"client_id": ClientID,
		"token":     refreshToken,
	})
	return err
}
