# PR Sheriff Report

Subject: `gastownhall/gastown` PR #4343 `fix(beads): pin server database in pinned bd env so mol bond targets the rig DB`
URL: https://github.com/gastownhall/gastown/pull/4343
Mode: individual_review / fixup analysis
Author: `marvincris` (known)
Base/Head: `main@5118351294c8e3cad288314b9a9b7d106ebce960` -> `fix/pinned-bd-env-server-database@bc5813e3eae6bd2bde079b799060093316c6f639`

## Blocking Gates

- `label_triage`: PR #4343 has only `status/needs-triage`; GitHub `priority/*` and `kind/*` labels are absent.
- `merge_status`: `status/needs-triage` is not `review-approved` or `merge-ready`.
- `blocker_scan`: PR #4343 is dirty/conflicting, has no reviews, has no substantive CI beyond auto-labeling, and would drop substantial #4337 cleanup if used as a replacement.
- `focused_verification`: PR #4343 has no substantive test/lint/build check evidence; only `add-triage-label` passed.

## Final Recommendation

Final verdict: `defer_human_review`
Merge path allowed: `false`

Do not merge PR #4343 as-is. Do not let PR #4343 supersede PR #4337.

Use PR #4343 as evidence only, or fold its narrow inside-town pinned-env regression coverage/wording into PR #4337 or a clean #4337 replacement. Preserve `marvincris` attribution if any #4343 code or test text is carried forward.

## Evidence Summary

- PR #4343 correctly identifies a real old-base failure class: pinned `bd` subprocesses for rig beads in a shared-server town must not carry town `BEADS_DOLT_DATA_DIR`, and should pin `BEADS_DOLT_SERVER_DATABASE` from the selected `.beads/metadata.json`.
- Against upstream `main` and PR #4337, that core pinned-env behavior is already present: `BuildPinnedBDEnv` pins `BEADS_DOLT_SERVER_DATABASE` via metadata and `doltTargetEnvFromBeadsDir` carries host/port only.
- PR #4343 adds a useful direct regression case, `TestBuildPinnedBDEnvPinsRigDatabaseInsideTown`, for a rig `.beads` directory inside a real town.
- PR #4343 is stale and narrower than PR #4337: it touches only `internal/beads/database.go` and `internal/beads/beads_test.go`, while #4337 carries 26-file cleanup across central bd env policy, read/mutation classification, fresh install config, mail, hook, convoy, and test hardening.
- PR #4343 as a replacement would regress #4337 `ArgsAreReadOnly`, `StripBDTargetEnv`, `DatabaseNameFromMetadata`, fresh-install hardening, mail/hook/convoy env convergence, and broader tests.
- PR #4337's current Integration Tests failure is not proven to be #4343's mol-bond/wisp target-database failure. The red check fails in `TestFreshInstallRigPolecatHookIntegration` with `ready_issues` / `depends_on_id` schema failure.
- No direct secrets, credential, workflow-permission, or dependency risk was found in #4343. The primary risk is data-integrity/cross-database targeting if stale env selectors are carried forward incorrectly.

## Research And Reviews

- Research legs: 15/15 completed.
- Pre-implementation/pre-decision reviews: 5/5 approving for the same report-only action.
- Post-implementation reviews: not applicable; no Sheriff code change, replacement, or cherry-pick was performed.
- Cleanup-first: `convergent`; the recommendation avoids a stale patch-on-patch replacement and preserves #4337's centralized cleanup path.

## Checker

Command: `gt-pr-sheriff-check --evidence pr-sheriff-evidence/gt-pr-4343-sheriff-review/evidence.json`

Exit code: `1`

Output:

```text
PR Sheriff: BLOCK
merge_path_allowed: false
verdict: defer_human_review
gates: 4 pass, 6 not_applicable, 0 waived, 4 fail
blocking_gates:
- label_triage: expected exactly one semantic priority, found 0; expected exactly one semantic kind, found 0
```

The checker block is expected for this non-merge recommendation because PR mutation was not authorized and PR #4343 is missing required GitHub priority/kind labels. The evidence still records `merge_path_allowed=false` and an internally consistent `defer_human_review` verdict.

## Required Next Actions

- If maintainers continue #4337, fold only #4343's narrow pinned-env regression test/intent into #4337 or a clean #4337 replacement.
- Keep routing env unpinned; do not reintroduce `BEADS_DOLT_DATA_DIR` as a target selector for routed bd subprocesses.
- Preserve contributor attribution if any #4343 code/test text is carried forward.
- Resolve #4337's separate Integration Tests failure before any merge-ready recommendation.

Evidence JSON: `pr-sheriff-evidence/gt-pr-4343-sheriff-review/evidence.json`
