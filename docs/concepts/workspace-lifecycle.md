# Workspace Lifecycle and Storage Law

Gas Town owns the durable location and lifecycle of repositories used by its
agents. A project is registered once as a rig; work then happens only in a
managed lane created by Gas Town.

## Canonical placement

1. Run `gt rig list`. If the project is absent, register it with
   `gt rig add <rig> <git-url>`.
2. For persistent operator work, run
   `gt crew add <name> --rig <rig> --branch` and use
   `<town>/<rig>/crew/<name>`.
3. For a tracked agent task, claim its bead and run
   `gt sling <bead-id> <rig>`; Gas Town creates a polecat lane under
   `<town>/<rig>/polecats/<name>`.
4. Keep `<town>/<rig>/mayor/rig` clean. It is the integration and control
   checkout, not a development workspace. If changes appear there, preserve
   them on a crew branch, validate them, and restore the Mayor checkout.

Do not create a raw clone for a project already managed by the town. Do not
create a manual worktree beside a rig.

## Temporary-storage law

System `/tmp` is limited to small operating-system artifacts with bounded
lifetime and deterministic cleanup. It must not contain project clones,
worktrees, virtual environments, database copies, persistent caches, or broad
Go build trees.

Broad builds and tests must set `TMPDIR` and `GOTMPDIR` to an ignored,
project-scoped directory on a capacity-checked filesystem and remove that
directory on normal exit and interruption. External projects that are not Gas
Town rigs still belong under a persistent workspace root, never `/tmp`.

Run `make temp` in the Gas Town source checkout before handoff. The audit is
read-only and fails when it finds a Git checkout or a known high-growth build,
clone, database, or virtual-environment pattern directly under `/tmp`.
