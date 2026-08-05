// tls_service.go implements `sference-switch tls service …` — installing
// the TLS front door as a root LaunchDaemon.
//
// The door has to run as root (it binds 443) and has to outlive the shell
// that starts it. Hand-starting it as `sudo … &` satisfies neither: the
// shell owns the job, so it dies with the terminal, and each start leaves
// a sudo wrapper plus a root-owned child — kill the wrapper and the child
// keeps holding 443, so the next start fails to bind. launchd removes
// both problems: one supervised instance, restarted on crash, started at
// boot, stopped by label.
package main

import (
	"fmt"
	"os"

	"github.com/sference/sference-switch/gateway/internal/config"
	"github.com/sference/sference-switch/gateway/internal/launchd"
)

// tlsDoorEnvPassthrough are the environment variables the daemon needs
// pinned into its plist. launchd starts jobs with a minimal environment,
// so anything the door reads from the environment must be carried here
// explicitly rather than inherited.
var tlsDoorEnvPassthrough = []string{
	"SFERENCE_SWITCH_INSECURE_TLS",
	"SFERENCE_SWITCH_CONFIG_DIR",
}

func cmdTLSService(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: sference-switch tls service install|uninstall|restart|status")
		return 2
	}
	switch args[0] {
	case "install":
		return cmdTLSServiceInstall(args[1:])
	case "uninstall":
		return cmdTLSServiceUninstall(args[1:])
	case "restart":
		return cmdTLSServiceRestart(args[1:])
	case "status":
		return cmdTLSServiceStatus(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown tls service subcommand: %s\n", args[0])
		return 2
	}
}

func requireRoot(what string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("%s requires root; re-run with sudo", what)
	}
	return nil
}

func cmdTLSServiceInstall(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: sference-switch tls service install")
		return 2
	}
	if err := requireRoot("tls service install"); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	// The plist must reference a stable path. Under sudo, os.Executable
	// still resolves to the invoked binary, so a door installed from a
	// build directory would break when that directory is cleaned.
	exe, err := launchd.StableProgramPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "tls service install: %v\n", err)
		return 1
	}
	if err := ensureDoorLogDir(); err != nil {
		fmt.Fprintf(os.Stderr, "tls service install: %v\n", err)
		return 1
	}

	env := map[string]string{}
	for _, k := range tlsDoorEnvPassthrough {
		if v := os.Getenv(k); v != "" {
			env[k] = v
		}
	}
	// The door reads its leaf certificate out of the config dir. Under
	// sudo, HOME is root's, so pin the invoking user's config dir unless
	// one was passed explicitly.
	if env["SFERENCE_SWITCH_CONFIG_DIR"] == "" {
		env["SFERENCE_SWITCH_CONFIG_DIR"] = config.DefaultDir()
	}

	plist := launchd.RenderDaemonPlist(launchd.Job{
		Label:            launchd.TLSDoorLabel,
		ProgramArguments: []string{exe, "tlsdoor"},
		Env:              env,
		LogPath:          launchd.TLSDoorLogPath,
	})

	runner := launchdRunner
	// Replace any previous instance so install is idempotent and always
	// lands on the current binary.
	if _, err := launchd.BootoutDaemon(runner, launchd.TLSDoorLabel); err != nil {
		fmt.Fprintf(os.Stderr, "tls service install: bootout previous: %v\n", err)
		return 1
	}
	path, err := launchd.WriteDaemonPlist(launchd.TLSDoorLabel, plist)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tls service install: %v\n", err)
		return 1
	}
	if err := launchd.BootstrapDaemon(runner, path); err != nil {
		fmt.Fprintf(os.Stderr, "tls service install: %v\n", err)
		return 1
	}
	fmt.Printf("TLS door installed as a LaunchDaemon.\n")
	fmt.Printf("  label:  %s\n", launchd.TLSDoorLabel)
	fmt.Printf("  plist:  %s\n", path)
	fmt.Printf("  binary: %s\n", exe)
	fmt.Printf("  log:    %s\n", launchd.TLSDoorLogPath)
	fmt.Printf("  config: %s\n", env["SFERENCE_SWITCH_CONFIG_DIR"])
	fmt.Println("\nlaunchd keeps exactly one instance alive and restarts it at boot.")
	return 0
}

func cmdTLSServiceUninstall(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: sference-switch tls service uninstall")
		return 2
	}
	if err := requireRoot("tls service uninstall"); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	wasLoaded, err := launchd.BootoutDaemon(launchdRunner, launchd.TLSDoorLabel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tls service uninstall: %v\n", err)
		return 1
	}
	if err := launchd.RemoveDaemonPlist(launchd.TLSDoorLabel); err != nil {
		fmt.Fprintf(os.Stderr, "tls service uninstall: %v\n", err)
		return 1
	}
	if wasLoaded {
		fmt.Println("TLS door daemon stopped and removed.")
	} else {
		fmt.Println("TLS door daemon was not loaded; plist removed.")
	}
	return 0
}

func cmdTLSServiceRestart(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: sference-switch tls service restart")
		return 2
	}
	if err := requireRoot("tls service restart"); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	if !launchd.DaemonLoaded(launchdRunner, launchd.TLSDoorLabel) {
		fmt.Fprintln(os.Stderr, "tls service restart: daemon is not installed; run 'sudo sference-switch tls service install'")
		return 1
	}
	if err := launchd.KickstartDaemon(launchdRunner, launchd.TLSDoorLabel); err != nil {
		fmt.Fprintf(os.Stderr, "tls service restart: %v\n", err)
		return 1
	}
	fmt.Println("TLS door restarted.")
	return 0
}

func cmdTLSServiceStatus(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: sference-switch tls service status")
		return 2
	}
	path := launchd.DaemonPlistPath(launchd.TLSDoorLabel)
	_, statErr := os.Stat(path)
	installed := statErr == nil
	loaded := launchd.DaemonLoaded(launchdRunner, launchd.TLSDoorLabel)

	switch {
	case loaded:
		fmt.Printf("tls door: running (launchd %s)\n", launchd.TLSDoorLabel)
	case installed:
		fmt.Printf("tls door: installed but not loaded (%s)\n", path)
	default:
		fmt.Println("tls door: not installed")
	}
	if installed {
		fmt.Printf("  plist:  %s\n", path)
		if bin := launchd.ProgramBinary(path); bin != "" {
			fmt.Printf("  binary: %s\n", bin)
		}
	}
	fmt.Printf("  log:    %s\n", launchd.TLSDoorLogPath)
	if !loaded {
		// `launchctl print` needs root for the system domain; a non-root
		// status read cannot distinguish "not loaded" from "not permitted".
		if os.Geteuid() != 0 {
			fmt.Println("\n(run with sudo for an authoritative loaded/not-loaded answer)")
		}
	}
	return 0
}

func ensureDoorLogDir() error {
	dir := "/var/log/sference-switch"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %v", dir, err)
	}
	return nil
}
