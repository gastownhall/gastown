package doctor

import (
	"encoding/csv"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jonbaldie/gastown/internal/doltserver"
)

// NullAssigneeCheck detects in_progress beads with a NULL or empty assignee.
//
// These records arise from partial writes (crash mid-transaction or race
// condition during bd update). They are invisible to bd show / bd list because
// bd's deserialization fails silently on a NULL assignee, so the steps remain
// stuck in_progress indefinitely and block molecule progress.
//
// Detection uses bd sql --csv (raw SQL passthrough, not affected by bd's ORM).
// Fix resets status to open + clears assignee so the step can be re-dispatched,
// then issues a Dolt commit to record the repair in history.
type NullAssigneeCheck struct {
	FixableCheck
	affected []nullAssigneeRow
}

type nullAssigneeRow struct {
	ID        string
	Title     string
	UpdatedAt string
	RigDB     string // Dolt database name (= rig name)
}

// NewNullAssigneeCheck creates a new null-assignee steps check.
func NewNullAssigneeCheck() *NullAssigneeCheck {
	return &NullAssigneeCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "null-assignee-steps",
				CheckDescription: "Check for in_progress beads with NULL assignee (invisible to bd, blocking indefinitely)",
				CheckCategory:    CategoryCleanup,
			},
		},
	}
}

const nullAssigneeSelectQuery = `SELECT id, title, updated_at FROM issues WHERE status = 'in_progress' AND (assignee IS NULL OR assignee = '') ORDER BY updated_at ASC`

const nullAssigneeFixQuery = `UPDATE issues SET status = 'open', assignee = '' WHERE status = 'in_progress' AND (assignee IS NULL OR assignee = '')`

// Run queries each rig database for in_progress beads with NULL/empty assignee.
func (c *NullAssigneeCheck) Run(ctx *CheckContext) *CheckResult {
	c.affected = nil

	databases, err := doltserver.ListDatabases(ctx.TownRoot)
	if err != nil || len(databases) == 0 {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusOK,
			Message:  "No rig databases found (skipping)",
			Category: c.Category(),
		}
	}

	for _, db := range databases {
		rigDir := filepath.Join(ctx.TownRoot, db)
		rows, err := queryNullAssigneeBeads(rigDir)
		if err != nil {
			// Non-fatal: Dolt might not be running or rig may not be bd-managed.
			continue
		}
		for _, row := range rows {
			row.RigDB = db
			c.affected = append(c.affected, row)
		}
	}

	if len(c.affected) == 0 {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusOK,
			Message:  "No in_progress beads with NULL assignee found",
			Category: c.Category(),
		}
	}

	details := make([]string, 0, len(c.affected))
	for _, row := range c.affected {
		details = append(details, fmt.Sprintf("[%s] %s — %s (updated: %s)",
			row.RigDB, row.ID, shortenTitle(row.Title, 60), row.UpdatedAt))
	}

	return &CheckResult{
		Name:   c.Name(),
		Status: StatusWarning,
		Message: fmt.Sprintf(
			"%d in_progress bead(s) with NULL assignee — invisible to bd, blocking molecule progress",
			len(c.affected),
		),
		Details:  details,
		FixHint:  "Run 'gt doctor --fix' to reset to open for re-dispatch",
		Category: c.Category(),
	}
}

// Fix resets all affected beads to open (clears assignee) via direct SQL,
// then commits the repair to Dolt history.
func (c *NullAssigneeCheck) Fix(ctx *CheckContext) error {
	if len(c.affected) == 0 {
		return nil
	}

	// Collect unique databases that have affected beads.
	affected := make(map[string]bool)
	for _, row := range c.affected {
		affected[row.RigDB] = true
	}

	var errs []string
	for db := range affected {
		rigDir := filepath.Join(ctx.TownRoot, db)

		// Reset beads via direct SQL (bypasses bd ORM which fails on NULL assignee).
		if err := execBdSQLWrite(rigDir, nullAssigneeFixQuery); err != nil {
			errs = append(errs, fmt.Sprintf("%s: update failed: %v", db, err))
			continue
		}

		// Commit the repair to Dolt history (non-fatal: repair is effective even
		// without a version commit, but the commit gives audit visibility).
		commitMsg := "fix: reset in_progress beads with null assignee (gt doctor)"
		if err := doltserver.CommitServerWorkingSet(ctx.TownRoot, db, commitMsg); err != nil {
			// Non-fatal: data is already fixed, commit is best-effort.
			_ = err
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("partial fix: %s", strings.Join(errs, "; "))
	}
	return nil
}

// queryNullAssigneeBeads returns in_progress beads with NULL/empty assignee for a rig.
// Uses bd sql --csv (raw SQL passthrough, not affected by bd ORM deserialization).
func queryNullAssigneeBeads(rigDir string) ([]nullAssigneeRow, error) {
	cmd := exec.Command("bd", "sql", "--csv", nullAssigneeSelectQuery) //nolint:gosec // G204: args are constants
	cmd.Dir = rigDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("bd sql: %w", err)
	}

	r := csv.NewReader(strings.NewReader(string(output)))
	records, err := r.ReadAll()
	if err != nil || len(records) < 2 {
		return nil, nil // No results or empty table
	}

	rows := make([]nullAssigneeRow, 0, len(records)-1)
	for _, rec := range records[1:] { // Skip CSV header
		if len(rec) < 3 {
			continue
		}
		rows = append(rows, nullAssigneeRow{
			ID:        strings.TrimSpace(rec[0]),
			Title:     strings.TrimSpace(rec[1]),
			UpdatedAt: strings.TrimSpace(rec[2]),
		})
	}
	return rows, nil
}

// execBdSQLWrite executes a SQL write statement via bd sql.
func execBdSQLWrite(rigDir, query string) error {
	cmd := exec.Command("bd", "sql", query) //nolint:gosec // G204: query is a constant
	cmd.Dir = rigDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

// shortenTitle truncates a title with ellipsis if it exceeds n runes.
func shortenTitle(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-3]) + "..."
}
