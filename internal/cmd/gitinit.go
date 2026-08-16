package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var (
	gitInitGitHub string
	gitInitPublic bool
)

var gitInitCmd = &cobra.Command{
	Use:     "git-init",
	GroupID: GroupWorkspace,
	Short:   "Initialize git repository for a Gas Town HQ",
	Long: `Initialize or configure git for an existing Gas Town HQ.

This command:
  1. Creates a comprehensive .gitignore for Gas Town
  2. Initializes a git repository if not already present
  3. Optionally creates a GitHub repository (private by default)

The .gitignore excludes:
  - Polecat worktrees and rig clones (recreated with 'gt sling' or 'gt rig add')
  - Runtime state files (state.json, *.lock)
  - OS and editor files

And tracks:
  - CLAUDE.md and role contexts
  - .beads/ configuration and issues
  - Rig configs and hop/ directory

Examples:
  gt git-init                             # Init git with .gitignore
  gt git-init --github=user/repo          # Create private GitHub repo (default)
  gt git-init --github=user/repo --public # Create public GitHub repo`,
	RunE: runGitInit,
}

func init() {
	gitInitCmd.Flags().StringVar(&gitInitGitHub, "github", "", "Create GitHub repo (format: owner/repo, private by default)")
	gitInitCmd.Flags().BoolVar(&gitInitPublic, "public", false, "Make GitHub repo public (repos are private by default)")
	rootCmd.AddCommand(gitInitCmd)
}

// HQGitignore is the standard .gitignore for Gas Town HQs
const HQGitignore = `# Gas Town HQ .gitignore
# Track: Role context, handoff docs, beads config/data, rig configs
# Ignore: Git worktrees (polecats) and clones (mayor/refinery rigs), runtime state

# =============================================================================
# Runtime state files (transient)
# =============================================================================
**/state.json
**/*.lock
**/*.flock
**/locks/
**/registry.json
**/*.pid
**/heartbeat.json
**/activity.json
.events.jsonl
.feed.jsonl
**/audit.log
**/last-touched
**/.local_version
**/.gt-types-configured
**/feed-*.json

# =============================================================================
# Runtime state directories
# =============================================================================
daemon/
logs/

# =============================================================================
# Centralized Dolt SQL server data directory
# =============================================================================
.dolt-data/

# Dolt internal directories at any level (rig databases, nested .beads dolt)
**/.dolt/
**/.doltcfg/

# =============================================================================
# Event stream storage
# =============================================================================
events/

# =============================================================================
# HQ beads directory
# =============================================================================
beads_hq/

# =============================================================================
# Rig git worktrees (recreate with 'gt sling' or 'gt rig add')
# =============================================================================

# Polecats - worker worktrees
**/polecats/

# Deacon dogs - patrol worker worktrees
**/deacon/dogs/

# Mayor rig clones
**/mayor/rig/

# Refinery working clones
**/refinery/rig/

# Crew workspaces (user-managed)
**/crew/

# =============================================================================
# Runtime state directories (gitignored ephemeral data)
# =============================================================================
**/.runtime/

# =============================================================================
# Rig .beads symlinks (point to ignored mayor/rig/.beads, recreated on setup)
# =============================================================================
# Add rig-specific symlinks here, e.g.:
# gastown/.beads

# =============================================================================
# OS and editor files
# =============================================================================
.DS_Store
*~
*.swp
*.swo
.vscode/
.idea/

# =============================================================================
# Explicitly track (override above patterns)
# =============================================================================
# Note: .beads/ has its own .gitignore that handles database files
# and keeps metadata.json, config file as source of truth
`

func runGitInit(cmd *cobra.Command, args []string) error {
	// Find the HQ root
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	hqRoot, err := workspace.Find(cwd)
	if err != nil || hqRoot == "" {
		return fmt.Errorf("not inside a Gas Town HQ (run 'gt install' first)")
	}

	fmt.Printf("%s Initializing git for HQ at %s\n\n",
		style.Bold.Render("🔧"), style.Dim.Render(hqRoot))

	// Create .gitignore
	gitignorePath := filepath.Join(hqRoot, ".gitignore")
	if err := createGitignore(gitignorePath); err != nil {
		return err
	}

	// Initialize git if needed
	gitDir := filepath.Join(hqRoot, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		if err := initGitRepo(hqRoot); err != nil {
			return err
		}
	} else {
		fmt.Printf("   ✓ Git repository already exists\n")
	}

	// Install pre-checkout hook to prevent accidental branch switches
	if err := InstallPreCheckoutHook(hqRoot); err != nil {
		fmt.Printf("   %s Could not install pre-checkout hook: %v\n", style.Dim.Render("⚠"), err)
	}

	// Create GitHub repo if requested
	if gitInitGitHub != "" {
		if err := createGitHubRepo(hqRoot, gitInitGitHub, !gitInitPublic); err != nil {
			return err
		}
	}

	fmt.Printf("\n%s Git initialization complete!\n", style.Bold.Render("✓"))

	// Show next steps if no GitHub was created
	if gitInitGitHub == "" {
		fmt.Println()
		fmt.Println("Next steps:")
		fmt.Printf("  1. Create initial commit: %s\n",
			style.Dim.Render("git add . && git commit -m 'Initial Gas Town HQ'"))
		fmt.Printf("  2. Create remote repo: %s\n",
			style.Dim.Render("gt git-init --github=user/repo"))
	}

	return nil
}

func createGitignore(path string) error {
	// Check if .gitignore already exists
	if _, err := os.Stat(path); err == nil {
		// Read existing content
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading existing .gitignore: %w", err)
		}

		// Check if it already has Gas Town section
		if strings.Contains(string(content), "Gas Town HQ") {
			fmt.Printf("   ✓ .gitignore already configured for Gas Town\n")
			return nil
		}

		// Append to existing
		combined := string(content) + "\n" + HQGitignore
		if err := os.WriteFile(path, []byte(combined), 0644); err != nil {
			return fmt.Errorf("updating .gitignore: %w", err)
		}
		fmt.Printf("   ✓ Updated .gitignore with Gas Town patterns\n")
		return nil
	}

	// Create new .gitignore
	if err := os.WriteFile(path, []byte(HQGitignore), 0644); err != nil {
		return fmt.Errorf("creating .gitignore: %w", err)
	}
	fmt.Printf("   ✓ Created .gitignore\n")
	return nil
}

func initGitRepo(path string) error {
	cmd := exec.Command("git", "init")
	cmd.Dir = path
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git init failed: %w", err)
	}
	fmt.Printf("   ✓ Initialized git repository\n")
	return nil
}

func createGitHubRepo(hqRoot, repo string, private bool) error {
	// Check if gh CLI is available
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("GitHub CLI (gh) not found. Install it with: brew install gh")
	}

	// Parse owner/repo format
	parts := strings.Split(repo, "/")
	if len(parts) != 2 {
		return fmt.Errorf("invalid GitHub repo format (expected owner/repo): %s", repo)
	}

	visibility := "private"
	if !private {
		visibility = "public"
	}
	fmt.Printf("   → Creating %s GitHub repository %s...\n", visibility, repo)

	// Ensure there's at least one commit before pushing.
	// gh repo create --push fails on empty repos with no commits.
	if err := ensureInitialCommit(hqRoot); err != nil {
		return fmt.Errorf("creating initial commit: %w", err)
	}

	// Build gh repo create command
	args := []string{"repo", "create", repo, "--source", hqRoot}
	if private {
		args = append(args, "--private")
	} else {
		args = append(args, "--public")
	}
	args = append(args, "--push")

	cmd := exec.Command("gh", args...)
	cmd.Dir = hqRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gh repo create failed: %w", err)
	}
	fmt.Printf("   ✓ Created and pushed to GitHub: %s (%s)\n", repo, visibility)
	if private {
		fmt.Printf("   ℹ To make this repo public: %s\n", style.Dim.Render("gh repo edit "+repo+" --visibility public"))
	}
	return nil
}

// ensureInitialCommit creates an initial commit if the repo has no commits.
// gh repo create --push requires at least one commit to push.
func ensureInitialCommit(hqRoot string) error {
	// Check if commits exist
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = hqRoot
	if cmd.Run() == nil {
		return nil
	}

	// Stage and commit
	addCmd := exec.Command("git", "add", ".")
	addCmd.Dir = hqRoot
	if err := addCmd.Run(); err != nil {
		return fmt.Errorf("git add: %w", err)
	}

	commitCmd := exec.Command("git", "commit", "-m", "Initial Gas Town HQ")
	commitCmd.Dir = hqRoot
	if output, err := commitCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit failed: %s", strings.TrimSpace(string(output)))
	}

	fmt.Printf("   ✓ Created initial commit\n")
	return nil
}

// InitGitForHarness is the shared implementation for git initialization.
// It can be called from both 'gt git-init' and 'gt install --git'.
// Note: Function name kept for backwards compatibility.
func InitGitForHarness(hqRoot string, github string, private bool) error {
	// Create .gitignore
	gitignorePath := filepath.Join(hqRoot, ".gitignore")
	if err := createGitignore(gitignorePath); err != nil {
		return err
	}

	// Initialize git if needed
	gitDir := filepath.Join(hqRoot, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		if err := initGitRepo(hqRoot); err != nil {
			return err
		}
	} else {
		fmt.Printf("   ✓ Git repository already exists\n")
	}

	// Install pre-checkout hook to prevent accidental branch switches
	if err := InstallPreCheckoutHook(hqRoot); err != nil {
		fmt.Printf("   %s Could not install pre-checkout hook: %v\n", style.Dim.Render("⚠"), err)
	}

	// Create GitHub repo if requested
	if github != "" {
		if err := createGitHubRepo(hqRoot, github, private); err != nil {
			return err
		}
	}

	return nil
}

// BranchProtectionMarker identifies our branch protection code in post-checkout.
const BranchProtectionMarker = "Gas Town branch protection"

// BranchProtectionScript is the code to prepend to post-checkout hook.
// It auto-reverts to main if a non-main branch was checked out in the town root.
//
// NOTE: Git does NOT support "pre-checkout" hooks. We use post-checkout to
// detect and auto-revert bad checkouts immediately after they happen.
const BranchProtectionScript = `# Gas Town branch protection
# Auto-reverts to main if a non-main branch is checked out in the town root.
# The town root must stay on main to avoid breaking gt commands.
# NOTE: Git does NOT support pre-checkout hooks, so we auto-revert after.

# Only check branch checkouts (not file checkouts)
# $3 is 1 for branch checkout, 0 for file checkout
if [ "$3" = "1" ]; then
    # Get current branch after checkout
    CURRENT_BRANCH=$(git branch --show-current 2>/dev/null)

    # If on main or master, all good
    if [ "$CURRENT_BRANCH" = "main" ] || [ "$CURRENT_BRANCH" = "master" ]; then
        : # OK, continue with rest of hook
    elif [ -n "$CURRENT_BRANCH" ]; then
        # Non-main branch detected - auto-revert!
        echo "" >&2
        echo "⚠️  AUTO-REVERTING: Town root must stay on main branch" >&2
        echo "" >&2
        echo "   Detected checkout to '$CURRENT_BRANCH' in the Gas Town HQ directory." >&2
        echo "   The town root should always be on main. Switching back..." >&2
        echo "" >&2

        # Revert to main
        if git checkout main >/dev/null 2>&1; then
            echo "   ✓ Reverted to main branch" >&2
        elif git checkout master >/dev/null 2>&1; then
            echo "   ✓ Reverted to master branch" >&2
        else
            echo "   ✗ Failed to revert - please run: git checkout main" >&2
        fi
        echo "" >&2
    fi
fi

`

// InstallPreCheckoutHook installs branch protection in the post-checkout hook.
// This auto-reverts accidental branch switches that can break gt commands.
//
// NOTE: The function name is kept for backwards compatibility, but it now
// installs protection in post-checkout (git doesn't support pre-checkout).
func InstallPreCheckoutHook(hqRoot string) error {
	return InstallBranchProtection(hqRoot)
}

// InstallBranchProtection adds branch protection to the post-checkout hook.
// If a non-main branch is checked out in the town root, it auto-reverts to main.
func InstallBranchProtection(hqRoot string) error {
	hooksDir := filepath.Join(hqRoot, ".git", "hooks")

	// Ensure hooks directory exists
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		return fmt.Errorf("creating hooks directory: %w", err)
	}

	// Remove obsolete pre-checkout hook if it's ours
	preCheckoutPath := filepath.Join(hooksDir, "pre-checkout")
	if content, err := os.ReadFile(preCheckoutPath); err == nil {
		if strings.Contains(string(content), "Gas Town pre-checkout hook") {
			_ = os.Remove(preCheckoutPath) // Best effort removal
			fmt.Printf("   ✓ Removed obsolete pre-checkout hook\n")
		}
	}

	hookPath := filepath.Join(hooksDir, "post-checkout")

	// Read existing hook content (if any)
	existingContent, err := os.ReadFile(hookPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading existing hook: %w", err)
	}

	// Check if already has branch protection
	if strings.Contains(string(existingContent), BranchProtectionMarker) {
		fmt.Printf("   ✓ Branch protection already installed\n")
		return nil
	}

	var newContent string
	if len(existingContent) == 0 {
		// No existing hook - create new one with shebang
		newContent = "#!/bin/sh\n" + BranchProtectionScript
	} else {
		// Prepend branch protection after shebang
		content := string(existingContent)
		if strings.HasPrefix(content, "#!") {
			// Find end of shebang line
			idx := strings.Index(content, "\n")
			if idx != -1 {
				newContent = content[:idx+1] + BranchProtectionScript + content[idx+1:]
			} else {
				newContent = content + "\n" + BranchProtectionScript
			}
		} else {
			newContent = "#!/bin/sh\n" + BranchProtectionScript + content
		}
	}

	// Write the hook
	if err := os.WriteFile(hookPath, []byte(newContent), 0755); err != nil {
		return fmt.Errorf("writing hook: %w", err)
	}

	fmt.Printf("   ✓ Installed branch protection (auto-reverts non-main checkouts)\n")
	return nil
}

// IsPreCheckoutHookInstalled checks if branch protection is installed.
// NOTE: Function name kept for backwards compatibility.
func IsPreCheckoutHookInstalled(hqRoot string) bool {
	return IsBranchProtectionInstalled(hqRoot)
}

// IsBranchProtectionInstalled checks if branch protection is in post-checkout.
func IsBranchProtectionInstalled(hqRoot string) bool {
	hookPath := filepath.Join(hqRoot, ".git", "hooks", "post-checkout")

	content, err := os.ReadFile(hookPath)
	if err != nil {
		return false
	}

	return strings.Contains(string(content), BranchProtectionMarker)
}
