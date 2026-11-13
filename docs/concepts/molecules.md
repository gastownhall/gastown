# Molecules

Molecules are workflow templates that coordinate multi-step work in Gas Town.

## Molecule Lifecycle

```
Formula (source TOML) ─── "Ice-9"
    │
    ▼ bd cook
Protomolecule (frozen template) ─── Solid
    │
    ├─▶ bd mol pour ──▶ Mol (persistent) ─── Liquid
    │
    └─▶ bd mol wisp --root-only ──▶ Root Wisp (ephemeral) ─── Vapor
```

**Root-only wisps** (default): Formula steps are NOT materialized as database rows.
Only a single root wisp is created. Agents read steps inline from the embedded
formula at prime time. This prevents wisp accumulation (~6,000+ rows/day → ~400/day).

**Poured wisps** (`pour = true`): Steps ARE materialized as sub-wisps with
checkpoint recovery. If a session dies, completed steps remain closed and work
resumes from the last checkpoint. Use pour for expensive, low-frequency workflows
where losing progress would be costly (e.g., release workflows).

## Core Concepts

| Term | Description |
|------|-------------|
| **Formula** | Source TOML template defining workflow steps |
| **Protomolecule** | Frozen template ready for instantiation |
| **Molecule** | Active workflow instance (root wisp only) |
| **Wisp** | Ephemeral molecule for patrols and polecat work (never synced) |
| **Root-only** | Only root wisp created; steps read from embedded formula |
| **Pour** | Formula flag (`pour = true`); steps materialized as sub-wisps with checkpoint recovery |

## How Agents See Steps

Agents do NOT use `bd mol current` or `bd close <step-id>` for formula workflows.
Instead, formula steps are rendered inline when the agent runs `gt prime`:

```
**Formula Checklist** (10 steps from mol-polecat-work):

### Step 1: Load context and verify assignment
Initialize your session and understand your assignment...

### Step 2: Set up working branch
Ensure you're on a clean feature branch...
```

The agent works through the checklist and runs `gt done` (polecats) or
`gt patrol report` (patrol agents) when complete.

## Molecule Commands

### Beads Operations (bd)

```bash
# Formulas
bd formula list              # Available formulas
bd formula show <name>       # Formula details
bd cook <formula>            # Formula → Proto

# Molecules (data operations)
bd mol list                  # Available protos
bd mol show <id>             # Proto details
bd mol wisp <proto>          # Create wisp (root-only by default)
bd mol bond <proto> <parent> # Attach to existing mol
```

### Agent Operations (gt)

```bash
# Hook management
gt hook                    # What's on MY hook?
gt prime                   # Shows inline formula checklist
gt mol attach <bead> <mol>   # Pin molecule to bead
gt mol detach <bead>         # Unpin molecule from bead

# Patrol lifecycle
gt patrol new              # Create patrol wisp and hook it
gt patrol report --summary "..."  # Close current patrol, start next cycle
```

## Polecat Workflow

Polecats receive work via their hook — a root wisp attached to an issue.
They see the formula checklist inline when they run `gt prime` and work
through each step in order.

### Polecat Workflow Summary

```
1. Spawn with work on hook
2. gt prime               # Shows formula checklist inline
3. Work through each step
4. Persist findings: bd update <issue> --notes "..."
5. gt done                # Submit, preserve handoff metadata, exit
```

### Molecule Types

| Type | Storage | Use Case |
|------|---------|----------|
| **Root-only Wisp** (`pour = false`) | `.beads/` (ephemeral) | Polecat work, patrols — high frequency, cheap steps |
| **Poured Wisp** (`pour = true`) | `.beads/` (sub-wisps) | Releases, long workflows — low frequency, expensive steps |

**Heuristic**: If you would curse losing the progress after a crash, set `pour = true`.
High frequency + cheap steps = inline (default). Low frequency + expensive steps = pour.

### "0 steps" is not a bug

A root-only wisp legitimately has no child rows, so anything that counts child
rows reports zero. That is the design working, not a lost pour. Confirm with:

```bash
bd mol wisp <formula> --dry-run
# Dry run: would create wisp with 1 issue (root only) from proto <formula>
#   Note: N child step(s) skipped — set pour=true in formula to materialize them
```

Code that consumes a molecule must therefore read steps from the formula, never
from child rows:

- `gt mol progress` / `gt mol dag` resolve the formula behind a childless root
  (from its `attached_formula:` field, or from the root title, which `bd mol wisp`
  sets to the formula name) and render the declared checklist. They report no
  per-step state, because none exists.
- The daemon's dog molecules (`internal/daemon/dog_molecule.go`) accumulate step
  outcomes in memory and write a summary onto the root wisp when the cycle closes,
  instead of closing per-step child wisps.

Do **not** fix a "0 steps" report by setting `pour = true` on a high-frequency
formula. `mol-dog-doctor` alone runs every 5 minutes with 13 steps; materializing
those steps would recreate the ~3.7k wisps/day flood that root-only removed.

## Patrol Workflow

Patrol agents (Deacon, Witness, Refinery) cycle through patrol formulas:

```
1. gt patrol new          # Create root-only patrol wisp
2. gt prime               # Shows patrol checklist inline
3. Work through each step
4. gt patrol report --summary "..."  # Close + start next cycle
```

`gt patrol report` atomically closes the current patrol root and spawns
a new one for the next cycle.

## Best Practices

1. **Persist findings early** — `bd update <issue> --notes "..."` before session death
2. **Run `gt done` when complete** — mandatory for polecats (pushes, submits to MQ/PR path, exits)
3. **Use `gt patrol report`** — for patrol agents to cycle (replaces squash+new pattern)
4. **File discovered work** — `bd create` for bugs found, don't fix them yourself
