#!/usr/bin/env bash

run_with_timeout() {
  local seconds="$1"
  shift

  if command -v timeout >/dev/null 2>&1; then
    command timeout "$seconds" "$@"
    return
  fi
  if command -v gtimeout >/dev/null 2>&1; then
    command gtimeout "$seconds" "$@"
    return
  fi
  if ! command -v perl >/dev/null 2>&1; then
    echo "no supported timeout command found" >&2
    return 127
  fi

  perl -e '$seconds = shift; alarm $seconds; exec @ARGV' "$seconds" "$@"
  local rc=$?
  if [[ $rc -eq 142 ]]; then
    return 124
  fi
  return "$rc"
}
