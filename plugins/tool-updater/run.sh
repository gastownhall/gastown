#!/usr/bin/env bash
# tool-updater/run.sh — Upgrade beads (bd) and dolt via Homebrew.
#
# gt is managed separately via rebuild-gt (builds from source).
# This plugin handles the Homebrew-installed tools on a weekly cadence.

set -euo pipefail

TOOLS=(beads dolt)

log() { echo "[tool-updater] $*"; }

# --- Refresh Homebrew formula index ------------------------------------------

log "Running brew update..."
HOMEBREW_NO_AUTO_UPDATE=1 brew update 2>&1 | tail -3 || true

# --- Check for outdated tools -------------------------------------------------

log "Checking for updates..."
OUTDATED=()
for TOOL in "${TOOLS[@]}"; do
  if brew outdated --quiet "$TOOL" 2>/dev/null | grep -q "$TOOL"; then
    OLD_VER=$(brew info --json "$TOOL" 2>/dev/null | python3 -c "import json,sys; d=json.load(sys.stdin)[0]; print(d['installed'][0]['version'])" 2>/dev/null || echo "?")
    OUTDATED+=("$TOOL@$OLD_VER")
    log "  $TOOL: update available (installed: $OLD_VER)"
  else
    log "  $TOOL: up to date"
  fi
done

if [[ ${#OUTDATED[@]} -eq 0 ]]; then
  log "All tools current. Nothing to do."
  __rid="$(bd create "tool-updater: all tools current (beads=$(bd version 2>/dev/null | awk '{print $3}'), dolt=$(dolt version 2>/dev/null | awk '{print $3}')" \
    -t chore --ephemeral -l type:plugin-run,plugin:tool-updater,result:success \
    --silent 2>/dev/null)" && [ -n "$__rid" ] && bd close "$__rid" --force 2>/dev/null || true
  exit 0
fi

# --- Upgrade outdated tools ---------------------------------------------------

UPGRADED=()
FAILED=()

for entry in "${OUTDATED[@]}"; do
  TOOL="${entry%%@*}"
  log "Upgrading $TOOL..."
  if HOMEBREW_NO_AUTO_UPDATE=1 brew upgrade "$TOOL" 2>&1; then
    NEW_VER=$(brew info --json "$TOOL" 2>/dev/null | python3 -c "import json,sys; d=json.load(sys.stdin)[0]; print(d['installed'][0]['version'])" 2>/dev/null || echo "?")
    log "  $TOOL upgraded to $NEW_VER"
    UPGRADED+=("$entry→$NEW_VER")
  else
    log "  WARN: $TOOL upgrade failed"
    FAILED+=("$TOOL")
  fi
done

# --- Report -------------------------------------------------------------------

SUMMARY="tool-updater: upgraded=${UPGRADED[*]:-none} failed=${FAILED[*]:-none}"
log ""
log "=== Done === $SUMMARY"

RESULT="success"
[[ ${#FAILED[@]} -gt 0 ]] && RESULT="warning"

__rid="$(bd create "$SUMMARY" -t chore --ephemeral \
  -l type:plugin-run,plugin:tool-updater,result:$RESULT \
  --silent 2>/dev/null)" && [ -n "$__rid" ] && bd close "$__rid" --force 2>/dev/null || true

if [[ ${#FAILED[@]} -gt 0 ]]; then
  gt escalate "tool-updater: ${#FAILED[@]} tool(s) failed to upgrade: ${FAILED[*]}" \
    -s medium \
    --reason "Homebrew upgrade failed for: ${FAILED[*]}" 2>/dev/null || true
fi
