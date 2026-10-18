// Package hostsblock detects and removes the stale /etc/hosts block left
// behind by the removed transparent-interception path (the block redirected
// api.anthropic.com to the local :443 TLS door). With nothing listening on
// :443 anymore, a leftover block breaks not just direct Claude Code traffic
// but the router's own native passthrough — and the commands that used to
// remove it (intercept off) no longer exist. This package is deliberately
// read/remove-only: nothing may ever write the block back.
package hostsblock

import (
	"fmt"
	"os"
	"strings"
)

const (
	// BeginMarker and EndMarker wrapped the managed block written by the
	// removed interception path. Kept byte-identical to it forever so
	// detection matches every block any released version wrote.
	BeginMarker = "# sference-switch begin"
	EndMarker   = "# sference-switch end"
	// HostsPath is the standard hosts file path on macOS and Linux.
	HostsPath = "/etc/hosts"
)

// Present reports whether the managed block is in the file at path.
func Present(path string) bool {
	data, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(data), BeginMarker)
}

// Remove deletes the managed block from the file at path, preserving every
// other byte. Idempotent: an absent block is a no-op. Requires write
// permission on the file (root for /etc/hosts).
func Remove(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	src := string(data)
	start := strings.Index(src, BeginMarker)
	if start < 0 {
		return nil
	}
	// The block was appended with a leading blank line; drop it too so a
	// remove leaves the file byte-identical to its pre-interception state.
	if start > 0 && src[start-1] == '\n' {
		start--
	}
	end := strings.Index(src[start:], EndMarker)
	if end < 0 {
		return fmt.Errorf("%s: found %q but no %q; edit the file by hand", path, BeginMarker, EndMarker)
	}
	end += start + len(EndMarker)
	if end < len(src) && src[end] == '\n' {
		end++
	}
	out := src[:start] + src[end:]
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
