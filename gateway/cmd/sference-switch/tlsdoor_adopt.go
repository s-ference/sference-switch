package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sference/sference-switch/gateway/internal/launchd"
)

// Root TLS-door adoption.
//
// launchd keeps a daemon on the inode it booted from: after an atomic
// binary replace the running tlsdoor still serves the previous build —
// and with it the previous picker injection. `sference-switch status`
// reports that skew for every other component ("restart to adopt"), and
// `up` (hence `restart` and `upgrade --restart`) adopts them; the root
// daemon was left out because adopting it needs privileges. The result
// was the stale-after-upgrade trap: after every release the door stayed a
// build behind until a reboot or a manual kickstart, and Claude Code's
// /model picker served the old build's entries.
//
// Adoption here is need-driven, never unconditional: the daemon's start
// time is read unprivileged from ps and compared against the binary's
// mtime, so an `up` with nothing to adopt is silent and password-free.
// Only a genuinely stale (or stopped-but-installed) daemon triggers the
// one privileged kickstart, prompted through osascript so it works with
// or without a terminal.

// tlsDoorAdoptDisabled is the env gate for adoption, the same contract as
// the LAUNCHD and MENUBAR gates: `off` disables the privileged restart so
// unit tests and check.sh scratch runs never prompt.
func tlsDoorAdoptDisabled() bool {
	return os.Getenv("SFERENCE_SWITCH_TLS_DOOR") == "off"
}

// tlsDoorPlistPath, tlsDoorPSLister, and tlsDoorKicker are seams so tests
// never touch the real launchd system domain or spawn osascript.
var (
	tlsDoorPlistPath = func() string {
		return launchd.DaemonPlistPath(launchd.TLSDoorLabel)
	}
	tlsDoorPSLister = func() (string, error) {
		return runCmd("/bin/ps", "-axo", "lstart=,command=")
	}
	tlsDoorKicker = kickstartTLSDoor
)

// adoptTLSDoor kicks the root daemon when it is running an older build of
// the binary its plist points at. Advisory only: a failed or cancelled
// kick never fails `up` — routing is already up by this point, only the
// picker injection lags, and the one-line fix is printed instead.
func adoptTLSDoor() {
	// Mirrors the SFERENCE_SWITCH_LAUNCHD/MENUBAR=off gates: check.sh
	// scratch runs and the unit tests set this so no test ever triggers
	// the admin password prompt against a developer's real daemon.
	if tlsDoorAdoptDisabled() {
		return
	}
	psOutput, err := tlsDoorPSLister()
	if err != nil {
		// Without a process listing staleness cannot be decided; a
		// password prompt on a guess would be worse than silence.
		return
	}
	needed, binaryPath, note := tlsDoorAdoption(tlsDoorPlistPath(), psOutput)
	if binaryPath != "" && !needed {
		// An advisory note (the plist's binary is missing) prints
		// without prompting: kickstarting could not fix it anyway.
		if note != "" {
			fmt.Fprintln(os.Stderr, note)
		}
		return
	}
	if !needed {
		return
	}
	fmt.Println("tls door: adopting the installed binary — macOS will ask for your password")
	if err := tlsDoorKicker(binaryPath); err != nil {
		fmt.Fprintf(os.Stderr, "tls door NOT restarted: %v\n", err)
		fmt.Fprintln(os.Stderr, "Fix: sudo sference-switch tls service restart")
		return
	}
	fmt.Printf("tls door: restarted (%s)\n", note)
}

// tlsDoorAdoption decides whether the root daemon needs a kickstart.
// needed=true means the daemon must be restarted and note is the cause;
// needed=false with a non-empty note is an advisory the caller prints
// (kickstarting could not fix it); needed=false with an empty note is
// silence. Not installed means door mode is off entirely.
func tlsDoorAdoption(plistPath, psOutput string) (needed bool, binaryPath, note string) {
	if _, err := os.Stat(plistPath); err != nil {
		return false, "", ""
	}
	binaryPath = launchd.ProgramBinary(plistPath)
	if binaryPath == "" {
		return false, "", ""
	}
	info, err := os.Stat(binaryPath)
	if err != nil {
		return false, binaryPath,
			"tls door: the daemon's binary " + binaryPath + " is missing; reinstall with 'sudo sference-switch tls service install'"
	}
	start, running := tlsDoorProcessStart(psOutput, binaryPath)
	if !running {
		return true, binaryPath, "daemon is installed but not running"
	}
	if start.Before(info.ModTime().Add(-time.Second)) {
		return true, binaryPath, "running daemon predates the installed binary"
	}
	return false, binaryPath, ""
}

// tlsDoorProcessStart finds the tlsdoor process in a `ps -axo
// lstart=,command=` listing. launchd runs exactly "<binary> tlsdoor", so
// the match is on the full command — a substring match would hit any
// unrelated process whose arguments merely mention the binary.
func tlsDoorProcessStart(psOutput, binaryPath string) (time.Time, bool) {
	for _, line := range strings.Split(psOutput, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 6 {
			continue
		}
		start, err := time.ParseInLocation(
			"Mon Jan 2 15:04:05 2006",
			strings.Join(fields[:5], " "),
			time.Local,
		)
		if err != nil {
			continue
		}
		if strings.Join(fields[5:], " ") == binaryPath+" tlsdoor" {
			return start, true
		}
	}
	return time.Time{}, false
}

// kickstartTLSDoor restarts the daemon. Already-root callers (sudo, the
// LaunchDaemon itself) hit launchctl directly; everyone else goes through
// osascript's administrator prompt, which works from a terminal and from
// the app with no TTY.
func kickstartTLSDoor(binaryPath string) error {
	if os.Geteuid() == 0 {
		return launchd.KickstartDaemon(launchdRunner, launchd.TLSDoorLabel)
	}
	if _, err := runCmd("/usr/bin/osascript", "-e", adminScript(binaryPath)); err != nil {
		return err
	}
	return nil
}

// adminScript builds the osascript that runs one CLI command with the
// administrator password prompt. Quotes are doubled per AppleScript string
// escaping. The binary path is the absolute one from the daemon's own
// plist: "do shell script" runs a minimal PATH without ~/.local/bin.
func adminScript(binaryPath string) string {
	command := strings.ReplaceAll(
		binaryPath+" tls service restart", `"`, `""`)
	return `do shell script "` + command + `" with administrator privileges`
}
