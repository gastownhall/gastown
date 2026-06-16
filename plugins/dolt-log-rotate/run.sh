#!/usr/bin/env bash
# dolt-log-rotate/run.sh — Rotate Dolt server log when it exceeds threshold.
#
# The Dolt server log at daemon/dolt.log can grow to multiple GB.
# This plugin rotates it, keeping compressed backups.

set -euo pipefail

# --- Configuration -----------------------------------------------------------

TOWN_ROOT="${GT_TOWN_ROOT:-$(gt town root 2>/dev/null)}"
LOG_DIR="${TOWN_ROOT}/daemon"
LOG_FILE="${LOG_DIR}/dolt.log"
MAX_MB="${GT_DOLT_LOG_MAX_MB:-100}"
KEEP="${GT_DOLT_LOG_KEEP:-3}"

log() { echo "[dolt-log-rotate] $*"; }

# --- Preflight ---------------------------------------------------------------

if [[ ! -f "$LOG_FILE" ]]; then
  log "No log file at $LOG_FILE. Nothing to do."
  exit 0
fi

# Get file size in MB (portable: stat differs on macOS vs Linux)
if [[ "$(uname)" == "Darwin" ]]; then
  SIZE_BYTES=$(stat -f%z "$LOG_FILE" 2>/dev/null || echo 0)
else
  SIZE_BYTES=$(stat -c%s "$LOG_FILE" 2>/dev/null || echo 0)
fi
SIZE_MB=$(( SIZE_BYTES / 1048576 ))

log "Current log size: ${SIZE_MB}MB (threshold: ${MAX_MB}MB)"

if [[ $SIZE_MB -lt $MAX_MB ]]; then
  log "Below threshold. Nothing to do."
  __rid="$(bd create "dolt-log-rotate: log size ${SIZE_MB}MB, below ${MAX_MB}MB threshold" \
    -t chore --ephemeral -l type:plugin-run,plugin:dolt-log-rotate,result:success \
    --silent 2>/dev/null)" && [ -n "$__rid" ] && bd close "$__rid" --force 2>/dev/null || true
  exit 0
fi

# --- Rotate ------------------------------------------------------------------

log "Rotating log (${SIZE_MB}MB exceeds ${MAX_MB}MB threshold)..."

# Shift existing rotated logs: .3.gz -> deleted, .2.gz -> .3.gz, etc.
for i in $(seq $((KEEP - 1)) -1 1); do
  src="${LOG_FILE}.$i.gz"
  dst="${LOG_FILE}.$((i + 1)).gz"
  if [[ -f "$src" ]]; then
    if [[ $((i + 1)) -gt $KEEP ]]; then
      rm -f "$src"
    else
      mv -f "$src" "$dst"
    fi
  fi
done

# Compress current log to .1.gz
gzip -c "$LOG_FILE" > "${LOG_FILE}.1.gz"

# Truncate the active log file instead of removing it.
# This is safe because Dolt holds an open fd to this file.
# Truncating (vs rename+create) means Dolt immediately starts writing
# to the now-empty file without needing a restart or SIGHUP.
: > "$LOG_FILE"

COMPRESSED_MB=$(( $(stat -f%z "${LOG_FILE}.1.gz" 2>/dev/null || stat -c%s "${LOG_FILE}.1.gz" 2>/dev/null || echo 0) / 1048576 ))

log "Rotated: ${SIZE_MB}MB -> ${LOG_FILE}.1.gz (${COMPRESSED_MB}MB compressed)"
log "Active log truncated to 0 bytes."

# List retained logs
RETAINED=0
for i in $(seq 1 $KEEP); do
  if [[ -f "${LOG_FILE}.$i.gz" ]]; then
    RETAINED=$((RETAINED + 1))
  fi
done
log "Retained $RETAINED compressed log(s) (max: $KEEP)"

# --- Report ------------------------------------------------------------------

SUMMARY="dolt-log-rotate: rotated ${SIZE_MB}MB -> ${COMPRESSED_MB}MB compressed, $RETAINED backups retained"
log ""
log "=== Done === $SUMMARY"

__rid="$(bd create "$SUMMARY" -t chore --ephemeral \
  -l type:plugin-run,plugin:dolt-log-rotate,result:success \
  --silent 2>/dev/null)" && [ -n "$__rid" ] && bd close "$__rid" --force 2>/dev/null || true
