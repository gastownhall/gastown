package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

var (
	migrateBeadLabelsDry bool
	migrateBeadLabelsDB  string
)

var migrateBeadLabelsCmd = &cobra.Command{
	Use:     "migrate-bead-labels",
	GroupID: GroupWorkspace,
	Short:   "Add gt:* labels to existing Gas Town beads",
	Long: `Add gt:* labels to existing beads based on their issue_type field.

Gas Town bead types were extracted from beads core in v0.46.0 and now use
labels (e.g., gt:agent) for identification. This one-time migration
backfills those labels on all pre-existing beads.

Type→label mappings:
  agent         → gt:agent
  role          → gt:role
  rig           → gt:rig
  convoy        → gt:convoy
  slot          → gt:slot
  queue         → gt:queue
  event         → gt:event
  message       → gt:message
  molecule      → gt:molecule
  gate          → gt:gate
  merge-request → gt:merge-request

The migration is idempotent — safe to run multiple times.
Use --dry-run to preview changes without applying them.

Must be run AFTER beads custom type support is deployed
(this is the Gas Town side of bd-jybi).`,
	RunE: runMigrateBeadLabels,
}

func init() {
	migrateBeadLabelsCmd.Flags().BoolVar(&migrateBeadLabelsDry, "dry-run", false,
		"Preview labels that would be added without making changes")
	migrateBeadLabelsCmd.Flags().StringVar(&migrateBeadLabelsDB, "db", "",
		"Migrate a single rig database instead of all (e.g. gastown, hq)")
	rootCmd.AddCommand(migrateBeadLabelsCmd)
}

func runMigrateBeadLabels(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	if migrateBeadLabelsDry {
		fmt.Printf("%s Dry-run mode — no changes will be made\n\n", style.Bold.Render("ℹ"))
	}

	if migrateBeadLabelsDB != "" {
		return runMigrateOneDB(townRoot, migrateBeadLabelsDB)
	}

	// Auto-detect: migrate every rig database on this server.
	databases, err := doltserver.ListDatabases(townRoot)
	if err != nil {
		return fmt.Errorf("listing databases: %w", err)
	}

	var totalIssues, totalWisps int
	for _, db := range databases {
		if db == "wl_commons" || strings.HasPrefix(db, "testdb_") || strings.HasPrefix(db, "beads_t") ||
			strings.HasPrefix(db, "beads_pt") || strings.HasPrefix(db, "doctest_") {
			continue
		}

		rigDir := rigDirForDB(townRoot, db)
		if rigDir == "" {
			continue
		}

		fmt.Printf("\n%s Migrating: %s\n", style.Bold.Render("→"), db)
		result, err := doltserver.MigrateBeadLabels(rigDir, migrateBeadLabelsDry)
		if err != nil {
			fmt.Printf("  %s %s: %v\n", style.Bold.Render("✗"), db, err)
			continue
		}
		printMigrateBeadLabelsResult(result)
		totalIssues += result.LabelsAdded
		totalWisps += result.WispLabelsAdded
	}

	action := "added"
	if migrateBeadLabelsDry {
		action = "would add"
	}
	fmt.Printf("\n%s Total: %s %d label(s) to issues, %d to wisps\n",
		style.Bold.Render("✓"), action, totalIssues, totalWisps)
	return nil
}

func runMigrateOneDB(townRoot, db string) error {
	rigDir := rigDirForDB(townRoot, db)
	if rigDir == "" {
		return fmt.Errorf("rig directory not found for database %q", db)
	}

	fmt.Printf("%s Migrating: %s\n", style.Bold.Render("→"), db)
	result, err := doltserver.MigrateBeadLabels(rigDir, migrateBeadLabelsDry)
	if err != nil {
		return err
	}
	printMigrateBeadLabelsResult(result)
	return nil
}

// rigDirForDB returns the rig directory to use as the working directory for bd
// commands against the given Dolt database. Returns "" if the directory does
// not exist (skipping non-rig databases is safe).
func rigDirForDB(townRoot, db string) string {
	if db == "hq" {
		return townRoot
	}
	dir := filepath.Join(townRoot, db)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return ""
	}
	return dir
}

func printMigrateBeadLabelsResult(result *doltserver.MigrateBeadLabelsResult) {
	if result.LabelsAdded == 0 && result.WispLabelsAdded == 0 {
		fmt.Printf("  %s Already up to date\n", style.Bold.Render("✓"))
		return
	}
	if result.LabelsAdded > 0 {
		fmt.Printf("  %s Added %d label(s) to issues\n", style.Bold.Render("✓"), result.LabelsAdded)
	}
	if result.WispLabelsAdded > 0 {
		fmt.Printf("  %s Added %d label(s) to wisps\n", style.Bold.Render("✓"), result.WispLabelsAdded)
	}
}
