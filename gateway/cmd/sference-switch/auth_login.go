// auth_login.go implements `sference-switch auth login --api-key sk_...`
// and `sference-switch auth logout`. The API key is written to
// ~/.sference/credentials.json (the same file the sference CLI writes and
// the gateway reads). After a successful login the running gateway is
// SIGHUP'd so it picks up the new key immediately.
package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/sference/sference-switch/gateway/internal/auth"
)

func cmdAuth(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: sference-switch auth login --api-key sk_...  |  auth logout")
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
	if apiKey == "" {
		// Fall back to an existing env var so scripts can pre-set it.
		if env := os.Getenv("SFERENCE_API_KEY"); env != "" {
			apiKey = env
		}
	}
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "auth login: --api-key is required (e.g. sference-switch auth login --api-key sk_...)")
		fmt.Fprintln(os.Stderr, "Create a key in Console → API Keys, then run this command.")
		return 1
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "auth login: API key is empty")
		return 1
	}
	if err := auth.Save(nil, &auth.StoredToken{AccessToken: apiKey}); err != nil {
		fmt.Fprintf(os.Stderr, "auth login: %v\n", err)
		return 1
	}
	fmt.Printf("Saved API key to ~/.sference/credentials.json\n")
	// SIGHUP the running gateway so it picks up the new key.
	switch state, pid := classifyPidfile(gatewayPidfilePath()); state {
	case pidfileAlive:
		if err := signalRouter(pid); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not SIGHUP router pid %d: %v; run 'sference-switch restart'\n", pid, err)
		} else {
			fmt.Fprintf(os.Stderr, "router reloaded (SIGHUP pid %d)\n", pid)
		}
	default:
		fmt.Fprintln(os.Stderr, "router not running; the new key loads at the next start")
	}
	return cmdWhoami(nil)
}

func cmdAuthLogout(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: sference-switch auth logout")
		return 2
	}
	if err := auth.Delete(nil); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "auth logout: %v\n", err)
			return 1
		}
	}
	fmt.Println("Removed ~/.sference/credentials.json")
	return 0
}
