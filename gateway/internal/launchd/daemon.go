// daemon.go renders and manages the *system*-domain LaunchDaemon for the
// TLS front door. This is separate from the user LaunchAgents in
// launchd.go: the door binds 443, so it must run as root out of
// /Library/LaunchDaemons in the `system` domain, not `gui/<uid>`.
//
// Supervision is not a nicety here. Started by hand as `sudo … &`, the
// door leaves a sudo wrapper plus a root-owned child; killing the
// wrapper orphans the child, which keeps holding 443 and makes the next
// start fail with "address already in use". launchd owns the process
// instead: one instance, restarted on crash, gone on bootout.
package launchd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// TLSDoorLabel is the launchd label for the root TLS front door.
const TLSDoorLabel = "co.sference.switch.tlsdoor"

// LaunchDaemonsDir is where system-wide daemons live. Overridable in tests.
var LaunchDaemonsDir = "/Library/LaunchDaemons"

// TLSDoorLogPath is where launchd appends the door's stdout/stderr.
const TLSDoorLogPath = "/var/log/sference-switch/tlsdoor.log"

// DaemonPlistPath is the on-disk plist path for a system daemon label.
func DaemonPlistPath(label string) string {
	return filepath.Join(LaunchDaemonsDir, label+".plist")
}

// SystemTarget is the system launchd domain target.
func SystemTarget() string { return "system" }

// RenderDaemonPlist renders a Job as a system LaunchDaemon plist.
//
// It differs from RenderPlist (user agents) in three ways: no
// AssociatedBundleIdentifiers (that key groups background items under an
// app in System Settings, which does not apply to a root daemon),
// explicit UserName=root, and a ThrottleInterval so a door that fails to
// bind does not spin in a tight respawn loop.
func RenderDaemonPlist(j Job) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + xmlEscape(j.Label) + `</string>
	<key>ProgramArguments</key>
	<array>
`)
	for _, a := range j.ProgramArguments {
		b.WriteString("\t\t<string>" + xmlEscape(a) + "</string>\n")
	}
	b.WriteString(`	</array>
	<key>UserName</key>
	<string>root</string>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ThrottleInterval</key>
	<integer>10</integer>
`)
	if len(j.Env) > 0 {
		b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
		keys := make([]string, 0, len(j.Env))
		for k := range j.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString("\t\t<key>" + xmlEscape(k) + "</key>\n")
			b.WriteString("\t\t<string>" + xmlEscape(j.Env[k]) + "</string>\n")
		}
		b.WriteString("\t</dict>\n")
	}
	b.WriteString(`	<key>StandardOutPath</key>
	<string>` + xmlEscape(j.LogPath) + `</string>
	<key>StandardErrorPath</key>
	<string>` + xmlEscape(j.LogPath) + `</string>
</dict>
</plist>
`)
	return b.String()
}

// DaemonLoaded reports whether launchd has the label loaded in the
// system domain.
func DaemonLoaded(r Runner, label string) bool {
	_, err := r.Run("print", SystemTarget()+"/"+label)
	return err == nil
}

// BootstrapDaemon loads a plist into the system domain. Requires root.
func BootstrapDaemon(r Runner, plistPath string) error {
	out, err := r.Run("bootstrap", SystemTarget(), plistPath)
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(out))
	}
	return nil
}

// BootoutDaemon unloads a label from the system domain. A label that is
// not loaded is tolerated so uninstall is idempotent.
func BootoutDaemon(r Runner, label string) (wasLoaded bool, err error) {
	out, rerr := r.Run("bootout", SystemTarget()+"/"+label)
	if rerr == nil {
		return true, nil
	}
	low := strings.ToLower(out + " " + rerr.Error())
	if strings.Contains(low, "no such process") || strings.Contains(low, "could not find") {
		return false, nil
	}
	return false, fmt.Errorf("%v: %s", rerr, strings.TrimSpace(out))
}

// KickstartDaemon restarts a loaded daemon (`launchctl kickstart -k`),
// used to adopt a rebuilt binary without a full uninstall/install cycle.
func KickstartDaemon(r Runner, label string) error {
	out, err := r.Run("kickstart", "-k", SystemTarget()+"/"+label)
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(out))
	}
	return nil
}

// WriteDaemonPlist writes the plist with the ownership and mode launchd
// requires of a system daemon: root:wheel, 0644. launchd silently
// refuses to load a LaunchDaemon plist that is group- or
// world-writable, so the mode is load-bearing, not hygiene.
func WriteDaemonPlist(label, contents string) (string, error) {
	if err := os.MkdirAll(LaunchDaemonsDir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %v", LaunchDaemonsDir, err)
	}
	p := DaemonPlistPath(label)
	if err := os.WriteFile(p, []byte(contents), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %v", p, err)
	}
	// WriteFile honours umask, which can strip the group/other read bits
	// launchd needs; set the mode explicitly.
	if err := os.Chmod(p, 0o644); err != nil {
		return "", fmt.Errorf("chmod %s: %v", p, err)
	}
	if err := os.Chown(p, 0, 0); err != nil {
		return "", fmt.Errorf("chown root:wheel %s: %v", p, err)
	}
	return p, nil
}

// RemoveDaemonPlist deletes the plist file; a missing file is not an error.
func RemoveDaemonPlist(label string) error {
	err := os.Remove(DaemonPlistPath(label))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
