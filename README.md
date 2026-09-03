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

Then open the app and do three things — no terminal commands needed:

1. **Sign in** with your Sference account. The app shows the device-flow
   verification code and opens the approval page (Account card → Sign In).
2. **Turn routing on** with the **Switch** toggle. The bundle's first toggle
   asks for your macOS password once (it installs the root TLS service and
   edits `/etc/hosts`), then routes any running Claude Code on the machine.
3. **Pick a model.** In Claude Code, `/model` lists `[Sference]` entries.
   Choose the `… (1M context)` variant of a 1M model to use its full window.

The CLI covers the same ground for scripting and headless machines — every
step above has a command equivalent that the app calls internally when you
toggle:

```sh
sference-switch auth login          # or: auth login --api-key 'sk_...'
sference-switch up                 # start the router
sference-switch setup              # install & bootstrap the root TLS door
sudo sference-switch intercept on  # write the /etc/hosts redirect
```

The app is the primary interface; you do not need to run these by hand.

## Authentication

Sign in in the app (Account card → Sign In), which runs the browser device
flow: the app shows a short verification code, opens the approval page, and
waits for approval. The resulting grant (24 h access token + 30 d rotating
refresh token) is written to `~/.sference/switch/credentials.json` — the
switch's own file, separate from the `sference` CLI's — and the gateway
refreshes it automatically. The app surfaces "reauthentication required" when
the credential expires.

The same login from a terminal (for scripting or headless setups):

```sh
sference-switch auth login
```

A static API key also works and never expires:

```sh
sference-switch auth login --api-key 'sk_...'
```

If the `sference` CLI is signed in (`sference auth login`), the switch can
also read that credential from `~/.sference/credentials.json` as a fallback.
The gateway reads credentials on startup and on SIGHUP, so a new login takes
effect immediately. Set `SFERENCE_API_KEY` in the environment to override all
files.

If macOS blocks the app's first launch, open **System Settings → Privacy &
Security**, scroll to **Security**, and click **Open Anyway**. This control
appears after a blocked launch attempt. A managed Mac may prohibit the
override.

## Transparent interception

Sference Switch is installed as a root service so that *any* Claude Code
session on the machine is routed without user intervention and without
changing Claude Code's configuration.

At startup the TLS door (a launch daemon owned by `root`) binds loopback
port 443. `intercept on` writes a guarded block to `/etc/hosts`:

```text
# sference-switch begin
127.0.0.1 api.anthropic.com
::1 api.anthropic.com
# sference-switch end
```

The door presents a leaf certificate for `api.anthropic.com` minted from a
Sference Switch local CA, which `tls install` trusts in the macOS System
keychain. Only the DNS name resolution is redirected: the request still says
it is going to `https://api.anthropic.com`, so Claude Code cannot distinguish
the switch from Anthropic's real edge.

Inside the door, requests are partitioned by path:

- **Inference** (`/v1/messages`, `/v1/messages/count_tokens`) and model
  discovery (`/v1/models`) go to the local Sference router.
- **Bootstrap** (`/api/claude_cli/bootstrap`) is fetched from the real
  Anthropic and post-processed to inject `[Sference]` models into the picker.
- **Everything else** — OAuth, feature flags, telemetry, MCP registry —
  passes through to the real `api.anthropic.com` unmodified.

Because of this allowlist, Claude Code keeps full first-party behaviour:
your Anthropic sign-in and OAuth work, feature flags load, and telemetry is
reported normally. The switch never sees the control-plane traffic except to
proxy it.

### DNS bypass

The door and router resolve their own outbound DNS via a raw A-record query
to `1.1.1.1`, so their calls to `api.anthropic.com` do not loop back into the
door through the `/etc/hosts` entry. This is what keeps passthrough traffic
from recursing into the proxy.

### Turning it off

```sh
sudo sference-switch intercept off   # remove the /etc/hosts block
sudo sference-switch tls service uninstall   # stop and unload the :443 daemon
```

`intercept off` restores `api.anthropic.com` to its real DNS result, so
Claude Code returns to talking directly to Anthropic.

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

Because interception happens at the network layer (see
[Transparent interception](#transparent-interception)), Claude Code needs no
configuration and no restart when you toggle routing. Sference models appear
in Claude Code's `/model` picker with a `[Sference]` prefix; run `/model` and
choose one, or keep a native Claude model for requests that pass through.

The picker is populated by injecting Sference models into Claude Code's
bootstrap response. It is on by default and can be toggled from the menu bar
app ("Include Sference in /model", Model Routing → Claude Code) or from the
CLI:

```sh
sference-switch config set picker_inject true|false
```

Turning it off removes `[Sference]` models from `/model`; model routing for
the families is unaffected.

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
sference-switch off                       # stop routing; native passthrough
sudo sference-switch intercept off        # remove the /etc/hosts block
sudo sference-switch tls service uninstall  # stop the :443 door daemon
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
~/.sference/switch/logs/tlsdoor-bootstrap.log
```

`router.log` is the Sference router; `door.log` is the plain loopback door
that Claude Code's `ANTHROPIC_BASE_URL` used before transparent interception.
`tlsdoor-bootstrap.log` traces the TLS door's model-picker injection — it is
user-readable so the interception path is debuggable without `sudo`, even
though the :443 door itself runs as `root`. The root door's own stderr is
`/var/log/sference-switch/tlsdoor.log`.

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

The `:443` TLS door runs as a separate root daemon and keeps its old binary
until it is restarted:

```sh
sudo launchctl kickstart -k system/co.sference.switch.tlsdoor
```

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
