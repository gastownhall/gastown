// Package cmd provides CLI commands for the gt tool.
package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/cli"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/telemetry"
	"github.com/steveyegge/gastown/internal/ui"
	"github.com/steveyegge/gastown/internal/version"
	"github.com/steveyegge/gastown/internal/workspace"
)

var rootCmd = &cobra.Command{
	Use:     "gt", // Updated in init() based on GT_COMMAND
	Short:   "Gas Town - Multi-agent workspace manager",
	Version: Version,
	Long:    "", // Updated in init() based on GT_COMMAND
	PersistentPreRunE: persistentPreRun,
}

func init() {
	// Update command name based on GT_COMMAND env var
	cmdName := cli.Name()
	rootCmd.Use = cmdName
	rootCmd.Long = fmt.Sprintf(`Gas Town (%s) manages multi-agent workspaces called rigs.

It coordinates agent spawning, work distribution, and communication
across distributed teams of AI agents working on shared codebases.`, cmdName)
}

// Commands that don't require beads to be installed/checked.
// These commands should work even when bd is missing or outdated.
var beadsExemptCommands = map[string]bool{
	"version":    true,
	"help":       true,
	"completion": true,
	"crew":       true,
	"polecat":    true,
	"witness":    true,
	"refinery":   true,
	"status":     true,
	"mail":       true,
	"hook":       true,
	"prime":      true,
	"nudge":      true,
	"seance":     true,
	"doctor":     true,
	"dolt":       true,
	"handoff":    true,
	"costs":      true,
	"feed":       true,
	"rig":        true,
	"config":     true,
	"install":    true,
	"tap":        true,
	"dnd":        true,
	"estop":      true, // E-stop must work when Dolt is down
	"thaw":       true, // Thaw must work when Dolt is down
	"signal":        true, // Hook signal handlers must be fast, handle beads internally
	"metrics":       true, // Metrics reads local JSONL, no beads needed
	"krc":           true, // KRC doesn't require beads
	"run-migration":       true, // Migration orchestrator handles its own beads checks
	"health":              true, // Health check doesn't require beads
	"upgrade":             true, // Post-install migration orchestrator
	"heartbeat":           true, // Heartbeat state update — must be fast and dependency-free
}

// Commands exempt from the town root branch warning.
// These are commands that help fix the problem or are diagnostic.
var branchCheckExemptCommands = map[string]bool{
	"version":    true,
	"help":       true,
	"completion": true,
	"doctor":     true, // Used to fix the problem
	"estop":      true, // Emergency stop must always work
	"thaw":       true, // Thaw must always work
	"install":    true, // Initial setup
	"git-init":   true, // Git setup
	"upgrade":    true, // Post-install migration
}

// persistentPreRun runs before every command.
func persistentPreRun(cmd *cobra.Command, args []string) error {
	// Check if binary was built properly (via make build, not raw go build).
	// Raw go build produces unsigned binaries that macOS may kill.
	// Warning only - doesn't block execution.
	// Skip warning when Build was set by a package manager (e.g. Homebrew sets
	// Build to "Homebrew" via ldflags but doesn't set BuiltProperly).
	if BuiltProperly == "" && Build == "dev" && runtime.GOOS == "darwin" {
		fmt.Fprintln(os.Stderr, "ERROR: This binary was built with 'go build' directly.")
		fmt.Fprintln(os.Stderr, "       macOS will SIGKILL unsigned binaries. Use 'make build' instead.")
		if gtRoot := os.Getenv("GT_ROOT"); gtRoot != "" {
			fmt.Fprintf(os.Stderr, "       Run from: %s\n", gtRoot)
		}
		os.Exit(1)
	}

	// Initialize CLI theme (dark/light mode support)
	initCLITheme()

	// Log command usage telemetry (fire-and-forget, excludes tap/signal)
	logCommandUsage(cmd, args)

	// Initialize session prefix registry and agent registry from town root.
	// Try CWD detection first, then fall back to GT_TOWN_ROOT / GT_ROOT env vars.
	// Env var fallback ensures commands invoked from outside the town directory
	// (e.g., "gt agents menu" via a cross-socket tmux binding) still connect to
	// the correct town socket rather than silently using the wrong server.
	if townRoot := detectTownRootFromCwd(); townRoot != "" {
		if err := session.InitRegistry(townRoot); err != nil {
			fmt.Fprintf(os.Stderr, "WARNING: failed to initialize town registry: %v\n", err)
		}
	}

	// Get the root command name being run
	cmdName := cmd.Name()

	// Check for stale binary (warning only, doesn't block)
	if !beadsExemptCommands[cmdName] {
		checkStaleBinaryWarning()
	}

	// Check town root branch (warning only, non-blocking)
	if !branchCheckExemptCommands[cmdName] {
		warnIfTownRootOffMain()
	}

	// Touch polecat session heartbeat on every gt command (gt-qjtq: ZFC liveness fix).
	// This is best-effort and non-blocking — the heartbeat file signals that the agent
	// is alive and actively running gt commands. Used by isSessionProcessDead to
	// determine liveness without PID signal probing.
	touchPolecatHeartbeat()

	// Skip beads check for exempt commands
	if beadsExemptCommands[cmdName] || isRoleCommand(cmd) {
		return nil
	}

	// Check beads version (non-blocking - warn only)
	if err := CheckBeadsVersion(); err != nil {
		fmt.Fprintf(os.Stderr, "\n%s beads (bd) version issue:\n", style.Bold.Render("⚠️  WARNING:"))
		fmt.Fprintf(os.Stderr, "   %v\n", err)
		fmt.Fprintf(os.Stderr, "   Run %s for details.\n\n", style.Dim.Render("gt doctor"))
	}
	return nil
}

// isRoleCommand returns true when the invoked command belongs to the `gt role` tree.
// Role introspection commands are often used in scripts and tests that expect clean
// output; beads version warnings are unrelated noise for these commands.
func isRoleCommand(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if c.Name() == "role" {
			return true
		}
	}
	return false
}

// initCLITheme initializes the CLI color theme based on settings and environment.
func initCLITheme() {
	// Try to load town settings for CLITheme config
	var configTheme string
	if townRoot, err := workspace.FindFromCwd(); err == nil && townRoot != "" {
		settingsPath := config.TownSettingsPath(townRoot)
		if settings, err := config.LoadOrCreateTownSettings(settingsPath); err == nil {
			configTheme = settings.CLITheme
		}
	}

	// Initialize theme with config value (env var takes precedence inside InitTheme)
	ui.InitTheme(configTheme)
	ui.ApplyThemeMode()
}

// touchPolecatHeartbeat touches the session heartbeat file for polecat agents.
// Called from persistentPreRun on every gt command. The heartbeat signals that
// the agent process is alive and actively running gt commands. Used by
// isSessionProcessDead to determine liveness without PID signal probing (gt-qjtq).
//
// This is best-effort: errors are silently ignored. Non-polecat sessions and
// sessions without GT_SESSION are skipped silently.
func touchPolecatHeartbeat() {
	sessionName := os.Getenv("GT_SESSION")
	if sessionName == "" {
		return
	}

	// Only polecats, crew, and dogs need heartbeats — they're the ones checked
	// by isSessionProcessDead for stale session detection.
	role := os.Getenv("GT_ROLE")
	if !strings.Contains(role, "polecat") && !strings.Contains(role, "crew") && !strings.Contains(role, "dog") {
		return
	}

	townRoot := detectTownRootFromCwd()
	if townRoot == "" {
		return
	}

	polecat.TouchSessionHeartbeat(townRoot, sessionName)
}

// warnIfTownRootOffMain prints a warning if the town root is not on main branch.
// This is a non-blocking warning to help catch accidental branch switches.
func warnIfTownRootOffMain() {
	// Find town root (silently - don't error if not in workspace)
	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		return
	}

	// Check if it's a git repo
	gitDir := townRoot + "/.git"
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		return
	}

	// Get current branch
	gitCmd := exec.Command("git", "branch", "--show-current")
	gitCmd.Dir = townRoot
	out, err := gitCmd.Output()
	if err != nil {
		return
	}

	branch := strings.TrimSpace(string(out))
	if branch == "" || branch == "main" || branch == "master" || branch == "gt_managed" {
		return
	}

	// Town root is on wrong branch - warn the user
	fmt.Fprintf(os.Stderr, "\n%s Town root is on branch '%s' (should be 'main')\n",
		style.Bold.Render("⚠️  WARNING:"), branch)
	fmt.Fprintf(os.Stderr, "   This can cause gt commands to fail. Run: %s\n\n",
		style.Dim.Render("gt doctor --fix"))
}

// staleBinaryWarned tracks if we've already warned about stale binary in this session.
// We use an environment variable since the binary restarts on each command.
var staleBinaryWarned = os.Getenv("GT_STALE_WARNED") == "1"

// checkStaleBinaryWarning checks if the installed binary is stale and prints a warning.
// This is a non-blocking check - errors are silently ignored.
func checkStaleBinaryWarning() {
	// Only warn once per shell session
	if staleBinaryWarned {
		return
	}

	repoRoot, err := version.GetRepoRoot()
	if err != nil {
		// Can't find repo - silently skip (might be running from non-dev environment)
		return
	}

	info := version.CheckStaleBinary(repoRoot)
	if info.Error != nil {
		// Check failed - silently skip
		return
	}

	if info.IsStale {
		staleBinaryWarned = true
		_ = os.Setenv("GT_STALE_WARNED", "1")

		msg := info.Describe("gt binary")
		fmt.Fprintf(os.Stderr, "%s %s\n", style.WarningPrefix, msg)
		fmt.Fprintf(os.Stderr, "    %s Run 'make install' in gastown repo to update\n", style.ArrowPrefix)
	}
}

// Execute runs the root command and returns an exit code.
// The caller (main) should call os.Exit with this code.
func Execute() int {
	ctx := context.Background()
	provider, err := telemetry.Init(ctx, "gastown", Version)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: telemetry init: %v\n", err)
	}
	if provider != nil {
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = provider.Shutdown(shutdownCtx)
		}()
		// Set OTEL_RESOURCE_ATTRIBUTES in the process env so all bd subprocesses
		// spawned via exec.Command inherit GT context automatically.
		telemetry.SetProcessOTELAttrs()
	}

	if err := rootCmd.Execute(); err != nil {
		// Check for silent exit (scripting commands that signal status via exit code)
		if code, ok := IsSilentExit(err); ok {
			return code
		}
		// Other errors already printed by cobra
		return 1
	}
	return 0
}

// Command group IDs - used by subcommands to organize help output
const (
	GroupWork      = "work"
	GroupAgents    = "agents"
	GroupComm      = "comm"
	GroupServices  = "services"
	GroupWorkspace = "workspace"
	GroupConfig    = "config"
	GroupDiag      = "diag"
)

func init() {
	// Enable prefix matching for subcommands (e.g., "gt ref at" -> "gt refinery attach")
	cobra.EnablePrefixMatching = true

	// Define command groups (order determines help output order)
	rootCmd.AddGroup(
		&cobra.Group{ID: GroupWork, Title: "Work Management:"},
		&cobra.Group{ID: GroupAgents, Title: "Agent Management:"},
		&cobra.Group{ID: GroupComm, Title: "Communication:"},
		&cobra.Group{ID: GroupServices, Title: "Services:"},
		&cobra.Group{ID: GroupWorkspace, Title: "Workspace:"},
		&cobra.Group{ID: GroupConfig, Title: "Configuration:"},
		&cobra.Group{ID: GroupDiag, Title: "Diagnostics:"},
	)

	// Put help and completion in a sensible group
	rootCmd.SetHelpCommandGroupID(GroupDiag)
	rootCmd.SetCompletionCommandGroupID(GroupConfig)

	// Global flags can be added here
	// rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file")
}

// buildCommandPath walks the command hierarchy to build the full command path.
// For example: "gt mail send", "gt status", etc.
func buildCommandPath(cmd *cobra.Command) string {
	var parts []string
	for c := cmd; c != nil; c = c.Parent() {
		parts = append([]string{c.Name()}, parts...)
	}
	return strings.Join(parts, " ")
}

// requireSubcommand returns a RunE function for parent commands that require
// a subcommand. Without this, Cobra silently shows help and exits 0 for
// unknown subcommands like "gt mol foobar", masking errors.
func requireSubcommand(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("requires a subcommand\n\nRun '%s --help' for usage", buildCommandPath(cmd))
	}
	unknown := args[0]
	errMsg := fmt.Sprintf("unknown command %q for %q", unknown, buildCommandPath(cmd))
	// Use cobra's suggestion engine (Levenshtein + SuggestFor lists)
	if suggestions := cmd.SuggestionsFor(unknown); len(suggestions) > 0 {
		errMsg += "\n\nDid you mean"
		if len(suggestions) == 1 {
			errMsg += " this?\n"
		} else {
			errMsg += " one of these?\n"
		}
		for _, s := range suggestions {
			errMsg += fmt.Sprintf("\t%s %s\n", buildCommandPath(cmd), s)
		}
	}
	errMsg += fmt.Sprintf("\nRun '%s --help' for available commands", buildCommandPath(cmd))
	return fmt.Errorf("%s", errMsg)
}

// checkHelpFlag checks if --help or -h is the first argument and shows help if so.
// Returns true if help was shown, false otherwise.
//
// This is needed for commands with DisableFlagParsing: true, which bypass
// Cobra's automatic help flag handling.
//
// We only check the FIRST argument to avoid false positives like:
//
//	gt commit -m "--help"  # User wants message "--help", not help output
//
// This covers the common case (gt commit --help) without breaking edge cases.
func checkHelpFlag(cmd *cobra.Command, args []string) (bool, error) {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		return true, cmd.Help()
	}
	return false, nil
}
