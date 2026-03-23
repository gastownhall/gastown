// Package doltserver - bead_labels_migrate.go adds gt:* labels to existing
// Gas Town beads based on their issue_type field.
//
// Gas Town bead types were extracted from beads core in v0.46.0 and now use
// labels (e.g., gt:agent) instead of issue_type for identification. This
// migration backfills the labels on all existing beads that have the old
// issue_type values but are missing the corresponding gt:* labels.
//
// The migration uses INSERT IGNORE so it is idempotent — safe to run multiple
// times. It covers both the issues table and the wisps table (dolt_ignored
// ephemeral beads).
package doltserver

import (
	"fmt"
)

// MigrateBeadLabelsResult holds statistics for a single database migration.
type MigrateBeadLabelsResult struct {
	LabelsAdded     int // Labels inserted into the issues labels table
	WispLabelsAdded int // Labels inserted into the wisp_labels table
}

// typeToLabelMappings enumerates the Gas Town issue_type values that should
// receive a corresponding gt:* label. This mirrors the BeadsCustomTypes
// constant in internal/constants/constants.go.
var typeToLabelMappings = []struct {
	issueType string
	label     string
}{
	{"agent", "gt:agent"},
	{"role", "gt:role"},
	{"rig", "gt:rig"},
	{"convoy", "gt:convoy"},
	{"slot", "gt:slot"},
	{"queue", "gt:queue"},
	{"event", "gt:event"},
	{"message", "gt:message"},
	{"molecule", "gt:molecule"},
	{"gate", "gt:gate"},
	{"merge-request", "gt:merge-request"},
}

// MigrateBeadLabels adds gt:* labels to beads in a single database that have
// the matching issue_type but are missing the label. Idempotent.
//
// workDir should be the rig directory (e.g. townRoot/gastown) so that bd can
// locate the correct .beads/ configuration.
func MigrateBeadLabels(workDir string, dryRun bool) (*MigrateBeadLabelsResult, error) {
	result := &MigrateBeadLabelsResult{}

	hasWisps := bdTableExists(workDir, "wisps")

	for _, m := range typeToLabelMappings {
		// Count issues rows that are missing the label.
		issuesCnt, err := bdSQLCount(workDir, fmt.Sprintf(
			"SELECT COUNT(*) as cnt FROM issues i"+
				" WHERE i.issue_type = '%s'"+
				" AND NOT EXISTS (SELECT 1 FROM labels l WHERE l.issue_id = i.id AND l.label = '%s')",
			m.issueType, m.label,
		))
		if err != nil {
			// Non-fatal: table may not exist yet in older schemas.
			continue
		}

		// Count wisps rows that are missing the label (if wisps table exists).
		var wispsCnt int
		if hasWisps {
			wispsCnt, _ = bdSQLCount(workDir, fmt.Sprintf(
				"SELECT COUNT(*) as cnt FROM wisps w"+
					" WHERE w.issue_type = '%s'"+
					" AND NOT EXISTS (SELECT 1 FROM wisp_labels wl WHERE wl.issue_id = w.id AND wl.label = '%s')",
				m.issueType, m.label,
			))
		}

		if issuesCnt == 0 && wispsCnt == 0 {
			continue
		}

		if dryRun {
			result.LabelsAdded += issuesCnt
			result.WispLabelsAdded += wispsCnt
			continue
		}

		// Back-fill labels table.
		if issuesCnt > 0 {
			if err := bdSQL(workDir, fmt.Sprintf(
				"INSERT IGNORE INTO labels (issue_id, label)"+
					" SELECT id, '%s' FROM issues WHERE issue_type = '%s'",
				m.label, m.issueType,
			)); err != nil {
				return result, fmt.Errorf("adding label %s to issues: %w", m.label, err)
			}
			result.LabelsAdded += issuesCnt
		}

		// Back-fill wisp_labels table.
		if hasWisps && wispsCnt > 0 {
			if err := bdSQL(workDir, fmt.Sprintf(
				"INSERT IGNORE INTO wisp_labels (issue_id, label)"+
					" SELECT id, '%s' FROM wisps WHERE issue_type = '%s'",
				m.label, m.issueType,
			)); err != nil {
				return result, fmt.Errorf("adding label %s to wisp_labels: %w", m.label, err)
			}
			result.WispLabelsAdded += wispsCnt
		}
	}

	return result, nil
}
