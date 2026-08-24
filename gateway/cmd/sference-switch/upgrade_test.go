package main

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestSwapBinary(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sference-switch")
	newBinary := filepath.Join(dir, "new-binary")

	// Write the "current" binary.
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Write the "new" binary.
	if err := os.WriteFile(newBinary, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := swapBinary(target, newBinary); err != nil {
		t.Fatalf("swapBinary: %v", err)
	}

	// Target should have the new content.
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("target = %q, want %q", got, "new")
	}
	// Backup should have the old content.
	backup, err := os.ReadFile(filepath.Join(dir, "sference-switch.previous"))
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != "old" {
		t.Errorf("backup = %q, want %q", backup, "old")
	}
}

func TestSwapBinaryOverwritesStaleBackup(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sference-switch")
	newBinary := filepath.Join(dir, "new-binary")
	backup := filepath.Join(dir, "sference-switch.previous")

	// Write current, stale backup, and new.
	if err := os.WriteFile(target, []byte("current"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, []byte("stale"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newBinary, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := swapBinary(target, newBinary); err != nil {
		t.Fatalf("swapBinary: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("target = %q, want %q", got, "new")
	}
	got, err = os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "current" {
		t.Errorf("backup = %q, want %q", got, "current")
	}
}

func TestSwapBinaryRollbackOnFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sference-switch")
	newBinary := filepath.Join(dir, "new-binary")

	if err := os.WriteFile(target, []byte("original"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newBinary, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Make the target directory read-only so the rename fails.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755)

	err := swapBinary(target, newBinary)
	if err == nil {
		t.Fatal("expected error")
	}

	// Restore permissions and check the original is intact.
	os.Chmod(dir, 0o755)
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Errorf("target = %q, want %q (rollback failed)", got, "original")
	}
}

func TestManifestValidation(t *testing.T) {
	valid := &upgradeManifest{
		SchemaVersion: 1,
		Product:       "sference-switch",
		OS:            "darwin",
		Arch:          "universal",
		SHA256:        hex.EncodeToString(make([]byte, 32)),
		Path:          "sference-switch/v0.1.0/artifact.zip",
		ChecksumsPath: "sference-switch/v0.1.0/checksums.txt",
	}
	if got := validateManifest(valid); got != nil {
		t.Errorf("valid manifest rejected: %v", got)
	}

	bad := *valid
	bad.SchemaVersion = 2
	if got := validateManifest(&bad); got == nil {
		t.Error("schema_version=2 should be rejected")
	}

	bad = *valid
	bad.Product = "other"
	if got := validateManifest(&bad); got == nil {
		t.Error("wrong product should be rejected")
	}

	bad = *valid
	bad.Path = "../../etc/passwd"
	if got := validateManifest(&bad); got == nil {
		t.Error("path with .. should be rejected")
	}

	bad = *valid
	bad.Path = "/etc/passwd"
	if got := validateManifest(&bad); got == nil {
		t.Error("absolute path should be rejected")
	}

	bad = *valid
	bad.SHA256 = "not-hex"
	if got := validateManifest(&bad); got == nil {
		t.Error("non-hex sha256 should be rejected")
	}
}

// validateManifest is a test helper that mirrors the validation in
// fetchUpgradeManifest. Kept separate so the test doesn't need a network.
func validateManifest(m *upgradeManifest) error {
	if m.SchemaVersion != 1 {
		return errInvalidManifest("unsupported schema_version")
	}
	if m.Product != "sference-switch" {
		return errInvalidManifest("wrong product")
	}
	if m.OS != "darwin" {
		return errInvalidManifest("wrong os")
	}
	if m.Arch != "universal" {
		return errInvalidManifest("wrong arch")
	}
	if len(m.SHA256) != 64 {
		return errInvalidManifest("invalid sha256")
	}
	if containsDotDot(m.Path) || isAbsolute(m.Path) {
		return errInvalidManifest("unsafe path")
	}
	if m.Path[:15] != "sference-switch" {
		return errInvalidManifest("path must start with sference-switch")
	}
	return nil
}

type manifestError string

func (e manifestError) Error() string { return string(e) }

func errInvalidManifest(msg string) error { return manifestError(msg) }

func containsDotDot(s string) bool {
	return len(s) >= 2 && (s == ".." || s[:2] == ".." || s[len(s)-2:] == ".." ||
		len(s) > 2 && (s[1:3] == ".." || s[len(s)-3:len(s)-1] == ".."))
}

func isAbsolute(s string) bool { return len(s) > 0 && s[0] == '/' }
