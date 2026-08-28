#!/bin/sh
# Install an ad-hoc signed Sference Switch beta release payload on macOS.
# The curl installer at https://get.sference.com is the canonical public
# installation path and delegates to this script. It is also run directly
# for release-asset verification and approved direct installs.
set -eu
cd "$(dirname "$0")"

fail() {
    echo "install.sh: $*" >&2
    exit 1
}

BIN_DIR="${SFERENCE_SWITCH_BIN_DIR:-$HOME/.local/bin}"
APP_DIR="$HOME/Applications"
APP_ZIP="Sference Switch.app.zip"
APP_NAME="Sference Switch.app"

[ "$(uname -s)" = "Darwin" ] || fail "requires macOS"
[ -x bin/sference-switch ] || fail "missing executable bin/sference-switch"
[ -f "$APP_ZIP" ] || fail "missing nested app payload '$APP_ZIP'"

mkdir -p "$APP_DIR"
STAGE="$(mktemp -d "$APP_DIR/.sference-switch-install.XXXXXX")"
trap 'rm -rf "$STAGE"' EXIT HUP INT TERM
/usr/bin/ditto -x -k "$APP_ZIP" "$STAGE"
APP="$STAGE/$APP_NAME"
PLIST="$APP/Contents/Info.plist"
APP_BIN="$APP/Contents/MacOS/SferenceSwitch"

[ -d "$APP" ] || fail "nested app ZIP did not contain '$APP_NAME'"
[ -x "$APP_BIN" ] || fail "app executable is missing"
[ "$(/usr/bin/plutil -extract CFBundleIdentifier raw "$PLIST")" = "co.sference.switch" ] \
    || fail "unexpected app bundle identifier"
[ "$(/usr/bin/plutil -extract CFBundleDisplayName raw "$PLIST")" = "Sference Switch" ] \
    || fail "unexpected app display name"
[ "$(/usr/bin/plutil -extract CFBundleExecutable raw "$PLIST")" = "SferenceSwitch" ] \
    || fail "unexpected app executable name"

MARKETING_VERSION="$(/usr/bin/plutil -extract CFBundleShortVersionString raw "$PLIST")"
BUILD_NUMBER="$(/usr/bin/plutil -extract CFBundleVersion raw "$PLIST")"
printf '%s\n' "$MARKETING_VERSION" | grep -Eq '^[0-9]+(\.[0-9]+){1,2}$' \
    || fail "app marketing version is not numeric: $MARKETING_VERSION"
printf '%s\n' "$BUILD_NUMBER" | grep -Eq '^[0-9]+(\.[0-9]+)*$' \
    || fail "app build number is not numeric: $BUILD_NUMBER"

CLI_VERSION="$(bin/sference-switch --version)"
case "$CLI_VERSION" in
    "sference-switch v$MARKETING_VERSION") ;;
    *) fail "CLI version '$CLI_VERSION' does not match app version '$MARKETING_VERSION'" ;;
esac

for binary in bin/sference-switch "$APP_BIN"; do
    ARCHS="$(/usr/bin/lipo -archs "$binary")"
    case " $ARCHS " in *" arm64 "*) ;; *) fail "$binary is missing arm64" ;; esac
    case " $ARCHS " in *" x86_64 "*) ;; *) fail "$binary is missing x86_64" ;; esac
done

/usr/bin/codesign --verify --strict --verbose=2 bin/sference-switch \
    || fail "CLI ad-hoc signature is invalid"
/usr/bin/codesign --verify --deep --strict --verbose=2 "$APP" \
    || fail "app ad-hoc signature is invalid"
CLI_SIGNATURE="$(/usr/bin/codesign --display --verbose=4 bin/sference-switch 2>&1)"
APP_SIGNATURE="$(/usr/bin/codesign --display --verbose=4 "$APP" 2>&1)"
printf '%s\n' "$CLI_SIGNATURE" | grep -qxF 'Signature=adhoc' \
    || fail "CLI does not have the required ad-hoc beta signature"
printf '%s\n' "$APP_SIGNATURE" | grep -qxF 'Signature=adhoc' \
    || fail "app does not have the required ad-hoc beta signature"

mkdir -p "$BIN_DIR"
/usr/bin/install -m 0755 bin/sference-switch "$BIN_DIR/sference-switch"
echo "installed $BIN_DIR/sference-switch ($CLI_VERSION)"

TARGET="$APP_DIR/$APP_NAME"
BACKUP="$APP_DIR/$APP_NAME.previous"
[ ! -e "$BACKUP" ] \
    || fail "previous app backup already exists at '$BACKUP'; verify or remove it before retrying"
if [ -e "$TARGET" ]; then
    /bin/mv "$TARGET" "$BACKUP"
fi
if ! /bin/mv "$APP" "$TARGET"; then
    [ ! -e "$BACKUP" ] || /bin/mv "$BACKUP" "$TARGET"
    fail "could not activate the new app"
fi
echo "installed $TARGET"
if [ -e "$BACKUP" ]; then
    echo "previous app retained at $BACKUP until you verify the new version"
fi

case ":$PATH:" in
    *":$BIN_DIR:"*) ;;
    *)
        echo ""
        echo "NOTE: $BIN_DIR is not on PATH. Add it, then reopen Terminal:"
        echo "  echo 'export PATH=\"$BIN_DIR:\$PATH\"' >> ~/.zshrc"
        ;;
esac

cat <<'EOF'

Beta notice:

  This build is ad-hoc signed and is not Apple-notarized. On first launch,
  macOS may require you to try opening Sference Switch once, then go to
  System Settings > Privacy & Security and click Open Anyway.

Quick start:

  sference-switch setup
  sference-switch up --install
  sference-switch claude on
  sference-switch doctor --probe

Upgrade later with:

  sference-switch upgrade --restart

or by re-running the canonical installer:

  curl -fsSL https://get.sference.com | sh

Sference Switch runs locally. Request bodies leave the machine only for
the model provider selected by routing policy.
EOF
