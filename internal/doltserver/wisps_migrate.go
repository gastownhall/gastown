// Package doltserver - wisps_migrate.go provides migration of agent beads to the wisps table.
//
// The wisps table is a dolt_ignored copy of the issues table schema, used for
// ephemeral operational data (agent beads, patrol wisps, etc.) that should not
// be version-controlled. This migration:
//  1. Creates the wisps table and auxiliary tables (wisp_labels, wisp_comments,
//     wisp_events, wisp_dependencies) if they don't exist
//  2. Copies existing agent beads (issue_type='agent') from issues to wisps
//  3. Copies associated labels, comments, events, and dependencies
//  4. Closes the originals in the issues table
//
// The migration uses `bd sql` for beads-side operations (copying agent beads between
// the issues and wisps tables in bd's own database). Additionally, it ensures that
// the wisps tables exist on the gt Dolt server (port 3307), since the reaper connects
// directly to that server. Without this, `bd sql` creates the tables on bd's Dolt
// instance (a separate process/port), and the reaper fails with "table not found".
package doltserver

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// MigrateWispsResult holds migration statistics.
type MigrateWispsResult struct {
	WispsTableCreated bool
	AuxTablesCreated  []string
	AgentsCopied      int
	AgentsClosed      int
	LabelsCopied      int
	CommentsCopied    int
	EventsCopied      int
	DepsCopied        int
}

// MigrateAgentBeadsToWisps creates the wisps table infrastructure and migrates
// existing agent beads from the issues table. Idempotent — safe to run multiple times.
//
// The workDir parameter should point to a directory where `bd` can find the correct
// beads database (typically the rig's .beads directory or a directory with a redirect).
func MigrateAgentBeadsToWisps(townRoot, workDir string, dryRun bool) (*MigrateWispsResult, error) {
	result := &MigrateWispsResult{}

	// Step 1: Ensure bd migrate has been run (sets up dolt_ignore entries)
	if err := bdExec(workDir, "migrate", "--yes"); err != nil {
		// Non-fatal: might already be up to date
		if !strings.Contains(err.Error(), "already") {
			fmt.Printf("  Note: bd migrate returned: %v\n", err)
		}
	}

	// Step 2: Create wisps table if it doesn't exist (in bd's database)
	created, err := ensureWispsTable(workDir)
	if err != nil {
		return nil, fmt.Errorf("creating wisps table: %w", err)
	}
	result.WispsTableCreated = created

	// Step 3: Create auxiliary tables (in bd's database)
	auxTables, err := ensureWispAuxTables(workDir)
	if err != nil {
		return nil, fmt.Errorf("creating auxiliary tables: %w", err)
	}
	result.AuxTablesCreated = auxTables
	if err := ensureWispDependencySchema(workDir); err != nil {
		return nil, fmt.Errorf("updating wisp dependency schema: %w", err)
	}

	// Step 3b: Ensure wisps tables also exist on the gt Dolt server.
	// The reaper connects directly to the gt Dolt server (port 3307), which is
	// a separate process from bd's Dolt instance. Without this step, bd creates
	// the tables on its own server but the reaper fails with "table not found".
	gtConfig := DefaultConfig(townRoot)
	dbName := deriveDBName(townRoot, workDir)
	if dbName != "" {
		gtCreated, gtAux, err := ensureWispsOnGTServer(gtConfig.Host, gtConfig.Port, dbName)
		if err != nil {
			fmt.Printf("  Note: gt Dolt server table creation failed: %v\n", err)
		} else {
			if gtCreated {
				result.WispsTableCreated = true
				fmt.Printf("  ✓ Created wisps table on gt Dolt server (%s:%d/%s)\n", gtConfig.Host, gtConfig.Port, dbName)
			}
			for _, t := range gtAux {
				result.AuxTablesCreated = append(result.AuxTablesCreated, t+" (gt server)")
				fmt.Printf("  ✓ Created %s on gt Dolt server\n", t)
			}
		}
	}

	if dryRun {
		cnt, _ := bdSQLCount(workDir, "SELECT COUNT(*) as cnt FROM issues WHERE issue_type = 'agent' AND status = 'open'")
		result.AgentsCopied = cnt
		return result, nil
	}

	// Step 4: Copy agent beads from issues to wisps
	if err := copyAgentBeadsToWisps(workDir, result); err != nil {
		return nil, fmt.Errorf("copying agent beads: %w", err)
	}

	// Step 5: Copy auxiliary data
	if err := copyAuxiliaryData(workDir, result); err != nil {
		return nil, fmt.Errorf("copying auxiliary data: %w", err)
	}

	// Step 6: Close originals in issues table
	if err := closeOriginalAgentBeads(workDir, result); err != nil {
		return nil, fmt.Errorf("closing originals: %w", err)
	}

	return result, nil
}

// bdSQL executes a SQL query via `bd sql`.
func bdSQL(workDir, query string) error {
	cmd := exec.Command("bd", "sql", query)
	cmd.Dir = workDir
	setProcessGroup(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("bd sql: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

// bdSQLCSV executes a SQL query via `bd sql --csv` and returns the output.
func bdSQLCSV(workDir, query string) (string, error) {
	cmd := exec.Command("bd", "sql", "--csv", query)
	cmd.Dir = workDir
	setProcessGroup(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("bd sql: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return string(output), nil
}

// bdExec executes a bd command.
func bdExec(workDir string, args ...string) error {
	cmd := exec.Command("bd", args...)
	cmd.Dir = workDir
	setProcessGroup(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("bd %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(string(output)), err)
	}
	return nil
}

// bdSQLCount executes a COUNT query and returns the integer result.
func bdSQLCount(workDir, query string) (int, error) {
	output, err := bdSQLCSV(workDir, query)
	if err != nil {
		return 0, err
	}
	// Parse CSV output: header\nvalue\n
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 2 {
		return 0, nil
	}
	cnt := 0
	fmt.Sscanf(strings.TrimSpace(lines[1]), "%d", &cnt)
	return cnt, nil
}

// bdTableExists checks if a table exists by attempting to query it.
func bdTableExists(workDir, tableName string) bool {
	err := bdSQL(workDir, fmt.Sprintf("SELECT 1 FROM `%s` LIMIT 1", tableName))
	return err == nil
}

// ensureWispsTable creates the wisps table with the core columns needed for agent beads.
func ensureWispsTable(workDir string) (bool, error) {
	if bdTableExists(workDir, "wisps") {
		return false, nil
	}

	// We use individual column definitions instead of CREATE TABLE LIKE because
	// LIKE can cause Dolt server crashes with dolt_ignored tables.
	if err := bdSQL(workDir, wispsCreateDDL); err != nil {
		return false, err
	}

	return true, nil
}

// ensureWispAuxTables creates auxiliary tables for wisps.
func ensureWispAuxTables(workDir string) ([]string, error) {
	var created []string

	for _, t := range wispAuxTableDDLs {
		if bdTableExists(workDir, t.name) {
			continue
		}
		if err := bdSQL(workDir, t.ddl); err != nil {
			return created, fmt.Errorf("creating %s: %w", t.name, err)
		}
		created = append(created, t.name)
	}

	return created, nil
}

func ensureWispDependencySchema(workDir string) error {
	if !bdTableExists(workDir, "wisp_dependencies") {
		return nil
	}
	columns, err := bdSQLCSV(workDir, "SHOW COLUMNS FROM wisp_dependencies")
	if err != nil {
		return err
	}
	if hasCSVColumn(columns, "depends_on_issue_id") && hasCSVColumn(columns, "depends_on_wisp_id") && hasCSVColumn(columns, "depends_on_external") {
		if !hasCSVColumn(columns, "depends_on_id") {
			return nil
		}
	}
	if !hasCSVColumn(columns, "depends_on_id") {
		return fmt.Errorf("wisp_dependencies missing split dependency columns")
	}
	return rebuildWispDependencyTable(workDir)
}

func rebuildWispDependencyTable(workDir string) error {
	if bdTableExists(workDir, "wisp_dependencies_legacy") {
		return fmt.Errorf("wisp_dependencies_legacy already exists; refusing to overwrite migration backup")
	}
	legacyCount, err := bdSQLCount(workDir, "SELECT COUNT(*) as cnt FROM wisp_dependencies")
	if err != nil {
		return err
	}
	if err := bdSQL(workDir, "RENAME TABLE wisp_dependencies TO wisp_dependencies_legacy"); err != nil {
		return err
	}
	if err := bdSQL(workDir, wispDependenciesCreateDDL); err != nil {
		return restoreWispDependencyBackup(workDir, err)
	}
	if err := bdSQL(workDir, `INSERT IGNORE INTO wisp_dependencies (issue_id, type, created_at, created_by, metadata, thread_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external)
SELECT wd.issue_id, wd.type, wd.created_at, wd.created_by, wd.metadata, wd.thread_id,
       CASE WHEN w.id IS NULL AND i.id IS NOT NULL THEN wd.depends_on_id ELSE NULL END,
       CASE WHEN w.id IS NOT NULL THEN wd.depends_on_id ELSE NULL END,
       CASE WHEN w.id IS NULL AND i.id IS NULL THEN wd.depends_on_id ELSE NULL END
FROM wisp_dependencies_legacy wd
LEFT JOIN wisps w ON w.id = wd.depends_on_id
LEFT JOIN issues i ON i.id = wd.depends_on_id`); err != nil {
		return restoreWispDependencyBackup(workDir, err)
	}
	migratedCount, err := bdSQLCount(workDir, "SELECT COUNT(*) as cnt FROM wisp_dependencies")
	if err != nil {
		return restoreWispDependencyBackup(workDir, err)
	}
	if migratedCount != legacyCount {
		return restoreWispDependencyBackup(workDir, fmt.Errorf("wisp_dependencies migration copied %d of %d rows", migratedCount, legacyCount))
	}
	return bdSQL(workDir, "DROP TABLE wisp_dependencies_legacy")
}

func restoreWispDependencyBackup(workDir string, cause error) error {
	if err := bdSQL(workDir, "DROP TABLE IF EXISTS wisp_dependencies"); err != nil {
		return fmt.Errorf("%w; restore failed dropping partial wisp_dependencies: %v", cause, err)
	}
	if err := bdSQL(workDir, "RENAME TABLE wisp_dependencies_legacy TO wisp_dependencies"); err != nil {
		return fmt.Errorf("%w; restore failed renaming wisp_dependencies_legacy: %v", cause, err)
	}
	return cause
}

func hasCSVColumn(csvOutput, column string) bool {
	for _, line := range strings.Split(csvOutput, "\n") {
		fields := strings.SplitN(line, ",", 2)
		if len(fields) > 0 && strings.Trim(fields[0], `"`) == column {
			return true
		}
	}
	return false
}

// copyAgentBeadsToWisps inserts agent beads from issues into wisps, skipping duplicates.
func copyAgentBeadsToWisps(workDir string, result *MigrateWispsResult) error {
	// INSERT IGNORE skips rows where the primary key already exists in wisps.
	// We use explicit column list to handle any schema differences.
	err := bdSQL(workDir,
		"INSERT IGNORE INTO wisps (id, title, description, status, issue_type, agent_state, role_type, rig, hook_bead, role_bead, created_at, updated_at, created_by, owner, assignee, priority, ephemeral, wisp_type, mol_type, metadata) "+
			"SELECT id, title, description, status, issue_type, agent_state, role_type, rig, hook_bead, role_bead, created_at, updated_at, created_by, owner, assignee, priority, 1, wisp_type, mol_type, metadata FROM issues WHERE issue_type = 'agent'")
	if err != nil {
		return err
	}

	cnt, _ := bdSQLCount(workDir, "SELECT COUNT(*) as cnt FROM wisps WHERE issue_type = 'agent'")
	result.AgentsCopied = cnt

	return nil
}

// copyAuxiliaryData copies labels, comments, events, and dependencies for agent beads.
func copyAuxiliaryData(workDir string, result *MigrateWispsResult) error {
	// Copy labels
	if err := bdSQL(workDir,
		"INSERT IGNORE INTO wisp_labels (issue_id, label) SELECT l.issue_id, l.label FROM labels l INNER JOIN wisps w ON l.issue_id = w.id"); err != nil {
		// Non-fatal if no matching labels
		if !strings.Contains(err.Error(), "nothing") {
			return fmt.Errorf("copying labels: %w", err)
		}
	}
	cnt, _ := bdSQLCount(workDir, "SELECT COUNT(*) as cnt FROM wisp_labels")
	result.LabelsCopied = cnt

	// Copy comments
	if err := bdSQL(workDir,
		"INSERT IGNORE INTO wisp_comments (issue_id, author, text, created_at) SELECT c.issue_id, c.author, c.text, c.created_at FROM comments c INNER JOIN wisps w ON c.issue_id = w.id"); err != nil {
		if !strings.Contains(err.Error(), "nothing") {
			return fmt.Errorf("copying comments: %w", err)
		}
	}
	cnt, _ = bdSQLCount(workDir, "SELECT COUNT(*) as cnt FROM wisp_comments")
	result.CommentsCopied = cnt

	// Copy events
	if err := bdSQL(workDir,
		"INSERT IGNORE INTO wisp_events (issue_id, event_type, actor, old_value, new_value, comment, created_at) SELECT e.issue_id, e.event_type, e.actor, e.old_value, e.new_value, e.comment, e.created_at FROM events e INNER JOIN wisps w ON e.issue_id = w.id"); err != nil {
		if !strings.Contains(err.Error(), "nothing") {
			return fmt.Errorf("copying events: %w", err)
		}
	}
	cnt, _ = bdSQLCount(workDir, "SELECT COUNT(*) as cnt FROM wisp_events")
	result.EventsCopied = cnt

	// Copy dependencies
	if err := bdSQL(workDir,
		"INSERT IGNORE INTO wisp_dependencies (issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_at, created_by, metadata, thread_id) SELECT d.issue_id, CASE WHEN target_wisp.id IS NULL THEN d.depends_on_issue_id ELSE NULL END, CASE WHEN target_wisp.id IS NOT NULL THEN d.depends_on_issue_id ELSE d.depends_on_wisp_id END, d.depends_on_external, d.type, d.created_at, d.created_by, d.metadata, d.thread_id FROM dependencies d INNER JOIN wisps w ON d.issue_id = w.id LEFT JOIN wisps target_wisp ON target_wisp.id = d.depends_on_issue_id"); err != nil {
		if !strings.Contains(err.Error(), "nothing") {
			return fmt.Errorf("copying dependencies: %w", err)
		}
	}
	if err := bdSQL(workDir,
		"UPDATE wisp_dependencies wd INNER JOIN wisps target_wisp ON target_wisp.id = wd.depends_on_issue_id SET wd.depends_on_wisp_id = wd.depends_on_issue_id, wd.depends_on_issue_id = NULL"); err != nil {
		if !strings.Contains(err.Error(), "nothing") {
			return fmt.Errorf("retargeting wisp dependency targets: %w", err)
		}
	}
	if err := bdSQL(workDir,
		"UPDATE dependencies d INNER JOIN wisps target_wisp ON target_wisp.id = d.depends_on_issue_id SET d.depends_on_wisp_id = d.depends_on_issue_id, d.depends_on_issue_id = NULL"); err != nil {
		if !strings.Contains(err.Error(), "nothing") {
			return fmt.Errorf("retargeting dependency targets: %w", err)
		}
	}
	cnt, _ = bdSQLCount(workDir, "SELECT COUNT(*) as cnt FROM wisp_dependencies")
	result.DepsCopied = cnt

	return nil
}

// deriveDBName extracts the database name from the workDir relative to townRoot.
// For the town root itself, returns "hq". For rig directories, returns the rig name.
func deriveDBName(townRoot, workDir string) string {
	// Normalize paths
	if townRoot == workDir {
		return "hq"
	}
	// workDir is typically townRoot/<rigname>
	rel := strings.TrimPrefix(workDir, townRoot)
	rel = strings.TrimPrefix(rel, "/")
	rel = strings.TrimSuffix(rel, "/")
	// Take just the first path component (the rig name)
	if idx := strings.Index(rel, "/"); idx >= 0 {
		rel = rel[:idx]
	}
	if rel == "" {
		return ""
	}
	return rel
}

// ensureWispsOnGTServer creates the wisps and auxiliary tables directly on the
// gt Dolt server via MySQL protocol. This is needed because the reaper connects
// to this server, not to bd's separate Dolt instance.
func ensureWispsOnGTServer(host string, port int, dbName string) (wispsCreated bool, auxCreated []string, err error) {
	if host == "" {
		host = "127.0.0.1"
	}
	dsn := fmt.Sprintf("root@tcp(%s:%d)/%s?parseTime=true&timeout=10s", host, port, dbName)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return false, nil, fmt.Errorf("connect to gt Dolt server: %w", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Check if wisps table exists
	if !gtTableExists(ctx, db, dbName, "wisps") {
		if _, err := db.ExecContext(ctx, wispsCreateDDL); err != nil {
			return false, nil, fmt.Errorf("create wisps: %w", err)
		}
		wispsCreated = true
	}

	// Create auxiliary tables
	for _, t := range wispAuxTableDDLs {
		if gtTableExists(ctx, db, dbName, t.name) {
			continue
		}
		if _, err := db.ExecContext(ctx, t.ddl); err != nil {
			return wispsCreated, auxCreated, fmt.Errorf("create %s: %w", t.name, err)
		}
		auxCreated = append(auxCreated, t.name)
	}

	// Commit the DDL changes to Dolt history
	if wispsCreated || len(auxCreated) > 0 {
		commitMsg := fmt.Sprintf("migrate-wisps: create wisps tables in %s", dbName)
		if _, err := db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_COMMIT('-Am', '%s')", commitMsg)); err != nil {
			// Non-fatal: tables are created in working set even without commit
			fmt.Printf("  Note: dolt commit after table creation: %v\n", err)
		}
	}
	if err := ensureWispDependencySchemaOnGT(ctx, db, dbName); err != nil {
		return wispsCreated, auxCreated, err
	}

	return wispsCreated, auxCreated, nil
}

// gtTableExists checks if a table exists on the gt Dolt server.
func gtTableExists(ctx context.Context, db *sql.DB, dbName, tableName string) bool {
	var dummy int
	// Use information_schema for a safe, parameterized check.
	err := db.QueryRowContext(ctx,
		"SELECT 1 FROM information_schema.tables WHERE table_schema = ? AND table_name = ?",
		dbName, tableName).Scan(&dummy)
	return err == nil
}

func gtWispDependencyColumnExists(ctx context.Context, db *sql.DB, dbName, columnName string) bool {
	var dummy int
	err := db.QueryRowContext(ctx,
		"SELECT 1 FROM information_schema.columns WHERE table_schema = ? AND table_name = ? AND column_name = ?",
		dbName, "wisp_dependencies", columnName).Scan(&dummy)
	return err == nil
}

func ensureWispDependencySchemaOnGT(ctx context.Context, db *sql.DB, dbName string) error {
	if !gtTableExists(ctx, db, dbName, "wisp_dependencies") {
		return nil
	}
	if gtWispDependencyColumnExists(ctx, db, dbName, "depends_on_issue_id") &&
		gtWispDependencyColumnExists(ctx, db, dbName, "depends_on_wisp_id") &&
		gtWispDependencyColumnExists(ctx, db, dbName, "depends_on_external") {
		if !gtWispDependencyColumnExists(ctx, db, dbName, "depends_on_id") {
			return nil
		}
	}
	if !gtWispDependencyColumnExists(ctx, db, dbName, "depends_on_id") {
		return fmt.Errorf("wisp_dependencies missing split dependency columns")
	}
	return rebuildWispDependencyTableOnGT(ctx, db, dbName)
}

func rebuildWispDependencyTableOnGT(ctx context.Context, db *sql.DB, dbName string) error {
	if gtTableExists(ctx, db, dbName, "wisp_dependencies_legacy") {
		return fmt.Errorf("wisp_dependencies_legacy already exists; refusing to overwrite migration backup")
	}
	var legacyCount int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM wisp_dependencies").Scan(&legacyCount); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, "RENAME TABLE wisp_dependencies TO wisp_dependencies_legacy"); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, wispDependenciesCreateDDL); err != nil {
		return restoreWispDependencyBackupOnGT(ctx, db, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT IGNORE INTO wisp_dependencies (issue_id, type, created_at, created_by, metadata, thread_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external)
SELECT wd.issue_id, wd.type, wd.created_at, wd.created_by, wd.metadata, wd.thread_id,
       CASE WHEN w.id IS NULL AND i.id IS NOT NULL THEN wd.depends_on_id ELSE NULL END,
       CASE WHEN w.id IS NOT NULL THEN wd.depends_on_id ELSE NULL END,
       CASE WHEN w.id IS NULL AND i.id IS NULL THEN wd.depends_on_id ELSE NULL END
FROM wisp_dependencies_legacy wd
LEFT JOIN wisps w ON w.id = wd.depends_on_id
LEFT JOIN issues i ON i.id = wd.depends_on_id`); err != nil {
		return restoreWispDependencyBackupOnGT(ctx, db, err)
	}
	var migratedCount int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM wisp_dependencies").Scan(&migratedCount); err != nil {
		return restoreWispDependencyBackupOnGT(ctx, db, err)
	}
	if migratedCount != legacyCount {
		return restoreWispDependencyBackupOnGT(ctx, db, fmt.Errorf("wisp_dependencies migration copied %d of %d rows", migratedCount, legacyCount))
	}
	_, err := db.ExecContext(ctx, "DROP TABLE wisp_dependencies_legacy")
	return err
}

func restoreWispDependencyBackupOnGT(ctx context.Context, db *sql.DB, cause error) error {
	if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS wisp_dependencies"); err != nil {
		return fmt.Errorf("%w; restore failed dropping partial wisp_dependencies: %v", cause, err)
	}
	if _, err := db.ExecContext(ctx, "RENAME TABLE wisp_dependencies_legacy TO wisp_dependencies"); err != nil {
		return fmt.Errorf("%w; restore failed renaming wisp_dependencies_legacy: %v", cause, err)
	}
	return cause
}

// wispsCreateDDL is the CREATE TABLE statement for the wisps table.
var wispsCreateDDL = `CREATE TABLE wisps (
  id varchar(255) NOT NULL,
  content_hash varchar(64),
  title varchar(500) NOT NULL,
  description text NOT NULL,
  design text NOT NULL DEFAULT '',
  acceptance_criteria text NOT NULL DEFAULT '',
  notes text NOT NULL DEFAULT '',
  status varchar(32) NOT NULL DEFAULT 'open',
  priority int NOT NULL DEFAULT 2,
  issue_type varchar(32) NOT NULL DEFAULT 'task',
  assignee varchar(255),
  estimated_minutes int,
  created_at datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  created_by varchar(255) DEFAULT '',
  owner varchar(255) DEFAULT '',
  updated_at datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  closed_at datetime,
  closed_by_session varchar(255) DEFAULT '',
  external_ref varchar(255),
  compaction_level int DEFAULT 0,
  compacted_at datetime,
  compacted_at_commit varchar(64),
  original_size int,
  deleted_at datetime,
  deleted_by varchar(255) DEFAULT '',
  delete_reason text DEFAULT '',
  original_type varchar(32) DEFAULT '',
  sender varchar(255) DEFAULT '',
  ephemeral tinyint(1) DEFAULT 1,
  pinned tinyint(1) DEFAULT 0,
  is_template tinyint(1) DEFAULT 0,
  crystallizes tinyint(1) DEFAULT 0,
  mol_type varchar(32) DEFAULT '',
  work_type varchar(32) DEFAULT 'mutex',
  quality_score double,
  source_system varchar(255) DEFAULT '',
  metadata json,
  source_repo varchar(512) DEFAULT '',
  close_reason text DEFAULT '',
  event_kind varchar(32) DEFAULT '',
  actor varchar(255) DEFAULT '',
  target varchar(255) DEFAULT '',
  payload text DEFAULT '',
  await_type varchar(32) DEFAULT '',
  await_id varchar(255) DEFAULT '',
  timeout_ns bigint DEFAULT 0,
  waiters text DEFAULT '',
  hook_bead varchar(255) DEFAULT '',
  role_bead varchar(255) DEFAULT '',
  agent_state varchar(32) DEFAULT '',
  last_activity datetime,
  role_type varchar(32) DEFAULT '',
  rig varchar(255) DEFAULT '',
  due_at datetime,
  defer_until datetime,
  wisp_type varchar(32) DEFAULT NULL,
  spec_id text,
  PRIMARY KEY (id),
  KEY idx_wisps_status (status),
  KEY idx_wisps_issue_type (issue_type)
)`

// wispAuxTableDDL holds the name and DDL for an auxiliary wisp table.
type wispAuxTableDDL struct {
	name string
	ddl  string
}

const wispDependenciesCreateDDL = `CREATE TABLE wisp_dependencies (
  id char(36) NOT NULL DEFAULT (uuid()),
  issue_id varchar(255) NOT NULL,
  type varchar(32) NOT NULL DEFAULT 'blocks',
  created_at datetime DEFAULT CURRENT_TIMESTAMP,
  created_by varchar(255) DEFAULT '',
  metadata json DEFAULT (json_object()),
  thread_id varchar(255) DEFAULT '',
  depends_on_issue_id varchar(255),
  depends_on_wisp_id varchar(255),
  depends_on_external varchar(255),
  PRIMARY KEY (id),
  KEY idx_wisp_dep_external_target (depends_on_external),
  KEY idx_wisp_dep_issue_target (depends_on_issue_id),
  KEY idx_wisp_dep_type (type),
  KEY idx_wisp_dep_type_external (type, depends_on_external),
  KEY idx_wisp_dep_type_issue (type, depends_on_issue_id),
  KEY idx_wisp_dep_type_wisp (type, depends_on_wisp_id),
  KEY idx_wisp_dep_wisp_target (depends_on_wisp_id),
  UNIQUE KEY uk_wisp_dep_external_target (issue_id, depends_on_external),
  UNIQUE KEY uk_wisp_dep_issue_target (issue_id, depends_on_issue_id),
  UNIQUE KEY uk_wisp_dep_wisp_target (issue_id, depends_on_wisp_id),
  CONSTRAINT fk_wisp_dep_issue FOREIGN KEY (issue_id) REFERENCES wisps (id) ON DELETE CASCADE ON UPDATE CASCADE,
  CONSTRAINT fk_wisp_dep_issue_target FOREIGN KEY (depends_on_issue_id) REFERENCES issues (id) ON DELETE CASCADE ON UPDATE CASCADE,
  CONSTRAINT fk_wisp_dep_wisp_target FOREIGN KEY (depends_on_wisp_id) REFERENCES wisps (id) ON DELETE CASCADE ON UPDATE CASCADE,
  CONSTRAINT ck_wisp_dep_one_target CHECK (((NOT(depends_on_issue_id IS NULL)) + (NOT(depends_on_wisp_id IS NULL)) + (NOT(depends_on_external IS NULL))) = 1)
)`

// wispAuxTableDDLs are the auxiliary tables needed alongside wisps.
var wispAuxTableDDLs = []wispAuxTableDDL{
	{
		name: "wisp_labels",
		ddl: `CREATE TABLE wisp_labels (
  issue_id varchar(255) NOT NULL,
  label varchar(255) NOT NULL,
  PRIMARY KEY (issue_id, label),
  KEY idx_wisp_labels_label (label)
)`,
	},
	{
		name: "wisp_comments",
		ddl: `CREATE TABLE wisp_comments (
  id bigint NOT NULL AUTO_INCREMENT,
  issue_id varchar(255) NOT NULL,
  author varchar(255) NOT NULL,
  text text NOT NULL,
  created_at datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_wisp_comments_issue (issue_id)
)`,
	},
	{
		name: "wisp_events",
		ddl: `CREATE TABLE wisp_events (
  id bigint NOT NULL AUTO_INCREMENT,
  issue_id varchar(255) NOT NULL,
  event_type varchar(32) NOT NULL,
  actor varchar(255) NOT NULL,
  old_value text,
  new_value text,
  comment text,
  created_at datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_wisp_events_issue (issue_id)
)`,
	},
	{
		name: "wisp_dependencies",
		ddl:  wispDependenciesCreateDDL,
	},
}

// closeOriginalAgentBeads closes the original agent beads in the issues table.
func closeOriginalAgentBeads(workDir string, result *MigrateWispsResult) error {
	// Close all open agent beads. We don't use a cross-table subquery because
	// that can crash the Dolt server when mixing regular and dolt_ignored tables.
	if err := bdSQL(workDir,
		"UPDATE issues SET status = 'closed', closed_at = NOW() WHERE issue_type = 'agent' AND status = 'open'"); err != nil {
		return err
	}

	cnt, _ := bdSQLCount(workDir, "SELECT COUNT(*) as cnt FROM issues WHERE issue_type = 'agent' AND status = 'closed'")
	result.AgentsClosed = cnt

	return nil
}
