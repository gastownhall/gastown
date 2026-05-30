#!/bin/bash
# vm-integration-test.sh - Phase 4b: Comprehensive VM migration integration tests
#
# Tests 5 migration configurations with artifact cleanliness checks after each.
# Designed for the GCE migration-test-lab VM.
#
# Usage: vm-integration-test.sh <town_root>
#
# Configurations tested:
#   1. Clean v0.5.0 install (no beads at all)
#   2. v0.5.0 with SQLite beads (normal user)
#   3. Partial migration interrupted, then resumed
#   4. Idempotent re-run (already migrated, run again)

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
BOLD='\033[1m'
NC='\033[0m'

log()     { echo -e "${GREEN}[+]${NC} $1"; }
warn()    { echo -e "${YELLOW}[!]${NC} $1"; }
section() { echo -e "\n${BLUE}${BOLD}=== $1 ===${NC}\n"; }
pass()    { echo -e "  ${GREEN}[PASS]${NC} $1"; PASSES=$((PASSES + 1)); }
fail_check() { echo -e "  ${RED}[FAIL]${NC} $1"; FAILURES=$((FAILURES + 1)); FAIL_DETAILS+=("$1"); }

TOWN_ROOT="${1:?Usage: vm-integration-test.sh <town_root>}"
TOWN_ROOT=$(cd "$TOWN_ROOT" && pwd)  # Absolute path
DOLT_DATA_DIR="/workspace/dolt-server"
DOLT_PID_FILE="$DOLT_DATA_DIR/server.pid"
DOLT_PORT="${DOLT_PORT:-$(python3 - <<'PY'
import socket
s = socket.socket()
s.bind(('127.0.0.1', 0))
print(s.getsockname()[1])
s.close()
PY
)}"
export GT_DOLT_PORT="$DOLT_PORT"
export BEADS_DOLT_PORT="$DOLT_PORT"
REPORT_FILE="/tmp/vm-integration-results.txt"
MASTER_BACKUP="/tmp/migration-master-backup"
FAILURES=0
PASSES=0
FAIL_DETAILS=()
TESTS_RUN=0

# ============================================
# HELPER FUNCTIONS
# ============================================

# Run as ubuntu user (files are owned by ubuntu)
run_as_ubuntu() {
    sudo -u ubuntu env GT_DOLT_PORT="$DOLT_PORT" BEADS_DOLT_PORT="$DOLT_PORT" "$@"
}

# Create a master backup of all .beads dirs BEFORE any tests run.
# This backup lives outside the town root so it survives all resets.
create_master_backup() {
    log "Creating master backup at $MASTER_BACKUP..."
    rm -rf "$MASTER_BACKUP"
    mkdir -p "$MASTER_BACKUP"

    for rig_dir in "$TOWN_ROOT"/*/; do
        local rig_name=$(basename "$rig_dir")
        local beads_dir="$rig_dir/.beads"
        [[ -d "$beads_dir" ]] || continue

        mkdir -p "$MASTER_BACKUP/$rig_name"
        sudo cp -a "$beads_dir" "$MASTER_BACKUP/$rig_name/.beads"
        log "Backed up $rig_name/.beads"
    done

    # Also backup town-level .beads
    if [[ -d "$TOWN_ROOT/.beads" ]]; then
        sudo cp -a "$TOWN_ROOT/.beads" "$MASTER_BACKUP/town-beads"
    fi

    # Clean .jsonl.lock from backup (they're empty 0-byte artifacts from bd operations)
    find "$MASTER_BACKUP" -name ".jsonl.lock" -delete 2>/dev/null || true

    log "Master backup created ($(du -sh "$MASTER_BACKUP" | cut -f1))"
}

is_owned_dolt_pid() {
    local pid="$1"
    [[ "$pid" =~ ^[0-9]+$ ]] || return 1
    [[ -r "/proc/$pid/cmdline" ]] || return 1

    local args=()
    mapfile -d '' -t args < "/proc/$pid/cmdline"
    [[ "${#args[@]}" -ge 2 ]] || return 1
    [[ "$(basename "${args[0]}")" == "dolt" && "${args[1]}" == "sql-server" ]] || return 1

    local data_dir=""
    local i
    for ((i = 2; i < ${#args[@]}; i++)); do
        case "${args[$i]}" in
            --data-dir)
                if (( i + 1 < ${#args[@]} )); then
                    data_dir="${args[$((i + 1))]}"
                fi
                ;;
            --data-dir=*)
                data_dir="${args[$i]#--data-dir=}"
                ;;
        esac
    done
    [[ "$data_dir" == "$DOLT_DATA_DIR" ]]
}

is_dolt_server_running() {
    [[ -f "$DOLT_PID_FILE" ]] || return 1
    local pid
    pid=$(cat "$DOLT_PID_FILE" 2>/dev/null || true)
    is_owned_dolt_pid "$pid"
}

stop_dolt_server() {
    log "Stopping owned Dolt server..."
    if [[ ! -f "$DOLT_PID_FILE" ]]; then
        warn "No Dolt PID file at $DOLT_PID_FILE"
        return 0
    fi

    local pid
    pid=$(cat "$DOLT_PID_FILE" 2>/dev/null || true)
    if ! is_owned_dolt_pid "$pid"; then
        warn "Refusing to stop unverified Dolt PID '$pid'"
        rm -f "$DOLT_PID_FILE"
        return 0
    fi

    sudo kill "$pid" 2>/dev/null || true
    for _ in {1..20}; do
        if ! kill -0 "$pid" 2>/dev/null; then
            rm -f "$DOLT_PID_FILE"
            return 0
        fi
        sleep 0.1
    done
    if is_owned_dolt_pid "$pid"; then
        sudo kill -9 "$pid" 2>/dev/null || true
    fi
    rm -f "$DOLT_PID_FILE"
}

# Kill bd user processes, stop only the Dolt server owned by this test.
kill_all_processes() {
    log "Killing bd daemons..."
    sudo killall -9 bd 2>/dev/null || true
    sleep 0.5

    stop_dolt_server

    # Verify no processes remain
    if pgrep -f "bd daemon" >/dev/null 2>&1; then
        warn "bd daemon still running after kill"
    fi
}

# Start dolt server
start_dolt_server() {
    log "Starting Dolt server on isolated port $DOLT_PORT..."
    local pid
    pid=$(run_as_ubuntu bash -c 'nohup dolt sql-server --host 127.0.0.1 --port "$GT_DOLT_PORT" --data-dir "$1" > "$1/server.log" 2>&1 & echo $!' _ "$DOLT_DATA_DIR")
    echo "$pid" > "$DOLT_PID_FILE"
    sleep 3

    if is_dolt_server_running; then
        log "Dolt server started (PID $(cat "$DOLT_PID_FILE"))"
    else
        warn "Owned Dolt server failed to start on port $DOLT_PORT"
        return 1
    fi
}

# Full reset: restore pristine v0.5.0 SQLite state from master backup
reset_to_v050() {
    log "Resetting to pristine v0.5.0 state from master backup..."

    kill_all_processes

    # Remove all dolt data from dolt-server
    for db_dir in "$DOLT_DATA_DIR"/beads*; do
        if [[ -d "$db_dir" ]]; then
            sudo rm -rf "$db_dir"
        fi
    done

    # Restore each rig's .beads from master backup
    for rig_dir in "$TOWN_ROOT"/*/; do
        local rig_name=$(basename "$rig_dir")
        local beads_dir="$rig_dir/.beads"
        local backup_beads="$MASTER_BACKUP/$rig_name/.beads"

        if [[ -d "$backup_beads" ]]; then
            # Remove current .beads entirely and restore from master backup
            sudo rm -rf "$beads_dir"
            sudo cp -a "$backup_beads" "$beads_dir"

            # Force SQLite metadata (overwrite whatever was backed up)
            sudo bash -c "cat > '$beads_dir/metadata.json'" <<METAEOF
{
  "database": "beads.db",
  "backend": "sqlite"
}
METAEOF
            sudo chown ubuntu:ubuntu "$beads_dir/metadata.json"

            # Clean WAL/SHM artifacts from backup copy
            sudo rm -f "$beads_dir/beads.db-wal" "$beads_dir/beads.db-shm"
            # Remove any dolt subdirectory from backup copy
            sudo rm -rf "$beads_dir/dolt"
            sudo rm -f "$beads_dir/bd.sock"
        else
            warn "No master backup for $rig_name, skipping"
        fi
    done

    # Restore town-level .beads if backed up
    if [[ -d "$MASTER_BACKUP/town-beads" ]]; then
        sudo rm -rf "$TOWN_ROOT/.beads"
        sudo cp -a "$MASTER_BACKUP/town-beads" "$TOWN_ROOT/.beads"
    fi

    # Remove migration checkpoint if present
    sudo rm -f "$TOWN_ROOT/.migration-checkpoint.json"

    # Restart dolt server (clean, no databases)
    start_dolt_server

    log "Reset complete"
}

# Remove ALL beads content from a rig (for clean-install test)
strip_beads_from_rig() {
    local rig_dir="$1"
    local beads_dir="$rig_dir/.beads"
    [[ -d "$beads_dir" ]] || return

    sudo rm -f "$beads_dir/beads.db" "$beads_dir/beads.db-wal" "$beads_dir/beads.db-shm"
    sudo rm -f "$beads_dir/issues.jsonl"
    sudo rm -f "$beads_dir/interactions.jsonl"
    sudo rm -f "$beads_dir/metadata.json"
    sudo rm -f "$beads_dir"/.jsonl.lock
    sudo rm -f "$beads_dir"/.local_version
    sudo rm -f "$beads_dir"/last-touched
    sudo rm -rf "$beads_dir/dolt"
    sudo rm -f "$beads_dir"/beads.backup-pre-dolt-*.db
}

# Strip SQLite (for migration testing from a clean state)
strip_sqlite() {
    local rig_dir="$1"
    local beads_dir="$rig_dir/.beads"
    [[ -d "$beads_dir" ]] || return

    sudo rm -f "$beads_dir/beads.db" "$beads_dir/beads.db-wal" "$beads_dir/beads.db-shm"
    sudo rm -rf "$beads_dir/dolt"
    sudo rm -f "$beads_dir"/beads.backup-pre-dolt-*.db
}

# Run migration for all rigs (bd migrate dolt) then gt dolt migrate + gt dolt start
run_full_migration() {
    log "Running full migration..."

    for rig_dir in "$TOWN_ROOT"/*/; do
        local rig_name=$(basename "$rig_dir")
        local metadata="$rig_dir/.beads/metadata.json"
        [[ -f "$metadata" ]] || continue

        local backend=$(sudo python3 -c "import json; print(json.load(open('$metadata')).get('backend', 'unknown'))" 2>/dev/null || echo "unknown")
        if [[ "$backend" == "dolt" ]]; then
            echo "  $rig_name: already Dolt, skipping"
            continue
        fi

        log "Migrating $rig_name..."
        cd "$rig_dir"
        echo y | run_as_ubuntu bd migrate dolt 2>&1 || warn "$rig_name: migrate returned non-zero"
    done

    # Stop dolt server before consolidation (gt dolt migrate requires it stopped)
    log "Stopping Dolt server for consolidation..."
    stop_dolt_server

    # Consolidate dolt databases
    cd "$TOWN_ROOT"
    run_as_ubuntu gt dolt migrate 2>&1 || warn "gt dolt migrate returned non-zero"

    # Restart dolt server
    start_dolt_server
}

# Zero-artifact verification suite (the P0 requirement)
verify_zero_artifacts() {
    local test_name="$1"
    log "Verifying zero artifacts for: $test_name"

    # Check 1: No bd daemons running
    if pgrep -f "bd daemon" >/dev/null 2>&1; then
        fail_check "$test_name: bd daemon still running"
    elif pgrep -f "bd sync" >/dev/null 2>&1; then
        fail_check "$test_name: bd sync still running"
    else
        pass "$test_name: no bd daemons"
    fi

    # Check 2: No SQLite artifacts (beads.db, -wal, -shm) as ACTIVE backend
    local sqlite_artifacts=$(sudo find "$TOWN_ROOT" -maxdepth 3 -name "beads.db" -not -path "*backup*" 2>/dev/null | wc -l)
    local wal_artifacts=$(sudo find "$TOWN_ROOT" -maxdepth 3 -name "beads.db-wal" 2>/dev/null | wc -l)
    local shm_artifacts=$(sudo find "$TOWN_ROOT" -maxdepth 3 -name "beads.db-shm" 2>/dev/null | wc -l)
    if [[ "$sqlite_artifacts" -gt 0 || "$wal_artifacts" -gt 0 || "$shm_artifacts" -gt 0 ]]; then
        fail_check "$test_name: SQLite artifacts remain (db=$sqlite_artifacts, wal=$wal_artifacts, shm=$shm_artifacts)"
    else
        pass "$test_name: no SQLite artifacts"
    fi

    # Check 3: No issues.jsonl (should be removed or renamed after migration)
    # Note: issues.jsonl may persist as a read-only export. Check metadata for backend.
    local jsonl_active=false
    for rig_dir in "$TOWN_ROOT"/*/; do
        local metadata="$rig_dir/.beads/metadata.json"
        [[ -f "$metadata" ]] || continue
        local backend=$(sudo python3 -c "import json; print(json.load(open('$metadata')).get('backend', 'unknown'))" 2>/dev/null || echo "unknown")
        if [[ "$backend" != "dolt" ]]; then
            local rig_name=$(basename "$rig_dir")
            fail_check "$test_name: $rig_name backend is '$backend', not 'dolt'"
            jsonl_active=true
        fi
    done
    if [[ "$jsonl_active" == "false" ]]; then
        pass "$test_name: all backends report dolt"
    fi

    # Check 4: No legacy lock files
    # .jsonl.lock is benign (zero-byte, created by bd for JSONL export locking)
    # dolt-access.lock and other .lock files in .beads/ are problematic
    local dangerous_locks=$(sudo find "$TOWN_ROOT" -maxdepth 4 -name "dolt-access.lock" 2>/dev/null | wc -l)
    local jsonl_locks=$(sudo find "$TOWN_ROOT" -maxdepth 4 -name ".jsonl.lock" -path "*/.beads/*" 2>/dev/null | wc -l)
    local other_locks=$(sudo find "$TOWN_ROOT" -maxdepth 4 -name "*.lock" -path "*/.beads/*" -not -name ".jsonl.lock" 2>/dev/null | wc -l)
    if [[ "$dangerous_locks" -gt 0 || "$other_locks" -gt 0 ]]; then
        fail_check "$test_name: dangerous lock files found (dolt-access=$dangerous_locks, other=$other_locks)"
    else
        pass "$test_name: no dangerous lock files"
    fi
    if [[ "$jsonl_locks" -gt 0 ]]; then
        echo -e "  ${YELLOW}[NOTE]${NC} $test_name: $jsonl_locks .jsonl.lock files present (benign, bd JSONL export artifact)"
    fi

    # Check 5: Dolt server is running
    if is_dolt_server_running; then
        pass "$test_name: Dolt server running"
    else
        fail_check "$test_name: Dolt server not running"
    fi

    # Check 6: metadata.json has correct dolt fields
    for rig_dir in "$TOWN_ROOT"/*/; do
        local rig_name=$(basename "$rig_dir")
        local metadata="$rig_dir/.beads/metadata.json"
        [[ -f "$metadata" ]] || continue

        local metadata_port=$(sudo python3 -c "import json; d=json.load(open('$metadata')); print(d.get('dolt_server_port', ''))" 2>/dev/null || echo "")
        local has_db=$(sudo python3 -c "import json; d=json.load(open('$metadata')); print('yes' if d.get('dolt_database','') else 'no')" 2>/dev/null || echo "no")
        if [[ "$metadata_port" != "$DOLT_PORT" || "$has_db" != "yes" ]]; then
            fail_check "$test_name: $rig_name metadata missing dolt fields (port=$metadata_port, want=$DOLT_PORT, db=$has_db)"
        fi
    done
}

# Verify bd operations work post-migration
verify_bd_operations() {
    local test_name="$1"
    local expect_data="${2:-true}"  # Some tests have empty rigs

    for rig_dir in "$TOWN_ROOT"/*/; do
        local rig_name=$(basename "$rig_dir")
        local metadata="$rig_dir/.beads/metadata.json"
        [[ -f "$metadata" ]] || continue

        cd "$rig_dir"

        # bd list should work
        local list_output=$(run_as_ubuntu bd list 2>&1) || true
        if echo "$list_output" | grep -qi "fatal\|panic\|SIGSEGV"; then
            fail_check "$test_name: bd list crashed for $rig_name"
        else
            pass "$test_name: bd list works for $rig_name"
        fi
    done
}

# ============================================
# MAIN TEST EXECUTION
# ============================================

echo "========================================================"
echo "  VM Integration Test Suite — Phase 4b"
echo "  Town: $TOWN_ROOT"
echo "  VM: $(hostname)"
echo "  Date: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo "========================================================"

# Create master backup before any tests modify data
create_master_backup

# ============================================
# TEST 1: CLEAN v0.5.0 INSTALL (NO BEADS)
# ============================================
section "Test 1: Clean v0.5.0 install (no beads)"
TESTS_RUN=$((TESTS_RUN + 1))

reset_to_v050

# Strip ALL beads content from both rigs
for rig_dir in "$TOWN_ROOT"/*/; do
    [[ -d "$rig_dir/.beads" ]] || continue
    strip_beads_from_rig "$rig_dir"
done

log "State: both rigs have empty .beads/ (no SQLite, no metadata)"

# Run migration — should handle empty rigs gracefully
run_full_migration

# Verify
verify_zero_artifacts "Test1-clean"
verify_bd_operations "Test1-clean" "false"

log "Test 1 complete"

# ============================================
# TEST 2: v0.5.0 WITH SQLite BEADS (NORMAL USER)
# ============================================
section "Test 2: v0.5.0 with SQLite beads (normal user)"
TESTS_RUN=$((TESTS_RUN + 1))

reset_to_v050

# Verify pre-migration state: should have beads.db + issues.jsonl
for rig_dir in "$TOWN_ROOT"/*/; do
    rig_name=$(basename "$rig_dir")
    beads_dir="$rig_dir/.beads"
    [[ -d "$beads_dir" ]] || continue

    has_db=$(sudo test -f "$beads_dir/beads.db" && echo "yes" || echo "no")
    has_jsonl=$(sudo test -f "$beads_dir/issues.jsonl" && echo "yes" || echo "no")
    backend=$(sudo python3 -c "import json; print(json.load(open('$beads_dir/metadata.json')).get('backend','unknown'))" 2>/dev/null || echo "unknown")
    log "Pre-state $rig_name: db=$has_db jsonl=$has_jsonl backend=$backend"
done

# Count pre-migration beads for comparison
declare -A PRE_COUNTS
for rig_dir in "$TOWN_ROOT"/*/; do
    rig_name=$(basename "$rig_dir")
    [[ -f "$rig_dir/.beads/metadata.json" ]] || continue
    cd "$rig_dir"
    count=$(run_as_ubuntu bd list 2>/dev/null | wc -l)
    PRE_COUNTS[$rig_name]=$count
    log "Pre-migration $rig_name: $count beads"
done

# Run migration
run_full_migration

# Verify artifacts
verify_zero_artifacts "Test2-sqlite-jsonl"

# Verify bead counts preserved
count_match=true
for rig_dir in "$TOWN_ROOT"/*/; do
    rig_name=$(basename "$rig_dir")
    [[ -f "$rig_dir/.beads/metadata.json" ]] || continue
    cd "$rig_dir"
    post_count=$(run_as_ubuntu bd list 2>/dev/null | wc -l)
    pre_count="${PRE_COUNTS[$rig_name]:-0}"
    log "Post-migration $rig_name: $post_count beads (was $pre_count)"
    if [[ "$pre_count" -gt 0 && "$post_count" -lt "$pre_count" ]]; then
        fail_check "Test2-sqlite-jsonl: $rig_name count dropped ($pre_count -> $post_count)"
        count_match=false
    fi
done
if [[ "$count_match" == "true" ]]; then
    pass "Test2-sqlite-jsonl: bead counts preserved"
fi

verify_bd_operations "Test2-sqlite-jsonl"

log "Test 2 complete"

# ============================================
# TEST 3: PARTIAL MIGRATION INTERRUPTED AND RESUMED
# ============================================
section "Test 3: Partial migration interrupted and resumed"
TESTS_RUN=$((TESTS_RUN + 1))

reset_to_v050

# Migrate only the FIRST rig (gastown), simulating crash before second rig
RIGS=()
for rig_dir in "$TOWN_ROOT"/*/; do
    rig_name=$(basename "$rig_dir")
    [[ -f "$rig_dir/.beads/metadata.json" ]] || continue
    RIGS+=("$rig_name")
done

if [[ ${#RIGS[@]} -ge 2 ]]; then
    FIRST_RIG="${RIGS[0]}"
    SECOND_RIG="${RIGS[1]}"

    log "Migrating only first rig ($FIRST_RIG), simulating crash..."
    cd "$TOWN_ROOT/$FIRST_RIG"
    echo y | run_as_ubuntu bd migrate dolt 2>&1 || warn "$FIRST_RIG: migrate returned non-zero"

    # Verify partial state
    first_backend=$(sudo python3 -c "import json; print(json.load(open('$TOWN_ROOT/$FIRST_RIG/.beads/metadata.json')).get('backend','unknown'))" 2>/dev/null)
    second_backend=$(sudo python3 -c "import json; print(json.load(open('$TOWN_ROOT/$SECOND_RIG/.beads/metadata.json')).get('backend','unknown'))" 2>/dev/null)
    log "After partial migration: $FIRST_RIG=$first_backend, $SECOND_RIG=$second_backend"

    if [[ "$first_backend" == "dolt" && "$second_backend" != "dolt" ]]; then
        pass "Test4-partial: partial state confirmed ($FIRST_RIG=dolt, $SECOND_RIG=$second_backend)"
    else
        warn "Test4-partial: unexpected partial state ($FIRST_RIG=$first_backend, $SECOND_RIG=$second_backend)"
    fi

    # Now "resume" — migrate remaining rigs
    log "Resuming migration for remaining rigs..."
    cd "$TOWN_ROOT/$SECOND_RIG"
    echo y | run_as_ubuntu bd migrate dolt 2>&1 || warn "$SECOND_RIG: migrate returned non-zero"

    # Consolidate (stop server first)
    stop_dolt_server
    cd "$TOWN_ROOT"
    run_as_ubuntu gt dolt migrate 2>&1 || warn "gt dolt migrate returned non-zero"
    start_dolt_server

    verify_zero_artifacts "Test4-partial-resume"

    # Verify both rigs work
    for rig_name in "${RIGS[@]}"; do
        cd "$TOWN_ROOT/$rig_name"
        count=$(run_as_ubuntu bd list 2>/dev/null | wc -l)
        if [[ "$count" -gt 0 ]]; then
            pass "Test4-partial-resume: $rig_name has $count beads after resume"
        else
            warn "Test4-partial-resume: $rig_name has 0 beads (may be expected)"
        fi
    done
else
    warn "Need at least 2 rigs for partial migration test, found ${#RIGS[@]}"
fi

log "Test 3 complete"

# ============================================
# TEST 4: IDEMPOTENT RE-RUN (ALREADY MIGRATED)
# ============================================
section "Test 4: Idempotent re-run"
TESTS_RUN=$((TESTS_RUN + 1))

# Don't reset — use the already-migrated state from Test 3

# Record current state
declare -A PASS1_COUNTS
for rig_dir in "$TOWN_ROOT"/*/; do
    rig_name=$(basename "$rig_dir")
    [[ -f "$rig_dir/.beads/metadata.json" ]] || continue
    cd "$rig_dir"
    count=$(run_as_ubuntu bd list 2>/dev/null | wc -l)
    PASS1_COUNTS[$rig_name]=$count
    log "Before re-run $rig_name: $count beads"
done

# Run migration again — should be idempotent
log "Running migration again on already-migrated system..."
IDEM_ERRORS=0

for rig_dir in "$TOWN_ROOT"/*/; do
    rig_name=$(basename "$rig_dir")
    [[ -f "$rig_dir/.beads/metadata.json" ]] || continue

    cd "$rig_dir"
    output=$(echo y | run_as_ubuntu bd migrate dolt 2>&1) || true
    if echo "$output" | grep -qi "fatal\|panic\|corrupt\|segfault"; then
        fail_check "Test5-idempotent: $rig_name had fatal error on re-run"
        IDEM_ERRORS=$((IDEM_ERRORS + 1))
    else
        log "$rig_name re-run: OK (output: $(echo "$output" | head -1))"
    fi
done

# Run gt dolt migrate again (stop server first, as required)
stop_dolt_server
cd "$TOWN_ROOT"
output=$(run_as_ubuntu gt dolt migrate 2>&1) || true
if echo "$output" | grep -qi "fatal\|panic\|corrupt"; then
    fail_check "Test5-idempotent: gt dolt migrate had fatal error on re-run"
    IDEM_ERRORS=$((IDEM_ERRORS + 1))
fi
start_dolt_server

# Run gt dolt fix-metadata twice (must be harmless)
cd "$TOWN_ROOT"
run_as_ubuntu gt dolt fix-metadata 2>&1 || true
run_as_ubuntu gt dolt fix-metadata 2>&1 || true

verify_zero_artifacts "Test5-idempotent"

# Verify counts unchanged
count_ok=true
for rig_dir in "$TOWN_ROOT"/*/; do
    rig_name=$(basename "$rig_dir")
    [[ -f "$rig_dir/.beads/metadata.json" ]] || continue
    cd "$rig_dir"
    post_count=$(run_as_ubuntu bd list 2>/dev/null | wc -l)
    pre_count="${PASS1_COUNTS[$rig_name]:-0}"
    if [[ "$pre_count" != "$post_count" ]]; then
        fail_check "Test5-idempotent: $rig_name count changed ($pre_count -> $post_count)"
        count_ok=false
    fi
done
if [[ "$count_ok" == "true" ]]; then
    pass "Test5-idempotent: all counts preserved after re-run"
fi

if [[ "$IDEM_ERRORS" -eq 0 ]]; then
    pass "Test5-idempotent: no fatal errors on re-run"
fi

log "Test 4 complete"

# ============================================
# FINAL SUMMARY
# ============================================
section "FINAL RESULTS"

TOTAL=$((PASSES + FAILURES))

echo "  Tests run:   $TESTS_RUN configurations"
echo "  Checks:      $TOTAL total"
echo "  Passed:      $PASSES"
echo "  Failed:      $FAILURES"
echo

if [[ $FAILURES -gt 0 ]]; then
    echo -e "  ${RED}${BOLD}FAILURES:${NC}"
    for detail in "${FAIL_DETAILS[@]}"; do
        echo -e "    ${RED}- $detail${NC}"
    done
    echo
fi

# Write report to file for extraction
{
    echo "# VM Integration Test Results"
    echo "Date: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "VM: $(hostname)"
    echo "Town: $TOWN_ROOT"
    echo "gt: $(gt --version 2>&1 | head -1)"
    echo "bd: $(bd --version 2>&1 | head -1)"
    echo "dolt: $(dolt version 2>&1 | head -1)"
    echo ""
    echo "## Summary"
    echo "- Configurations tested: $TESTS_RUN"
    echo "- Total checks: $TOTAL"
    echo "- Passed: $PASSES"
    echo "- Failed: $FAILURES"
    echo ""
    if [[ $FAILURES -gt 0 ]]; then
        echo "## Failures"
        for detail in "${FAIL_DETAILS[@]}"; do
            echo "- $detail"
        done
        echo ""
    fi
    echo "## Configurations"
    echo "1. Clean v0.5.0 (no beads)"
    echo "2. SQLite beads (normal user)"
    echo "3. Partial migration interrupted + resumed"
    echo "4. Idempotent re-run"
} > "$REPORT_FILE"

echo "  Report saved to: $REPORT_FILE"

if [[ $FAILURES -eq 0 ]]; then
    echo -e "\n  ${GREEN}${BOLD}ALL TESTS PASSED${NC}\n"
else
    echo -e "\n  ${RED}${BOLD}$FAILURES FAILURE(S) — SEE ABOVE${NC}\n"
fi

echo "========================================================"

exit $FAILURES
