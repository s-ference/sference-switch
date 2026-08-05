package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/sference/sference-switch/gateway/internal/auth"
)

// cmdWhoami reports the current Sference API key status. With a static
// API key (no OAuth, no JWT), it shows whether a key is present and a
// masked prefix for identification — enough to confirm which key is loaded.
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
			// accepted for compat, no-op (static key, nothing to refresh)
		}
	}
	tok, _, err := auth.Load("")
	if err != nil {
		if errors.Is(err, auth.ErrNotSignedIn) {
			fmt.Fprintln(os.Stderr, "not signed in (run 'sference auth login --api-key sk_...')")
			return 3
		}
		fmt.Fprintf(os.Stderr, "whoami: %v\n", err)
		return 1
	}
	if tok == nil {
		fmt.Fprintln(os.Stderr, "not signed in (run 'sference auth login --api-key sk_...')")
		return 3
	}
	key := tok.AccessToken
	masked := key
	if len(key) > 8 {
		masked = key[:4] + "..." + key[len(key)-4:]
	}
	fmt.Printf("Signed in with API key: %s\n", masked)
	fmt.Printf("Fingerprint:           %s\n", auth.CredFingerprint(key))
	return 0
}
