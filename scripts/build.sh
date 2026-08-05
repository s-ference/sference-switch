#!/usr/bin/env bash
# Build the sference-switch stack.
#   ./scripts/build.sh gateway      build only the Go gateway binary

set -euo pipefail
cd "$(dirname "$0")/.."

target="${1:-gateway}"

case "$target" in
  gateway)
    # Single binary: the front door is the `sference-switch door` subcommand.
    # Version is stamped from git so `status` can flag skew between the
    # running processes and the binary on disk ("restart to adopt").
    # Plain `go build` leaves it as "dev".
    VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
    (cd gateway && go build \
        -ldflags "-X github.com/sference/sference-switch/gateway/internal/version.Version=${VERSION}" \
        -o bin/sference-switch ./cmd/sference-switch)
    ls -lh gateway/bin/sference-switch
    ;;
  *)
    echo "unknown target: $target" >&2
    exit 2
    ;;
esac
