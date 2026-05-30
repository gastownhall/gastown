# gt 1.2 Release Coordination Evidence

Refreshed: 2026-05-26 19:42 UTC

Scope: release decision evidence only. This artifact records the current gate map, CI inventory, PR disposition, wrong-target records, and validation/review passes for `gt-12-release-evidence-refresh`.

## Executive Snapshot

- Release evidence branch: `integration/gt-1-2-convergence-cleanup` at `863ca7c0f5bdf840371d128146978a402ac9a254`.
- No GitHub Actions runs exist for `integration/gt-1-2-convergence-cleanup` or `integration/test-beaddolt-hardenning` as branch filters; current workflows primarily run on `main`, `pull_request` to `main`, schedules, tags, and metadata events.
- `#4110`-`#4113` are internal integration-branch merge PRs and are merged into their subepic integration branches, not into `main`.
- `#4114` is closed as a wrong-target/non-deliverable artifact and must not be treated as release-gate delivery; `#4109` is also closed/unmerged and carries no release-gate delivery record.
- Current older PR disposition: `#4085` remains open only as a review/internal reference; `#4086`, `#4087`, `#4088`, `#4089`, and `#4092` are closed as superseded or partially extracted. Replacement main-target paths have advanced: `#4081` and `#4096` are now merged to `main`, while `#4080` and `#4108` are closed.

## Release Gate Map

| Gate | Branch | Current head | Evidence | Release disposition |
| --- | --- | --- | --- | --- |
| Convergence cleanup baseline | `integration/gt-1-2-convergence-cleanup` | `863ca7c0f5bdf840371d128146978a402ac9a254` | `git ls-remote --heads origin 'integration/gt-1-2-*'`; commit message `fix: reduce witness deacon mail floods (gt-12-deacon-witness-mail-overload)` | Baseline branch for this evidence refresh. |
| Routing identity gate | `integration/gt-1-2-routing-identity-gate-identity` | `21c5d9244d4a067b72df60de6c808672db9ca620` | PR `#4110` merged 2026-05-23 19:18:52 UTC | Internal integration branch merge recorded. |
| Capacity and admission gate | `integration/gt-1-2-capacity-and-admission-gate-admission` | `aa3ade0205534ddca44449f1119bd47eb4db6f1c` | PR `#4111` merged 2026-05-23 19:10:56 UTC, merge commit `53f9c3806ce208a7a07c069f2ca0c28007d70ea0` | Internal integration branch merge recorded. |
| Notification actionability gate | `integration/gt-1-2-notification-actionability-gate-actionability` | `ede0d98af9db24ce83c73ac2265dca1092964a1f` | PR `#4112` merged 2026-05-23 19:18:52 UTC | Internal integration branch merge recorded. |
| Recovery false-positive matrix | `integration/gt-1-2-recovery-false-positive-matrix-positives` | `fa5a9da9f130ba6f9109ebe0e799d729c2d42534` | PR `#4113` merged 2026-05-23 19:18:53 UTC | Internal integration branch merge recorded. |
| Release candidate and canary gate | `integration/gt-1-2-release-candidate-and-canary-gate-canary` | `625bcf8a92f9faef9804f73624a8bf770085ebd2` | `git ls-remote --heads origin 'integration/gt-1-2-*'`; commit message `fix: tolerate reaped active MR during cleanup (gt-rca-alias-no-merge-cleanup)` | Lags the refreshed convergence baseline; no distinct PR evidence found. |
| Canonical polecat workstate | `integration/gt-1-2-canonical-polecat-workstate-workstate` | `1d3e6039ffac3af8be5485d0fc8a22e0efbb9cf4` | `git ls-remote --heads origin 'integration/gt-1-2-*'` | Branch exists; no `gt-12` PR disposition found in this refresh. |
| MR target and source transition | `integration/gt-1-2-mr-target-and-source-transition-gate-source` | `2718682b8b7a1e75aded6ab63029d9820402ac65` | `git ls-remote --heads origin 'integration/gt-1-2-*'` | Branch exists; no `gt-12` PR disposition found in this refresh. |
| Parent gate rollups | `integration/gt-1-2-canonical-polecat-workstate`, `integration/gt-1-2-capacity-and-admission-gate`, `integration/gt-1-2-mr-target-and-source-transition-gate`, `integration/gt-1-2-notification-actionability-gate`, `integration/gt-1-2-recovery-false-positive-matrix`, `integration/gt-1-2-release-candidate-and-canary-gate`, `integration/gt-1-2-routing-identity-gate` | `b381f60a76589016589fd9be93c18c9902e69c9b` | `git ls-remote --heads origin 'integration/gt-1-2-*'`; commit message `fix: reconcile polecat recovery git state (gt-recovery-false-positive-clean-closed)` | Shared parent rollup state visible in current branch inventory. |

## CI Inventory

| Workflow | Trigger | Release relevance | Jobs |
| --- | --- | --- | --- |
| `CI` (`.github/workflows/ci.yml`) | `push` to `main`, `pull_request` to `main` | Primary maintainer-facing PR/main gate. Does not run for `integration/gt-1-2-*` branch pushes by current branch filters. | `Reject go.mod replace directives`, `Reject issues.jsonl`, `Test`, `Lint`, `Integration Tests`. |
| `Windows CI` (`.github/workflows/windows-ci.yml`) | `push` to `main`, `pull_request` to `main` | Windows smoke/build/vet gate for maintainer-facing PR/main. | `Windows Smoke Test`. |
| `E2E Tests` (`.github/workflows/e2e.yml`) | Daily schedule, `workflow_dispatch` | Baseline scheduled container E2E signal. | `E2E Tests (Container)`. |
| `Nightly Integration Tests` (`.github/workflows/nightly-integration.yml`) | Daily schedule, `workflow_dispatch` | Baseline full integration signal. | `Full Integration Tests`. |
| `Release` (`.github/workflows/release.yml`) | `push` tags `v*`, `workflow_dispatch` | Release artifact publication gate; branch refs are tag-gated out for publishing. | `goreleaser`, `attest-release`, `update-homebrew-formula`, `publish-npm`. |
| `Block Internal PRs` (`.github/workflows/block-internal-prs.yml`) | `pull_request` opened/reopened | PR policy guard. Internal same-repo PRs are closed/failed; fork PRs skip. | `Block Internal PRs`. |
| `Auto-label new issues and PRs` (`.github/workflows/triage-label.yml`) | Issues opened, `pull_request_target` opened | Metadata-only triage signal; does not validate code. | `add-triage-label`. |
| `Remove needs-info on author response` (`.github/workflows/remove-needs-info.yml`) | Issue comments, `pull_request_target` synchronize | Metadata-only label cleanup; does not validate code. | `remove-label`. |
| `Remove needs-triage when triaged` (`.github/workflows/remove-needs-triage.yml`) | Issues labeled, `pull_request_target` labeled | Metadata-only label cleanup; does not validate code. | `remove-triage-label`. |
| `Close stale needs-info / needs-repro issues` (`.github/workflows/close-stale-needs.yml`) | Daily schedule, `workflow_dispatch` | Issue hygiene only. | `close-needs-info`. |

## PR Disposition Evidence

| PR | State | Base | Head | Evidence | Decision record |
| --- | --- | --- | --- | --- | --- |
| `#4110` `Merge: gt-12-formula-identity-tests` | Merged | `integration/gt-1-2-routing-identity-gate-identity` | `polecat/ghoul/gt-12-formula-identity-tests@mpigkq65` | Merged 2026-05-23 19:18:52 UTC, merge commit `21c5d9244d4a067b72df60de6c808672db9ca620`; checks: `Block Internal PRs` skipped job `77529397296`, `add-triage-label` success job `77529397084`. | Internal integration branch merge; not a main-target release PR. |
| `#4111` `Merge: gt-12-fold-4087-capacity` | Merged | `integration/gt-1-2-capacity-and-admission-gate-admission` | `polecat/radrat/gt-12-fold-4087-capacity@mpigfiff` | Merged 2026-05-23 19:10:56 UTC, merge commit `53f9c3806ce208a7a07c069f2ca0c28007d70ea0`; checks: `Block Internal PRs` skipped job `77529475224`, `add-triage-label` success job `77529474905`. | Internal integration branch merge; folds useful `#4087` capacity cases. |
| `#4112` `Merge: gt-12-notification-regression-tests` | Merged | `integration/gt-1-2-notification-actionability-gate-actionability` | `polecat/radrat/gt-12-notification-regression-tests@mpihccma` | Merged 2026-05-23 19:18:52 UTC, merge commit `ede0d98af9db24ce83c73ac2265dca1092964a1f`; checks: `Block Internal PRs` skipped job `77530910484`, `add-triage-label` success job `77530904269`. | Internal integration branch merge; not a main-target release PR. |
| `#4113` `Merge: gt-12-live-polecat-fixtures` | Merged | `integration/gt-1-2-recovery-false-positive-matrix-positives` | `polecat/ghoul/gt-12-live-polecat-fixtures@mpih5jxl` | Merged 2026-05-23 19:18:53 UTC, merge commit `fa5a9da9f130ba6f9109ebe0e799d729c2d42534`; checks: `Block Internal PRs` skipped job `77531020190`, `add-triage-label` success job `77531019972`. | Internal integration branch merge; not a main-target release PR. |
| `#4114` `Merge: gt-pr-main-4089-reuse-startup-fold` | Closed | `integration/test-beaddolt-hardenning` | `polecat/ghoul/gt-pr-main-4089-reuse-startup-fold@mpii6er0` | Closed 2026-05-23 19:19:39 UTC. Comment `4526309016`: `Closing wrong-target PR-mode artifact... Do not merge this branch as part of release-gate delivery.` Checks: `Block Internal PRs` skipped job `77531943075`, `add-triage-label` success job `77531942832`. | Wrong-target/non-deliverable artifact. Do not retarget, merge, or count as delivery. |
| `#4109` `Merge: gt-12-baseline-ci-inventory` | Closed | `integration/test-beaddolt-hardenning` | `polecat/thunder/gt-12-baseline-ci-inventory@mpig5s75` | Closed 2026-05-23 14:47:42 UTC, not merged, no PR comments recorded by `gh pr view --comments`. | Closed/unmerged internal artifact; no release-gate delivery evidence found. |
| `#4085` `RCA canonical: design routing repair and migration guard` | Open | `integration/test-beaddolt-hardenning` | `polecat/brotherhood/gt-rca-canon-routing-repair-design` | Comment `4525854875`: open only as review/internal reference, not maintainer-facing merge target; diagnostic/design slice requires separate clean main-target PR or explicit drop decision. | Review-only/internal reference; not release delivery as-is. |
| `#4086` `fix: block rig add prefix route hijacks` | Closed | `integration/test-beaddolt-hardenning` | `polecat/brahmin/gt-rca-canon-routing-rig-add-mpeuzvbu` | Comment `4525854509`: superseded by clean main-target routing replacement `#4096`; tracked-prefix guard/rollback/tests preserved there. | Closed superseded; do not retarget or merge. |
| `#4087` `fix: count recovery slots in scheduler capacity` | Closed | `integration/test-beaddolt-hardenning` | `polecat/crater/gt-rca-canon-polecat-refill-capacity@mpev7b1n` | Comment `4525854511`: superseded by replacement capacity path; main-target `#4081` carries admission-cap work and `#4111` folded useful recovery-slot/stale-assignment occupancy cases. | Closed superseded; useful release evidence is `#4111`. |
| `#4088` `test: cover newly-created rig bead sling routing` | Closed | `integration/test-beaddolt-hardenning` | `polecat/foundation/gt-rca-canon-new-bead-sling-smoke@mpevij9s` | Comment `4525854512`: superseded by clean main-target `#4096`; smoke coverage preserved there. | Closed superseded; do not retarget or merge. |
| `#4089` `fix: harden polecat reuse and session startup` | Closed | `integration/test-beaddolt-hardenning` | `polecat/dust/gt-rca-canon-polecat-stale-idle@mpev9pi0` | Comment `4525854864`: superseded/partially extracted; folded path preserved unique active-MR reuse blocking plus tmux/session-startup hardening. Earlier validation comment `4504995144` recorded green targeted validation before supersession. | Closed superseded/partially extracted; do not retarget or merge. |
| `#4092` `fix: converge routing sling safeguards` | Closed | `integration/test-beaddolt-hardenning` | `polecat/brahmin/gt-rca-routing-convergence@mpfr891z` | Comment `4525854872`: superseded by clean main-target `#4096`; preserves useful `#4092`/`#4086`/`#4088` route-registration and sling-routing work. Comment `4511115581` records targeted validation and a broad `go test ./...` timeout/failure outside changed-file scope. | Closed superseded; do not retarget or merge. |

## CI Failure Classification

### Baseline/Environmental Signals

These failures are not owned by the `#4110`-`#4114` internal integration PRs. They are scheduled or push-run failures on `main` and therefore classify as baseline/mainline signals unless reproduced against a release branch delta.

| Class | Run | Branch/SHA | Failing job evidence | Classification |
| --- | --- | --- | --- | --- |
| Baseline scheduled E2E | Run `26437582676` `E2E Tests` | `main` / `94b3d5aae3d96c1ed9cf1fa7eab51ffdee9cee17` | Job `77823878356` `E2E Tests (Container)`, step `Run E2E tests`, failed 2026-05-26 07:08:20 UTC. | Baseline/environmental until reproduced against a branch delta; same SHA as current `main`. |
| Baseline scheduled integration | Run `26438097543` `Nightly Integration Tests` | `main` / `94b3d5aae3d96c1ed9cf1fa7eab51ffdee9cee17` | Job `77825538322` `Full Integration Tests`, step `Run all integration tests`, failed 2026-05-26 07:25:02 UTC. | Baseline/environmental until reproduced against a branch delta; same SHA as current `main`. |
| Baseline main push CI | Run `26420662457` `CI` | `main` / `94b3d5aae3d96c1ed9cf1fa7eab51ffdee9cee17` | Jobs `77774572073` `Lint` and `77774572090` `Test` failed; `Integration Tests` job `77774572091` passed. | Mainline push signal after `#4096` merged; not owned by `#4110`-`#4114` internal integration PRs. |
| Baseline main push Windows | Run `26420662473` `Windows CI` | `main` / `94b3d5aae3d96c1ed9cf1fa7eab51ffdee9cee17` | Job `77774571977` `Windows Smoke Test`, step `Build binary`, failed 2026-05-25 21:38:37 UTC. | Mainline push signal after `#4096` merged; not owned by `#4110`-`#4114` internal integration PRs. |

### Branch-Owned or PR-Owned Failing Signals

| PR/branch | Run | Failing job evidence | Classification |
| --- | --- | --- | --- |
| `temp-merge-gt-wisp-7bc` / `fix: --force now truly bypasses MR verdict check in polecat nuke` | CI run `26463482040`; Windows run `26463482056` | Jobs `77917419179` `Lint` and `77917419207` `Test` failed; Windows job `77917418798` `Windows Smoke Test`, step `Build binary`, failed. `Integration Tests` job `77917419263` passed. | Branch-owned/temp-merge failure on `d9e3a6a165473f9f43f79a58e0810c5961269a4d`; outside `#4110`-`#4114` internal release evidence. |
| `doctor/rig-config-sync-clean` and temp merge variants | CI/Windows runs `26421685252`, `26421685230`, `26421755731`, `26421755601` | Branch jobs `77777509560` `Lint` and `77777509503` `Test` failed; branch Windows job `77777509549` `Windows Smoke Test` failed. Temp-merge jobs `77777714979` `Lint` and `77777714981` `Test` failed; temp-merge Windows job `77777714776` `Windows Smoke Test` failed. | Branch-owned doctor-path failures; not release-gate evidence. |
| Historical `#4080` / `#4081` / `#4096` / `#4108` branch signals | Prior CI runs `26263001617`, `26185346987`, `26185346989`, `26298563367`, `26334452289` | Jobs `77300338263` `Integration Tests`, `77300338267` `Test`, `77300338274` `Lint`, `77039169592` `Lint`, `77039169636` `Test`, `77039169665` `Integration Tests`, `77039169505` `Windows Smoke Test`, `77417512051` `Test`, `77417512096` `Integration Tests`, `77525753579` `Integration Tests`, `77525753580` `Test`, and `77525753586` `Lint` failed. Current PR state changed: `#4080` closed, `#4081` merged to `main`, `#4096` merged to `main`, `#4108` closed. | Not current open release blockers for `#4110`-`#4114`; resulting `main` push failures are tracked above as baseline/mainline signals. |

### Internal Integration PR Check State

`#4110`-`#4114` did not run the full `CI` or `Windows CI` code-validation workflows because their bases were integration branches, not `main`. They only show metadata/policy checks: `Block Internal PRs` skipped and `Auto-label new issues and PRs` succeeded. Treat these as PR bookkeeping evidence, not code gate evidence.

## Research Pass Log

1. Read `bd show gt-12-release-evidence-refresh` for scope, labels, and acceptance criteria.
2. Searched the worktree for existing release/evidence/disposition artifacts; no dedicated release-evidence artifact existed.
3. Read `.github/workflows/ci.yml` to inventory `main`/main-PR code gates and job names.
4. Read `.github/workflows/windows-ci.yml` to inventory Windows smoke gate.
5. Read `.github/workflows/e2e.yml` and `.github/workflows/nightly-integration.yml` to inventory scheduled baseline gates.
6. Read `.github/workflows/release.yml` to inventory tag-gated release publication jobs.
7. Read metadata workflows: `block-internal-prs`, `triage-label`, `remove-needs-info`, `remove-needs-triage`, and `close-stale-needs`.
8. Queried PR `#4110` JSON and comments; recorded merge into routing identity integration branch.
9. Queried PR `#4111` JSON and comments; recorded merge into capacity/admission integration branch.
10. Queried PR `#4112` JSON and comments; recorded merge into notification actionability integration branch.
11. Queried PR `#4113` JSON and comments; recorded merge into recovery false-positive integration branch.
12. Queried PR `#4114` JSON and comments; recorded wrong-target/non-deliverable closure comment.
13. Queried PRs `#4085`-`#4089` and `#4092` JSON/comments; recorded current supersession/internal-reference dispositions.
14. Queried workflow run lists for `integration/gt-1-2-convergence-cleanup`, `integration/test-beaddolt-hardenning`, recent failures, and target branch metadata.
15. Queried failed run job details for `26437582676`, `26438097543`, `26420662457`, `26420662473`, `26463482040`, `26463482056`, `26421685252`, `26421685230`, `26421755731`, and `26421755601` to classify baseline/mainline vs branch-owned failures.

## Pre-Implementation Review Log

1. Scope review: artifact is release evidence only; no broad README or product docs changes.
2. Gate-map review: verified all recorded branches came from `git ls-remote --heads origin 'integration/gt-1-2-*'` or GitHub branch API.
3. PR-disposition review: verified `#4110`-`#4113` are merged and `#4114` has an explicit wrong-target closure comment.
4. Supersession review: verified `#4086`, `#4087`, `#4088`, `#4089`, and `#4092` closure comments identify replacement paths; `#4085` remains open only as internal reference.
5. CI-classification review: verified integration branches have no branch-filtered Actions runs, so PR metadata checks are not code gate evidence.

## Targeted Validation

- `gh run list --branch integration/gt-1-2-convergence-cleanup --limit 10` returned no runs.
- `gh api repos/gastownhall/gastown/actions/runs?branch=integration/gt-1-2-convergence-cleanup&per_page=10` returned `total_count: 0`.
- `gh api repos/gastownhall/gastown/actions/runs?branch=integration/test-beaddolt-hardenning&per_page=10` returned `total_count: 0`.
- `gh pr list --search "gt-12 OR gt 1.2"` returned `#4110`-`#4114` and `#4109` as the relevant current `gt-12` PR evidence set.
- `gh pr view 4080`, `4081`, `4096`, and `4108` verified current replacement-path disposition: `#4081` and `#4096` merged to `main`; `#4080` and `#4108` closed.

## Post-Implementation Review Log

1. Diff-scope review: only `docs/release/gt-1.2-release-evidence.md` was added; no broad docs or code files were changed.
2. PR-table review: required records `#4110`-`#4114`, `#4085`-`#4089`, and `#4092` are present with current state, base/head, and decision evidence.
3. CI-inventory review: all workflow files under `.github/workflows` are represented by code-gate, release, schedule, or metadata categories.
4. Failure-classification review: baseline/environmental and branch-owned failures include exact run IDs, job IDs, job names, and branch/SHA context.
5. Placeholder review: checked common placeholder markers; no stale placeholders remain after this update.
