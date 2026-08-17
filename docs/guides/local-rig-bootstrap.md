# Local Rig Bootstrap

For a parent folder of project repositories, use `gt from`. That command
creates a Town next to the folder (or at a path you pass) and adds one Rig
per child Git repository. Original working trees stay in place so Compose
can keep running from the parent folder.

```bash
gt from ~/code
gt from ~/code ~/gt
gt from ~/code --dry-run
```

`gt from` never uses `--adopt`. Use `--adopt` only for an already-assembled
Rig directory. Use the bootstrap script below for one local repository when
you already have a Town.

`--adopt` is meant for registering an already-assembled rig directory. It trusts the
existing shape, which makes it a poor fit for manually assembled local rigs where
`.repo.git`, worktrees, and metadata may already be inconsistent.

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

- You already have a real Gas Town rig directory that was created elsewhere and you
  only need to register it in a town.
