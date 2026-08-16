package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jonbaldie/gastown/internal/session"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/tmux"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

// Warrant flags
var (
	warrantReason  string
	warrantListAll bool
	warrantForce   bool
	warrantStdin   bool // Read reason from stdin
)

// Warrant represents a death warrant for an agent
type Warrant struct {
	ID         string     `json:"id"`
	Target     string     `json:"target"` // e.g., "gastown/polecats/alpha", "deacon/dogs/bravo"
	Reason     string     `json:"reason"`
	FiledBy    string     `json:"filed_by"`
	FiledAt    time.Time  `json:"filed_at"`
	Executed   bool       `json:"executed,omitempty"`
	ExecutedAt *time.Time `json:"executed_at,omitempty"`
}

var warrantCmd = &cobra.Command{
	Use:   "warrant",
	Short: "Manage death warrants for stuck agents",
	Long: `Manage death warrants for agents that need termination.

Death warrants are filed when an agent is stuck, unresponsive, or needs
forced termination. Boot handles warrant execution during triage cycles.

The warrant system provides a controlled way to terminate agents:
1. Deacon/Witness files a warrant with a reason
2. Boot picks up the warrant during triage
3. Boot executes the warrant (terminates session, updates state)
4. Warrant is marked as executed

Warrants are stored in ~/gt/warrants/ as JSON files.`,
}

var warrantFileCmd = &cobra.Command{
	Use:   "file <target>",
	Short: "File a death warrant for an agent",
	Long: `File a death warrant for an agent that needs termination.

The target should be an agent path like:
  - gastown/polecats/alpha
  - deacon/dogs/bravo
  - beads/polecats/charlie

Examples:
  gt warrant file gastown/polecats/alpha --reason "Zombie: no session, idle >10m"
  gt warrant file deacon/dogs/bravo --reason "Stuck: working on task for >2h"`,
	Args: cobra.ExactArgs(1),
	RunE: runWarrantFile,
}

var warrantListCmd = &cobra.Command{
	Use:   "list",
	Short: "List pending warrants",
	Long: `List all pending (unexecuted) warrants.

Use --all to include executed warrants.

Examples:
  gt warrant list
  gt warrant list --all`,
	RunE: runWarrantList,
}

var warrantExecuteCmd = &cobra.Command{
	Use:   "execute <target>",
	Short: "Execute a warrant (terminate agent)",
	Long: `Execute a pending warrant for the specified target.

This will:
1. Find the warrant for the target
2. Terminate the agent's tmux session (if exists)
3. Mark the warrant as executed

Use --force to execute even if no warrant exists.

Examples:
  gt warrant execute gastown/polecats/alpha
  gt warrant execute deacon/dogs/bravo --force`,
	Args: cobra.ExactArgs(1),
	RunE: runWarrantExecute,
}

func init() {
	// File flags
	warrantFileCmd.Flags().StringVarP(&warrantReason, "reason", "r", "", "Reason for the warrant (required unless --stdin)")
	warrantFileCmd.Flags().BoolVar(&warrantStdin, "stdin", false, "Read reason from stdin (avoids shell quoting issues)")

	// List flags
	warrantListCmd.Flags().BoolVarP(&warrantListAll, "all", "a", false, "Include executed warrants")

	// Execute flags
	warrantExecuteCmd.Flags().BoolVarP(&warrantForce, "force", "f", false, "Execute even without a warrant")

	warrantCmd.AddCommand(warrantFileCmd)
	warrantCmd.AddCommand(warrantListCmd)
	warrantCmd.AddCommand(warrantExecuteCmd)

	rootCmd.AddCommand(warrantCmd)
}

// getWarrantDir returns the warrants directory path
func getWarrantDir() (string, error) {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return "", fmt.Errorf("finding town root: %w", err)
	}
	return filepath.Join(townRoot, "warrants"), nil
}

// warrantFilePath returns the path for a warrant file
func warrantFilePath(dir, target string) string {
	// Replace / with _ for filename safety
	safe := strings.ReplaceAll(target, "/", "_")
	return filepath.Join(dir, safe+".warrant.json")
}

func runWarrantFile(cmd *cobra.Command, args []string) error {
	// Handle --stdin: read reason from stdin (avoids shell quoting issues)
	if warrantStdin {
		if warrantReason != "" {
			return fmt.Errorf("cannot use --stdin with --reason/-r")
		}
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("reading stdin: %w", err)
		}
		warrantReason = strings.TrimRight(string(data), "\n")
	}

	// Require reason via --reason or --stdin
	if warrantReason == "" {
		return fmt.Errorf("required flag \"reason\" not set (use --reason/-r or --stdin)")
	}

	target := args[0]

	warrantDir, err := getWarrantDir()
	if err != nil {
		return err
	}

	// Create warrants directory if needed
	if err := os.MkdirAll(warrantDir, 0755); err != nil {
		return fmt.Errorf("creating warrants directory: %w", err)
	}

	// Check if warrant already exists
	warrantPath := warrantFilePath(warrantDir, target)
	if _, err := os.Stat(warrantPath); err == nil {
		// Load existing warrant
		data, _ := os.ReadFile(warrantPath)
		var existing Warrant
		if json.Unmarshal(data, &existing) == nil && !existing.Executed {
			fmt.Printf("Warrant already exists for %s\n", target)
			fmt.Printf("  Reason: %s\n", existing.Reason)
			fmt.Printf("  Filed: %s\n", existing.FiledAt.Format(time.RFC3339))
			return nil
		}
	}

	// Get filer identity
	filedBy := os.Getenv("BD_ACTOR")
	if filedBy == "" {
		filedBy = "unknown"
	}

	warrant := Warrant{
		ID:       fmt.Sprintf("warrant-%d", time.Now().UnixMilli()),
		Target:   target,
		Reason:   warrantReason,
		FiledBy:  filedBy,
		FiledAt:  time.Now(),
		Executed: false,
	}

	data, err := json.MarshalIndent(warrant, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling warrant: %w", err)
	}

	if err := os.WriteFile(warrantPath, data, 0644); err != nil {
		return fmt.Errorf("writing warrant: %w", err)
	}

	fmt.Printf("✓ Filed death warrant for %s\n", style.Bold.Render(target))
	fmt.Printf("  Reason: %s\n", warrantReason)
	fmt.Printf("  ID: %s\n", warrant.ID)

	return nil
}

func runWarrantList(cmd *cobra.Command, args []string) error {
	warrantDir, err := getWarrantDir()
	if err != nil {
		return err
	}

	entries, err := os.ReadDir(warrantDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No warrants filed")
			return nil
		}
		return fmt.Errorf("reading warrants directory: %w", err)
	}

	var warrants []Warrant
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".warrant.json") {
			continue
		}

		data, err := os.ReadFile(filepath.Join(warrantDir, entry.Name()))
		if err != nil {
			continue
		}

		var w Warrant
		if err := json.Unmarshal(data, &w); err != nil {
			continue
		}

		if warrantListAll || !w.Executed {
			warrants = append(warrants, w)
		}
	}

	if len(warrants) == 0 {
		if warrantListAll {
			fmt.Println("No warrants found")
		} else {
			fmt.Println("No pending warrants")
		}
		return nil
	}

	fmt.Println(style.Bold.Render("Death Warrants"))
	fmt.Println()

	for _, w := range warrants {
		status := "⚠️  PENDING"
		if w.Executed {
			status = "✓ EXECUTED"
		}
		fmt.Printf("  %s %s\n", status, style.Bold.Render(w.Target))
		fmt.Printf("     Reason: %s\n", w.Reason)
		fmt.Printf("     Filed: %s by %s\n", w.FiledAt.Format("2006-01-02 15:04"), w.FiledBy)
		if w.Executed && w.ExecutedAt != nil {
			fmt.Printf("     Executed: %s\n", w.ExecutedAt.Format("2006-01-02 15:04"))
		}
		fmt.Println()
	}

	return nil
}

func runWarrantExecute(cmd *cobra.Command, args []string) error {
	target := args[0]

	warrantDir, err := getWarrantDir()
	if err != nil {
		return err
	}

	warrantPath := warrantFilePath(warrantDir, target)
	var warrant *Warrant

	// Load warrant if exists
	if data, err := os.ReadFile(warrantPath); err == nil {
		var w Warrant
		if json.Unmarshal(data, &w) == nil {
			warrant = &w
		}
	}

	if warrant == nil && !warrantForce {
		return fmt.Errorf("no warrant found for %s (use --force to execute anyway)", target)
	}

	if warrant != nil && warrant.Executed {
		fmt.Printf("Warrant for %s already executed at %s\n", target, warrant.ExecutedAt.Format(time.RFC3339))
		return nil
	}

	tm := tmux.NewTmux()

	if warrant != nil {
		if err := executeOneWarrant(warrant, warrantPath, tm); err != nil {
			return fmt.Errorf("executing warrant: %w", err)
		}
	} else {
		// --force without a warrant file: just kill the session
		sessionName, err := targetToSessionName(target)
		if err != nil {
			return fmt.Errorf("determining session name: %w", err)
		}
		if has, err := tm.HasSession(sessionName); err != nil {
			return fmt.Errorf("checking session %s: %w", sessionName, err)
		} else if has {
			if err := tm.KillSessionWithProcesses(sessionName); err != nil {
				return fmt.Errorf("killing session %s: %w", sessionName, err)
			}
			fmt.Printf("✓ Terminated session %s\n", sessionName)
		} else {
			fmt.Printf("  Session %s not found (already dead)\n", sessionName)
		}
	}

	fmt.Printf("✓ Warrant executed for %s\n", style.Bold.Render(target))
	return nil
}

// executeOneWarrant executes a single pending warrant: checks if the target
// session exists, kills it with full process tree cleanup, and marks the warrant
// as executed on disk. Returns nil on success. On error, the warrant is NOT
// marked as executed so it can be retried on the next triage cycle.
func executeOneWarrant(w *Warrant, warrantPath string, tm *tmux.Tmux) error {
	sessionName, err := targetToSessionName(w.Target)
	if err != nil {
		return fmt.Errorf("invalid target %s: %w", w.Target, err)
	}

	has, err := tm.HasSession(sessionName)
	if err != nil {
		return fmt.Errorf("checking session %s: %w", sessionName, err)
	}

	if has {
		if err := tm.KillSessionWithProcesses(sessionName); err != nil {
			return fmt.Errorf("killing session %s: %w", sessionName, err)
		}
		fmt.Printf("Warrant executed: terminated session %s (%s)\n", sessionName, w.Target)
	} else {
		fmt.Printf("Warrant executed: session %s already dead (%s)\n", sessionName, w.Target)
	}

	now := time.Now()
	w.Executed = true
	w.ExecutedAt = &now
	data, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling warrant: %w", err)
	}
	if err := os.WriteFile(warrantPath, data, 0644); err != nil {
		return fmt.Errorf("writing warrant file: %w", err)
	}

	return nil
}

// targetToSessionName converts a target path to a tmux session name
func targetToSessionName(target string) (string, error) {
	parts := strings.Split(target, "/")

	switch {
	case len(parts) == 3 && parts[1] == "polecats":
		// gastown/polecats/alpha -> {prefix}-alpha
		return session.PolecatSessionName(session.PrefixFor(parts[0]), parts[2]), nil
	case len(parts) == 3 && parts[1] == "crew":
		// gastown/crew/bob -> {prefix}-crew-bob
		return session.CrewSessionName(session.PrefixFor(parts[0]), parts[2]), nil
	case len(parts) == 2 && parts[1] == "witness":
		// gastown/witness -> {prefix}-witness
		return session.WitnessSessionName(session.PrefixFor(parts[0])), nil
	case len(parts) == 2 && parts[1] == "refinery":
		// gastown/refinery -> {prefix}-refinery
		return session.RefinerySessionName(session.PrefixFor(parts[0])), nil
	case len(parts) == 2 && parts[0] == "deacon" && parts[1] == "dogs":
		return "", fmt.Errorf("invalid target: need dog name (e.g., deacon/dogs/alpha)")
	case len(parts) == 3 && parts[0] == "deacon" && parts[1] == "dogs":
		// deacon/dogs/alpha -> hq-dog-alpha
		return fmt.Sprintf("hq-dog-%s", parts[2]), nil
	default:
		prefix := session.DefaultPrefix
		if len(parts) > 0 {
			prefix = session.PrefixFor(parts[0])
		}
		return prefix + "-" + strings.ReplaceAll(target, "/", "-"), nil
	}
}
