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

var wlShowJSON bool

var wlShowCmd = &cobra.Command{
	Use:   "show <work-id>",
	Short: "Show full details of a wanted item",
	Long: `Display all fields of a single wanted item from the wl-commons database.

Unlike 'gt wl browse' which truncates titles and omits descriptions,
'gt wl show' displays every field of the item.

The local wl-commons database is queried directly (kept in sync by gt wl sync).

EXAMPLES:
  gt wl show w-abc123             # Display all fields
  gt wl show w-abc123 --json      # JSON output`,
	Args: cobra.ExactArgs(1),
	RunE: runWLShow,
}

func init() {
	wlShowCmd.Flags().BoolVar(&wlShowJSON, "json", false, "Output as JSON")
	wlCmd.AddCommand(wlShowCmd)
}

func runWLShow(cmd *cobra.Command, args []string) error {
	wantedID := args[0]

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Fast path: query through the Dolt server if the database is registered.
	dbName := wasteland.ResolveDBName(townRoot)
	if doltserver.DatabaseExists(townRoot, dbName) {
		store := doltserver.NewWLCommonsWithDB(townRoot, dbName)
		return showWanted(store, wantedID, wlShowJSON)
	}

	// Fallback: read from local filesystem clone.
	doltPath, err := exec.LookPath("dolt")
	if err != nil {
		return fmt.Errorf("dolt not found in PATH — install from https://docs.dolthub.com/introduction/installation")
	}

	cloneDir, tmpDir, err := resolveWLCommonsClone(townRoot, doltPath)
	if err != nil {
		return err
	}
	if tmpDir != "" {
		defer os.RemoveAll(tmpDir)
	} else {
		// Persistent clone exists — auto-fetch to freshen data.
		autoFetchWLCommons(doltPath, cloneDir, townRoot)
	}

	query := buildWLShowQuery(wantedID)

	if wlShowJSON {
		sqlCmd := exec.Command(doltPath, "sql", "-q", query, "-r", "json")
		sqlCmd.Dir = cloneDir
		sqlCmd.Stdout = os.Stdout
		sqlCmd.Stderr = os.Stderr
		return sqlCmd.Run()
	}

	item, err := queryWantedFromClone(doltPath, cloneDir, wantedID)
	if err != nil {
		return err
	}
	return renderWantedItem(item)
}

// resolveWLCommonsClone finds the local wl-commons clone directory.
// Returns (cloneDir, tmpDir, err). If tmpDir is non-empty, caller must
// defer os.RemoveAll(tmpDir) — a temporary clone was created.
func resolveWLCommonsClone(townRoot, doltPath string) (cloneDir, tmpDir string, err error) {
	// Try wasteland config (set by gt wl join).
	if cfg, cfgErr := wasteland.LoadConfig(townRoot); cfgErr == nil && cfg.LocalDir != "" {
		if _, statErr := os.Stat(filepath.Join(cfg.LocalDir, ".dolt")); statErr == nil {
			return cfg.LocalDir, "", nil
		}
	}

	// Try standard location: .wasteland/hop/wl-commons.
	stdPath := wasteland.LocalCloneDir(townRoot, "hop", "wl-commons")
	if _, statErr := os.Stat(filepath.Join(stdPath, ".dolt")); statErr == nil {
		return stdPath, "", nil
	}

	// Try common fallback locations.
	if forkDir := findWLCommonsFork(townRoot); forkDir != "" {
		return forkDir, "", nil
	}

	// No local clone — do a one-time clone-then-discard, like browse.
	// Read upstream from config, or default to hop/wl-commons.
	remote := "hop/wl-commons"
	if cfg, cfgErr := wasteland.LoadConfig(townRoot); cfgErr == nil && cfg.Upstream != "" {
		remote = cfg.Upstream
	}
	fmt.Fprintf(os.Stderr, "No local wl-commons clone found. Cloning temporarily.\nRun 'gt wl sync' to keep a persistent local copy.\n\n")
	tmpDir, err = os.MkdirTemp("", "wl-show-*")
	if err != nil {
		return "", "", fmt.Errorf("creating temp directory: %w", err)
	}
	parts := strings.SplitN(remote, "/", 2)
	dbName := "wl-commons"
	if len(parts) == 2 {
		dbName = parts[1]
	}
	cloneDir = filepath.Join(tmpDir, dbName)
	fmt.Printf("Cloning %s...\n", style.Bold.Render(remote))
	cloneCmd := exec.Command(doltPath, "clone", remote, cloneDir)
	cloneCmd.Stderr = os.Stderr
	if cloneErr := cloneCmd.Run(); cloneErr != nil {
		os.RemoveAll(tmpDir)
		return "", "", fmt.Errorf("cloning %s: %w\nEnsure the database exists on DoltHub: https://www.dolthub.com/%s", remote, cloneErr, remote)
	}
	return cloneDir, tmpDir, nil
}

// autoFetchWLCommons runs dolt fetch + merge on the local clone to freshen data.
// Errors are non-fatal: the command proceeds with whatever local data exists.
func autoFetchWLCommons(doltPath, cloneDir, townRoot string) {
	// Use "upstream" remote if this is a wasteland fork, otherwise "origin".
	remote := "origin"
	if cfg, err := wasteland.LoadConfig(townRoot); err == nil && cfg.LocalDir == cloneDir {
		remote = "upstream"
	}
	fetchCmd := exec.Command(doltPath, "fetch", remote)
	fetchCmd.Dir = cloneDir
	if err := fetchCmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to fetch from %s: %v\n", remote, err)
		return
	}
	mergeCmd := exec.Command(doltPath, "merge", remote+"/main")
	mergeCmd.Dir = cloneDir
	if err := mergeCmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to merge %s/main: %v\n", remote, err)
	}
}

// buildWLShowQuery returns the SELECT query for a single wanted item.
func buildWLShowQuery(wantedID string) string {
	return fmt.Sprintf(
		"SELECT id, title,"+
			" COALESCE(description, '') as description,"+
			" COALESCE(project, '') as project,"+
			" COALESCE(type, '') as type,"+
			" priority,"+
			" COALESCE(tags, JSON_ARRAY()) as tags,"+
			" COALESCE(posted_by, '') as posted_by,"+
			" COALESCE(claimed_by, '') as claimed_by,"+
			" status,"+
			" COALESCE(effort_level, '') as effort_level,"+
			" COALESCE(evidence_url, '') as evidence_url,"+
			" COALESCE(sandbox_required, 0) as sandbox_required,"+
			" COALESCE(CAST(created_at AS CHAR), '') as created_at,"+
			" COALESCE(CAST(updated_at AS CHAR), '') as updated_at"+
			" FROM wanted WHERE id='%s'",
		doltserver.EscapeSQL(wantedID))
}

// queryWantedFromClone queries a local dolt clone dir and returns the WantedItem.
func queryWantedFromClone(doltPath, cloneDir, wantedID string) (*doltserver.WantedItem, error) {
	query := buildWLShowQuery(wantedID)
	sqlCmd := exec.Command(doltPath, "sql", "-q", query, "-r", "csv")
	sqlCmd.Dir = cloneDir
	output, err := sqlCmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("query failed: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("running query: %w", err)
	}

	rows := wlParseCSV(string(output))
	if len(rows) < 2 {
		return nil, fmt.Errorf("wanted item %q not found", wantedID)
	}

	headers := rows[0]
	values := rows[1]
	data := make(map[string]string, len(headers))
	for i, h := range headers {
		if i < len(values) {
			data[h] = values[i]
		}
	}

	item := &doltserver.WantedItem{
		ID:          data["id"],
		Title:       data["title"],
		Description: data["description"],
		Project:     data["project"],
		Type:        data["type"],
		PostedBy:    data["posted_by"],
		ClaimedBy:   data["claimed_by"],
		Status:      data["status"],
		EffortLevel: data["effort_level"],
		EvidenceURL: data["evidence_url"],
		CreatedAt:   data["created_at"],
		UpdatedAt:   data["updated_at"],
	}
	if p := data["priority"]; p != "" {
		_, _ = fmt.Sscanf(p, "%d", &item.Priority)
	}
	if data["sandbox_required"] == "1" {
		item.SandboxRequired = true
	}
	if tagsJSON := data["tags"]; tagsJSON != "" && tagsJSON != "[]" {
		_ = json.Unmarshal([]byte(tagsJSON), &item.Tags)
	}
	return item, nil
}

// showWanted contains the testable logic for displaying a wanted item.
func showWanted(store doltserver.WLCommonsStore, wantedID string, asJSON bool) error {
	item, err := store.QueryWantedFull(wantedID)
	if err != nil {
		return err
	}

	if asJSON {
		data, err := json.MarshalIndent(item, "", "  ")
		if err != nil {
			return fmt.Errorf("marshaling JSON: %w", err)
		}
		fmt.Println(string(data))
		return nil
	}

	return renderWantedItem(item)
}

func renderWantedItem(item *doltserver.WantedItem) error {
	tags := strings.Join(item.Tags, ", ")

	sandboxVal := "No"
	if item.SandboxRequired {
		sandboxVal = "Yes"
	}

	rows := []struct{ label, value string }{
		{"ID", item.ID},
		{"Title", item.Title},
		{"Description", item.Description},
		{"Project", item.Project},
		{"Type", item.Type},
		{"Priority", wlFormatPriority(fmt.Sprintf("%d", item.Priority))},
		{"Tags", tags},
		{"Posted By", item.PostedBy},
		{"Claimed By", item.ClaimedBy},
		{"Status", item.Status},
		{"Effort", item.EffortLevel},
		{"Evidence URL", item.EvidenceURL},
		{"Sandbox", sandboxVal},
		{"Created", item.CreatedAt},
		{"Updated", item.UpdatedAt},
	}

	labelWidth := 13
	indent := strings.Repeat(" ", labelWidth+2)
	for _, r := range rows {
		val := r.value
		if val == "" {
			val = "(none)"
		}
		lines := strings.Split(val, "\n")
		fmt.Printf("%-*s  %s\n", labelWidth, r.label+":", lines[0])
		for _, line := range lines[1:] {
			fmt.Printf("%s%s\n", indent, line)
		}
	}
	return nil
}

// getString safely extracts a string from a map value.
func getString(m map[string]interface{}, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case float64:
		return fmt.Sprintf("%.0f", val)
	default:
		return fmt.Sprintf("%v", val)
	}
}
