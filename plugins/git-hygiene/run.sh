#!/usr/bin/env bash
# git-hygiene/run.sh — Report (and optionally clean) stale branches, stashes,
# and loose objects across all rig repos.
#
# SAFETY: This plugin defaults to REPORT-ONLY. It has never run destructively
# in this town, and its deletions are irreversible in places where a branch is
# the only durable record of a worker's commits. Nothing is mutated unless
# GIT_HYGIENE_APPLY=1 is set explicitly.
#
# Covers:
# - Merged local branches
# - Orphan local branches (polecat/*, dog/*, fix/*, etc. with no remote)
# - Merged remote branches on GitHub
# - Stale stashes
# - Git garbage collection

set -euo pipefail

log() { echo "[git-hygiene] $*"; }

# --- Mode --------------------------------------------------------------------

if [ "${GIT_HYGIENE_APPLY:-0}" = "1" ]; then
  DRY_RUN=0
  MODE="apply"
else
  DRY_RUN=1
  MODE="report-only"
fi

# record <result> <title> <description>
record() {
  gt plugin record-run --plugin git-hygiene --result "$1" \
    --title "$2" --description "$3" >/dev/null 2>&1 || true
}

# act <description> -- <command...>
# Runs the command when applying; only logs it when reporting.
act() {
  local desc="$1"; shift
  [ "$1" = "--" ] && shift
  if [ "$DRY_RUN" = "1" ]; then
    log "    WOULD $desc"
    return 0
  fi
  log "    $desc"
  "$@"
}

log "Mode: $MODE (set GIT_HYGIENE_APPLY=1 to actually delete)"

# --- Enumerate rig repos -----------------------------------------------------

RIG_JSON=$(gt rig list --json 2>/dev/null) || {
  log "SKIP: could not get rig list"
  record skipped "Plugin: git-hygiene [skipped]" "Skipped: could not get rig list"
  exit 0
}

RIG_PATHS=$(echo "$RIG_JSON" | python3 -c "
import json, sys
rigs = json.load(sys.stdin)
for r in rigs:
    p = r.get('repo_path') or ''
    if p: print(p)
" 2>/dev/null)

if [ -z "$RIG_PATHS" ]; then
  log "SKIP: no rigs with repo paths found"
  record skipped "Plugin: git-hygiene [skipped]" \
    "Skipped: no rigs reported a repo_path from 'gt rig list --json'"
  exit 0
fi

RIG_COUNT=$(echo "$RIG_PATHS" | wc -l | tr -d ' ')
log "Found $RIG_COUNT rig repo(s) to clean"

# --- Process each rig repo ----------------------------------------------------

TOTAL_LOCAL_MERGED=0
TOTAL_LOCAL_ORPHAN=0
TOTAL_REMOTE=0
TOTAL_STASHES=0
TOTAL_GC=0

while IFS= read -r REPO_PATH; do
  [ -z "$REPO_PATH" ] && continue

  if ! git -C "$REPO_PATH" rev-parse --git-dir >/dev/null 2>&1; then
    log "SKIP: $REPO_PATH is not a git repo"
    continue
  fi

  log ""
  log "=== Cleaning: $REPO_PATH ==="

  # Detect default branch. symbolic-ref exits non-zero when origin/HEAD is not
  # set, which under `set -e` + pipefail would abort the whole run, so tolerate
  # the failure and fall back.
  DEFAULT_BRANCH=$(git -C "$REPO_PATH" symbolic-ref refs/remotes/origin/HEAD 2>/dev/null \
    | sed 's|refs/remotes/origin/||' || true)
  if [ -z "$DEFAULT_BRANCH" ]; then
    DEFAULT_BRANCH="main"
  fi
  CURRENT_BRANCH=$(git -C "$REPO_PATH" branch --show-current 2>/dev/null || true)

  # Step 1: Prune remote tracking refs
  if [ "$DRY_RUN" = "1" ]; then
    log "  Skipping fetch --prune (report-only); results reflect current refs"
  else
    log "  Pruning remote tracking refs..."
    git -C "$REPO_PATH" fetch --prune --all 2>/dev/null || true
  fi

  # Step 2: Delete merged local branches
  log "  Merged local branches..."
  MERGED_BRANCHES=$(git -C "$REPO_PATH" branch --merged "$DEFAULT_BRANCH" 2>/dev/null \
    | grep -v "^\*" \
    | grep -v "^+" \
    | grep -v -E "^\s*(main|master)$" \
    | sed 's/^[[:space:]]*//' || true)

  LOCAL_MERGED=0
  while IFS= read -r BRANCH; do
    [ -z "$BRANCH" ] && continue
    if [ "$BRANCH" = "$CURRENT_BRANCH" ] || [ "$BRANCH" = "$DEFAULT_BRANCH" ]; then
      continue
    fi
    case "$BRANCH" in
      refinery-patrol|merge/*) continue ;;
    esac
    if act "delete merged branch: $BRANCH" -- git -C "$REPO_PATH" branch -d "$BRANCH" 2>/dev/null; then
      LOCAL_MERGED=$((LOCAL_MERGED + 1))
    fi
  done <<< "$MERGED_BRANCHES"
  TOTAL_LOCAL_MERGED=$((TOTAL_LOCAL_MERGED + LOCAL_MERGED))

  # Step 3: Delete stale unmerged orphan branches
  log "  Stale orphan branches..."
  STALE_PATTERNS="polecat/|dog/|fix/|pr-|integration/|worktree-agent-"
  ALL_BRANCHES=$(git -C "$REPO_PATH" branch 2>/dev/null \
    | grep -v "^\*" \
    | grep -v "^+" \
    | sed 's/^[[:space:]]*//' || true)

  LOCAL_ORPHAN=0
  while IFS= read -r BRANCH; do
    [ -z "$BRANCH" ] && continue
    if ! echo "$BRANCH" | grep -qE "^($STALE_PATTERNS)"; then
      continue
    fi
    if [ "$BRANCH" = "$CURRENT_BRANCH" ] || [ "$BRANCH" = "$DEFAULT_BRANCH" ]; then
      continue
    fi
    case "$BRANCH" in
      main|master|refinery-patrol|merge/*) continue ;;
    esac
    if git -C "$REPO_PATH" rev-parse --verify "refs/remotes/origin/$BRANCH" >/dev/null 2>&1; then
      continue
    fi
    if act "force-delete orphan branch: $BRANCH" -- git -C "$REPO_PATH" branch -D "$BRANCH" 2>/dev/null; then
      LOCAL_ORPHAN=$((LOCAL_ORPHAN + 1))
    fi
  done <<< "$ALL_BRANCHES"
  TOTAL_LOCAL_ORPHAN=$((TOTAL_LOCAL_ORPHAN + LOCAL_ORPHAN))

  # Step 4: Delete merged remote branches on GitHub
  log "  Merged remote branches..."
  REMOTE_DELETED=0

  GH_REPO=$(git -C "$REPO_PATH" remote get-url origin 2>/dev/null \
    | sed -E 's|.*github\.com[:/]||; s|\.git$||')

  if [ -n "$GH_REPO" ]; then
    REMOTE_BRANCHES=$(git -C "$REPO_PATH" branch -r 2>/dev/null \
      | grep -v HEAD \
      | grep -v "origin/$DEFAULT_BRANCH" \
      | grep -v "origin/dependabot/" \
      | grep -v "origin/refinery-patrol" \
      | grep -vE "origin/merge/" \
      | sed 's|^[[:space:]]*origin/||' || true)

    REMOTE_PATTERNS="polecat/|fix/|pr-|integration/|worktree-agent-"

    while IFS= read -r RBRANCH; do
      [ -z "$RBRANCH" ] && continue
      if ! echo "$RBRANCH" | grep -qE "^($REMOTE_PATTERNS)"; then
        continue
      fi
      if git -C "$REPO_PATH" merge-base --is-ancestor "origin/$RBRANCH" "origin/$DEFAULT_BRANCH" 2>/dev/null; then
        if act "delete REMOTE branch $GH_REPO:$RBRANCH" -- gh api "repos/$GH_REPO/git/refs/heads/$RBRANCH" -X DELETE 2>/dev/null; then
          REMOTE_DELETED=$((REMOTE_DELETED + 1))
        fi
      fi
    done <<< "$REMOTE_BRANCHES"
  fi
  TOTAL_REMOTE=$((TOTAL_REMOTE + REMOTE_DELETED))

  # Step 5: Clear stale stashes
  log "  Stashes..."
  STASH_COUNT=$(git -C "$REPO_PATH" stash list 2>/dev/null | wc -l | tr -d ' ')
  if [ "$STASH_COUNT" -gt 0 ]; then
    act "clear $STASH_COUNT stash(es)" -- git -C "$REPO_PATH" stash clear 2>/dev/null || true
    TOTAL_STASHES=$((TOTAL_STASHES + STASH_COUNT))
  fi

  # Step 6: Garbage collect
  log "  Garbage collection..."
  if act "run git gc --prune=now" -- git -C "$REPO_PATH" gc --prune=now --quiet 2>/dev/null; then
    TOTAL_GC=$((TOTAL_GC + 1))
  fi

  log "  Done: $LOCAL_MERGED merged, $LOCAL_ORPHAN orphan, $REMOTE_DELETED remote, $STASH_COUNT stash(es)"
done <<< "$RIG_PATHS"

# --- Report -------------------------------------------------------------------

SUMMARY="[$MODE] $RIG_COUNT rig(s): $TOTAL_LOCAL_MERGED merged, $TOTAL_LOCAL_ORPHAN orphan, $TOTAL_REMOTE remote, $TOTAL_STASHES stash(es), $TOTAL_GC gc"
log ""
log "=== Git Hygiene Summary ==="
log "$SUMMARY"
if [ "$DRY_RUN" = "1" ]; then
  log "Report-only: nothing was deleted. Re-run with GIT_HYGIENE_APPLY=1 to apply."
fi

record success "git-hygiene: $SUMMARY" "$SUMMARY"
