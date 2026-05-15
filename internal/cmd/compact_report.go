package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/style"
)

// beadIDLine matches a bead ID printed on its own line by `bd create`.
// We scan `bd create` output (which may include startup warnings such as the
// "beads.role not configured (GH#2950)" notice) for the last line that is
// just a bead ID, instead of trusting that stdout contained only the ID.
//
// IDs look like `hq-1a2b`, `co-rln`, `h25-mrd`, `my-rig-abc`: a rig prefix,
// dash, then a short alphanumeric token.
var beadIDLine = regexp.MustCompile(`(?m)^\s*([a-z][a-z0-9-]{0,19}-[a-z0-9]+)\s*$`)

// extractBeadID returns the last line of `output` that is a bare bead ID.
// Returns an error if no bead-ID-shaped line is found.
//
// This guards against `bd` emitting startup warnings before the ID — see
// gastown issue: "Fix compact_report.go beadID capture (corrupts on stdout
// warnings)". Without this, a noisy stdout poisons `beadID`, the subsequent
// `bd close <beadID>` silently fails, the audit bead stays open, and the
// daily-digest idempotency check (filtered by status=closed) never matches —
// producing one duplicate digest mail per patrol cycle.
func extractBeadID(output string) (string, error) {
	matches := beadIDLine.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		preview := strings.TrimSpace(output)
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		return "", fmt.Errorf("no bead ID found in bd output: %q", preview)
	}
	return matches[len(matches)-1][1], nil
}

var (
	compactReportDryRun  bool
	compactReportWeekly  bool
	compactReportVerbose bool
	compactReportDate    string
	compactReportJSON    bool
)

// wispCategory maps individual wisp types to display categories.
// Matches the design doc: Heartbeats, Patrols, Errors, Untyped.
var wispCategoryMap = map[string]string{
	"heartbeat":  "Heartbeats",
	"ping":       "Heartbeats",
	"patrol":     "Patrols",
	"gc_report":  "Patrols",
	"error":      "Errors",
	"recovery":   "Errors",
	"escalation": "Errors",
}

// categoryOrder is the display order for categories in reports.
var categoryOrder = []string{"Heartbeats", "Patrols", "Errors", "Untyped"}

// categoryStats tracks per-category compaction statistics.
type categoryStats struct {
	Deleted  int `json:"deleted"`
	Promoted int `json:"promoted"`
	Active   int `json:"active"`
}

// compactReport is the full daily digest data.
type compactReport struct {
	Date       string                    `json:"date"`
	Categories map[string]*categoryStats `json:"categories"`
	Promotions []compactAction           `json:"promotions,omitempty"`
	Anomalies  []string                  `json:"anomalies,omitempty"`
	Errors     []string                  `json:"errors,omitempty"`
}

// weeklyRollup aggregates daily reports for trend data.
type weeklyRollup struct {
	WeekStart  string                    `json:"week_start"`
	WeekEnd    string                    `json:"week_end"`
	Days       int                       `json:"days"`
	Totals     map[string]*categoryStats `json:"totals"`
	Promotions int                       `json:"total_promotions"`
	Anomalies  []string                  `json:"anomalies,omitempty"`
}

var compactReportCmd = &cobra.Command{
	Use:   "report",
	Short: "Generate and send compaction digest report",
	Long: `Generate a compaction digest and send it to deacon/ (cc mayor/).

The daily digest shows per-category breakdown of deleted, promoted, and active
wisps, plus any promotions with reasons and detected anomalies.

The weekly rollup (--weekly) aggregates the past 7 days of compaction event
beads and sends trend data to mayor/.

Examples:
  gt compact report              # Run compaction + send daily digest
  gt compact report --dry-run    # Preview the report without sending
  gt compact report --weekly     # Send weekly rollup to mayor/
  gt compact report --json       # Output report as JSON`,
	RunE: runCompactReport,
}

func init() {
	compactReportCmd.Flags().BoolVar(&compactReportDryRun, "dry-run", false, "Preview report without sending")
	compactReportCmd.Flags().BoolVar(&compactReportWeekly, "weekly", false, "Generate weekly rollup instead of daily digest")
	compactReportCmd.Flags().BoolVarP(&compactReportVerbose, "verbose", "v", false, "Verbose output")
	compactReportCmd.Flags().StringVar(&compactReportDate, "date", "", "Report for specific date (YYYY-MM-DD); default: today")
	compactReportCmd.Flags().BoolVar(&compactReportJSON, "json", false, "Output report as JSON")

	compactCmd.AddCommand(compactReportCmd)
}

func runCompactReport(cmd *cobra.Command, args []string) error {
	if compactReportWeekly {
		return runWeeklyRollup()
	}
	return runDailyDigest()
}

func runDailyDigest() error {
	now := time.Now().UTC()
	dateStr := now.Format("2006-01-02")
	if compactReportDate != "" {
		if _, err := time.Parse("2006-01-02", compactReportDate); err != nil {
			return fmt.Errorf("invalid date format (use YYYY-MM-DD): %w", err)
		}
		dateStr = compactReportDate
	}

	// Idempotency check: see if digest already exists for this date
	existingID, err := findExistingCompactReport(dateStr)
	if err != nil {
		// Non-fatal: continue with creation attempt
		if compactReportVerbose {
			fmt.Fprintf(os.Stderr, "warning: idempotency check failed: %v\n", err)
		}
	} else if existingID != "" {
		fmt.Printf("%s Compaction digest already sent for %s (bead: %s)\n",
			style.Dim.Render("○"), dateStr, existingID)
		return nil
	}

	// Run compaction with --json to get results
	compactOut, err := exec.Command("gt", "compact", "--json").Output()
	if err != nil {
		return fmt.Errorf("running compaction: %w", err)
	}

	var result compactResult
	if err := json.Unmarshal(extractJSONObject(compactOut), &result); err != nil {
		return fmt.Errorf("parsing compaction output: %w", err)
	}

	// Query active wisps for the "Active" column
	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working dir: %w", err)
	}
	bd := beads.New(workDir)
	activeWisps, err := listWisps(bd)
	if err != nil {
		return fmt.Errorf("listing active wisps: %w", err)
	}

	// Build report
	report := buildReport(dateStr, &result, activeWisps)

	// Detect anomalies
	report.Anomalies = detectAnomalies(report)

	if compactReportJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}

	// Format as markdown
	markdown := formatDailyDigest(report)

	if compactReportDryRun {
		fmt.Printf("%s [DRY RUN] Daily compaction digest for %s:\n\n", style.Dim.Render("[dry-run]"), dateStr)
		fmt.Println(markdown)
		return nil
	}

	// Create permanent event bead for audit trail
	beadID, err := createCompactReportBead(report, markdown)
	if err != nil {
		return fmt.Errorf("recording compact report audit bead: %w", err)
	}

	// Send mail to deacon/, cc mayor/
	if err := sendCompactDigest(dateStr, markdown); err != nil {
		return fmt.Errorf("sending digest: %w", err)
	}

	fmt.Printf("%s Compaction digest sent for %s\n", style.Success.Render("✓"), dateStr)
	if beadID != "" {
		fmt.Printf("  Audit bead: %s\n", beadID)
	}

	return nil
}

// buildReport aggregates compaction results by category.
func buildReport(dateStr string, result *compactResult, activeWisps []*compactIssue) *compactReport {
	report := &compactReport{
		Date:       dateStr,
		Categories: make(map[string]*categoryStats),
		Errors:     result.Errors,
	}

	// Initialize all categories
	for _, cat := range categoryOrder {
		report.Categories[cat] = &categoryStats{}
	}

	// Tally deleted by category
	for _, d := range result.Deleted {
		cat := wispTypeToCategory(d.WispType, d.Title)
		report.Categories[cat].Deleted++
	}

	// Tally promoted by category
	for _, p := range result.Promoted {
		cat := wispTypeToCategory(p.WispType, p.Title)
		report.Categories[cat].Promoted++
		report.Promotions = append(report.Promotions, p)
	}

	// Tally active wisps by category
	for _, w := range activeWisps {
		cat := wispTypeToCategory(w.WispType, w.Title)
		report.Categories[cat].Active++
	}

	return report
}

// wispTypeToCategory maps a wisp_type string to its display category.
func wispTypeToCategory(wispType, title string) string {
	if cat, ok := wispCategoryMap[wispType]; ok {
		return cat
	}
	if wispType == "" && strings.Contains(strings.ToLower(title), "patrol") {
		return "Patrols"
	}
	return "Untyped"
}

// detectAnomalies checks for unusual patterns in the compaction data.
func detectAnomalies(report *compactReport) []string {
	var anomalies []string

	for _, cat := range categoryOrder {
		stats := report.Categories[cat]

		// High deletion volume (> 1000 in a single day for heartbeats suggests restart loop)
		if cat == "Heartbeats" && stats.Deleted > 1000 {
			anomalies = append(anomalies, fmt.Sprintf(
				"%dx normal heartbeat volume (possible restart loop)",
				stats.Deleted/300)) // ~300/day is baseline for a rig
		}

		// Zero patrols is suspicious (witness may be down)
		if cat == "Patrols" && stats.Active == 0 && stats.Deleted == 0 && stats.Promoted == 0 {
			anomalies = append(anomalies, "0 patrol wisps (patrol agents may be down)")
		}

		// High promotion rate (> 50% of non-skipped suggests miscategorized wisps)
		total := stats.Deleted + stats.Promoted
		if total > 10 && stats.Promoted > total/2 {
			anomalies = append(anomalies,
				fmt.Sprintf("%s: high promotion rate (%d/%d) — review wisp classification",
					cat, stats.Promoted, total))
		}
	}

	return anomalies
}

// formatDailyDigest renders the markdown daily digest per the design doc format.
func formatDailyDigest(report *compactReport) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("## Wisp Compaction: %s\n\n", report.Date))

	// Summary table
	sb.WriteString("### Summary\n")
	sb.WriteString("| Category | Deleted | Promoted | Active |\n")
	sb.WriteString("|----------|---------|----------|--------|\n")

	for _, cat := range categoryOrder {
		stats := report.Categories[cat]
		// Skip empty categories
		if stats.Deleted == 0 && stats.Promoted == 0 && stats.Active == 0 {
			continue
		}
		sb.WriteString(fmt.Sprintf("| %s | %d | %d | %d |\n",
			cat, stats.Deleted, stats.Promoted, stats.Active))
	}

	// Promotions
	if len(report.Promotions) > 0 {
		sb.WriteString("\n### Promotions\n")
		for _, p := range report.Promotions {
			sb.WriteString(fmt.Sprintf("- %s: %q (reason: %s)\n",
				p.ID, compactTruncate(p.Title, 60), p.Reason))
		}
	}

	// Anomalies
	if len(report.Anomalies) > 0 {
		sb.WriteString("\n### Anomalies\n")
		for _, a := range report.Anomalies {
			sb.WriteString(fmt.Sprintf("- %s\n", a))
		}
	}

	// Errors
	if len(report.Errors) > 0 {
		sb.WriteString("\n### Errors\n")
		for _, e := range report.Errors {
			sb.WriteString(fmt.Sprintf("- %s\n", e))
		}
	}

	return sb.String()
}

// sendCompactDigest sends the daily digest via gt mail send.
func sendCompactDigest(dateStr, body string) error {
	subject := fmt.Sprintf("Wisp Compaction: %s", dateStr)

	// Send to mayor/ only — deacon/ is not a valid mail address (audit bead
	// serves as the deacon-side record).
	mailCmd := exec.Command("gt", "mail", "send", "mayor/",
		"-s", subject,
		"-m", body,
	)
	mailCmd.Stdout = os.Stdout
	mailCmd.Stderr = os.Stderr
	return mailCmd.Run()
}

// createCompactReportBead creates a permanent audit bead for the daily digest.
func createCompactReportBead(report *compactReport, markdown string) (string, error) {
	payloadJSON, err := json.Marshal(report)
	if err != nil {
		return "", fmt.Errorf("marshaling report payload: %w", err)
	}

	title := fmt.Sprintf("Compaction Report %s", report.Date)
	bdArgs := []string{
		"create",
		"--type=event",
		"--title=" + title,
		"--event-category=wisp.compaction",
		"--event-payload=" + string(payloadJSON),
		"--description=" + markdown,
		"--silent",
	}

	bdCmd := exec.Command("bd", bdArgs...)
	output, err := bdCmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("creating report bead: %w\nOutput: %s", err, string(output))
	}

	beadID, err := extractBeadID(string(output))
	if err != nil {
		return "", fmt.Errorf("parsing report bead id: %w", err)
	}

	// Auto-close (audit record, not work). Surface failures: if close fails,
	// the bead stays open and findExistingCompactReport (filter status=closed)
	// will never match, causing the digest to re-fire every patrol cycle.
	closeCmd := exec.Command("bd", "close", beadID, "--reason=daily compaction report")
	if out, err := closeCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("auto-closing report bead %s: %w\nOutput: %s", beadID, err, string(out))
	}

	return beadID, nil
}

// --- Weekly Rollup ---

func runWeeklyRollup() error {
	now := time.Now().UTC()
	weekEnd := now.Format("2006-01-02")
	weekStart := now.AddDate(0, 0, -7).Format("2006-01-02")

	// Idempotency check: see if weekly rollup already exists for this week
	existingID, err := findExistingWeeklyRollup(weekStart, weekEnd)
	if err != nil {
		if compactReportVerbose {
			fmt.Fprintf(os.Stderr, "warning: weekly idempotency check failed: %v\n", err)
		}
	} else if existingID != "" {
		fmt.Printf("%s Weekly rollup already sent for %s to %s (bead: %s)\n",
			style.Dim.Render("○"), weekStart, weekEnd, existingID)
		return nil
	}

	// Query compaction report event beads from the past week
	reports, err := queryCompactionReports(weekStart, weekEnd)
	if err != nil {
		return fmt.Errorf("querying compaction reports: %w", err)
	}

	rollup := &weeklyRollup{
		WeekStart: weekStart,
		WeekEnd:   weekEnd,
		Days:      len(reports),
		Totals:    make(map[string]*categoryStats),
	}

	// Initialize totals
	for _, cat := range categoryOrder {
		rollup.Totals[cat] = &categoryStats{}
	}

	// Aggregate
	for _, report := range reports {
		for cat, stats := range report.Categories {
			if _, ok := rollup.Totals[cat]; !ok {
				rollup.Totals[cat] = &categoryStats{}
			}
			rollup.Totals[cat].Deleted += stats.Deleted
			rollup.Totals[cat].Promoted += stats.Promoted
			rollup.Totals[cat].Active = stats.Active // Use latest active count
		}
		rollup.Promotions += len(report.Promotions)
		rollup.Anomalies = append(rollup.Anomalies, report.Anomalies...)
	}

	if compactReportJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rollup)
	}

	markdown := formatWeeklyRollup(rollup)

	if compactReportDryRun {
		fmt.Printf("%s [DRY RUN] Weekly compaction rollup (%s to %s):\n\n",
			style.Dim.Render("[dry-run]"), weekStart, weekEnd)
		fmt.Println(markdown)
		return nil
	}

	// Create audit event bead for the weekly rollup (for future idempotency checks)
	beadID, beadErr := createWeeklyRollupBead(rollup, markdown)
	if beadErr != nil {
		return fmt.Errorf("recording weekly rollup audit bead: %w", beadErr)
	}

	// Send to mayor/
	subject := fmt.Sprintf("Weekly Wisp Compaction: %s to %s", weekStart, weekEnd)
	mailCmd := exec.Command("gt", "mail", "send", "mayor/",
		"-s", subject,
		"-m", markdown,
	)
	mailCmd.Stdout = os.Stdout
	mailCmd.Stderr = os.Stderr
	if err := mailCmd.Run(); err != nil {
		return fmt.Errorf("sending weekly rollup: %w", err)
	}

	fmt.Printf("%s Weekly compaction rollup sent to mayor/ (%s to %s)\n",
		style.Success.Render("✓"), weekStart, weekEnd)
	if beadID != "" {
		fmt.Printf("  Audit bead: %s\n", beadID)
	}

	return nil
}

// queryCompactionReports queries compaction report event beads in a date range.
func queryCompactionReports(startDate, endDate string) ([]*compactReport, error) {
	listCmd := exec.Command("bd", "list",
		"--type=event",
		"--json",
		"--limit=0",
	)
	listOutput, err := listCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("listing event beads: %w", err)
	}

	var events []struct {
		ID           string `json:"id"`
		Title        string `json:"title"`
		EventPayload string `json:"event_payload"`
	}
	if err := json.Unmarshal(extractJSONArray(listOutput), &events); err != nil {
		return nil, fmt.Errorf("parsing event list: %w", err)
	}

	var reports []*compactReport
	for _, evt := range events {
		if !strings.HasPrefix(evt.Title, "Compaction Report ") {
			continue
		}
		// Extract date from title
		evtDate := strings.TrimPrefix(evt.Title, "Compaction Report ")
		if evtDate < startDate || evtDate > endDate {
			continue
		}

		// Parse the event payload back into a compactReport
		if evt.EventPayload == "" {
			continue
		}
		var report compactReport
		if err := json.Unmarshal([]byte(evt.EventPayload), &report); err != nil {
			continue
		}
		reports = append(reports, &report)
	}

	// Sort by date
	sort.Slice(reports, func(i, j int) bool {
		return reports[i].Date < reports[j].Date
	})

	return reports, nil
}

// formatWeeklyRollup renders the markdown weekly rollup.
func formatWeeklyRollup(rollup *weeklyRollup) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("## Weekly Wisp Compaction: %s to %s\n\n", rollup.WeekStart, rollup.WeekEnd))
	sb.WriteString(fmt.Sprintf("**Days reported:** %d\n\n", rollup.Days))

	// Totals table
	sb.WriteString("### Totals\n")
	sb.WriteString("| Category | Deleted | Promoted | Active (latest) |\n")
	sb.WriteString("|----------|---------|----------|----------------|\n")

	totalDeleted := 0
	totalPromoted := 0

	for _, cat := range categoryOrder {
		stats := rollup.Totals[cat]
		if stats.Deleted == 0 && stats.Promoted == 0 && stats.Active == 0 {
			continue
		}
		sb.WriteString(fmt.Sprintf("| %s | %d | %d | %d |\n",
			cat, stats.Deleted, stats.Promoted, stats.Active))
		totalDeleted += stats.Deleted
		totalPromoted += stats.Promoted
	}

	// Rates
	sb.WriteString(fmt.Sprintf("\n### Rates\n"))
	sb.WriteString(fmt.Sprintf("- **Total deleted:** %d\n", totalDeleted))
	sb.WriteString(fmt.Sprintf("- **Total promoted:** %d\n", totalPromoted))
	if totalDeleted+totalPromoted > 0 {
		rate := float64(totalPromoted) / float64(totalDeleted+totalPromoted) * 100
		sb.WriteString(fmt.Sprintf("- **Promotion rate:** %.1f%%\n", rate))
	}
	if rollup.Days > 0 {
		sb.WriteString(fmt.Sprintf("- **Avg deleted/day:** %d\n", totalDeleted/rollup.Days))
	}

	// Anomalies across the week
	if len(rollup.Anomalies) > 0 {
		sb.WriteString("\n### Anomalies This Week\n")
		// Deduplicate
		seen := make(map[string]bool)
		for _, a := range rollup.Anomalies {
			if !seen[a] {
				sb.WriteString(fmt.Sprintf("- %s\n", a))
				seen[a] = true
			}
		}
	}

	return sb.String()
}

// findExistingCompactReport checks if a compaction digest already exists for the given date.
// Returns the bead ID if found, empty string if not found.
func findExistingCompactReport(dateStr string) (string, error) {
	expectedTitle := fmt.Sprintf("Compaction Report %s", dateStr)

	listCmd := exec.Command("bd", "list",
		"--type=event",
		"--status=closed",
		"--json",
		"--limit=50",
	)
	listOutput, err := listCmd.Output()
	if err != nil {
		return "", err
	}

	var events []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(extractJSONArray(listOutput), &events); err != nil {
		return "", err
	}

	for _, evt := range events {
		if evt.Title == expectedTitle {
			return evt.ID, nil
		}
	}
	return "", nil
}

// findExistingWeeklyRollup checks if a weekly rollup already exists for the given week.
// Returns the bead ID if found, empty string if not found.
func findExistingWeeklyRollup(weekStart, weekEnd string) (string, error) {
	expectedTitle := fmt.Sprintf("Weekly Compaction Rollup %s to %s", weekStart, weekEnd)

	listCmd := exec.Command("bd", "list",
		"--type=event",
		"--json",
		"--limit=20",
	)
	listOutput, err := listCmd.Output()
	if err != nil {
		return "", err
	}

	var events []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(extractJSONArray(listOutput), &events); err != nil {
		return "", err
	}

	for _, evt := range events {
		if evt.Title == expectedTitle {
			return evt.ID, nil
		}
	}
	return "", nil
}

// extractJSONObject finds the first '{' byte in data and returns from that
// point onward. Strips non-JSON prefix from subprocess output.
func extractJSONObject(data []byte) []byte {
	idx := bytes.IndexByte(data, '{')
	if idx < 0 {
		return data
	}
	return data[idx:]
}

// createWeeklyRollupBead creates a permanent audit bead for the weekly rollup.
func createWeeklyRollupBead(rollup *weeklyRollup, markdown string) (string, error) {
	payloadJSON, err := json.Marshal(rollup)
	if err != nil {
		return "", fmt.Errorf("marshaling rollup payload: %w", err)
	}

	title := fmt.Sprintf("Weekly Compaction Rollup %s to %s", rollup.WeekStart, rollup.WeekEnd)
	bdArgs := []string{
		"create",
		"--type=event",
		"--title=" + title,
		"--event-category=wisp.compaction.weekly",
		"--event-payload=" + string(payloadJSON),
		"--description=" + markdown,
		"--silent",
	}

	bdCmd := exec.Command("bd", bdArgs...)
	output, err := bdCmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("creating weekly rollup bead: %w\nOutput: %s", err, string(output))
	}

	beadID, err := extractBeadID(string(output))
	if err != nil {
		return "", fmt.Errorf("parsing weekly rollup bead id: %w", err)
	}

	// Auto-close (audit record, not work). Surface failures so mail is not sent
	// without a matching audit record for future idempotency checks.
	closeCmd := exec.Command("bd", "close", beadID, "--reason=weekly compaction rollup")
	if out, err := closeCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("auto-closing rollup bead %s: %w\nOutput: %s", beadID, err, string(out))
	}

	return beadID, nil
}
