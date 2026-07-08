# Agent API Touch-Point Inventory

Complete catalog of all GT↔agent integration points, mapped to source code
and the proposed Factory Worker API endpoints.

Ref: gt-5zs8 | Companion: [factory-worker-api.md](factory-worker-api.md)

---

## How to Read This Document

Each touch point lists:
- **What**: What GT does through this touch point
- **Code**: Source files and key functions (line numbers approximate after edits)
- **Flow**: What information moves and in which direction (GT→Agent or Agent→GT)
- **Fragility**: What breaks and why
- **API mapping**: Which Factory Worker API endpoint replaces it

---

## 1. Prompt Delivery (tmux send-keys)

**What**: GT sends text to agent sessions via tmux terminal injection.

**Code**:
- `internal/tmux/tmux.go` — `NudgeSession()` (line ~1300): 8-step protocol
  (serialize → find pane → exit copy mode → sanitize → chunk at 512 bytes →
  debounce 500ms → ESC + 600ms readline dance → Enter with retries → SIGWINCH wake)
- `internal/tmux/tmux.go` — `sendMessageToTarget()` (line ~1210): splits at 512 bytes,
  10ms inter-chunk delay
- `internal/tmux/tmux.go` — `sendKeysLiteralWithRetry()` (line ~1253): exponential
  backoff (500ms→2s cap) for startup race
- `internal/tmux/tmux.go` — `sanitizeNudgeMessage()` (line ~1179): strips ESC, CR, BS,
  DEL; replaces TAB with space
- `internal/tmux/tmux.go` — `SendKeys()`, `SendKeysDebounced()`, `SendKeysRaw()`,
  `SendKeysReplace()`, `SendKeysDelayed()` — variant entry points
- `internal/cmd/nudge.go` — `runNudge()` (line ~196), `deliverNudge()` (line ~129):
  CLI entry point, routes by mode (immediate/queue/wait-idle)

**Flow**: GT→Agent. Text string in, no structured response.

**Fragility**:
- 600ms ESC delay must exceed bash readline's 500ms keyseq-timeout; otherwise
  ESC+Enter becomes M-Enter (meta-return) = no submit
- 512-byte chunk size is empirical; tmux send-keys has undocumented limits
- Sanitization strips control chars but cannot handle all edge cases
- No delivery confirmation — GT has no way to know the agent received the message
- Per-session channel semaphore (30s timeout) serializes concurrent nudges

**API mapping**: `POST /prompt` — structured JSON delivery with accepted/queued response

---

## 2. Three Delivery Modes (immediate, wait-idle, queue)

**What**: GT routes prompt delivery through three modes depending on urgency.

**Code**:
- `internal/cmd/nudge.go` — mode constants: `NudgeModeImmediate`, `NudgeModeQueue`,
  `NudgeModeWaitIdle` (lines ~38-44)
- `internal/nudge/queue.go` — `Enqueue()` (line ~86): writes JSON file to
  `.runtime/nudge_queue/<session>/`, atomic naming with nanosecond timestamp
- `internal/nudge/queue.go` — `Drain()` (line ~143): atomic claim via rename to
  `.claimed`, orphan recovery for abandoned claims >5min, expiry filtering
- `internal/nudge/queue.go` — `FormatForInjection()` (line ~277): formats queued
  nudges as `<system-reminder>` blocks for Claude Code hook injection
- `internal/cmd/mail_check.go` — `runMailCheck()` (line ~16): UserPromptSubmit hook
  drains queue + checks mail, outputs injection block
- `internal/mail/router.go` — `NotifyRecipient()` (line ~1568): wait-idle-first
  strategy with 3s timeout, queue fallback

**Flow**: GT→Agent. Immediate: terminal injection. Queue: file→hook→injection.

**Fragility**:
- Queue drain depends on UserPromptSubmit hook — non-Claude agents never drain
- TTLs hardcoded (normal: 30min, urgent: 2hr, max depth: 50)
- Idle agents never call Drain(), so queued nudges can expire unseen
- Witness nudges to Refinery use immediate-only (line ~639 in handlers.go)

**API mapping**: `POST /prompt` with `priority` field (system/urgent/normal)

---

## 3. Idle Detection (prompt prefix + status bar)

**What**: GT determines if an agent is idle (waiting for input) or busy.

**Code**:
- `internal/tmux/tmux.go` — `matchesPromptPrefix()` (line ~2261): NBSP normalization
  (U+00A0→space), matches `DefaultReadyPromptPrefix = "❯ "` (U+276F)
- `internal/tmux/tmux.go` — `IsIdle()` (line ~2386): status bar parsing for `⏵⏵`
  (U+23F5), busy = "esc to interrupt" present
- `internal/tmux/tmux.go` — `WaitForIdle()` (line ~2321): polls 200ms interval,
  captures 5 pane lines, returns `ErrIdleTimeout`
- `internal/tmux/tmux.go` — `IsAtPrompt()` (line ~2359): non-blocking point-in-time
  check
- `internal/tmux/tmux.go` — `promptSuffixes` (line ~1478):
  `[">", "$", "%", "#", "❯"]` for dialog detection

**Flow**: Agent→GT (inferred). GT scrapes terminal output; agent doesn't know.

**Fragility**:
- Prompt prefix `❯` is a Claude Code UI string — any change breaks detection
- Status bar `⏵⏵` and "esc to interrupt" are undocumented Claude Code internals
- NBSP normalization was a bug fix (issues/1387) for a Claude Code rendering change
- Different agents have different prompts — no universal detection
- Point-in-time: race between check and state change

**API mapping**: `POST /lifecycle` with `event: "idle" | "busy"`

---

## 4. Rate Limit Detection (pane content scanning)

**What**: GT scans terminal output to detect rate-limited sessions for account rotation.

**Code**:
- `internal/quota/scan.go` — `Scanner` struct, `ScanAll()` (line ~77),
  `scanSession()` (line ~99): captures 30 lines of pane content, checks bottom 20
  against rate-limit regex patterns
- `internal/constants/constants.go` — `DefaultRateLimitPatterns`: regex patterns
  for rate limit messages

**Flow**: Agent→GT (inferred). GT reads pane; agent doesn't participate.

**Fragility**:
- Regex patterns must match exact rate limit error messages
- Messages can change across Claude Code versions
- Captures only bottom 20 of 30 lines — rate limit message must be recent
- No structured signal from agent that it's rate-limited

**API mapping**: `POST /lifecycle` with `event: "degraded"` + rate limit metadata,
or `POST /telemetry` with rate limit event

---

## 5. Account/Quota Management (keychain token swapping)

**What**: GT rotates API credentials across sessions when accounts hit rate limits.

**Code**:
- `internal/quota/keychain.go` — Darwin-only (289 lines):
  `KeychainServiceName()` (line ~35): SHA-256 hash of config dir,
  `SwapKeychainCredential()` (line ~78): backup target → read source → write target,
  `SwapOAuthAccount()` (line ~121): swaps `.claude.json` oauthAccount field,
  `ValidateKeychainToken()` (line ~203): checks expiry (JSON, JWT, opaque)
- `internal/quota/scan.go` — `ScanAll()` (line ~77): scan for rate-limited sessions
- `internal/quota/rotate.go` — `PlanRotation()` (line ~42): 4-stage pipeline
  (scan → state manager → planner → executor)
- `internal/quota/executor.go` — `Rotator.Execute()` (line ~81): atomic execution
  with flock, concurrent on independent sessions

**Flow**: GT→Agent. GT swaps credentials; agent is restarted with new token.

**Fragility**:
- macOS-only — entire keychain subsystem is darwin-only, no Linux/Windows
- Credential swap requires session restart (kill processes → respawn pane)
- OAuth account field location in `.claude.json` is undocumented
- SHA-256 keying assumes Claude Code's keychain service naming convention
- No agent-side credential refresh — always a full restart

**API mapping**: `POST /identity` with `credentials` field — runtime applies without restart

---

## 6. Session Lifecycle (creation, restart, teardown)

**What**: GT creates, restarts, and tears down agent tmux sessions.

**Code**:
- `internal/session/lifecycle.go` — `StartSession()` (line ~121): 13-step unified
  lifecycle (resolve config → settings → command → session → env → theme → wait →
  dialogs → delay → verify → respawn → PID track)
- `internal/polecat/session_manager.go` — `Start()` (line ~186): polecat-specific
  session with zombie kill, worktree, beacon, env injection, pane-died hook
- `internal/witness/manager.go` — `Start()` (line ~107): witness session with
  zombie grace period, role config, theme, pane-died hook
- `internal/dog/session_manager.go` — `Start()` (line ~85): dog session via
  unified `session.StartSession()`
- `internal/tmux/tmux.go` — `NewSessionWithCommand()`: single-command session creation,
  `SetAutoRespawnHook()` (line ~3126): pane-died auto-respawn with 3s debounce
- `internal/tmux/tmux.go` — `KillSessionWithProcesses()` (line ~499): 8-step teardown
  (process group → tree walk → SIGTERM → 2s grace → SIGKILL → pane → session)

**Flow**: GT→Agent. GT controls entire lifecycle; agent is passive.

**Fragility**:
- 13-step creation has many failure points (tmux, dialogs, readiness)
- Auto-respawn via pane-died hook depends on tmux's hook mechanism
- Kill sequence must handle reparented processes (PPID=1 check)
- Zombie cleanup has TOCTOU gap (re-verified before kill)
- Session creation takes 5-60s depending on agent startup time

**API mapping**: `POST /lifecycle` (agent reports transitions),
`POST /identity` (GT assigns identity at creation)

---

## 7. Spawn Admission Control

**What**: GT gates polecat creation with health checks and capacity limits.

**Code**:
- `internal/cmd/polecat_spawn.go` — `SpawnPolecatForSling()` (line ~62):
  Dolt health check, connection capacity, polecat count cap (25), per-bead
  respawn circuit breaker, per-rig directory cap (30), idle polecat reuse
- `internal/polecat/manager.go` — `CheckDoltHealth()` (line ~223): retry with
  exponential backoff + jitter; `CheckDoltServerCapacity()` (line ~276):
  connection count admission gate
- `internal/witness/spawn_count.go` — `ShouldBlockRespawn()` (line ~74): circuit
  breaker after 3 respawns per bead, `RecordBeadRespawn()` (line ~104): flock'd
  cross-process counter

**Flow**: GT internal. Admission decisions don't involve the agent.

**Fragility**:
- Polecat cap (25) and dir cap (30) are hardcoded
- Circuit breaker state in JSON file (`bead-respawn-counts.json`)
- Dolt health check adds latency to every spawn

**API mapping**: Internal to GT orchestration — not part of agent-facing API

---

## 8. Agent Identity (env vars + preset registry)

**What**: GT assigns identity to agents via environment variables and a preset registry.

**Code**:
- `internal/config/env.go` — `AgentEnv()` (line ~65): generates 30+ env vars
  including GT_ROLE, GT_RIG, GT_POLECAT, GT_CREW, BD_ACTOR, GIT_AUTHOR_NAME,
  GT_ROOT, GT_AGENT, GT_SESSION, plus OTEL and credential passthrough
- `internal/config/agents.go` — `builtinPresets` (line ~164): 10 agent presets
  (Claude, Gemini, Codex, Cursor, Auggie, AMP, OpenCode, Copilot, Pi, OMP)
  with 21 fields each (Command, Args, ProcessNames, SessionIDEnv, etc.)
- `internal/session/identity.go` — `ParseSessionName()` (line ~84),
  `ParseAddress()` (line ~30), `SessionName()` (line ~163): identity parsing
  and formatting
- `internal/constants/constants.go` — role constants (lines ~196-215):
  `RoleMayor`, `RoleDeacon`, `RoleWitness`, `RoleRefinery`, `RolePolecat`, `RoleCrew`

**Flow**: GT→Agent. GT sets env vars; agent reads them.

**Fragility**:
- 30+ env vars must be kept in sync across tmux SetEnvironment and exec-env
- Three propagation mechanisms (tmux SetEnvironment, PrependEnv inline, cmd.Env)
  can diverge
- Agent preset discovery relies on GT_AGENT or GT_PROCESS_NAMES env vars
- Role detection hierarchy (env → CWD → fallback) can produce mismatches

**API mapping**: `POST /identity` — structured identity assignment with all fields

---

## 9. Priming (context injection)

**What**: GT injects role context, work assignments, and system state at session start.

**Code**:
- `internal/cmd/prime.go` — `runPrime()` (line ~101): full prime or compact/resume path
- `internal/cmd/prime_output.go` — `outputPrimeContext()` (line ~22): role-specific
  context rendering; role functions: `outputMayorContext()`, `outputWitnessContext()`,
  `outputRefineryContext()`, `outputPolecatContext()`, `outputCrewContext()`, etc.
- `internal/cmd/prime_session.go` — `handlePrimeHookMode()` (line ~266): SessionStart
  hook integration, reads session ID from stdin JSON, persists to disk
- `internal/cmd/prime_session.go` — `detectSessionState()` (line ~202): returns
  "normal" | "post-handoff" | "crash-recovery" | "autonomous"
- `internal/cmd/prime.go` — `checkSlungWork()` (line ~421): detects hooked work,
  `outputAutonomousDirective()` (line ~542): "AUTONOMOUS WORK MODE" output
- `internal/cmd/prime_molecule.go` — `outputMoleculeContext()` (line ~182): molecule
  progress and step display

**Flow**: GT→Agent. 10-section output: beacon, handoff warning, role context,
CONTEXT.md, handoff content, attachment status, autonomous directive, molecule
context, checkpoint, startup directive.

**Fragility**:
- Non-Claude agents without hooks lose automatic priming entirely
- Compact/resume path must be lighter to prevent re-initialization loops
- Session state detection depends on handoff marker files
- Role template rendering uses Go text/template — errors silent

**API mapping**: `POST /context` with sections array and mode (full/compact/resume)

---

## 10. Hooks (settings.json installation)

**What**: GT installs hook configurations into agent runtime settings files.

**Code**:
- `internal/hooks/config.go` — `HooksConfig` (line ~28): 8 event types
  (PreToolUse, PostToolUse, SessionStart, Stop, PreCompact, UserPromptSubmit,
  WorktreeCreate, WorktreeRemove)
- `internal/hooks/config.go` — `DefaultBase()` (line ~711): base hooks including
  PR-workflow guard, dangerous-command guard, SessionStart → `gt prime --hook`,
  UserPromptSubmit → `gt mail check --inject`, Stop → `gt costs record`
- `internal/hooks/config.go` — `DefaultOverrides()` (line ~199): role-specific
  overrides (crew PreCompact → handoff cycle, witness/deacon/refinery patrol guards)
- `internal/hooks/merge.go` — `MergeHooks()` (line ~24): applies overrides in
  specificity order
- `internal/cmd/hooks_install.go` — `runHooksInstall()` (line ~48): installs hooks
  from registry to worktrees, `installHookTo()` (line ~245): loads, merges, writes
  settings.json
- `internal/hooks/config.go` — `DiscoverTargets()` (line ~382): finds all settings
  files (mayor, deacon, crew, polecats, witness, refinery per rig)
- `internal/runtime/runtime.go` — hook installer registration for 6 providers:
  claude, gemini, opencode, copilot, omp, pi

**Flow**: GT→Agent (at install time). Agent reads settings.json; GT wrote it.

**Fragility**:
- Each agent vendor has different hook formats (settings.json, plugins, extensions)
- 6 different hook providers, each with different file locations
- Non-hook agents (no framework) get no hooks at all
- Hook merging logic (base → role → rig+role) is complex

**API mapping**: `POST /authorize` (replaces PreToolUse guards),
`POST /context` (replaces SessionStart/PreCompact priming),
`POST /telemetry` (replaces Stop cost recording)

---

## 11. Guard Scripts (command blocking)

**What**: GT blocks dangerous or policy-violating commands via PreToolUse hooks.

**Code**:
- `internal/cmd/tap_guard.go` — `runTapGuardPRWorkflow()` (line ~34): blocks
  `gh pr create`, `git checkout -b`, `git switch -c` in Gas Town agent contexts;
  `isGasTownAgentContext()` (line ~103) checks GT_* env vars and CWD paths
- `internal/cmd/tap_guard_dangerous.go` — `runTapGuardDangerous()` (line ~66):
  blocks 5 patterns: `rm -rf /`, `git push --force`, `git push -f`,
  `git reset --hard`, `git clean -f`; `extractCommand()` (line ~104) parses
  Claude Code JSON hook input
- Exit code convention: 2 = BLOCK

**Flow**: Agent→GT→Agent. Agent calls hook → GT evaluates → exit 0 (allow) or 2 (block).

**Fragility**:
- Guards read hook input from stdin in Claude Code's JSON format — format change breaks
- Pattern matching is substring-based — can miss variations
- Guards fail-open on stdin errors (can't parse = allow)
- Only 3 guard scripts; coverage is incomplete

**API mapping**: `POST /authorize` — GT evaluates tool calls with full context,
returns allow/deny with reason

---

## 12. Conversation Log Access (JSONL scraping)

**What**: GT reads Claude Code's conversation transcripts for cost and session data.

**Code**:
- `internal/cmd/costs.go` — `getClaudeProjectDir()` (line ~704): maps workdir to
  `~/.claude/projects/{slug}/`; `findLatestTranscript()` (line ~717): finds most
  recent `.jsonl`; `parseTranscriptUsage()` (line ~751): line-by-line JSONL scan
  summing token usage
- `internal/cmd/seance.go` — session discovery from `.events.jsonl` (line ~61),
  fallback scan of `~/.claude/projects/` (line ~513), `sessions-index.json` (line ~674)
- Data structure: `TranscriptMessage` with `Type`, `SessionId`, `Message.Model`,
  `Message.Usage.{InputTokens, CacheCreationInputTokens, CacheReadInputTokens,
  OutputTokens}`

**Flow**: Agent→GT (inferred). Claude Code writes JSONL; GT scrapes filesystem.

**Fragility**:
- Path encoding convention (slashes→dashes) is undocumented Claude Code internal
- JSONL message format, usage field nesting can change without notice
- Three independent JSONL parsers (agentlog, costs.go, seance) — no shared code
- `sessions-index.json` format is Claude Code internal
- Non-Claude agents don't produce JSONL transcripts

**API mapping**: `POST /telemetry` — agent pushes structured usage events

---

## 13. Token Usage & Cost Tracking

**What**: GT computes session costs from transcript token counts and hardcoded pricing.

**Code**:
- `internal/cmd/costs.go` — 1516 lines total:
  `calculateCost()` (line ~801): token→USD using `modelPricing` map,
  `extractCostFromWorkDir()` (line ~823): extract from Claude transcript,
  `runCostsRecord()` (line ~956): Stop hook appends to `~/.gt/costs.jsonl`,
  `runCostsDigest()` (line ~1155): daily digest bead from costs.jsonl
- `internal/cmd/costs.go` — `modelPricing` (line ~222): hardcoded table
  (Opus: $15/$75, Sonnet: $3/$15, Haiku: $1/$5 per million tokens,
  cache read 90% discount, cache create 25% premium)
- `internal/config/cost_tier.go` — `CostTierRoleAgents()` (line ~44): maps roles to
  models per cost tier (standard/economy/budget)

**Flow**: Agent→GT (inferred). GT reads transcripts at session end.

**Fragility**:
- Pricing table is hardcoded — must be updated when Anthropic changes pricing
- Cost computed at session end via Stop hook, not real-time
- No per-bead cost attribution
- Model ID matching is fragile (substring matching against model names)
- Non-Claude agents have no cost tracking

**API mapping**: `POST /telemetry` with `usage.cost_usd` — runtime reports cost at source

---

## 14. Process Liveness Detection

**What**: GT checks if an agent process is actually running inside a tmux session.

**Code**:
- `internal/tmux/tmux.go` — `IsAgentAlive()` (line ~2157): preferred method,
  delegates to `IsRuntimeRunning()` (line ~2091) with session process names
- `internal/tmux/tmux.go` — `resolveSessionProcessNames()` (line ~2164): priority
  GT_PROCESS_NAMES env → GT_AGENT env → config fallback
- `internal/tmux/tmux.go` — `GetPaneCommand()` (line ~1579): `#{pane_current_command}`
  via tmux format
- `internal/tmux/tmux.go` — `hasDescendantWithNames()` (line ~1823): recursive
  `pgrep -P <pid> -l` tree walk (maxDepth=10)
- `internal/tmux/tmux.go` — `processMatchesNames()` (line ~1800): `ps -p <pid> -o comm=`
- `internal/tmux/tmux.go` — `getAllDescendants()` (line ~681): deepest-first process
  tree for safe cleanup
- `internal/tmux/process_group_unix.go` — `getProcessGroupMembers()` (line ~38),
  `getParentPID()` (line ~20), `getProcessGroupID()` (line ~30)
- `internal/config/agents.go` — `ProcessNames` per preset: Claude=`["node","claude"]`,
  Gemini=`["gemini"]`, etc.

**Flow**: Agent→GT (inferred). GT walks process tree; agent doesn't know.

**Fragility**:
- Process name detection relies on exact binary names
- Shell wrappers (e.g., c2claude) require descendant tree walking
- `pgrep` and `ps` output parsing is platform-dependent
- Process can exit between check and action (TOCTOU)

**API mapping**: `GET /health` — agent reports its own liveness status

---

## 15. Three-Level Health Check

**What**: GT performs a 3-level health assessment of agent sessions.

**Code**:
- `internal/tmux/tmux.go` — `CheckSessionHealth()` (line ~1771):
  Level 1: `HasSession()` (tmux session exists?),
  Level 2: `IsAgentAlive()` (agent process running?),
  Level 3: `GetSessionActivity()` (activity within maxInactivity?)
- `internal/tmux/tmux.go` — `ZombieStatus` (line ~1723): enum with
  `SessionHealthy`, `SessionDead`, `AgentDead`, `AgentHung`;
  `IsZombie()` returns true for AgentDead or AgentHung

**Flow**: GT→GT (internal health assessment).

**Fragility**:
- HungSessionThreshold = 30 minutes (hardcoded default)
- Activity timestamp from tmux `#{session_activity}` — measures any terminal
  activity, not meaningful agent work
- A sleeping agent with no output looks hung even if healthy

**API mapping**: `GET /health` — agent reports status, context_usage, last_activity

---

## 16. Heartbeat Files

**What**: GT uses heartbeat files for liveness detection outside tmux.

**Code**:
- `internal/polecat/heartbeat.go` — `TouchSessionHeartbeat()` (line ~34): writes JSON
  to `.runtime/heartbeats/<session>.json`, `IsSessionHeartbeatStale()` (line ~74):
  3-minute threshold, `ReadSessionHeartbeat()` (line ~54), `RemoveSessionHeartbeat()`
- `internal/deacon/heartbeat.go` — `WriteHeartbeat()` (line ~52): deacon heartbeat
  at `deacon/heartbeat.json` with cycle count, health stats;
  `IsFresh()` (<5min), `IsStale()` (5-15min), `IsVeryStale()` (>15min)

**Flow**: Agent→GT (implicit). Agent command writes file; GT reads it.

**Fragility**:
- File-based — no notification on write, must poll
- Stale threshold (3min) chosen empirically
- Heartbeat touch depends on GT commands being called (not agent-initiated)

**API mapping**: `GET /health` — agent reports liveness directly; no files needed

---

## 17. Working Directory Detection

**What**: GT determines an agent's working directory through multiple methods.

**Code**:
- `internal/tmux/tmux.go` — `GetPaneWorkDir()` (line ~1676):
  `#{pane_current_path}` via tmux
- `internal/workspace/find.go` — `Find()` (line ~29): walks up from CWD looking
  for `mayor/town.json` marker; handles worktree paths (polecats/, crew/);
  `FindFromCwdWithFallback()` (line ~113): GT_TOWN_ROOT env fallback for deleted
  worktrees
- `internal/config/env.go` — GT_ROOT env var set in `AgentEnv()`

**Flow**: GT→GT (detection) and GT→Agent (env var).

**Fragility**:
- 5 detection methods can disagree (tmux CWD, env vars, path parsing, git worktree)
- Worktree deletion leaves agent with no valid CWD
- GT_TOWN_ROOT fallback exists specifically because worktree cleanup breaks CWD

**API mapping**: Part of `POST /identity` — GT assigns working directory

---

## 18. Permission Bypass (YOLO flags)

**What**: GT starts all agents with vendor-specific permission bypass flags.

**Code**:
- `internal/config/agents.go` — per-preset Args:
  - Claude: `--dangerously-skip-permissions`
  - Gemini: `--approval-mode yolo`
  - Codex: `--dangerously-bypass-approvals-and-sandbox`
  - Cursor: `-f`
  - Auggie: `--allow-indexing`
  - AMP: `--dangerously-allow-all --no-ide`
  - OpenCode: env `OPENCODE_PERMISSION={"*":"allow"}`
  - Copilot: `--yolo`
- `internal/tmux/tmux.go` — `AcceptBypassPermissionsWarning()` (line ~1509):
  polls for "Bypass Permissions mode" dialog, sends Down+Enter;
  `DismissStartupDialogsBlind()` (line ~1558): blind key sequence fallback

**Flow**: GT→Agent (at startup). Always-on, no per-role granularity.

**Fragility**:
- 10 different flag names across 10 agents — each is a different string
- All-or-nothing: no per-role permission granularity
- Claude's permission warning dialog detection depends on exact text
- No opt-out — every agent runs with full bypass

**API mapping**: `POST /authorize` — per-call authorization with role-based rules

---

## 19. Non-Interactive Mode

**What**: GT runs agents in non-interactive mode for specific tasks.

**Code**:
- `internal/config/agents.go` — `NonInteractiveConfig` (line ~92):
  `ExecSubcommand` (e.g., "exec"), `PromptFlag` (e.g., "-p"),
  `OutputFormatFlag` (e.g., "--output-format json")
- `internal/config/agents.go` — `PromptMode` (line ~98): "arg" or "none"

**Flow**: GT→Agent. GT constructs CLI invocation with flags.

**Fragility**:
- Exec subcommand and flag names differ per agent
- Output format parsing depends on agent's output structure
- Not all agents support non-interactive execution

**API mapping**: `POST /prompt` with structured I/O replaces CLI flag composition

---

## 20. Session Resume/Fork

**What**: GT resumes prior sessions or forks them for conversation recall.

**Code**:
- `internal/config/agents.go` — `ResumeFlag`, `ContinueFlag`, `ResumeStyle`
  ("flag" vs "subcommand") per preset; `BuildResumeCommand()` (line ~534)
- `internal/cmd/seance.go` — `runSeance()` (line ~85): spawns
  `claude --fork-session --resume <id>` for predecessor recall
- `internal/session/startup.go` — `FormatStartupBeacon()` (line ~69):
  `[GAS TOWN] recipient <- sender • timestamp • topic` format

**Flow**: GT→Agent. GT constructs resume command with session ID.

**Fragility**:
- Resume semantics differ per agent (flag vs subcommand)
- `--fork-session` is Claude Code specific
- Session ID stored in env vars and files — multiple sources of truth
- Beacon format parsed by LLMs — format changes affect comprehension

**API mapping**: `POST /context` with `mode: "resume"` and session history

---

## 21. Config Directory Isolation

**What**: GT isolates agent configuration per account to support credential rotation.

**Code**:
- `internal/config/agents.go` — `ConfigDirEnv` (e.g., "CLAUDE_CONFIG_DIR"),
  `ConfigDir` (e.g., ".claude") per preset
- `internal/config/env.go` — `CLAUDE_CONFIG_DIR` set in `AgentEnv()` (line ~148)
- `internal/quota/keychain.go` — `KeychainServiceName()` (line ~35): SHA-256 hash
  of config dir for per-account keychain isolation
- Account directory pattern: `~/.claude-accounts/<handle>/`

**Flow**: GT→Agent. GT sets config dir env; agent uses it for all settings.

**Fragility**:
- Config dir layout is Claude Code internal
- Symlink switching between accounts is fragile
- SHA-256 keying of keychain service names depends on Claude Code convention

**API mapping**: `POST /identity` with `credentials` — runtime manages its own config

---

## 22. Theme/Display (tmux status bar)

**What**: GT applies role-specific tmux status bar themes.

**Code**:
- `internal/cmd/theme.go` — `runTheme()`: applies role/rig-specific tmux status
  line formatting
- Applied during `StartSession()` step in `internal/session/lifecycle.go`

**Flow**: GT→tmux. Display-only, doesn't affect agent behavior.

**Fragility**:
- Purely cosmetic — but theme strings used in idle detection (⏵⏵)
- Theme depends on tmux being the terminal multiplexer

**API mapping**: Not part of agent API — display concern stays in GT

---

## 23. Agent Output Capture (tmux capture-pane)

**What**: GT reads agent terminal output for various purposes.

**Code**:
- `internal/tmux/tmux.go` — `CapturePaneTrimmed()`, `CapturePaneLines()`:
  captures N lines from agent's terminal
- Used by: idle detection (5 lines), rate limit scanning (30 lines),
  dialog detection, readiness polling, nudge verification
- `internal/telemetry/recorder.go` — `RecordPaneRead()` (line ~266): OTel event
  for every capture-pane call

**Flow**: Agent→GT (inferred). GT reads terminal; agent doesn't know.

**Fragility**:
- Terminal content is unstructured text — parsing is always regex/heuristic
- Capture-pane only gets visible terminal buffer — scrollback limited
- Multi-pane sessions require FindAgentPane() first

**API mapping**: Eliminated — `POST /lifecycle` and `POST /telemetry` provide
structured data; no need to scrape terminal

---

## 24. Done/Exit Signaling

**What**: Agent signals work completion through GT commands and intent files.

**Code**:
- `internal/cmd/done.go` — `runDone()` (line ~81): retired completion model,
  transitions clean completions to DONE and exits the session; exit constants: `ExitCompleted`,
  `ExitEscalated`, `ExitDeferred` (line ~65)
- `internal/cmd/signal_stop.go` — `runSignalStop()` (line ~47): Stop hook handler,
  checks unread mail and hooked work, returns JSON
  `{"decision":"block"|"approve","reason":"..."}`
- `internal/witness/handlers.go` — `HandlePolecatDone()` (line ~110): processes
  POLECAT_DONE messages

**Flow**: Agent→GT. Agent calls `gt done`; GT processes exit type.

**Fragility**:
- Done detection relies on agent calling `gt done` (a GT CLI command)
- Stop hook must parse Claude Code's expected JSON format
- 4 exit types but no structured error reporting
- Stop state tracking (in /tmp) to prevent infinite block loops

**API mapping**: `POST /lifecycle` with `event: "stopping"` + exit metadata

---

## 25. Environment Variable Injection

**What**: GT injects 30+ env vars into agent sessions via tmux.

**Code**:
- `internal/config/env.go` — `AgentEnv()` (line ~65): generates full env map
  (GT_*, BD_*, GIT_*, CLAUDE_*, OTEL_*, credential passthrough)
- Three propagation mechanisms:
  1. `tmux.SetEnvironment()` — session-level via `set-environment`
  2. `config.PrependEnv()` — inline `export K=V &&` before command
  3. `config.EnvForExecCommand()` — `cmd.Env` append for subprocess
- Safety guards: `NODE_OPTIONS=""` (clears VSCode debugger), `CLAUDECODE=""`
  (prevents nested session detection)
- Credential passthrough: 40+ cloud API vars (Anthropic, AWS, Google, proxy, mTLS)

**Flow**: GT→Agent. GT sets env; agent inherits.

**Fragility**:
- Three propagation mechanisms can diverge
- env vars visible to any process in the session (security concern)
- Credential passthrough list must be manually maintained
- tmux SetEnvironment only affects new shell invocations, not running processes

**API mapping**: `POST /identity` with `env` map — single structured delivery

---

## 26. Telemetry (OTel integration)

**What**: GT emits OpenTelemetry metrics and logs for all agent operations.

**Code**:
- `internal/telemetry/telemetry.go` — `Init()` (line ~104): OTel provider setup,
  VictoriaMetrics/VictoriaLogs endpoints, 30s export interval
- `internal/telemetry/recorder.go` — 18 event types:
  `RecordSessionStart()`, `RecordSessionStop()`, `RecordPromptSend()`,
  `RecordPaneRead()`, `RecordPrime()`, `RecordAgentStateChange()`,
  `RecordPolecatSpawn()`, `RecordPolecatRemove()`, `RecordSling()`,
  `RecordMail()`, `RecordNudge()`, `RecordDone()`, `RecordDaemonRestart()`,
  `RecordFormulaInstantiate()`, `RecordConvoyCreate()`, `RecordPaneOutput()`,
  `RecordBDCall()`, `RecordPrimeContext()`
- 17 OTel Int64Counter metrics (gastown.session.starts.total, etc.)
- `internal/telemetry/subprocess.go` — `SetProcessOTELAttrs()`: propagates
  OTEL_RESOURCE_ATTRIBUTES to subprocesses

**Flow**: GT→Metrics backend. Agent operations tracked by GT, not agent.

**Fragility**:
- OTel export depends on VictoriaMetrics/Logs being available
- No correlation ID threads through all events (PR #2068 proposed run.id)
- Agent has no say in what's recorded or how

**API mapping**: `POST /telemetry` — agent pushes its own events with run_id

---

## 27. Event Logging (.events.jsonl)

**What**: GT logs all significant events to a JSONL file.

**Code**:
- `internal/events/events.go` — `Log()` (line ~85), `LogFeed()` (line ~98),
  `LogAudit()` (line ~103): append to `.events.jsonl` with flock
- Event types (lines ~36-77): sling, handoff, done, hook, unhook, spawn, kill,
  boot, halt, session_start, session_end, session_death, mass_death,
  patrol_*, merge_*, scheduler_*
- `internal/tui/feed/events.go` — `GtEventsSource` (line ~216): tails .events.jsonl
  for TUI feed display

**Flow**: GT→File. Events from GT operations, not agent-reported.

**Fragility**:
- Single JSONL file for all events — no rotation or size management
- flock serialization can contend under high concurrency
- No correlation ID linking events to specific agent runs

**API mapping**: `POST /telemetry` events supersede GT-side logging for agent-reported data

---

## 28. Zombie Detection & Recovery

**What**: GT detects and recovers from zombie sessions (tmux alive, agent dead).

**Code**:
- `internal/doctor/zombie_check.go` — `ZombieSessionCheck.Run()` (line ~33):
  filters known GT sessions, excludes crew, calls `IsAgentAlive()`;
  `ZombieSessionCheck.Fix()` (line ~113): re-verifies before kill (TOCTOU guard),
  never kills crew sessions
- `internal/daemon/wisp_reaper.go` — wisp reaper for stale wisp cleanup
- `internal/witness/handlers.go` — witness patrol with restart-first policy
  (not nuke-first)
- `internal/dog/health.go` — `HealthChecker.Check()` (line ~46): dog-specific
  health check using CheckSessionHealth()
- `internal/witness/spawn_count.go` — spawn storm circuit breaker:
  `ShouldBlockRespawn()` (line ~74), escalates to mayor after threshold

**Flow**: GT→GT. Internal monitoring, agent is passive subject.

**Fragility**:
- Zombie detection depends on process tree walking (platform-specific)
- Grace periods and thresholds are empirical (zombie kill grace, hung threshold)
- TOCTOU gap between detection and action
- Circuit breaker state in JSON file

**API mapping**: `GET /health` — agent reports status directly; zombie detection
becomes trivial (no response = dead)

---

## Cross-Cutting Themes

### Correlation Gap
No single ID connects: OTel event ↔ conversation transcript ↔ cost entry ↔
session event ↔ bead. `run_id` in the Factory Worker API solves this.

### Claude Code Coupling
17 of 28 touch points depend on Claude Code internals:
- Prompt prefix (`❯`), status bar (`⏵⏵`), JSONL format, config dir layout,
  keychain service naming, session index, hook JSON format, permission dialog text,
  bypass flag name, resume flag semantics, `sessions-index.json`, transcript
  message structure, usage field nesting.

### Agent Parity Gap
Non-Claude agents lose:
- Hooks (no automatic priming, guards, or mail injection)
- Conversation log access (no JSONL transcripts)
- Cost tracking (no transcript to parse)
- Resume/fork (different or no mechanism)
- Permission dialog handling (different UI)

The Factory Worker API eliminates this — one API, all agents.

### Push vs Scrape
Current: GT scrapes 6+ sources (tmux pane, JSONL files, process tree, heartbeat
files, keychain, config dirs).
Proposed: Agent pushes lifecycle events, telemetry, and health — GT never scrapes.

---

## Summary: Touch Points → API Endpoints

| # | Touch Point | Current Mechanism | API Endpoint |
|---|-------------|-------------------|-------------|
| 1 | Prompt delivery | tmux send-keys 8-step | `POST /prompt` |
| 2 | Delivery modes | immediate/queue/wait-idle | `POST /prompt` priority |
| 3 | Idle detection | prompt prefix + status bar | `POST /lifecycle` |
| 4 | Rate limit detection | pane content regex | `POST /lifecycle` |
| 5 | Account rotation | macOS keychain swap | `POST /identity` |
| 6 | Session lifecycle | 13-step tmux create | `POST /lifecycle` |
| 7 | Spawn admission | capacity gates | Internal (not agent-facing) |
| 8 | Agent identity | 30+ env vars | `POST /identity` |
| 9 | Priming | 10-section text output | `POST /context` |
| 10 | Hooks | settings.json install | Multiple endpoints |
| 11 | Guard scripts | PreToolUse exit code 2 | `POST /authorize` |
| 12 | JSONL scraping | filesystem transcript read | `POST /telemetry` |
| 13 | Cost tracking | hardcoded pricing table | `POST /telemetry` |
| 14 | Process liveness | pgrep tree walk | `GET /health` |
| 15 | Health check | 3-level tmux check | `GET /health` |
| 16 | Heartbeat files | JSON file write/poll | `GET /health` |
| 17 | Working dir detection | 5 methods (tmux, env, path) | `POST /identity` |
| 18 | Permission bypass | 10 vendor-specific flags | `POST /authorize` |
| 19 | Non-interactive mode | CLI flag composition | `POST /prompt` |
| 20 | Session resume/fork | --resume/--fork flags | `POST /context` |
| 21 | Config dir isolation | CLAUDE_CONFIG_DIR env | `POST /identity` |
| 22 | Theme/display | tmux status bar | Not agent-facing |
| 23 | Output capture | tmux capture-pane | Eliminated |
| 24 | Done/exit signaling | gt done CLI call | `POST /lifecycle` |
| 25 | Env var injection | 3 propagation mechanisms | `POST /identity` |
| 26 | OTel telemetry | GT-side recording | `POST /telemetry` |
| 27 | Event logging | .events.jsonl append | `POST /telemetry` |
| 28 | Zombie detection | process tree + thresholds | `GET /health` |

**28 touch points → 7 API endpoints.** Every hack replaced by structured communication.
