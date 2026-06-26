+++
name = "dolt-archive"
description = "Offsite backup: JSONL snapshots to git, dolt push to GitHub/DoltHub"
version = 1

[gate]
type = "cooldown"
duration = "1h"

[tracking]
labels = ["plugin:dolt-archive", "category:data-safety"]
digest = true

[execution]
timeout = "15m"
notify_on_failure = true
severity = "critical"
+++

# Dolt Archive

Gets production data off this machine. Three layers:

1. **JSONL export** — Human-readable snapshots (saved us in Clown Show #13)
2. **Git push** — JSONL files committed and pushed to GitHub
3. **Dolt push** — Native Dolt replication to GitHub/DoltHub (if configured)

JSONL is the last-resort recovery layer. Always maintain it regardless of
whether the other layers work.

## Config

```bash
DOLT_DATA_DIR="$GT_TOWN_ROOT/.dolt-data"
PROD_DBS=("hq" "gt" "mo")
JSONL_EXPORT_DIR="$GT_TOWN_ROOT/.dolt-archive/jsonl"
DOLT_HOST="${GT_DOLT_HOST:-127.0.0.1}"
DOLT_PORT="${GT_DOLT_PORT:-3307}"
DOLT_USER="root"
```

## Step 1: JSONL export

Export all issues from each production database to JSONL files. These are
human-readable, diffable, and survive any storage backend failure.

```bash
echo "=== JSONL Export ==="
EXPORTED=0
EXPORT_FAILED=0

mkdir -p "$JSONL_EXPORT_DIR"

for DB in "${PROD_DBS[@]}"; do
  EXPORT_FILE="$JSONL_EXPORT_DIR/${DB}-$(date +%Y%m%d-%H%M).jsonl"
  LATEST_LINK="$JSONL_EXPORT_DIR/${DB}-latest.jsonl"

  echo "Exporting $DB..."

  # Use bd export if available, otherwise query directly
  if bd export --db "$DB" --format jsonl > "$EXPORT_FILE" 2>/dev/null; then
    LINE_COUNT=$(wc -l < "$EXPORT_FILE" | tr -d ' ')
    FILE_SIZE=$(du -h "$EXPORT_FILE" | cut -f1)
    echo "  $DB: $LINE_COUNT issues exported ($FILE_SIZE)"

    # Update latest symlink
    ln -sf "$EXPORT_FILE" "$LATEST_LINK"
    EXPORTED=$((EXPORTED + 1))
  else
    # Fallback: query Dolt directly for issue data
    dolt sql -q "SELECT * FROM issues ORDER BY id" \
      --host "$DOLT_HOST" --port "$DOLT_PORT" -u "$DOLT_USER" \
      -d "$DB" --no-auto-commit --result-format json \
      > "$EXPORT_FILE" 2>/dev/null

    if [ $? -eq 0 ] && [ -s "$EXPORT_FILE" ]; then
      LINE_COUNT=$(wc -l < "$EXPORT_FILE" | tr -d ' ')
      echo "  $DB: exported via SQL ($LINE_COUNT lines)"
      ln -sf "$EXPORT_FILE" "$LATEST_LINK"
      EXPORTED=$((EXPORTED + 1))
    else
      echo "  WARN: $DB export failed"
      rm -f "$EXPORT_FILE"
      EXPORT_FAILED=$((EXPORT_FAILED + 1))
    fi
  fi
done

# Prune old exports (keep last 24 snapshots per DB)
for DB in "${PROD_DBS[@]}"; do
  SNAPSHOTS=$(ls -t "$JSONL_EXPORT_DIR/${DB}-2"*.jsonl 2>/dev/null | tail -n +25)
  if [ -n "$SNAPSHOTS" ]; then
    echo "$SNAPSHOTS" | xargs rm -f
    echo "Pruned old $DB snapshots"
  fi
done

echo "Exported: $EXPORTED, failed: $EXPORT_FAILED"
```

## Step 2: Git commit and push

Commit JSONL snapshots to a backup branch and push to GitHub.

```bash
echo "=== Git Push ==="
GIT_PUSHED=false

# Check if we have a git backup repo configured
BACKUP_REPO="$HOME/gt/.dolt-archive/git"

if [ -d "$BACKUP_REPO/.git" ]; then
  cd "$BACKUP_REPO"

  # Copy latest JSONL files
  for DB in "${PROD_DBS[@]}"; do
    LATEST="$JSONL_EXPORT_DIR/${DB}-latest.jsonl"
    if [ -f "$LATEST" ]; then
      cp "$(readlink "$LATEST" || echo "$LATEST")" "$BACKUP_REPO/${DB}.jsonl"
    fi
  done

  # Check for changes
  if git diff --quiet && git diff --staged --quiet; then
    echo "No changes to commit"
  else
    git add *.jsonl
    git commit -m "Archive snapshot $(date +%Y-%m-%d-%H%M)" \
      --author="Gas Town Archive <archive@gastown.local>" 2>/dev/null

    # Check if remote exists before pushing
    if git remote get-url origin > /dev/null 2>&1; then
      if git push origin main 2>/dev/null; then
        GIT_PUSHED=true
        echo "Pushed to GitHub"
      else
        echo "WARN: Git push to remote failed (check GitHub credentials/permissions)"
      fi
    else
      echo "WARN: No git remote configured for backup repo"
      echo "  To set up: cd $BACKUP_REPO && git remote add origin <github-url>"
    fi
  fi
else
  echo "No git backup repo at $BACKUP_REPO — skipping git push"
  echo "  To set up: git init $BACKUP_REPO && cd $BACKUP_REPO && git remote add origin <url>"
fi
```

## Step 3: Dolt native push

Push production databases to GitHub/DoltHub remotes via `dolt push`.

```bash
echo "=== Dolt Push ==="
DOLT_PUSHED=0
DOLT_PUSH_FAILED=0

for DB in "${PROD_DBS[@]}"; do
  DB_DIR="$DOLT_DATA_DIR/$DB"

  if [ ! -d "$DB_DIR/.dolt" ]; then
    echo "  $DB: no .dolt directory, skipping"
    continue
  fi

  # Check if remotes are configured
  REMOTES=$(cd "$DB_DIR" && dolt remote -v 2>/dev/null | grep -v "^$" | head -5)

  if [ -z "$REMOTES" ]; then
    echo "  $DB: no remotes configured, skipping dolt push"
    continue
  fi

  echo "  $DB: pushing to remotes..."

  # Push to each remote
  cd "$DB_DIR"
  for REMOTE_NAME in $(dolt remote -v 2>/dev/null | awk '{print $1}' | sort -u); do
    if timeout 120 dolt push "$REMOTE_NAME" main 2>/dev/null; then
      echo "    $REMOTE_NAME: pushed"
      DOLT_PUSHED=$((DOLT_PUSHED + 1))
    else
      echo "    $REMOTE_NAME: FAILED"
      DOLT_PUSH_FAILED=$((DOLT_PUSH_FAILED + 1))
    fi
  done
done

echo "Dolt push: $DOLT_PUSHED succeeded, $DOLT_PUSH_FAILED failed"
```

## Step 4: Verify remote has data

Verify that the backup data has successfully reached the remote and is accessible.

```bash
echo "=== Verification ==="
VERIFY_PASSED=0
VERIFY_FAILED=0

# Verify JSONL in git backup
if [ -d "$BACKUP_REPO/.git" ]; then
  echo "Verifying git remote..."
  if cd "$BACKUP_REPO" && git ls-remote origin HEAD > /dev/null 2>&1; then
    # Try to clone into temp directory to verify
    TEMP_CLONE=$(mktemp -d)
    if git clone --depth 1 origin "$TEMP_CLONE" 2>/dev/null; then
      for DB in "${PROD_DBS[@]}"; do
        if [ -f "$TEMP_CLONE/${DB}.jsonl" ]; then
          REMOTE_COUNT=$(wc -l < "$TEMP_CLONE/${DB}.jsonl" | tr -d ' ')
          echo "  git: $DB verified ($REMOTE_COUNT lines in remote)"
          VERIFY_PASSED=$((VERIFY_PASSED + 1))
        else
          echo "  git: $DB MISSING from remote"
          VERIFY_FAILED=$((VERIFY_FAILED + 1))
        fi
      done
    else
      echo "  git: Clone verification failed"
      VERIFY_FAILED=$((VERIFY_FAILED + 1))
    fi
    rm -rf "$TEMP_CLONE"
  else
    echo "  git: Remote not accessible"
  fi
fi

# Verify dolt push (check if remotes have our commits)
for DB in "${PROD_DBS[@]}"; do
  DB_DIR="$DOLT_DATA_DIR/$DB"
  if [ -d "$DB_DIR/.dolt" ]; then
    # Check if any dolt remotes are reachable
    REMOTE_HEADS=$(cd "$DB_DIR" && dolt remote -v 2>/dev/null | awk '{print $1}' | sort -u)
    if [ -n "$REMOTE_HEADS" ]; then
      cd "$DB_DIR"
      # Verify at least one remote has data
      for REMOTE in $REMOTE_HEADS; do
        if dolt log "$REMOTE/main" -n 1 > /dev/null 2>&1; then
          echo "  dolt: $DB on $REMOTE verified"
          VERIFY_PASSED=$((VERIFY_PASSED + 1))
          break
        fi
      done
    fi
  fi
done

echo "Verified: $VERIFY_PASSED, failed: $VERIFY_FAILED"
```

## Record Result

```bash
SUMMARY="Archive: jsonl=$EXPORTED/$((EXPORTED + EXPORT_FAILED)), git=${GIT_PUSHED}, dolt_push=$DOLT_PUSHED/$((DOLT_PUSHED + DOLT_PUSH_FAILED)), verify=$VERIFY_PASSED/$((VERIFY_PASSED + VERIFY_FAILED))"
echo "=== $SUMMARY ==="

RESULT="success"
if [ "$EXPORT_FAILED" -gt 0 ] || [ "$DOLT_PUSH_FAILED" -gt 0 ] || [ "$VERIFY_FAILED" -gt 0 ]; then
  RESULT="warning"
fi

gt plugin record-run --plugin dolt-archive --result "$RESULT" \
  --title "$SUMMARY" --description "$SUMMARY" >/dev/null 2>&1 || true

if [ "$EXPORT_FAILED" -gt 0 ]; then
  gt escalate "JSONL export failed for $EXPORT_FAILED databases" \
    --severity critical \
    --reason "JSONL is our last-resort recovery layer. $EXPORT_FAILED databases failed to export."
fi
```
