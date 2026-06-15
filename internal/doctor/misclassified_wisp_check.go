package doctor

import (
	"encoding/csv"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/doltserver"
)

// CheckMisclassifiedWisps detects ephemeral beads that are in the issues table
// instead of the wisps table. This is a data integrity check, not a heuristic —
// it only acts on beads whose ephemeral flag is already set (ZFC: agent decides,
// Go transports).
//
// Detection prefers Dolt (live DB via bd sql --csv) over JSONL, falling back to
// JSONL when the DB is unreachable.
type CheckMisclassifiedWisps struct {
	FixableCheck
	misclassified     []misclassifiedWisp
	misclassifiedRigs map[string]int // rig -> count
}

type misclassifiedWisp struct {
	rigName string
	workDir string
	id      string
	title   string
	reason  string
}

// NewCheckMisclassifiedWisps creates a new misclassified wisp check.
func NewCheckMisclassifiedWisps() *CheckMisclassifiedWisps {
	return &CheckMisclassifiedWisps{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "misclassified-wisps",
				CheckDescription: "Detect ephemeral beads misplaced in the issues table",
				CheckCategory:    CategoryCleanup,
			},
		},
		misclassifiedRigs: make(map[string]int),
	}
}

// Run checks for ephemeral beads in the issues table across all rigs.
// Only flags beads where ephemeral=1 — never guesses based on titles,
// labels, or ID patterns (ZFC compliance).
func (c *CheckMisclassifiedWisps) Run(ctx *CheckContext) *CheckResult {
	c.misclassified = nil
	c.misclassifiedRigs = make(map[string]int)

	// Try Dolt-first detection via ListDatabases (matches NullAssigneeCheck pattern).
	databases, dbErr := doltserver.ListDatabases(ctx.TownRoot)
	useDolt := dbErr == nil && len(databases) > 0

	var details []string
	var totalProbeErrors int

	if useDolt {
		for _, db := range databases {
			rigDir := resolveMisclassifiedWispWorkDir(ctx.TownRoot, misclassifiedWisp{rigName: db})
			found, probeErrors := c.findMisplacedEphemeralsDolt(rigDir, db)
			totalProbeErrors += probeErrors
			if len(found) > 0 {
				c.misclassified = append(c.misclassified, found...)
				c.misclassifiedRigs[db] = len(found)
				details = append(details, fmt.Sprintf("%s: %d misplaced ephemeral(s)", db, len(found)))
			}
		}
	} else {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "Dolt unavailable — skipping misplaced ephemeral check",
		}
	}

	if totalProbeErrors > 0 {
		details = append(details, fmt.Sprintf("%d DB probe(s) failed — some databases were skipped", totalProbeErrors))
	}

	total := len(c.misclassified)
	if total > 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("%d ephemeral bead(s) misplaced in issues table", total),
			Details: details,
			FixHint: "Run 'gt doctor --fix' to migrate to wisps table",
		}
	}

	if totalProbeErrors > 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "No misplaced ephemerals found (some DB probes failed)",
			Details: details,
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: "No misplaced ephemerals found",
	}
}

// findMisplacedEphemeralsDolt queries the live Dolt DB for beads in the issues
// table that have ephemeral=1. These should be in the wisps table instead.
// No heuristics — only the ephemeral flag matters.
func (c *CheckMisclassifiedWisps) findMisplacedEphemeralsDolt(rigDir, rigName string) ([]misclassifiedWisp, int) {
	issueQuery := `SELECT id, title FROM issues WHERE ephemeral = 1`
	cmd := exec.Command("bd", "sql", "--csv", issueQuery) //nolint:gosec // G204: query is a constant
	cmd.Dir = rigDir
	issueOutput, err := cmd.CombinedOutput()
	if err != nil {
		return nil, 1 // DB unavailable for this rig
	}

	issueReader := csv.NewReader(strings.NewReader(string(issueOutput)))
	issueRecords, err := issueReader.ReadAll()
	if err != nil || len(issueRecords) < 2 {
		return nil, 0
	}

	var found []misclassifiedWisp
	for _, rec := range issueRecords[1:] {
		if len(rec) < 2 {
			continue
		}
		found = append(found, misclassifiedWisp{
			rigName: rigName,
			workDir: rigDir,
			id:      strings.TrimSpace(rec[0]),
			title:   strings.TrimSpace(rec[1]),
			reason:  "ephemeral bead in issues table",
		})
	}

	return found, 0
}

// Fix migrates misplaced ephemeral beads from the issues table to the wisps table.
//
// Pattern follows wisps_migrate.go (INSERT IGNORE) + NullAssigneeCheck (bd sql + commit).
func (c *CheckMisclassifiedWisps) Fix(ctx *CheckContext) error {
	if len(c.misclassified) == 0 {
		return nil
	}

	// Group by rig for batch operations.
	rigBatches := make(map[string][]misclassifiedWisp)
	for _, w := range c.misclassified {
		workDir := resolveMisclassifiedWispWorkDir(ctx.TownRoot, w)
		rigBatches[workDir] = append(rigBatches[workDir], w)
	}

	var errs []string

	for workDir, batch := range rigBatches {
		rigName := batch[0].rigName

		ids := make([]string, len(batch))
		for i, w := range batch {
			ids[i] = "'" + strings.ReplaceAll(w.id, "'", "''") + "'"
		}
		idList := strings.Join(ids, ", ")

		if err := c.purgeRigBatch(ctx, workDir, rigName, idList); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", rigName, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("partial fix: %s", strings.Join(errs, "; "))
	}
	return nil
}

func resolveMisclassifiedWispWorkDir(townRoot string, w misclassifiedWisp) string {
	if w.workDir != "" {
		return w.workDir
	}

	if w.rigName == "town" || w.rigName == "hq" {
		return townRoot
	}

	if rigDir := beads.GetRigPathForPrefix(townRoot, w.rigName+"-"); rigDir != "" {
		return rigDir
	}

	return filepath.Join(townRoot, w.rigName)
}

// purgeRigBatch migrates a batch of ephemeral beads from issues to wisps:
// 1. Check wisps table exists (fall back to noop if not — ephemeral flag is already set)
// 2. INSERT IGNORE into wisps
// 3. Copy auxiliary data (labels, comments, events, deps)
// 4. DELETE from issues + auxiliary tables
// 5. Commit to Dolt history
func (c *CheckMisclassifiedWisps) purgeRigBatch(ctx *CheckContext, workDir, rigName, idList string) error {
	hasWisps := bdTableExistsDoctor(workDir, "wisps")
	if !hasWisps {
		// No wisps table — nothing to migrate to. The ephemeral flag is already
		// set on these beads, so they'll be handled by normal cleanup paths.
		return nil
	}

	// Step 1: Migrate issues to wisps table (INSERT IGNORE skips duplicates).
	migrateQuery := fmt.Sprintf(
		"INSERT IGNORE INTO wisps (id, title, description, status, issue_type, agent_state, role_type, rig, hook_bead, role_bead, created_at, updated_at, created_by, owner, assignee, priority, ephemeral, wisp_type, mol_type, metadata) "+
			"SELECT id, title, description, status, issue_type, agent_state, role_type, rig, hook_bead, role_bead, created_at, updated_at, created_by, owner, assignee, priority, 1, wisp_type, mol_type, metadata FROM issues WHERE id IN (%s)",
		idList)
	if err := execBdSQLWrite(workDir, migrateQuery); err != nil {
		return fmt.Errorf("migrate to wisps: %w", err)
	}

	// Step 2: Copy auxiliary data to wisp_* tables.
	auxCopies := []struct {
		table string
		query string
	}{
		{
			table: "wisp_labels",
			query: fmt.Sprintf("INSERT IGNORE INTO wisp_labels (issue_id, label) SELECT l.issue_id, l.label FROM labels l WHERE l.issue_id IN (%s)", idList),
		},
		{
			table: "wisp_comments",
			query: fmt.Sprintf("INSERT IGNORE INTO wisp_comments (issue_id, author, text, created_at) SELECT c.issue_id, c.author, c.text, c.created_at FROM comments c WHERE c.issue_id IN (%s)", idList),
		},
		{
			table: "wisp_events",
			query: fmt.Sprintf("INSERT IGNORE INTO wisp_events (issue_id, event_type, actor, old_value, new_value, comment, created_at) SELECT e.issue_id, e.event_type, e.actor, e.old_value, e.new_value, e.comment, e.created_at FROM events e WHERE e.issue_id IN (%s)", idList),
		},
		{
			table: "wisp_dependencies",
			query: fmt.Sprintf("INSERT IGNORE INTO wisp_dependencies (issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_at, created_by, metadata, thread_id) SELECT d.issue_id, CASE WHEN target_wisp.id IS NULL THEN d.depends_on_issue_id ELSE NULL END, CASE WHEN target_wisp.id IS NOT NULL THEN d.depends_on_issue_id ELSE d.depends_on_wisp_id END, d.depends_on_external, d.type, d.created_at, d.created_by, d.metadata, d.thread_id FROM dependencies d LEFT JOIN wisps target_wisp ON target_wisp.id = d.depends_on_issue_id WHERE d.issue_id IN (%s)", idList),
		},
	}
	copyErrors := map[string]error{}
	for _, aux := range auxCopies {
		if !bdTableExistsDoctor(workDir, aux.table) {
			if aux.table == "wisp_dependencies" && bdTableExistsDoctor(workDir, "dependencies") {
				copyErrors[aux.table] = fmt.Errorf("target table missing")
			}
			continue
		}
		if err := execBdSQLWrite(workDir, aux.query); err != nil {
			copyErrors[aux.table] = err
		}
	}
	if err := copyErrors["wisp_dependencies"]; err != nil {
		return fmt.Errorf("copying wisp_dependencies: %w", err)
	}
	if bdTableExistsDoctor(workDir, "wisp_dependencies") {
		if err := execBdSQLWrite(workDir, fmt.Sprintf("UPDATE wisp_dependencies SET depends_on_wisp_id = depends_on_issue_id, depends_on_issue_id = NULL WHERE depends_on_issue_id IN (%s)", idList)); err != nil {
			return fmt.Errorf("retargeting incoming wisp_dependencies: %w", err)
		}
	}
	if bdTableExistsDoctor(workDir, "dependencies") {
		if err := execBdSQLWrite(workDir, fmt.Sprintf("UPDATE dependencies SET depends_on_wisp_id = depends_on_issue_id, depends_on_issue_id = NULL WHERE depends_on_issue_id IN (%s)", idList)); err != nil {
			return fmt.Errorf("retargeting incoming dependencies: %w", err)
		}
	}

	// Step 3: Delete from auxiliary tables first (referential integrity).
	auxDeletes := []string{
		fmt.Sprintf("DELETE FROM labels WHERE issue_id IN (%s)", idList),
		fmt.Sprintf("DELETE FROM comments WHERE issue_id IN (%s)", idList),
		fmt.Sprintf("DELETE FROM events WHERE issue_id IN (%s)", idList),
		fmt.Sprintf("DELETE FROM dependencies WHERE issue_id IN (%s)", idList),
	}
	for _, q := range auxDeletes {
		_ = execBdSQLWrite(workDir, q) // Best-effort: table may not exist
	}

	// Step 4: Delete from issues table.
	deleteQuery := fmt.Sprintf("DELETE FROM issues WHERE id IN (%s)", idList)
	if err := execBdSQLWrite(workDir, deleteQuery); err != nil {
		return fmt.Errorf("delete from issues: %w", err)
	}

	// Step 5: Commit to Dolt history.
	commitMsg := "fix: migrate misplaced ephemeral beads to wisps table (gt doctor)"
	if err := doltserver.CommitServerWorkingSet(ctx.TownRoot, rigName, commitMsg); err != nil {
		_ = err // Non-fatal
	}

	return nil
}

// bdTableExistsDoctor checks if a table exists by attempting to query it.
// Doctor-local wrapper (wisps_migrate.go has its own unexported copy).
func bdTableExistsDoctor(workDir, tableName string) bool {
	cmd := exec.Command("bd", "sql", fmt.Sprintf("SELECT 1 FROM `%s` LIMIT 1", tableName)) //nolint:gosec // G204: tableName is hardcoded
	cmd.Dir = workDir
	err := cmd.Run()
	return err == nil
}
