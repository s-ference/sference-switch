package gateway

import (
	"os"
	"testing"
)

// TestMain makes this package hermetic against the developer's real
// credentials. The gateway reads SFERENCE_API_KEY (and the native provider
// keys) from the environment at startup, so a machine with those exported —
// which any Sference developer has — injects the real key into upstream
// requests under test. It then appears verbatim in failure output, and from
// there in CI logs and any contributor's terminal.
//
// Tests that need a credential set their own with t.Setenv.
func TestMain(m *testing.M) {
	for _, key := range []string{
		"SFERENCE_API_KEY",
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
		"OPENAI_API_KEY",
	} {
		os.Unsetenv(key)
	}
	os.Exit(m.Run())
}
