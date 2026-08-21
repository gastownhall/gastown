# Releasing Gas Town

## Authority Boundary

This checkout is the maintained `marlon-costa-dc/gastown` downstream fork. Its
checked-in [release workflow](.github/workflows/release.yml) publishes
fork-owned prereleases; it is not the `gastownhall/gastown` release workflow.
The canonical upstream process lives in the
[upstream RELEASING.md](https://github.com/gastownhall/gastown/blob/main/RELEASING.md).
Do not copy either process into the other repository.

A release requires an explicit release assignment and approval. Documentation,
test, and integration work do not create a tag or release.

## Downstream Fork Release Delta

The downstream **project integration lane** is fork `main`. It is distinct from
a temporary Gas Town **epic integration branch**, which lands into its recorded
base through `gt mq integration land`; see
[Integration Branches](docs/concepts/integration-branches.md#terminology-boundary).

Every downstream release records and verifies:

- the upstream tag or commit used as its base;
- the reviewed fork-only commit delta on fork `main`;
- the downstream version and consumer pins;
- the release repository, provenance owner, and install target;
- fresh build, test, and installed-binary evidence from the tagged integration
  commit.

Fork versions use `X.Y.Z-dcN`, with an annotated `vX.Y.Z-dcN` tag. The tag text
without its leading `v` must exactly match `Version` in
`internal/cmd/version.go`; `make check-version-tag` enforces that equality.

The upstream `scripts/bump-version.sh` accepts only `MAJOR.MINOR.PATCH`, updates
upstream package channels, and is not the owner for a `-dcN` release. The
upstream release formula is not the downstream release path either. Prepare the
fork version in a dedicated release lane and review it through a fork PR.

## Prepare and Tag

1. Start from current fork `main` in the release lane. Record the upstream base,
   fork delta, target version, and affected consumer pins on the release bead.
2. Update `internal/cmd/version.go` to the new `X.Y.Z-dcN` version and update any
   downstream consumer pins named by the release bead.
3. Run the repository gates and review the release diff. Merge the release PR
   into fork `main`; never tag an unintegrated lane commit.
4. Confirm the proposed tag and release do not already exist. Published releases
   and tags are immutable.
5. With the reviewed fork-main merge commit checked out, create the annotated
   tag, run the tag/version check, and only then push the tag:

   ```bash
   git tag -a vX.Y.Z-dcN -m "Release vX.Y.Z-dcN"
   make check-version-tag
   git push origin refs/tags/vX.Y.Z-dcN
   ```

`make check-version-tag` is intentionally a no-op before HEAD has an exact
`v*` tag, so its decisive release check belongs after local tag creation and
before the tag push.

## What the Fork Workflow Publishes

A pushed `v*` tag starts four build jobs and one publish job:

- GoReleaser builds Linux AMD64 and FreeBSD AMD64 archives without publishing;
- native runners build Linux ARM64, Windows AMD64, macOS ARM64, and macOS AMD64;
- Linux, Windows, and macOS binaries are smoke-tested before packaging; the
  FreeBSD archive is cross-built and checked as part of the complete asset set;
- the publish job requires all six archives, writes SHA-256 checksums, produces
  an SPDX JSON SBOM, and creates GitHub artifact attestations;
- the release is assembled as a draft and becomes a public prerelease only
  after the complete asset set uploads successfully.

The publish step hard-gates `GITHUB_REPOSITORY` to
`marlon-costa-dc/gastown`. It publishes only to that fork's GitHub Releases.
It does not update Homebrew, npm, or upstream release metadata, and the workflow
has no manual-dispatch branch path.

If a draft exists after a failed run, rerunning that tagged workflow may resume
the draft and replace its incomplete assets. If a published release already
exists, the workflow fails instead of mutating it. Fixes after publication use
a new reviewed commit and a new `-dcN` version; never move or replace the old
tag.

## Verify the Published Prerelease

Release evidence includes the successful tag workflow, the non-draft
prerelease, its complete assets, checksums, attestations, and an exercised
downloaded binary. For example:

```bash
gh run list --repo marlon-costa-dc/gastown --workflow Release --branch vX.Y.Z-dcN --limit 1
gh release view vX.Y.Z-dcN --repo marlon-costa-dc/gastown
gh release download vX.Y.Z-dcN --repo marlon-costa-dc/gastown --dir dist
(cd dist && sha256sum -c gastown_*_checksums.txt)
```

Verify downloaded archives against the published checksum file, then run the
binary for the target platform and confirm it reports `X.Y.Z-dcN`. Record the
commands, exit codes, decisive output, artifact URLs, and consumer-pin update on
the release bead.

## Upstream Releases

Upstream distribution channels, version preparation, Homebrew, npm, and
troubleshooting are owned by
[gastownhall/gastown's release documentation](https://github.com/gastownhall/gastown/blob/main/RELEASING.md).
Use that runbook only from an upstream release checkout with upstream release
authority.
