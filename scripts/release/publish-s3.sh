#!/usr/bin/env bash
# Publish Sference Switch release artifacts to S3 and flip the stable manifest.
#
# Called from the release workflow after build-artifacts.sh produces dist/.
# Uploads versioned artifacts (immutable), then flips sference-switch/stable/
# latest.json last so a client never sees a manifest pointing at a missing
# artifact. Then uploads the bootstrap script and invalidates CloudFront.
#
# Never replaces published artifacts — head-object precheck aborts if any
# versioned key already exists.
#
# Usage: publish-s3.sh --tag v0.2.0 --dist-dir dist --bucket sference-prod-releases \
#          --distribution-id E1234567890ABC --channel stable
set -euo pipefail

tag=""
dist_dir=""
bucket=""
distribution_id=""
channel="stable"
bootstrap="scripts/release/get.sh"
dry_run=false

while [ $# -gt 0 ]; do
    case "$1" in
        --tag) tag="$2"; shift 2 ;;
        --dist-dir) dist_dir="$2"; shift 2 ;;
        --bucket) bucket="$2"; shift 2 ;;
        --distribution-id) distribution_id="$2"; shift 2 ;;
        --channel) channel="$2"; shift 2 ;;
        --bootstrap) bootstrap="$2"; shift 2 ;;
        --dry-run) dry_run=true; shift ;;
        *) echo "unknown option: $1" >&2; exit 2 ;;
    esac
done

[ -n "$tag" ] || { echo "missing --tag" >&2; exit 2; }
[ -n "$dist_dir" ] || { echo "missing --dist-dir" >&2; exit 2; }
[ -n "$bucket" ] || { echo "missing --bucket" >&2; exit 2; }
[ -n "$distribution_id" ] || { echo "missing --distribution-id" >&2; exit 2; }
# The bootstrap is what makes `curl https://get.sference.com | sh` work.
# It used to be skipped when absent, so a publish job running without a
# repository checkout published a release whose install URL stayed 404 —
# and nothing failed, because the smoke test only reads the manifest.
[ -f "$bootstrap" ] \
    || { echo "bootstrap script not found: $bootstrap (pass --bootstrap)" >&2; exit 2; }

version="${tag#v}"
artifact="sference-switch_${version}_darwin_universal.zip"
zip_path="$dist_dir/$artifact"
checksums_path="$dist_dir/checksums.txt"

[ -f "$zip_path" ] || { echo "missing artifact: $zip_path" >&2; exit 1; }
[ -f "$checksums_path" ] || { echo "missing checksums: $checksums_path" >&2; exit 1; }

# Compute the manifest fields.
zip_sha256="$(shasum -a 256 "$zip_path" | awk '{print $1}')"
# stat is not portable: BSD/macOS spells this -f%z, GNU/Linux -c%s (where -f
# means "filesystem" and fails). The publish job runs on ubuntu while local
# testing happens on macOS, so try both rather than assuming either. wc -c is
# the POSIX fallback.
zip_size="$(stat -f%z "$zip_path" 2>/dev/null \
    || stat -c%s "$zip_path" 2>/dev/null \
    || wc -c <"$zip_path" | tr -d ' ')"
[ -n "$zip_size" ] \
    || { echo "could not determine size of $zip_path" >&2; exit 1; }
published_at="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"

# The manifest is one field per line so the POSIX-sh installer can extract
# fields with sed without jq.
read -r -d '' manifest <<JSON || true
{
  "schema_version": 1,
  "product": "sference-switch",
  "channel": "$channel",
  "tag": "$tag",
  "version": "$version",
  "published_at": "$published_at",
  "os": "darwin",
  "arch": "universal",
  "filename": "$artifact",
  "path": "sference-switch/$tag/$artifact",
  "checksums_path": "sference-switch/$tag/checksums.txt",
  "sha256": "$zip_sha256",
  "size": $zip_size,
  "signing": "adhoc",
  "notarized": false,
  "minimum_macos": "13.0"
}
JSON

if $dry_run; then
    echo "=== dry run ==="
    echo "tag:              $tag"
    echo "version:          $version"
    echo "artifact:         $artifact"
    echo "sha256:           $zip_sha256"
    echo "size:             $zip_size"
    echo "bucket:           $bucket"
    echo "distribution_id:  $distribution_id"
    echo "channel:          $channel"
    echo "manifest:"
    echo "$manifest"
    exit 0
fi

# Immutability check: never replace published artifacts.
prefix="sference-switch/$tag"
for key in "$prefix/$artifact" "$prefix/checksums.txt" "$prefix/manifest.json"; do
    if aws s3api head-object --bucket "$bucket" --key "$key" 2>/dev/null; then
        echo "error: $key already exists in $bucket (immutable)" >&2
        exit 1
    fi
done

# Upload versioned artifacts.
echo "Uploading $artifact to s3://$bucket/$prefix/"
aws s3 cp "$zip_path" "s3://$bucket/$prefix/$artifact" \
    --cache-control "public, max-age=31536000, immutable" \
    --content-type "application/zip" \
    --no-progress

echo "Uploading checksums.txt"
aws s3 cp "$checksums_path" "s3://$bucket/$prefix/checksums.txt" \
    --cache-control "public, max-age=31536000, immutable" \
    --content-type "text/plain; charset=utf-8" \
    --no-progress

# Verify the upload round-trip.
echo "Verifying upload"
downloaded_sha="$(aws s3 cp "s3://$bucket/$prefix/$artifact" - --no-progress 2>/dev/null | shasum -a 256 | awk '{print $1}')"
if [ "$downloaded_sha" != "$zip_sha256" ]; then
    echo "error: uploaded artifact sha256 mismatch (local=$zip_sha256 remote=$downloaded_sha)" >&2
    exit 1
fi

# Upload the immutable manifest copy.
manifest_file="$(mktemp)"
trap 'rm -f "$manifest_file"' EXIT
printf '%s\n' "$manifest" > "$manifest_file"

echo "Uploading manifest.json"
aws s3 cp "$manifest_file" "s3://$bucket/$prefix/manifest.json" \
    --cache-control "public, max-age=31536000, immutable" \
    --content-type "application/json" \
    --no-progress

# Flip the stable pointer last — this is the atomicity guarantee.
echo "Flipping $channel/latest.json"
aws s3 cp "$manifest_file" "s3://$bucket/sference-switch/$channel/latest.json" \
    --cache-control "public, max-age=60" \
    --content-type "application/json" \
    --no-progress

# Upload the bootstrap script. Its presence is validated up front, so a
# missing bootstrap fails the publish instead of silently shipping a
# release whose documented install command 404s.
echo "Uploading install.sh"
aws s3 cp "$bootstrap" "s3://$bucket/install.sh" \
    --cache-control "public, max-age=300" \
    --content-type "text/plain; charset=utf-8" \
    --no-progress

# Invalidate the mutable paths.
echo "Invalidating CloudFront"
aws cloudfront create-invalidation \
    --distribution-id "$distribution_id" \
    --paths "/sference-switch/$channel/latest.json" "/install.sh" "/" \
    --no-cli-pager

echo "Published $tag to $channel"
