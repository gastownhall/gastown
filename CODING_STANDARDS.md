# Coding standards

## Tests

- Strongly prefer integration and end-to-end tests over unit tests when behaviour crosses CLI, process, filesystem, tmux, Git, or Dolt boundaries.
- Strongly prefer exercising real system behaviour over treating a green test suite as proof that the product works.
- Mock only third-party services or process boundaries that we cannot control. Do not mock packages or business rules that we own.
- For command and workflow changes, the default proof is to run the real command against a temporary town or rig and assert exit status, output, and persisted state.
- Assert results produced by production code. Do not assert values assembled only by test helpers, copied production logic, or mocks configured by the same test.
- Use `testutil.RequireDoltContainer`, `testutil.StartIsolatedDoltContainer`, or `testutil.RequireTownEnv` when a test needs those resources. Self-contained tests that create their own temporary town do not need an integration guard.
- Keep concurrency tests deterministic. Synchronize on observable state or explicit test seams instead of sleeps when possible, and run race-sensitive packages with `go test -race ./path/to/package`.
- Use `make test` as the repository-wide unit-test gate; it includes shell checks before `go test ./...`. Run tagged integration tests for affected command paths and use `make test-e2e-container` for installation or full-workspace behaviour.

## Comments and docs

- Code comments use ASD-STE100 Simplified Technical English: use short direct sentences, one instruction per sentence, and consistent terms.
- Use established Gas Town domain terms consistently: town, rig, convoy, bead, molecule, wisp, polecat, crew, dog, hook, sling, and refinery. Use "merge request" for Gas Town's refinery queue and "pull request" for GitHub contributions. Do not invent synonyms for existing terms.
- Do not write comments that only repeat what the code already makes clear. Explain invariants, boundary conditions, compatibility constraints, and non-obvious reasons.
- Do not put brittle references in comments or docs, such as line numbers, temporary paths, current versions, or "as of today" claims, when those details can change.
- Update user-facing docs when commands, configuration, output, or operational behaviour changes.

## Common footguns

- Tautological tests that assert a mock was called exactly as the test configured it.
- Mocks of packages, modules, or services we own.
- Treating a green suite as proof that a real agent, town, or rig can complete the workflow.
- Encoding agent judgment in Go: behavioural heuristics and arbitrary decision thresholds belong in agents and formulas. Deterministic protocol rules and safety boundaries can remain in Go.
- Confusing ephemeral wisps with durable Dolt-backed issues, or assuming every command reads the same Beads database.
- Swallowing process, tmux, Git, or persistence errors and then reporting success.
- Using sleeps to hide lifecycle races or relying on goroutine completion order for stable results.
- Narrating comments, stale README claims, and hard-coded implementation details.
- Evading complexity or quality gates with denser syntax, hidden branching, or indirection that does not reduce real complexity.

## Go

- Format with `gofmt`; keep `go vet ./...` and `golangci-lint run --timeout=5m` clean.
- Production Go code changes should report no violations on the `messgo` rulesets `design`, `codesize`, and `unusedcode`.
- Production Go code changes should report a covered-MSI of 80% or above from `mutago`.
- Validate trust boundaries manually as well as with linters. Check user-controlled paths, subprocess arguments, SQL identifiers, and external input before use; linter exclusions are not evidence that an operation is safe.
- Keep the module path `github.com/steveyegge/gastown` and the Go version in `go.mod` honest. Do not use newer language features without deliberately updating the module version.
- Follow Zero Framework Cognition: Go code transports data, enforces deterministic protocols and safety boundaries, and performs deterministic operations; agents and molecule formulas perform behavioural judgment and reasoning.
- Use existing package boundaries and production seams. Keep Cobra command wiring in `internal/cmd` thin, and put reusable domain behaviour in the relevant `internal` package.
- Pass `context.Context` through blocking work. Give subprocesses and external operations explicit cancellation or timeouts.
- Wrap errors with operation and resource context. Preserve causes with `%w`, and do not convert failures into apparent success.
- Make concurrent output deterministic. Protect shared state, avoid goroutine leaks, and verify concurrency changes with the race detector.
- Preserve compatibility across documented town, rig, worktree, and configuration layouts unless a migration is part of the change.
- Run `make build` and `make test` before merging changes that can affect Go behaviour. Run focused package tests during development, tagged integration tests for affected integration paths, and `make test-e2e-container` when changing installation or end-to-end workspace behaviour. Markdown-only changes do not require Go builds or tests.
- For releases, keep the release tag and `internal/cmd/version.go` version equal; verify tagged commits with `make check-version-tag`.
- Use a dedicated branch for every pull request, based on the intended upstream branch. Never open a pull request from a fork's `main` branch.
