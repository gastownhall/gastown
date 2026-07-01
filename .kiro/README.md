# Kiro CLI in this repo

This directory contains workspace-scoped Kiro CLI context for Gas Town.

## Gas Town runtime preset

Gas Town has a built-in `kiro` agent preset. After building `gt`, use Kiro CLI
as a Gas Town runtime with:

```bash
gt config default-agent kiro
gt start --agent kiro
```

The preset launches:

```bash
kiro-cli chat --trust-all-tools
```

Gas Town passes startup beacons as positional chat input, uses
`kiro-cli chat --resume` for latest-session resume, and uses
`kiro-cli chat --resume-id <id>` for explicit session resume.

## Kiro workspace context

Kiro CLI reads project steering files from `.kiro/steering/`. The files here
cover:

- `product.md` - what Gas Town is and why it exists
- `tech.md` - the main stack, build/test commands, and runtime assumptions
- `structure.md` - where core packages and docs live

For agent workflow rules that apply across tools, read the root `AGENTS.md`.

