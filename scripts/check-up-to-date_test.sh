#!/usr/bin/env bash
# Tests for the Makefile check-up-to-date ancestry gate.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
MAKE_BIN="$(command -v "${MAKE:-make}")"
TEST_ROOT="$(mktemp -d)"
PASS=0
FAIL=0

cleanup() {
  rm -rf "$TEST_ROOT"
}
trap cleanup EXIT

git_in() {
  local root="$1"
  shift
  git -C "$root" "$@"
}

seed_repo() {
  local name="$1"
  local remote="$TEST_ROOT/$name-remote.git"
  local work="$TEST_ROOT/$name"
  git init -q --bare "$remote"
  git init -q "$work"
  git_in "$work" checkout -q -b main
  git_in "$work" config user.email test@example.com
  git_in "$work" config user.name "Make Tests"
  printf 'base\n' > "$work/state.txt"
  git_in "$work" add state.txt
  git_in "$work" commit -q -m base
  git_in "$work" remote add origin "$remote"
  git_in "$work" push -q -u origin main
  printf '%s\n' "$work"
}

run_gate() {
  local work="$1"
  "$MAKE_BIN" --no-print-directory -f "$REPO_ROOT/Makefile" \
    -C "$work" check-up-to-date
}

pass() {
  echo "  PASS: $1"
  PASS=$((PASS + 1))
}

fail() {
  echo "  FAIL: $1"
  FAIL=$((FAIL + 1))
}

echo "=== check-up-to-date tests ==="

equal_repo="$(seed_repo equal)"
if run_gate "$equal_repo" >/dev/null; then pass "equal branch"; else fail "equal branch"; fi

ahead_repo="$(seed_repo ahead)"
printf 'ahead\n' >> "$ahead_repo/state.txt"
git_in "$ahead_repo" commit -qam ahead
if run_gate "$ahead_repo" >/dev/null; then pass "local branch ahead"; else fail "local branch ahead"; fi

behind_repo="$(seed_repo behind)"
other="$TEST_ROOT/behind-other"
git clone -q "$TEST_ROOT/behind-remote.git" "$other"
git_in "$other" config user.email test@example.com
git_in "$other" config user.name "Make Tests"
printf 'remote\n' >> "$other/state.txt"
git_in "$other" commit -qam remote
git_in "$other" push -q
if run_gate "$behind_repo" >/dev/null 2>&1; then fail "local branch behind"; else pass "local branch behind"; fi

diverged_repo="$(seed_repo diverged)"
printf 'local\n' >> "$diverged_repo/state.txt"
git_in "$diverged_repo" commit -qam local
other="$TEST_ROOT/diverged-other"
git clone -q "$TEST_ROOT/diverged-remote.git" "$other"
git_in "$other" config user.email test@example.com
git_in "$other" config user.name "Make Tests"
printf 'remote\n' >> "$other/state.txt"
git_in "$other" commit -qam remote
git_in "$other" push -q
if run_gate "$diverged_repo" >/dev/null 2>&1; then fail "diverged branch"; else pass "diverged branch"; fi

echo "Results: $PASS passed, $FAIL failed"
[[ "$FAIL" -eq 0 ]]
