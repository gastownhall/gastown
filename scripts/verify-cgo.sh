#!/usr/bin/env bash
# Verify that a user-facing binary keeps CGO support without an ICU runtime
# dependency. The build contract follows the pinned Beads production policy:
# CGO_ENABLED=1 plus the gms_pure_go build tag.

set -euo pipefail

if [[ $# -ne 1 ]]; then
    echo "Usage: $0 <path-to-binary>" >&2
    exit 2
fi

binary_path="$1"

if [[ ! -f "$binary_path" ]]; then
    echo "ERROR: binary not found: $binary_path" >&2
    exit 1
fi

if ! command -v strings >/dev/null 2>&1; then
    echo "ERROR: 'strings' is required to verify CGO metadata" >&2
    exit 1
fi

if strings "$binary_path" | awk '/^build[[:space:]]+CGO_ENABLED=0$/ { found=1 } END { exit(found?0:1) }'; then
    echo "ERROR: $binary_path was built without CGO support" >&2
    exit 1
fi

if command -v readelf >/dev/null 2>&1 && readelf -d "$binary_path" 2>/dev/null | grep -qi 'libicu'; then
    echo "ERROR: $binary_path has an unexpected ICU runtime dependency" >&2
    readelf -d "$binary_path" | grep -i 'libicu' >&2
    exit 1
fi

if command -v otool >/dev/null 2>&1 && otool -L "$binary_path" 2>/dev/null | grep -qi 'libicu'; then
    echo "ERROR: $binary_path has an unexpected ICU runtime dependency" >&2
    otool -L "$binary_path" | grep -i 'libicu' >&2
    exit 1
fi

if command -v objdump >/dev/null 2>&1 && objdump -p "$binary_path" 2>/dev/null | grep -qi 'libicu'; then
    echo "ERROR: $binary_path has an unexpected ICU runtime dependency" >&2
    objdump -p "$binary_path" | grep -i 'libicu' >&2
    exit 1
fi

echo "OK: $binary_path has CGO support and no ICU runtime dependency"
