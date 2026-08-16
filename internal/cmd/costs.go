// Package cmd provides CLI commands for the gt tool.
package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"github.com/jonbaldie/gastown/internal/beads"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/constants"
	"github.com/jonbaldie/gastown/internal/session"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/tmux"
	"github.com/jonbaldie/gastown/internal/worker"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var (
	costsJSON    bool
	costsToday   bool
	costsWeek    bool
	costsByRole  bool
	costsByRig   bool
	costsVerbose bool

	// Record subcommand flags
	recordSession  string
	recordWorkItem string

	// Digest subcommand flags
	digestYesterday bool
	digestDate      string
	digestDryRun    bool
)

var costsCmd = &cobra.Command{
	Use:     "costs",
	GroupID: GroupDiag,
	Short:   "Show costs for running Claude sessions",
	Long: `Display costs for Claude Code sessions in Gas Town.

Costs are calculated from Claude Code transcript files in
$CLAUDE_CONFIG_DIR/projects/ (defaults to ~/.claude/projects/) by summing
token usage from assistant messages and applying model-specific pricing.

Examples:
  gt costs              # Live costs from running sessions
  gt costs --today      # Today's costs from log file (not yet digested)
  gt costs --week       # This week's costs from digest beads + today's log
  gt costs --by-role    # Breakdown by role (polecat, witness, etc.)
  gt costs --by-rig     # Breakdown by rig
  gt costs --json       # Output as JSON
  gt costs -v           # Show debug output for failures

Subcommands:
  gt costs record       # Record session cost to local log file (Stop hook)
  gt costs digest       # Aggregate log entries into daily digest bead (Deacon patrol)`,
	RunE: runCosts,
}

var costsRecordCmd = &cobra.Command{
	Use:   "record",
	Short: "Record session cost to local log file (called by Stop hook)",
	Long: `Record the final cost of a session to a local log file.

This command is intended to be called from a Claude Code Stop hook.
It reads token usage from the Claude Code transcript file
($CLAUDE_CONFIG_DIR/projects/... or ~/.claude/projects/...)
and calculates the cost based on model pricing, then appends it to
~/.gt/costs.jsonl. This is a simple append operation that never fails
due to database availability.

Session costs are aggregated daily by 'gt costs digest' into a single
permanent "Cost Report YYYY-MM-DD" bead for audit purposes.

Examples:
  gt costs record --session gt-gastown-toast
  gt costs record --session gt-gastown-toast --work-item gt-abc123`,
	RunE: runCostsRecord,
}

var costsDigestCmd = &cobra.Command{
	Use:   "digest",
	Short: "Aggregate session cost log entries into a daily digest bead",
	Long: `Aggregate session cost log entries into a permanent daily digest.

This command is intended to be run by Deacon patrol (daily) or manually.
It reads entries from ~/.gt/costs.jsonl for a target date, creates a single
aggregate "Cost Report YYYY-MM-DD" bead, then removes the source entries.

The resulting digest bead is permanent (synced via git) and provides
an audit trail without log-in-database pollution.

Examples:
  gt costs digest --yesterday   # Digest yesterday's costs (default for patrol)
  gt costs digest --date 2026-01-07  # Digest a specific date
  gt costs digest --yesterday --dry-run  # Preview without changes`,
	RunE: runCostsDigest,
}

func init() {
	rootCmd.AddCommand(costsCmd)
	costsCmd.Flags().BoolVar(&costsJSON, "json", false, "Output as JSON")
	costsCmd.Flags().BoolVar(&costsToday, "today", false, "Show today's total from session events")
	costsCmd.Flags().BoolVar(&costsWeek, "week", false, "Show this week's total from session events")
	costsCmd.Flags().BoolVar(&costsByRole, "by-role", false, "Show breakdown by role")
	costsCmd.Flags().BoolVar(&costsByRig, "by-rig", false, "Show breakdown by rig")
	costsCmd.Flags().BoolVarP(&costsVerbose, "verbose", "v", false, "Show debug output for failures")

	// Add record subcommand
	costsCmd.AddCommand(costsRecordCmd)
	costsRecordCmd.Flags().StringVar(&recordSession, "session", "", "Tmux session name to record")
	costsRecordCmd.Flags().StringVar(&recordWorkItem, "work-item", "", "Work item ID (bead) for attribution")

	// Add digest subcommand
	costsCmd.AddCommand(costsDigestCmd)
	costsDigestCmd.Flags().BoolVar(&digestYesterday, "yesterday", false, "Digest yesterday's costs (default for patrol)")
	costsDigestCmd.Flags().StringVar(&digestDate, "date", "", "Digest a specific date (YYYY-MM-DD)")
	costsDigestCmd.Flags().BoolVar(&digestDryRun, "dry-run", false, "Preview what would be done without making changes")

}

// SessionCost represents cost info for a single session.
type SessionCost struct {
	Session string  `json:"session"`
	Role    string  `json:"role"`
	Rig     string  `json:"rig,omitempty"`
	Worker  string  `json:"worker,omitempty"`
	Cost    float64 `json:"cost_usd"`
	Running bool    `json:"running"`
}

// CostEntry is a ledger entry for historical cost tracking.
type CostEntry struct {
	SessionID string    `json:"session_id"`
	Role      string    `json:"role"`
	Rig       string    `json:"rig,omitempty"`
	Worker    string    `json:"worker,omitempty"`
	AgentType string    `json:"agent_type,omitempty"`
	RunID     string    `json:"run_id,omitempty"`
	CostUSD   float64   `json:"cost_usd"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	WorkItem  string    `json:"work_item,omitempty"`
}

// CostsOutput is the JSON output structure.
type CostsOutput struct {
	Sessions    []SessionCost      `json:"sessions,omitempty"`
	Entries     []CostEntry        `json:"entries,omitempty"`
	Total       float64            `json:"total_usd"`
	ByRole      map[string]float64 `json:"by_role,omitempty"`
	ByRig       map[string]float64 `json:"by_rig,omitempty"`
	ByAgentType map[string]float64 `json:"by_agent_type,omitempty"`
	Period      string             `json:"period,omitempty"`
}

// costRegex matches cost patterns like "$1.23" or "$12.34"
var costRegex = regexp.MustCompile(`\$(\d+\.\d{2})`)

// TranscriptMessage represents a message from a Claude Code transcript file.
type TranscriptMessage struct {
	Type      string                 `json:"type"`
	SessionID string                 `json:"sessionId"`
	CWD       string                 `json:"cwd"`
	Message   *TranscriptMessageBody `json:"message,omitempty"`
}

// TranscriptMessageBody contains the message content and usage info.
type TranscriptMessageBody struct {
	Model string           `json:"model"`
	Role  string           `json:"role"`
	Usage *TranscriptUsage `json:"usage,omitempty"`
}

// TranscriptUsage contains token usage information.
type TranscriptUsage struct {
	InputTokens              int `json:"input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	OutputTokens             int `json:"output_tokens"`
}

// TokenUsage aggregates token usage across a session.
type TokenUsage struct {
	Model                    string
	InputTokens              int
	CacheCreationInputTokens int
	CacheReadInputTokens     int
	OutputTokens             int
}

// Model pricing per million tokens (as of Jan 2025).
// See: https://www.anthropic.com/pricing
var modelPricing = map[string]struct {
	InputPerMillion       float64
	OutputPerMillion      float64
	CacheReadPerMillion   float64 // 90% discount on input price
	CacheCreatePerMillion float64 // 25% premium on input price
}{
	// Claude Opus 4.5
	"claude-opus-4-5-20251101": {15.0, 75.0, 1.5, 18.75},
	// Claude Sonnet 4
	"claude-sonnet-4-20250514": {3.0, 15.0, 0.3, 3.75},
	// Claude Haiku 3.5
	"claude-3-5-haiku-20241022": {1.0, 5.0, 0.1, 1.25},
	// Fallback for unknown models (use Sonnet pricing)
	"default": {3.0, 15.0, 0.3, 3.75},
}

func runCosts(cmd *cobra.Command, args []string) error {
	// If querying ledger, use ledger functions
	if costsToday || costsWeek || costsByRole || costsByRig {
		return runCostsFromLedger()
	}

	// Default: show live costs from running sessions
	return runLiveCosts()
}

func runLiveCosts() error {
	t := tmux.NewTmux()

	// Get all tmux sessions
	sessions, err := t.ListSessions()
	if err != nil {
		return fmt.Errorf("listing sessions: %w", err)
	}

	var costs []SessionCost
	var total float64

	for _, sess := range sessions {
		// Only process Gas Town sessions
		if !session.IsKnownSession(sess) {
			continue
		}

		// Parse session name to get role/rig/worker
		role, rig, worker := parseSessionName(sess)

		// Get working directory of the session
		workDir, err := getTmuxSessionWorkDir(sess)
		if err != nil {
			if costsVerbose {
				fmt.Fprintf(os.Stderr, "[costs] could not get workdir for %s: %v\n", sess, err)
			}
			continue
		}

		// Extract cost from Claude transcript
		cost, err := extractCostFromWorkDir(workDir)
		if err != nil {
			if costsVerbose {
				fmt.Fprintf(os.Stderr, "[costs] could not extract cost for %s: %v\n", sess, err)
			}
			// Still include the session with zero cost
			cost = 0.0
		}

		// Check if an agent appears to be running
		running := t.IsAgentRunning(sess)

		costs = append(costs, SessionCost{
			Session: sess,
			Role:    role,
			Rig:     rig,
			Worker:  worker,
			Cost:    cost,
			Running: running,
		})
		total += cost
	}

	// Sort by session name
	sort.Slice(costs, func(i, j int) bool {
		return costs[i].Session < costs[j].Session
	})

	if costsJSON {
		return outputCostsJSON(CostsOutput{
			Sessions: costs,
			Total:    total,
		})
	}

	return outputCostsHuman(costs, total)
}

func runCostsFromLedger() error {
	now := time.Now()
	var entries []CostEntry
	var err error

	if costsToday {
		// For today: query ephemeral wisps (not yet digested)
		// This gives real-time view of today's costs
		entries, err = querySessionCostEntries(now)
		if err != nil {
			return fmt.Errorf("querying session cost wisps: %w", err)
		}
	} else if costsWeek {
		// For week: query digest beads (costs.digest events)
		// These are the aggregated daily reports
		entries, err = queryDigestBeads(7)
		if err != nil {
			return fmt.Errorf("querying digest beads: %w", err)
		}

		// Also include today's wisps (not yet digested)
		todayEntries, _ := querySessionCostEntries(now)
		entries = append(entries, todayEntries...)
	} else if costsByRole || costsByRig {
		// When using --by-role or --by-rig without time filter, default to today
		// (querying all historical events would be expensive and likely empty)
		entries, err = querySessionCostEntries(now)
		if err != nil {
			return fmt.Errorf("querying session cost entries: %w", err)
		}
	} else {
		// No time filter and no breakdown flags: query both digests and legacy session.ended events
		// (for backwards compatibility during migration)
		entries = querySessionEvents()
	}

	if len(entries) == 0 {
		fmt.Println(style.Dim.Render("No cost data found. Costs are recorded when sessions end."))
		return nil
	}

	// Calculate totals
	var total float64
	byRole := make(map[string]float64)
	byRig := make(map[string]float64)
	byAgent := make(map[string]float64)

	for _, entry := range entries {
		total += entry.CostUSD
		byRole[entry.Role] += entry.CostUSD
		if entry.Rig != "" {
			byRig[entry.Rig] += entry.CostUSD
		}
		if entry.AgentType != "" {
			byAgent[entry.AgentType] += entry.CostUSD
		}
	}

	// Build output
	output := CostsOutput{
		Total: total,
	}

	if costsByRole {
		output.ByRole = byRole
		output.Entries = entries
		if len(byAgent) > 0 {
			output.ByAgentType = byAgent
		}
	}
	if costsByRig {
		output.ByRig = byRig
	}

	// Set period label
	if costsToday {
		output.Period = "today"
	} else if costsWeek {
		output.Period = "this week"
	}

	if costsJSON {
		return outputCostsJSON(output)
	}

	return outputLedgerHuman(output, entries)
}

// SessionEvent represents a session.ended event from beads.
type SessionEvent struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	EventKind string    `json:"event_kind"`
	Actor     string    `json:"actor"`
	Target    string    `json:"target"`
	Payload   string    `json:"payload"`
}

// SessionPayload represents the JSON payload of a session event.
type SessionPayload struct {
	CostUSD   float64 `json:"cost_usd"`
	SessionID string  `json:"session_id"`
	Role      string  `json:"role"`
	Rig       string  `json:"rig"`
	Worker    string  `json:"worker"`
	EndedAt   string  `json:"ended_at"`
}

// EventListItem represents an event from bd list (minimal fields).
type EventListItem struct {
	ID string `json:"id"`
}

// querySessionEvents queries beads for session.ended events and converts them to CostEntry.
// It queries both town-level beads and all rig-level beads to find all session events.
// Errors from individual locations are logged (if verbose) but don't fail the query.
func querySessionEvents() []CostEntry {
	// Discover town root for cwd-based bd discovery
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		// Not in a Gas Town workspace - return empty list
		return nil
	}

	// Collect all beads locations to query
	beadsLocations := []string{townRoot}

	// Load rigs to find all rig beads locations
	rigsConfigPath := filepath.Join(townRoot, constants.DirMayor, constants.FileRigsJSON)
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err == nil && rigsConfig != nil {
		for rigName := range rigsConfig.Rigs {
			rigPath := filepath.Join(townRoot, rigName)
			// Verify rig has a beads database
			rigBeadsPath := filepath.Join(rigPath, constants.DirBeads)
			if _, statErr := os.Stat(rigBeadsPath); statErr == nil {
				beadsLocations = append(beadsLocations, rigPath)
			}
		}
	}

	// Query each beads location and merge results
	var allEntries []CostEntry
	seenIDs := make(map[string]bool)

	for _, location := range beadsLocations {
		entries, err := querySessionEventsFromLocation(location)
		if err != nil {
			// Log but continue with other locations
			if costsVerbose {
				fmt.Fprintf(os.Stderr, "[costs] query from %s failed: %v\n", location, err)
			}
			continue
		}

		// Deduplicate by event ID (use SessionID as key)
		for _, entry := range entries {
			key := entry.SessionID + entry.EndedAt.String()
			if !seenIDs[key] {
				seenIDs[key] = true
				allEntries = append(allEntries, entry)
			}
		}
	}

	return allEntries
}

// querySessionEventsFromLocation queries a single beads location for session.ended events.
func querySessionEventsFromLocation(location string) ([]CostEntry, error) {
	// Step 1: Get list of event IDs
	listArgs := []string{
		"list",
		"--type=event",
		"--all",
		"--limit=0",
		"--json",
	}

	listCmd := beads.Spawn(listArgs...)
	listCmd.Dir = location
	listOutput, err := listCmd.Output()
	if err != nil {
		// If bd fails (e.g., no beads database), return empty list
		return nil, nil
	}

	var listItems []EventListItem
	if err := json.Unmarshal(listOutput, &listItems); err != nil {
		return nil, fmt.Errorf("parsing event list: %w", err)
	}

	if len(listItems) == 0 {
		return nil, nil
	}

	// Step 2: Get full details for all events using bd show
	// (bd list doesn't include event_kind, actor, payload)
	showArgs := []string{"show", "--json"}
	for _, item := range listItems {
		showArgs = append(showArgs, item.ID)
	}

	showCmd := beads.Spawn(showArgs...)
	showCmd.Dir = location
	showOutput, err := showCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("showing events: %w", err)
	}

	var events []SessionEvent
	if err := json.Unmarshal(showOutput, &events); err != nil {
		return nil, fmt.Errorf("parsing event details: %w", err)
	}

	var entries []CostEntry
	for _, event := range events {
		// Filter for session.ended events only
		if event.EventKind != "session.ended" {
			continue
		}

		// Parse payload
		var payload SessionPayload
		if event.Payload != "" {
			if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
				continue // Skip malformed payloads
			}
		}

		// Parse ended_at from payload, fall back to created_at
		endedAt := event.CreatedAt
		if payload.EndedAt != "" {
			if parsed, err := time.Parse(time.RFC3339, payload.EndedAt); err == nil {
				endedAt = parsed
			}
		}

		entries = append(entries, CostEntry{
			SessionID: payload.SessionID,
			Role:      payload.Role,
			Rig:       payload.Rig,
			Worker:    payload.Worker,
			CostUSD:   payload.CostUSD,
			EndedAt:   endedAt,
			WorkItem:  event.Target,
		})
	}

	return entries, nil
}

// queryDigestBeads queries costs.digest events from the past N days and extracts session entries.
func queryDigestBeads(days int) ([]CostEntry, error) {
	// Get list of event IDs
	listArgs := []string{
		"list",
		"--type=event",
		"--all",
		"--limit=0",
		"--json",
	}

	listCmd := beads.Spawn(listArgs...)
	listOutput, err := listCmd.Output()
	if err != nil {
		return nil, nil
	}

	var listItems []EventListItem
	if err := json.Unmarshal(listOutput, &listItems); err != nil {
		return nil, fmt.Errorf("parsing event list: %w", err)
	}

	if len(listItems) == 0 {
		return nil, nil
	}

	// Get full details for all events
	showArgs := []string{"show", "--json"}
	for _, item := range listItems {
		showArgs = append(showArgs, item.ID)
	}

	showCmd := beads.Spawn(showArgs...)
	showOutput, err := showCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("showing events: %w", err)
	}

	var events []SessionEvent
	if err := json.Unmarshal(showOutput, &events); err != nil {
		return nil, fmt.Errorf("parsing event details: %w", err)
	}

	// Calculate date range
	now := time.Now()
	cutoff := now.AddDate(0, 0, -days)

	var entries []CostEntry
	for _, event := range events {
		// Filter for costs.digest events only
		if event.EventKind != "costs.digest" {
			continue
		}

		// Parse the digest payload
		var digest CostDigest
		if event.Payload != "" {
			if err := json.Unmarshal([]byte(event.Payload), &digest); err != nil {
				continue
			}
		}

		// Check date is within range
		digestDate, err := time.Parse("2006-01-02", digest.Date)
		if err != nil {
			continue
		}
		if digestDate.Before(cutoff) {
			continue
		}

		// If the digest has per-session data (old format), use it directly.
		// Otherwise, synthesize entries from the aggregate ByRole data.
		if len(digest.Sessions) > 0 {
			entries = append(entries, digest.Sessions...)
		} else {
			for role, cost := range digest.ByRole {
				entries = append(entries, CostEntry{
					SessionID: fmt.Sprintf("digest-%s-%s", digest.Date, role),
					Role:      role,
					CostUSD:   cost,
					EndedAt:   digestDate,
				})
			}
		}
	}

	return entries, nil
}

// parseSessionName extracts role, rig, and worker from a session name.
// Delegates to session.ParseSessionName for correct handling of hyphenated rig names.
func parseSessionName(sess string) (role, rig, worker string) {
	identity, err := session.ParseSessionName(sess)
	if err != nil {
		return "unknown", "", strings.TrimPrefix(sess, constants.SessionPrefix)
	}

	switch identity.Role {
	case session.RoleMayor:
		return constants.RoleMayor, "", "mayor"
	case session.RoleDeacon:
		// Boot is modeled as a deacon dog (Role: deacon, Name: boot).
		// Attribute its costs separately so token spend is visible per role.
		if identity.Name == "boot" {
			return constants.RoleBoot, "", "boot"
		}
		return constants.RoleDeacon, "", "deacon"
	case session.RoleWitness:
		return constants.RoleWitness, identity.Rig, ""
	case session.RoleRefinery:
		return constants.RoleRefinery, identity.Rig, ""
	case session.RoleCrew:
		return constants.RoleCrew, identity.Rig, identity.Name
	case session.RolePolecat:
		return constants.RolePolecat, identity.Rig, identity.Name
	default:
		return "unknown", identity.Rig, identity.Name
	}
}

// extractCost finds the most recent cost value in pane content.
// DEPRECATED: Claude Code no longer displays cost in a scrapable format.
// This is kept for backwards compatibility but always returns 0.0.
// Use extractCostFromTranscript instead.
func extractCost(content string) float64 {
	matches := costRegex.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return 0.0
	}

	// Get the last (most recent) match
	lastMatch := matches[len(matches)-1]
	if len(lastMatch) < 2 {
		return 0.0
	}

	var cost float64
	_, _ = fmt.Sscanf(lastMatch[1], "%f", &cost)
	return cost
}

// getClaudeProjectDir returns the Claude Code project directory for a working directory.
// Claude Code stores transcripts in <config-dir>/projects/<path-with-dashes-instead-of-slashes>/
// Respects CLAUDE_CONFIG_DIR env var, falling back to ~/.claude.
func getClaudeProjectDir(workDir string) (string, error) {
	configDir, err := config.ClaudeConfigDir()
	if err != nil {
		return "", err
	}

	// Convert path to Claude's directory naming: replace / and _ with -
	// Claude Code encodes both path separators and underscores as hyphens.
	// Keep leading slash - it becomes a leading dash in Claude's encoding.
	projectName := strings.ReplaceAll(workDir, "/", "-")
	projectName = strings.ReplaceAll(projectName, "_", "-")
	return filepath.Join(configDir, "projects", projectName), nil
}

// findLatestTranscript finds the most recently modified .jsonl file in a directory.
func findLatestTranscript(projectDir string) (string, error) {
	var latestPath string
	var latestTime time.Time

	err := filepath.WalkDir(projectDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != projectDir {
			return fs.SkipDir // Don't recurse into subdirectories
		}
		if !d.IsDir() && strings.HasSuffix(path, ".jsonl") {
			info, err := d.Info()
			if err != nil {
				return nil // Skip files we can't stat
			}
			if info.ModTime().After(latestTime) {
				latestTime = info.ModTime()
				latestPath = path
			}
		}
		return nil
	})

	if err != nil {
		return "", err
	}
	if latestPath == "" {
		return "", fmt.Errorf("no transcript files found in %s", projectDir)
	}
	return latestPath, nil
}

// parseTranscriptUsage reads a transcript file and sums token usage from assistant messages.
func parseTranscriptUsage(transcriptPath string) (*TokenUsage, error) {
	file, err := os.Open(transcriptPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	usage := &TokenUsage{}
	scanner := bufio.NewScanner(file)
	// Increase buffer for potentially large JSON lines
	buf := make([]byte, 0, 256*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var msg TranscriptMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue // Skip malformed lines
		}

		// Only process assistant messages with usage info
		if msg.Type != "assistant" || msg.Message == nil || msg.Message.Usage == nil {
			continue
		}

		// Capture the model (use first one found, they should all be the same)
		if usage.Model == "" && msg.Message.Model != "" {
			usage.Model = msg.Message.Model
		}

		// Sum token usage
		u := msg.Message.Usage
		usage.InputTokens += u.InputTokens
		usage.CacheCreationInputTokens += u.CacheCreationInputTokens
		usage.CacheReadInputTokens += u.CacheReadInputTokens
		usage.OutputTokens += u.OutputTokens
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return usage, nil
}

// calculateCost converts token usage to USD cost based on model pricing.
func calculateCost(usage *TokenUsage) float64 {
	if usage == nil {
		return 0.0
	}

	// Look up pricing for the model
	pricing, ok := modelPricing[usage.Model]
	if !ok {
		pricing = modelPricing["default"]
	}

	// Calculate cost (prices are per million tokens)
	inputCost := float64(usage.InputTokens) / 1_000_000 * pricing.InputPerMillion
	cacheReadCost := float64(usage.CacheReadInputTokens) / 1_000_000 * pricing.CacheReadPerMillion
	cacheCreateCost := float64(usage.CacheCreationInputTokens) / 1_000_000 * pricing.CacheCreatePerMillion
	outputCost := float64(usage.OutputTokens) / 1_000_000 * pricing.OutputPerMillion

	return inputCost + cacheReadCost + cacheCreateCost + outputCost
}

// extractCostFromWorkDir extracts cost from Claude Code transcript for a working directory.
// This reads the most recent transcript file and sums all token usage.
func extractCostFromWorkDir(workDir string) (float64, error) {
	projectDir, err := getClaudeProjectDir(workDir)
	if err != nil {
		return 0, fmt.Errorf("getting project dir: %w", err)
	}

	transcriptPath, err := findLatestTranscript(projectDir)
	if err != nil {
		return 0, fmt.Errorf("finding transcript: %w", err)
	}

	usage, err := parseTranscriptUsage(transcriptPath)
	if err != nil {
		return 0, fmt.Errorf("parsing transcript: %w", err)
	}

	return calculateCost(usage), nil
}

// getTmuxSessionWorkDir gets the current working directory of a tmux session.
func getTmuxSessionWorkDir(session string) (string, error) {
	cmd := tmux.BuildCommand("display-message", "-t", session, "-p", "#{pane_current_path}")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func outputCostsJSON(output CostsOutput) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(output)
}

func outputCostsHuman(costs []SessionCost, total float64) error {
	if len(costs) == 0 {
		fmt.Println(style.Dim.Render("No Gas Town sessions found"))
		return nil
	}

	fmt.Printf("\n%s Live Session Costs\n\n", style.Bold.Render("💰"))

	// Print table header
	fmt.Printf("%-25s %-10s %-15s %10s %8s\n",
		"Session", "Role", "Rig/Worker", "Cost", "Status")
	fmt.Println(strings.Repeat("─", 75))

	// Print each session
	for _, c := range costs {
		statusIcon := style.Success.Render("●")
		if !c.Running {
			statusIcon = style.Dim.Render("○")
		}

		rigWorker := c.Rig
		if c.Worker != "" && c.Worker != c.Rig {
			if rigWorker != "" {
				rigWorker += "/" + c.Worker
			} else {
				rigWorker = c.Worker
			}
		}

		fmt.Printf("%-25s %-10s %-15s %10s %8s\n",
			c.Session,
			c.Role,
			rigWorker,
			fmt.Sprintf("$%.2f", c.Cost),
			statusIcon)
	}

	// Print total
	fmt.Println(strings.Repeat("─", 75))
	fmt.Printf("%s %s\n", style.Bold.Render("Total:"), fmt.Sprintf("$%.2f", total))

	return nil
}

func outputLedgerHuman(output CostsOutput, entries []CostEntry) error {
	periodStr := ""
	if output.Period != "" {
		periodStr = fmt.Sprintf(" (%s)", output.Period)
	}

	fmt.Printf("\n%s Cost Summary%s\n\n", style.Bold.Render("📊"), periodStr)

	// Total
	fmt.Printf("%s $%.2f\n", style.Bold.Render("Total:"), output.Total)

	// By role breakdown
	if output.ByRole != nil && len(output.ByRole) > 0 {
		fmt.Printf("\n%s\n", style.Bold.Render("By Role:"))
		for role, cost := range output.ByRole {
			icon := constants.RoleEmoji(role)
			fmt.Printf("  %s %-12s $%.2f\n", icon, role, cost)
		}
	}

	// By rig breakdown
	if output.ByRig != nil && len(output.ByRig) > 0 {
		fmt.Printf("\n%s\n", style.Bold.Render("By Rig:"))
		for rig, cost := range output.ByRig {
			fmt.Printf("  %-15s $%.2f\n", rig, cost)
		}
	}

	if output.ByAgentType != nil && len(output.ByAgentType) > 0 {
		fmt.Printf("\n%s\n", style.Bold.Render("By Agent Type:"))
		for agent, cost := range output.ByAgentType {
			fmt.Printf("  %-15s $%.2f\n", agent, cost)
		}
	}

	if len(entries) > 0 {
		fmt.Printf("\n%s\n", style.Bold.Render("Runs:"))
		for _, e := range entries {
			bead := e.WorkItem
			if bead == "" {
				bead = "-"
			}
			agent := e.AgentType
			if agent == "" {
				agent = "-"
			}
			fmt.Printf("  bead=%s role=%s agent=%s $%.2f\n", bead, e.Role, agent, e.CostUSD)
		}
	}

	// Session count
	fmt.Printf("\n%s %d sessions\n", style.Dim.Render("Entries:"), len(entries))

	return nil
}

// CostLogEntry represents a single entry in the costs.jsonl log file.
type CostLogEntry struct {
	SessionID string    `json:"session_id"`
	Role      string    `json:"role"`
	Rig       string    `json:"rig,omitempty"`
	Worker    string    `json:"worker,omitempty"`
	CostUSD   float64   `json:"cost_usd"`
	EndedAt   time.Time `json:"ended_at"`
	WorkItem  string    `json:"work_item,omitempty"`
}

// getCostsLogPath returns the path to the costs log file.
// Location: $GT_HOME/.gt/costs.jsonl when GT_HOME is set, otherwise ~/.gt/.
func getCostsLogPath() string {
	return filepath.Join(gtDataDir(), "costs.jsonl")
}

// runCostsRecord captures the final cost from a session and appends it to a local log file.
// This is called by the Claude Code Stop hook. It's designed to never fail due to
// database availability - it's a simple file append operation.
func runCostsRecord(cmd *cobra.Command, args []string) error {
	// Get session from flag or try to detect from environment
	session := recordSession
	if session == "" {
		session = os.Getenv("GT_SESSION")
	}
	if session == "" {
		// Derive session name from GT_* environment variables
		session = deriveSessionName()
	}
	if session == "" {
		// Try to detect current tmux session (works when running inside tmux)
		session = detectCurrentTmuxSession()
	}
	if session == "" {
		// Not a Gas Town session (e.g., Claude Code launched outside gt agent system).
		// Exit silently — no costs to record.
		if costsVerbose {
			fmt.Fprintf(os.Stderr, "[costs] no session context found, skipping costs record\n")
		}
		return nil
	}

	// Get working directory from environment or tmux session
	workDir := os.Getenv("GT_CWD")
	if workDir == "" {
		// Try to get from tmux session
		var err error
		workDir, err = getTmuxSessionWorkDir(session)
		if err != nil {
			if costsVerbose {
				fmt.Fprintf(os.Stderr, "[costs] could not get workdir for %s: %v\n", session, err)
			}
		}
	}

	// Extract cost from Claude transcript
	var cost float64
	if workDir != "" {
		var err error
		cost, err = extractCostFromWorkDir(workDir)
		if err != nil {
			if costsVerbose {
				fmt.Fprintf(os.Stderr, "[costs] could not extract cost from transcript: %v\n", err)
			}
			cost = 0.0
		}
	}

	// Parse session name
	role, rig, worker := parseSessionName(session)

	// Build log entry
	entry := CostLogEntry{
		SessionID: session,
		Role:      role,
		Rig:       rig,
		Worker:    worker,
		CostUSD:   cost,
		EndedAt:   time.Now(),
		WorkItem:  recordWorkItem,
	}

	// Marshal to JSON
	entryJSON, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshaling cost entry: %w", err)
	}

	// Append to log file
	logPath := getCostsLogPath()

	// Ensure directory exists
	logDir := filepath.Dir(logPath)
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return fmt.Errorf("creating log directory: %w", err)
	}

	// Open file for append (create if doesn't exist).
	// O_APPEND writes are atomic on POSIX for writes < PIPE_BUF (~4KB).
	// A JSON log entry is ~200 bytes, so concurrent appends are safe.
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("opening costs log: %w", err)
	}
	defer f.Close()

	// Write entry with newline
	if _, err := f.Write(append(entryJSON, '\n')); err != nil {
		return fmt.Errorf("writing to costs log: %w", err)
	}

	// Output confirmation (silent if cost is zero and no work item)
	if cost > 0 || recordWorkItem != "" {
		fmt.Printf("%s Recorded $%.2f for %s", style.Success.Render("✓"), cost, session)
		if recordWorkItem != "" {
			fmt.Printf(" (work: %s)", recordWorkItem)
		}
		fmt.Println()
	}

	return nil
}

// deriveSessionName derives the tmux session name from GT_* environment variables.
// Uses session.* helpers for canonical naming. Parses GT_ROLE via parseRoleString
// so compound forms (e.g. "gastown/witness") resolve to their canonical session names.
func deriveSessionName() string {
	role := os.Getenv("GT_ROLE")
	rig := os.Getenv("GT_RIG")
	polecat := os.Getenv("GT_POLECAT")
	crew := os.Getenv("GT_CREW")

	// Parse GT_ROLE once to handle both bare and compound forms.
	parsedRole, _, parsedName := parseRoleString(role)

	// Polecat: {prefix}-{polecat}
	// Gate on GT_ROLE: coordinators may have stale GT_POLECAT from spawning polecats.
	if polecat != "" && rig != "" {
		if role == "" || parsedRole == RolePolecat {
			return session.PolecatSessionName(session.PrefixFor(rig), polecat)
		}
	}

	// Crew: {prefix}-crew-{crew} (from GT_CREW or parsed compound role)
	if parsedRole == RoleCrew && parsedName != "" && rig != "" {
		return session.CrewSessionName(session.PrefixFor(rig), parsedName)
	}
	if crew != "" && rig != "" {
		return session.CrewSessionName(session.PrefixFor(rig), crew)
	}

	// Town-level roles (mayor, deacon)
	if parsedRole == RoleMayor {
		return session.MayorSessionName()
	}
	if parsedRole == RoleDeacon {
		return session.DeaconSessionName()
	}

	// Rig-based roles (witness, refinery): {prefix}-{role}
	if rig != "" {
		prefix := session.PrefixFor(rig)
		switch parsedRole {
		case RoleWitness:
			return session.WitnessSessionName(prefix)
		case RoleRefinery:
			return session.RefinerySessionName(prefix)
		}
	}

	return ""
}

// detectCurrentTmuxSession returns the current tmux session name if running inside tmux.
// Uses `tmux display-message -p '#S'` which prints the session name.
// Note: TMUX_PANE may not be inherited when Claude Code runs bash commands,
// so we return "" as a safe fallback.
func detectCurrentTmuxSession() string {
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		return ""
	}
	cmd := tmux.BuildCommand("display-message", "-t", pane, "-p", "#S")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}

	session := strings.TrimSpace(string(output))
	// Only return if it looks like a Gas Town session
	// Accept both gt- (rig sessions) and hq- (town-level sessions like hq-mayor)
	if strings.HasPrefix(session, constants.SessionPrefix) || strings.HasPrefix(session, constants.HQSessionPrefix) {
		return session
	}
	return ""
}

// CostDigest represents the aggregated daily cost report.
type CostDigest struct {
	Date         string             `json:"date"`
	TotalUSD     float64            `json:"total_usd"`
	SessionCount int                `json:"session_count"`
	Sessions     []CostEntry        `json:"sessions,omitempty"`
	ByRole       map[string]float64 `json:"by_role"`
	ByRig        map[string]float64 `json:"by_rig,omitempty"`
}

// CostDigestPayload is the compact payload stored in the bead.
// It excludes per-session details to avoid exceeding Dolt column size limits.
type CostDigestPayload struct {
	Date         string             `json:"date"`
	TotalUSD     float64            `json:"total_usd"`
	SessionCount int                `json:"session_count"`
	ByRole       map[string]float64 `json:"by_role"`
	ByRig        map[string]float64 `json:"by_rig,omitempty"`
}

// runCostsDigest aggregates session cost entries into a daily digest bead.
func runCostsDigest(cmd *cobra.Command, args []string) error {
	// Determine target date
	var targetDate time.Time

	if digestDate != "" {
		parsed, err := time.Parse("2006-01-02", digestDate)
		if err != nil {
			return fmt.Errorf("invalid date format (use YYYY-MM-DD): %w", err)
		}
		targetDate = parsed
	} else if digestYesterday {
		targetDate = time.Now().AddDate(0, 0, -1)
	} else {
		return fmt.Errorf("specify --yesterday or --date YYYY-MM-DD")
	}

	dateStr := targetDate.Format("2006-01-02")

	// Query session cost entries for target date
	costEntries, err := querySessionCostEntries(targetDate)
	if err != nil {
		return fmt.Errorf("querying session cost entries: %w", err)
	}

	if len(costEntries) == 0 {
		fmt.Printf("%s No session cost entries found for %s\n", style.Dim.Render("○"), dateStr)
		return nil
	}

	// Build digest
	digest := CostDigest{
		Date:     dateStr,
		Sessions: costEntries,
		ByRole:   make(map[string]float64),
		ByRig:    make(map[string]float64),
	}

	for _, e := range costEntries {
		digest.TotalUSD += e.CostUSD
		digest.SessionCount++
		digest.ByRole[e.Role] += e.CostUSD
		if e.Rig != "" {
			digest.ByRig[e.Rig] += e.CostUSD
		}
	}

	if digestDryRun {
		fmt.Printf("%s [DRY RUN] Would create Cost Report %s:\n", style.Bold.Render("📊"), dateStr)
		fmt.Printf("  Total: $%.2f\n", digest.TotalUSD)
		fmt.Printf("  Sessions: %d\n", digest.SessionCount)
		fmt.Printf("  By Role:\n")
		for role, cost := range digest.ByRole {
			fmt.Printf("    %s: $%.2f\n", role, cost)
		}
		if len(digest.ByRig) > 0 {
			fmt.Printf("  By Rig:\n")
			for rig, cost := range digest.ByRig {
				fmt.Printf("    %s: $%.2f\n", rig, cost)
			}
		}
		return nil
	}

	// Create permanent digest bead
	digestID, err := createCostDigestBead(digest)
	if err != nil {
		return fmt.Errorf("creating digest bead: %w", err)
	}

	// Delete source entries from log file
	deletedCount, deleteErr := deleteSessionCostEntries(targetDate)
	if deleteErr != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to delete some source entries: %v\n", deleteErr)
	}

	fmt.Printf("%s Created Cost Report %s (bead: %s)\n", style.Success.Render("✓"), dateStr, digestID)
	fmt.Printf("  Total: $%.2f from %d sessions\n", digest.TotalUSD, digest.SessionCount)
	if deletedCount > 0 {
		fmt.Printf("  Removed %d entries from costs log\n", deletedCount)
	}

	return nil
}

// querySessionCostEntries reads session cost entries from the local log file for a target date.
func querySessionCostEntries(targetDate time.Time) ([]CostEntry, error) {
	logPath := getCostsLogPath()

	// Read log file
	targetDay := targetDate.UTC().Format("2006-01-02")
	var entries []CostEntry

	data, err := os.ReadFile(logPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("reading costs log: %w", err)
	}

	// Parse each line as a CostLogEntry
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var logEntry CostLogEntry
		if err := json.Unmarshal([]byte(line), &logEntry); err != nil {
			if costsVerbose {
				fmt.Fprintf(os.Stderr, "[costs] failed to parse log entry: %v\n", err)
			}
			continue
		}

		// Filter by target date
		if logEntry.EndedAt.Format("2006-01-02") != targetDay {
			continue
		}

		entries = append(entries, CostEntry{
			SessionID: logEntry.SessionID,
			Role:      logEntry.Role,
			Rig:       logEntry.Rig,
			Worker:    logEntry.Worker,
			CostUSD:   logEntry.CostUSD,
			EndedAt:   logEntry.EndedAt,
			WorkItem:  logEntry.WorkItem,
		})
	}

	townRoot, townErr := workspace.FindFromCwd()
	if townErr != nil || townRoot == "" {
		if env := os.Getenv("GT_TOWN_ROOT"); env != "" {
			townRoot = env
			townErr = nil
		}
	}
	if townErr == nil && townRoot != "" {
		if recs, err := worker.ReadCosts(townRoot); err == nil {
			for _, rec := range recs {
				if !rec.Timestamp.IsZero() && rec.Timestamp.UTC().Format("2006-01-02") != targetDay {
					continue
				}
				entries = append(entries, CostEntry{
					SessionID: rec.SessionID,
					Role:      rec.Role,
					Rig:       rec.Rig,
					Worker:    rec.AgentName,
					AgentType: rec.AgentType,
					RunID:     rec.RunID,
					CostUSD:   rec.CostUSD,
					EndedAt:   rec.Timestamp,
					WorkItem:  rec.BeadID,
				})
			}
		}
	}

	return entries, nil
}

// createCostDigestBead creates a permanent bead for the daily cost digest.
func createCostDigestBead(digest CostDigest) (string, error) {
	// Build description with aggregate data
	var desc strings.Builder
	desc.WriteString(fmt.Sprintf("Daily cost aggregate for %s.\n\n", digest.Date))
	desc.WriteString(fmt.Sprintf("**Total:** $%.2f from %d sessions\n\n", digest.TotalUSD, digest.SessionCount))

	if len(digest.ByRole) > 0 {
		desc.WriteString("## By Role\n")
		roles := make([]string, 0, len(digest.ByRole))
		for role := range digest.ByRole {
			roles = append(roles, role)
		}
		sort.Strings(roles)
		for _, role := range roles {
			icon := constants.RoleEmoji(role)
			desc.WriteString(fmt.Sprintf("- %s %s: $%.2f\n", icon, role, digest.ByRole[role]))
		}
		desc.WriteString("\n")
	}

	if len(digest.ByRig) > 0 {
		desc.WriteString("## By Rig\n")
		rigs := make([]string, 0, len(digest.ByRig))
		for rig := range digest.ByRig {
			rigs = append(rigs, rig)
		}
		sort.Strings(rigs)
		for _, rig := range rigs {
			desc.WriteString(fmt.Sprintf("- %s: $%.2f\n", rig, digest.ByRig[rig]))
		}
		desc.WriteString("\n")
	}

	// Build compact payload (aggregate only, no per-session details).
	// Per-session details can be thousands of records and exceed Dolt column limits.
	compactPayload := CostDigestPayload{
		Date:         digest.Date,
		TotalUSD:     digest.TotalUSD,
		SessionCount: digest.SessionCount,
		ByRole:       digest.ByRole,
		ByRig:        digest.ByRig,
	}
	payloadJSON, err := json.Marshal(compactPayload)
	if err != nil {
		return "", fmt.Errorf("marshaling digest payload: %w", err)
	}

	// Create the digest bead (NOT ephemeral - this is permanent)
	title := fmt.Sprintf("Cost Report %s", digest.Date)
	bdArgs := []string{
		"create",
		"--type=event",
		"--title=" + title,
		"--event-category=costs.digest",
		"--event-payload=" + string(payloadJSON),
		"--description=" + desc.String(),
		"--silent",
	}

	bdCmd := beads.Spawn(bdArgs...)
	output, err := bdCmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("creating digest bead: %w\nOutput: %s", err, string(output))
	}

	digestID := strings.TrimSpace(string(output))

	// Auto-close the digest (it's an audit record, not work)
	closeCmd := beads.Spawn("close", digestID, "--reason=daily cost digest")
	_ = closeCmd.Run() // Best effort

	return digestID, nil
}

// deleteSessionCostEntries removes entries for a target date from the costs log file.
// It rewrites the file without the entries for that date.
func deleteSessionCostEntries(targetDate time.Time) (int, error) {
	logPath := getCostsLogPath()

	// Read log file
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil // No log file
		}
		return 0, fmt.Errorf("reading costs log: %w", err)
	}

	targetDay := targetDate.Format("2006-01-02")
	var keepLines []string
	deletedCount := 0

	// Filter out entries for target date
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var logEntry CostLogEntry
		if err := json.Unmarshal([]byte(line), &logEntry); err != nil {
			// Keep unparseable lines (shouldn't happen but be safe)
			keepLines = append(keepLines, line)
			continue
		}

		// Remove entries from target date
		if logEntry.EndedAt.Format("2006-01-02") == targetDay {
			deletedCount++
			continue
		}

		keepLines = append(keepLines, line)
	}

	if deletedCount == 0 {
		return 0, nil
	}

	// Rewrite file without deleted entries
	newContent := strings.Join(keepLines, "\n")
	if len(keepLines) > 0 {
		newContent += "\n"
	}

	if err := os.WriteFile(logPath, []byte(newContent), 0644); err != nil {
		return 0, fmt.Errorf("rewriting costs log: %w", err)
	}

	return deletedCount, nil
}
