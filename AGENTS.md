# Agent Instructions

See **CLAUDE.md** for complete agent context and instructions.

This file exists for compatibility with tools that look for AGENTS.md.

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

<!-- BEGIN BEADS INTEGRATION v:1 profile:full hash:d4f96305 -->
## Issue Tracking with bd (beads)

**IMPORTANT**: This project uses **bd (beads)** for ALL issue tracking. Do NOT use markdown TODOs, task lists, or other tracking methods.

### Why bd?

- Dependency-aware: Track blockers and relationships between issues
- Git-friendly: Dolt-powered version control with native sync
- Agent-optimized: JSON output, ready work detection, discovered-from links
- Prevents duplicate tracking systems and confusion

### Quick Start

**Check for ready work:**

```bash
bd ready --json
```

**Create new issues:**

```bash
bd create "Issue title" --description="Detailed context" -t bug|feature|task -p 0-4 --json
bd create "Issue title" --description="What this issue is about" -p 1 --deps discovered-from:bd-123 --json
```

**Claim and update:**

```bash
bd update <id> --claim --json
bd update bd-42 --priority 1 --json
```

**Complete work:**

```bash
bd close bd-42 --reason "Completed" --json
```

### Issue Types

- `bug` - Something broken
- `feature` - New functionality
- `task` - Work item (tests, docs, refactoring)
- `epic` - Large feature with subtasks
- `chore` - Maintenance (dependencies, tooling)

### Priorities

- `0` - Critical (security, data loss, broken builds)
- `1` - High (major features, important bugs)
- `2` - Medium (default, nice-to-have)
- `3` - Low (polish, optimization)
- `4` - Backlog (future ideas)

### Workflow for AI Agents

1. **Check ready work**: `bd ready` shows unblocked issues
2. **Claim your task atomically**: `bd update <id> --claim`
3. **Work on it**: Implement, test, document
4. **Discover new work?** Create linked issue:
   - `bd create "Found bug" --description="Details about what was found" -p 1 --deps discovered-from:<parent-id>`
5. **Complete**: `bd close <id> --reason "Done"`

### Auto-Sync

bd automatically syncs via Dolt:

- Each write auto-commits to Dolt history
- Use `bd dolt push`/`bd dolt pull` for remote sync
- No manual export/import needed!

### Important Rules

- ✅ Use bd for ALL task tracking
- ✅ Always use `--json` flag for programmatic use
- ✅ Link discovered work with `discovered-from` dependencies
- ✅ Check `bd ready` before asking "what should I work on?"
- ❌ Do NOT create markdown TODO lists
- ❌ Do NOT use external issue trackers
- ❌ Do NOT duplicate tracking systems

For more details, see README.md and docs/QUICKSTART.md.

## Landing the Plane (Session Completion)

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

## Landing the Plane (Session Completion)

**When ending a work session**, you MUST complete ALL steps below. Work is NOT complete until `git push` succeeds.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **PUSH TO REMOTE** - This is MANDATORY:
   ```bash
   git pull --rebase
   bd sync
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
