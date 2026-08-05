// Package hosts manages the /etc/hosts entry that redirects
// api.anthropic.com to the local TLS door. The entry is wrapped in
// markers so it can be added and removed cleanly without touching other
// entries.
package hosts

import (
	"fmt"
	"os"
	"strings"
)

const (
	// BeginMarker and EndMarker wrap the managed block in /etc/hosts.
	BeginMarker = "# sference-switch begin"
	EndMarker   = "# sference-switch end"
	// HostsPath is the standard macOS hosts file.
	HostsPath = "/etc/hosts"
	// TargetHost is the hostname we intercept.
	TargetHost = "api.anthropic.com"
	// TargetIP is where we redirect it.
	TargetIP = "127.0.0.1"
	// TargetIPv6 is the IPv6 loopback — nothing listens there, so IPv6
	// connection attempts fail fast and the client falls back to IPv4.
	TargetIPv6 = "::1"
)

// Add appends the managed block to /etc/hosts. Idempotent: if the block
// already exists, it is a no-op. Requires root.
func Add() error {
	return AddTo(HostsPath)
}

// AddTo is Add with an explicit path (for tests).
func AddTo(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if strings.Contains(string(data), BeginMarker) {
		return nil // already present
	}
	// Cover both address families: macOS prefers IPv6 when the hostname
	// resolves to a real AAAA record, which would bypass an IPv4-only
	// redirect. The TLS door binds IPv4 only, so we redirect IPv6 to ::1
	// where nothing listens — the connection fails fast and the client
	// falls back to IPv4 (our door).
	block := fmt.Sprintf("\n%s\n%s %s\n%s %s\n%s\n", BeginMarker, TargetIP, TargetHost, TargetIPv6, TargetHost, EndMarker)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s for append: %w", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(block); err != nil {
		return fmt.Errorf("append to %s: %w", path, err)
	}
	return nil
}

// Remove deletes the managed block from /etc/hosts. Idempotent: if the
// block is absent, it is a no-op. Requires root.
func Remove() error {
	return RemoveFrom(HostsPath)
}

// RemoveFrom is Remove with an explicit path (for tests).
func RemoveFrom(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	content := string(data)
	if !strings.Contains(content, BeginMarker) {
		return nil // already absent
	}
	lines := strings.Split(content, "\n")
	var out []string
	inBlock := false
	for _, line := range lines {
		if strings.TrimSpace(line) == BeginMarker {
			inBlock = true
			continue
		}
		if strings.TrimSpace(line) == EndMarker {
			inBlock = false
			continue
		}
		if !inBlock {
			out = append(out, line)
		}
	}
	// Trim trailing blank lines left by the removal.
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	newContent := strings.Join(out, "\n") + "\n"
	if err := os.WriteFile(path, []byte(newContent), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// IsPresent reports whether the managed block is in /etc/hosts.
func IsPresent() bool {
	return IsPresentIn(HostsPath)
}

// IsPresentIn is IsPresent with an explicit path (for tests).
func IsPresentIn(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), BeginMarker)
}
