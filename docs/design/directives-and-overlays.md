# Role Directives and Formula Overlays

> Operator-customizable agent behavior without modifying the Go binary.

> **Reference examples:** [`docs/contrib-harnesses/`](../contrib-harnesses/)
> contains copy-and-adapt directives and overlays that contributors can drop
> into their own rig. See for example `polecat-pr-flow/` for a rig that gates
> work on GitHub PR review rather than the canonical Refinery merge queue.

## Problem

The MEOW stack embeds formulas and role templates in the binary — intentionally
centralized for consistency, but leaving no override path. Operators cannot
customize agent behavior at the rig or town level.

**Concrete failure:** Multiple crew members autonomously posted `gh pr review`
comments on GitHub during PR review tasks. The formula says "post to GitHub,"
and there was no way for the operator to say "actually, in this rig, report
back instead."

## Design: Two Levels

### Level 1: Role Directives

Per-role behavioral boundaries injected at prime time. Operator-authored
Markdown that modifies how agents of a given role behave, regardless of which
formula they are running.

**File layout:**

```
~/gt/directives/<role>.md              # Town-level (all rigs)
~/gt/<rig>/directives/<role>.md        # Rig-level (wins by appearing last)
```

**Injection point:** After the role template, before context files and handoff
content. Directives carry an authority marker that lets them override formula
workflow instructions. They cannot redefine the installed command contract:
`bd` owns issue CRUD, while `gt` owns execution and lifecycle.

**Precedence:** Town and rig directives **concatenate**. If both exist, the
combined output is `<town content>\n<rig content>`. The rig directive gets the
last word, so it effectively overrides the town directive on conflicting
instructions.

**Implementation:**
- Loader: `internal/config/directives.go` → `LoadRoleDirective(role, townRoot, rigName) string`
- Integration: `internal/cmd/prime_output.go` → `outputRoleDirectives(ctx RoleContext)`
- Called in the `gt prime` pipeline after `outputPrimeContext()`

### Level 2: Formula Overlays

Per-formula, per-step overrides at rig or town scope. CSS-like step
modifications applied post-parse before rendering at prime time.

**File layout:**

```
~/gt/formula-overlays/<formula>.toml        # Town-level
~/gt/<rig>/formula-overlays/<formula>.toml  # Rig-level (full precedence)
```

**Precedence:** Rig-level overlays **fully replace** town-level overlays (not
merged). If a rig overlay exists, the town overlay is completely ignored. This
prevents conflicting step modifications from merging unpredictably.

**Implementation:**
- Loader: `internal/formula/overlay.go` → `LoadFormulaOverlay(formulaName, townRoot, rigName) (*FormulaOverlay, error)`
- Applier: `internal/formula/overlay.go` → `ApplyOverlays(f *Formula, overlay *FormulaOverlay) []string`
- Integration: `internal/cmd/prime_molecule.go` → `applyFormulaOverlays()` called in `showFormulaStepsFull()`

## TOML Format (Overlays)

Formula overlays use TOML with a `[[step-overrides]]` array:

```toml
[[step-overrides]]
step_id = "submit-review"
mode = "replace"
description = """
Report your review findings back to the conversation instead of posting
to GitHub. Format as a structured summary with grade and findings."""

[[step-overrides]]
step_id = "build"
mode = "append"
description = """
Also run integration tests: npm run test:integration"""

[[step-overrides]]
step_id = "deprecated-step"
mode = "skip"
```

### Override Modes

| Mode | Effect | `description` Required |
|------|--------|----------------------|
| `replace` | Swap the step description entirely | Yes |
| `append` | Add text after the existing step description (newline-separated) | Yes |
| `skip` | Remove the step from the formula | No |

### Skip Mode Dependency Handling

When a step is skipped, steps that depended on it inherit its `needs`
(dependencies). This preserves the formula DAG integrity. For example, if
step B depends on step A, and step A is skipped, then step B inherits
whatever step A depended on.

### Validation Rules

- `step_id` is required on every override
- `mode` must be one of: `replace`, `append`, `skip`
- Malformed TOML returns an error during load
- Step IDs that don't match any formula step generate warnings (stale overrides)

## Directive Format (Markdown)

Role directives are plain Markdown files. There is no special syntax — the
content is injected verbatim into the agent's prime output with an authority
header.

```markdown
## PR Review Policy

Do NOT post review comments directly to GitHub via `gh pr review`.
Instead, report your findings back in the conversation as a structured summary.

## Code Style

Always run `npm run lint --fix` before committing.
Follow existing patterns in the codebase.
```

## CLI Commands

> **Note:** CLI commands are being added in gt-3kg.5. The interface below
> reflects the planned design.

### Directive Commands

```bash
gt directive show <role> [--rig <rig>]    # Show active directive with source
gt directive edit <role> [--rig <rig>]    # Open in editor (creates file if needed)
gt directive list                         # List all directive files
```

### Overlay Commands

```bash
gt formula overlay show <formula> [--rig <rig>]   # Show active overlay with source
gt formula overlay edit <formula> [--rig <rig>]   # Open in editor (creates file if needed)
gt formula overlay list                           # List all overlay files
```

The `edit` commands create the directory and file if they don't exist (following
the `gt hooks override` precedent). The `show` commands display the resolved
content with source annotation (town vs rig).

## gt doctor Integration

The `overlay-health` doctor check validates formula overlays:

```bash
gt doctor                    # Runs all checks including overlay health
```

**What it checks:**
- Scans all town-level and rig-level overlay TOML files
- Parses each overlay and loads the corresponding embedded formula
- Validates every `step_id` exists in the current formula version
- Reports stale step IDs (formula was updated, overlay wasn't)

**Results:**
- **OK:** "N overlay(s) healthy" or "no overlay files found"
- **Warning:** Stale step IDs found (auto-fixable)
- **Error:** Malformed TOML (requires manual fix)

**Auto-fix:**

```bash
gt doctor --fix              # Removes stale step-override entries
```

The fix removes step overrides that reference non-existent step IDs. If all
overrides in a file are stale, the entire file is removed. Malformed TOML
is left untouched.

**Implementation:** `internal/doctor/overlay_health_check.go`

## Worked Example: The PR Review Override

This is the motivating use case that drove the feature.

### The Problem

The `mol-polecat-work` formula has a step called `submit-review` that tells
polecats to post review results to GitHub using `gh pr review --comment`.
In the gastown rig, the operator wants polecats to report findings back in
conversation instead.

### The Solution

**Step 1: Create a rig-level formula overlay.**

```bash
mkdir -p ~/gt/gastown/formula-overlays
```

Create `~/gt/gastown/formula-overlays/mol-polecat-work.toml`:

```toml
[[step-overrides]]
step_id = "submit-review"
mode = "replace"
description = """
Report your review findings back to the conversation. Format as:

## Review: <file or component>
**Grade:** A-F
**Findings:**
- CRITICAL: ...
- MAJOR: ...
- MINOR: ...

Do NOT post comments to GitHub via gh pr review."""
```

**Step 2: Verify with gt doctor.**

```bash
gt doctor
# ✓ overlay-health: 1 overlay(s) healthy
```

**Step 3: Test with gt prime.**

```bash
gt prime --explain
# Shows: "Formula overlay: applying 1 override(s) for mol-polecat-work (rig=gastown)"
```

Now any polecat in the gastown rig running `mol-polecat-work` will see the
replacement step instead of the original "post to GitHub" instruction.

### What If the Formula Changes?

If a future `gt` release renames `submit-review` to `post-results`, the
overlay's `step_id` becomes stale. On next `gt doctor` run:

```
⚠ overlay-health: stale step IDs in gastown/formula-overlays/mol-polecat-work.toml:
  - step_id "submit-review" not found in formula mol-polecat-work
```

Running `gt doctor --fix` removes the stale override. The operator then
creates a new override targeting `post-results`.

## Design Rationale

### Why Two Levels, Not One?

Directives and overlays solve different problems at different granularities:

| Aspect | Directives | Overlays |
|--------|-----------|----------|
| Scope | Entire role behavior | Individual formula steps |
| Granularity | Broad policy | Surgical modification |
| Format | Markdown (prose) | TOML (structured) |
| Precedence | Concatenate (additive) | Replace (exclusive) |
| Example | "Never post to GitHub" | "In step X, do Y instead" |

A role directive saying "never post to GitHub" applies everywhere — any formula,
any step. An overlay targeting `submit-review` in `mol-polecat-work` applies
only to that specific step in that specific formula.

Both are needed: directives for broad guardrails, overlays for surgical fixes.

### Why Not Modify Formulas Directly?

Formulas are embedded in the Go binary. Modifying them requires rebuilding
and redeploying. Directives and overlays are external config files that take
effect immediately on the next `gt prime`.

### Architectural Harmony

- **Fits gt prime pipeline:** Role template → directives → context → handoff → formula
- **Follows hooks override precedent:** `~/.gt/hooks-overrides/<target>.json`
- **Extends property layers:** Rig > town > system precedence
- **ZFC-compliant:** Go transports the content, agents interpret the instructions
- **Rendered through gt:** `bd` doesn't render formulas, so `gt prime` injects overlays

### Dissonance to Manage

- **Conflicting workflow instructions:** Directive says "don't X", formula says
  "do X" → mitigated with clear authority framing at injection. A directive that
  changes installed command ownership is invalid rather than an override.
- **Unstable step IDs:** Formula steps are not a stable API; step IDs can change
  across versions → `gt doctor` warns about stale overlays
- **Discoverability:** `gt prime --explain` shows active directives/overlays
  with source annotations

## File Reference

| File | Purpose |
|------|---------|
| `internal/config/directives.go` | Directive loader (`LoadRoleDirective`) |
| `internal/config/directives_test.go` | Directive tests |
| `internal/formula/overlay.go` | Overlay loader and applier |
| `internal/formula/overlay_test.go` | Overlay tests |
| `internal/cmd/prime_output.go` | `outputRoleDirectives()` integration |
| `internal/cmd/prime_molecule.go` | `applyFormulaOverlays()` integration |
| `internal/doctor/overlay_health_check.go` | Doctor check and auto-fix |
