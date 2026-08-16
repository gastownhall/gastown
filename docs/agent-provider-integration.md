# Agent Provider Integration Guide

> How to integrate your agent CLI with Gas Town (and the upcoming Gas City).

This guide is for teams building coding agent CLIs who want their agent to
participate in Gas Town's multi-agent orchestration. It explains the existing
extension points, the four tiers of integration depth, and the forward-looking
Gas City provider contract.

## What Gas Town Is

Gas Town is a multi-agent workspace manager that orchestrates coding agents
(Claude, Gemini, Codex, Cursor, AMP, OpenCode, Copilot, and others) through
tmux sessions. It provides:

- **Identity and role management** — each agent gets a role (polecat, crew,
  witness, refinery) with appropriate context and permissions
- **Work assignment** — beads (issue tracking), mail, and hook-based dispatching
- **Session lifecycle** — start, resume, handoff, and context cycling
- **Merge queue** — automated testing and merging of agent work
- **Inter-agent communication** — nudges, mail, and shared state

The key design principle is **loose coupling**: Gas Town orchestrates agents
through tmux and environment variables. It does not import agent libraries,
link against agent code, or require agents to import Gas Town code. Integration
is configuration, not compilation.

## Integration Tiers

| Tier | Effort | What You Get | What You Provide |
|------|--------|--------------|------------------|
| **0: Zero** | Nothing | Basic tmux orchestration | A CLI that runs in a terminal |
| **1: Preset** | JSON config file | Full lifecycle, resume, process detection | Preset entry in `agents.json` |
| **2: Hooks** | Settings file or plugin | Context injection, tool guards, mail delivery | Hook installer function |
| **3: Deep** | Code + scripts | Non-interactive mode, session forking, wrapper | Native API integration |

Most agent teams should target **Tier 1** first (15 minutes of work), then
**Tier 2** if their CLI supports a hooks/plugin system.

---

## Tier 0: Zero Integration

**Any CLI that runs in a terminal works in Gas Town with zero changes.**

Gas Town launches agents in tmux sessions and communicates via `send-keys`.
If your agent has a REPL or accepts text input, Gas Town can:

- Start it in a tmux pane
- Send work instructions via keystroke injection
- Detect liveness via `pane_current_command`
- Read output via `capture-pane`

This is the "tmux shim layer" — it works but is timing-sensitive and has no
delivery confirmation. You get basic orchestration for free.

**What you miss at Tier 0:**
- No session resume (fresh session every time)
- No automatic context injection (agent doesn't know its Gas Town role)
- Delay-based readiness detection (Gas Town guesses when you're ready)
- No process name detection (Gas Town can't distinguish your agent from `bash`)

---

## Tier 1: Preset Registration

**JSON config only. No code changes to Gas Town or your agent.**

A preset tells Gas Town everything it needs to launch, detect, resume, and
communicate with your agent. You register it by creating a JSON file — no
Go code, no PRs, no build steps.

### Where to put the config

There are three levels, checked in order:

| Level | Path | Scope |
|-------|------|-------|
| Town | `~/gt/settings/agents.json` | All rigs in the town |
| Rig | `~/gt/<rig>/settings/agents.json` | Single rig only |
| Built-in | Compiled into `gt` binary | Ships with Gas Town |

For external agent teams, **town-level** is the right choice. Users drop your
config into `~/gt/settings/agents.json` and every rig can use it.

### Registry schema

The file is an `AgentRegistry` JSON object:

```json
{
  "version": 1,
  "agents": {
    "kiro": {
      ...preset fields...
    }
  }
}
```

The `version` field must be `1` (current schema version). The `agents` map
keys are the agent name used in Gas Town config (e.g., `"agent": "kiro"` in
rig settings).

### AgentPresetInfo field reference

Every field from the `AgentPresetInfo` struct in `internal/config/agents.go`:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Preset identifier (e.g., `"kiro"`) |
| `command` | string | Yes | CLI binary name or path (e.g., `"kiro"`) |
| `args` | string[] | Yes | Default args for autonomous mode (e.g., `["--yolo"]`) |
| `env` | map[string]string | No | Extra env vars to set (merged with GT_* vars) |
| `process_names` | string[] | No | Process names for tmux liveness detection |
| `session_id_env` | string | No | Env var the agent sets for session ID tracking |
| `resume_flag` | string | No | Flag or subcommand for resuming sessions |
| `resume_style` | string | No | `"flag"` (e.g., `--resume <id>`) or `"subcommand"` (e.g., `resume <id>`) |
| `supports_hooks` | bool | No | Whether the agent has a hooks/plugin system |
| `supports_fork_session` | bool | No | Whether `--fork-session` is available |
| `non_interactive` | object | No | Settings for headless execution (see below) |
| `prompt_mode` | string | No | `"arg"` (prompt as CLI arg) or `"none"` (no prompt support). Default: `"arg"` |
| `config_dir_env` | string | No | Env var for agent's config directory |
| `config_dir` | string | No | Top-level config dir name (e.g., `".kiro"`) |
| `hooks_provider` | string | No | Hooks framework identifier (for Tier 2) |
| `hooks_dir` | string | No | Directory for hooks/settings files |
| `hooks_settings_file` | string | No | Settings/plugin filename |
| `hooks_informational` | bool | No | `true` if hooks are instructions-only (not executable) |
| `ready_prompt_prefix` | string | No | Prompt string for readiness detection (e.g., `"❯ "`) |
| `ready_delay_ms` | int | No | Fallback delay for readiness (milliseconds) |
| `instructions_file` | string | No | Instruction file name (default: `"AGENTS.md"`) |
| `emits_permission_warning` | bool | No | Whether agent shows a startup permission warning |

**NonInteractiveConfig** (for `non_interactive` field):

| Field | Type | Description |
|-------|------|-------------|
| `subcommand` | string | Subcommand for non-interactive execution (e.g., `"exec"`) |
| `prompt_flag` | string | Flag for passing prompts (e.g., `"-p"`) |
| `output_flag` | string | Flag for structured output (e.g., `"--json"`) |

### Example: Kiro preset

```json
{
  "version": 1,
  "agents": {
    "kiro": {
      "name": "kiro",
      "command": "kiro",
      "args": ["--autonomous"],
      "process_names": ["kiro", "node"],
      "session_id_env": "KIRO_SESSION_ID",
      "resume_flag": "--resume",
      "resume_style": "flag",
      "prompt_mode": "arg",
      "ready_prompt_prefix": "> ",
      "ready_delay_ms": 5000,
      "instructions_file": "AGENTS.md",
      "non_interactive": {
        "prompt_flag": "-p",
        "output_flag": "--json"
      }
    }
  }
}
```

### Built-in preset: GitHub Copilot CLI

`copilot` ships as a built-in preset — no JSON file needed. It uses the `--yolo` flag for
autonomous mode and flag-style session resume. Copilot CLI supports full executable lifecycle
hooks via `.github/hooks/gastown.json`:

```json
{
  "name": "copilot",
  "command": "copilot",
  "args": ["--yolo"],
  "process_names": ["copilot"],
  "resume_flag": "--resume",
  "resume_style": "flag",
  "ready_delay_ms": 5000,
  "hooks_provider": "copilot",
  "hooks_dir": ".github/hooks",
  "hooks_settings_file": "gastown.json",
  "instructions_file": "AGENTS.md"
}
```

Gas Town provisions `.github/hooks/gastown.json` in the agent's working directory with the
standard lifecycle hooks (`sessionStart`, `userPromptSubmitted`, `preToolUse`, `sessionEnd`).
This is the same hook events as Claude Code, just in Copilot's JSON format.

> **Note on readiness detection**: Copilot CLI doesn't emit a detectable prompt prefix, so
> Gas Town uses a 5-second delay instead of prompt-based detection. Sessions take slightly
> longer to become ready than Claude.

> **Enterprise requirement**: Copilot CLI must be enabled at two levels before use:
> 1. Enterprise → Settings → AI controls → Copilot → **"Copilot in the CLI" = Enabled**
> 2. Org → Settings → Copilot → Policies → **"Copilot in the CLI" = Enabled**
>
> Users also need a Copilot seat assigned. See [GitHub Copilot in the CLI](https://docs.github.com/en/copilot/using-github-copilot/using-github-copilot-in-the-command-line).

To activate:
```bash
gt config default-agent copilot        # Set as town default
gt start --agent copilot               # Or pass per-command
```

### Activating a custom preset

Once a JSON file exists, configure a rig (or the whole town) to use it:

```json
// In ~/gt/<rig>/settings/config.json
{
  "type": "rig-settings",
  "version": 1,
  "agent": "kiro"
}
```

Or set it as the town-wide default:

```json
// In ~/gt/settings/config.json
{
  "type": "town-settings",
  "version": 1,
  "default_agent": "kiro"
}
```

You can also assign agents per-role for cost optimization, or mix
providers in one town (for example Codex workers with a Pi mayor):

```bash
gt config mix default=codex mayor=pi:high crew=codex
gt config mix crew:alice=pi
```

```json
{
  "type": "town-settings",
  "version": 1,
  "default_agent": "codex",
  "role_agents": {
    "mayor": "pi",
    "crew": "codex"
  },
  "crew_agents": {
    "alice": "pi"
  }
}
```

See [Mixed agent types](guides/mixed-agents.md).

### Resolution order

When Gas Town starts an agent session, it resolves the config through this chain:

1. Role-specific override (`role_agents[role]` in rig settings)
2. Role-specific override (`role_agents[role]` in town settings)
3. Rig's `agent` field
4. Town's `default_agent` field
5. Built-in fallback: `"claude"`

At each step, the agent name is looked up in:
1. Rig's custom agents (`rig settings/agents.json`)
2. Town's custom agents (`town settings/agents.json`)
3. Built-in presets (compiled into `gt`)

This means your JSON preset is found automatically — no code change needed.

---

## Tier 2: Hooks Integration

Hooks let Gas Town inject context into your agent at session start, guard
tool calls, and deliver mail. There are three patterns depending on what
your agent supports.

### Pattern A: Claude-compatible settings.json

If your agent supports a `settings.json` with lifecycle hooks (like Claude Code
or Gemini CLI), Gas Town can install hooks automatically.

**What the hooks do:**

| Hook | Event | Command |
|------|-------|---------|
| `SessionStart` | Agent session begins | `gt prime --hook && gt mail check --inject` |
| `PreCompact` | Before context compaction | `gt prime --hook` |
| `UserPromptSubmit` | User sends a message | `gt mail check --inject` |
| `PreToolUse` | Before tool execution | `gt tap guard pr-workflow` (guards PR creation) |
| `Stop` | Session ends | `gt costs record` |

Reference template: `internal/claude/config/settings-autonomous.json`

```json
{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "gt prime --hook && gt mail check --inject"
          }
        ]
      }
    ],
    "UserPromptSubmit": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "gt mail check --inject"
          }
        ]
      }
    ]
  }
}
```

**To integrate**: Register a `HookInstallerFunc` that writes this settings
file into the correct location. The function signature
(from `internal/config/agents.go`):

```go
type HookInstallerFunc func(settingsDir, workDir, role, hooksDir, hooksFile string) error
```

Parameters:
- `settingsDir` — Gas Town-managed parent dir (used by agents with `--settings` flag)
- `workDir` — the agent's working directory (customer repo clone)
- `role` — Gas Town role (`"polecat"`, `"crew"`, `"witness"`, `"refinery"`)
- `hooksDir` — from preset's `hooks_dir` field
- `hooksFile` — from preset's `hooks_settings_file` field

Registration happens in `internal/runtime/runtime.go` via `init()`:

```go
config.RegisterHookInstaller("kiro", func(settingsDir, workDir, role, hooksDir, hooksFile string) error {
    // Write your settings file to the appropriate location
    return kiro.EnsureSettingsForRoleAt(settingsDir, role, hooksDir, hooksFile)
})
```

### Pattern B: Plugin/script hooks

If your agent uses a plugin system (like OpenCode's JS plugins), Gas Town can
install a plugin file instead of a settings.json.

Reference: `internal/hooks/templates/opencode/gastown.js`

```javascript
export const GasTown = async ({ $, directory }) => {
  const role = (process.env.GT_ROLE || "").toLowerCase();
  const autonomousRoles = new Set(["polecat", "witness", "refinery", "deacon"]);

  const run = async (cmd) => {
    try {
      await $`/bin/sh -lc ${cmd}`.cwd(directory);
    } catch (err) {
      console.error(`[gastown] ${cmd} failed`, err?.message || err);
    }
  };

  const injectContext = async () => {
    await run("gt prime");
    if (autonomousRoles.has(role)) {
      await run("gt mail check --inject");
    }
  };

  return {
    event: async ({ event }) => {
      if (event?.type === "session.created") {
        await injectContext();
      }
      if (event?.type === "session.compacted") {
        await injectContext();
      }
    },
  };
};
```

The key commands are the same (`gt prime`, `gt mail check --inject`). The
delivery mechanism adapts to the agent's plugin API.

### Pattern C: Informational hooks (instructions file)

If your agent doesn't support executable hooks but reads an instructions/context
file, Gas Town can install a markdown file with startup instructions.

Reference: `internal/hooks/templates/copilot/copilot-instructions.md`

```markdown
# Gas Town Agent Context

You are running inside Gas Town, a multi-agent workspace manager.

## Startup Protocol

On session start or after compaction, run:
\`\`\`
gt prime
\`\`\`
This loads your full role context, mail, and pending work.
```

Set `hooks_informational: true` in the preset. Gas Town will then send
`gt prime` via tmux nudge as a fallback (since hooks won't run automatically).

> **Note**: GitHub Copilot CLI previously used Pattern C, but now supports full
> executable lifecycle hooks (Pattern B equivalent, using its own JSON format).
> See the built-in Copilot preset section above for current configuration.

### How Gas Town chooses the fallback strategy

The startup fallback matrix (from `internal/runtime/runtime.go`):

| Has Hooks | Has Prompt | Context Source | Work Instructions |
|-----------|-----------|----------------|-------------------|
| Yes | Yes | Hook runs `gt prime` | In CLI prompt arg |
| Yes | No | Hook runs `gt prime` | Sent via nudge |
| No | Yes | "Run `gt prime`" in prompt | Delayed nudge |
| No | No | "Run `gt prime`" via nudge | Delayed nudge |

Agents with hooks get the most reliable experience. Without hooks, Gas Town
falls back to tmux-based delivery with timing heuristics.

---

## Tier 3: Deep Integration

These are optional capabilities that enable advanced orchestration features.

### Non-interactive mode

Used by Gas Town's formula system (automated workflows) and dogs (infrastructure
helpers) for headless execution. Configure via the `non_interactive` preset field:

```json
{
  "non_interactive": {
    "subcommand": "exec",
    "prompt_flag": "-p",
    "output_flag": "--json"
  }
}
```

Gas Town builds the command as: `kiro exec -p "prompt" --json`

### Session forking

If your agent supports forking a past session (creating a read-only copy
for inspection), set `supports_fork_session: true`. Used by the `gt seance`
command for talking to past agent sessions.

### Wrapper scripts

For agents that don't support hooks at all, a wrapper script can inject
Gas Town context before launching the agent.

Reference: `internal/wrappers/scripts/gt-codex`

```bash
#!/bin/bash
set -e

if command -v gt &>/dev/null; then
    gt prime 2>/dev/null || true
fi

exec codex "$@"
```

The wrapper runs `gt prime` before `exec`-ing the real agent binary. Users
install it as `gt-codex` in their PATH.

### Experimental Codex hooks via custom profile

Gas Town also supports an experimental opt-in Codex hooks path for users who define a custom Codex agent profile with explicit hook settings.

Use this only when both of these are true:
- Your custom agent profile sets `prompt_mode: "arg"` plus `hooks.provider: "codex"`, `hooks.dir: ".codex"`, and `hooks.settings_file: "hooks.json"`
- Codex has its upstream hooks feature enabled via `[features].codex_hooks = true`

This installs `.codex/hooks.json` through the existing provider installer path and keeps the implementation intentionally small:
- `SessionStart` runs `gt prime --hook`
- Autonomous `SessionStart` also runs `gt mail check --inject`
- `Stop` runs `gt costs record`

Example custom profile:

```json
{
  "agents": {
    "codex-worker-hooks": {
      "command": "codex",
      "args": ["--dangerously-bypass-approvals-and-sandbox"],
      "prompt_mode": "arg",
      "hooks": {
        "provider": "codex",
        "dir": ".codex",
        "settings_file": "hooks.json"
      }
    }
  }
}
```

This path does not attempt broader hook parity such as tool guards, prompt-submit hooks, or pre-compact behavior.

The default built-in `codex` preset does not change. It remains on the no-hooks fallback path, and the `gt-codex` wrapper guidance above still applies to that default path unless you explicitly opt into a custom hook-capable Codex profile.

### Slash commands

Gas Town provisions slash commands (like `/commit`, `/handoff`) into agent
config directories. If your agent reads commands from a config directory,
set `config_dir` in the preset and Gas Town will provision commands there.

---

## Capability Matrix

Current agent capabilities at a glance:

| Agent | Hooks | Resume | Non-Interactive | Fork | Prompt Mode | Process Names |
|-------|-------|--------|-----------------|------|-------------|---------------|
| Claude | Yes (settings.json) | `--resume` (flag) | Native | Yes | arg | node, claude |
| Gemini | Yes | `--resume` (flag) | `-p` | No | arg | gemini |
| Codex | No | `resume` (subcmd) | `exec` subcmd | No | none | codex |
| Cursor | Yes (`.cursor/hooks.json`) | `--resume` (flag) | `-p` / `--print` + `--output-format` | No | arg | cursor-agent, agent |
| Auggie | No | `--resume` (flag) | No | No | arg | auggie |
| AMP | No | `threads continue` (subcmd) | No | No | arg | amp |
| OpenCode | Yes (plugin JS) | No | `run` subcmd | No | none | opencode, node, bun |

---

## Gas City Provider Contract (Forward-Looking)

Gas Town is being succeeded by Gas City, which formalizes the implicit
provider interface into an explicit contract. The contract is derived from
what Gas Town currently shims via tmux — making native what was previously
heuristic.

### The interface

```
interface AgentProvider {
    // --- Tier 1: Required ---

    // Lifecycle
    Start(workDir string, env map[string]string) -> Process
    IsReady() -> bool
    IsAlive() -> bool

    // Communication
    SendMessage(text string) -> error
    GetStatus() -> AgentStatus

    // Identity
    Name() -> string
    Version() -> string

    // --- Tier 2: Preferred ---

    // Context injection
    InjectContext(context string) -> error
    OnSessionStart(callback) -> void

    // Session management
    Resume(sessionID string) -> Process
    SessionID() -> string

    // Tool guards
    OnToolCall(callback) -> void

    // --- Tier 3: Advanced ---

    // Session forking
    ForkSession(sessionID string) -> Process

    // Non-interactive execution
    Exec(prompt string) -> Result

    // Cost tracking
    GetUsage() -> UsageReport
}
```

### What stays the same

- JSON preset registration (`agents.json`)
- Environment-based identity (`GT_ROLE`, `GT_RIG`, `BD_ACTOR`)
- Hook patterns (`gt prime` for context, `gt mail check --inject` for mail)
- Tmux as the universal fallback

### What changes in Gas City

- Providers can implement `IsReady()` natively instead of relying on prompt
  prefix scanning or delay heuristics
- `SendMessage()` replaces tmux `send-keys` for providers that support it
- `GetStatus()` replaces tmux `capture-pane` screen-scraping
- `InjectContext()` provides a standard API for what hooks currently do

**Bottom line**: If you integrate at Tier 1 today (JSON preset), you're already
90% of the way to the Gas City contract. The JSON fields map directly to the
provider interface capabilities.

---

## Design Principles

### Discover, Don't Track

Agent liveness is derived from tmux state, not tracked in a database.
Process names and ready prompts are observed, not self-reported.

### ZFC: Zero Framework Cognition

The agent decides what to do with instructions. Gas Town provides transport
(tmux, hooks, nudges) but doesn't make decisions for agents. The interface
is about communication channels, not control flow.

### Graceful Degradation

Every capability has a fallback:
- No hooks? -> Startup fallback commands via tmux
- No prompt mode? -> Nudge delivery
- No resume? -> Fresh session with handoff mail
- No process API? -> Tmux pane_current_command

The system works (less reliably) with zero native API support.

---

## Common Mistakes

These are patterns we've seen in integration attempts that cause problems.

### Hardcoding into GT internals

Adding your agent as a Go constant in `agents.go`, adding switch cases in
`types.go`, or modifying `runtime.go` creates tight coupling. Your agent
becomes a build-time dependency of Gas Town. Instead, use the JSON registry
(`settings/agents.json`) which is loaded at runtime.

### Modifying default resolution functions

The `default*()` functions in `types.go` resolve values from the preset
registry. Adding agent-specific cases here means every Gas Town release must
include your agent's defaults. The preset struct already has fields for all
these values — set them in your JSON preset instead.

### Forking hook templates

Copying and modifying Claude's `settings-autonomous.json` for your agent
creates a maintenance burden. The hook commands (`gt prime`, `gt mail check`)
are agent-agnostic. Adapt them to your agent's hook format, but don't change
the underlying commands.

### Coupling to Gas Town's internal module structure

Importing Gas Town Go packages, referencing internal file paths, or depending
on internal data structures means your integration breaks when Gas Town
refactors. The public interface is:
- `gt` CLI commands (`gt prime`, `gt mail`, `gt hook`, etc.)
- Environment variables (`GT_ROLE`, `GT_RIG`, `GT_ROOT`, `BD_ACTOR`)
- JSON config files (`settings/agents.json`)

### Skipping the preset for direct RuntimeConfig hacks

The `RuntimeConfig` in rig `settings/config.json` is a backwards-compatibility
path. The modern approach is preset registration. RuntimeConfig works but
misses features like session resume, process detection, and non-interactive
mode that are only available through `AgentPresetInfo`.

---

## Step-by-Step: Integrating Your Agent Today

### Step 1: Create the preset file (5 minutes)

Create `~/gt/settings/agents.json` (or add to existing):

```json
{
  "version": 1,
  "agents": {
    "your-agent": {
      "name": "your-agent",
      "command": "your-agent-cli",
      "args": ["--autonomous", "--no-confirm"],
      "process_names": ["your-agent-cli"],
      "prompt_mode": "arg",
      "ready_delay_ms": 5000,
      "instructions_file": "AGENTS.md"
    }
  }
}
```

### Step 2: Test basic launch (5 minutes)

```bash
# Set your agent as default for a rig
gt config set agent your-agent --rig <rigname>

# Or test with a one-off override
gt crew start jack --agent your-agent
```

Verify:
- Agent starts in a tmux pane
- `gt prime` content is delivered (either via hooks, prompt, or nudge)
- Agent can receive nudges (`gt nudge <rig>/crew/jack "hello"`)

### Step 3: Add session resume (if supported)

Add to your preset:

```json
{
  "session_id_env": "YOUR_AGENT_SESSION_ID",
  "resume_flag": "--resume",
  "resume_style": "flag"
}
```

Test: Start a session, note the session ID, kill the tmux pane, and verify
the agent resumes with context when restarted.

### Step 4: Add hooks (if your agent supports them)

Choose Pattern A, B, or C from the Hooks Integration section above.

If your agent supports Claude-compatible `settings.json` hooks:
1. Set `hooks_provider`, `hooks_dir`, and `hooks_settings_file` in the preset
2. Register a `HookInstallerFunc` in your agent's Go package
3. Register it in `internal/runtime/runtime.go`'s `init()`

If your agent reads a custom instructions file:
1. Set `hooks_informational: true` in the preset
2. Set `hooks_dir` and `hooks_settings_file` to point to your instructions file
3. Register a hook installer that writes the Gas Town instructions

### Step 5: Add non-interactive mode (if supported)

Add to your preset:

```json
{
  "non_interactive": {
    "subcommand": "run",
    "prompt_flag": "-p",
    "output_flag": "--json"
  }
}
```

This enables your agent for formula execution and dog tasks.

---

## FAQ

### Do I need to submit a PR to Gas Town?

**No** for Tiers 0-1. The JSON preset is user-managed config. Users drop
the file into their town settings and it works.

**Yes** for Tier 2 (hook installer registration) if you want it built-in.
But users can also install hooks manually or via a wrapper script without
any PR.

### What if my agent doesn't support autonomous mode?

Gas Town requires autonomous mode (no confirmation prompts) for unattended
operation. If your agent doesn't have a `--yolo` or `--dangerously-skip-permissions`
equivalent, Gas Town can't use it for polecats or automated roles. It can
still work for crew (human-supervised) sessions.

### What environment variables does Gas Town set?

| Variable | Example | Purpose |
|----------|---------|---------|
| `GT_ROLE` | `gastown/crew/jack` | Agent's role in the system |
| `GT_RIG` | `gastown` | Which rig the agent belongs to |
| `GT_ROOT` | `/Users/me/gt` | Town root directory |
| `BD_ACTOR` | `gastown/crew/jack` | Beads identity for issue tracking |
| `GIT_AUTHOR_NAME` | `gastown/crew/jack` | Git commit identity |
| `GT_AGENT` | `kiro` | Which agent preset is active |
| `GT_SESSION_ID_ENV` | `KIRO_SESSION_ID` | Which env var holds the session ID |

### What is `gt prime`?

`gt prime` is the context injection command. It outputs the agent's role
documentation, mail, hooked work, and system instructions as markdown to
stdout. Agents read this output to understand their identity and current
assignment. It's the single most important Gas Town command for agents.

### Can I override a built-in preset?

Yes. User-defined agents in `settings/agents.json` take precedence over
built-in presets with the same name. You can override `"claude"` if needed.

### What's the difference between `AgentPresetInfo` and `RuntimeConfig`?

`AgentPresetInfo` is the static preset — what you configure in JSON. It
describes your agent's capabilities and defaults.

`RuntimeConfig` is the fully resolved runtime config, produced by merging
the preset with user overrides and filling in defaults. It's what Gas Town
actually uses to build the startup command.

`RuntimeConfigFromPreset()` converts one to the other.
`normalizeRuntimeConfig()` fills defaults from the preset's `default*()`
functions.

### How does process detection work?

Gas Town checks `tmux display-message -p '#{pane_current_command}'` against
the preset's `process_names` list. If your agent runs as a Node.js process,
you might need `["node", "your-agent"]` since tmux may report either name.

### How does readiness detection work?

Two strategies:

1. **Prompt prefix** — Gas Town scans the tmux pane for `ready_prompt_prefix`
   (e.g., `"❯ "`). Reliable but requires a known prompt format.
2. **Delay** — Gas Town waits `ready_delay_ms` milliseconds. Used when the
   agent has a TUI that can't be scanned for a known prompt.

Set one or both in your preset. Prompt prefix is preferred when available.
