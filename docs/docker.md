# Running Gas Town in Docker

Gas Town ships two Docker images. The production runtime image hosts a sandboxed Gas Town workspace and is the focus of most of this document. The e2e testing image runs integration tests inside CI and is described in its own section near the end.

Docker is one of two installation paths for Gas Town. The other path installs `gt` directly on your host machine. [INSTALLING.md](INSTALLING.md) covers the native install. The two paths are alternatives — pick one. Docker gives you stronger isolation and bundles every prerequisite. The native install puts `gt` directly on your host.

## What ships in the repository

The Docker tooling lives at the repository root.

| File | Purpose |
|---|---|
| `Dockerfile` | Production runtime image. The Dockerfile builds on Anthropic's claude-code sandbox template, installs Git, Go, Dolt, beads, plus utility tooling, and compiles `gt` from source. |
| `docker-compose.yml` | Service definition for the runtime. The compose file sets capabilities, volumes, and the bind mount you point at a host directory. |
| `docker-entrypoint.sh` | First-run bootstrap script. The script sets git and dolt identity from environment variables, then runs `gt install /gt --git` against the bind-mounted workspace. |
| `Dockerfile.e2e` | Image used by CI for integration tests. The e2e image is Alpine-based, more minimal than the production image, and runs `go test` rather than a long-lived service. |
| `.dockerignore` | Build-context exclusions. The ignore list keeps `.git`, `.beads`, agent state directories, and build artifacts out of the image. |

CI workflows under `.github/workflows/` use `Dockerfile.e2e` for e2e jobs and pull `dolthub/dolt-sql-server` images for tests that need a Dolt server outside a full Gas Town environment. Application code detects whether it is running inside the sandbox container by reading one environment variable: `IS_SANDBOX`. The compose file sets `IS_SANDBOX=1`, and `internal/cmd/dashboard.go:50` reads it to decide the dashboard's default bind address.

## Quick start

The runtime image expects three environment variables and one host directory.

```bash
export GIT_USER="<your name>"
export GIT_EMAIL="<your email>"
export FOLDER="/path/to/empty/dir"   # becomes /gt inside the container
export DASHBOARD_PORT=8080            # optional, host port for the dashboard

mkdir -p "$FOLDER"
docker compose build
docker compose up -d
docker compose exec gastown zsh
```

The entrypoint runs `gt install /gt --git` automatically on first start. After you exec a shell into the running container, finish bootstrapping with the commands below.

```bash
gt enable
gt shell install
gt up --restore
gt mayor attach
```

`docker compose down -v` tears everything down, including the persisted Docker volumes. Run the command from the directory that holds `docker-compose.yml`.

## How the container is built

The Dockerfile has roughly four stages: base image, system packages, language runtimes, and the `gt` build itself.

### Base image

```dockerfile
FROM docker/sandbox-templates:claude-code
```

The base image is Anthropic's hardened Linux template for Claude Code sandboxes. The template provides a non-root `agent` user with a configured home directory. The Dockerfile flips to `root` only long enough to install system packages, then drops back to `agent` for the build steps.

### System packages

A single `apt-get install` adds the tooling Gas Town uses at runtime: `build-essential`, `git`, `sqlite3`, `tmux`, `curl`, `ripgrep`, `zsh`, `gh`, `netcat-openbsd`, `tini`, and `vim`. The same `RUN` step cleans up `/var/lib/apt/lists/` afterwards to keep the layer small. `tini` runs as the container's PID 1 (see *Entrypoint*).

### Language runtimes and gt dependencies

Go installs from the official tarball because the Debian-packaged version lags. The Go version is controlled by `ARG GO_VERSION` (currently `1.25.8`). The Dockerfile detects the host architecture at build time, which lets the same Dockerfile produce working images on amd64 and arm64. The architecture-detection change came from commit `ac4b65d1`.

`bd` and `dolt` install via the upstream `curl | bash` install scripts. The scripts deposit binaries on `$PATH` for the `agent` user.

`PATH` is set so `/app/gastown` (where the freshly-built `gt` binary lives) takes priority. `/usr/local/go/bin` and `/home/agent/go/bin` follow. The Dockerfile writes the same prefixes into `/etc/profile.d/gastown.sh` and `/etc/zsh/zshenv` so both bash and zsh interactive sessions see the right binaries.

### Building gt from source

The Dockerfile copies the repository into `/app/gastown` with `--chown=agent:agent` so the build runs as the `agent` user (commit `480f00f0`). `make build` then produces a `gt` binary at `/app/gastown/gt`. The `PATH` ordering exposes that binary as the default `gt` for any user inside the container.

`.dockerignore` keeps host state out of the build context. The ignore list excludes `.git`, `.beads`, build artifacts, and agent state directories: `crew/`, `mayor/`, `polecats/`, `refinery/`, `witness/`, `logs/`. The exclusion list keeps the image lean and prevents accidental host-state leaks.

### Entrypoint

```dockerfile
ENTRYPOINT ["tini", "--", "/app/docker-entrypoint.sh"]
CMD ["sleep", "infinity"]
```

`tini` is the init process. `tini` runs `/app/docker-entrypoint.sh` and reaps zombie processes that would otherwise accumulate from gt's many subprocesses (commit `9c2f0d06`). The default `CMD` is `sleep infinity` because the container is designed to live as a long-running service that you exec into.

## Service configuration (docker-compose.yml)

`docker-compose.yml` defines a single service named `gastown`. Most of the file is security and storage configuration. The parts you typically change are environment variables and the bind-mount target.

### Environment variables

| Variable | Default | Purpose |
|---|---|---|
| `GIT_USER` | `TestUser` | The entrypoint sets `git config --global user.name` and `dolt config --global user.name` from this variable. Gas Town's identity detection requires a value here. |
| `GIT_EMAIL` | `test@example.com` | The entrypoint sets `user.email` for git and dolt from this variable. The entrypoint also enables `credential.helper store` so subsequent git pushes can persist credentials. |
| `FOLDER` | (unset, **required**) | `FOLDER` is the host path bind-mounted to `/gt` inside the container. The directory must exist before `docker compose up`, and must be empty or already a Gas Town HQ — the entrypoint runs `gt install /gt --git` against the bind-mounted directory on first start, which converts whatever is there into an HQ. |
| `DASHBOARD_PORT` | `8080` | `DASHBOARD_PORT` is the host port mapped to the container's port 8080 (the `gt dashboard` web UI). |
| `IS_SANDBOX` | `1` (set by compose) | `internal/cmd/dashboard.go` reads `IS_SANDBOX`. When set, `gt dashboard` binds to `0.0.0.0` instead of `127.0.0.1` so the host port forward can reach the server. Leave the variable alone for normal use. |

The recommended way to set `GIT_USER`, `GIT_EMAIL`, and `FOLDER` is a `.env` file in the same directory as `docker-compose.yml`. Compose reads the `.env` file automatically on every invocation. Values in the `.env` file persist across terminals and reboots.

```
GIT_USER=Your Name
GIT_EMAIL=you@example.com
FOLDER=/path/to/empty/dir
DASHBOARD_PORT=8080
```

Inline `export` statements work for one-off invocations but expire when the shell exits.

### Volumes

The compose file declares three volume mounts on the `gastown` service.

| Mount | Type | Purpose |
|---|---|---|
| `agent-home:/home/agent` | Named volume | The `agent-home` volume persists the `agent` user's home directory across container restarts. The volume holds caches, shell history, and credentials. |
| `${FOLDER}:/gt` | Host bind mount | The bind mount exposes the Gas Town HQ to the host so you can read and edit workspace state from outside the container. The bind mount is required. |
| `dolt-data:/gt/.dolt-data` | Named volume nested in the bind mount | The `dolt-data` volume keeps Dolt's data on a real ext4 (or equivalent) volume rather than the bind mount. |

The third mount is intentional layering. The named volume overrides the bind mount at `/gt/.dolt-data` only. Dolt journaling on macOS bind mounts uses VirtioFS, which can corrupt under certain `fsync` patterns. The `dolt-data` volume sidesteps that path entirely (commit `a84977c0`). Linux hosts have a lower corruption risk, but the same layout runs everywhere for consistency.

`agent-home` and `dolt-data` are created on first `docker compose up` and survive `docker compose down`. Only `docker compose down -v` removes them. The `${FOLDER}` host directory is never touched by `down`. The host directory lives on your filesystem and is your responsibility to clean up.

### Security

The container drops every Linux capability and adds back only what is strictly needed.

```yaml
security_opt:
  - no-new-privileges:true
cap_drop:
  - ALL
cap_add:
  - CHOWN
  - SETUID
  - SETGID
  - DAC_OVERRIDE
  - FOWNER
  - NET_RAW
```

`no-new-privileges` prevents any process inside the container from elevating privileges via setuid binaries. The added capabilities cover what `apt`, `git`, the build steps, and per-process file ownership need at runtime. `NET_RAW` is included for tools that emit ICMP, which is mostly diagnostics.

`stdin_open: false` and `tty: false` mean the long-running `sleep infinity` foreground process does not hold a TTY. Interactive sessions come through `docker compose exec` instead.

### Networking

A single port forward exposes the dashboard.

```yaml
ports:
  - "${DASHBOARD_PORT:-8080}:8080"
```

No other ports leave the container. Dolt runs on port 3307 inside the container's network namespace, but Dolt is unreachable from the host. The unreachability is intentional: the container is meant to be a self-contained Gas Town environment.

## Workspace bootstrap (docker-entrypoint.sh)

The entrypoint script is small and POSIX.

```sh
#!/bin/sh
set -e

if [ -n "$GIT_USER" ] && [ -n "$GIT_EMAIL" ]; then
    git config --global user.name "$GIT_USER"
    git config --global user.email "$GIT_EMAIL"
    git config --global credential.helper store
    dolt config --global --add user.name "$GIT_USER"
    dolt config --global --add user.email "$GIT_EMAIL"
fi

if [ ! -f /gt/mayor/town.json ]; then
    /app/gastown/gt install /gt --git
else
    /app/gastown/gt install /gt --git --force
fi

exec "$@"
```

The identity block runs every time the container starts. The behavior means you can update `GIT_USER` or `GIT_EMAIL` in your `.env` file and bounce the container to switch identities, even though the persistent `agent-home` volume retains the old `~/.gitconfig`.

The install block uses `mayor/town.json` as a marker for "is this already a Gas Town workspace?" If the marker is absent, the bind mount is a fresh `${FOLDER}`, and `gt install /gt --git` initializes it. If the marker is present, the workspace already exists from a previous run, and `gt install /gt --git --force` refreshes the workspace in place. The `--force` path is idempotent and survives version bumps.

The final `exec "$@"` hands off to the container's `CMD`, which is `sleep infinity`. The script terminates and the sleep takes over the foreground, with `tini` holding PID 1.

## Lifecycle

### First start

```bash
docker compose build       # only on first run or after pulling new code
docker compose up -d
```

`build` runs every Dockerfile step. Expect 5-15 minutes the first time, depending on network speed and CPU. Subsequent builds reuse cached layers.

`up -d` creates the named volumes (if not yet created), creates the network, and starts the container. The entrypoint runs `gt install /gt --git` against `${FOLDER}`, which takes another 30-60 seconds. Watch the entrypoint output with the command below.

```bash
docker compose logs -f gastown
```

Wait until `HQ created successfully!` appears in the logs before exec'ing in.

### Connecting

```bash
docker compose exec gastown zsh   # or bash
```

The `exec` command drops you into a shell as the `agent` user with the right `PATH`, working directory `/gt`, and the Gas Town environment.

### Inside the container

The entrypoint's `gt install /gt --git` does not run `gt up`, install shell integration, or restore agent settings. Those steps happen on first interactive use.

```bash
gt enable           # turn on shell hooks for Claude Code SessionStart events
gt shell install    # install zsh integration (sets GT_TOWN_ROOT, GT_RIG)
gt up --restore     # start the daemon and restore Mayor/Deacon settings
```

After the sequence above, `gt doctor` should report mostly clean. See *Known issues* below for the `claude-settings` failure that persists in the docker setup.

From here, the workflow matches a native install: `gt rig add <name> <url>`, `gt crew add <name> --rig <rig>`, and `gt mayor attach`.

For private GitHub repos, log in inside the container.

```bash
gh auth login
```

The container has `gh` preinstalled. Credentials persist through the `agent-home` volume.

### Stopping and tearing down

`docker compose stop` halts the container without removing it. `docker compose start` brings the container back. Volumes, network, and the workspace at `${FOLDER}` stay intact.

`docker compose down` removes the container and the network. The volumes and `${FOLDER}` survive `down`. Use `down` when you want a clean container start without losing state.

`docker compose down -v` removes the named volumes as well: `agent-home` and `dolt-data`. The bind-mounted `${FOLDER}` is *not* removed by `down -v`, only the docker-managed volumes. Use `down -v` for a fully fresh start.

To remove the image too — for instance to force a clean rebuild after pulling new code — combine `down -v` with `docker rmi`.

```bash
docker compose down -v
docker rmi gastown-gastown:latest
```

Replace `gastown-gastown` with whatever image name `docker compose images` reports if you have renamed the project.

## Known issues

A few rough edges are worth knowing about.

**Doctor reports a persistent `claude-settings` failure after install.** During `gt install /gt --git`, `bd init` commits `.claude/settings.json` to the workspace's git repo. Doctor later flags the file as stale and refuses to overwrite the file because the file is tracked. To clear the failure manually, untrack the file and re-run the fix.

```bash
git -C /gt rm --cached .claude/settings.json
git -C /gt commit -m "Untrack Claude settings"
gt doctor --fix
```

**The first `docker compose up` on a busy host can race.** On rare occasions, the entrypoint's `bd init` step has been observed to fail with a dynamic-linker error before Dolt's startup completes. Retrying with `docker compose down -v && docker compose up -d` resolves the failure. The race has not been reproduced under controlled conditions.

**`gt up`'s daemon status reads `failed to start` for a moment.** The entrypoint already started Dolt and registered Mayor/Deacon. When you run `gt up` afterwards, the daemon-spawn check fires before the new daemon's PID file exists, producing a transient `failed to start` line. `gt daemon status` confirms the daemon is actually running. The mismatch is cosmetic.

**Do not run a host `gt` and a container `gt` against the same workspace.** A docker container running against a `${FOLDER}` host path will spin up its own Dolt server and daemon. A native `gt` running against the same path will spin up a separate Dolt server. The two will write to the same workspace and clobber each other. Use distinct host paths if you need both installations.

## E2E testing image (Dockerfile.e2e)

`Dockerfile.e2e` serves a different purpose from the production image. The e2e image builds a minimal Alpine-based container whose only job is to run gastown's e2e integration tests and exit.

```bash
docker build -f Dockerfile.e2e -t gastown-test .
docker run --rm gastown-test
```

The e2e image installs Go 1.26, a pinned Dolt version (built from source for reproducibility), a pinned `bd` version, and the system tools needed by `gt install` and `gt dolt start` — notably `procps` and `lsof`. The default `CMD` runs `go test -tags=e2e -run TestInstall ./internal/cmd/...` against the integration tests, with `-count=1 -parallel 1` to disable caching and avoid concurrency surprises.

Common use cases for the e2e image:

- A developer reproduces a CI failure locally. The same `Dockerfile.e2e`, build, and run produce the same isolated environment.
- A developer tests changes to the install or workspace lifecycle without touching the host.
- CI validates that `gt install --git` works from a clean filesystem.

You typically do not run `Dockerfile.e2e` directly. CI does.

## CI integration

Three workflows under `.github/workflows/` use Docker. `e2e.yml` builds and runs `Dockerfile.e2e` for each PR — the e2e workflow is where install-flow regressions surface. `ci.yml` and `nightly-integration.yml` pre-pull `dolthub/dolt-sql-server` images for tests that need a Dolt server outside a full Gas Town environment. The production runtime `Dockerfile` is not used by CI.

