#!/usr/bin/env bash
# Verify the public Go install path from outside the repository.
set -euo pipefail

MODULE="github.com/jonbaldie/gastown"
REF="${1:-main}"
TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

mkdir -p "$TMPDIR/bin" "$TMPDIR/work"
cd "$TMPDIR/work"

GOWORK=off CGO_ENABLED=0 GOBIN="$TMPDIR/bin" \
  go install "$MODULE/cmd/gt@$REF"

meta="$(go version -m "$TMPDIR/bin/gt")"
printf '%s\n' "$meta" | awk -v path="path\t${MODULE}/cmd/gt" -v mod="mod\t${MODULE}\t" '
  $0 ~ path { found_path = 1 }
  $0 ~ mod { found_mod = 1 }
  END {
    if (!found_path) {
      print "missing package path " path > "/dev/stderr"
      exit 1
    }
    if (!found_mod) {
      print "missing module line " mod > "/dev/stderr"
      exit 1
    }
  }
'
"$TMPDIR/bin/gt" version
