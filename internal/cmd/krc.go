package cmd

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jonbaldie/gastown/internal/krc"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var krcCmd = &cobra.Command{
	Use:   "krc",
	Short: "Key Record Chronicle - manage ephemeral data TTLs",
	Long: `Key Record Chronicle (KRC) manages TTL-based lifecycle for Level 0 ephemeral data.

Per DOLT-STORAGE-DESIGN-V3.md, Level 0 includes patrol heartbeats, status checks,
and other operational data that decays in forensic value over days.

KRC provides:
  - Configurable TTLs per event type
  - Auto-pruning of expired events
  - Statistics on ephemeral data lifecycle

Examples:
  gt krc stats              # Show event statistics
  gt krc prune              # Remove expired events
  gt krc prune --dry-run    # Preview what would be pruned
  gt krc config             # Show TTL configuration
  gt krc config set patrol_* 12h   # Set TTL for patrol events`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

var krcStatsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show statistics about ephemeral data",
	Long: `Display statistics about events and their TTL status.

Shows:
  - File sizes and event counts
  - Events by type and age
  - TTL configuration and expiration status`,
	RunE: runKrcStats,
}

var krcPruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Remove expired events",
	Long: `Prune events that have exceeded their TTL.

Events are removed from both .events.jsonl and .feed.jsonl.
The operation is atomic (uses temp files and rename).

Use --dry-run to preview what would be pruned without making changes.`,
	RunE: runKrcPrune,
}

var krcConfigCmd = &cobra.Command{
	Use:   "config [subcommand]",
	Short: "View or modify TTL configuration",
	Long: `View or modify the KRC TTL configuration.

Without arguments, shows the current configuration.

Subcommands:
  set <pattern> <ttl>   Set TTL for event type pattern
  reset                 Reset to default configuration

Examples:
  gt krc config                     # Show current config
  gt krc config set patrol_* 12h    # Set patrol TTL to 12 hours
  gt krc config set default 3d      # Set default TTL to 3 days
  gt krc config reset               # Reset to defaults`,
	RunE: runKrcConfig,
}

var krcConfigSetCmd = &cobra.Command{
	Use:   "set <pattern> <ttl>",
	Short: "Set TTL for an event type pattern",
	Long: `Set the TTL for events matching the given pattern.

Patterns support glob-style matching with * (e.g., "patrol_*" matches all patrol events).
Use "default" as the pattern to set the default TTL.

TTL format: 1h, 12h, 1d, 7d, 30d, etc.`,
	Args: cobra.ExactArgs(2),
	RunE: runKrcConfigSet,
}

var krcConfigResetCmd = &cobra.Command{
	Use:   "reset",
	Short: "Reset TTL configuration to defaults",
	Long: `Reset the KRC TTL configuration file to built-in defaults.

This overwrites any custom TTL patterns and restores the default
prune interval, minimum retain count, and per-type TTLs.`,
	RunE: runKrcConfigReset,
}

var krcDecayCmd = &cobra.Command{
	Use:   "decay",
	Short: "Show forensic value decay report",
	Long: `Display how forensic value is decaying across event types.

Each event type has a decay curve that models how its value diminishes over time:
  rapid   - value drops quickly (heartbeats, pings)
  steady  - linear decay (session events, patrols)
  slow    - value persists longer (errors, escalations)
  flat    - full value until near TTL (audit events, deaths)

Events with low forensic scores are candidates for aggressive pruning.`,
	RunE: runKrcDecay,
}

var krcAutoPruneStatusCmd = &cobra.Command{
	Use:   "auto-prune-status",
	Short: "Show auto-prune scheduling state",
	Long: `Display the auto-prune scheduling state including:
  - Last prune time and result
  - Total prunes and bytes freed
  - Time until next scheduled prune`,
	RunE: runKrcAutoPruneStatus,
}

var (
	krcPruneDryRun bool
	krcPruneAuto   bool
	krcStatsJSON   bool
	krcDecayJSON   bool
)

func init() {
	rootCmd.AddCommand(krcCmd)
	krcCmd.AddCommand(krcStatsCmd)
	krcCmd.AddCommand(krcPruneCmd)
	krcCmd.AddCommand(krcConfigCmd)
	krcCmd.AddCommand(krcDecayCmd)
	krcCmd.AddCommand(krcAutoPruneStatusCmd)
	krcConfigCmd.AddCommand(krcConfigSetCmd)
	krcConfigCmd.AddCommand(krcConfigResetCmd)

	krcPruneCmd.Flags().BoolVar(&krcPruneDryRun, "dry-run", false, "Preview changes without modifying files")
	krcPruneCmd.Flags().BoolVar(&krcPruneAuto, "auto", false, "Daemon mode: only prune if PruneInterval has elapsed")
	krcStatsCmd.Flags().BoolVar(&krcStatsJSON, "json", false, "Output in JSON format")
	krcDecayCmd.Flags().BoolVar(&krcDecayJSON, "json", false, "Output in JSON format")
}

func runKrcStats(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	config, err := krc.LoadConfig(townRoot)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	stats, err := krc.GetStats(townRoot, config)
	if err != nil {
		return fmt.Errorf("getting stats: %w", err)
	}

	if krcStatsJSON {
		data, err := json.MarshalIndent(stats, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}

	// Human-readable output
	fmt.Println(style.Bold.Render("Key Record Chronicle Statistics"))
	fmt.Println()

	// File stats
	fmt.Println(style.Bold.Render("Files:"))
	fmt.Printf("  Events: %s (%d events)\n", formatBytes(stats.EventsFile.Size), stats.EventsFile.EventCount)
	fmt.Printf("  Feed:   %s (%d events)\n", formatBytes(stats.FeedFile.Size), stats.FeedFile.EventCount)
	fmt.Println()

	// Age distribution
	fmt.Println(style.Bold.Render("Age Distribution:"))
	ages := []string{"0-1d", "1-7d", "7-30d", "30d+"}
	for _, age := range ages {
		count := stats.ByAge[age]
		if count > 0 {
			fmt.Printf("  %-8s %d events\n", age+":", count)
		}
	}
	fmt.Println()

	// Time range
	if !stats.OldestEvent.IsZero() {
		fmt.Printf("Oldest event: %s (%s ago)\n", stats.OldestEvent.Format(time.RFC3339), krcFormatDuration(time.Since(stats.OldestEvent)))
		fmt.Printf("Newest event: %s (%s ago)\n", stats.NewestEvent.Format(time.RFC3339), krcFormatDuration(time.Since(stats.NewestEvent)))
		fmt.Println()
	}

	// TTL breakdown (show types with expired events first)
	fmt.Println(style.Bold.Render("TTL Status by Type:"))

	// Sort by expired count (descending), then by name
	var types []string
	for t := range stats.TTLBreakdown {
		types = append(types, t)
	}
	sort.Slice(types, func(i, j int) bool {
		ei := stats.TTLBreakdown[types[i]].Expired
		ej := stats.TTLBreakdown[types[j]].Expired
		if ei != ej {
			return ei > ej
		}
		return types[i] < types[j]
	})

	for _, t := range types {
		info := stats.TTLBreakdown[t]
		status := style.Success.Render("OK")
		if info.Expired > 0 {
			status = style.Warning.Render(fmt.Sprintf("%d expired", info.Expired))
		}
		fmt.Printf("  %-20s TTL: %-6s Count: %-5d %s\n", t, krcFormatDuration(info.TTL), info.Count, status)
	}

	return nil
}

func runKrcPrune(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	config, err := krc.LoadConfig(townRoot)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	// Auto mode: respect PruneInterval scheduling
	if krcPruneAuto {
		return runKrcAutoPrune(townRoot, config)
	}

	if krcPruneDryRun {
		// Show what would be pruned
		stats, err := krc.GetStats(townRoot, config)
		if err != nil {
			return fmt.Errorf("getting stats: %w", err)
		}

		totalExpired := 0
		for _, info := range stats.TTLBreakdown {
			totalExpired += info.Expired
		}

		if totalExpired == 0 {
			fmt.Println("No expired events to prune.")
			return nil
		}

		fmt.Println(style.Bold.Render("Dry run - would prune:"))
		fmt.Println()

		// Sort by expired count
		var types []string
		for t, info := range stats.TTLBreakdown {
			if info.Expired > 0 {
				types = append(types, t)
			}
		}
		sort.Slice(types, func(i, j int) bool {
			return stats.TTLBreakdown[types[i]].Expired > stats.TTLBreakdown[types[j]].Expired
		})

		for _, t := range types {
			info := stats.TTLBreakdown[t]
			fmt.Printf("  %-20s %d events (TTL: %s)\n", t, info.Expired, krcFormatDuration(info.TTL))
		}
		fmt.Println()
		fmt.Printf("Total: %d events would be pruned\n", totalExpired)
		fmt.Println()
		fmt.Println("Run without --dry-run to prune.")
		return nil
	}

	// Actually prune
	pruner := krc.NewPruner(townRoot, config)
	result, err := pruner.Prune()
	if err != nil {
		return fmt.Errorf("pruning: %w", err)
	}

	if result.EventsPruned == 0 {
		fmt.Println("No expired events to prune.")
		return nil
	}

	fmt.Println(style.Bold.Render("Prune complete:"))
	fmt.Printf("  Events processed: %d\n", result.EventsProcessed)
	fmt.Printf("  Events pruned:    %d\n", result.EventsPruned)
	fmt.Printf("  Events retained:  %d\n", result.EventsRetained)
	fmt.Printf("  Space saved:      %s\n", formatBytes(result.BytesBefore-result.BytesAfter))
	fmt.Printf("  Duration:         %s\n", result.Duration.Round(time.Millisecond))

	if len(result.PrunedByType) > 0 {
		fmt.Println()
		fmt.Println("Pruned by type:")

		// Sort by count
		var types []string
		for t := range result.PrunedByType {
			types = append(types, t)
		}
		sort.Slice(types, func(i, j int) bool {
			return result.PrunedByType[types[i]] > result.PrunedByType[types[j]]
		})

		for _, t := range types {
			fmt.Printf("  %-20s %d\n", t, result.PrunedByType[t])
		}
	}

	return nil
}

func runKrcConfig(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	config, err := krc.LoadConfig(townRoot)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	fmt.Println(style.Bold.Render("KRC Configuration"))
	fmt.Println()
	fmt.Printf("Config file: %s\n", krc.ConfigFile(townRoot))
	fmt.Println()
	fmt.Printf("Default TTL:     %s\n", krcFormatDuration(config.DefaultTTL))
	fmt.Printf("Prune interval:  %s\n", krcFormatDuration(config.PruneInterval))
	fmt.Printf("Min retain:      %d events\n", config.MinRetainCount)
	fmt.Println()
	fmt.Println(style.Bold.Render("TTLs by pattern:"))

	// Sort patterns
	var patterns []string
	for p := range config.TTLs {
		patterns = append(patterns, p)
	}
	sort.Strings(patterns)

	for _, p := range patterns {
		fmt.Printf("  %-20s %s\n", p, krcFormatDuration(config.TTLs[p]))
	}

	return nil
}

func runKrcConfigSet(cmd *cobra.Command, args []string) error {
	pattern := args[0]
	ttlStr := args[1]

	ttl, err := krcParseDuration(ttlStr)
	if err != nil {
		return fmt.Errorf("invalid TTL %q: %w", ttlStr, err)
	}

	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	config, err := krc.LoadConfig(townRoot)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	if pattern == "default" {
		config.DefaultTTL = ttl
		fmt.Printf("Set default TTL to %s\n", krcFormatDuration(ttl))
	} else {
		if config.TTLs == nil {
			config.TTLs = make(map[string]time.Duration)
		}
		config.TTLs[pattern] = ttl
		fmt.Printf("Set TTL for %q to %s\n", pattern, krcFormatDuration(ttl))
	}

	if err := krc.SaveConfig(townRoot, config); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	return nil
}

func runKrcConfigReset(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	config := krc.DefaultConfig()
	if err := krc.SaveConfig(townRoot, config); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	fmt.Println("Reset KRC configuration to defaults.")
	return nil
}

// krcParseDuration parses a duration string with day support (e.g., "7d", "12h", "30m").
func krcParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "d") {
		days := strings.TrimSuffix(s, "d")
		var d int
		if _, err := fmt.Sscanf(days, "%d", &d); err != nil {
			return 0, fmt.Errorf("invalid days: %s", days)
		}
		return time.Duration(d) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

// krcFormatDuration formats a duration in human-readable form.
func krcFormatDuration(d time.Duration) string {
	if d >= 24*time.Hour {
		days := d / (24 * time.Hour)
		if d%(24*time.Hour) == 0 {
			return fmt.Sprintf("%dd", days)
		}
		hours := (d % (24 * time.Hour)) / time.Hour
		return fmt.Sprintf("%dd%dh", days, hours)
	}
	if d >= time.Hour {
		hours := d / time.Hour
		if d%time.Hour == 0 {
			return fmt.Sprintf("%dh", hours)
		}
		mins := (d % time.Hour) / time.Minute
		return fmt.Sprintf("%dh%dm", hours, mins)
	}
	if d >= time.Minute {
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return d.String()
}

// formatBytes formats bytes in human-readable form.
func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

// runKrcAutoPrune runs pruning only if the PruneInterval has elapsed.
// Designed for daemon/deacon integration: call frequently, only prunes when due.
func runKrcAutoPrune(townRoot string, config *krc.Config) error {
	result, ran, err := krc.AutoPrune(townRoot, config)
	if err != nil {
		return fmt.Errorf("auto-prune: %w", err)
	}

	if !ran {
		state, _ := krc.LoadAutoPruneState(townRoot)
		sinceLastPrune := state.TimeSinceLastPrune()
		remaining := config.PruneInterval - sinceLastPrune
		if remaining < 0 {
			remaining = 0
		}
		fmt.Printf("%s Auto-prune skipped (next in %s)\n",
			style.Dim.Render("○"), krcFormatDuration(remaining))
		return nil
	}

	if result.EventsPruned == 0 {
		fmt.Printf("%s Auto-prune ran: no expired events\n", style.Dim.Render("○"))
		return nil
	}

	fmt.Printf("%s Auto-pruned %d events (%s freed)\n",
		style.Bold.Render("✓"),
		result.EventsPruned,
		formatBytes(result.BytesBefore-result.BytesAfter))

	return nil
}

func runKrcDecay(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	config, err := krc.LoadConfig(townRoot)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	stats, err := krc.GetStats(townRoot, config)
	if err != nil {
		return fmt.Errorf("getting stats: %w", err)
	}

	report := krc.GenerateDecayReport(stats, config)

	if krcDecayJSON {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}

	// Human-readable output
	fmt.Println(style.Bold.Render("Forensic Value Decay Report"))
	fmt.Println()

	if len(report.Types) == 0 {
		fmt.Printf("%s No events found\n", style.Dim.Render("○"))
		return nil
	}

	fmt.Printf("Total events: %d   Avg score: %.0f%%   At risk: %d   Expired: %d\n",
		report.TotalEvents, report.TotalScore*100, report.AtRisk, report.Expired)
	fmt.Println()

	// Table header
	fmt.Printf("  %-20s %-7s %-6s %-8s %-8s %-8s %s\n",
		"TYPE", "CURVE", "TTL", "COUNT", "AVG AGE", "SCORE", "STATUS")
	fmt.Printf("  %-20s %-7s %-6s %-8s %-8s %-8s %s\n",
		strings.Repeat("-", 20), "------", "-----", "-------", "-------", "-------", "------")

	for _, di := range report.Types {
		// Color-code the score
		scoreStr := fmt.Sprintf("%.0f%%", di.AvgScore*100)
		var statusStr string
		switch {
		case di.ExpiredCount > 0:
			statusStr = style.Warning.Render(fmt.Sprintf("%d expired", di.ExpiredCount))
		case di.AvgScore < 0.25:
			statusStr = style.Warning.Render("low value")
		case di.AvgScore < 0.5:
			statusStr = style.Dim.Render("decaying")
		default:
			statusStr = style.Success.Render("healthy")
		}

		ageStr := ""
		if di.AvgAge > 0 {
			ageStr = krcFormatDuration(di.AvgAge)
		}

		fmt.Printf("  %-20s %-7s %-6s %-8d %-8s %-8s %s\n",
			di.EventType, di.Curve, krcFormatDuration(di.TTL),
			di.Count, ageStr, scoreStr, statusStr)
	}

	fmt.Println()
	fmt.Println(style.Dim.Render("Decay curves: rapid (heartbeats), steady (sessions), slow (errors), flat (audit)"))

	return nil
}

func runKrcAutoPruneStatus(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	config, err := krc.LoadConfig(townRoot)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	state, err := krc.LoadAutoPruneState(townRoot)
	if err != nil {
		return fmt.Errorf("loading auto-prune state: %w", err)
	}

	fmt.Println(style.Bold.Render("Auto-Prune Status"))
	fmt.Println()
	fmt.Printf("Prune interval: %s\n", krcFormatDuration(config.PruneInterval))

	if state.LastPruneTime.IsZero() {
		fmt.Printf("Last prune:     %s\n", style.Dim.Render("never"))
		fmt.Printf("Status:         %s\n", style.Warning.Render("prune pending"))
	} else {
		fmt.Printf("Last prune:     %s (%s ago)\n",
			state.LastPruneTime.Format(time.RFC3339),
			krcFormatDuration(time.Since(state.LastPruneTime)))

		if state.ShouldPrune(config.PruneInterval) {
			fmt.Printf("Status:         %s\n", style.Warning.Render("prune due"))
		} else {
			remaining := config.PruneInterval - time.Since(state.LastPruneTime)
			fmt.Printf("Status:         next in %s\n", krcFormatDuration(remaining))
		}
	}

	fmt.Println()
	fmt.Printf("Total prunes:      %d\n", state.PruneCount)
	fmt.Printf("Total pruned:      %d events\n", state.TotalPruned)
	fmt.Printf("Total space freed: %s\n", formatBytes(state.TotalBytesFreed))

	if state.LastResult != nil && state.LastResult.EventsPruned > 0 {
		fmt.Println()
		fmt.Println(style.Bold.Render("Last prune result:"))
		fmt.Printf("  Processed: %d  Pruned: %d  Retained: %d\n",
			state.LastResult.EventsProcessed,
			state.LastResult.EventsPruned,
			state.LastResult.EventsRetained)
	}

	return nil
}
