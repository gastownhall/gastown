#!/usr/bin/env bash
# dolt-backup/run.sh — Deterministic Dolt database backup.
#
# Syncs production databases to filesystem backups via `dolt backup sync`.
# Skips databases that haven't changed since last backup (hash check).
# Only escalates when actual backup operations fail — not on ping failures.
#
# Usage: ./run.sh [--databases db1,db2,...] [--dry-run]

set -euo pipefail

# --- Configuration -----------------------------------------------------------

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/timeout.sh"

# Honor GT_TOWN_ROOT first (set by daemon when invoking plugins). The
# earlier hardcoded ~/gt fallback caused "No databases found" for towns
# rooted elsewhere (hq-huub).
if [[ -z "${DOLT_DATA_DIR:-}" ]]; then
  if [[ -n "${GT_TOWN_ROOT:-}" && -d "$GT_TOWN_ROOT/.dolt-data" ]]; then
    DOLT_DATA_DIR="$GT_TOWN_ROOT/.dolt-data"
  else
    DOLT_DATA_DIR="$HOME/gt/.dolt-data"
  fi
fi
if [[ -z "${DOLT_BACKUP_DIR:-}" ]]; then
  if [[ -n "${GT_TOWN_ROOT:-}" ]]; then
    DOLT_BACKUP_DIR="$GT_TOWN_ROOT/.dolt-backup"
  else
    DOLT_BACKUP_DIR="$HOME/gt/.dolt-backup"
  fi
fi
BACKUP_DIR="$DOLT_BACKUP_DIR"
BACKUP_TIMEOUT=60

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
    # Signal liveness to the daemon's dir-mtime freshness check even when
    # there is nothing to sync — otherwise checkBackupFreshness reports the
    # backup patrol as stalled forever and pours doctor molecules.
    touch "$BACKUP_DIR/$DB" 2>/dev/null || true
    continue
  fi

  if $DRY_RUN; then
    log "  $DB: DRY RUN would sync ($LAST_HASH -> $CURRENT_HASH)"
    SYNCED=$((SYNCED + 1))
    continue
  fi

  # Ensure the backup remote exists before syncing. Without this, towns
  # that never ran `dolt backup add` fail every sync (historically masked
  # by the SYNC_RC bug below).
  if ! (cd "$DB_DIR" && dolt backup -v 2>/dev/null | awk '{print $1}' | grep -qx "$BACKUP_NAME"); then
    log "  $DB: backup remote $BACKUP_NAME missing, adding -> file://$BACKUP_DIR/$DB/$BACKUP_NAME"
    if ! (cd "$DB_DIR" && dolt backup add "$BACKUP_NAME" "file://$BACKUP_DIR/$DB/$BACKUP_NAME" 2>&1); then
      FAILED=$((FAILED + 1))
      FAILED_DBS="$FAILED_DBS $DB(add-remote)"
      log "  $DB: FAILED to add backup remote"
      continue
    fi
  fi

  # Sync backup with timeout
  log "  $DB: syncing ($LAST_HASH -> $CURRENT_HASH)..."
  SYNC_START=$(date +%s)

  # NOTE: capture the sync exit code directly. The previous
  # `... || true; SYNC_RC=${PIPESTATUS[0]:-$?}` pattern always yielded 0
  # (PIPESTATUS reflects the `true`), recording failed syncs as successful
  # and writing hash markers for backups that never happened.
  SYNC_RC=0
  SYNC_OUTPUT=$(cd "$DB_DIR" && run_with_timeout "$BACKUP_TIMEOUT" dolt backup sync "$BACKUP_NAME" 2>&1) || SYNC_RC=$?
  SYNC_ELAPSED=$(( $(date +%s) - SYNC_START ))

  if [[ $SYNC_RC -eq 0 ]]; then
    # Record the hash we just backed up
    mkdir -p "$(dirname "$HASH_FILE")"
    echo "$CURRENT_HASH" > "$HASH_FILE"
    # Bump dir mtime for the daemon's freshness check (see skip branch).
    touch "$BACKUP_DIR/$DB" 2>/dev/null || true

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

# --- Step 3: Report results ---------------------------------------------------

SUMMARY="Backup: $SYNCED synced, $SKIPPED unchanged, $FAILED failed (of ${#PROD_DBS[@]} DBs)"
log "$SUMMARY"

# --- Step 4: Record result and escalate if needed -----------------------------

if [[ "$FAILED" -eq 0 ]]; then
  # Success — record quietly
  gt plugin record-run --plugin dolt-backup --result success \
    --title "dolt-backup: $SUMMARY" --description "$SUMMARY" >/dev/null 2>&1 || true
else
  # Failure — record and escalate
  FAIL_MSG="$SUMMARY. Failed:$FAILED_DBS"
  gt plugin record-run --plugin dolt-backup --result failure \
    --title "dolt-backup: FAILED - $FAIL_MSG" --description "$FAIL_MSG" >/dev/null 2>&1 || true

  gt escalate "dolt-backup FAILED: $FAIL_MSG" \
    --severity high \
    --reason "$FAIL_MSG" 2>/dev/null || true

  exit 1
fi
