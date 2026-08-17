# Contributing to Gas Town

Thanks for your interest in contributing! Gas Town is experimental software, and we welcome contributions that help explore these ideas.

## Getting Started

1. Fork the repository
2. Clone your fork
3. Install prerequisites (see README.md)
4. Build and test: `make build-dev && go test ./...`
   (`make build` defaults to `CGO_ENABLED=0`. Bare `go build` / `go run` do not.
   Use `make build-cgo` only if you need beads' in-process embedded Dolt engine.)

## Setting up a rig to contribute to Gas Town

If you run a Gas Town rig against this repo, you don't own the canonical
repository, so the rig must **fetch from upstream but push to your fork**.
`gt rig add` has first-class support for this through `--push-url` and
`--upstream-url`.

1. Fork `gastownhall/gastown` on GitHub (gives you
   `https://github.com/<you>/gastown`).
2. Add the rig with fork routing:

   ```bash
   gt rig add gastown https://github.com/gastownhall/gastown \
     --push-url     https://github.com/<you>/gastown \
     --upstream-url https://github.com/gastownhall/gastown
   ```

What each flag does at the git-remote level:

- The positional `<git-url>` (`https://github.com/gastownhall/gastown`)
  becomes `origin`'s **fetch** URL — the rig pulls canonical history from
  upstream.
- `--push-url` sets `origin`'s **push** URL to your fork, so all pushes land
  on `https://github.com/<you>/gastown` and never on the canonical repo.
- `--upstream-url` adds a separate named `upstream` remote pointing at the
  canonical repo, so rebases against `upstream/main` work without juggling
  URLs.

> **Current limitation — the refinery is not yet fork-aware.** Until the
> behavioral half of
> [gastownhall/gastown#1794](https://github.com/gastownhall/gastown/issues/1794)
> ships, even a correctly-configured fork rig will have its refinery attempt
> to **merge polecat branches into the fork's `main`**, diverging it from
> upstream. If you want strict PR-only behavior, do not start the refinery
> (park the rig with `gt rig park <rig>`) and use the
> polecat → branch → manual PR path instead.

If you set up a rig **without** these flags and your fork's `main` has
already been polluted, see
[docs/guides/fork-rig-setup.md](docs/guides/fork-rig-setup.md) for
verification and recovery steps.

## Development Workflow

We use a direct-to-main workflow for trusted contributors. For external contributors:

1. Create a feature branch from `main`
2. Make your changes
3. Ensure tests pass: `go test ./...`
4. Submit a pull request

### PR Branch Naming

**Never create PRs from your fork's `main` branch.** Always create a dedicated branch for each PR:

```bash
# Good - dedicated branch per PR
git checkout -b fix/deacon-startup upstream/main
git checkout -b feat/auto-seance upstream/main

# Bad - PR from main accumulates unrelated commits
git checkout main  # Don't PR from here!
```

Why this matters:
- PRs from `main` accumulate ALL commits pushed to your fork
- Multiple contributors pushing to the same fork's `main` creates chaos
- Reviewers can't tell which commits belong to which PR
- You can't have multiple PRs open simultaneously

Branch naming conventions:
- `fix/*` - Bug fixes
- `feat/*` - New features
- `refactor/*` - Code restructuring
- `docs/*` - Documentation only

## Code Style

- Follow standard Go conventions (`gofmt`, `go vet`)
- Keep functions focused and small
- Add comments for non-obvious logic
- Include tests for new functionality

## Design Philosophy

Gas Town follows two core principles that shape every contribution. Understanding
these will save you (and reviewers) time.

### Zero Framework Cognition (ZFC)

**Go provides transport. Agents provide cognition.**

Gas Town's Go code handles plumbing: tmux sessions, message delivery, hooks,
nudges, file transport, and observability primitives (like `bd show --json`).
All reasoning, judgment calls, and decision-making happen in the AI agents via
molecule formulas and role templates.

This means:
- **No hardcoded thresholds in Go.** Don't write `if age > 5*time.Minute`
  to decide if an agent is stuck. Expose the age as data and let the agent decide.
- **No heuristics in Go.** Don't write detection logic that pattern-matches
  agent behavior. Give agents the tools to observe, and let them reason.
- **Formulas over subcommands.** If the feature is "detect X and do Y," it's
  probably a molecule step, not a new `gt` subcommand.

**The test:** Before adding Go code, ask yourself — *"Am I adding transport or
cognition?"* If the answer is cognition, it should be a molecule step or
formula instruction instead.

For the full rationale, see
[Zero Framework Cognition](https://steve-yegge.medium.com/zero-framework-cognition-a-way-to-build-resilient-ai-applications-56b090ed3e69).

### Bitter Lesson Alignment

Gas Town bets on models getting smarter, not on hand-crafted heuristics getting
more elaborate. If an AI agent can observe data and reason about it, we expose
the data (transport) rather than encoding the reasoning (cognition). Today's
clumsy heuristic is tomorrow's technical debt — but a clean observability
primitive ages well.

**Examples:**

| Good (transport) | Bad (cognition in Go) |
|---|---|
| `gt nudge <session> "message"` | Go code deciding *when* to nudge |
| `bd show --json` exposing step status | Go code deciding *what* step status means |
| `tmux has-session` checking liveness | Go code with hardcoded "stuck after N minutes" |

## What to Contribute

Good first contributions:
- Bug fixes with clear reproduction steps
- Documentation improvements
- Test coverage for untested code paths
- Small, focused features

For larger changes, please open an issue first to discuss the approach.

## Commit Messages

- Use present tense ("Add feature" not "Added feature")
- Keep the first line under 72 characters
- Reference issues when applicable: `Fix timeout bug (gt-xxx)`

## Testing

Run the full test suite before submitting:

```bash
go test ./...
```

For specific packages:

```bash
go test ./internal/wisp/...
go test ./cmd/gt/...
```

### Integration Test Guards

Integration tests (tagged `//go:build integration`) require external resources
that may not be available in every environment. Use the helpers in
`internal/testutil` to skip gracefully when prerequisites are missing:

| Helper | When to use |
|--------|-------------|
| `testutil.RequireDoltContainer(t)` | Test needs a running Dolt SQL server (starts a Docker container) |
| `testutil.StartIsolatedDoltContainer(t)` | Test needs its own isolated Dolt instance (per-test container) |
| `testutil.RequireTownEnv(t)` | Test needs a live Gas Town workspace (checks `workspace.FindFromCwd` + `rigs.json`); returns root path |

**`requireDoltServer`** (in `internal/cmd`) is a local wrapper around
`testutil.RequireDoltContainer` used by the `cmd` package's integration tests.

**When to use which guard:**

- Tests that connect to Dolt (create databases, run SQL) →
  `RequireDoltContainer` or `StartIsolatedDoltContainer`
- Tests that need a real Gas Town directory tree (shell out to `gt`/`bd` with
  workspace detection) → `RequireTownEnv`
- Tests that create their own temporary town via `t.TempDir()` → no guard needed
  (they are self-contained)

For packages with many Dolt-dependent tests, prefer adding
`testutil.EnsureDoltContainerForTestMain()` in a `TestMain` function so all
tests in the package share a single container.

**A note on Ollama:** if a test needs a real LLM provider, check resource use
before reaching for a local Ollama install — a local model can OOM or hang the
host machine (dev laptop or CI runner alike). If you're determined to exercise
Ollama, prefer running it in Docker with capped CPU/memory (`--cpus`, `--memory`)
over a bare host install.

## Releasing

Releases are cut from tags of the form `vX.Y.Z`. See [RELEASING.md](RELEASING.md)
for the full workflow. One guardrail to know about:

- `make check-version-tag` verifies the `Version` constant in
  `internal/cmd/version.go` matches the tag at HEAD. The release workflow runs
  this before GoReleaser and fails the release on mismatch. Prevents recurrence
  of [#3459](https://github.com/steveyegge/gastown/issues/3459). Run it locally
  after bumping if you want to catch drift before pushing the tag.

## Questions?

Open an issue for questions about contributing. We're happy to help!
