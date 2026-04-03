// Emergency stop (gt estop / gt thaw) — pause and resume agent work.
//
// Original implementation by outdoorsea (PR #3237). Cherry-picked for
// manual-only operation: no daemon auto-trigger.
package cmd

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/estop"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

var (
	estopReason string
	estopRig    string
	thawRig     string
)

var estopCmd = &cobra.Command{
	Use:     "estop",
	GroupID: GroupServices,
	Short:   "Emergency stop — freeze all agent work",
	Long: `Emergency stop: freeze agent sessions across the town (or a single rig).

This is the factory floor E-stop button. Agent sessions are sent SIGTSTP
to freeze in place. Context is preserved — no work is lost.

The Mayor and overseer are exempt so they can coordinate recovery.

Use --rig to freeze a single rig instead of the whole town. Per-rig
E-stop is useful when traveling or pausing non-critical work while
keeping other rigs running.

To resume: gt thaw [--rig <name>]

Examples:
  gt estop                              # Freeze everything
  gt estop -r "closing laptop"          # Freeze with reason
  gt estop --rig gastown                # Freeze only gastown
  gt estop --rig beads -r "maintenance" # Freeze beads rig`,
	RunE: runEstop,
}

var thawCmd = &cobra.Command{
	Use:     "thaw",
	GroupID: GroupServices,
	Short:   "Resume from emergency stop — thaw all frozen agents",
	Long: `Resume agent sessions that were frozen by gt estop.

Sends SIGCONT to all frozen sessions, removes the ESTOP sentinel file,
and nudges all sessions to alert them that work can continue.

Examples:
  gt thaw                    # Thaw everything
  gt thaw --rig gastown      # Thaw only gastown`,
	RunE: runThaw,
}

func init() {
	estopCmd.Flags().StringVarP(&estopReason, "reason", "r", "", "Reason for the E-stop")
	estopCmd.Flags().StringVar(&estopRig, "rig", "", "Freeze only this rig (instead of all)")
	thawCmd.Flags().StringVar(&thawRig, "rig", "", "Thaw only this rig (instead of all)")
	rootCmd.AddCommand(estopCmd)
	rootCmd.AddCommand(thawCmd)
}

func runEstop(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Per-rig E-stop
	if estopRig != "" {
		return runEstopRig(townRoot, estopRig)
	}

	if estop.IsActive(townRoot) {
		info := estop.Read(townRoot)
		if info != nil {
			fmt.Printf("%s E-stop already active (triggered %s: %s)\n",
				style.Error.Render("⛔"), info.Trigger, info.Reason)
		}
		return nil
	}

	// Create the sentinel file first — this is the source of truth
	if err := estop.Activate(townRoot, estop.TriggerManual, estopReason); err != nil {
		return fmt.Errorf("failed to create ESTOP file: %w", err)
	}

	fmt.Printf("%s EMERGENCY STOP\n", style.Error.Render("⛔"))
	if estopReason != "" {
		fmt.Printf("   Reason: %s\n", estopReason)
	}
	fmt.Println()

	t := tmux.NewTmux()
	if !t.IsAvailable() {
		fmt.Printf("%s tmux not available — ESTOP file created but cannot freeze sessions\n",
			style.Warning.Render("!"))
		return nil
	}

	frozen := freezeAllSessions(t, townRoot, "")

	fmt.Println()
	fmt.Printf("%s %d session(s) frozen\n", style.Error.Render("⛔"), frozen)
	fmt.Printf("   Resume with: %s\n", style.Bold.Render("gt thaw"))

	return nil
}

func runEstopRig(townRoot, rigName string) error {
	if estop.IsRigActive(townRoot, rigName) {
		info := estop.ReadRig(townRoot, rigName)
		if info != nil {
			fmt.Printf("%s E-stop already active for %s (triggered %s: %s)\n",
				style.Error.Render("⛔"), rigName, info.Trigger, info.Reason)
		}
		return nil
	}

	if err := estop.ActivateRig(townRoot, rigName, estop.TriggerManual, estopReason); err != nil {
		return fmt.Errorf("failed to create ESTOP file for %s: %w", rigName, err)
	}

	fmt.Printf("%s EMERGENCY STOP: %s\n", style.Error.Render("⛔"), style.Bold.Render(rigName))
	if estopReason != "" {
		fmt.Printf("   Reason: %s\n", estopReason)
	}
	fmt.Println()

	t := tmux.NewTmux()
	if !t.IsAvailable() {
		return nil
	}

	frozen := freezeAllSessions(t, townRoot, rigName)

	fmt.Println()
	fmt.Printf("%s %d session(s) frozen in %s\n", style.Error.Render("⛔"), frozen, rigName)
	fmt.Printf("   Resume with: %s\n", style.Bold.Render("gt thaw --rig "+rigName))

	return nil
}

func runThaw(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Per-rig thaw
	if thawRig != "" {
		return runThawRig(townRoot, thawRig)
	}

	if !estop.IsActive(townRoot) {
		fmt.Println("No E-stop active.")
		return nil
	}

	info := estop.Read(townRoot)

	t := tmux.NewTmux()
	if t.IsAvailable() {
		thawed := thawAllSessions(t, townRoot, "")
		fmt.Printf("%s %d session(s) resumed\n", style.Success.Render("✓"), thawed)

		reason := ""
		if info != nil {
			reason = info.Reason
		}
		nudged := nudgeAllSessions(t, townRoot, "", reason)
		if nudged > 0 {
			fmt.Printf("   Nudged %d session(s)\n", nudged)
		}
	}

	if err := estop.Deactivate(townRoot, false); err != nil {
		return fmt.Errorf("failed to remove ESTOP file: %w", err)
	}

	if info != nil {
		duration := time.Since(info.Timestamp).Round(time.Second)
		fmt.Printf("   E-stop was active for %s\n", duration)
	}

	return nil
}

func runThawRig(townRoot, rigName string) error {
	if !estop.IsRigActive(townRoot, rigName) {
		fmt.Printf("No E-stop active for %s.\n", rigName)
		return nil
	}

	info := estop.ReadRig(townRoot, rigName)

	t := tmux.NewTmux()
	if t.IsAvailable() {
		thawed := thawAllSessions(t, townRoot, rigName)
		fmt.Printf("%s %d session(s) resumed in %s\n", style.Success.Render("✓"), thawed, rigName)

		reason := ""
		if info != nil {
			reason = info.Reason
		}
		nudged := nudgeAllSessions(t, townRoot, rigName, reason)
		if nudged > 0 {
			fmt.Printf("   Nudged %d session(s)\n", nudged)
		}
	}

	if err := estop.DeactivateRig(townRoot, rigName); err != nil {
		return fmt.Errorf("failed to remove ESTOP file for %s: %w", rigName, err)
	}

	if info != nil {
		duration := time.Since(info.Timestamp).Round(time.Second)
		fmt.Printf("   E-stop for %s was active for %s\n", rigName, duration)
	}

	return nil
}

// exemptSessions are sessions that should NOT be frozen during E-stop.
var exemptSessions = map[string]bool{
	session.MayorSessionName():    true,
	session.OverseerSessionName(): true,
}

// freezeAllSessions sends SIGTSTP to all Gas Town agent sessions via
// process-group signaling. Mayor and overseer sessions are exempt.
// If rigFilter is non-empty, only sessions for that rig are frozen.
// Returns count of frozen sessions.
func freezeAllSessions(t *tmux.Tmux, townRoot string, rigFilter string) int {
	sessions := collectGTSessions(t, townRoot)
	frozen := 0

	var rigPrefix string
	if rigFilter != "" {
		rigPrefix = session.PrefixFor(rigFilter)
	}

	for _, sess := range sessions {
		if exemptSessions[sess] {
			fmt.Printf("   %s %s (exempt)\n", style.Dim.Render("⏭"), sess)
			continue
		}

		if rigFilter != "" && !isRigSession(sess, rigPrefix) {
			continue
		}

		if err := signalSessionGroup(t, sess, sigFreeze); err != nil {
			fmt.Printf("   %s %s: %v\n", style.Warning.Render("!"), sess, err)
			continue
		}
		fmt.Printf("   %s %s\n", style.Error.Render("⏸"), sess)
		frozen++
	}

	return frozen
}

// thawAllSessions sends SIGCONT to all Gas Town agent sessions.
// If rigFilter is non-empty, only sessions for that rig are thawed.
func thawAllSessions(t *tmux.Tmux, townRoot string, rigFilter string) int {
	sessions := collectGTSessions(t, townRoot)
	thawed := 0

	var rigPrefix string
	if rigFilter != "" {
		rigPrefix = session.PrefixFor(rigFilter)
	}

	for _, sess := range sessions {
		if exemptSessions[sess] {
			continue
		}
		if rigFilter != "" && !isRigSession(sess, rigPrefix) {
			continue
		}
		if err := signalSessionGroup(t, sess, sigThaw); err != nil {
			continue
		}
		thawed++
	}

	return thawed
}

// nudgeAllSessions sends a nudge to all GT sessions to alert them of resume.
// If rigFilter is non-empty, only sessions for that rig are nudged.
// If estopReason is non-empty, it is included in the nudge message.
func nudgeAllSessions(t *tmux.Tmux, townRoot string, rigFilter string, estopReason string) int {
	sessions := collectGTSessions(t, townRoot)
	nudged := 0

	var rigPrefix string
	if rigFilter != "" {
		rigPrefix = session.PrefixFor(rigFilter)
	}

	msg := "E-stop cleared. Work may resume."
	if estopReason != "" {
		msg = fmt.Sprintf("E-stop cleared (was: %s). Work may resume.", estopReason)
	}

	for _, sess := range sessions {
		if exemptSessions[sess] {
			continue
		}
		if rigFilter != "" && !isRigSession(sess, rigPrefix) {
			continue
		}
		if err := t.NudgeSession(sess, msg); err == nil {
			nudged++
		}
	}

	return nudged
}

// isRigSession checks if a session name belongs to a specific rig prefix.
func isRigSession(name, rigPrefix string) bool {
	return strings.HasPrefix(name, rigPrefix+"-") || name == rigPrefix
}

// collectGTSessions returns all Gas Town tmux sessions.
func collectGTSessions(t *tmux.Tmux, townRoot string) []string {
	allSessions, err := t.ListSessions()
	if err != nil {
		return nil
	}

	rigs := discoverRigs(townRoot)
	prefixes := make(map[string]bool)
	for _, rigName := range rigs {
		prefixes[session.PrefixFor(rigName)] = true
	}

	var gtSessions []string
	for _, sess := range allSessions {
		if isGTSession(sess, prefixes) {
			gtSessions = append(gtSessions, sess)
		}
	}
	return gtSessions
}

// isGTSession checks if a session name belongs to Gas Town.
func isGTSession(name string, rigPrefixes map[string]bool) bool {
	// Town-level sessions (hq-*)
	if strings.HasPrefix(name, session.HQPrefix) {
		return true
	}

	// Rig-level sessions: <prefix>-witness, <prefix>-refinery,
	// <prefix>-crew-<name>, <prefix>-<polecat-name>
	for prefix := range rigPrefixes {
		if strings.HasPrefix(name, prefix+"-") || name == prefix {
			return true
		}
	}

	return false
}

// addEstopToStatus checks for E-stop and prints a banner if active.
// Called from gt status to surface E-stop state.
func addEstopToStatus(townRoot string) {
	if estop.IsActive(townRoot) {
		info := estop.Read(townRoot)
		if info != nil {
			age := time.Since(info.Timestamp).Round(time.Second)
			fmt.Printf("%s  E-STOP ACTIVE (%s, %s ago", style.Error.Render("⛔"), info.Trigger, age)
			if info.Reason != "" {
				fmt.Printf(": %s", info.Reason)
			}
			fmt.Println(")")
			fmt.Println()
		}
	}

	// Check for per-rig E-stops
	entries, _ := filepath.Glob(filepath.Join(townRoot, "ESTOP.*"))
	for _, entry := range entries {
		rigName := strings.TrimPrefix(filepath.Base(entry), "ESTOP.")
		info := estop.ReadRig(townRoot, rigName)
		if info != nil {
			age := time.Since(info.Timestamp).Round(time.Second)
			fmt.Printf("%s  E-STOP: %s (%s, %s ago", style.Error.Render("⏸"), rigName, info.Trigger, age)
			if info.Reason != "" {
				fmt.Printf(": %s", info.Reason)
			}
			fmt.Println(")")
		}
	}
	if len(entries) > 0 {
		fmt.Println()
	}
}
