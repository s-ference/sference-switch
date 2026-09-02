package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tlsDoorEnv writes a daemon plist pointing at a real binary file in a
// scratch dir and returns (plistPath, binaryPath). The binary's mtime is
// controllable so staleness tests are exact.
func tlsDoorEnv(t *testing.T, daemonStart time.Time, running bool) (plistPath, binaryPath string) {
	t.Helper()
	dir := t.TempDir()
	binaryPath = filepath.Join(dir, "sference-switch")
	if err := os.WriteFile(binaryPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plistPath = filepath.Join(dir, "co.sference.switch.tlsdoor.plist")
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>co.sference.switch.tlsdoor</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + binaryPath + `</string>
		<string>tlsdoor</string>
	</array>
</dict>
</plist>
`
	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	// The binary lands "now"; the daemon either predates it (stale) or
	// started after it (current).
	if err := os.Chtimes(binaryPath, time.Now(), time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	_ = daemonStart
	_ = running
	return plistPath, binaryPath
}

// psLine renders the daemon's line in `ps -axo lstart=,command=` format
// for the given binary and start time.
func psLine(binaryPath string, start time.Time) string {
	return start.Format("Mon Jan  2 15:04:05 2006") + " " + binaryPath + " tlsdoor"
}

// A daemon older than the installed binary must be adopted; a daemon that
// started after the binary landed is current and stays silent.
func TestTLSDoorAdoptionStaleness(t *testing.T) {
	plistPath, binaryPath := tlsDoorEnv(t, time.Time{}, false)

	psStale := psLine(binaryPath, time.Now().Add(-2*time.Hour))
	if needed, _, note := tlsDoorAdoption(plistPath, psStale); !needed {
		t.Fatalf("daemon predating the binary not adopted: needed=false note=%q", note)
	}

	psCurrent := psLine(binaryPath, time.Now().Add(time.Minute))
	if needed, _, _ := tlsDoorAdoption(plistPath, psCurrent); needed {
		t.Fatal("current daemon reported as needing adoption; every up would prompt")
	}
}

// The daemon is installed but its process is absent: KeepAlive should
// have it running, so this is adoptable too — the kickstart also starts.
func TestTLSDoorAdoptionNotRunning(t *testing.T) {
	plistPath, _ := tlsDoorEnv(t, time.Time{}, false)
	needed, _, note := tlsDoorAdoption(plistPath, "ps: nothing here")
	if !needed || note == "" {
		t.Fatalf("installed-but-stopped daemon not adopted: needed=%v note=%q", needed, note)
	}
}

// No plist means door mode is off: adoption stays silent, no prompt.
func TestTLSDoorAdoptionNotInstalled(t *testing.T) {
	needed, _, note := tlsDoorAdoption(filepath.Join(t.TempDir(), "absent.plist"), "")
	if needed || note != "" {
		t.Fatalf("absent daemon produced needed=%v note=%q, want silence", needed, note)
	}
}

// A plist whose binary is gone cannot be fixed by kickstarting: the
// result is an advisory line, never a password prompt.
func TestTLSDoorAdoptionMissingBinary(t *testing.T) {
	plistPath, binaryPath := tlsDoorEnv(t, time.Time{}, false)
	if err := os.Remove(binaryPath); err != nil {
		t.Fatal(err)
	}
	needed, _, note := tlsDoorAdoption(plistPath, psLine(binaryPath, time.Now()))
	if needed {
		t.Fatal("missing binary triggers a kickstart prompt")
	}
	if note == "" {
		t.Fatal("missing binary produced no advisory line")
	}
}

// The daemon command match is exact: another process whose arguments
// merely mention the binary must not be mistaken for the daemon.
func TestTLSDoorProcessStartExactMatch(t *testing.T) {
	_, binaryPath := tlsDoorEnv(t, time.Time{}, false)
	want := time.Now().Add(-3 * time.Hour).Truncate(time.Second)
	ps := strings.Join([]string{
		psLine(binaryPath, want),
		"Mon Jan  2 10:00:00 2006 vim notes/sference-switch tlsdoor-todo.txt",
		"Mon Jan  2 09:00:00 2006 /some/other/sference-switch tlsdoor",
	}, "\n")
	got, ok := tlsDoorProcessStart(ps, binaryPath)
	if !ok {
		t.Fatal("daemon line not found")
	}
	if !got.Equal(want) {
		t.Fatalf("start = %v, want %v", got, want)
	}
}

// adoptTLSDoor must stay silent (and never prompt) for a current daemon,
// kick a stale one with the plist's binary, and print the manual fix when
// the privileged step fails.
func TestAdoptTLSDoorKicksOnlyStale(t *testing.T) {
	oldPath, oldList, oldKick := tlsDoorPlistPath, tlsDoorPSLister, tlsDoorKicker
	t.Cleanup(func() { tlsDoorPlistPath, tlsDoorPSLister, tlsDoorKicker = oldPath, oldList, oldKick })

	kicks := 0
	var kicked string
	tlsDoorKicker = func(binary string) error {
		kicks++
		kicked = binary
		return nil
	}

	plistPath, binaryPath := tlsDoorEnv(t, time.Time{}, false)
	tlsDoorPlistPath = func() string { return plistPath }

	// Current daemon: silent, no prompt.
	tlsDoorPSLister = func() (string, error) { return psLine(binaryPath, time.Now().Add(time.Minute)), nil }
	adoptTLSDoor()
	if kicks != 0 {
		t.Fatalf("current daemon kicked %d times", kicks)
	}

	// Stale daemon: kicked with the plist's binary path.
	tlsDoorPSLister = func() (string, error) { return psLine(binaryPath, time.Now().Add(-2*time.Hour)), nil }
	adoptTLSDoor()
	if kicks != 1 || kicked != binaryPath {
		t.Fatalf("kicks=%d kicked=%q, want 1 kick with the plist binary", kicks, kicked)
	}

	// Declined prompt: the kick fails, adoption does not loop or panic.
	tlsDoorKicker = func(string) error { return os.ErrPermission }
	adoptTLSDoor() // must not panic; the manual fix is printed to stderr
}

// The osascript payload runs the daemon's own absolute binary path —
// "do shell script" has a minimal PATH without ~/.local/bin — and
// escapes quotes per AppleScript string rules.
func TestAdminScriptUsesPlistBinaryAndEscapes(t *testing.T) {
	script := adminScript("/Users/me/bin with space/sference-switch")
	if !strings.Contains(script, `do shell script "/Users/me/bin with space/sference-switch tls service restart" with administrator privileges`) {
		t.Fatalf("script = %q", script)
	}
	if script != adminScript(`/Users/me/bin with space/sference-switch`) {
		t.Fatal("script is not deterministic")
	}
	escaped := adminScript(`/bin/we"ird`)
	if !strings.Contains(escaped, `we""ird`) {
		t.Fatalf("quote not doubled: %q", escaped)
	}
}
