# Factory Worker API

Design for the API boundary between Gas Town and AI agent runtimes.

Ref: gt-5zs8

## Problem

Gas Town has no stable interface with AI agents. Every integration is a hack:

- **Prompt delivery**: tmux send-keys with 512-byte chunking, ESC+600ms readline
  dance, Enter retries, SIGWINCH wake
- **Idle detection**: regex matching on Claude Code's `❯` prompt prefix (with NBSP
  normalization) and `⏵⏵` status bar parsing
- **Telemetry**: filesystem scraping of `~/.claude/projects/<slug>/<session>.jsonl`
- **Cost tracking**: parsing token usage from JSONL transcripts with hardcoded pricing
- **Account rotation**: macOS-only keychain token swapping
- **Liveness**: walking tmux process trees via `pane_current_command` + `pgrep`
- **Guard scripts**: exit code 2 from PreToolUse hooks to block commands
- **Permission bypass**: many vendor-specific trust/approval flags, one per agent runtime

28+ touch points cataloged. All depend on implementation details of Claude Code,
tmux, macOS Keychain, or filesystem conventions that can change without notice.

## Design Principles

1. **Push, not scrape.** The agent reports its state; GT doesn't guess from
   terminal output.
2. **Structured, not string-matched.** JSON messages, not regex on pane content.
3. **Agent-agnostic.** One API, regardless of whether the worker is Claude, Gemini,
   Codex, Kiro, or a custom runtime.
4. **Correlation by design.** A single `run_id` threads through every event from
   spawn to death.
5. **Fail-closed defaults.** Unknown state = don't send work, don't kill session.

## API Surface

### 1. Lifecycle

The runtime reports lifecycle transitions. GT does not infer them.

```
POST /lifecycle
{
  "event": "started" | "ready" | "busy" | "idle" | "stopping" | "stopped",
  "run_id": "uuid",
  "session_id": "gt-crew-max",
  "timestamp": "2026-03-01T15:00:00Z",
  "metadata": {}           // event-specific (e.g., exit_code for "stopped")
}
```

Replaces: prompt prefix matching, status bar parsing, `pane_current_command`,
`IsAgentAlive()`, `GetSessionActivity()`, heartbeat files, `WaitForIdle()` polling,
`WaitForRuntimeReady()` polling.

**Key events:**

| Event | Replaces | When |
|-------|----------|------|
| `started` | session creation detection | Agent process begins |
| `ready` | `WaitForRuntimeReady()` polling | Agent ready for first prompt |
| `idle` | prompt prefix + status bar detection | Turn complete, awaiting input |
| `busy` | "esc to interrupt" detection | Processing a prompt |
| `stopping` | done-intent file detection | Agent initiating shutdown |
| `stopped` | process tree check | Agent process exited |

### 2. Prompt Submission

GT sends structured messages. No terminal injection.

```
POST /prompt
{
  "run_id": "uuid",
  "content": "Review PR #2068...",
  "priority": "normal" | "urgent" | "system",
  "source": "nudge" | "mail" | "sling" | "prime",
  "metadata": {
    "from": "gastown/crew/tom",
    "bead_id": "gt-abc12"
  }
}

Response:
{
  "accepted": true,
  "queued": false,          // true if agent was busy and queued it
  "position": 0             // queue position if queued
}
```

Replaces: `NudgeSession()` (8-step tmux send-keys protocol), 512-byte chunking,
ESC+readline dance, debounce timers, nudge queue JSON files, `UserPromptSubmit`
hook drain, large-prompt temp file workaround.

**Priority semantics:**
- `system`: injected at turn boundary (replaces `<system-reminder>` blocks)
- `urgent`: interrupts current work (replaces immediate nudge)
- `normal`: delivered when idle (replaces wait-idle + queue fallback)

### 3. Context Injection (Priming)

Structured context delivery at session start and compaction.

```
POST /context
{
  "run_id": "uuid",
  "sections": [
    {"type": "role", "content": "You are a polecat worker..."},
    {"type": "work", "content": "AUTONOMOUS WORK MODE: gt-abc12..."},
    {"type": "mail", "content": "2 unread messages..."},
    {"type": "checkpoint", "content": "Previous session state..."},
    {"type": "directive", "content": "Execute your hooked work."}
  ],
  "mode": "full" | "compact" | "resume"
}
```

Replaces: `gt prime` pipeline (10-section output), `SessionStart` hook,
`PreCompact` hook, beacon injection, startup nudge fallback for non-hook agents,
role template rendering to stdout.

### 4. Tool Authorization

The runtime asks GT before executing tools. GT decides.

```
POST /authorize
{
  "run_id": "uuid",
  "tool": "Bash",
  "input": {"command": "git push --force"},
  "context": {
    "role": "polecat",
    "rig": "gastown",
    "bead_id": "gt-abc12"
  }
}

Response:
{
  "allowed": false,
  "reason": "force push blocked by dangerous-command guard"
}
```

Replaces: `PreToolUse` hook with exit code 2, PR-workflow guard, dangerous-command
guard, patrol-formula guard, per-agent `--dangerously-*` flags.

**Permission model:**
- Per-role permission sets (polecat: full, witness: read-only, crew: configurable)
- Guard rules as data, not shell scripts
- Fail-closed: if GT is unreachable, block the tool call

### 5. Telemetry & Cost Reporting

The runtime pushes structured events. No filesystem scraping.

```
POST /telemetry
{
  "run_id": "uuid",
  "events": [
    {
      "type": "turn_complete",
      "timestamp": "2026-03-01T15:01:00Z",
      "usage": {
        "input_tokens": 12000,
        "output_tokens": 3500,
        "cache_read_tokens": 8000,
        "cache_creation_tokens": 0,
        "model": "claude-opus-4-6",
        "cost_usd": 0.2325
      },
      "tools_called": [
        {"name": "Bash", "success": true, "duration_ms": 1200},
        {"name": "Read", "success": true, "duration_ms": 50}
      ]
    }
  ]
}
```

Replaces: JSONL transcript scraping, `extractCostFromWorkDir()`, hardcoded pricing
table, `agentlog` package (Claude Code JSONL tailing), `RecordPaneRead`, stop hook
`gt costs record`, 6 separate log files with no correlation.

**What flows:**
- Token usage per turn (not per content block — avoids double-counting)
- Cost computed at source (runtime knows the model and pricing)
- Tool call results with success/failure and timing
- All events carry `run_id` for correlation

### 6. Identity & Credentials

GT assigns identity; the runtime authenticates.

```
POST /identity
{
  "run_id": "uuid",
  "role": "polecat",
  "rig": "gastown",
  "agent_name": "alpha",
  "session_id": "gt-gastown-alpha",
  "credentials": {
    "type": "api_key" | "oauth" | "token",
    "value": "sk-ant-...",
    "expires_at": "2026-03-02T00:00:00Z"
  },
  "env": {
    "GT_ROLE": "gastown/polecats/alpha",
    "BD_ACTOR": "gastown/polecats/alpha",
    "GT_ROOT": "/Users/stevey/gt"
  }
}
```

Replaces: `AgentEnv()` (30+ env vars via tmux `SetEnvironment` + `PrependEnv`),
macOS keychain token swapping, `CLAUDE_CONFIG_DIR` isolation, account switching
symlinks, `GT_QUOTA_ACCOUNT` env var, credential passthrough allowlist.

**Credential rotation:**
- GT pushes new credentials when rotating; runtime applies them without restart
- No keychain dependency — works on any OS
- Runtime reports credential expiry; GT proactively rotates before it hits

### 7. Health & Liveness

Bidirectional health checks.

```
GET /health
Response:
{
  "status": "healthy" | "degraded" | "unhealthy",
  "run_id": "uuid",
  "uptime_seconds": 3600,
  "current_state": "idle" | "busy" | "stopping",
  "last_activity": "2026-03-01T15:00:00Z",
  "context_usage": 0.73,       // fraction of context window used
  "error": null
}
```

Replaces: `CheckSessionHealth()` (3-level tmux check), `IsAgentAlive()` (process
tree walking), `GetSessionActivity()` (tmux activity timestamp), heartbeat files,
`TouchSessionHeartbeat()`, zombie detection heuristics, spawn storm detection.

**Context window pressure** is a new signal — the runtime knows how full its context
is. GT can use this to trigger compaction/handoff before the agent degrades.

## Transport

The API is local-only. Two options:

**Unix domain socket** (preferred): `$GT_ROOT/.runtime/worker.sock`
- No network exposure
- File-permission-based access control
- GT is the server; the agent runtime is the client
- Agent connects at startup, maintains persistent connection

**Embedded HTTP**: localhost with random port, written to a well-known file.
- Fallback for runtimes that can't do Unix sockets
- Port file at `$GT_ROOT/.runtime/worker-<session>.port`

## Migration

The factory worker API doesn't require replacing Claude Code. It can be implemented
as a **sidecar** that:

1. Runs alongside Claude Code in the same tmux session
2. Translates between the API and Claude Code's existing mechanisms (hooks,
   JSONL, send-keys)
3. Progressively replaces tmux-mediated interactions as the sidecar matures

This lets us validate the API design before building a full GT-native runtime.

## Non-Goals

- **Multi-machine orchestration.** This is a local API between GT and a worker on
  the same machine.
- **Agent intelligence.** The API is plumbing, not policy. What the agent *does*
  with prompts is its business.
- **Backward compatibility with every agent vendor.** The sidecar handles
  translation. The API is designed for a GT-native runtime.

## Open Questions

1. Should prompt submission be synchronous (block until accepted) or fire-and-forget
   with status callbacks?
2. Should tool authorization be per-call (latency cost) or per-session with a
   pre-negotiated capability set?
3. How does compaction/handoff work? Does GT tell the runtime "compact now" or does
   the runtime report context pressure and GT decides?
4. Should the runtime expose its conversation history, or is the telemetry stream
   sufficient for GT's needs?
