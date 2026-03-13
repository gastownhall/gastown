# Gas Town Patches — Panza Local Patches

This document describes all local patches applied on top of upstream Gas Town
(`origin/main`). Use it when merging upstream to ensure patches are re-applied.

**Last updated:** 2026-03-12
**Upstream HEAD:** `3bfb3b71` (fix: spider SQL compatibility with Dolt only_full_group_by mode)
**Patch surface:** 4 files, +89/-52 lines

---

## Quick Reference

| Patch | File(s) | Description |
|-------|---------|-------------|
| LOCAL-002a | `internal/web/api.go` | SSE force refresh for idle dashboards |
| LOCAL-002b | `internal/web/fetcher.go` | Heartbeat JSON field name mismatch |
| LOCAL-004a | `internal/cmd/rig_dock.go` | Wisp write on dock/undock |
| LOCAL-004b | `internal/cmd/rig_helpers.go` | Fail-safe: treat as docked when can't verify |

---

## Patches Previously Carried, Now Upstream

These were local patches that have since landed on `origin/main`. They were
removed during the 2026-03-12 cleanup when upstream was pulled to `3bfb3b71`.

| Former patch | Upstream commit | PR |
|---|---|---|
| LOCAL-001: Doctor skip redirect rigs | `db851a59` | - |
| LOCAL-003: Cross-DB convoy resolution | `aaa46701` | [#2625](https://github.com/steveyegge/gastown/pull/2625) |
| LOCAL-004 (daemon part): getRigBeadsPrefix | `98b748d8` | - |
| LOCAL-004 (daemon fail-safe): Dolt unavailable | `cf3bdbee` | [#2600](https://github.com/steveyegge/gastown/pull/2600) |
| beads.go cross-rig Close/Update routing | upstream `beads.ResolveBeadsDirForID()` | [#2402](https://github.com/steveyegge/gastown/pull/2402) |

---

## LOCAL-002a: SSE Force Refresh (Dashboard)

**Problem:** When SSE is connected, htmx's `every 30s` polling trigger is
disabled. If the town is idle (dashboard hash unchanged), the page never
refreshes — status goes stale silently.

**Fix:** Add a 30-second force-refresh ticker in `handleSSE()` alongside
the existing hash-change detection. Sends a `dashboard-update` event
unconditionally every 30s so the page stays current.

**File:** `internal/web/api.go` (+14 lines)

**Upstream candidate:** Yes — affects any Gas Town dashboard using SSE.

---

## LOCAL-002b: Deacon Heartbeat JSON Field Name

**Problem:** `FetchHealth()` expects JSON field `last_heartbeat` but the
Deacon writes `timestamp`. Heartbeat time is always zero on the dashboard.

**Fix:** Change JSON struct tag from `json:"last_heartbeat"` to
`json:"timestamp"`.

**File:** `internal/web/fetcher.go` (+1/-1)

**Upstream candidate:** Yes — one-liner bug fix.

---

## LOCAL-004a: Wisp Write on Dock/Undock

**Problem:** `gt rig dock` sets a bead label (`status:docked`) but never
writes wisp config. The daemon's fast-path check reads wisp first — if
wisp is empty, it falls through to bead lookup which may also fail (Dolt
down, rig bead missing). Result: docked rigs get restarted every heartbeat.

`gt rig undock` bails early if the rig bead doesn't exist, leaving stale
wisp state behind.

**Fix:**
- **dock:** After bead label, also `wisp.Set("status", "docked")`. If bead
  update fails (broken Dolt), log warning and continue — wisp alone is
  sufficient for daemon fast-path.
- **undock:** Always clear wisp status independently of bead state. Both
  layers managed independently for resilience.

**File:** `internal/cmd/rig_dock.go` (+50/-33)

**Upstream candidate:** Yes — fixes the asymmetry noted in upstream's own
`IsRigParkedOrDocked` comment: "Docked state is bead-label only because
`gt rig dock` never writes to wisp."

---

## LOCAL-004b: Fail-Safe Docked When Can't Verify

**Problem:** `hasRigBeadLabel()` and `IsRigParkedOrDocked()` return `false`
(not docked) when prefix lookup or bead query fails. This means rigs whose
state can't be verified are treated as operational — agents get spawned on
rigs that may be intentionally docked but have a broken Dolt or missing bead.

**Fix:** Fail closed instead of open:
- No beads prefix in `rigs.json` → return `true, "docked (no beads prefix)"`
- Bead lookup fails (Dolt down, bead missing) → return `true, "docked (bead lookup failed)"`

Also inlines the `rigs.json` lookup instead of using the `rigBeadsPrefix()`
helper that falls back to `config.json` (which doesn't exist for most rigs).

**File:** `internal/cmd/rig_helpers.go` (+24/-18)

**Upstream candidate:** Yes — defensive improvement. The daemon path already
has fail-safe via `cf3bdbee`, but the CLI paths (`gt sling`, `gt convoy`)
used by `IsRigParkedOrDocked` do not.

---

## Merge Workflow

```bash
# 1. Save local patches
git diff > /tmp/panza-patches.diff

# 2. Pull upstream
git stash
git pull origin main --ff-only  # or reset --hard if diverged
git stash pop                   # will likely conflict

# 3. If stash pop conflicts, re-apply from saved diff
git checkout -- .
git apply /tmp/panza-patches.diff

# 4. Build check
cd /path/to/gastown-patched
go build ./...

# 5. Update this file with new upstream HEAD
```

---

## Known Gaps (not patched, workarounds in place)

**Daemon not auto-started:** `gt rig undock` / `gt sling` don't auto-start
the daemon. Workaround: Mayor CLAUDE.md instructs checking `gt daemon status`
at startup.

**hq-prefixed beads lack rig routing for convoy stage:** `gt convoy stage`
can't map `hq-*` beads to target rigs. Workaround: add `rig:<name>` label
to task beads.
