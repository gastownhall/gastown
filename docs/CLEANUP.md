# Gastown/Beads Cleanup Commands Reference

A comprehensive catalog of all cleanup-related commands in the gastown/beads ecosystem, organized by scope and severity.

---

## Process Cleanup

| Command | What it does |
|---------|-------------|
| `gt cleanup` | Kills orphaned Claude processes not tied to active tmux sessions |
| `gt orphans procs list` | Lists orphaned Claude processes (PPID=1) |
| `gt orphans procs kill` | Kills orphaned Claude processes (`--aggressive` for tmux-verified) |
| `gt deacon cleanup-orphans` | Kills orphaned Claude subagent processes (no controlling TTY) |
| `gt deacon zombie-scan` | Finds/kills zombie Claude processes not in active tmux sessions |

## Polecat (Agent Sandbox) Cleanup

| Command | What it does |
|---------|-------------|
| `gt polecat remove <rig>/<polecat>` | Removes polecat worktree/directory (fails if session running) |
| `gt polecat nuke <rig>/<polecat>` | Nuclear: kills session, deletes worktree, deletes branch, closes bead |
| `gt polecat nuke <rig> --all` | Nukes all polecats in a rig |
| `gt polecat gc <rig>` | GC stale polecat branches (orphaned, old timestamped) |
| `gt polecat stale <rig>` | Detects stale polecats; `--cleanup` auto-nukes them |
| `gt polecat check-recovery` | Pre-nuke safety check (SAFE_TO_NUKE vs NEEDS_RECOVERY) |
| `gt polecat identity remove <rig> <name>` | Removes a polecat identity |
| `gt done` | Polecat self-cleaning: pushes branch, submits MR/PR path as configured, preserves handoff metadata, kills own session. MR skipped for `--status ESCALATED\|DEFERRED` or `no_merge` paths |

## Git Artifact Cleanup

| Command | What it does |
|---------|-------------|
| `gt prune-branches` | Removes stale local polecat tracking branches (`git fetch --prune` + safe delete) |
| `gt orphans` | Finds orphaned commits never merged (detection only) |
| `gt orphans kill` | Prunes orphaned commits (`git gc --prune=now`) + kills orphaned processes |

## Rig-Level Cleanup

| Command | What it does |
|---------|-------------|
| `gt rig reset` | Resets handoff content, stale mail, orphaned in_progress issues |
| `gt rig reset --handoff` | Clears handoff content only |
| `gt rig reset --mail` | Clears stale mail only |
| `gt rig reset --stale` | Resets orphaned in_progress issues |
| `gt rig remove <name>` | Unregisters rig from registry, cleans up beads routes |
| `gt rig shutdown <rig>` | Stops all agents: polecats, refinery, witness |
| `gt rig stop <rig>...` | Stop one or more rigs |
| `gt rig restart <rig>...` | Stop then start (stop phase cleans up) |

## Town-Wide Shutdown

| Command | What it does |
|---------|-------------|
| `gt down` | Stops all infrastructure (refinery, witness, mayor, boot, deacon, daemon, dolt) |
| `gt down --polecats` | Also stops all polecat sessions |
| `gt down --all` | Full shutdown with orphan cleanup and verification |
| `gt down --nuke` | Kills entire tmux server (DESTRUCTIVE - kills non-GT sessions too) |
| `gt shutdown` | "Done for the day" - stops agents AND removes polecat worktrees/branches. Flags control aggressiveness (`--graceful`, `--force`, `--nuclear`, `--polecats-only`, etc.) |

## Crew Workspace Cleanup

| Command | What it does |
|---------|-------------|
| `gt crew stop [name]` | Stops crew tmux sessions |
| `gt crew restart [name]` | Kills and restarts crew fresh ("clean slate", no handoff mail) |
| `gt crew remove <name>` | Removes workspace, closes agent bead |
| `gt crew remove <name> --purge` | Full obliteration: deletes agent bead, unassigns beads, clears mail |
| `gt crew pristine [name]` | Syncs workspaces with remote (`git pull`) |

## Ephemeral Data / Event Cleanup

| Command | What it does |
|---------|-------------|
| `gt compact` | TTL-based compaction: promotes/deletes wisps past their TTL |
| `gt krc prune` | Prunes expired events from the KRC event store |
| `gt krc config reset` | Resets KRC TTL configuration to defaults |
| `gt krc decay` | Shows forensic value decay report (pruning guidance) |

## Dolt Database Cleanup

| Command | What it does |
|---------|-------------|
| `gt dolt cleanup` | Removes orphaned databases from `.dolt-data/` |
| `gt dolt stop` | Stops the Dolt SQL server |
| `gt dolt rollback [backup-dir]` | Restores `.beads` from backup, resets metadata |

## Bead / Hook Cleanup

| Command | What it does |
|---------|-------------|
| `gt close <bead-id>` | Closes beads (lifecycle termination) |
| `gt unsling` / `gt unhook` | Removes work from agent's hook, resets bead status to "open" |
| `gt hook clear` | Alias for unsling |

## Dog (Infrastructure Worker) Cleanup

| Command | What it does |
|---------|-------------|
| `gt dog remove <name>` | Removes worktrees and dog directory |
| `gt dog remove --all` | Removes all dogs |
| `gt dog clear <name>` | Resets stuck dog to idle state |
| `gt dog done [name]` | Marks dog as done, clears work field |

## Convoy Cleanup

| Command | What it does |
|---------|-------------|
| `gt convoy close <id>` | Closes a convoy bead |
| `gt convoy land <id>` | Closes convoy, cleans up polecat worktrees, sends completion notifications |

## Mail Cleanup

| Command | What it does |
|---------|-------------|
| `gt mail delete <msg-id>` | Deletes specific messages |
| `gt mail archive <msg-id>` | Archives messages (`--stale` for stale ones) |
| `gt mail clear [target]` | Deletes all messages from an inbox (town quiescence) |

## Misc State Cleanup

| Command | What it does |
|---------|-------------|
| `gt namepool reset` | Releases all claimed polecat names |
| `gt checkpoint clear` | Removes checkpoint file |
| `gt issue clear` | Clears issue from tmux status line |
| `gt doctor --fix` | Auto-fixes: orphan sessions, wisp GC, stale redirects, worktree validity |

## System-Level Cleanup

| Command | What it does |
|---------|-------------|
| `gt disable --clean` | Disables gastown + removes shell integration |
| `gt shell remove` | Removes shell integration from RC files |
| `gt config agent remove <name>` | Removes custom agent definition |
| `gt uninstall` | Full removal: shell integration, wrapper scripts, state/config/cache dirs |
| `make clean` | Removes compiled `gt` binary |

## Scripts

| Command | What it does |
|---------|-------------|
| `scripts/migration-test/reset-vm.sh` | Restores VM to pristine v0.5.0 state (test environments) |

## Internal (Automatic / Side-Effect)

| Function | Where | What it does |
|----------|-------|-------------|
| `cleanupOrphanedProcesses()` | `polecat.go` | Auto-runs after nuke/stale cleanup |
| `retirePolecatSessionAfterDone()` | `done.go` | Self-terminates tmux session with PID exclusion after durable handoff |
| `rollbackSlingArtifacts()` | `sling.go` | Cleans up partial sling failures |
| `cleanStaleHookedBeads()` | `unsling.go` | Repairs beads stuck in "hooked" state |
| `gt signal stop` | `signal_stop.go` | Clears stop-state temp files at turn boundaries |
| `make install` | `Makefile` | Removes stale `~/go/bin/gt` and `~/bin/gt` binaries |

---

## Cleanup Layers (Low to High Severity)

| Layer | Scope | Key Commands |
|-------|-------|-------------|
| **L0** | Ephemeral data | `gt compact`, `gt krc prune` (TTL-based lifecycle) |
| **L1** | Processes | `gt cleanup`, `gt orphans procs kill`, `gt deacon cleanup-orphans` |
| **L2** | Git artifacts | `gt prune-branches`, `gt polecat gc`, `gt orphans kill` |
| **L3** | Agents/sessions | `gt polecat nuke`, `gt done`, `gt shutdown`, `gt down` |
| **L4** | Workspace | `gt rig reset`, `gt doctor --fix`, `gt dolt cleanup` |
| **L5** | System | `gt uninstall`, `gt disable --clean` |

**Total: ~62 commands/functions** across the cleanup ecosystem.
