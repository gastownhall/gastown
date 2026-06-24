package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/daemon"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

var doltCmd = &cobra.Command{
	Use:     "dolt",
	GroupID: GroupServices,
	Short:   "Manage the Dolt SQL server",
	RunE:    requireSubcommand,
	Long: `Manage the Dolt SQL server for Gas Town beads.

The Dolt server provides multi-client access to all rig databases,
avoiding the single-writer limitation of embedded Dolt mode.

Server configuration:
  - Port: 3307 (avoids conflict with MySQL on 3306)
  - User: root (default Dolt user, no password for localhost)
  - Data directory: .dolt-data/ (contains all rig databases)

Each rig (hq, gastown, beads) has its own database subdirectory.`,
}

var doltInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize and repair Dolt workspace configuration",
	Long: `Verify and repair the Dolt workspace configuration.

This command scans all rig metadata.json files for Dolt server configuration
and ensures the referenced databases actually exist. It fixes the broken state
where metadata.json says backend=dolt but the database is missing from .dolt-data/.

For each broken workspace, it will:
  1. Check if local .beads/dolt/ data exists and migrate it
  2. Otherwise, create a fresh database in .dolt-data/

This is safe to run multiple times (idempotent). It will not modify workspaces
that are already healthy.`,
	RunE: runDoltInit,
}

var doltStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the Dolt server",
	Long: `Start the Dolt SQL server in the background.

The server will run until stopped with 'gt dolt stop'.`,
	RunE: runDoltStart,
}

var doltStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the Dolt server",
	Long:  `Stop the running Dolt SQL server.`,
	RunE:  runDoltStop,
}

var doltRestartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart the Dolt server (kills imposters)",
	Long: `Stop the Dolt SQL server, kill any imposter servers on the configured port,
and start the correct server from the configured data directory.

This is the nuclear option for recovering from a hijacked port — when another
process (e.g., bd's embedded Dolt server) has taken over the port with a
different data directory, serving empty/wrong databases.

Steps:
  1. Stop the tracked server (via PID file)
  2. Kill any other dolt sql-server on the configured port (imposters)
  3. Start the correct server from .dolt-data/`,
	RunE: runDoltRestart,
}

var doltKillImpostersCmd = &cobra.Command{
	Use:   "kill-imposters",
	Short: "Kill dolt servers hijacking this workspace's port",
	Long: `Find and kill any dolt sql-server that holds this workspace's configured
port but serves from a different data directory (an "imposter").

This is safe to run at any time. It only kills servers that are:
  1. Listening on the same port as this workspace's Dolt config
  2. Serving from a data directory OTHER than this workspace's .dolt-data/

It never kills the workspace's own legitimate Dolt server.

Examples:
  gt dolt kill-imposters          # Kill imposters on configured port
  gt dolt kill-imposters --dry-run # Preview without killing`,
	RunE: runDoltKillImposters,
}

var doltKillImpostersDry bool

var doltStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show Dolt server status",
	Long:  `Show the current status of the Dolt SQL server.`,
	RunE:  runDoltStatus,
}

var doltLogsCmd = &cobra.Command{
	Use:   "logs",
	Short: "View Dolt server logs",
	Long:  `View the Dolt server log file.`,
	RunE:  runDoltLogs,
}

var doltDumpCmd = &cobra.Command{
	Use:   "dump",
	Short: "Collect non-fatal Dolt server diagnostics",
	Long: `Collect a non-fatal Dolt diagnostic snapshot for incident response.

This command does not send SIGQUIT. Dolt 1.86.5 terminates sql-server after
SIGQUIT, so default diagnostics gather process metadata and recent logs only.`,
	RunE: runDoltDump,
}

var doltSQLCmd = &cobra.Command{
	Use:   "sql",
	Short: "Open Dolt SQL shell",
	Long: `Open an interactive SQL shell to the Dolt database.

Works in both embedded mode (no server) and server mode.
For multi-client access, start the server first with 'gt dolt start'.`,
	RunE: runDoltSQL,
}

var doltInitRigCmd = &cobra.Command{
	Use:   "init-rig <name>",
	Short: "Initialize a new rig database",
	Long: `Initialize a new rig database in the Dolt data directory.

Each rig (e.g., gastown, beads) gets its own database that will be
served by the Dolt server. The rig name becomes the database name
when connecting via MySQL protocol.

Example:
  gt dolt init-rig gastown
  gt dolt init-rig beads`,
	Args: cobra.ExactArgs(1),
	RunE: runDoltInitRig,
}

var doltListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available rig databases",
	Long:  `List all rig databases in the Dolt data directory.`,
	RunE:  runDoltList,
}

var doltMigrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Migrate existing dolt databases to centralized data directory",
	Long: `Migrate existing dolt databases from .beads/dolt/ locations to the
centralized .dolt-data/ directory structure.

This command will:
1. Detect existing dolt databases in .beads/dolt/ directories
2. Move them to .dolt-data/<rigname>/
3. Remove the old empty directories

Use --dry-run to preview what would be moved (source/target paths and sizes)
without making any changes.

After migration, start the server with 'gt dolt start'.`,
	RunE: runDoltMigrate,
}

var doltFixMetadataCmd = &cobra.Command{
	Use:   "fix-metadata",
	Short: "Update metadata.json in all rig .beads directories",
	Long: `Ensure all rig .beads/metadata.json files have correct Dolt server configuration.

This fixes the split-brain problem where bd falls back to local embedded databases
instead of connecting to the centralized Dolt server. It updates metadata.json with:
  - backend: "dolt"
  - dolt_mode: "server"
  - dolt_database: "<rigname>"

Safe to run multiple times (idempotent). Preserves any existing fields in metadata.json.`,
	RunE: runDoltFixMetadata,
}

var doltRecoverCmd = &cobra.Command{
	Use:   "recover",
	Short: "Detect and recover from Dolt read-only state",
	Long: `Detect if the Dolt server is in read-only mode and attempt recovery.

When the Dolt server enters read-only mode (e.g., from concurrent write
contention on the storage manifest), all write operations fail. This command:

  1. Probes the server to detect read-only state
  2. Stops the server if read-only
  3. Restarts the server
  4. Verifies recovery with a write probe

If the server is already writable, this is a no-op.

The daemon performs this check automatically every 30 seconds. Use this command
for immediate recovery without waiting for the daemon's health check loop.`,
	RunE: runDoltRecover,
}

var doltSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Push Dolt databases to DoltHub remotes",
	Long: `Push all local Dolt databases to their configured DoltHub remotes.

When the Dolt server is running, pushes via SQL (CALL DOLT_PUSH) so the server
stays up and running agents are not disrupted. Falls back to CLI push (which
requires stopping the server) only when the server is not running.

This command automates the tedious process of pushing each database individually:
  1. Optionally purges closed ephemeral beads (--gc)
  2. Iterates databases in .dolt-data/
  3. For each database with a configured remote, pushes via SQL or CLI
  4. Reports success/failure per database

Use --db to sync a single database, --dry-run to preview, or --force for force-push.
Use --gc to purge closed ephemeral beads (wisps, convoys) before pushing.

Examples:
  gt dolt sync                # Push all databases with remotes
  gt dolt sync --dry-run      # Preview what would be pushed
  gt dolt sync --db gastown   # Push only the gastown database
  gt dolt sync --force        # Force-push all databases
  gt dolt sync --gc           # Purge closed ephemeral beads, then push
  gt dolt sync --gc --dry-run # Preview purge + push without changes`,
	RunE: runDoltSync,
}

var doltPullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Pull Dolt databases from remotes",
	Long: `Pull all local Dolt databases from their configured remotes.

When the Dolt server is running, pulls via SQL (CALL DOLT_PULL) so the server
stays up and avoids lock contention. Falls back to CLI pull only when the server
is not running.

This is the safe way to pull databases — using 'dolt pull' directly on a database
that the server is managing can cause exclusive lock contention and prevent
server restarts.

Examples:
  gt dolt pull                # Pull all databases with remotes
  gt dolt pull --db xtm       # Pull only the xtm database
  gt dolt pull --dry-run      # Preview what would be pulled`,
	RunE: runDoltPull,
}

var doltCleanupCmd = &cobra.Command{
	Use:   "cleanup",
	Short: "Remove orphaned databases from .dolt-data/",
	Long: `Detect and remove orphaned databases from the .dolt-data/ directory.

An orphaned database is one that exists in .dolt-data/ but is not referenced
by any rig's metadata.json. These are typically left over from partial setups,
renamed databases, or failed migrations.

Use --dry-run to preview what would be removed without making changes.

Examples:
  gt dolt cleanup             # Remove all orphaned databases
  gt dolt cleanup --dry-run   # Preview what would be removed`,
	RunE: runDoltCleanup,
}

var doltRollbackCmd = &cobra.Command{
	Use:   "rollback [backup-dir]",
	Short: "Restore .beads directories from a migration backup",
	Long: `Roll back a migration by restoring .beads directories from a backup.

If no backup directory is specified, the most recent migration-backup-TIMESTAMP/
directory is used automatically.

This command will:
1. Stop the Dolt server if running
2. Find the specified (or most recent) backup
3. Restore all .beads directories from the backup
4. Reset metadata.json files to their pre-migration state
5. Validate the restored state with bd list

The backup directory is expected to be in the format created by the migration
formula's backup step (migration-backup-YYYYMMDD-HHMMSS/).`,
	Args: cobra.MaximumNArgs(1),
	RunE: runDoltRollback,
}

var doltMigrateWispsCmd = &cobra.Command{
	Use:   "migrate-wisps",
	Short: "Migrate agent beads from issues to wisps table",
	Long: `Create the wisps table infrastructure and migrate existing agent beads.

This command:
1. Creates the wisps table (dolt_ignored, same schema as issues)
2. Creates auxiliary tables (wisp_labels, wisp_comments, wisp_events, wisp_dependencies)
3. Copies agent beads (issue_type='agent') from issues to wisps
4. Copies associated labels, comments, events, and dependencies
5. Closes the originals in the issues table

Idempotent — safe to run multiple times. Use --dry-run to preview.

After migration, 'bd mol wisp list' will work and agent lifecycle
(spawn, sling, work, done, nuke, respawn) uses the wisps table.`,
	RunE: runDoltMigrateWisps,
}

var (
	doltLogLines     int
	doltLogFollow    bool
	doltMigrateDry   bool
	doltCleanupDry   bool
	doltCleanupForce bool

	doltMigrateWispsDry bool
	doltMigrateWispsDB  string
	doltRollbackDry     bool
	doltRollbackList    bool
	doltSyncDry         bool
	doltSyncForce       bool
	doltSyncDB          string
	doltSyncGC          bool
	doltPullDry         bool
	doltPullDB          string
)

func init() {
	doltCmd.AddCommand(doltInitCmd)
	doltCmd.AddCommand(doltStartCmd)
	doltCmd.AddCommand(doltStopCmd)
	doltCmd.AddCommand(doltRestartCmd)
	doltCmd.AddCommand(doltKillImpostersCmd)
	doltCmd.AddCommand(doltStatusCmd)
	doltCmd.AddCommand(doltLogsCmd)
	doltCmd.AddCommand(doltDumpCmd)
	doltCmd.AddCommand(doltSQLCmd)
	doltCmd.AddCommand(doltInitRigCmd)
	doltCmd.AddCommand(doltListCmd)
	doltCmd.AddCommand(doltMigrateCmd)
	doltCmd.AddCommand(doltFixMetadataCmd)
	doltCmd.AddCommand(doltRecoverCmd)
	doltCmd.AddCommand(doltCleanupCmd)
	doltCmd.AddCommand(doltRollbackCmd)
	doltCmd.AddCommand(doltSyncCmd)
	doltCmd.AddCommand(doltPullCmd)
	doltCmd.AddCommand(doltMigrateWispsCmd)

	doltKillImpostersCmd.Flags().BoolVar(&doltKillImpostersDry, "dry-run", false, "Preview without killing")

	doltCleanupCmd.Flags().BoolVar(&doltCleanupDry, "dry-run", false, "Preview what would be removed without making changes")
	doltCleanupCmd.Flags().BoolVar(&doltCleanupForce, "force", false, "Remove databases even if they have user tables")
	doltLogsCmd.Flags().IntVarP(&doltLogLines, "lines", "n", 50, "Number of lines to show")
	doltLogsCmd.Flags().BoolVarP(&doltLogFollow, "follow", "f", false, "Follow log output")

	doltMigrateCmd.Flags().BoolVar(&doltMigrateDry, "dry-run", false, "Preview what would be migrated without making changes")

	doltRollbackCmd.Flags().BoolVar(&doltRollbackDry, "dry-run", false, "Show what would be restored without making changes")
	doltRollbackCmd.Flags().BoolVar(&doltRollbackList, "list", false, "List available backups and exit")

	doltSyncCmd.Flags().BoolVar(&doltSyncDry, "dry-run", false, "Preview what would be pushed without pushing")
	doltSyncCmd.Flags().BoolVar(&doltSyncForce, "force", false, "Force-push to remotes")
	doltSyncCmd.Flags().StringVar(&doltSyncDB, "db", "", "Sync a single database instead of all")
	doltSyncCmd.Flags().BoolVar(&doltSyncGC, "gc", false, "Purge closed ephemeral beads before push (requires bd purge)")

	doltPullCmd.Flags().BoolVar(&doltPullDry, "dry-run", false, "Preview what would be pulled without pulling")
	doltPullCmd.Flags().StringVar(&doltPullDB, "db", "", "Pull a single database instead of all")

	doltMigrateWispsCmd.Flags().BoolVar(&doltMigrateWispsDry, "dry-run", false, "Preview what would be migrated without making changes")
	doltMigrateWispsCmd.Flags().StringVar(&doltMigrateWispsDB, "db", "", "Target database (default: auto-detect from rig)")

	rootCmd.AddCommand(doltCmd)
}

func runDoltStart(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	config := doltserver.DefaultConfig(townRoot)
	if config.IsRemote() {
		return fmt.Errorf("Dolt server is remote (%s) — start/stop managed externally", config.HostPort())
	}

	// Check for databases before starting — user-facing guard for manual starts.
	// Internal callers (install, migrate) may legitimately start with an empty
	// data dir and create databases afterward via bd init.
	databases, _ := doltserver.ListDatabases(townRoot)
	if len(databases) == 0 {
		return fmt.Errorf("no databases found in %s\nInitialize with: gt dolt init-rig <name>", config.DataDir)
	}

	if err := doltserver.Start(townRoot); err != nil {
		return err
	}

	// Get state for display
	state, _ := doltserver.LoadState(townRoot)

	fmt.Printf("%s Dolt server started (PID %d, port %d)\n",
		style.Bold.Render("✓"), state.PID, config.Port)
	fmt.Printf("  Data dir: %s\n", state.DataDir)
	fmt.Printf("  Databases: %s\n", style.Dim.Render(strings.Join(state.Databases, ", ")))
	fmt.Printf("  Connection: %s\n", style.Dim.Render(doltserver.GetConnectionString(townRoot)))

	// Verify all filesystem databases are actually served by the SQL server.
	// Use retry since Start() only waits 500ms — DBs may still be loading.
	served, missing, verifyErr := doltserver.VerifyDatabasesWithRetry(townRoot, 5)
	if verifyErr != nil {
		fmt.Printf("  %s Could not verify databases: %v\n", style.Dim.Render("⚠"), verifyErr)
	} else if len(missing) > 0 {
		fmt.Printf("\n%s Some databases exist on disk but are NOT served:\n", style.Bold.Render("⚠"))
		for _, db := range missing {
			fmt.Printf("  - %s\n", db)
		}
		fmt.Printf("\n  Served: %v\n", served)
		fmt.Printf("  This usually means the database has a stale manifest.\n")
		fmt.Printf("  Try: %s\n", style.Dim.Render("cd ~/gt/.dolt-data/<db> && dolt fsck --repair"))
	} else {
		fmt.Printf("  %s All %d databases verified\n", style.Bold.Render("✓"), len(served))
	}

	return nil
}

func runDoltKillImposters(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	config := doltserver.DefaultConfig(townRoot)
	if config.IsRemote() {
		return fmt.Errorf("Dolt server is remote — imposter detection requires local server")
	}

	conflictPID, conflictDataDir := doltserver.CheckPortConflict(townRoot)
	if conflictPID == 0 {
		fmt.Printf("%s No imposters found on port %d\n", style.Bold.Render("✓"), config.Port)
		return nil
	}

	fmt.Printf("Found imposter dolt server:\n")
	fmt.Printf("  PID:      %d\n", conflictPID)
	fmt.Printf("  Data-dir: %s\n", conflictDataDir)
	fmt.Printf("  Expected: %s\n", config.DataDir)

	if doltKillImpostersDry {
		fmt.Printf("\n%s Dry-run — not killing\n", style.Warning.Render("~"))
		return nil
	}

	if err := doltserver.KillImposters(townRoot); err != nil {
		return fmt.Errorf("killing imposter: %w", err)
	}
	fmt.Printf("%s Imposter killed (PID %d)\n", style.Bold.Render("✓"), conflictPID)
	return nil
}

func runDoltStop(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	config := doltserver.DefaultConfig(townRoot)
	if config.IsRemote() {
		return fmt.Errorf("Dolt server is remote (%s) — start/stop managed externally", config.HostPort())
	}

	_, pid, _ := doltserver.IsRunning(townRoot)

	if err := doltserver.Stop(townRoot); err != nil {
		return err
	}

	fmt.Printf("%s Dolt server stopped (was PID %d)\n", style.Bold.Render("✓"), pid)
	return nil
}

func runDoltRestart(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	config := doltserver.DefaultConfig(townRoot)
	if config.IsRemote() {
		return fmt.Errorf("Dolt server is remote (%s) — start/stop managed externally", config.HostPort())
	}

	// Step 1: Stop tracked server (if running)
	running, pid, _ := doltserver.IsRunning(townRoot)
	if running {
		fmt.Printf("Stopping Dolt server (PID %d)...\n", pid)
		if err := doltserver.Stop(townRoot); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: stop failed: %v (continuing with imposter kill)\n", err)
		} else {
			fmt.Printf("%s Stopped\n", style.Bold.Render("✓"))
		}
	}

	// Step 2: Kill any imposters on the port
	fmt.Println("Checking for imposter servers...")
	if err := doltserver.KillImposters(townRoot); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: imposter kill failed: %v\n", err)
	}

	// Brief pause to let port be released
	time.Sleep(500 * time.Millisecond)

	// Step 3: Check for databases before starting
	databases, _ := doltserver.ListDatabases(townRoot)
	if len(databases) == 0 {
		return fmt.Errorf("no databases found in %s\nInitialize with: gt dolt init-rig <name>", config.DataDir)
	}

	// Step 4: Start the correct server
	fmt.Println("Starting Dolt server...")
	if err := doltserver.Start(townRoot); err != nil {
		return fmt.Errorf("restart failed: %w", err)
	}

	// Display status (same as gt dolt start)
	state, _ := doltserver.LoadState(townRoot)

	fmt.Printf("%s Dolt server restarted (PID %d, port %d)\n",
		style.Bold.Render("✓"), state.PID, config.Port)
	fmt.Printf("  Data dir: %s\n", state.DataDir)
	fmt.Printf("  Databases: %s\n", style.Dim.Render(strings.Join(state.Databases, ", ")))
	fmt.Printf("  Connection: %s\n", style.Dim.Render(doltserver.GetConnectionString(townRoot)))

	// Verify databases
	served, missing, verifyErr := doltserver.VerifyDatabasesWithRetry(townRoot, 5)
	if verifyErr != nil {
		fmt.Printf("  %s Could not verify databases: %v\n", style.Dim.Render("⚠"), verifyErr)
	} else if len(missing) > 0 {
		fmt.Printf("\n%s Some databases exist on disk but are NOT served:\n", style.Bold.Render("⚠"))
		for _, db := range missing {
			fmt.Printf("  - %s\n", db)
		}
		fmt.Printf("\n  Served: %v\n", served)
	} else {
		fmt.Printf("  %s All %d databases verified\n", style.Bold.Render("✓"), len(served))
	}

	return nil
}

func runDoltStatus(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	running, pid, err := doltserver.IsRunning(townRoot)
	if err != nil {
		return fmt.Errorf("checking server status: %w", err)
	}

	config := doltserver.DefaultConfig(townRoot)

	if config.IsRemote() {
		if running {
			fmt.Printf("%s Dolt server is %s (remote: %s)\n",
				style.Bold.Render("●"),
				style.Bold.Render("reachable"),
				config.HostPort())
		} else {
			fmt.Printf("%s Dolt server is %s (remote: %s)\n",
				style.Dim.Render("○"),
				"not reachable",
				config.HostPort())
		}
		fmt.Printf("  Connection: %s\n", doltserver.GetConnectionString(townRoot))
		printBeadsRuntimeConfig(townRoot)
		if running {
			metrics := doltserver.GetHealthMetrics(townRoot)
			fmt.Printf("\n  %s\n", style.Bold.Render("Resource Metrics:"))
			fmt.Printf("    Query latency: %v\n", metrics.QueryLatency.Round(time.Millisecond))
			fmt.Printf("    Connections:   %d / %d (%.0f%%)\n",
				metrics.Connections, metrics.MaxConnections, metrics.ConnectionPct)
			if metrics.ReadOnly {
				fmt.Printf("\n  %s %s\n",
					style.Bold.Render("!!!"),
					style.Bold.Render("SERVER IS READ-ONLY — contact the remote server admin"))
			}
		}
		return nil
	}

	if running {
		fmt.Printf("%s Dolt server is %s (PID %d)\n",
			style.Bold.Render("●"),
			style.Bold.Render("running"),
			pid)

		// Load state for more details
		state, err := doltserver.LoadState(townRoot)
		if err == nil && !state.StartedAt.IsZero() {
			fmt.Printf("  Started: %s\n", state.StartedAt.Format("2006-01-02 15:04:05"))
			fmt.Printf("  Port: %d\n", state.Port)
			fmt.Printf("  Data dir: %s\n", state.DataDir)
			if len(state.Databases) > 0 {
				owners := doltserver.CollectDatabaseOwners(townRoot)
				fmt.Printf("  Databases:\n")
				for _, db := range state.Databases {
					if owner, ok := owners[db]; ok {
						fmt.Printf("    - %-20s (%s)\n", db, owner)
					} else {
						fmt.Printf("    - %s\n", db)
					}
				}
			}
			fmt.Printf("  Connection: %s\n", doltserver.GetConnectionString(townRoot))
			printBeadsRuntimeConfig(townRoot)
		}

		// Resource metrics
		metrics := doltserver.GetHealthMetrics(townRoot)
		fmt.Printf("\n  %s\n", style.Bold.Render("Resource Metrics:"))
		fmt.Printf("    Query latency: %v\n", metrics.QueryLatency.Round(time.Millisecond))
		fmt.Printf("    Connections:   %d / %d (%.0f%%)\n",
			metrics.Connections, metrics.MaxConnections, metrics.ConnectionPct)
		fmt.Printf("    Disk usage:    %s\n", metrics.DiskUsageHuman)
		if metrics.ReadOnly {
			fmt.Printf("\n  %s %s\n",
				style.Bold.Render("!!!"),
				style.Bold.Render("SERVER IS READ-ONLY — run 'gt dolt recover' to restart"))
		}

		// Verify all filesystem databases are actually served.
		_, missing, verifyErr := doltserver.VerifyDatabases(townRoot)
		if verifyErr != nil {
			fmt.Printf("\n  %s Database verification failed: %v\n", style.Bold.Render("!"), verifyErr)
		} else if len(missing) > 0 {
			fmt.Printf("\n  %s %s\n", style.Bold.Render("!!!"),
				style.Bold.Render("MISSING DATABASES — exist on disk but not served:"))
			for _, db := range missing {
				fmt.Printf("    - %s\n", db)
			}
			fmt.Printf("  Try: cd ~/gt/.dolt-data/<db> && dolt fsck --repair\n")
		}

		// Check for orphaned databases
		orphans, orphanErr := doltserver.FindOrphanedDatabases(townRoot)
		if orphanErr == nil && len(orphans) > 0 {
			fmt.Printf("\n  %s %d orphaned database(s) (not referenced by any rig):\n",
				style.Bold.Render("!"), len(orphans))
			for _, o := range orphans {
				fmt.Printf("    - %s (%s)\n", o.Name, formatBytes(o.SizeBytes))
			}
			fmt.Printf("  Clean up with: %s\n", style.Dim.Render("gt dolt cleanup"))
		}

		if len(metrics.Warnings) > 0 {
			fmt.Printf("\n  %s\n", style.Bold.Render("Warnings:"))
			for _, w := range metrics.Warnings {
				fmt.Printf("    %s %s\n", style.Bold.Render("!"), w)
			}
		}
	} else {
		fmt.Printf("%s Dolt server is %s\n",
			style.Dim.Render("○"),
			"not running")

		// List available databases
		databases, _ := doltserver.ListDatabases(townRoot)
		if len(databases) == 0 {
			fmt.Printf("\n%s No rig databases found in %s\n",
				style.Bold.Render("!"),
				config.DataDir)
			fmt.Printf("  Initialize with: %s\n", style.Dim.Render("gt dolt init-rig <name>"))
		} else {
			fmt.Printf("\nAvailable databases in %s:\n", config.DataDir)
			owners := doltserver.CollectDatabaseOwners(townRoot)
			for _, db := range databases {
				if owner, ok := owners[db]; ok {
					fmt.Printf("  - %-20s (%s)\n", db, owner)
				} else {
					fmt.Printf("  - %s\n", db)
				}
			}
			fmt.Printf("\nStart with: %s\n", style.Dim.Render("gt dolt start"))
		}
	}

	return nil
}

type beadsRuntimeConfig struct {
	Source   string
	Database string
	Host     string
	Port     int
}

func currentBeadsRuntimeConfig() (beadsRuntimeConfig, bool) {
	cwd, err := os.Getwd()
	if err != nil {
		return beadsRuntimeConfig{}, false
	}
	return readBeadsRuntimeConfig(beads.ResolveBeadsDir(cwd))
}

func readBeadsRuntimeConfig(beadsDir string) (beadsRuntimeConfig, bool) {
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return beadsRuntimeConfig{}, false
	}

	var metadata struct {
		Backend        string `json:"backend"`
		Database       string `json:"database"`
		DoltMode       string `json:"dolt_mode"`
		DoltDatabase   string `json:"dolt_database"`
		DoltServerHost string `json:"dolt_server_host"`
		DoltServerPort int    `json:"dolt_server_port"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		return beadsRuntimeConfig{}, false
	}
	if metadata.Backend != "dolt" || metadata.DoltMode != "server" {
		return beadsRuntimeConfig{}, false
	}

	host := metadata.DoltServerHost
	if host == "" {
		host = "127.0.0.1"
	}
	port := metadata.DoltServerPort
	if port == 0 {
		if data, err := os.ReadFile(filepath.Join(beadsDir, "dolt-server.port")); err == nil {
			if parsed, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && parsed > 0 {
				port = parsed
			}
		}
	}
	if port == 0 {
		port = doltserver.DefaultPort
	}
	database := metadata.DoltDatabase
	if database == "" {
		database = metadata.Database
	}

	return beadsRuntimeConfig{
		Source:   metadataPath,
		Database: database,
		Host:     host,
		Port:     port,
	}, true
}

func printBeadsRuntimeConfig(townRoot string) {
	cfg, ok := currentBeadsRuntimeConfig()
	if !ok {
		return
	}
	parts := []string{"server metadata"}
	if cfg.Database != "" {
		parts = append(parts, "database "+cfg.Database)
	}
	if cfg.Host != "" && cfg.Port > 0 {
		parts = append(parts, netJoinHostPort(cfg.Host, cfg.Port))
	}
	if cfg.Source != "" {
		parts = append(parts, "from "+cfg.Source)
	}
	fmt.Printf("  Beads client: %s\n", strings.Join(parts, ", "))
	if hint := beadsScopeHint(cfg.Database, townRoot); hint != "" {
		fmt.Print(hint)
	}
}

func beadsScopeHint(database, townRoot string) string {
	if database != "hq" {
		return ""
	}

	return fmt.Sprintf("    Gas Town town beads use database hq. Use `bd -C %s <cmd>` for hq-* beads; do not use `bd --global`, which targets Beads' beads_global database.\n", townRoot)
}

func netJoinHostPort(host string, port int) string {
	return host + ":" + strconv.Itoa(port)
}

func runDoltLogs(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	config := doltserver.DefaultConfig(townRoot)

	if _, err := os.Stat(config.LogFile); os.IsNotExist(err) {
		return fmt.Errorf("no log file found at %s", config.LogFile)
	}

	if doltLogFollow {
		// Use tail -f for following
		tailCmd := exec.Command("tail", "-f", config.LogFile)
		tailCmd.Stdout = os.Stdout
		tailCmd.Stderr = os.Stderr
		return tailCmd.Run()
	}

	// Use tail -n for last N lines
	tailCmd := exec.Command("tail", "-n", strconv.Itoa(doltLogLines), config.LogFile)
	tailCmd.Stdout = os.Stdout
	tailCmd.Stderr = os.Stderr
	return tailCmd.Run()
}

func runDoltDump(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	running, pid, err := doltserver.IsRunning(townRoot)
	if err != nil {
		return fmt.Errorf("checking server status: %w", err)
	}
	if !running {
		return fmt.Errorf("Dolt server is not running — nothing to dump")
	}

	config := doltserver.DefaultConfig(townRoot)

	fmt.Printf("Dolt diagnostic snapshot (non-fatal)\n")
	fmt.Printf("  Live PID:   %d\n", pid)
	fmt.Printf("  Port:       %d\n", config.Port)
	fmt.Printf("  Data dir:   %s\n", config.DataDir)
	fmt.Printf("  Log file:   %s\n", config.LogFile)
	fmt.Printf("  Connection: %s\n", doltserver.GetConnectionString(townRoot))

	if info, err := doltserver.ReadSQLServerInfo(townRoot); err == nil {
		fmt.Printf("  SQL metadata: %s\n", info.Path)
		fmt.Printf("    PID:       %d\n", info.PID)
		fmt.Printf("    Port:      %d\n", info.Port)
		if info.ServerID != "" {
			fmt.Printf("    Server ID: %s\n", info.ServerID)
		}
	} else {
		fmt.Printf("  SQL metadata: unavailable (%v)\n", err)
	}

	if state, err := doltserver.LoadState(townRoot); err == nil && state.PID > 0 {
		fmt.Printf("  Daemon state: %s\n", doltserver.StateFile(townRoot))
		fmt.Printf("    PID:       %d", state.PID)
		if state.PID != pid {
			fmt.Printf(" (stale; live PID is %d)", pid)
		}
		fmt.Println()
		if !state.StartedAt.IsZero() {
			fmt.Printf("    Started:   %s\n", state.StartedAt.Format("2006-01-02 15:04:05"))
		}
		if state.DataDir != "" {
			fmt.Printf("    Data dir:  %s\n", state.DataDir)
		}
	}

	fmt.Printf("\nRecent Dolt log lines:\n")
	tailCmd := exec.Command("tail", "-n", "200", config.LogFile)
	tailCmd.Stdout = os.Stdout
	tailCmd.Stderr = os.Stderr
	if err := tailCmd.Run(); err != nil {
		fmt.Printf("  (unable to read recent logs: %v)\n", err)
	}

	fmt.Printf("\nNo signal was sent. Do not use kill -QUIT for routine diagnostics unless the Dolt version has been verified not to terminate on SIGQUIT.\n")

	return nil
}

func runDoltSQL(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	config := doltserver.DefaultConfig(townRoot)

	// Check if server is running - if so, connect via Dolt SQL client
	running, _, _ := doltserver.IsRunning(townRoot)
	if running {
		// Connect to running server using dolt sql client
		// Using --no-tls since server doesn't have TLS configured
		host := config.Host
		if host == "" {
			host = "127.0.0.1"
		}
		sqlArgs := []string{
			"--host", host,
			"--port", strconv.Itoa(config.Port),
			"--user", config.User,
			"--no-tls",
			"sql",
		}
		sqlCmd := exec.Command("dolt", sqlArgs...)
		// GH#2537: Set cmd.Dir to prevent stray .doltcfg/privileges.db in CWD.
		sqlCmd.Dir = config.DataDir
		if config.Password != "" {
			sqlCmd.Env = append(os.Environ(), "DOLT_CLI_PASSWORD="+config.Password)
		}
		sqlCmd.Stdin = os.Stdin
		sqlCmd.Stdout = os.Stdout
		sqlCmd.Stderr = os.Stderr
		return sqlCmd.Run()
	}

	// Server not running - list databases and pick first one for embedded mode
	databases, err := doltserver.ListDatabases(townRoot)
	if err != nil {
		return fmt.Errorf("listing databases: %w", err)
	}

	if len(databases) == 0 {
		return fmt.Errorf("no databases found in %s\nInitialize with: gt dolt init-rig <name>", config.DataDir)
	}

	// Use first database for embedded SQL shell
	dbDir := doltserver.RigDatabaseDir(townRoot, databases[0])
	fmt.Printf("Using database: %s (start server with 'gt dolt start' for multi-database access)\n\n", databases[0])

	sqlCmd := exec.Command("dolt", "sql")
	sqlCmd.Dir = dbDir
	sqlCmd.Stdin = os.Stdin
	sqlCmd.Stdout = os.Stdout
	sqlCmd.Stderr = os.Stderr

	return sqlCmd.Run()
}

func runDoltInitRig(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	rigName := args[0]

	serverWasRunning, created, err := doltserver.InitRig(townRoot, rigName)
	if err != nil {
		return err
	}

	config := doltserver.DefaultConfig(townRoot)
	rigDir := doltserver.RigDatabaseDir(townRoot, rigName)

	if !created {
		fmt.Printf("%s Rig database %q already exists (no-op)\n", style.Bold.Render("✓"), rigName)
		fmt.Printf("  Location: %s\n", rigDir)
		return nil
	}

	fmt.Printf("%s Initialized rig database %q\n", style.Bold.Render("✓"), rigName)
	fmt.Printf("  Location: %s\n", rigDir)
	fmt.Printf("  Data dir: %s\n", config.DataDir)

	if serverWasRunning {
		fmt.Printf("  Server: %s\n", style.Bold.Render("database registered with running server"))
	} else {
		fmt.Printf("\nStart server with: %s\n", style.Dim.Render("gt dolt start"))
	}

	return nil
}

func runDoltInit(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Find workspaces with broken Dolt configuration
	broken, verifyWarning := doltserver.FindBrokenWorkspaces(townRoot)
	if verifyWarning != "" {
		fmt.Printf("  %s %s\n\n", style.Bold.Render("⚠"), verifyWarning)
	}

	// Check for orphaned databases regardless of broken workspaces
	orphans, orphanErr := doltserver.FindOrphanedDatabases(townRoot)

	if len(broken) == 0 {
		// Also check if there are any databases at all
		databases, _ := doltserver.ListDatabases(townRoot)
		if len(databases) == 0 {
			fmt.Println("No Dolt databases found and no workspaces configured for Dolt.")
			fmt.Printf("\nInitialize a rig database with: %s\n", style.Dim.Render("gt dolt init-rig <name>"))
		} else {
			fmt.Printf("%s All workspaces healthy (%d database(s) verified)\n",
				style.Bold.Render("✓"), len(databases))
		}

		// Report orphans even when workspaces are healthy
		if orphanErr == nil && len(orphans) > 0 {
			fmt.Printf("\n%s %d orphaned database(s) in .dolt-data/ (not referenced by any rig):\n",
				style.Bold.Render("!"), len(orphans))
			for _, o := range orphans {
				fmt.Printf("  - %s (%s)\n", o.Name, formatBytes(o.SizeBytes))
			}
			fmt.Printf("\nClean up with: %s\n", style.Dim.Render("gt dolt cleanup"))
		}

		return nil
	}

	fmt.Printf("Found %d workspace(s) with broken Dolt configuration:\n\n", len(broken))

	repaired := 0
	for _, ws := range broken {
		if ws.NotServed {
			fmt.Printf("  %s %s: database %q exists on disk but is not served by the running Dolt server\n",
				style.Bold.Render("!"), ws.RigName, ws.ConfiguredDB)
			fmt.Printf("    Try restarting the server: %s\n", style.Dim.Render("gt dolt restart"))
			continue
		}
		fmt.Printf("  %s %s: metadata.json → database %q (missing from .dolt-data/)\n",
			style.Bold.Render("!"), ws.RigName, ws.ConfiguredDB)
		if ws.HasLocalData {
			fmt.Printf("    Local data found at %s\n", style.Dim.Render(ws.LocalDataPath))
		}

		action, err := doltserver.RepairWorkspace(townRoot, ws)
		if err != nil {
			fmt.Printf("    %s Repair failed: %v\n", style.Bold.Render("✗"), err)
			continue
		}

		fmt.Printf("    %s Repaired: %s\n", style.Bold.Render("✓"), action)
		repaired++
	}

	if repaired > 0 {
		fmt.Printf("\n%s Repaired %d/%d workspace(s)\n", style.Bold.Render("✓"), repaired, len(broken))
	}

	// Report orphans after repairs
	if orphanErr == nil && len(orphans) > 0 {
		fmt.Printf("\n%s %d orphaned database(s) in .dolt-data/ (not referenced by any rig):\n",
			style.Bold.Render("!"), len(orphans))
		for _, o := range orphans {
			fmt.Printf("  - %s (%s)\n", o.Name, formatBytes(o.SizeBytes))
		}
		fmt.Printf("\nClean up with: %s\n", style.Dim.Render("gt dolt cleanup"))
	}

	return nil
}

func runDoltCleanup(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	orphans, err := doltserver.FindOrphanedDatabases(townRoot)
	if err != nil {
		return fmt.Errorf("finding orphaned databases: %w", err)
	}

	if len(orphans) == 0 {
		fmt.Printf("%s No orphaned databases found in .dolt-data/\n", style.Bold.Render("✓"))
		return nil
	}

	fmt.Printf("Found %d orphaned database(s) in .dolt-data/:\n\n", len(orphans))
	for _, o := range orphans {
		fmt.Printf("  %s %s (%s)\n", style.Bold.Render("!"), o.Name, formatBytes(o.SizeBytes))
		fmt.Printf("    %s\n", style.Dim.Render(o.Path))
	}

	if doltCleanupDry {
		fmt.Println("\nDry run: no changes made.")
		return nil
	}

	// BALK: If orphans are a large fraction of all databases, something is likely
	// wrong with the orphan detection (e.g., metadata files not found). Refuse to
	// proceed without --force to prevent accidentally dropping production databases. (gt-xvh)
	allDBs, _ := doltserver.ListDatabases(townRoot)
	if len(allDBs) > 0 && !doltCleanupForce {
		orphanRatio := float64(len(orphans)) / float64(len(allDBs))
		if orphanRatio > 0.5 && len(orphans) > 3 {
			fmt.Printf("\n%s %d of %d databases (%.0f%%) flagged as orphans — this is suspicious.\n",
				style.Bold.Render("!"), len(orphans), len(allDBs), orphanRatio*100)
			fmt.Printf("  This usually means metadata.json files are missing or incorrect,\n")
			fmt.Printf("  not that the databases are actually orphaned.\n\n")
			fmt.Printf("  To proceed anyway: gt dolt cleanup --force\n")
			fmt.Printf("  To diagnose: gt dolt list   (check owner column for mismatches)\n")
			return fmt.Errorf("refusing to clean %d/%d databases without --force (safety check, gt-xvh)", len(orphans), len(allDBs))
		}
	}

	// BALK: If there are too many orphans, SQL-based cleanup will take hours
	// because each DROP DATABASE is a separate query against an overloaded server.
	// Force the user to stop the server and clean the filesystem directly.
	// (Clown Show #18: 245 orphans at 27s latency = ~2 hour cleanup)
	const maxSQLCleanup = 50
	if len(orphans) > maxSQLCleanup {
		fmt.Printf("\n%s Too many orphans (%d) for SQL-based cleanup (max %d).\n",
			style.Bold.Render("!"), len(orphans), maxSQLCleanup)
		fmt.Printf("  The server is likely overloaded. SQL cleanup would take hours.\n\n")
		fmt.Printf("  Instead, stop the server and clean the filesystem:\n\n")
		fmt.Printf("    gt dolt stop\n")
		fmt.Printf("    cd %s/.dolt-data && rm -rf testdb_* beads_t* beads_pt* beads_vr* doctest_* doctortest_*\n", townRoot)
		fmt.Printf("    gt dolt start\n\n")
		fmt.Printf("  This is safe — orphan databases have no production data.\n")
		return fmt.Errorf("too many orphans (%d) for SQL cleanup — see instructions above", len(orphans))
	}

	fmt.Println()
	removed := 0
	for _, o := range orphans {
		if err := doltserver.RemoveDatabase(townRoot, o.Name, doltCleanupForce); err != nil {
			// If DROP caused read-only, stop immediately and recover (gt-r1cyd)
			if doltserver.IsReadOnlyError(err.Error()) {
				fmt.Printf("  %s DROP put server into read-only mode — attempting recovery...\n", style.Bold.Render("!"))
				if recoverErr := doltserver.RecoverReadOnly(townRoot); recoverErr != nil {
					fmt.Printf("  %s Recovery failed: %v\n", style.Bold.Render("✗"), recoverErr)
					fmt.Printf("  Run: gt dolt stop && gt dolt start\n")
				} else {
					fmt.Printf("  %s Server recovered from read-only state\n", style.Bold.Render("✓"))
				}
				break
			}
			fmt.Printf("  %s Failed to remove %s: %v\n", style.Bold.Render("✗"), o.Name, err)
			continue
		}
		fmt.Printf("  %s Removed %s\n", style.Bold.Render("✓"), o.Name)
		removed++

		// Health check after each DROP to catch read-only early (gt-r1cyd)
		if readOnly, _ := doltserver.CheckReadOnly(townRoot); readOnly {
			fmt.Printf("  %s Server went read-only after DROP — attempting recovery...\n", style.Bold.Render("!"))
			if recoverErr := doltserver.RecoverReadOnly(townRoot); recoverErr != nil {
				fmt.Printf("  %s Recovery failed: %v\n", style.Bold.Render("✗"), recoverErr)
				fmt.Printf("  Run: gt dolt stop && gt dolt start\n")
				break
			}
			fmt.Printf("  %s Server recovered — continuing cleanup\n", style.Bold.Render("✓"))
		}
	}

	fmt.Printf("\n%s Removed %d/%d orphaned database(s)\n",
		style.Bold.Render("✓"), removed, len(orphans))

	return nil
}

func runDoltList(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	config := doltserver.DefaultConfig(townRoot)
	databases, err := doltserver.ListDatabases(townRoot)
	if err != nil {
		return fmt.Errorf("listing databases: %w", err)
	}

	if len(databases) == 0 {
		fmt.Printf("No rig databases found in %s\n", config.DataDir)
		fmt.Printf("\nInitialize with: %s\n", style.Dim.Render("gt dolt init-rig <name>"))
		return nil
	}

	owners := doltserver.CollectDatabaseOwners(townRoot)
	fmt.Printf("Rig databases in %s:\n\n", config.DataDir)
	for _, db := range databases {
		dbDir := doltserver.RigDatabaseDir(townRoot, db)
		if owner, ok := owners[db]; ok {
			fmt.Printf("  %s (%s)\n    %s\n", style.Bold.Render(db), owner, style.Dim.Render(dbDir))
		} else {
			fmt.Printf("  %s (orphan)\n    %s\n", style.Bold.Render(db), style.Dim.Render(dbDir))
		}
	}

	return nil
}

func runDoltMigrate(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	config := doltserver.DefaultConfig(townRoot)
	if config.IsRemote() {
		return fmt.Errorf("Dolt server is remote (%s) — migration requires local server access", config.HostPort())
	}

	// Check if daemon is running - must stop first to avoid race conditions.
	// The daemon spawns many bd processes via gt status heartbeats. If these
	// run concurrently with migration, race conditions occur between old
	// old and new backends.
	daemonRunning, _, _ := daemon.IsRunning(townRoot)
	if daemonRunning {
		return fmt.Errorf("Gas Town daemon is running. Stop it first with: gt daemon stop\n\nThe daemon spawns bd processes that can race with migration.\nStop the daemon, run migration, then restart it.")
	}

	// Check if Dolt server is running - must stop first
	running, _, _ := doltserver.IsRunning(townRoot)
	if running {
		return fmt.Errorf("Dolt server is running. Stop it first with: gt dolt stop")
	}

	// Find databases to migrate
	migrations := doltserver.FindMigratableDatabases(townRoot)
	if len(migrations) == 0 {
		fmt.Println("No databases found to migrate.")
		return nil
	}

	fmt.Printf("Found %d database(s) to migrate:\n\n", len(migrations))
	for _, m := range migrations {
		sizeStr := dirSizeHuman(m.SourcePath)
		fmt.Printf("  %s (%s)\n", m.SourcePath, sizeStr)
		fmt.Printf("    → %s\n\n", m.TargetPath)
	}

	if doltMigrateDry {
		fmt.Println("Dry run: no changes made.")
		return nil
	}

	// Perform migrations
	for _, m := range migrations {
		fmt.Printf("Migrating %s...\n", m.RigName)
		if err := doltserver.MigrateRigFromBeads(townRoot, m.RigName, m.SourcePath); err != nil {
			return fmt.Errorf("migrating %s: %w", m.RigName, err)
		}
		fmt.Printf("  %s Migrated to %s\n", style.Bold.Render("✓"), m.TargetPath)
	}

	// Update metadata.json for all migrated rigs
	updated, metaErrs := doltserver.EnsureAllMetadata(townRoot)
	if len(updated) > 0 {
		fmt.Printf("\nUpdated metadata.json for: %s\n", strings.Join(updated, ", "))
	}
	for _, err := range metaErrs {
		fmt.Printf("  %s metadata.json update failed: %v\n", style.Dim.Render("⚠"), err)
	}

	fmt.Printf("\n%s Migration complete.\n", style.Bold.Render("✓"))

	// Auto-start the Dolt server to prevent split-brain risk.
	// If bd commands are run before the server starts, they may silently create
	// isolated local databases instead of connecting to the centralized server.
	fmt.Printf("\nStarting Dolt server to prevent split-brain risk...\n")
	if err := doltserver.Start(townRoot); err != nil {
		fmt.Printf("\n%s Could not auto-start Dolt server: %v\n", style.Bold.Render("⚠"), err)
		fmt.Printf("\n%s WARNING: Do NOT run bd commands until the server is started!\n", style.Bold.Render("⚠"))
		fmt.Printf("  Running bd before 'gt dolt start' risks split-brain: bd may create an\n")
		fmt.Printf("  isolated local database instead of connecting to the centralized server.\n")
		fmt.Printf("\n  Start manually with: %s\n", style.Dim.Render("gt dolt start"))
	} else {
		state, _ := doltserver.LoadState(townRoot)
		fmt.Printf("%s Dolt server started (PID %d)\n", style.Bold.Render("✓"), state.PID)

		// Verify the server is actually serving all databases that exist on disk.
		// Dolt silently skips databases with stale manifests after migration,
		// so filesystem discovery and SQL discovery can diverge.
		// Use retry since the server may still be loading databases after Start().
		served, missing, verifyErr := doltserver.VerifyDatabasesWithRetry(townRoot, 5)
		if verifyErr != nil {
			fmt.Printf("  %s Could not verify databases: %v\n", style.Dim.Render("⚠"), verifyErr)
			fmt.Printf("  Migration may be incomplete. Verify manually with: %s\n", style.Dim.Render("gt dolt status"))
			return fmt.Errorf("database verification failed after migration: %w", verifyErr)
		} else if len(missing) > 0 {
			fmt.Printf("\n%s Some databases exist on disk but are NOT served by Dolt:\n", style.Bold.Render("⚠"))
			for _, db := range missing {
				fmt.Printf("  - %s\n", db)
			}
			fmt.Printf("\n  Served databases: %v\n", served)
			fmt.Printf("\n  This usually means the database has a stale manifest from migration.\n")
			fmt.Printf("  To fix, try:\n")
			fmt.Printf("    1. Stop the server:  %s\n", style.Dim.Render("gt dolt stop"))
			fmt.Printf("    2. Repair the DB:    %s\n", style.Dim.Render("cd ~/gt/.dolt-data/<db> && dolt fsck --repair"))
			fmt.Printf("    3. Restart:           %s\n", style.Dim.Render("gt dolt start"))
			return fmt.Errorf("migration incomplete: %d database(s) exist on disk but are not served: %v", len(missing), missing)
		} else {
			fmt.Printf("  %s All %d databases verified as served\n", style.Bold.Render("✓"), len(served))
		}
	}

	return nil
}

// dirSizeHuman returns a human-readable size string for a directory tree.
func dirSizeHuman(path string) string {
	var total int64
	_ = filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip errors
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return formatBytes(total)
}

func runDoltFixMetadata(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	updated, errs := doltserver.EnsureAllMetadata(townRoot)

	if len(updated) > 0 {
		fmt.Printf("%s Updated metadata.json for %d rig(s):\n", style.Bold.Render("✓"), len(updated))
		for _, name := range updated {
			fmt.Printf("  - %s\n", name)
		}
	}

	if len(errs) > 0 {
		fmt.Println()
		for _, err := range errs {
			fmt.Printf("  %s %v\n", style.Dim.Render("⚠"), err)
		}
	}

	if len(updated) == 0 && len(errs) == 0 {
		fmt.Println("No rig databases found. Nothing to update.")
	}

	return nil
}

func runDoltRecover(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	config := doltserver.DefaultConfig(townRoot)
	if config.IsRemote() {
		return fmt.Errorf("Dolt server is remote (%s) — recovery requires local server access", config.HostPort())
	}

	running, _, _ := doltserver.IsRunning(townRoot)
	if !running {
		return fmt.Errorf("Dolt server is not running — start with 'gt dolt start'")
	}

	readOnly, err := doltserver.CheckReadOnly(townRoot)
	if err != nil {
		return fmt.Errorf("read-only probe failed: %w", err)
	}

	if !readOnly {
		fmt.Printf("%s Dolt server is writable (no recovery needed)\n", style.Bold.Render("✓"))
		return nil
	}

	if err := doltserver.RecoverReadOnly(townRoot); err != nil {
		return fmt.Errorf("recovery failed: %w", err)
	}

	fmt.Printf("%s Dolt server recovered from read-only state\n", style.Bold.Render("✓"))
	return nil
}

func runDoltRollback(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	config := doltserver.DefaultConfig(townRoot)
	if config.IsRemote() {
		return fmt.Errorf("Dolt server is remote (%s) — rollback requires local server access", config.HostPort())
	}

	// Find available backups
	backups, err := doltserver.FindBackups(townRoot)
	if err != nil {
		return fmt.Errorf("finding backups: %w", err)
	}

	if len(backups) == 0 {
		return fmt.Errorf("no migration backups found in %s\nExpected directories matching: migration-backup-YYYYMMDD-HHMMSS/", townRoot)
	}

	// List mode: show available backups and exit
	if doltRollbackList {
		fmt.Printf("Available migration backups in %s:\n\n", townRoot)
		for i, b := range backups {
			label := ""
			if i == 0 {
				label = " (most recent)"
			}
			fmt.Printf("  %s%s\n", b.Timestamp, label)
			fmt.Printf("    %s\n", style.Dim.Render(b.Path))
			if b.Metadata != nil {
				if createdAt, ok := b.Metadata["created_at"]; ok {
					fmt.Printf("    Created: %v\n", createdAt)
				}
			}
		}
		return nil
	}

	// Determine which backup to use
	var backupPath string
	if len(args) > 0 {
		// User specified a backup directory
		backupPath = args[0]
		// Check if it's a relative path or timestamp
		if _, err := os.Stat(backupPath); os.IsNotExist(err) {
			// Try as a timestamp suffix
			candidate := fmt.Sprintf("migration-backup-%s", args[0])
			candidatePath := fmt.Sprintf("%s/%s", townRoot, candidate)
			if _, err := os.Stat(candidatePath); err == nil {
				backupPath = candidatePath
			} else {
				return fmt.Errorf("backup not found: %s\nUse --list to see available backups", args[0])
			}
		}
	} else {
		// Use the most recent backup
		backupPath = backups[0].Path
	}

	fmt.Printf("Backup: %s\n", backupPath)

	// Dry-run mode: show what would be restored
	if doltRollbackDry {
		fmt.Printf("\n%s Dry run - no changes will be made\n\n", style.Bold.Render("!"))
		printBackupContents(backupPath, townRoot)
		return nil
	}

	// Stop Dolt server if running
	running, _, _ := doltserver.IsRunning(townRoot)
	if running {
		fmt.Println("Stopping Dolt server...")
		if err := doltserver.Stop(townRoot); err != nil {
			return fmt.Errorf("stopping Dolt server: %w", err)
		}
		fmt.Printf("%s Dolt server stopped\n", style.Bold.Render("✓"))
	}

	// Perform the rollback
	fmt.Println("\nRestoring from backup...")
	result, err := doltserver.RestoreFromBackup(townRoot, backupPath)
	if err != nil {
		return fmt.Errorf("rollback failed: %w", err)
	}

	// Report results
	fmt.Println()
	if result.RestoredTown {
		fmt.Printf("  %s Restored town-level .beads\n", style.Bold.Render("✓"))
	}
	for _, rig := range result.RestoredRigs {
		fmt.Printf("  %s Restored %s/.beads\n", style.Bold.Render("✓"), rig)
	}
	for _, rig := range result.SkippedRigs {
		fmt.Printf("  %s Skipped %s (restore failed)\n", style.Dim.Render("⚠"), rig)
	}

	if len(result.MetadataReset) > 0 {
		fmt.Printf("\n  Metadata reset for: %s\n", strings.Join(result.MetadataReset, ", "))
	}

	// Validate restored state
	fmt.Println("\nValidating restored state...")
	validateCmd := exec.Command("bd", "list", "--limit", "5")
	validateCmd.Dir = townRoot
	output, validateErr := validateCmd.CombinedOutput()
	if validateErr != nil {
		fmt.Printf("  %s bd list returned an error: %v\n",
			style.Dim.Render("⚠"), validateErr)
		if len(output) > 0 {
			fmt.Printf("  %s\n", string(output))
		}
	} else {
		fmt.Printf("  %s bd list succeeded\n", style.Bold.Render("✓"))
		if len(output) > 0 {
			// Show first few lines of output
			lines := strings.Split(strings.TrimSpace(string(output)), "\n")
			for _, line := range lines {
				fmt.Printf("  %s\n", style.Dim.Render(line))
			}
		}
	}

	fmt.Printf("\n%s Rollback complete from %s\n", style.Bold.Render("✓"), backupPath)

	return nil
}

// printBackupContents shows what's in a backup directory for dry-run output.
func printBackupContents(backupPath, townRoot string) {
	// Check town-level backup
	townBackup := fmt.Sprintf("%s/town-beads", backupPath)
	if _, err := os.Stat(townBackup); err == nil {
		dst := fmt.Sprintf("%s/.beads", townRoot)
		fmt.Printf("  Would restore: %s\n", style.Dim.Render(dst))
		fmt.Printf("    From: %s\n", style.Dim.Render(townBackup))
	}

	// Check formula-style rig backups
	entries, err := os.ReadDir(backupPath)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == "town-beads" || name == "rigs" {
			continue
		}
		if strings.HasSuffix(name, "-beads") {
			rigName := strings.TrimSuffix(name, "-beads")
			dst := fmt.Sprintf("%s/%s/.beads", townRoot, rigName)
			src := fmt.Sprintf("%s/%s", backupPath, name)
			fmt.Printf("  Would restore: %s\n", style.Dim.Render(dst))
			fmt.Printf("    From: %s\n", style.Dim.Render(src))
		}
	}

	// Check test-backup-style rig backups
	rigsDir := fmt.Sprintf("%s/rigs", backupPath)
	if rigEntries, err := os.ReadDir(rigsDir); err == nil {
		for _, entry := range rigEntries {
			if !entry.IsDir() {
				continue
			}
			rigName := entry.Name()
			beadsDir := fmt.Sprintf("%s/%s/.beads", rigsDir, rigName)
			if _, err := os.Stat(beadsDir); err != nil {
				continue
			}
			dst := fmt.Sprintf("%s/%s/.beads", townRoot, rigName)
			fmt.Printf("  Would restore: %s\n", style.Dim.Render(dst))
			fmt.Printf("    From: %s\n", style.Dim.Render(beadsDir))
		}
	}
}

func runDoltSync(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	config := doltserver.DefaultConfig(townRoot)
	if config.IsRemote() {
		return fmt.Errorf("Dolt server is remote (%s) — sync requires local server access", config.HostPort())
	}

	// Validate --db flag if set
	if doltSyncDB != "" && !doltserver.DatabaseExists(townRoot, doltSyncDB) {
		return fmt.Errorf("database %q not found in .dolt-data/\nRun 'gt dolt list' to see available databases", doltSyncDB)
	}

	// Check server state
	wasRunning, _, _ := doltserver.IsRunning(townRoot)

	// GC phase: purge closed ephemeral beads (requires running server).
	purgeResults := make(map[string]struct {
		purged int
		err    error
	})
	if doltSyncGC {
		if !wasRunning {
			fmt.Fprintf(os.Stderr, "Warning: --gc requires a running Dolt server, skipping purge\n")
		} else {
			databases, listErr := doltserver.ListDatabases(townRoot)
			if listErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: --gc: could not list databases: %v\n", listErr)
			} else {
				for _, db := range databases {
					if doltSyncDB != "" && db != doltSyncDB {
						continue
					}
					purged, purgeErr := doltserver.PurgeClosedEphemerals(townRoot, db, doltSyncDry)
					purgeResults[db] = struct {
						purged int
						err    error
					}{purged, purgeErr}
				}
			}
		}
	}

	opts := doltserver.SyncOptions{
		Force:  doltSyncForce,
		DryRun: doltSyncDry,
		Filter: doltSyncDB,
	}

	// Use SQL push through the running server (no downtime).
	// Fall back to CLI push (with server stop/restart) only when server isn't running.
	var results []doltserver.SyncResult
	if wasRunning {
		fmt.Printf("Pushing via SQL (server stays running)...\n")
		results = doltserver.SyncDatabasesSQL(townRoot, opts)
	} else {
		fmt.Printf("Server not running — using CLI push...\n")
		results = doltserver.SyncDatabases(townRoot, opts)
	}

	if len(results) == 0 {
		fmt.Println("No databases to sync.")
		return nil
	}

	fmt.Printf("\nSyncing %d database(s)...\n", len(results))

	var pushed, skipped, failed, totalPurged int
	for _, r := range results {
		fmt.Println()
		// Show purge results if --gc was used
		if doltSyncGC {
			if pr, ok := purgeResults[r.Database]; ok {
				if pr.err != nil {
					fmt.Printf("  %s %s gc: %v\n", style.Bold.Render("!"), r.Database, pr.err)
				} else if pr.purged > 0 {
					verb := "purged"
					if doltSyncDry {
						verb = "would purge"
					}
					fmt.Printf("  %s %s gc: %s %d closed ephemeral bead(s)\n", style.Bold.Render("✓"), r.Database, verb, pr.purged)
					totalPurged += pr.purged
				}
			}
		}
		switch {
		case r.Pushed:
			fmt.Printf("  %s %s → origin main\n", style.Bold.Render("✓"), r.Database)
			fmt.Printf("    %s\n", style.Dim.Render(r.Remote))
			pushed++
		case r.DryRun:
			fmt.Printf("  %s %s → origin main (dry run)\n", style.Bold.Render("~"), r.Database)
			fmt.Printf("    %s\n", style.Dim.Render(r.Remote))
			pushed++ // count as would-push for summary
		case r.Skipped:
			fmt.Printf("  %s %s — no remote configured\n", style.Dim.Render("○"), r.Database)
			skipped++
		case r.Error != nil:
			fmt.Printf("  %s %s → origin main\n", style.Bold.Render("✗"), r.Database)
			fmt.Printf("    error: %v\n", r.Error)
			failed++
		}
	}

	summary := fmt.Sprintf("Summary: %d pushed, %d skipped, %d failed", pushed, skipped, failed)
	if doltSyncGC && totalPurged > 0 {
		if doltSyncDry {
			summary += fmt.Sprintf(", %d would be purged", totalPurged)
		} else {
			summary += fmt.Sprintf(", %d purged", totalPurged)
		}
	}
	fmt.Printf("\n%s\n", summary)

	if failed > 0 {
		return fmt.Errorf("%d database(s) failed to sync", failed)
	}
	return nil
}

func runDoltPull(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	config := doltserver.DefaultConfig(townRoot)
	if config.IsRemote() {
		return fmt.Errorf("Dolt server is remote (%s) — pull requires local server access", config.HostPort())
	}

	// Validate --db flag if set
	if doltPullDB != "" && !doltserver.DatabaseExists(townRoot, doltPullDB) {
		return fmt.Errorf("database %q not found in .dolt-data/\nRun 'gt dolt list' to see available databases", doltPullDB)
	}

	// Check server state
	wasRunning, _, _ := doltserver.IsRunning(townRoot)

	opts := doltserver.SyncOptions{
		DryRun: doltPullDry,
		Filter: doltPullDB,
	}

	// Use SQL pull through the running server (no lock contention).
	// Fall back to CLI pull only when server isn't running.
	var results []doltserver.SyncResult
	if wasRunning {
		fmt.Printf("Pulling via SQL (server stays running)...\n")
		results = doltserver.PullDatabasesSQL(townRoot, opts)
	} else {
		fmt.Printf("Server not running — using CLI pull...\n")
		results = doltserver.PullDatabases(townRoot, opts)
	}

	if len(results) == 0 {
		fmt.Println("No databases to pull.")
		return nil
	}

	fmt.Printf("\nPulling %d database(s)...\n", len(results))

	var pulled, skipped, failed int
	for _, r := range results {
		switch {
		case r.Pushed: // reused field = success
			fmt.Printf("  %s %s ← %s\n", style.Bold.Render("✓"), r.Database, r.Remote)
			pulled++
		case r.DryRun:
			fmt.Printf("  %s %s ← %s (dry run)\n", style.Bold.Render("~"), r.Database, r.Remote)
			pulled++
		case r.Skipped:
			fmt.Printf("  %s %s — no remote configured\n", style.Dim.Render("○"), r.Database)
			skipped++
		case r.Error != nil:
			fmt.Printf("  %s %s ← remote\n", style.Bold.Render("✗"), r.Database)
			fmt.Printf("    error: %v\n", r.Error)
			failed++
		}
	}

	fmt.Printf("\nSummary: %d pulled, %d skipped, %d failed\n", pulled, skipped, failed)

	if failed > 0 {
		return fmt.Errorf("%d database(s) failed to pull", failed)
	}
	return nil
}

func runDoltMigrateWisps(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Determine which rigs to migrate
	if doltMigrateWispsDB != "" {
		// Migrate a specific rig
		rigDir := filepath.Join(townRoot, doltMigrateWispsDB)
		if _, err := os.Stat(rigDir); os.IsNotExist(err) {
			return fmt.Errorf("rig directory not found: %s", rigDir)
		}
		fmt.Printf("%s Migrating: %s\n", style.Bold.Render("→"), doltMigrateWispsDB)
		result, err := doltserver.MigrateAgentBeadsToWisps(townRoot, rigDir, doltMigrateWispsDry)
		if err != nil {
			return err
		}
		printMigrateWispsResult(result)
		return nil
	}

	// Auto-detect: migrate all rigs that have beads databases
	databases, err := doltserver.ListDatabases(townRoot)
	if err != nil {
		return fmt.Errorf("listing databases: %w", err)
	}

	for _, db := range databases {
		// Skip non-rig databases
		if db == "wl_commons" || strings.HasPrefix(db, "testdb_") {
			continue
		}
		// Find the rig directory for this database.
		// The "hq" database lives at the town root itself, not townRoot/hq.
		rigDir := filepath.Join(townRoot, db)
		if db == "hq" {
			rigDir = townRoot
		} else if _, err := os.Stat(rigDir); os.IsNotExist(err) {
			continue // Not a rig directory
		}
		fmt.Printf("\n%s Migrating: %s\n", style.Bold.Render("→"), db)
		result, err := doltserver.MigrateAgentBeadsToWisps(townRoot, rigDir, doltMigrateWispsDry)
		if err != nil {
			fmt.Printf("  %s %s: %v\n", style.Bold.Render("✗"), db, err)
			continue
		}
		printMigrateWispsResult(result)
	}
	return nil
}

func printMigrateWispsResult(result *doltserver.MigrateWispsResult) {
	if result.WispsTableCreated {
		fmt.Printf("  %s Created wisps table\n", style.Bold.Render("✓"))
	}
	for _, t := range result.AuxTablesCreated {
		fmt.Printf("  %s Created %s\n", style.Bold.Render("✓"), t)
	}
	if result.AgentsCopied > 0 {
		fmt.Printf("  %s Copied %d agent beads to wisps\n", style.Bold.Render("✓"), result.AgentsCopied)
	}
	if result.LabelsCopied > 0 {
		fmt.Printf("  %s Copied %d labels\n", style.Bold.Render("✓"), result.LabelsCopied)
	}
	if result.CommentsCopied > 0 {
		fmt.Printf("  %s Copied %d comments\n", style.Bold.Render("✓"), result.CommentsCopied)
	}
	if result.EventsCopied > 0 {
		fmt.Printf("  %s Copied %d events\n", style.Bold.Render("✓"), result.EventsCopied)
	}
	if result.DepsCopied > 0 {
		fmt.Printf("  %s Copied %d dependencies\n", style.Bold.Render("✓"), result.DepsCopied)
	}
	if result.AgentsClosed > 0 {
		fmt.Printf("  %s Closed %d original agent beads\n", style.Bold.Render("✓"), result.AgentsClosed)
	}
	if result.AgentsCopied == 0 && len(result.AuxTablesCreated) == 0 && !result.WispsTableCreated {
		fmt.Printf("  %s Already migrated (no changes needed)\n", style.Bold.Render("✓"))
	}
}
