# Testing

How Sference Switch is tested and how to run each layer.

## Strategy

Testing runs in three layers, ordered from cheapest to most realistic:

1. **Unit and integration tests** (Go and Swift): hermetic, run on every change.
2. **The `check.sh` gate**: build, vet, full test suite, plus live smoke tests
   on scratch ports. This is the required gate for every change set.
3. **Fresh-install simulation** (`tests/fresh-install/`): a clean Debian
   container exercises the source-build lifecycle and verifies the full
   install-to-first-request path.

## Layer 1: unit and integration tests

### Go

```sh
cd gateway && go test ./...
```

| Package | Covers |
|---|---|
| `cmd/sference-switch` | CLI surface, clean config reset, global mutation CAS/journal/reconciliation, Claude settings adapter, and diagnostics |
| `cmd/gateway` | Routing engine, global gate and resolver status, fallback waterfall, upstream discovery, subagent gate, TTFT timing, reload behavior, and admin security |
| `internal/*` | Analytics aggregation and indexing, config editing, OAuth and credential isolation, door circuit breaker, pidfiles, sanitization, telemetry, protocol translation, and usage accounting |

The only production package with no tests is `internal/version`.

### Swift (menubar app)

Tests under `mac/SferenceSwitch/Tests/SferenceSwitchTests/` cover
`DisplayTests`, `DoorStatusTests`, `LoginItemTests`, `PopupDisplayTests`,
`RuntimeCoordinationTests`, `StatsTests`, and `TrafficTests`. Run with:

```sh
cd mac/SferenceSwitch && swift test
```

These cover display formatting, stats parsing, login-item policy,
isolated runtime environments, runtime-trust
interlocks, binary lookup, icon sizing, window-presentation state, and
routing mutation tokens, receipts, timeouts, and reconciliation.
Traffic tests also cover analytics decoding, range requests, stale replay,
request coalescing, and navigation. Nothing drives live AppKit behavior
(status item, native menu, window controls, or route-flip shell-outs).

Release validation also covers the packaged app, accessibility, and
performance.

## Layer 2: the `check.sh` gate

```sh
scripts/check.sh            # full gate
scripts/check.sh --offline  # skips only the network smoke (step 5)
```

Required before calling any change set done, including work delegated to
agents. Steps, in order:

1. `gofmt` on changed files (ratchet: ignores pre-existing drift).
2. `go build ./...`, `go vet ./...`, `go test ./...`.
3. Build the `sference-switch` binary.
4. Verify the scratch ports are free. Defaults are 28081-28083, 28182-28183,
   and 28786-28787; `SFERENCE_SWITCH_CHECK_*_PORT` variables override them for parallel
   worktrees.
5. Smoke: `whoami --refresh` against the configured credential store, forcing
   a live `/v1/users/me` call. Skipped by `--offline` or when no local auth
   exists.
6. Smoke: throwaway gateway boot on scratch ports; asserts `/healthz` and two
   expected preflight warnings.
7. Smoke: throwaway door boot against a dead router; asserts `/doorz` reports
   tripped.
8. Smoke: `up`, `status`, idempotent `up`, `down`, `status` round trip on
   scratch ports with `SFERENCE_SWITCH_LAUNCHD=off` and a fully sandboxed env. Never
   touches the live gateway.
All smoke steps isolate themselves with the env seams listed below; the gate
never edits `~/.sference/switch` or touches a running production gateway.

## Layer 3: fresh-install simulation (`tests/fresh-install/`)

A clean Debian container installs the binary, writes config and a 0600 env
file, runs `sference-switch up`, then verifies that a real request through the
front door returns 200 from Sference and that telemetry records the request.

```sh
cd tests/fresh-install
SFERENCE_API_KEY=... ./run.sh    # keyed mode: real routed request
./run.sh --no-key               # keyless: asserts 503 needs-login, then a
                                # SIGHUP route flip to a local stub
```

The API key enters only via `docker run -e`, never a image layer. Not
simulated: macOS keychain OAuth, the menubar app, Gatekeeper/notarization,
and real launchd installs.

## Writing tests: isolation seams

Production code honors env overrides so tests and scratch instances never
touch real state. Any test that boots a component must set the relevant ones:

| Seam | Purpose |
|---|---|
| `SFERENCE_SWITCH_CONFIG_PATH` | config file location |
| `SFERENCE_SWITCH_ADMIN_ADDR` | admin API address |
| `SFERENCE_SWITCH_CLAUDE_SETTINGS`, `SFERENCE_SWITCH_BACKUP_DIR` | Claude settings.json adapter targets |
| `SFERENCE_SWITCH_LAUNCHD=off` | disable launchd supervision entirely |
| `SFERENCE_SWITCH_GATEWAY_PIDFILE`, `SFERENCE_SWITCH_DOOR_PIDFILE` | pidfile locations |
| `SFERENCE_SWITCH_GATEWAY_LOG`, `SFERENCE_SWITCH_DOOR_LOG` | process log locations |
| `SFERENCE_SWITCH_TELEMETRY_DIR` | segmented telemetry store location |
| `SFERENCE_SWITCH_OAUTH_PROFILE` | named Sference CLI profile; unset follows the CLI's current profile (point at a nonexistent name to run credential-less) |
| `SFERENCE_SWITCH_MENUBAR_APP` | menubar bundle path override |

Hard rules for contributors and agents:

- Never point a test at the real `~/.claude/settings.json` or
  `~/.sference/switch`; always go through the seams above.
- Never restart or kill a running production gateway from a test.
- Gate every change set with `scripts/check.sh`. `go test ./...` alone does
  not exercise the live refresh path.
- New scripts and flows must be executed at least once (or have a `--dry-run`
  wired into `check.sh`) before being handed to a user.
