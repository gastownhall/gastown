package daemon

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jonbaldie/gastown/internal/util"
)

const (
	defaultDoltRemotesInterval = 15 * time.Minute
	doltPushTimeout            = 60 * time.Second
)

// doltRemotesInterval returns the configured push interval, or the default (15m).
func doltRemotesInterval(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.DoltRemotes != nil {
		if config.Patrols.DoltRemotes.Interval > 0 {
			return config.Patrols.DoltRemotes.Interval
		}
	}
	return defaultDoltRemotesInterval
}

// pushDoltRemotes commits and pushes each configured database to its remote.
// Non-fatal: errors are logged but don't stop the patrol.
func (d *Daemon) pushDoltRemotes() {
	if !d.isPatrolActive("dolt_remotes") {
		return
	}

	// Need dolt server to be configured for data dir
	if d.doltServer == nil || !d.doltServer.IsEnabled() {
		d.logger.Printf("dolt_remotes: dolt server not configured, skipping")
		return
	}

	dataDir := d.doltServer.config.DataDir
	if dataDir == "" {
		d.logger.Printf("dolt_remotes: no data dir configured, skipping")
		return
	}

	config := d.patrolConfig.Patrols.DoltRemotes
	remote := config.Remote
	branch := config.Branch
	if branch == "" {
		branch = "main"
	}

	// Get list of databases to push.
	// When a specific remote is configured, filter by it.
	// When no remote is configured, discover databases with any remote.
	databases := config.Databases
	if len(databases) == 0 {
		var err error
		if remote != "" {
			databases, err = d.discoverDatabasesWithRemotes(dataDir, remote)
		} else {
			databases, err = d.discoverDatabasesWithAnyRemote(dataDir)
		}
		if err != nil {
			d.logger.Printf("dolt_remotes: error discovering databases: %v", err)
			return
		}
	}

	if len(databases) == 0 {
		d.logger.Printf("dolt_remotes: no databases with remotes found")
		return
	}

	if remote != "" {
		d.logger.Printf("dolt_remotes: pushing %d database(s) to %s/%s", len(databases), remote, branch)
	} else {
		d.logger.Printf("dolt_remotes: pushing %d database(s) (auto-detected remotes)/%s", len(databases), branch)
	}

	pushed := 0
	for _, db := range databases {
		pushRemote := remote
		if pushRemote == "" {
			// Auto-detect the remote name for this database
			pushRemote = d.findDatabaseRemote(dataDir, db)
			if pushRemote == "" {
				d.logger.Printf("dolt_remotes: %s: no remote found, skipping", db)
				continue
			}
		}
		if err := d.pushDatabase(dataDir, db, pushRemote, branch); err != nil {
			d.logger.Printf("dolt_remotes: %s: push failed: %v", db, err)
		} else {
			pushed++
		}
	}

	d.logger.Printf("dolt_remotes: pushed %d/%d database(s)", pushed, len(databases))
}

// pushDatabase commits pending changes and pushes a single database to its remote.
func (d *Daemon) pushDatabase(dataDir, db, remote, branch string) error {
	// Safety: refuse to push anything that looks like a test database.
	// This is the last line of defense against pushing pollution to GitHub.
	for _, prefix := range []string{"test", "beads_t", "beads_pt", "doctest_"} {
		if strings.HasPrefix(db, prefix) {
			return fmt.Errorf("REFUSED: %q looks like a test database (prefix %q)", db, prefix)
		}
	}

	// Step 1: Stage any unstaged changes (non-fatal)
	addQuery := fmt.Sprintf("USE `%s`; CALL DOLT_ADD('-A')", db)
	if err := d.runDoltSQL(dataDir, addQuery); err != nil {
		// Ignore - may have nothing to stage
		d.logger.Printf("dolt_remotes: %s: add (non-fatal): %v", db, err)
	}

	// Step 2: Commit staged changes only if dolt_status shows pending work.
	// Skipping DOLT_COMMIT when nothing is staged avoids "nothing to commit"
	// warnings in dolt.log, which were causing log bloat at ~3/sec (gt-zb8).
	if d.hasStagedChanges(dataDir, db) {
		commitQuery := fmt.Sprintf(
			"USE `%s`; CALL DOLT_COMMIT('-m', 'daemon: auto-commit pending changes', '--author', 'Gas Town Daemon <daemon@gastown.local>')",
			db,
		)
		if err := d.runDoltSQL(dataDir, commitQuery); err != nil {
			d.logger.Printf("dolt_remotes: %s: commit (non-fatal): %v", db, err)
		}
	}

	// Step 3: Push to remote
	pushQuery := fmt.Sprintf("USE `%s`; CALL DOLT_PUSH('%s', '%s')", db, remote, branch)
	if err := d.runDoltSQL(dataDir, pushQuery); err != nil {
		return fmt.Errorf("push failed: %w", err)
	}

	d.logger.Printf("dolt_remotes: %s: pushed to %s/%s", db, remote, branch)
	return nil
}

// runDoltSQL executes a SQL query against the Dolt data directory.
func (d *Daemon) runDoltSQL(dataDir, query string) error {
	ctx, cancel := context.WithTimeout(context.Background(), doltPushTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "dolt", "sql", "-q", query)
	cmd.Dir = dataDir
	util.SetDetachedProcessGroup(cmd)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return fmt.Errorf("%s", errMsg)
		}
		return err
	}

	return nil
}

// hasStagedChanges returns true if the database has staged changes in dolt_status.
// Uses dolt_status WHERE staged=1. Fails open (returns true) on query errors so
// that a DOLT_COMMIT attempt is still made and the error is surfaced normally.
func (d *Daemon) hasStagedChanges(dataDir, db string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), doltPushTimeout)
	defer cancel()

	query := fmt.Sprintf("USE `%s`; SELECT COUNT(*) FROM dolt_status WHERE staged = 1", db)
	cmd := exec.CommandContext(ctx, "dolt", "sql", "-r", "csv", "-q", query)
	cmd.Dir = dataDir
	util.SetDetachedProcessGroup(cmd)

	output, err := cmd.Output()
	if err != nil {
		// Fail open: if we can't check, attempt the commit and let it fail naturally.
		return true
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) < 2 {
		return false
	}
	return strings.TrimSpace(lines[1]) != "0"
}

// discoverDatabasesWithRemotes lists databases in the data directory
// that have the specified remote configured.
func (d *Daemon) discoverDatabasesWithRemotes(dataDir, remote string) ([]string, error) {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return nil, fmt.Errorf("reading data dir: %w", err)
	}

	var databases []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Skip hidden directories
		if strings.HasPrefix(name, ".") {
			continue
		}
		// Check if this directory is a Dolt database (has .dolt subdirectory)
		doltDir := filepath.Join(dataDir, name, ".dolt")
		if _, err := os.Stat(doltDir); os.IsNotExist(err) {
			continue
		}
		// Check if it has the specified remote
		if d.databaseHasRemote(dataDir, name, remote) {
			databases = append(databases, name)
		}
	}

	return databases, nil
}

// escapeSQL escapes single quotes and backslashes for safe SQL string interpolation.
func escapeSQL(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, "'", "''")
}

// databaseHasRemote checks if a database has the specified remote configured.
func (d *Daemon) databaseHasRemote(dataDir, db, remote string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), doltCmdTimeout)
	defer cancel()

	query := fmt.Sprintf("USE `%s`; SELECT name FROM dolt_remotes WHERE name = '%s'", db, escapeSQL(remote))
	cmd := exec.CommandContext(ctx, "dolt", "sql", "-r", "csv", "-q", query)
	cmd.Dir = dataDir
	util.SetDetachedProcessGroup(cmd)

	output, err := cmd.Output()
	if err != nil {
		return false
	}

	// If we get more than just the header line, the remote exists
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	return len(lines) > 1
}

// databaseHasAnyRemote checks if a database has any remote configured.
func (d *Daemon) databaseHasAnyRemote(dataDir, db string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), doltCmdTimeout)
	defer cancel()

	query := fmt.Sprintf("USE `%s`; SELECT name FROM dolt_remotes LIMIT 1", db)
	cmd := exec.CommandContext(ctx, "dolt", "sql", "-r", "csv", "-q", query)
	cmd.Dir = dataDir
	util.SetDetachedProcessGroup(cmd)

	output, err := cmd.Output()
	if err != nil {
		return false
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	return len(lines) > 1
}

// findDatabaseRemote returns the name of the first remote configured for a database.
// Returns empty string if no remote is found.
func (d *Daemon) findDatabaseRemote(dataDir, db string) string {
	ctx, cancel := context.WithTimeout(context.Background(), doltCmdTimeout)
	defer cancel()

	query := fmt.Sprintf("USE `%s`; SELECT name FROM dolt_remotes LIMIT 1", db)
	cmd := exec.CommandContext(ctx, "dolt", "sql", "-r", "csv", "-q", query)
	cmd.Dir = dataDir
	util.SetDetachedProcessGroup(cmd)

	output, err := cmd.Output()
	if err != nil {
		return ""
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) < 2 {
		return ""
	}
	return strings.TrimSpace(lines[1])
}

// discoverDatabasesWithAnyRemote lists databases that have any remote configured.
func (d *Daemon) discoverDatabasesWithAnyRemote(dataDir string) ([]string, error) {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return nil, fmt.Errorf("reading data dir: %w", err)
	}

	var databases []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		doltDir := filepath.Join(dataDir, name, ".dolt")
		if _, err := os.Stat(doltDir); os.IsNotExist(err) {
			continue
		}
		if d.databaseHasAnyRemote(dataDir, name) {
			databases = append(databases, name)
		}
	}

	return databases, nil
}
