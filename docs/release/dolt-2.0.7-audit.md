# Dolt 2.0.7 Audit for gt 1.2

Date: 2026-05-26

Scope: audit the Gas Town Dolt dependency from the previously pinned/released floor (`1.84.0` minimum, `1.83.0` testcontainers image, `1.82.4` E2E Docker build) to Dolt `2.0.7`.

## Updated References

- Runtime minimum: `internal/deps.MinDoltVersion` is now `2.0.7`.
- Testcontainers image: `dolthub/dolt-sql-server:2.0.7` in Unix and Windows testutil constants.
- CI integration image pre-pulls now use `dolthub/dolt-sql-server:2.0.7`.
- CI, nightly, and Docker installs pin Dolt `v2.0.7` instead of relying on `latest`.
- E2E Docker build `DOLT_VERSION` is now `2.0.7`.
- User-facing prerequisites now require Dolt `2.0.7+`.

## Compatibility Findings

- Dolt 2.0 is backward compatible with 1.x databases, but databases written by 2.x clients may not be readable by all 1.x clients. Gas Town now rejects Dolt binaries older than `2.0.7` before install/doctor use, which prevents mixed-client writes to shared `.dolt-data` stores.
- Dolt 2.0 enables automatic garbage collection, archival storage, and adaptive storage for TEXT, JSON, GEOMETRY, and BLOB types by default. Gas Town does not parse Dolt storage internals directly, so no migration code is needed beyond the binary floor.
- Dolt 1.86 changed the `dolt_revert()` result schema. Gas Town does not call `dolt_revert()` directly, so no command compatibility shim is needed.
- Dolt 2.0.1 changed `dolt diff -r sql` to return a nonzero exit when schema changes prevent a complete SQL diff. Gas Town does not depend on that command in production paths.
- Dolt 2.0.3 fixes an SSH child-process leak from `CALL dolt_fetch` against SSH remotes. This reduces risk for `gt dolt` remote flows; no Gas Town workaround is needed.
- Dolt 2.0.7 includes the `go-mysql-server` CachedResults/hash-join leak fix, which is relevant to long-lived `dolt sql-server` processes and is the main reason to require this patch release.

## Validation Evidence

- `dolt version`: local host still reports `dolt version 1.84.0`, so the new `CheckDolt` gate should classify this host as too old until the system binary is upgraded.
- `gt dolt status`: server was running on port `3307`, query latency `0s`, `4 / 1000` connections, with one pre-existing orphan database (`testrig`) reported for cleanup.
- `gt scheduler status`: scheduler active, 3 scheduled beads, 1 ready, 3 active polecats, and 9 free slots of 25.
- `go test ./internal/deps ./internal/doctor ./internal/testutil`: passed.
- Focused Dolt command tests under `./internal/cmd`: passed.
- `go build ./cmd/gt`: passed.
- `gh api repos/dolthub/dolt/releases/tags/v2.0.7 --jq '.assets[].name'`: verified the `install.sh` and `dolt-linux-amd64.tar.gz` release assets used by CI/Docker are present.
- `docker manifest inspect docker.io/dolthub/dolt-sql-server:2.0.7`: verified the pinned testcontainers image exists for linux/amd64 and linux/arm64. The unqualified `dolthub/dolt-sql-server:2.0.7` manifest check fails in this environment because short-name resolution requires an interactive prompt; CI/testcontainers use Docker Hub resolution for the same image name.
- Release-note sources checked with `gh api repos/dolthub/dolt/releases/tags/v2.0.7`, `v2.0.0`, and release entries for `v1.85.0`, `v1.86.0`, `v1.86.5`, `v2.0.1` through `v2.0.6`.

Non-blocking note: an intentionally broad `go test ./internal/cmd -run 'TestDolt|TestInstall.*Dolt|Test.*Dolt'` selection also matched `TestSlingSetsDoltAutoCommitOff` and failed because fixture bead `gt-test456` was absent. A focused Dolt command test selection passed.
