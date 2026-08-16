# Agent Instructions

**jonbaldie/gastown** is home. Ship to `origin`. `upstream` is `gastownhall/gastown` for syncs when asked. For a `gastownhall/gastown` contribution, read CONTRIBUTING.md.

> **Recovery**: Run `gt prime` after compaction, clear, or new session

Full context is injected by `gt prime` at session start.

<!-- beads-agent-instructions-v2 -->

---

## Beads Workflow Integration

This project uses [beads](https://github.com/steveyegge/beads) for issue tracking. Issues live in `.beads/` and are tracked in git.

Two CLIs: **bd** (issue CRUD) and **bv** (graph-aware triage, read-only).

### bd: Issue Management

```bash
bd ready              # Unblocked issues ready to work
bd list --status=open # All open issues
bd show <id>          # Full details with dependencies
bd create --title="..." --type=task --priority=2
bd update <id> --status=in_progress
bd close <id>         # Mark complete
bd close <id1> <id2>  # Close multiple
bd dep add <a> <b>    # a depends on b
bd sync               # Sync with git
```

### bv: Graph Analysis (read-only)

**NEVER run bare `bv`** — it launches interactive TUI. Always use `--robot-*` flags:

```bash
bv --robot-triage     # Ranked picks, quick wins, blockers, health
bv --robot-next       # Single top pick + claim command
bv --robot-plan       # Parallel execution tracks
bv --robot-alerts     # Stale issues, cascades, mismatches
bv --robot-insights   # Full graph metrics: PageRank, betweenness, cycles
```

### Workflow

1. **Start**: `bd ready` (or `bv --robot-triage` for graph analysis)
2. **Claim**: `bd update <id> --status=in_progress`
3. **Work**: Implement the task
4. **Complete**: `bd close <id>`
5. **Sync**: `bd sync` at session end

### Session Close Protocol

```bash
git status            # Check what changed
git add <files>       # Stage code changes
bd sync               # Commit beads changes
git commit -m "..."   # Commit code
bd sync               # Commit any new beads changes
git push              # Push to remote
```

### Key Concepts

- **Priority**: P0=critical, P1=high, P2=medium, P3=low, P4=backlog (numbers only)
- **Types**: task, bug, feature, epic, question, docs
- **Dependencies**: `bd ready` shows only unblocked work

<!-- end-beads-agent-instructions -->

<!-- gastown-agent-instructions-v1 -->

---

## Gas Town Multi-Agent Communication

This workspace is part of a **Gas Town** multi-agent environment. You communicate
with other agents using `gt` commands — never by printing text or using raw tmux.

### Nudging Agents (Immediate Delivery)

`gt nudge` sends a message directly to another agent's active session:

```bash
gt nudge mayor "Status update: PR review complete"
gt nudge laneassist/crew/dom "Check your mail — PR ready for review"
gt nudge witness "Polecat health check needed"
gt nudge refinery "Merge queue has items"
```

**Target formats:**
- Role shortcuts: `mayor`, `deacon`, `witness`, `refinery`
- Full path: `<rig>/crew/<name>`, `<rig>/polecats/<name>`

**Important:** `gt nudge` is the ONLY way to send text to another agent's session.
Never print "Hey @name" — the other agent cannot see your terminal output.

### Sending Mail (Persistent Messages)

`gt mail` sends messages that persist across session restarts:

```bash
# Reading
gt mail inbox                    # List messages
gt mail read <id>                # Read a specific message

# Sending (use --stdin for multi-line content)
gt mail send mayor/ -s "Subject" -m "Short message"
gt mail send laneassist/crew/dom -s "PR Review" --stdin <<'BODY'
Multi-line message content here.
Details about the PR and what to look for.
BODY
gt mail send --human -s "Subject" -m "Message to overseer"
```

### When to Use Which

| Want to... | Command | Why |
|------------|---------|-----|
| Wake a sleeping agent | `gt nudge <target> "msg"` | Immediate delivery |
| Send detailed task/info | `gt mail send <target> -s "..." --stdin` | Persists across restarts |
| Both: send + wake | `gt mail send` then `gt nudge` | Mail carries payload, nudge wakes |

### Context Recovery

After compaction or new session, run `gt prime` to reload your full role context,
identity, and any pending work.

```bash
gt prime              # Full context reload
gt hook               # Check for assigned work
gt mail inbox         # Check for messages
```

<!-- end-gastown-agent-instructions -->

<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:ca08a54f -->
## Beads Issue Tracker

This project uses **bd (beads)** for issue tracking. Run `gt prime` to see full workflow context and commands.

### Quick Reference

```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --claim  # Claim work
bd close <id>         # Complete work
```

### Rules

- Use `bd` for ALL task tracking — do NOT use TodoWrite, TaskCreate, or markdown TODO lists
- Run `gt prime` for detailed command reference and session close protocol
- Use `bd remember` for persistent knowledge — do NOT use MEMORY.md files

## Session Completion

**When ending a work session**, you MUST complete ALL steps below. Work is NOT complete until `git push` succeeds.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **PUSH TO REMOTE** - This is MANDATORY:
   ```bash
   git pull --rebase
   bd dolt push
   git push
   git status  # MUST show "up to date with origin"
   ```
5. **Clean up** - Clear stashes, prune remote branches
6. **Verify** - All changes committed AND pushed
7. **Hand off** - Provide context for next session

**CRITICAL RULES:**
- Work is NOT complete until `git push` succeeds
- NEVER stop before pushing - that leaves work stranded locally
- NEVER say "ready to push when you are" - YOU must push
- If push fails, resolve and retry until it succeeds
<!-- END BEADS INTEGRATION -->

---

## Cursor Cloud specific instructions

Gas Town is a Go CLI (`gt`). The environment build already has: Go (base `go1.22.2`, but
`go.mod` pins `go 1.26.2`, auto-fetched via `GOTOOLCHAIN=auto`), `libicu-dev` (required for the
`go-icu-regex` CGo dependency), `golangci-lint` v2.11.4, `bd` (beads), and `dolt` v2.0.7. The
update script runs `go mod download` to refresh module deps after a pull.

### Build / lint / test / run

- Build: `make build` → produces `gt`, `gt-proxy-server`, `gt-proxy-client` at repo root.
  Default is `CGO_ENABLED=0` (no beads embedded Dolt engine).
  `make build-dev` builds only `gt`. `make build-cgo` restores the CGO graph.
  Bare `go build` / `go run` do not set `CGO_ENABLED`; use `make` or `CGO_ENABLED=0`.
- Lint: `golangci-lint run --timeout=5m` (installed at `~/go/bin`).
  - GOTCHA: `golangci-lint` refuses to run if it was built with an older Go than `go.mod`'s
    `1.26.2` ("the Go language version go1.25 ... is lower than the targeted Go version 1.26.2").
    `go install ...@v2.11.4` picks the tool's own toolchain (1.25). It must be built with
    `GOTOOLCHAIN=go1.26.2 go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.11.4`.
    The prebuilt binary in the snapshot is already correct.
- Test (unit): `go test -short ./...`. Full suite is slow (~15-20 min) because many packages spawn
  real `git`/`tmux` subprocesses.
- Integration tests are build-tagged and need Dolt: `go test -tags=integration ./internal/cmd/...`.
  Dolt Docker helpers in `internal/testutil` compile only with `-tags=integration`.
  Default `go test` uses skip stubs so unit compiles do not pull testcontainers.
- Run the app: `gt install <path>` creates a town HQ and auto-starts a Dolt SQL server; then
  `gt status`, `bd create/list/update/close` (beads work ledger), `gt rig add`, etc. `gt` must be on
  `PATH` for hooks/crew workflows (`cp gt ~/go/bin/` or `make install` → `~/.local/bin`).

### Known environment-induced test failures (NOT code bugs)

Under a full `go test ./...` in the cloud VM, these fail purely due to the VM's global git config
and full-suite parallelism — they all pass when isolated:

- `internal/git` (`TestConfigurePushURL`, `TestGetPushURL_NoPushURL`, `TestClearPushURL`) and
  `internal/doctor` (`TestBareRepoExistsCheck_*PushURL*`): the cloud VM injects Cursor-managed
  `url.https://x-access-token:***@github.com/.insteadOf` rewrites into the global git config, so
  round-tripped remote URLs don't match. Re-run with a clean config to confirm green, e.g.
  `TMP=$(mktemp); printf '[user]\n\tname=t\n\temail=t@t' >$TMP; GIT_CONFIG_GLOBAL=$TMP go test ./internal/git/ ./internal/doctor/`.
- `internal/config` `TestBuiltinPresets` / `TestRuntimeConfigFromPreset` can flake under
  `./...` when a sibling test overlays the live agent registry (`Command = env`,
  empty `ProcessNames`). Those tests now read the compile-time `builtinPresets`
  table. They pass reliably via `go test ./internal/config/`.

### Agent skills

Project skills live in `.agents/skills/`. That tree vendors
[`mattpocock/skills`](https://github.com/mattpocock/skills) and
[`jonbaldie/skills`](https://github.com/jonbaldie/skills) so `/ship-spec` can
reach `/implement` in the same directory. See `.agents/skills/NOTICE` for the
pinned commits and license.

Refresh from the collection installer:

```bash
curl -fsSL https://raw.githubusercontent.com/jonbaldie/skills/main/install.sh | bash -s -- --project --agent universal --copy --with-prereqs --yes
```

The environment snapshot may still install the same collections globally under
`~/.agents/skills/` and mirror them to `~/.cursor/skills-cursor/` (Cursor Cloud
discovers global skills there, not `~/.cursor/skills`). That snapshot copy is a
fallback for sessions that boot before a pull. The in-repo tree is the source of
truth for this project. The snapshot refresh is best-effort (`|| true`). Changes
under `.agents/` skip the Go CI workflows.
