# Changelog

All notable changes to the Gas Town project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- **Daemon crash-loop vs Claude usage limits** — Stuck-agent-dog now inspects
  the agent's tmux pane for Claude usage-limit / rate-limit signatures before
  killing and restarting. Detected pauses apply a fixed retry delay
  (`PauseBackoff`, default 60s) and don't count toward the crash-loop fault
  budget, letting `quota_dog` rotate accounts instead of burning the budget on
  transient API limits (gh#3398, hq-j6hur.4.1).

## [1.1.0] - 2026-05-06

### Added

- **Convoy completion + cross-rig dep notifications** — Convoy completion and
  cross-rig dependency resolution now fire notifications, surfacing milestone
  events without polling (#3838, gt-wfs-55hsg).

### Fixed

- **Scheduler guards** — `scheduleBead` now skips closed and tombstone beads,
  preventing wasted dispatch attempts and "no such bead" errors (hq-ki2,
  #3840). Mirrors the existing `runSling` / `executeSling` guards.
- **Mail inbox Dolt churn** — Reduced repeated Dolt commits on inbox reads
  (gt-05ld, #3830).
- **Polish** — Rename `DeliveryAckLabelSequenceIdempotent` to clarify intent
  (gt-ekuh).
- **`jsonl_git_backup` plugin** — Forward `USER`, `LOGNAME`, and `HOME` env to
  git children so the plugin can run under uid-501 wrapper contexts (gt-zt1w).
- **Resilience under Dolt memory pressure** — Subprocess timeout and parallel
  rig scan so `gt status` and related commands degrade gracefully when Dolt is
  under load instead of hanging.
- **`bd` 1.0+ compatibility** — `bd init --prefix <X>` now persists the prefix
  directly (previously relied on the removed `bd config set issue_prefix` call).
- **Cross-rig mail delivery** — Delivery acks routed via bd prefix routing;
  Create routes via BEADS_DIR instead of `--repo` to prevent pthread deadlock;
  zero-copy fix for routed writes.
- **Daemon hardening** — Filter messaging beads, guard cross-rig prefix, fail
  prime fast on bad state. `hasAssignedOpenWork` checked via `--repo` (gt-fcw)
  and before reaping working-bead-lookup-failed polecats. Legacy flat polecat
  layout supported with regression coverage.
- **Polecat / sling lifecycle** — Auto-burn orphan molecules from beads stuck
  in hooked-with-no-assignee (gh-3697); use `IsAgentAlive` in `Start` zombie
  check (hq-k1ot); accept dog pool targets in deferred mode (aa-4yf2);
  CLOSED bead treated as terminal in `check-recovery` (aa-55d8); zombie-scan
  skips restart if polecat branch already merged (aa-apw); persist convoy
  fields for single-bead dispatch.
- **Convoy** — `bdListChildren` falls back to deps table when the primary path
  is unavailable (#3700); cross-rig wave staging regression test (hq-mtc).
- **Doctor** — Role-aware Stop hook check for polecats (#3648); detect and
  recover corrupt `.repo.git` bare repos (gt-61twf).
- **Install / dolt** — Fail fast when Dolt is unavailable during install;
  clamp Dolt idle session `wait_timeout` to prevent connection exhaustion
  (gh-3623); switch remaining DDL call sites to server connection (#3641);
  atomic `settings.json` write to prevent concurrent-spawn corruption (gh-3500).
- **Hooks / agent config** — Pass agent `Args` to `ResolveProcessNames` at all
  call sites; resolve agent process names through wrapper commands; preserve
  built-in opencode preset fields in overrides; sync opencode hooks in nested
  polecat worktrees (gt-hii); reject non-bead args before `bd show` (#3701).
- **Done / safety nets** — Auto-pop orphaned stashes in gt-pvx safety net;
  auto-save uncommitted implementation work (gt-pvx safety net); honor
  explicit target in done contamination check (gt-nmt); verify pushed commits
  before bead closure.
- **Mail / archive** — Tolerate GC'd bead IDs in archive and `archive --stale`
  (aa-6hv); clear satisfied mail reply reminders (gt-niu2); isolate
  `NotifyMergeOutcome` tests from production mail.
- **Plugin runner** — Explicit `gate=manual` skip in `dispatchPlugins`
  (hq-suin); dispatcher skips idle dogs with leaked tmux sessions (gt-o24);
  `stuck-agent-dog` uses `gt hook show` to inspect other agents' hooks.
- **Test infrastructure** — Don't skip non-Dolt daemon tests when Docker is
  unavailable (gt-kw4449); enforce standalone formula singleton at sling
  boundary (gt-3kir); `gt prime` renders formula steps for both town- and
  rig-level formulas; nightly integration test failures fixed for wl-commons
  (closes antns1/fergus#336).
- **Lint** — Correct British spellings to American English; `gt status` no
  longer hangs in `bd` probe.
- **Plugin portability** — `dolt-archive` and `dolt-backup` no longer rely on
  the bash 4 `mapfile` builtin, restoring production DB discovery on macOS
  systems where `/bin/bash` (3.2.57) is invoked.

## [1.0.1] - 2026-04-25

### Fixed

- **`gt dog done` closes accumulated plugin mails** — Plugin dispatch mails sent
  by the daemon to dogs were never closed after execution, causing dog inboxes
  to accumulate hundreds of open "Plugin: X" beads. On every UserPromptSubmit
  hook, `gt mail check --inject` re-injected ALL open mails, ballooning agent
  context to 60-70% and causing compaction/hook conflicts that froze the deacon.
  `gt dog done` now archives all open "Plugin: " mails from the dog's inbox
  before clearing work and terminating the session.

## [1.0.0] - 2026-04-02

### Added

- **Windows platform support** — Cherry-picked Windows support: platform-specific
  signal handling, process management, tmux descendant tracking, and estop split
  into OS-specific files.
- **Workflow formula type** — `gt formula run` now supports `type = "workflow"`
  formulas with interactive step execution.
- **Refinery PR merge strategy** — New `merge_strategy=pr` option uses `gh pr merge`
  instead of direct push, enabling GitHub's native merge queue.
- **`/crew-commit` skill** — Canonical crew commit workflow as a Claude skill.
- **Rate-limit watchdog plugin** — Auto-estop on API 429 rate limit errors.
- **`gt mail send --from` flag** — Relay/bridge use case for mail forwarding.
- **`gt mail mark-read --all`** — Mark all inbox messages as read at once.
- **`RequireTownEnv` test helper** — Integration test guard with documentation
  for GH#2717.
- **Prefix collision checking** — `gt rig add` and `gt rig adopt` now detect
  prefix collisions before creation.
- **Bead description in PR body** — PR body now includes bead description and
  diff stat for richer context.
- **Dolt commit freshness health check** — Dolt health metrics now include commit
  freshness monitoring.
- **Default effort level config** — `CLAUDE_CODE_EFFORT_LEVEL` configurable for
  all agents.
- **`gt dolt pull`** — New command for pulling Dolt remotes.

### Changed

- **CI: Windows smoke tests** — Replaced Windows unit tests with lighter smoke
  tests for faster CI.
- **Refinery requires review** — `require_review=true` now blocks merge until PR
  has an approving review.
- **Mayor approval for scope expansion** — Polecats must get mayor approval
  before expanding molecule scope.
- **Polecat PreToolUse guard** — Blocks `sudo` and system package installs in
  polecat sessions.
- **Patrol formulas use rig-prefixed vars** — Template variables for agent bead
  IDs are now rig-prefixed.
- **Polecat auto-checkout** — Sessions auto-checkout a fresh branch when started
  on the default branch.
- **Makefile OOM fixes** — Strip flags and codesign removal to prevent OOM kills
  during builds.

### Fixed

- **SQL injection in dolt_remotes** — Escaped SQL string in remote name query
  (security fix).
- **ACP integration test flakiness** — Resolved CleanExit and FullLoop test
  races.
- **Witness zombie detection** — Distinguish bead lookup failure from
  closed/reaped beads.
- **Scheduler capacity counting** — Exclude idle polecats from capacity count.
- **Nested town root detection** — `FindTownRoot` now returns outermost town
  root for nested rigs.
- **Convoy +Inf metadata** — Fix detection and flip-flop in convoy metadata.
- **Carry branch builds** — Support `carry/*` branches in build infrastructure.
- **Unsigned binary handling** — Refuse to run unsigned binary instead of just
  warning.
- **Shell hook shebang** — `shell-hook.sh` shebang now matches registered shell.
- **Sling context routing** — Route sling-context wisp to target rig instead of
  HQ.
- **Feed timestamps** — Display feed timestamps in local timezone instead of UTC.
- **Crew status across rigs** — `gt crew status` now shows all rigs.
- **Polecat CLAUDE.md commit guard** — Prevent polecats from committing Gas Town
  overlay CLAUDE.md.
- **PR branch deletion guard** — Guard PR branch deletion and add review approval
  check.
- **Lint fixes** — Resolve unconvert, unparam, and misspell warnings.
- **Git identity in worktrees** — Propagate global git identity into polecat
  worktrees.
- **Sparse-checkout deletions** — Ignore sparse-checkout deletions in git status.
- **Beads config parsing** — Ignore `(not set)` in beads config output.
- **Plugin heartbeat path** — Check `heartbeat.json` instead of legacy
  `.deacon-heartbeat`.
- **Dog mail race condition** — Send dog mail before session start to prevent
  race.
- **Polecat dashboard drops** — Use local prefix registry in dashboard
  FetchWorkers.
- **Dolt TCP ping fallback** — Always TCP-ping dolt port as last resort in
  IsRunning.
- **ENABLED_RIGS unbound variable** — Initialize array to avoid error with
  `set -u`.
- **Claude project dir path encoding** — Encode underscores as hyphens in
  project directory path.
- **Hook template PATH export** — Replace `export PATH` with `{{GT_BIN}}` in all
  hook templates.

## [0.13.0] - 2026-03-29

### Added

- **Directives and overlays** — New `gt prime` directive loader and overlay
  system with CLI commands (`gt directive`, `gt overlay`), formula overlay
  support, and doctor health check for overlay integrity.
- **Gate bead instruction template** — Gate beads now carry structured
  instruction templates with GitHub API client support.
- **Merge queue step dependencies** — `gt mq submit` enforces molecule step
  dependency ordering before submission.
- **Convoy watch/unwatch** — `gt convoy watch` and `gt convoy unwatch` for
  opt-in completion notifications on convoy progress.
- **Convoy merge queue panel** — Feed view now shows merge queue status in
  convoy panels.
- **Patrol scan CLI** — `gt patrol scan` detects zombie and stalled polecats
  from the command line.
- **Checkpoint dog** — New `checkpoint_dog` auto-commits WIP changes in
  polecat worktrees periodically.
- **Crash recovery on startup** — `gt up` detects and recovers orphaned hooked
  beads left by crashed sessions.
- **Post-squash gate phase** — Refinery adds a pre-push validation gate after
  squash merging.
- **Refinery auto\_push config** — New `auto_push` rig setting controls whether
  refinery pushes after merge.
- **PR feedback patrol formula** — `mol-pr-feedback-patrol` formula for
  automated PR review triage.
- **Configurable tmux theming** — Window tint and `window-style` theming with
  resolver; Mayor gets terminal-default theme.
- **`gt changelog` command** — Generate changelogs from the CLI with tests.
- **Wasteland stamps and pilot cohorts** — `gt wl stamp`, `gt wl stamps`
  commands and `pilot_cohort` column for HOP pilot program.
- **Wasteland scorekeeper** — Charsheet, scorekeeper, and stamp loop
  integration tests.
- **`gt wl show <work-id>`** — Structured work-item detail view with
  auto-fetch.
- **`gt default-agent list`** — New subcommand to list available agent presets.
- **Disabled patrols setting** — `disabled_patrols` town config to suppress
  patrols without editing daemon.json.
- **Dolt failover/failback** — Multi-host Dolt setups can failover and
  failback between primary and replica.
- **`.no-sync` marker files** — Drop a `.no-sync` file in a database directory
  to exclude it from sync.
- **`/done` slash command** — Polecats can invoke `/done` with a Stop hook
  safety net for clean lifecycle exit.
- **Sling `--review-only` flag** — Prevent assignee from merging; report back
  only.
- **Copilot agent support** — GitHub Copilot CLI documented and preset updated
  for GA release (Feb 2026).
- **Unique polecat namepool** — Polecat names are now globally unique across
  rigs via shared namepool with auto-assigned themes.
- **Handoff restart prompt** — `gt handoff` now prompts the user before
  restarting the session.
- **Patrol effort tuning** — Idle patrol cycles now run at reduced reasoning
  effort; configurable per-formula with `effort_idle` and `effort_active`.
- **Longer patrol backoff** — Max backoff increased from 5m to 15m for idle
  patrols, reducing cost by ~66% for dormant rigs.
- **Formula/path discoverability** — Reference docs for formulas, beads CLI,
  and Dolt injected into agent context to eliminate discovery tax.

### Changed

- **Beads dependency** upgraded to v0.62.0.
- **Compactor-dog threshold** — Default compaction threshold raised from 500 to
  2000 to reduce unnecessary compactions.
- **Dolt startup timeout** — Scales dynamically by database count (5s per DB)
  instead of fixed timeout.
- **Dolt SIGTERM→SIGKILL timeout** — Increased from 5s to 30s for graceful
  shutdown of large databases.
- **Polecat CLAUDE.md provisioning** — Lifecycle instructions provisioned on
  all spawn paths including worktree reuse, with `gt done` reminders injected
  at startup and after compaction.
- **Boot and dog cost tiers** — Boot and dog roles now tracked in the cost tier
  system.
- **Plugin database discovery** — Plugins auto-discover databases instead of
  using hardcoded lists; reaper uses `DiscoverDatabases` with proper error
  handling.
- **Dolt `dolt_transaction_commit` disabled** — Prevents read-only commit
  storms on busy servers.

### Fixed

- **Daemon beads compatibility guard** — `gt daemon run` now fail-fast checks
  workspace beads schema compatibility before Convoy polling starts, and
  `gt daemon start` surfaces the startup mismatch directly instead of only
  telling operators to inspect logs.
- **Dolt server stability** — Fixed thundering herd in `doltserver.Start()`,
  port-squatter detection and kill on startup, `cmd.Dir` set on all CLI/SQL
  invocations to prevent stray `.doltcfg` directories, and timing race in
  startup sequence.
- **Security hardening** — Bead ID suffix validation enforced, formula
  variables use bead IDs instead of user-supplied titles, and
  `--subject`/`--args` sanitized before tmux pane injection.
- **Tmux reliability** — Replaced timing-based Enter delivery with
  verification-based retry, detect and dismiss Claude Code Rewind menu during
  nudge delivery, restored per-town socket isolation, and added flock-based
  cross-process nudge lock to prevent interleaved delivery.
- **Windows fixes** — Atomic counter in `generateStampID` for timer resolution,
  pipe deadlock prevention in `prime_test`, process group test skips, and
  multiple CI test stabilizations.
- **Polecat lifecycle** — Skip crash/zombie alerts for done/nuked polecats,
  use `IsIdle` instead of `IsAtPrompt` for startup nudge verify, clean dirty
  worktree before reuse, kill session unconditionally when reusing idle
  polecats, and wire operational config into startup nudge loop.
- **Refinery fixes** — Use commit SHA instead of branch name for MR dedup,
  supersede MR on same-branch re-submission, check `no_merge` flag before
  merging, close task beads after successful merge, wait for CI in PR mode,
  and filter MR listings by rig to prevent cross-rig contamination.
- **Convoy fixes** — Use Unix epoch instead of zero time for initial event poll,
  stranded scan checks completion status, create legs in target rig beads,
  and cross-rig dependency routing uses town root.
- **Cross-town safety** — Prevent orphan cleanup from killing agents on other
  towns' sockets, distinguish sibling Gas Town instances from test zombies.
- **Dog and daemon** — Clear agent identity env vars at startup, prevent
  duplicate Mayor spawns during `gt up`, auto-clear hung dogs and orphan
  sessions, include dogs in startup retry loop, prevent daemon restart during
  `gt down`, and respect global default agent for dog spawns.
- **Doctor improvements** — Avoid slow `filepath.Walk` on Docker bind mounts,
  stale `sql-server.info` detection, hooks-sync check detects stale Gemini
  settings, route misclassified wisp fixes by workdir, and repair relocated
  worktree gitdir paths.
- **Mail and communication** — Drain crashed polecat notifications, prefer
  `GT_TOWN_ROOT` env var for town root detection, fall back to explicit agent
  workspaces for mail delivery.
- **Dolt plugins** — `dolt-archive` uses `while-read` loops for bash 3.2
  compatibility (macOS), `dolt-backup` uses `$HOME/gt` as `GT_ROOT` fallback,
  named Docker volume prevents journal corruption on macOS, and `grep -v`
  exit code handled under `pipefail`.
- **Formula and molecule** — Cap backoff before overflow in `await-event` and
  `await-signal`, inject `merge_strategy` from rig settings into formula vars,
  propagate `base_branch` to MR target in `gt done` and `gt mq submit`.
- **Sling** — Serialize concurrent hook writes with per-assignee flock,
  `--dry-run` detects tmux session collision before spawn, guard `sha[:8]`
  slice against short hashes.
- **Config and identity** — Dog sessions inherit env vars from base agent, custom
  agents inherit Session/Tmux from preset, `CLAUDE_CONFIG_DIR` respected in
  `gt costs`, rig prefix pattern refresh when stale, propagate
  `BEADS_DOLT_SERVER_HOST` to subprocesses, and repair PROJECT IDENTITY
  MISMATCH after crash.
- **Guard and compliance** — Block polecats from pushing directly to main.
- **Misc** — `formatPeriod` returns "Week of" on Mondays instead of "Today",
  sync `agent_state` between column and description on transitions, validate
  git URL before crew clone, `--flat` flag on all `bd list --json` calls to
  guarantee JSON output, `gt upgrade` repairs missing identity beads, and
  `CLAUDE.local.md` added to gitignore patterns.

### Removed

- **Session-hygiene plugin** — Removed entirely after causing repeated crew
  session kills.
- **`--no-history` flag** — Removed from identity bead creation in favor of
  proper ephemeral bead support.
- **Hardcoded database lists** — Reaper and plugin database discovery replaced
  with dynamic `DiscoverDatabases`.
- **Legacy `gt` database** — Removed from reaper fallback list.

## [0.12.1] - 2026-03-15

### Added

- **Agent Client Protocol (ACP)** — New protocol for structured agent
  communication with propulsion trigger detection and output suppression.
- **gt mountain** — Stage, label, and launch epic work in one command.
- **gt assign** — One-shot bead creation + hook for direct agent assignment.
- **Convoy --from-epic** — `gt convoy create --from-epic` stages epic children
  into convoy waves with automatic validation bead.
- **Typed memories** — `gt remember --type feedback/project/user/reference` for
  categorized agent memory storage.
- **Repo-sourced rig settings** — `.gastown/settings.json` in repos auto-configures
  rig behavior (test gates, merge strategy).
- **exec-wrapper plugin type** — Plugins can now wrap agent execution.
- **Prior attempt context** — Polecats receive context from previous failed
  attempts when re-dispatched.
- **Spider Protocol** — Fraud detection for Wasteland stamp system.

### Changed

- **Reaper plugin receipt cleanup** — Plugin run receipts now fast-tracked for
  closure (1h) instead of waiting for 7-day stale issue AutoClose.
- **Dog dispatch handler** — Daemon lifecycle defaults include handler for
  direct dog dispatch.
- **Formula v2** — mol-idea-to-plan with iterative review rounds and inline
  eval/smoke-test bead creation.

### Fixed

- **Idle patrol CPU burn** — Patrol agents no longer burn CPU/tokens in handoff
  restart loops.
- **Compactor-dog false positives** — Fixed concurrent write detection and hash
  validation for Dolt base32 format.
- **Dolt server stability** — Fixed stale socket cleanup, server ownership
  detection, rogue process race on restart, idle-monitor orphans on `gt down`.
- **Cross-rig wisp contamination** — MQ list filtered by rig to prevent leaks.
- **Polecat lifecycle** — Fixed idle reuse with live sessions, CRASHED_POLECAT
  alerts for closed beads, spawn storm dedup.
- **Session prefix parsing** — Fixed hq- prefix collision and rig-level fallback.
- **Unicode handling** — Fixed parse errors in `gt compact`.
- **Non-Claude agent support** — Liveness env vars, idle-wait instructions, and
  nudge startup prompts for Gemini/Codex runtimes.
- **Test isolation** — 5 tests isolated from live Dolt server; sleep sessions
  used in cleanup tests to avoid .zshrc interference.
- **Witness completion notifications** — Mayor now notified on polecat completion.
- **Shell quoting** — Agent args properly quoted, model flags respected.
- **Exponential backoff** — Convoy event poller backs off on Dolt errors.
- **Docker** — Added tini for zombie process reaping in containers.

## [0.12.0] - 2026-03-11

### Added

- **Event-driven polecat lifecycle** — Polecats now use FIX_NEEDED feedback loop
  with awaiting_verdict state, replacing polling-based lifecycle (gt-k0h).
- **Cross-database convoy resolution** — CLI-side dep resolution for multi-rig
  towns where bd SQL JOINs fail across databases (GH#2624, GH#2625).
- **Plugin sync** — `gt plugin sync` auto-deploys plugins after build (hq-o9gna).
- **Compactor dog** — Executable `run.sh` for Dolt database compaction with
  DoltHub remote sync, validation, and dry-run support.
- **GitHub sheriff v2** — Single API call PR categorization with structured output.
- **Mail reply reminders** — Deferred nudge delivery for unanswered mail.
- **Git hygiene dog** — Automated repo cleanup plugin (gt-cdm).
- **Crew agent assignment** — Town-level `crew_agents` config for per-crew
  agent runtime selection.
- **Partial clones** — `gt rig add` supports `--reference` for submodule init
  and sparse checkout.
- **Formula composition** — `extends` and `compose/expand` support for formulas.
- **Background nudge poller** — Queue-based nudge delivery for non-Claude agents.
- **Review command** — `/review` with A-F grading and refinery integration (#2636).
- **Escalation channels** — Email, Slack, SMS, and log notification channels.
- **Pressure checks** — Opt-in CPU/memory pressure gating before agent spawns.
- **MVGT integration guide** — Comprehensive Wasteland federation guide for
  non-Gas-Town systems.
- **Crew specialization design** — Capability-based dispatch design doc.

### Changed

- **Refinery merge strategy** — Configurable direct vs PR mode (gt-fln).
- **Polecat lifecycle patrol** — Redesigned formula for event-driven model.
- **Session hygiene** — Converted from plugin.md to deterministic `run.sh`.
- **DND auto-reset** — Muted mode auto-resets on `gt up`.
- **Nudge degradation** — Wait-idle gracefully degrades to queue for agents
  without prompt detection.

### Fixed

- **Install bootstrap** — `gt install` now waits for MySQL readiness and always
  passes `--server-port` to `bd init` (GH#2572, GH#2573).
- **Rig add database creation** — `InitRig` runs CREATE DATABASE on live server
  before schema migration.
- **Boot triage loop** — Removed ZFC-violating decision engine that consumed
  unbounded tokens on failed installs.
- **Polecat spawn storm** — Two-layer circuit breaker caps respawns and total
  active polecats (clown show #22).
- **Standing-order beads** — Protected from AutoClose reaper and polecat
  removal status reset.
- **Tmux socket split-brain** — Prevented nudge failures from socket mismatch
  (GH#2442).
- **Reaper Sprintf bugs** — Fixed format string issues and missing schema guard
  (GH#2469).
- **bd JSON corruption** — Strip bd stdout warnings before JSON parsing.
- **Remote branch deletion** — Restricted to polecat branches only (GH#2669).
- **EnsureAllMetadata** — Uses rig name and correct DB prefix (GH#2668).
- **Ephemeral beads** — Auto-purge closed ephemeral beads on session end.
- **MR back-link** — Source issue linked to MR bead on creation.
- **Convoy routing** — Town root and BEADS_DIR properly stripped for bd
  subprocess calls.
- **CI stability** — Resolved lint warnings (unparam, misspell) and 5 test
  failures on main.

## [0.11.0] - 2026-03-05

### Added

- **Docker support** — docker-compose and Dockerfile for containerized deployment.
- **Cursor hooks** — Polecat agent integration for Cursor IDE sessions.
- **Context-budget guard** — External script to prevent context window overflow (#2008).
- **Cascade close** — `bd close --cascade` closes parent and all children with cycle
  guard and depth limit (#998).
- **Schema evolution** — `gt wl sync` supports Wasteland schema changes (gp-c7e).
- **Dashboard enrichment** — Convoy panel shows progress %, ready/active counts,
  and assignees.
- **Polecat slot env** — `POLECAT_SLOT` environment variable for test isolation (#954).

### Changed

- **Beads dependency** upgraded from v0.57.0 to v0.59.0.
- **Hook installers consolidated** — Per-agent hook installer packages replaced with
  generic declarative system (gt-071h).
- **Agent preset registry** — Hardcoded `isKnownAgent` switch replaced with
  `config.IsKnownPreset` (gt-7r3c).
- **Reaper TTLs shortened** — Auto-close 7d, purge 3d (previously longer).
- **`CreateOptions.Type` deprecated** in favor of Labels.

### Removed

- **`gt swarm` command** — Deprecated command and `internal/swarm` package removed (#1170).
- **Beads Classic legacy code** — Remaining SQLite/JSONL/sync code paths removed.
- **Vestigial `sync.mode` plumbing** — Dead config removed.

### Fixed

- **Serial killer bug** — Removed hung session detection that was killing healthy
  witnesses and refineries (f3d47a96). Stuck agent detection moved to Dog plugin
  (5a5deaac).
- **Sling race condition** — Hook write visibility ensured before polecat startup
  (GH#2389).
- **Refinery** — PostMerge now uses `ForceCloseWithReason` for source issue (GH#2321).
- **Crew mail prefix** — Regression test added for crew mail send prefix mismatch
  (gt-brip).
- **bd JSON guard** — Non-JSON output from bd v0.58.0 handled in remaining parsers
  (gt-ac0i).
- **CI release guards** — Blocks `go.mod` replace directives in releases (gt-qex2).
- **go vet** — Pre-existing failures on main resolved (gt-77xe).
- **Branch contamination** — Preflight check added to `gt done` (#2220).
- **Polecat nuke** — Uses `ClonePath` for best-effort push (hq-9pcb0).
- **Polecat state** — JSON list state reconciled with session liveness.
- **Convoy** — External tracked IDs resolved during launch collection.
- **`gt done`** — Correct rig used when Claude Code resets shell cwd. Tolerates
  Gas Town runtime artifacts in worktrees (#2382).
- **Dolt server** — Server-side timeouts prevent CLOSE_WAIT accumulation (#2287).
- **Daemon** — 5-minute grace period before auto-closing empty convoys (GH#2303).
- **Sling TTL** — Prevents permanent scheduling blocks (GH#2279).
- **Tmux** — Refresh cycle bindings when prefix pattern is stale (#2300).
- **Patrol** — Cap stale cleanup and break early on active patrol found (#2285).
- **Reaper** — Correct database name; O(n*m) correlated EXISTS replaced with LEFT
  JOIN anti-pattern.
- **Hook show** — Normalized targets; prefer hooked bead over stale agent hook.
- **Tmux socket** — Derived from town name instead of defaulting to "default".
- **Gitignore** — Broadened patterns for Cursor runtime artifacts and Gas Town
  infrastructure directories.
- **Rig remove** — Shows actionable guidance for orphaned rig directories.
- **CI** — Lint errors, Windows test failures, proxy log truncation fixed.
- **Mayor clone** — Reuses bare repo as reference for faster cloning (#1059).
- **Prefix registry** — Reloaded on heartbeat to prevent ghost sessions (#2338).
- **Dog molecule** — JSON parsing fix for `bd show --children` output.
- **`--allow-stale`** — Made conditional on bd version support.

## [0.10.0] - 2026-03-03

_Release contained incremental fixes between v0.9.0 and v0.10.0. See git log for details._

## [0.9.0] - 2026-03-01

### Added

- **Batch-then-bisect merge queue** — Bors-style MQ batches MRs, runs tests on
  the tip, and binary-bisects on failure. GatesParallel runs test + lint
  concurrently per MR.
- **Persistent polecats** — Polecat identity and sandbox survive across work
  assignments. Sessions are ephemeral; identity accumulates forever. `gt done`
  transitions to idle instead of nuking.
- **Compactor Dog** — Daily Dolt commit history flattening via `DOLT_RESET --soft`.
  Runs on live server, no downtime. Configurable threshold. Includes surgical
  interactive rebase for advanced use.
- **Doctor Dog** — Automated health monitoring patrol for Dolt server. Detects
  zombies, orphan databases, and stale locks. Reports structured data for agent
  decision-making (ZFC-compliant).
- **JSONL Dog** — Spike detection and pollution firewall for JSONL backup exports.
  Scrubs test artifacts before git commit.
- **Wisp Reaper Dog** — Automated wisp GC with DELETE of closed wisps >7d, auto-close
  of stale issues >30d, and mail purge >7d.
- **Root-only wisps** — Formula steps no longer materialized as individual database
  rows. Single root wisp per formula. Cuts ~6,000 ephemeral rows/day from Dolt.
- **Six-stage data lifecycle** — CREATE, LIVE, CLOSE, DECAY, COMPACT, FLATTEN,
  all automated via Dogs. `EnsureLifecycleDefaults()` auto-populates daemon.json
  on startup.
- **`gt maintain`** — One-command Dolt maintenance (flatten + gc).
- **`gt vitals`** — Unified health dashboard command.
- **`gt upgrade`** — Post-binary migration command for config propagation.
- **`gt convoy stage`** — Stage convoys with `--title` flag and smart defaults.
- **`gt mol await-signal`** — Alias for step-level signal awaiting.
- **`gt daemon clear-backoff`** — Reset exponential backoff on daemon restarts.
- **`gt mq post-merge`** — Branch cleanup after refinery merge.
- **`gt mail drain`** — Drain inbox command.
- **Sandbox sync** — Branch-only polecat reuse with `pool-init`.
- **Per-worker agent selection** — `worker_agents` config for crew members.
- **Dangerous-command guard hook** — Blocks `cp -i`, `mv -i`, `rm -i` in agent sessions.
- **Log rotation** — Daemon and Dolt server logs now rotate with gzip compression.
- **Did-you-mean suggestions** — Unknown subcommands suggest closest match.
- **`gt version --verbose/--short`** — Version display options.
- **Town-root CLAUDE.md version check** — `gt doctor` detects stale CLAUDE.md files.
- **Lifecycle defaults doctor check** — Detects missing daemon.json patrol entries.

### Changed

- **OperationalConfig for config-driven thresholds (ZFC)** — Hardcoded thresholds
  for hung sessions, stale claims, max retries, GUPP violations, and crash-loop
  backoff are now in `operational.json`. Go code reads config; agents set values.
- **Nudge-first communication** — Protocol messages (LIFECYCLE, WORK_DONE,
  merge notifications) changed from permanent mail to ephemeral wisps or nudges.
  Reduces Dolt commit volume by ~80% for patrol traffic.
- **Default Dolt log level** changed from debug to info.
- **Convoy IDs** shortened from 5 chars to 3.
- **Mail purge age** reduced from 30 days to 7 days.
- **Compactor threshold** bumped from 500 to 10,000 commits.
- **Default max Dolt connections** bumped from 200 to 1,000.
- **Dolt flatten runs on live server** — No maintenance window needed.

### Fixed

- **Mail delivery** — Fixed `--id` flag breaking all mail creation paths. Fixed
  recipient validation to include pinned and inactive agents. Added auto-nudge
  on mail delivery to idle agents.
- **Witness patrol** — Stopped nuking idle polecats. Stopped auto-closing permanent
  issues (ZFC violation). Replaced screen-scraping with structured signals for
  stalled polecat detection.
- **Polecat lifecycle** — Prevent sleepwalking completions with zero commits. Normalize
  CWD to git root before beads resolution. Preserve remote branches when MR pending.
  Close orphan step wisps before closing root molecule.
- **Refinery** — Removed dead `findTownRoot` filesystem inference (ZFC). Removed
  hardcoded severity/priority logic. Made stale-claim thresholds config-driven.
  Extracted typed `ConvoyFields` accessor.
- **Boot triage** — Removed ZFC-violating decision engine from degraded boot triage.
- **PID detection** — Replaced PID signal probing with heartbeat-based liveness.
  Replaced ps string matching with nonce-based PID files. Replaced per-PID pgrep
  with single ps-based process tree scan.
- **tmux** — Use default socket instead of per-town socket. Set `window-size=latest`
  on detached sessions. Auto-dismiss workspace trust dialog blocking agent sessions.
- **Convoy** — Prevent auto-close of stuck convoys. Recovery sweep after Dolt
  reconnect. Check parked AND docked rigs in dispatch. Prevent double-spawn from
  stale feed. Idempotent close handling.
- **Doctor** — Fixed silent failure for agent-beads and misclassified-wisps checks.
  Repair beads redirect targets with missing config.yaml. Handle worktree branch
  conflicts. Skip rig dirs whose .beads symlinks to town root.
- **Test isolation** — Migrate test infrastructure to testcontainers. Ephemeral Dolt
  server per test suite. `BEADS_TEST_MODE=1` enforcement. Dropped 45 zombie test
  servers (7GB RAM).
- **Rig lifecycle** — Enforce dock/park across all startup, patrol, and sling paths.
  Dogs correctly use plugin lookup. Formula lookup falls back to embedded for
  non-gastown rigs.
- **Handoff** — Deterministic git state in handoff context. Fix socket confusion
  using caller socket. Preserve conversation context on PreCompact cycle.
- **Windows** — Cross-compilation fixes for `syscall.Flock`, `syscall.SIGUSR2`,
  bash-dependent tests, `GOPATH/bin` creation, batch mock caret escaping.
- **Hardcoded strings** — Replaced ~50 hardcoded state/status string comparisons
  with typed enums across witness, refinery, and beads packages.
- **CI** — Fixed lint errors (errcheck, misspell) and integration test port collision.

## [0.8.0] - 2026-02-23

### Added

#### Work Queue & Dispatch Engine
- **`gt queue` CLI and dispatch engine** — Enqueue work items for daemon-driven dispatch
- **`gt queue epic`** — Bulk enqueue of epic children
- **`gt convoy queue`** — Bulk enqueue of convoy-tracked issues
- **`gt sling --queue`** — Enqueue path for asynchronous dispatch
- **Queue daemon heartbeat step** for reliable dispatch processing
- **Enqueue-time validation** and enhanced metadata on queued items
- **Config-driven capacity scheduler** with sling context beads

#### Telemetry & Observability (OTEL)
- **Full OpenTelemetry instrumentation** across gt, bd, and Claude Code
- **VictoriaMetrics and VictoriaLogs integration** for metrics and log export
- **Lifecycle events, duration histograms, and operation traces**
- **Claude Code tool content and user prompt logging** via OTLP
- **`gt.* OTEL` resource attributes**, prime context events, and session metadata
- **Command usage telemetry** and `gt metrics` reader

#### Dog Subsystem (Deacon Workers)
- **Handler patrol** for Dog lifecycle and plugin dispatch
- **Session-hygiene Dog plugin** for zombie tmux cleanup
- **Idle dog reaping** — Kill stale tmux sessions and trim oversized pools
- **Stale working dog detection** for dogs stuck with idle sessions
- **Wisp compaction formula** for Dogs
- **Shutdown dance molecule** — Death warrant execution state machine for Dogs

#### Wisps & Ephemeral Storage
- **Wisps table migration** for agent beads (ephemeral SQLite store)
- **Agent bead readers updated** to query wisps table
- **Closed wisp GC** added to patrol cycles to prevent accumulation

#### Convoy & Sling Improvements
- **`gt convoy stage` and `gt convoy launch`** commands
- **Auto-resolve rig from bead prefixes** in batch mode
- **Reject deferred/post-launch beads** at sling time
- **Auto-check convoy completion** on `bd close`
- **Deacon feed-stranded command** for auto-feeding stranded convoys
- **Notify deacon of convoy-eligible merges** for immediate feeding

#### Witness & Refinery
- **Configurable quality gates** before merge in refinery
- **MR queue nonempty tracking** in witness check-refinery step
- **Bead respawn count tracking** for spawn storm detection
- **Verify MR bead exists** before sending MERGE_READY
- **Refinery nudges deacon** after merge to check stranded convoys
- **Auto-close completed convoys** after merge

#### Agent Providers & Runtime
- **Pi agent provider** with unit tests
- **Promptfoo model comparison framework** for patrol agents
- **Cost-tier presets** for model selection
- **Ralphcat opt-in loop mode** for multi-step workflows

#### Dashboard & UX
- **Rename Workers panel to Polecats** in dashboard
- **Session terminal preview** in dashboard Sessions panel
- **Mail threading** in dashboard inbox
- **Convoy drill-down** — Expand rows to show tracked issues
- **Date+time timestamps** instead of relative ago format

#### CLI & Workflow
- **`gt handoff --cycle`** — Full session replacement flag
- **`gt handoff --agent`** — Explicit runtime selection
- **`gt crew start --resume`** — Resume flag for crew sessions
- **`gt crew at --reset`** — Branch switch opt-in with reset flag
- **`gt deacon pending`** — AI-based spawn observation command
- **`gt patrol step-drift`** subcommand
- **`gt up --json`** output flag
- **`gt migrate-bead-labels`** command
- **Desire-path fixes** for rig settings, crew list, mail directory
- **Idle-aware notification** in mail router with auto-nudge on send

#### Infrastructure
- **WorktreeCreate/WorktreeRemove hook event support**
- **Remote Dolt server support** (`gt dolt`)
- **Dolt server identity verification and restart command**
- **Orphaned Dolt branch cleanup**
- **Nix flake** and integration with bump-version script
- **Boot block scraper VM deployment script**
- **Beads-redirect-target doctor check**
- **Unified zombie session detection** with 3-level health check
- **Emit-event command** and nudgeRefinery event wiring
- **`gt mol step await-event`** for channel-based event subscription
- **Doctor check** for in_progress beads with NULL assignee
- **PR creation guard** blocking direct pushes to steveyegge/gastown
- **Orphan scan** for polecat worktrees with unmerged branches
- **Wasteland CLI command suite** (`gt wl`)
- **GitHub Sheriff Deacon plugin** for CI failure monitoring

### Changed

- **Removed Dolt branch-per-polecat infrastructure** — Dead code from pre-server-mode era
- **Removed BD_BRANCH from session manager and polecat spawn** — Server mode eliminates branch isolation
- **Removed JSONL fallback code** — Dolt is now the sole backend
- **Removed deprecated role bead TTL layer** from compact
- **Removed deprecated role bead lookups** that blocked witness startup
- **Stale JSONL references** cleaned from documentation and comments
- **Molecule squash `--no-digest`** flag to skip digest creation
- **Dog startup honors role_agents config**
- **Handoff restart honors role_agents config**

### Fixed

#### Dolt & Storage Stability
- **Replace dolthub/dolt with nil-check fork** to fix SEGVs
- **Prevent polecat spawns from creating orphan databases**
- **Drop orphaned `beads_<prefix>` databases** after `bd init`
- **Isolate Dolt integration tests** from production server
- **Dynamic port for test Dolt server** to avoid production collision
- **Restore BEADS_DIR stripping** in `buildRunEnv()` lost in merge
- **Strip BD_BRANCH from wisp creation and cook steps**

#### Agent & Session Management
- **LookupEnv for GT_AGENT** to prevent tmux env contamination
- **KillSession idempotent** + ensureMayorInfra on attach
- **Detect non-polecat CWD** when Claude Code resets shell to mayor/rig
- **Filter getCurrentWork by assignee** to prevent shared rig beads leaking across sessions
- **Clean up stale molecules during polecat nuke** to unblock re-sling
- **Set GT_PROCESS_NAMES in tmux env** for all session types
- **Resolve agent config from --agent override** for Codex polecat startup
- **Tell polecats to close bead explicitly** when nothing to commit
- **Silent costs-record skip** for non-GT sessions

#### Test Infrastructure
- **Flaky TestFindActivePatrol tests** caused by bd daemon contamination
- **Isolated patrol tests** from shared Dolt database
- **Convoy/epic test failures** and TestValidateRecipient resolved
- **Extract requireDoltServer** into shared testutil package
- **gotestsum + JUnit test failure reporting** in CI

#### Patrol & Boot
- **Remove redundant Status.Running field** (ZFC compliance)
- **Prefer gastown repo over crew rigs** in stale binary check
- **Session-name-format doctor check** for outdated session names
- **Handoff warn on uncommitted/unpushed work** before session cycling
- **Helpful error for `gt mail read` with no args** instead of cobra usage

## [0.7.0] - 2026-02-15

### Added

#### Convoy Ownership & Merge Strategies
- **Convoy ownership model** — `--owned` flag for `gt convoy create` and `gt sling`
- **Merge strategy selection** — `--merge` flag with `direct`, `mr`, and `local` strategies
- **`gt convoy land`** — New command for owned convoy cleanup and completion
- **Skip witness/refinery registration** for owned+direct convoys (faster dispatch)
- **Ownership and merge strategy display** in `gt convoy status` and `gt convoy list`

#### Agent Resilience & Lifecycle
- **`gt done` checkpoint-based resilience** — Recovery from session death mid-completion
- **Agent factory** — Data-driven preset registry replaces provider switch statements
- **Gemini CLI integration** — First-class Gemini CLI as runtime adapter
- **GitHub Copilot CLI integration** — Copilot CLI as runtime adapter
- **Non-destructive nudge delivery** — Queue and wait-idle modes prevent message loss
- **Auto-dismiss stalled polecat permission prompts** — Witness detects and clears stuck prompts
- **Dead crew agent detection** — Detect dead crew agents on startup and restart them
- **Remote hook attach** — `gt hook attach` with remote target support

#### Dashboard & Web UI
- **Rich activity timeline** — Chronological view with filtering
- **Mobile-friendly responsive layout** — Dashboard works on small screens
- **Toast notifications and escalation actions** — Interactive escalation UI
- **Escape key closes expanded panels** — Keyboard navigation improvement

#### Witness & Patrol
- **JSON patrol receipts** for stale/orphan verdicts — Structured patrol output
- **Orphaned molecule detection** — Detect and close orphaned `mol-polecat-work` molecules
- **IN_PROGRESS beads assigned to dead polecats** — Automatic detection and recovery
- **Deterministic stale/orphan receipt ordering** — Consistent patrol results

#### Infrastructure
- **Submodule support** — Worktree and refinery merge queue support for git submodules
- **Merge queue `--verify` flag** — Detect orphaned/missing merge queue entries
- **Cost digest aggregate-only payload** — Fixes Dolt column overflow
- **Rig-specific beads prefix for tmux session names** — Better multi-rig session isolation
- **Mayor GT_ROLE Task tool guard** — Block Task tool for Mayor via GT_ROLE check
- **Server-side database creation** during `gt rig add` with issue_prefix setup

### Changed

- **Beads Classic dead code removed** — -924 lines of SQLite/JSONL/sync code eliminated
- **Agent provider consolidated** — Data-driven preset registry replaces switch statements
- **Session prefix renamed** — Registry-based prefixes replace hardcoded `gt-*` patterns
- **Agent config resolution** — Moved mutex to package config for thread safety
- **Molecule step readiness** — Delegated to `bd ready --mol` instead of custom logic

### Fixed

#### Reliability & Race Conditions
- **Options cache and command concurrency** race conditions in web dashboard
- **Feed curator** race conditions with RWMutex protection
- **TUI convoy** concurrent access guarded with RWMutex
- **TUI feed** concurrent access guarded with RWMutex
- **Dolt backoff** — Thread-safe jitter using `math/rand/v2`
- **Concurrent Start()** and feed file access protection
- **Witness manager** race condition fix

#### Agent & Session Management
- **Nudge delivery** — Unique claim suffix prevents Windows race in concurrent Drain
- **Signal stop hook** — Prevent infinite loop with state-based dedup
- **Polecat zero-commit completion** blocked — Must have at least one commit
- **Molecule step instructions** — Use `bd mol current` instead of `bd ready`
- **Role inference** — Don't infer RoleMayor from town root cwd
- **Boot role bead ID** — Add RoleBoot case to buildAgentBeadID and ActorString
- **IsAgentRunning replaced with IsAgentAlive** — More accurate agent status
- **Stale prime help text** updated with town root regression tests
- **Sling validation** — Allow polecat/crew shorthand, validate before dispatch fork

#### Convoy & Workflow
- **Convoy lifecycle guards** — Extended to batch auto-close and synthesis paths
- **Empty convoy handling** — Auto-close and flag in stranded detection
- **Formalized lifecycle transition guards** for convoys

#### Rig & Infrastructure
- **Rig remove kills tmux sessions** — Clean up sessions on rig removal
- **Rig adopt** — Init `.beads/` when no existing database found
- **Revert shared-DB for untracked-beads rigs** — Fixes bead creation breakage
- **Install preserves existing configs** — `town.json` and `rigs.json` kept on re-install
- **Orphaned dolt server** detected and stopped during `gt install`
- **Doctor dolt check** — Uses platform-appropriate mock binaries, adds dolt binary check
- **Doctor dolt-server-reachable** reads host/port from metadata instead of hardcoding
- **IPv6 safety** and accurate rig count in doctor
- **Rig remove** aborts on kill failures, propagates session check errors
- **Spurious `go build` warning** fixed for Homebrew installs

#### Deacon & Dogs
- **Deacon scoped zombie/orphan detection** to Gas Town workspace only
- **Deacon heartbeat** surfaced in `gt deacon status`
- **Deacon loop-or-exit** step updated with squash/create-wisp/hook cycle
- **Dog agent beads** — Added description for mail routing

#### Other Fixes
- **Overflow polecat names** — Remove rig prefix
- **Witness per-label `--set-labels=` pattern** — Improved tests
- **Feed auto-disable follow** when stdout is not a TTY
- **Mail inject** — Improved output wording and test coverage
- **Codex config** — Replace invalid `--yolo` with `--dangerously-bypass-approvals-and-sandbox`
- **Cross-prefix beads routing** via `runWithRouting` for slot ops
- **Tmux `-u` flag** added to remaining client-side callsites
- **JSON output** — Return `[]` instead of `null` for empty slices
- **Windows CI** cut from ~13 min to ~4 min
- **Dolt server auto-start** in `gt start`
- 50+ additional bug fixes from community contributions

## [0.6.0] - 2026-02-15

### Added

#### Dolt-Native Architecture
- **Complete SQLite-to-Dolt migration** - Gas Town is now Dolt-native; all SQLite code removed
- **`gt dolt` command** - Server management (start, stop, status, migrate, rollback, sync, init-rig)
- **`gt install` consolidation** - Folds Dolt identity, HQ database, and server start into single command
- **Branch-per-polecat write isolation** - Each polecat gets its own Dolt branch to prevent write conflicts
- **Proactive Dolt health alerting** - Daemon monitors server health with dedicated 30s ticker
- **Auto-create DoltHub repos and configure remotes** - `gt dolt sync` pushes to DoltHub
- **Dolt remotes patrol** - Periodic push to git remotes for federation readiness
- **Max-connections and admission control** - Prevents connection storms on Dolt server

#### Dashboard & Web UI
- **Comprehensive UX overhaul** - 13 interactive data panels with crew notifications
- **SSE real-time updates** - Replaces 10s polling with server-sent events
- **Interactive command palette** - Autocomplete, recent history, contextual suggestions
- **Interactive convoy management** - Create, close, feed convoys from dashboard
- **Interactive hook management** - View and manage hooks from dashboard
- **Issue management actions** - Work panel detail view with sling buttons
- **Convoy titles alongside IDs** - Better convoy identification

#### Daemon & Supervision
- **launchd/systemd supervision support** - OS-native daemon management
- **Exponential backoff for agent restarts** - Prevents restart storms
- **Mayor daemon supervision** - Daemon manages Mayor session lifecycle
- **Boot watchdog** - Ephemeral dog that triages Deacon state on each daemon tick

#### Molecule & Workflow System
- **DAG visualization** (`gt mol dag`) - Visualize molecule dependency graphs
- **Fan-out/gather pattern** - Parallel steps in patrol workflows
- **Wisp compaction** (`gt compact`) - TTL-based wisp lifecycle management
- **Key Record Chronicle (KRC)** - Forensic value decay model for ephemeral data
- **Wisp promotion criteria** - Helpers for squash-to-persistent decisions
- **Formula variable declarations** - Proper `[vars]` sections in all formulas

#### Hooks Management
- **Centralized hook management** - `gt hooks sync`, `gt hooks diff`, `gt hooks list`
- **Per-matcher merge logic** - Fine-grained hook configuration
- **Hook registry integration** - Hooks wired into `gt rig add` and `gt doctor`

#### Agent Lifecycle
- **Persistent polecat identity model** - Agent beads survive nuke; identity accumulates forever
- **Auto re-dispatch recovered beads** - Deacon recovers work from dead polecats
- **Witness resets abandoned beads** - Dead polecat detection triggers work recovery
- **Auto-respawn hooks** - Mayor sessions survive tmux detach
- **Signal stop handler** (`gt signal stop`) - Turn-boundary messaging for clean stops
- **PID tracking for spawned agents** - Better process management

#### Convoy System
- **Completion notifications** - Push convoy completion to active Mayor session
- **Auto-close empty convoys** - Empty 0/0 convoys auto-closed on create
- **`--merge` and `--owned` flags** for `gt convoy create` and `gt sling`
- **Safety checks on `gt convoy close`** with `--force` override
- **Reactive convoy continuation feeding** - Observer auto-feeds convoys

#### CLI Improvements
- **`--stdin` flag** - Shell-quoting-safe message bodies for mail, nudge, handoff, escalate, sling
- **`--auto` flag for handoff** - PreCompact auto-handoff support
- **`gt hook clear`** - Alias for `gt unhook`
- **`gt dog clear` and `gt warrant`** - Dog management commands
- **`gt rig settings`** - Interactive rig settings management
- **`--adopt` flag for `gt rig add`** - Register existing directories
- **Enhanced `--help` text** - Long descriptions added to 30+ commands
- **Dark mode CLI theme support** - Configurable terminal themes
- **Agent switcher keybinding** - `C-b g` for tmux agent switching
- **`gt prime` compact/resume detection** - Lighter post-compaction priming
- **Command quick-reference in CLAUDE.md** - Auto-generated per role

#### Community Contributions
- **Containerized E2E tests** - Docker-based install and daemon testing
- **Integration branch enhancements** - End-to-end integration branches across the pipeline
- **Stale claim timeout in refinery** - Prevents stuck MRs
- **Serialize main pushes with merge slot** - Prevents push conflicts
- **Agent-agnostic zombie detection** - Works with any AI agent, not just Claude
- **Configurable CLI name** (`GT_COMMAND` env var) - For custom installations
- **Compaction reporting** - Daily digest and weekly rollup

### Changed

- **Dolt is the only backend** - All SQLite code removed; `--no-daemon` flag deprecated
- **Settings moved to `settings.local.json`** - Cleaner separation from repo config
- **`gt status --fast` optimized** - From ~5s to ~2s
- **Refinery squash merge** - Closed MRs excluded from queue output
- **Priority-based mail notifications** - Prevents agent derailment from low-priority mail
- **Compaction reporting** - Automated daily digest and weekly rollup
- **Formula template rendering** - Go text/template for convoy prompts
- **Centralized configuration** - Hardcoded timeouts and thresholds moved to TownSettings

### Fixed

#### Reliability & Race Conditions
- **flock-based locking** across molecule attach, events/feed writes, crew files, and lock acquisition
- **TOCTOU guards** on Dolt server startup, worktree operations, cleanup actions, and FindRigBeadsDir
- **Atomic writes** for catalog, routes, settings, and per-bead files
- **Thread-safe NotificationManager** with mutex protection
- **Deadlock elimination** in daemon restart backoff tests
- **Process group termination** using verified member enumeration instead of blind PGID kill

#### Security Hardening
- **Input validation** for web dashboard handlers, group names, dog names, issue creation
- **Path traversal prevention** in dog names, rig names, and bead operations
- **Shell injection prevention** via session name validation and branch name sanitization
- **Rejected flag-like titles** to prevent garbage bead creation from malformed commands

#### Dolt Backend
- **Read-only state auto-recovery** - Commands detect and recover from Dolt read-only mode
- **Split-brain prevention** when `bd` used before Dolt server starts
- **Exponential backoff with jitter** for Dolt retries (10 attempts)
- **Database verification** after migration and server start
- **Orphaned database detection and cleanup** in `.dolt-data/`

#### Session & Process Management
- **Polecat nuke improvements** - Close open MRs, verify worktree removal, handle cd'd shells
- **Zombie detection** - tmux-alive-but-agent-dead detection, cleanup_status handling
- **Respawn protection** - Prevents destroying unmerged MR work on respawn
- **Nudge backoff** - Correct cap, reduced timeout, transient error retry
- **Session name parsing** - Handles hyphenated rig names correctly
- **NBSP normalization** - Fixes Claude Code readiness detection

#### Cross-Rig Operations
- **Beads routing fixes** - Correct prefix detection, redirect topology verification
- **Cross-rig agent bead operations** routed to correct database
- **Convoy tracking** - Proper external issue status refresh and stranded detection
- **Doctor checks** continue on error in agent/rig bead fix methods

#### Many more fixes
- 200+ bug fixes from community contributions and internal development
- See `git log v0.5.0..v0.6.0` for complete details

## [0.5.0] - 2026-01-22

### Added

#### Mail Improvements
- **Numeric index support for `gt mail read`** - Read messages by inbox position (e.g., `gt mail read 1`)
- **`gt mail hook` alias** - Shortcut for `gt hook attach` from mail context
- **`--body` alias for `--message`** - More intuitive flag in `gt mail send` and `gt mail reply`
- **Multiple message IDs in delete** - `gt mail delete msg1 msg2 msg3`
- **Positional message arg in reply** - `gt mail reply <id> "message"` without --message flag
- **`--all` flag for inbox** - Show all messages including read
- **Parallel inbox queries** - ~6x speedup for mail inbox

#### Command Aliases
- **`gt bd`** - Alias for `gt bead`
- **`gt work`** - Alias for `gt hook`
- **`--comment` alias for `--reason`** - In `gt close`
- **`read` alias for `show`** - In `gt bead`

#### Configuration & Agents
- **OpenCode as built-in agent preset** - Configure with `gt config set agent opencode`
- **Config-based role definition system** - Roles defined in config, not beads
- **Env field in RuntimeConfig** - Custom environment variables for agent presets
- **ShellQuote helper** - Safe env var escaping for shell commands

#### Infrastructure
- **Deacon status line display** - Shows deacon icon in mayor status line
- **Configurable polecat branch naming** - Template-based branch naming
- **Hook registry and install command** - Manage Claude Code hooks via `gt hooks`
- **Doctor auto-fix capability** - SessionHookCheck can auto-repair
- **`gt orphans kill` command** - Clean up orphaned Claude processes
- **Zombie-scan command for deacon** - tmux-verified process cleanup
- **Initial prompt for autonomous patrol startup** - Better agent priming

#### Refinery & Merging
- **Squash merge for cleaner history** - Eliminates redundant merge commits
- **Redundant observers** - Witness and Refinery both watch convoys

### Fixed

#### Crew & Session Stability
- **Don't kill pane processes on new sessions** - Prevents destroying fresh shells
- **Auto-recover from stale tmux pane references** - Recreates sessions automatically
- **Preserve GT_AGENT across session restarts** - Handoff maintains identity

#### Process Management
- **KillPaneProcesses kills pane process itself** - Not just descendants
- **Kill pane processes before all RespawnPane calls** - Prevents orphan leaks
- **Shutdown reliability improvements** - Multiple fixes for clean shutdown
- **Deacon spawns immediately after killing stuck session**

#### Convoy & Routing
- **Pass convoy ID to convoy check command** - Correct ID propagation
- **Multi-repo routing for custom types** - Correct beads routing across repos
- **Normalize agent ID trailing slash** - Consistent ID handling

#### Miscellaneous
- **Sling auto-apply mol-polecat-work** - Auto-attach on open polecat beads
- **Wisp orphan lifecycle bug** - Proper cleanup of abandoned wisps
- **Misclassified wisp detection** - Defense-in-depth filtering
- **Cross-account session access in seance** - Talk to predecessors across accounts
- **Many more bug fixes** - See git log for full details

## [0.4.0] - 2026-01-19

_Changelog not documented at release time. See git log v0.3.1..v0.4.0 for changes._

## [0.3.1] - 2026-01-18

_Changelog not documented at release time. See git log v0.3.0..v0.3.1 for changes._

## [0.3.0] - 2026-01-17

### Added

#### Release Automation
- **`gastown-release` molecule formula** - Workflow for releases with preflight checks, CHANGELOG/info.go updates, local install, and daemon restart

#### New Commands
- **`gt show`** - Inspect bead contents and metadata
- **`gt cat`** - Display bead content directly
- **`gt orphans list/kill`** - Detect and clean up orphaned Claude processes
- **`gt convoy close`** - Manual convoy closure command
- **`gt commit`** - Wrapper for git commit with bead awareness
- **`gt trail`** - View commit trail for current work
- **`gt mail ack`** - Alias for mark-read command

#### Plugin System
- **Plugin discovery and management** - `gt plugin run`, `gt plugin history`
- **`gt dispatch --plugin`** - Execute plugins via dispatch command

#### Messaging Infrastructure (Beads-Native)
- **Queue beads** - New bead type for message queues
- **Channel beads** - Pub/sub messaging with retention
- **Group beads** - Group management for messaging
- **Address resolution** - Resolve agent addresses for mail routing
- **`gt mail claim`** - Claim messages from queues

#### Agent Identity
- **`gt polecat identity show`** - Display CV summary for agents
- **Worktree setup hooks** - Inject local configurations into worktrees

#### Performance & Reliability
- **Parallel agent startup** - Faster boot with concurrency limit
- **Event-driven convoy completion** - Deacon checks convoy status on events
- **Automatic orphan cleanup** - Detect and kill orphaned Claude processes
- **Namepool auto-theming** - Themes selected per rig based on name hash

### Changed

- **MR tracking via beads** - Removed mrqueue package, MRs now stored as beads
- **Desire-path commands** - Added agent ergonomics shortcuts
- **Explicit escalation in templates** - Polecat templates include escalation instructions
- **NamePool state is transient** - InUse state no longer persisted to config

### Fixed

#### Process Management
- **Kill process tree on shutdown** - Prevents orphaned Claude processes
- **Explicit pane process kill** - Prevents setsid orphans in tmux
- **Session survival verification** - Verify session survives startup before returning
- **Batch session queries** - Improved performance in `gt down`
- **Prevent tmux server exit** - `gt down` no longer kills tmux server

#### Beads & Routing
- **Agent bead prefix alignment** - Force multi-hyphen IDs for consistency
- **hq- prefix for town-level beads** - Groups, channels use correct prefix
- **CreatedAt for group/channel beads** - Proper timestamps on creation
- **Routes.jsonl protection** - Doctor check for rig-level routing issues
- **Clear BEADS_DIR in auto-convoys** - Prevent prefix inheritance issues

#### Mail & Communication
- **Channel routing in router.Send()** - Mail correctly routes to channels
- **Filter unread in beads mode** - Correct unread message filtering
- **Town root detection** - Use workspace.Find for consistent detection

#### Session & Lifecycle
- **Idle Polecat Heresy warnings** - Templates warn against idle waiting
- **Direct push prohibition for polecats** - Explicit in templates
- **Handoff working directory** - Use correct witness directory
- **Dead polecat handling in sling** - Detect and handle dead polecats
- **gt done self-cleaning** - Kill tmux session on completion

#### Doctor & Diagnostics
- **Zombie session detection** - Detect dead Claude processes in tmux
- **sqlite3 availability check** - Verify sqlite3 is installed
- **Clone divergence check** - Remove blocking git fetch

#### Build & Platform
- **Windows build support** - Platform-specific process/signal handling
- **macOS codesigning** - Sign binary after install

### Documentation

- **Idle Polecat Heresy** - Document the anti-pattern of waiting for work
- **Bead ID vs Issue ID** - Clarify terminology in README
- **Explicit escalation** - Add escalation guidance to polecat templates
- **Getting Started placement** - Fix README section ordering

## [0.2.6] - 2026-01-12

### Added

#### Escalation System
- **Unified escalation system** - Complete escalation implementation with severity levels, routing, and tracking (gt-i9r20)
- **Escalation config schema alignment** - Configuration now matches design doc specifications

#### Agent Identity & Management
- **`gt polecat identity` subcommand group** - Agent bead management commands for polecat lifecycle
- **AGENTS.md fallback copy** - Polecats automatically copy AGENTS.md from mayor/rig for context bootstrapping
- **`--debug` flag for `gt crew at`** - Debug mode for crew attachment troubleshooting
- **Boot role detection in priming** - Proper context injection for boot role agents (#370)

#### Statusline Improvements
- **Per-agent-type health tracking** - Statusline now shows health status per agent type (#344)
- **Visual rig grouping** - Rigs sorted by activity with visual grouping in tmux statusline (#337)

#### Mail & Communication
- **`gt mail show` alias** - Alternative command for reading mail (#340)

#### Developer Experience
- **`gt stale` command** - Check for stale binaries and version mismatches

### Changed

- **Refactored statusline** - Merged session loops and removed dead code for cleaner implementation
- **Refactored sling.go** - Split 1560-line file into 7 focused modules for maintainability
- **Magic numbers extracted** - Suggest package now uses named constants (#353)

### Fixed

#### Configuration & Environment
- **Empty GT_ROOT/BEADS_DIR not exported** - AgentEnv no longer exports empty environment variables (#385)
- **Inherited BEADS_DIR prefix mismatch** - Prevent inherited BEADS_DIR from causing prefix mismatches (#321)

#### Beads & Routing
- **routes.jsonl corruption prevention** - Added protection against routes.jsonl corruption with doctor check for rig-level issues (#377)
- **Tracked beads init after clone** - Initialize beads database for tracked beads after git clone (#376)
- **Rig root from BeadsPath()** - Correctly return rig root to respect redirect system

#### Sling & Formula
- **Feature and issue vars in formula-on-bead mode** - Pass both variables correctly (#382)
- **Crew member shorthand resolution** - Resolve crew members correctly with shorthand paths
- **Removed obsolete --naked flag** - Cleanup of deprecated sling option

#### Doctor & Diagnostics
- **Role beads check with shared definitions** - Doctor now validates role beads using shared role definitions (#378)
- **Filter bd "Note:" messages** - Custom types check no longer confused by bd informational output (#381)

#### Installation & Setup
- **gt:role label on role beads** - Role beads now properly labeled during creation (#383)
- **Fetch origin after refspec config** - Bare clones now fetch after configuring refspec (#384)
- **Allow --wrappers in existing town** - No longer recreates HQ unnecessarily (#366)

#### Session & Lifecycle
- **Fallback instructions in start/restart beacons** - Session beacons now include fallback instructions
- **Handoff recognizes polecat session pattern** - Correctly handles gt-<rig>-<name> session names (#373)
- **gt done resilient to missing agent beads** - No longer fails when agent beads don't exist
- **MR beads as ephemeral wisps** - Create MR beads as ephemeral wisps for proper cleanup
- **Auto-detect cleanup status** - Prevents premature polecat nuke (#361)
- **Delete remote polecat branches after merge** - Refinery cleans up remote branches (#369)

#### Costs & Events
- **Query all beads locations for session events** - Cost tracking finds events across locations (#374)

#### Linting & Quality
- **errcheck and unparam violations resolved** - Fixed linting errors
- **NudgeSession for all agent notifications** - Mail now uses consistent notification method

### Documentation

- **Polecat three-state model** - Clarified working/stalled/zombie states
- **Name pool vs polecat pool** - Clarified misconception about pools
- **Plugin and escalation system designs** - Added design documentation
- **Documentation reorganization** - Concepts, design, and examples structure
- **gt prime clarification** - Clarified that gt prime is context recovery, not session start (GH #308)
- **Formula package documentation** - Comprehensive package docs
- **Various godoc additions** - GenerateMRIDWithTime, isAutonomousRole, formatInt, nil sentinel pattern
- **Beads issue ID format** - Clarified format in README (gt-uzx2c)
- **Stale polecat identity description** - Fixed outdated documentation

### Tests

- **AGENTS.md worktree tests** - Test coverage for AGENTS.md in worktrees
- **Comprehensive test coverage** - Added tests for 5 packages (#351)
- **Sling test for bd empty output** - Fixed test for empty output handling

### Deprecated

- **`gt polecat add`** - Added migration warning for deprecated command

### Contributors

Thanks to all contributors for this release:
- @JeremyKalmus - Various contributions (#364)
- @boshu2 - Formula package documentation (#343), PR documentation (#352)
- @sauerdaniel - Polecat mail notification fix (#347)
- @abhijit360 - Assign model to role (#368)
- @julianknutsen - Beads path fix (#334)

## [0.2.5] - 2026-01-11

### Added
- **`gt mail mark-read`** - Mark messages as read without opening them (desire path)
- **`gt down --polecats`** - Shut down polecats without affecting other components
- **Self-cleaning polecat model** - Polecats self-nuke on completion, witness tracks leases
- **`gt prime --state` validation** - Flag exclusivity checks for cleaner CLI

### Changed
- **Removed `gt stop`** - Use `gt down --polecats` instead (cleaner semantics)
- **Policy-neutral templates** - crew.md.tmpl checks remote origin for PR policy
- **Refactored prime.go** - Split 1833-line file into logical modules

### Fixed
- **Polecat re-spawn** - CreateOrReopenAgentBead handles polecat lifecycle correctly (#333)
- **Vim mode compatibility** - tmux sends Escape before Enter for vim users
- **Worktree default branch** - Uses rig's configured default branch (#325)
- **Agent bead type** - Sets --type=agent when creating agent beads
- **Bootstrap priming** - Reduced AGENTS.md to bootstrap pointer, fixed CLAUDE.md templates

### Documentation
- Updated witness help text for self-cleaning model
- Updated daemon comments for self-cleaning model
- Policy-aware PR guidance in crew template

## [0.2.4] - 2026-01-10

Priming subsystem overhaul and Zero Framework Cognition (ZFC) improvements.

### Added

#### Priming Subsystem
- **PRIME.md provisioning** - Auto-provision PRIME.md at rig level so all workers inherit Gas Town context (GUPP, hooks, propulsion) (#hq-5z76w)
- **Post-handoff detection** - `gt prime` detects handoff marker and outputs "HANDOFF COMPLETE" warning to prevent handoff loop bug (#hq-ukjrr)
- **Priming health checks** - `gt doctor` validates priming subsystem: SessionStart hook, gt prime command, PRIME.md presence, CLAUDE.md size (#hq-5scnt)
- **`gt prime --dry-run`** - Preview priming without side effects
- **`gt prime --state`** - Output session state (normal, post-handoff, crash-recovery, autonomous)
- **`gt prime --explain`** - Add [EXPLAIN] tags for debugging priming decisions

#### Formula & Configuration
- **Rig-level default formulas** - Configure default formula at rig level (#297)
- **Witness --agent/--env overrides** - Override agent and environment variables for witness (#293, #294)

#### Developer Experience
- **UX system import** - Comprehensive UX system from beads (#311)
- **Explicit handoff instructions** - Clearer nudge message for handoff recipients

### Fixed

#### Zero Framework Cognition (ZFC)
- **Query tmux directly** - Remove marker TTL, query tmux for agent state
- **Remove PID-based detection** - Agent liveness from tmux, not PIDs
- **Agent-controlled thresholds** - Stuck detection moved to agent config
- **Remove pending.json tracking** - Eliminated anti-pattern
- **Derive state from files** - ZFC state from filesystem, not memory cache
- **Remove Go-side computation** - No stderr parsing violations

#### Hooks & Beads
- **Cross-level hook visibility** - Hooked beads visible to mayor/deacon (#aeb4c0d)
- **Warn on closed hooked bead** - Alert when hooked bead already closed (#2f50a59)
- **Correct agent bead ID format** - Fix bd create flags for agent beads (#c4fcdd8)

#### Formula
- **rigPath fallback** - Set rigPath when falling back to gastown default (#afb944f)

#### Doctor
- **Full AgentEnv for env-vars check** - Use complete environment for validation (#ce231a3)

### Changed

- **Refactored beads/mail modules** - Split large files into focused modules for maintainability

## [0.2.3] - 2026-01-09

Worker safety release - prevents accidental termination of active agents.

> **Note**: The Deacon safety improvements are believed to be correct but have not
> yet been extensively tested in production. We recommend running with
> `gt deacon pause` initially and monitoring behavior before enabling full patrol.
> Please report any issues. A 0.3.0 release will follow once these changes are
> battle-tested.

### Critical Safety Improvements

- **Kill authority removed from Deacon** - Deacon patrol now only detects zombies via `--dry-run`, never kills directly. Death warrants are filed for Boot to handle interrogation/execution. This prevents destruction of worker context, mid-task progress, and unsaved state (#gt-vhaej)
- **Bulletproof pause mechanism** - Multi-layer pause for Deacon with file-based state, `gt deacon pause/resume` commands, and guards in `gt prime` and heartbeat (#265)
- **Doctor warns instead of killing** - `gt doctor` now warns about stale town-root settings rather than killing sessions (#243)
- **Orphan process check informational** - Doctor's orphan process detection is now informational only, not actionable (#272)

### Added

- **`gt account switch` command** - Switch between Claude Code accounts with `gt account switch <handle>`. Manages `~/.claude` symlinks and updates default account
- **`gt crew list --all`** - Show all crew members across all rigs (#276)
- **Rig-level custom agent support** - Configure different agents per-rig (#12)
- **Rig identity beads check** - Doctor validates rig identity beads exist
- **GT_ROOT env var** - Set for all agent sessions for consistent environment
- **New agent presets** - Added Cursor, Auggie (Augment Code), and Sourcegraph AMP as built-in agent presets (#247)
- **Context Management docs** - Added to Witness template for better context handling (gt-jjama)

### Fixed

- **`gt prime --hook` recognized** - Doctor now recognizes `gt prime --hook` as valid session hook config (#14)
- **Integration test reliability** - Improved test stability (#13)
- **IsClaudeRunning detection** - Now detects 'claude' and version patterns correctly (#273)
- **Deacon heartbeat restored** - `ensureDeaconRunning` restored to heartbeat using Manager pattern (#271)
- **Deacon session names** - Correct session name references in formulas (#270)
- **Hidden directory scanning** - Ignore `.claude` and other dot directories when enumerating polecats (#258, #279)
- **SetupRedirect tracked beads** - Works correctly with tracked beads architecture where canonical location is `mayor/rig/.beads`
- **Tmux shell ready** - Wait for shell ready before sending keys (#264)
- **Gastown prefix derivation** - Correctly derive `gt-` prefix for gastown compound words (gt-m46bb)
- **Custom beads types** - Register custom beads types during install (#250)

### Changed

- **Refinery Manager pattern** - Replaced `ensureRefinerySession` with `refinery.Manager.Start()` for consistency

### Removed

- **Unused formula JSON** - Removed unused JSON formula file (cleanup)

### Contributors

Thanks to all contributors for this release:
- @julianknutsen - Doctor fixes (#14, #271, #272, #273), formula fixes (#270), GT_ROOT env (#268)
- @joshuavial - Hidden directory scanning (#258, #279), crew list --all (#276)

## [0.2.2] - 2026-01-07

Rig operational state management, unified agent startup, and extensive stability fixes.

### Added

#### Rig Operational State Management
- **`gt rig park/unpark` commands** - Level 1 rig control: pause daemon auto-start while preserving sessions
- **`gt rig dock/undock` commands** - Level 2 rig control: stop all sessions and prevent auto-start (gt-9gm9n)
- **`gt rig config` commands** - Per-rig configuration management (gt-hhmkq)
- **Rig identity beads** - Schema and creation for rig identity tracking (gt-zmznh)
- **Property layer lookup** - Hierarchical configuration resolution (gt-emh1c)
- **Operational state in status** - `gt rig status` shows park/dock state

#### Agent Configuration & Startup
- **`--agent` overrides** - Override agent for start/attach/sling commands
- **Unified agent startup** - Manager pattern for consistent agent initialization
- **Claude settings installation** - Auto-install during rig and HQ creation
- **Runtime-aware tmux checks** - Detect actual agent state from tmux sessions

#### Status & Monitoring
- **`gt status --watch`** - Watch mode with auto-refresh (#231)
- **Compact status output** - One-line-per-worker format as new default
- **LED status indicators** - Visual indicators for rigs in Mayor tmux status line
- **Parked/docked indicators** - Pause emoji (⏸) for inactive rigs in statusline

#### Beads & Workflow
- **Minimum beads version check** - Validates beads CLI compatibility (gt-im3fl)
- **ZFC convoy auto-close** - `bd close` triggers convoy completion (gt-3qw5s)
- **Stale hooked bead cleanup** - Deacon clears orphaned hooks (gt-2yls3)
- **Doctor prefix mismatch detection** - Detect misconfigured rig prefixes (gt-17wdl)
- **Unified beads redirect** - Single redirect system for tracked and local beads (#222)
- **Route from rig to town beads** - Cross-level bead routing

#### Infrastructure
- **Windows-compatible file locking** - Daemon lock works on Windows
- **`--purge` flag for crews** - Full crew obliteration option
- **Debug logging for suppressed errors** - Better visibility into startup issues (gt-6d7eh)
- **hq- prefix in tmux cycle bindings** - Navigate to Mayor/Deacon sessions
- **Wisp config storage layer** - Transient/local settings for ephemeral workflows
- **Sparse checkout** - Exclude Claude context files from source repos

### Changed

- **Daemon respects rig operational state** - Parked/docked rigs not auto-started
- **Agent startup unified** - Manager pattern replaces ad-hoc initialization
- **Mayor files moved** - Reorganized into `mayor/` subdirectory
- **Refinery merges local branches** - No longer fetches from origin (gt-cio03)
- **Polecats start from origin/default-branch** - Consistent recycled state
- **Observable states removed** - Discover agent state from tmux, don't track (gt-zecmc)
- **mol-town-shutdown v3** - Complete cleanup formula (gt-ux23f)
- **Witness delays polecat cleanup** - Wait until MR merges (gt-12hwb)
- **Nudge on divergence** - Daemon nudges agents instead of silent accept
- **README rewritten** - Comprehensive guides and architecture docs (#226)
- **`gt rigs` → `gt rig list`** - Command renamed in templates/docs (#217)

### Fixed

#### Doctor & Lifecycle
- **`--restart-sessions` flag required** - Doctor won't cycle sessions without explicit flag (gt-j44ri)
- **Only cycle patrol roles** - Doctor --fix doesn't restart crew/polecats (hq-qthgye)
- **Session-ended events auto-closed** - Prevent accumulation (gt-8tc1v)
- **GUPP propulsion nudge** - Added to daemon restartSession

#### Sling & Beads
- **Sling uses bd native routing** - No BEADS_DIR override needed
- **Sling parses wisp JSON correctly** - Handle `new_epic_id` field
- **Sling resolves rig path** - Cross-rig bead hooking works
- **Sling waits for Claude ready** - Don't nudge until session responsive (#146)
- **Correct beads database for sling** - Rig-level beads used (gt-n5gga)
- **Close hooked beads before clearing** - Proper cleanup order (gt-vwjz6)
- **Removed dead sling flags** - `--molecule` and `--quality` cleaned up

#### Agent Sessions
- **Witness kills tmux on Stop()** - Clean session termination
- **Deacon uses session package** - Correct hq- session names (gt-r38pj)
- **Honor rig agent for witness/refinery** - Respect per-rig settings
- **Canonical hq role bead IDs** - Consistent naming
- **hq- prefix in status display** - Global agents shown correctly (gt-vcvyd)
- **Restart Claude when dead** - Recover sessions where tmux exists but Claude died
- **Town session cycling** - Works from any directory

#### Polecat & Crew
- **Nuke not blocked by stale hooks** - Closed beads don't prevent cleanup (gt-jc7bq)
- **Crew stop dry-run support** - Preview cleanup before executing (gt-kjcx4)
- **Crew defaults to --all** - `gt crew start <rig>` starts all crew (gt-s8mpt)
- **Polecat cleanup handlers** - `gt witness process` invokes handlers (gt-h3gzj)

#### Daemon & Configuration
- **Create mayor/daemon.json** - `gt start` and `gt doctor --fix` initialize daemon state (#225)
- **Initialize git before beads** - Enable repo fingerprint (#180)
- **Handoff preserves env vars** - Claude Code environment not lost (#216)
- **Agent settings passed correctly** - Witness and daemon respawn use rigPath
- **Log rig discovery errors** - Don't silently swallow (gt-rsnj9)

#### Refinery & Merge Queue
- **Use rig's default_branch** - Not hardcoded 'main'
- **MERGE_FAILED sent to Witness** - Proper failure notification
- **Removed BranchPushedToRemote checks** - Local-only workflow support (gt-dymy5)

#### Misc Fixes
- **BeadsSetupRedirect preserves tracked files** - Don't clobber existing files (gt-fj0ol)
- **PATH export in hooks** - Ensure commands find binaries
- **Replace panic with fallback** - ID generation gracefully degrades (#213)
- **Removed duplicate WorktreeAddFromRef** - Code cleanup
- **Town root beads for Deacon** - Use correct beads location (gt-sstg)

### Refactored

- **AgentStateManager pattern** - Shared state management extracted (gt-gaw8e)
- **CleanupStatus type** - Replace raw strings (gt-77gq7)
- **ExecWithOutput utility** - Common command execution (gt-vurfr)
- **runBdCommand helper** - DRY mail package (gt-8i6bg)
- **Config expansion helper** - Generic DRY config (gt-i85sg)

### Documentation

- **Property layers guide** - Implementation documentation
- **Worktree architecture** - Clarified beads routing
- **Agent config** - Onboarding docs mention --agent overrides
- **Polecat Operations section** - Added to Mayor docs (#140)

### Contributors

Thanks to all contributors for this release:
- @julianknutsen - Claude settings inheritance (#239)
- @joshuavial - Sling wisp JSON parse (#238)
- @michaellady - Unified beads redirect (#222), daemon.json fix (#225)
- @greghughespdx - PATH in hooks fix (#139)

## [0.2.1] - 2026-01-05

Bug fixes, security hardening, and new `gt config` command.

### Added

- **`gt config` command** - Manage agent settings (model, provider) per-rig or globally
- **`hq-` prefix for patrol sessions** - Mayor and Deacon sessions use town-prefixed names
- **Doctor hooks-path check** - Verify Git hooks path is configured correctly
- **Block internal PRs** - Pre-push hook and GitHub Action prevent accidental internal PRs (#117)
- **Dispatcher notifications** - Notify dispatcher when polecat work completes
- **Unit tests** - Added tests for `formatTrackBeadID` helper, done redirect, hook slot E2E

### Fixed

#### Security
- **Command injection prevention** - Validate beads prefix to prevent injection (gt-l1xsa)
- **Path traversal prevention** - Validate crew names to prevent traversal (gt-wzxwm)
- **ReDoS prevention** - Escape user input in mail search (gt-qysj9)
- **Error handling** - Handle crypto/rand.Read errors in ID generation

#### Convoy & Sling
- **Hook slot initialization** - Set hook slot when creating agent beads during sling (#124)
- **Cross-rig bead formatting** - Format cross-rig beads as external refs in convoy tracking (#123)
- **Reliable bd calls** - Add `--no-daemon` and `BEADS_DIR` for reliable beads operations

#### Rig Inference
- **`gt rig status`** - Infer rig name from current working directory
- **`gt crew start --all`** - Infer rig from cwd for batch crew starts
- **`gt prime` in crew start** - Pass as initial prompt in crew start commands
- **Town default_agent** - Honor default agent setting for Mayor and Deacon

#### Session & Lifecycle
- **Hook persistence** - Hook persists across session interruption via `in_progress` lookup (gt-ttn3h)
- **Polecat cleanup** - Clean up stale worktrees and git tracking
- **`gt done` redirect** - Use ResolveBeadsDir for redirect file support

#### Build & CI
- **Embedded formulas** - Sync and commit formulas for `go install @latest`
- **CI lint fixes** - Resolve lint and build errors
- **Flaky test fix** - Sync database before beads integration tests

## [0.2.0] - 2026-01-04

Major release featuring the Convoy Dashboard, two-level beads architecture, and significant multi-agent improvements.

### Added

#### Convoy Dashboard (Web UI)
- **`gt dashboard` command** - Launch web-based monitoring UI for Gas Town (#71)
- **Polecat Workers section** - Real-time activity monitoring with tmux session timestamps
- **Refinery Merge Queue display** - Always-visible MR queue status
- **Dynamic work status** - Convoy status columns with live updates
- **HTMX auto-refresh** - 10-second refresh interval for real-time monitoring

#### Two-Level Beads Architecture
- **Town-level beads** (`~/gt/.beads/`) - `hq-*` prefix for Mayor mail and cross-rig coordination
- **Rig-level beads** - Project-specific issues with rig prefixes (e.g., `gt-*`)
- **`gt migrate-agents` command** - Migration tool for two-level architecture (#nnub1)
- **TownBeadsPrefix constant** - Centralized `hq-` prefix handling
- **Prefix-based routing** - Commands auto-route to correct rig via `routes.jsonl`

#### Multi-Agent Support
- **Pluggable agent registry** - Multi-agent support with configurable providers (#107)
- **Multi-rig management** - `gt rig start/stop/restart/status` for batch operations (#11z8l)
- **`gt crew stop` command** - Stop crew sessions cleanly
- **`spawn` alias** - Alternative to `start` for all role subcommands
- **Batch slinging** - `gt sling` supports multiple beads to a rig in one command (#l9toz)

#### Ephemeral Polecat Model
- **Immediate recycling** - Polecats recycled after each work unit (#81)
- **Updated patrol formula** - Witness formula adapted for ephemeral model
- **`mol-polecat-work` formula** - Updated for ephemeral polecat lifecycle (#si8rq.4)

#### Cost Tracking
- **`gt costs` command** - Session cost tracking and reporting
- **Beads-based storage** - Costs stored in beads instead of JSONL (#f7jxr)
- **Stop hook integration** - Auto-record costs on session end
- **Tmux session auto-detection** - Costs hook finds correct session

#### Conflict Resolution
- **Conflict resolution workflow** - Formula-based conflict handling for polecats (#si8rq.5)
- **Merge-slot gate** - Refinery integration for ordered conflict resolution
- **`gt done --phase-complete`** - Gate-based phase handoffs (#si8rq.7)

#### Communication & Coordination
- **`gt mail archive` multi-ID** - Archive multiple messages at once (#82)
- **`gt mail --all` flag** - Clear all mail for agent ergonomics (#105q3)
- **Convoy stranded detection** - Detect and feed stranded convoys (#8otmd)
- **`gt convoy --tree`** - Show convoy + child status tree
- **`gt convoy check`** - Cross-rig auto-close for completed convoys (#00qjk)

#### Developer Experience
- **Shell completion** - Installation instructions for bash/zsh/fish (#pdrh0)
- **`gt prime --hook`** - LLM runtime session handling flag
- **`gt doctor` enhancements** - Session-hooks check, repo-fingerprint validation (#nrgm5)
- **Binary age detection** - `gt status` shows stale binary warnings (#42whv)
- **Circuit breaker** - Automatic handling for stuck agents (#72cqu)

#### Infrastructure
- **SessionStart hooks** - Deployed during `gt install` for Mayor role
- **`hq-dog-role` beads** - Town-level dog role initialization (#2jjry)
- **Watchdog chain docs** - Boot/Deacon lifecycle documentation (#1847v)
- **Integration tests** - CI workflow for `gt install` and `gt rig add` (#htlmp)
- **Local repo reference clones** - Save disk space with `--reference` cloning

### Changed

- **Handoff migrated to skills** - `gt handoff` now uses skills format (#nqtqp)
- **Crew workers push to main** - Documentation clarifies no PR workflow for crew
- **Session names include town** - Mayor/Deacon sessions use town-prefixed names
- **Formula semantics clarified** - Formulas are templates, not instructions
- **Witness reports stopped** - No more routine Mayor reports (saves tokens)

### Fixed

#### Daemon & Session Stability
- **Thread-safety** - Added locks for agent session resume support
- **Orphan daemon prevention** - File locking prevents duplicate daemons (#108)
- **Zombie tmux cleanup** - Kill zombie sessions before recreating (#vve6k)
- **Tmux exact matching** - `HasSession` uses exact match to prevent prefix collisions
- **Health check fallback** - Prevents killing healthy sessions on tmux errors

#### Beads Integration
- **Mayor/rig path** - Use correct path for beads to prevent prefix mismatch (#38)
- **Agent bead creation** - Fixed during `gt rig add` (#32)
- **bd daemon startup** - Circuit breaker and restart logic (#2f0p3)
- **BEADS_DIR environment** - Correctly set for polecat hooks and cross-rig work

#### Agent Workflows
- **Default branch detection** - `gt done` no longer hardcodes 'main' (#42)
- **Enter key retry** - Reliable Enter key delivery with retry logic (#53)
- **SendKeys debounce** - Increased to 500ms for reliability
- **MR bead closure** - Close beads after successful merge from queue (#52)

#### Installation & Setup
- **Embedded formulas** - Copy formulas to new installations (#86)
- **Vestigial cleanup** - Remove `rigs/` directory and `state.json` files
- **Symlink preservation** - Workspace detection preserves symlink paths (#3, #75)
- **Golangci-lint errors** - Resolved errcheck and gosec issues (#76)

### Contributors

Thanks to all contributors for this release:
- @kiwiupover - README updates (#109)
- @michaellady - Convoy dashboard (#71), ResolveBeadsDir fix (#54)
- @jsamuel1 - Dependency updates (#83)
- @dannomayernotabot - Witness fixes (#87), daemon race condition (#64)
- @markov-kernel - Mayor session hooks (#93), daemon init recommendation (#95)
- @rawwerks - Multi-agent support (#107)
- @jakehemmerle - Daemon orphan race condition (#108)
- @danshapiro - Install role slots (#106), rig beads dir (#61)
- @vessenes - Town session helpers (#91), install copy formulas (#86)
- @kustrun - Init bugs (#34)
- @austeane - README quickstart fix (#44)
- @Avyukth - Patrol roles per-rig check (#26)

## [0.1.1] - 2026-01-02

### Fixed

- **Tmux keybindings scoped to Gas Town sessions** - C-b n/p no longer override default tmux behavior in non-GT sessions (#13)

### Added

- **OSS project files** - CHANGELOG.md, .golangci.yml, RELEASING.md
- **Version bump script** - `scripts/bump-version.sh` for releases
- **Documentation fixes** - Corrected `gt rig add` and `gt crew add` CLI syntax (#6)
- **Rig prefix routing** - Agent beads now use correct rig-specific prefixes (#11)
- **Beads init fix** - Rig beads initialization targets correct database (#9)

## [0.1.0] - 2026-01-02

### Added

Initial public release of Gas Town - a multi-agent workspace manager for Claude Code.

#### Core Architecture
- **Town structure** - Hierarchical workspace with rigs, crews, and polecats
- **Rig management** - `gt rig add/list/remove` for project containers
- **Crew workspaces** - `gt crew add` for persistent developer workspaces
- **Polecat workers** - Transient agent workers managed by Witness

#### Agent Roles
- **Mayor** - Global coordinator for cross-rig work
- **Deacon** - Town-level lifecycle patrol and heartbeat
- **Witness** - Per-rig polecat lifecycle manager
- **Refinery** - Merge queue processor with code review
- **Crew** - Persistent developer workspaces
- **Polecat** - Transient worker agents

#### Work Management
- **Convoy system** - `gt convoy create/list/status` for tracking related work
- **Sling workflow** - `gt sling <bead> <rig>` to assign work to agents
- **Hook mechanism** - Work attached to agent hooks for pickup
- **Molecule workflows** - Formula-based multi-step task execution

#### Communication
- **Mail system** - `gt mail inbox/send/read` for agent messaging
- **Escalation protocol** - `gt escalate` with severity levels
- **Handoff mechanism** - `gt handoff` for context-preserving session cycling

#### Integration
- **Beads integration** - Issue tracking via beads (`bd` commands)
- **Tmux sessions** - Agent sessions in tmux with theming
- **GitHub CLI** - PR creation and merge queue via `gh`

#### Developer Experience
- **Status dashboard** - `gt status` for town overview
- **Session cycling** - `C-b n/p` to navigate between agents
- **Activity feed** - `gt feed` for real-time event stream
- **Nudge system** - `gt nudge` for reliable message delivery to sessions

### Infrastructure
- **Daemon mode** - Background lifecycle management
- **npm package** - Cross-platform binary distribution
- **GitHub Actions** - CI/CD workflows for releases
- **GoReleaser** - Multi-platform binary builds
