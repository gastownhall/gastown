package doctor

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/doltserver"
)

// CheckJSONLBloat detects when issues.jsonl is massively stale compared to the
// live Dolt database — typically caused by ephemeral wisp data accumulating in
// the git-tracked JSONL export. This is warn-only (bd controls the export).
type CheckJSONLBloat struct {
	BaseCheck
}

// NewCheckJSONLBloat creates a new JSONL bloat detection check.
func NewCheckJSONLBloat() *CheckJSONLBloat {
	return &CheckJSONLBloat{
		BaseCheck: BaseCheck{
			CheckName:        "jsonl-bloat",
			CheckDescription: "Detect stale/bloated issues.jsonl vs live database",
			CheckCategory:    CategoryCleanup,
		},
	}
}

// Run compares issues.jsonl entry counts with live DB record counts across rigs.
func (c *CheckJSONLBloat) Run(ctx *CheckContext) *CheckResult {
	databases, err := doltserver.ListDatabases(ctx.TownRoot)
	if err != nil || len(databases) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No rig databases found (skipping JSONL bloat check)",
		}
	}

	var details []string
	bloated := false

	for _, db := range databases {
		rigDir := filepath.Join(ctx.TownRoot, db)
		jsonlCount, ephemeralCount, err := countJSONLEntries(rigDir)
		if err != nil {
			continue // No JSONL file for this rig
		}
		if jsonlCount == 0 {
			continue
		}

		liveCount, err := queryLiveIssueCount(rigDir)
		if err != nil {
			continue // DB not reachable for this rig
		}

		// Check bloat: JSONL has >10x the live count.
		if liveCount > 0 && jsonlCount > liveCount*10 {
			details = append(details, fmt.Sprintf(
				"%s: issues.jsonl has %d entries vs %d live DB records (%.0fx bloat)",
				db, jsonlCount, liveCount, float64(jsonlCount)/float64(liveCount)))
			bloated = true
		}

		// Check ephemeral ratio: >50% of JSONL entries are ephemeral.
		if jsonlCount > 0 && ephemeralCount*100/jsonlCount > 50 {
			details = append(details, fmt.Sprintf(
				"%s: %d/%d JSONL entries (%d%%) are ephemeral — wisp data polluting git-tracked export",
				db, ephemeralCount, jsonlCount, ephemeralCount*100/jsonlCount))
			bloated = true
		}
	}

	if bloated {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "issues.jsonl contains stale/ephemeral data bloating the git-tracked export",
			Details: details,
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: "issues.jsonl not bloated",
	}
}

// countJSONLEntries counts total and ephemeral entries in issues.jsonl.
func countJSONLEntries(rigDir string) (total, ephemeral int, err error) {
	beadsDir := beads.ResolveBeadsDir(rigDir)
	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	file, err := os.Open(issuesPath)
	if err != nil {
		return 0, 0, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		total++

		var issue struct {
			Ephemeral bool `json:"ephemeral"`
		}
		if err := json.Unmarshal([]byte(line), &issue); err == nil && issue.Ephemeral {
			ephemeral++
		}
	}

	return total, ephemeral, nil
}

// queryLiveIssueCount returns the total count of issues in the live DB.
// Counts all records (including closed) to match countJSONLEntries which also counts all.
func queryLiveIssueCount(rigDir string) (int, error) {
	cmd := beads.Spawn("sql", "--csv", "SELECT COUNT(*) as cnt FROM issues") //nolint:gosec // G204: query is a constant
	cmd.Dir = rigDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("bd sql: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) < 2 {
		return 0, nil
	}
	cnt := 0
	fmt.Sscanf(strings.TrimSpace(lines[1]), "%d", &cnt)
	return cnt, nil
}
