# Dolt Storage Architecture

> **Status**: Current reference for Gas Town agents
> **Updated**: 2026-02-28
> **Context**: Dolt is the sole storage backend for Beads and Gas Town

---

## Overview

Gas Town uses [Dolt](https://github.com/dolthub/dolt), an open-source
SQL database with Git-like versioning (Apache 2.0). One Dolt SQL server
per town serves all databases via MySQL protocol on port 3307. There is
no embedded mode and no SQLite. JSONL is used only for disaster-recovery
backups (the JSONL Dog exports scrubbed snapshots every 15 minutes to a
git-backed archive), not as a primary storage format.

The `gt daemon` manages the server lifecycle (auto-start, health checks
every 30s, crash restart with exponential backoff).

## Server Architecture

```
Dolt SQL Server (one per town, port 3307)
├── hq/       town-level beads  (hq-* prefix)
├── gastown/  rig beads         (gt-* prefix)
├── beads/    rig beads         (bd-* prefix)
├── wyvern/   rig beads         (wy-* prefix)
└── sky/      rig beads         (sky-* prefix)
```

**Data directory**: `~/gt/.dolt-data/` — each subdirectory is a database
accessible via `USE <name>` in SQL.

**Connection**: `root@tcp(<host>:3307)/<database>` (no password).

## Environment Variables

gt and bd use separate env vars for Dolt connection. gt automatically
translates its variables to bd's equivalents when spawning agents.

| gt (Gas Town) | bd (Beads) | Purpose |
|---------------|------------|---------|
| `GT_DOLT_HOST` | `BEADS_DOLT_SERVER_HOST` | Server host (bd defaults to `127.0.0.1` if unset) |
| `GT_DOLT_PORT` | `BEADS_DOLT_PORT` | Server port (default: `3307`) |
| `GT_DOLT_EXTERNAL` | — (gt-only ownership flag) | Marks the endpoint as externally managed, even on loopback |

**Remote Dolt servers**: If Dolt runs on a different machine (e.g., over
Tailscale), set `GT_DOLT_HOST` in the environment. gt propagates this as
`BEADS_DOLT_SERVER_HOST` to all bd subprocesses, overriding bd's hardcoded
`127.0.0.1` default. Without this, every new rig/worktree/polecat silently
connects to localhost and fails.

Per-workspace override: set `dolt.host` in a rig's `.beads/config.yaml`.
This takes priority over the env var for that specific workspace.

**Externally managed local servers**: When the Dolt server is owned by
another process on this machine (e.g., Beads' shared server on `:3308`),
set `gt config set dolt.external true`. This writes `GT_DOLT_EXTERNAL=true`
to `mayor/daemon.json` and changes ownership semantics everywhere:

- `gt dolt start`/`stop`, `gt up`/`start`/`down`, and the daemon's Dolt
  manager never start, stop, restart, or kill the shared server;
- `gt doctor` probes the declared endpoint instead of rewriting
  `.beads/metadata.json` to the local managed port;
- endpoint resolution (host/port) prefers `daemon.json` over the local
  `.dolt-data/config.yaml`, which may describe a stale local server;
- `GT_DOLT_EXTERNAL` is propagated to spawned agents alongside the port.

Lifecycle commands that require Town-owned storage (`gt dolt migrate`,
`recover`, `rollback`, `flatten`, `rebase`, `sync`, `pull`, `maintain`)
refuse to run against an externally managed endpoint.

## Commands

```bash
# Daemon manages server lifecycle (preferred)
gt daemon start

# Manual management
gt dolt start          # Start server
gt dolt stop           # Stop server
gt dolt status         # Health check, list databases
gt dolt logs           # View server logs
gt dolt sql            # Open SQL shell
gt dolt init-rig <X>   # Create a new rig database
gt dolt list           # List all databases
```

If the server isn't running, `bd` fails fast with a clear message
pointing to `gt dolt start`.

## Gas Town Scope vs `bd --global`

Gas Town's town-level beads are the `hq` database. Access them by running
direct `bd` commands from the town root (`~/gt`) or with `bd -C ~/gt ...`.
Direct `bd` commands from rig worktrees use that rig's `.beads` redirect and
database, so do not assume an `hq-*` ID will retarget the command.

Do not use `bd --global` for Gas Town town beads. In Beads, `--global`
means the standalone shared-server database named `beads_global`; it does
not mean Gas Town's `hq` database, and `BEADS_DOLT_DATABASE=hq` does not
retarget `--global`.

For Gas Town Dolt health, use `gt dolt status`. `bd dolt status` reports
the Beads client/runtime view and can say no Beads-managed server is running
even when the Gas Town Dolt server on port 3307 is healthy.

## Write Concurrency: All-on-Main

All agents — polecats, crew, witness, refinery, deacon — write directly
to `main`. Concurrency is managed through transaction discipline: every
write wraps `BEGIN` / `DOLT_COMMIT` / `COMMIT` atomically.

```
bd update <bead> --status=in_progress
  → BEGIN
  → UPDATE issues SET status='in_progress' ...
  → CALL DOLT_COMMIT('-Am', 'update status')
  → COMMIT
```

This eliminates the former branch-per-worker strategy (BD_BRANCH,
per-polecat Dolt branches, merge-at-done). All writes are immediately
visible to all agents — no cross-agent visibility gaps.

Multi-statement `bd` commands batch their writes inside a single
transaction to maintain atomicity.

## Schema

Schema version 6. The full schema lives in `beads/.../storage/dolt/schema.go`.
Key tables shown below; see source for indexes and full column lists.

```sql
-- Core: every bead is a row in issues (tasks, messages, agents, gates, etc.)
CREATE TABLE issues (
    id VARCHAR(255) PRIMARY KEY,
    title VARCHAR(500) NOT NULL,
    description TEXT NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'open',
    priority INT NOT NULL DEFAULT 2,
    issue_type VARCHAR(32) NOT NULL DEFAULT 'task',
    assignee VARCHAR(255),
    owner VARCHAR(255) DEFAULT '',
    sender VARCHAR(255) DEFAULT '',          -- messaging
    mol_type VARCHAR(32) DEFAULT '',         -- molecule type
    work_type VARCHAR(32) DEFAULT 'mutex',   -- mutex vs open_competition
    hook_bead VARCHAR(255) DEFAULT '',       -- agent hook
    role_bead VARCHAR(255) DEFAULT '',       -- agent role
    agent_state VARCHAR(32) DEFAULT '',      -- agent lifecycle
    wisp_type VARCHAR(32) DEFAULT '',        -- TTL-based compaction class
    metadata JSON DEFAULT (JSON_OBJECT()),   -- extensible metadata
    created_at DATETIME, updated_at DATETIME, closed_at DATETIME
    -- ... plus ~20 more columns (see schema.go)
);

-- Relationships between beads
CREATE TABLE dependencies (
    issue_id VARCHAR(255) NOT NULL,
    depends_on_id VARCHAR(255) NOT NULL,
    type VARCHAR(32) NOT NULL DEFAULT 'blocks',   -- blocks, parent-child, thread
    PRIMARY KEY (issue_id, depends_on_id)
);

-- Labels (many-to-many)
CREATE TABLE labels (
    issue_id VARCHAR(255) NOT NULL,
    label VARCHAR(255) NOT NULL,
    PRIMARY KEY (issue_id, label)
);

-- Audit trail
CREATE TABLE comments (id BIGINT AUTO_INCREMENT PRIMARY KEY, issue_id, author, text, created_at);
CREATE TABLE events   (id BIGINT AUTO_INCREMENT PRIMARY KEY, issue_id, event_type, actor, old_value, new_value, created_at);

-- Agent interaction log
CREATE TABLE interactions (id, kind, actor, issue_id, model, prompt, response, created_at);

-- Infrastructure
CREATE TABLE config          (key PRIMARY KEY, value);       -- runtime config knobs
CREATE TABLE metadata        (key PRIMARY KEY, value);       -- schema version, etc.
CREATE TABLE routes          (prefix PRIMARY KEY, path);     -- prefix→database routing
CREATE TABLE issue_counter   (prefix PRIMARY KEY, last_id);  -- sequential ID generation
CREATE TABLE child_counters  (parent_id PRIMARY KEY, last_child);
CREATE TABLE federation_peers (name PRIMARY KEY, remote_url, sovereignty, last_sync);

-- Compaction
CREATE TABLE issue_snapshots     (id, issue_id, compaction_level, original_content, ...);
CREATE TABLE compaction_snapshots (id, issue_id, compaction_level, snapshot_json, ...);
CREATE TABLE repo_mtimes         (repo_path PRIMARY KEY, mtime_ns, last_checked);
```

**Wisps** (ephemeral patrol data) reuse the same `issues` table with
`wisp_type` set. They are Dolt-ignored (`dolt_ignore` table) so wisp
mutations don't generate Dolt commits — only structural changes to the
ignore config itself are committed.

**Mail** is implemented as beads with `issue_type='message'` in the
issues table — there is no separate mail table. The `sender` field and
`dependencies` (type='thread') provide threading.

## Dolt-Specific Capabilities

These are available to agents via SQL and used throughout Gas Town:

| Feature | Usage |
|---------|-------|
| `dolt_history_*` tables | Full row-level history, queryable via SQL |
| `AS OF` queries | Time-travel: "what did this look like yesterday?" |
| `dolt_diff()` | "What changed between these two points?" |
| `DOLT_COMMIT` | Explicit commit with message (auto-commit is the default) |
| `DOLT_MERGE` | Merge branches (integration branches, federation) |
| `dolt_conflicts` table | Programmatic conflict resolution after merge |
| `DOLT_BRANCH` | Create/delete branches (integration branches) |

**Auto-commit** is on by default: every write gets a Dolt commit. Agents
can batch writes by disabling auto-commit temporarily.

**Conflict resolution** default: `newest` (most recent `updated_at` wins).
Arrays (labels): `union` merge. Counters: `max`.

## Three Data Planes

Beads data falls into three planes with different characteristics:

| Plane | What | Mutation | Durability | Transport | Status |
|-------|------|----------|------------|-----------|--------|
| **Operational** | Work in progress, status, assignments, heartbeats | High (seconds) | Days–weeks | Dolt SQL server (local) | **Live** |
| **Ledger** | Completed work, permanent record | Low (completion boundaries) | Permanent | JSONL export → git push to GitHub | **Live** |
| **Design** | Epics, RFCs, specs — ideas not yet claimed | Conversational | Until crystallized | DoltHub commons (shared) | **Planned** |

The operational plane lives entirely in the local Dolt server. The ledger
plane is currently served by the JSONL Dog, which exports scrubbed snapshots
to a git-backed archive every 15 minutes — this is the durable record that
survives disasters (proven in Clown Show #13). The design plane will
federate via DoltHub as part of the Wasteland commons (planned, not yet
in active development).

## Data Lifecycle: Think Git, Not SQL (CRITICAL)

Dolt is git under the hood. **The commit graph IS the storage cost, not the
rows.** Every `bd create`, `bd update`, `bd close` generates a Dolt commit.
DELETE a row and the commit that wrote it still exists in history. `dolt gc`
reclaims unreferenced chunks, but the commit graph itself grows forever.

This is the key insight from Tim Sehn (Dolt founder, 2026-02-27):

> "Your Beads databases are small but your commit history is big."
>
> "If you delete a bead you want to rebase with the commit that wrote it
> so it just isn't there any more in history."

**Rebase** (`CALL DOLT_REBASE()`, available since v1.81.2) rewrites the
commit graph — it's the real cleanup mechanism. DELETE + gc is necessary
but insufficient. DELETE + rebase + gc is the full pipeline.

**Critical update** (Tim Sehn, 2026-02-28): All compaction operations —
`DOLT_RESET --soft`, `DOLT_REBASE()`, `dolt_gc()` — are **safe on a
running server**. Routine compaction does not need downtime. Auto-GC has
been ON by default since Dolt 1.75.0. Flatten is trivially cheap (pointer
moves, not data writes). Can run daily or more frequently.

Reference: https://www.dolthub.com/blog/2026-01-28-everybody-rebase/

### The Six-Stage Lifecycle

```
CREATE → LIVE → CLOSE → DECAY → COMPACT → FLATTEN
  │        │       │        │        │          │
  Dolt   active   done   DELETE   REBASE     SQUASH
  commit  work    bead    rows    commits    all history
                         >7-30d  together   to 1 commit
```

| Stage | Owner | Frequency | Mechanism |
|-------|-------|-----------|-----------|
| CREATE | Any agent | Continuous | `bd create`, `bd mol wisp create` |
| CLOSE | Agent or patrol | Per-task | `bd close`, `gt done` |
| DECAY | Reaper Dog | Daily | `DELETE FROM wisps WHERE status='closed' AND age > 7d` |
| COMPACT | Compactor Dog | Daily | `DOLT_RESET --soft` + `DOLT_COMMIT` (safe on running server) |
| FLATTEN | Compactor Dog | Daily | Same as COMPACT — no downtime, no maintenance window |

All six stages are implemented in code. DECAY runs in the Reaper Dog
(wisp_reaper.go), COMPACT/FLATTEN run in the Compactor Dog (compactor_dog.go).
All lifecycle tickers are enabled by default via `EnsureLifecycleDefaults()`
(lifecycle_defaults.go), which auto-populates daemon.json with sensible
defaults on `gt init` or `gt up`. Explicitly disabled patrols are preserved.

### Two Data Streams

```
EPHEMERAL (wisps, patrol data)          PERMANENT (issues, molecules, agents)
  CREATE                                  CREATE
  → work                                  → work
  → CLOSE (>24h)                          → CLOSE
  → DELETE rows (Reaper)                  → JSONL export (scrubbed)
  → REBASE history (Compactor)            → git push to GitHub
  → gc unreferenced chunks (Compactor)    → COMPACT/FLATTEN daily (no downtime)
```

**Ephemeral data** (wisps, wisp_events, wisp_labels, wisp_deps) is
high-volume patrol exhaust. Valuable in real-time, worthless after 24h.
The Reaper Dog DELETES the rows. The Compactor Dog flattens the commits
that wrote them out of history. Without both, storage grows without bound.

**Permanent data** (issues, molecules, agents, dependencies, labels) is
the ledger. Even permanent data benefits from history compaction — a bead
that was created, updated 5 times, and closed generates 7 commits that
can be rebased into 1. The data survives; the intermediate history doesn't.

### History Compaction Operations

**Daily compaction** (Compactor Dog or Dolt scheduled event):

All compaction operations are safe on a running server — no downtime
needed. Can also be wired as a Dolt scheduled event (MySQL-style cron):
https://www.dolthub.com/blog/2023-10-02-scheduled-events/

```sql
-- Simple daily flatten (squash everything older than working set)
SET @init = (SELECT commit_hash FROM dolt_log ORDER BY date ASC LIMIT 1);
CALL DOLT_RESET('--soft', @init);
CALL DOLT_COMMIT('-Am', 'daily compaction');
```

**Flatten** (safe on a running server — no downtime needed):

Flatten is trivially cheap. `dolt_reset --soft` doesn't write any data —
it moves the parent pointer of the working set to the referenced commit.
The subsequent commit writes a new commit and two small pointer writes.
Can be run daily or even more frequently (Tim Sehn, 2026-02-28).

```sql
-- Flatten via SQL on a running server (preferred)
-- Find the initial commit
SET @init = (SELECT commit_hash FROM dolt_log ORDER BY date ASC LIMIT 1);
CALL DOLT_RESET('--soft', @init);
CALL DOLT_COMMIT('-Am', 'flatten: squash history');
-- GC runs automatically when journal exceeds 50MB
```

Concurrent writes during a flatten are safe — the merge base becomes
the initial commit, but the diff is just what the transaction wrote,
so the merge succeeds.

**Surgical compaction** via interactive rebase (squash old, keep recent):

Unlike flatten (which squashes everything), interactive rebase lets you
keep recent individual commits while squashing old history. Runs on a
live server. Based on Jason Fulghum's rebase implementation.

**Concurrent write hazard**: DOLT_REBASE is NOT safe with concurrent writes
(Tim Sehn, 2026-02-28). If agents commit to the database during rebase, Dolt
detects the graph change and errors. The Compactor Dog retries once on such
errors. Flatten mode (DOLT_RESET --soft) is unaffected — concurrent writes
are safe there because the merge base shifts but the diff is just the txn.

```sql
-- 1. Create a branch at the initial commit (the rebase "upstream")
SET @init = (SELECT commit_hash FROM dolt_log ORDER BY date ASC LIMIT 1);
CALL DOLT_BRANCH('compact-base', @init);

-- 2. Create a work branch from main (never rebase main directly)
CALL DOLT_BRANCH('compact-work', 'main');
CALL DOLT_CHECKOUT('compact-work');

-- 3. Start interactive rebase — populates dolt_rebase system table
--    All commits between compact-base and compact-work go into the plan
CALL DOLT_REBASE('--interactive', 'compact-base');

-- 4. Modify the plan: squash old commits, keep recent ones
--    First commit must stay 'pick' (squash needs a parent to fold into).
--    Keep last N commits as 'pick', squash everything else.
SET @keep_recent = 50;  -- keep last 50 commits individual
UPDATE dolt_rebase SET action = 'squash'
WHERE rebase_order > (SELECT MIN(rebase_order) FROM dolt_rebase)
  AND rebase_order <= (SELECT MAX(rebase_order) FROM dolt_rebase) - @keep_recent;

-- 5. Execute the rebase plan
CALL DOLT_REBASE('--continue');

-- 6. Swap branches: make compact-work the new main
CALL DOLT_CHECKOUT('compact-work');
CALL DOLT_BRANCH('-D', 'main');
CALL DOLT_BRANCH('-m', 'compact-work', 'main');
CALL DOLT_BRANCH('-D', 'compact-base');
CALL DOLT_CHECKOUT('main');
-- GC runs automatically when journal exceeds 50MB
```

**Rebase actions** (from `dolt_rebase` table):
- `pick` — keep commit as-is
- `squash` — fold into previous commit, concatenate messages
- `fixup` — fold into previous commit, discard message
- `drop` — remove commit entirely
- `reword` — keep commit, change message

**Caveat**: Conflicts during rebase cause automatic abort (no manual
resolution yet). The simple flatten is more reliable for daily use;
surgical rebase is for cases where you need to preserve some history.

Reference: https://www.dolthub.com/blog/2024-01-03-announcing-dolt-rebase/

### Dolt GC

`dolt gc` compacts old chunk data AFTER rebase removes commits from the
graph. Run gc after rebase, not instead of it. Order matters: rebase
first, gc second.

**Automatic GC is ON by default** since Dolt 1.75.0 (October 2025). It
triggers when the journal file (`.dolt/noms/vvvv...`) reaches 50MB. No
manual gc or server stop is required — the server handles it.

Gas Town managed Dolt configs should keep `auto_gc_behavior` enabled with
`archive_level: 1` so the sql-server does not retain every old chunk index
and grow RSS indefinitely. The config is regenerated only when the managed
Dolt server starts; a deployed change takes effect on the next Dolt restart.
Set `GT_DOLT_AUTO_GC=off` before that restart to emit `enable: false` and
`archive_level: 0` as an operational rollback switch.

If auto-GC was disabled long enough for a database to bloat, schedule one
explicit `dolt gc --full --archive-level=1` in a maintenance window to reset
the storage/RSS baseline. That one-time reclaim is an operator action, not a
hidden daemon task; after it completes, auto-GC handles ongoing chunk
reclamation.

```sql
-- Manual gc (safe on a running server, no need to stop)
CALL dolt_gc();
```

GC is memory-hungry but our databases are small, so no concern (Tim Sehn,
2026-02-28).

### Dolt Scheduled Events (Spike Results, 2026-02-28)

Dolt supports MySQL-style `CREATE EVENT` for server-maintained cron jobs.
Reference: https://www.dolthub.com/blog/2023-10-02-scheduled-events/

**Tested on Dolt 1.82.6:**
- `CREATE EVENT ... EVERY 1 DAY DO CALL dolt_gc()` — works
- Events persist in `dolt_schemas` table — survive server restart
- Events only fire on the `main` branch
- Stored procedures work (`CREATE PROCEDURE` with DECLARE, BEGIN...END)
- Events can call stored procedures
- Minimum interval: 30 seconds (Dolt enforces this floor)

**Can scheduled events replace the Compactor Dog?**

**No.** The Compactor Dog's 10-step flatten algorithm requires safety features
that SQL events cannot provide:
- Threshold checking (only compact when commit count exceeds N)
- Integrity verification (row count comparison pre/post)
- Concurrency abort (detects if main HEAD moved during compaction)
- Error escalation (notifies Mayor on failure)
- Cross-database iteration (single patrol handles all DBs)
- Daemon-level logging and observability

A stored procedure could implement the raw flatten SQL, but lacks escalation,
observability, and integration with the daemon lifecycle.

**What scheduled events CAN do:**
- Supplement the Compactor Dog with explicit `dolt_gc()` scheduling
- But auto-gc is already ON by default since Dolt 1.75.0, making this redundant

**Recommendation:** Keep the Compactor Dog for flatten. Auto-gc handles chunk
reclamation. Scheduled events add no value beyond what we already have.

### Pollution Prevention

Pollution enters Dolt via four vectors:

1. **Commit graph growth**: Every mutation = a commit. Rebase compacts.
2. **Mail pollution**: Agents overuse `gt mail send` for routine comms.
   Use `gt nudge` (ephemeral, zero Dolt cost) instead. See mail-protocol.md.
3. **Test artifacts**: Test code creating issues on production server.
   Firewall in store.go refuses test-prefixed CREATE DATABASE on port 3307.
4. **Zombie processes**: Test dolt-server processes that outlive tests.
   Doctor Dog kills these. 45 zombies (7GB RAM) found and killed 2026-02-27.

Prevention is layered:
- **Prompting**: Agents prefer `gt nudge` over `gt mail send` (zero commits)
- **Firewall** (store.go): refuses test-prefixed CREATE DATABASE on port 3307
- **Reaper Dog**: DELETEs closed wisps, auto-closes stale issues
- **Compactor Dog**: flattens old commits to compress history, runs gc after
- **Doctor Dog**: kills zombie servers, detects orphan DBs, monitors health
- **JSONL Dog**: scrubs exports, rejects pollution, spike-detects before commit

All Dogs are enabled by default via `EnsureLifecycleDefaults()` in
lifecycle_defaults.go. The daemon auto-populates missing patrol entries
in daemon.json on startup (`gt init` / `gt up`). To disable a specific
Dog, set `"enabled": false` in its daemon.json section — the auto-populate
logic preserves explicitly configured entries.

### Communication Hygiene (Reducing Commit Volume)

Every `gt mail send` creates a bead + Dolt commit. Every `gt nudge`
creates nothing. The rule:

**Default to `gt nudge`. Only use `gt mail send` when the message MUST
survive the recipient's session death.**

| Role | Mail budget | Nudge for everything else |
|------|-------------|--------------------------|
| Polecat | 0-1 per session (HELP only) | Status, questions, updates |
| Witness | Protocol messages only | Health checks, polecat pokes |
| Refinery | Protocol messages only | Status to Witness |
| Deacon | Escalations only | Timer callbacks, health pokes |
| Dogs | Zero (never mail) | DOG_DONE via nudge to Deacon |

## Standalone Beads Note

The `bd` CLI retains an embedded Dolt option for standalone use (outside
Gas Town). Server-only mode applies to Gas Town exclusively — standalone
users may not have a Dolt server running.

The Dolt team is working on improving embedded mode for single-process
use cases like standalone Beads. This would give solo `bd` users a
zero-config experience (no server to manage) while retaining Dolt's
versioning capabilities.

## Remote Push (Git Protocol)

Gas Town pushes Dolt databases to GitHub remotes via `gt dolt sync`. These
use git SSH protocol (`git+ssh://git@github.com/...`), not DoltHub's native
protocol.

### Git Remote Cache

Dolt maintains a cache at `~/gt/.dolt-data/<db>/.dolt/git-remote-cache/` that
stores git objects built from Dolt's internal format. Per the Dolt team
(Dustin Brown, 2026-02-26):

- **The cache is necessary** — Dolt uses it to build git objects for push/pull
- **Accumulates garbage** (orphaned refs) and is not cleaned up automatically
- **Safe to delete** between pushes, but causes a full rebuild on next push
  (beads: ~20 min rebuild, gastown: even longer)
- **Orphaned refs** can be pruned without deleting the whole cache — better balance
- **Grows over time** as the database grows — inherent to git-protocol remotes

**Guidance**: Do NOT routinely delete the cache. Prefer pruning orphaned refs.
Full deletion should only be done when disk pressure is critical and a long
rebuild is acceptable.

### Sync Procedure

`gt dolt sync` parks all rigs (stops witnesses/refineries), stops the Dolt
server, runs `dolt push` for each database with a configured remote, then
restarts the server and unparks rigs. The parking prevents witnesses from
detecting the server outage and restarting it mid-push.

### Force Push

After data recovery (e.g., Clown Show #13), local and remote histories
diverge. Use `gt dolt sync --force` for the first push to overwrite the
remote with local state. Subsequent pushes should work without `--force`.

### Known Limitations

- **Slow**: Git-protocol remotes are orders of magnitude slower than DoltHub
  native remotes. A 71MB database takes ~90s; larger ones take 20+ minutes.
- **Cache growth**: No automatic garbage collection. Orphan pruning TBD.
- **Server downtime**: Push requires exclusive access to the data directory,
  so the server must be stopped during push. This creates a maintenance window.

### DoltHub Remotes (Planned)

DoltHub's native protocol (`https://doltremoteapi.dolthub.com/...`) avoids
the git-remote-cache entirely and is much faster. DoltHub-based federation
is planned as part of the Wasteland commons — this would replace
git-protocol remotes for the design and ledger planes. Migration would
require DoltHub accounts and reconfiguring remotes with
`dolt remote set-url`. Not currently in active development.

## File Layout

```
~/gt/                            Town root
├── .dolt-data/                  Centralized Dolt data directory
│   ├── hq/                      Town beads (hq-*)
│   ├── gastown/                 Gastown rig (gt-*)
│   ├── beads/                   Beads rig (bd-*)
│   ├── wyvern/                  Wyvern rig (wy-*)
│   └── sky/                     Sky rig (sky-*)
├── daemon/
│   ├── dolt.pid                 Server PID (daemon-managed)
│   ├── dolt.log                 Server log
│   └── dolt-state.json          Server state
└── mayor/
    └── daemon.json              Daemon config (dolt_server section)
```
