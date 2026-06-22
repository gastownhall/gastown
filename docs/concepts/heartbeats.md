# Heartbeats

Gas Town has **three distinct heartbeat stores**. They have different readers
and thresholds, so Deacon heartbeat commands refresh the Deacon-specific stores
together to avoid false "stuck agent" escalations (see hq-qxl9: a Deacon
refreshed its session heartbeat while the file store aged past threshold).

## The three stores

### 1. Deacon heartbeat file — `<townRoot>/deacon/heartbeat.json`

- **Written by:** `gt deacon heartbeat [action]` and `gt heartbeat` when
  `GT_ROLE=deacon` → `deacon.Touch()` / `deacon.TouchWithAction()`
  (`internal/deacon/heartbeat.go`).
- **Read by:** the stuck-agent-dog plugin (parses the JSON `timestamp`, falling
  back to mtime for malformed legacy files, and records stale age as NOTICE-only)
  and the Go daemon (`deacon.ReadHeartbeat`; thresholds 5m stale / 20m
  very-stale → poke/restart).
- **Also touches:** the legacy `deacon/.deacon-heartbeat` mtime file for old
  shell scripts.

### 2. Session heartbeat (per-session state store)

- **Written by:** `gt heartbeat [--state=working|idle|exiting|stuck]` →
  `polecat.TouchSessionHeartbeatWithState()`. Requires `GT_SESSION`.
- **Read by:** the Witness, which reads the self-reported state instead of
  inferring liveness from timers (ZFC: gt-3vr5). This is the store polecats
  refresh.

### 3. Agent-bead label — `heartbeat:<EPOCH>` on the agent bead (e.g. `hq-deacon`)

- **Written by:** `gt mol await-signal` on each timeout/signal wake
  (`updateAgentHeartbeat` in `internal/cmd/molecule_await_signal.go`). A
  label rewrite is used because `bd agent heartbeat` was never shipped
  (steveyegge/beads#2828). Deacon heartbeat commands also sync this label when
  it is older than half of the stale threshold.
- **Read by:** Witness second-order monitoring ("who watches the watchers"):
  Witnesses check the Deacon's bead activity and alert the Mayor if it looks
  unresponsive (>5 minutes per the patrol formula).
- **Gotcha:** a session that never reaches `await-signal` (handoff churn,
  session limits, one very long patrol turn) leaves this label stale for
  hours even though the agent is healthy.

## Rules of thumb

- **Deacon sessions:** `gt deacon heartbeat` refreshes the Deacon file and
  throttled bead label. `gt heartbeat` also refreshes the session store and,
  when `GT_ROLE=deacon`, uses the same Deacon file/label sync path.
- **Polecats / Witness / Refinery:** `gt heartbeat` (session store) is the
  one that matters.
- **Monitoring scripts:** never declare an agent stuck from a single store.
  A live session with a stale store is *heartbeat-write divergence*, not a stuck
  agent. The stuck-agent-dog plugin treats Deacon heartbeat staleness as
  NOTICE-only and escalates only dead Deacon session/runtime evidence; the Go
  daemon owns heartbeat nudge/restart.
