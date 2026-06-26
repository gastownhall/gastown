+++
name = "dolt-snapshots"
description = "Tag Dolt databases at convoy boundaries for audit, diff, and rollback"
version = 3

[gate]
type = "event"
on = "convoy.created"

[tracking]
labels = ["plugin:dolt-snapshots", "category:data-safety"]
digest = true

[execution]
timeout = "2m"
notify_on_failure = true
severity = "low"
+++

# Dolt Snapshots v3

Snapshots Dolt databases at convoy lifecycle boundaries using **tags** (immutable)
and optionally **branches** (mutable, for working diffs).

Implemented as a standalone Go binary with parameterized SQL — no shell
interpolation, no subshell bugs, no auto-committing dirty state.

## What this enables

**Convoy audit** — verify agents did what they were supposed to:
```sql
SELECT * FROM dolt_diff('staged/pi-rust-bug-fixes-hq-cv-xrwki', 'HEAD', 'issues')
SELECT * FROM dolt_diff_stat('staged/pi-rust-bug-fixes-hq-cv-xrwki', 'HEAD')
```

**Convoy rollback** — revert a database to pre-convoy state:
```sql
CALL DOLT_CHECKOUT('staged/pi-rust-bug-fixes-hq-cv-xrwki');          -- whole DB
CALL DOLT_CHECKOUT('staged/pi-rust-bug-fixes-hq-cv-xrwki', 'issues'); -- single table
```

**Cross-convoy comparison** — track progress between runs:
```sql
SELECT * FROM dolt_diff('staged/pi-rust-bug-fixes-hq-cv-xrwki', 'staged/otel-dashboard-hq-cv-7q3vi', 'issues')
```

**Data loss investigation** — when backup alerts fire, diff against last snapshot:
```sql
SELECT * FROM dolt_diff('staged/pi-rust-bug-fixes-hq-cv-xrwki', 'HEAD', 'issues')
WHERE diff_type = 'removed'
```

## What branches enable (mutable sandboxes)

Branches are writable copies of the database at snapshot time. Unlike tags,
you can commit to them — making them useful for:

- **Dry-run convoy work** — test bulk operations without touching main
- **Isolated convoy writes** — agents write to branch, refinery merges
- **What-if analysis** — test theories without risk
- **Parallel convoy isolation** — two convoys write to separate branches

## Why tags over branches

- A branch is just a pointer that moves with new commits — not a true snapshot
- A tag is immutable: `staged/pi-rust-bug-fixes-hq-cv-xrwki` always points to
  the exact state when the convoy was staged
- Tags survive branch cleanup and are cheaper to keep long-term
- `dolt diff staged/convoy-A staged/convoy-B` works with tags

## Trigger

This is one of three event-gated plugins sharing the same Go binary:

| Plugin | Event | Snapshot |
|--------|-------|----------|
| `dolt-snapshots` | `convoy.created` | `open/` tags (pre-work baseline) |
| `dolt-snapshots-staged` | `convoy.staged` | `staged/` tags + branches (staging baseline) |
| `dolt-snapshots-launched` | `convoy.launched` | `staged/` tags + branches (launch baseline) |

Each fires on its specific event. The binary is idempotent — it checks all
convoys and creates whichever tags/branches are missing.

## Step 1: Build and start the snapshot watcher

The Go binary handles all Dolt operations with parameterized SQL.
It connects using gastown's configured Dolt endpoint (default 127.0.0.1:3307,
root, no password) and reads routes.jsonl to discover rig databases.

In `--watch` mode, the binary tails `~/.events.jsonl` and runs a snapshot cycle
immediately (<1s) when convoy events are detected. This is much faster than the
~60s deacon patrol polling approach — critical for `convoy.launched` where agents
start writing to databases immediately.

```bash
PLUGIN_DIR="$(dirname "$0")"
PIDFILE="$PLUGIN_DIR/.snapshot.pid"

# If watcher already running, skip
if [ -f "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
  echo "Snapshot watcher already running (PID $(cat "$PIDFILE"))"
  exit 0
fi

# Build if binary missing or source is newer
if [ ! -f "$PLUGIN_DIR/snapshot" ] || [ "$PLUGIN_DIR/main.go" -nt "$PLUGIN_DIR/snapshot" ]; then
  echo "Building dolt-snapshots binary..."
  cd "$PLUGIN_DIR" && go build -o snapshot . 2>&1
  if [ $? -ne 0 ]; then
    echo "FATAL: Go build failed"
    exit 1
  fi
fi

# Run one-shot first to catch up on anything missed while watcher was down
"$PLUGIN_DIR/snapshot" --cleanup --routes "$HOME/gt/.beads/routes.jsonl"
SNAPSHOT_EXIT=$?

if [ $SNAPSHOT_EXIT -ne 0 ]; then
  echo "Snapshot catch-up exited with code $SNAPSHOT_EXIT"
fi

# Start watcher in background (sub-second response to convoy events)
nohup "$PLUGIN_DIR/snapshot" --watch --routes "$HOME/gt/.beads/routes.jsonl" \
  >> "$PLUGIN_DIR/.snapshot.log" 2>&1 &
echo $! > "$PIDFILE"
echo "Snapshot watcher started (PID $!)"
```

## Step 2: Record result

```bash
RESULT="success"
if [ $SNAPSHOT_EXIT -ne 0 ]; then
  RESULT="failure"
fi

gt plugin record-run --plugin dolt-snapshots --result "$RESULT" \
  --title "dolt-snapshots: $RESULT" \
  --description "dolt-snapshots plugin completed with exit code $SNAPSHOT_EXIT. Watcher started." >/dev/null 2>&1 || true
```
