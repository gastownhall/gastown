#!/usr/bin/env bash
# Regression tests for Makefile check-up-to-date, incl. fork-backed asymmetric
# fetch/push origins where local HEAD legitimately runs ahead of upstream.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
MAKE_BIN="$(command -v "${MAKE:-make}")"
TMPDIR=""
PASS=0
FAIL=0

cleanup() {
  if [[ -n "$TMPDIR" && -d "$TMPDIR" ]]; then
    rm -rf "$TMPDIR"
  fi
}
trap cleanup EXIT

# Minimal Makefile stand-in that pulls in only the target under test, so the
# test doesn't depend on the rest of this repo's build graph.
write_check_target() {
  local dir="$1"
  awk '/^check-up-to-date:/,/^endif$/' "$REPO_ROOT/Makefile" > "$dir/Makefile"
}

setup_repo() {
  TMPDIR="$(mktemp -d)"
  UPSTREAM="$TMPDIR/upstream"
  LOCAL="$TMPDIR/local"

  git init --quiet -b main "$UPSTREAM"
  git -C "$UPSTREAM" -c user.email=t@t -c user.name=t commit --quiet --allow-empty -m base

  git clone --quiet "$UPSTREAM" "$LOCAL"
  git -C "$LOCAL" -c user.email=t@t -c user.name=t config user.email t@t
  git -C "$LOCAL" -c user.email=t@t -c user.name=t config user.name t
  write_check_target "$LOCAL"
}

run_check() {
  (cd "$LOCAL" && "$MAKE_BIN" --no-print-directory check-up-to-date 2>&1)
}

# set -e treats a failing command substitution in a plain assignment as fatal,
# so route through if/else to capture non-zero exits without aborting the run.
capture() {
  if output="$(run_check)"; then rc=0; else rc=$?; fi
}

assert_pass() {
  local test_name="$1" output="$2" rc="$3"
  if [[ "$rc" -eq 0 ]]; then
    echo "  PASS: $test_name"
    PASS=$((PASS + 1))
  else
    echo "  FAIL: $test_name (expected exit 0, got $rc)"
    echo "$output"
    FAIL=$((FAIL + 1))
  fi
}

assert_fail() {
  local test_name="$1" output="$2" rc="$3"
  if [[ "$rc" -ne 0 ]] && [[ "$output" == *"not up to date"* ]]; then
    echo "  PASS: $test_name"
    PASS=$((PASS + 1))
  else
    echo "  FAIL: $test_name (expected non-zero exit + 'not up to date', got rc=$rc)"
    echo "$output"
    FAIL=$((FAIL + 1))
  fi
}

echo "=== check-up-to-date tests ==="

# Case 1: local == upstream -> pass
setup_repo
capture
assert_pass "local equal to upstream" "$output" "$rc"
cleanup

# Case 2: local behind upstream -> fail
setup_repo
git -C "$UPSTREAM" -c user.email=t@t -c user.name=t commit --quiet --allow-empty -m "upstream-ahead"
git -C "$LOCAL" fetch --quiet origin main
capture
assert_fail "local behind upstream" "$output" "$rc"
cleanup

# Case 3: local diverged from upstream -> fail
setup_repo
git -C "$UPSTREAM" -c user.email=t@t -c user.name=t commit --quiet --allow-empty -m "upstream-diverge"
git -C "$LOCAL" -c user.email=t@t -c user.name=t commit --quiet --allow-empty -m "local-diverge"
git -C "$LOCAL" fetch --quiet origin main
capture
assert_fail "local diverged from upstream" "$output" "$rc"
cleanup

# Case 4: fork-backed asymmetric origin — local is a descendant of upstream
# (fork carries extra downstream commits, e.g. origin fetch=upstream repo,
# push=fork repo) -> must pass, not be treated as stale.
setup_repo
git -C "$LOCAL" -c user.email=t@t -c user.name=t commit --quiet --allow-empty -m "fork-only-commit-1"
git -C "$LOCAL" -c user.email=t@t -c user.name=t commit --quiet --allow-empty -m "fork-only-commit-2"
capture
assert_pass "local ahead of upstream (fork-backed descendant)" "$output" "$rc"
cleanup

echo "Results: $PASS passed, $FAIL failed"
[[ "$FAIL" -eq 0 ]] && exit 0 || exit 1
