package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/agentaddr"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/suggest"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/townlog"
	"github.com/steveyegge/gastown/internal/workspace"
)

// Session command flags
var (
	sessionIssue               string
	sessionForce               bool
	sessionLines               int
	sessionMessage             string
	sessionFile                string
	sessionRigFilter           string
	sessionListJSON            bool
	sessionStatusJSON          bool
	sessionHealthJSON          bool
	sessionHealthMaxInactivity time.Duration
)

var sessionCmd = &cobra.Command{
	Use:     "session",
	Aliases: []string{"sess"},
	GroupID: GroupAgents,
	Short:   "Manage polecat sessions",
	RunE:    requireSubcommand,
	Long: `Manage tmux sessions for polecats.

Sessions are tmux sessions running Claude for each polecat.
Use the subcommands to start, stop, attach, and monitor sessions.

TIP: To send messages to a running session, use 'gt nudge' (not 'session inject').
The nudge command uses reliable delivery that works correctly with Claude Code.`,
}

var sessionStartCmd = &cobra.Command{
	Use:   "start <rig>/<polecat>",
	Short: "Start a polecat session",
	Long: `Start a new tmux session for a polecat.

Creates a tmux session, navigates to the polecat's working directory,
and launches claude. Optionally inject an initial issue to work on.

Examples:
  gt session start wyvern/Toast
  gt session start wyvern/Toast --issue gt-123`,
	Args: cobra.ExactArgs(1),
	RunE: runSessionStart,
}

var sessionStopCmd = &cobra.Command{
	Use:   "stop <rig>/<polecat>",
	Short: "Stop a polecat session",
	Long: `Stop a running polecat session.

Attempts graceful shutdown first (Ctrl-C), then kills the tmux session.
Use --force to skip graceful shutdown.`,
	Args: cobra.ExactArgs(1),
	RunE: runSessionStop,
}

var sessionAtCmd = &cobra.Command{
	Use:     "at <rig>/<polecat>",
	Aliases: []string{"attach"},
	Short:   "Attach to a running session",
	Long: `Attach to a running polecat session.

Attaches the current terminal to the tmux session. Detach with Ctrl-B D.`,
	Args: cobra.ExactArgs(1),
	RunE: runSessionAttach,
}

var sessionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all sessions",
	Long: `List all running polecat sessions.

Shows session status, rig, and polecat name. Use --rig to filter by rig.`,
	RunE: runSessionList,
}

var sessionCaptureCmd = &cobra.Command{
	Use:   "capture <rig>/<polecat> [count]",
	Short: "Capture recent session output",
	Long: `Capture recent output from a polecat session.

Returns the last N lines of terminal output. Useful for checking progress.

Examples:
  gt session capture wyvern/Toast        # Last 100 lines (default)
  gt session capture wyvern/Toast 50     # Last 50 lines
  gt session capture wyvern/Toast -n 50  # Same as above`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runSessionCapture,
}

var sessionInjectCmd = &cobra.Command{
	Use:   "inject <rig>/<polecat>",
	Short: "Send message to session (prefer 'gt nudge')",
	Long: `Send a message to a polecat session.

NOTE: For sending messages to Claude sessions, use 'gt nudge' instead.
It uses reliable delivery (literal mode + timing) that works correctly
with Claude Code's input handling.

This command is a low-level primitive for file-based injection or
cases where you need raw tmux send-keys behavior.

Examples:
  gt nudge greenplace/furiosa "Check your mail"     # Preferred
  gt session inject wyvern/Toast -f prompt.txt   # For file injection`,
	Args: cobra.ExactArgs(1),
	RunE: runSessionInject,
}

var sessionRestartCmd = &cobra.Command{
	Use:   "restart <rig>/<polecat>",
	Short: "Restart a polecat session",
	Long: `Restart a polecat session (stop + start).

Gracefully stops the current session and starts a fresh one.
Use --force to skip graceful shutdown.`,
	Args: cobra.ExactArgs(1),
	RunE: runSessionRestart,
}

var sessionStatusCmd = &cobra.Command{
	Use:   "status <rig>/<polecat>",
	Short: "Show session status details",
	Long: `Show detailed status for a polecat session.

Displays running state, uptime, session info, and activity.`,
	Args: cobra.ExactArgs(1),
	RunE: runSessionStatus,
}

var sessionCheckCmd = &cobra.Command{
	Use:   "check [rig]",
	Short: "Check session health for polecats",
	Long: `Check if polecat tmux sessions are alive and healthy.

This command validates that:
1. Polecats with work-on-hook have running tmux sessions
2. Sessions are responsive

Use this for manual health checks or debugging session issues.

Examples:
  gt session check              # Check all rigs
  gt session check greenplace      # Check specific rig`,
	Args: cobra.MaximumNArgs(1),
	RunE: runSessionCheck,
}

var sessionHealthCmd = &cobra.Command{
	Use:   "health <tmux-session>",
	Short: "Check a tmux agent session with central runtime liveness",
	Long: `Check a tmux agent session using the central runtime-aware liveness path.

This wraps tmux.CheckSessionHealth, which reads GT_PROCESS_NAMES/GT_AGENT from
the session environment before falling back to built-in agent process names.

The command exits successfully for all valid health states; inspect the status
field when using --json. Operational failures, argument errors, or invalid flags
return non-zero.

Examples:
  gt session health gt-vault --json
  gt session health gt-vault --json --max-inactivity 30m`,
	Args: cobra.ExactArgs(1),
	RunE: runSessionHealth,
}

func init() {
	// Start flags
	sessionStartCmd.Flags().StringVar(&sessionIssue, "issue", "", "Issue ID to work on")

	// Stop flags
	sessionStopCmd.Flags().BoolVarP(&sessionForce, "force", "f", false, "Force immediate shutdown")

	// List flags
	sessionListCmd.Flags().StringVar(&sessionRigFilter, "rig", "", "Filter by rig name")
	sessionListCmd.Flags().BoolVar(&sessionListJSON, "json", false, "Output as JSON")

	// Capture flags
	sessionCaptureCmd.Flags().IntVarP(&sessionLines, "lines", "n", 100, "Number of lines to capture")

	// Inject flags
	sessionInjectCmd.Flags().StringVarP(&sessionMessage, "message", "m", "", "Message to inject")
	sessionInjectCmd.Flags().StringVarP(&sessionFile, "file", "f", "", "File to read message from")

	// Restart flags
	sessionRestartCmd.Flags().BoolVarP(&sessionForce, "force", "f", false, "Force immediate shutdown")

	// Status flags
	sessionStatusCmd.Flags().BoolVar(&sessionStatusJSON, "json", false, "Output as JSON")

	// Health flags
	sessionHealthCmd.Flags().BoolVar(&sessionHealthJSON, "json", false, "Output as JSON")
	sessionHealthCmd.Flags().DurationVar(&sessionHealthMaxInactivity, "max-inactivity", 0, "Maximum tmux inactivity before reporting agent-hung (0 disables activity check)")

	// Add subcommands
	sessionCmd.AddCommand(sessionStartCmd)
	sessionCmd.AddCommand(sessionStopCmd)
	sessionCmd.AddCommand(sessionAtCmd)
	sessionCmd.AddCommand(sessionListCmd)
	sessionCmd.AddCommand(sessionCaptureCmd)
	sessionCmd.AddCommand(sessionInjectCmd)
	sessionCmd.AddCommand(sessionRestartCmd)
	sessionCmd.AddCommand(sessionStatusCmd)
	sessionCmd.AddCommand(sessionCheckCmd)
	sessionCmd.AddCommand(sessionHealthCmd)

	rootCmd.AddCommand(sessionCmd)
}

type sessionHealthReport struct {
	Session              string `json:"session"`
	Status               string `json:"status"`
	Healthy              bool   `json:"healthy"`
	Zombie               bool   `json:"zombie"`
	MaxInactivitySeconds int64  `json:"max_inactivity_seconds"`
}

func newSessionHealthReport(session string, status tmux.ZombieStatus, maxInactivity time.Duration) sessionHealthReport {
	return sessionHealthReport{
		Session:              session,
		Status:               status.String(),
		Healthy:              status == tmux.SessionHealthy,
		Zombie:               status.IsZombie(),
		MaxInactivitySeconds: int64(maxInactivity.Seconds()),
	}
}

// parseAddress parses "rig/polecat" format.
// If no "/" is present, attempts to infer rig from current directory.
//
// The canonical worker forms ("rig/polecats/name", "rig/crew/name") are
// recognised through agentaddr, so the address a bead stores is also the
// address these commands accept (gt-cw1). Anything else keeps the historic
// split-on-first-slash behaviour, which callers rely on for rig-level roles
// such as "gastown/witness".
func parseAddress(addr string) (rigName, polecatName string, err error) {
	if parsed, ok := agentaddr.Parse(addr); ok {
		switch parsed.Role {
		case agentaddr.RolePolecat, agentaddr.RoleCrew:
			if parsed.Rig != "" && parsed.Name != "" {
				return parsed.Rig, parsed.Name, nil
			}
		}
	}

	parts := strings.SplitN(addr, "/", 2)
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		return parts[0], parts[1], nil
	}

	// No slash - try to infer rig from cwd
	if !strings.Contains(addr, "/") && addr != "" {
		townRoot, err := workspace.FindFromCwd()
		if err == nil && townRoot != "" {
			inferredRig, err := inferRigFromCwd(townRoot)
			if err == nil && inferredRig != "" {
				return inferredRig, addr, nil
			}
		}
	}

	return "", "", fmt.Errorf("invalid address format: expected 'rig/polecat', got '%s'", addr)
}

// getSessionManager creates a session manager for the given rig.
func getSessionManager(rigName string) (*polecat.SessionManager, *rig.Rig, error) {
	_, r, err := getRig(rigName)
	if err != nil {
		return nil, nil, err
	}

	t := tmux.NewTmux()
	polecatMgr := polecat.NewSessionManager(t, r)

	return polecatMgr, r, nil
}

func runSessionStart(cmd *cobra.Command, args []string) error {
	rigName, polecatName, err := parseAddress(args[0])
	if err != nil {
		return err
	}

	polecatMgr, r, err := getSessionManager(rigName)
	if err != nil {
		return err
	}

	// Check polecat exists
	found := false
	for _, p := range r.Polecats {
		if p == polecatName {
			found = true
			break
		}
	}
	if !found {
		suggestions := suggest.FindSimilar(polecatName, r.Polecats, 3)
		hint := fmt.Sprintf("Create with: gt polecat identity add %s %s", rigName, polecatName)
		return fmt.Errorf("%s", suggest.FormatSuggestion("Polecat", polecatName, suggestions, hint))
	}

	opts := polecat.SessionStartOptions{
		Issue: sessionIssue,
	}

	fmt.Printf("Starting session for %s/%s...\n", rigName, polecatName)
	if err := polecatMgr.Start(polecatName, opts); err != nil {
		return fmt.Errorf("starting session: %w", err)
	}

	fmt.Printf("%s Session started. Attach with: %s\n",
		style.Bold.Render("✓"),
		style.Dim.Render(fmt.Sprintf("gt session at %s/%s", rigName, polecatName)))

	// Log wake event
	if townRoot, err := workspace.FindFromCwd(); err == nil && townRoot != "" {
		agent := fmt.Sprintf("%s/%s", rigName, polecatName)
		logger := townlog.NewLogger(townRoot)
		_ = logger.Log(townlog.EventWake, agent, sessionIssue)
	}

	return nil
}

func runSessionStop(cmd *cobra.Command, args []string) error {
	rigName, polecatName, err := parseAddress(args[0])
	if err != nil {
		return err
	}

	polecatMgr, _, err := getSessionManager(rigName)
	if err != nil {
		return err
	}

	if sessionForce {
		fmt.Printf("Force stopping session for %s/%s...\n", rigName, polecatName)
	} else {
		fmt.Printf("Stopping session for %s/%s...\n", rigName, polecatName)
	}
	if err := polecatMgr.Stop(polecatName, sessionForce); err != nil {
		return fmt.Errorf("stopping session: %w", err)
	}

	fmt.Printf("%s Session stopped.\n", style.Bold.Render("✓"))

	// Log kill event
	if townRoot, err := workspace.FindFromCwd(); err == nil && townRoot != "" {
		agent := fmt.Sprintf("%s/%s", rigName, polecatName)
		reason := "gt session stop"
		if sessionForce {
			reason = "gt session stop --force"
		}
		logger := townlog.NewLogger(townRoot)
		_ = logger.Log(townlog.EventKill, agent, reason)
	}

	return nil
}

func runSessionAttach(cmd *cobra.Command, args []string) error {
	rigName, polecatName, err := parseAddress(args[0])
	if err != nil {
		return err
	}

	polecatMgr, _, err := getSessionManager(rigName)
	if err != nil {
		return err
	}

	running, err := polecatMgr.IsRunning(polecatName)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}
	if !running {
		return polecat.ErrSessionNotFound
	}

	// Hand the terminal off to tmux via syscall.Exec so tmux inherits our
	// controlling TTY directly. Running tmux as a subprocess with buffered
	// stdio triggers "open terminal failed: not a terminal".
	return attachToTmuxSession(polecatMgr.SessionName(polecatName))
}

// SessionListItem represents a session in list output.
type SessionListItem struct {
	Rig       string `json:"rig"`
	Polecat   string `json:"polecat"`
	SessionID string `json:"session_id"`
	Running   bool   `json:"running"`
}

func runSessionList(cmd *cobra.Command, args []string) error {
	// Find town root
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Load rigs config
	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		rigsConfig = &config.RigsConfig{Rigs: make(map[string]config.RigEntry)}
	}

	// Get all rigs
	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)
	rigs, err := rigMgr.DiscoverRigs()
	if err != nil {
		return fmt.Errorf("discovering rigs: %w", err)
	}

	// Filter if requested
	if sessionRigFilter != "" {
		var filtered []*rig.Rig
		for _, r := range rigs {
			if r.Name == sessionRigFilter {
				filtered = append(filtered, r)
			}
		}
		rigs = filtered
	}

	// Collect sessions from all rigs
	t := tmux.NewTmux()
	var allSessions []SessionListItem

	for _, r := range rigs {
		polecatMgr := polecat.NewSessionManager(t, r)
		infos, err := polecatMgr.List()
		if err != nil {
			continue
		}

		for _, info := range infos {
			allSessions = append(allSessions, SessionListItem{
				Rig:       r.Name,
				Polecat:   info.Polecat,
				SessionID: info.SessionID,
				Running:   info.Running,
			})
		}
	}

	// Output
	if sessionListJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(allSessions)
	}

	if len(allSessions) == 0 {
		fmt.Println("No active sessions.")
		return nil
	}

	fmt.Printf("%s\n\n", style.Bold.Render("Active Sessions"))
	for _, s := range allSessions {
		status := style.Bold.Render("●")
		if !s.Running {
			status = style.Dim.Render("○")
		}
		fmt.Printf("  %s %s/%s\n", status, s.Rig, s.Polecat)
		fmt.Printf("    %s\n", style.Dim.Render(s.SessionID))
	}

	return nil
}

func runSessionCapture(cmd *cobra.Command, args []string) error {
	rigName, polecatName, err := parseAddress(args[0])
	if err != nil {
		return err
	}

	polecatMgr, _, err := getSessionManager(rigName)
	if err != nil {
		return err
	}

	// Use positional count if provided, otherwise use flag value
	lines := sessionLines
	if len(args) > 1 {
		n, err := strconv.Atoi(args[1])
		if err != nil {
			return fmt.Errorf("invalid line count '%s': must be a number", args[1])
		}
		if n <= 0 {
			return fmt.Errorf("line count must be positive, got %d", n)
		}
		lines = n
	}

	output, err := polecatMgr.Capture(polecatName, lines)
	if err != nil {
		return fmt.Errorf("capturing output: %w", err)
	}

	fmt.Print(output)
	return nil
}

func runSessionInject(cmd *cobra.Command, args []string) error {
	rigName, polecatName, err := parseAddress(args[0])
	if err != nil {
		return err
	}

	// Get message
	message := sessionMessage
	if sessionFile != "" {
		data, err := os.ReadFile(sessionFile)
		if err != nil {
			return fmt.Errorf("reading file: %w", err)
		}
		message = string(data)
	}

	if message == "" {
		return fmt.Errorf("no message provided (use -m or -f)")
	}

	polecatMgr, _, err := getSessionManager(rigName)
	if err != nil {
		return err
	}

	if err := polecatMgr.Inject(polecatName, message); err != nil {
		return fmt.Errorf("injecting message: %w", err)
	}

	fmt.Printf("%s Message sent to %s/%s\n",
		style.Bold.Render("✓"), rigName, polecatName)
	return nil
}

func runSessionRestart(cmd *cobra.Command, args []string) error {
	rigName, polecatName, err := parseAddress(args[0])
	if err != nil {
		return err
	}

	polecatMgr, _, err := getSessionManager(rigName)
	if err != nil {
		return err
	}

	// Check if running
	running, err := polecatMgr.IsRunning(polecatName)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}

	if running {
		// Stop first
		if sessionForce {
			fmt.Printf("Force stopping session for %s/%s...\n", rigName, polecatName)
		} else {
			fmt.Printf("Stopping session for %s/%s...\n", rigName, polecatName)
		}
		if err := polecatMgr.Stop(polecatName, sessionForce); err != nil {
			return fmt.Errorf("stopping session: %w", err)
		}

		// Wait for session to fully terminate before starting a new one.
		// Without this, Start may fail or create a duplicate if the old
		// session hasn't been cleaned up by tmux yet.
		for i := 0; i < 10; i++ {
			still, _ := polecatMgr.IsRunning(polecatName)
			if !still {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	// Start fresh session
	fmt.Printf("Starting session for %s/%s...\n", rigName, polecatName)
	opts := polecat.SessionStartOptions{}
	if err := polecatMgr.Start(polecatName, opts); err != nil {
		return fmt.Errorf("starting session: %w", err)
	}

	fmt.Printf("%s Session restarted. Attach with: %s\n",
		style.Bold.Render("✓"),
		style.Dim.Render(fmt.Sprintf("gt session at %s/%s", rigName, polecatName)))
	return nil
}

func runSessionStatus(cmd *cobra.Command, args []string) error {
	rigName, polecatName, err := parseAddress(args[0])
	if err != nil {
		return err
	}

	polecatMgr, _, err := getSessionManager(rigName)
	if err != nil {
		return err
	}

	info, err := polecatMgr.Status(polecatName)
	if err != nil {
		return fmt.Errorf("getting status: %w", err)
	}

	if sessionStatusJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(info)
	}

	fmt.Printf("%s Session: %s/%s\n\n", style.Bold.Render("📺"), rigName, polecatName)

	if info.Running {
		fmt.Printf("  State: %s\n", style.Bold.Render("● running"))
	} else {
		fmt.Printf("  State: %s\n", style.Dim.Render("○ stopped"))
		return nil
	}

	fmt.Printf("  Session ID: %s\n", info.SessionID)

	if info.Attached {
		fmt.Printf("  Attached: yes\n")
	} else {
		fmt.Printf("  Attached: no\n")
	}

	if !info.Created.IsZero() {
		uptime := time.Since(info.Created)
		fmt.Printf("  Created: %s\n", info.Created.Format("2006-01-02 15:04:05"))
		fmt.Printf("  Uptime: %s\n", formatDuration(uptime))
	}

	fmt.Printf("\nAttach with: %s\n", style.Dim.Render(fmt.Sprintf("gt session at %s/%s", rigName, polecatName)))
	return nil
}

func runSessionHealth(cmd *cobra.Command, args []string) error {
	sessionName := args[0]
	status := tmux.NewTmux().CheckSessionHealth(sessionName, sessionHealthMaxInactivity)
	report := newSessionHealthReport(sessionName, status, sessionHealthMaxInactivity)

	if sessionHealthJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}

	if report.Healthy {
		fmt.Printf("%s: %s\n", sessionName, style.Bold.Render(report.Status))
	} else {
		fmt.Printf("%s: %s\n", sessionName, style.Dim.Render(report.Status))
	}
	return nil
}

// formatDuration formats a duration for human display.
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	hours := int(d.Hours())
	mins := int(d.Minutes()) % 60
	if hours >= 24 {
		days := hours / 24
		hours = hours % 24
		return fmt.Sprintf("%dd %dh %dm", days, hours, mins)
	}
	return fmt.Sprintf("%dh %dm", hours, mins)
}

func runSessionCheck(cmd *cobra.Command, args []string) error {
	// Find town root
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Load rigs config
	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		rigsConfig = &config.RigsConfig{Rigs: make(map[string]config.RigEntry)}
	}

	// Get rigs to check
	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)
	rigs, err := rigMgr.DiscoverRigs()
	if err != nil {
		return fmt.Errorf("discovering rigs: %w", err)
	}

	// Filter if specific rig requested
	if len(args) > 0 {
		rigFilter := args[0]
		var filtered []*rig.Rig
		for _, r := range rigs {
			if r.Name == rigFilter {
				filtered = append(filtered, r)
			}
		}
		if len(filtered) == 0 {
			return fmt.Errorf("rig not found: %s", rigFilter)
		}
		rigs = filtered
	}

	fmt.Printf("%s Session Health Check\n\n", style.Bold.Render("🔍"))

	t := tmux.NewTmux()
	totalChecked := 0
	totalHealthy := 0
	totalCrashed := 0

	for _, r := range rigs {
		polecatsDir := filepath.Join(r.Path, "polecats")
		entries, err := os.ReadDir(polecatsDir)
		if err != nil {
			continue // Rig might not have polecats
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			if strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			polecatName := entry.Name()
			sessionName := session.PolecatSessionName(session.PrefixFor(r.Name), polecatName)
			totalChecked++

			// Check if session exists
			running, err := t.HasSession(sessionName)
			if err != nil {
				fmt.Printf("  %s %s/%s: %s\n", style.Bold.Render("⚠"), r.Name, polecatName, style.Dim.Render("error checking session"))
				continue
			}

			if running {
				fmt.Printf("  %s %s/%s: %s\n", style.Bold.Render("✓"), r.Name, polecatName, style.Dim.Render("session alive"))
				totalHealthy++
			} else {
				// Check if polecat has work on hook (would need restart)
				fmt.Printf("  %s %s/%s: %s\n", style.Bold.Render("✗"), r.Name, polecatName, style.Dim.Render("session not running"))
				totalCrashed++
			}
		}
	}

	// Summary
	fmt.Printf("\n%s Summary: %d checked, %d healthy, %d not running\n",
		style.Bold.Render("📊"), totalChecked, totalHealthy, totalCrashed)

	if totalCrashed > 0 {
		fmt.Printf("\n%s To restart crashed polecats: gt session restart <rig>/<polecat>\n",
			style.Dim.Render("Tip:"))
	}

	return nil
}
