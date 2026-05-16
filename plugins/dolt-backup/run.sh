#!/usr/bin/env bash
# dolt-backup/run.sh — Deterministic Dolt database backup.
#
# Syncs production databases to filesystem backups via `dolt backup sync`.
# Skips databases that haven't changed since last backup (hash check).
# Only escalates when actual backup operations fail — not on ping failures.
# After each run, prunes old .darc files keeping at least BACKUP_SAFETY_FLOOR
# most-recent archives and deleting any older than BACKUP_RETENTION_DAYS.
#
# Usage: ./run.sh [--databases db1,db2,...] [--dry-run]

set -euo pipefail

# --- Configuration -----------------------------------------------------------

DOLT_DATA_DIR="${DOLT_DATA_DIR:-$HOME/gt/.dolt-data}"
BACKUP_DIR="${DOLT_BACKUP_DIR:-$HOME/gt/.dolt-backup}"
BACKUP_TIMEOUT=60
BACKUP_RETENTION_DAYS="${BACKUP_RETENTION_DAYS:-7}"
BACKUP_SAFETY_FLOOR="${BACKUP_SAFETY_FLOOR:-3}"

# --- Argument parsing ---------------------------------------------------------

DRY_RUN=false
EXPLICIT_DBS=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --databases) EXPLICIT_DBS="$2"; shift 2 ;;
    --dry-run)   DRY_RUN=true; shift ;;
    --help|-h)
      echo "Usage: $0 [--databases db1,db2,...] [--dry-run]"
      exit 0
      ;;
    *) echo "Unknown option: $1"; exit 1 ;;
  esac
done

# --- Helpers ------------------------------------------------------------------

log() {
  echo "[dolt-backup] $*"
}

# retention_cleanup <backup_path>
# Deletes .darc files older than BACKUP_RETENTION_DAYS, always keeping the
# BACKUP_SAFETY_FLOOR most-recent files. Non-fatal: errors are logged and skipped.
retention_cleanup() {
  local backup_path="$1"
  [[ -d "$backup_path" ]] || return 0

  # Collect all .darc files sorted newest-first (ls -t is portable on macOS+Linux)
  local -a darcs=()
  while IFS= read -r f; do
    darcs+=("$f")
  done < <(ls -t "$backup_path"/*.darc 2>/dev/null || true)

  local total=${#darcs[@]}
  [[ $total -eq 0 ]] && return 0

  local deleted=0
  local freed_kb=0
  local cutoff=$(( $(date +%s) - BACKUP_RETENTION_DAYS * 86400 ))

  for (( i=0; i<total; i++ )); do
    local f="${darcs[$i]}"
    # Safety floor: never delete the BACKUP_SAFETY_FLOOR most-recent archives
    (( i < BACKUP_SAFETY_FLOOR )) && continue

    # mtime: macOS uses stat -f "%m"; GNU stat uses stat -c "%Y"
    local mtime
    mtime=$(stat -f "%m" "$f" 2>/dev/null || stat -c "%Y" "$f" 2>/dev/null || echo 0)

    if (( mtime < cutoff )); then
      local age_days=$(( ( $(date +%s) - mtime ) / 86400 ))
      local kb
      kb=$(du -k "$f" 2>/dev/null | cut -f1 || echo 0)
      if rm -f "$f" 2>/dev/null; then
        deleted=$(( deleted + 1 ))
        freed_kb=$(( freed_kb + kb ))
        log "    retention: removed $(basename "$f") (${age_days}d old, ${kb}KB)"
      else
        log "    retention: could not remove $(basename "$f") — skipping"
      fi
    fi
  done

  if (( deleted > 0 )); then
    log "  retention: freed ${freed_kb}KB across ${deleted} file(s) (${BACKUP_RETENTION_DAYS}d policy, floor ${BACKUP_SAFETY_FLOOR})"
  fi
}

# --- Step 1: Discover databases -----------------------------------------------

# Use explicit list if provided, otherwise auto-discover by scanning
# DOLT_DATA_DIR for directories that contain a .dolt subdirectory,
# excluding system and test databases.
if [[ -n "$EXPLICIT_DBS" ]]; then
  IFS=',' read -ra PROD_DBS <<< "$EXPLICIT_DBS"
else
  PROD_DBS=()
  while IFS= read -r line; do
    PROD_DBS+=("$line")
  done < <(
    for d in "$DOLT_DATA_DIR"/*/; do
      name="$(basename "$d")"
      [[ -d "$d/.dolt" ]] || continue
      [[ "$name" =~ ^(testdb_|beads_t|beads_pt|doctest_) ]] && continue
      echo "$name"
    done | sort
  )
  if [[ ${#PROD_DBS[@]} -eq 0 ]]; then
    log "ERROR: No databases found in $DOLT_DATA_DIR"
    exit 1
  fi
fi

log "Databases to backup (${#PROD_DBS[@]}): ${PROD_DBS[*]}"

# --- Step 2: Backup each database ---------------------------------------------

SYNCED=0
SKIPPED=0
FAILED=0
FAILED_DBS=""

for DB in "${PROD_DBS[@]}"; do
  DB_DIR="$DOLT_DATA_DIR/$DB"
  BACKUP_NAME="${DB}-backup"
  HASH_FILE="$BACKUP_DIR/${DB}/.last-backup-hash"

  # Check DB dir exists
  if [[ ! -d "$DB_DIR/.dolt" ]]; then
    log "  $DB: no .dolt directory, skipping"
    FAILED=$((FAILED + 1))
    FAILED_DBS="$FAILED_DBS $DB(no-dir)"
    continue
  fi

  # Get current HEAD hash
  CURRENT_HASH=$(cd "$DB_DIR" && dolt log -n 1 --oneline 2>/dev/null | head -1 | cut -d' ' -f1 || true)
  if [[ -z "$CURRENT_HASH" ]]; then
    log "  $DB: could not get HEAD hash, will sync anyway"
    CURRENT_HASH="unknown"
  fi

  # Check last backed-up hash
  LAST_HASH=""
  if [[ -f "$HASH_FILE" ]]; then
    LAST_HASH=$(cat "$HASH_FILE")
  fi

  if [[ "$CURRENT_HASH" = "$LAST_HASH" ]] && [[ "$CURRENT_HASH" != "unknown" ]]; then
    log "  $DB: unchanged ($CURRENT_HASH), skipping"
    SKIPPED=$((SKIPPED + 1))
    continue
  fi

  if $DRY_RUN; then
    log "  $DB: DRY RUN would sync ($LAST_HASH -> $CURRENT_HASH)"
    SYNCED=$((SYNCED + 1))
    continue
  fi

  # Sync backup with timeout
  log "  $DB: syncing ($LAST_HASH -> $CURRENT_HASH)..."
  SYNC_START=$(date +%s)

  SYNC_OUTPUT=$(cd "$DB_DIR" && timeout "$BACKUP_TIMEOUT" dolt backup sync "$BACKUP_NAME" 2>&1) || true
  SYNC_RC=${PIPESTATUS[0]:-$?}
  SYNC_ELAPSED=$(( $(date +%s) - SYNC_START ))

  if [[ $SYNC_RC -eq 0 ]]; then
    # Record the hash we just backed up
    mkdir -p "$(dirname "$HASH_FILE")"
    echo "$CURRENT_HASH" > "$HASH_FILE"

    DB_SIZE=$(du -sh "$BACKUP_DIR/$DB" 2>/dev/null | cut -f1 || echo "?")
    SYNCED=$((SYNCED + 1))
    log "  $DB: synced in ${SYNC_ELAPSED}s ($DB_SIZE)"
  elif [[ $SYNC_RC -eq 124 ]]; then
    FAILED=$((FAILED + 1))
    FAILED_DBS="$FAILED_DBS $DB(timeout)"
    log "  $DB: TIMEOUT after ${BACKUP_TIMEOUT}s"
  else
    FAILED=$((FAILED + 1))
    FAILED_DBS="$FAILED_DBS $DB(exit-$SYNC_RC)"
    log "  $DB: FAILED (exit $SYNC_RC): $SYNC_OUTPUT"
  fi
done

# --- Step 3: Retention — prune old .darc files --------------------------------
# Read each DB's configured backup URL from repo_state.json. Only file:// remotes
# are pruned (remote/cloud remotes manage their own retention).

RETENTION_CLEANED=0
for DB in "${PROD_DBS[@]}"; do
  DB_DIR="$DOLT_DATA_DIR/$DB"
  REPO_STATE="$DB_DIR/.dolt/repo_state.json"
  [[ -f "$REPO_STATE" ]] || continue

  # Extract first backup URL (python3 is available on all Gas Town nodes)
  BACKUP_URL=$(python3 -c "
import json, sys
try:
    d = json.load(open(sys.argv[1]))
    bk = d.get('backups', {})
    if bk:
        print(list(bk.values())[0]['url'])
except Exception:
    pass
" "$REPO_STATE" 2>/dev/null || true)

  if [[ "$BACKUP_URL" == file://* ]]; then
    BACKUP_PATH="${BACKUP_URL#file://}"
    if $DRY_RUN; then
      DARC_COUNT=$(ls "$BACKUP_PATH"/*.darc 2>/dev/null | wc -l | tr -d ' ')
      log "  $DB: DRY RUN retention — $DARC_COUNT .darc in $BACKUP_PATH"
    else
      retention_cleanup "$BACKUP_PATH"
      RETENTION_CLEANED=$(( RETENTION_CLEANED + 1 ))
    fi
  fi
done

# --- Step 4: Report results ---------------------------------------------------

SUMMARY="Backup: $SYNCED synced, $SKIPPED unchanged, $FAILED failed (of ${#PROD_DBS[@]} DBs); retention checked ${RETENTION_CLEANED} backup dir(s)"
log "$SUMMARY"

# --- Step 5: Record result and escalate if needed -----------------------------

if [[ "$FAILED" -eq 0 ]]; then
  # Success — record quietly
  bd create --title "dolt-backup: $SUMMARY" -t chore --ephemeral \
    -l type:plugin-run,plugin:dolt-backup,result:success \
    -d "$SUMMARY" --silent 2>/dev/null || true
else
  # Failure — record and escalate
  FAIL_MSG="$SUMMARY. Failed:$FAILED_DBS"
  bd create --title "dolt-backup: FAILED - $FAIL_MSG" -t chore --ephemeral \
    -l type:plugin-run,plugin:dolt-backup,result:failure \
    -d "$FAIL_MSG" --silent 2>/dev/null || true

  gt escalate "dolt-backup FAILED: $FAIL_MSG" \
    --severity high \
    --reason "$FAIL_MSG" 2>/dev/null || true

  exit 1
fi
