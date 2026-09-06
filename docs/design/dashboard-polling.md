# Dashboard polling and subprocess ownership

The dashboard shares one change-detection attempt across its SSE clients for a two-second interval. Status, hooks, and inbox outputs are hashed in a stable order. Failed attempts also receive the interval: ten clients encountering unavailable data must not turn one failed attempt into thirty commands. Only a complete set of outputs replaces the previous hash. A disconnected waiter returns promptly, without cancelling another client's current attempt.

This extends the polling coalescing in PR #4811. PR #4791 reduces redundant `mail read` mutations; it does not address the `mail inbox` fanout seen in incidents gt-8i2 and hq-wisp-nfmp6.

On Unix, dashboard subprocesses have a process-group cancellation hook and a bounded pipe wait. Synchronous read commands receive the internal `GT_MANAGED_READ=1` environment marker. Nested Gas Town command launchers then inherit the existing group instead of detaching. Without this inheritance, `gt mail inbox` can launch several detached `bd` processes which survive cancellation of the outer command and keep contending for the HQ schema initialization lock. Ordinary CLI commands retain their existing group policy. The marker is applied only to the explicitly classified dashboard reads, not mutation commands.

The CLI entry point supplies its own executable path to the dashboard API and canonical convoy reader. Library constructors retain an explicit path seam and never discover the test executable automatically. This prevents an adopted dashboard from silently executing an older installed `gt` whose nested subprocess behavior differs from the serving build.

Canonical convoy hydration, panel data, token protections, and the exact-origin/source readiness contract remain unchanged. Polling does not shorten the consumer's twelve-second acceptance deadline. Unix cancellation is exercised with the real mailbox fanout and fake `bd` executables; these tests require no live database. Windows keeps the existing process helper behavior and is not covered by the Unix process-group fixture.

The managed-read policy covers the actual Ready, Crew, Polecat, Mail Inbox, Status, Hooks, Rigs, and Convoy read calls. The Ready regression runs the real `/api/ready` handler and CLI `runReady`, including parallel town and rig `Beads.Ready` calls against fake executables. `mail read` is excluded because it marks delivery state; PR #4791 addresses its redundant mutations.
