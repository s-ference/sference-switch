#!/usr/bin/env bash
# Executes scripts/release/get.sh against a local mirror of the S3 layout.
#
# get.sh previously had only `sh -n` syntax checking, which cannot catch a
# regex that parses but never matches. A `grep -Eq '^[0-9a-f]\{64\}$'`
# (BRE braces under ERE) shipped in v0.1.0 and rejected every valid
# manifest, breaking `curl https://get.sference.com | sh` for all users.
#
# These tests run the real script over HTTP against a real manifest, so a
# validator that rejects good input fails here instead of in production.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GET_SH="$REPO_DIR/scripts/release/get.sh"

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
pass() { printf 'ok: %s\n' "$*"; }

command -v python3 >/dev/null 2>&1 || fail "python3 is required"
command -v openssl >/dev/null 2>&1 || fail "openssl is required"

work="$(mktemp -d)"
server_pid=""
cleanup() {
    [ -n "$server_pid" ] && kill "$server_pid" 2>/dev/null || true
    rm -rf "$work"
}
trap cleanup EXIT

version="0.1.0"
tag="v$version"
artifact="sference-switch_${version}_darwin_universal.zip"
mirror="$work/mirror"
mkdir -p "$mirror/sference-switch/stable" "$mirror/sference-switch/$tag"

# A payload shaped like the real one: get.sh delegates to the bundled
# install.sh, so the archive must carry one.
payload="$work/payload"
mkdir -p "$payload/bin"
printf '#!/bin/sh\necho stub\n' >"$payload/bin/sference-switch"
chmod 0755 "$payload/bin/sference-switch"
printf '#!/bin/sh\necho "install.sh ran with BIN_DIR=$SFERENCE_SWITCH_BIN_DIR"\n' \
    >"$payload/install.sh"
chmod 0755 "$payload/install.sh"
(cd "$payload" && zip -qry "$mirror/sference-switch/$tag/$artifact" .)
(cd "$mirror/sference-switch/$tag" && shasum -a 256 "$artifact" >checksums.txt)

sha256="$(shasum -a 256 "$mirror/sference-switch/$tag/$artifact" | awk '{print $1}')"
size="$(wc -c <"$mirror/sference-switch/$tag/$artifact" | tr -d ' ')"

write_manifest() {
    cat >"$mirror/sference-switch/stable/latest.json" <<JSON
{
  "schema_version": 1,
  "product": "sference-switch",
  "channel": "stable",
  "tag": "$tag",
  "version": "$version",
  "published_at": "2026-01-01T00:00:00Z",
  "os": "darwin",
  "arch": "universal",
  "filename": "$artifact",
  "path": "sference-switch/$tag/$artifact",
  "checksums_path": "sference-switch/$tag/checksums.txt",
  "sha256": "${1:-$sha256}",
  "size": ${2:-$size},
  "signing": "adhoc",
  "notarized": false,
  "minimum_macos": "13.0"
}
JSON
}
write_manifest

# get.sh pins --proto '=https' on every fetch. That guard stops a redirect
# downgrading an install to plaintext, so it must not be relaxed to make
# testing easier: the mirror serves TLS with a throwaway self-signed
# certificate that curl trusts via CURL_CA_BUNDLE.
cert="$work/cert.pem"
openssl req -x509 -newkey rsa:2048 -keyout "$cert" -out "$cert" \
    -days 1 -nodes -subj "/CN=127.0.0.1" \
    -addext "subjectAltName=IP:127.0.0.1" >/dev/null 2>&1 \
    || fail "could not generate a test certificate"
export CURL_CA_BUNDLE="$cert"

port=8917
python3 "$REPO_DIR/scripts/release/testdata/tls_mirror.py" \
    "$mirror" "$port" "$cert" >/dev/null 2>&1 &
server_pid=$!
base="https://127.0.0.1:$port"
ready=0
for _ in $(seq 1 60); do
    if curl -fsS --max-time 1 "$base/sference-switch/stable/latest.json" \
        >/dev/null 2>&1; then ready=1; break; fi
    sleep 0.2
done
[ "$ready" = 1 ] || fail "test TLS mirror did not come up on $base"

# --- 1. A valid manifest must be accepted -------------------------------
# This is the regression: the sha256 validator rejected real digests.
out="$(SFERENCE_SWITCH_BASE_URL="$base" "$GET_SH" --dry-run 2>&1)" \
    || fail "dry run rejected a valid manifest:
$out"
printf '%s' "$out" | grep -q "not 64 lowercase hex" \
    && fail "valid 64-hex sha256 was rejected:
$out"
printf '%s' "$out" | grep -q "$version" \
    || fail "dry run did not report version $version:
$out"
pass "valid manifest accepted (64-hex sha256 passes validation)"

# --- 2. A malformed sha256 must still be rejected -----------------------
# The fix must not have simply removed the check.
write_manifest "nothex"
out="$(SFERENCE_SWITCH_BASE_URL="$base" "$GET_SH" --dry-run 2>&1)" && rc=0 || rc=$?
[ "$rc" -ne 0 ] || fail "a malformed sha256 was accepted"
printf '%s' "$out" | grep -q "not 64 lowercase hex" \
    || fail "malformed sha256 rejected with the wrong error:
$out"
pass "malformed sha256 rejected"

# 63 hex characters is the off-by-one the literal-brace bug also let through.
write_manifest "$(printf 'a%.0s' $(seq 1 63))"
out="$(SFERENCE_SWITCH_BASE_URL="$base" "$GET_SH" --dry-run 2>&1)" && rc=0 || rc=$?
[ "$rc" -ne 0 ] || fail "a 63-character sha256 was accepted"
pass "63-character sha256 rejected"

# Uppercase hex is not what shasum emits and must not be accepted.
write_manifest "$(printf 'A%.0s' $(seq 1 64))"
out="$(SFERENCE_SWITCH_BASE_URL="$base" "$GET_SH" --dry-run 2>&1)" && rc=0 || rc=$?
[ "$rc" -ne 0 ] || fail "an uppercase sha256 was accepted"
pass "uppercase sha256 rejected"

# --- 3. Path traversal in the manifest must be rejected -----------------
write_manifest
python3 - "$mirror/sference-switch/stable/latest.json" <<'PY'
import sys
p = sys.argv[1]
s = open(p).read().replace(
    '"path": "sference-switch/v0.1.0/',
    '"path": "../../etc/')
open(p, "w").write(s)
PY
out="$(SFERENCE_SWITCH_BASE_URL="$base" "$GET_SH" --dry-run 2>&1)" && rc=0 || rc=$?
[ "$rc" -ne 0 ] || fail "a traversing manifest path was accepted"
pass "traversing manifest path rejected"

# --- 4. A real end-to-end install over HTTP -----------------------------
# Exercises download, checksum verification, extraction, and the handoff
# to the bundled install.sh.
write_manifest
bin_dir="$work/bin"
mkdir -p "$bin_dir"
out="$(SFERENCE_SWITCH_BASE_URL="$base" "$GET_SH" --bin-dir "$bin_dir" 2>&1)" \
    || fail "install failed:
$out"
printf '%s' "$out" | grep -q "install.sh ran" \
    || fail "bundled install.sh was not invoked:
$out"
pass "end-to-end install verified checksum and ran install.sh"

# --- 5. A corrupted artifact must abort ---------------------------------
printf 'corrupted' >>"$mirror/sference-switch/$tag/$artifact"
out="$(SFERENCE_SWITCH_BASE_URL="$base" "$GET_SH" --bin-dir "$bin_dir" 2>&1)" && rc=0 || rc=$?
[ "$rc" -ne 0 ] || fail "a corrupted artifact was accepted"
pass "corrupted artifact rejected"

printf '\nall get.sh installer tests passed\n'
