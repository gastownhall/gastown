# Persistent Polecat Pool

**Issue:** gt-lpop
**Status:** Design
**Author:** Mayor

## Problem

Three concepts are conflated in the polecat lifecycle:

| Concept | Lifecycle | Current behavior |
|---------|-----------|-----------------|
| **Identity** | Long-lived (name, CV, ledger) | Destroyed on nuke |
| **Sandbox** | Per-assignment (worktree, branch) | Destroyed on nuke |
| **Session** | Ephemeral (Claude context window) | = polecat lifetime |

Consequences:
- Work is lost when polecats are nuked before pushing
- 219 stale remote branches from destroyed worktrees
- Slow dispatch (~5s worktree creation per assignment)
- Lost capability record (CV, completion history)
- Idle polecats were treated as waste and nuked

## Design

### Lifecycle Separation

```
IDENTITY (persistent)
  Name: "furiosa"
  Agent bead: gt-gastown-polecat-furiosa
  CV: work history, languages, completion rate
  Lifecycle: created once, never destroyed (unless explicitly retired)

SANDBOX (per-assignment, reusable)
  Worktree: polecats/furiosa/gastown/
  Branch: polecat/furiosa/<issue>+<timestamp>
  Lifecycle: synced to main between assignments, not destroyed

SESSION (ephemeral)
  Tmux: gt-gastown-furiosa
  Claude context: cycles on compaction/handoff
  Lifecycle: independent of identity and sandbox
```

### Pool States

```
         ┌──────────┐
    ┌───►│  IDLE    │◄──── sync sandbox to main
    │    └────┬─────┘      clear hook
    │         │ gt sling
    │         ▼
    │    ┌──────────┐
    │    │ WORKING  │◄──── session active, hook set
    │    └────┬─────┘
    │         │ work complete
    │         ▼
    │    ┌──────────┐
    └────┤  DONE    │──── push branch, submit MR
         └──────────┘
```

This historical design described IDLE reuse after DONE. Current behavior retires
clean completed sessions instead of returning them to the idle reuse pool.

### Pool Management

**Pool size:** Fixed per rig. Configured in `rig.config.json`:
```json
{
  "polecat_pool_size": 4,
  "polecat_names": ["furiosa", "nux", "toast", "slit"]
}
```

**Initialization:** `gt rig add` or `gt polecat pool init <rig>` creates N polecats
with identities and worktrees. They start in IDLE state.

**Dispatch:** `gt sling <bead> <rig>` allocates capacity, attaches work, and starts
a session on a fresh work branch.

**Completion:** When a polecat finishes work:
1. Push branch to origin
2. Submit MR (if code changes)
3. Clear hook_bead
4. Set state to DONE
5. Preserve branch/MR metadata for refinery/review
6. Retire the live session

### Sandbox Retirement (DONE transition)

When work completes, `gt done` preserves the branch and handoff metadata:

```bash
git push origin polecat/furiosa/<issue>@<suffix>
# Refinery/review and cleanup own the remaining branch/worktree state
```

When new work is slung:
```bash
# Create fresh branch from current main
git checkout -b polecat/furiosa/<new-issue>+<timestamp>
# Start working
```

No worktree add/remove. Just branch operations on an existing worktree.

### Refinery Integration

No changes to refinery. Refinery still:
1. Sees MR from polecat branch
2. Reviews and merges to main
3. Deletes remote polecat branch (NEW: add this step)

The polecat does not move to main locally during `gt done`; branch/MR metadata
remains available for refinery/review and cleanup.

### Witness Integration

Witness patrol behavior (shipped):
- Sees idle polecat → healthy state, skip
- **Stuck detection:** Polecat in WORKING state for too long → escalate (don't nuke)
- **Dead session detection:** Session died but state=WORKING → restart session (not nuke polecat)

### What Nuke Becomes

`gt polecat nuke` is reserved for exceptional cases:
- Polecat worktree is irrecoverably broken
- Need to reclaim disk space
- Decommissioning a rig

It should be rare and manual, not part of normal workflow.

### Branch Pollution Solution

With retired completion, branches have clear owners:
- Active branches: polecat is WORKING on them
- Merged branches: refinery deletes after merge
- Abandoned branches: cleanup/recovery decides after durable handoff evidence

The 219 stale branches came from nuked polecats that never cleaned up. Current
branch lifecycle is managed by refinery/recovery, not local branch deletion in `gt done`.

### One-time Cleanup

For the existing 219 stale branches:
```bash
# Delete all remote polecat branches that don't belong to active polecats
git branch -r | grep 'origin/polecat/' | grep -v 'furiosa/gt-ziiu' | grep -v 'nux/gt-uj16' \
  | sed 's/origin\///' | xargs -I{} git push origin --delete {}
```

## Implementation Phases

### Phase 1: Stop the bleeding — SHIPPED
- Witness no longer nukes idle polecats
- `gt done` transitions to DONE and retires the session instead of local sync/reuse
- Refinery deletes remote branch after merge

### Phase 2: Pool initialization — DEFERRED
- `gt polecat pool init <rig>` creates N persistent polecats
- Pool size configured in rig.config.json
- Worktrees are created for active work and retired after cleanup

**Status:** Polecats are allocated on-demand by `gt sling` via capacity allocation.
Pool size enforcement is a future optimization, not a blocker.

### Phase 3: Sandbox sync — SUPERSEDED
- DONE no longer syncs the worktree to main in `gt done`
- Clean completion sets done-state handoff and retires the session
- Branch/MR metadata remains for refinery/review and cleanup

### Phase 4: Session independence — SHIPPED
- Session cycling doesn't affect polecat state
- Dead sessions restarted by witness (restart-first policy, no auto-nuke)
- Handoff preserves polecat identity across session boundaries
- `gt handoff` works for all roles (Mayor, Crew, Witness, Refinery, Polecats)

### Phase 5: One-time cleanup — PARTIALLY SHIPPED
- Polecat branch cleanup after merge: SHIPPED (landed to main; PRs #2436/#2437 closed)
- Refinery notifies mayor after merge: not yet shipped
- Pool reconciliation (`ReconcilePool`): not yet implemented

### Implementation Status Summary

| Component | Status | Key Files |
|-----------|--------|-----------|
| `gt done` (push, MR/PR handoff, done-state session retirement) | SHIPPED | `internal/cmd/done.go` |
| `gt sling` (capacity allocation, branch setup) | SHIPPED | `internal/cmd/sling.go`, `polecat_spawn.go` |
| `gt handoff` (session cycle, all roles) | SHIPPED | `internal/cmd/handoff.go` |
| Witness patrol (zombie, stale, orphan detection) | SHIPPED | `internal/witness/handlers.go`, `internal/polecat/manager.go` |
| Cleanup pipeline (POLECAT_DONE → MERGE_READY → MERGED) | SHIPPED | `internal/witness/handlers.go`, `internal/refinery/engineer.go` |
| Idle polecat heresy fix (skip healthy idle) | SHIPPED | `internal/witness/handlers.go` |
| Restart-first policy (no auto-nuke) | SHIPPED | `internal/polecat/manager.go` |
| Polecat branch always deleted after merge | SHIPPED | `internal/refinery/engineer.go` |
| Refinery notifies mayor after merge | NOT SHIPPED | — |
| Pool size enforcement | DEFERRED | — |
| `ReconcilePool()` | DEFERRED | — |
| `gt polecat pool init` command | DEFERRED | — |
