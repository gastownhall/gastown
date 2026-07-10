# Installing Gas Town

Complete setup guide for Gas Town multi-agent orchestrator.

For the shortest path, use `brew install gastown` on macOS or the Docker setup in [docker.md](docker.md). Homebrew installs `gt`, `bd`, and `dolt` together. Docker supplies the runtime tools inside the container. The native/source paths below are for hosts where you install and run `gt` directly.

## Prerequisites

### Required

Native source installs require these host tools. Homebrew and Docker installs provide some of them for you, as noted in the platform sections below. Docker installs only require Docker Compose on the host; the container supplies Go, Dolt, `bd`, tmux, and CLI utilities.

| Tool | Version | Check | Install |
|------|---------|-------|---------|
| **Go** | 1.26.2+ | `go version` | See [golang.org](https://go.dev/doc/install) |
| **Git** | 2.20+ | `git --version` | See below |
| **sqlite3** | any | `sqlite3 --version` | Usually pre-installed on macOS; Linux packages are commonly named `sqlite3` |
| **ICU4C dev headers** | varies | `pkg-config --modversion icu-uc`, `dpkg -l libicu-dev`, `rpm -q libicu-devel`, or `brew --prefix icu4c` | Source builds need Debian/Ubuntu `libicu-dev`, Fedora/RHEL `libicu-devel` with `pkgconf-pkg-config`, macOS `icu4c`, or native Windows MSYS2 ICU/toolchain/pkg-config packages |
| **Dolt** | >= 2.0.7 | `dolt version` | macOS: `brew install dolt`; other platforms: see [dolthub/dolt](https://github.com/dolthub/dolt?tab=readme-ov-file#installation) |
| **Beads** | >= 0.57.0 | `bd version` | Installed by `brew install gastown`, or from source with `go install github.com/steveyegge/beads/cmd/bd@latest` |
| **Docker Compose** | v2+ | `docker compose version` | Docker setup only. Install Docker Desktop or Docker Engine with the Compose plugin. |

### Optional (for Full Stack Mode)

| Tool | Version | Check | Install |
|------|---------|-------|---------|
| **tmux** | 3.0+ | `tmux -V` | See below |
| **Claude Code** (default) | >= 2.0.20 | `claude --version` | See [claude.ai/claude-code](https://claude.ai/claude-code) |
| **Codex CLI** (optional) | latest | `codex --version` | See [developers.openai.com/codex/cli](https://developers.openai.com/codex/cli) |
| **OpenCode CLI** (optional) | latest | `opencode --version` | See [opencode.ai](https://opencode.ai) |
| **GitHub Copilot CLI** (optional) | latest | `copilot --version` | See [cli.github.com](https://cli.github.com) (requires Copilot seat) |

## Installing Prerequisites

### macOS

Use Homebrew for the normal macOS install. It installs `gt`, `bd`, and `dolt` together.

```bash
# Install Homebrew if needed
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"

# Recommended install
brew install gastown

# Optional: source builds also need Go, Dolt, and ICU4C
brew install go dolt icu4c

# Optional: Docker setup only
# Install Docker Desktop or another Docker Engine with Compose v2.

# Optional (for full stack mode)
brew install tmux
```

### Linux (Debian/Ubuntu)

```bash
# Required
sudo apt update
sudo apt install -y git sqlite3 libicu-dev

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
sudo dnf install -y git sqlite libicu-devel pkgconf-pkg-config
# Install Go 1.26.2+ from your distro if available, otherwise use the official Go installer.
# Install Dolt: see https://github.com/dolthub/dolt?tab=readme-ov-file#installation
# Docker setup only: install Docker Engine with the Compose plugin.

# Optional
sudo dnf install -y tmux
```

### Windows

Install Go and Dolt first, then install `gt` and `bd` with Go. The binaries land in `%USERPROFILE%\go\bin`; put that directory before older `gt` or `bd` install locations on `PATH`, then open a new shell. For Docker setup, install Docker Desktop with Compose support.

Native Windows source builds that compile the ICU-backed query layer need an MSYS2 UCRT64 or MinGW64 shell with matching `icu`, `toolchain`, and `pkg-config` packages. The repository's Windows CI uses `pacboy -S icu:p toolchain:p pkg-config:p` before running Go commands; plain PowerShell/MSVC is not enough for that CGO build.

```powershell
go install github.com/steveyegge/gastown/cmd/gt@latest
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

If you used `brew install gastown`, the binaries are already installed. Verify them:

```bash
gt version
bd version
dolt version
```

On Linux and Windows, install `gt` and `bd` with Go after installing Dolt separately:

```bash
go install github.com/steveyegge/gastown/cmd/gt@latest
go install github.com/steveyegge/beads/cmd/bd@latest
```

Homebrew installs the runtime dependencies declared by the core formula. The
`gastownhall/gastown` tap is reserved for emergency updates. If you build from
source instead, install `dolt` and ICU4C first, install `bd` with Go, and ensure both
`~/.local/bin` and `$GOPATH/bin` (usually `~/go/bin`) appear before older
install locations. On macOS, do not install `gt` with `go install`:
unsigned binaries may be killed by the OS. Clone the repository and use `make`
instead.

```bash
brew install dolt icu4c
go install github.com/steveyegge/beads/cmd/bd@latest
export PATH="$HOME/.local/bin:$HOME/go/bin:$PATH"
git clone https://github.com/steveyegge/gastown.git
cd gastown
make install
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
#   ├── CLAUDE.md          # Identity anchor (run gt prime)
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

Gas Town supports built-in runtimes (`claude`, `gemini`, `codex`, `kiro`, `cursor`, `auggie`, `amp`, `opencode`, `copilot`) plus custom agent aliases.

```bash
# List available agents
gt config agent list

# Create an alias (aliases can encode model/thinking flags)
gt config agent set codex-low "codex --thinking low"
gt config agent set claude-haiku "claude --model haiku --dangerously-skip-permissions"

# Set the town default agent (used when a rig doesn't specify one)
gt config default-agent codex-low
```

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

The Gas Town binary directory is not in PATH. Homebrew usually handles this for
Homebrew installs. Source installs place `gt` in `~/.local/bin`:

```bash
# Add to your shell config (~/.bashrc, ~/.zshrc)
export PATH="$HOME/.local/bin:$PATH"
source ~/.bashrc  # or restart terminal
```

If you also installed Beads with Go, keep `$HOME/go/bin` in PATH for `bd`.

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

Update Gas Town through the same channel you used to install it. For the
recommended Homebrew install:

```bash
brew update
brew upgrade gastown
command -v gt              # Should be Homebrew's gt, e.g. /opt/homebrew/bin/gt
gt version
gt doctor --fix            # Fix any post-update issues
```

If you installed from source, update the checkout and rebuild with `make` rather
than installing `gt` with `go install` on macOS:

```bash
git pull --ff-only
make install
command -v gt              # Should be ~/.local/bin/gt
gt version
gt doctor --fix
```

If you maintain Beads separately from Homebrew, update `bd` from its own source:

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
2. **Try a simple workflow** - `bd create "Test task"` then `gt convoy create "Test" <bead-id>`
3. **Explore docs** - `docs/reference.md` for command reference
4. **Run doctor regularly** - `gt doctor` catches problems early
5. **Join the Wasteland** - `gt wl join hop/wl-commons` to browse and claim federated work (see [WASTELAND.md](WASTELAND.md))
