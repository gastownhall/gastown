#!/usr/bin/env bash
# Tests for Makefile check-install-path shadow warnings.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
MAKE_BIN="$(command -v "${MAKE:-make}")"
SYSTEM_PATH="/usr/bin:/bin"
SCRATCH_DIR=""
PASS=0
FAIL=0

cleanup() {
  if [[ -n "$SCRATCH_DIR" && -d "$SCRATCH_DIR" ]]; then
    find "$SCRATCH_DIR" -depth -delete
  fi
	SCRATCH_DIR=""
}
trap cleanup EXIT

setup_bins() {
  local scratch_base="${TMPDIR:-${XDG_CACHE_HOME:-$HOME/.cache}/gastown/test-tmp}"
  mkdir -p "$scratch_base"
  SCRATCH_DIR="$(mktemp -d "$scratch_base/gt-check-install-path.XXXXXX")"
  INSTALL_DIR="$SCRATCH_DIR/home/.local/bin"
  BREW_DIR="$SCRATCH_DIR/usr/local/bin"
  OTHER_DIR="$SCRATCH_DIR/usr/bin"
  mkdir -p "$INSTALL_DIR" "$BREW_DIR" "$OTHER_DIR"
  printf '#!/usr/bin/env sh\nexit 0\n' > "$INSTALL_DIR/gt"
  printf '#!/usr/bin/env sh\nexit 0\n' > "$BREW_DIR/gt"
  chmod +x "$INSTALL_DIR/gt" "$BREW_DIR/gt"
}

run_check() {
  local path_value="$1"
  env PATH="$path_value" "$MAKE_BIN" --no-print-directory -C "$REPO_ROOT" \
    check-install-path INSTALL_DIR="$INSTALL_DIR" 2>&1
}

assert_empty() {
  local test_name="$1"
  local output="$2"
  if [[ -z "$output" ]]; then
    echo "  PASS: $test_name"
    PASS=$((PASS + 1))
  else
    echo "  FAIL: $test_name"
    echo "$output"
    FAIL=$((FAIL + 1))
  fi
}

assert_warns() {
  local test_name="$1"
  local output="$2"
  local expected="$3"
  if [[ "$output" == *"Warning: gt resolves to $expected, not $INSTALL_DIR/gt"* ]] && \
     [[ "$output" == *"export PATH=\"$INSTALL_DIR:\$PATH\""* ]]; then
    echo "  PASS: $test_name"
    PASS=$((PASS + 1))
  else
    echo "  FAIL: $test_name"
    echo "$output"
    FAIL=$((FAIL + 1))
  fi
}

echo "=== check-install-path tests ==="

setup_bins
output="$(run_check "$INSTALL_DIR:$BREW_DIR:$OTHER_DIR:$SYSTEM_PATH")"
assert_empty "no warning when install dir wins PATH" "$output"
cleanup

setup_bins
output="$(run_check "$OTHER_DIR:$SYSTEM_PATH")"
assert_warns "warns when install dir is omitted from PATH" "$output" "nothing in PATH"
cleanup

setup_bins
output="$(run_check "$BREW_DIR:$INSTALL_DIR:$OTHER_DIR:$SYSTEM_PATH")"
assert_warns "warns when earlier Homebrew-style gt shadows install" "$output" "$BREW_DIR/gt"
cleanup

echo "Results: $PASS passed, $FAIL failed"
[[ "$FAIL" -eq 0 ]] && exit 0 || exit 1
