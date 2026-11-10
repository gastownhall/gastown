# gtb Reshape Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the Gas Town fork into gtb.
- Every gated work item follows a code-enforced step machine modelled on tbb.
- A `gtb` binary for Linux and macOS runs agents locally and reports outbound to `gtb.nhaboc.com`.
- The server serves a Fabro-style board where the maintainer approves work.

**Architecture:** There are three new Go packages.
- `internal/workflow`: steps, gates, evidence and artifacts. It is the source of truth on each machine, stored in the rig's Beads issue plus new Dolt tables.
- `internal/agentlink`: the machine's outbound client for `/agent/v1/`.
- `internal/server`: `gtb server`, with a SQLite projection store, the agent API, the browser API and the embedded React UI.

Existing roles (polecats, refinery, witness, Mayor) are wired into the workflow through step formulas, `gtb done`, and a new refinery merge gate. The server never runs agents.

**Tech Stack:**
- Go 1.26 (existing module) and cobra, using `internal/beads`, `internal/doltserver`, `internal/refinery`, `internal/config`, `internal/git`, and `go-sql-driver/mysql` for Dolt.
- `modernc.org/sqlite`, which is pure Go, for the server store.
- `golang.org/x/oauth2` for GitHub login.
- React 19, TypeScript and Vite built with Bun, under `web/`, embedded with `embed`.
- goreleaser.

**Spec:** `docs/superpowers/specs/2026-10-22-gtb-reshape-design.md` (approved 2026-10-22). Read it before every task.

## Global Constraints

**Platform and build**
- Platforms: linux/amd64, linux/arm64, darwin/amd64, darwin/arm64. Windows and FreeBSD are not built (§3, §6).
- The command is `gtb`. `gt` remains as a symlink that `make install` creates (§6; alias kept, builder's call).
- The Go module path stays `github.com/steveyegge/gastown`, to avoid rewriting 2,000 import lines. Only binary names and user-facing text change.

**Repos and state**
- gtb never calls the `tbb` CLI. Gates are Go code in `internal/workflow` (D1).
- `gtb rig add` refuses a repo whose checkout contains `.tbb/` (§3).
- Artifacts live at `<townRoot>/<rig>/.gtb/items/<id>/<artifact>.md` and are never written inside the rig's git checkout (§5.2).
- An artifact's approval hash is the sha256 of the file with its `status:` frontmatter line removed. Editing the file voids the approval (§5.2).
- Evidence records carry `source` ∈ {`gtb`, `agent`, `human`} and `provenance` ∈ {`verified`, `recorded`, `reported`}. Output tails are capped at 1000 characters (§5.2).
- Budgets default to `max_fix_files` 5, `max_fix_rounds` 3 and `max_plan_loops` 3 (tbb defaults, §5.4).

**Approvals**
- No agent can approve. `gtb approve` requires an interactive TTY on stdin and stdout, and refuses if any of these is non-empty: `GT_ROLE`, `GT_POLECAT`, `BD_ACTOR`, `CLAUDECODE`, `CLAUDE_CODE_ENTRYPOINT`, `CODEX_SANDBOX` (D3, §7.4).
- Version bump: the review artifact carries `bump: patch|minor|major`. The refinery applies it to every `workflow.version_files` entry, all set to the same value, after rebasing, and merges via PR once CI is green. A CI failure sends the item back to `execute` (D4).

**Server link**
- Machines connect outbound only, over HTTPS to `/agent/v1/`. Plain `http://` is allowed only for `127.0.0.1`, `localhost` and `::1` (§7.1).
- Long-polls last at most 30 s and event batches hold at most 500 events. A `409` with body `{"error":"resync"}` triggers a new snapshot (§7.1).
- Requests from the server form a closed set: `approve`, `request_changes`, `item.create`, `item.sling`, `item.cancel`, `snapshot`, `inspect`, `terminal.peek`. There are no shell strings and no paths (§7.3).
- Requests are idempotent by `key`, and a request past `expires_at` is never run (§7.3).
- Every server request comes from the maintainer's browser session. `approve` and `request_changes` are refused for API tokens. Machines never send requests to other machines (D2, §7.3).
- The machine token lives at `<townRoot>/.runtime/agentlink/machine-token`, mode 0600, and is never logged (§6).

**Server data and auth**
- Server retention: events and evidence summaries are kept 90 days. Artifacts and item projections are kept until the item is deleted (§7.2).
- GitHub OAuth login is restricted to the login `baovo15`, set by `GTB_ALLOWED_LOGIN` with that as the default (§8).

**Deploy**
- Server images are pushed to `127.0.0.1:30082/gtb:<sha>` (Nexus) (§10).
- The k3s namespace is `gtb` and the NodePort is `30881` (existing deploy).

## Review Focus

These are the likely failure modes that no task's main tests cover. Each one has a pinning test added to the task named in brackets.

1. **An artifact edited after approval** (whitespace, frontmatter reordering, a CRLF save from an editor) must void the approval. A `status:` change alone must not void it. → Task 2, `TestArtifactHash_*`.
2. **An approval arriving from the dashboard for an artifact the agent has since changed** (the sha doesn't match on the machine) must be refused and reported back as `stale_sha`. It must not be applied to the new text. → Task 14, `TestApplyApprove_StaleSHA`.
3. **The machine is offline while the maintainer approves, then reconnects after the request's `expires_at`:** the request must not run, the item must stay in **Needs you**, and the dashboard must show the request as expired. → Tasks 11 and 14, `TestRequests_ExpiredNotDelivered`, `TestAgentLoop_DropsExpired`.
4. **A gate is called twice concurrently** (two `gtb advance` processes, or a polecat and a human): only one step transition may be recorded. → Task 3, `TestAdvance_ConcurrentSingleTransition`.
5. **`gtb approve` from inside a polecat's tmux pane**, which is an interactive TTY with `GT_ROLE` set, must be refused even though stdin is a terminal. → Task 6, `TestApproveGuard_TTYButAgentEnv`.

---

## File Structure

**New packages**
- `internal/workflow/`
  - `types.go`: `Kind`, `Step`, `Item`, `Evidence` records.
  - `machine.go`: step tables per kind.
  - `gates.go`: gate functions.
  - `artifact.go`: path, read, hash.
  - `store.go`: Dolt evidence tables.
  - `item.go`: Beads-backed item state.
  - `advance.go`: transitions.
  - `tdd.go`, `check.go`: verified command runs.
  - `approve.go`: the approval guard.
  - `config.go`: rig workflow config.
  - `bump.go`: version files.
  - `*_test.go` next to each file.
- `internal/agentlink/`
  - `protocol.go`: wire types shared with the server.
  - `client.go`: HTTP client.
  - `queue.go`: local outbound event queue.
  - `loop.go`: session loop.
  - `apply.go`: request handlers.
  - `token.go`.
- `internal/server/`
  - `store.go`: SQLite schema and queries.
  - `agentapi.go`: `/agent/v1/`.
  - `enroll.go`.
  - `webapi.go`: `/api/v1/` for the UI, plus SSE.
  - `auth.go`: GitHub OAuth and sessions.
  - `retention.go`.
  - `static.go`: embed `web/dist`.
  - `server.go`: wiring.

**New commands**
- `internal/cmd/wf.go`: `gtb item new|show|list`, `gtb advance`, `gtb approve`, `gtb verdict`, `gtb red`, `gtb green`, `gtb check`.
- `internal/cmd/join.go`.
- `internal/cmd/server.go`.

**Modified code**
- `internal/cmd/rig.go`: the `.tbb/` refusal.
- `internal/cmd/sling_schedule.go`: step formula resolution.
- `internal/cmd/done.go`: workflow-aware completion.
- `internal/refinery/engineer.go` plus a new `internal/refinery/workflow_gate.go`.
- `internal/config/types.go`: a `Gated` sub-block in `WorkflowConfig`.
- `internal/daemon/`: starts the agentlink loop.

**New formulas**
- `internal/formula/formulas/gtb-<kind>-<step>.formula.toml`

**Web UI**
- `web/` (React app): `src/pages/{Board,Item,Machines,Coordination,Settings}.tsx`, `src/api.ts`.

**Build and deploy**
- `Makefile`, `.goreleaser.yml`, `.github/workflows/*`
- `deploy/k3s/gtb.yaml`, `scripts/gtb-deploy.sh`, and a new `deploy/Dockerfile.server`.

---

## Phase 1: Workflow core (one machine, no server)

### Task 1: Rename the binary to `gtb` and limit release targets

**Files:**
- Modify: `Makefile` (build/install targets), `.goreleaser.yml`, `internal/cmd/root.go:26` (`Use`), `.github/workflows/ci.yml`, `.github/workflows/release.yml`
- Move: `cmd/gt/` → `cmd/gtb/`
- Delete: `.github/workflows/windows-ci.yml`
- Test: `internal/cmd/root_test.go` (new test)

**Interfaces:**
- Produces: the binary `gtb`. `make install` puts `gtb` in `~/.local/bin` plus a `gt -> gtb` symlink. `rootCmd.Use` comes from `GT_COMMAND`, defaulting to `"gtb"`.

- [ ] **Step 1: Write the failing test.** `TestRootCommandName_DefaultsToGtb`: with `GT_COMMAND` unset, after `init()`, `rootCmd.Use == "gtb"`. With `GT_COMMAND=gt`, it is `"gt"`.
- [ ] **Step 2: Run it and confirm it fails.** Run `go test ./internal/cmd -run TestRootCommandName -v`. Expected: FAIL (`Use` is `"gt"`).
- [ ] **Step 3: Change the default name to `"gtb"`** in `root.go`. Move `cmd/gt` to `cmd/gtb`. In the Makefile, build `gtb`, and have `install` create the `gt` symlink.
  - In goreleaser, set `builds[].binary: gtb`, `goos: [linux, darwin]`, `goarch: [amd64, arm64]`, and drop the windows and freebsd ignores.
  - In CI, drop the Windows job and keep the linux and macOS jobs.
- [ ] **Step 4: Verify.** Run `go test ./internal/cmd -run TestRootCommandName -v`, which should PASS. `make build && ./gtb version` should print a version. `goreleaser check` should report no errors.
- [ ] **Step 5: Commit.** `chore: rename binary to gtb, release linux+darwin only`

### Task 2: Workflow types, step tables and artifact hashing

**Files:**
- Create: `internal/workflow/types.go`, `internal/workflow/machine.go`, `internal/workflow/artifact.go`
- Test: `internal/workflow/machine_test.go`, `internal/workflow/artifact_test.go`

**Interfaces:**
- Produces:
  - `type Kind string`, with `KindTask = "task"`, `KindFix = "fix"`, `KindEpic = "epic"`.
  - `type Step string`.
  - `func Steps(k Kind) []Step`, returning:
    - task: `spec, plan, execute, verify, review, merge`
    - fix: `report, reproduce, rca, fix, verify, merge`
    - epic: `epic, breakdown, tasks, verification`
  - `func Next(k Kind, s Step) (Step, bool)`: `false` at the last step.
  - `func ArtifactFor(k Kind, s Step) string`, mapping each step to its artifact name:
    - task: spec→`spec`, plan→`plan`, review→`review`
    - fix: report→`report`, rca→`rca`
    - epic: epic→`epic`, breakdown→`breakdown`, verification→`verification`
    - every other step: `""`
  - `func ArtifactPath(townRoot, rig, itemID, name string) string`, returning `<townRoot>/<rig>/.gtb/items/<itemID>/<name>.md`.
  - `func ArtifactHash(content []byte) string`: lowercase hex sha256, computed after normalising CRLF to LF and removing the first frontmatter line that matches `^status:\s*.*$`.
  - `func ReadArtifact(path string) (content []byte, sha string, fm map[string]string, err error)`: `fm` holds frontmatter `key: value` pairs between the leading `---` lines.

- [ ] **Step 1: Write the failing tests.**
  - `TestSteps_Task`: equals the six task steps in order.
  - `TestNext_LastStep`: `Next(KindTask,"merge")` returns `("", false)`.
  - `TestArtifactPath`: `ArtifactPath("/t","r","gt-1","spec") == "/t/r/.gtb/items/gt-1/spec.md"`.
  - `TestArtifactHash_StatusLineIgnored`: two files that differ only in `status: draft` vs `status: approved` hash the same.
  - `TestArtifactHash_BodyEditChanges`: a one-character body edit gives a different hash.
  - `TestArtifactHash_CRLFEqualsLF`.
  - `TestArtifactHash_TrailingSpaceChanges`: a trailing-space edit changes the hash (Review Focus 1).
  - `TestReadArtifact_Frontmatter`: reads `bump: minor` into `fm["bump"]`.
- [ ] **Step 2: Run them and confirm they fail.** Run `go test ./internal/workflow -v`. Expected: compile failure (package missing).
- [ ] **Step 3: Implement** the types and functions above.
- [ ] **Step 4: Verify.** Run `go test ./internal/workflow -v`. Expected: PASS.
- [ ] **Step 5: Commit.** `feat(workflow): step tables and artifact hashing`

### Task 3: Evidence store (Dolt) and item state (Beads)

**Files:**
- Create: `internal/workflow/store.go`, `internal/workflow/item.go`
- Test: `internal/workflow/store_test.go` (build tag `integration`, uses `testutil.RequireDoltContainer`), `internal/workflow/item_test.go`

**Interfaces:**
- Consumes: `beads.New(workDir)`, `(*Beads).Show`, `(*Beads).Update`, `(*Beads).Create` (`internal/beads/beads.go:570,1556,1708,1965`), `doltserver.DefaultConfig(townRoot)`, and `beads.DatabaseNameFromMetadata(beadsDir)`.
- Produces:
  - Records:
    - `type Approval struct{ItemID, Artifact, SHA, Verdict, Role, Source, Channel, Actor string; At time.Time}`. `Verdict` ∈ {`approve`, `concerns`, `reject`, `request_changes`}. `Role` is `"human"` for approvals, or a reviewer role. `Channel` ∈ {`terminal`, `dashboard`}.
    - `type TDDRun struct{ItemID, PlanStep, Phase, Cmd string; Exit int; TestFilesSHA, Tail, Source, Provenance string; At time.Time}`. `Phase` ∈ {`red`, `green`, `refused`}.
    - `type CheckRun struct{ItemID, CmdSetSHA string; Passed bool; Tail, Source, Provenance string; At time.Time}`.
    - `type Transition struct{ItemID string; From, To Step; Actor string; At time.Time}`.
  - `type Store interface{ AddApproval(Approval) error; Approvals(itemID string) ([]Approval, error); AddTDD(TDDRun) error; TDD(itemID string) ([]TDDRun, error); AddCheck(CheckRun) error; Checks(itemID string) ([]CheckRun, error); Transition(itemID string, from, to Step, actor string) error; Transitions(itemID string) ([]Transition, error) }`
  - `func NewDoltStore(db *sql.DB) (Store, error)` creates the tables `gtb_approvals`, `gtb_tdd`, `gtb_checks` and `gtb_transitions` if they are missing. All are append-only, with auto-increment `id` and `at DATETIME(6)`.
  - `func NewMemStore() Store` for unit tests.
  - `Store.Transition` must fail with `ErrStepConflict` when the item's latest transition `To` is not `from` (Review Focus 4).
    - Dolt: insert inside a transaction that selects the latest row `FOR UPDATE`.
    - MemStore: a mutex.
  - Item state:
    - `type Item struct{ID, Rig, Title, Parent string; Kind Kind; Step Step; StepEnteredAt time.Time; WaitingForHuman string}`
    - `type Items interface{ Get(id string) (*Item, error); Create(rig string, k Kind, title, parent string) (*Item, error); SetStep(id string, s Step, at time.Time) error; SetWaiting(id, reason string) error; List(rig string) ([]*Item, error) }`
    - `func NewBeadsItems(b *beads.Beads) Items` stores `kind`, `step`, `step_entered_at` and `waiting_for_human` as `gtb:` labels:
      - `gtb:wf:<kind>`
      - `gtb:step:<step>`, replaced on change
      - `gtb:entered:<RFC3339>`
      - `gtb:waiting:<reason>`, removed when empty
    - `func NewMemItems() Items`.

- [ ] **Step 1: Write the failing tests.**
  - `TestMemStore_TransitionConflict`: the first `Transition(id,"spec","plan")` succeeds. A second `Transition(id,"spec","plan")` returns `ErrStepConflict`.
  - `TestAdvance_ConcurrentSingleTransition` (in `store_test.go`, integration): 10 goroutines call `Transition(id,"spec","plan")` against Dolt. Exactly 1 succeeds, and `Transitions(id)` has length 1.
  - `TestBeadsItems_StepLabelReplaced`: uses a fake `bd` on PATH, as `internal/beads` tests already do. After `SetStep(id,"plan")`, the labels contain `gtb:step:plan` and no `gtb:step:spec`.
- [ ] **Step 2: Confirm they fail.** Run `go test ./internal/workflow -run 'Store|Items' -v` and `go test -tags integration ./internal/workflow -run Concurrent -v`. Expected: FAIL (undefined).
- [ ] **Step 3: Implement** `store.go` and `item.go` as specified.
- [ ] **Step 4: Verify.** Both commands should PASS. The integration run needs Docker.
- [ ] **Step 5: Commit.** `feat(workflow): dolt evidence store and beads-backed item state`

### Task 4: Rig workflow config and the `.tbb/` refusal

**Files:**
- Modify: `internal/config/types.go:663-667` (`WorkflowConfig`), `internal/cmd/rig.go` (`runRigAdd`, after clone and before registration; `runRigAdopt`)
- Create: `internal/workflow/config.go`
- Test: `internal/workflow/config_test.go`, `internal/cmd/rig_tbb_test.go`

**Interfaces:**
- Produces:
  - `WorkflowConfig` gains `Gated *GatedWorkflow` with JSON key `gated`:
    - `type GatedWorkflow struct{Enabled bool; CheckCommands []string; CheckCommandsApprovedSHA string; VersionFiles []string; MaxFixFiles, MaxFixRounds, MaxPlanLoops int}`
    - JSON keys are `enabled`, `check_commands`, `check_commands_approved_sha`, `version_files`, `max_fix_files`, `max_fix_rounds` and `max_plan_loops`.
  - `func workflow.LoadConfig(rigPath string) (*config.GatedWorkflow, error)` fills in defaults of 5, 3 and 3 for zero budgets.
  - `func workflow.CheckCommandsSHA(cmds []string) string` returns the sha256 of the commands joined with `\n`.
  - `func workflow.ChecksApproved(c *config.GatedWorkflow) bool` returns `CheckCommandsApprovedSHA == CheckCommandsSHA(CheckCommands)`.
  - `func rejectTbbRepo(checkoutDir string) error` returns an error containing `uses tbb (.tbb/ found); gtb does not manage tbb repos` when `<checkoutDir>/.tbb` exists.

- [ ] **Step 1: Write the failing tests.**
  - `TestLoadConfig_DefaultBudgets`: an empty `gated:{enabled:true}` gives `5/3/3`.
  - `TestChecksApproved_EditVoids`: after appending a command to `CheckCommands`, `ChecksApproved` returns false.
  - `TestRejectTbbRepo`: a temp directory with `.tbb/` gives an error matching the message above. Without `.tbb/`, it returns nil.
- [ ] **Step 2: Confirm they fail.** Run `go test ./internal/workflow ./internal/cmd -run 'LoadConfig|ChecksApproved|RejectTbb' -v`. Expected: FAIL.
- [ ] **Step 3: Implement.** Call `rejectTbbRepo` in `runRigAdd` on the fresh clone. On error, remove the clone and the partial rig directory. Also call it in `runRigAdopt` on the adopted directory.
- [ ] **Step 4: Verify.** The same command should PASS.
- [ ] **Step 5: Commit.** `feat(workflow): rig gated-workflow config; refuse tbb repos`

### Task 5: Gates and `Advance`

**Files:**
- Create: `internal/workflow/gates.go`, `internal/workflow/advance.go`
- Test: `internal/workflow/gates_test.go`, `internal/workflow/advance_test.go`

**Interfaces:**
- Consumes: Task 2, Task 3 (`Store`, `Items`), and Task 4 (`GatedWorkflow`).
- Produces:
  - `type GateEnv struct{TownRoot, Rig string; Store Store; Items Items; Config *config.GatedWorkflow; Git ChangedFiles}`
  - `type ChangedFiles interface{ Changed(itemID string) ([]string, error) }`. The production implementation uses `git.DiffNameOnly(base, head)` on the item's polecat branch (`internal/git/git.go:1179`).
  - `type GateResult struct{Passed bool; Reasons []string; WaitingForHuman string}`
  - `func Gate(env GateEnv, it *Item) GateResult`
  - `func Advance(env GateEnv, itemID, actor string) (*Item, GateResult, error)`:
    - It runs `Gate`. When the gate passes, it calls `Store.Transition(id, cur, next, actor)` and `Items.SetStep`.
    - When the gate fails, it returns the result unchanged with no error.
    - `ErrStepConflict` is passed back to the caller.
- Gate rules, one test per row:

| Kind / step | Passes when |
|---|---|
| any step with an artifact (`ArtifactFor != ""`) | the latest `Approval` for that artifact has `Verdict=="approve"`, `Role=="human"`, and `SHA` equal to the current file's `ArtifactHash`. Otherwise `WaitingForHuman="approve <artifact>"` |
| task/plan | also no `Approval` with `Verdict=="reject"` on the current sha, and fewer than `MaxPlanLoops` plan transitions back into `plan` |
| task/execute | every plan step listed in `plan.md` frontmatter `steps: a,b,c` has a `TDDRun` `red` followed later by a `green`, both `Provenance=="verified"` |
| task/verify, fix/verify | `ChecksApproved(cfg)`, and a `CheckRun{Passed:true}` exists with `At >= StepEnteredAt` and `CmdSetSHA == CheckCommandsSHA(cfg.CheckCommands)`. If checks are not approved, `WaitingForHuman="approve check commands"` |
| fix/reproduce | a `TDDRun{PlanStep:"repro",Phase:"red",Provenance:"verified"}`, or `report.md` frontmatter `repro: manual` |
| fix/fix | a `green` for `repro` after its `red`, and `len(Changed) <= MaxFixFiles` |
| task/review | the review artifact is approved, and its frontmatter `bump` ∈ {patch, minor, major}. Otherwise the reason is `review.md must set bump: patch|minor|major` |
| epic/breakdown | approved. Every `AC<n>` in `epic.md` appears in `breakdown.md`. The `depends:` lines in `breakdown.md` have no cycle |
| epic/tasks | every child item (`Items.List` filtered by `Parent`) is at step `merge` with a `merge` transition recorded as done |
| */merge | never passes in `Advance`. It is moved only by the refinery (Task 9), which returns the reason `merge is performed by the refinery` |

- [ ] **Step 1: Write the failing tests.** In `gates_test.go`, write one table test per row above using `NewMemStore`, `NewMemItems`, a temp `TownRoot`, and a fake `ChangedFiles`. Include:
  - `TestGate_ApprovalVoidedByEdit`: approve, edit the artifact body, and the gate fails with `WaitingForHuman=="approve spec"`.
  - `TestGate_ApprovalByAgentRoleIgnored`: an `Approval{Role:"checker",Verdict:"approve"}` does not pass the spec gate.
  - `TestGate_VerifyRequiresFreshCheck`: a passing check whose `At` is before `StepEnteredAt` fails.
  - `TestGate_FixTooManyFiles`: 6 changed files with the default budget fails, with the reason `6 files changed, budget 5`.
  - `TestAdvance_GateFailNoTransition`: `Transitions` stays empty.
  - `TestAdvance_PassMovesStep`: spec → plan, with `Items.Get(id).Step=="plan"`.
- [ ] **Step 2: Confirm they fail.** Run `go test ./internal/workflow -run 'Gate|Advance' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** `Gate` as a switch on `(Kind, Step)`, plus `Advance`.
- [ ] **Step 4: Verify.** The same command should PASS.
- [ ] **Step 5: Commit.** `feat(workflow): gates and advance`

### Task 6: Verified runs and the human approval guard

**Files:**
- Create: `internal/workflow/tdd.go`, `internal/workflow/check.go`, `internal/workflow/approve.go`
- Test: `internal/workflow/tdd_test.go`, `internal/workflow/check_test.go`, `internal/workflow/approve_test.go`

**Interfaces:**
- Produces:
  - `type Runner interface{ Run(ctx context.Context, dir string, argv []string) (exit int, tail string, err error) }`. The production implementation is exec with no shell, a 1000-character tail, and full output written to `<townRoot>/<rig>/.gtb/items/<id>/outputs/<ts>.log`.
  - `func Red(env GateEnv, r Runner, itemID, planStep, dir string, argv []string, testFiles []string) (TDDRun, error)` records `red` with `Provenance:"verified"` and `Source:"gtb"` only if `exit != 0`. If the command exits 0, it records `refused` with the tail `test passed before implementation`.
  - `func Green(env GateEnv, r Runner, itemID, planStep, dir string) (TDDRun, error)` re-runs the argv from the latest `red` for that step. It records `refused` if the sha of the test files differs from red's `TestFilesSHA` (reason `test files changed since red`) or if `exit != 0`. Otherwise it records `green`.
  - `func Check(env GateEnv, r Runner, itemID, dir string) (CheckRun, error)` refuses with `check commands not approved` unless `ChecksApproved`. Otherwise it runs every `CheckCommands` entry through `sh -c`, the one place a shell is allowed because the commands are human-approved config, and records one `CheckRun`.
  - `type Terminal interface{ IsTTY() bool; Confirm(prompt string) (bool, error) }`
  - `var AgentMarkerEnv = []string{"GT_ROLE","GT_POLECAT","BD_ACTOR","CLAUDECODE","CLAUDE_CODE_ENTRYPOINT","CODEX_SANDBOX"}`
  - `func HumanApprove(env GateEnv, t Terminal, getenv func(string) string, itemID, artifact, verdict, actor string) (Approval, error)` behaves as follows:
    - It refuses with `ErrNotHuman` if any marker variable is non-empty or `!t.IsTTY()`.
    - It shows the artifact sha, and asks `Approve <artifact> sha <first 12> for <item>? [y/N]`.
    - It records `Role:"human"`, `Source:"human"`, `Channel:"terminal"` and `SHA` set to the current hash.
  - `func RecordVerdict(env GateEnv, itemID, artifact, role, verdict, actor string) (Approval, error)` is for agent reviewer roles. It always records `Source:"agent"` and refuses `role=="human"`.

- [ ] **Step 1: Write the failing tests.**
  - `TestRed_PassingTestRefused`.
  - `TestGreen_TestFileChangedRefused`.
  - `TestCheck_UnapprovedRefused`.
  - `TestHumanApprove_NoTTYRefused`.
  - `TestApproveGuard_TTYButAgentEnv`: `IsTTY()==true` and `getenv("GT_ROLE")=="rig/polecats/nux"` give `ErrNotHuman` (Review Focus 5).
  - `TestRecordVerdict_HumanRoleRefused`.
- [ ] **Step 2: Confirm they fail.** Run `go test ./internal/workflow -run 'Red|Green|Check|Approve|Verdict' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** the functions above.
- [ ] **Step 4: Verify.** The same command should PASS.
- [ ] **Step 5: Commit.** `feat(workflow): verified tdd/check runs and human approval guard`

### Task 7: CLI commands

**Files:**
- Create: `internal/cmd/wf.go`
- Test: `internal/cmd/wf_test.go`

**Interfaces:**
- Consumes: Tasks 2–6. Rig and town discovery go through `beads.FindTownRoot` and `inferRigFromCwd` (`internal/cmd/helpers.go:17`).
- Produces these top-level commands, in group `GroupWork`. Every one supports `--json`.
  - `gtb item new <task|fix|epic> <title> [--parent ID] [--rig R]`: creates the item at the first step and writes an artifact stub with frontmatter `id`, `status: draft` and `parent`.
  - `gtb item show <id>`: step, gate result, artifact paths and shas.
  - `gtb item list [--rig R]`.
  - `gtb advance <id>`: exit 0 when the item moved, 3 when the gate refused (reasons printed), 4 on `ErrStepConflict`.
  - `gtb approve <id> <artifact>` and `gtb request-changes <id> <artifact>`: go through `HumanApprove`.
  - `gtb approve-checks [--rig R]`: same guard. Sets `check_commands_approved_sha` in the rig settings.
  - `gtb verdict <id> <artifact> <role> approve|concerns|reject`.
  - `gtb red <id> <plan-step> --tests <glob,...> -- <argv...>`, `gtb green <id> <plan-step>`, `gtb check <id>`.
- The existing `check` subcommands live under other parents, so they don't collide with the top-level `check`. Verify with `gtb check --help`.

- [ ] **Step 1: Write the failing tests.**
  - `TestAdvanceCmd_ExitCodes`: with an in-memory env injected through a package-level `var newWorkflowEnv = func(...) (workflow.GateEnv, error)`, a gate refusal exits 3 and prints the reasons. A conflict exits 4.
  - `TestApproveCmd_RefusedUnderAgentEnv`: with `GT_ROLE` set, the command prints `approvals must come from a human` and exits 1.
  - `TestItemNew_WritesStub`: the stub exists at `ArtifactPath(...,"spec")` with `status: draft`.
- [ ] **Step 2: Confirm they fail.** Run `go test ./internal/cmd -run 'AdvanceCmd|ApproveCmd|ItemNew' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** `wf.go` with cobra, following `hook.go`'s `init()` pattern.
- [ ] **Step 4: Verify.** The same command should PASS. Also do a manual smoke test in a scratch town: `gtb item new task "x"`, then `gtb advance <id>` should exit 3 with `approve spec`.
- [ ] **Step 5: Commit.** `feat(cmd): gtb item/advance/approve/verdict/red/green/check`

### Task 8: Step formulas, sling and done integration

**Files:**
- Create: `internal/formula/formulas/gtb-task-{spec,plan,execute,verify,review}.formula.toml`, `gtb-fix-{report,reproduce,rca,fix,verify}.formula.toml`, `gtb-epic-{epic,breakdown,verification}.formula.toml`
- Modify: `internal/cmd/sling_schedule.go:295` (`resolveFormula`), `internal/cmd/done.go` (completion path, near line 681 where the exit type is validated)
- Test: `internal/cmd/sling_schedule_test.go` (add), `internal/cmd/done_workflow_test.go`, `internal/formula/embed_test.go` (formula count)

**Interfaces:**
- Consumes: `workflow.Items`, `workflow.Advance`.
- Produces:
  - `resolveFormula` gains an `itemID string` parameter. When the rig has `gated.enabled` and the bead carries `gtb:wf:<kind>`, it returns `gtb-<kind>-<step>` before any other layer except an explicit `--formula`.
  - Each formula follows `mol-polecat-work`'s TOML shape: `description`, `formula`, `version = 1`, and `[[steps]]`. Its steps tell the agent to:
    - write or update the step's artifact at the path printed by `gtb item show <id> --json` (`.artifact_path`), and run `gtb red`/`gtb green`/`gtb check` where the step needs evidence;
    - run `gtb advance <id>`;
    - then run `gtb done --status DEFERRED` if the advance exited 3 with a human wait, or `gtb done` otherwise.
  - Formulas never tell the agent to run `gtb approve`.
  - In `gtb done`, for a gated item: if the item's step is not `merge`, no merge request is submitted. A gate that is waiting calls `Items.SetWaiting(id, reason)`. The polecat self-cleans as usual. Only an item whose step is `merge` goes down the existing MR submit path.

- [ ] **Step 1: Write the failing tests.**
  - `TestResolveFormula_GatedItemUsesStepFormula`: a gated rig with item labels `gtb:wf:task` and `gtb:step:plan` gives `"gtb-task-plan"`. An explicit `--formula x` still gives `"x"`.
  - `TestDone_GatedNonMergeStepNoMR`: no MR bead is created, and `SetWaiting` is called with the gate reason.
  - `TestEmbeddedFormulas_IncludeGtbSteps`: all 13 `gtb-*` files parse with `formula.Parse`.
- [ ] **Step 2: Confirm they fail.** Run `go test ./internal/cmd ./internal/formula -run 'ResolveFormula_Gated|Done_Gated|IncludeGtb' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** the formulas and the two code changes.
- [ ] **Step 4: Verify.** The same command should PASS. Also run `go test ./internal/cmd -run 'Sling|Done' -short` to check that the existing tests still pass.
- [ ] **Step 5: Commit.** `feat: step formulas; sling and done follow the gated workflow`

### Task 9: Refinery merge gate: PR, CI and version bump

**Files:**
- Create: `internal/refinery/workflow_gate.go`, `internal/workflow/bump.go`
- Modify: `internal/refinery/engineer.go` (`ProcessMRInfo` at line 1321, and before `doMergePR` at line 775)
- Test: `internal/workflow/bump_test.go`, `internal/refinery/workflow_gate_test.go`

**Interfaces:**
- Consumes: `MRInfo.SourceIssue` (`engineer.go:204`), `PRProvider` (`pr_provider.go:7`), `workflow.Items`, and `workflow.Store`.
- Produces:
  - `func workflow.ApplyBump(repoDir string, files []string, bump string) (newVersion string, err error)`:
    - It reads the semver `version` in each file. It supports JSON `"version"` keys and a plain `VERSION` file.
    - It errors with `version files disagree: ...` when they differ.
    - It writes the bumped value to all of them.
  - `func (e *Engineer) workflowPreMerge(ctx context.Context, mr *MRInfo) (ok bool, reason string)` runs for gated items only (`gtb:wf:` label on `SourceIssue`). In order, it requires:
    1. The item is at step `merge`.
    2. The review approval sha matches.
    3. It applies `ApplyBump` with `review.md`'s `bump`, then commits `chore: bump version to <v>` on the MR branch after the rebase, and pushes.
    4. The PR exists.
    5. CI checks for the head sha are all successful. This uses a new `PRProvider` method, `ChecksPassed(pr *git.PullRequestInfo) (passed bool, pending bool, err error)`, implemented for GitHub with `gh pr checks --json state`.
  - When CI is pending, the MR is requeued with no state change.
  - When CI fails, the item goes back to `execute`:
    - `Store.Transition(id,"merge","execute","refinery")`
    - `Items.SetStep(id,"execute")`
    - a `CheckRun{Passed:false, Source:"gtb", Provenance:"verified", Tail: <failing check names>}`
    - the MR is closed as rejected.
  - When the merge succeeds, the refinery records a `Transition(id,"merge","done","refinery")` and closes the bead. `Step "done"` is terminal and is only ever written by the refinery.

- [ ] **Step 1: Write the failing tests.**
  - `TestApplyBump_MinorBothFiles`: `package.json` and `.claude-plugin/plugin.json` both at `0.24.7` with `minor` give `0.25.0` in both.
  - `TestApplyBump_Disagree`.
  - `TestWorkflowPreMerge_CIPendingRequeues`.
  - `TestWorkflowPreMerge_CIFailedBackToExecute`: the item step is `execute` and a failed `CheckRun` is recorded.
  - `TestWorkflowPreMerge_NonGatedPassThrough`: `ok==true`, untouched.
  - Use the fake PR provider pattern from the existing `pr_provider_*_test.go`.
- [ ] **Step 2: Confirm they fail.** Run `go test ./internal/workflow ./internal/refinery -run 'ApplyBump|WorkflowPreMerge' -v`. Expected: FAIL.
- [ ] **Step 3: Implement**, and call `workflowPreMerge` from `ProcessMRInfo` before the merge strategy dispatch. For gated rigs, force `MergeStrategy=pr`.
- [ ] **Step 4: Verify.** The same command should PASS, and `go test ./internal/refinery -short` should still pass.
- [ ] **Step 5: Commit.** `feat(refinery): gated merge via PR+CI with approved version bump`

### Task 10: Turn off upstream features gtb doesn't use (spec §9)

**Files:**
- Modify:
  - `internal/cmd/root.go`: hide the `wl` (wasteland) and `proxy-*` commands unless `GTB_UPSTREAM_FEATURES=1`.
  - `internal/daemon/` (`dolt_remotes.go` and the DoltHub sync paths): skip them unless the town setting `upstream_features.dolthub` is true.
  - `cmd/gt-proxy-server`, `cmd/gt-proxy-client`: removed from the `Makefile` `build` target and from goreleaser.
- Test: `internal/cmd/root_upstream_test.go`, `internal/daemon/dolt_remotes_test.go` (add)

**Interfaces:**
- Produces: `func upstreamFeaturesEnabled() bool`, which reads `GTB_UPSTREAM_FEATURES`. The code is kept, as spec §9 requires: disabled, not deleted.

- [ ] **Step 1: Write the failing tests.**
  - `TestRoot_WastelandHiddenByDefault`: with the env var unset, `wlCmd.Hidden` is true. With `GTB_UPSTREAM_FEATURES=1`, it is false.
  - `TestDaemon_DoltRemotesSkippedByDefault`: the remotes push function is not called with the default town settings.
- [ ] **Step 2: Confirm they fail.** Run `go test ./internal/cmd ./internal/daemon -run 'WastelandHidden|DoltRemotesSkipped' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** the env-var and setting checks, and drop the proxy binaries from the build and the release.
- [ ] **Step 4: Verify.** The same command should PASS. `make build` should produce only `gtb`.
- [ ] **Step 5: Commit.** `chore: disable upstream wasteland, DoltHub sync and proxy binaries by default`

---

## Phase 2: Machine ↔ server link

### Task 11: Protocol types and the server store

**Files:**
- Create: `internal/agentlink/protocol.go`, `internal/server/store.go`, `internal/server/retention.go`
- Test: `internal/agentlink/protocol_test.go`, `internal/server/store_test.go`, `internal/server/retention_test.go`

**Interfaces:**
- Produces:
  - In `agentlink`, with JSON fields in snake_case:
    - `const ProtocolVersion = "1.0.0"`
    - `type Hello struct{MachineID, Hostname, OS, Arch, GtbVersion, Protocol string}`
    - `type Snapshot struct{Rigs []Rig; Items []ItemView; Sessions []SessionView; Cursor string}`
    - `type Rig struct{Name, Remote string; Gated bool}`
    - `type ItemView struct{ID, Rig, Kind, Step, Title, Parent, Branch, WaitingForHuman string; DependsOn []string; StepEnteredAt time.Time; Artifacts []ArtifactView; Diffstat []FileStat; Usage *Usage}`
    - `type ArtifactView struct{Name, SHA, Content string}`
    - `type FileStat struct{Path string; Added, Deleted int}`
    - `type Usage struct{InputTokens, OutputTokens int64; CostUSD float64}`
    - `type SessionView struct{Name, Role, Rig, ItemID, State string; LastActivity time.Time}`
    - `type Event struct{ID string; At time.Time; Type string; ItemID string; Data json.RawMessage}`. `ID` is a ULID and `Type` ∈ {`item.upsert`, `item.deleted`, `evidence`, `artifact`, `session`, `heartbeat`}.
    - `type Request struct{Key, Type string; ItemID string; Params json.RawMessage; ExpiresAt time.Time}`
    - `type Result struct{Key string; OK bool; Error string; Data json.RawMessage}`
    - `var RequestTypes = []string{"approve","request_changes","item.create","item.sling","item.cancel","snapshot","inspect","terminal.peek"}`
    - `type ApproveParams struct{Artifact, SHA, Actor string}`
  - In `server`, using `modernc.org/sqlite`:
    - `type Store struct{ db *sql.DB }`
    - `func OpenStore(path string) (*Store, error)` creates the tables `machines`, `tokens`, `enroll_codes`, `rigs`, `items`, `artifacts`, `events`, `requests`, `results` and `web_sessions`.
    - Methods: `UpsertMachine`, `MachineByToken(tokenSHA string)`, `ApplySnapshot(machineID string, s agentlink.Snapshot)`, `AppendEvents(machineID string, evs []agentlink.Event) (lastID string, err error)` (idempotent on `ID`), `Items(filter ItemFilter) ([]ItemRow, error)`, `Item(machineID, id string)`, `EnqueueRequest(machineID string, r agentlink.Request)`, `PendingRequests(machineID string, now time.Time) ([]agentlink.Request, error)` (excludes expired requests and requests that already have a result), `AddResult(machineID string, r agentlink.Result)`, `ExpireRequests(now time.Time) (int, error)`.
    - Tokens are stored only as a sha256.
    - `func (s *Store) Prune(now time.Time) (int, error)` deletes `events` older than 90 days, never `items` or `artifacts`.

- [ ] **Step 1: Write the failing tests.**
  - `TestRequestTypes_Closed`: equals the 8-element list.
  - `TestAppendEvents_Idempotent`: appending the same events twice stores them once.
  - `TestRequests_ExpiredNotDelivered`: a request with `ExpiresAt` 1 s ago is not returned by `PendingRequests`, and `ExpireRequests` marks it as expired (Review Focus 3).
  - `TestPrune_90Days`: an event 91 days old is pruned, one 89 days old is kept, and artifacts are kept.
  - `TestMachineByToken_HashOnly`: the raw token never appears in the `tokens` table.
- [ ] **Step 2: Confirm they fail.** Run `go test ./internal/agentlink ./internal/server -v`. Expected: FAIL.
- [ ] **Step 3: Implement** the above, adding `modernc.org/sqlite` to `go.mod`.
- [ ] **Step 4: Verify.** The same command should PASS.
- [ ] **Step 5: Commit.** `feat: agentlink protocol types and server sqlite store`

### Task 12: Enrollment and `gtb join`

**Files:**
- Create: `internal/server/enroll.go`, `internal/agentlink/token.go`, `internal/cmd/join.go`
- Test: `internal/server/enroll_test.go`, `internal/agentlink/token_test.go`, `internal/cmd/join_test.go`

**Interfaces:**
- Produces:
  - `func (s *Store) CreateEnrollCode(ttl time.Duration) (code string, err error)`: 128-bit random, base32. `ttl` defaults to 15 minutes, and the code is single-use.
  - `POST /agent/v1/enroll` with body `{"code","hello":Hello}` returns `{"machine_id","token"}`.
    - The token is 256-bit random and is stored as a sha256.
    - The code is consumed atomically.
    - An unknown, used or expired code gives `401 {"error":"invalid_code"}`.
  - `func agentlink.SaveToken(townRoot, token string) error` writes `<townRoot>/.runtime/agentlink/machine-token` with mode 0600, plus `machine.json` holding `{server_url, machine_id}`.
  - `func agentlink.LoadToken(townRoot string) (serverURL, machineID, token string, err error)` refuses a token file whose mode is more open than 0600.
  - `func agentlink.ValidateServerURL(u string) error` allows `https://` anywhere and `http://` only for `127.0.0.1`, `localhost` and `[::1]`.
  - `gtb join <server-url> --code <code>`.

- [ ] **Step 1: Write the failing tests.**
  - `TestEnroll_CodeSingleUse`: a second use gives 401.
  - `TestEnroll_Expired`.
  - `TestLoadToken_RejectsWorldReadable`: mode 0644 gives an error.
  - `TestValidateServerURL`: `http://gtb.nhaboc.com` gives an error, `http://127.0.0.1:8080` is ok, `https://gtb.nhaboc.com` is ok.
  - `TestJoin_DoesNotPrintToken`: the captured stdout and stderr don't contain the token returned by an `httptest` server.
- [ ] **Step 2: Confirm they fail.** Run `go test ./internal/server ./internal/agentlink ./internal/cmd -run 'Enroll|Token|ServerURL|Join' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** the above.
- [ ] **Step 4: Verify.** The same command should PASS.
- [ ] **Step 5: Commit.** `feat: one-time enrollment and gtb join`

### Task 13: Server agent API (`/agent/v1/`)

**Files:**
- Create: `internal/server/agentapi.go`
- Test: `internal/server/agentapi_test.go`

**Interfaces:**
- Consumes: Task 11 store and Task 12 tokens.
- Produces these routes, all requiring `Authorization: Bearer <token>`. A missing or unknown token gives 401.
  - `POST /agent/v1/hello` with body `Hello` returns `{"resync":bool}`. `resync` is true when the server has no snapshot for this machine.
  - `POST /agent/v1/snapshot` with body `Snapshot`.
  - `POST /agent/v1/events` with body `{"after":cursor,"events":[...]}`:
    - More than 500 events gives 413.
    - An `after` that doesn't match the stored cursor gives `409 {"error":"resync"}`.
    - Otherwise it returns `{"cursor":lastID}`.
  - `GET /agent/v1/requests?wait=30s` long-polls with `wait` capped at 30 s. It returns `[]Request` without expired ones.
  - `POST /agent/v1/results` with body `[]Result`.
  - `POST /agent/v1/heartbeat`.
- `func NewAgentAPI(st *Store, now func() time.Time, trusted ...netip.Prefix) http.Handler`. `trusted` lists reverse proxies whose `X-Forwarded-For` identifies the client for the enroll limiter (R41/R42, Task 15).

- [ ] **Step 1: Write the failing tests** with `httptest`:
  - `TestEvents_Over500_413`.
  - `TestEvents_CursorMismatch_409Resync`.
  - `TestRequests_LongPollCapped`: `wait=120s` returns within 31 s with a fake clock or a context deadline.
  - `TestRequests_OnlyOwnMachine`: a token for machine A never receives B's requests.
  - `TestAgentAPI_NoToken401`.
- [ ] **Step 2: Confirm they fail.** Run `go test ./internal/server -run 'Events_|Requests_|AgentAPI' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** the routes.
- [ ] **Step 4: Verify.** The same command should PASS.
- [ ] **Step 5: Commit.** `feat(server): agent API`

### Task 14: Machine agent loop and request handlers

**Files:**
- Create: `internal/agentlink/client.go`, `internal/agentlink/queue.go`, `internal/agentlink/loop.go`, `internal/agentlink/apply.go`
- Modify: `internal/daemon/daemon.go` (start the loop when `LoadToken` succeeds)
- Test: `internal/agentlink/loop_test.go`, `internal/agentlink/apply_test.go`, `internal/agentlink/queue_test.go`

**Interfaces:**
- Consumes: the Task 11 protocol, `workflow.Items`, `workflow.Store`, `workflow.ReadArtifact`, `git.DiffStat` (`internal/git/git.go:2516`), and the tmux capture used by `internal/web` (`/session/preview`).
- Produces:
  - `type Queue struct` is an append-only JSONL file at `<townRoot>/.runtime/agentlink/outbox.jsonl` holding `Event`s, with an acknowledged-cursor file next to it.
    - `func (q *Queue) Append(e Event) error`
    - `func (q *Queue) Pending(max int) ([]Event, error)`
    - `func (q *Queue) Ack(lastID string) error`
  - `func Run(ctx context.Context, cfg LoopConfig) error`, where `LoopConfig{TownRoot, ServerURL, Token string; Collect func() (Snapshot, error); Apply Applier; Now func() time.Time}`:
    - It sends hello. If `resync` is true, it sends a snapshot.
    - It loops: flushes `Pending(500)`, long-polls requests, applies them, posts results, and sends a heartbeat every 30 s.
    - Network errors back off exponentially from 1 s to 60 s, and nothing is dropped.
    - A `409` resync triggers a new snapshot.
  - `type Applier interface{ Apply(ctx context.Context, r Request) Result }`. The production `WorkflowApplier` behaves as follows:
    - `approve` / `request_changes`: re-reads the artifact. If its sha ≠ `params.SHA`, the result is `{OK:false, Error:"stale_sha"}` and nothing is recorded (Review Focus 2). Otherwise it records an `Approval{Role:"human", Source:"human", Channel:"dashboard", Actor: params.Actor}`.
    - `item.create`: calls `Items.Create`.
    - `item.sling`: runs `gtb sling <id> <rig>` through the existing sling code path, with no shell.
    - `item.cancel`: the existing unsling/kill path.
    - `snapshot`: forces a resync.
    - `inspect`: an item view with artifacts.
    - `terminal.peek`: a capture of the item's polecat pane, last 200 lines.
    - An unknown type gives `{OK:false, Error:"unsupported"}`.
    - It drops a request whose `ExpiresAt` is in the past, with `{OK:false, Error:"expired"}` and no side effects (Review Focus 3).
  - Hooks in `workflow`: the `Store` writes and `Items.SetStep` / `SetWaiting` append the matching `Event` to the queue through an optional `workflow.Observer` interface, `OnEvent(agentlink.Event)`. It is set in `GateEnv` as a field `Observer`, and is nil-safe.

- [ ] **Step 1: Write the failing tests.**
  - `TestApplyApprove_StaleSHA`.
  - `TestApplyApprove_RecordsDashboardChannel`.
  - `TestAgentLoop_DropsExpired`.
  - `TestAgentLoop_OfflineQueuesAndReplays`: an `httptest` server is down for the first 3 calls, then comes up. All queued events arrive in order exactly once.
  - `TestAgentLoop_ResyncOn409`.
  - `TestApply_UnknownTypeUnsupported`.
- [ ] **Step 2: Confirm they fail.** Run `go test ./internal/agentlink -v`. Expected: FAIL.
- [ ] **Step 3: Implement** the above. In the daemon, start `agentlink.Run` in a goroutine when a token exists, and log `agentlink: connected to <server_url>` without the token.
- [ ] **Step 4: Verify.** `go test ./internal/agentlink -v` should PASS, and `go test ./internal/daemon -short` should still pass.
- [ ] **Step 5: Commit.** `feat(agentlink): outbound loop, offline queue, typed request handlers`

---

## Phase 3: Server and dashboard

### Task 15: `gtb server`, auth and browser API

**Files:**
- Create: `internal/server/server.go`, `internal/server/auth.go`, `internal/server/webapi.go`, `internal/server/static.go`, `internal/cmd/server.go`
- Test: `internal/server/auth_test.go`, `internal/server/webapi_test.go`

**Interfaces:**
- Consumes: Tasks 11–13.
- Produces:
  - `gtb server --listen :8080 --data /data --public-url https://gtb.nhaboc.com`. The environment provides `GTB_GITHUB_CLIENT_ID`, `GTB_GITHUB_CLIENT_SECRET`, `GTB_SESSION_SECRET` and `GTB_ALLOWED_LOGIN` (default `baovo15`).
  - It mounts `/agent/v1/` (Task 13), `/auth/github/{login,callback}`, `/auth/logout`, `/api/v1/*`, `/health`, and the SPA at `/`.
  - Auth:
    - After OAuth, it fetches the GitHub user. A login ≠ `GTB_ALLOWED_LOGIN` gives 403.
    - The session cookie is `gtb_session` with HttpOnly, Secure (when the public URL is https) and SameSite=Lax.
    - Mutating `/api/v1` calls need the `X-CSRF-Token` header to match the session's token.
  - `/api/v1`:
    - `GET /me`.
    - `GET /board?machine=&rig=&kind=` returns `{lanes:[{step, items:[ItemRow]}], needs_you:[ItemRow]}`.
      - `needs_you` holds items with a non-empty `waiting_for_human`, sorted oldest first.
      - Each `ItemRow` includes `machine_id`, `freshness_seconds` (now − last event from that machine), and `stale` (true when freshness > 120 s).
    - `GET /items/{machine}/{id}` returns the item with artifacts, evidence, events and diffstat.
    - `POST /items/{machine}/{id}/approve` with body `{artifact, sha}`, and `POST /items/{machine}/{id}/request-changes`. Both enqueue a request with key `uuid` and `ExpiresAt` set to now + 24 h, and `Actor` set to the GitHub login.
    - `POST /items/{machine}/{id}/sling`.
    - `POST /items/{machine}/{id}/cancel`.
    - `POST /items` with body `{machine, rig, kind, title, parent}`.
    - `GET /items/{machine}/{id}/terminal` enqueues `terminal.peek` and waits up to 10 s for the result.
    - `GET /machines`.
    - `POST /enroll-codes` returns `{code, expires_at}`.
    - `DELETE /machines/{id}` revokes the machine's tokens.
    - `GET /events` is SSE that pushes `board.changed` when events arrive.
  - Approvals: `approve` and `request-changes` are refused with 403 when the request carries `Authorization: Bearer` instead of a session cookie.
  - `static.go`: `//go:embed all:dist` from `internal/server/dist/`, which the Task 16 build copies into place. When the directory is empty, it serves a placeholder `index.html` saying `UI not built`.

- [ ] **Step 1: Write the failing tests.**
  - `TestAuth_WrongLogin403`, with a fake GitHub user endpoint.
  - `TestWebAPI_ApproveRequiresSession`: the bearer header gives 403.
  - `TestWebAPI_ApproveRequiresCSRF`.
  - `TestBoard_NeedsYouOldestFirst`.
  - `TestBoard_StaleFlag`: the last event 121 s ago gives `stale:true`.
  - `TestApprove_EnqueuesWith24hExpiry`.
- [ ] **Step 2: Confirm they fail.** Run `go test ./internal/server -run 'Auth_|WebAPI_|Board_|Approve_' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** the above, adding `golang.org/x/oauth2` to `go.mod`. Run retention with `Store.Prune` and `ExpireRequests` every hour.
- [ ] **Step 4: Verify.** The same command should PASS. Also run `go run ./cmd/gtb server --listen 127.0.0.1:18080 --data $(mktemp -d)`; `curl -s localhost:18080/health` should return `ok`.
- [ ] **Step 5: Commit.** `feat(server): gtb server with GitHub login, browser API, SSE`

### Task 16: React dashboard (Fabro-style)

**Files:**
- Create:
  - `web/package.json` (React 19, react-router 7, Vite, TypeScript, Tailwind 4, Vitest + Testing Library), `web/vite.config.ts`, `web/index.html`
  - `web/src/api.ts`, `web/src/App.tsx`
  - `web/src/pages/Board.tsx`, `web/src/pages/Item.tsx`, `web/src/pages/Machines.tsx`, `web/src/pages/Coordination.tsx`, `web/src/pages/Settings.tsx`
  - `web/src/components/{ItemCard,ApproveBar,Freshness,TerminalView}.tsx`
- Modify: `Makefile`. Add a `web` target that runs `cd web && bun install --frozen-lockfile && bun run build && rm -rf ../internal/server/dist && cp -r dist ../internal/server/dist`, and make `build` depend on `web` when `bun` is present.
- Test: `web/src/pages/Board.test.tsx`, `web/src/pages/Item.test.tsx`
- Reference: Fabro's `apps/fabro-web/app/routes/runs.tsx` and `run-*.tsx` (MIT) for layout. Copied components keep an attribution header comment.

**Interfaces:**
- Consumes: the Task 15 `/api/v1` routes exactly as specified.
- Produces:
  - **Routes.** `/` (Board), `/items/:machine/:id` (Item, with tabs `overview|artifacts|evidence|logs|files|usage|terminal`), `/machines`, `/coordination`, `/settings`.
  - **Board.** The **Needs you** lane is first, then one column per step in the order of `Steps(kind)`. Filters for machine, rig and kind are kept in the URL query. Each card shows the item id, title, rig, machine and a `Freshness` badge (grey "stale" when `stale`).
  - **Approving.** `ApproveBar` on the Artifacts tab shows the artifact sha (first 12 characters). It posts `approve` with that sha, then shows `pending` until the item's next event. A `stale_sha` result shows `Artifact changed on the machine; reload to review the new version`.
  - **Coordination.** A read-only list of each machine's Mayor plan beads, laid out in the coordination-record sections: Goal, Acceptance, Repository tasks, Decisions.

- [ ] **Step 1: Write the failing tests.**
  - `Board.test.tsx`: given a mocked `/board` response, the first lane heading is `Needs you`, and a stale item shows `stale`.
  - `Item.test.tsx`: clicking Approve posts `{artifact:"spec", sha:"<sha>"}`. A mocked `stale_sha` result shows the reload message.
- [ ] **Step 2: Confirm they fail.** Run `cd web && bun run test`. Expected: FAIL.
- [ ] **Step 3: Implement** the pages and components.
- [ ] **Step 4: Verify.** `cd web && bun run test` should PASS. `make build`, then `./gtb server ...` serves the board at `/`. Check it manually against a seeded store (`go run ./internal/server/devseed`, a small dev-only seeder that is part of this task).
- [ ] **Step 5: Commit.** `feat(web): Fabro-style board, item detail and approvals`

---

## Phase 4: Packaging and deploy

### Task 17: Server image and k3s deploy

**Files:**
- Create: `deploy/Dockerfile.server`. It is multi-stage: `oven/bun:1.3.14` builds `web/`, `golang:1.26.10-bookworm` builds `gtb` with `CGO_ENABLED=1` (ICU, same as `make build`), and the runtime is `debian:bookworm-slim` with `ca-certificates` and `libicu72`. It runs as uid 1000 with entrypoint `gtb server --listen :8080 --data /data`.
- Modify: `deploy/k3s/gtb.yaml` and `scripts/gtb-deploy.sh`. Replace the town StatefulSet and Dolt StatefulSet with a single `gtb` StatefulSet:
  - It mounts the `/data` PVC at `/var/lib/gtb`.
  - Its env vars come from the Secret `gtb-server` (`GTB_GITHUB_CLIENT_ID`, `GTB_GITHUB_CLIENT_SECRET`, `GTB_SESSION_SECRET`) plus `GTB_ALLOWED_LOGIN=baovo15`.
  - The readiness probe is `/health`.
  - It keeps the `gtb` Service on NodePort `30881`.
  - The NetworkPolicy allows ingress on 8080 and egress to the internet for GitHub OAuth only. The `gtb-dolt` objects and the ninerouter egress are removed.
  - The deploy script creates `gtb-server` with a random `GTB_SESSION_SECRET` the first time. It refuses to continue (`MANUAL: create a GitHub OAuth app ...`) until `GTB_GITHUB_CLIENT_ID` and `GTB_GITHUB_CLIENT_SECRET` are provided as environment variables. It builds with `-f deploy/Dockerfile.server`.
- Test: `kubectl apply --dry-run=client -f deploy/k3s/gtb.yaml` and `bash -n scripts/gtb-deploy.sh`.

- [ ] **Step 1: Check the image builds.** Run `docker buildx build --platform linux/amd64 -f deploy/Dockerfile.server -t gtb-server:test --load .`. Expected: success.
- [ ] **Step 2: Smoke-test it.** Run `docker run --rm -d -p 18080:8080 -v $(mktemp -d):/data gtb-server:test`, then `curl -s localhost:18080/health`. Expected: `ok`.
- [ ] **Step 3: Validate the manifests.** The dry-run should list `statefulset.apps/gtb`, `service/gtb` and the NetworkPolicies, with no `gtb-dolt`. `bash -n` should exit 0.
- [ ] **Step 4: Commit.** `deploy: gtb server image and k3s manifests`

### Task 18: Release workflow and install docs

**Files:**
- Modify: `.github/workflows/release.yml`. Set up Bun, run `make web` before goreleaser, and publish linux/darwin amd64/arm64 archives plus `checksums.txt`.
- Modify: `README.md`. Add a gtb section at the top: install from the release, `gtbb`-style quickstart (`gtb install ~/gtb`, `gtb join https://gtb.nhaboc.com --code …`, `gtb rig add <name> <url>`, `gtb item new task "…"`), and a link to the spec.
- Delete: `npm-package/`. It is no longer published; the spec ships only binaries plus checksums.
- Test: `goreleaser release --snapshot --clean`.

- [ ] **Step 1: Snapshot release.** Run `goreleaser release --snapshot --clean`. Expected: `dist/` holds four `gtb_*_{linux,darwin}_{amd64,arm64}` archives and `checksums.txt`, and no windows or freebsd archives.
- [ ] **Step 2: Install from the snapshot on macOS.** Extract the darwin/arm64 archive and run `./gtb version`. Expected: a version is printed.
- [ ] **Step 3: Commit.** `release: linux+darwin binaries with checksums; gtb quickstart`

### Task 19: End-to-end check across a real machine and server

**Files:**
- Create: `internal/e2e/gtb_flow_test.go` (build tag `e2e`, run in `Dockerfile.e2e`)
- Modify: `Dockerfile.e2e:74` (parameterise the test selection)

**Interfaces:**
- Consumes everything above.

- [ ] **Step 1: Write the test** `TestGtbFlow_ApproveFromDashboard`:
  1. Start `gtb server` on loopback with a fake OAuth login injected through a test-only `GTB_TEST_LOGIN` (honoured only when built with `-tags e2e`).
  2. Run `gtb install`, then `gtb join http://127.0.0.1:<port> --code <code>`.
  3. Run `gtb rig add` on a local `file://` repo with gated enabled.
  4. Run `gtb item new task "demo"`, then write the spec.
  5. `gtb advance` exits 3.
  6. The board's `needs_you` contains the item.
  7. POST approve with the current sha. Within 10 s, `gtb advance` exits 0 and the item is at `plan`.
  8. Edit the spec, then POST approve with the old sha. The result is `stale_sha`.
- [ ] **Step 2: Run it.** `Dockerfile.e2e` hard-codes `-run TestInstall ./internal/cmd/...` (line 74). Change it to take `ARG E2E_TEST_RUN=TestInstall` and `ARG E2E_TEST_PKGS=./internal/cmd/...`, and pass both through `E2E_BUILD_FLAGS`. Then run `make test-e2e-container E2E_BUILD_FLAGS="--build-arg E2E_TEST_RUN=TestGtbFlow --build-arg E2E_TEST_PKGS=./internal/e2e/..."`. Expected: PASS. The plain `make test-e2e-container` must still run `TestInstall`.
- [ ] **Step 3: Commit.** `test(e2e): approve-from-dashboard flow`
