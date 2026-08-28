---
name: go-development
description: go, development, building, reviewing, testing, simplifying, detected, go.mod, go.work
license: MIT
metadata:
  version: 1.0.0
---

# Go Development

Follow the active project's Go version, module boundaries, public APIs, and
documented commands.

## Workflow

1. Read `go.mod`, `go.work` when present, and the project's instructions.
2. Keep packages cohesive, dependencies directed, exported APIs documented, and
   errors wrapped with useful context while preserving `errors.Is`/`errors.As`.
3. Pass `context.Context` explicitly across cancellable I/O boundaries; never
   store it in long-lived structs.
4. Prefer simple synchronous code. Add goroutines only with explicit ownership,
   cancellation, bounded lifetime, and race-safe shared state.
5. Format changed Go files and run the project's canonical vet, static-analysis,
   test, race, and build commands as applicable.

## Critical rules

- Do not hide errors, leak goroutines, copy mutexes, or introduce package cycles.
- Do not change exported behavior during cleanup without explicit authorization.
- Treat generated files as outputs and edit their declared source instead.
