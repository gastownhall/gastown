package doctor

import (
	"encoding/csv"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/doltserver"
)

// CheckStrandedDurableWisps detects durable records sitting in the wisps table.
//
// This is the mirror image of CheckMisclassifiedWisps: that check finds
// ephemeral beads misplaced in the versioned issues table, this one finds
// audit-bearing beads stranded in the unversioned wisps table.
//
// The wisps table and everything matching wisp_% are in dolt_ignore (bd schema
// migration 0019, applied at init on every database), so wisp rows carry no
// Dolt history at all: no commit on write, no diff row on delete, no record of
// who reaped what. That is correct for genuine ephemera — heartbeats, patrol
// steps, notifications — but wrong for records the town treats as durable.
//
// Two record classes qualify (hq-81i):
//
//   - Rig identity beads (gt:rig). These are configuration, not ephemera, and
//     `rig` is deliberately excluded from constants.BeadsInfraTypes for exactly
//     that reason. Rows created before that exclusion landed are still stranded.
//   - Escalations (gt:escalation). An escalation carries a severity decision,
//     an ack, and a resolution, and its ID is embedded as a live handle in the
//     permanent escalation mail bead ("gt escalate ack <id>"). Escalations are
//     now created persistent; rows created before that change are still stranded
//     and can be reaped out from under the versioned mail bead pointing at them.
//
// Detection reads the live Dolt DB. There is no JSONL fallback — wisps are, by
// construction, absent from JSONL exports.
type CheckStrandedDurableWisps struct {
	FixableCheck
	stranded []strandedWisp
}

type strandedWisp struct {
	rigName string
	workDir string
	id      string
	title   string
	kind    string // "rig" or "escalation"
}

// strandedDurableLabels maps the label that marks a durable record to the
// human-readable kind reported to the user.
var strandedDurableLabels = map[string]string{
	"gt:rig":        "rig",
	"gt:escalation": "escalation",
}

// NewCheckStrandedDurableWisps creates a new stranded durable wisp check.
func NewCheckStrandedDurableWisps() *CheckStrandedDurableWisps {
	return &CheckStrandedDurableWisps{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "stranded-durable-wisps",
				CheckDescription: "Detect durable beads stranded in the unversioned wisps table",
				CheckCategory:    CategoryCleanup,
			},
		},
	}
}

// Run checks for durable records in the wisps table across all databases.
// Only labels in strandedDurableLabels are flagged — never a guess based on
// titles or ID patterns.
func (c *CheckStrandedDurableWisps) Run(ctx *CheckContext) *CheckResult {
	c.stranded = nil

	databases, dbErr := doltserver.ListDatabases(ctx.TownRoot)
	if dbErr != nil || len(databases) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "Dolt unavailable — skipping stranded durable wisp check",
		}
	}

	var probeErrors int
	byRig := make(map[string]int)

	for _, db := range databases {
		rigDir := resolveStrandedWispWorkDir(ctx.TownRoot, db)
		found, probeErr := c.findStrandedDolt(rigDir, db)
		probeErrors += probeErr
		if len(found) > 0 {
			c.stranded = append(c.stranded, found...)
			byRig[db] = len(found)
		}
	}

	var details []string
	rigNames := make([]string, 0, len(byRig))
	for rig := range byRig {
		rigNames = append(rigNames, rig)
	}
	sort.Strings(rigNames)
	for _, rig := range rigNames {
		details = append(details, fmt.Sprintf("%s: %d durable bead(s) in wisps table", rig, byRig[rig]))
	}
	if probeErrors > 0 {
		details = append(details, fmt.Sprintf("%d DB probe(s) failed — some databases were skipped", probeErrors))
	}

	if len(c.stranded) > 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("%d durable bead(s) stranded in the unversioned wisps table", len(c.stranded)),
			Details: details,
			FixHint: "Run 'gt doctor --fix' to promote them to the versioned issues table",
		}
	}

	if probeErrors > 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "No stranded durable beads found (some DB probes failed)",
			Details: details,
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: "No durable beads stranded in the wisps table",
	}
}

// findStrandedDolt queries the live Dolt DB for wisps carrying a durable label.
// Returns the matches and 1 if the database could not be probed.
func (c *CheckStrandedDurableWisps) findStrandedDolt(rigDir, rigName string) ([]strandedWisp, int) {
	labels := make([]string, 0, len(strandedDurableLabels))
	for label := range strandedDurableLabels {
		labels = append(labels, "'"+label+"'")
	}
	sort.Strings(labels)

	query := fmt.Sprintf(
		"SELECT w.id, l.label, w.title FROM wisps w JOIN wisp_labels l ON l.issue_id = w.id WHERE l.label IN (%s)",
		strings.Join(labels, ", "))

	cmd := exec.Command("bd", "sql", "--csv", query) //nolint:gosec // G204: query is built from a fixed label set
	cmd.Dir = rigDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		// A database with no wisps table has nothing to strand — that is a
		// clean skip, not a failed probe. Anything else means bd could not
		// reach the database and the result is unknown.
		if !bdTableExistsDoctor(rigDir, "wisps") {
			return nil, 0
		}
		return nil, 1
	}

	records, err := csv.NewReader(strings.NewReader(string(output))).ReadAll()
	if err != nil || len(records) < 2 {
		return nil, 0
	}

	var found []strandedWisp
	for _, rec := range records[1:] {
		if len(rec) < 3 {
			continue
		}
		kind, ok := strandedDurableLabels[strings.TrimSpace(rec[1])]
		if !ok {
			continue
		}
		found = append(found, strandedWisp{
			rigName: rigName,
			workDir: rigDir,
			id:      strings.TrimSpace(rec[0]),
			title:   strings.TrimSpace(rec[2]),
			kind:    kind,
		})
	}

	return found, 0
}

// Fix promotes each stranded bead into the versioned issues table.
//
// `bd update <id> --persistent` is the same primitive `gt compact` uses to
// promote a wisp, and it preserves the original ID — so handles already
// embedded in versioned rows ("gt escalate ack hq-wisp-7lt") keep resolving
// after the move.
func (c *CheckStrandedDurableWisps) Fix(ctx *CheckContext) error {
	if len(c.stranded) == 0 {
		return nil
	}

	var errs []string
	for _, w := range c.stranded {
		workDir := w.workDir
		if workDir == "" {
			workDir = resolveStrandedWispWorkDir(ctx.TownRoot, w.rigName)
		}

		cmd := exec.Command("bd", "update", w.id, "--persistent") //nolint:gosec // G204: id comes from the local DB
		cmd.Dir = workDir
		if output, err := cmd.CombinedOutput(); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %s: %v", w.id, strings.TrimSpace(string(output)), err))
			continue
		}

		reason := fmt.Sprintf("Promoted to versioned issues table: %s records need an audit trail (hq-81i)", w.kind)
		commentCmd := exec.Command("bd", "comments", "add", w.id, reason) //nolint:gosec // G204: id comes from the local DB
		commentCmd.Dir = workDir
		_ = commentCmd.Run()
	}

	if len(errs) > 0 {
		return fmt.Errorf("partial fix: %s", strings.Join(errs, "; "))
	}
	return nil
}

// resolveStrandedWispWorkDir maps a database name to the directory bd should
// run in. Mirrors resolveMisclassifiedWispWorkDir.
func resolveStrandedWispWorkDir(townRoot, rigName string) string {
	if rigName == "town" || rigName == "hq" {
		return townRoot
	}
	if rigDir := beads.GetRigPathForPrefix(townRoot, rigName+"-"); rigDir != "" {
		return rigDir
	}
	return filepath.Join(townRoot, rigName)
}
