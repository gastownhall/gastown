+++
name = "rate-limit-watchdog"
description = "Auto-estop on API rate limit, auto-thaw when clear — no LLM needed"
version = 1

[gate]
type = "cooldown"
duration = "3m"

[tracking]
labels = ["plugin:rate-limit-watchdog", "category:safety"]
digest = true

[execution]
timeout = "30s"
notify_on_failure = true
severity = "high"
+++

# Rate Limit Watchdog

Monitors the Anthropic API for rate limiting (HTTP 429). When detected,
attempts credential rotation via `gt quota rotate` first. Only triggers
`gt estop` as a last resort when no accounts are available. Periodically
re-checks and runs `gt thaw` when the rate limit clears.

This is a **shell-only plugin** — no LLM calls. It runs as a daemon
plugin on a 3-minute cooldown gate.

## How It Works

1. Send a minimal API probe (1 token to haiku — cheapest possible check)
2. If 429 → try `gt quota rotate` to swap blocked sessions to fresh accounts
3. If rotation succeeds → done (no estop needed)
4. If rotation fails (no available accounts) → `gt estop -r "API rate limited"`
5. If 200 and estop is active with rate-limit reason → `gt thaw`

The probe costs ~$0.0001 per check. At 3-minute intervals, ~$0.05/day.

## Behavior

| API Status | ESTOP Active? | Action |
|------------|--------------|--------|
| 429 | Any | Try `gt quota rotate` first |
| 429 (rotation succeeded) | Any | No estop — sessions rotated |
| 429 (rotation failed) | No | `gt estop -r "API rate limited"` |
| 429 (rotation failed) | Yes | No-op (already frozen) |
| 200 | Yes (rate-limit) | `gt thaw` |
| 200 | Yes (other reason) | No-op (manual estop) |
| 200 | No | No-op (healthy) |
| Error | Any | Log warning, skip |

## Configuration

The plugin uses these environment variables:
- `ANTHROPIC_API_KEY` — required for the probe request
- `GT_ROOT` — town root for estop/thaw commands

Requires at least 2 accounts configured (`gt account add`) for rotation
to work. With only 1 account, the plugin falls back to estop on 429.

The 3-minute cooldown gate prevents rapid rotation/estop cycling.
