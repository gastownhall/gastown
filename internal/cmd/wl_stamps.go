package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jonbaldie/gastown/internal/doltserver"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/wasteland"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var (
	wlStampsRig         string
	wlStampsAuthor      string
	wlStampsSkill       string
	wlStampsContextType string
	wlStampsStampType   string
	wlStampsCohort      string
	wlStampsSeverity    string
	wlStampsLimit       int
	wlStampsJSON        bool
)

var wlStampsCmd = &cobra.Command{
	Use:   "stamps <rig-handle>",
	Short: "Query stamps for a rig",
	Args:  cobra.ExactArgs(1),
	RunE:  runWLStamps,
	Long: `Query the stamps table for a given rig handle.

Shows stamps where the rig is the subject (worker being stamped).
Use --author to filter by who issued the stamp. Use --skill, --type,
and --severity to narrow results.

EXAMPLES:
  gt wl stamps gastown                         # All stamps for gastown
  gt wl stamps gastown --author hop-mayor      # Stamps from a specific validator
  gt wl stamps gastown --skill go              # Filter by skill tag
  gt wl stamps gastown --type boot_block       # Boot block stamps only
  gt wl stamps gastown --severity branch       # Branch-level stamps
  gt wl stamps gastown --limit 10              # Show 10 stamps
  gt wl stamps gastown --json                  # JSON output`,
}

func init() {
	wlStampsCmd.Flags().StringVar(&wlStampsAuthor, "author", "", "Filter by stamper rig handle")
	wlStampsCmd.Flags().StringVar(&wlStampsSkill, "skill", "", "Filter by skill tag (searches JSON array)")
	wlStampsCmd.Flags().StringVar(&wlStampsContextType, "type", "", "Filter by context_type (completion, endorsement, boot_block, validation_received, sandboxed_completion)")
	wlStampsCmd.Flags().StringVar(&wlStampsStampType, "stamp-type", "", "Filter by stamp_type (work, mentoring, peer_review, endorsement, boot_block)")
	wlStampsCmd.Flags().StringVar(&wlStampsCohort, "cohort", "", "Filter by pilot_cohort (andela-pilot, commbank-pilot, indie)")
	wlStampsCmd.Flags().StringVar(&wlStampsSeverity, "severity", "", "Filter by severity (leaf, branch, root)")
	wlStampsCmd.Flags().IntVar(&wlStampsLimit, "limit", 50, "Maximum stamps to display")
	wlStampsCmd.Flags().BoolVar(&wlStampsJSON, "json", false, "Output as JSON")

	wlCmd.AddCommand(wlStampsCmd)
}

// StampsFilter holds filter parameters for building a stamps query.
type StampsFilter struct {
	Subject     string
	Author      string
	Skill       string
	ContextType string
	StampType   string
	PilotCohort string
	Severity    string
	Limit       int
}

func buildStampsQuery(f StampsFilter) string {
	var conditions []string

	conditions = append(conditions, fmt.Sprintf("subject = '%s'", doltserver.EscapeSQL(f.Subject)))

	if f.Author != "" {
		conditions = append(conditions, fmt.Sprintf("author = '%s'", doltserver.EscapeSQL(f.Author)))
	}
	if f.ContextType != "" {
		conditions = append(conditions, fmt.Sprintf("context_type = '%s'", doltserver.EscapeSQL(f.ContextType)))
	}
	if f.StampType != "" {
		conditions = append(conditions, fmt.Sprintf("stamp_type = '%s'", doltserver.EscapeSQL(f.StampType)))
	}
	if f.PilotCohort != "" {
		conditions = append(conditions, fmt.Sprintf("pilot_cohort = '%s'", doltserver.EscapeSQL(f.PilotCohort)))
	}
	if f.Severity != "" {
		conditions = append(conditions, fmt.Sprintf("severity = '%s'", doltserver.EscapeSQL(f.Severity)))
	}
	if f.Skill != "" {
		// JSON_CONTAINS checks if the skill_tags array contains the given value
		conditions = append(conditions, fmt.Sprintf("JSON_CONTAINS(skill_tags, '\"%s\"')", doltserver.EscapeSQL(f.Skill)))
	}

	query := "SELECT id, author, subject, valence, confidence, severity, context_id, context_type, skill_tags, message, created_at FROM stamps"
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY created_at DESC"
	query += fmt.Sprintf(" LIMIT %d", f.Limit)

	return query
}

func runWLStamps(cmd *cobra.Command, args []string) error {
	wlStampsRig = args[0]

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Fast path: query through the Dolt server if the database is registered.
	dbName := wasteland.ResolveDBName(townRoot)
	if doltserver.DatabaseExists(townRoot, dbName) {
		query := buildStampsQuery(StampsFilter{
			Subject:     wlStampsRig,
			Author:      wlStampsAuthor,
			Skill:       wlStampsSkill,
			ContextType: wlStampsContextType,
			StampType:   wlStampsStampType,
			PilotCohort: wlStampsCohort,
			Severity:    wlStampsSeverity,
			Limit:       wlStampsLimit,
		})
		serverQuery := fmt.Sprintf("USE %s; %s", dbName, query)

		if wlStampsJSON {
			output, err := doltserver.QueryJSON(townRoot, serverQuery)
			if err != nil {
				return err
			}
			fmt.Print(output)
			return nil
		}

		// Use JSON output for richer parsing (valence, skill_tags are JSON).
		output, err := doltserver.QueryJSON(townRoot, serverQuery)
		if err != nil {
			return err
		}

		var result struct {
			Rows []map[string]interface{} `json:"rows"`
		}
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}

		if len(result.Rows) == 0 {
			fmt.Printf("No stamps found for rig %q.\n", wlStampsRig)
			return nil
		}

		tbl := style.NewTable(
			style.Column{Name: "ID", Width: 16},
			style.Column{Name: "AUTHOR", Width: 20},
			style.Column{Name: "VALENCE", Width: 28},
			style.Column{Name: "CONF", Width: 5, Align: style.AlignRight},
			style.Column{Name: "SEVERITY", Width: 8},
			style.Column{Name: "TYPE", Width: 14},
			style.Column{Name: "SKILLS", Width: 18},
			style.Column{Name: "DATE", Width: 10},
		)

		for _, row := range result.Rows {
			id := getString(row, "id")
			author := getString(row, "author")
			valence := formatValence(row["valence"])
			conf := getString(row, "confidence")
			severity := getString(row, "severity")
			ctxType := getString(row, "context_type")
			skills := formatSkillTags(row["skill_tags"])
			date := formatStampDate(getString(row, "created_at"))
			tbl.AddRow(id, author, valence, conf, severity, ctxType, skills, date)
		}

		fmt.Printf("Stamps for %s (%d):\n\n", style.Bold.Render(wlStampsRig), len(result.Rows))
		fmt.Print(tbl.Render())
		return nil
	}

	// Fallback: read from local filesystem clone.
	doltPath, err := exec.LookPath("dolt")
	if err != nil {
		return fmt.Errorf("dolt not found in PATH — install from https://docs.dolthub.com/introduction/installation")
	}

	// Try wasteland config first (set by gt wl join).
	var cloneDir string
	if cfg, cfgErr := wasteland.LoadConfig(townRoot); cfgErr == nil && cfg.LocalDir != "" {
		if _, statErr := os.Stat(filepath.Join(cfg.LocalDir, ".dolt")); statErr == nil {
			cloneDir = cfg.LocalDir
		}
	}

	// Try local fork (fast path)
	if cloneDir == "" {
		cloneDir = findWLCommonsFork(townRoot)
	}

	// No local fork — clone fresh from config upstream or default
	if cloneDir == "" {
		commonsOrg := "hop"
		commonsDB := "wl-commons"
		if cfg, cfgErr := wasteland.LoadConfig(townRoot); cfgErr == nil && cfg.Upstream != "" {
			if o, d, parseErr := wasteland.ParseUpstream(cfg.Upstream); parseErr == nil {
				commonsOrg = o
				commonsDB = d
			}
		}

		tmpDir, tmpErr := os.MkdirTemp("", "wl-stamps-*")
		if tmpErr != nil {
			return fmt.Errorf("creating temp directory: %w", tmpErr)
		}
		defer os.RemoveAll(tmpDir)

		cloneDir = filepath.Join(tmpDir, commonsDB)
		remote := fmt.Sprintf("%s/%s", commonsOrg, commonsDB)

		if !wlStampsJSON {
			fmt.Printf("Cloning %s...\n", style.Bold.Render(remote))
		}

		cloneCmd := exec.Command(doltPath, "clone", remote, cloneDir)
		if !wlStampsJSON {
			cloneCmd.Stderr = os.Stderr
		}
		if err := cloneCmd.Run(); err != nil {
			return fmt.Errorf("cloning %s: %w\nEnsure the database exists on DoltHub: https://www.dolthub.com/%s", remote, err, remote)
		}
		if !wlStampsJSON {
			fmt.Printf("%s Cloned successfully\n\n", style.Bold.Render("✓"))
		}
	}

	query := buildStampsQuery(StampsFilter{
		Subject:     wlStampsRig,
		Author:      wlStampsAuthor,
		Skill:       wlStampsSkill,
		ContextType: wlStampsContextType,
		StampType:   wlStampsStampType,
		PilotCohort: wlStampsCohort,
		Severity:    wlStampsSeverity,
		Limit:       wlStampsLimit,
	})

	if wlStampsJSON {
		sqlCmd := exec.Command(doltPath, "sql", "-q", query, "-r", "json")
		sqlCmd.Dir = cloneDir
		sqlCmd.Stdout = os.Stdout
		sqlCmd.Stderr = os.Stderr
		return sqlCmd.Run()
	}

	return renderStampsTable(doltPath, cloneDir, query)
}

func renderStampsTable(doltPath, cloneDir, query string) error {
	// Use JSON output for richer parsing of nested fields (valence, skill_tags)
	sqlCmd := exec.Command(doltPath, "sql", "-q", query, "-r", "json")
	sqlCmd.Dir = cloneDir
	output, err := sqlCmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("query failed: %s", string(exitErr.Stderr))
		}
		return fmt.Errorf("running query: %w", err)
	}

	var result struct {
		Rows []map[string]interface{} `json:"rows"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return fmt.Errorf("parsing response: %w", err)
	}

	if len(result.Rows) == 0 {
		fmt.Printf("No stamps found for rig %q.\n", wlStampsRig)
		return nil
	}

	tbl := style.NewTable(
		style.Column{Name: "ID", Width: 16},
		style.Column{Name: "AUTHOR", Width: 20},
		style.Column{Name: "VALENCE", Width: 28},
		style.Column{Name: "CONF", Width: 5, Align: style.AlignRight},
		style.Column{Name: "SEVERITY", Width: 8},
		style.Column{Name: "TYPE", Width: 14},
		style.Column{Name: "SKILLS", Width: 18},
		style.Column{Name: "DATE", Width: 10},
	)

	for _, row := range result.Rows {
		id := getString(row, "id")
		author := getString(row, "author")
		valence := formatValence(row["valence"])
		conf := getString(row, "confidence")
		severity := getString(row, "severity")
		ctxType := getString(row, "context_type")
		skills := formatSkillTags(row["skill_tags"])
		date := formatStampDate(getString(row, "created_at"))

		tbl.AddRow(id, author, valence, conf, severity, ctxType, skills, date)
	}

	fmt.Printf("Stamps for %s (%d):\n\n", style.Bold.Render(wlStampsRig), len(result.Rows))
	fmt.Print(tbl.Render())

	return nil
}

// formatValence renders the valence JSON into a compact human-readable string.
// e.g. {"quality": 4, "reliability": 3, "creativity": 2} → "Q:4 R:3 C:2"
func formatValence(v interface{}) string {
	if v == nil {
		return ""
	}

	var valMap map[string]interface{}

	switch val := v.(type) {
	case string:
		if err := json.Unmarshal([]byte(val), &valMap); err != nil {
			return val
		}
	case map[string]interface{}:
		valMap = val
	default:
		return fmt.Sprintf("%v", v)
	}

	var parts []string
	for _, pair := range []struct {
		key   string
		label string
	}{
		{"quality", "Q"},
		{"reliability", "R"},
		{"creativity", "C"},
		{"volume", "V"},
	} {
		if score, ok := valMap[pair.key]; ok {
			parts = append(parts, fmt.Sprintf("%s:%.0f", pair.label, toFloat(score)))
		}
	}

	if len(parts) == 0 {
		// Fallback: show all keys
		for k, score := range valMap {
			parts = append(parts, fmt.Sprintf("%s:%.0f", k, toFloat(score)))
		}
	}

	return strings.Join(parts, " ")
}

// formatSkillTags renders the skill_tags JSON array into a comma-separated string.
func formatSkillTags(v interface{}) string {
	if v == nil {
		return ""
	}

	switch val := v.(type) {
	case string:
		var tags []string
		if err := json.Unmarshal([]byte(val), &tags); err != nil {
			return val
		}
		return strings.Join(tags, ", ")
	case []interface{}:
		var tags []string
		for _, t := range val {
			tags = append(tags, fmt.Sprintf("%v", t))
		}
		return strings.Join(tags, ", ")
	default:
		return fmt.Sprintf("%v", v)
	}
}

// formatStampDate extracts YYYY-MM-DD from a timestamp string.
func formatStampDate(ts string) string {
	if len(ts) >= 10 {
		return ts[:10]
	}
	return ts
}

// toFloat converts a JSON number to float64.
func toFloat(v interface{}) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case int:
		return float64(val)
	default:
		return 0
	}
}
