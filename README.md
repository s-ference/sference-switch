# Sference Switch

Sference Switch is a local macOS app and gateway that routes supported AI
coding harnesses between their native providers and models served on Sference.
One global switch controls Sference routing, and per-client mappings select the
model that serves each request.

> **Beta:** Sference Switch is under active development. Interfaces,
> configuration, and behavior may change between releases. The current macOS
> build is ad hoc signed and not yet notarized by Apple, so first launch may
> require approval under System Settings → Privacy & Security.

The first public release supports macOS 13 or newer on Apple Silicon and Intel.

## Quick start

```sh
curl -fsSL https://get.sference.com | sh
```

This downloads the latest release, verifies the checksum, and installs the CLI
to `~/.local/bin` and the menubar app to `~/Applications`. No Homebrew required.

Then open the app for the first two steps; the third is one terminal command:

1. **Sign in** with your Sference account. The app shows the device-flow
   verification code and opens the approval page (Account card → Sign In).
2. **Turn routing on** with the **Switch** toggle. It flips the global routing
   switch: off is native passthrough, on applies your saved model mappings.
3. **Connect Claude Code** by running `sference-switch claude on` once (see
   [How Claude Code is connected](#how-claude-code-is-connected)). Then
   `/model` lists `[Sference]` entries — choose the `… (1M context)` variant
   of a 1M model to use its full window.

The app is the primary interface; the equivalent terminal commands, for
scripting and headless machines, are collected in the
[command-line reference](#command-line-reference).

## Authentication

Sign in in the app (Account card → Sign In), which runs the browser device
flow: the app shows a short verification code, opens the approval page, and
waits for approval. The resulting grant (24 h access token + 30 d rotating
refresh token) is written to `~/.sference/switch/credentials.json` — the
switch's own file, separate from the `sference` CLI's — and the gateway
refreshes it automatically. The app surfaces "reauthentication required" when
the credential expires.

A static API key also works and never expires — see
[Command-line reference](#command-line-reference).

If the `sference` CLI is signed in (`sference auth login`), the switch can
also read that credential from `~/.sference/credentials.json` as a fallback.
The gateway reads credentials on startup and on SIGHUP, so a new login takes
effect immediately. Set `SFERENCE_API_KEY` in the environment to override all
files.

If macOS blocks the app's first launch, open **System Settings → Privacy &
Security**, scroll to **Security**, and click **Open Anyway**. This control
appears after a blocked launch attempt. A managed Mac may prohibit the
override.

## How Claude Code is connected

The switch points Claude Code at the local gateway through
`ANTHROPIC_BASE_URL` in `~/.claude/settings.json`:

```sh
sference-switch claude on    # wire it up
sference-switch claude off   # restore the previous settings
```

`claude on` writes the base URL plus
`CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1`, and primes the picker's
discovery cache (`~/.claude/cache/gateway-models.json`) from the gateway's own
`/v1/models`. That is what makes the `[Sference]` entries appear in `/model`.

No credential is installed: gateway model discovery skips its live fetch when
no token-style credential resolves (an OAuth sign-in alone does not count), but
Claude Code primes the picker from the cache regardless — while an
`apiKeyHelper` or API key would shadow the claude.ai login and disable your
organization's claude.ai connectors.

`claude off` restores the settings exactly when possible, or strips only
gateway-owned values after drift, and removes the primed cache.

### Model windows

Claude Code assumes any model id it does not recognize holds 200k tokens. A
1M-context Sference model is therefore published only under its `[1m]` id
(`claude-sference-moonshotai-kimi-k3[1m]`), which Claude Code reads as a 1M
model; the bare id keeps routing for existing sessions but is hidden from the
picker. Pick the `[1m]` entry in `/model` to use the full window.

## Use the Mac app

The menu bar app is the primary interface for daily use. It provides:

- the global Sference routing switch;
- Claude Code and Codex model configuration;
- current health, active fallback, and authentication status;
- recent traffic, performance, and spend views;
- actions to start the system and run it at login.

After an upgrade, the app's **Update & Restart** action adopts the new CLI and
app version and restarts the system (with one macOS password prompt to
kick the root TLS door). `sference-switch up` is the equivalent from a
terminal; run `sference-switch menubar` when you only need to install, refresh,
or reopen the app from the currently installed package.

The app and CLI update the same configuration. You can use either interface
without maintaining separate state.

## Claude Code

Run `sference-switch claude on` once (see
[How Claude Code is connected](#how-claude-code-is-connected)); Sference models
then appear in Claude Code's `/model` picker with a `[Sference]` prefix. Run
`/model` and choose one, or keep a native Claude model for requests that pass
through.

Useful routing controls:

```sh
sference-switch on
sference-switch off
sference-switch claude status
sference-switch claude route
sference-switch claude route sonnet zai-org/GLM-5.2
sference-switch claude route sonnet native
sference-switch claude subagents zai-org/GLM-5.2
```

`on` and `off` change one global routing switch. Saved model mappings remain
editable while routing is off. A Claude family can map to `native`, a
configured alias, or a Sference model slug. Run
`sference-switch claude route <family> default` to remove a family override.

To stop all routing through Sference and return to direct Anthropic:

```sh
sference-switch off          # stop routing; native passthrough
sference-switch claude off   # restore Claude Code's settings
```

## Codex CLI

Codex support is opt-in. Install
[Codex CLI](https://github.com/openai/codex), start Sference Switch,
and create its managed profile:

```sh
sference-switch codex on
sference-switch codex route zai-org/GLM-5.2
sference-switch codex status
codex --profile sference
```

The first `codex on` may request permission to enable the parked Codex
listener. Sference Switch writes `~/.codex/sference.config.toml`; it does not
modify `~/.codex/config.toml`. Start Codex without `--profile sference` to use
native OpenAI routing.

Remove the managed profile and restore any file it replaced:

```sh
sference-switch codex off
```

Do not override the managed profile's compatibility model with `-m`. Select
the upstream Sference model with `sference-switch codex route` instead.

## Status and troubleshooting

```sh
sference-switch status
sference-switch status --verbose
sference-switch doctor
sference-switch doctor --probe
```

`status` summarizes the router, front door, Mac app, authentication, and
client routing state. `doctor` inspects the same path without changing it and
prints the first failure with a concrete fix. `doctor --fix` can apply
supported repairs after confirmation.

Sference Switch stores configuration, local state, logs, and telemetry under
`~/.sference/switch/`. The primary logs are:

```text
~/.sference/switch/logs/router.log
~/.sference/switch/logs/door.log
```

`router.log` is the Sference router; `door.log` is the loopback door that
Claude Code's `ANTHROPIC_BASE_URL` points at.

Run `sference-switch auth login` if the Sference credential expires or the
grant is revoked. The command runs the browser device flow, reloads the
gateway, and prints the current identity.

## Upgrade

```sh
sference-switch upgrade --restart
sference-switch doctor
```

`upgrade` fetches the latest release manifest from `get.sference.com`, verifies
the SHA-256 checksum, and replaces the CLI and menubar app in place. No sudo is
needed. Pass `--check` to report whether an update is available without
installing one, or `--cli-only` to leave the app alone.

Re-running `curl -fsSL https://get.sference.com | sh` is equivalent to
`upgrade` and also works.

## Uninstall

Inspect the exact removal first:

```sh
sference-switch uninstall --dry-run
```

Then remove managed harness settings, processes, launch agents, runtime
residue, and the Mac app:

```sh
sference-switch uninstall
```

The default uninstall retains configuration, telemetry, logs, and backups.
To remove those files as well, use this instead of the standard uninstall:

```sh
sference-switch uninstall --purge --yes
```

Uninstall never removes Sference CLI credentials or keychain entries. If the
app's Start at Login item prevents safe bundle removal, the command prints the
manual action required in macOS System Settings.

## Privacy and trust

Sference Switch binds its services to the local loopback interface. It receives
harness requests and credentials because it is in the selected request path.
Request content leaves the machine only for the upstream chosen by the active
routing policy. Sference credentials go only to Sference, and native credentials
go only to their matching native provider.

Local telemetry contains request metadata, not prompts, responses,
credentials, headers, or request bodies. Disable future records by setting
`telemetry_enabled: false` in `gateway.yaml`, then reload the configuration.
Delete existing records from
`~/.sference/switch/telemetry/`.

Public model-catalog refreshes from models.dev send no credential or
user-derived data. The unauthenticated administration API binds to loopback
and must never be exposed on a network interface.

## Configuration

The generated configuration is
`~/.sference/switch/gateway.yaml`. Prefer the Mac app or typed CLI
commands over direct edits; routing changes hot-reload without a restart.

See [config/schema.md](config/schema.md) for every field and
[config/gateway.example.yaml](config/gateway.example.yaml) for the generated
shape. Store API-key overrides in
`~/.sference/switch/env`, which must use mode `0600`, rather than in
`gateway.yaml`.

## Command-line reference

The app is the primary interface and drives every command below when you use
its controls. These are the same steps for scripting, headless machines, and
diagnostics.

**Sign in** (the app's Account card runs this):

```sh
sference-switch auth login                 # browser device flow
sference-switch auth login --api-key 'sk_...'   # static API key, never expires
```

**Set up and start**:

```sh
sference-switch setup                      # check prerequisites, sign in, init config
sference-switch up                         # start the router and door
sference-switch claude on                  # point Claude Code at the gateway
```

**Route and inspect:**

```sh
sference-switch on
sference-switch off
sference-switch claude status
sference-switch doctor --probe
```

Other useful commands appear in the [Claude Code](#claude-code),
[Codex CLI](#codex-cli), [Status and troubleshooting](#status-and-troubleshooting),
[Upgrade](#upgrade), and [Uninstall](#uninstall) sections.

## Build and test

The Go module in `gateway/` builds the CLI, router, administration API, and
front door. The Swift package in `mac/SferenceSwitch/` builds the native menu
bar app without external Swift packages.

```sh
scripts/build.sh
scripts/check.sh
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for contribution and dependency rules.
See [TESTING.md](TESTING.md) for test layers and isolation requirements.

## Nix source build

The Nix flake is an alternate source build for Linux and Apple Silicon macOS.
It does not include the signed Mac app or install the Sference CLI dependency:

```sh
nix profile install github:sference/sference-switch#sference-switch
```

Upgrade and restart the locally running components:

```sh
nix profile upgrade --refresh sference-switch
sference-switch up
```

The curl installer is the supported path for the complete macOS product.

## License

Sference Switch is available under the [MIT License](LICENSE).
