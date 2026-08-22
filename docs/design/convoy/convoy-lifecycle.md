# Convoy Lifecycle Design

> Making convoys actively converge on completion.

## Flow

```mermaid
flowchart TB
    %% ---- Creation ----
    create_manual(["gt convoy create"])
    create_sling(["gt sling<br/>(auto-convoy)"])
    create_formula(["gt formula run<br/>(convoy type)"])

    create_manual --> convoy["Convoy bead created<br/>(hq-cv-*)"]
    create_sling --> convoy
    create_formula --> convoy

    convoy --> track["Track issues<br/>via dep add --type=tracks"]

    %% ---- Dispatch ----
    track --> dispatch["gt sling dispatches<br/>work to polecats"]
    dispatch --> working["Polecats execute<br/>tracked issues"]

    %% ---- Completion detection ----
    working --> issue_close["Issue closes<br/>(owning lifecycle)"]

    issue_close --> check{"All tracked<br/>issues closed?"}

    check -->|No| feed["Feed next ready issue<br/>to available polecat"]
    feed --> working

    check -->|Yes| close["Close convoy +<br/>send notifications"]
    close --> done(["Convoy landed"])

    %% ---- Daemon observer (event-driven + stranded scan) ----
    issue_close -.-> obs_daemon["Daemon ConvoyManager<br/>Event poll (5s) + CheckConvoysForIssue<br/>Stranded scan (30s) as safety net"]

    obs_daemon -.-> check

    %% ---- Manual overrides ----
    force_close(["gt convoy close --force"]) -.-> close
    land(["gt convoy land<br/>(owned convoys)"]) -.-> close

    classDef successNode fill:#27ae60,color:#fff,stroke:none
    classDef extNode fill:#34495e,color:#ecf0f1,stroke:none

    class done successNode
    class obs_daemon extNode
```

Three creation paths feed into the same lifecycle. Completion is event-driven
via the daemon's `ConvoyManager`, which runs two goroutines:

- **Event poll** (every 5s): Polls all rig beads stores + hq via
  `GetAllEventsSince`, detects close events, and calls
  `convoy.CheckConvoysForIssue` — which both checks completion *and* feeds
  the next ready issue to a polecat.
- **Stranded scan** (every 30s): Runs `gt convoy stranded --json` to catch
  convoys missed by the event-driven path (e.g. after crash/restart). Feeds
  ready work or auto-closes empty convoys.

Manual overrides (`close --force`, `land`) bypass the check entirely.

> **History**: Witness and Refinery observers were originally planned as
> redundant observers but were removed (spec S-04, S-05). The daemon's
> multi-rig event poll + stranded scan provide sufficient coverage.

---

## Auto-convoy creation: what `gt sling` actually does

`gt sling` auto-creates a convoy for every bead it dispatches, unless
`--no-convoy` is passed. The behavior differs significantly between
single-bead and multi-bead (batch) sling.

### Single-bead sling

```
gt sling sh-task-1 gastown
```

1. Checks if `sh-task-1` is already tracked by an open convoy.
2. If not tracked: creates one auto-convoy `"Work: <issue-title>"` tracking
   that single bead.
3. Spawns one polecat, hooks the bead, starts working.

Result: 1 bead, 1 convoy, 1 polecat.

### Batch sling (3+ args, rig auto-resolved)

```
gt sling gt-task-1 gt-task-2 gt-task-3
```

The rig is auto-resolved from the beads' prefixes via `routes.jsonl`
(`resolveRigFromBeadIDs` in `sling_batch.go`). All beads must resolve
to the same rig. An explicit rig arg still works but prints a
deprecation warning:

```
gt sling gt-task-1 gt-task-2 gt-task-3 gastown
# Deprecation: gt sling now auto-resolves the rig from bead prefixes.
#              You no longer need to explicitly specify <gastown>.
```

**Batch sling creates one convoy tracking all beads.** Before spawning
any polecats, `runBatchSling` (`sling_batch.go`) calls
`createBatchConvoy` (`sling_convoy.go`) which creates a single convoy
with title `"Batch: N beads to <rig>"` and adds `tracks` deps for all
beads.

Result: 3 beads, **1 convoy**, 3 polecats -- all dispatched in parallel
with 2-second delays between spawns.

The convoy ID and merge strategy are stored on each bead via
`beadFieldUpdates`, so `gt done` can find the convoy via the fast path.

There is no upper limit on the number of beads. `gt sling <10 beads>`
spawns 10 polecats sharing 1 convoy. The only throttle is
`--max-concurrent` (default 0 = unlimited).

### Rig resolution errors

When the rig is explicit, the cross-rig guard checks each bead's prefix
against the target rig. On mismatch, it errors with batch-specific
suggested actions (remove the bead, sling it separately, or `--force`).

When auto-resolving, `resolveRigFromBeadIDs` errors if:
- A bead has no valid prefix
- A prefix is not mapped in `routes.jsonl` (including town-level `path="."`)
- Beads resolve to different rigs (lists each bead's rig, suggests slinging separately)

### Already-tracked bead conflict

If any bead in the batch is already tracked by another convoy, batch
sling **errors** before spawning any polecats. It prints:

- Which bead conflicts and which convoy it belongs to
- All beads in the existing convoy with their statuses
- The conflicting bead highlighted
- 4 recommended actions (remove from batch, move bead, close old convoy, add to existing)

### Initial dispatch vs daemon feeding

- **Initial dispatch is parallel.** All beads get polecats sequentially
  with 2-second delays between spawns, but they all dispatch in the same
  batch sling call regardless of deps. Even if `gt-task-2` has a `blocks`
  dep on `gt-task-1`, both get slung. The `isIssueBlocked` check only
  applies to daemon-driven convoy feeding (after a close event), not to
  the initial batch sling dispatch.
- **Subsequent feeding respects deps.** When a task closes, the daemon's
  event-driven feeder checks `IsSlingableType` and `isIssueBlocked`
  before dispatching the next ready issue from the shared convoy.

### No-convoy mode

Batch sling with `--no-convoy` skips convoy creation entirely:

```
gt sling gt-task-1 gt-task-2 gt-task-3 gastown --no-convoy
```

---

## Historical Problem Statement

Before `ConvoyManager` shipped, convoys were passive trackers. They grouped work
but did not drive it, leaving a structural gap in the completion loop:

```
Create → Assign → Execute → Issues close → ??? → Convoy closes
```

The `???` was “Deacon patrol runs `gt convoy check`,” a poll-based single point
of failure. When Deacon was down, convoys did not close. This section records
the problem that motivated the implemented daemon manager; it is not the
current runtime model.

## Historical State Before `ConvoyManager`

### What Worked
- Convoy creation and issue tracking
- `gt convoy status` shows progress
- `gt convoy stranded` finds unassigned work
- `gt convoy check` auto-closes completed convoys

### What Was Missing
1. **Poll-based completion**: Only Deacon ran `gt convoy check`
2. **No event-driven trigger**: Issue close did not propagate to a convoy
3. **Inconsistent manual-close docs**: Some docs described the implemented
   `gt convoy close --force` command as missing
4. **No recovery scan**: Missed close events had no daemon-owned safety net
5. **Weak notification ownership**: The convoy requester was not always clear

## Current Runtime: Active Convoy Convergence

### Principle: Event-Driven, Centrally Managed

Convoy completion is:
1. **Event-driven**: Triggered by issue close, not polling
2. **Centrally managed**: Single owner (daemon) avoids scattered side-effect hooks
3. **Manually overridable**: Humans can force-close

### Event-Driven Completion

When an issue closes, check if it's tracked by a convoy:

```mermaid
flowchart TD
    close["Issue closes"] --> tracked{"Tracked by convoy?"}
    tracked -->|No| done1(["Done"])
    tracked -->|Yes| check["CheckConvoysForIssue"]
    check --> all{"All tracked<br/>issues closed?"}
    all -->|No| done2(["Done"])
    all -->|Yes| notify["Close convoy +<br/>send notifications"]
```

**Implementation**: The daemon's `ConvoyManager` event poll detects close events
via SDK `GetAllEventsSince` across all rig stores + hq. This catches all closes
regardless of source (CLI, witness, refinery, manual).

### Observer: Daemon ConvoyManager

The daemon's `ConvoyManager` is the sole convoy observer, running two
independent goroutines:

| Loop | Trigger | What it does |
|------|---------|--------------|
| **Event poll** | `GetAllEventsSince` every 5s (all rig stores + hq) | Detects close events, calls `CheckConvoysForIssue` |
| **Stranded scan** | `gt convoy stranded --json` every 30s | Feeds first ready issue via `gt sling`, auto-closes empty convoys |

Both loops are context-cancellable. The shared `CheckConvoysForIssue` function
is idempotent — closing an already-closed convoy is a no-op.

> **History**: The original design called for three redundant observers (Daemon,
> Witness, Refinery) per the "Redundant Monitoring Is Resilience" principle.
> Witness observers were removed (spec S-04) because convoy tracking is
> orthogonal to polecat lifecycle management. Refinery observers were removed
> (spec S-05) after S-17 found they were silently broken (wrong root path) with
> no visible impact, confirming single-observer coverage is sufficient.

### Issue-to-Rig Resolution

Convoys are rig-agnostic. A convoy like `hq-cv-6vjz2` lives in the hq store and
tracks issue IDs like `sh-pb6sa` — but it doesn't store which rig that issue
belongs to. The rig association is resolved at dispatch time via two lookups:

1. **Extract prefix**: `sh-pb6sa` → `sh-` (string parsing)
2. **Resolve rig**: look up `sh-` in `~/gt/.beads/routes.jsonl` → finds
   `{"prefix":"sh-","path":"gastown/.beads"}` → rig name is `gastown`

This happens in `feedFirstReady` (stranded scan path) and `feedNextReadyIssue`
(event poll path) just before calling `gt sling`. Issues with prefixes that
don't appear in `routes.jsonl` (or that map to `path="."` like `hq-*`) are
skipped — see `isSlingableBead()`.

Both paths check `isRigParked` after resolving the rig name. Issues targeting
parked rigs are logged and skipped rather than dispatched.

### Current Manual Close Command

`gt convoy close` is implemented, including `--force` for abandoned convoys.

```bash
# Close a completed convoy
gt convoy close hq-cv-abc

# Force-close an abandoned convoy
gt convoy close hq-cv-xyz --force --reason="work done differently"

# Close with explicit notification
gt convoy close hq-cv-abc --notify mayor/
```

Use cases:
- Abandoned convoys no longer relevant
- Work completed outside tracked path
- Force-closing stuck convoys

### Convoy Owner/Requester

The implemented owner field records who requested the convoy for targeted
notifications:

```bash
gt convoy create "Feature X" gt-abc --owner mayor/ --notify overseer
```

| Field | Purpose |
|-------|---------|
| `owner` | Who requested (gets completion notification) |
| `notify` | Additional subscribers |

If `owner` not specified, defaults to creator (from `created_by`).

### Convoy States

```
OPEN ──(all issues close)──► CLOSED
  │                             │
  │                             ▼
  │                    (add issues)
  │                             │
  └─────────────────────────────┘
         (auto-reopens)
```

Adding issues to closed convoy reopens automatically.

**Proposed abandonment state (not implemented):**

```
OPEN ──► CLOSED (completed)
  │
  └────► ABANDONED (force-closed without completion)
```

### Timeout/SLA (Future)

Optional `due_at` field for convoy deadline:

```bash
gt convoy create "Sprint work" gt-abc --due="2026-01-15"
```

Overdue convoys surface in `gt convoy stranded --overdue`.

## Current Commands

### Current: `gt convoy close`

```bash
gt convoy close <convoy-id> [--force] [--reason=<reason>] [--notify=<agent>]
```

- Verifies tracked issues are complete by default
- `--force` closes even when tracked issues remain open
- Sets `close_reason` field
- Sends notification to owner and subscribers
- Idempotent - closing closed convoy is no-op

### Current: `gt convoy check`

```bash
# Check all convoys (current behavior)
gt convoy check

# Check specific convoy
gt convoy check <convoy-id>

# Dry-run mode
gt convoy check --dry-run
```

### Future: `gt convoy reopen`

```bash
gt convoy reopen <convoy-id>
```

Explicit reopen for clarity (currently implicit via add).

## Implementation Status

Core convoy manager and owner notifications are implemented and tested (see
[spec.md](spec.md), stories S-01 through S-18). Future items are explicitly
labelled above: a distinct abandonment state, an explicit `gt convoy reopen`
command, and the optional timeout/SLA contract.

## Key Files

| Component | File |
|-----------|------|
| Convoy command | `internal/cmd/convoy.go` |
| Auto-convoy (sling) | `internal/cmd/sling_convoy.go` |
| Convoy operations | `internal/convoy/operations.go` (`CheckConvoysForIssue`, `feedNextReadyIssue`) |
| Daemon manager | `internal/daemon/convoy_manager.go` |
| Formula convoy | `internal/cmd/formula.go` (`executeConvoyFormula`) |

## Related

- [convoy.md](../../concepts/convoy.md) - Convoy concept and usage
- [watchdog-chain.md](../watchdog-chain.md) - Daemon/boot/deacon watchdog chain
- [mail-protocol.md](../mail-protocol.md) - Notification delivery
