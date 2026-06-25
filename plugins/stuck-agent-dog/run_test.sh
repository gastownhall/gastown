#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="$ROOT_DIR/plugins/stuck-agent-dog/run.sh"
ORIGINAL_PATH="$PATH"
PASS=0
FAIL=0
CLEANUP_DIRS=()

cleanup() {
  for dir in "${CLEANUP_DIRS[@]}"; do
    rm -rf "$dir"
  done
}
trap cleanup EXIT

record_pass() {
  PASS=$((PASS + 1))
  printf 'PASS: %s\n' "$1"
}

record_fail() {
  FAIL=$((FAIL + 1))
  printf 'FAIL: %s\n' "$1"
}

assert_file_empty() {
  local file="$1"
  local label="$2"
  if [ ! -s "$file" ]; then
    record_pass "$label"
  else
    record_fail "$label"
    printf '  unexpected contents of %s:\n' "$file"
    sed 's/^/    /' "$file"
  fi
}

assert_file_contains() {
  local file="$1"
  local needle="$2"
  local label="$3"
  if grep -Fq -- "$needle" "$file"; then
    record_pass "$label"
  else
    record_fail "$label"
    printf '  expected %q in %s\n' "$needle" "$file"
    sed 's/^/    /' "$file" 2>/dev/null || true
  fi
}

assert_file_not_contains() {
  local file="$1"
  local needle="$2"
  local label="$3"
  if ! grep -Fq -- "$needle" "$file" 2>/dev/null; then
    record_pass "$label"
  else
    record_fail "$label"
    printf '  did not expect %q in %s\n' "$needle" "$file"
    sed 's/^/    /' "$file" 2>/dev/null || true
  fi
}

assert_line_count() {
  local file="$1"
  local expected="$2"
  local label="$3"
  local actual=0

  if [ -f "$file" ]; then
    actual=$(wc -l < "$file" | tr -d ' ')
  fi
  if [ "$actual" = "$expected" ]; then
    record_pass "$label"
  else
    record_fail "$label"
    printf '  expected %s lines in %s, got %s\n' "$expected" "$file" "$actual"
    sed 's/^/    /' "$file" 2>/dev/null || true
  fi
}

write_fake_commands() {
  local bin_dir="$1"

  cat > "$bin_dir/gt" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

case "${1:-}" in
  town)
    if [ "${2:-}" = "root" ]; then
      printf '%s\n' "$GT_TOWN_ROOT"
      exit 0
    fi
    ;;
  hook)
    if [ "${2:-}" = "show" ]; then
      target="${3:-}"
      name="${target##*/}"
      if [ -f "$TEST_STATE/hook_fail/$name" ]; then
        exit 1
      fi
      if [ -f "$TEST_STATE/nohook/$name" ]; then
        printf '{"agent":"%s","status":"empty"}\n' "$target"
      else
        status="hooked"
        if [ -f "$TEST_STATE/hook_status/$name" ]; then
          status=$(tr -d '\n' < "$TEST_STATE/hook_status/$name")
        fi
        printf '{"agent":"%s","bead_id":"gt-hook-%s","status":"%s"}\n' "$target" "$name" "$status"
      fi
      exit 0
    fi
    ;;
  rig)
    if [ "${2:-}" = "list" ] && [ "${3:-}" = "--json" ]; then
      if [ -f "$TEST_STATE/rig_list_fail" ]; then
        exit 1
      fi
      if [ -f "$TEST_STATE/rig_list.json" ]; then
        cat "$TEST_STATE/rig_list.json"
      else
        printf '[{"name":"gastown","beads_prefix":"gt","status":"operational"}]\n'
      fi
      exit 0
    fi
    ;;
  session)
    if [ "${2:-}" = "health" ]; then
      session="${3:-}"
      shift 3
      max_inactivity="0s"
      while [ "$#" -gt 0 ]; do
        case "$1" in
          --max-inactivity)
            max_inactivity="${2:-}"
            shift 2
            ;;
          *)
            shift
            ;;
        esac
      done

      status="healthy"
      if [ -f "$TEST_STATE/health/$session" ]; then
        status=$(sed -n '1p' "$TEST_STATE/health/$session" | tr -d '\n')
        if [ "$(wc -l < "$TEST_STATE/health/$session" | tr -d ' ')" -gt 1 ]; then
          sed '1d' "$TEST_STATE/health/$session" > "$TEST_STATE/health/$session.tmp"
          mv "$TEST_STATE/health/$session.tmp" "$TEST_STATE/health/$session"
        fi
      fi
      printf '%s --max-inactivity %s\n' "$session" "$max_inactivity" >> "$TEST_STATE/health_calls.log"
      healthy=false
      zombie=false
      case "$status" in
        healthy) healthy=true ;;
        agent-dead|agent-hung) zombie=true ;;
      esac
      printf '{"session":"%s","status":"%s","healthy":%s,"zombie":%s,"max_inactivity_seconds":0}\n' "$session" "$status" "$healthy" "$zombie"
      exit 0
    fi
    ;;
  mail)
    if [ "${2:-}" = "send" ]; then
      printf '%s\n' "$*" >> "$TEST_STATE/mail.log"
      while IFS= read -r _line; do :; done
      exit 0
    fi
    ;;
  escalate)
    printf '%s\n' "$*" >> "$TEST_STATE/escalate.log"
    exit 0
    ;;
esac

printf 'unexpected gt call: %s\n' "$*" >&2
exit 1
SH
  chmod +x "$bin_dir/gt"

  cat > "$bin_dir/tmux" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

arg_after_t() {
  while [ "$#" -gt 0 ]; do
    if [ "$1" = "-t" ]; then
      printf '%s\n' "${2:-}"
      return 0
    fi
    shift
  done
  return 1
}

case "${1:-}" in
  has-session)
    session=$(arg_after_t "$@" || true)
    [ -n "$session" ] && [ -f "$TEST_STATE/sessions/$session" ]
    ;;
  kill-session)
    session=$(arg_after_t "$@" || true)
    printf '%s\n' "$session" >> "$TEST_STATE/kill.log"
    ;;
  list-panes)
    printf '999\n'
    ;;
  display-message)
    date +%s
    ;;
  capture-pane)
    printf 'active opencode research in progress\n'
    ;;
  *)
    printf 'unexpected tmux call: %s\n' "$*" >&2
    exit 1
    ;;
esac
SH
  chmod +x "$bin_dir/tmux"

  cat > "$bin_dir/bd" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

case "${1:-}" in
  show)
    bead="${2:-}"
    status="open"
    if [ -f "$TEST_STATE/status/$bead" ]; then
      status=$(tr -d '\n' < "$TEST_STATE/status/$bead")
    fi
    printf '[{"status":"%s"}]\n' "$status"
    ;;
  list)
    printf '[]\n'
    ;;
  create)
    printf '%s\n' "$*" >> "$TEST_STATE/bd.log"
    ;;
  *)
    printf 'unexpected bd call: %s\n' "$*" >&2
    exit 1
    ;;
esac
SH
  chmod +x "$bin_dir/bd"

  cat > "$bin_dir/ps" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

if [ "${1:-}" = "-o" ] && [ "${2:-}" = "comm=" ]; then
  printf 'bash\n'
  exit 0
fi

printf 'unexpected ps call: %s\n' "$*" >&2
exit 1
SH
  chmod +x "$bin_dir/ps"
}

setup_case() {
  TEST_TMP=$(mktemp -d)
  CLEANUP_DIRS+=("$TEST_TMP")
  export TEST_STATE="$TEST_TMP/state"
  export GT_TOWN_ROOT="$TEST_TMP/town"
  local bin_dir="$TEST_TMP/bin"

  mkdir -p "$TEST_STATE/health" "$TEST_STATE/hook_fail" "$TEST_STATE/hook_status" "$TEST_STATE/nohook" "$TEST_STATE/sessions" "$TEST_STATE/status" "$bin_dir"
  mkdir -p "$GT_TOWN_ROOT/gastown/polecats" "$GT_TOWN_ROOT/deacon"
  printf '{"rigs":{"gastown":{"beads":{"prefix":"gt"}}}}\n' > "$GT_TOWN_ROOT/rigs.json"
  : > "$TEST_STATE/mail.log"
  : > "$TEST_STATE/kill.log"
  : > "$TEST_STATE/escalate.log"
  : > "$TEST_STATE/health_calls.log"
  : > "$TEST_STATE/bd.log"
  touch "$TEST_STATE/sessions/hq-deacon"

  write_fake_commands "$bin_dir"
  export PATH="$bin_dir:$ORIGINAL_PATH"
  export GT_STUCK_AGENT_DOG_MAX_INACTIVITY=0s
  unset GT_STUCK_AGENT_DOG_MASS_DEATH_THRESHOLD
}

add_polecat() {
  local name="$1"
  local status="$2"

  add_polecat_in_rig gastown gt "$name" "$status"
}

add_polecat_in_rig() {
  local rig="$1"
  local prefix="$2"
  local name="$3"
  local status="$4"
  local session="$prefix-$name"

  mkdir -p "$GT_TOWN_ROOT/$rig/polecats/$name"
  touch "$TEST_STATE/sessions/$session"
  printf '%s\n' "$status" > "$TEST_STATE/health/$session"
}

run_script() {
  bash "$SCRIPT" > "$TEST_STATE/output.log" 2>&1
}

test_healthy_runtime() {
  local runtime="$1"

  setup_case
  add_polecat "$runtime" healthy
  run_script

  assert_file_empty "$TEST_STATE/kill.log" "$runtime healthy: no session kill"
  assert_file_empty "$TEST_STATE/mail.log" "$runtime healthy: no restart mail"
  assert_file_empty "$TEST_STATE/escalate.log" "$runtime healthy: no escalation"
  assert_file_contains "$TEST_STATE/health_calls.log" "gt-$runtime --max-inactivity 0s" "$runtime healthy: used central health"
}

test_long_research_active_pane() {
  setup_case
  export GT_STUCK_AGENT_DOG_MAX_INACTIVITY=30m
  add_polecat research agent-hung
  run_script

  assert_file_empty "$TEST_STATE/kill.log" "active research: no session kill"
  assert_file_empty "$TEST_STATE/mail.log" "active research: no restart mail"
  assert_file_empty "$TEST_STATE/escalate.log" "active research: no mass-death escalation"
  assert_file_contains "$TEST_STATE/output.log" "OBSERVE: gt-research runtime alive" "active research: observed live runtime"
  assert_file_contains "$TEST_STATE/output.log" "0 crashed, 0 stuck, 1 healthy" "active research: counted healthy"
}

test_dead_agent_restarts_one() {
  setup_case
  add_polecat alpha agent-dead
  run_script

  assert_line_count "$TEST_STATE/kill.log" 1 "dead agent: one session kill"
  assert_file_contains "$TEST_STATE/kill.log" "gt-alpha" "dead agent: killed target session"
  assert_line_count "$TEST_STATE/mail.log" 1 "dead agent: one restart mail"
  assert_file_contains "$TEST_STATE/mail.log" "gastown/witness" "dead agent: mailed rig witness"
  assert_file_empty "$TEST_STATE/escalate.log" "dead agent: no mass-death escalation"
}

test_in_progress_hook_restarts_one() {
  setup_case
  add_polecat alpha agent-dead
  printf 'in_progress\n' > "$TEST_STATE/hook_status/alpha"
  run_script

  assert_line_count "$TEST_STATE/kill.log" 1 "in_progress hook: one session kill"
  assert_line_count "$TEST_STATE/mail.log" 1 "in_progress hook: one restart mail"
  assert_file_empty "$TEST_STATE/escalate.log" "in_progress hook: no mass-death escalation"
}

test_dead_session_restarts_one() {
  setup_case
  add_polecat beta session-dead
  run_script

  assert_file_empty "$TEST_STATE/kill.log" "dead session: no session kill"
  assert_line_count "$TEST_STATE/mail.log" 1 "dead session: one restart mail"
  assert_file_contains "$TEST_STATE/mail.log" "RESTART_POLECAT: gastown/beta" "dead session: restart requested"
  assert_file_empty "$TEST_STATE/escalate.log" "dead session: no mass-death escalation"
}

test_closed_hook_skips_restart() {
  setup_case
  add_polecat alpha agent-dead
  printf 'closed\n' > "$TEST_STATE/hook_status/alpha"
  run_script

  assert_file_empty "$TEST_STATE/kill.log" "closed hook: no session kill"
  assert_file_empty "$TEST_STATE/mail.log" "closed hook: no restart mail"
  assert_file_contains "$TEST_STATE/output.log" "status=closed not actionable" "closed hook: status checked"
}

test_no_hook_dead_sessions_do_not_mass_death() {
  setup_case
  add_polecat alpha session-dead
  add_polecat beta session-dead
  add_polecat gamma session-dead
  touch "$TEST_STATE/nohook/alpha" "$TEST_STATE/nohook/beta" "$TEST_STATE/nohook/gamma"
  run_script

  assert_file_empty "$TEST_STATE/kill.log" "idle no-hook: no kills"
  assert_file_empty "$TEST_STATE/mail.log" "idle no-hook: no restart mail"
  assert_file_empty "$TEST_STATE/escalate.log" "idle no-hook: no escalation"
  assert_file_not_contains "$TEST_STATE/output.log" "MASS DEATH" "idle no-hook: no mass death"
  assert_file_contains "$TEST_STATE/output.log" "0 crashed, 0 stuck" "idle no-hook: not counted"
}

test_non_actionable_hook_statuses_do_not_mass_death() {
  setup_case
  add_polecat alpha agent-dead
  add_polecat beta agent-dead
  add_polecat gamma agent-dead
  printf 'open\n' > "$TEST_STATE/hook_status/alpha"
  printf 'closed\n' > "$TEST_STATE/hook_status/beta"
  printf 'deferred\n' > "$TEST_STATE/hook_status/gamma"
  run_script

  assert_file_empty "$TEST_STATE/kill.log" "stale statuses: no kills"
  assert_file_empty "$TEST_STATE/mail.log" "stale statuses: no restart mail"
  assert_file_empty "$TEST_STATE/escalate.log" "stale statuses: no escalation"
  assert_file_contains "$TEST_STATE/output.log" "status=open not actionable" "stale statuses: open skipped"
  assert_file_not_contains "$TEST_STATE/output.log" "MASS DEATH" "stale statuses: no mass death"
}

test_docked_rig_skipped() {
  setup_case
  cat > "$TEST_STATE/rig_list.json" <<'JSON'
[{"name":"gastown","beads_prefix":"gt","status":"operational"},{"name":"dockedrig","beads_prefix":"dk","status":"docked"}]
JSON
  add_polecat_in_rig dockedrig dk alpha agent-dead
  add_polecat_in_rig dockedrig dk beta agent-dead
  add_polecat_in_rig dockedrig dk gamma agent-dead
  run_script

  assert_file_empty "$TEST_STATE/kill.log" "docked rig: no kills"
  assert_file_empty "$TEST_STATE/mail.log" "docked rig: no restart mail"
  assert_file_empty "$TEST_STATE/escalate.log" "docked rig: no escalation"
  assert_file_not_contains "$TEST_STATE/health_calls.log" "dk-alpha" "docked rig: alpha not health-checked"
  assert_file_not_contains "$TEST_STATE/output.log" "MASS DEATH" "docked rig: no mass death"
}

test_rig_list_unavailable_fails_closed() {
  setup_case
  touch "$TEST_STATE/rig_list_fail"
  add_polecat alpha agent-dead
  add_polecat beta agent-dead
  add_polecat gamma agent-dead
  run_script

  assert_file_empty "$TEST_STATE/kill.log" "rig list unavailable: no kills"
  assert_file_empty "$TEST_STATE/mail.log" "rig list unavailable: no restart mail"
  assert_file_empty "$TEST_STATE/escalate.log" "rig list unavailable: no escalation"
  assert_file_empty "$TEST_STATE/health_calls.log" "rig list unavailable: no health checks"
  assert_file_contains "$TEST_STATE/output.log" "gt rig list --json unavailable" "rig list unavailable: logged fail-closed"
}

test_mass_death_recheck_recovered() {
  setup_case
  add_polecat alpha agent-dead
  add_polecat beta agent-dead
  add_polecat gamma agent-dead
  printf 'agent-dead\nhealthy\n' > "$TEST_STATE/health/gt-alpha"
  printf 'agent-dead\nhealthy\n' > "$TEST_STATE/health/gt-beta"
  printf 'agent-dead\nhealthy\n' > "$TEST_STATE/health/gt-gamma"
  run_script

  assert_file_empty "$TEST_STATE/kill.log" "recovered mass candidates: no kills"
  assert_file_empty "$TEST_STATE/mail.log" "recovered mass candidates: no restart mail"
  assert_file_empty "$TEST_STATE/escalate.log" "recovered mass candidates: no escalation"
  assert_file_contains "$TEST_STATE/output.log" "dropped to 0 after live re-check" "recovered mass candidates: recheck suppressed critical"
}

test_mass_death_recheck_one_remaining_restarts() {
  setup_case
  add_polecat alpha agent-dead
  add_polecat beta agent-dead
  add_polecat gamma agent-dead
  printf 'agent-dead\nagent-dead\n' > "$TEST_STATE/health/gt-alpha"
  printf 'agent-dead\nhealthy\n' > "$TEST_STATE/health/gt-beta"
  printf 'agent-dead\nhealthy\n' > "$TEST_STATE/health/gt-gamma"
  run_script

  assert_line_count "$TEST_STATE/kill.log" 1 "one remaining: one kill"
  assert_file_contains "$TEST_STATE/kill.log" "gt-alpha" "one remaining: killed confirmed zombie"
  assert_line_count "$TEST_STATE/mail.log" 1 "one remaining: one restart mail"
  assert_file_empty "$TEST_STATE/escalate.log" "one remaining: no mass-death escalation"
  assert_file_contains "$TEST_STATE/output.log" "dropped to 1 after live re-check" "one remaining: recheck downgraded"
}

test_mass_death_recheck_reclassifies_dead_statuses() {
  setup_case
  add_polecat alpha agent-dead
  add_polecat beta session-dead
  add_polecat gamma agent-dead
  printf 'agent-dead\nsession-dead\n' > "$TEST_STATE/health/gt-alpha"
  printf 'session-dead\nagent-dead\n' > "$TEST_STATE/health/gt-beta"
  printf 'agent-dead\nagent-dead\n' > "$TEST_STATE/health/gt-gamma"
  run_script

  assert_file_empty "$TEST_STATE/kill.log" "reclassified mass: no session kills"
  assert_file_empty "$TEST_STATE/mail.log" "reclassified mass: no restart mail"
  assert_line_count "$TEST_STATE/escalate.log" 1 "reclassified mass: one escalation"
  assert_file_contains "$TEST_STATE/escalate.log" "Mass agent death: 3 agents down" "reclassified mass: confirmed all dead"
  assert_file_contains "$TEST_STATE/escalate.log" "--fingerprint stuck-agent-dog:mass-death" "reclassified mass: fingerprint set"
}

test_mass_death_skips_actions() {
  setup_case
  add_polecat alpha agent-dead
  add_polecat beta agent-dead
  add_polecat gamma agent-dead
  run_script

  assert_file_empty "$TEST_STATE/kill.log" "mass death: no session kills"
  assert_file_empty "$TEST_STATE/mail.log" "mass death: no restart mail"
  assert_line_count "$TEST_STATE/escalate.log" 1 "mass death: one escalation"
  assert_file_contains "$TEST_STATE/escalate.log" "--source plugin:stuck-agent-dog" "mass death: source set"
  assert_file_contains "$TEST_STATE/escalate.log" "--fingerprint stuck-agent-dog:mass-death" "mass death: fingerprint set"
  assert_file_contains "$TEST_STATE/output.log" "Skipping per-agent restart/kill actions" "mass death: action loops skipped"
}

test_invalid_mass_death_threshold_defaults() {
  setup_case
  export GT_STUCK_AGENT_DOG_MASS_DEATH_THRESHOLD=0
  run_script

  assert_file_empty "$TEST_STATE/escalate.log" "zero threshold: no empty mass-death escalation"
  assert_file_not_contains "$TEST_STATE/output.log" "MASS DEATH" "zero threshold: no mass death"
}

test_healthy_runtime opencode
test_healthy_runtime bun
test_healthy_runtime node
test_healthy_runtime claude
test_long_research_active_pane
test_dead_agent_restarts_one
test_in_progress_hook_restarts_one
test_dead_session_restarts_one
test_closed_hook_skips_restart
test_no_hook_dead_sessions_do_not_mass_death
test_non_actionable_hook_statuses_do_not_mass_death
test_docked_rig_skipped
test_rig_list_unavailable_fails_closed
test_mass_death_recheck_recovered
test_mass_death_recheck_one_remaining_restarts
test_mass_death_recheck_reclassifies_dead_statuses
test_mass_death_skips_actions
test_invalid_mass_death_threshold_defaults

printf '\n%s passed, %s failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
