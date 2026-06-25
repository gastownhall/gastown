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

integer_or_default() {
  local value="$1"
  local default="$2"

  case "$value" in
    ''|*[!0-9]*) echo "$default" ;;
    *) echo "$value" ;;
  esac
}

positive_integer_or_default() {
  local value="$1"
  local default="$2"

  case "$value" in
    ''|*[!0-9]*) echo "$default" ;;
    *)
      if [ "$value" -ge 1 ]; then
        echo "$value"
      else
        echo "$default"
      fi
      ;;
  esac
}

POLECAT_MAX_INACTIVITY="${GT_STUCK_AGENT_DOG_MAX_INACTIVITY:-0s}"
[ "$POLECAT_MAX_INACTIVITY" = "0" ] && POLECAT_MAX_INACTIVITY="0s"
DEACON_STALE_SECONDS=$(integer_or_default "${GT_STUCK_AGENT_DOG_DEACON_STALE_SECONDS:-}" 1200)
ACTIVITY_GRACE_SECONDS=$(integer_or_default "${GT_STUCK_AGENT_DOG_ACTIVITY_GRACE_SECONDS:-}" "$DEACON_STALE_SECONDS")
MASS_DEATH_THRESHOLD=$(positive_integer_or_default "${GT_STUCK_AGENT_DOG_MASS_DEATH_THRESHOLD:-}" 3)

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

rig_hook_assignment() {
  local rig="$1" pcat="$2" dir=""
  local hook_json="" bead="" status=""

  if ! dir=$(rig_workdir "$rig"); then
    return 0
  fi

  hook_json=$( ( cd "$dir" 2>/dev/null && gt hook show "$rig/polecats/$pcat" --json 2>/dev/null ) || true )
  if [ -z "$hook_json" ]; then
    return 0
  fi

  bead=$(printf '%s' "$hook_json" | jq -r '.bead_id // empty' 2>/dev/null || true)
  status=$(printf '%s' "$hook_json" | jq -r '.status // empty' 2>/dev/null || true)
  [ -n "$bead" ] || return 0

  printf '%s|%s\n' "$bead" "$status"
}

hook_restartable() {
  local session="$1" bead="$2" status="$3"

  case "$status" in
    hooked|in_progress) [ -n "$bead" ] && return 0 ;;
    empty|"") log "  SKIP $session: no active hook" ;;
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

operational_rig_prefix_map() {
  local rig_json="" rows=""

  if ! rig_json=$(cd "$TOWN_ROOT" 2>/dev/null && gt rig list --json 2>/dev/null); then
    log "SKIP: gt rig list --json unavailable; cannot verify operational rig state" >&2
    return 0
  fi

  if ! rows=$(printf '%s' "$rig_json" | jq -r '
    if type == "array" then .[] else empty end
    | select((.status // "" | ascii_downcase) == "operational")
    | select((.name // "") != "" and (.beads_prefix // "") != "")
    | "\(.name)|\(.beads_prefix)"
  ' 2>/dev/null); then
    log "SKIP: gt rig list --json not parseable; cannot verify operational rig state" >&2
    return 0
  fi

  printf '%s\n' "$rows" | awk -F'|' 'NF >= 2 && $1 != "" && $2 != ""'
}

confirm_current_polecat_outage() {
  local session="$1" rig="$2" pcat="$3"
  local health_status="" hook_assignment="" hook_bead="" hook_status=""

  health_status=$(session_health_status "$session" || true)
  case "$health_status" in
    session-dead|session_dead)
      hook_assignment=$(rig_hook_assignment "$rig" "$pcat" || true)
      IFS='|' read -r hook_bead hook_status <<< "$hook_assignment"
      if hook_restartable "$session" "$hook_bead" "$hook_status"; then
        CONFIRMED_CRASHED+=("$session|$rig|$pcat|$hook_bead")
      fi
      ;;
    agent-dead|agent_dead)
      hook_assignment=$(rig_hook_assignment "$rig" "$pcat" || true)
      IFS='|' read -r hook_bead hook_status <<< "$hook_assignment"
      if hook_restartable "$session" "$hook_bead" "$hook_status"; then
        CONFIRMED_STUCK+=("$session|$rig|$pcat|$hook_bead|agent_dead")
      fi
      ;;
    healthy|agent-hung|agent_hung)
      log "  NOTICE: $session recovered before mass-death escalation (health=$health_status)"
      ;;
    *)
      log "  NOTICE: $session not confirmed before mass-death escalation (health=${health_status:-unknown})"
      ;;
  esac
}

confirm_polecat_outages() {
  local entry="" session="" rig="" pcat="" hook="" reason=""

  CONFIRMED_CRASHED=()
  CONFIRMED_STUCK=()

  for entry in ${CRASHED[@]+"${CRASHED[@]}"}; do
    [ -n "$entry" ] || continue
    IFS='|' read -r session rig pcat hook <<< "$entry"
    confirm_current_polecat_outage "$session" "$rig" "$pcat"
  done

  for entry in ${STUCK[@]+"${STUCK[@]}"}; do
    [ -n "$entry" ] || continue
    IFS='|' read -r session rig pcat hook reason <<< "$entry"
    confirm_current_polecat_outage "$session" "$rig" "$pcat"
  done
}

# --- Enumerate agents ---------------------------------------------------------

log "=== Checking agent health ==="

# Build operational rig_name|prefix mapping. The rig registry is the live
# parked/docked filter; if it is unavailable, fail closed.
RIG_PREFIX_MAP=$(operational_rig_prefix_map)
if [ -z "$RIG_PREFIX_MAP" ]; then
  log "SKIP: no operational rigs found"
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
        HOOK_ASSIGNMENT=$(rig_hook_assignment "$RIG" "$PCAT_NAME")
        IFS='|' read -r HOOK_BEAD HOOK_STATUS <<< "$HOOK_ASSIGNMENT"
        if hook_restartable "$SESSION_NAME" "$HOOK_BEAD" "$HOOK_STATUS"; then
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
        HOOK_ASSIGNMENT=$(rig_hook_assignment "$RIG" "$PCAT_NAME")
        IFS='|' read -r HOOK_BEAD HOOK_STATUS <<< "$HOOK_ASSIGNMENT"
        if hook_restartable "$SESSION_NAME" "$HOOK_BEAD" "$HOOK_STATUS"; then
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
  log ""
  log "Mass-death candidate threshold reached ($TOTAL_ISSUES); re-checking live health before escalation"
  confirm_polecat_outages
  CRASHED=("${CONFIRMED_CRASHED[@]}")
  STUCK=("${CONFIRMED_STUCK[@]}")
  CONFIRMED_TOTAL=$(( ${#CRASHED[@]} + ${#STUCK[@]} ))

  if [ "$CONFIRMED_TOTAL" -ge "$MASS_DEATH_THRESHOLD" ]; then
    MASS_DEATH=1
    log "MASS DEATH: $CONFIRMED_TOTAL agents down confirmed — escalating instead of restarting"
    gt escalate "Mass agent death: $CONFIRMED_TOTAL agents down" \
      -s CRITICAL \
      --source "plugin:stuck-agent-dog" \
      --fingerprint "stuck-agent-dog:mass-death" 2>/dev/null || true
  else
    log "NOTICE: mass-death candidates dropped to $CONFIRMED_TOTAL after live re-check; no CRITICAL escalation"
  fi
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
