# Local Gas Town Reconciliation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rebuild the local Gas Town distribution on current upstream while preserving all required local behavior, retiring completed polecats through upstream's proof-bound lifecycle, and making Mountain pause and merge-integration guarantees enforceable.

**Architecture:** Start from a clean current-upstream integration base and replay only audited local behavior, rather than rebasing the entire 35-commit branch mechanically. Make the town agent store authoritative for inventory, preserve upstream's intentional `WORKING -> DONE -> SAFE_TO_NUKE -> retired` lifecycle, and gate every ConvoyManager dispatch path on the Mountain pause label. Never return completed sandboxes to `IDLE`; after verified integration, canonical recovery and exact branch-ownership proof may retire them through the existing non-force cleanup path so the pool can create a fresh `IDLE` worker.

**Tech Stack:** Go, Cobra, Git, tmux, Beads/Dolt, TOML formulas, Go unit/integration tests.

---

## Upstream-First Triage Rule

Before designing or implementing any Gas Town bug fix, inspect the live
`gastownhall/gastown` GitHub repository for an existing fix or documented
intent. Verify the current `main` ref, then search relevant issues, pull
requests, commits, documentation, and code history. Reuse or align with
upstream behavior when available; implement a local fix only after recording
what upstream provides, what remains missing, and why the local change is
necessary.

## Compatibility Matrix

| Group | Action | Commits |
|---|---|---|
| Upstream safety base | Take from current `origin/main` | `b3bf0852`, `5213588f`, `5001748f`, `0b0259ff..c01557c0`, `cc9aecb1`, `e01f892d`, `0d007297` |
| Copilot composer behavior | Replay and reconcile over `e01f892d` | `80ab5a1f`, `b23bd932`, `2c575410`, `a63d7e99`, `ae524f1d`, `5c2e673f`, `353fb9be`, `e57e97b2` |
| Local base-ref completion | Replay in order; manually reconcile `done.go` | `07b5ad43`, `41e0cd2b`, `01c69c99`, `9bf3c8e2`, `6126ef9f`, `5490a7e1`, `5ee4b8ca` |
| Polecat formula protection | Replay after the base-ref stack | `e1d7a089`, `c2fad8e6`, `4f0cf3d4` |
| Prefix-aware refinery patrol | Replay over upstream post-merge formula | `9ee7c350`, `04df2d86`, `bdba8c9d` |
| StateDone classifier | Keep patch-equivalent behavior, not duplicate history | local `a2a57539` or upstream `4ba6105e` |
| Temporary direct-DONE reuse | Keep only during migration; then remove | `524a4f5b` |
| Net-zero reverted ancestry gate | Drop both implementation and revert | `e6718643`, `51ab0347`, `4901b7dc`, `b8b7424b`, `2c0a24d3`, `e8771360`, `33b1e438` |
| Historical design docs | Preserve/update only if still accurate | preserve `9d22ad1d`, `3b3b20b6`, `04f23b1e`, `a3aa3858`; update `e5dd48ad` |

## File Responsibilities

- `internal/cmd/done.go`: preserve resolved dispatch-base and target semantics on top of upstream worktree/source validation.
- `internal/tmux/tmux.go`, `internal/cmd/nudge_poller.go`: retain Copilot runtime/composer delivery behavior over upstream process snapshots.
- `internal/cmd/polecat.go`, `internal/cmd/polecat_capacity.go`: read agent metadata from `ForAgentBead()` while retaining rig-local active-work queries.
- `internal/refinery/terminal_mr.go`, `internal/refinery/manager.go`: preserve upstream verified post-merge MR/source cleanup and matching `active_mr` clearing.
- `internal/witness/handlers.go`, `internal/polecat/workstate.go`: retire only canonically `SAFE_TO_NUKE` completed workers through non-force cleanup after verified merge evidence.
- `internal/polecat/manager.go`: preserve idle-only allocation; completed sandboxes are retired, never reused.
- `internal/daemon/convoy_manager.go`: enforce Mountain pause before reactive and stranded dispatch.
- `internal/convoy/operations.go`: enforce the same pause policy at the shared continuation boundary where feasible.
- `internal/cmd/mq.go`: retain upstream target-reachability proof and conditional branch deletion.
- `internal/formula/formulas/mol-refinery-patrol.formula.toml`: retain upstream verified post-merge flow and local routed step inspection.
- `docs/concepts/polecat-lifecycle.md`: document the final `DONE -> SAFE_TO_NUKE -> retired` transition and evidence boundary.

### Task 1: Establish the reviewed upstream integration base

**Files:**
- No source files modified.

- [ ] **Step 1: Obtain the human-selected integration branch**

Use the exact branch approved by the human. The repository has `origin/main` but no `develop`, so do not create a branch until the base exception is explicitly approved.

- [ ] **Step 2: Create the selected branch from the approved base**

```bash
git fetch origin
git switch --create feature/local-gastown-reconciliation origin/main
```

Expected: the branch starts at current `origin/main`, currently containing the upstream safety commits in the matrix.

- [ ] **Step 3: Record the old local tip**

```bash
git branch archive/integration-local-combined-routing integration/local-combined-routing
```

Expected: the original local stack remains recoverable without mixing it into the new history.

### Task 2: Replay the local composer and resolved-base behavior

**Files:**
- Modify: `internal/tmux/tmux.go`
- Modify: `internal/cmd/nudge_poller.go`
- Modify: `internal/cmd/done.go`
- Modify: `internal/cmd/polecat_spawn.go`
- Modify: `internal/cmd/sling_schedule.go`
- Modify: `internal/formula/formulas/mol-polecat-work.formula.toml`
- Modify: existing tests adjacent to these files

- [ ] **Step 1: Replay the composer commits**

```bash
git cherry-pick 80ab5a1f b23bd932 2c575410 a63d7e99 ae524f1d 5c2e673f 353fb9be e57e97b2
```

Resolve `internal/tmux/tmux.go` by retaining upstream `e01f892d` process snapshots as the traversal source and retaining all eight local Copilot state/delivery decisions.

- [ ] **Step 2: Run the tmux and nudge-poller tests**

```bash
go test ./internal/tmux ./internal/cmd -run 'Copilot|Nudge|Composer|Poller'
```

Expected: PASS.

- [ ] **Step 3: Replay the resolved-base stack**

```bash
git cherry-pick 07b5ad43 41e0cd2b 01c69c99 9bf3c8e2 6126ef9f 5490a7e1 5ee4b8ca
```

In `internal/cmd/done.go`, preserve upstream fail-closed worktree/source validation first, then apply the local resolved `baseRef`, `preVerifiedBase`, and no-MR target checks. Do not restore the reverted ancestry-gate series.

- [ ] **Step 4: Run focused completion and sling tests**

```bash
go test ./internal/cmd -run 'Done|Sling|BaseRef|Target|NoMR'
```

Expected: PASS.

- [ ] **Step 5: Commit any manual conflict reconciliation**

```bash
git add internal/tmux internal/cmd internal/formula
git commit -m "fix: reconcile local runtime and dispatch-base behavior"
```

### Task 3: Replay formula and routed-refinery behavior

**Files:**
- Modify: `internal/formula/formulas/mol-polecat-work.formula.toml`
- Modify: `internal/formula/formulas/mol-refinery-patrol.formula.toml`
- Modify: `internal/formula/local_base_test.go`
- Modify: `internal/templates/roles/refinery.md.tmpl`
- Modify: `internal/templates/templates_test.go`
- Modify: `docs/design/polecat-lifecycle-patrol.md`

- [ ] **Step 1: Replay the local-branch preservation stack**

```bash
git cherry-pick e1d7a089 c2fad8e6 4f0cf3d4
```

Expected behavior: stable local `refs/heads/*` bases are preserved, validated before replay, and dotted issue IDs are matched literally.

- [ ] **Step 2: Replay prefix-aware refinery inspection**

```bash
git cherry-pick 9ee7c350 04df2d86 bdba8c9d
```

Resolve `mol-refinery-patrol.formula.toml` by keeping upstream `gt mq post-merge` proof and replacing only step inspection with routed `gt show`.

- [ ] **Step 3: Run focused formula and template tests**

```bash
go test ./internal/formula ./internal/templates
```

Expected: PASS.

### Task 4: Make the town agent store authoritative for inventory

**Files:**
- Modify: `internal/cmd/polecat.go`
- Modify: `internal/cmd/polecat_capacity.go`
- Test: `internal/cmd/polecat_inventory_test.go`
- Test: `internal/cmd/polecat_capacity_test.go`

- [ ] **Step 1: Write failing routed-agent-store tests**

Add test seams around agent listing so the test provides different rig-store and town-agent-store results. Assert that a town agent bead containing:

```text
agent_state: done
cleanup_status: clean
active_mr: null
last_source_issue: auso-oz4.4
```

produces `Reusable=true`, `CleanupStatus=clean`, and does not become `cleanup-unknown` merely because the rig-local store has no agent bead.

- [ ] **Step 2: Run the tests and verify failure**

```bash
go test ./internal/cmd -run 'Polecat.*AgentStore|Capacity.*AgentStore'
```

Expected: FAIL because both production call sites currently use `beads.New(rigPath).ListAgentBeads()`.

- [ ] **Step 3: Route only agent metadata to the town store**

Change both call sites to:

```go
rigBeads := beads.New(rigPath)
agents, err := rigBeads.ForAgentBead().ListAgentBeads()
```

Keep `listActivePolecatWorkByName(rigBeads, rigName)` on the rig authority because source work belongs to the rig store.

- [ ] **Step 4: Run focused tests**

```bash
go test ./internal/cmd -run 'Polecat|Capacity'
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cmd/polecat.go internal/cmd/polecat_capacity.go internal/cmd/*polecat*test.go
git commit -m "fix(polecat): read inventory metadata from agent authority"
```

### Task 5: Retire completed workers only after verified integration

**Upstream contract:** Preserve PR #4434 (`DONE` retirement), PR #4530
(`StateDone` may classify `SAFE_TO_NUKE`), and PR #4560 (post-merge proof
before cleanup). Do not transition completed workers back to `IDLE`.

**Files:**
- Modify: `internal/witness/handlers.go`
- Modify only if needed: `internal/polecat/workstate.go`
- Test: `internal/witness/handlers_test.go`
- Test: adjacent polecat recovery/workstate tests

- [ ] **Step 1: Write failing proof-bound retirement tests**

Cover verified merged completion, pending MR, dirty worktree, live session,
hooked worker, failure flags, missing/ambiguous source, mismatched branch owner,
and the sibling-branch cross-wire reported by upstream issue #4535. Assert that
uncertain cases preserve the worker and evidence unchanged.

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/witness ./internal/polecat -run 'Merged|Retire|SafeToNuke|BranchOwner'
```

Expected: verified terminal `DONE` workers remain stranded because no general
post-merge owner invokes safe retirement.

- [ ] **Step 3: Add narrow post-merge retirement**

After existing verified post-merge MR/source cleanup, ask the canonical
workstate/check-recovery path to classify the exact owning worker. Require:

- `StateDone`;
- terminal merged MR and terminal source evidence;
- matching/cleared `active_mr` according to upstream compare-and-clear rules;
- `cleanup_status=clean`, empty hook, no push/MR failure flags;
- no live session;
- clean live git predicates;
- exactly one worktree owning the submitted branch and matching agent.

Only `SAFE_TO_NUKE` may call the existing non-force nuke/removal path. Never
use `--force`, never convert to `IDLE`, and never delete a branch when ownership
is missing or ambiguous. Errors must preserve the sandbox and surface for retry.

- [ ] **Step 4: Run focused tests and commit**

```bash
go test ./internal/witness ./internal/polecat -run 'Merged|Retire|SafeToNuke|BranchOwner'
git add internal/witness internal/polecat
git commit -m "fix(polecat): retire merged workers after verified cleanup"
```

### Task 6: Retire proven legacy workers and preserve idle-only allocation

**Files:**
- Modify: `internal/cmd/polecat.go`
- Modify: `internal/polecat/manager.go`
- Modify: `internal/polecat/manager_test.go`
- Modify: `docs/concepts/polecat-lifecycle.md`

- [ ] **Step 1: Add a dry-run retirement command test**

Add `gt polecat retire-completed [rig] --dry-run --json`. It must report the
canonical recovery decision and exact branch-ownership evidence for every
`StateDone` worker. Dirty, unknown, failed, hooked, live, pending-MR, missing,
or ambiguous workers are reported but never changed.

- [ ] **Step 2: Implement explicit legacy retirement**

Without `--dry-run`, re-read and compare the assessed evidence, then invoke the
same non-force retirement path as Task 5 only for `SAFE_TO_NUKE`. Support older
merged workers whose matching `active_mr` was already safely cleared, but do
not infer merge success from absence alone.

- [ ] **Step 3: Preserve idle-only allocation**

`FindIdlePolecat` must admit only `StateIdle`. Remove temporary direct-DONE
admission and assert a clean `StateDone` worker is unavailable until retired.
Pool replenishment creates a fresh `IDLE` sandbox after retirement.

- [ ] **Step 4: Update lifecycle documentation**

Document:

```text
WORKING -> DONE -> SAFE_TO_NUKE -> RETIRED
                                  -> fresh IDLE worker on replenishment
```

- [ ] **Step 5: Run dry-run, tests, and commit**

```bash
gt polecat retire-completed --all --dry-run --json
go test ./internal/polecat ./internal/cmd
git add internal/polecat internal/cmd docs/concepts/polecat-lifecycle.md
git commit -m "fix(polecat): retire proven legacy workers safely"
```

### Task 7: Enforce Mountain pause on every successor-dispatch path

**Files:**
- Modify: `internal/daemon/convoy_manager.go`
- Modify: `internal/convoy/operations.go`
- Test: `internal/daemon/convoy_manager_test.go`
- Test: `internal/daemon/convoy_manager_integration_test.go`
- Test: existing `internal/convoy/*_test.go`

- [ ] **Step 1: Write failing reactive and stranded pause tests**

Create a Mountain convoy with labels:

```go
[]string{"mountain", "mountain:paused"}
```

Assert:

1. A close event may update completion state but never invokes `gt sling`.
2. A stranded scan with ready issues never invokes `gt sling`.
3. Removing `mountain:paused` allows exactly one ready issue to dispatch.
4. A regular non-Mountain convoy is unaffected.
5. Failure to read the pause state fails closed for dispatch and logs the error.

- [ ] **Step 2: Run tests and verify failure**

```bash
go test ./internal/daemon ./internal/convoy -run 'Paused|Mountain'
```

Expected: FAIL because `mountain:paused` currently has no production reader outside the CLI that writes/removes it.

- [ ] **Step 3: Add a shared dispatch-permission check**

Use the authoritative HQ convoy issue and implement semantics equivalent to:

```go
func convoyDispatchAllowed(issue *beads.Issue) bool {
    return !beads.HasLabel(issue, "mountain:paused")
}
```

Apply it immediately before execution, not only while computing readiness, so deferred work cannot dispatch after a pause race.

- [ ] **Step 4: Gate reactive continuation**

In the `CheckConvoysForIssue -> feedNextReadyIssue` chain, load the convoy immediately before sling. If paused, log and return without dispatch.

- [ ] **Step 5: Gate stranded dispatch**

Before `feedFirstReady`, load the convoy from HQ and apply the same check. On lookup error, do not sling.

- [ ] **Step 6: Run focused and integration tests**

```bash
go test ./internal/daemon ./internal/convoy
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/daemon/convoy_manager.go internal/daemon/*convoy*test.go internal/convoy
git commit -m "fix(mountain): enforce pause before successor dispatch"
```

### Task 8: Keep MR-backed sources open until verified integration

**Files:**
- Modify: `internal/cmd/done.go`
- Modify: `internal/cmd/molecule_step.go`
- Test: `internal/cmd/done_test.go`
- Test: `internal/cmd/done_closeDescendants_test.go`
- Test: `internal/cmd/molecule_step_test.go`
- Test: `internal/refinery/manager_test.go`
- Test: `internal/convoy/operations_test.go`

- [ ] **Step 1: Write the failing source-transition tests**

Cover a normal merge-request completion with an ordinary `blocks` successor:

```go
source := issue("gt-source", "in_progress")
successor := issue("gt-successor", "open", blocks("gt-source"))
result := runDoneWithVerifiedMR(source)

assert.Equal(t, "in_progress", result.SourceStatus)
assert.True(t, result.MRCreated)
assert.True(t, result.SuccessorBlocked)
```

Also cover proof failure, superseded MR retry, attached-molecule cleanup, MR-backed workflow steps on `COMPLETED` and `DEFERRED`, `gt mol step done`, rejection/requeue, direct merge, and no-merge behavior.

- [ ] **Step 2: Run the red tests**

```bash
go test ./internal/cmd ./internal/refinery ./internal/convoy -run 'Source.*Integration|MRBacked|PostMerge'
```

Expected: FAIL because the common `notifyWitness` path closes `hookedBeadID` after creating a verified MR.

- [ ] **Step 3: Make source ownership explicit**

Enforce centrally that an issue referenced by an open MR cannot become terminal outside verified post-merge. In the normal MR path, retain the source issue and attached source molecule until verified post-merge cleanup. Continue clearing the worker hook and retiring the session, but do not call `hookBd.Close(hookedBeadID)` or burn source attachment descendants while `mrID` is non-empty. Apply the same guard to workflow-step and molecule-step completion.

Direct-merge and no-merge paths retain their existing terminal behavior. Failed MR creation keeps the source open.

- [ ] **Step 4: Verify refinery is the only MR source closer**

Require:

```text
verify target contains submitted SHA
-> close MR as merged
-> close source
-> clear only matching MR metadata
-> leave canonical evidence for witness retirement
```

A failure at any stage must leave the remaining evidence retriable.

- [ ] **Step 5: Run focused tests and commit**

```bash
go test ./internal/cmd ./internal/refinery ./internal/convoy -run 'Done|Source|PostMerge|MergeBlocks'
git add internal/cmd internal/refinery internal/convoy
git commit -m "fix(done): retain MR sources until verified integration"
```

Expected: PASS.

### Task 9: Make respawn accounting attempt-scoped

**Files:**
- Modify: `internal/cmd/polecat_spawn.go`
- Modify: `internal/witness/spawn_count.go`
- Test: existing adjacent sling and polecat tests

- [ ] **Step 1: Write failing respawn-epoch tests**

Cover:

```text
failed preflight/checkout/allocation -> counter unchanged
established failed session -> counter increments once
successful completion -> deterministic reset/new epoch
reopened bead -> deterministic reset/new epoch
concurrent dispatch -> live circuit breaker remains enforced
```

- [ ] **Step 2: Run the red tests**

```bash
go test ./internal/cmd ./internal/witness -run 'Respawn|SpawnCount'
```

Expected: failed allocation currently consumes an attempt and lifecycle epochs are not represented.

- [ ] **Step 3: Count established attempts, not intentions**

Move accounting after successful worker/session establishment and record the bead lifecycle epoch. A failed preflight, checkout, or allocation must leave the count unchanged. Preserve the live circuit breaker and define completion/reopen reset semantics without silently clearing a live failure loop.

- [ ] **Step 4: Run focused tests and commit**

```bash
go test ./internal/cmd ./internal/witness -run 'Respawn|SpawnCount|Sling'
git add internal/cmd/polecat_spawn.go internal/witness
git commit -m "fix(sling): scope respawn counts to established attempts"
```

Expected: PASS.

### Task 10: Bind continuation to one branch-owning worktree

**Files:**
- Modify: `internal/cmd/sling.go`
- Modify: `internal/cmd/sling_dispatch.go`
- Modify: `internal/cmd/polecat_spawn.go`
- Modify: `internal/polecat/manager.go`
- Modify: `internal/git/git.go`
- Test: existing adjacent sling, git, and polecat tests

- [ ] **Step 1: Write failing continuation tests**

Cover idle, done, occupied, missing-local, and remote-only branches. Assert that an occupied branch resolves to its existing worker or fails without mutation; no path may use forced duplicate checkout. Assert exactly one canonical `base_branch` and `base_ref` for direct, deferred, batch, local, and continuation dispatch.

- [ ] **Step 2: Run the red tests**

```bash
go test ./internal/cmd ./internal/git ./internal/polecat -run 'Continuation|Occup|BaseRef|ResumeBranch'
```

Expected: new-worker continuation can force a duplicate checkout while reuse fails, and formula variables can contain contradictory duplicate bases.

- [ ] **Step 3: Resolve ownership before allocation**

Locate the unique worktree owning the requested branch. Reactivate that exact polecat when safe, or fail closed with the owning worker/worktree in the error. Remove forced duplicate branch checkout. Keep continuation source distinct from integration target and canonicalize formula variables before dispatch.

- [ ] **Step 4: Run focused tests and commit**

```bash
go test ./internal/cmd ./internal/git ./internal/polecat -run 'Sling|Continuation|Occup|BaseRef|ResumeBranch'
git add internal/cmd internal/git internal/polecat
git commit -m "fix(sling): bind continuation to branch ownership"
```

Expected: PASS.

### Task 11: Validate the combined build and install safely

**Files:**
- Modify only if validation reveals defects directly caused by the reconciliation.

- [ ] **Step 1: Run the full Go test suite**

```bash
TMPDIR=/private/tmp \
CGO_CPPFLAGS="-I$(brew --prefix icu4c@78)/include" \
CGO_LDFLAGS="-L$(brew --prefix icu4c@78)/lib" \
go test ./...
```

Expected: PASS.

- [ ] **Step 2: Build with the supported project command**

```bash
TMPDIR=/private/tmp \
CGO_CPPFLAGS="-I$(brew --prefix icu4c@78)/include" \
CGO_LDFLAGS="-L$(brew --prefix icu4c@78)/lib" \
make build
```

Expected: PASS.

- [ ] **Step 3: Review the full diff against upstream**

```bash
git diff --stat origin/main...HEAD
git log --oneline origin/main..HEAD
```

Expected: only the audited local behavior and the new lifecycle/Mountain fixes are present; the reverted ancestry-gate series is absent.

- [ ] **Step 4: Install and verify live behavior**

```bash
TMPDIR=/private/tmp \
CGO_CPPFLAGS="-I$(brew --prefix icu4c@78)/include" \
CGO_LDFLAGS="-L$(brew --prefix icu4c@78)/lib" \
make install

gt version
gt polecat list --all --json
gt mountain status --json
```

Expected: inventory agrees with `check-recovery`, proven merged legacy workers
are retired and replaced with fresh idle pool capacity, pending/unsafe
completed workers remain done, and paused Mountains dispatch no successors.

Also run one daemon dog-molecule cycle against installed `bd` and verify the map-keyed `bd show --children --json` envelope is parsed: steps are discovered and closed without `unknown step` or `unrecognized JSON shape` log entries. Confirm a successful 13/13 Dolt backup does not emit a FAILED escalation.

### Task 12: Review, publish, and integrate

**Files:**
- No additional source files.

- [ ] **Step 1: Request independent code review**

Review specifically:

- resolved-base semantics after upstream `done.go` changes;
- Copilot process-snapshot reconciliation;
- agent-store routing;
- proof-bound post-merge retirement ordering, branch ownership, and idempotency;
- pause race behavior in both dispatch paths;
- source retention from MR submission through verified post-merge;
- respawn, continuation, reservation, and branch-occupancy ownership.

- [ ] **Step 2: Push the approved feature branch**

```bash
git push --set-upstream origin feature/local-gastown-reconciliation
```

- [ ] **Step 3: Open a pull request to the repository’s approved integration target**

Do not merge it until the human explicitly approves the final merge.
