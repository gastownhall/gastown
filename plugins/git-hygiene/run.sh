#!/usr/bin/env bash
# git-hygiene/run.sh — Clean stale branches, stashes, and loose objects.
#
# Runs across all rig repos. Covers:
# - Merged local branches
# - Orphan local branches (polecat/*, dog/*, fix/*, etc. with no remote)
# - Merged remote branches on GitHub
# - Stale stashes
# - Git garbage collection

set -euo pipefail

log() { echo "[git-hygiene] $*"; }

# --- Enumerate rig repos -----------------------------------------------------

RIG_JSON=$(gt rig list --json 2>/dev/null) || {
  log "SKIP: could not get rig list"
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

  # Detect default branch
  DEFAULT_BRANCH=$(git -C "$REPO_PATH" symbolic-ref refs/remotes/origin/HEAD 2>/dev/null \
    | sed 's|refs/remotes/origin/||')
  if [ -z "$DEFAULT_BRANCH" ]; then
    DEFAULT_BRANCH="main"
  fi
  CURRENT_BRANCH=$(git -C "$REPO_PATH" branch --show-current 2>/dev/null)

  # Step 1: Prune remote tracking refs
  log "  Pruning remote tracking refs..."
  git -C "$REPO_PATH" fetch --prune --all 2>/dev/null || true

  # Step 2: Delete merged local branches
  log "  Deleting merged local branches..."
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
    log "    Deleting merged: $BRANCH"
    git -C "$REPO_PATH" branch -d "$BRANCH" 2>/dev/null && LOCAL_MERGED=$((LOCAL_MERGED + 1))
  done <<< "$MERGED_BRANCHES"
  TOTAL_LOCAL_MERGED=$((TOTAL_LOCAL_MERGED + LOCAL_MERGED))

  # Step 3: Delete stale unmerged orphan branches
  log "  Deleting stale orphan branches..."
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
    log "    Deleting orphan: $BRANCH"
    git -C "$REPO_PATH" branch -d "$BRANCH" 2>/dev/null && LOCAL_ORPHAN=$((LOCAL_ORPHAN + 1))
  done <<< "$ALL_BRANCHES"
  TOTAL_LOCAL_ORPHAN=$((TOTAL_LOCAL_ORPHAN + LOCAL_ORPHAN))

  # Step 4: Delete merged remote branches on GitHub
  log "  Deleting merged remote branches..."
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
        log "    Deleting remote: origin/$RBRANCH"
        gh api "repos/$GH_REPO/git/refs/heads/$RBRANCH" -X DELETE 2>/dev/null && REMOTE_DELETED=$((REMOTE_DELETED + 1))
      fi
    done <<< "$REMOTE_BRANCHES"
  fi
  TOTAL_REMOTE=$((TOTAL_REMOTE + REMOTE_DELETED))

  # Step 5: Skipped — stash clear removed (too destructive, risks losing agent WIP)
  STASH_COUNT=0

  # Step 6: Garbage collect
  log "  Running git gc..."
  git -C "$REPO_PATH" gc --prune=now --quiet 2>/dev/null && TOTAL_GC=$((TOTAL_GC + 1))

  log "  Done: $LOCAL_MERGED merged, $LOCAL_ORPHAN orphan, $REMOTE_DELETED remote, $STASH_COUNT stash(es)"
done <<< "$RIG_PATHS"

# --- Report -------------------------------------------------------------------

SUMMARY="$RIG_COUNT rig(s): $TOTAL_LOCAL_MERGED merged, $TOTAL_LOCAL_ORPHAN orphan, $TOTAL_REMOTE remote, $TOTAL_STASHES stash(es), $TOTAL_GC gc"
log ""
log "=== Git Hygiene Summary ==="
log "$SUMMARY"

gt plugin record-run --plugin git-hygiene --result success \
  --title "git-hygiene: $SUMMARY" --description "$SUMMARY" >/dev/null 2>&1 || true
