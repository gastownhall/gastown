# Security Dependency Exceptions

Last reviewed: 2026-10-24

This file records dependency advisories that are accepted temporarily because
they are limited to local development, evaluation, or test tooling. Runtime
dependency advisories are not eligible for this exception without a separate
security review.

## Model evaluation npm tooling

Scope:

- `gt-model-eval/package.json`
- `gt-model-eval/package-lock.json`
- `promptfoo` and transitive local evaluation dependencies

Status:

- `npm audit --omit=dev` is clean.
- `promptfoo` 0.125.0 plus `npm audit fix` cleared the earlier `uuid` chain
  and every other advisory that has a fix within the current dependency ranges.
- A full `npm audit` still reports two high findings that need a major bump
  of a transitive dependency to fix:
  - `@azure/msal-node` 6.x (GHSA-xcx6-jx8p-x3h5, fixed in 7.1.0), pulled in
    by `@azure/identity`.
  - `basic-ftp` 5.x (GHSA-c475-qrg2-pj4r, fixed in 6.2.1), pulled in by
    `promptfoo` -> `proxy-agent` -> `pac-proxy-agent` -> `get-uri`.

Risk decision:

`gt-model-eval` is a private local evaluation harness. It is not packaged into
the `gt` release archives, not published by the npm package, and not used by the
runtime CLI. The remaining findings require a breaking `promptfoo` change or an
upstream transitive fix, so they are accepted for the local eval harness only.

Required controls:

- `npm audit --omit=dev` for `gt-model-eval/` must remain clean.
- Do not expose `promptfoo view` or other eval servers on a public interface.
- Do not commit generated eval result artifacts containing prompts, credentials,
  or model responses.
- Renovate must include `gt-model-eval/package.json` so promptfoo updates are
  proposed automatically.
- Re-test and remove this exception when a compatible promptfoo update clears
  the remaining `@azure/msal-node` and `basic-ftp` advisories.

## Testcontainer Docker client tooling

Scope:

- `internal/testutil`
- `github.com/testcontainers/testcontainers-go`
- `github.com/docker/docker` as pulled by testcontainers

Status:

- `govulncheck ./cmd/gt` is clean.
- A repository scan excluding `gt-model-eval/node_modules` reports
  `GO-2026-4887` and `GO-2026-4883` in Docker/Moby via the testcontainer
  graph. The advisories currently report no fixed Docker/Moby module version.

Risk decision:

The vulnerable Docker client dependency is in the testcontainer path used by
test helpers. It is not in the production `cmd/gt` dependency graph. The
affected Moby advisories concern Docker plugin behavior and are accepted for
local testcontainer usage while no fixed module version is available.

Required controls:

- `govulncheck ./cmd/gt` must remain clean for release builds.
- Do not import `internal/testutil` or testcontainer packages from production
  code.
- Run testcontainer tests only against trusted local Docker daemons.
- Re-check the exception when `github.com/docker/docker` or testcontainers ships
  a fixed version for these advisories.
