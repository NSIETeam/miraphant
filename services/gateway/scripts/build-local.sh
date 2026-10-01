#!/usr/bin/env bash
set -euo pipefail

gateway_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo_dir="$(cd "$gateway_dir/../.." && pwd)"
go_bin="${GO_BIN:-go}"
build_dir="${MIRAPHANT_GATEWAY_BUILD_DIR:-$repo_dir/../miraphant-gateway-build}"
git_bin="${GIT_BIN:-git}"

if ! command -v "$git_bin" >/dev/null 2>&1; then
  printf 'Git executable not found: %s\n' "$git_bin" >&2
  exit 2
fi
source_revision="$("$git_bin" -C "$repo_dir" rev-parse HEAD)"
source_short_revision="${source_revision:0:12}"
source_dirty=false
if [[ -n $("$git_bin" -C "$repo_dir" status --porcelain --untracked-files=all) ]]; then
  source_dirty=true
fi
version="miraphant+${source_short_revision}"
if [[ "$source_dirty" == true ]]; then
  version="${version}.dirty"
fi

if ! command -v "$go_bin" >/dev/null 2>&1; then
  printf 'Go executable not found: %s\n' "$go_bin" >&2
  exit 2
fi
go_version="$("$go_bin" version)"
case "$go_version" in
  *"go1.25.1"*) ;;
  *) printf 'Expected Go 1.25.1, found: %s\n' "$go_version" >&2; exit 2 ;;
esac

command -v node >/dev/null 2>&1 || { printf 'Node.js is required.\n' >&2; exit 2; }
command -v npm >/dev/null 2>&1 || { printf 'npm is required.\n' >&2; exit 2; }
node_version="$(node --version)"
npm_version="$(npm --version)"
[[ "$node_version" == "v24.20.0" ]] || { printf 'Expected Node.js v24.20.0, found %s\n' "$node_version" >&2; exit 2; }
[[ "$npm_version" == "11.19.0" ]] || { printf 'Expected npm 11.19.0, found %s\n' "$npm_version" >&2; exit 2; }
go_os="$($go_bin env GOOS)"
go_arch="$($go_bin env GOARCH)"
if command -v sha256sum >/dev/null 2>&1; then
  binary_sha256() { sha256sum "$1" | awk '{print $1}'; }
else
  binary_sha256() { shasum -a 256 "$1" | awk '{print $1}'; }
fi

mkdir -p "$build_dir"
rm -rf "$gateway_dir/web/build"
mkdir -p "$gateway_dir/web/build"

for theme in default berry air; do
  printf '\n== Install and build frontend theme: %s ==\n' "$theme"
  (
    cd "$gateway_dir/web/$theme"
    npm ci --no-audit --no-fund
    DISABLE_ESLINT_PLUGIN=true REACT_APP_VERSION="$version" npm run build
  )
  test -s "$gateway_dir/web/build/$theme/index.html"
done

printf '\n== Compile gateway ==\n'
(
  cd "$gateway_dir"
  "$go_bin" build -trimpath \
    -ldflags "-s -w -X github.com/songquanpeng/one-api/common.Version=$version" \
    -o "$build_dir/one-api" .
)

test -s "$build_dir/one-api"
{
  printf 'baseline=%s\n' 'songquanpeng/one-api@v0.6.10 (3915ce9814b8261a1ab13ed93adec58b463cd75c)'
  printf 'source_revision=%s\n' "$source_revision"
  printf 'source_dirty=%s\n' "$source_dirty"
  printf 'source_dirty_status_entries=%s\n' "$("$git_bin" -C "$repo_dir" status --porcelain --untracked-files=all | wc -l | tr -d ' ')"
  printf 'go=%s\n' "$go_version"
  printf 'goos=%s\n' "$go_os"
  printf 'goarch=%s\n' "$go_arch"
  printf 'node=%s\n' "$node_version"
  printf 'npm=%s\n' "$npm_version"
  printf 'gateway_version=%s\n' "$version"
  printf 'binary_sha256=%s\n' "$(binary_sha256 "$build_dir/one-api")"
} > "$build_dir/build-info.txt"

printf '\nBuild complete: %s\n' "$build_dir/one-api"
cat "$build_dir/build-info.txt"
