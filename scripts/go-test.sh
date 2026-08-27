#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cache_home="${XDG_CACHE_HOME:-$HOME/.cache}"
scratch_base="${GT_TEST_TMP_BASE:-$cache_home/gastown/test-tmp}"
go_cache="${GT_TEST_GOCACHE:-$cache_home/gastown/go-build}"
mkdir -p "$scratch_base" "$go_cache"
test_root="$(mktemp -d "$scratch_base/gastown-test-env.XXXXXX")"

cleanup() {
  if [[ -n "${test_root:-}" && -d "$test_root" ]]; then
    find "$test_root" -depth -delete
  fi
}
trap cleanup EXIT

mkdir -p "$test_root/home" "$test_root/tmp" "$test_root/go-tmp" \
  "$test_root/xdg-config" "$test_root/dolt-root"
cat >"$test_root/gitconfig" <<'EOF'
[user]
	name = gastown-test
	email = test@gastown.local
[init]
	defaultBranch = main
EOF

export HOME="$test_root/home"
export USERPROFILE="$test_root/home"
export TMPDIR="$test_root/tmp"
export GOTMPDIR="$test_root/go-tmp"
export XDG_CONFIG_HOME="$test_root/xdg-config"
export DOLT_ROOT_PATH="$test_root/dolt-root"
export GOCACHE="$go_cache"
export GIT_CONFIG_NOSYSTEM=1
export GIT_CONFIG_GLOBAL="$test_root/gitconfig"
export GT_TEST_DISCOVERY_CEILING="$test_root"
unset GT_ROOT GT_TOWN_ROOT BEADS_DIR BEADS_DB

cd "$repo_root"
go test "$@"
