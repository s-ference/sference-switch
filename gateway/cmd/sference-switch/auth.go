package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sference/sference-switch/gateway/internal/auth"
)

// cmdWhoami reports the current Sference credential: source (env, the
// switch's own file, or the shared CLI file), kind (device grant or static
// API key), a masked identifier, and for device grants the access-token
// expiry.
func cmdWhoami(args []string) int {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--profile":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "--profile requires a value")
				return 2
			}
			i++ // accepted for compat, ignored (one credential)
		case strings.HasPrefix(args[i], "--profile="):
			// accepted for compat, ignored
		case args[i] == "--refresh":
			// accepted for compat, no-op (refresh is lazy in the gateway)
		}
	}
	tok, _, err := auth.Load("")
	if err != nil {
		if errors.Is(err, auth.ErrNotSignedIn) {
			fmt.Fprintln(os.Stderr, "not signed in (run 'sference-switch auth login')")
			return 3
		}
		fmt.Fprintf(os.Stderr, "whoami: %v\n", err)
		return 1
	}
	if tok == nil {
		fmt.Fprintln(os.Stderr, "not signed in (run 'sference-switch auth login')")
		return 3
	}
	key := tok.AccessToken
	masked := key
	if len(key) > 8 {
		masked = key[:4] + "..." + key[len(key)-4:]
	}
	switch tok.Kind {
	case auth.KindDevice:
		fmt.Printf("Signed in with device grant: %s\n", masked)
		fmt.Printf("Source:                  %s\n", tok.Path)
		if !tok.Expiry.IsZero() {
			fmt.Printf("Access token expires:    %s\n", tok.Expiry.UTC().Format(time.RFC3339))
		}
	case auth.KindSharedDevice:
		fmt.Printf("Signed in via sference CLI device grant: %s\n", masked)
		fmt.Printf("Source:                            %s (read-only; refreshed by the CLI)\n", tok.Path)
		if !tok.Expiry.IsZero() {
			fmt.Printf("Access token expires:              %s\n", tok.Expiry.UTC().Format(time.RFC3339))
		}
	default:
		fmt.Printf("Signed in with API key: %s\n", masked)
		fmt.Printf("Source:                 %s\n", tok.Path)
	}
	fmt.Printf("Fingerprint:           %s\n", auth.CredFingerprint(key))
	return 0
}
