#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/timeout.sh"

fallback_path="$(mktemp -d)"
trap 'rm -f "$fallback_path/bash" "$fallback_path/dirname" "$fallback_path/perl"; rmdir "$fallback_path"' EXIT

for command in bash dirname perl; do
  command_path="$(command -v "$command")"
  ln -s "$command_path" "$fallback_path/$command"
done

PATH="$fallback_path" run_with_timeout 2 bash -c 'exit 0'
