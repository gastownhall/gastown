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
triggers `gt estop` to freeze all agents. Periodically re-checks and
runs `gt thaw` when the rate limit clears.

This is a **shell-only plugin** — no LLM calls. It runs as a daemon
plugin on a 3-minute cooldown gate.

## How It Works

1. Send a minimal API probe (1 token to haiku — cheapest possible check)
2. If 429 → `gt estop -r "API rate limited"` (if not already active)
3. If 200 and estop is active with rate-limit reason → `gt thaw`
4. Record result as tracking wisp

The probe costs ~$0.0001 per check. At 3-minute intervals, ~$0.05/day.

## Behavior

| API Status | ESTOP Active? | Action |
|------------|--------------|--------|
| 429 | No | `gt estop -r "API rate limited"` |
| 429 | Yes | No-op (already frozen) |
| 200 | Yes (rate-limit) | `gt thaw` |
| 200 | Yes (other reason) | No-op (manual estop) |
| 200 | No | No-op (healthy) |
| Error | Any | Log warning, skip |

## Configuration

The plugin uses these environment variables:
- `ANTHROPIC_API_KEY` — required for the probe request
- `GT_ROOT` — town root for estop/thaw commands

No additional configuration needed. The 3-minute cooldown gate prevents
rapid estop/thaw cycling.
