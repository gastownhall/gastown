+++
name = "stuck-agent-dog"
description = "Context-aware stuck/crashed agent detection and restart for polecats and deacons"
version = 1

[gate]
type = "cooldown"
duration = "5m"

[tracking]
labels = ["plugin:stuck-agent-dog", "category:health"]
digest = true

[execution]
timeout = "5m"
notify_on_failure = true
severity = "high"
+++

# Stuck Agent Dog

Detects stuck or crashed polecats and deacons by inspecting tmux session context
before taking action. Unlike the daemon's blind kill-and-restart approach, this
plugin checks whether an agent is truly unresponsive before restarting.

**Design principle**: The daemon should NEVER kill workers. It detects and logs.
This plugin (running as a Dog agent with AI judgment) makes the restart decision
after inspecting tmux pane output for signs of life.

Reference: WAR-ROOM-SERIAL-KILLER.md, commit f3d47a96.

## Scope — What You May and May NOT Touch

**IN SCOPE** (these are the ONLY sessions this plugin may inspect or act on):
- Polecat sessions (`<prefix>-<name>`, e.g. `gt-minuteman`)
- Deacon session (`hq-deacon`)

**OUT OF SCOPE — NEVER touch these, under any circumstances:**
- **Crew sessions** (`<rig>-crew-<name>`, e.g. `gastown-crew-bear`). Crew lifecycle
  is managed by the overseer (human), not dogs. Crew members are persistent,
  long-lived, and user-managed. A crew session that looks idle is NOT stuck — it
  is waiting for its human. Killing a crew session destroys the overseer's active
  workspace and is a **critical incident**.
- **Mayor session** (`hq-mayor`)
- **Witness sessions** (`<rig>-witness`)
- **Refinery sessions** (`<rig>-refinery`)
- Any session not explicitly enumerated by the bash scripts in Steps 1-3

**This scope is absolute.** Do NOT extend it based on your own judgment. The bash
scripts enumerate exactly the sessions you should check. If a session does not
appear in `CRASHED[]` or `STUCK[]` arrays, it does not exist for your purposes.

## Step 1: Enumerate agents to check

Gather all polecats and the deacon session. We check crashed sessions
(`session-dead`, work on hook) and confirmed zombie sessions (`agent-dead`).
`agent-hung` is observe-only for polecats.

```bash
echo "=== Stuck Agent Dog: Checking agent health ==="

TOWN_ROOT="$HOME/gt"

# Read the live rig registry for operational rig names and beads prefixes.
# CRITICAL: We need both the rig name (for filesystem paths like $TOWN_ROOT/$RIG/polecats/)
# and the beads prefix (for tmux session names like $PREFIX-$NAME).
# These can differ — e.g. rig "cfutons" may have prefix "CF".
if ! RIG_JSON=$(gt rig list --json 2>/dev/null); then
  echo "SKIP: gt rig list --json unavailable; cannot verify operational rig state"
  exit 0
fi

if ! RIG_PREFIX_MAP=$(printf '%s' "$RIG_JSON" | jq -r '
  if type == "array" then .[] else empty end
  | select((.status // "" | ascii_downcase) == "operational")
  | select((.name // "") != "" and (.beads_prefix // "") != "")
  | "\(.name)|\(.beads_prefix)"
' 2>/dev/null); then
  echo "SKIP: gt rig list --json not parseable; cannot verify operational rig state"
  exit 0
fi

# Filter out any malformed/blank rows so partial registry state fails safe.
RIG_PREFIX_MAP=$(printf '%s\n' "$RIG_PREFIX_MAP" | awk -F'|' 'NF >= 2 && $1 != "" && $2 != ""')
if [ -z "$RIG_PREFIX_MAP" ]; then
  echo "SKIP: no operational rigs found"
  exit 0
fi
```

## Step 2: Check polecat health

For each rig, enumerate polecats and check their session status.
A polecat is a concern if:
- `gt hook show --json` reports active work with status `hooked` or `in_progress`
- Its central runtime-aware health is `session-dead` OR `agent-dead`

Polecat liveness must use `gt session health`, which wraps the central
`tmux.CheckSessionHealth` path. That path reads `GT_PROCESS_NAMES`, `GT_AGENT`,
and `GT_PANE_ID`, so opencode/node/bun detection stays in the shared runtime
configuration instead of a plugin-local process regex. Treat `agent-hung` as
observe-only for polecats; quiet OpenCode research can be legitimate live work.

```bash
CRASHED=()
STUCK=()
HEALTHY=0

while IFS='|' read -r RIG PREFIX; do
  [ -z "$RIG" ] && continue
  # List polecat directories
  POLECAT_DIR="$TOWN_ROOT/$RIG/polecats"
  [ -d "$POLECAT_DIR" ] || continue

  for PCAT_PATH in "$POLECAT_DIR"/*/; do
    [ -d "$PCAT_PATH" ] || continue
    PCAT_NAME=$(basename "$PCAT_PATH")
    # Use beads prefix (not rig name) for tmux session name
    SESSION_NAME="${PREFIX}-${PCAT_NAME}"

    HEALTH_STATUS=$(gt session health "$SESSION_NAME" --json --max-inactivity "${GT_STUCK_AGENT_DOG_MAX_INACTIVITY:-0s}" 2>/dev/null \
      | jq -r '.status // empty' 2>/dev/null || true)

    case "$HEALTH_STATUS" in
      healthy)
        HEALTHY=$((HEALTHY + 1))
        ;;
      session-dead)
        # Check hook/status through the target rig workspace before acting.
        # Only hook-show statuses hooked/in_progress are restartable.
        HOOK_ASSIGNMENT=$(rig_hook_assignment "$RIG" "$PCAT_NAME")
        IFS='|' read -r HOOK_BEAD HOOK_STATUS <<< "$HOOK_ASSIGNMENT"
        if hook_restartable "$SESSION_NAME" "$HOOK_BEAD" "$HOOK_STATUS"; then
          CRASHED+=("$SESSION_NAME|$RIG|$PCAT_NAME|$HOOK_BEAD")
          echo "  CRASHED: $SESSION_NAME (hook=$HOOK_BEAD)"
        fi
        ;;
      agent-dead)
        HOOK_ASSIGNMENT=$(rig_hook_assignment "$RIG" "$PCAT_NAME")
        IFS='|' read -r HOOK_BEAD HOOK_STATUS <<< "$HOOK_ASSIGNMENT"
        if hook_restartable "$SESSION_NAME" "$HOOK_BEAD" "$HOOK_STATUS"; then
          STUCK+=("$SESSION_NAME|$RIG|$PCAT_NAME|$HOOK_BEAD|agent_dead")
          echo "  ZOMBIE: $SESSION_NAME (agent runtime dead, hook=$HOOK_BEAD)"
        fi
        ;;
      agent-hung)
        HEALTHY=$((HEALTHY + 1))
        echo "  OBSERVE: $SESSION_NAME runtime alive but inactive; not restarting"
        ;;
      *)
        echo "  SKIP $SESSION_NAME: central liveness probe inconclusive"
        ;;
    esac
  done
done <<< "$RIG_PREFIX_MAP"

echo ""
echo "Health summary: ${#CRASHED[@]} crashed, ${#STUCK[@]} stuck, $HEALTHY healthy"
```

## Step 3: Check deacon health

The deacon session is `hq-deacon`. Check heartbeat staleness from the JSON
`timestamp` field in `deacon/heartbeat.json` (fall back to file mtime only if
the timestamp is missing or malformed). A live Deacon with no `in_progress` work
is not an actionable stuck-heartbeat event; log and skip so idle patrol backoff
does not produce escalation noise.

```bash
echo ""
echo "=== Deacon Health ==="

DEACON_SESSION="hq-deacon"
DEACON_ISSUE=""
DEACON_DIVERGENCE=""
DEACON_PROCESS_ALIVE=0

if ! tmux has-session -t "$DEACON_SESSION" 2>/dev/null; then
  echo "  CRASHED: Deacon session is dead"
  DEACON_ISSUE="crashed"
else
  DEACON_PID=$(tmux list-panes -t "$DEACON_SESSION" -F '#{pane_pid}' 2>/dev/null | head -1 || true)
  DEACON_COMM=$(ps -o comm= -p "$DEACON_PID" 2>/dev/null || true)
  if [ -z "$DEACON_COMM" ]; then
    echo "  ZOMBIE: Deacon process dead (pid=$DEACON_PID), session alive"
    DEACON_ISSUE="zombie"
  else
    echo "  Process alive: pid=$DEACON_PID comm=$DEACON_COMM"
    DEACON_PROCESS_ALIVE=1
  fi

  HEARTBEAT_FILE="$TOWN_ROOT/deacon/heartbeat.json"
  if [ -z "$DEACON_ISSUE" ] && [ -f "$HEARTBEAT_FILE" ]; then
    HEARTBEAT_TIME=$(heartbeat_epoch "$HEARTBEAT_FILE" || true)
    NOW=$(date +%s)
    HEARTBEAT_AGE=$(( NOW - ${HEARTBEAT_TIME:-0} ))

    if [ "$HEARTBEAT_AGE" -gt "${GT_STUCK_AGENT_DOG_DEACON_STALE_SECONDS:-1200}" ]; then
      ACTIVITY_TIME=$(tmux display-message -t "$DEACON_SESSION" -p '#{window_activity}' 2>/dev/null || true)
      case "$ACTIVITY_TIME" in
        ''|*[!0-9]*) ACTIVITY_AGE="" ;;
        *) ACTIVITY_AGE=$(( NOW - ACTIVITY_TIME )) ;;
      esac
      if [ -n "$ACTIVITY_AGE" ] && [ "$ACTIVITY_AGE" -le "${GT_STUCK_AGENT_DOG_ACTIVITY_GRACE_SECONDS:-1200}" ]; then
        echo "  DIVERGENCE: heartbeat file stale (${HEARTBEAT_AGE}s) but session active ${ACTIVITY_AGE}s ago — write divergence, not stuck"
        DEACON_DIVERGENCE="heartbeat_write_divergence_${HEARTBEAT_AGE}s_active_${ACTIVITY_AGE}s"
      elif [ "$DEACON_PROCESS_ALIVE" -eq 1 ] && ! has_in_progress_work; then
        echo "  SKIP: Deacon heartbeat stale (${HEARTBEAT_AGE}s old) but process is alive and no in_progress work exists"
      else
        echo "  STUCK: Deacon heartbeat stale (${HEARTBEAT_AGE}s old, no recent session activity)"
        DEACON_ISSUE="stuck_heartbeat_${HEARTBEAT_AGE}s"
      fi
    else
      echo "  OK: Deacon heartbeat ${HEARTBEAT_AGE}s old"
    fi
  fi
fi
```

## Step 4: Inspect context before acting (AI judgment)

**This is the key difference from daemon blind-kill.** For each crashed or stuck
agent, inspect the tmux pane context to determine if restart is appropriate.

**SCOPE REMINDER: You may ONLY act on entries in the `CRASHED[]` and `STUCK[]`
arrays populated by Steps 2-3. These arrays contain ONLY polecats and deacon.
Do NOT inspect, evaluate, or act on ANY other sessions (crew, mayor, witness,
refinery). If you find yourself considering a session not in these arrays, STOP.**

**You (the dog agent) must evaluate each case:**

For CRASHED agents (session dead, active work on hook):
- This is almost always a legitimate crash needing restart
- If `gt hook show --json` no longer reports `hooked|in_progress`, skip it.
- Do not do a separate `bd show` status lookup; hook state is the active-work authority.

For STUCK agents (session alive, agent dead):
- Kill the zombie session, then restart
- `agent-hung` is not STUCK for polecats; central health keeps that observe-only.

For DEACON stuck (stale heartbeat):
- Capture pane output: `tmux capture-pane -t hq-deacon -p -S -20`
- If output shows active work (recent timestamps, command output), the heartbeat
  file may just be stale — nudge instead of kill
- If output shows no recent activity, escalation is warranted
- Use a stable escalation fingerprint (`stuck-agent-dog:deacon:stuck-heartbeat`)
  for stale-heartbeat events; do not include the age seconds in the fingerprint.

**Decision framework:**
1. If central health is `session-dead` and hook status is `hooked|in_progress` → request restart
2. If central health is `agent-dead` and hook status is `hooked|in_progress` → clear zombie, request restart
3. If central health is `agent-hung` → observe/report only; do not restart polecat research sessions
4. If confirmed mass death remains after live re-check (threshold default 3) → escalate and skip all per-agent actions

## Step 5: Mass death check

If multiple active polecats crash in the same cycle, this may indicate a
systemic issue (Dolt outage, OOM, etc.). The executable script re-checks live
health and active hook state before CRITICAL escalation. If the confirmed count
drops below threshold, normal per-agent action proceeds for the remaining
confirmed outages.

```bash
TOTAL_ISSUES=$(( ${#CRASHED[@]} + ${#STUCK[@]} ))
MASS_DEATH=0
if [ "$TOTAL_ISSUES" -ge "$MASS_DEATH_THRESHOLD" ]; then
  confirm_polecat_outages
  CRASHED=("${CONFIRMED_CRASHED[@]}")
  STUCK=("${CONFIRMED_STUCK[@]}")
  CONFIRMED_TOTAL=$(( ${#CRASHED[@]} + ${#STUCK[@]} ))

  if [ "$CONFIRMED_TOTAL" -ge "$MASS_DEATH_THRESHOLD" ]; then
    MASS_DEATH=1
    echo "MASS DEATH: $CONFIRMED_TOTAL agents down confirmed — escalating"
    gt escalate "Mass agent death: $CONFIRMED_TOTAL agents down" \
      -s CRITICAL \
      --source "plugin:stuck-agent-dog" \
      --fingerprint "stuck-agent-dog:mass-death"
    echo "Skipping per-agent restart/kill actions during mass-death escalation"
  fi
fi
```

## Step 6: Take action

For each agent requiring restart:

```bash
if [ "$MASS_DEATH" -eq 1 ]; then
  echo "Skipping per-agent restart/kill actions during mass-death escalation"
else
# For crashed polecats — notify witness to handle restart
for ENTRY in "${CRASHED[@]}"; do
  IFS='|' read -r SESSION RIG PCAT HOOK <<< "$ENTRY"

  echo "Requesting restart for $RIG/polecats/$PCAT (hook=$HOOK)"

  gt mail send "$RIG/witness" \
    -s "RESTART_POLECAT: $RIG/$PCAT" \
    --stdin <<BODY
Polecat $PCAT crash confirmed by stuck-agent-dog plugin.
Context-aware inspection completed — agent is genuinely dead.

hook_bead: $HOOK
action: restart requested

Please restart this polecat session.
BODY

done

# For zombie polecats — kill zombie session first, then request restart
for ENTRY in "${STUCK[@]}"; do
  IFS='|' read -r SESSION RIG PCAT HOOK REASON <<< "$ENTRY"

  echo "Killing zombie session $SESSION and requesting restart"
  tmux kill-session -t "$SESSION" 2>/dev/null || true

  gt mail send "$RIG/witness" \
    -s "RESTART_POLECAT: $RIG/$PCAT (zombie cleared)" \
    --stdin <<BODY
Polecat $PCAT zombie session cleared by stuck-agent-dog plugin.
Session was alive but agent process was dead.

hook_bead: $HOOK
reason: $REASON
action: restart requested

Please restart this polecat session.
BODY

done
fi

# For deacon issues
if [ -n "$DEACON_ISSUE" ]; then
  echo "Escalating deacon issue: $DEACON_ISSUE"
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
    --fingerprint "$DEACON_FINGERPRINT"
fi
```

## Record Result

```bash
SUMMARY="Agent health check: ${#CRASHED[@]} crashed, ${#STUCK[@]} stuck, $HEALTHY healthy"
if [ -n "$DEACON_ISSUE" ]; then
  SUMMARY="$SUMMARY, deacon=$DEACON_ISSUE"
fi
echo "=== $SUMMARY ==="
```

On success (no issues or issues handled):
```bash
gt plugin record-run --plugin stuck-agent-dog --result success \
  --title "stuck-agent-dog: $SUMMARY" --description "$SUMMARY" >/dev/null 2>&1 || true
```

On failure:
```bash
gt plugin record-run --plugin stuck-agent-dog --result failure \
  --title "stuck-agent-dog: FAILED" \
  --description "Agent health check failed: $ERROR" >/dev/null 2>&1 || true

gt escalate "Plugin FAILED: stuck-agent-dog" \
  --severity high \
  --reason "$ERROR"
```
