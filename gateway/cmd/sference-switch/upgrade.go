// upgrade.go implements `sference-switch upgrade` — self-update from
// get.sference.com. Downloads the latest release manifest, verifies the
// SHA-256 checksum, extracts the ZIP, and swaps the running binary in
// place (temp file + rename on the same filesystem). The .app bundle is
// replaced via the existing menubar.go machinery (materialize + activate).
//
// No sudo is needed: the CLI installs to ~/.local/bin and the app to
// ~/Applications, both user-owned. The daemons (router, door, tlsdoor) are
// not restarted — `sference-switch restart` adopts the new binary.
package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sference/sference-switch/gateway/internal/version"
)

// upgradeManifest mirrors the flat manifest.json published to S3. One field
// per line, no nested objects — the POSIX-sh installer extracts fields with
// sed, and Go unmarshals directly.
type upgradeManifest struct {
	SchemaVersion int    `json:"schema_version"`
	Product       string `json:"product"`
	Channel       string `json:"channel"`
	Tag           string `json:"tag"`
	Version       string `json:"version"`
	PublishedAt   string `json:"published_at"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	Filename      string `json:"filename"`
	Path          string `json:"path"`
	ChecksumsPath string `json:"checksums_path"`
	SHA256        string `json:"sha256"`
	Size          int64  `json:"size"`
	Signing       string `json:"signing"`
	Notarized     bool   `json:"notarized"`
	MinimumMacOS  string `json:"minimum_macos"`
}

// upgradeBaseURL is a package var so tests can override it without network.
var upgradeBaseURL = func() string {
	if v := os.Getenv("SFERENCE_SWITCH_BASE_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://get.sference.com"
}

// upgradeChannel is a package var so tests can override it.
var upgradeChannel = func() string {
	if v := os.Getenv("SFERENCE_SWITCH_CHANNEL"); v != "" {
		return v
	}
	return "stable"
}

// upgradeHTTPClient is a package var so tests can inject a fake.
var upgradeHTTPClient = &http.Client{Timeout: 30 * time.Second}

// upgradeExecutablePath is a package var so tests can override it.
var upgradeExecutablePath = func() (string, error) {
	return os.Executable()
}

func cmdUpgrade(args []string) int {
	check := false
	force := false
	cliOnly := false
	restart := false
	for _, a := range args {
		switch a {
		case "--check":
			check = true
		case "--force":
			force = true
		case "--cli-only":
			cliOnly = true
		case "--restart":
			restart = true
		default:
			fmt.Fprintf(os.Stderr, "unknown option: %s\n", a)
			return 2
		}
	}

	// Guard: refuse to upgrade a Homebrew or Nix install.
	exe, err := upgradeExecutablePath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "upgrade: cannot resolve executable path: %v\n", err)
		return 1
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		fmt.Fprintf(os.Stderr, "upgrade: cannot resolve symlinks: %v\n", err)
		return 1
	}
	if strings.HasPrefix(resolved, "/opt/homebrew") ||
		strings.HasPrefix(resolved, "/usr/local/Cellar") ||
		strings.HasPrefix(resolved, "/usr/local/opt") {
		fmt.Fprintf(os.Stderr, "upgrade: this binary is managed by Homebrew; use 'brew upgrade sference-switch'\n")
		return 1
	}
	if strings.HasPrefix(resolved, "/nix/store") {
		fmt.Fprintf(os.Stderr, "upgrade: this binary is managed by Nix; use 'nix profile upgrade'\n")
		return 1
	}

	// Fetch the manifest.
	manifest, err := fetchUpgradeManifest()
	if err != nil {
		fmt.Fprintf(os.Stderr, "upgrade: %v\n", err)
		return 1
	}

	current := version.Version
	fmt.Printf("current:  %s\n", current)
	fmt.Printf("available: %s\n", manifest.Version)

	cmp := compareSemver(current, manifest.Version)
	if current == "dev" && !force {
		fmt.Fprintln(os.Stderr, "development build; pass --force to replace it")
		return 1
	}
	if cmp >= 0 && !force {
		fmt.Println("already up to date")
		return 0
	}
	if check {
		fmt.Println("update available")
		return 0
	}
	fmt.Printf("upgrading to %s\n", manifest.Version)

	// Download and verify.
	tmpDir, err := os.MkdirTemp("", "sference-switch-upgrade.")
	if err != nil {
		fmt.Fprintf(os.Stderr, "upgrade: mktemp: %v\n", err)
		return 1
	}
	defer os.RemoveAll(tmpDir)

	zipPath := filepath.Join(tmpDir, manifest.Filename)
	if err := downloadAndVerify(manifest, zipPath); err != nil {
		fmt.Fprintf(os.Stderr, "upgrade: %v\n", err)
		return 1
	}

	// Extract.
	if err := extractUpgradePayload(zipPath, tmpDir); err != nil {
		fmt.Fprintf(os.Stderr, "upgrade: %v\n", err)
		return 1
	}

	// Verify the new binary before trusting it.
	newBinary := filepath.Join(tmpDir, "bin", "sference-switch")
	if err := verifyUpgradeBinary(newBinary, manifest.Version); err != nil {
		fmt.Fprintf(os.Stderr, "upgrade: %v\n", err)
		return 1
	}

	// Swap the running binary.
	if err := swapBinary(resolved, newBinary); err != nil {
		fmt.Fprintf(os.Stderr, "upgrade: %v\n", err)
		return 1
	}
	fmt.Printf("CLI updated: %s\n", resolved)

	// Swap the app bundle.
	if !cliOnly {
		if err := upgradeAppBundle(tmpDir); err != nil {
			fmt.Fprintf(os.Stderr, "upgrade: app bundle: %v\n", err)
			// The CLI is already updated; the app failure is not fatal.
		}
	}

	fmt.Printf("upgraded to %s\n", manifest.Version)
	if !manifest.Notarized {
		fmt.Fprintln(os.Stderr, "note: Sference Switch is ad-hoc signed and not Apple-notarized.")
		fmt.Fprintln(os.Stderr, "If macOS blocks the app, open it once from ~/Applications,")
		fmt.Fprintln(os.Stderr, "then approve it in System Settings > Privacy & Security > Open Anyway.")
	}
	if restart {
		fmt.Println("restarting services…")
		return cmdRestart(nil)
	}
	fmt.Println("run 'sference-switch restart' to adopt the new binary in running services")
	return 0
}

// fetchUpgradeManifest downloads and validates the latest.json manifest.
func fetchUpgradeManifest() (*upgradeManifest, error) {
	url := upgradeBaseURL() + "/sference-switch/" + upgradeChannel() + "/latest.json"
	resp, err := upgradeHTTPClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch manifest: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("fetch manifest: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read manifest: %v", err)
	}
	var m upgradeManifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %v", err)
	}
	if m.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported manifest schema_version: %d", m.SchemaVersion)
	}
	if m.Product != "sference-switch" {
		return nil, fmt.Errorf("manifest product is %q, want sference-switch", m.Product)
	}
	if m.OS != "darwin" {
		return nil, fmt.Errorf("manifest os is %q, want darwin", m.OS)
	}
	if m.Arch != "universal" {
		return nil, fmt.Errorf("manifest arch is %q, want universal", m.Arch)
	}
	if !isHex64(m.SHA256) {
		return nil, fmt.Errorf("manifest sha256 is not 64 lowercase hex")
	}
	if strings.Contains(m.Path, "..") || strings.HasPrefix(m.Path, "/") {
		return nil, fmt.Errorf("manifest path is unsafe: %q", m.Path)
	}
	if !strings.HasPrefix(m.Path, "sference-switch/") {
		return nil, fmt.Errorf("manifest path must start with sference-switch/")
	}
	if strings.Contains(m.ChecksumsPath, "..") || strings.HasPrefix(m.ChecksumsPath, "/") {
		return nil, fmt.Errorf("manifest checksums_path is unsafe: %q", m.ChecksumsPath)
	}
	if !strings.HasPrefix(m.ChecksumsPath, "sference-switch/") {
		return nil, fmt.Errorf("manifest checksums_path must start with sference-switch/")
	}
	return &m, nil
}

// downloadAndVerify streams the ZIP to disk while hashing it.
func downloadAndVerify(m *upgradeManifest, dest string) error {
	url := upgradeBaseURL() + "/" + m.Path
	resp, err := upgradeHTTPClient.Get(url)
	if err != nil {
		return fmt.Errorf("download: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}

	f, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create: %v", err)
	}
	defer f.Close()

	h := sha256.New()
	written, err := io.Copy(f, io.TeeReader(io.LimitReader(resp.Body, m.Size+1<<20), h))
	if err != nil {
		return fmt.Errorf("download: %v", err)
	}
	if written != m.Size {
		return fmt.Errorf("download: expected %d bytes, got %d", m.Size, written)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != m.SHA256 {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", m.SHA256, got)
	}
	return nil
}

// extractUpgradePayload extracts the ZIP into dest, rejecting path traversal.
func extractUpgradePayload(zipPath, dest string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open zip: %v", err)
	}
	defer r.Close()

	for _, f := range r.File {
		// Reject path traversal and symlinks.
		if strings.Contains(f.Name, "..") || strings.HasPrefix(f.Name, "/") {
			return fmt.Errorf("zip entry %q is unsafe", f.Name)
		}
		if f.FileInfo().Mode()&os.ModeSymlink != 0 {
			continue // skip symlinks
		}
		target := filepath.Join(dest, f.Name)
		if !strings.HasPrefix(target, filepath.Clean(dest)+string(os.PathSeparator)) {
			return fmt.Errorf("zip entry %q escapes destination", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %v", target, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %v", filepath.Dir(target), err)
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("open zip entry %s: %v", f.Name, err)
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.FileInfo().Mode())
		if err != nil {
			rc.Close()
			return fmt.Errorf("create %s: %v", target, err)
		}
		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			rc.Close()
			return fmt.Errorf("write %s: %v", target, err)
		}
		out.Close()
		rc.Close()
	}
	return nil
}

// verifyUpgradeBinary checks the new binary before trusting it.
func verifyUpgradeBinary(path, wantVersion string) error {
	// codesign --verify --strict
	if out, err := runCmd("/usr/bin/codesign", "--verify", "--strict", path); err != nil {
		return fmt.Errorf("codesign verify failed: %v %s", err, out)
	}
	// codesign --display --verbose=4 → Signature=adhoc
	if out, err := runCmd("/usr/bin/codesign", "--display", "--verbose=4", path); err != nil {
		return fmt.Errorf("codesign display failed: %v %s", err, out)
	} else if !strings.Contains(out, "Signature=adhoc") {
		return fmt.Errorf("binary is not ad-hoc signed")
	}
	// lipo -archs → arm64 + x86_64
	if out, err := runCmd("/usr/bin/lipo", "-archs", path); err != nil {
		return fmt.Errorf("lipo failed: %v %s", err, out)
	} else if !strings.Contains(out, "arm64") || !strings.Contains(out, "x86_64") {
		return fmt.Errorf("binary is not universal (archs: %s)", out)
	}
	// --version must match the manifest.
	out, err := runCmd(path, "--version")
	if err != nil {
		return fmt.Errorf("--version failed: %v %s", err, out)
	}
	want := "sference-switch v" + wantVersion
	if strings.TrimSpace(out) != want {
		return fmt.Errorf("version mismatch: got %q, want %q", strings.TrimSpace(out), want)
	}
	return nil
}

// swapBinary replaces the running binary with the new one via temp+rename.
func swapBinary(target, newBinary string) error {
	dir := filepath.Dir(target)
	stage := filepath.Join(dir, ".sference-switch.upgrade-"+strconv.FormatInt(time.Now().UnixNano(), 36))
	backup := filepath.Join(dir, "sference-switch.previous")

	// Copy the verified binary to the same filesystem (same directory).
	src, err := os.Open(newBinary)
	if err != nil {
		return fmt.Errorf("open new binary: %v", err)
	}
	defer src.Close()
	dst, err := os.OpenFile(stage, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("create staging: %v", err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		os.Remove(stage)
		return fmt.Errorf("copy to staging: %v", err)
	}
	if err := dst.Sync(); err != nil {
		dst.Close()
		os.Remove(stage)
		return fmt.Errorf("sync staging: %v", err)
	}
	dst.Close()

	// Rename current → backup, then stage → target.
	if _, err := os.Lstat(target); err == nil {
		// Overwrite any stale backup.
		os.Remove(backup)
		if err := os.Rename(target, backup); err != nil {
			os.Remove(stage)
			return fmt.Errorf("backup current binary: %v", err)
		}
	}
	if err := os.Rename(stage, target); err != nil {
		// Restore the backup.
		if _, berr := os.Lstat(backup); berr == nil {
			_ = os.Rename(backup, target)
		}
		os.Remove(stage)
		return fmt.Errorf("install new binary: %v", err)
	}
	return nil
}

// upgradeAppBundle replaces the .app bundle using the menubar.go machinery.
func upgradeAppBundle(tmpDir string) error {
	appZip := filepath.Join(tmpDir, "Sference Switch.app.zip")
	if _, err := os.Stat(appZip); err != nil {
		return fmt.Errorf("app payload missing: %v", err)
	}
	if menubarDisabled() {
		fmt.Println("menubar app: skipped (SFERENCE_SWITCH_MENUBAR=off)")
		return nil
	}
	if err := quitMenubar(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not quit menubar: %v\n", err)
	}
	home, _ := os.UserHomeDir()
	appDir := filepath.Join(home, "Applications")
	app, cleanup, err := materializeMenubarPayload(appZip, appDir)
	if err != nil {
		return fmt.Errorf("materialize app: %v", err)
	}
	defer cleanup()
	dst := filepath.Join(appDir, menubarAppName)
	if err := activateMenubarBundle(app, dst); err != nil {
		return fmt.Errorf("activate app: %v", err)
	}
	// Touch the bundle so staleness detection sees the update.
	now := time.Now()
	if err := os.Chtimes(dst, now, now); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not touch app bundle: %v\n", err)
	}
	fmt.Printf("App updated: %s\n", dst)
	return nil
}

// compareSemver compares two semver strings. Returns -1, 0, or 1.
// "dev" compares as less than everything.
func compareSemver(a, b string) int {
	if a == "dev" {
		return -1
	}
	if b == "dev" {
		return 1
	}
	a = strings.TrimPrefix(a, "v")
	b = strings.TrimPrefix(b, "v")
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		var av, bv int
		if i < len(aParts) {
			av, _ = strconv.Atoi(aParts[i])
		}
		if i < len(bParts) {
			bv, _ = strconv.Atoi(bParts[i])
		}
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}
	return 0
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
