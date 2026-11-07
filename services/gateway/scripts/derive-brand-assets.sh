#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
GATEWAY_ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd)"
REPO_ROOT="$(cd -- "$GATEWAY_ROOT/../.." && pwd)"
SVG="$GATEWAY_ROOT/web/default/public/miraphant.svg"
SOURCE_SVG="$REPO_ROOT/images/miraphant.svg"
PUBLIC="$GATEWAY_ROOT/web/default/public"
EXPECTED_SHA256="5f69642734d961b4616e35bfae63385030688a253763549edeca72babb791e63"

for tool in sips shasum cmp; do
  command -v "$tool" >/dev/null 2>&1 || { echo "required tool not found: $tool" >&2; exit 1; }
done
cmp -s "$SVG" "$SOURCE_SVG" || { echo "public SVG differs from the canonical repository asset" >&2; exit 1; }
actual_sha="$(shasum -a 256 "$SVG" | awk '{print $1}')"
[[ "$actual_sha" == "$EXPECTED_SHA256" ]] || { echo "unexpected canonical SVG checksum: $actual_sha" >&2; exit 1; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
sips -s format png "$SVG" --out "$tmp/source.png" >/dev/null
sips -Z 448 "$tmp/source.png" --out "$PUBLIC/logo.png" >/dev/null

sips -Z 480 "$tmp/source.png" --out "$tmp/touch-logo.png" >/dev/null
sips -p 512 512 --padColor F5F7F2 "$tmp/touch-logo.png" --out "$PUBLIC/apple-touch-icon.png" >/dev/null

sips -z 480 840 "$tmp/source.png" --out "$tmp/share-logo.png" >/dev/null
sips -p 630 1200 --padColor F5F7F2 "$tmp/share-logo.png" --out "$PUBLIC/miraphant-share.png" >/dev/null

sips -Z 32 "$tmp/source.png" --out "$tmp/favicon-logo.png" >/dev/null
sips -p 32 32 --padColor F5F7F2 "$tmp/favicon-logo.png" --out "$PUBLIC/favicon-32.png" >/dev/null
sips -s format ico "$PUBLIC/favicon-32.png" --out "$PUBLIC/favicon.ico" >/dev/null

echo "Derived brand assets from verified SVG $actual_sha"
