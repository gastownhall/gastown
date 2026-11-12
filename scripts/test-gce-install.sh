#!/bin/bash
# test-gce-install.sh - Test Gas Town installation on fresh GCE VM
#
# Usage:
#   # Create a fresh Debian/Ubuntu VM on GCE, then:
#   curl -fsSL https://raw.githubusercontent.com/steveyegge/gastown/main/scripts/test-gce-install.sh | bash
#
#   # Or clone and run locally:
#   ./scripts/test-gce-install.sh
#
# This script:
#   1. Installs all prerequisites (Go, git, tmux, beads, Claude Code)
#   2. Installs Gas Town
#   3. Runs verification tests
#   4. Reports success/failure

set -e

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log() { echo -e "${GREEN}[+]${NC} $1"; }
warn() { echo -e "${YELLOW}[!]${NC} $1"; }
fail() { echo -e "${RED}[X]${NC} $1"; exit 1; }
check() { echo -e "${GREEN}[✓]${NC} $1"; }

echo "================================================"
echo "  Gas Town GCE Installation Test"
echo "  $(date)"
echo "================================================"
echo

# Detect OS
if [[ -f /etc/os-release ]]; then
    . /etc/os-release
    OS=$ID
    log "Detected OS: $OS ($VERSION_ID)"
else
    fail "Cannot detect OS"
fi

# ============================================
# STEP 1: Install Prerequisites
# ============================================
log "Installing prerequisites..."

# Update package manager
if [[ "$OS" == "ubuntu" ]]; then
    # The Ubuntu archive has no git fixed for CVE-2026-105437; the git-core PPA does.
    sudo apt-get update -qq
    sudo apt-get install -y -qq software-properties-common
    sudo add-apt-repository -y ppa:git-core/ppa
    sudo apt-get update -qq
    sudo apt-get install -y -qq git tmux curl
elif [[ "$OS" == "debian" ]]; then
    sudo apt-get update -qq
    sudo apt-get install -y -qq git tmux curl
elif [[ "$OS" == "fedora" || "$OS" == "centos" || "$OS" == "rhel" ]]; then
    sudo dnf install -y git tmux curl
    # dnf install leaves a preinstalled git alone; pull any newer security build.
    sudo dnf upgrade -y --refresh git
else
    warn "Unknown OS, assuming deps are installed"
fi

# git_is_fixed VERSION: succeeds if VERSION carries the CVE-2026-105437 fix
# (2.50.2, 2.51.3, 2.52.1, 2.53.1, 2.54.1, 2.55.1, 2.56.1, or 2.57+).
git_is_fixed() {
    local major minor patch
    IFS=. read -r major minor patch _ <<< "$1"
    patch=${patch%%[!0-9]*}
    patch=${patch:-0}
    if (( major > 2 || (major == 2 && minor >= 57) )); then
        return 0
    fi
    (( major < 2 || minor < 50 )) && return 1
    local -A fixed_patch=([50]=2 [51]=3 [52]=1 [53]=1 [54]=1 [55]=1 [56]=1)
    (( patch >= ${fixed_patch[$minor]} ))
}

if command -v git &> /dev/null; then
    GIT_VERSION=$(git --version | awk '{print $3}')
    if git_is_fixed "$GIT_VERSION"; then
        check "git $GIT_VERSION (fixed for CVE-2026-105437)"
    else
        warn "git $GIT_VERSION is vulnerable to CVE-2026-105437 (need 2.56.1+ or a fixed maint release); no fixed package is available from the enabled repos yet"
    fi
else
    fail "git is not installed"
fi

# Install Go 1.26.10+ (earlier releases trust the compromised eMudhra intermediates)
GO_MIN_VERSION=1.26.10
if command -v go &> /dev/null; then
    GO_VERSION=$(go version | grep -oP 'go\K[0-9]+\.[0-9]+(\.[0-9]+)?')
    if [[ "$(printf '%s\n' "$GO_MIN_VERSION" "$GO_VERSION" | sort -V | head -1)" == "$GO_MIN_VERSION" ]]; then
        check "Go $GO_VERSION already installed"
    else
        warn "Go $GO_VERSION too old, installing $GO_MIN_VERSION..."
        INSTALL_GO=1
    fi
else
    INSTALL_GO=1
fi

if [[ -n "$INSTALL_GO" ]]; then
    log "Installing Go $GO_MIN_VERSION..."
    sudo rm -rf /usr/local/go
    curl -fsSL "https://go.dev/dl/go${GO_MIN_VERSION}.linux-amd64.tar.gz" | sudo tar -C /usr/local -xzf -
    export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin
    echo 'export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin' >> ~/.bashrc
    check "Go installed: $(go version)"
fi

# Ensure GOBIN is in PATH
export PATH=$PATH:$HOME/go/bin

# ============================================
# STEP 2: Install Beads
# ============================================
log "Installing beads (bd)..."

if command -v bd &> /dev/null; then
    check "beads already installed: $(bd --version)"
else
    # Install via go install
    go install github.com/steveyegge/beads/cmd/bd@latest
    if command -v bd &> /dev/null; then
        check "beads installed: $(bd --version)"
    else
        fail "beads installation failed"
    fi
fi

# ============================================
# STEP 3: Install Gas Town
# ============================================
log "Installing Gas Town (gt)..."

go install github.com/steveyegge/gastown/cmd/gt@latest

if command -v gt &> /dev/null; then
    check "gt installed: $(gt --version 2>/dev/null || echo 'version unknown')"
else
    fail "gt installation failed - check PATH includes ~/go/bin"
fi

# ============================================
# STEP 4: Create Test Workspace
# ============================================
TEST_DIR="$HOME/gt-test-$$"
log "Creating test workspace at $TEST_DIR..."

gt install "$TEST_DIR" --name test-town

if [[ -d "$TEST_DIR" && -f "$TEST_DIR/CLAUDE.md" ]]; then
    check "Workspace created successfully"
else
    fail "Workspace creation failed"
fi

cd "$TEST_DIR"

# ============================================
# STEP 5: Verification Tests
# ============================================
log "Running verification tests..."

# Test 1: gt status
if gt status &> /dev/null; then
    check "gt status works"
else
    warn "gt status failed (might be OK without daemon)"
fi

# Test 2: beads init
if [[ -d ".beads" ]]; then
    check ".beads directory exists"
else
    fail ".beads directory not created"
fi

# Test 3: bd commands work
if bd stats &> /dev/null; then
    check "bd stats works"
else
    warn "bd stats failed"
fi

# Test 4: Check for hardcoded paths
log "Checking for hardcoded paths..."
if grep -r "/Users/stevey" "$TEST_DIR" 2>/dev/null; then
    warn "Found hardcoded /Users/stevey paths!"
else
    check "No hardcoded user paths found"
fi

# Test 5: gt doctor
log "Running gt doctor..."
if gt doctor 2>&1 | tee /tmp/gt-doctor.log; then
    check "gt doctor passed"
else
    warn "gt doctor reported issues (see /tmp/gt-doctor.log)"
fi

# ============================================
# STEP 6: Test rig add (optional - needs real repo)
# ============================================
log "Testing rig add with sample repo..."

# Use a small public repo for testing
if gt rig add test-rig --remote=https://github.com/steveyegge/beads.git 2>&1; then
    check "gt rig add works"

    # Verify rig structure
    if [[ -d "beads" ]]; then
        check "Rig directory created"
    fi
else
    warn "gt rig add failed (might need auth)"
fi

# ============================================
# STEP 7: Claude Code CLI Check
# ============================================
log "Checking Claude Code CLI..."

if command -v claude &> /dev/null; then
    check "Claude Code CLI found: $(claude --version 2>/dev/null || echo 'installed')"
else
    warn "Claude Code CLI not installed"
    echo "  Install from: https://claude.ai/code"
    echo "  Gas Town works without it, but agents won't spawn"
fi

# ============================================
# Cleanup
# ============================================
log "Cleaning up test workspace..."
rm -rf "$TEST_DIR"
check "Cleanup complete"

# ============================================
# Summary
# ============================================
echo
echo "================================================"
echo "  Installation Test Complete"
echo "================================================"
echo
echo "Prerequisites installed:"
echo "  - Go: $(go version | grep -oP 'go[0-9]+\.[0-9]+\.[0-9]+')"
echo "  - Git: $(git --version | grep -oP '[0-9]+\.[0-9]+\.[0-9]+')"
echo "  - tmux: $(tmux -V | grep -oP '[0-9]+\.[0-9]+')"
echo "  - beads: $(bd --version 2>/dev/null | grep -oP '[0-9]+\.[0-9]+\.[0-9]+' || echo 'installed')"
echo "  - gt: installed"
if command -v claude &> /dev/null; then
    echo "  - Claude Code: installed"
else
    echo "  - Claude Code: NOT INSTALLED (optional for basic usage)"
fi
echo
echo "Gas Town is ready to use!"
echo "  gt install ~/my-workspace"
echo "  cd ~/my-workspace"
echo "  gt rig add myproject --remote=<url>"
echo
