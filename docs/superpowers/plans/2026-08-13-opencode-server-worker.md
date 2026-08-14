# OpenCode Server Worker Implementation Plan

1. Add failing tests for the authenticated, directory-scoped OpenCode client,
   bounded errors, session status, async prompts, and abort.
2. Add failing tests for crash-safe state mappings and active-server checks.
3. Add failing process tests for launch arguments, random authentication,
   health readiness, version gating, early exit, and idempotent shutdown.
4. Add failing delivery tests proving queued nudges remain queued while busy,
   are submitted once when idle, and are requeued on HTTP failure.
5. Implement `internal/opencodeserver` with client, process, state, and worker
   components.
6. Add the hidden `gt opencode-worker` entry point and the opt-in
   `opencode-server` preset.
7. Route active server-backed sessions to queue delivery and suppress the tmux
   nudge poller for runtimes that manage their own nudge transport.
8. Document activation and model overrides; update the changelog.
9. Run focused tests, the real OpenCode no-model integration smoke test, then
   the repository test suite and vet.
10. Harden async delivery with a pre-prompt SSE subscription and an exclusive
    per-session worker lock; prove stale idle status cannot admit a second turn.
11. Route sling and nudge traffic outside tmux, stop stale generic pollers,
    preserve effective agents across lifecycle restarts, and protect Windows
    credentials with explicit ACLs.
