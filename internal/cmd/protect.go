package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
)

var (
	protectReason string
	protectCheck  bool
)

var protectCmd = &cobra.Command{
	Use:     "protect",
	GroupID: GroupWork,
	Short:   "Protect session from handoff (e.g., during plan mode)",
	Long: `Mark the current session as protected, preventing handoff from respawning it.

Protected sessions block gt handoff (both self and remote) unless --force is used.
Protection expires automatically after 60 minutes to prevent stale locks.

Use --check to test protection status (exit 0 if protected, exit 1 if not).

Examples:
  gt protect                          # Protect with default reason
  gt protect --reason plan-mode       # Protect with specific reason
  gt protect --check                  # Check if current session is protected
  gt unprotect                        # Remove protection`,
	RunE: runProtect,
}

var unprotectCmd = &cobra.Command{
	Use:     "unprotect",
	GroupID: GroupWork,
	Short:   "Remove session protection, allowing handoff",
	Long: `Clear the session protection marker, allowing gt handoff to respawn this session.

Examples:
  gt unprotect                        # Remove protection from current session`,
	RunE: runUnprotect,
}

func init() {
	protectCmd.Flags().StringVar(&protectReason, "reason", "protected", "Reason for protection")
	protectCmd.Flags().BoolVar(&protectCheck, "check", false, "Check protection status (exit 0=protected, 1=not)")
	rootCmd.AddCommand(protectCmd)
	rootCmd.AddCommand(unprotectCmd)
}

func runProtect(cmd *cobra.Command, args []string) error {
	sessionName, t, err := resolveCurrentSessionForProtect()
	if err != nil {
		return err
	}

	if protectCheck {
		protected, reason := t.IsSessionProtected(sessionName, constants.MaxSessionProtectionDuration)
		if protected {
			fmt.Printf("Session %s is protected (%s)\n", sessionName, reason)
			return nil // exit 0
		}
		return NewSilentExit(1) // exit 1 = not protected
	}

	if err := t.SetSessionProtected(sessionName, protectReason); err != nil {
		return fmt.Errorf("setting session protection: %w", err)
	}
	fmt.Printf("%s Session %s protected (%s)\n", style.Bold.Render("🛡️"), sessionName, protectReason)
	return nil
}

func runUnprotect(cmd *cobra.Command, args []string) error {
	sessionName, t, err := resolveCurrentSessionForProtect()
	if err != nil {
		return err
	}

	if err := t.ClearSessionProtected(sessionName); err != nil {
		// Ignore errors from unsetting a var that doesn't exist
		_ = err
	}
	fmt.Printf("%s Session %s protection cleared\n", style.Bold.Render("🔓"), sessionName)
	return nil
}

// resolveCurrentSessionForProtect returns the current session name and a Tmux
// instance suitable for session environment operations.
func resolveCurrentSessionForProtect() (string, *tmux.Tmux, error) {
	if !tmux.IsInsideTmux() {
		return "", nil, fmt.Errorf("not running in tmux")
	}

	// Use GT_SESSION or fall back to tmux display-message
	sessionName := os.Getenv("GT_SESSION")
	if sessionName == "" {
		var err error
		sessionName, err = getCurrentTmuxSession()
		if err != nil {
			return "", nil, fmt.Errorf("getting session name: %w", err)
		}
	}

	callerSocket := tmux.SocketFromEnv()
	t := tmux.NewTmuxWithSocket(callerSocket)
	return sessionName, t, nil
}
