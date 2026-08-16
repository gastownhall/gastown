# gt-proxy-server and gt-proxy-client

The proxy server and client implement sandboxed execution for polecats: containers
can call `gt` and `bd` commands, and push/pull git repositories, over an encrypted
and mutually authenticated channel — without direct access to the host filesystem,
credentials, or GitHub.

## Overview

When a polecat runs inside a container or isolated execution environment (such as
[Daytona](https://www.daytona.io/)), it still needs to interact with Gas Town's
control plane. Specifically, it needs to:

- Call `gt` and `bd` commands (mail, status, handoff, issue updates, etc.)
- Push its work to the polecat branch in the rig's `.repo.git` bare repository

The proxy solves this by running two small Go binaries:

| Binary | Runs on | Purpose |
|--------|---------|---------|
| `gt-proxy-server` | Host | Accepts mTLS connections; executes `gt`/`bd` and serves git smart-HTTP |
| `gt-proxy-client` | Container | Installed as `gt` and `bd`; forwards calls to the server over mTLS |

```
 Container                          Host
 ─────────────────────              ──────────────────────────────────────────
  gt mail inbox           ──mTLS──► gt-proxy-server ──► exec gt mail inbox
  git push origin/proxy   ──mTLS──► gt-proxy-server ──► git-receive-pack ~/gt/MyRig/.repo.git
```

Both sides authenticate with certificates signed by a single CA that the server
generates and manages.  All traffic is TLS 1.3.

---

## Prerequisites

| Tool | Version | Install |
|------|---------|---------|
| **Go** | 1.21+ | [go.dev](https://go.dev/doc/install) |
| **git** | 2.20+ | `brew install git` / `apt install git` |

The binaries live alongside `gt` in the same module:

```bash
# Build both binaries
CGO_ENABLED=0 go install github.com/jonbaldie/gastown/cmd/gt-proxy-server@main
CGO_ENABLED=0 go install github.com/jonbaldie/gastown/cmd/gt-proxy-client@main
```

---

## gt-proxy-server

### What it does

The server listens on an mTLS port and provides two endpoints:

- **`POST /v1/exec`** — run a `gt` or `bd` subcommand on behalf of a polecat
- **`GET/POST /v1/git/<rig>/...`** — proxy git smart-HTTP for a rig's bare repo

Every client must present a certificate signed by the server's CA.  Only
certificates whose Common Name matches `gt-<rig>-<name>` are accepted (polecat
identity format).

### Starting the server

```bash
gt-proxy-server \
  --listen 0.0.0.0:9876 \
  --ca-dir ~/gt/.runtime/ca \
  --allowed-cmds gt,bd \
  --town-root ~/gt
```

The server generates or loads a CA on first run, then self-issues a server
certificate.  After startup you will see:

```
gt-proxy-server: listening  addr=0.0.0.0:9876  tls=mTLS
```

### CLI flags

| Flag | Default | Description |
|------|---------|-------------|
| `--listen` | `0.0.0.0:9876` | TCP address to listen on |
| `--admin-listen` | `127.0.0.1:9877` | Address for the local admin HTTP server; set to `""` to disable |
| `--ca-dir` | `~/gt/.runtime/ca` | Directory that stores `ca.crt` and `ca.key` |
| `--allowed-cmds` | `gt,bd` | Comma-separated list of binary names containers may invoke |
| `--allowed-subcmds` | *(auto-discovered)* | Semicolon-separated subcommand allowlists per binary, e.g. `gt:prime,hook,done;bd:create,update` |
| `--town-root` | `$GT_TOWN` or `~/gt` | Gas Town root directory; used to locate bare repos |
| `--config` | `~/gt/.runtime/proxy/config.json` | Path to a JSON config file; file values are overridden by explicit CLI flags |

### Environment variables

| Variable | Description |
|----------|-------------|
| `GT_TOWN` | Overrides the town root directory (same as `--town-root`) |

### Allowed commands and subcommands

Only the binary names listed in `--allowed-cmds` can be called via `/v1/exec`.
The default `gt,bd` is appropriate for production.  Entries must be plain names
(no `/` or `\`); path-separator entries are logged and dropped at startup.

Binary paths are resolved once at startup to prevent PATH-hijacking after the
server is running.

If you want to restrict further, pass a subset:

```bash
# Only allow gt; no bd access
gt-proxy-server --allowed-cmds gt
```

Subcommand filtering is enforced on every `/v1/exec` request.  If a command has
an entry in `--allowed-subcmds`, `argv[1]` must appear in that list or the
request is rejected with HTTP 403.  If a command has no entry, all subcommands
are allowed for that command (not recommended for `gt` or `bd`).

The default subcommand allowlists are:

| Binary | Subcommands |
|--------|-------------|
| `gt` | `prime`, `hook`, `done`, `mail`, `nudge`, `mol`, `status`, `handoff`, `version`, `convoy`, `sling` |
| `bd` | `create`, `update`, `close`, `show`, `list`, `ready`, `dep`, `export`, `prime`, `stats`, `blocked`, `doctor` |

#### Auto-discovery via `gt proxy-subcmds`

At startup the server runs `gt proxy-subcmds` to let the installed `gt` binary
declare its own safe subcommand list.  If the command succeeds and produces
non-empty output, that output replaces the built-in default above.  If it fails
or returns empty output, the built-in default is used.

This means upgrading `gt` on the host automatically propagates any newly-allowed
subcommands to the proxy on the next restart, without requiring a manual config
change.  You can always override the result by passing `--allowed-subcmds`
explicitly.

### CA and certificate lifecycle

The CA is a self-signed certificate stored in `--ca-dir`:

```
~/gt/.runtime/ca/
  ca.crt   ← CA certificate (distribute to containers as GT_PROXY_CA)
  ca.key   ← CA private key (keep on host only; never distribute)
```

On first run the CA is created automatically.  You can pre-create it or
rotate it with `gt-proxy-server --ca-dir` pointing at a fresh directory.

Polecat leaf certificates are issued per-polecat and must be generated
separately (see "Issuing polecat certificates" below).

### HTTP timeouts

| Timeout | Value | Notes |
|---------|-------|-------|
| ReadTimeout | 30 s | Entire request headers + body |
| WriteTimeout | 5 min | Generous for git push/fetch streams |
| IdleTimeout | 2 min | Keep-alive connection idle |
| Shutdown drain | 30 s | Grace period when the process receives SIGINT/SIGTERM |

### Rate limiting and concurrency

The server applies two independent protection layers to `/v1/exec` requests:

| Limit | Default | Config field |
|-------|---------|--------------|
| Per-client sustained rate | 10 req/s | `exec_rate_limit` |
| Per-client burst | 20 requests | `exec_rate_burst` |
| Global concurrent subprocesses | 32 | `max_concurrent_exec` |
| Per-command timeout | 60 s | `exec_timeout` |

Clients are identified by their mTLS certificate CN.  A client that exceeds its
rate limit receives HTTP 429; a server that is fully occupied returns HTTP 503.
Defaults can be overridden in the JSON config file.

---

## gt-proxy-client

### What it does

The client is installed inside the container as the `gt` and `bd` binaries (or
as symlinks to a single `gt-proxy-client` binary).  When called:

1. If `GT_PROXY_URL`, `GT_PROXY_CERT`, and `GT_PROXY_KEY` are all set → forward
   the call to the proxy server over mTLS.
2. Otherwise → `exec` the real binary at `GT_REAL_BIN` (default:
   `/usr/local/bin/gt.real`).

The fallback means the same binary works both inside and outside the sandbox
without any changes to agent code.

### Environment variables

| Variable | Required | Description |
|----------|----------|-------------|
| `GT_PROXY_URL` | Yes (for proxy) | Base URL of the proxy server, e.g. `https://192.168.1.10:9876` |
| `GT_PROXY_CERT` | Yes (for proxy) | Path to the polecat's client certificate (PEM) |
| `GT_PROXY_KEY` | Yes (for proxy) | Path to the polecat's client private key (PEM) |
| `GT_PROXY_CA` | Recommended | Path to the CA certificate used to verify the server's TLS cert |
| `GT_REAL_BIN` | No | Path to the real `gt` binary when falling back (default: `/usr/local/bin/gt.real`) |

If any of `GT_PROXY_URL`, `GT_PROXY_CERT`, or `GT_PROXY_KEY` is absent, the
client silently falls through to `execReal()`.  This makes it safe to install
unconditionally — polecats that are not sandboxed simply exec the real binary.

### Git integration

For git operations, configure git to use the proxy's git smart-HTTP endpoint:

```bash
# Tell git to use the proxy server for this rig's repo
git remote set-url origin https://<proxy-host>:9876/v1/git/<RigName>

# Tell git to use the CA cert and polecat cert for TLS
export GIT_SSL_CAINFO=$GT_PROXY_CA
export GIT_SSL_CERT=$GT_PROXY_CERT
export GIT_SSL_KEY=$GT_PROXY_KEY
```

The git client authenticates with the same mTLS certificate as the exec client.
Branch authorization is enforced server-side: a polecat named `rust` can only
push to `refs/heads/polecat/rust-*`.

---

## End-to-end setup

### Step 1: Start the server on the host

```bash
# Installs the CA on first run
gt-proxy-server --listen 0.0.0.0:9876

# The CA cert is now at ~/gt/.runtime/ca/ca.crt
```

### Step 2: Issue a polecat certificate

Use the Go API or a small helper:

```go
ca, _ := proxy.LoadOrGenerateCA("~/gt/.runtime/ca")
certPEM, keyPEM, _ := ca.IssuePolecat("gt-MyRig-rust", 365*24*time.Hour)
```

Save the output files:

```
~/gt/.runtime/polecats/rust/
  polecat.crt   ← client certificate for this polecat
  polecat.key   ← client private key for this polecat
```

### Step 3: Install the client binary in the container

```bash
# Option A: Copy the binary twice
cp gt-proxy-client /usr/local/bin/gt
cp gt-proxy-client /usr/local/bin/bd

# Option B: Copy once and symlink
cp gt-proxy-client /usr/local/bin/gt-proxy-client
ln -s gt-proxy-client /usr/local/bin/gt
ln -s gt-proxy-client /usr/local/bin/bd

# If the real gt binary should be accessible as a fallback:
mv /usr/local/bin/gt.original /usr/local/bin/gt.real
```

### Step 4: Configure the container environment

```bash
export GT_PROXY_URL=https://192.168.1.10:9876
export GT_PROXY_CERT=/secrets/polecat.crt
export GT_PROXY_KEY=/secrets/polecat.key
export GT_PROXY_CA=/secrets/ca.crt

# For git operations:
export GIT_SSL_CAINFO=$GT_PROXY_CA
export GIT_SSL_CERT=$GT_PROXY_CERT
export GIT_SSL_KEY=$GT_PROXY_KEY
```

You may mount `ca.crt`, `polecat.crt`, and `polecat.key` as container secrets
(Docker secrets, Kubernetes secrets, Daytona workspace env, etc.).

### Step 5: Verify the connection

Inside the container:

```bash
gt version           # Should print the Gas Town version via the proxy
gt status            # Should show town status from the host
git push origin HEAD # Should push to the polecat branch via the proxy
```

---

## Configuration file

Server-side options can be set in a JSON config file.  The default path is
`~/gt/.runtime/proxy/config.json`; override it with `--config`.  CLI flags
always take precedence over file values.

```json
{
  "listen_addr":        "0.0.0.0:9876",
  "admin_listen_addr":  "127.0.0.1:9877",
  "ca_dir":             "",
  "town_root":          "",
  "allowed_commands":   ["gt", "bd"],
  "allowed_subcommands": {
    "gt": ["prime", "hook", "done", "mail", "nudge", "mol", "status", "handoff", "version", "convoy", "sling"],
    "bd": ["create", "update", "close", "show", "list", "ready", "dep", "export", "prime", "stats", "blocked", "doctor"]
  },
  "extra_san_ips":      ["10.0.1.5", "172.20.0.1"],
  "extra_san_hosts":    ["my-dev-vm.local", "proxy.corp.example.com"],
  "max_concurrent_exec": 32,
  "exec_rate_limit":    10.0,
  "exec_rate_burst":    20,
  "exec_timeout":       "60s"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `listen_addr` | `string` | TCP address for the mTLS server (default: `0.0.0.0:9876`) |
| `admin_listen_addr` | `string` | TCP address for the local admin HTTP server (default: `127.0.0.1:9877`); set to `""` to disable |
| `ca_dir` | `string` | Directory holding `ca.crt` and `ca.key` (default: `~/gt/.runtime/ca`) |
| `town_root` | `string` | Gas Town root directory (default: `$GT_TOWN` or `~/gt`) |
| `allowed_commands` | `[]string` | Binary names polecats may execute |
| `allowed_subcommands` | `map[string][]string` | Per-command subcommand allowlists |
| `extra_san_ips` | `[]string` | Additional IP addresses to include in the server certificate's SAN list |
| `extra_san_hosts` | `[]string` | Additional hostnames (DNS names) to include in the server certificate's SAN list |
| `max_concurrent_exec` | `int` | Maximum simultaneous exec subprocesses (default: 32) |
| `exec_rate_limit` | `float64` | Sustained exec requests per second per client (default: 10) |
| `exec_rate_burst` | `int` | Burst size for per-client rate limiter (default: 20) |
| `exec_timeout` | `string` | Maximum duration for a single exec subprocess, e.g. `"60s"` (default: 60 s) |

### Local IPs vs external/NAT IPs

The server automatically detects and includes all local network interface IPs
(via `net.Interfaces()`) in its TLS certificate's Subject Alternative Names.
This covers direct LAN connections.

**External / NAT IP addresses are not auto-detected.**  The exit IP lives on
the router — it is not present on any OS network interface — so there is no
reliable way to discover it without contacting an external service.

If containers connect to the proxy through a NAT boundary (e.g., the host is
behind a home router and containers run on a cloud VM), add the external IP
to `extra_san_ips`:

```json
{
  "extra_san_ips": ["203.0.113.42"]
}
```

You can find your external IP with:

```bash
curl -s https://api.ipify.org
```

---

## Security model

### What is enforced

| Layer | What | How |
|-------|------|-----|
| **Transport** | All traffic is encrypted | TLS 1.3 minimum |
| **Server identity** | Container verifies the host is legitimate | Server cert signed by the shared CA |
| **Client identity** | Server verifies every request comes from a known polecat | Client cert signed by the same CA; CN format `gt-<rig>-<name>` required |
| **Exec allowlist** | Containers can only call `gt` and `bd` (or the configured set) | `--allowed-cmds` checked on every `/v1/exec` request |
| **Subcommand allowlist** | Polecats may only invoke permitted subcommands of `gt`/`bd` | `--allowed-subcmds` checked on every `/v1/exec` request; missing or disallowed subcommands → 403 |
| **Subcommand injection** | Polecat identity is injected as `--identity <rig>/<name>` and cannot be overridden | Server derives identity from the client certificate, not from the request body |
| **Branch scope** | A polecat can only push to `refs/heads/polecat/<name>-*` | pkt-line stream parsed and validated before `git-receive-pack` is invoked |
| **Path traversal** | Rig names are validated against `[a-zA-Z0-9_-]+` | Rejects `../` and other traversal attempts |
| **Body size limits** | `/v1/exec` body capped at 1 MiB; receive-pack ref list capped at 32 MiB | `http.MaxBytesReader` applied before reading |
| **Env isolation** | `gt`/`bd`/`git` subprocesses only see `HOME` and `PATH` | Server never passes its own `GITHUB_TOKEN`, `GT_TOKEN`, or other credentials |
| **Rate limiting** | Per-client exec rate limited (default: 10 req/s, burst 20) | `golang.org/x/time/rate` limiter per mTLS cert CN; HTTP 429 on excess |
| **Concurrency cap** | Global exec subprocess limit (default: 32) | Semaphore; HTTP 503 when full |
| **Certificate revocation** | Compromised cert serials can be denied at runtime | In-memory deny list checked at TLS handshake; updated via local admin API |

### What is not enforced

- **Filesystem access from within the container** — the proxy only mediates `gt`/`bd` and git; a container with volume mounts can still read those files directly.
- **Network egress from the container** — the proxy does not prevent containers from making outbound connections to GitHub or other services.

---

## Local admin server

The server starts a second HTTP listener bound to `127.0.0.1:9877` (configurable
via `--admin-listen`; set to `""` to disable).  This server has **no TLS** —
it is intentionally local-only and relies on OS-level access control for
security.

### Admin endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/v1/admin/issue-cert` | Issue a new polecat client certificate |
| `POST` | `/v1/admin/deny-cert` | Add a certificate serial to the runtime deny list |

### Issuing a polecat certificate

Issue a client certificate for a polecat by providing the rig name, polecat
name, and an optional TTL (defaults to 720h / 30 days):

```bash
curl -s -X POST http://127.0.0.1:9877/v1/admin/issue-cert \
  -H 'Content-Type: application/json' \
  -d '{"rig": "MyRig", "name": "rust", "ttl": "720h"}'
```

Returns HTTP 200 with a JSON body containing the PEM-encoded certificate, key,
and CA certificate, plus metadata:

```json
{
  "cn":         "gt-MyRig-rust",
  "cert":       "-----BEGIN CERTIFICATE-----\n...",
  "key":        "-----BEGIN EC PRIVATE KEY-----\n...",
  "ca":         "-----BEGIN CERTIFICATE-----\n...",
  "serial":     "3f2a1b...",
  "expires_at": "2026-04-01T22:37:00Z"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `rig` | `string` | **Required.** Rig name (e.g. `"MyRig"`) |
| `name` | `string` | **Required.** Polecat name (e.g. `"rust"`) |
| `ttl` | `string` | Optional Go duration (e.g. `"720h"`). Default: `720h` (30 days) |

### Revoking a certificate

Send the certificate serial number as lowercase hex in the request body:

```bash
curl -s -X POST http://127.0.0.1:9877/v1/admin/deny-cert \
  -H 'Content-Type: application/json' \
  -d '{"serial": "3f2a1b"}'
```

Returns HTTP 204 on success.  The serial is added to an in-memory deny list;
any future TLS handshake presenting that certificate is rejected immediately.
The deny list is not persisted across restarts — if a cert must remain revoked
after a restart, do not reissue it.

---

## How git proxying works

The server implements the [git smart-HTTP protocol](https://git-scm.com/docs/http-backend)
over mTLS.  Git clients inside containers configure their remote URL to point at
the proxy:

```
https://<proxy-host>:9876/v1/git/<RigName>
```

Git then makes the same requests it would make to any HTTPS git server:

```
# Clone / fetch
GET  /v1/git/MyRig/info/refs?service=git-upload-pack
POST /v1/git/MyRig/git-upload-pack

# Push
GET  /v1/git/MyRig/info/refs?service=git-receive-pack
POST /v1/git/MyRig/git-receive-pack
```

The server translates each request into a local subprocess call:

```
git-upload-pack  --stateless-rpc [--advertise-refs] ~/gt/MyRig/.repo.git
git-receive-pack --stateless-rpc [--advertise-refs] ~/gt/MyRig/.repo.git
```

For pushes (`git-receive-pack`), the server reads the pkt-line ref list **before**
passing the body to git, and rejects any ref that falls outside the polecat's
allowed scope:

```
refs/heads/polecat/<name>-*   ✓ allowed
refs/heads/main               ✗ denied (403 Forbidden)
refs/heads/polecat/other-*    ✗ denied (belongs to another polecat)
```

The pkt-line stream is then rewound and fed to `git-receive-pack` unchanged, so
git sees a normal push body.

---

## Troubleshooting

### `x509: certificate is valid for ..., not <IP>`

The container is connecting to the server by an IP address that is not listed in
the server certificate's Subject Alternative Names.

**Fix**: Add the IP to `extra_san_ips` in `~/gt/.runtime/proxy/config.json` and
restart the server (a new server cert is issued on each startup).

```json
{ "extra_san_ips": ["10.0.2.15"] }
```

### `remote error: tls: bad certificate`

The client certificate was not issued by the CA the server trusts, or `GT_PROXY_CA`
points at the wrong file.

Verify:

```bash
# Check that the client cert was signed by ca.crt
openssl verify -CAfile ~/gt/.runtime/ca/ca.crt /path/to/polecat.crt

# Check that GT_PROXY_CA points at the correct CA
openssl x509 -in $GT_PROXY_CA -noout -subject
```

### `command not allowed: "sh"`

The container tried to exec a binary not in `--allowed-cmds`.  The server returns
HTTP 403 and logs the attempt.

If this is legitimate, add the command to `--allowed-cmds`.  If not, it indicates
the agent is trying to execute a shell — which is intentionally blocked.

### `push to "refs/heads/main" denied`

The polecat tried to push to a branch it does not own.  Polecats may only push to
`refs/heads/polecat/<their-name>-*`.  The refinery merges these branches; polecats
do not push directly to `main` or `proxy`.

### `gt-proxy-client: proxy request failed: ...` (fallback active)

If any of `GT_PROXY_URL`, `GT_PROXY_CERT`, or `GT_PROXY_KEY` is unset, the client
falls back to `execReal()` (the real `gt` binary at `GT_REAL_BIN`).  Check that
all three environment variables are set inside the container:

```bash
echo $GT_PROXY_URL
echo $GT_PROXY_CERT
echo $GT_PROXY_KEY
```

### Server cert contains only `gt-proxy-server` as SAN

This is expected if `extra_san_ips` / `extra_san_hosts` are not configured.
For testing you can pass `--insecure` / set `GIT_SSL_NO_VERIFY=1` temporarily,
but for production always configure the correct SANs or use a hostname.

---

## Reference

### Server endpoints

**mTLS server (default: `0.0.0.0:9876`)**

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/v1/exec` | Execute a `gt` or `bd` command |
| `GET` | `/v1/git/<rig>/info/refs?service=<svc>` | git smart-HTTP capability advertisement |
| `POST` | `/v1/git/<rig>/git-upload-pack` | git fetch / clone |
| `POST` | `/v1/git/<rig>/git-receive-pack` | git push (CN-scoped branch authorization) |

**Local admin server (default: `127.0.0.1:9877`, no TLS)**

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/v1/admin/issue-cert` | Issue a new polecat client certificate |
| `POST` | `/v1/admin/deny-cert` | Add a certificate serial to the runtime deny list |

### Certificate CN format

| Role | CN format | Example |
|------|-----------|---------|
| Server | `gt-proxy-server` | `gt-proxy-server` |
| Polecat client | `gt-<rig>-<name>` | `gt-GasTown-rust` |

The server derives the polecat's identity (`<rig>/<name>`) from the CN at request
time.  The last `-` in the remainder after stripping `gt-` is the rig/name
separator, so hyphenated rig names such as `my-rig` are parsed correctly:

```
CN: gt-my-rig-rust   →   rig=my-rig, name=rust, identity=my-rig/rust
```

### File layout

```
~/gt/
  .runtime/
    ca/
      ca.crt           ← CA certificate (safe to distribute to containers)
      ca.key           ← CA private key  (host-only; never leave this machine)
    proxy/
      config.json      ← Optional: extra_san_ips, extra_san_hosts
    polecats/
      <name>/
        polecat.crt    ← Per-polecat client certificate
        polecat.key    ← Per-polecat private key
  <RigName>/
    .repo.git/         ← Bare repository proxied by git endpoints
```
