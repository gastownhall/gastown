+++
name = "compactor-dog"
description = "Monitor Dolt commit growth across production DBs and escalate when compaction is needed"
version = 1

[gate]
type = "cooldown"
duration = "30m"

[tracking]
labels = ["plugin:compactor-dog", "category:maintenance"]
digest = true

[execution]
timeout = "5m"
notify_on_failure = true
severity = "medium"
+++

# Compactor Dog

Monitors Dolt commit growth across all production databases and escalates to
the Mayor when history compaction or flatten is needed. This is a judgment
call, not a hard threshold trigger.

**You are a dog agent (Claude). Gather the data below, then use your judgment
to decide if maintenance is needed.** Consider:

- Commit count per DB (absolute size)
- Growth rate (commits per hour since last check)
- Time since last flatten or compaction
- Current swarm activity (more polecats = faster growth)
- Whether growth is "normal busy" or "runaway"

## Config

```bash
DOLT_HOST="${GT_DOLT_HOST:-127.0.0.1}"
DOLT_PORT="${GT_DOLT_PORT:-3307}"
DOLT_USER="root"
DOLT_DATA_DIR="$HOME/gt/.dolt-data"
STATE_FILE="$HOME/gt/.dolt-data/.compactor-state.json"
```

## Step 1: Discover production databases

Find all active production databases on the Dolt server:

```bash
echo "=== Compactor Dog: Checking commit health ==="

PROD_DBS=$(dolt sql -q "SHOW DATABASES" \
  --host "$DOLT_HOST" --port "$DOLT_PORT" -u "$DOLT_USER" \
  --result-format csv 2>/dev/null \
  | tail -n +2 \
  | grep -v -E '^(information_schema|mysql|dolt_cluster|testdb_|beads_t|beads_pt|doctest_)' \
  | tr -d '\r')

if [ -z "$PROD_DBS" ]; then
  echo "SKIP: No production databases found (is Dolt running?)"
  exit 0
fi

echo "Production databases: $(echo "$PROD_DBS" | tr '\n' ' ')"
```

## Step 2: Count commits per database

Query each database's commit history:

```bash
echo ""
echo "=== Commit Counts ==="

REPORT=""
TOTAL_COMMITS=0
NOW=$(date +%s)

while IFS= read -r DB; do
  [ -z "$DB" ] && continue

  # Total commit count
  COUNT=$(dolt sql -q "SELECT count(*) AS cnt FROM dolt_log" \
    --host "$DOLT_HOST" --port "$DOLT_PORT" -u "$DOLT_USER" \
    -d "$DB" --result-format csv 2>/dev/null \
    | tail -1 | tr -d '\r')

  # Commits in last hour (growth rate indicator)
  RECENT=$(dolt sql -q "SELECT count(*) AS cnt FROM dolt_log WHERE date > DATE_SUB(NOW(), INTERVAL 1 HOUR)" \
    --host "$DOLT_HOST" --port "$DOLT_PORT" -u "$DOLT_USER" \
    -d "$DB" --result-format csv 2>/dev/null \
    | tail -1 | tr -d '\r')

  # Commits in last 24h
  DAILY=$(dolt sql -q "SELECT count(*) AS cnt FROM dolt_log WHERE date > DATE_SUB(NOW(), INTERVAL 24 HOUR)" \
    --host "$DOLT_HOST" --port "$DOLT_PORT" -u "$DOLT_USER" \
    -d "$DB" --result-format csv 2>/dev/null \
    | tail -1 | tr -d '\r')

  # Oldest commit date (approximation of last flatten)
  OLDEST=$(dolt sql -q "SELECT MIN(date) AS oldest FROM dolt_log" \
    --host "$DOLT_HOST" --port "$DOLT_PORT" -u "$DOLT_USER" \
    -d "$DB" --result-format csv 2>/dev/null \
    | tail -1 | tr -d '\r')

  LINE="$DB: total=$COUNT, last_1h=$RECENT, last_24h=$DAILY, oldest_commit=$OLDEST"
  echo "  $LINE"
  REPORT="$REPORT\n$LINE"
  TOTAL_COMMITS=$((TOTAL_COMMITS + ${COUNT:-0}))
done <<< "$PROD_DBS"

echo ""
echo "Total commits across all DBs: $TOTAL_COMMITS"
```

## Step 3: Check swarm activity

Count active polecats and dogs to gauge expected commit velocity:

```bash
echo ""
echo "=== Swarm Activity ==="

# Count active tmux sessions (proxy for agent activity)
POLECAT_SESSIONS=$(tmux list-sessions -F '#{session_name}' 2>/dev/null \
  | grep -c 'polecat\|pcat' || echo 0)
DOG_SESSIONS=$(tmux list-sessions -F '#{session_name}' 2>/dev/null \
  | grep -c 'dog' || echo 0)
TOTAL_SESSIONS=$(tmux list-sessions 2>/dev/null | wc -l | tr -d ' ')

echo "  Active polecats: $POLECAT_SESSIONS"
echo "  Active dogs: $DOG_SESSIONS"
echo "  Total sessions: $TOTAL_SESSIONS"
```

## Step 4: Load previous state

Check when the last compaction or flatten happened:

```bash
echo ""
echo "=== Previous State ==="

if [ -f "$STATE_FILE" ]; then
  LAST_CHECK=$(cat "$STATE_FILE" 2>/dev/null)
  echo "  Last state: $LAST_CHECK"
else
  echo "  No previous state found (first run)"
  LAST_CHECK="{}"
fi

# Check for recent compactor-dog or flatten runs in beads
RECENT_RUNS=$(bd list --label plugin:compactor-dog --status closed --json 2>/dev/null \
  | jq -r '.[0].created_at // "never"' 2>/dev/null || echo "unknown")
echo "  Last compactor run: $RECENT_RUNS"

# Check for flatten evidence (single-commit history = recently flattened)
FLATTEN_CANDIDATES=""
while IFS= read -r DB; do
  [ -z "$DB" ] && continue
  COUNT=$(dolt sql -q "SELECT count(*) AS cnt FROM dolt_log" \
    --host "$DOLT_HOST" --port "$DOLT_PORT" -u "$DOLT_USER" \
    -d "$DB" --result-format csv 2>/dev/null \
    | tail -1 | tr -d '\r')
  if [ "${COUNT:-0}" -le 5 ]; then
    FLATTEN_CANDIDATES="$FLATTEN_CANDIDATES $DB(${COUNT})"
  fi
done <<< "$PROD_DBS"

if [ -n "$FLATTEN_CANDIDATES" ]; then
  echo "  Recently flattened DBs:$FLATTEN_CANDIDATES"
fi
```

## Step 5: Save current state

Record this check's data for the next run to compare growth rates:

```bash
# Save state for next run
cat > "$STATE_FILE" << STATEOF
{
  "checked_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "total_commits": $TOTAL_COMMITS,
  "active_polecats": $POLECAT_SESSIONS,
  "active_dogs": $DOG_SESSIONS
}
STATEOF

echo ""
echo "State saved to $STATE_FILE"
```

## Step 6: Make the judgment call

**This is where you (the dog agent) use judgment.** Review all the data
gathered above and decide whether to escalate.

**Guidelines for judgment** (not rules — context matters):

| Signal | Comfortable | Getting warm | Escalate |
|--------|------------|--------------|----------|
| Total commits (per DB) | <200 | 200-500 | >500 |
| Hourly growth rate | <10/hr | 10-30/hr | >30/hr |
| Daily growth rate | <100/day | 100-300/day | >300/day |
| Time since flatten | <2 weeks | 2-4 weeks | >4 weeks |

**But override the table if context warrants it:**
- 400 commits after a 10-polecat swarm = normal, will settle
- 200 commits growing at 50/hr with no swarm = something's wrong
- Any DB over 1000 commits = escalate regardless

**If you judge maintenance is needed:**

```bash
gt escalate "Dolt compaction recommended" \
  -s MEDIUM \
  --reason "Commit growth analysis:
$REPORT

Total: $TOTAL_COMMITS commits across all DBs
Active polecats: $POLECAT_SESSIONS
Recommendation: Run compaction on databases exceeding comfort threshold.
See dolt-storage.md for procedure."
```

**If everything looks comfortable, just record the result:**

```bash
echo "All databases within comfortable commit ranges. No action needed."
```

## Record Result

```bash
SUMMARY="Compactor check: $TOTAL_COMMITS total commits across $(echo "$PROD_DBS" | wc -l | tr -d ' ') DBs, $POLECAT_SESSIONS active polecats"
echo "=== $SUMMARY ==="
```

On success (no escalation needed):
```bash
gt plugin record-run --plugin compactor-dog --result success \
  --title "compactor-dog: $SUMMARY" --description "$SUMMARY" >/dev/null 2>&1 || true
```

On escalation:
```bash
gt plugin record-run --plugin compactor-dog --result warning \
  --title "compactor-dog: ESCALATED - $SUMMARY" \
  --description "Escalated to Mayor for compaction. $SUMMARY" >/dev/null 2>&1 || true
```

On failure:
```bash
gt plugin record-run --plugin compactor-dog --result failure \
  --title "compactor-dog: FAILED" \
  --description "Compactor check failed: $ERROR" >/dev/null 2>&1 || true

gt escalate "Plugin FAILED: compactor-dog" \
  --severity medium \
  --reason "$ERROR"
```
