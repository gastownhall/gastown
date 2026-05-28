# gt 1.2 Release Build Audit

Date: 2026-05-29

Scope: release-build validation for the v1.2.0 schema/config hotfix branch. This records the Go toolchain, GoReleaser target matrix, CGO setting, and dependency-surface decision behind the beads v1.0.4 update.

## Build Matrix

`.goreleaser.yml` builds `cmd/gt` for:

- `linux/amd64`
- `linux/arm64`
- `darwin/amd64`
- `darwin/arm64`
- `windows/amd64`
- `freebsd/amd64`

Every release target sets `CGO_ENABLED=0`. The linux arm64 and windows amd64 `CC`/`CXX` entries were removed because they are inert when CGO is disabled.

## Toolchain

- Module directive: `go 1.26.2`
- Local validation toolchain: `go env GOVERSION` returned `go1.26.2`
- GoReleaser config version: `.goreleaser.yml` `version: 2`

The Go 1.26.2 directive is intentional for this release branch so the release workflow and local validation use the same toolchain surface as the beads v1.0.4 dependency update.

## Dependency Surface

`github.com/steveyegge/beads v1.0.4` is required for the schema/config compatibility fixes in this branch. Its module graph includes embedded Dolt engine and cloud SDK packages, but those packages must not be linked into release archives unless the release build actually imports the CGO-enabled code path.

Validation evidence:

- `CGO_ENABLED=0 go list -deps ./cmd/gt` produced no packages matching `github.com/dolthub/dolt/`, `github.com/dolthub/go-mysql-server`, `vitess.io/vitess`, `github.com/aws/aws-sdk-go-v2`, `github.com/Azure/azure-sdk-for-go`, `cloud.google.com/go`, or `github.com/oracle/oci-go-sdk`.
- `CGO_ENABLED=1 go list -deps ./cmd/gt` does include that embedded Dolt/cloud SDK surface, confirming the release mitigation depends on preserving CGO-off builds.
- `goreleaser check` passed for the edited GoReleaser configuration.
- `goreleaser build --snapshot --clean` passed and built all six release targets listed above.

## Release Decision

The v1.2.0 release branch should keep CGO disabled across the GoReleaser matrix. Re-enabling CGO on any release target requires a fresh dependency-surface audit and platform build validation before tagging.
