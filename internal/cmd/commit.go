package cmd

import (
	"os"
	"os/exec"
	"strings"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

// DefaultAgentEmailDomain is the default domain for agent git emails.
const DefaultAgentEmailDomain = "gastown.local"

var commitCmd = &cobra.Command{
	Use:   "commit [flags] [-- git-commit-args...]",
	Short: "Git commit with automatic agent identity",
	Long: `Git commit wrapper that automatically sets git author identity for agents.

When run by an agent (GT_ROLE set), this command:
1. Detects the agent identity from environment variables
2. Converts it to a git-friendly name and email
3. Runs 'git commit' with the correct identity

The email domain is configurable in town settings (agent_email_domain).
Default: gastown.local

Examples:
  gt commit -m "Fix bug"              # Commit as current agent
  gt commit -am "Quick fix"           # Stage all and commit
  gt commit -- --amend                # Amend last commit

Identity mapping:
  Agent: gastown/crew/jack  →  Name: gastown/crew/jack
                                Email: gastown.crew.jack@gastown.local

When run without GT_ROLE (human), passes through to git commit with no changes.`,
	RunE:               runCommit,
	DisableFlagParsing: true, // We'll parse flags ourselves to pass them to git
}

func init() {
	commitCmd.GroupID = GroupWork
	rootCmd.AddCommand(commitCmd)
}

func runCommit(cmd *cobra.Command, args []string) error {
	// Handle --help since DisableFlagParsing bypasses Cobra's help handling
	if helped, err := checkHelpFlag(cmd, args); helped || err != nil {
		return err
	}

	// Detect agent identity
	identity := detectSender()

	// If overseer (human), just pass through to git commit
	if identity == "overseer" {
		return runGitCommit(args, "", "")
	}

	// Load agent email domain from town settings
	domain := DefaultAgentEmailDomain
	townRoot, err := workspace.FindFromCwd()
	if err == nil && townRoot != "" {
		settings, err := config.LoadOrCreateTownSettings(config.TownSettingsPath(townRoot))
		if err == nil && settings.AgentEmailDomain != "" {
			domain = settings.AgentEmailDomain
		}
	}

	// Convert identity to git-friendly email
	// "gastown/crew/jack" → "gastown.crew.jack@domain"
	email := identityToEmail(identity, domain)

	// Use identity as the author name (human-readable)
	name := identity

	return runGitCommit(args, name, email)
}

// identityToEmail converts a Gas Town identity to a git email address.
// "gastown/crew/jack" → "gastown.crew.jack@domain"
// "mayor/" → "mayor@domain"
func identityToEmail(identity, domain string) string {
	// Remove trailing slash if present
	identity = strings.TrimSuffix(identity, "/")

	// Replace slashes with dots for email local part
	localPart := strings.ReplaceAll(identity, "/", ".")

	return localPart + "@" + domain
}

// runGitCommit executes git commit with optional identity override.
// If name and email are empty, runs git commit with no overrides.
// Preserves git's exit code for proper wrapper behavior.
func runGitCommit(args []string, name, email string) error {
	var gitArgs []string

	// If we have an identity, prepend -c flags
	if name != "" && email != "" {
		gitArgs = append(gitArgs, "-c", "user.name="+name)
		gitArgs = append(gitArgs, "-c", "user.email="+email)
	}

	gitArgs = append(gitArgs, "commit")
	gitArgs = append(gitArgs, args...)

	gitCmd := exec.Command("git", gitArgs...)
	gitCmd.Stdin = os.Stdin
	gitCmd.Stdout = os.Stdout
	gitCmd.Stderr = os.Stderr

	if err := gitCmd.Run(); err != nil {
		// Preserve git's exit code for proper wrapper behavior
		if exitErr, ok := err.(*exec.ExitError); ok {
			return NewSilentExit(exitErr.ExitCode())
		}
		return err
	}
	return nil
}
