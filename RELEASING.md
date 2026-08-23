# Releasing Gas Town

This document owns the upstream `gastownhall/gastown` release process. The
workflow below is not a generic fork release mechanism.

## Distribution Channels

| Channel | Mechanism | Automatic? |
|---------|-----------|------------|
| **GitHub Release** | GoReleaser via Actions on tag push | Yes |
| **Homebrew tap** (`gastownhall/gastown`) | Actions writes an asset-based formula after archives upload | Yes |
| **Homebrew core** (if listed) | Homebrew bot detects new release | Yes (24-48h delay) |
| **npm** (`@gastown/gt`) | Actions workflow, OIDC trusted publishing | Yes (when org is set up) |

## Downstream Fork Release Delta

A maintained downstream fork has a separate release authority and integration
lane. Its release procedure must define all of the following outside the
upstream runbook:

- the upstream tag or commit used as the base;
- the reviewed fork-only commit delta;
- the downstream version/channel and every consumer pin;
- the artifact repository, signing/provenance owner, and install target;
- fresh build, test, and installed-binary evidence from the fork integration
  lane.

Fork release binaries that keep embedded Dolt support must build with
`CGO_ENABLED=1` and the dependency-owner `gms_pure_go` tag. This keeps the
embedded backend while removing the version-specific ICU shared-library
dependency. Run `scripts/verify-cgo.sh` on each CGO release binary before
publication; an archive that links `libicu` is not portable and must not be
published.

The upstream automation deliberately does not publish fork tags:

- `.github/workflows/release.yml` gates publishing jobs to the
  `gastownhall/gastown` repository;
- `scripts/bump-version.sh` accepts upstream `MAJOR.MINOR.PATCH` versions, not a
  downstream suffix scheme;
- the release formula, Homebrew, npm, and attestation destinations are upstream
  owners.

Therefore a downstream release needs downstream-owned automation or an
explicitly documented manual owner. Do not push a fork tag expecting the
upstream workflow to publish it, and do not modify upstream release metadata to
encode a local-only version.

The downstream **project integration lane** (often fork `main`) is distinct
from a Gas Town **epic integration branch**. The latter is temporary,
epic-scoped, and lands through `gt mq integration land`; see
[Integration Branches](docs/concepts/integration-branches.md#terminology-boundary).
Documentation-only convergence does not create a tag or release.

## How to Release

### Option A: Automated (recommended)

Use the release formula, which handles all steps:

```bash
gt mol wisp create gastown-release --var version=X.Y.Z
```

### Option B: Bump script

```bash
cd gastown/mayor/rig
./scripts/bump-version.sh X.Y.Z --commit --tag --push --install
```

### Option C: Manual

1. Update CHANGELOG.md `[Unreleased]` section
2. Update `internal/cmd/info.go` `versionChanges` slice
3. Run `./scripts/bump-version.sh X.Y.Z` (updates version.go, package.json, CHANGELOG header)
4. Commit, tag, push:

```bash
git add -A
git commit -m "chore: Bump version to X.Y.Z"
git tag -a vX.Y.Z -m "Release vX.Y.Z"
git push origin main
git push origin vX.Y.Z
```

5. Rebuild locally:

```bash
make install        # builds, codesigns, installs to ~/.local/bin
gt daemon stop && gt daemon start
```

## What Happens After Tag Push

The `release.yml` workflow triggers automatically:

1. **Verify tag matches Version constant** — runs `make check-version-tag` and
   aborts the release if the pushed tag (`vX.Y.Z`) doesn't match the `Version`
   constant in `internal/cmd/version.go`. Prevents recurrence of
   [#3459](https://github.com/gastownhall/gastown/issues/3459) where v0.13.0
   shipped reporting 0.12.1.
2. **goreleaser** job builds binaries for all platforms and creates the GitHub Release
3. **update-homebrew-formula** job writes an asset-based formula to `gastownhall/homebrew-gastown` when tap credentials are configured
4. **publish-npm** job publishes to npm (best-effort, `continue-on-error: true`)

Manual dispatch is only for rerunning a release from a `v*` tag. Publishing jobs are guarded to skip branch refs.

### Running the tag/version check locally

```bash
make check-version-tag
```

The target is a no-op on untagged HEADs, so it's safe to run on any checkout.
It only fails when HEAD is tagged `vX.Y.Z` and the `Version` constant doesn't
match. Run it after `scripts/bump-version.sh` and before pushing the tag if you
want to catch drift before CI does.

## Homebrew tap (`gastownhall/gastown`)

The release workflow automatically overwrites `Formula/gastown.rb` in the `gastownhall/homebrew-gastown` repo on every tag push. It prefers the GitHub App credentials `HOMEBREW_TAP_APP_ID` and `HOMEBREW_TAP_APP_PRIVATE_KEY`, and falls back to `HOMEBREW_TAP_TOKEN` if present.

The tap formula installs prebuilt release assets:

```bash
brew install gastownhall/gastown/gastown
```

The normal user-facing Homebrew path remains homebrew-core:

```bash
brew install gastown
```

## Homebrew core

If Gastown is listed in **homebrew-core**, the formula lives at:
`https://github.com/Homebrew/homebrew-core/blob/HEAD/Formula/g/gastown.rb`

### How it updates

Homebrew's `BrewTestBot` automatically detects new GitHub releases and opens
a PR to homebrew-core. Gastown is on the autobump list — the bot checks
**every ~3 hours**.

### If the bot doesn't pick it up

Gastown is on the autobump list, so `brew bump-formula-pr` will refuse to
submit a manual PR. If the bot hasn't updated after 6+ hours, check
https://github.com/Homebrew/homebrew-core/pulls?q=gastown for stuck PRs.

### Verifying

```bash
brew update
brew info gastown    # Check version
brew upgrade gastown # Upgrade if installed
```

## npm (`@gastown/gt`)

### How it works

The workflow uses **OIDC trusted publishing** (npm provenance). No NPM_TOKEN
secret is needed — the `id-token: write` permission on the job generates a
short-lived OIDC token that npm trusts because the GitHub repo is linked to
the npm package.

### Prerequisites

The `@gastown` npm organization must exist and be linked to this repo:

1. Go to https://www.npmjs.com and create (or join) the `@gastown` org
2. Under org settings, enable "Require 2FA" and configure trusted publishing
3. Link `gastownhall/gastown` as a trusted publisher for `@gastown/gt`

### Current status (as of 2026-03-06)

The `@gastown` npm org was secured by a community member (Ivan Casco Valero,
ivan@ivancasco.com) to prevent scope squatting. Ownership transfer is pending.
Until the org is transferred, npm publish will fail gracefully without blocking
the release (`continue-on-error: true` in the workflow).

### Verifying

```bash
npm view @gastown/gt version
npm install -g @gastown/gt
gt version
```

## Files Updated During Release

| File | What changes |
|------|-------------|
| `CHANGELOG.md` | New version section with date |
| `internal/cmd/info.go` | `versionChanges` entry for `gt info --whats-new` |
| `internal/cmd/version.go` | `Version` constant |
| `npm-package/package.json` | `version` field |
| `flake.nix` | version + vendorHash (only if `nix` is in PATH) |
| `gastownhall/homebrew-gastown/Formula/gastown.rb` | asset URLs + `sha256` updated by release workflow |

## Troubleshooting

### GoReleaser fails with "replace directives"

The workflow rejects `go.mod` files with `replace` directives (they break
`go install`). Remove the replace directive and commit before tagging.

### npm publish returns 404

The `@gastown` npm org doesn't exist or you don't have publish access.
See the npm section above. The release still succeeds — npm is best-effort.

### Homebrew shows old version after a release

For the `gastownhall/gastown` tap, check the `update-homebrew-formula` job and
the tap's `Formula/gastown.rb` commit history. For homebrew-core, check
https://github.com/Homebrew/homebrew-core/pulls?q=gastown for stuck BrewTestBot
PRs. Manual `brew bump-formula-pr` is blocked for autobump formulae.

### `make install` shows `-dirty` suffix

The `.beads/` directory has unstaged changes. This is cosmetic — the version
number is correct. The `-dirty` comes from `git describe` seeing any unstaged
modifications.

### Version in version.go is still old after bump script

The bump script reads the current version from version.go and replaces it.
If version.go was manually edited to a different version, the script's sed
pattern won't match. Fix version.go manually and re-run.
