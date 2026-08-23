# Local Rig Bootstrap

For a NightRider-style local setup, use a clean bootstrap when no Gas Town rig
layout exists yet. Use `gt rig add --adopt` when the directory already contains
Mayor or polecat work that must remain in place.

Adoption restores the canonical shared `.repo.git` and dedicated `refinery/rig`
worktree when they are missing. It reads the rig's configured repository and
default branch, and it does not rewrite or re-parent existing Mayor and polecat
worktrees. A conflicting noncanonical refinery directory is reported as an error
instead of being replaced or used as a fallback.

Use the bootstrap script instead:

```bash
./scripts/bootstrap-local-rig.sh \
  --town-root /gt \
  --rig nightrider_local \
  --local-repo /gt/nightRider \
  --prefix nr \
  --polecat-agent claude \
  --witness-agent codex \
  --refinery-agent codex
```

If you omit `--remote`, the script registers the rig with `file://<local-repo>`.
That is usually the right choice for local-only or private repos inside the
Gastown container, where the upstream remote may not be reachable or authenticated.

What this does:

- Uses `gt rig add <name> <git-url> --local-repo <path>` so Gas Town creates a fresh,
  standard rig container instead of inheriting a hand-built one.
- Reuses objects from the local repo, so bootstrap stays fast and does not modify the
  source repo.
- Leaves the resulting rig with the normal `.repo.git`, `mayor/rig`, `refinery/rig`,
  `settings/`, and `.beads/` layout that Gas Town expects.
- Optionally pins per-rig role agents in `settings/config.json`.

When to still use `--adopt`:

- You already have a Gas Town rig directory that was created elsewhere and only need
  to register it in a town.
- The rig has active Mayor or polecat work, but `.repo.git` or `refinery/rig` is
  missing and must be restored without touching that work.
