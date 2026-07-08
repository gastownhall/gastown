# Polecat Lifecycle and Patrol Coordination

> **Bead:** gt-t6muy
> **Date:** 2026-02-20
> **Author:** capable (gastown polecat)
> **Status:** Implemented — core lifecycle shipped, branch cleanup shipped, mayor notify pending
> **Updated:** 2026-03-07 (gt-o8g8 implementation audit by bear)
> **Related:** gt-dtw9u (Witness monitoring), gt-qpwv4 (Completion detection),
> gt-6qyt1 (Refinery queue), gt-budeb (Auto-nuke), gt-5j3ia (Swarm aggregation),
> gt-1dbcp (Polecat auto-start), w-gt-004 (Wasteland lifecycle item)

---

## 1. Overview

This document formalizes how Deacon, Witness, Refinery, and Polecats coordinate
to move work through the Gas Town propulsion system. It captures the
session-per-step model, defines the two cleanup stages, designs the per-rig
lifecycle channel, and resolves open design questions about step granularity,
recycling, and spawning.

**Core insight:** Polecats do NOT complete complex molecules end-to-end. Instead,
each molecule step gets one polecat session. The sandbox (branch, worktree)
persists across sessions. Sessions are the pistons; sandboxes are the cylinders.

---

## 2. Session-Per-Step Model

### 2.1 The Relay Race

See [concepts/polecat-lifecycle.md](../concepts/polecat-lifecycle.md) for the relay race model.

### 2.2 Session Cycling vs Step Cycling

These are distinct concepts:

| Concept | Trigger | What Changes | What Persists |
|---------|---------|-------------|---------------|
| **Session cycle** | Handoff, compaction, crash | Claude context window | Branch, worktree, molecule state |
| **Step cycle** | Step bead closed | Current step focus | Branch, worktree, remaining steps |

A single step may span multiple session cycles (if the step is complex or
compaction occurs). Multiple steps may fit in a single session (if steps are
small and context permits). The session-per-step model is a design target, not a
hard constraint.

### 2.3 When Sessions Cycle

| Trigger | Who Initiates | What Happens |
|---------|--------------|-------------|
| Step completion | Polecat | `bd close <step>` then `gt handoff` for next step |
| Context filling | Claude Code | Auto-compaction; PreCompact hook saves state |
| Crash/timeout | Infrastructure | Witness detects, respawns session |
| `gt done` | Polecat | Final step; submit to MQ, go idle (sandbox preserved) |

### 2.4 State Continuity

Between sessions, state is preserved through:

- **Git state:** Commits, staged changes, branch position
- **Beads state:** Molecule progress (which steps are closed)
- **Hook state:** `hook_bead` on agent bead persists across sessions
- **Agent bead:** `agent_state`, `cleanup_status`, `hook_bead` fields

The new session discovers its position via:

```bash
gt prime --hook    # Loads role context, reads hook
bd mol current     # Discovers which step is next
bd show <step-id>  # Reads step instructions
```

No explicit "handoff payload" is needed. The beads state IS the handoff.

---

## 3. Two Cleanup Stages

### 3.1 Step Cleanup (Session Dies, Sandbox Lives)

Triggered when a step completes but more steps remain in the molecule.

| Action | Result |
|--------|--------|
| Close step bead | `bd close <step-id>` |
| Session cycles | `gt handoff` (voluntary) or crash recovery |
| Sandbox persists | Branch, worktree, uncommitted work all survive |
| Molecule persists | Remaining steps still open, hook still set |
| Identity persists | Agent bead unchanged, CV accumulates |

**Who handles it:**
- Polecat initiates via `gt handoff`
- Witness respawns if crash (via `SessionManager.Start`)
- Daemon triggers if session is dead (`LIFECYCLE:Shutdown` → witness)

### 3.2 Molecule Cleanup (Polecat Goes Idle)

Triggered when the molecule's final step completes and work is submitted.

| Action | Result |
|--------|--------|
| Polecat runs `gt done` | Pushes branch, submits MR, sets `cleanup_status=clean` |
| Polecat sets agent state | `agent_state=idle`, `hook_bead` cleared |
| Polecat kills session | Session terminated, sandbox preserved |
| Witness receives `POLECAT_DONE` | Acknowledges idle transition |
| Refinery merges | Squash-merge to main, closes MR and source issue |
| Identity survives | Agent bead still exists; CV chain has new entry; polecat ready for reuse |

```
STEP CLEANUP (intermediate)          MOLECULE CLEANUP (final)
┌────────────────────┐               ┌────────────────────────────┐
│ Step bead: closed  │               │ All step beads: closed     │
│ Session: terminated│               │ Session: terminated        │
│ Sandbox: ALIVE     │               │ Sandbox: PRESERVED (idle)  │
│ Molecule: ACTIVE   │               │ Molecule: SQUASHED         │
│ Hook: SET          │               │ Hook: CLEARED              │
│ Agent bead: working│               │ Agent bead: nuked          │
│ Branch: ALIVE      │               │ Branch: PUSHED (idle)      │
└────────────────────┘               └────────────────────────────┘
```

### 3.3 The Cleanup Pipeline

The cleanup pipeline is a chain of handoffs, not a monolithic operation:

```
Polecat calls gt done
    │
    ├── Sets cleanup_status=clean on agent bead
    ├── Pushes branch to origin
    ├── Creates MR bead (label: gt:merge-request)
    ├── Sends POLECAT_DONE mail to witness
    └── Session exits
         │
         ▼
Witness receives POLECAT_DONE
    │
    ├── Checks cleanup_status (ZFC: trust polecat self-report)
    ├── If clean → sends MERGE_READY to refinery
    ├── If dirty → creates cleanup wisp (cannot auto-nuke)
    └── Nudges refinery session
         │
         ▼
Refinery processes MERGE_READY
    │
    ├── Claims MR (sets assignee)
    ├── Acquires merge slot (serialized push lock)
    ├── Runs quality gates
    ├── Squash-merges to main
    ├── Closes MR bead and source issue
    ├── Sends MERGED mail to witness
    └── Releases merge slot
         │
         ▼
Witness receives MERGED
    │
    ├── Verifies commit is on main (all remotes)
    ├── Checks cleanup_status
    ├── Acknowledges merge (polecat already idle, sandbox preserved)
    └── If dirty → warns (shouldn't happen post-merge)
```

### 3.4 Failure Recovery in the Cleanup Pipeline

Each stage can fail independently. Recovery is handled by the next patrol cycle:

| Failure | Detection | Recovery |
|---------|-----------|---------|
| `gt done` fails mid-execution | Zombie state: session alive, done-intent label | Witness `DetectZombiePolecats()` finds stuck-in-done, recovers |
| `POLECAT_DONE` mail lost | Witness patrol: finds dead session with `hook_bead` | `DetectZombiePolecats()` with agent-dead-in-session |
| Merge conflict | Refinery `doMerge()` detects | Creates conflict resolution task, blocks MR |
| `MERGED` mail lost | Refinery closed the bead; witness patrol finds closed bead with live session | `DetectZombiePolecats()` bead-closed-still-running |
| Nuke fails | Session still running after kill attempt | Next patrol detects zombie, retries nuke |

---

## 4. Per-Rig Polecat Channel

### 4.1 Design Decision: Mail-Based Channel

The per-rig polecat channel is implemented using the existing `gt mail` system.
This was chosen over beads-based queues or state files because:

1. **Consistency:** Mail is already the coordination primitive for all Gas Town agents
2. **Persistence:** Messages survive process crashes and session cycles
3. **Routing:** Mail addresses (`gastown/witness`) already map to rig-level agents
4. **Audit trail:** Mail creates beads entries (observable, discoverable)
5. **No new infrastructure:** No new Dolt tables, no file-based queues

### 4.2 Channel Addresses

Each rig has implicit lifecycle channels via existing mail routing:

| Channel | Address | Purpose | Serviced By |
|---------|---------|---------|-------------|
| Polecat lifecycle | `<rig>/witness` | Recycle, nuke, health requests | Witness patrol |
| Merge queue | `<rig>/refinery` | MERGE_READY, conflict reports | Refinery patrol |
| Rig coordination | `<rig>/witness` | Spawn requests, escalations | Witness |
| Town coordination | `mayor/` | Cross-rig, strategic | Mayor |

### 4.3 Lifecycle Message Protocol

Messages in the polecat lifecycle channel follow the existing witness protocol
(`protocol.go`):

| Subject Pattern | Type | Sender | Action |
|----------------|------|--------|--------|
| `POLECAT_DONE <name>` | Completion | Polecat | Verify clean, forward to refinery |
| `LIFECYCLE:Shutdown <name>` | External shutdown | Daemon | Auto-nuke or cleanup wisp |
| `LIFECYCLE:Cycle <name>` | Session restart | Daemon | Kill and restart session |
| `HELP: <topic>` | Escalation | Polecat | Witness evaluates, relays if needed |
| `MERGED <id>` | Post-merge | Refinery | Nuke polecat sandbox |
| `MERGE_FAILED <id>` | Merge failure | Refinery | Notify polecat, rework needed |
| `RECOVERED_BEAD <id>` | Orphan recovery | Witness | Deacon re-dispatches work |
| `GUPP_VIOLATION: <name>` | Stall detected | Daemon | Witness investigates |
| `ORPHANED_WORK: <name>` | Dead session + work | Daemon | Witness recovers or nukes |

### 4.4 Channel Processing

The witness processes its channel during patrol cycles. Processing is
first-come-first-served within each cycle. The patrol pattern:

```
Witness patrol cycle:
    │
    ├── 1. Check inbox (gt mail inbox)
    │   └── Process lifecycle messages in order
    │
    ├── 2. Detect zombie polecats
    │   └── For each zombie: nuke or escalate
    │
    ├── 3. Detect orphaned beads
    │   └── For each orphan: reset status, mail deacon
    │
    ├── 4. Detect stalled polecats
    │   └── For each stalled: nudge or escalate
    │
    ├── 5. Check for pending spawns
    │   └── Process spawn requests from daemon
    │
    └── 6. Write patrol receipt
        └── Machine-readable summary of findings
```

### 4.5 Who Services the Channel

The witness is the primary consumer, but the design supports opportunistic
servicing by other patrol agents:

| Agent | When It Services | What It Can Do |
|-------|-----------------|---------------|
| **Witness** | Every patrol cycle | Full lifecycle: spawn, nuke, escalate |
| **Deacon** | During rig-wide patrol | Detect unserviced requests, nudge witness |
| **Daemon** | Every heartbeat tick | Detect dead sessions, send LIFECYCLE messages |
| **Refinery** | During merge processing | Send MERGED/MERGE_FAILED to witness |

This creates redundant monitoring: if the witness misses a message, the deacon or
daemon detects the resulting state (dead session, orphaned bead) and either
handles it directly or nudges the witness.

---

## 5. GUPP + Pinned Work = Completion Guarantee

### 5.1 The Completion Invariant

As long as three conditions hold, a molecule WILL eventually complete:

1. **Work is pinned** (`hook_bead` set on agent bead)
2. **Sandbox persists** (branch + worktree exist)
3. **Someone keeps spawning sessions** (witness respawn on crash)

GUPP ensures that when a session starts with a hook, it executes. The hook
persists across session cycles. The sandbox provides continuity. The witness
provides resurrection. Together, these guarantee eventual completion.

### 5.2 The Completion Loop

```
┌─────────────────────────────────────────────┐
│              COMPLETION LOOP                 │
│                                              │
│   Session spawns → gt prime → discovers hook │
│        │                                     │
│        ▼                                     │
│   GUPP fires → execute current step          │
│        │                                     │
│        ▼                                     │
│   Step complete → bd close → handoff         │
│        │                                     │
│        ▼                                     │
│   More steps? ──yes──▶ Respawn session ──┐   │
│        │                                 │   │
│        no                                │   │
│        │                                 │   │
│        ▼                                 │   │
│   gt done → merge → nuke                 │   │
│                                          │   │
│   Session crashes? ──▶ Witness respawns ─┘   │
│                                              │
└─────────────────────────────────────────────┘
```

### 5.3 What Breaks the Guarantee

| Failure | Effect | Recovery |
|---------|--------|---------|
| Witness down | No respawn on crash | Deacon detects, restarts witness |
| Sandbox corrupted | Branch or worktree broken | `RepairWorktree()` or nuke and respawn |
| Hook cleared accidentally | GUPP doesn't fire | Witness `DetectOrphanedBeads()` finds in-progress bead, resets for re-dispatch |
| Dolt server down | Cannot read beads state | Daemon auto-restarts Dolt; polecat retries |
| Crash loop (3+ crashes) | Same step keeps failing | Witness escalates to mayor; filed as bug |

### 5.4 Liveness vs Safety

The system prioritizes **liveness** (work eventually completes) over strict safety
(no duplicate work). This means:

- **Duplicate detection is best-effort.** If two sessions somehow run the same
  step, the git branch serializes writes and one will fail to push.
- **Idempotent operations are preferred.** Closing an already-closed bead is a
  no-op. Pushing an already-pushed branch is safe.
- **Crash recovery may re-execute partial work.** A step that crashed mid-way
  will be re-executed from the start. Git state helps: if commits were made,
  the new session sees them.

---

## 6. Patrol Coordination

### 6.1 The Four Patrol Agents

Gas Town has four agents that perform patrol (periodic health monitoring):

| Agent | Scope | Frequency | Key Checks |
|-------|-------|-----------|-----------|
| **Daemon** | Town-wide | 3-minute heartbeat | Session liveness, GUPP violations, orphaned work |
| **Boot/Deacon** | Town-wide | Per daemon tick | Deacon health, witness health, cross-rig issues |
| **Witness** | Per-rig | Continuous | Polecat health, zombie detection, completion handling |
| **Refinery** | Per-rig | On demand | Merge queue processing, conflict detection |

### 6.2 Patrol Overlap as Resilience

Multiple agents observing overlapping state is intentional redundancy:

```
               Daemon                          Deacon
           (mechanical)                    (intelligent)
                │                               │
    ┌───────────┼───────────┐       ┌──────────┼──────────┐
    │           │           │       │          │          │
 Session    GUPP         Orphan   Witness   Refinery    Cross-rig
 liveness   violations   work    health    health      convoy
    │           │           │       │          │
    └───────────┤           │       │          │
                │           │       │          │
                ▼           ▼       ▼          ▼
              Witness               Witness    Refinery
           (per-rig patrol)      (responds)   (responds)
                │
    ┌───────────┼───────────┐
    │           │           │
 Zombie      Orphaned     Stalled
 detection   beads        polecats
```

**Key property:** If any single patrol agent fails, the others detect the
resulting state degradation and compensate. The daemon detects dead sessions.
The deacon detects dead witnesses. The witness detects dead polecats.

### 6.3 Information Flow Between Patrol Agents

```
Daemon ───LIFECYCLE:──────▶ Witness inbox
Daemon ───GUPP_VIOLATION:─▶ Witness inbox
Daemon ───ORPHANED_WORK:──▶ Witness inbox

Deacon ◀──heartbeat.json──── Daemon
Deacon ───nudge────────────▶ Witness (if stale)
Deacon ───nudge────────────▶ Refinery (if stale)

Witness ──MERGE_READY:────▶ Refinery inbox
Witness ──RECOVERED_BEAD:─▶ Deacon (for re-dispatch)
Witness ──patrol receipt───▶ Beads (audit trail)

Refinery ─MERGED:─────────▶ Witness inbox
Refinery ─MERGE_FAILED:───▶ Witness inbox
Refinery ─convoy check─────▶ Deacon (for stranded convoys)
```

### 6.4 Convergent State

All patrol agents converge on the same observable state: beads (via Dolt), git
(via branches and worktrees), and tmux (via session liveness). No agent maintains
private state that others depend on. This is the "discover, don't track" principle
applied to monitoring.

If state diverges (e.g., a message is lost), the next patrol cycle re-derives
state from observables and self-heals.

---

## 7. Resolved Design Questions

### Q1: Spoon-Feeding and Step Granularity

**Question:** How many logical steps per physical molecule step? How many steps
per polecat session?

**Answer:** Use formulas to define granularity, and let context pressure determine
session boundaries.

**Step granularity guidelines:**

| Step Type | Granularity | Example |
|-----------|-------------|---------|
| Setup / teardown | One physical step | "Set up working branch" |
| Implementation | One per logical unit | "Implement the solution" (may span sessions) |
| Verification | One per check type | "Run quality checks", "Self-review" |
| Handoff | One per lifecycle event | "Commit changes", "Submit work" |

The `mol-polecat-work` formula currently uses 10 steps. This is appropriate for
most work because:

- Each step has clear entry/exit criteria
- Steps are independently resumable (a crash mid-step loses at most one step's work)
- Context stays focused (one step's instructions, not the whole molecule)

**Session-per-step is a guideline, not a rule.** A polecat may complete multiple
steps in one session if context permits. The key constraint is that each step
is closed individually (no batch-closing — the Batch-Closure Heresy).

**Anti-patterns:**
- Steps so small they're just `git add` commands (overhead exceeds value)
- Steps so large they exhaust context (implementation + testing + review in one step)
- Steps that can't be independently resumed (step 3 requires step 2's context window)

### Q2: Mechanical vs Agent-Driven Recycling

**Question:** When is mechanical intervention (daemon-driven) appropriate vs
agent-driven (polecat requests its own recycle)?

**Answer:** Prefer explicit self-recycling. Use mechanical intervention only as a
safety net.

**The spectrum:**

```
AGENT-DRIVEN (preferred)              MECHANICAL (safety net)
├── gt done (polecat retires session) ├── Daemon detects dead session
├── gt handoff (polecat self-cycles)  ├── Daemon detects GUPP violation
├── gt escalate (polecat asks help)   ├── Witness zombie sweep
└── HELP mail (polecat signals)       └── Deacon restart on stale heartbeat
```

**Design principle:** The polecat is the authority on its own state. External
intervention should only occur when the polecat cannot speak for itself (dead
session, hung process, stuck-in-done).

**Concrete thresholds (agent-determined, not hardcoded):**

The daemon uses broad thresholds for safety-net detection:
- **GUPP violation:** 30 minutes with `hook_bead` but no progress
- **Hung session:** 30 minutes of no tmux output (`HungSessionThresholdMinutes`)
- **Stuck-in-done:** 60 seconds with `done-intent` label

These thresholds are intentionally generous. The goal is to catch truly stuck
polecats, not polecats that are thinking hard. False positives (the "Deacon
murder spree" bug) are worse than slow detection.

**The murder spree lesson:** Mechanical detection of "stuck" is fragile because
distinguishing "thinking deeply" from "hung" requires intelligence. This is why
Boot exists (intelligent triage) and why the daemon's thresholds are conservative.
Only the witness (an AI agent) should make judgment calls about whether a polecat
is truly stuck.

### Q3: Channel Implementation

**Question:** Mail-based, beads-based, or state file?

**Answer:** Mail-based. See [Section 4](#4-per-rig-polecat-channel) for full design.

**Why not beads-based (special issue type)?**
- Beads issues are durable work artifacts. Lifecycle requests are transient signals.
- Creating/closing beads for "recycle me" adds unnecessary Dolt write pressure.
- Mail is already the coordination primitive and has the right lifecycle (read → process → delete).

**Why not state files (rig/polecat-queue.json)?**
- State files require explicit locking for concurrent access.
- No audit trail (file gets overwritten).
- Doesn't integrate with existing patrol patterns (agents already check mail).
- Recovery after crash is harder (partially-written JSON).

### Q4: Who Spawns the Next Step?

**Question:** After a polecat completes a step and hands off, who spawns the
next session to continue the molecule?

**Answer:** The witness, triggered by either handoff detection or daemon lifecycle
request.

**The spawn chain:**

```
Polecat completes step
    │
    ├── Closes step bead
    ├── Calls gt handoff (creates handoff mail)
    └── Session exits
         │
         ▼
Daemon heartbeat tick
    │
    ├── Detects dead polecat session
    ├── Finds hook_bead still set (work isn't done)
    └── Triggers session restart
         │
         ▼
SessionManager.Start()
    │
    ├── Creates new tmux session in existing worktree
    ├── Injects env vars (GT_POLECAT, GT_RIG)
    ├── SessionStart hook fires: gt prime --hook
    └── New session discovers next step via bd mol current
```

**Current implementation:** The daemon's `processLifecycleRequests()` handles
this. When a session dies but the hook is still set, the daemon either sends a
`LIFECYCLE:` message to the witness or directly restarts the session (depending
on configuration). Polecat startup is handled end-to-end by the GUPP/beacon
flow (SessionManager → StartupNudge → BuildStartupPrompt → SessionStart hook
→ gt prime).

**Future (AT integration):** The witness spawns replacement teammates directly
via `Teammate({ operation: "spawn" })`. The SubagentStop hook detects teammate
death and triggers respawn. See `docs/design/witness-at-team-lead.md` for details.

---

## 8. Edge Cases and Failure Modes

### 8.1 The Stuck-in-Done Zombie

A polecat runs `gt done` but the session hangs before cleanup completes.

**Detection:** Witness `DetectZombiePolecats()` checks for `done-intent` label
older than 60 seconds with a live session.

**Recovery:** Witness kills the session and continues the cleanup pipeline
(verify `cleanup_status`, forward to refinery if MR exists).

### 8.2 The Orphaned Sandbox

A polecat directory exists but no tmux session and no `hook_bead`.

**Detection:** `Manager.ReconcilePool()` finds directories without sessions.
`DetectStalePolecats()` identifies sandboxes far behind main with no work.

**Recovery:** If no uncommitted work and no active MR, nuke the sandbox. If
uncommitted work exists, escalate (someone needs to decide if the work matters).

### 8.3 The Split-Brain Merge

The refinery starts merging while the polecat is still pushing.

**Prevention:** The `cleanup_status=clean` field on the agent bead serializes
this. The witness only sends `MERGE_READY` after verifying the polecat has
exited and the branch is clean. The merge slot provides additional serialization.

### 8.4 The Infinite Cycle

A step keeps failing and the session keeps restarting.

**Detection:** Track crash count per polecat (via `ReconcilePool` or
ephemeral state). Three crashes on the same step triggers escalation.

**Recovery:** Witness stops respawning, creates a bug bead, mails the mayor.
The molecule stays in its current state (recoverable when the bug is fixed).

### 8.5 Concurrent Polecats on Same Issue

Should not happen because the hook is exclusive (one `hook_bead` per agent bead,
one agent bead per polecat name). But if it does:

**Prevention:** Git branch naming includes a unique suffix (`+<timestamp>`).
The TOCTOU guard in `DetectZombiePolecats()` (records `detectedAt`, re-verifies
before destructive action) prevents racing between detection and action.

**Recovery:** The second session fails to push (branch diverged) and escalates.

---

## 9. Future: AT Integration Impact

The Agent Teams (AT) integration (see `docs/design/witness-at-team-lead.md`)
changes the transport layer but preserves the lifecycle model:

| Aspect | Current (tmux) | Future (AT) |
|--------|---------------|-------------|
| Session management | tmux sessions | AT teammates |
| Spawning | `SessionManager.Start()` | `Teammate({ operation: "spawn" })` |
| Health monitoring | tmux liveness + pane output | AT lifecycle hooks (SubagentStop) |
| Messaging | `gt nudge` (tmux send-keys) | AT messaging |
| Cleanup | Session kill (sandbox preserved) | `Teammate({ operation: "requestShutdown" })` (sandbox preserved) |

**What stays the same:**
- Beads as the durable ledger
- Molecules as workflow templates
- `gt done` as the polecat idle signal
- Two-stage cleanup (step vs molecule)
- Mail for cross-rig communication
- The completion guarantee (GUPP + pinned work + respawn)

**What changes:**
- The witness becomes an AT team lead (delegate mode)
- Zombie detection becomes structural (hooks vs polling)
- Polecat-to-polecat isolation is hook-enforced, not tmux-enforced
- Real-time coordination moves from tmux to AT (ephemeral), reducing Dolt pressure

---

## 10. Implementation Status (gt-o8g8 audit, 2026-03-07)

### Shipped

All core lifecycle operations are implemented and running in production:

| Operation | Command/Component | Key Implementation |
|-----------|------------------|-------------------|
| Spawn/assign | `gt sling` | `sling.go`, `polecat_spawn.go` — finds idle polecat or allocates new slot |
| Work execution | `gt prime --hook` | Session discovers hook via `bd mol current`, GUPP fires |
| Session cycling | `gt handoff` | `handoff.go` — all roles, preserves sandbox and identity |
| Step completion | `bd close` + `gt handoff` | Step cleanup: session dies, sandbox lives |
| Work submission | `gt done` | `done.go` — push, MR/PR handoff, set done, retire session |
| Polecat allocation | `gt sling` | `polecat/manager.go` + `polecat_spawn.go` — capacity allocation and branch setup |
| Zombie detection | Witness patrol | `witness/handlers.go`: `DetectZombiePolecats()` — restart-first, no auto-nuke |
| Stale detection | Witness patrol | `polecat/manager.go`: `DetectStalePolecats()` — tmux-based, protects paused states |
| Orphan recovery | Witness patrol | `witness/handlers.go`: `DetectOrphanedBeads()` — reset and re-dispatch |
| Cleanup pipeline | Mail-based | POLECAT_DONE → Witness → MERGE_READY → Refinery → MERGED |
| Merge queue | Refinery | Squash-merge, close MR and issue, convoy check |

### Pending

| Feature | Description | Impact |
|---------|-------------|--------|
| Refinery notifies mayor after merge | PRs #2436/#2437 closed; branch cleanup shipped, mayor notify not yet | Unblocks dependent work dispatch |

### Deferred (design only)

| Feature | Rationale for deferral |
|---------|----------------------|
| Pool size enforcement | On-demand allocation works; fixed pool is optimization, not correctness |
| `gt polecat pool init` | Polecats created naturally by first `gt sling`; pre-allocation unnecessary |
| `ReconcilePool()` | Witness patrol already detects state drift via zombie/stale/orphan checks |

---

## 11. Summary

See [concepts/polecat-lifecycle.md](../concepts/polecat-lifecycle.md) for the
complete lifecycle model (three layers, four states, persistent polecat design).
This document covers the implementation details: cleanup stages, mail channels,
patrol coordination, and edge case handling.
