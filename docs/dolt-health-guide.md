# Dolt Health Guide

This guide covers evidence capture for Dolt outages and Gas Town behavior
mismatches that look like Dolt trouble.

## When To Use This

Use this checklist when any of these happen:

- `bd` commands hang, time out, or return unexpected empty results.
- `gt dolt status` reports unhealthy server state, high latency, stale PIDs, or
  orphan test databases.
- A Gas Town command behaves differently from its documented or expected behavior
  and Dolt is part of the control path.

Do not restart Dolt before collecting diagnostics. A blind restart can destroy the
state needed to explain the incident.

## Immediate Diagnostics

Capture non-fatal diagnostics first:

```bash
gt dolt dump 2>&1 | tee /tmp/dolt-hang-$(date +%s).log
gt dolt status 2>&1 | tee /tmp/dolt-status-$(date +%s).log
```

Then escalate with the evidence path:

```bash
gt escalate -s HIGH "Dolt: <symptom>" -m "Evidence: /tmp/dolt-status-..."
```

## RCA Capture Checklist

Attach this checklist to the escalation body, the follow-up bead, or the war-room
entry. Use `N/A` only when a field truly does not apply to a non-Dolt behavior
mismatch.

```markdown
### RCA Capture

- Trigger command:
- Concurrent GT processes:
- Dolt pid/status:
- Stale pid status:
- Orphan test server status:
- Suspected GT code path:
- Expected behavior:
- Observed behavior:
- Evidence source:
- Likely root cause:
- Smallest fix direction:
```

## Field Notes

- **Trigger command**: the exact command or agent action that exposed the issue.
- **Concurrent GT processes**: active mayor, witness, refinery, polecat, dog, or
  test processes that may share Dolt.
- **Dolt pid/status**: server PID, health, latency, and port state from
  `gt dolt status` or `gt dolt dump`.
- **Stale pid status**: whether pid files point at missing or unrelated processes.
- **Orphan test server status**: orphan database or test-server count, especially
  `testdb_*`, `beads_t*`, `beads_pt*`, or `doctest_*`.
- **Suspected GT code path**: command, package, plugin, or template that most
  likely drove the behavior.
- **Expected behavior**: what the command or workflow should have done.
- **Observed behavior**: what actually happened, including errors and timings.
- **Evidence source**: log files, command output, bead IDs, session IDs, or branch names.
- **Likely root cause**: current best explanation, clearly marked if uncertain.
- **Smallest fix direction**: the least invasive code, docs, or operations change
  that would prevent repeat incidents.

## Simulated Incident Smoke Check

For documentation-only RCA work, use this smoke check to verify the checklist is
available and wired into the escalation path:

```bash
test -f docs/dolt-health-guide.md
grep -n "Trigger command" docs/dolt-health-guide.md
grep -n "RCA capture checklist" internal/templates/townroot/claude.md docs/design/escalation.md
```
