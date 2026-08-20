// auth_login.go implements `sference-switch auth login` and
// `sference-switch auth logout`.
//
// Login has two paths:
//   - default: OAuth device flow (RFC 8628) — prints a code, opens the
//     verification page, polls until approved, then writes the v2 grant
//     ({access_token, refresh_token, expires_at}) to the switch's own auth
//     file. The gateway refreshes it from there.
//   - --api-key sk_...: static key, written to the same file in the legacy
//     {"token": ...} shape. Never refreshed.
//
// Both paths write the switch's OWN file (SFERENCE_SWITCH_AUTH_FILE
// override, else ~/.sference/switch/credentials.json) — never the shared
// ~/.sference/credentials.json the sference CLI owns. After a successful
// login the running gateway is SIGHUP'd so it picks up the credential
// immediately.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/sference/sference-switch/gateway/internal/auth"
)

func cmdAuth(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: sference-switch auth login [--api-key sk_...]  |  auth logout")
		return 2
	}
	switch args[0] {
	case "login":
		return cmdAuthLogin(args[1:])
	case "logout":
		return cmdAuthLogout(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown auth subcommand: %s\n", args[0])
		return 2
	}
}

func cmdAuthLogin(args []string) int {
	var apiKey string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--api-key":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "--api-key requires a value")
				return 2
			}
			apiKey = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--api-key="):
			apiKey = strings.TrimPrefix(args[i], "--api-key=")
		default:
			fmt.Fprintf(os.Stderr, "unknown argument: %s\n", args[i])
			return 2
		}
	}
	if apiKey != "" {
		return loginWithAPIKey(apiKey)
	}
	// No --api-key: always the device flow. SFERENCE_API_KEY is NOT
	// consulted here — an exported key silently hijacking the interactive
	// login is a worse surprise than scripts passing --api-key explicitly.
	return loginWithDeviceFlow()
}

// loginWithAPIKey persists a static API key to the switch's own auth file.
func loginWithAPIKey(apiKey string) int {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "auth login: API key is empty")
		return 1
	}
	if err := auth.Save(nil, &auth.StoredToken{AccessToken: apiKey}); err != nil {
		fmt.Fprintf(os.Stderr, "auth login: %v\n", err)
		return 1
	}
	fmt.Printf("Saved API key to %s\n", auth.OwnCredentialsPathForDisplay())
	notifyGateway()
	return cmdWhoami(nil)
}

// loginWithDeviceFlow runs the RFC 8628 device flow: request a code, show
// it, open the verification page, poll until the user approves.
func loginWithDeviceFlow() int {
	baseURL := auth.DefaultHostFunc()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	label, _ := os.Hostname()
	dc, err := auth.StartDeviceLogin(ctx, baseURL, label)
	if err != nil {
		fmt.Fprintf(os.Stderr, "auth login: could not start device login: %v\n", err)
		return 1
	}

	fmt.Printf("\nTo sign in, open %s and enter code:\n\n    %s\n\n", dc.VerificationURI, dc.UserCode)
	openBrowser(dc.VerificationURI)
	fmt.Printf("Waiting for approval (code expires in %d minutes)...\n", dc.ExpiresIn/60)

	tokens, err := auth.PollForTokens(ctx, baseURL, dc.DeviceCode,
		time.Duration(dc.Interval)*time.Second, time.Duration(dc.ExpiresIn)*time.Second, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "auth login: %v\n", err)
		return 1
	}
	tok := &auth.StoredToken{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		Expiry:       time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second),
		TokenType:    "Bearer",
		RemoteURL:    baseURL,
	}
	if err := auth.Save(nil, tok); err != nil {
		fmt.Fprintf(os.Stderr, "auth login: %v\n", err)
		return 1
	}
	fmt.Printf("Signed in — grant saved to %s\n", auth.OwnCredentialsPathForDisplay())
	notifyGateway()
	return cmdWhoami(nil)
}

// openBrowser best-effort opens the verification page. Failure is fine —
// the URL is printed right above. A package var so tests can stub it (a
// real `open` would fire a browser on the dev machine).
var openBrowser = func(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	default:
		return
	}
	_ = cmd.Start()
}

// notifyGateway SIGHUPs the running gateway so it re-reads credentials.
func notifyGateway() {
	switch state, pid := classifyPidfile(gatewayPidfilePath()); state {
	case pidfileAlive:
		if err := signalRouter(pid); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not SIGHUP router pid %d: %v; run 'sference-switch restart'\n", pid, err)
		} else {
			fmt.Fprintf(os.Stderr, "router reloaded (SIGHUP pid %d)\n", pid)
		}
	default:
		fmt.Fprintln(os.Stderr, "router not running; the new credential loads at the next start")
	}
}

func cmdAuthLogout(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: sference-switch auth logout")
		return 2
	}
	// Revoke the grant server-side first (best-effort) so it disappears
	// from the console's devices page; then remove the local file.
	if tok, _, _ := auth.Load(""); tok != nil && tok.Kind == auth.KindDevice {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := auth.RevokeToken(ctx, auth.DefaultHostFunc(), tok.RefreshToken); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not revoke grant server-side: %v\n", err)
		}
	}
	if err := auth.Delete(nil); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "auth logout: %v\n", err)
			return 1
		}
	}
	fmt.Printf("Removed %s\n", auth.OwnCredentialsPathForDisplay())
	return 0
}
