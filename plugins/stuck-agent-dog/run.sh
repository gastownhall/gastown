#!/usr/bin/env bash
# stuck-agent-dog/run.sh — Context-aware stuck/crashed agent detection.
#
# SCOPE: Only polecats and deacon. NEVER touches crew, mayor, witness, or refinery.
# The daemon detects; this plugin inspects context before acting.

set -euo pipefail

log() { echo "[stuck-agent-dog] $*"; }

TOWN_ROOT="${GT_TOWN_ROOT:-}"
if [ -z "$TOWN_ROOT" ]; then
  if ! TOWN_ROOT=$(gt town root 2>/dev/null); then
    log "SKIP: could not resolve town root"
    exit 0
  fi
fi

RIGS_JSON_PATH="${TOWN_ROOT}/rigs.json"
if [ ! -f "$RIGS_JSON_PATH" ] && [ -f "$TOWN_ROOT/mayor/rigs.json" ]; then
  RIGS_JSON_PATH="$TOWN_ROOT/mayor/rigs.json"
fi

integer_or_default() {
  local value="$1"
  local default="$2"

  case "$value" in
    ''|*[!0-9]*) echo "$default" ;;
    *) echo "$value" ;;
  esac
}

POLECAT_MAX_INACTIVITY="${GT_STUCK_AGENT_DOG_MAX_INACTIVITY:-0s}"
[ "$POLECAT_MAX_INACTIVITY" = "0" ] && POLECAT_MAX_INACTIVITY="0s"
DEACON_STALE_SECONDS=$(integer_or_default "${GT_STUCK_AGENT_DOG_DEACON_STALE_SECONDS:-}" 1200)
ACTIVITY_GRACE_SECONDS=$(integer_or_default "${GT_STUCK_AGENT_DOG_ACTIVITY_GRACE_SECONDS:-}" "$DEACON_STALE_SECONDS")
MASS_DEATH_THRESHOLD=$(integer_or_default "${GT_STUCK_AGENT_DOG_MASS_DEATH_THRESHOLD:-}" 3)

heartbeat_epoch() {
  local file="$1"
  local ts=""

  ts=$(jq -r '(.timestamp // empty) | sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601? // empty' "$file" 2>/dev/null || true)
  if [ -n "$ts" ]; then
    echo "$ts"
    return 0
  fi

  # Fallback for malformed legacy files: use mtime rather than failing open.
  # GNU stat (-c %Y) first: on GNU, 'stat -f' is filesystem mode and dumps a
  # multi-line "File: ..." block to stdout BEFORE failing, polluting the
  # command substitution and breaking downstream arithmetic (hq-wisp-0vrp).
  # BSD/macOS stat (-f %m) is the fallback.
  stat -c %Y "$file" 2>/dev/null || stat -f %m "$file" 2>/dev/null
}

has_in_progress_work() {
  local locations=("$TOWN_ROOT")
  local rig=""
  local loc=""
  local output=""
  local count=""

  while IFS='|' read -r rig _prefix; do
    [ -z "$rig" ] && continue
    [ -d "$TOWN_ROOT/$rig" ] && locations+=("$TOWN_ROOT/$rig")
  done <<< "$RIG_PREFIX_MAP"

  for loc in "${locations[@]}"; do
    output=$(cd "$loc" && bd list --status=in_progress --json --limit=1 2>/dev/null) || return 0
    count=$(printf '%s' "$output" | jq 'length' 2>/dev/null || echo 1)
    if [ "${count:-1}" -gt 0 ]; then
      return 0
    fi
  done

  return 1
}

# --- Beads resolution helpers -------------------------------------------------
# Plugin scripts may run outside a beads workspace. Resolve hook and status
# lookups from the target rig workspace, and make missing/inactive rigs
# non-fatal so one bad rig does not abort the dog under `set -e` (hq-9e770).

rig_workdir() {
  local rig="$1"

  if [ -d "$TOWN_ROOT/$rig/mayor/rig" ]; then
    printf '%s\n' "$TOWN_ROOT/$rig/mayor/rig"
    return 0
  fi

  if [ -d "$TOWN_ROOT/$rig" ]; then
    printf '%s\n' "$TOWN_ROOT/$rig"
    return 0
  fi

  return 1
}

rig_hook_bead() {
  local rig="$1" pcat="$2" dir=""

  if ! dir=$(rig_workdir "$rig"); then
    return 0
  fi

  ( cd "$dir" 2>/dev/null && gt hook show "$rig/polecats/$pcat" --json 2>/dev/null ) \
    | jq -r '.bead_id // empty' 2>/dev/null || true
}

rig_bead_status() {
  local rig="$1" bead="$2" dir=""

  if ! dir=$(rig_workdir "$rig"); then
    return 0
  fi

  ( cd "$dir" 2>/dev/null && bd show "$bead" --json 2>/dev/null ) \
    | jq -r '.[0].status // empty' 2>/dev/null || true
}

# DEFERRED-TRACKER convention (2026-07-12, capital/witness request hq-wisp-l4zal):
# some polecats are deliberately stopped while their hook bead stays OPEN as a
# long-lived tracker (e.g. awaiting an epic close or a human gate sign-off).
# The witness marks such beads with a comment containing "DEFERRED-TRACKER".
# The marker is visible in plain `bd show` output (comments section), NOT in
# the --json issue object. Such beads are never restartable.
bead_deferred_tracker() {
  local rig="$1" bead="$2" dir=""

  if ! dir=$(rig_workdir "$rig"); then
    return 1
  fi

  ( cd "$dir" 2>/dev/null && bd show "$bead" 2>/dev/null ) \
    | grep -q "DEFERRED-TRACKER"
}

bead_restartable() {
  local session="$1" rig="$2" bead="$3" status=""

  status=$(rig_bead_status "$rig" "$bead")

  case "$status" in
    open|hooked|in_progress)
      if bead_deferred_tracker "$rig" "$bead"; then
        log "  DEFERRED-TRACKER: $session hook=$bead deliberately stopped, bead open as tracker — not restartable"
        return 1
      fi
      return 0 ;;
    closed) log "  SKIP $session: bead closed (completed normally)" ;;
    "") log "  SKIP $session: hook=$bead status unavailable" ;;
    *) log "  SKIP $session: hook=$bead status=$status not actionable" ;;
  esac

  return 1
}

session_health_status() {
  local session_name="$1"
  local health_json=""
  local status=""

  health_json=$(gt session health "$session_name" --json --max-inactivity "$POLECAT_MAX_INACTIVITY" 2>/dev/null) || return 1
  status=$(printf '%s' "$health_json" | jq -r '.status // empty' 2>/dev/null || true)
  [ -n "$status" ] || return 1
  printf '%s\n' "$status"
}

# --- Enumerate agents ---------------------------------------------------------

log "=== Checking agent health ==="

if [ ! -f "$RIGS_JSON_PATH" ]; then
  log "SKIP: rigs.json not found"
  exit 0
fi

# Build rig_name|prefix mapping
if ! RIG_PREFIX_MAP=$(jq -r '
  if (.rigs | type) == "object" then
    .rigs | to_entries[] | "\(.key)|\(.value.beads.prefix // .key)"
  else
    empty
  end
' "$RIGS_JSON_PATH" 2>/dev/null); then
  log "SKIP: could not parse rigs.json"
  exit 0
fi

RIG_PREFIX_MAP=$(printf '%s\n' "$RIG_PREFIX_MAP" | awk -F'|' 'NF >= 2 && $1 != "" && $2 != ""')
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

    HEALTH_STATUS=$(session_health_status "$SESSION_NAME" || true)
    case "$HEALTH_STATUS" in
      healthy)
        HEALTHY=$((HEALTHY + 1))
        ;;
      agent-dead|agent_dead)
        HOOK_BEAD=$(rig_hook_bead "$RIG" "$PCAT_NAME")
        if [ -n "$HOOK_BEAD" ] && bead_restartable "$SESSION_NAME" "$RIG" "$HOOK_BEAD"; then
          STUCK+=("$SESSION_NAME|$RIG|$PCAT_NAME|$HOOK_BEAD|agent_dead")
          log "  ZOMBIE: $SESSION_NAME (agent runtime dead, hook=$HOOK_BEAD)"
        fi
        ;;
      agent-hung|agent_hung)
        # A live runtime with quiet output can be a long research turn. Do not
        # kill it here; operators can tune the threshold and inspect manually.
        HEALTHY=$((HEALTHY + 1))
        log "  OBSERVE: $SESSION_NAME runtime alive but inactive beyond $POLECAT_MAX_INACTIVITY; not restarting"
        ;;
      session-dead|session_dead)
        HOOK_BEAD=$(rig_hook_bead "$RIG" "$PCAT_NAME")
        if [ -n "$HOOK_BEAD" ] && bead_restartable "$SESSION_NAME" "$RIG" "$HOOK_BEAD"; then
          CRASHED+=("$SESSION_NAME|$RIG|$PCAT_NAME|$HOOK_BEAD")
          log "  CRASHED: $SESSION_NAME (hook=$HOOK_BEAD)"
        fi
        ;;
      *)
        log "  SKIP $SESSION_NAME: central liveness probe inconclusive"
        ;;
    esac
  done
done <<< "$RIG_PREFIX_MAP"

log ""
log "Polecat health: ${#CRASHED[@]} crashed, ${#STUCK[@]} stuck, $HEALTHY healthy"

# --- Check deacon health -----------------------------------------------------

log ""
log "=== Deacon Health ==="

DEACON_SESSION="hq-deacon"
DEACON_ISSUE=""
DEACON_DIVERGENCE=""
DEACON_PROCESS_ALIVE=0

if ! tmux has-session -t "$DEACON_SESSION" 2>/dev/null; then
  log "  CRASHED: Deacon session is dead"
  DEACON_ISSUE="crashed"
else
  DEACON_PID=$(tmux list-panes -t "$DEACON_SESSION" -F '#{pane_pid}' 2>/dev/null | head -1 || true)
  DEACON_COMM=$(ps -o comm= -p "$DEACON_PID" 2>/dev/null || true)
  if [ -z "$DEACON_COMM" ]; then
    log "  ZOMBIE: Deacon process dead (pid=$DEACON_PID), session alive"
    DEACON_ISSUE="zombie"
  else
    log "  Process alive: pid=$DEACON_PID comm=$DEACON_COMM"
    DEACON_PROCESS_ALIVE=1
  fi

  HEARTBEAT_FILE="$TOWN_ROOT/deacon/heartbeat.json"
  if [ -z "$DEACON_ISSUE" ] && [ -f "$HEARTBEAT_FILE" ]; then
    HEARTBEAT_TIME=$(heartbeat_epoch "$HEARTBEAT_FILE" || true)
    NOW=$(date +%s)
    HEARTBEAT_AGE=$(( NOW - ${HEARTBEAT_TIME:-0} ))

    if [ "$HEARTBEAT_AGE" -gt "$DEACON_STALE_SECONDS" ]; then
      # Cross-check tmux activity before declaring stuck: heartbeat.json is
      # only ONE of three heartbeat stores (hq-qxl9). A live session with
      # recent activity means the file-write path diverged (e.g. a long
      # turn, or the agent refreshing a different store) — not a stuck
      # Deacon. Escalating that as stuck caused a false-positive storm.
      ACTIVITY_TIME=$(tmux display-message -t "$DEACON_SESSION" -p '#{window_activity}' 2>/dev/null || true)
      case "$ACTIVITY_TIME" in
        ''|*[!0-9]*) ACTIVITY_AGE="" ;;
        *) ACTIVITY_AGE=$(( NOW - ACTIVITY_TIME )) ;;
      esac
      if [ -n "$ACTIVITY_AGE" ] && [ "$ACTIVITY_AGE" -le "$ACTIVITY_GRACE_SECONDS" ]; then
        log "  DIVERGENCE: heartbeat file stale (${HEARTBEAT_AGE}s) but session active ${ACTIVITY_AGE}s ago — write divergence, not stuck"
        DEACON_DIVERGENCE="heartbeat_write_divergence_${HEARTBEAT_AGE}s_active_${ACTIVITY_AGE}s"
      elif [ "$DEACON_PROCESS_ALIVE" -eq 1 ] && ! has_in_progress_work; then
        log "  SKIP: Deacon heartbeat stale (${HEARTBEAT_AGE}s old) but process is alive and no in_progress work exists"
      else
        log "  STUCK: Deacon heartbeat stale (${HEARTBEAT_AGE}s old, >${DEACON_STALE_SECONDS}s threshold), no recent session activity"
        DEACON_ISSUE="stuck_heartbeat_${HEARTBEAT_AGE}s"
      fi
    else
      log "  OK: Deacon heartbeat ${HEARTBEAT_AGE}s old"
    fi
  fi
fi

# --- Mass death check ---------------------------------------------------------

TOTAL_ISSUES=$(( ${#CRASHED[@]} + ${#STUCK[@]} ))
MASS_DEATH=0
if [ "$TOTAL_ISSUES" -ge "$MASS_DEATH_THRESHOLD" ]; then
  MASS_DEATH=1
  log ""
  log "MASS DEATH: $TOTAL_ISSUES agents down — escalating instead of restarting"
  gt escalate "Mass agent death: $TOTAL_ISSUES agents down" \
    -s CRITICAL 2>/dev/null || true
fi

# --- Take action --------------------------------------------------------------

if [ "$MASS_DEATH" -eq 1 ]; then
  log "Skipping per-agent restart/kill actions during mass-death escalation"
else
  # Crashed polecats: notify witness to restart
  # Note: `"${arr[@]:-}"` expands an empty array to a single empty string under
  # `set -u`, which would fire a phantom `RESTART_POLECAT: /` notification. The
  # `${arr[@]+"${arr[@]}"}` form expands to nothing when the array is empty.
  for ENTRY in ${CRASHED[@]+"${CRASHED[@]}"}; do
    IFS='|' read -r SESSION RIG PCAT HOOK <<< "$ENTRY"
    # Mailer-time re-check (op-omcx, defense-in-depth): the marker can land
    # between detection and action, and this mailer is where every historical
    # bypass actually fired. Refuse DEFERRED-TRACKER beads here too.
    if bead_deferred_tracker "$RIG" "$HOOK"; then
      log "  MAILER-GATE: $SESSION hook=$HOOK carries DEFERRED-TRACKER — refusing RESTART_POLECAT mail"
      continue
    fi
    log "Requesting restart for $RIG/polecats/$PCAT (hook=$HOOK)"
    gt mail send "$RIG/witness" -s "RESTART_POLECAT: $RIG/$PCAT" --stdin <<BODY || log "  WARN: restart mail failed for $RIG/$PCAT"
Polecat $PCAT crash confirmed by stuck-agent-dog plugin.
hook_bead: $HOOK
action: restart requested
BODY
  done

  # Zombie polecats: kill zombie session, then request restart
  for ENTRY in ${STUCK[@]+"${STUCK[@]}"}; do
    IFS='|' read -r SESSION RIG PCAT HOOK REASON <<< "$ENTRY"
    # Mailer-time re-check (op-omcx): see the CRASHED loop above.
    if bead_deferred_tracker "$RIG" "$HOOK"; then
      log "  MAILER-GATE: $SESSION hook=$HOOK carries DEFERRED-TRACKER — refusing RESTART_POLECAT mail (zombie session left untouched)"
      continue
    fi
    log "Killing zombie session $SESSION and requesting restart"
    tmux kill-session -t "$SESSION" 2>/dev/null || true
    gt mail send "$RIG/witness" -s "RESTART_POLECAT: $RIG/$PCAT (zombie cleared)" --stdin <<BODY || log "  WARN: restart mail failed for $RIG/$PCAT"
Polecat $PCAT zombie session cleared by stuck-agent-dog plugin.
hook_bead: $HOOK
reason: $REASON
action: restart requested
BODY
  done
fi

# Deacon issues: escalate
if [ -n "$DEACON_ISSUE" ]; then
	log "Escalating deacon issue: $DEACON_ISSUE"
	DEACON_SEVERITY="HIGH"
	DEACON_FINGERPRINT="stuck-agent-dog:deacon:$DEACON_ISSUE"
	case "$DEACON_ISSUE" in
		stuck_heartbeat_*)
			DEACON_SEVERITY="MEDIUM"
			DEACON_FINGERPRINT="stuck-agent-dog:deacon:stuck-heartbeat"
			;;
	esac
	gt escalate "Deacon $DEACON_ISSUE detected by stuck-agent-dog" \
		-s "$DEACON_SEVERITY" \
		--source "plugin:stuck-agent-dog" \
		--fingerprint "$DEACON_FINGERPRINT" 2>/dev/null || true
fi

# --- Report -------------------------------------------------------------------

SUMMARY="Agent health: ${#CRASHED[@]} crashed, ${#STUCK[@]} stuck, $HEALTHY healthy"
[ -n "$DEACON_ISSUE" ] && SUMMARY="$SUMMARY, deacon=$DEACON_ISSUE"
[ -n "$DEACON_DIVERGENCE" ] && SUMMARY="$SUMMARY, deacon=$DEACON_DIVERGENCE (not escalated)"
log ""
log "=== $SUMMARY ==="

gt plugin record-run --plugin stuck-agent-dog --result success \
  --title "stuck-agent-dog: $SUMMARY" --description "$SUMMARY" >/dev/null 2>&1 || true
