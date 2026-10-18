// hosts_clean.go implements `sference-switch hosts-clean` — the escape
// hatch for a stale /etc/hosts interception block left behind by the
// removed transparent-interception path. The block redirects
// api.anthropic.com to a :443 door that no longer exists, breaking both
// direct Claude Code traffic and the router's native passthrough.
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/sference/sference-switch/gateway/internal/hostsblock"
)

func cmdHostsClean(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: sference-switch hosts-clean")
		return 2
	}
	if !hostsblock.Present(hostsblock.HostsPath) {
		fmt.Println("hosts-clean: no sference-switch block in " + hostsblock.HostsPath)
		return 0
	}
	if err := hostsblock.Remove(hostsblock.HostsPath); err != nil {
		if errors.Is(err, fs.ErrPermission) {
			fmt.Fprintf(os.Stderr, "hosts-clean: %v\n/etc/hosts is root-owned; run: sudo sference-switch hosts-clean\n", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "hosts-clean: %v\n", err)
		return 1
	}
	fmt.Println("hosts-clean: removed the sference-switch block from " + hostsblock.HostsPath)
	return 0
}
