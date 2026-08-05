package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/sference/sference-switch/gateway/internal/auth"
	"github.com/sference/sference-switch/gateway/internal/config"
)

var errSetupCredentialUnavailable = errors.New("current Sference credential is missing or unusable")

type setupDependencies struct {
	loadCredential func() (string, error)
	configPath     func() string
	stat           func(string) (os.FileInfo, error)
	initConfig     func(string, bool, io.Writer) int
}

func defaultSetupDependencies() setupDependencies {
	return setupDependencies{
		loadCredential: loadCurrentSetupCredential,
		configPath: func() string {
			return envDefault("SFERENCE_SWITCH_CONFIG_PATH", config.DefaultPath())
		},
		stat:       os.Stat,
		initConfig: runConfigInit,
	}
}

func cmdSetup(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "usage: sference-switch setup")
		return 2
	}
	return runSetup(defaultSetupDependencies(), os.Stdout, os.Stderr)
}

func runSetup(deps setupDependencies, out, errOut io.Writer) int {
	credential, err := deps.loadCredential()
	if err != nil {
		fmt.Fprintf(out, "Sference credential: unavailable (%v)\n", err)
		fmt.Fprintln(out, "Run: sference-switch auth login --api-key sk_...")
		fmt.Fprintln(out, "Create a key in Console → API Keys, then run the command above.")
		return 1
	}
	fmt.Fprintf(out, "Sference credential: %s\n", credential)

	path := deps.configPath()
	if _, err := deps.stat(path); err == nil {
		fmt.Fprintf(out, "Gateway config: using existing %s\n", path)
	} else if !os.IsNotExist(err) {
		fmt.Fprintf(errOut, "setup: inspect gateway config %s: %v\n", path, err)
		return 1
	} else {
		if code := deps.initConfig(path, false, io.Discard); code != 0 {
			fmt.Fprintf(errOut, "setup: could not initialize gateway config at %s\n", path)
			return 1
		}
		fmt.Fprintf(out, "Gateway config: created %s\n", path)
	}

	fmt.Fprintln(out, "\nNext commands:")
	fmt.Fprintln(out, "sference-switch up --install")
	fmt.Fprintln(out, "sference-switch claude on")
	fmt.Fprintln(out, "sference-switch doctor --probe")
	return 0
}

func loadCurrentSetupCredential() (string, error) {
	token, _, err := auth.Load("")
	if err != nil {
		return "", fmt.Errorf("%w: %v", errSetupCredentialUnavailable, err)
	}
	if token == nil || token.AccessToken == "" {
		return "", errSetupCredentialUnavailable
	}
	return "current API key is available", nil
}
