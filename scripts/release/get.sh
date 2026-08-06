#!/bin/sh
# Curl-to-shell installer for Sference Switch on macOS.
#
# Downloads the latest release from get.sference.com, verifies the SHA-256
# checksum, extracts the ZIP, and delegates to the bundled install.sh (which
# verifies ad-hoc signatures, bundle identity, version match, and universal
# architectures before installing).
#
# Usage:
#   curl -fsSL https://get.sference.com | sh
#   curl -fsSL https://get.sference.com | sh -s -- --bin-dir ~/bin
#   curl -fsSL https://get.sference.com | sh -s -- --version 0.2.0
#   curl -fsSL https://get.sference.com | sh -s -- --dry-run
#
# Environment overrides (flags win over env):
#   SFERENCE_SWITCH_BASE_URL    CDN base URL (default: https://get.sference.com)
#   SFERENCE_SWITCH_CHANNEL     Release channel (default: stable)
#   SFERENCE_SWITCH_VERSION     Pin to exact version (skips manifest fetch)
#   SFERENCE_SWITCH_BIN_DIR     CLI install directory (default: ~/.local/bin)
#
# Structural safety for curl|sh: the entire body is wrapped in main() and
# invoked at the end, so a truncated download dies at parse time instead of
# half-executing. Never reads stdin (stdin is the pipe under curl|sh).
set -eu

fail() {
    echo "get.sference.com: $*" >&2
    exit 1
}

main() {
    base_url="https://get.sference.com"
    channel="stable"
    version=""
    bin_dir="$HOME/.local/bin"
    cli_only=false
    dry_run=false

    # Flags override env, env overrides defaults.
    [ -n "${SFERENCE_SWITCH_BASE_URL:-}" ] && base_url="$SFERENCE_SWITCH_BASE_URL"
    [ -n "${SFERENCE_SWITCH_CHANNEL:-}" ] && channel="$SFERENCE_SWITCH_CHANNEL"
    [ -n "${SFERENCE_SWITCH_VERSION:-}" ] && version="$SFERENCE_SWITCH_VERSION"
    [ -n "${SFERENCE_SWITCH_BIN_DIR:-}" ] && bin_dir="$SFERENCE_SWITCH_BIN_DIR"

    while [ $# -gt 0 ]; do
        case "$1" in
            --bin-dir)
                [ $# -lt 2 ] && fail "--bin-dir requires a path"
                bin_dir="$2"
                shift 2
                ;;
            --version)
                [ $# -lt 2 ] && fail "--version requires a version"
                version="$2"
                shift 2
                ;;
            --channel)
                [ $# -lt 2 ] && fail "--channel requires a name"
                channel="$2"
                shift 2
                ;;
            --cli-only)
                cli_only=true
                shift
                ;;
            --dry-run)
                dry_run=true
                shift
                ;;
            --help|-h)
                cat <<'EOF'
curl -fsSL https://get.sference.com | sh

Install Sference Switch on macOS without Homebrew.

Options:
  --bin-dir PATH    Install CLI to PATH (default: ~/.local/bin)
  --version X.Y.Z   Install a specific version instead of latest
  --channel NAME    Release channel (default: stable)
  --cli-only        Install the CLI only, skip the menubar app
  --dry-run         Print what would be done without downloading
  --help            Show this help

Environment overrides:
  SFERENCE_SWITCH_BASE_URL    CDN base URL
  SFERENCE_SWITCH_CHANNEL     Release channel
  SFERENCE_SWITCH_VERSION     Pin to exact version
  SFERENCE_SWITCH_BIN_DIR     CLI install directory
EOF
                exit 0
                ;;
            *)
                fail "unknown option: $1"
                ;;
        esac
    done

    # Preflight.
    [ "$(uname -s)" = "Darwin" ] || fail "requires macOS (Darwin)"
    arch="$(uname -m)"
    case "$arch" in
        arm64|x86_64) ;;
        *) fail "unsupported architecture: $arch" ;;
    esac
    for tool in curl shasum unzip mktemp; do
        command -v "$tool" >/dev/null 2>&1 || fail "missing required tool: $tool"
    done

    # Warn about conflicting installs.
    existing="$(command -v sference-switch 2>/dev/null || true)"
    if [ -n "$existing" ]; then
        case "$existing" in
            /opt/homebrew/*|/usr/local/Cellar/*|/usr/local/opt/*)
                echo "note: a Homebrew install exists at $existing; this will shadow it" >&2
                ;;
            /nix/store/*)
                echo "note: a Nix install exists at $existing; this will shadow it" >&2
                ;;
        esac
    fi

    # Fetch the manifest (or use the pinned version).
    if [ -z "$version" ]; then
        manifest_url="$base_url/sference-switch/$channel/latest.json"
        echo "Fetching $manifest_url" >&2
        manifest="$(curl -fsSL --proto '=https' --tlsv1.2 --retry 3 --retry-connrefused --max-time 30 "$manifest_url")" \
            || fail "could not fetch manifest: $manifest_url"
        version="$(printf '%s\n' "$manifest" | sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
        [ -n "$version" ] || fail "manifest has no version field: $manifest_url"
        path="$(printf '%s\n' "$manifest" | sed -n 's/.*"path"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
        checksums_path="$(printf '%s\n' "$manifest" | sed -n 's/.*"checksums_path"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
        filename="$(printf '%s\n' "$manifest" | sed -n 's/.*"filename"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
        sha256="$(printf '%s\n' "$manifest" | sed -n 's/.*"sha256"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
        size="$(printf '%s\n' "$manifest" | sed -n 's/.*"size"[[:space:]]*:[[:space:]]*\([0-9]*\).*/\1/p')"
    else
        # Pinned version: construct paths directly.
        path="sference-switch/v$version/sference-switch_${version}_darwin_universal.zip"
        checksums_path="sference-switch/v$version/checksums.txt"
        filename="sference-switch_${version}_darwin_universal.zip"
        sha256=""
        size=""
    fi

    # Validate extracted fields.
    printf '%s' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' \
        || fail "invalid version in manifest: $version"
    printf '%s' "$path" | grep -qF '..' && fail "manifest path contains '..'"
    printf '%s' "$path" | grep -qE '^/' && fail "manifest path is absolute"
    printf '%s' "$path" | grep -qE '^sference-switch/' \
        || fail "manifest path must start with sference-switch/"
    printf '%s' "$checksums_path" | grep -qF '..' && fail "manifest checksums_path contains '..'"
    printf '%s' "$checksums_path" | grep -qE '^/' && fail "manifest checksums_path is absolute"
    printf '%s' "$checksums_path" | grep -qE '^sference-switch/' \
        || fail "manifest checksums_path must start with sference-switch/"

    if [ -n "$sha256" ]; then
        printf '%s' "$sha256" | grep -Eq '^[0-9a-f]\{64\}$' \
            || fail "manifest sha256 is not 64 lowercase hex: $sha256"
    fi

    artifact_url="$base_url/$path"
    checksums_url="$base_url/$checksums_path"

    if $dry_run; then
        echo "Would install sference-switch $version" >&2
        echo "  from:     $artifact_url" >&2
        echo "  bin dir:  $bin_dir" >&2
        echo "  app dir:  $HOME/Applications" >&2
        [ "$cli_only" = "true" ] && echo "  app:      skipped (--cli-only)" >&2
        exit 0
    fi

    # Download and verify.
    tmp="$(mktemp -d)" || fail "mktemp failed"
    trap 'rm -rf "$tmp"' EXIT HUP INT TERM

    echo "Downloading $artifact_url" >&2
    curl -fsSL --proto '=https' --retry 3 --max-time 120 -o "$tmp/$filename" "$artifact_url" \
        || fail "download failed: $artifact_url"

    echo "Downloading $checksums_url" >&2
    curl -fsSL --proto '=https' --retry 3 --max-time 30 -o "$tmp/checksums.txt" "$checksums_url" \
        || fail "download failed: $checksums_url"

    # Verify: shasum against checksums.txt, and cross-check manifest sha256.
    echo "Verifying checksum" >&2
    (cd "$tmp" && shasum -a 256 -c checksums.txt) \
        || fail "checksum verification failed"

    if [ -n "$sha256" ]; then
        actual_sha="$(shasum -a 256 "$tmp/$filename" | awk '{print $1}')"
        [ "$actual_sha" = "$sha256" ] \
            || fail "manifest sha256 ($sha256) does not match downloaded file ($actual_sha)"
    fi

    # Extract.
    echo "Extracting" >&2
    unzip -q "$tmp/$filename" -d "$tmp/extracted" \
        || fail "unzip failed"
    [ -x "$tmp/extracted/bin/sference-switch" ] || fail "extracted binary missing"
    [ -f "$tmp/extracted/install.sh" ] || fail "extracted install.sh missing"

    # Delegate to the bundled installer (verifies signatures, versions, archs).
    export SFERENCE_SWITCH_BIN_DIR="$bin_dir"
    sh "$tmp/extracted/install.sh"
}

main "$@"
