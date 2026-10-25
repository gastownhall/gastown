# OpenCode Server Worker Specification

## Objective

Add an opt-in Gas Town worker transport that controls OpenCode through its
headless HTTP server instead of typing into the OpenCode TUI. Tmux remains the
process supervisor so existing Gas Town lifecycle and cleanup code continues to
work.

The existing `opencode` preset remains unchanged. The new preset is named
`opencode-server`.

## Pinned Contract

This first implementation targets OpenCode 1.18.16 and uses its legacy HTTP
surface, which is present and verified in that release:

- `GET /global/health`
- `POST /session`
- `GET /session/status`
- `POST /session/:id/prompt_async`
- `POST /session/:id/abort`
- `GET /event`

Every instance-scoped request sends `X-OpenCode-Directory` with the worker
worktree. Every request uses HTTP Basic authentication. The server listens only
on `127.0.0.1` and receives a random per-process password through
`OPENCODE_SERVER_PASSWORD`.

The adapter accepts OpenCode 1.x versions at or above 1.18.16. Other major
versions fail closed until their API is verified.

## Lifecycle

1. Gas Town starts `gt opencode-worker <startup-prompt>` in the worker's tmux
   pane.
2. The worker starts `opencode serve` on an available loopback port and waits
   for a successful health response.
3. The worker acquires an exclusive per-Gas-Town-session lock, then creates or
   resumes one OpenCode session scoped to the worktree.
4. It atomically records the mapping under
   `<town>/.runtime/opencode-server/` with mode `0600`.
5. It snapshots the session status, then subscribes to lifecycle events before
   submitting the startup prompt asynchronously. A turn remains locally in
   flight until `session.status` reports busy and `session.idle` completes that
   transition. A `session.error` or idempotently recovered prompt requires two
   consistent idle snapshots before another prompt is admitted, so an
   immediately stale idle status cannot clear the turn.
6. It watches the Gas Town nudge queue. When OpenCode is idle, queued nudges are
   durably claimed and submitted through `prompt_async`. Claims are removed only
   after acceptance and released on failure. A stable OpenCode message ID lets a
   retry recognize an already accepted prompt without submitting it twice.
7. On shutdown it aborts an active turn and stops the server process tree. The
   server is also attached to an OS parent-death guard so abrupt tmux teardown
   cannot leave the transport running. The mapping remains so a restart on the
   same branch can resume the OpenCode
   session; a new branch replaces the mapping with a fresh session.

The lock is the ownership signal for structured routing. A healthy server with
an unlocked mapping is stale and must not receive Gas Town nudges. On Windows,
the mapping directory, lock, and credential-bearing state file use protected
ACLs limited to the current user and Local System.

The mapping records the Gas Town session, OpenCode session, branch/work key, worktree, loopback
URL, server PID, credentials, version, and creation time. Credentials never
appear in command arguments or logs.

## Configuration

The built-in `opencode-server` preset uses the OpenCode `build` agent and the
user's configured default model. Operators may override:

- `GT_OPENCODE_COMMAND`: OpenCode executable, default `opencode`
- `GT_OPENCODE_AGENT`: OpenCode agent, default `build`
- `GT_OPENCODE_MODEL`: model in `provider/model` form
- `GT_OPENCODE_VARIANT`: provider-specific model variant

OpenCode plugins remain enabled so the existing Gas Town plugin injects
`gt prime --hook` into the system prompt.

## Completion Semantics

HTTP `204` from `prompt_async` means accepted, not completed. The worker keeps
the transport busy until `/session/status` returns idle after the submitted
turn. Gas Town still decides task completion from beads, commits, tests, CI,
and merge evidence; model prose is never completion evidence.

## Non-Goals

- Replacing tmux as Gas Town's process supervisor
- Changing the existing OpenCode TUI preset
- Generalizing Gas Town's attached-Mayor ACP proxy to all workers
- Encoding scheduling or completion decisions in the transport
- Supporting remote or non-loopback OpenCode servers in this first slice
