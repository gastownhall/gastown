package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonbaldie/gastown/internal/constants"
	"github.com/jonbaldie/gastown/internal/git"
	"github.com/jonbaldie/gastown/internal/rig"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/tmux"
)

// inferRigFromCwd tries to determine the rig from the current directory.
func inferRigFromCwd(townRoot string) (string, error) {
	cwd, err := filepath.Abs(".")
	if err != nil {
		return "", err
	}

	// Check if cwd is within a rig
	rel, err := filepath.Rel(townRoot, cwd)
	if err != nil {
		return "", fmt.Errorf("not in workspace")
	}

	// Normalize and split path - first component is the rig name
	rel = filepath.ToSlash(rel)
	parts := strings.Split(rel, "/")

	if len(parts) > 0 && parts[0] != "" && parts[0] != "." {
		return parts[0], nil
	}

	return "", fmt.Errorf("could not infer rig from current directory")
}

// inferRigFromCrewName scans all rigs in the town root for a crew member
// with the given name. Returns the rig name if the crew member is unique
// across all rigs. Returns an error if not found or ambiguous.
func inferRigFromCrewName(townRoot, crewName string) (string, error) {
	entries, err := os.ReadDir(townRoot)
	if err != nil {
		return "", fmt.Errorf("reading town root: %w", err)
	}

	var matches []string
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		crewPath := filepath.Join(townRoot, entry.Name(), "crew", crewName)
		if info, err := os.Stat(crewPath); err == nil && info.IsDir() {
			matches = append(matches, entry.Name())
		}
	}

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no rig found with crew member %q", crewName)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("crew member %q exists in multiple rigs: %s (use --rig to specify)",
			crewName, strings.Join(matches, ", "))
	}
}

// parseRigSlashName parses "rig/name" format into separate rig and name parts.
// Returns (rig, name, true) if the format matches, or ("", original, false) if not.
// Examples:
//   - "beads/emma" -> ("beads", "emma", true)
//   - "emma" -> ("", "emma", false)
//   - "beads/crew/emma" -> ("beads", "crew/emma", true) - only first slash splits
func parseRigSlashName(input string) (rigName, name string, ok bool) {
	// Only split on first slash to handle edge cases
	idx := strings.Index(input, "/")
	if idx == -1 {
		return "", input, false
	}
	return input[:idx], input[idx+1:], true
}

// isInTmuxSession checks if we're currently inside the target tmux session.
func isInTmuxSession(targetSession string) bool {
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		return false // Not in tmux at all
	}

	// Get current session name, targeting our specific pane
	cmd := tmux.BuildCommand("display-message", "-t", pane, "-p", "#{session_name}")
	out, err := cmd.Output()
	if err != nil {
		return false
	}

	currentSession := strings.TrimSpace(string(out))
	return currentSession == targetSession
}

// isInSameTmuxSocket checks if we're inside a tmux session on the same socket
// as the current town. Used to decide between switch-client and attach-session.
func isInSameTmuxSocket() bool {
	return tmux.IsInSameSocket()
}

// isShellCommand checks if the command is a shell (meaning the runtime has exited).
func isShellCommand(cmd string) bool {
	shells := constants.SupportedShells
	for _, shell := range shells {
		if cmd == shell {
			return true
		}
	}
	return false
}

// ensureDefaultBranch checks if a git directory is on the default branch.
// ensureDefaultBranch checks out the configured default branch and pulls latest.
// Returns an error if the checkout or pull fails.
func ensureDefaultBranch(dir, roleName, rigPath string) error {
	g := git.NewGit(dir)

	branch, err := g.CurrentBranch()
	if err != nil {
		// Not a git repo or other error, skip check
		return fmt.Errorf("could not determine current branch: %w", err)
	}

	// Get configured default branch for this rig
	defaultBranch := "main" // fallback
	if rigCfg, err := rig.LoadRigConfig(rigPath); err == nil && rigCfg.DefaultBranch != "" {
		defaultBranch = rigCfg.DefaultBranch
	}

	if branch == defaultBranch {
		// Already on default branch — still pull to ensure up-to-date
		if err := g.Pull("origin", defaultBranch); err != nil {
			return fmt.Errorf("pull failed on %s: %w", defaultBranch, err)
		}
		fmt.Printf("  %s Already on %s, pulled latest\n", style.Success.Render("✓"), defaultBranch)
		return nil
	}

	// Not on default branch — switch to it
	fmt.Printf("  %s is on branch '%s', switching to %s...\n", roleName, branch, defaultBranch)
	if err := g.Checkout(defaultBranch); err != nil {
		return fmt.Errorf("could not switch to %s: %w", defaultBranch, err)
	}

	// Pull latest
	if err := g.Pull("origin", defaultBranch); err != nil {
		return fmt.Errorf("pull failed on %s: %w", defaultBranch, err)
	}
	fmt.Printf("  %s Switched to %s and pulled latest\n", style.Success.Render("✓"), defaultBranch)

	return nil
}

// warnIfNotDefaultBranch prints a warning if the workspace is not on the
// configured default branch. Used when --reset is not set to alert users
// before an agent wastes its context window on a branch that can't push.
func warnIfNotDefaultBranch(dir, roleName, rigPath string) {
	g := git.NewGit(dir)

	branch, err := g.CurrentBranch()
	if err != nil {
		return
	}

	defaultBranch := "main"
	if rigCfg, err := rig.LoadRigConfig(rigPath); err == nil && rigCfg.DefaultBranch != "" {
		defaultBranch = rigCfg.DefaultBranch
	}

	if branch == defaultBranch {
		return
	}

	fmt.Printf("\n%s %s is on branch '%s', not '%s'.\n",
		style.Warning.Render("⚠"),
		roleName,
		branch,
		defaultBranch)
	fmt.Printf("  Use --reset to switch to %s, or continue at your own risk.\n\n", defaultBranch)
}
