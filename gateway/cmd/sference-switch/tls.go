// tls.go implements `sference-switch tls setup|install|uninstall` and
// `sference-switch intercept on|off` — the transparent-interception
// management commands.
package main

import (
	"fmt"
	"os"

	"github.com/sference/sference-switch/gateway/internal/config"
	"github.com/sference/sference-switch/gateway/internal/hosts"
	"github.com/sference/sference-switch/gateway/internal/tlsca"
)

func cmdTLS(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: sference-switch tls setup|install|uninstall|service")
		return 2
	}
	switch args[0] {
	case "setup":
		return cmdTLSSetup(args[1:])
	case "install":
		return cmdTLSInstall(args[1:])
	case "uninstall":
		return cmdTLSUninstall(args[1:])
	case "service":
		return cmdTLSService(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown tls subcommand: %s\n", args[0])
		return 2
	}
}

func cmdTLSSetup(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: sference-switch tls setup")
		return 2
	}
	configDir := config.DefaultDir()
	leafCert, leafKey, err := tlsca.Ensure(configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tls setup: %v\n", err)
		return 1
	}
	fmt.Printf("CA and leaf certificate ready:\n  CA:   %s\n  Leaf: %s\n  Key:  %s\n", configDir+"/ca/ca.pem", leafCert, leafKey)
	fmt.Println("\nNext: sudo sference-switch tls install")
	return 0
}

func cmdTLSInstall(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: sference-switch tls install")
		return 2
	}
	configDir := config.DefaultDir()
	if err := tlsca.InstallCAToSystemKeychain(configDir); err != nil {
		fmt.Fprintf(os.Stderr, "tls install: %v\n", err)
		return 1
	}
	fmt.Println("CA installed to System keychain (trusted root).")
	fmt.Println("Next: sudo sference-switch intercept on")
	return 0
}

func cmdTLSUninstall(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: sference-switch tls uninstall")
		return 2
	}
	configDir := config.DefaultDir()
	if err := tlsca.RemoveCAFromSystemKeychain(configDir); err != nil {
		fmt.Fprintf(os.Stderr, "tls uninstall: %v\n", err)
		return 1
	}
	fmt.Println("CA removed from System keychain.")
	return 0
}

func cmdIntercept(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: sference-switch intercept on|off|status")
		return 2
	}
	switch args[0] {
	case "on":
		return cmdInterceptOn(args[1:])
	case "off":
		return cmdInterceptOff(args[1:])
	case "status":
		return cmdInterceptStatus(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown intercept subcommand: %s\n", args[0])
		return 2
	}
}

func cmdInterceptOn(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: sference-switch intercept on")
		return 2
	}
	if err := hosts.Add(); err != nil {
		fmt.Fprintf(os.Stderr, "intercept on: %v\n", err)
		return 1
	}
	fmt.Println("Intercept enabled: api.anthropic.com → 127.0.0.1")
	fmt.Println("Next: sference-switch up (starts the TLS door)")
	return 0
}

func cmdInterceptOff(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: sference-switch intercept off")
		return 2
	}
	if err := hosts.Remove(); err != nil {
		fmt.Fprintf(os.Stderr, "intercept off: %v\n", err)
		return 1
	}
	fmt.Println("Intercept disabled: api.anthropic.com restored to real DNS")
	return 0
}

func cmdInterceptStatus(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: sference-switch intercept status")
		return 2
	}
	if hosts.IsPresent() {
		fmt.Println("intercept: on (api.anthropic.com → 127.0.0.1)")
	} else {
		fmt.Println("intercept: off")
	}
	return 0
}
