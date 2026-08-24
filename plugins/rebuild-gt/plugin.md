+++
name = "rebuild-gt"
description = "Rebuild stale gt binary from gastown source"
version = 2

[gate]
type = "cooldown"
duration = "1h"

[tracking]
labels = ["plugin:rebuild-gt", "category:maintenance"]
digest = true

[execution]
timeout = "5m"
notify_on_failure = true
severity = "medium"
+++

# Rebuild gt Binary

Checks if the gt binary is stale (built from older commit than HEAD) and rebuilds.

**SAFETY**: This plugin MUST only rebuild forward (binary ancestor of HEAD) and
only from the main branch. Rebuilding to an older or diverged commit caused a
crash loop where every new session's startup hook failed, the witness respawned
it, and the loop repeated every 1-2 minutes.

## Gate Check

The Deacon evaluates this before dispatch. If gate closed, skip.

## Configuration

`GT_SOURCE_RIG` names the registered rig that carries the gt source checkout
(source lives at `<rig>/mayor/rig`). Defaults to `gastown_fork`, the
registered downstream fork rig; override for other topologies.

## Detection

Check binary staleness:

```bash
gt stale --json
```

Parse the JSON output and check these fields:
- If `"stale": false` → record success wisp and exit early (binary is fresh)
- If `"safe_to_rebuild": false` → **DO NOT REBUILD**. Record a skip wisp and exit.
  This means the repo is on a non-main branch or HEAD is not a descendant of the
  binary commit (would be a downgrade).
- If `"safe_to_rebuild": true` → proceed to build

If `safe_to_rebuild` is false, record a skip wisp:
```bash
gt plugin record-run --plugin rebuild-gt --result skipped --rig "$SOURCE_RIG" \
  --title "Plugin: rebuild-gt [skipped]" \
  --description "Skipped: not safe to rebuild (forward=$FORWARD, main=$ON_MAIN)" >/dev/null 2>&1 || true
```

## Pre-flight Checks

Before building, verify the source repo is clean and on main:

```bash
cd ~/gt/${GT_SOURCE_RIG:-gastown_fork}/mayor/rig
git status --porcelain  # Must be clean
git branch --show-current  # Must be "main"
```

If either check fails, skip the rebuild and record a wisp.

## Action

Rebuild from source (the mayor/rig directory is the canonical source):

```bash
cd ~/gt/${GT_SOURCE_RIG:-gastown_fork}/mayor/rig && make build && make safe-install
```

**IMPORTANT**: Use `make safe-install` (not `make install`) to avoid restarting
the daemon while sessions are active. safe-install replaces the binary but does
NOT restart the daemon — sessions will pick up the new binary on their next cycle.

## Record Result

On success:
```bash
gt plugin record-run --plugin rebuild-gt --result success --rig "$SOURCE_RIG" \
  --title "Plugin: rebuild-gt [success]" \
  --description "Rebuilt gt: $OLD → $NEW ($N commits)" >/dev/null 2>&1 || true
```

On failure:
```bash
gt plugin record-run --plugin rebuild-gt --result failure --rig "$SOURCE_RIG" \
  --title "Plugin: rebuild-gt [failure]" \
  --description "Build failed: $ERROR" >/dev/null 2>&1 || true

gt escalate --severity=medium \
  --subject="Plugin FAILED: rebuild-gt" \
  --body="$ERROR" \
  --source="plugin:rebuild-gt"
```
