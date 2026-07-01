# Product Overview

Gas Town is a multi-agent workspace manager for coordinating AI coding agents
across git-backed projects. It gives each agent a role, persistent identity,
mailbox, work queue, and reproducible workspace so long-running work can survive
session restarts, context loss, and concurrent execution.

Primary users are developers and operators running multiple coding agents over
one or more repositories. Gas Town should make those agents easier to supervise:
tasks are tracked through Beads, agents communicate through `gt mail` and
`gt nudge`, and completed work flows through the refinery merge queue.

Important product principles:

- Preserve work state in files, git worktrees, and Beads rather than transient
  chat memory.
- Keep runtime integration loose. Gas Town launches agents through tmux,
  environment variables, settings files, hooks, and CLI flags rather than
  linking to agent SDKs.
- Prefer explicit status and handoff paths. Agents should leave enough context
  for another session to resume safely.
- Treat Claude Code as the default runtime while keeping Codex, Gemini, Cursor,
  OpenCode, Copilot, Kiro, and other runtimes selectable through presets.

