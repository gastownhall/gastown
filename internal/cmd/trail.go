package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/events"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var (
	trailSince string
	trailLimit int
	trailJSON  bool
	trailAll   bool
)

var trailCmd = &cobra.Command{
	Use:     "trail",
	Aliases: []string{"recent", "recap"},
	GroupID: GroupWork,
	Short:   "Show recent agent activity",
	Long: `Show recent activity in the workspace.

Without a subcommand, shows recent commits from agents.

Subcommands:
  commits    Recent git commits from agents
  beads      Recent beads (work items)
  hooks      Recent hook activity

Flags:
  --since    Show activity since this time (e.g., "1h", "24h", "7d")
  --limit    Maximum number of items to show (default: 20)
  --json     Output as JSON
  --all      Include all activity (not just agents)

Examples:
  gt trail                     # Recent commits (default)
  gt trail commits             # Same as above
  gt trail commits --since 1h  # Last hour
  gt trail beads               # Recent beads
  gt trail hooks               # Recent hook activity
  gt recent                    # Alias for gt trail
  gt recap --since 24h         # Activity from last 24 hours`,
	RunE: runTrailCommits, // Default to commits
}

var trailCommitsCmd = &cobra.Command{
	Use:   "commits",
	Short: "Show recent commits from agents",
	Long: `Show recent git commits made by agents.

By default, filters to commits from agents (using the configured
email domain). Use --all to include all commits.

Examples:
  gt trail commits              # Recent agent commits
  gt trail commits --since 1h   # Last hour of commits
  gt trail commits --all        # All commits (including non-agents)
  gt trail commits --json       # JSON output`,
	RunE: runTrailCommits,
}

var trailBeadsCmd = &cobra.Command{
	Use:   "beads",
	Short: "Show recent beads",
	Long: `Show recently created or modified beads (work items).

Examples:
  gt trail beads              # Recent beads
  gt trail beads --since 24h  # Last 24 hours of beads
  gt trail beads --json       # JSON output`,
	RunE: runTrailBeads,
}

var trailHooksCmd = &cobra.Command{
	Use:   "hooks",
	Short: "Show recent hook activity",
	Long: `Show recent hook activity (agents taking or dropping hooks).

Examples:
  gt trail hooks              # Recent hook activity
  gt trail hooks --since 1h   # Last hour of hook activity
  gt trail hooks --json       # JSON output`,
	RunE: runTrailHooks,
}

func init() {
	// Add flags to trail command
	trailCmd.PersistentFlags().StringVar(&trailSince, "since", "", "Show activity since this time (e.g., 1h, 24h, 7d)")
	trailCmd.PersistentFlags().IntVar(&trailLimit, "limit", 20, "Maximum number of items to show")
	trailCmd.PersistentFlags().BoolVar(&trailJSON, "json", false, "Output as JSON")
	trailCmd.PersistentFlags().BoolVar(&trailAll, "all", false, "Include all activity (not just agents)")

	// Add subcommands
	trailCmd.AddCommand(trailCommitsCmd)
	trailCmd.AddCommand(trailBeadsCmd)
	trailCmd.AddCommand(trailHooksCmd)

	// Register with root
	rootCmd.AddCommand(trailCmd)
}

// CommitEntry represents a git commit for output.
type CommitEntry struct {
	Hash      string    `json:"hash"`
	ShortHash string    `json:"short_hash"`
	Author    string    `json:"author"`
	Email     string    `json:"email"`
	Date      time.Time `json:"date"`
	DateRel   string    `json:"date_relative"`
	Subject   string    `json:"subject"`
	IsAgent   bool      `json:"is_agent"`
}

func runTrailCommits(cmd *cobra.Command, args []string) error {
	// Get email domain for agent filtering
	domain := DefaultAgentEmailDomain
	townRoot, err := workspace.FindFromCwd()
	if err == nil && townRoot != "" {
		settings, err := config.LoadOrCreateTownSettings(config.TownSettingsPath(townRoot))
		if err == nil && settings.AgentEmailDomain != "" {
			domain = settings.AgentEmailDomain
		}
	}

	// Build git log command
	gitArgs := []string{
		"log",
		"--format=%H|%h|%an|%ae|%aI|%ar|%s",
		fmt.Sprintf("-n%d", trailLimit*2), // Get extra to filter
	}

	if trailSince != "" {
		duration, err := parseDuration(trailSince)
		if err != nil {
			return fmt.Errorf("invalid --since value: %w", err)
		}
		since := time.Now().Add(-duration)
		gitArgs = append(gitArgs, fmt.Sprintf("--since=%s", since.Format(time.RFC3339)))
	}

	gitCmd := exec.Command("git", gitArgs...)
	output, err := gitCmd.Output()
	if err != nil {
		return fmt.Errorf("running git log: %w", err)
	}

	// Parse commits
	var commits []CommitEntry
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, "|", 7)
		if len(parts) < 7 {
			continue
		}

		date, _ := time.Parse(time.RFC3339, parts[4])
		isAgent := strings.HasSuffix(parts[3], "@"+domain)

		// Skip non-agents unless --all is set
		if !trailAll && !isAgent {
			continue
		}

		commits = append(commits, CommitEntry{
			Hash:      parts[0],
			ShortHash: parts[1],
			Author:    parts[2],
			Email:     parts[3],
			Date:      date,
			DateRel:   parts[5],
			Subject:   parts[6],
			IsAgent:   isAgent,
		})

		if len(commits) >= trailLimit {
			break
		}
	}

	if trailJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(commits)
	}

	// Text output
	if len(commits) == 0 {
		fmt.Println("No commits found")
		return nil
	}

	fmt.Printf("%s\n\n", style.Bold.Render("Recent Commits"))
	for _, c := range commits {
		authorLabel := c.Author
		if c.IsAgent {
			authorLabel = style.Bold.Render(c.Author)
		}

		fmt.Printf("%s %s\n", style.Dim.Render(c.ShortHash), c.Subject)
		fmt.Printf("    %s %s\n", authorLabel, style.Dim.Render(c.DateRel))
	}

	return nil
}

// BeadEntry represents a bead for output.
type BeadEntry struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	Agent     string    `json:"agent,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdateRel string    `json:"updated_relative"`
}

// HookEntry represents a hook/unhook event for output.
type HookEntry struct {
	Type      string    `json:"type"`
	Actor     string    `json:"actor"`
	Bead      string    `json:"bead,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	TimeRel   string    `json:"time_relative"`
}

func runTrailBeads(cmd *cobra.Command, args []string) error {
	// Find beads directory
	beadsDir, err := findBeadsDir()
	if err != nil {
		return fmt.Errorf("finding beads: %w", err)
	}

	// Use beads query to get recent beads
	beadsArgs := []string{
		"query",
		"--format", "{{.ID}}|{{.Title}}|{{.Status}}|{{.Agent}}|{{.UpdatedAt}}",
		"--limit", fmt.Sprintf("%d", trailLimit),
		"--sort", "-updated_at",
	}

	if trailSince != "" {
		duration, err := parseDuration(trailSince)
		if err != nil {
			return fmt.Errorf("invalid --since value: %w", err)
		}
		since := time.Now().Add(-duration)
		beadsArgs = append(beadsArgs, "--since", since.Format(time.RFC3339))
	}

	beadsCmd := exec.Command("beads", beadsArgs...)
	beadsCmd.Dir = beadsDir
	beadsCmd.Env = append(os.Environ(), "BEADS_DIR="+beadsDir+"/.beads")
	output, err := beadsCmd.Output()
	if err != nil {
		// Fallback: beads might not support all these flags
		// Try a simpler approach
		return runTrailBeadsSimple(beadsDir)
	}

	// Parse output
	var beads []BeadEntry
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, "|", 5)
		if len(parts) < 5 {
			continue
		}

		updatedAt, _ := time.Parse(time.RFC3339, parts[4])
		beads = append(beads, BeadEntry{
			ID:        parts[0],
			Title:     parts[1],
			Status:    parts[2],
			Agent:     parts[3],
			UpdatedAt: updatedAt,
			UpdateRel: relativeTime(updatedAt),
		})
	}

	if trailJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(beads)
	}

	// Text output
	if len(beads) == 0 {
		fmt.Println("No beads found")
		return nil
	}

	fmt.Printf("%s\n\n", style.Bold.Render("Recent Beads"))
	for _, b := range beads {
		statusColor := style.Dim
		switch b.Status {
		case "open":
			statusColor = style.Success
		case "in_progress":
			statusColor = style.Warning
		case "done", "merged":
			statusColor = style.Info
		}

		fmt.Printf("%s %s\n", style.Bold.Render(b.ID), b.Title)
		fmt.Printf("    %s %s", statusColor.Render(b.Status), style.Dim.Render(b.UpdateRel))
		if b.Agent != "" {
			fmt.Printf(" by %s", b.Agent)
		}
		fmt.Println()
	}

	return nil
}

func runTrailBeadsSimple(beadsDir string) error {
	// Simple fallback using beads list
	beadsCmd := exec.Command("beads", "list", "--limit", fmt.Sprintf("%d", trailLimit))
	beadsCmd.Dir = beadsDir
	beadsCmd.Env = append(os.Environ(), "BEADS_DIR="+beadsDir+"/.beads")
	beadsCmd.Stdout = os.Stdout
	beadsCmd.Stderr = os.Stderr
	return beadsCmd.Run()
}

func runTrailHooks(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return err
	}

	var since time.Time
	if trailSince != "" {
		duration, err := parseDuration(trailSince)
		if err != nil {
			return fmt.Errorf("invalid --since value: %w", err)
		}
		since = time.Now().Add(-duration)
	}

	entries, err := readHookTrailEntries(filepath.Join(townRoot, events.EventsFile), since, trailLimit)
	if err != nil {
		return err
	}

	if trailJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(entries)
	}

	if len(entries) == 0 {
		fmt.Println("No hook activity found")
		return nil
	}

	fmt.Printf("%s\n\n", style.Bold.Render("Hook Activity"))
	for _, entry := range entries {
		action := "hooked"
		if entry.Type == events.TypeUnhook {
			action = "unhooked"
		}
		target := "a bead"
		if entry.Bead != "" {
			target = entry.Bead
		}
		fmt.Printf(
			"%s %s %s %s\n",
			style.Dim.Render(entry.Timestamp.Format("2006-01-02 15:04")),
			style.Bold.Render(entry.Actor),
			action,
			style.Bold.Render(target),
		)
		if entry.TimeRel != "" {
			fmt.Printf("    %s\n", style.Dim.Render(entry.TimeRel))
		}
	}

	return nil
}

func readHookTrailEntries(eventsPath string, since time.Time, limit int) ([]HookEntry, error) {
	if limit <= 0 {
		return []HookEntry{}, nil
	}

	data, err := os.ReadFile(eventsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading events file: %w", err)
	}

	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil, nil
	}

	lines := strings.Split(trimmed, "\n")
	entryCap := len(lines)
	if limit < entryCap {
		entryCap = limit
	}
	entries := make([]HookEntry, 0, entryCap)
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}

		var event events.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if event.Type != events.TypeHook && event.Type != events.TypeUnhook {
			continue
		}

		ts, err := time.Parse(time.RFC3339, event.Timestamp)
		if err != nil {
			continue
		}
		if !since.IsZero() && ts.Before(since) {
			continue
		}

		bead := ""
		if rawBead, ok := event.Payload["bead"]; ok && rawBead != nil {
			bead = strings.TrimSpace(fmt.Sprint(rawBead))
		}

		actor := strings.TrimSpace(event.Actor)
		if actor == "" {
			actor = "unknown"
		}

		entries = append(entries, HookEntry{
			Type:      event.Type,
			Actor:     actor,
			Bead:      bead,
			Timestamp: ts,
			TimeRel:   relativeTime(ts),
		})
		if len(entries) >= limit {
			break
		}
	}

	return entries, nil
}

func findBeadsDir() (string, error) {
	// Try local beads dir first
	dir, err := findLocalBeadsDir()
	if err == nil {
		return dir, nil
	}

	// Fall back to town root
	return findMailWorkDir()
}

func relativeTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}

	diff := time.Since(t)
	switch {
	case diff < time.Minute:
		return "just now"
	case diff < time.Hour:
		mins := int(diff.Minutes())
		if mins == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", mins)
	case diff < 24*time.Hour:
		hours := int(diff.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	default:
		days := int(diff.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	}
}
