#!/usr/bin/env bash

set -euo pipefail

audit_root="${GT_TEMP_AUDIT_ROOT:-/tmp}"
max_kb="${GT_TEMP_AUDIT_MAX_KB:-102400}"
violations=0

if [[ ! -d "$audit_root" ]]; then
  printf 'temp audit: root does not exist: %s\n' "$audit_root" >&2
  exit 2
fi

while IFS= read -r -d '' entry; do
  name="$(basename "$entry")"
  size_kb="$(du -sk "$entry" 2>/dev/null | awk '{print $1}')"
  reason=""

  if [[ -d "$entry/.git" || -f "$entry/.git" ]]; then
    reason="Git checkout"
  elif [[ -d "$entry/.venv" ]]; then
    reason="project virtual environment"
  elif [[ -d "$entry" && "$name" =~ ^(go-build|go-link|aihub-|flext-|beads-audit|beads-bd-tests|bd-v.*-schema|beads-schema-) ]]; then
    reason="known high-growth temporary pattern"
  elif (( size_kb > max_kb )); then
    reason="entry exceeds ${max_kb} KB"
  fi

  if [[ -n "$reason" ]]; then
    printf 'VIOLATION %s: %s (%s KB)\n' "$entry" "$reason" "$size_kb" >&2
    violations=$((violations + 1))
  fi
done < <(find "$audit_root" -mindepth 1 -maxdepth 1 -print0)

if (( violations > 0 )); then
  printf 'temp audit failed: %d violation(s) under %s\n' "$violations" "$audit_root" >&2
  exit 1
fi

printf 'temp audit clean: no durable clones or oversized build state under %s\n' "$audit_root"
