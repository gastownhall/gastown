#!/usr/bin/env bash
# Regression test for dolt-backup/run.sh database discovery (hq-mnux, gtf-58w).
#
# Root cause #3 (hq-l3k2.8): the discovery step regressed from reading the
# canonical rig registry (routes.jsonl + metadata.json) back to a filesystem
# walk over DOLT_DATA_DIR with an incomplete exclusion filter, which restored
# orphaned/test-fixture databases into backups. This test builds a fake town
# root with a registry and both registered and orphan .dolt dirs, then
# asserts --dry-run backs up exactly the registered databases.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
FAILURES=0

TMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TMP_ROOT"' EXIT

TOWN_ROOT="$TMP_ROOT/town"
DATA_DIR="$TMP_ROOT/dolt-data"
BACKUP_DIR="$TMP_ROOT/dolt-backup"

mkdir -p "$TOWN_ROOT/.beads" "$DATA_DIR" "$BACKUP_DIR"

# --- Registered rigs: alpha -> db "alpha_db", beta -> db "beta_db" ---
mkdir -p "$TOWN_ROOT/rigs/alpha/.beads" "$TOWN_ROOT/rigs/beta/.beads"
cat > "$TOWN_ROOT/rigs/alpha/.beads/metadata.json" <<'EOF'
{"dolt_database": "alpha_db"}
EOF
cat > "$TOWN_ROOT/rigs/beta/.beads/metadata.json" <<'EOF'
{"database": "beta_db"}
EOF
cat > "$TOWN_ROOT/.beads/routes.jsonl" <<EOF
{"prefix":"alpha","path":"rigs/alpha"}
{"prefix":"beta","path":"rigs/beta"}
EOF

# .dolt dirs for the registered databases.
mkdir -p "$DATA_DIR/alpha_db/.dolt" "$DATA_DIR/beta_db/.dolt"

# Orphan/test-fixture dirs that must NOT be picked up (unregistered, but
# would have survived the old testdb_/beads_t/beads_pt/doctest_ filter).
mkdir -p "$DATA_DIR/beads/.dolt" "$DATA_DIR/forkrig/.dolt" "$DATA_DIR/testrig/.dolt"

OUTPUT="$(GT_TOWN_ROOT="$TOWN_ROOT" DOLT_DATA_DIR="$DATA_DIR" DOLT_BACKUP_DIR="$BACKUP_DIR" \
  "$SCRIPT_DIR/run.sh" --dry-run 2>&1)"

assert_contains() {
  local needle="$1"
  if [[ "$OUTPUT" != *"$needle"* ]]; then
    echo "FAIL: expected output to contain: $needle"
    echo "--- actual output ---"
    echo "$OUTPUT"
    FAILURES=$((FAILURES + 1))
  fi
}

assert_not_contains() {
  local needle="$1"
  if [[ "$OUTPUT" == *"$needle"* ]]; then
    echo "FAIL: expected output NOT to contain: $needle"
    echo "--- actual output ---"
    echo "$OUTPUT"
    FAILURES=$((FAILURES + 1))
  fi
}

echo "=== registry-driven discovery tests ==="

assert_contains "Databases to backup (2): alpha_db beta_db"
assert_contains "alpha_db: DRY RUN"
assert_contains "beta_db: DRY RUN"
assert_not_contains "beads"
assert_not_contains "forkrig"
assert_not_contains "testrig"

echo ""
if [[ $FAILURES -gt 0 ]]; then
  echo "FAILED: $FAILURES test(s) failed"
  exit 1
else
  echo "PASSED: all tests passed"
fi
