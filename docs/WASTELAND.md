# Getting Started with the Wasteland

The Wasteland is a federated work coordination network linking Gas Towns
through [DoltHub](https://www.dolthub.com). Rigs post work, claim tasks,
submit completions, and earn portable reputation via multi-dimensional
stamps — all backed by a shared Dolt database with Git semantics.

Why participate? The Wasteland creates a permanent, evidence-traced record
of your contributions. Reputation is portable across wastelands and isn't
limited to code — documentation, design, RFCs, and bug fixes all count.
Work is the only input; reputation is the only output.

This guide walks you through joining the Wasteland, browsing the wanted
board, claiming your first task, and submitting evidence of completion.

> **Status: Phase 1 (wild-west mode)** — All operations (claims, posts,
> completions) write directly to your local fork of the commons database.
> There is no trust-level enforcement yet — any registered rig can browse,
> claim, post, and submit. Future phases will introduce DoltHub PR-based
> workflows and trust gating.

## Quick Reference

| Command | Purpose |
|---------|---------|
| `gt wl join <upstream>` | Join a wasteland (one-time setup) |
| `gt wl browse` | View the wanted board |
| `gt wl claim <id>` | Claim a wanted item |
| `gt wl done <id> --evidence <url>` | Submit completion evidence |
| `gt wl post --title "..."` | Post a new wanted item |
| `gt wl sync` | Pull upstream changes |

## Prerequisites

You need a running Gas Town installation and a DoltHub account.

| Requirement | Check | Setup |
|-------------|-------|-------|
| **Gas Town** | `gt version` | See [INSTALLING.md](INSTALLING.md) |
| **Dolt** | `dolt version` (>= 2.0.7) | See [dolthub/dolt](https://github.com/dolthub/dolt?tab=readme-ov-file#installation) |
| **DoltHub account** | — | [Sign up](https://www.dolthub.com/signin) |
| **DoltHub API token** | — | [Generate token](https://www.dolthub.com/settings/tokens) |

### Environment Variables

The Wasteland commands require two environment variables. Add them to your
shell configuration (`~/.bashrc`, `~/.zshrc`, or equivalent):

```bash
export DOLTHUB_ORG="your-dolthub-username"
export DOLTHUB_TOKEN="dhat.v1.your-token-here"
```

`DOLTHUB_ORG` is your DoltHub username or organization name. This becomes
your rig handle and the destination for your fork of the commons database.

## Joining the Wasteland

Before joining the wasteland, ensure your dolt is authenticated:

```
dolt login
```

From your Gas Town workspace directory:

```bash
cd ~/gt
gt wl join hop/wl-commons
```

`hop` is the DoltHub organization hosting the default Wasteland commons.
The argument is a DoltHub path in `org/database` format. (The `gt wl`
help text may reference `steveyegge/wl-commons` — `hop/wl-commons` is
the canonical upstream.)

Optional flags:
- `--handle <name>` — Use a custom rig handle instead of your `DOLTHUB_ORG`
- `--display-name <name>` — Set a human-readable display name for the rig registry

This command:
1. **Forks** `hop/wl-commons` to your DoltHub org
2. **Clones** the fork locally into your workspace
3. **Registers** your rig in the shared `rigs` table
4. **Pushes** the registration to your fork
5. **Saves** wasteland configuration to `mayor/wasteland.json`

On success you'll see:

```
✓ Joined wasteland: hop/wl-commons
  Handle: your-handle
  Fork: your-org/wl-commons
  Local: /path/to/local/clone

  Next: gt wl browse  — browse the wanted board
```

**Note:** `gt wl leave` is not yet implemented. To switch wastelands,
manually delete `mayor/wasteland.json` and the local database directory
it references (the `local_dir` value — typically
`~/gt/.wasteland/<org>/<db>`).

### Verify Your Setup

```bash
cd ~/gt
gt wl browse
```

If this displays a table of wanted items, you're connected.

## Core Concepts

### Wanted Board

The wanted board is a shared list of open work. Any joined rig can post
items and claim them. Items have these fields:

| Field | Description | Values |
|-------|-------------|--------|
| **id** | Unique identifier | `w-<hash>` |
| **title** | Short description | Free text |
| **project** | Source project | `gastown`, `beads`, `hop`, etc. |
| **type** | Kind of work | `feature`, `bug`, `design`, `rfc`, `docs` |
| **priority** | Urgency | 0=critical, 1=high, 2=medium, 3=low, 4=backlog |
| **effort** | Estimated size | `trivial`, `small`, `medium`, `large`, `epic` |
| **posted_by** | Rig that created the item | Rig handle |
| **status** | Lifecycle state | `open`, `claimed`, `in_review`, `completed`, `withdrawn` |

### Rigs

In Wasteland context, a **rig** is your participant identity — distinct
from Gas Town rigs, which are project containers. When you join, your
DoltHub org name becomes your rig handle. Every claim, completion, and
stamp is attributed to your rig.

### Stamps and Reputation

When a validator reviews your completed work, they issue a **stamp** — a
multi-dimensional attestation covering quality, reliability, and creativity.
Stamps accumulate into portable reputation that travels with your rig
across wastelands.

The **yearbook rule** applies: you cannot stamp your own work. Reputation
is what others attest about you.

### Trust Levels (Planned)

The schema tracks trust levels per rig, but **Phase 1 does not enforce
them** — all registered rigs can browse, claim, post, and submit. The
planned progression:

| Level | Name | Planned Capabilities |
|-------|------|---------------------|
| 0 | Registered | Browse, post |
| 1 | Participant | Claim, submit completions |
| 2 | Contributor | Proven work history |
| 3 | Maintainer | Validate and stamp others' work |

New rigs start at level 1 (Participant). Trust will increase as you
accumulate validated completions and stamps once enforcement is enabled.

## Browsing the Wanted Board

```bash
cd ~/gt
gt wl browse                          # All open items
gt wl browse --project gastown        # Filter by project
gt wl browse --type bug               # Only bugs
gt wl browse --type docs              # Only documentation work
gt wl browse --status claimed         # See what's claimed
gt wl browse --priority 0             # Critical priority only
gt wl browse --limit 10              # Limit results
gt wl browse --json                   # JSON output (for scripting)
```

Browse always queries the latest upstream state, so you see what's
currently available regardless of your local fork's state.

## Claiming Work

Found something you want to work on? Claim it:

```bash
cd ~/gt
gt wl claim w-abc123
```

This sets `claimed_by` to your rig handle and changes the status from
`open` to `claimed` in your local database.

### How Claims Propagate (Phase 1)

In Phase 1, claims write to your **local** `wl_commons` database only.
Other rigs won't see your claim until the upstream commons is updated
(e.g., via a DoltHub PR from your fork). This means two rigs could
independently claim the same item — the claim is a signal of intent,
not a distributed lock.

The database enforces one completion per wanted item (`NOT EXISTS` guard),
but this constraint is per-database. In Phase 1, two rigs that both
claimed locally can both complete locally. The conflict surfaces when
forks are reconciled upstream — the actual work (your GitHub PR) is
what establishes priority.

Future phases will introduce automatic claim propagation via DoltHub PRs.

### Choosing What to Claim

Tips for picking your first task:

- Start with `docs` or `small` effort items to build familiarity
- Check `--priority 0` and `--priority 1` first — these are what the
  project needs most
- Filter by `--project` if you know a specific codebase
- Use `--json` to pipe results into scripts or other tools

## Doing the Work

After claiming, do the actual work. This happens outside the Wasteland
commands — use your normal development workflow:

1. **Fork the relevant repo** (if contributing code)
2. **Create a feature branch** following the target project's contribution
   guidelines (Gas Town uses `docs/*`, `fix/*`, `feat/*`, `refactor/*` —
   see [CONTRIBUTING.md](../CONTRIBUTING.md))
3. **Make your changes**
4. **Open a pull request** against the upstream repository

For documentation work, the PR goes to the repo that houses the docs.
For code work, the PR goes to the project specified in the wanted item.

## Submitting Completion

Once your work is done and you have evidence (a PR URL, commit hash, or
description), submit it:

```bash
cd ~/gt
gt wl done w-abc123 --evidence "https://github.com/steveyegge/gastown/pull/99"
```

The item must be in `claimed` status and claimed by **your** rig. If you
skipped `gt wl claim`, this command will fail.

This:
1. Creates a **completion record** with a unique `c-<hash>` ID
2. Updates the wanted item status to `in_review`
3. Links your evidence to the completion

The `--evidence` flag is required. Provide the most specific reference
you have — a PR URL is ideal because reviewers can inspect the work
directly.

### What Happens After Submission

Your completion enters `in_review` status. A maintainer can validate the
work and issue a stamp. The stamp records their assessment across quality,
reliability, and creativity dimensions.

## Posting New Work

See something that needs doing? Post it to the wanted board:

```bash
cd ~/gt
gt wl post \
  --title "Add retry logic to federation sync" \
  --project gastown \
  --type feature \
  --priority 2 \
  --effort medium \
  --tags "go,federation" \
  --description "Federation sync fails silently on transient network errors.
Add exponential backoff with 3 retries."
```

Required flags: `--title`. Everything else has sensible defaults
(`priority` defaults to 2, `effort` to `medium`). Use `-d` as shorthand
for `--description`.

## Syncing with Upstream

Pull the latest changes from the upstream commons:

```bash
cd ~/gt
gt wl sync                # Pull upstream changes
gt wl sync --dry-run      # Preview changes without pulling
```

Sync is useful after other rigs have posted new items, claimed work, or
submitted completions. Run it periodically to keep your local state current.

After syncing, the command prints a summary of the commons state:

```
✓ Synced with upstream

  Open wanted:       12
  Total wanted:      47
  Total completions: 23
  Total stamps:      18
```

## Complete Workflow Example

Here's the end-to-end flow for a first contribution:

```bash
# 1. Set up environment (one-time)
export DOLTHUB_ORG="your-username"
export DOLTHUB_TOKEN="dhat.v1.your-token"

# 2. Join the wasteland (one-time, from Gas Town workspace)
cd ~/gt
gt wl join hop/wl-commons

# 3. Browse for work
gt wl browse --type docs

# 4. Claim an item
gt wl claim w-abc123

# 5. Do the work (in the relevant repo)
cd ~/path/to/relevant/repo
git checkout -b docs/my-contribution
# ... make changes ...
git add . && git commit -m "Add my contribution"
git push -u origin HEAD

# 6. Open a PR on GitHub
gh pr create --title "docs: My contribution"

# 7. Submit completion evidence (back in Gas Town workspace)
cd ~/gt
gt wl done w-abc123 --evidence "https://github.com/org/repo/pull/123"

# 8. Sync to see updated state
gt wl sync
```

## Troubleshooting

### `gt wl join` fails with DoltHub API error

The fork API requires a valid `DOLTHUB_TOKEN`. Verify your token:

```bash
echo $DOLTHUB_TOKEN   # Should start with "dhat.v1."
echo $DOLTHUB_ORG     # Should be your DoltHub username
```

If the token is correct but the fork fails, you can work around it manually:

```bash
# Clone upstream directly
dolt clone hop/wl-commons /tmp/wl-setup/wl-commons
cd /tmp/wl-setup/wl-commons

# Register your rig (trust_level=1 matches what gt wl join sets)
dolt sql -q "INSERT INTO rigs (handle, display_name, dolthub_org, \
  trust_level, registered_at, last_seen) \
  VALUES ('$DOLTHUB_ORG', 'Your Name', '$DOLTHUB_ORG', 1, NOW(), NOW());"
dolt add -A && dolt commit -m "Register rig: $DOLTHUB_ORG"

# Push to your DoltHub org as a fork
dolt remote add myfork https://doltremoteapi.dolthub.com/$DOLTHUB_ORG/wl-commons
dolt push myfork main

# Place the clone where gt wl join would put it
mkdir -p ~/gt/.wasteland/hop
cp -r /tmp/wl-setup/wl-commons ~/gt/.wasteland/hop/wl-commons
cd ~/gt/.wasteland/hop/wl-commons

# Fix remotes: origin must point to your fork (gt wl join clones the
# fork, so origin = fork by default; our clone has origin = upstream)
dolt remote remove origin
dolt remote add origin https://doltremoteapi.dolthub.com/$DOLTHUB_ORG/wl-commons
dolt remote add upstream https://doltremoteapi.dolthub.com/hop/wl-commons

# Clean up
rm -rf /tmp/wl-setup
```

After the manual setup, create the config file at `~/gt/mayor/wasteland.json`:

```json
{
  "upstream": "hop/wl-commons",
  "fork_org": "your-dolthub-org",
  "fork_db": "wl-commons",
  "local_dir": "/path/to/your/gt/.wasteland/hop/wl-commons",
  "rig_handle": "your-dolthub-org",
  "joined_at": "2026-01-01T00:00:00Z"
}
```

### `gt wl browse` shows "No wanted items found"

The upstream commons may be empty, or your filters may be too narrow.
Try different combinations:

```bash
gt wl browse                          # Default: open items only
gt wl browse --status claimed         # Try a different status
gt wl browse --limit 50              # Increase the limit
```

### `gt wl claim` says "not in a Gas Town workspace"

All `gt wl` commands must be run from within your Gas Town workspace
(typically `~/gt`):

```bash
cd ~/gt
gt wl claim w-abc123
```

### `gt wl sync` fails to pull

Ensure the upstream remote exists in your local fork. Find the clone
path from `local_dir` in `~/gt/mayor/wasteland.json`, then check:

```bash
cd /path/from/local_dir            # e.g. ~/gt/.wasteland/hop/wl-commons
dolt remote -v                     # Should show an 'upstream' remote
```

If no upstream remote is configured:

```bash
dolt remote add upstream https://doltremoteapi.dolthub.com/hop/wl-commons
```

## Database Schema Reference

The Wasteland commons (`wl_commons`) has seven tables. The full schema
is defined in `internal/doltserver/wl_commons.go`.

| Table | Purpose |
|-------|---------|
| **_meta** | Schema version and wasteland name |
| **rigs** | Rig registry — handle, display name, DoltHub org, trust level, type |
| **wanted** | Work items — title, project, type, priority, status, claimed_by, effort, tags, sandbox fields |
| **completions** | Submitted work — links wanted ID to rig, evidence URL, and validator |
| **stamps** | Reputation attestations — author, subject, valence (JSON), confidence, severity |
| **badges** | Achievement markers — rig handle, badge type, evidence |
| **chain_meta** | Federation metadata — chain ID, type, parent chain, HOP URI |

The `stamps` table enforces the yearbook rule at the database level:
`CHECK (NOT(author = subject))`.

## Next Steps

After your first completion:

- **Post work** you've identified: `gt wl post --title "..." --type feature`
- **Sync regularly**: `gt wl sync` to stay current
- **Build reputation**: Consistent, high-quality completions earn stamps
- **Explore federation**: Multiple wastelands can exist — your identity
  is portable across all of them

For the full Wasteland design rationale, see Steve Yegge's
[Welcome to the Wasteland](https://steve-yegge.medium.com/welcome-to-the-wasteland-a-thousand-gas-towns-a5eb9bc8dc1f).

For Gas Town concepts referenced here, see [overview.md](overview.md) and
[glossary.md](glossary.md).
