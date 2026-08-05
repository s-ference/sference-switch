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
brew install sference/sference/sference-switch
sference-switch setup
sference-switch up --install
sference-switch claude on
sference-switch doctor --probe
```

The fully qualified Homebrew command adds Sference's public tap and installs
both Sference Switch and its Sference CLI dependency. It requires no GitHub
login, separate `brew tap`, second install command, or local compiler. The beta
release includes a universal macOS artifact for Apple Silicon and Intel.

`setup` verifies that an API key is configured (from `~/.sference/credentials.json`
or `SFERENCE_API_KEY`), and creates the initial configuration. It never
overwrites an existing configuration. `up --install` installs the user launch
agents, starts the local gateway, installs Sference.app in `~/Applications`,
and opens the app. `claude on` connects new Claude Code sessions to the
gateway. The final command checks the complete request path with a small live
request.

## Authentication

Sference Switch uses the same API key as the Sference CLI. Store it once:

```sh
sference-switch auth login --api-key 'sk_...'
```

The key is written to `~/.sference/credentials.json` (shared with `sference`
CLI). The gateway reads it on startup and on SIGHUP, so a new key takes effect
immediately after `sference-switch restart`. Set `SFERENCE_API_KEY` in the
environment to override the file.

If macOS blocks the app's first launch, open **System Settings → Privacy &
Security**, scroll to **Security**, and click **Open Anyway**. This control
appears after a blocked launch attempt. A managed Mac may prohibit the
override.

## Use the Mac app

The menu bar app is the primary interface for daily use. It provides:

- the global Sference routing switch;
- Claude Code and Codex model configuration;
- current health, active fallback, and authentication status;
- recent traffic, performance, and spend views;
- actions to start the system and run it at login.

After an upgrade, `sference-switch up` adopts the new CLI and app version.
Run `sference-switch menubar` when you only need to install, refresh, or reopen
the app from the current Homebrew package.

The app and CLI update the same configuration. You can use either interface
without maintaining separate state.

## Claude Code

`sference-switch claude on` saves the previous Claude Code setting and points
new sessions at Sference Switch. Restart Claude Code after enabling or
disabling the integration.

Useful controls:

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

To restore the Claude Code setting that existed before setup:

```sh
sference-switch claude off
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

Run `sference-switch auth login` if the Sference credential expires. The command
delegates authentication to the Sference CLI, reloads the gateway, and prints
the current identity.

## Upgrade

```sh
brew upgrade sference-switch
sference-switch up
sference-switch doctor
```

`up` leaves healthy current components alone and moves stale components to the
new binary and app. Homebrew remains the canonical public install and upgrade
channel.

## Uninstall

Inspect the exact removal first:

```sh
sference-switch uninstall --dry-run
```

Then remove managed harness settings, processes, launch agents, runtime
residue, and the Mac app:

```sh
sference-switch uninstall
brew uninstall sference-switch
```

The default uninstall retains configuration, telemetry, logs, and backups.
To remove those files as well, use this instead of the standard uninstall:

```sh
sference-switch uninstall --purge --yes
brew uninstall sference-switch
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

Homebrew is the supported path for the complete macOS product.

## License

Sference Switch is available under the [MIT License](LICENSE).
