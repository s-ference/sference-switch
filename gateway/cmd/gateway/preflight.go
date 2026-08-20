package gateway

// Preflight checks run at startup and on SIGHUP reload. They are warn-only:
// they log actionable lines and never stop the gateway.

import (
	"fmt"
	"io"
	"os"

	"github.com/sference/sference-switch/gateway/internal/auth"
	"github.com/sference/sference-switch/gateway/internal/config"
)

// preflightConfigPath mirrors Gateway.activeConfigPath for a bare Config.
func preflightConfigPath(cfg *Config) string {
	if cfg.ConfigPath != "" {
		return cfg.ConfigPath
	}
	return config.DefaultPath()
}

// applyGlobalAuth loads gateway.yaml and applies global.auth to the
// process environment exactly like the admin config PUT path
// (applyConfigEnv), then refreshes the key fields on cfg from the
// environment. This keeps startup, SIGHUP, and admin updates consistent.
func applyGlobalAuth(cfg *Config) {
	f, err := config.Load(preflightConfigPath(cfg))
	if err != nil {
		return
	}
	applyConfigEnv(f)
	if v := os.Getenv("SFERENCE_API_KEY"); v != "" {
		cfg.SferenceKey = v
	}
}

// UnresolvedPlaceholders returns the ${VAR} placeholder names referenced
// by the config that are not set in the process environment. Exported
// for `sference-switch doctor`, which runs the same detection out of process
// (and additionally consults the gateway's env file, which the gateway
// itself loads into its environment before this check runs).
func UnresolvedPlaceholders(f *config.File) []string {
	var out []string
	for _, name := range f.CollectPlaceholders() {
		if os.Getenv(name) == "" {
			out = append(out, name)
		}
	}
	return out
}

// warnUnresolvedPlaceholders logs one actionable line per unresolved
// ${VAR}, naming the variable and where to set it.
func warnUnresolvedPlaceholders(f *config.File, out io.Writer) {
	for _, name := range UnresolvedPlaceholders(f) {
		fmt.Fprintf(out,
			"[gateway] warning: gateway.yaml references ${%s} but %s is not set; it expands to empty. Fix: add %s=... to %s or export it in the gateway's environment\n",
			name, name, name, config.EnvFilePath())
	}
}

// sferenceRoutedClients returns a display entry per enabled client whose
// route or fallback_route is sference. Passthrough routes (anthropic,
// openai) are excluded: they use harness credential passthrough and need
// no gateway-side key.
func sferenceRoutedClients(resolved []resolvedClientConfig) []string {
	var out []string
	for _, rc := range resolved {
		switch {
		case rc.Route == "sference":
			out = append(out, rc.Name+" (route: sference)")
		case rc.FallbackRoute == "sference":
			out = append(out, rc.Name+" (fallback_route: sference)")
		}
	}
	return out
}

// hasSferenceCredential reports whether an API key is available from the
// shared credentials file or the SFERENCE_API_KEY environment variable.
func hasSferenceCredential(profile, apiKey string) bool {
	if apiKey != "" {
		return true
	}
	tok, _, _ := auth.Load(profile)
	return tok != nil
}

// warnMissingSferenceCreds prints a prominent banner naming the affected
// clients and the fix. Warn-only.
func warnMissingSferenceCreds(names []string, out io.Writer) {
	if len(names) == 0 {
		return
	}
	rule := "[gateway] =============================================================="
	fmt.Fprintln(out, rule)
	fmt.Fprintln(out, "[gateway] WARNING: no Sference credential found (no OAuth login and no")
	fmt.Fprintln(out, "[gateway] API key). These clients route to sference and their requests")
	fmt.Fprintln(out, "[gateway] will fail until a credential is configured:")
	for _, n := range names {
		fmt.Fprintf(out, "[gateway]   - %s\n", n)
	}
	fmt.Fprintln(out, "[gateway] Fix: run 'sference-switch auth login', or set SFERENCE_API_KEY in")
	fmt.Fprintf(out, "[gateway] %s (or via global.auth.sference in gateway.yaml).\n", config.EnvFilePath())
	fmt.Fprintln(out, rule)
}

// runPreflight runs the warn-only checks: unresolved ${VAR} placeholders
// against the process environment, and sference-routed clients without a
// usable credential. Never fatal.
func runPreflight(cfg *Config, resolved []resolvedClientConfig, out io.Writer) {
	if f, err := config.Load(preflightConfigPath(cfg)); err == nil {
		warnUnresolvedPlaceholders(f, out)
	}
	names := sferenceRoutedClients(resolved)
	if len(names) == 0 {
		return
	}
	if hasSferenceCredential(cfg.OAuthProfile, cfg.SferenceKey) {
		return
	}
	warnMissingSferenceCreds(names, out)
}
