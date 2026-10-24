# gtb: reshaping the Gas Town fork around tieudidi's concepts

- Status: **draft for approval** (2026-10-22; maintainer decisions D1–D4 recorded in §12)
- Repo: `https://github.com/baovo15/gtb` (fork of `steveyegge/gastown`, upstream remote `upstream`)
- Owner: the maintainer (baovo15)

Statements are marked by where they came from:
- **(maintainer)**: said by the maintainer in the design conversation.
- **(proposal)**: the designer's call, open to change.
- Unmarked: facts read from the code.

## 1. Intent

gtb is a separate tool. It is built in parallel with tieudidi, not as part of it **(maintainer)**. It borrows two things from tieudidi **(maintainer)**:
1. tbb's gated workflow: spec → plan → TDD → approval → check → review → version bump.
2. The split of responsibilities between Factory, tbb and orchestration.

The outcome is a multi-agent orchestrator with two halves:
- **On each machine:** agents run locally against repos checked out on that machine **(maintainer)**.
- **On the server, `gtb.nhaboc.com`:** every machine reports there **(maintainer)**, and the server shows a Fabro-style dashboard **(maintainer)**.

Linux and macOS come first **(maintainer)**.

**Why (proposal):** Gas Town runs many agents but trusts them to follow a checklist. tbb enforces gates but runs one repo at a time. gtb aims to do both: many agents on many machines, each held to tbb-style gates, all visible from one dashboard.

## 2. Goals

1. **G1: Gated work items.** Every unit of work in gtb follows a fixed, code-enforced step machine modelled on tbb (§5). A step cannot be left until its gate passes and evidence is recorded.
2. **G2: Machine agent.** A `gtb` binary for linux/amd64, linux/arm64, darwin/amd64 and darwin/arm64 runs the local half (§6).
   - It is a single binary, released as binaries plus checksums.
   - It runs agents in tmux the way Gas Town does today.
3. **G3: Report to the server.** The machine agent reports to the server over an outbound-only connection: items, steps, evidence, logs, usage, and pending approvals (§7). Work continues when the server is unreachable and syncs when it comes back **(proposal)**.
4. **G4: Fabro-style dashboard.** The server serves a board of work items across all machines and repos **(maintainer: "like fabro")** (§8).
   - Columns are workflow steps.
   - Each item has a detail page with stages, logs, files, usage and a live terminal view.
5. **G5: Humans approve, agents don't.** Approval gates wait for a human. A human can approve in the dashboard or with `gtb approve` and a terminal prompt on the machine **(maintainer)**. No agent and no automation can approve. The local guard matches tbb's level: an interactive TTY prompt plus refusal when agent marker variables are set. It stops accidents, but a determined agent running as the same user could get around it **(maintainer accepted this level)**.
6. **G6: Orchestration split.** Each machine has its own Mayor, which plans and assigns work across that machine's repos but does not do the work **(maintainer)**. Work that spans machines is assigned by the maintainer by hand. The dashboard shows it and does not route agent requests between machines **(maintainer)**. Each repo's item state is the source of truth for that item (§4).

## 3. Non-goals and anti-goals

**Non-goals (for this design):**
- Windows support. It comes later **(maintainer)**.
- Running agents on the server. The server only coordinates and displays **(maintainer)**.
- Depending on tieudidi at runtime: no requirement on the `tbb` CLI, Factory or the orchestration repo. gtb implements its own gates in Go **(maintainer)**.
- Running inside a repo that already uses tbb. `gtb rig add` refuses a repo that has `.tbb/`, so two workflows never compete over one repo **(proposal)**.
- Interoperating with Factory or tbb's shared contract. `/agent/v1/` is gtb's own private protocol, and only its session shape is borrowed **(proposal)**.
- Multi-user accounts and roles beyond one maintainer plus machine tokens **(proposal)**.
- Cloud sandboxes such as Daytona or Fabro's Docker provider. Agents run in local worktrees as Gas Town does now **(proposal)**.
- Keeping upstream mergeability. gtb is a fork that will diverge; upstream fixes are cherry-picked as needed **(proposal)**.

**Anti-goals (a failure even if it works):**
- An agent can mark its own gate passed, or can approve. Evidence must be produced or checked by gtb code, not reported by the agent.
- The server needs an inbound connection to a machine, or a machine has to open a port.
- Source code, git credentials or provider API keys leave the machine. Only metadata, evidence summaries, artifacts, log tails and diffstats go to the server (§7.2) **(proposal)**.
- The dashboard executes arbitrary commands on machines. Today `gt dashboard` has `POST /api/run` with an allowlist and CSRF only. The remote dashboard may only send a closed set of typed requests (§7.3).

## 4. Roles and responsibilities

This maps Gas Town's roles onto the tieudidi split **(proposal)**:

| tieudidi concept | gtb role | Built from (Gas Town) | Owns |
|---|---|---|---|
| tbb (repo-local workflow) | **Rig workflow store**, per repo on each machine | Beads issues and Dolt (`internal/beads`, `internal/doltserver`) | item state, step, evidence and approvals for that repo: **source of truth** |
| tbb executor runs | **Polecats** | `internal/polecat`, `internal/session`, `internal/tmux`, formulas | doing one step's work in a worktree |
| tbb `check` and `merge`, version-bump rule | **Refinery** | `internal/refinery` (`Engineer`, `MergeStrategy: pr`, `MergeQueueConfig.Gates`) | the merge gate: opens the PR, waits for CI, verifies the approved version bump, then merges |
| watchdog (gtbb G3) | **Witness and Deacon** | `internal/witness`, `internal/deacon`, `internal/daemon` | detecting stuck or dead workers and **notifying only**, as in gtbb decision Q1 |
| orchestration (coordinator) | **Mayor**, one per machine | `internal/mayor` | cross-repo plans and coordination records for that machine's rigs; assigning items to those rigs. It never advances a step itself |
| Factory (dashboard and control plane) | **gtb server** | new: `internal/server`; UI replaces `internal/web` | the fleet view, central copies of reported state, routing approvals back down |
| gtbb (machine daemon) | **gtb machine agent** | `internal/daemon` plus a new `internal/agentlink` | supervising local roles and the outbound connection |

Conflict rule **(proposal, from tbb)**: the machine's rig store wins. The server's copy is a projection, and the dashboard shows its source and freshness for each item.

## 5. Workflow model

### 5.1 Item kinds and steps

These are carried over from tbb (`tieubocboc/cli/lib/workflows.js`) **(proposal: start with these three; the init/project workflow is left out of the first version)**:

| Kind | Steps (gate to leave each step) |
|---|---|
| **task** | `spec` (spec approved by a human) → `plan` (plan approved; may get a checker-role verdict) → `execute` (TDD: each plan step has a recorded red, then a green) → `verify` (check commands passed since the step was entered) → `review` (review approved; the review artifact carries the proposed bump size) → `merge` (refinery: PR open, CI green, bump applied as approved, merged) |
| **fix** | `report` (approved) → `reproduce` (red repro recorded) → `rca` (approved) → `fix` (green repro; changed files ≤ budget) → `verify` (check passed) → `merge` |
| **epic** | `epic` (numbered acceptance criteria approved) → `breakdown` (approved; every criterion covered by a task; no dependency cycles) → `tasks` (all child tasks merged) → `verification` (check passed plus human approval) |

The version bump is part of the merge gate, not a separate step **(maintainer: PR + CI, agent proposes)**:
- The agent writes `bump: patch|minor|major` in the review artifact. Approving the review approves that size.
- The refinery applies the bump to every configured version file, in step, on the PR branch after rebasing. Doing it at merge time avoids parallel polecats conflicting on version files **(proposal)**.
- The refinery opens the PR with `MergeStrategy: pr`, waits for the rig's CI to pass, and merges.
- If CI fails, the item goes back to `execute` with the CI output recorded as evidence **(proposal)**.

### 5.2 Where state lives

- **The item is a Beads issue.** New labels or metadata fields hold `workflow` (task, fix or epic), `step`, and `step_entered_at`. Parent and child links and dependencies reuse what Beads already has.
- **Artifacts** (spec, plan, rca, review and so on) are markdown files kept outside the target repo, in the rig's directory in the town (`~/gtb/<rig>/.gtb/items/<id>/<artifact>.md`). They are never merged into the repo's main branch, so gtb leaves no files behind in repos it works on **(proposal)**.
- **Evidence** is append-only records in the rig's Dolt database, one table per record type:
  - Approvals and verdicts store the artifact's sha256, ignoring the `status:` line, the way tbb does. Editing an artifact voids its approval.
  - TDD records hold red and green results for each plan step: command, exit code, test-file hashes, and the last 1000 characters of output.
  - Check runs hold the command set hash, results and timestamp.
  - Every record carries `source` (gtb, agent or human) and `provenance` (verified, recorded or reported).
- **Gates** are Go functions in a new `internal/workflow` package that read only evidence and artifacts. `gt`/`gtb advance <id>` runs the gate and moves the step, or refuses with a reason. The agent has no other way to change `step`.

### 5.3 How agents follow it

- When a polecat is slung an item, it gets a formula that matches the item's current step. New formulas are written per step, for example `gtb-task-spec` and `gtb-task-execute`. They replace `mol-polecat-work` for gated items.
- The formula tells the agent what to produce. The agent runs `gtb red <step> -- <cmd>` and `gtb green <step>` so that gtb itself runs the commands and records verified evidence.
- At a human gate, the polecat records `waiting_for_human` and exits, following the self-cleaning model. The item shows up as **Needs you** on the dashboard. Once a human approves, the next step can be slung.
- Existing Gas Town features stay as they are: mail, nudge, convoys, and items without a workflow (plain beads). Gated workflow is opt-in per rig at first **(proposal)**.

### 5.4 Rig configuration

Rig config gains:
- `workflow.enabled`
- `workflow.check_commands`
- `workflow.version_files`, for example `package.json` and `.claude-plugin/plugin.json`, kept in step
- `workflow.budget`: max fix files, max fix rounds, max plan loops

Changing `check_commands` needs a human approval, as in tbb.

## 6. Machine agent (`gtb` on Linux/macOS)

- **Binary:** `cmd/gt` becomes `cmd/gtb`, and the command is `gtb`. A `gt` symlink or alias is kept during migration **(proposal)**.
  - goreleaser targets: linux and darwin, amd64 and arm64.
  - Windows and FreeBSD are dropped for now.
  - CGO and ICU stay as upstream requires. Making the build CGO-free is left to the builder.
- **Install:** download the binary and checksum from GitHub Releases, then `gtb install` creates the town at `~/gtb`, as `gt install` does today.
- **Join the server:** `gtb join https://gtb.nhaboc.com --code <one-time-code>` **(proposal)**.
  1. The maintainer creates a one-time, single-use enrollment code in the dashboard.
  2. `gtb join` exchanges it for a machine token.
  3. The token is stored owner-only in `~/gtb/.runtime/agentlink/machine-token` and is never logged.
- **Add a repo:** `gtb rig add <git-url>`, as today, after which the rig is reported to the server.
- **Runtime:** a local Dolt server, the daemon, the Deacon, the Mayor, a Witness and Refinery per rig, and polecats in tmux. This is unchanged from Gas Town. Every machine runs its own Mayor for its own rigs **(maintainer)**.
- **Fresh start:** no existing Gas Town towns are migrated. `gtb install` creates a new town **(proposal)**.
- **Agent CLI:** Claude Code by default, using the existing presets in `internal/config/agents.go`. Provider keys and base URLs stay in the machine's environment.

## 7. Machine ↔ server link

### 7.1 Transport

- The machine opens an outbound HTTPS connection to `https://gtb.nhaboc.com/agent/v1/`, authenticated with its machine token.
- Plain HTTP is allowed only for loopback.
- The session shape is copied from the tbb agent protocol **(proposal)**: `hello` → full snapshot → push events from a cursor in batches → long-poll for requests (at most 30 s) → post results → heartbeat. A `409 resync` from the server triggers a new snapshot.

### 7.2 What the machine sends

- Machine identity and health, and the list of rigs.
- Items: id, kind, step, title, parent, dependencies, assignee, branch, and the `waiting_for_human` reason.
- Evidence summaries: the records from §5.2, with output tails only.
- Worker sessions: role, state and last activity, plus a read-only terminal snapshot on request.
- Token and cost usage per item where the agent CLI reports it.
- Artifacts (spec, plan, rca and review markdown, with their sha) every time one changes. The server keeps the latest version of each, so item detail and approval work while the machine is offline. An approval from the dashboard is checked against the sha on the machine when it arrives **(proposal)**.
- Diffstat per item (files and line counts), not full diffs. Log tails are capped at 1000 characters per record, and full logs stay on the machine **(proposal)**.
- Server retention: events and evidence summaries for 90 days. Artifacts and item projections are kept until the item is deleted **(proposal)**.

### 7.3 What the server may ask a machine to do

This is a closed, typed set. There are no shell strings and no paths.

| Request | Effect on the machine |
|---|---|
| `approve` / `request_changes` | Records a human approval or verdict against an artifact sha. Refused if the sha has changed |
| `item.create` | Creates an item in a rig (kind, title, parent); sent from the dashboard by the maintainer |
| `item.sling` | Assigns an item to a polecat on its current step; sent from the dashboard by the maintainer |
| `item.cancel` | Stops the item's worker |
| `snapshot` / `inspect` / `terminal.peek` | Read-only |

Requests are idempotent by key, and expired requests are never run.

All requests come from the maintainer's logged-in browser session. Machines never send requests to other machines through the server **(maintainer: per-machine Mayor, manual cross-machine assignment)**. `approve` and `request_changes` are additionally refused from any API token.

### 7.4 Offline behaviour

- The machine keeps working when the server is unreachable. Events queue locally and are replayed from the cursor.
- Pending approvals wait.
- The machine can approve locally with `gtb approve <id> <artifact>` **(maintainer)**:
  - It needs an interactive TTY and refuses when agent marker variables are set (`GT_ROLE`, `CLAUDECODE` and similar).
  - It shows the artifact sha and asks for confirmation.
  - The approval is recorded with `source: human`, `channel: terminal` and synced later.

## 8. Server and dashboard (`gtb.nhaboc.com`)

- **Process:** `gtb server` is a new subcommand in the same binary **(proposal)**. It runs in k3s, in namespace `gtb`, from the existing `deploy/k3s/gtb.yaml` and `scripts/gtb-deploy.sh`. The pod changes from "Gas Town plus a Dolt pod" to "gtb server plus its store", and runs no agents.
- **Server store:** the server's own database of machines, rigs, item projections, evidence summaries, events, enrollment codes and sessions. Choosing SQLite on the PVC or the existing Dolt pod is left to the builder.
- **Auth:**
  - The maintainer logs in with an **admin token**, not GitHub OAuth **(maintainer, 2026-10-23: "we don't need OAuth app, when start pod in k3s give me the token")**.
    - On first start, the server generates a 256-bit random admin token. It stores only the sha256 and prints the plain token once to the pod log, framed so `kubectl logs` shows it clearly.
    - The token can also be supplied via the env var `GTB_ADMIN_TOKEN` (for example from a k3s Secret). In that case nothing is printed.
    - `gtb server admin-token --rotate` replaces the token and prints the new one.
    - The login page takes the token and exchanges it for an HttpOnly session cookie, plus a CSRF token for writes.
  - Machines use machine tokens.
  - The dashboard is reachable only on the tailnet, as all of `*.nhaboc.com` is.
  - Machines are assumed to be on the tailnet too. Tokens and TLS are still required, so a machine could later connect from outside without a redesign **(proposal)**.
- **Dashboard, modelled on Fabro's web UI (`fabro-sh/fabro` `apps/fabro-web`):**
  - **Board.** Columns are workflow steps. Cards are items across all machines and rigs, filterable by machine, rig and kind. A **Needs you** lane is pinned first and holds items waiting on a human gate **(proposal)**.
  - **Item detail.** Tabs:
    - Overview: step history and gates with pass or fail reasons.
    - Artifacts: spec, plan and review, with an **Approve / Request changes** action.
    - Evidence: TDD and check runs.
    - Logs and events.
    - Files: changed files and diffstat.
    - Usage: tokens and cost.
    - Terminal: a read-only live view of the worker's tmux pane.
  - **Machines.** Each machine's health, rigs, running workers and last heartbeat. Freshness is shown on every projected value.
  - **Coordination.** Each machine's Mayor plans, laid out like orchestration's coordination records: goal, acceptance, repo task table and decisions. These are display only. The maintainer creates and slings items for cross-machine work by hand from the board.
  - **Settings.** Enrollment codes, rig workflow config (read-only display), and machine revocation.
- **UI tech:** React and TypeScript in the style of Fabro's web UI, built with Bun and embedded into the Go binary with `embed` **(proposal)**. It replaces `internal/web`'s htmx dashboard. Copying components from Fabro (MIT licensed) is allowed with attribution.

## 9. Migration from today's fork

- The current `deploy/k3s/gtb.yaml` runs a full Gas Town town inside the pod. It has not been deployed yet.
- Until §8 exists, either deploy it as-is or don't deploy at all (Q3).
- Upstream Gas Town features that don't fit stay in the code but are disabled in gtb's default config **(proposal)**. These include wasteland/DoltHub federation, the mTLS proxy for sandboxed polecats, and the unused `internal/connection` SSH registry. Deleting them is a later cleanup.

## 10. Constraints

- Go, building on the existing fork. Fork-local CI is reduced to Linux and macOS jobs **(proposal)**.
- About 475k lines of Go with 568 test files. Changes keep upstream's test conventions: `*_test.go` next to the code, the `integration` and `e2e` build tags, and `go test ./...` plus golangci-lint.
- Linux and macOS only for the machine agent. The server runs in the existing k3s cluster on beboc (amd64).
- Server images go through the nhaboc Nexus at `127.0.0.1:30082`, as in the current deploy script.

## 11. Left to the builder

- Package layout inside `internal/` for `workflow`, `server` and `agentlink`.
- The exact Dolt table schemas for evidence and their migrations.
- The server store technology (SQLite or Dolt).
- The wire format details of `/agent/v1/` within the shape in §7.
- Exact formula wording for each step.
- How the React UI talks to the server: REST plus SSE, as Fabro does, is suggested.
- Whether `gt` remains as an alias, and for how long.
- Order of delivery. The suggested order is:
  1. workflow core on one machine, with no server
  2. machine ↔ server link
  3. dashboard
  4. coordination view
  5. release packaging

## 12. Decisions

**Maintainer, 2026-10-22:**
- **D1.** gtb has its own gates in Go and does not call the `tbb` CLI.
- **D2.** Each machine runs its own Mayor. Cross-machine work is assigned by hand.
- **D3.** Approvals come from the dashboard or from a terminal prompt with `gtb approve`. The local guard is at tbb's level, and the maintainer accepted that.
- **D4.** Merging goes through PR plus CI. The agent proposes the bump size, and the review approval confirms it.

**Designer's calls (open to change):**
- Refuse repos that use tbb.
- `/agent/v1/` is private, not the Factory contract.
- Artifacts live outside the target repo.
- The server stores artifacts and diffstats, not full diffs or logs, with 90-day retention for events.
- Machines are on the tailnet, but auth is still required.
- Fresh start, with no migration of existing towns.
- The refinery applies the bump at merge time.
- A failing CI run sends the item back to `execute`.
- The dashboard opens on the board with the **Needs you** lane first.

**Still open:**
- **Q3.** Should the current gtb deploy (a full Gas Town in a pod) go out now, or wait for the new server? This is a release decision and doesn't change the build.
