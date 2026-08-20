package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/sference/sference-switch/gateway/internal/auth"
)

// auth_device_admin.go implements the admin endpoints behind the menubar
// app's in-UI sign-in: the app shows the user code and opens the browser;
// the gateway owns the whole OAuth round trip (device code request, paced
// polling, grant persistence) and live-reloads itself on approval — no
// Terminal, no SIGHUP.
//
//	POST /v1/admin/auth/device/start   -> {state, user_code, verification_uri, expires_at}
//	GET  /v1/admin/auth/device/status  -> {state, user_code?, verification_uri?, error?}
//	POST /v1/admin/auth/device/cancel  -> {state}
//
// States: idle (no login in flight), pending (waiting for approval),
// approved (grant saved + reloaded), failed (error field set). start is
// idempotent: while a login is pending it returns the SAME code so a
// reopened menu rejoins the flow instead of minting a second one.

// deviceLoginStates.
const (
	deviceLoginIdle     = "idle"
	deviceLoginPending  = "pending"
	deviceLoginApproved = "approved"
	deviceLoginFailed   = "failed"
)

type deviceLoginFlow struct {
	state           string
	userCode        string
	verificationURI string
	expiresAt       time.Time
	err             string
	cancel          context.CancelFunc
}

var (
	deviceLoginMu sync.Mutex
	deviceLogin   = &deviceLoginFlow{state: deviceLoginIdle}
)

// deviceLoginSnapshot is the JSON-renderable copy of the flow state.
func deviceLoginSnapshot() map[string]any {
	deviceLoginMu.Lock()
	defer deviceLoginMu.Unlock()
	m := map[string]any{"state": deviceLogin.state}
	if deviceLogin.userCode != "" {
		m["user_code"] = deviceLogin.userCode
	}
	if deviceLogin.verificationURI != "" {
		m["verification_uri"] = deviceLogin.verificationURI
	}
	if !deviceLogin.expiresAt.IsZero() {
		m["expires_at"] = deviceLogin.expiresAt.UTC().Format(time.RFC3339)
	}
	if deviceLogin.err != "" {
		m["error"] = deviceLogin.err
	}
	return m
}

func (g *Gateway) handleDeviceLoginStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		g.reject(w, 405, "method not allowed")
		return
	}
	deviceLoginMu.Lock()
	if deviceLogin.state == deviceLoginPending && time.Now().Before(deviceLogin.expiresAt) {
		// Rejoin the in-flight login.
		deviceLoginMu.Unlock()
		writeJSON(w, 200, deviceLoginSnapshot())
		return
	}
	if deviceLogin.cancel != nil {
		deviceLogin.cancel()
	}
	deviceLogin = &deviceLoginFlow{state: deviceLoginIdle}
	deviceLoginMu.Unlock()

	baseURL := auth.DefaultHostFunc()
	ctx, cancel := context.WithCancel(context.Background())
	label, _ := os.Hostname()
	dc, err := auth.StartDeviceLogin(ctx, baseURL, label)
	if err != nil {
		cancel()
		deviceLoginMu.Lock()
		deviceLogin = &deviceLoginFlow{state: deviceLoginFailed, err: err.Error()}
		deviceLoginMu.Unlock()
		writeJSON(w, 200, deviceLoginSnapshot())
		return
	}
	deviceLoginMu.Lock()
	deviceLogin = &deviceLoginFlow{
		state:           deviceLoginPending,
		userCode:        dc.UserCode,
		verificationURI: dc.VerificationURI,
		expiresAt:       time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second),
		cancel:          cancel,
	}
	deviceLoginMu.Unlock()

	go g.runDeviceLoginPoll(ctx, baseURL, dc)
	writeJSON(w, 200, deviceLoginSnapshot())
}

// runDeviceLoginPoll polls the token endpoint with RFC 8628 pacing until
// the user approves or the code expires. On approval the grant is
// persisted and the gateway live-reloads its credential.
func (g *Gateway) runDeviceLoginPoll(ctx context.Context, baseURL string, dc *auth.DeviceCodeResponse) {
	tokens, err := auth.PollForTokens(ctx, baseURL, dc.DeviceCode,
		time.Duration(dc.Interval)*time.Second, time.Duration(dc.ExpiresIn)*time.Second, nil)
	if err != nil {
		deviceLoginMu.Lock()
		// A cancel (new start, explicit cancel) is not a failure — the
		// replacing flow owns the state now.
		if deviceLogin.state == deviceLoginPending && !errors.Is(err, context.Canceled) {
			deviceLogin.state = deviceLoginFailed
			deviceLogin.err = err.Error()
		}
		deviceLoginMu.Unlock()
		return
	}
	tok := &auth.StoredToken{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		Expiry:       time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second),
		TokenType:    "Bearer",
		RemoteURL:    baseURL,
	}
	if err := auth.Save(nil, tok); err != nil {
		deviceLoginMu.Lock()
		deviceLogin.state = deviceLoginFailed
		deviceLogin.err = fmt.Sprintf("saving grant: %v", err)
		deviceLoginMu.Unlock()
		return
	}
	deviceLoginMu.Lock()
	deviceLogin.state = deviceLoginApproved
	deviceLoginMu.Unlock()
	fmt.Fprintf(os.Stderr, "[gateway] auth: device login approved, credential reloaded\n")
	g.refreshAuth()
}

func (g *Gateway) handleDeviceLoginStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		g.reject(w, 405, "method not allowed")
		return
	}
	writeJSON(w, 200, deviceLoginSnapshot())
}

func (g *Gateway) handleDeviceLoginCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		g.reject(w, 405, "method not allowed")
		return
	}
	deviceLoginMu.Lock()
	if deviceLogin.cancel != nil {
		deviceLogin.cancel()
	}
	deviceLogin = &deviceLoginFlow{state: deviceLoginIdle}
	deviceLoginMu.Unlock()
	writeJSON(w, 200, deviceLoginSnapshot())
}
