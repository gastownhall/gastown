# DRAFT — upstream issue for steveyegge/gastown (and notes for steveyegge/beads)

> Status: DRAFT. Do **not** post live. Written by gastown/nux for gt-7cy.
> Two related asks below: (1) bd daemon/socket mode (the real latency fix, in `bd`),
> (2) an SDK read-only / skip-DDL open mode (unblocks in-process reads from embedders like gastown).

---

## Title

bd daemon/socket mode: eliminate ~1s cold-start per **write** invocation

## Summary

Every `bd` invocation is a fresh OS process. For **writes** (`bd create`, `bd close`,
`bd update`, `bd comment`), gastown measures ~1050ms wall-clock per call. Profiling
attributes ~6ms to the Dolt query and ~73ms to `dolt_commit`; the remaining ~950ms is
process cold-start (≈190MB Go binary, runtime init, cobra parse, Dolt TCP connect +
MySQL handshake, and per-invocation schema/migration checks).

Gas Town drives `bd` programmatically from long-lived agents. Write-heavy flows
(creating molecules, closing work, slinging beads across rigs) pay this cold-start on
every mutation, which dominates agent latency.

A persistent daemon/socket mode — keeping one warm process with an open Dolt
connection and serving requests over `bd.sock` — would amortize cold-start across calls.

### Note: the skeleton already exists in gastown but is unimplemented in `bd`

- `bd.sock`, `daemon.lock`, `daemon.pid`, `daemon.log` appear in gastown's runtime-file
  list (`internal/beads/beads_redirect.go`).
- `BEADS_DOLT_SERVER_SOCKET`, `BEADS_DOLT_SERVER_MODE` are recognized env vars
  (`internal/beads/database.go`).
- But `bd daemon` / `bd serve` / `bd socket` subcommands do **not** exist in the
  binary (verified against bd v1.0.5).

### Request

A `bd daemon` (or `bd serve`) subcommand that:
1. Holds one process with a warm Dolt connection.
2. Listens on `bd.sock` (Unix socket; local-only is fine — remote agents already use TCP Dolt).
3. Accepts the existing command surface and returns the same JSON.
4. Cleans up `bd.sock` on exit; refuses/steals stale sockets safely.
5. Flushes pending `dolt_commit` on SIGTERM/SIGHUP.

### Measured impact (gastown, GT2: 61GB RAM / 16 cores)

| Path           | Now      | With daemon (projected) |
|----------------|----------|--------------------------|
| `bd create`    | ~1050ms  | ~80ms                    |
| read ops       | ~60ms    | ~60ms (already fast)     |

> Correction to an earlier internal estimate: **reads are already ~60ms** on a warm
> binary (`bd list`/`bd show` measured 52–68ms; `bd version` ~30ms). The cold-start
> tax that matters is on the **write** path. Daemon mode is a write-latency fix.

---

## Companion ask — SDK: read-only / skip-DDL open mode (`github.com/steveyegge/beads`)

Embedding apps (gastown) can already bypass the `bd` subprocess for reads by calling
`beadsdk.OpenFromConfig(ctx, beadsDir)` and querying Dolt in-process. In practice this
is blocked by two SDK behaviors:

1. **`OpenFromConfig` performs DDL on open.** It runs `CREATE … ready_issues` /
   `blocked_issues` views (and wisp tables) on every open. Against a database whose
   schema was migrated by a **newer** `bd` than the SDK the embedder is compiled
   against, this fails hard. Observed against production:

   ```
   failed to initialize schema: failed to create ready_issues view:
   Error 1105 (HY000): table "d" does not have column "depends_on_id"
   ```

   Root cause: bd v1.0.5 migrated the `dependencies` table to
   `depends_on_issue_id` / `depends_on_wisp_id` / `depends_on_external`, but the
   SDK's view DDL still references `depends_on_id`. An embedder pinned to an older
   SDK can therefore never open a store that a newer `bd` has touched — even for a
   pure read.

2. **No read-only open.** There is no documented way to open a connection that skips
   view/table creation and forward-schema checks (analogous to the binary's
   `--ignore-schema-skew` / `--readonly` flags) for callers that only intend to read.

### Request

- An `OpenReadOnly(ctx, beadsDir)` (or an option on `OpenFromConfig`) that:
  - opens a connection **without** running DDL/migrations, and
  - tolerates forward schema skew for read queries (like `--ignore-schema-skew`).
- This lets embedders do fast in-process reads without lockstep SDK/binary versioning.

### Why this matters

Gastown wants to route read-only bd calls through the in-process SDK to drop the
per-call subprocess spawn. The mechanism exists and works when versions match, but the
DDL-on-open behavior makes it fragile precisely in the common case (binary ahead of
embedded SDK). A read-only open closes that gap.
