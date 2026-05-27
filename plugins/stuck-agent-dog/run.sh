#!/usr/bin/env bash
# stuck-agent-dog/run.sh — Context-aware stuck/crashed agent detection.
#
# SCOPE: Only polecats and deacon. NEVER touches crew, mayor, witness, or refinery.
# The daemon detects; this plugin inspects context before acting.

set -euo pipefail

TOWN_ROOT="${GT_TOWN_ROOT:-$(gt town root 2>/dev/null)}"
RIGS_JSON_PATH="${TOWN_ROOT}/mayor/rigs.json"

log() { echo "[stuck-agent-dog] $*"; }

# --- Enumerate agents ---------------------------------------------------------

log "=== Checking agent health ==="

if [ ! -f "$RIGS_JSON_PATH" ]; then
  log "SKIP: rigs.json not found"
  exit 0
fi

# Build rig_name|prefix mapping
RIG_PREFIX_MAP=$(jq -r '.rigs | to_entries[] | "\(.key)|\(.value.beads.prefix // .key)"' "$RIGS_JSON_PATH" 2>/dev/null)
if [ -z "$RIG_PREFIX_MAP" ]; then
  log "SKIP: no rigs in rigs.json"
  exit 0
fi

# --- Check polecat health ----------------------------------------------------

CRASHED=()
STUCK=()
HEALTHY=0

while IFS='|' read -r RIG PREFIX; do
  [ -z "$RIG" ] && continue
  POLECAT_DIR="$TOWN_ROOT/$RIG/polecats"
  [ -d "$POLECAT_DIR" ] || continue

  for PCAT_PATH in "$POLECAT_DIR"/*/; do
    [ -d "$PCAT_PATH" ] || continue
    PCAT_NAME=$(basename "$PCAT_PATH")
    SESSION_NAME="${PREFIX}-${PCAT_NAME}"

    if ! tmux has-session -t "$SESSION_NAME" 2>/dev/null; then
      # Session dead — check hook
      HOOK_OUTPUT=$(gt hook show "$RIG/polecats/$PCAT_NAME" 2>/dev/null | head -1)
      HOOK_BEAD=$(echo "$HOOK_OUTPUT" | grep -v '(empty)' | awk '{print $2}' || true)

      if [ -n "$HOOK_BEAD" ]; then
        # Check agent_state
        AGENT_STATE=$(bd show "$HOOK_BEAD" --json 2>/dev/null \
          | python3 -c "import json,sys; d=json.load(sys.stdin); print(d[0].get('status',''))" 2>/dev/null || echo "")

        case "$AGENT_STATE" in
          closed) log "  SKIP $SESSION_NAME: bead closed (completed normally)"; continue ;;
        esac

        CRASHED+=("$SESSION_NAME|$RIG|$PCAT_NAME|$HOOK_BEAD")
        log "  CRASHED: $SESSION_NAME (hook=$HOOK_BEAD)"
      fi
    else
      # Session alive — check process
      PANE_PID=$(tmux list-panes -t "$SESSION_NAME" -F '#{pane_pid}' 2>/dev/null | head -1)
      if [ -n "$PANE_PID" ]; then
        PROC_COMM=$(ps -o comm= -p "$PANE_PID" 2>/dev/null)
        if [ -z "$PROC_COMM" ]; then
          # Zombie: process dead, session alive
          HOOK_OUTPUT=$(gt hook show "$RIG/polecats/$PCAT_NAME" 2>/dev/null | head -1)
          HOOK_BEAD=$(echo "$HOOK_OUTPUT" | grep -v '(empty)' | awk '{print $2}' || true)
          if [ -n "$HOOK_BEAD" ]; then
            STUCK+=("$SESSION_NAME|$RIG|$PCAT_NAME|$HOOK_BEAD|agent_dead")
            log "  ZOMBIE: $SESSION_NAME (pid=$PANE_PID dead, hook=$HOOK_BEAD)"
          fi
        else
          HEALTHY=$((HEALTHY + 1))
        fi
      else
        HEALTHY=$((HEALTHY + 1))
      fi
    fi
  done
done <<< "$RIG_PREFIX_MAP"

log ""
log "Polecat health: ${#CRASHED[@]} crashed, ${#STUCK[@]} stuck, $HEALTHY healthy"

# --- Check deacon health -----------------------------------------------------

log ""
log "=== Deacon Health ==="

DEACON_SESSION="hq-deacon"
DEACON_ISSUE=""

if ! tmux has-session -t "$DEACON_SESSION" 2>/dev/null; then
  log "  CRASHED: Deacon session is dead"
  DEACON_ISSUE="crashed"
else
  DEACON_PID=$(tmux list-panes -t "$DEACON_SESSION" -F '#{pane_pid}' 2>/dev/null | head -1)
  DEACON_COMM=$(ps -o comm= -p "$DEACON_PID" 2>/dev/null)
  if [ -z "$DEACON_COMM" ]; then
    log "  ZOMBIE: Deacon process dead (pid=$DEACON_PID), session alive"
    DEACON_ISSUE="zombie"
  else
    log "  Process alive: pid=$DEACON_PID comm=$DEACON_COMM"
  fi

  # Heartbeat staleness alone is NOT "stuck". The Deacon is event-driven and
  # parks during await-signal idle — a stale heartbeat just means there was no
  # work to do, not that the Deacon died. Real death is already caught by the
  # session/process checks above (crashed/zombie). The Go daemon owns the
  # nudge/kill decision on heartbeat staleness, and it correctly skips when no
  # work is in flight (see internal/daemon/daemon.go:1600 idle-guard and the
  # threshold rationale in internal/deacon/heartbeat.go). Escalating here would
  # duplicate that check WITHOUT the idle-guard, producing recurring false
  # HIGH escalations. So: log a NOTICE for visibility only, never escalate.
  HEARTBEAT_FILE="$TOWN_ROOT/deacon/heartbeat.json"
  if [ -f "$HEARTBEAT_FILE" ]; then
    HEARTBEAT_TIME=$(stat -f %m "$HEARTBEAT_FILE" 2>/dev/null || stat -c %Y "$HEARTBEAT_FILE" 2>/dev/null)
    NOW=$(date +%s)
    HEARTBEAT_AGE=$(( NOW - HEARTBEAT_TIME ))

    if [ "$HEARTBEAT_AGE" -gt 7200 ]; then
      # NOTICE only — does NOT set DEACON_ISSUE, so no escalation. The daemon's
      # idle-guarded nudge/kill handles genuine staleness; this is for visibility.
      log "  NOTICE: Deacon heartbeat ${HEARTBEAT_AGE}s old (>2h) — likely await-signal idle, not stuck; daemon owns nudge/kill"
    else
      log "  OK: Deacon heartbeat ${HEARTBEAT_AGE}s old"
    fi
  fi
fi

# --- Mass death check ---------------------------------------------------------

TOTAL_ISSUES=$(( ${#CRASHED[@]} + ${#STUCK[@]} ))
if [ "$TOTAL_ISSUES" -ge 3 ]; then
  log ""
  log "MASS DEATH: $TOTAL_ISSUES agents down — escalating instead of restarting"
  gt escalate "Mass agent death: $TOTAL_ISSUES agents down" \
    -s CRITICAL 2>/dev/null || true
fi

# --- Take action --------------------------------------------------------------

# Crashed polecats: notify witness to restart
# Note: `"${arr[@]:-}"` expands an empty array to a single empty string under
# `set -u`, which would fire a phantom `RESTART_POLECAT: /` notification. The
# `${arr[@]+"${arr[@]}"}` form expands to nothing when the array is empty.
for ENTRY in ${CRASHED[@]+"${CRASHED[@]}"}; do
  IFS='|' read -r SESSION RIG PCAT HOOK <<< "$ENTRY"
  log "Requesting restart for $RIG/polecats/$PCAT (hook=$HOOK)"
  gt mail send "$RIG/witness" -s "RESTART_POLECAT: $RIG/$PCAT" --stdin <<BODY
Polecat $PCAT crash confirmed by stuck-agent-dog plugin.
hook_bead: $HOOK
action: restart requested
BODY
done

# Zombie polecats: kill zombie session, then request restart
for ENTRY in ${STUCK[@]+"${STUCK[@]}"}; do
  IFS='|' read -r SESSION RIG PCAT HOOK REASON <<< "$ENTRY"
  log "Killing zombie session $SESSION and requesting restart"
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  gt mail send "$RIG/witness" -s "RESTART_POLECAT: $RIG/$PCAT (zombie cleared)" --stdin <<BODY
Polecat $PCAT zombie session cleared by stuck-agent-dog plugin.
hook_bead: $HOOK
reason: $REASON
action: restart requested
BODY
done

# Deacon issues: escalate
if [ -n "$DEACON_ISSUE" ]; then
  log "Escalating deacon issue: $DEACON_ISSUE"
  gt escalate "Deacon $DEACON_ISSUE detected by stuck-agent-dog" -s HIGH 2>/dev/null || true
fi

# --- Report -------------------------------------------------------------------

SUMMARY="Agent health: ${#CRASHED[@]} crashed, ${#STUCK[@]} stuck, $HEALTHY healthy"
[ -n "$DEACON_ISSUE" ] && SUMMARY="$SUMMARY, deacon=$DEACON_ISSUE"
log ""
log "=== $SUMMARY ==="

bd create "stuck-agent-dog: $SUMMARY" -t chore --ephemeral \
  -l type:plugin-run,plugin:stuck-agent-dog,result:success \
  -d "$SUMMARY" --silent 2>/dev/null || true
