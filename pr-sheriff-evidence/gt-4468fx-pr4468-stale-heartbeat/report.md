PR Sheriff Report
Subject: gastownhall/gastown PR #4468 "fix(polecat): require agent-process death before reconcile-killing stale-heartbeat sessions" https://github.com/gastownhall/gastown/pull/4468
Mode: replacement_fixup + merge_decision
Author: marvincris / Marvin Cris Andrade (known)
Base/Head: upstream/main@62fc77c7ecd262461802cd55fcadcd69f5393209 -> d3858c3c91c3bee3252b1095551af5579bc07f34
Repo config used: pr-sheriff default policy v1.1
Labels: status=merge-ready priority=p1 kind=bug for replacement target; original PR remains status/needs-review priority/p1 kind/bug
Triage category: needs_replacement_or_fixup_analysis
Action mode: replacement_pr
Research legs: 15/15
Pre-implementation reviews: 5/5
Post-implementation reviews: 5/5
Cleanup-first: acceptable_minimal
Human approvals: not_required for kind/bug implementation; original PR has no merge approval
Verification: focused polecat stale-heartbeat tests pass; focused tmux checked-liveness tests pass; CGO_ENABLED=0 go test ./internal/polecat passes; CGO_ENABLED=0 go build ./cmd/gt passes; git diff --check passes; default CGO blocked by missing local ICU header; broad internal/tmux sleep/coreutils failures reproduce on upstream/main baseline
Baseline-red waiver: not_applicable
Replacement/fixup: original PR #4468 carried forward on current-main branch polecat/mirelurk/gt-4468fx-pr4468-stale-heartbeat at d3858c3c91c3bee3252b1095551af5579bc07f34
Contributor attribution: preserved via commit Refs to PR #4468 and original commit plus Co-authored-by: mayor <marvincris@outlook.com>
Superseded closure: deferred_until_replacement_merge
Blocker scan: original PR merge-as-is blockers resolved by replacement; no unresolved replacement blockers
Gate summary: 12 pass, 2 not_applicable, 0 waived, 0 fail
Blocking gates: none for replacement merge gate
Final verdict: merge_replacement
Merge path allowed: true
Required next actions: do not close PR #4468 as superseded until replacement lands with attribution evidence
Evidence refs: pr-sheriff-evidence/gt-4468fx-pr4468-stale-heartbeat/evidence.json; pr-sheriff-evidence/gt-4468fx-pr4468-stale-heartbeat/merge-gate-check.txt
