# Trusted dashboard bead navigation (v1)

Run the Control Center from an initialized Gas Town workspace:

```sh
gt dashboard --port 8081 --embed-parent-origin http://127.0.0.1:3000
```

Use the actual Canvas origin instead of the example port. The flag accepts one
literal HTTPS origin or loopback HTTP origin, with no path (including trailing
slash), credentials, query, fragment, wildcard, or explicit default port.
Setup mode does not support this integration.

The Canvas iframe loads `http://127.0.0.1:8081/?embed=1`. The server configuration
chooses the trusted parent; query parameters cannot override it. Without the
flag, `?embed=1` is rejected. Without the query or outside an iframe, clicking
an issue retains the standalone detail view. Embedded documents use a CSP
`frame-ancestors` restricted to the configured parent. Other dashboard responses
allow same-origin framing only. Reverse proxies must preserve these headers;
an existing stricter proxy policy may prevent embedding.

A user clicking a bead sends exactly this object to the configured origin:

```json
{"type":"gastown:focus-bead","version":1,"beadId":"inktree-3r67i"}
```

On initialization the opted-in producer also sends exactly
`{"type":"gastown:ready","version":1}` once. Canvas must validate its origin and
source using the same checks as bead focus. A frame `load` event alone does not
prove availability (browser error pages also load); the consumer can use a
bounded readiness timeout to present its unavailable state. This confirms
producer initialization, not data freshness or backend health.

Issue rows, dependency links, convoy IDs and tracked-issue IDs, crew/polecat work IDs and
hook IDs use their actual bead identifiers. PR URLs remain PR links: this
dashboard has no authoritative PR-to-bead mapping and does not invent one.
Cross-rig dependency IDs wrapped as `external:prefix:id` emit the underlying ID.
There are no incoming dashboard messages and no action, URL, authentication
token, title, or status in the payload. Existing API token and CORS behavior
remain in force, including for a trusted parent.

The Canvas consumer must first check both `event.origin` against the configured
dashboard origin and `event.source === iframe.contentWindow`. Then validate
the exact type/version and a bounded bead ID string. Resolve the real bead
through trusted routing before selecting it; reveal filters and focus the exact
bubble. Unknown IDs must produce a visible error. Never derive dashboard URLs
or authorization from message data. No acknowledgement is required in v1.

Validation:

```sh
go test ./internal/web -run 'TestEmbed|TestDashboardEmbed|TestValidateEmbed'
node --test internal/web/testdata/embed.test.cjs
go test -tags=embedbrowser ./internal/web -run TestEmbedBrowser -v
```

The browser suite requires Chrome and checks production dashboard rendering,
cross-origin delivery, standalone dependency navigation and blocked framing.
On macOS, use the ICU compiler/linker flags configured by the Makefile.

## Convoy summary hydration

Dashboard summaries use `gt convoy list --status=open --json`, which resolves raw cross-database tracking edges and hydrates their issue IDs through prefix routing. This avoids the local dependency join that could show an existing cross-rig convoy as `0/0`. The dashboard preserves unresolved tracked IDs in the denominator and displays an Unknown badge; blocked or unknown issues are not counted as ready. Missing tracked data produces a fetch error. The reader runs from the town root, clears caller database-directory overrides, and has a process-group timeout.
