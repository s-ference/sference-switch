// config_set.go implements `sference-switch config set <key> <value>` —
// simple scalar mutations on gateway.yaml that the menubar app invokes
// when a user toggles a setting. After writing, SIGHUPs the running
// router so it hot-reloads the config — without this, the admin status
// reports the old value and the UI grays out waiting for a reload that
// never comes.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/sference/sference-switch/gateway/internal/config"
)

func cmdConfigSet(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: sference-switch config set <key> <value>")
		return 2
	}
	key := args[0]
	value := strings.ToLower(strings.TrimSpace(args[1]))

	configPath := config.DefaultPath()
	if p := os.Getenv("SFERENCE_SWITCH_CONFIG_PATH"); p != "" {
		configPath = p
	}

	switch key {
	case "picker_inject":
		var enabled bool
		switch value {
		case "true", "on", "1", "yes":
			enabled = true
		case "false", "off", "0", "no":
			enabled = false
		default:
			fmt.Fprintf(os.Stderr, "picker_inject: invalid value %q (want true/false)\n", value)
			return 1
		}
		if err := config.SetPickerInject(configPath, enabled); err != nil {
			fmt.Fprintf(os.Stderr, "config set picker_inject: %v\n", err)
			return 1
		}
		fmt.Printf("picker_inject set to %t\n", enabled)
		// SIGHUP the running router so it hot-reloads the config.
		// Without this, the admin status reports the old value and
		// the UI's canMutate check sees a pending reload, graying out
		// all controls.
		switch state, pid := classifyPidfile(gatewayPidfilePath()); state {
		case pidfileAlive:
			if err := signalRouter(pid); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not SIGHUP router pid %d: %v; run 'sference-switch restart'\n", pid, err)
			} else {
				fmt.Fprintf(os.Stderr, "router reloaded (SIGHUP pid %d)\n", pid)
			}
		}
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown config key: %s\n", key)
		return 2
	}
}
