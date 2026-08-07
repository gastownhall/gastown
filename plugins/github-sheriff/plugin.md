+++
name = "github-sheriff"
description = "Monitor GitHub CI checks on open PRs across all rigs and create beads for failures"
version = 2

[gate]
type = "cooldown"
duration = "2h"

[tracking]
labels = ["plugin:github-sheriff", "category:ci-monitoring"]
digest = true

[execution]
timeout = "10m"
notify_on_failure = true
severity = "low"
+++

# GitHub Sheriff

Sweeps every rig's GitHub repo for open pull requests, categorizes them by
readiness, and files one `ci-failure` bead per PR with failing CI. Implements
the PR Sheriff pattern from the
[Gas Town User Manual](https://steve-yegge.medium.com/gas-town-emergency-user-manual-cf0e4556d74b)
as a Deacon plugin.

Categorizes each PR as:
- **Easy win**: CI passing, small (<200 LOC changed), no merge conflicts
- **Needs review**: CI failing, large, or has conflicts

Requires: `gh` CLI installed and authenticated (`gh auth status`).

> **This plugin has no `run.sh`. The commands below are the implementation, not
> illustration.** Run them as written. If a step fails, report the failure —
> do NOT substitute your own method of finding repos, composing bead titles, or
> deciding what counts as a failure. Every field marked MANDATORY below is
> machine-matched by later runs; improvising any of them creates duplicate beads
> that no query can reconcile.

## Detection

Verify `gh` is available and authenticated:

```bash
gh auth status 2>/dev/null
if [ $? -ne 0 ]; then
  echo "SKIP: gh CLI not authenticated"
  exit 0
fi
```

This plugin is dispatched town-level, so it sweeps **every rig**, not one repo.
Enumerate rigs from `mayor/rigs.json` — the authoritative rig list — and resolve
each rig's GitHub repo from its real git directory.

Rigs come in two layouts and they genuinely differ, so both must be tried:

- **Worktree rigs** (laser, teleport, payment_portal, gleam, …) keep their
  remote in `<rig>/.repo.git`. A plain `git -C <rig>` finds no repository at
  all — these are exactly the rigs that have CI failures.
- **Plain-clone rigs** (beads, gastown) answer to `git -C <rig>`.

```bash
TOWN_ROOT="${GT_TOWN_ROOT:-${GT_ROOT:-$HOME/gt}}"
RIGS_JSON="$TOWN_ROOT/mayor/rigs.json"

if [ ! -f "$RIGS_JSON" ]; then
  echo "FAIL: rigs.json not found at $RIGS_JSON — cannot enumerate rigs"
  exit 1
fi

REPOS=()
UNRESOLVED=()

while IFS= read -r RIG; do
  [ -z "$RIG" ] && continue
  RIG_PATH="$TOWN_ROOT/$RIG"

  # Worktree layout first, then plain clone, then rigs.json as last resort.
  URL=$(git --git-dir="$RIG_PATH/.repo.git" remote get-url origin 2>/dev/null)
  [ -z "$URL" ] && URL=$(git -C "$RIG_PATH" remote get-url origin 2>/dev/null)
  [ -z "$URL" ] && URL=$(jq -r --arg r "$RIG" '.rigs[$r].git_url // empty' "$RIGS_JSON")

  REPO=$(printf '%s' "$URL" | sed -E 's|.*github\.com[:/]||; s|\.git$||')

  if [ -z "$REPO" ]; then
    UNRESOLVED+=("$RIG")
    continue
  fi

  # The workspace root repo is not a project repo — it has no PRs and never
  # will. Sweeping it yields a confident "0 open PRs" that means nothing.
  case "$REPO" in
    */gas-town-workspace|gas-town-workspace)
      echo "REJECT: rig $RIG resolved to $REPO (workspace root, not a project repo)"
      continue
      ;;
  esac

  REPOS+=("$RIG|$REPO")
done < <(jq -r '.rigs | keys[]' "$RIGS_JSON")

echo "Detected ${#REPOS[@]} repo(s) to sweep"
[ ${#UNRESOLVED[@]} -gt 0 ] && echo "Unresolved rigs (no git remote): ${UNRESOLVED[*]}"
```

**Never use `$GT_RIG_ROOT`.** It is not set in agent sessions, and `git -C ""`
silently operates on the current directory instead of erroring — which is how
this plugin spent weeks reporting success while sweeping the workspace root.

Detecting nothing is a failure, not an all-clear:

```bash
if [ ${#REPOS[@]} -eq 0 ]; then
  echo "FAIL: detected 0 repos to sweep — nothing was checked"
  gt plugin record-run --plugin github-sheriff --result failure \
    --title "github-sheriff: FAILED — detected 0 repos" \
    --description "Rig enumeration produced no GitHub repos. Unresolved: ${UNRESOLVED[*]:-none}" \
    >/dev/null 2>&1 || true
  gt escalate "Plugin FAILED: github-sheriff detected 0 repos" \
    --severity low \
    --reason "Sweep found no repos to check; CI is unmonitored until this is fixed"
  exit 1
fi
```

## Action

### Step 1: Sweep every repo and categorize its PRs

Track swept repos and errors separately. A repo that failed to enumerate was
**not** checked, and must never be counted as clean.

This is one block on purpose — the repo loop and the PR loop nest, so run it
whole. Process substitution (not a pipe) keeps array modifications after the
loop.

```bash
SINCE=$(date -d '7 days ago' +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -v-7d +%Y-%m-%dT%H:%M:%SZ)

SWEPT=0
SWEEP_ERRORS=()
TOTAL_PRS=0
# Space-delimited sets, consulted by Step 4 so it needs no extra API calls.
OPEN_KEYS=" "
EXPIRABLE_REPOS=" "
EASY_WINS=()
NEEDS_REVIEW=()
FAILURES=()
NON_FAILURES=()

for ENTRY in "${REPOS[@]}"; do
  IFS='|' read -r RIG REPO <<< "$ENTRY"

  ALL_PRS=$(gh pr list --repo "$REPO" --state open \
    --json number,title,author,additions,deletions,mergeable,statusCheckRollup,url,updatedAt \
    --limit 100 2>/dev/null)

  # An empty result is an API failure. A repo with no PRs returns "[]".
  if [ -z "$ALL_PRS" ]; then
    SWEEP_ERRORS+=("$REPO")
    continue
  fi

  SWEPT=$((SWEPT + 1))
  ALL_COUNT=$(echo "$ALL_PRS" | jq length)

  # Record every open PR so Step 4 can expire beads without re-querying
  # GitHub. At the --limit ceiling the list may be truncated, and a PR missing
  # from a truncated list is not evidence that it closed — so this repo is not
  # safe to expire against.
  if [ "$ALL_COUNT" -lt 100 ]; then
    EXPIRABLE_REPOS+="$REPO "
    while IFS= read -r N; do
      [ -n "$N" ] && OPEN_KEYS+="$REPO/$N "
    done < <(echo "$ALL_PRS" | jq -r '.[].number')
  fi

  # Only recently-touched PRs are worth categorizing.
  PRS=$(echo "$ALL_PRS" | jq --arg since "$SINCE" '[.[] | select(.updatedAt >= $since)]')
  PR_COUNT=$(echo "$PRS" | jq length)
  TOTAL_PRS=$((TOTAL_PRS + PR_COUNT))
  [ "$PR_COUNT" -eq 0 ] && continue

  while IFS= read -r PR_JSON; do
    [ -z "$PR_JSON" ] && continue

    PR_NUM=$(echo "$PR_JSON" | jq -r '.number')
    PR_TITLE=$(echo "$PR_JSON" | jq -r '.title')
    AUTHOR=$(echo "$PR_JSON" | jq -r '.author.login')
    ADDITIONS=$(echo "$PR_JSON" | jq -r '.additions // 0')
    DELETIONS=$(echo "$PR_JSON" | jq -r '.deletions // 0')
    MERGEABLE=$(echo "$PR_JSON" | jq -r '.mergeable')
    TOTAL_CHANGES=$((ADDITIONS + DELETIONS))

    TOTAL_CHECKS=$(echo "$PR_JSON" | jq '.statusCheckRollup | length')
    PASSING_CHECKS=$(echo "$PR_JSON" | jq '[.statusCheckRollup[] | select(
      .conclusion == "SUCCESS" or .conclusion == "NEUTRAL" or
      .conclusion == "SKIPPED" or .state == "SUCCESS"
    )] | length')

    if [ "$TOTAL_CHECKS" -gt 0 ] && [ "$TOTAL_CHECKS" -eq "$PASSING_CHECKS" ]; then
      CI_PASS=true
    else
      CI_PASS=false
    fi

    # A real failure only. CANCELLED and TIMED_OUT are infrastructure noise,
    # not code defects — see "What counts as a failure" below.
    FAILED_CHECKS=$(echo "$PR_JSON" | jq -r '[.statusCheckRollup[] | select(
      .conclusion == "FAILURE" or .state == "FAILURE" or .state == "ERROR"
    ) | .name // .context] | join(", ")')

    # Counted for the summary so cancellations stay visible without filing beads.
    NOISE=$(echo "$PR_JSON" | jq '[.statusCheckRollup[] | select(
      .conclusion == "CANCELLED" or .conclusion == "TIMED_OUT"
    )] | length')
    [ "$NOISE" -gt 0 ] && NON_FAILURES+=("$REPO#$PR_NUM: $NOISE cancelled/timed-out")

    if [ -n "$FAILED_CHECKS" ]; then
      FAILURES+=("$REPO|$PR_NUM|$PR_TITLE|$FAILED_CHECKS")
    fi

    if [ "$MERGEABLE" = "MERGEABLE" ] && [ "$CI_PASS" = true ] && [ "$TOTAL_CHANGES" -lt 200 ]; then
      EASY_WINS+=("$REPO#$PR_NUM: $PR_TITLE (by $AUTHOR, +$ADDITIONS/-$DELETIONS)")
    else
      REASONS=""
      [ "$MERGEABLE" != "MERGEABLE" ] && REASONS+="conflicts "
      [ -n "$FAILED_CHECKS" ] && REASONS+="ci-failing "
      [ "$TOTAL_CHANGES" -ge 200 ] && REASONS+="large(${TOTAL_CHANGES}loc) "
      [ -n "$REASONS" ] && NEEDS_REVIEW+=("$REPO#$PR_NUM: $PR_TITLE (by $AUTHOR, ${REASONS% })")
    fi
  done < <(echo "$PRS" | jq -c '.[]')
done
```

#### What counts as a failure

| Conclusion | File a bead? | Why |
|---|---|---|
| `FAILURE`, state `FAILURE`/`ERROR` | **Yes** | A real check failure |
| `CANCELLED` | **No** | Runner/timeout noise. Cancelled jobs have zero failed steps — filing them manufactures work that does not exist |
| `TIMED_OUT` | **No** | A CI-health signal, not a code defect. Report in the summary; do not file a bead a worker will try to debug |
| `SUCCESS`, `NEUTRAL`, `SKIPPED` | No | Passing |

### Step 2: Adopt legacy beads (self-retiring migration)

Beads filed before the structural key exists carry no `ci:` label, so Step 2
cannot see them — it would file a second bead beside every one of them. Adopt
them instead: parse the PR reference out of the old title once, attach the key,
and collapse duplicates onto a single survivor.

This is the **only** place a bead title is ever parsed, and it is one-way: once
a bead carries the key it takes the normal path above and this step skips it.
Beads whose title does not resolve are left untouched and reported.

```bash
ADOPTED=0
COLLAPSED=0
UNADOPTED=0

resolve_repo() {
  TOK="${1##*/}"
  for E in "${REPOS[@]}"; do
    RIG="${E%%|*}"; RREPO="${E##*|}"
    if [ "$TOK" = "${RREPO##*/}" ] || [ "$TOK" = "$RIG" ]; then echo "$RREPO"; return 0; fi
  done
  return 1
}

while IFS= read -r B; do
  [ -z "$B" ] && continue
  BEAD_ID=$(echo "$B" | jq -r '.id')

  # Already keyed — Step 2 owns it.
  echo "$B" | jq -e '.labels[]? | select(startswith("ci:"))' >/dev/null 2>&1 && continue

  TITLE=$(echo "$B" | jq -r '.title')
  # Two passes: "<repo> PR #N" first, then "<repo>#N" / "<repo> #N".
  PARSED=$(printf '%s' "$TITLE" | sed -nE 's|^.*[[:space:]:]([A-Za-z0-9._/-]+)[[:space:]]+PR[[:space:]]*#([0-9]+).*$|\1 \2|p')
  [ -z "$PARSED" ] && PARSED=$(printf '%s' "$TITLE" | sed -nE 's|^.*[[:space:]:]([A-Za-z0-9._/-]+)[[:space:]]*#([0-9]+).*$|\1 \2|p')
  if [ -z "$PARSED" ]; then UNADOPTED=$((UNADOPTED + 1)); continue; fi

  TOK=${PARSED%% *}; PR_NUM=${PARSED##* }
  REPO=$(resolve_repo "$TOK") || { UNADOPTED=$((UNADOPTED + 1)); continue; }
  KEY="ci:$REPO/$PR_NUM"

  # Ask who already owns this key. That covers a bead this plugin filed, and a
  # legacy bead adopted earlier in this same pass — the label is live
  # immediately, so the query is the whole bookkeeping. One bead survives per
  # key; the rest are the duplicates the broken digest produced.
  OWNER=$(bd list --label "$KEY" --status open --limit 0 --json 2>/dev/null | jq -r '.[0].id // empty')

  if [ -n "$OWNER" ] && [ "$OWNER" != "$BEAD_ID" ]; then
    bd close "$BEAD_ID" --reason "Duplicate of $OWNER ($KEY) — github-sheriff dedupe was title-derived and never matched" \
      >/dev/null 2>&1 && COLLAPSED=$((COLLAPSED + 1))
  else
    bd update "$BEAD_ID" --add-label "$KEY" >/dev/null 2>&1 && ADOPTED=$((ADOPTED + 1))
  fi
done < <(bd list --label ci-failure --status open --limit 0 --json 2>/dev/null | jq -c '.[]')
```

### Step 3: File one bead per failing PR

**One bead per PR, not per check.** Per-check beads are what produced 34 beads
for a single PR. Failing check names go in the description, which is updated in
place on later runs.

Every bead MUST carry these exact fields. They are machine-matched, not prose:

- **Title, exactly**: `ci-failure: <owner>/<repo>#<pr>`
  Nothing appended, no `[bug]` prefix, no check name, no `PR` before the number.
- **Labels, all three MANDATORY**: `ci-failure`, `plugin:github-sheriff`, and
  the structural dedupe key `ci:<owner>/<repo>/<pr>`.

Dedupe on the **structural label**, never on the title. Titles are prose and
prose drifts; a title-derived digest is what let this plugin re-file the same
failure in seven different formats.

```bash
CREATED=0
UPDATED=0

for F in "${FAILURES[@]}"; do
  IFS='|' read -r REPO PR_NUM PR_TITLE FAILED_CHECKS <<< "$F"

  KEY="ci:$REPO/$PR_NUM"
  BEAD_TITLE="ci-failure: $REPO#$PR_NUM"
  DESCRIPTION="Failing checks: $FAILED_CHECKS

PR: https://github.com/$REPO/pull/$PR_NUM
PR title: $PR_TITLE
Last seen failing: $(date -u +%Y-%m-%dT%H:%M:%SZ)"

  EXISTING=$(bd list --label "$KEY" --status open --json 2>/dev/null | jq -r '.[0].id // empty')

  if [ -n "$EXISTING" ]; then
    bd update "$EXISTING" -d "$DESCRIPTION" >/dev/null 2>&1 && UPDATED=$((UPDATED + 1))
    continue
  fi

  BEAD_ID=$(bd create "$BEAD_TITLE" -t task -p 2 \
    -d "$DESCRIPTION" \
    -l "ci-failure,plugin:github-sheriff,$KEY" \
    --json 2>/dev/null | jq -r '.id // empty')

  if [ -n "$BEAD_ID" ]; then
    CREATED=$((CREATED + 1))
    gt activity emit github_check_failed \
      --message "CI failing on $REPO#$PR_NUM ($FAILED_CHECKS), bead $BEAD_ID" \
      2>/dev/null || true
  fi
done
```

### Step 4: Expire beads whose PR is no longer open

A bead for a closed or merged PR is dead weight. Closing it is safe: no future
run re-files it, because the PR is no longer in the open-PR sweep.

Decide this from the sweep in Step 1, not from fresh API calls. A `gh pr view`
per bead is an extra request per open bead per run, which at this plugin's
dispatch rate is what exhausts the GitHub quota — and a rate-limited expiry
step would start closing beads on failed lookups.

Only repos that swept cleanly are eligible. If a repo errored, its beads are
simply unknown this run, and unknown is never a reason to close:

```bash
EXPIRED=0

while IFS= read -r B; do
  [ -z "$B" ] && continue
  BEAD_ID=$(echo "$B" | jq -r '.id')
  KEY=$(echo "$B" | jq -r '.labels[]? | select(startswith("ci:"))' | head -1)
  [ -z "$KEY" ] && continue

  TARGET=${KEY#ci:}
  REPO=${TARGET%/*}
  PR_NUM=${TARGET##*/}

  # Not swept cleanly this run -> no opinion.
  case "$EXPIRABLE_REPOS" in *" $REPO "*) ;; *) continue ;; esac

  # Swept, and the PR was not among its open PRs -> it closed or merged.
  case "$OPEN_KEYS" in
    *" $REPO/$PR_NUM "*) ;;
    *)
      bd close "$BEAD_ID" --reason "PR $REPO#$PR_NUM is no longer open" >/dev/null 2>&1 \
        && EXPIRED=$((EXPIRED + 1))
      ;;
  esac
done < <(bd list --label ci-failure --status open --limit 0 --json 2>/dev/null | jq -c '.[]')
```

## Record Result

"0 failures" and "nothing was swept" are different outcomes and must never
render the same. Report what was actually checked:

```bash
if [ ${#EASY_WINS[@]} -gt 0 ]; then
  echo "Easy wins (${#EASY_WINS[@]}):"
  printf '  %s\n' "${EASY_WINS[@]}"
fi
if [ ${#NEEDS_REVIEW[@]} -gt 0 ]; then
  echo "Needs review (${#NEEDS_REVIEW[@]}):"
  printf '  %s\n' "${NEEDS_REVIEW[@]}"
fi
if [ ${#NON_FAILURES[@]} -gt 0 ]; then
  echo "Not filed — cancelled/timed-out (${#NON_FAILURES[@]}):"
  printf '  %s\n' "${NON_FAILURES[@]}"
fi

SUMMARY="swept $SWEPT/${#REPOS[@]} repos, $TOTAL_PRS open PRs — ${#EASY_WINS[@]} easy win(s), ${#NEEDS_REVIEW[@]} need review, ${#FAILURES[@]} PR(s) with CI failures ($CREATED bead(s) created, $UPDATED updated, $EXPIRED expired)"
[ "$ADOPTED" -gt 0 ] || [ "$COLLAPSED" -gt 0 ] && SUMMARY="$SUMMARY; legacy: $ADOPTED adopted, $COLLAPSED duplicate(s) collapsed, $UNADOPTED unrecognized"
[ ${#SWEEP_ERRORS[@]} -gt 0 ] && SUMMARY="$SUMMARY; FAILED TO SWEEP: ${SWEEP_ERRORS[*]}"
echo "$SUMMARY"
```

Success requires that every detected repo was actually enumerated. A partial
sweep is a failure, however few failures it found:

```bash
if [ ${#SWEEP_ERRORS[@]} -eq 0 ] && [ "$SWEPT" -gt 0 ]; then
  gt plugin record-run --plugin github-sheriff --result success \
    --title "github-sheriff: $SUMMARY" --description "$SUMMARY" >/dev/null 2>&1 || true
else
  gt plugin record-run --plugin github-sheriff --result failure \
    --title "github-sheriff: PARTIAL SWEEP" --description "$SUMMARY" >/dev/null 2>&1 || true
  gt escalate "Plugin FAILED: github-sheriff partial sweep" \
    --severity low \
    --reason "$SUMMARY"
fi
```

On an unhandled error:
```bash
gt plugin record-run --plugin github-sheriff --result failure \
  --title "github-sheriff: FAILED" \
  --description "GitHub sheriff failed: $ERROR" >/dev/null 2>&1 || true

gt escalate "Plugin FAILED: github-sheriff" \
  --severity low \
  --reason "$ERROR"
```
