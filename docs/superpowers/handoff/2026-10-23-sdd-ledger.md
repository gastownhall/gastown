# SDD ledger — plan: docs/superpowers/plans/2026-10-22-gtb-reshape.md
Spec: docs/superpowers/specs/2026-10-22-gtb-reshape-design.md
Worktree: /Users/admin/Me/tieudidi/gtb-reshape, branch feat/gtb-reshape (from main 734f1373)
Env: macOS needs CGO_CPPFLAGS=-I/opt/homebrew/opt/icu4c@78/include CGO_LDFLAGS=-L/opt/homebrew/opt/icu4c@78/lib for go build/test (Makefile sets these automatically).
Baseline: go build ./... OK; internal/config -short had one flaky failure, passes with -count=1.

## Pre-flight scan
| Pair / task | Produces vs consumes | Finding |
|---|---|---|
| T2→T3 | Kind/Step types → Store/Items | consistent |
| T3→T5 | Store.Transition/ErrStepConflict, Items → Advance | consistent |
| T4→T5,T6 | GatedWorkflow, ChecksApproved, CheckCommandsSHA → gates/Check | consistent |
| T5→T6 | GateEnv → Red/Green/Check/HumanApprove | consistent; T6 also writes outputs/ under artifact dir |
| T5,T6→T7 | Advance/HumanApprove/... → CLI | consistent; T7 needs a Runner + Terminal impl (production) — T6 defines interfaces; prod impls belong in T6 |
| T3,T5→T8 | Items labels gtb:wf/gtb:step → resolveFormula, done | resolveFormula signature change touches all callers (sling_batch.go:73 and others) — T8 must update callers |
| T5→T9 | merge step never passes in Advance; refinery moves merge→done | "done" is not in Steps(); T2 Next() at merge returns false. Ruling R1 |
| T3→T9 | Store.Transition(from=merge,to=execute) | T3 Transition checks latest To==from — merge→execute valid |
| T1 vs T10 | both edit Makefile build + goreleaser (proxy binaries) | sequential, no conflict |
| T11→T12,T13,T14,T15 | protocol + Store methods | consistent; T12 Store.CreateEnrollCode adds to T11 Store |
| T13→T14 | /agent/v1 routes ↔ client | consistent |
| T5/T13 | Observer field on GateEnv added in T14 (modifies T5 type) | Ruling R2 |
| T15→T16 | /api/v1 routes → UI | consistent |
| T16 embed dir internal/server/dist written by Makefile web target; T15 needs placeholder | T15 must commit internal/server/dist/.keep + placeholder fallback | consistent with T15 text |
| T17 vs existing deploy | replaces deploy/k3s/gtb.yaml from e30786fe | intended |
| T18 deletes npm-package | release.yml/goreleaser may reference npm | T18 must remove those references |
| T19 | modifies Dockerfile.e2e:74 | consistent |
| each task self-consistency | tests vs code vs files | T8: "13 gtb-* files" = 5+5+3 = 13 ✓. T7 exit codes 3/4 ✓ used by T8 formulas. T15 GTB_TEST_LOGIN referenced in T19 but defined in neither → Ruling R3 |

## Rulings
- Ruling R1: Step "done" is a terminal pseudo-step written only by the refinery; Steps()/Next() stay as specified and gates treat Step "done" as complete (epic/tasks gate checks Step=="done") — plan T5 epic/tasks row says "at step merge with a merge transition recorded as done"; simplify to Step=="done" — cost if wrong: small gate edit.
- Ruling R2: GateEnv.Observer (interface OnEvent(kind string, itemID string, data any)) is introduced in Task 5 as a nil-safe field so T14 does not edit T5's type; workflow must not import agentlink (agentlink imports workflow) — Observer takes workflow-local types and agentlink adapts — cost if wrong: one adapter.
- Ruling R3: GTB_TEST_LOGIN test bypass is implemented in Task 15 behind build tag `e2e` (file auth_e2e.go) — T19 consumes it — cost if wrong: e2e harness change.
Task 1: dispatched (BASE 734f1373, implementer a5d87dba7f1ea56e1, sonnet)
Ruling R4: subagents run on the inherited session model (sonnet/haiku tiers return model_not_found via this router) — only working model — cost: higher token spend per task.
Task 1: re-dispatched (BASE 734f1373, implementer a66ba38f481355db7, session model)
Task 1: implementer stopped by user mid-task (uncommitted changes left in worktree); paused awaiting user
Task 1: resumed by fresh implementer a5bc488c1b8ad1b73 from uncommitted partial changes (user asked to resume)
Task 1: implemented 4492c020 (DONE_WITH_CONCERNS: goreleaser not installed; no macOS CI job exists; `gtb version` prints "gt version"; cli.Name() defaults "gt" in ~80 hints; stale cmd/gt refs in uninstall.go/formula/scripts/docs; 3 pre-existing -short failures in internal/cmd also on base). Review dispatched a211cd65b37861f90.
Task 1: review a211cd65 — Needs fixes. Important: (I1) npm-package postinstall expects `gt` in archives → broken release; (I2) brew/nix/go-install users get no `gt` on PATH but ~20 exec.Command("gt") calls need it. ⚠️ goreleaser check not run.
Ruling R5: I1 fixed by pulling Task 18's npm removal forward — delete npm-package/ and the release.yml publish-npm job now (spec §6: binaries+checksums only) — cost: none, Task 18 then just skips that bullet.
Ruling R6: I2 fixed minimally in Task 1 (brew `bin.install_symlink "gtb" => "gt"`, nix symlink); replacing exec.Command("gt") with self-exec is a follow-up recorded for final review — cost: non-symlinked installs (raw archive, go install) still need a manual `gt` symlink until then.
Ruling R7: goreleaser check must actually run — implementer may use `go run github.com/goreleaser/goreleaser/v2@<version pinned in release.yml> check` — cost: a module download.
Task 1: minor (deferred): two command-name sources (cli.Name() defaults "gt", ~80 hints) — make cli.Name() the single source defaulting to gtb
Task 1: minor (deferred): Makefile shadow cleanup ignores old ~/go/bin/gt
Task 1: minor (deferred): stale cmd/gt refs: scripts/test-gce-install.sh:108, uninstall.go hint
Task 1: minor (deferred): flake pname/app names "gt", buildGT names, `gtb version` prints "gt version"
Task 1: minor (deferred): test calls applyRootCommandName not init(); add comment
Task 1: fix round 1 implemented c922bc46 (npm-package removed incl. bump-version.sh/release formula/RELEASING/codecov/renovate refs; brew+nix gt symlink; goreleaser v2.18.3 check validated, linux_arm64 override confirmed; nix untested). Re-review a16c6e95 dispatched.
Task 1: fix round 1/5 (3 addressed, 0 open; commits 4492c020..c922bc46)
Task 1: minor (deferred): brew formula test block doesn't assert bin/gt symlink exists
Task 1: follow-up (from R6): replace exec.Command("gt", ...) (~20 sites) with self-exec so raw-archive/go-install users don't need a gt symlink
Task 1: complete (commits 734f1373..c922bc46, review clean)
Task 2: dispatched (BASE c922bc46)
Task 2: implemented 903901e4 (DONE; note: frontmatter values not unquoted)
Task 2: minor (deferred): add tests for frontmatter-key reorder and whitespace-in-frontmatter voiding approval (Review Focus 1)
Task 2: minor (deferred): moving only the status: line within frontmatter keeps the hash — intended, document it
Task 2: minor (deferred): no test pins first-status-match-only
Task 2: minor (deferred): ArtifactPath does not validate itemID/rig (../..) — Task 7 CLI must validate IDs
Task 2: minor (deferred): StepDone comment names refinery; ReadArtifact normalises twice
Task 2: complete (commits c922bc46..903901e4, review clean)
Task 3: dispatched (BASE 903901e4)
Task 3: implemented 4b1d10b7 (DONE_WITH_CONCERNS: FOR UPDATE doesn't serialize in Dolt → added seq column UNIQUE(item_id,seq), loser mapped to ErrStepConflict via error-text match "constraint violation" (1105); extra label gtb:rig:<rig>; SetStep/SetWaiting read-modify-write non-atomic; beadsItems.Create untested with fake bd)
Task 3: review a3b67cfe — Approved. ⚠️ resolved by controller: dolt_force_transaction_commit is not set anywhere in repo/deploy config (default off) → losing commits roll back.
Ruling R8: Task 5 Advance must treat Store.Transition as the arbiter and make Items.SetStep failure recoverable: on SetStep error after a successful Transition, return the error and have Gate/Get reconcile by preferring the store's latest transition To over the bead label (store is append-only evidence, bead label is a cache) — reverses spec wording "bead is source of truth" only for the step field — cost if wrong: a step shown from the bead could lag the store until next advance.
Task 3: minor (deferred): fixtures use Source "cli"/Provenance "p" outside enums; store doesn't validate enums or cap Tail (Runner caps per plan)
Task 3: minor (deferred): beadsItems.List not routed to rig db (document NewBeadsItems needs rig-bound Beads); List("") matches nothing
Task 3: minor (deferred): SetStep/SetWaiting don't map beads.ErrNotFound→ErrItemNotFound
Task 3: minor (deferred): race detection matches Dolt 1105 text "constraint violation" — add version comment
Task 3: minor (deferred): integration race test covers only first transition, not existing-row race
Task 3: minor (deferred): labels unsanitized (comma in reason/rig splits labels) — Task 7 must validate rig names / reasons
Task 3: minor (deferred): fakeBD unused id param
Task 3: complete (commits 903901e4..4b1d10b7, review clean)
Task 4: dispatched (BASE 4b1d10b7)
Task 4: implemented 79db7eb9 (DONE: added AddRigOptions.ValidateCheckout hook in internal/rig/manager.go (not in brief file list) since clone+register are inside Manager.AddRig; adopt checks rig dir+mayor/rig+refinery/rig; 9 pre-existing Rig -short failures also on base)
Task 4: review a1197735 — Approved. ⚠️ adopt e2e untested (code path read: check precedes RegisterRig).
Task 4: minor (deferred): rejectTbbRepo fails open on non-NotExist stat errors (should surface)
Task 4: minor (deferred): refusal message names deleted temp clone path; name rig/URL instead
Task 4: minor (deferred): ChecksApproved(nil) panics; GatedWorkflow budgets not validated (negatives accepted)
Task 4: complete (commits 4b1d10b7..79db7eb9, review clean)
Task 5: dispatched (BASE 79db7eb9)
Task 5: implemented 927838f2, b37f96b1 (DONE_WITH_CONCERNS: Task 3 race test flaky — Dolt 1213 serialization failure not mapped by raceErr → lost race surfaces as generic error, not ErrStepConflict)
Ruling R9: the 1213 mapping is load-bearing for Task 7 exit code 4 and Review Focus 4 — fold it into Task 5's fix round as a finding (store.go raceErr: map MySQL 1213 and 1105 conflict text to ErrStepConflict; run the race test ≥20x) — cost: Task 5 diff touches a Task 3 file.
Task 5: review a949a5b7 — Needs fixes. Important: (I1) execute/reproduce/breakdown gates read plan.md steps / report.md repro / epic.md ACs without requiring human approval of current sha; (I2) bead-only fallback with zero transitions lets a relabelled bead start at any step, and tasksGate counts bead-only "done"; (I3) Dolt 1213 not mapped to ErrStepConflict (+ uncaptured 62.9s flake).
Ruling R10 (refines R8): with zero store transitions an item is only accepted at Steps(kind)[0]; any other bead step fails with "no transition history for step <s>". tasksGate counts a child done only if its latest store transition To==done — Task 9 refinery must record Transition(id,"merge","done") (already in plan) — cost: bead edits can never advance an item, which is the intent.
Ruling R11: epics have no merge step, so Advance at epic/verification on a passing gate records Transition(verification→done) and SetStep(done); tasks/fixes still reach done only via the refinery — cost: one special case in Advance.
Ruling R12: any gate that reads an artifact's content (plan steps, report repro, epic ACs, breakdown) must first require the latest human approve of that artifact's current sha; otherwise fail with WaitingForHuman "approve <artifact>" — cost: edits after approval force re-approval, which is the intent.
Task 5: minor (deferred): late SetStep can write older step over newer (bead drift)
Task 5: minor (deferred): gateConfig rewrites explicit MaxFixFiles 0 as 5
Task 5: minor (deferred): AC regex: AC1.2 covers AC1; regex compiled per AC
Task 5: minor (deferred): planGate any-role reject blocks — add comment; Advance reads Transitions 3-4x
Task 5: note: CheckRun/TDDRun At set by caller — Task 6 Runner must set At from gtb clock, not caller input
Task 5: fix round 1 dispatched (resumed implementer a2ccb96c; FIX_BASE b37f96b1)
Task 5: fix round 1 implemented dd27c42a (race 20/20 + 15 separate; 62.9s flake inferred as testcontainers startup timeout, orphaned 'created' container, unproven)
Task 5: fix round 1/5 (3 addressed, 0 open; commits b37f96b1..dd27c42a)
Task 5: minor (deferred): unknown-kind / bead-only done give "no transition history" reason (still fail-closed)
Task 5: minor (deferred, Task 3 follow-up): Store.Transition doesn't check first-transition From == Steps(kind)[0]; only Advance protects it
Task 5: complete (commits 79db7eb9..dd27c42a, review clean)
Task 6: dispatched (BASE dd27c42a)
Task 6: implemented 7a3e3e68 (DONE; concerns: store writers can forge verified/human rows (DB access = trust boundary, accepted per D3); env+TTY guard bypassable by agent unsetting markers under a pty (accepted per D3 tbb-level); Runner has no timeout — Task 7 passes ctx w/ timeout; extras: ErrRefused, ErrChecksNotApproved, exit -1 on refusal, artifact name validation, ns log names)
Task 6: review a08031de — Needs fixes. Important: Check failing tail = cmd+"\n"+tail exceeds 1000 cap.
Ruling R13: in the same fix round also (a) validate itemID in workflow entry points (Red/Green/Check/HumanApprove/RecordVerdict/ArtifactPath callers) with the same safe-name rule family (^[A-Za-z0-9][A-Za-z0-9._-]*$, no "..") — closes traversal now instead of relying on Task 7; (b) Green picks the latest red by store order, not At — both cheap and security-adjacent — cost: small scope growth in the fix.
Task 6: minor (deferred): Green dir not recorded at red; evidence limits (arbitrary argv) — add doc comment on Red
Task 6: minor (deferred): no Dolt migration for test_files (no deployed DBs)
Task 6: minor (deferred): StdTerminal.Confirm new bufio.Reader per call; nil getenv panics; double log.Close; no metachar-literal test
Task 6: minor (deferred): refused rows carry Provenance verified
Ruling R14: gates additionally require Source=="human" for human decisions and Source=="gtb" for TDD/check evidence (defence in depth; matches what Red/Green/Check/HumanApprove write) — folded into Task 6 fix round — cost: gate tests must set Source.
Task 6: fix round 1 dispatched (resumed ae5f34db; FIX_BASE 7a3e3e68)
Task 6: fix round 1 implemented e7b3f646
Task 6: fix round 1/5 (4 addressed, 0 open; commits 7a3e3e68..e7b3f646)
Task 6: minor (deferred): plan-step names restricted to ID regex at Red/Green but executeGate accepts any name from plan.md — plan gate should validate step names (gives a clear reason)
Task 6: minor (deferred): ErrInvalidItemID message says "item id" for bad plan step
Task 6: minor (deferred): gates order by At while Green orders by store position
Task 6: complete (commits dd27c42a..e7b3f646, review clean)
Task 7: dispatched (BASE e7b3f646)
Task 7: implemented 989eed44 (DONE with gap: e2e smoke not run — dolt/bd not installed; check exits 3 when checks unapproved; exported workflow.RequireHuman + ValidateID)
Task 7: review a0350883 — Needs fixes. Important: checkout/branch from cwd; fallback to mayor/rig makes fix-file budget silently pass and red/green run in wrong tree. Spec gap: --json prompt text on stdout.
Ruling R15: git-dependent commands (advance on fix/fix, red, green, check) require cwd inside a git worktree under <townRoot>/<rig>/ that is not mayor/rig or refinery/rig; else exit 1 "run from the item's worktree (polecat or crew checkout); got <dir>". No silent fallback. The RepoDir and branch used are printed and included in JSON as "checkout":{"dir","branch"}. Binding a worktree to an item is deferred to Task 8 (sling knows the polecat worktree; it can record gtb:branch:<b> on the bead) — cost if wrong: Task 8 adds the label and these commands prefer it.
Ruling R16: `gtb item new` refuses to write the stub if ArtifactPath resolves inside any git worktree (git rev-parse from the stub dir) — covers adopted rigs whose root is a repo — cost: adopted repo-root rigs can't use gated items until moved.
Ruling R17: under --json, prompts go to stderr (StdTerminal{In: os.Stdin, Out: os.Stderr}); TTY check still needs stdin+stderr terminals — cost: none.
Task 7: minor (deferred): NewDoltStore DDL runs on read-only item show/list; DSN lacks Read/WriteTimeout; sql.DB never closed
Task 7: minor (deferred): effectiveStep duplicated in cmd (export from workflow); advance JSON reports bead step not effective step
Task 7: minor (deferred): item new orphan bead if stub write fails; error JSON lacks id
Task 7: minor (deferred): SaveRigSettings round-trip drops unmodelled keys (plan-mandated)
Task 7: minor (deferred): cmd.Annotations runtime mutation; wf.go 902 lines; rig inference from mayor/settings misleading
Task 7: minor (deferred): missing tests for non-TTY refusal w/o markers, ErrNotConfirmed, workflowCheckoutDir/pathWithin
Task 7: note for final review: check_commands_approved_sha is a bare sha an agent can compute and write into settings/config.json directly (D3 boundary)
Task 7: fix round 1 dispatched (resumed a1b99e18; FIX_BASE 989eed44)
Task 7: fix round 1 implemented 2aeb86be (R15/R16/R17). Concern: R16 refuses all items in towns that are git repos (gt install --git → InitGitForHarness makes town root a repo).
Ruling R18 (amends R16): the stub refusal applies only when the git toplevel is a rig-level repo (toplevel != townRoot, i.e. the toplevel is <townRoot>/<rig> or deeper). A town-root repo (HQ git) is allowed, and gtb adds `**/.gtb/` to HQGitignore so artifacts stay untracked there — cost: HQ gitignore gains one line; towns with a hand-made .gitignore could track .gtb/ (acceptable; user-owned).
Task 7: fix round 2 implemented 0196fea5 (R18)
Task 7: fix rounds 1-2/5 (3 addressed, 0 open; commits 989eed44..0196fea5)
Task 7: minor (deferred → Task 8 requirement R19): worktree check accepts any nested repo under rig (agent could git init a fresh dir to zero the fix budget)
Ruling R19: Task 8 binds worktree to item — sling records label gtb:branch:<branch> on the item bead; git-dependent commands require the cwd checkout's branch == that label when present, else refuse "checkout branch <b> is not item <id>'s branch <x>"; items with no branch label (never slung) refuse git-dependent commands at fix step — cost: manual crew work must set the label via sling/hook.
Task 7: minor (deferred): samePath exact-case compare may falsely refuse HQ repo on case-insensitive FS; nearestExisting can climb above town
Task 7: minor (deferred): existing HQ repos lack **/.gtb/ in .gitignore (only new git-init runs)
Task 7: complete (commits e7b3f646..0196fea5, review clean)
Task 8: dispatched (BASE 0196fea5)
Task 8: implemented 07b3d2b1 (DONE; concerns: batch sling passes itemID "" → gated items get mol-polecat-work; R19 branch only on fresh spawn — slinging to existing polecat → "has no worktree"; scheduled sling resolves formula at queue time (stale if item advances); gated pre-merge done pushes branch, skipping MR-path checks; package-level doneHoldGatedItem flag)
Task 8: review ac6b2d2b — Needs fixes. Important: (1) waiting never cleared; (2) no branch continuity across steps (plan gap); (3) done fails open on Items.Get errors; (4) DEFERRED hold doesn't self-clean (plan-mandated); (5) hold branch of updateAgentStateOnDone untested; (6) batch sling itemID "".
Ruling R20: one branch per gated item for its whole life — when slinging a gated item that has Item.Branch, pass ResumeBranch=Item.Branch (existing SlingSpawnOptions field); first sling creates it and R19 records it — cost: if a branch was deleted, sling must fail loudly ("item branch <b> missing") rather than silently creating a new one.
Ruling R21: waiting is a projection of the gate: done hold always calls SetWaiting(id, res.WaitingForHuman) (empty clears); Advance clears waiting after a successful transition; HumanApprove/approve CLI does not touch it — cost: none.
Ruling R22: gated hold exits as COMPLETED-equivalent for session purposes: formulas use `gtb done` (not DEFERRED) at a human wait; done's gated-hold path retires the polecat session and unassigns the item bead (assignee cleared, status open) so the next sling doesn't need --force — spec §5.3 "records waiting_for_human and exits" — cost: no session to resume; the next step always gets a fresh polecat on the same branch (R20).
Ruling R23: batch sling resolves per bead when any bead in the batch is gated (call resolveFormula per bead with its ID); if the batch machinery cooks one formula for all, refuse batches containing gated items with "gated items must be slung individually" — implementer picks whichever fits the code, documents it.
Ruling R24: done gated detection: only ErrItemNotFound / missing gtb:wf label means non-gated; any other Items.Get/env error is returned (fail closed).
Task 8: minor (deferred): doneHoldGatedItem package global; gated push skips contamination checks (refinery is backstop, add comment); scheduled formula stale at dispatch; formulas don't say ESCALATED on unfixable exit 3/4; Dolt env not closed; pre-existing Sling/Done test failures (TestRunDoneWithRoutedIssueIgnoresCurrentRigMirror, TestSlingRejectsBeadMissingFromTargetRigBeforeSpawn) + BuiltProperly ldflag needed on macOS; bindGatedItemBranch call sites untested
Task 8: fix round 1 dispatched (resumed aa619ffa; FIX_BASE 07b3d2b1)
Task 8: fix round 1 implemented fcd868c7 (R20-R24; per-bead batch resolution; concerns: ESCALATED/push-failed hold releases item but keeps session; ls-remote fallback latency)
Task 8: fix round 1/5 (6 addressed, 2 new Important: re-dispatch loop of waiting items via convoy stranded scan; ESCALATED/push-failed hold releases item while session kept; commits 07b3d2b1..fcd868c7)
Ruling R25: isReadyIssue (convoy.go:1700) returns false for any tracked issue carrying a gtb:waiting:<...> label (labels already on trackedIssueInfo) — waiting items are parked until a human acts; once a gate passes, done clears the wait (R21) and normal dispatch resumes — cost: a stale wait label would park an item; `gtb item show` surfaces it.
Ruling R26: releaseGatedItem runs only when shouldRetireGatedHold(exitType, pushFailed) is true; ESCALATED / push-failed holds keep the item hooked+assigned to the preserved session — cost: those items need witness/human recovery as today.
Task 8: minor (deferred): done fails closed on malformed settings for non-gated rigs (R24 consequence); gatedResumeBranch bd show per bead on gated rigs; Advance waiting-clear failure exits 1 after transition (self-heals); gatedBranchExists treats ls-remote error as missing (misleading msg); batch pre-cook uses first bead's hook dir (pre-existing)
Task 8: fix round 2 dispatched (resumed aa619ffa; FIX_BASE fcd868c7)
Task 8: fix round 2 implemented a4c1a03e (R25 incl. second dispatcher feedNextReadyIssue in internal/convoy/operations.go; R26)
Task 8: fix round 2/5 (3 addressed, 0 open; commits fcd868c7..a4c1a03e)
Task 8: minor (deferred, should fix before merge): mol-convoy-feed.formula.toml:73-76 dog prose rule doesn't skip gtb:waiting: items — a deacon dog could still sling a waiting gated item
Task 8: minor (deferred): feeder dependency-metadata fallback labels may be stale (fails open)
Task 8: complete (commits 0196fea5..a4c1a03e, review clean)
Task 9: dispatched (BASE a4c1a03e)
Task 9: implemented c7c8e9a4 (DONE_WITH_CONCERNS). Controller verified concern 1: production refinery = mol-refinery-patrol formula (agent runs git merge / gh pr merge then `gt mq post-merge`); Engineer.ProcessMRInfo/ProcessBatch have no production caller → workflowPreMerge never runs live. PLAN DEFECT.
Ruling R27: add a CLI chokepoint the patrol formula must pass through, reusing Task 9's code: (a) `gtb mq gate <rig> <mr-id> [--json]` runs workflowPreMerge (bump+push, PR find, CI) and exits 0 ready-to-merge / 3 pending (requeue) / 5 rejected-back-to-execute / 1 error; non-gated MR → exit 0 no-op. (b) `gt mq post-merge` for a gated source item records Transition(merge→done)+SetStep(done) before closing (refuses if item step != merge). (c) mol-refinery-patrol: in process-branch/merge-push, run `gt mq gate` before any merge (direct or PR) and skip merge unless exit 0. Defence: post-merge refuses a gated item whose gate never passed (no gate-pass marker → require latest CI-check evidence passed + bump commit at head) — cost: one more task-sized fix round on Task 9; formula prose is advisory to an agent, CLI checks are the real enforcement.
Task 9: first reviewer stopped by user; user said 'go ahead' — relaunching review
Pushed feat/gtb-reshape @ c7c8e9a4 to origin (user asked 'git push and commit'); main left untouched (origin/main has 50352b32 docker fix #1 not in local main)
Task 9: review aac0d142 — Needs fixes. Important: (1) processSingleMR passes a copy to doMerge; adopted bump CommitSHA not propagated → closeMRWithReason snapshot mismatch → item done but MR+item beads open, wedged forever; (2) malformed settings now fail-closes every MR on non-gated rigs (untested, unreported).
Ruling R28: keep fail-closed on unreadable/malformed rig settings (consistent with R24 in done; can't know if a rig is gated from a file we can't parse); error names the settings path and says merges are paused until it parses; add a test — cost: a broken settings file pauses merges on that rig.
Ruling R29 (folded in): AddCheck(failed CI) before Transition(merge→execute) so evidence can't be lost; if the bump commit is already on the branch and the target's version moved, refuse "rebase onto <target> (version moved)" instead of stacking a second bump; rune-safe CI tail truncation.
Task 9 fix round 1 also carries R27 (CLI chokepoint) — see above.
Task 9: minor (deferred): pending reuses NeedsApproval log text; refusals nudge MERGE_FAILED every poll; duplicate top-level version keys edit first; env cache never refreshes; workflowPreMerge test-only wrapper
Task 9: fix round 1 dispatched (resumed aa594a78; FIX_BASE c7c8e9a4; includes R27 CLI chokepoint, R28, R29)
Task 9: fix round 1 implemented cd004b43 (batch head propagation, R27 gt mq gate + post-merge check + patrol formula v17, R28, R29; concern: only post-merge enforces — an agent bypassing the formula can still git push/merge code; item won't reach done)
Task 9: fix round 1/5 (4 addressed, 0 open; commits c7c8e9a4..cd004b43)
Task 9: minor (deferred, MUST FIX before merge — weakens post-merge chokepoint): post-merge accepts a stale ci pass (no time bound vs latest transition into merge)
Task 9: minor (deferred, MUST FIX before merge): post-merge proves target contains H but not that the merged PR head == H (H2 pushed after gate could land)
Task 9: minor (deferred, MUST FIX before merge): patrol formula direct path lets gated items bypass PR — add "gated ⇒ pr path"
Task 9: minor (deferred): merged-pr-sweep lacks recovery for owner-merged gated PR; GateMR skips eligibility rechecks + no fetch; mq gate JSON omitempty fields
Task 9: idea for user (not in spec): gt mq gate posts commit status gtb/merge-gate; branch protection requires it → hard stop for gh pr merge bypass
Task 9: complete (commits a4c1a03e..cd004b43, review clean)
Task 10: dispatched (BASE cd004b43)
Ruling R30: dolt_remotes patrol is already opt-in (IsPatrolEnabled false unless Patrols.DoltRemotes.Enabled) — Task 10 does not add upstream_features.dolthub; it pins existing default-off with TestDaemon_DoltRemotesSkippedByDefault — cost: none (spec §9 'disabled by default' already holds).
Task 10: implemented d62737c2 (DONE; proxy-subcmds already hidden upstream; no release refs; connection registry has no command)
Task 10: minor (deferred): duplicate proxySubcmdsCmd.Hidden; clean doesn't remove stale gt-proxy-*; proxy docs still say make build produces them; dolt_remotes test relies on log prefix
Task 10: complete (commits cd004b43..d62737c2, review clean)
PHASE 1 COMPLETE (Tasks 1-10).
Task 11: dispatched (BASE d62737c2)
Task 11: implemented 0e542ecb (DONE; sqlite v1.52.0 pinned to avoid x/* upgrades; nothing sets delivered yet (Task 13); late result after expiry → done; FK error not ErrNotFound for missing machine; enroll_codes/web_sessions placeholder)
Ruling R31: a result arriving after expiry is recorded but the request stays `expired` (status not flipped to done) and the dashboard shows expired — matches Review Focus 3 — carried into Task 13 dispatch.
Task 11: review aadab570 — Needs fixes. Important: addResult flips expired→done (R31 breach); test asserts done after expires_at.
Ruling R32 (refines R31): a result is "late" if request status is expired OR its expires_at <= result time; late results are stored for audit but status = expired. RequestStatus derives expired on read when expires_at <= now and no on-time result. EnqueueRequest rejects zero/past ExpiresAt. — folded into fix round (cheap, same code path).
Task 11: minor (deferred): event ID not ULID-validated (cursor string compare); orphan artifact rows for missing items; DSN concat with ?/#; SetMaxOpenConns(1) deadlock hazard for Task 13 (note in dispatch); FK error vs ErrNotFound; index shape; schema_version newer-than-code check
Task 11: fix round 1 dispatched (resumed a9c57fec; FIX_BASE 0e542ecb)
Task 11: fix round 1 implemented 5989e403 (R32)
Task 11: fix round 1/5 (1 addressed, 0 open; commits 0e542ecb..5989e403)
Task 11: minor (deferred): expiry validation at ns precision vs ms storage (sub-ms edge); RequestStatus relies on addResult being sole results writer
Task 11: complete (commits d62737c2..5989e403, review clean)
Task 12: dispatched (BASE 5989e403)
Task 12: implemented 6630f8dc (DONE; consume inserts machine+token inline in its tx; --force writes machine.json before token (mismatch if 2nd write fails); limiter burst/RemoteAddr-only)
Task 12: minor (deferred, fix before merge): LoadToken Stat follows symlinks + TOCTOU — use O_NOFOLLOW + f.Stat; writeFileAtomic chmods shared .runtime to 0700 (other components) — keep token in a dedicated subdir
Task 12: minor (deferred): limiter burst overshoot + RemoteAddr-only (Task 15 proxy awareness); --force machine.json/token mismatch on partial failure; codes case-sensitive (uppercase+trim server-side); ::ffff:127.0.0.1 accepted; trailing JSON ignored
Task 12: complete (commits 5989e403..6630f8dc, review clean)
Task 13: dispatched (BASE 6630f8dc)
Task 13: implemented 39c75f5f (DONE; results {accepted,ignored}; schema v2 has_snapshot; notifier on Store; AppendEventsAfter atomic; out-of-order+stale after → 400 not 409; updated_at wall clock; no ExpireRequests sweep (Task 15 hourly); unknown paths 404 pre-auth)
Task 13: review a5e0e342 — Needs fixes. Important: long-poll hot-loops while any delivered request lacks a result (PendingRequests returns delivered too).
Ruling R33: fix both sides. Server: return immediately only if ≥1 request is `pending`; if all are `delivered`, wait (wake/timeout) then return the full set incl. re-deliveries. Contract for Task 14: the agent posts a Result for EVERY delivered request it receives, including dropped (expired/unsupported/dup) ones — carried into Task 14 dispatch — cost: none.
Task 13: minor (deferred): NewAgentAPI returns http.Handler so Notify needs type assertion (enqueue already notifies); recheck<=0 panics; wall-clock updated_at; results partial apply on store error (retry-safe, add comment); ExpireRequests sweep → Task 15
Task 13: fix round 1 dispatched (resumed a3b51c33; FIX_BASE 39c75f5f)
Task 13: fix round 1 implementer lost API connection before commit (uncommitted diff 3 files); fresh agent ae141cc1 finishing from working tree
Disk: user approved cleanup of unused (containers, images -a, builder -a, go cache); volumes kept; free 1.6G→110G
Task 13: fix round 1 implemented cbcc607d (R33 + recheck guard)
Task 13: fix round 1/5 (2 addressed, 0 open; commits 39c75f5f..cbcc607d)
Task 13: complete (commits 6630f8dc..cbcc607d, review clean)
Task 14: dispatched (BASE cbcc607d)
Task 14: implemented 6d6a47c6..c8ba7ddc (4 commits; DONE_WITH_CONCERNS: daemon/cmd TestMain hangs on Docker cred helper → ran with DOCKER_HOST=bogus both base/HEAD; item.cancel = unsling --force + session stop (vs polecat nuke?); sling/cancel only via function fields; CLI item.upsert lacks diffstat until next collect; 400/413 batch dropped + resync (evidence events lost))
Task 14: review a1b7d1ef — Needs fixes. Critical: raw GitURL (may contain creds) sent as Rig.Remote. Important: expiry checked before applied-set (applied+expired redelivery reported expired); backoff resets after hello success (collect/5xx failures retry ~1s forever); applied set stores full read-only results (peek/inspect) → unbounded memory/disk.
Ruling R34: Rig.Remote = util.RedactURL(GitURL) (strip userinfo; also drop query) — and the collector must never put any URL with userinfo anywhere in a snapshot/event; test.
Ruling R35: applied-set lookup before expiry check; backoff resets only after a full productive round (snapshot-if-needed + flush + poll all OK); applied set stores full results only for mutating types (approve, request_changes, item.create/sling/cancel); read-only types (snapshot, inspect, terminal.peek) store {ok,error} only (or aren't stored) — re-running them is harmless.
Ruling R36: item.cancel keeps unsling --force + session stop (non-destructive); polecat nuke rejected — dashboard cancel must not discard the worktree.
Task 14: minor (deferred): 400/413 drops whole batch (bisect to isolate); Ack trusts server cursor (verify == last sent); crash between snapshot re-append and ack duplicates evidence; agentlinkWG not awaited on Stop; GTB_AGENTLINK=0 logs on unjoined machine; double transition upsert; numstat every 30s per branched item (cache by head sha)
Task 14: fix round 1 dispatched (resumed a03c4b51; FIX_BASE c8ba7ddc)
Task 14: fix round 1 implemented ff0a19b5 (R34 RedactRemote + error-text URL redaction; R35a/b/c)
Task 14: fix round 1/5 (3 addressed, 1 open — R34 partial: evidence cmd/tail in views.go:110-117 and peek lines unredacted; commits c8ba7ddc..ff0a19b5)
Ruling R37: every free-text field leaving the machine (evidence cmd + tail, terminal.peek lines, any error text) passes RedactText; RedactText also masks bare secrets in common forms: "Authorization: Bearer <x>", "token=<x>"/"access_token=", "x-access-token:<x>@", GitHub tokens gh[pousr]_[A-Za-z0-9]{20,}, "password=<x>" — replaced with "<redacted>". Plus fold in two cheap minors from the re-review: skip Put for read-only types; mark productive only after handle() (results POST) succeeds.
Task 14: fix round 2 dispatched (resumed a03c4b51; FIX_BASE ff0a19b5)
Task 14: fix round 2 implemented e72b0dfa (R37 full redaction + bare-secret patterns; skip Put read-only; productive after results POST)
Task 14: fix round 2/5 (3 addressed, 1 new Important: tail/ExecGtb redaction runs after the byte cap so a key split at the cut leaks; commits ff0a19b5..e72b0dfa)
Ruling R38: outbound capped text is made safe by dropping the partial first line whenever text was truncated at its start (evidence tail, ExecGtb error text), then RedactText — avoids changing workflow's stored tails (evidence integrity) and works regardless of where the cut lands; also peek uses capture-pane -J. — cost: up to one line of context lost from capped tails.
Task 14: minor (deferred): JSON/YAML key styles + "Authorization: token" not masked; benign over-redaction of word:value@host
Task 14: fix round 3 dispatched (resumed a03c4b51; FIX_BASE e72b0dfa)
Task 14: fix round 3 implemented 2c72a8a6 (R38 SafeTail, redact-before-cap for ExecGtb/peek, CapturePaneJoined)
Task 14: fix round 3/5 (2 addressed, 1 new Important: multi-line check command makes checkTail split inside the command, leaking the cut output line; commits e72b0dfa..2c72a8a6)
Ruling R39: workflow capTail flattens newlines in the command prefix (strings.ReplaceAll(cmd, "\n", " ⏎ ") or similar single-line form) so the prefix is always exactly one line; checkTail's first-newline split is then correct. CmdSetSHA is computed from config, not the tail, so gates are unaffected. — cost: stored failed-check tails show multi-line commands on one line.
Task 14: round 4 uses a fresh implementer per process; no more capable model is available through this router (R4), so same model.
Task 14: minor (deferred): tailTruncated heuristic drops first CI name for >=997-byte refinery CI tails (display only)
Task 14: fix round 4 dispatched (fresh implementer ad069b46; FIX_BASE 2c72a8a6)
Task 14: fix round 4 implemented 93bc2aef (R39 capTail flattens command newlines)
Task 14: fix round 4/5 (1 addressed, 0 open; commits 2c72a8a6..93bc2aef)
Task 14: minor (deferred): pre-R39 stored check tails still contain raw \n (only matters if sent); CLI shows ⏎ in failed multi-line command
Task 14: complete (commits cbcc607d..93bc2aef, review clean after 4 rounds)
PHASE 2 COMPLETE (Tasks 11-14).
Ruling R40 (maintainer, 2026-10-23): no GitHub OAuth; dashboard login = admin token printed to pod log on first start (or GTB_ADMIN_TOKEN from Secret); session cookie + CSRF; rotate via gtb server admin-token --rotate. Spec §8 updated; plan Tasks 15/17/19 amended in dispatches.
Task 15: dispatched (BASE 550d07c6, admin-token auth per R40)
STOPPED by user during Task 15 (implementer a4e401f9 killed). Resume point: Task 15 from BASE 550d07c6 with R40.
RESUMED by user: Task 15 re-dispatched (BASE 550d07c6)
Task 15: implemented 1e0886cb (DONE_WITH_CONCERNS: limiter per RemoteAddr — behind host nginx → NodePort (externalTrafficPolicy Cluster, no ingress controller) all clients share one IP → 10 bad logins lock everyone out 15m; in-memory limiter/SSE ⇒ single replica; board.changed on any event incl heartbeat; admin created_at wall clock)
Ruling R41: add --trusted-proxy CIDR flag (repeatable; env GTB_TRUSTED_PROXIES, default empty). When the immediate peer is in a trusted CIDR, the client IP is the right-most X-Forwarded-For entry not itself trusted; else RemoteAddr. Applies to login and enroll limiters. k3s deploy (Task 17) sets the pod/cluster + node CIDRs and nginx must send X-Forwarded-For. Also: a successful login resets that IP's failure count. — folded into Task 15 fix round with review findings.
Task 15: review afcfbcee — Approved (no Critical/Important). R41 (controller ruling) still to apply → fix round 1 carries R41 + cheap minors: Bearer match case/whitespace-insensitive (refuse any Authorization header on approve/request-changes); limiter reserves attempt atomically in allow; warn when --public-url not https and listen not loopback.
Task 15: minor (deferred): terminal GET enqueues w/o throttle; machine error string as error code; RevokeMachine wall clock + no board notify; re-login leaves old session; duplicate token-length check; shutdown cancels in-flight (BaseContext); machines N+1 rigs query; parent not existence-checked
Task 15: fix round 1 dispatched (resumed aa2b6bf9; FIX_BASE 1e0886cb; R41 + 3 minors)
Task 15: fix round 1 implemented 24111a85 (R41 + 3 minors); Task 17 must pass --trusted-proxy + nginx X-Forwarded-For
Task 15: fix round 1/5 (4 addressed, 1 new Important: a client-injected malformed XFF entry makes clientIP fall back to the shared proxy bucket — caused by R41 wording; commits 1e0886cb..24111a85)
Ruling R42 (amends R41): walk XFF right→left parsing lazily; strip an optional :port and [] brackets and unmap 4-in-6 before parsing; skip trusted entries; return the first untrusted parseable entry; only a malformed entry reached BEFORE any untrusted one falls back to RemoteAddr (entries left of the first untrusted are client-controlled and ignored). Normalize peer path the same way (unmap) and Unmap()/Masked prefixes for 4-in-6 CIDRs. — cost: none.
Task 15: fix round 2 dispatched (resumed aa2b6bf9; FIX_BASE 24111a85; R42)
Task 15: fix round 2 implemented 08f010b1 (R42)
Task 15: fix round 2/5 (1 addressed, 0 open; commits 24111a85..08f010b1)
Task 15: minor (deferred): zoned IPv6 accepted in ParseAddrPort branch; unbalanced brackets accepted (both harmless)
Task 17 requirement (from Task 15 review): nginx must `proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for` (set, not add; no real_ip rewrite in front); every hop (host nginx + node/cluster CIDR for NodePort SNAT) listed in --trusted-proxy; document both.
Task 15: complete (commits 550d07c6..08f010b1, review clean)
Task 16: dispatched (BASE 08f010b1)
Task 16: implemented e54f84dd, 050f9079 (DONE; jsdom AbortSignal shim in setup.ts; static Go test uses empty FS; stale dist after make web then go build; 400kB single bundle; Coordination from board data; not browser-tested)
Task 16: review a2514526 — Needs fixes. Important: (1) live board.changed refetch swaps artifact content+sha under the reader → approve posts a sha never read, stale_sha unreachable; (2) freshness/stale frozen between events (heartbeats don't notify).
Ruling R43: pin the artifact (content+sha) the user opened; on refetch with a different sha keep showing the pinned version, show "A newer version is on the machine — load it" banner, and Approve posts the PINNED sha (server/machine then answers stale_sha → existing message). Loading the new version re-pins.
Ruling R44: client-side freshness: compute age from fetch time + freshness_seconds with a 5 s tick; stale when > 120 s; also revalidate the board every 60 s as a backstop.
Folded cheap minors: map machine error "expired" to the expired message; TerminalView per-effect AbortController (no shared alive/inflight refs) + abort on unmount; root errorElement with a plain "Not found" page.
Task 16: minor (deferred): request_not_found message wording; visibility test restore in afterEach; CI doesn't run web tests/tsc (Task 18); toast timers not cleared; make clean should clear dist
Task 16: fix round 1 dispatched (resumed ac2f8604; FIX_BASE 050f9079)
Task 16: fix round 1 implemented 8a98e58b (R43 pin, R44 client freshness, 3 minors; Markdown memoized)
Task 16: fix round 1/5 (R43 + 3 minors addressed; R44 partial: Item/Machines pages age without revalidating → healthy idle machines falsely stale; commits 050f9079..8a98e58b)
Ruling R45: move the 60 s backstop into useApi as an opt-in `refreshInterval` used by Board, Item and Machines; pause it while document.hidden; reset it after any SSE-driven revalidate. Also fold: a pinned artifact missing from the latest data shows "<name> is no longer on the machine" and disables Approve/Request changes.
Task 16: minor (deferred): Load it during pending approval drops outcome; staleAfterSeconds duplicates server const; errorElement shows Not found for crashes
Task 16: fix round 2 dispatched (resumed ac2f8604; FIX_BASE 8a98e58b; R45)
Task 16: fix round 2 implemented fdd6b4df (R45)
Task 16: fix round 2/5 (2 addressed, 0 open; commits 8a98e58b..fdd6b4df)
Task 16: minor (deferred): hideable() restore not in afterEach (test hygiene)
Task 16: complete (commits 08f010b1..fdd6b4df, review clean)
PHASE 3 COMPLETE (Tasks 15-16).
Task 17: dispatched (BASE fdd6b4df)
Pre-Task 17 check: live gtb namespace = old Gas Town town (image gtb:50352b32, gtb-0 + gtb-dolt-0, PVs gtb-data /var/lib/gtb, gtb-dolt-data /var/lib/gtb/dolt) with "No rigs configured" — nothing to migrate. origin/main has 50352b32 (Dockerfile Go fix, PR #1) not on feat branch.
Ruling R46: Task 17 rewrites the manifest for the server but does not delete old data: new PV gtb-server-data at /var/lib/gtb/server (new storageClass gtb-server-local), StatefulSet renamed gtb-server; old gtb/gtb-dolt objects removed from the manifest only; the deploy script prints exact `kubectl delete` commands for the old StatefulSets/PVCs and leaves running them to the maintainer (destructive, outside worktree). Service gtb keeps NodePort 30881 but selects app=gtb-server. --trusted-proxy set to node IP 192.168.2.3/32 + pod CIDR 10.42.0.0/16 + 127.0.0.1/32; script prints the nginx `proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;` requirement. Admin token: script creates Secret gtb-server with optional GTB_ADMIN_TOKEN only if user passes one; otherwise the pod prints it and script shows `kubectl -n gtb logs gtb-server-0 | sed -n '/admin token/,/====/p'`. Building/applying/pushing images is NOT done in this task (verify with dry-run and local docker build only).
Task 17: also merge origin/main 50352b32 into the feature branch first if it touches Dockerfile build (Go install fix needed by image build).
Task 17: cherry-picked 50352b32 as 35a055a0; deploy files written (uncommitted); amd64 image build under emulation running >30 min
Task 17: implemented 3c2cb6ed (+ cherry-pick 35a055a0). DONE_WITH_CONCERNS: bun 1.4.2 in image (1.3.14 can't read lockfile written by 1.4.2); nginx X-Forwarded-For unverified (script prints MANUAL); token frame only in first container's log; local Docker cred-helper hang worked around with temp config.
Task 17: review a9b1d3d9 — Approved (server-side dry-run against live cluster accepted; no deletes; token via stdin; PV isolated by subPath mounts). Controller pulls 3 deploy-day minors into one fix round: rollback hint on failed rollout (print svc selector patch); rotate hint must detect an existing Secret token (tell user to update the Secret, else rotation is reverted on restart); align dockerignore with root (.beads-credential-key, settings/, CLAUDE.local.md, .dolt*, .runtime, .events.jsonl*).
Task 17: minor (deferred): -trimpath vs make build flags; bun pinned by tag not digest; report wrongly says server/ visible to old pod
Task 17: fix round 1 dispatched (resumed a6b00f16; FIX_BASE 3c2cb6ed)
Task 17: fix round 1 implemented 8be08c64 (rollback hint, Secret-aware token hint, dockerignore aligned; image rebuilt + smoke OK)
User request: switch remaining tasks to haiku. Test: Task 17 re-review re-dispatched on model=haiku (a031a55a) after user stopped the previous re-review. Earlier R4: sonnet returned model_not_found via router.
Haiku test FAILED: model_not_found (claude-haiku-5-5, HTTP 404) via router. Only the session model works for subagents. Awaiting user decision.
Pushed through 8be08c64 before user switches machines; Task 17 re-review still pending.
