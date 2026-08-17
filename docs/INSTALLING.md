# Installing Gas Town

Complete setup guide for Gas Town multi-agent orchestrator.

For the shortest native path, install `gt` globally with `CGO_ENABLED=0 go install github.com/jonbaldie/gastown/cmd/gt@main`. Use `@main` until a post-migration release tag exists; `@latest` still resolves to the pre-migration `v1.2.1` module path. Install `bd` and Dolt separately as described below. Docker supplies the runtime tools inside the container.

## Prerequisites

### Required

Native source installs require these host tools. Docker installs only require Docker Compose on the host; the container supplies Go, Dolt, `bd`, tmux, and CLI utilities.

| Tool | Version | Check | Install |
|------|---------|-------|---------|
| **Go** | 1.26.2+ | `go version` | See [golang.org](https://go.dev/doc/install) |
| **Git** | 2.20+ | `git --version` | See below |
| **sqlite3** | any | `sqlite3 --version` | Usually pre-installed on macOS; Linux packages are commonly named `sqlite3` |
| **ICU4C dev headers** | varies | `pkg-config --modversion icu-uc`, `dpkg -l libicu-dev`, `rpm -q libicu-devel`, or `brew --prefix icu4c` | Required only for `make build-cgo`, which compiles the optional embedded query layer |
| **Dolt** | >= 2.0.7 | `dolt version` | macOS: `brew install dolt`; other platforms: see [dolthub/dolt](https://github.com/dolthub/dolt?tab=readme-ov-file#installation) |
| **Beads** | >= 0.57.0 | `bd version` | `go install github.com/steveyegge/beads/cmd/bd@latest` |
| **Docker Compose** | v2+ | `docker compose version` | Docker setup only. Install Docker Desktop or Docker Engine with the Compose plugin. |

### Optional (for Full Stack Mode)

| Tool | Version | Check | Install |
|------|---------|-------|---------|
| **tmux** | 3.0+ | `tmux -V` | See below |
| **Claude Code** (default) | >= 2.0.20 | `claude --version` | See [claude.ai/claude-code](https://claude.ai/claude-code) |
| **Pi** (optional) | latest | `pi --version` | See [Pi runtime](PI.md) |
| **Codex CLI** (optional) | latest | `codex --version` | See [developers.openai.com/codex/cli](https://developers.openai.com/codex/cli) |
| **OpenCode CLI** (optional) | latest | `opencode --version` | See [opencode.ai](https://opencode.ai) |
| **GitHub Copilot CLI** (optional) | latest | `copilot --version` | See [cli.github.com](https://cli.github.com) (requires Copilot seat) |

## Installing Prerequisites

### macOS

Install Go and Dolt with Homebrew, then install `gt` and `bd` with Go.

```bash
brew install go dolt
CGO_ENABLED=0 go install github.com/jonbaldie/gastown/cmd/gt@main
go install github.com/steveyegge/beads/cmd/bd@latest

# Optional: Docker setup only
# Install Docker Desktop or another Docker Engine with Compose v2.

# Optional (for full stack mode)
brew install tmux
```

### Linux (Debian/Ubuntu)

```bash
# Required
sudo apt update
sudo apt install -y git sqlite3

# Install Go (apt version may be outdated, use official installer)
wget https://go.dev/dl/go1.26.2.linux-amd64.tar.gz
sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf go1.26.2.linux-amd64.tar.gz
echo 'export PATH=/usr/local/go/bin:$HOME/go/bin:$PATH' >> ~/.bashrc
source ~/.bashrc

# Install Dolt: see https://github.com/dolthub/dolt?tab=readme-ov-file#installation

# Docker setup only: install Docker Engine with the Compose plugin.

# Optional (for full stack mode)
sudo apt install -y tmux
```

### Linux (Fedora/RHEL)

```bash
# Required
sudo dnf install -y git sqlite
# Install Go 1.26.2+ from your distro if available, otherwise use the official Go installer.
# Install Dolt: see https://github.com/dolthub/dolt?tab=readme-ov-file#installation
# Docker setup only: install Docker Engine with the Compose plugin.

# Optional
sudo dnf install -y tmux
```

### Windows

Install Go and Dolt first, then install `gt` and `bd` with Go. The binaries land in `%USERPROFILE%\go\bin`; put that directory before older `gt` or `bd` install locations on `PATH`, then open a new shell. For Docker setup, install Docker Desktop with Compose support.

`make build` defaults to `CGO_ENABLED=0` and talks to Dolt as an external SQL server. Native Windows source builds that compile the ICU-backed embedded query layer (`make build-cgo`) need an MSYS2 UCRT64 or MinGW64 shell with matching `icu`, `toolchain`, and `pkg-config` packages. The repository's Windows CI uses `pacboy -S icu:p toolchain:p pkg-config:p` before running Go commands; plain PowerShell/MSVC is not enough for that CGO build.

```powershell
$env:CGO_ENABLED = "0"
go install github.com/jonbaldie/gastown/cmd/gt@main
go install github.com/steveyegge/beads/cmd/bd@latest
```

Use WSL or another Linux environment for tmux-backed workflows. Native Windows shells are best suited to minimal CLI-only use.

### Verify Prerequisites

```bash
# Check all prerequisites
go version        # Should show go1.26.2 or higher
git --version     # Should show 2.20 or higher
dolt version      # Should show 2.0.7 or higher
tmux -V           # (Optional) Should show 3.0 or higher
```

## Installing Gas Town

### Step 1: Install the Binaries

On macOS and Linux, install `gt` and Beads with Go after installing Dolt separately:

```bash
CGO_ENABLED=0 go install github.com/jonbaldie/gastown/cmd/gt@main
go install github.com/steveyegge/beads/cmd/bd@latest
```

The binaries land in `$GOBIN`, or `$GOPATH/bin` (usually `~/go/bin`) when `GOBIN` is unset. Put that directory before older install locations on `PATH`, then verify the native dependencies:

```bash
gt version
bd version
dolt version
```

### Step 2: Create Your Workspace

Run these workspace steps on macOS, Linux, or WSL. Native Windows shells are minimal CLI-only environments; use WSL for `--shell`, `gt up`, tmux-backed roles, and Mayor sessions.

```bash
# Set identity before --git so the initial HQ commit and Dolt config are valid
git config --global user.name "Your Name"
git config --global user.email "you@example.com"

# Create a Gas Town workspace (HQ)
gt install ~/gt --shell --git

# This creates:
#   ~/gt/
#   ├── AGENTS.md          # Identity anchor (CLAUDE.md symlinks here)
#   ├── mayor/             # Mayor config and state
#   ├── rigs/              # Project containers (initially empty)
#   └── .beads/            # Town-level issue tracking
```

### Step 3: Add a Project (Rig)

```bash
# Add your first project
gt rig add myproject https://github.com/you/repo.git

# This clones the repo and sets up:
#   ~/gt/myproject/
#   ├── .beads/            # Project issue tracking
#   ├── mayor/rig/         # Mayor's clone (canonical)
#   ├── refinery/rig/      # Merge queue processor
#   ├── witness/           # Worker monitor
#   └── polecats/          # Worker clones (created on demand)
```

### Step 4: Verify Installation

```bash
cd ~/gt

gt enable              # enable Gas Town system-wide
gt up                  # Start all services. Use gt down or gt shutdown for stopping. 

gt doctor --fix        # Run health checks and fix post-install warnings
gt status              # Show workspace status
```

### Step 5: Configure Agents (Optional)

Gas Town supports built-in runtimes (`claude`, `gemini`, `codex`, `kiro`, `cursor`, `auggie`, `amp`, `opencode`, `copilot`, `pi`) plus custom agent aliases.

```bash
# List available agents
gt config agent list

# Create an alias (aliases can encode model/thinking flags).
# Codex aliases inherit --dangerously-bypass-approvals-and-sandbox and
# -c check_for_update_on_startup=false unless you set a sandbox/approval policy.
gt config agent set codex-low "codex --thinking low"
gt config agent set claude-haiku "claude --model haiku --dangerously-skip-permissions"

# Set the town default agent (used when a rig doesn't specify one)
gt config default-agent codex-low
```

Mix agent types across roles in one command. Codex and Pi (or any built-in
pair) can run in the same town:

```bash
gt config mix default=codex mayor=pi:high crew=codex polecat=codex
gt config mix
```

Or assign one role at a time, including named crew workers:

```bash
gt config agent set pi-luna "pi --model openai-codex/gpt-5.6-luna" --provider pi
gt config role set mayor pi-luna high
gt config role set witness pi-luna low
gt config mix crew:alice=pi
gt config role list
```

See [Pi runtime](PI.md) for model profiles, role and rig overrides, thinking
effort, and container authentication.

You can also override the agent per command without changing defaults:

```bash
gt start --agent codex-low
gt sling gt-abc12 myproject --agent claude-haiku
```

## Minimal Mode vs Full Stack Mode

Gas Town supports two operational modes:

### Minimal Mode (No Daemon)

Run individual runtime instances manually. Gas Town only tracks state.

```bash
# Create and assign work
gt convoy create "Fix bugs" gt-abc12
gt sling gt-abc12 myproject

# Run runtime manually
cd ~/gt/myproject/polecats/<worker>
claude --resume          # Claude Code
# or: codex              # Codex CLI

# Check progress
gt convoy list
```

**When to use**: Testing, simple workflows, or when you prefer manual control.

### Full Stack Mode (With Daemon)

Agents run in tmux sessions. Daemon manages lifecycle automatically.

```bash
# Start the daemon
gt daemon start

# Create and assign work (workers spawn automatically)
gt convoy create "Feature X" gt-abc12 gt-def34
gt sling gt-abc12 myproject
gt sling gt-def34 myproject

# Monitor on dashboard
gt convoy list

# Attach to any agent session
gt mayor attach
gt witness attach myproject
```

**When to use**: Production workflows with multiple concurrent agents.

### Choosing Roles

Gas Town is modular. Enable only what you need:

| Configuration | Roles | Use Case |
|--------------|-------|----------|
| **Polecats only** | Workers | Manual spawning, no monitoring |
| **+ Witness** | + Monitor | Automatic lifecycle, stuck detection |
| **+ Refinery** | + Merge queue | MR review, code integration |
| **+ Mayor** | + Coordinator | Cross-project coordination |

## Troubleshooting

### `gt: command not found`

The Gas Town binary directory is not in PATH. `go install` places `gt` in `$GOBIN`, or `$GOPATH/bin` (usually `~/go/bin`) when `GOBIN` is unset:

```bash
# Add to your shell config (~/.bashrc, ~/.zshrc)
export PATH="$HOME/go/bin:$PATH"
source ~/.bashrc  # or restart terminal
```

### `bd: command not found`

Beads CLI not installed:

```bash
go install github.com/steveyegge/beads/cmd/bd@latest
```

### `gt doctor` shows errors

Run with `--fix` to auto-repair common issues:

```bash
gt doctor --fix
```

For persistent issues, check specific errors:

```bash
gt doctor --verbose
```

### Daemon not starting

Check if tmux is installed and working:

```bash
tmux -V                    # Should show version
tmux new-session -d -s test && tmux kill-session -t test  # Quick test
```

### Git authentication issues

Ensure SSH keys or credentials are configured:

```bash
# Test SSH access
ssh -T git@github.com

# Or configure credential helper
git config --global credential.helper cache
```

### Beads issues

If experiencing beads problems:

```bash
cd ~/gt/myproject/mayor/rig
bd status                  # Check database health
bd doctor                  # Run beads health check
```

## Updating

Reinstall `gt` from `main` to pick up updates:

```bash
CGO_ENABLED=0 go install github.com/jonbaldie/gastown/cmd/gt@main
command -v gt              # Should be $GOBIN or $GOPATH/bin, usually ~/go/bin/gt
gt version
gt doctor --fix            # Fix any post-update issues
```

Update Beads the same way:

```bash
go install github.com/steveyegge/beads/cmd/bd@latest
```

Run the `command -v gt` and `gt version` checks before `gt doctor --fix` so a
stale shadow binary does not run the repair step first.

If `command -v gt` points at a different install channel than the one you just
updated, fix your PATH before continuing.

## Uninstalling

```bash
# Remove binaries
rm $(which gt) $(which bd)

# Remove workspace (CAUTION: deletes all work)
rm -rf ~/gt
```

## Next Steps

After installation:

1. **Read the README** - Core concepts and workflows
2. **Try a simple workflow** - create work in the target rig, then convoy and sling it:
   `bd -C ~/gt/<rig> create --title="Test task" --type=feature`
   then `gt convoy create "Test" <rig-prefix-id>` and `gt sling <rig-prefix-id> <rig> --merge=local`
3. **Explore docs** - `docs/reference.md` for command reference
4. **Run doctor regularly** - `gt doctor` catches problems early
5. **Join the Wasteland** - `gt wl join hop/wl-commons` to browse and claim federated work (see [WASTELAND.md](WASTELAND.md))
