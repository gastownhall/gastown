+++
name = "git-hygiene"
description = "Clean up stale git branches, stashes, and loose objects across all rig repos"
version = 1

[gate]
type = "cooldown"
duration = "12h"

[tracking]
labels = ["plugin:git-hygiene", "category:cleanup"]
digest = true

[execution]
timeout = "10m"
notify_on_failure = true
severity = "low"
+++

# Git Hygiene

Automated cleanup of stale git branches, stashes, and loose objects across all
rig repos. Covers local branches (merged and orphaned), remote branches on
GitHub, stale stashes, and garbage collection.

Requires: `gh` CLI installed and authenticated (`gh auth status`).

## Safety: report-only by default

This plugin **does not delete anything** unless `GIT_HYGIENE_APPLY=1` is set.
By default it reports what it *would* delete and records a run, leaving every
repo untouched.

This matters because several of its steps are irreversible and operate well
beyond the local machine:

- it force-deletes local branches (`git branch -D`), and in this town a polecat
  branch is often the only durable record of that worker's commits;
- it **deletes remote branches on GitHub** via `gh api ... -X DELETE`, including
  branches in third-party orgs;
- it runs `git stash clear` and `git gc --prune=now`.

Review a report-only run before enabling deletion:

```bash
bash plugins/git-hygiene/run.sh                     # report only
GIT_HYGIENE_APPLY=1 bash plugins/git-hygiene/run.sh # actually delete
```

## How to run

Run the script. Do **not** execute the underlying git commands by hand: the
script holds the full logic *and* the safety gating, so performing the steps
manually bypasses report-only mode and deletes for real.

```bash
bash ~/gt/plugins/git-hygiene/run.sh
```

That is report-only and is what a scheduled patrol run must use. It enumerates
every rig from the `repo_path` field of `gt rig list --json`, prints each
branch, stash and gc it *would* touch as a `WOULD ...` line, records the run,
and changes nothing.

## Enabling deletion (humans only)

Deletion is opt-in and must never be enabled automatically:

```bash
GIT_HYGIENE_APPLY=1 bash ~/gt/plugins/git-hygiene/run.sh
```

Always review a report-only run first. Some branches in this town exist only
locally, so deleting them destroys the only copy, and the remote step removes
branches from GitHub orgs including third-party ones.

## Result

The script records its own outcome through `gt plugin record-run` on every
path, including skips, so there is nothing to record manually.

If the script exits non-zero, escalate:

```bash
gt escalate "Plugin FAILED: git-hygiene" \
  --severity low \
  --reason "$ERROR"
```
