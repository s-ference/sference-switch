# Install Sference Switch on macOS

> **Beta:** Sference Switch is currently a public beta. The CLI and app are
> ad-hoc signed but are not Apple-notarized. macOS may require explicit
> approval before the app opens for the first time.

Homebrew is the canonical public install and upgrade channel. This one
command installs the Sference CLI and Sference Switch from Sference's public tap:

```sh
brew install sference/sference/sference-switch
```

Then authenticate and start the gateway:

```sh
sference-switch setup
sference-switch up --install
sference-switch claude on
sference-switch doctor --probe
```

## Direct release asset

Approved direct installs use
`sference-switch_<version>_darwin_universal.zip`. The release also publishes
`checksums.txt`. Verify the ZIP's SHA-256 entry before extracting it.

The release ZIP contains the universal, ad-hoc signed CLI, a nested ad-hoc signed
`Sference Switch.app.zip`, and `install.sh`. Run:

```sh
unzip sference-switch_<version>_darwin_universal.zip \
  -d sference-switch_<version>
cd sference-switch_<version>
./install.sh
```

The installer validates the ad-hoc signatures, matching versions, and universal
architectures. An ad-hoc signature detects changes after packaging, but it does
not establish an Apple-verified developer identity or notarization.

## First launch approval

If macOS blocks the beta app on first launch:

1. In Finder, open `~/Applications`, double-click **Sference Switch**, then
   dismiss the warning.
2. Choose **Apple menu > System Settings > Privacy & Security**.
3. Scroll to the **Security** section and click **Open Anyway** for Sference
   Switch.
4. Confirm **Open** and authenticate if macOS asks.

The **Open Anyway** button is available for about one hour after the blocked
launch attempt. A managed Mac may prevent this override. Sference Switch does
not ask users to clear quarantine metadata or disable Gatekeeper.

## Uninstall

```sh
sference-switch uninstall --dry-run
sference-switch uninstall
```

Use `sference-switch uninstall --purge --yes` to remove retained Sference Switch
config, telemetry, logs, and backups. Sference CLI credentials and keychain
entries are never removed. The command prints manual instructions when the
macOS app's Start at Login item prevents safe automated bundle removal.

The repository README contains the supported install, operation,
troubleshooting, and uninstall instructions.
