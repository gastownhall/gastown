#!/usr/bin/env bash

set -euo pipefail

plugin_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
script=${plugin_dir}/run.sh
docs=${plugin_dir}/plugin.md

grep -Fq 'SOURCE_RIG="${GT_SOURCE_RIG:-gastown_fork}"' "$script"
grep -Fq 'RIG_ROOT="${TOWN_ROOT}/${SOURCE_RIG}/mayor/rig"' "$script"

if grep -Fq 'TOWN_ROOT}/gastown/mayor/rig' "$script"; then
  printf 'legacy gastown source path remains\n' >&2
  exit 1
fi

if grep -Fq -- '--rig gastown' "$docs"; then
  printf 'legacy gastown tracking rig remains\n' >&2
  exit 1
fi

printf 'rebuild-gt source rig contract: ok\n'
