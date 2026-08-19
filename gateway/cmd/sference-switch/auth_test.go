package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAuthJSON(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SFERENCE_SWITCH_AUTH_FILE", path)
}

func captureStdout(t *testing.T, fn func() int) (string, int) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	code := fn()
	w.Close()
	os.Stdout = old
	b, _ := io.ReadAll(r)
	r.Close()
	return string(b), code
}

// TestWhoamiWithAPIKey verifies whoami reads the API key from the credentials
// file and prints a masked key + fingerprint.
func TestWhoamiWithAPIKey(t *testing.T) {
	writeAuthJSON(t, `{"token":"sk-test-1234567890abcdef"}`)
	out, code := captureStdout(t, func() int {
		return cmdWhoami(nil)
	})
	if code != 0 {
		t.Fatalf("cmdWhoami = %d, want 0; output:\n%s", code, out)
	}
	if !strings.Contains(out, "Signed in with API key") {
		t.Fatalf("output should report API key sign-in:\n%s", out)
	}
	if !strings.Contains(out, "sk-t...cdef") {
		t.Fatalf("output should show masked key:\n%s", out)
	}
	if !strings.Contains(out, "Fingerprint") {
		t.Fatalf("output should show fingerprint:\n%s", out)
	}
}

// TestWhoamiWithEnvKey verifies whoami reads the API key from SFERENCE_API_KEY.
func TestWhoamiWithEnvKey(t *testing.T) {
	t.Setenv("SFERENCE_API_KEY", "sk-env-test-key-12345")
	writeAuthJSON(t, `{"token":""}`)
	out, code := captureStdout(t, func() int {
		return cmdWhoami(nil)
	})
	if code != 0 {
		t.Fatalf("cmdWhoami = %d, want 0; output:\n%s", code, out)
	}
	if !strings.Contains(out, "Signed in with API key") {
		t.Fatalf("output should report API key sign-in:\n%s", out)
	}
	if !strings.Contains(out, "sk-e...2345") {
		t.Fatalf("output should show masked env key:\n%s", out)
	}
}

// TestWhoamiNotSignedIn verifies whoami exits 3 when no key is found.
func TestWhoamiNotSignedIn(t *testing.T) {
	writeAuthJSON(t, `{"token":""}`)
	out, code := captureStdout(t, func() int {
		return cmdWhoami(nil)
	})
	if code != 3 {
		t.Fatalf("cmdWhoami = %d, want 3; output:\n%s", code, out)
	}
	// The 'not signed in' message goes to stderr; stdout is empty.
	// The exit code (3) is the assertion.
}

// TestWhoamiProfileFlagAccepted verifies --profile is accepted for compat
// but ignored (one credential).
func TestWhoamiProfileFlagAccepted(t *testing.T) {
	writeAuthJSON(t, `{"token":"sk-test-1234567890abcdef"}`)
	out, code := captureStdout(t, func() int {
		return cmdWhoami([]string{"--profile", "anything"})
	})
	if code != 0 {
		t.Fatalf("cmdWhoami = %d, want 0; output:\n%s", code, out)
	}
	if !strings.Contains(out, "Signed in with API key") {
		t.Fatalf("output should report API key sign-in:\n%s", out)
	}
}
