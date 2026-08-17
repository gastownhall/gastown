# Snapshot: cheap Pi models for Gas Town roles

**Date:** 2026-08-17
**Status:** Point-in-time eval. Prices, free-tier availability, and OpenRouter
data-policy routing change without notice. Re-run before treating any ranking
as current.

This is not a recommendation to change built-in presets. It records one
throwaway Town run so a later session can see what failed and why.

## Setup

- Worktree: `/tmp/gt-pi-model-eval` at `origin/main` (`0259f8ac`)
- Isolated Town: `/tmp/gt-pi-eval-town` (`--name pi-eval --git --dolt-port 3317`)
- Rig: real GitHub project `https://github.com/jonbaldie/tui-reader.git`
- Runtime: Pi (`pi --print --mode text --no-session --approve`)
- Shared Bead-sized task: add vim-style `j`/`k` page navigation in
  `internal/tui/keys.go`, update `docs/controls.md` and `README.md`, add
  adversarial tests modeled on the existing `l`/`h` cases, run
  `go test ./internal/tui/ -count=1`
- Cleanup: Town, Dolt on `:3317`, eval copies, and worktree deleted after the
  run

The same prompt ran against isolated clones. Models did not commit or push.

## What worked

Independent `go test ./internal/tui/ -count=1` after each run. Times are
`/usr/bin/time -p` wall clock for the whole Pi session.

| Provider | Model | Price (in/out) | Time | Tests | Diff quality |
|---|---|---|---|---|---|
| Codex | `gpt-5.6-luna` | $0.20 / $1.20 | 37s | pass | Four files. Tests inserted above the `l`/`h` section headers. Docs columns slightly misaligned. |
| xAI | `grok-build-0.1` | $1 / $2 | 70s | pass | Four files. Cleanest placement and docs alignment. |
| OpenRouter | `qwen/qwen3.7-flash` | $0.03 / $0.13 | 31s | pass | Four files. Same shape as Grok. Fastest successful run. |
| OpenRouter | `z-ai/glm-4.7-flash` | $0.06 / $0.40 | 94s | pass | Four files. Correct, slower, chatty. |
| OpenRouter | `google/gemma-4-31b-it` (paid) | $0.10 / $0.34 | 83s | pass | Four files. Free SKU of the same model did not start. |
| OpenRouter | `openai/gpt-oss-20b` (paid) | $0.03 / $0.13 | 74s | pass | Added `j`/`k`, then re-indented all of `adversarial_test.go` (435-line diff). |

## What did not start

OpenRouter `:free` IDs on this account mostly failed before any edit.

| Model | Failure |
|---|---|
| `google/gemma-4-31b-it:free` | 404: no endpoints matching guardrail / data-policy restrictions |
| `nvidia/nemotron-3.5-lightning:free` | same 404 |
| `poolside/laguna-xs-2.1:free` | same 404 |
| `nvidia/nemotron-nano-9b-v2:free` | same 404 |
| `cohere/north-mini-code:free` | `Provider finish_reason: error` (twice; with thinking `off` and `low`) |
| `openai/gpt-oss-20b:free` | 400 if `--thinking off` ("reasoning is mandatory"); with thinking `low`, empty reply and no file changes |

A role assigned one of these models will fail at session start, before the Hook
runs.

## Ranking for this snapshot

1. **Codex `gpt-5.6-luna`** — default cheap Polecat on this date. Fast, scoped,
   already used in [Pi runtime](../PI.md).
2. **OpenRouter `qwen/qwen3.7-flash`** — cheapest clean patch if the Polecat
   must use OpenRouter.
3. **xAI `grok-build-0.1`** — nicest diff; not the cheapest.
4. **GLM-4.7-flash / paid Gemma-4-31B** — correct; no reason to prefer them.
5. **`openai/gpt-oss-20b`** — task complete, repo dirtied. Skip.
6. **Any `:free` OpenRouter model on this account** — dead until privacy /
   data-policy settings change.

Mayor, Deacon, Witness, and Refinery need the model to start. Do not assign a
`:free` profile to those roles from this snapshot.

## Re-run

The eval is gone. To repeat it:

1. Worktree off current `origin/main`.
2. `gt install` an isolated Town on a free Dolt port.
3. `gt rig add` a small real GitHub repo.
4. Clone the Mayor workspace once per model.
5. Feed the same prompt through `pi --provider … --model … --print --no-session --approve`.
6. Score the independent `git diff` and `go test`, then delete the Town.
