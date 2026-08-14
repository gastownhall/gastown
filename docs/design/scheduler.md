# Scheduler Architecture

> Config-driven capacity-controlled polecat dispatch.

## Quick Start

Enable deferred dispatch and schedule some work:

```bash
# 1. Enable deferred dispatch (config-driven, no per-command flag)
gt config set scheduler.max_polecats 5

# 2. Schedule work via gt sling (auto-defers when max_polecats > 0)
gt sling gt-abc gastown              # Single task bead
gt sling gt-abc gt-def gt-ghi gastown  # Batch task beads
gt sling hq-cv-abc                   # Convoy (schedules all tracked issues)
gt sling gt-epic-123                 # Epic (schedules all children)

# 3. Check what's scheduled
gt scheduler status
gt scheduler list

# 4. Dispatch manually (or let the daemon do it)
gt scheduler run
gt scheduler run --dry-run    # Preview first
```

### Dispatch Modes

The `scheduler.max_polecats` config value controls dispatch behavior:

| Value | Mode | Behavior |
|-------|------|----------|
| `-1` (default) | Direct dispatch | `gt sling` dispatches immediately, near-zero overhead |
| `0` | Direct dispatch | Same as `-1` — `gt sling` dispatches immediately |
| `N > 0` | Deferred dispatch | `gt sling` creates sling context bead, daemon dispatches |

No per-invocation flag needed. The same `gt sling` command adapts automatically.

### Common CLI

| Command | Description |
|---------|-------------|
| `gt sling <bead> <rig>` | Sling bead (direct or deferred, per config) |
| `gt sling <bead>... <rig>` | Batch sling/schedule multiple beads |
| `gt sling <convoy-id>` | Sling/schedule all tracked issues in convoy |
| `gt sling <epic-id>` | Sling/schedule all children of epic |
| `gt scheduler status` | Show scheduler state and capacity |
| `gt scheduler list` | List all scheduled beads by rig |
| `gt scheduler run` | Trigger dispatch manually |
| `gt scheduler pause` | Pause all dispatch town-wide |
| `gt scheduler resume` | Resume dispatch |
| `gt scheduler clear` | Remove beads from scheduler |

### Minimal Example

```bash
gt config set scheduler.max_polecats 5
gt sling gt-abc gastown              # Defers: creates sling context bead
gt scheduler status                  # "Queued: 1 total, 1 ready"
gt scheduler run                     # Dispatches -> spawns polecat -> closes context
```

---

## Overview

The scheduler solves **back-pressure** and **capacity control** for batched polecat dispatch.

Without the scheduler, slinging N beads spawns N polecats simultaneously, exhausting API rate limits, memory, and CPU. The scheduler introduces a governor: beads enter a waiting state and the daemon dispatches them incrementally, respecting a configurable concurrency cap.

The scheduler integrates into the daemon heartbeat as **step 14** — after all agent health checks, lifecycle processing, and branch pruning. This ensures the system is healthy before spawning new work.

```
Daemon heartbeat (every 3 min)
    |
    +- Steps 0-13: Health checks, agent recovery, cleanup
    |
    +- Step 14: gt scheduler run (capacity-controlled dispatch)
         |
         +- flock (exclusive)
         +- Check paused state
         +- Load config (max_polecats, batch_size)
         +- Count active polecats (tmux)
         +- Query sling contexts (bd list --label=gt:sling-context)
         +- Join with bd ready to determine unblocked beads
         +- DispatchCycle.Run() — plan + execute + report
         |    +- PlanDispatch(availableCapacity, batchSize, ready)
         |    +- For each planned bead: Execute → OnSuccess/OnFailure
         +- Wake rig agents (witness, refinery)
         +- Save dispatch state
```

---

## Sling Context Beads

Scheduling state is stored on **separate ephemeral beads** called sling contexts. The work bead is never modified by the scheduler.

Each sling context bead:
- Is created via `bd create --ephemeral` with label `gt:sling-context`
- Has a `tracks` dependency pointing to the work bead
- Stores all scheduling parameters as JSON in its description
- Is closed when dispatch succeeds, the bead is cleared, or the circuit breaker trips

### Why Separate Beads?

The previous approach stored scheduling metadata on the work bead's description (delimited block) and used labels (`gt:queued`) as state signals. This required:
- Two-step writes with rollback (metadata then label)
- Description sanitization to avoid delimiter collision
- Three-step dispatch cleanup (strip metadata + swap labels + retry)
- Custom key-value format/parse/strip functions (~250 lines)

Sling context beads eliminate all of this:
- **Single atomic create** — `bd create --ephemeral` is one operation
- **JSON format** — `json.Marshal`/`json.Unmarshal` replaces custom parsers
- **Work bead pristine** — no description mutation, no label manipulation
- **Clean lifecycle** — open context = scheduled, closed context = done

### Context Fields (JSON)

| Field | Type | Description |
|-------|------|-------------|
| `version` | int | Schema version (currently 1) |
| `work_bead_id` | string | The actual work bead being scheduled |
| `target_rig` | string | Destination rig name |
| `formula` | string | Formula to apply at dispatch (e.g., `mol-polecat-work`) |
| `args` | string | Natural language instructions for executor |
| `vars` | string | Newline-separated formula variables (`key=value`) |
| `enqueued_at` | RFC3339 | Timestamp of schedule |
| `merge` | string | Merge strategy: `direct`, `mr`, `local` |
| `convoy` | string | Convoy bead ID (set after auto-convoy creation) |
| `base_branch` | string | Override base branch for polecat worktree |
| `resume_branch` | string | Resume an existing branch instead of creating a fresh one |
| `no_merge` | bool | Skip merge queue on completion |
| `account` | string | Claude Code account handle |
| `agent` | string | Agent/runtime override |
| `hook_raw_bead` | bool | Hook without default formula |
| `owned` | bool | Caller-managed convoy lifecycle |
| `mode` | string | Execution mode: `ralph` (fresh context per step) |
| `dispatch_failures` | int | Consecutive failure count (circuit breaker) |
| `last_failure` | string | Most recent dispatch error message |

---

## Bead State Machine

A sling context transitions through these states:

```
                                  +------------------+
                                  |                  |
                                  v                  |
          +----------+    dispatch ok     +--------+ |
 schedule |  CONTEXT  | ----------------> | CLOSED | |
--------> |   OPEN    |                   | (done) | |
          +----------+                    +--------+ |
                |                                    |
                +-- 3 failures --> CLOSED (circuit-broken)
                |
                +-- gt scheduler clear --> CLOSED (cleared)
```

| State | Representation | Trigger |
|-------|---------------|---------|
| **SCHEDULED** | Open sling context bead | `scheduleBead()` |
| **DISPATCHED** | Closed sling context (reason: "dispatched") | `dispatchSingleBead()` success |
| **CIRCUIT-BROKEN** | Closed sling context (reason: "circuit-broken") | `dispatch_failures >= 3` |
| **CLEARED** | Closed sling context (reason: "cleared") | `gt scheduler clear` |

Key invariant: the work bead is **never modified** by the scheduler. All state lives on the sling context bead.

---

## Entry Points

### CLI Entry Points

`gt sling` auto-detects the dispatch mode from config and the ID type:

| Command | Direct Mode (max_polecats=-1) | Deferred Mode (max_polecats>0) |
|---------|-------------------------------|-------------------------------|
| `gt sling <bead> <rig>` | Immediate dispatch | Schedule for later dispatch |
| `gt sling <bead>... <rig>` | Batch immediate dispatch | Batch schedule |
| `gt sling <epic-id>` | `runEpicSlingByID()` — dispatch all children | `runEpicScheduleByID()` — schedule all children |
| `gt sling <convoy-id>` | `runConvoySlingByID()` — dispatch all tracked | `runConvoyScheduleByID()` — schedule all tracked |

**Detection chain** in `runSling`:
1. `shouldDeferDispatch()` — check `scheduler.max_polecats` config
2. Batch (3+ args, last is rig) — `runBatchSchedule()` or `runBatchSling()`
3. `--on` flag set — formula-on-bead mode
4. 2 args + last is rig — `scheduleBead()` or inline dispatch
5. 1 arg, auto-detect type: epic/convoy/task

All schedule paths go through `scheduleBead()` in `internal/cmd/sling_schedule.go`.
All dispatch goes through `dispatchScheduledWork()` in `internal/cmd/capacity_dispatch.go`.

### Daemon Entry Point

The daemon calls `gt scheduler run` as a subprocess on each heartbeat (step 14):

```go
// internal/daemon/daemon.go
func (d *Daemon) dispatchScheduledWork() {
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
    defer cancel()
    cmd := exec.CommandContext(ctx, "gt", "scheduler", "run")
    cmd.Env = append(os.Environ(), "GT_DAEMON=1", "BD_DOLT_AUTO_COMMIT=off")
    // ...
}
```

| Property | Value |
|----------|-------|
| Timeout | 5 minutes |
| Environment | `GT_DAEMON=1` (identifies daemon dispatch) |
| Gating | `scheduler.max_polecats > 0` (deferred mode) |

---

## Schedule Path

`scheduleBead()` performs these steps in order:

1. **Validate** bead exists, rig exists
2. **Cross-rig guard** — reject if bead prefix doesn't match target rig (unless `--force`)
3. **Idempotency** — skip if an open sling context already exists for this work bead
4. **Status guard** — reject if bead is hooked/in_progress (unless `--force`)
5. **Validate formula** — verify formula exists (lightweight, no side effects)
6. **Cook formula** — `bd cook` to catch bad protos before daemon dispatch
7. **Build context fields** — `SlingContextFields` struct with all sling params
8. **Create sling context** — `bd create --ephemeral` + `bd dep add --type=tracks` (atomic)
9. **Auto-convoy** — create convoy if not already tracked, store convoy ID in context fields
10. **Log event** — feed event for dashboard visibility

The create is a **single atomic operation** — no two-step write, no rollback needed.

---

## Dispatch Engine

### DispatchCycle

The dispatch loop is a generic orchestrator with injected callbacks:

```go
type DispatchCycle struct {
    AvailableCapacity func() (int, error)        // Free dispatch slots (0=unlimited)
    QueryPending      func() ([]PendingBead, error) // Work items eligible for dispatch
    Execute           func(PendingBead) error     // Dispatch a single item
    OnSuccess         func(PendingBead) error     // Post-dispatch cleanup
    OnFailure         func(PendingBead, error)    // Failure handling
    BatchSize         int
    SpawnDelay        time.Duration
}
```

`Run()` internally calls `PlanDispatch(availableCapacity, batchSize, ready)` to determine what to dispatch, then executes each planned item with callbacks.

### Dispatch Flow

```
DispatchCycle.Run()
    |
    +- AvailableCapacity() → capacity = maxPolecats - activePolecats
    |
    +- QueryPending() → getReadySlingContexts():
    |    +- bd list --label=gt:sling-context --status=open (all rig DBs)
    |    +- Parse SlingContextFields from each context bead description
    |    +- bd ready --json --limit=0 (all rig DBs) → readyWorkIDs set
    |    +- Filter: context beads whose WorkBeadID is in readyWorkIDs
    |    +- Skip circuit-broken (dispatch_failures >= threshold)
    |
    +- PlanDispatch(capacity, batchSize, ready)
    |    +- Returns DispatchPlan{ToDispatch, Skipped, Reason}
    |
    +- For each planned bead:
         +- Execute: sling.FromContext(fields) → Lifecycle.Execute(intent)
         +- OnSuccess: CloseSlingContext(contextID, "dispatched")
         +- OnFailure: increment dispatch_failures, update context, maybe close
         +- sleep(SpawnDelay)
```

### dispatchSingleBead

The scheduler adapter reconstructs `sling.Intent` from context fields and invokes
the deep Slinging lifecycle. It does not re-project convoy, merge, ownership, or
formula policy. Recorded Convoy identity is reused; a second Convoy is not created.

Post-dispatch cleanup is handled by callbacks:
- **OnSuccess**: `CloseSlingContext(b.ID, "dispatched")`
- **OnFailure**: increment `dispatch_failures`, update context bead, close if circuit-broken

---

## Capacity Management

### Configuration

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `scheduler.max_polecats` | *int | `-1` | Max concurrent polecats (-1=direct, 0=disabled, N=deferred) |
| `scheduler.batch_size` | *int | `1` | Beads dispatched per heartbeat tick |
| `scheduler.spawn_delay` | string | `"0s"` | Delay between spawns (Dolt lock contention) |

Set via `gt config set`:

```bash
gt config set scheduler.max_polecats 5    # Enable deferred dispatch
gt config set scheduler.max_polecats -1   # Direct dispatch (default)
gt config set scheduler.batch_size 2
gt config set scheduler.spawn_delay 3s
```

### Dispatch Count Formula

```
toDispatch = min(capacity, batchSize, readyCount)

where:
  capacity   = maxPolecats - activePolecats (positive = that many slots, 0 or negative = no capacity)
  batchSize  = scheduler.batch_size (default 1)
  readyCount = sling contexts whose work bead appears in bd ready
```

### Active Polecat Counting

Active polecats are counted by scanning tmux sessions and matching role via `session.ParseSessionName()`. This counts **all** polecats (both scheduler-dispatched and directly-slung) because API rate limits, memory, and CPU are shared resources.

---

## Circuit Breaker

The circuit breaker prevents permanently-failing beads from causing infinite retry loops.

| Property | Value |
|----------|-------|
| Threshold | `maxDispatchFailures = 3` |
| Counter | `dispatch_failures` field in sling context JSON |
| Break action | Close sling context (reason: "circuit-broken") |
| Reset | No automatic reset (manual intervention required) |

### Flow

```
Dispatch attempt fails
    |
    +- Increment dispatch_failures in context bead
    +- Store last_failure error message
    |
    +- dispatch_failures >= 3?
         +- Yes -> CloseSlingContext(contextID, "circuit-broken")
         |         (context bead closed, work bead untouched)
         +- No  -> bead stays scheduled, retried next cycle
```

---

## Scheduler Control

### Pause / Resume

Pausing stops all dispatch town-wide. The state is stored in `.runtime/scheduler-state.json`.

```bash
gt scheduler pause    # Sets paused=true, records actor and timestamp
gt scheduler resume   # Clears paused state
```

Write is atomic (temp file + rename) to prevent corruption from concurrent writers.

### Clear

Closes sling context beads, removing beads from the scheduler:

```bash
gt scheduler clear              # Close ALL sling contexts
gt scheduler clear --bead gt-abc  # Close context for specific bead
```

### Status / List

```bash
gt scheduler status         # Summary: paused, queued count, active polecats
gt scheduler status --json  # JSON output

gt scheduler list           # Beads grouped by target rig, with blocked indicator
gt scheduler list --json    # JSON output
```

`list` reconciles sling contexts (all scheduled) with `bd ready` (unblocked work beads) to mark blocked beads.

---

## Scheduler and Convoy Integration

Convoys and the scheduler are complementary but distinct mechanisms. Convoys track completion of related beads; the scheduler controls dispatch capacity. Two paths exist for dispatching convoy work:

### Dispatch Paths

| Path | Trigger | Capacity Control | Use Case |
|------|---------|-----------------|----------|
| **Direct dispatch** | `gt sling <convoy-id>` (max_polecats=-1) | None (fires immediately) | Default mode — all issues dispatch at once |
| **Deferred dispatch** | `gt sling <convoy-id>` (max_polecats>0) | Yes (daemon heartbeat, max_polecats, batch_size) | Capacity-controlled — batched with back-pressure |

**Direct dispatch** (max_polecats=-1): `gt sling <convoy-id>` calls `runConvoySlingByID()` which dispatches all open tracked issues immediately through the deep Slinging lifecycle. Each issue's rig is auto-resolved from its bead ID prefix. No capacity control — all issues dispatch at once.

**Deferred dispatch** (max_polecats>0): `gt sling <convoy-id>` calls `runConvoyScheduleByID()` which schedules all open tracked issues (creating sling context beads). The daemon dispatches incrementally via `gt scheduler run`, respecting `max_polecats` and `batch_size`. Use this for large batches where simultaneous dispatch would exhaust resources.

### When to Use Which

- **Small convoys (< 5 issues)**: Direct dispatch (default, max_polecats=-1)
- **Large batches (5+ issues)**: Set `scheduler.max_polecats` for capacity-controlled dispatch
- **Epics**: Same logic — `gt sling <epic-id>` auto-resolves mode from config

### Rig Resolution

`gt sling <convoy-id>` and `gt sling <epic-id>` auto-resolve the target rig per-bead from its ID prefix using `beads.ExtractPrefix()` + `beads.GetRigNameForPrefix()`. Town-root beads (`hq-*`) are skipped with a warning since they are coordination artifacts, not dispatchable work.

---

## Safety Properties

| Property | Mechanism |
|----------|-----------|
| **Schedule idempotency** | Skip if open sling context already exists for work bead |
| **Work bead pristine** | Scheduler never modifies work bead description or labels |
| **Cross-rig guard** | Reject if bead prefix doesn't match target rig (unless `--force`) |
| **Dispatch serialization** | `flock(scheduler-dispatch.lock)` prevents double-dispatch |
| **Atomic scheduling** | Single `bd create --ephemeral` — no two-step write, no rollback |
| **Formula pre-cooking** | `bd cook` at schedule time catches bad protos before daemon dispatch loop |
| **Fresh state on save** | Dispatch re-reads state before saving to avoid clobbering concurrent pause |

---

## Deep Slinging module

Direct `gt sling <bead> <rig>` and deferred scheduler dispatch are **two adapters**
over one lifecycle (`internal/sling`).

| Adapter | Converts | Invokes |
|---------|----------|---------|
| Direct command (`runRigBeadSling`) | CLI flags → `sling.Intent` | `Lifecycle.Execute` |
| Scheduler (`dispatchSingleBead`) | Sling context JSON → `sling.Intent` | `Lifecycle.Execute` |

The lifecycle owns validation, Convoy create/reuse, Polecat spawn, Formula and
Molecule work, Hook placement, Bead attachment fields, and compensation on fatal
failure after durable artifacts exist. Adapters own argument conversion,
invocation, and presentation only.

---

## Code Layout

| Path | Purpose |
|------|---------|
| `internal/sling/` | Deep Slinging module: `Intent`, `Lifecycle`, `FromContext` |
| `internal/cmd/sling_lifecycle.go` | Production `Lifecycle` implementation and command adapter |
| `internal/scheduler/capacity/config.go` | `SchedulerConfig` type, defaults, `IsDeferred()` |
| `internal/scheduler/capacity/pipeline.go` | `PendingBead`, `SlingContextFields`, `PlanDispatch()` |
| `internal/scheduler/capacity/dispatch.go` | `DispatchCycle` type — generic dispatch orchestrator |
| `internal/scheduler/capacity/state.go` | `SchedulerState` persistence |
| `internal/beads/beads_sling_context.go` | Sling context CRUD (create, find, list, close, update) |
| `internal/cmd/sling.go` | CLI entry, config-driven routing, non-rig targets |
| `internal/cmd/sling_schedule.go` | `scheduleBead()`, `shouldDeferDispatch()`, `isScheduled()` |
| `internal/cmd/scheduler.go` | `gt scheduler` command tree |
| `internal/cmd/scheduler_epic.go` | Epic schedule/sling handlers |
| `internal/cmd/scheduler_convoy.go` | Convoy schedule/sling handlers |
| `internal/cmd/capacity_dispatch.go` | `dispatchScheduledWork()`, deferred adapter |
| `internal/daemon/daemon.go` | Heartbeat integration (`gt scheduler run`) |

---

## See Also

- [Watchdog Chain](watchdog-chain.md) — Daemon heartbeat, where scheduler dispatch runs as step 14
- [Convoys](../concepts/convoy.md) — Convoy tracking, auto-convoy on schedule
- [Property Layers](property-layers.md) — Labels-as-state pattern used by scheduler labels (see Operational State Events section)
