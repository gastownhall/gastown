// Package main implements the dolt-snapshots plugin binary.
// It creates immutable tags (and optional branches) on Dolt databases
// at convoy lifecycle boundaries for audit, diff, and rollback.
//
// Fixes from PR #2324 review:
//   - Parameterized SQL (no shell interpolation / SQL injection)
//   - No subshell counter bugs (Go, not bash pipelines)
//   - No auto-commit of dirty state (tags HEAD as-is)
package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

var (
	safeNameRe = regexp.MustCompile(`[^a-zA-Z0-9-]`)
	multiDash  = regexp.MustCompile(`-{2,}`)
)

// route represents a single entry from routes.jsonl.
type route struct {
	Prefix string `json:"prefix"`
	Path   string `json:"path"`
}

// convoyRow holds query results for convoys needing snapshots.
type convoyRow struct {
	ID           string
	Title        string
	Status       string
	HasOpenTag   bool
	HasStagedTag bool
}

func main() {
	host := flag.String("host", "", "Dolt server host (default: 127.0.0.1)")
	port := flag.String("port", "", "Dolt server port (default: GT_DOLT_PORT or 3307)")
	routesFile := flag.String("routes", "", "Path to routes.jsonl (default: ~/gt/.beads/routes.jsonl)")
	dryRun := flag.Bool("dry-run", false, "Show what would be done without making changes")
	cleanup := flag.Bool("cleanup", false, "Also escalate stale convoy branches for review")
	watch := flag.Bool("watch", false, "Watch events.jsonl and snapshot immediately on convoy events")
	flag.Parse()

	// Resolve defaults
	h := resolveHost(*host)
	p := resolvePort(*port)
	rf := resolveRoutesFile(*routesFile)

	if *watch {
		if err := watchEvents(h, p, rf, *cleanup); err != nil {
			log.Fatalf("Watch failed: %v", err)
		}
		return
	}

	dsn := fmt.Sprintf("root@tcp(%s:%s)/information_schema?parseTime=true&timeout=5s&readTimeout=30s&writeTimeout=30s", h, p)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		log.Fatalf("Failed to connect to Dolt: %v", err)
	}
	defer db.Close()

	db.SetMaxOpenConns(2)
	db.SetConnMaxLifetime(time.Minute)

	if err := db.Ping(); err != nil {
		log.Fatalf("Dolt server unreachable at %s:%s: %v", h, p, err)
	}

	// Discover databases
	databases, err := listDatabases(db)
	if err != nil {
		log.Fatalf("Failed to list databases: %v", err)
	}

	routes := loadRoutes(rf)

	// Run snapshot cycle
	stats, err := snapshotConvoys(db, databases, routes, *dryRun)
	if err != nil {
		log.Fatalf("Snapshot cycle failed: %v", err)
	}

	if *cleanup {
		escalateStale(db, databases, *dryRun)
	}

	fmt.Printf("=== Dolt Snapshots Complete ===\n")
	fmt.Printf("Tags created: %d\n", stats.tagsCreated)
	fmt.Printf("Branches created: %d\n", stats.branchesCreated)
	fmt.Printf("Tags failed: %d\n", stats.tagsFailed)
	if *dryRun {
		fmt.Printf("(dry-run — no changes made)\n")
	}
}

func resolveHost(flag string) string {
	if flag != "" {
		return flag
	}
	if h := os.Getenv("DOLT_HOST"); h != "" {
		return h
	}
	return "127.0.0.1"
}

func resolvePort(flag string) string {
	if flag != "" {
		return flag
	}
	if p := os.Getenv("GT_DOLT_PORT"); p != "" {
		return p
	}
	if p := os.Getenv("DOLT_PORT"); p != "" {
		return p
	}
	return "3307"
}

func resolveRoutesFile(flag string) string {
	if flag != "" {
		return flag
	}
	if rf := os.Getenv("ROUTES_FILE"); rf != "" {
		return rf
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "gt", ".beads", "routes.jsonl")
}

// listDatabases returns all non-system databases on the Dolt server.
func listDatabases(db *sql.DB) ([]string, error) {
	rows, err := db.Query("SHOW DATABASES")
	if err != nil {
		return nil, fmt.Errorf("SHOW DATABASES: %w", err)
	}
	defer rows.Close()

	var databases []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		// Skip system databases and test pollution
		if isSystemDB(name) {
			continue
		}
		databases = append(databases, name)
	}
	return databases, rows.Err()
}

func isSystemDB(name string) bool {
	switch name {
	case "information_schema", "mysql", "dolt_cluster":
		return true
	}
	for _, prefix := range []string{"testdb_", "beads_t", "beads_pt", "doctest_"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// loadRoutes parses routes.jsonl and returns prefix → database name mapping.
func loadRoutes(path string) map[string]string {
	result := make(map[string]string)

	f, err := os.Open(path)
	if err != nil {
		log.Printf("Warning: cannot read routes file %s: %v", path, err)
		return result
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var r route
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue
		}
		prefix := strings.TrimRight(r.Prefix, "-")
		if prefix == "" || r.Path == "" || r.Path == "." {
			continue
		}
		// Extract database name from path (first component)
		dbName := r.Path
		if idx := strings.Index(r.Path, "/"); idx > 0 {
			dbName = r.Path[:idx]
		}
		result[prefix] = dbName
	}
	return result
}

// sanitizeName creates a safe tag/branch name from a convoy title and ID.
func sanitizeName(title, id string) string {
	slug := strings.ToLower(title)
	slug = safeNameRe.ReplaceAllString(slug, "-")
	slug = multiDash.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")

	// Truncate to keep total name reasonable
	const maxSlug = 50
	if len(slug) > maxSlug {
		slug = slug[:maxSlug]
		slug = strings.TrimRight(slug, "-")
	}

	if slug == "" {
		return id
	}
	return slug + "-" + id
}

type snapshotStats struct {
	tagsCreated     int
	branchesCreated int
	tagsFailed      int
}

// snapshotConvoys finds convoys needing snapshots and creates tags/branches.
func snapshotConvoys(db *sql.DB, databases []string, routes map[string]string, dryRun bool) (snapshotStats, error) {
	var stats snapshotStats

	// Query HQ for convoys needing snapshots.
	// Uses parameterized queries — no string interpolation.
	convoys, err := findConvoysNeedingSnapshots(db)
	if err != nil {
		return stats, fmt.Errorf("finding convoys: %w", err)
	}

	if len(convoys) == 0 {
		fmt.Println("No convoys need snapshots.")
		return stats, nil
	}

	fmt.Printf("Found %d convoy(s) needing snapshots.\n", len(convoys))

	for _, c := range convoys {
		safeName := sanitizeName(c.Title, c.ID)
		fmt.Printf("\n--- Convoy: %s (%s) [%s] ---\n", c.Title, c.ID, c.Status)

		// Discover which rig databases this convoy touches
		rigDBs, err := discoverConvoyDatabases(db, c.ID, databases, routes)
		if err != nil {
			log.Printf("Warning: cannot discover databases for convoy %s: %v", c.ID, err)
			continue
		}

		if len(rigDBs) == 0 {
			fmt.Printf("  No rig databases found for convoy %s, skipping.\n", c.ID)
			continue
		}

		// Always tag HQ too, dedup
		dbSet := make(map[string]bool)
		dbSet["hq"] = true
		for _, d := range rigDBs {
			dbSet[d] = true
		}
		var allDBs []string
		for d := range dbSet {
			allDBs = append(allDBs, d)
		}

		// Create open/ tags for convoys that need them
		if !c.HasOpenTag {
			tagName := "open/" + safeName
			for _, dbName := range allDBs {
				msg := fmt.Sprintf("Pre-work baseline for convoy %s", c.ID)
				if dryRun {
					fmt.Printf("  [dry-run] Would create tag %s on %s\n", tagName, dbName)
				} else {
					if err := createTag(db, dbName, tagName, msg); err != nil {
						log.Printf("  Failed to create tag %s on %s: %v", tagName, dbName, err)
						stats.tagsFailed++
					} else {
						fmt.Printf("  Created tag %s on %s\n", tagName, dbName)
						stats.tagsCreated++
					}
				}
			}
		}

		// Create staged/ tags + branches for staged/launched/closed convoys
		if !c.HasStagedTag && c.Status != "open" {
			tagName := "staged/" + safeName
			branchName := "convoy/" + safeName
			for _, dbName := range allDBs {
				msg := fmt.Sprintf("Launch baseline for convoy %s", c.ID)
				if dryRun {
					fmt.Printf("  [dry-run] Would create tag %s on %s\n", tagName, dbName)
					fmt.Printf("  [dry-run] Would create branch %s on %s\n", branchName, dbName)
				} else {
					if err := createTag(db, dbName, tagName, msg); err != nil {
						log.Printf("  Failed to create tag %s on %s: %v", tagName, dbName, err)
						stats.tagsFailed++
					} else {
						fmt.Printf("  Created tag %s on %s\n", tagName, dbName)
						stats.tagsCreated++
					}

					if err := createBranch(db, dbName, branchName); err != nil {
						log.Printf("  Failed to create branch %s on %s: %v", branchName, dbName, err)
					} else {
						fmt.Printf("  Created branch %s on %s\n", branchName, dbName)
						stats.branchesCreated++
					}
				}
			}
		}
	}

	return stats, nil
}

// findConvoysNeedingSnapshots queries HQ for convoys that need tags.
func findConvoysNeedingSnapshots(db *sql.DB) ([]convoyRow, error) {
	query := `
		SELECT i.id, i.title, i.status,
			CASE WHEN EXISTS (SELECT 1 FROM hq.dolt_tags t WHERE t.tag_name LIKE CONCAT('open/%-', i.id))
				 THEN 1 ELSE 0 END AS has_open_tag,
			CASE WHEN EXISTS (SELECT 1 FROM hq.dolt_tags t WHERE t.tag_name LIKE CONCAT('staged/%-', i.id))
				 THEN 1 ELSE 0 END AS has_staged_tag
		FROM hq.issues i
		WHERE (i.issue_type = 'convoy' OR EXISTS (
				SELECT 1 FROM hq.labels l
				WHERE l.issue_id = i.id AND l.label = 'gt:convoy'
			))
			AND (
				i.status IN ('staged_ready', 'staged_warnings', 'launched', 'open')
				OR (i.status = 'closed' AND i.updated_at >= NOW() - INTERVAL 24 HOUR)
			)
			AND EXISTS (
				SELECT 1 FROM hq.dependencies d
				WHERE d.issue_id = i.id AND d.type = 'tracks'
			)
		HAVING has_open_tag = 0 OR has_staged_tag = 0
	`

	rows, err := db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("convoy query: %w", err)
	}
	defer rows.Close()

	var convoys []convoyRow
	for rows.Next() {
		var c convoyRow
		var hasOpen, hasStaged int
		if err := rows.Scan(&c.ID, &c.Title, &c.Status, &hasOpen, &hasStaged); err != nil {
			return nil, err
		}
		c.HasOpenTag = hasOpen == 1
		c.HasStagedTag = hasStaged == 1
		convoys = append(convoys, c)
	}
	return convoys, rows.Err()
}

// discoverConvoyDatabases finds which rig databases a convoy touches
// by looking at its tracked issues' prefixes.
func discoverConvoyDatabases(db *sql.DB, convoyID string, databases []string, routes map[string]string) ([]string, error) {
	rows, err := db.Query(convoyDependencyTargetsQuery(), convoyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	dbSet := make(map[string]bool)
	for rows.Next() {
		var depID string
		if err := rows.Scan(&depID); err != nil {
			return nil, err
		}

		dbName := resolveDependencyDB(depID, routes)
		if dbName == "" {
			continue
		}

		// Verify database actually exists on server (exact match, not substring)
		for _, d := range databases {
			if d == dbName {
				dbSet[dbName] = true
				break
			}
		}
	}

	var result []string
	for d := range dbSet {
		result = append(result, d)
	}
	return result, rows.Err()
}

func convoyDependencyTargetsQuery() string {
	return `
		SELECT DISTINCT COALESCE(d.depends_on_issue_id, d.depends_on_wisp_id, d.depends_on_external) AS depends_on_id
		FROM hq.dependencies d
		WHERE d.issue_id = ? AND d.type = 'tracks'
			AND COALESCE(d.depends_on_issue_id, d.depends_on_wisp_id, d.depends_on_external) IS NOT NULL
	`
}

// resolveDependencyDB extracts the database name from a dependency ID.
// Handles two formats:
//   - "external:<prefix-or-rig>:<bead_id>" → routes[prefix] or rig name
//   - "<prefix>-<id>" → routes[prefix]
func resolveDependencyDB(depID string, routes map[string]string) string {
	if strings.HasPrefix(depID, "external:") {
		// Format: external:<prefix-or-rig>:<bead_id>
		parts := strings.SplitN(depID, ":", 3)
		if len(parts) >= 2 {
			if mapped, ok := routes[parts[1]]; ok {
				return mapped
			}
			return parts[1]
		}
		return ""
	}
	// Format: <prefix>-<id> — look up via routes
	parts := strings.SplitN(depID, "-", 2)
	if len(parts) >= 2 {
		if mapped, ok := routes[parts[0]]; ok {
			return mapped
		}
	}
	return ""
}

// createTag creates an immutable tag on a Dolt database at HEAD.
// Uses a dedicated connection to avoid USE statement races on the shared pool.
func createTag(db *sql.DB, dbName, tagName, message string) error {
	// Check if tag already exists
	var count int
	sdb := sanitizeDBName(dbName)
	checkQuery := fmt.Sprintf("SELECT COUNT(*) FROM `%s`.dolt_tags WHERE tag_name = ?", sdb)
	if err := db.QueryRow(checkQuery, tagName).Scan(&count); err != nil {
		return fmt.Errorf("checking tag existence: %w", err)
	}
	if count > 0 {
		return nil // Already exists, idempotent
	}

	// Use a dedicated connection for USE + DOLT_TAG to avoid races on the shared pool
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquiring connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, fmt.Sprintf("USE `%s`", sdb)); err != nil {
		return fmt.Errorf("switching to database %s: %w", dbName, err)
	}

	if _, err := conn.ExecContext(ctx, "CALL DOLT_TAG(?, 'HEAD', '-m', ?)", tagName, message); err != nil {
		return fmt.Errorf("creating tag: %w", err)
	}

	return nil
}

// createBranch creates a branch on a Dolt database at HEAD.
// Uses a dedicated connection to avoid USE statement races on the shared pool.
func createBranch(db *sql.DB, dbName, branchName string) error {
	// Check if branch already exists
	var count int
	sdb := sanitizeDBName(dbName)
	checkQuery := fmt.Sprintf("SELECT COUNT(*) FROM `%s`.dolt_branches WHERE name = ?", sdb)
	if err := db.QueryRow(checkQuery, branchName).Scan(&count); err != nil {
		return fmt.Errorf("checking branch existence: %w", err)
	}
	if count > 0 {
		return nil // Already exists, idempotent
	}

	// Use a dedicated connection for USE + DOLT_BRANCH to avoid races on the shared pool
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquiring connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, fmt.Sprintf("USE `%s`", sdb)); err != nil {
		return fmt.Errorf("switching to database %s: %w", dbName, err)
	}

	if _, err := conn.ExecContext(ctx, "CALL DOLT_BRANCH(?, 'HEAD')", branchName); err != nil {
		return fmt.Errorf("creating branch: %w", err)
	}

	return nil
}

// sanitizeDBName ensures a database name is safe for use in backtick-quoted identifiers.
// Only allows alphanumeric and underscore characters.
func sanitizeDBName(name string) string {
	var b strings.Builder
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// escalateStale finds convoy branches older than 7 days and reports them.
func escalateStale(db *sql.DB, databases []string, dryRun bool) {
	fmt.Println("\n=== Stale Branch Review ===")

	type staleBranch struct {
		Database string
		Branch   string
	}

	var stale []staleBranch

	for _, dbName := range databases {
		query := fmt.Sprintf(`
			SELECT b.name FROM %s.dolt_branches b
			WHERE b.name LIKE 'convoy/%%'
				AND EXISTS (
					SELECT 1 FROM hq.issues i
					WHERE (i.issue_type = 'convoy' OR EXISTS (
							SELECT 1 FROM hq.labels l
							WHERE l.issue_id = i.id AND l.label = 'gt:convoy'
						))
						AND b.name LIKE CONCAT('%%-', i.id)
						AND i.status IN ('closed', 'landed')
						AND i.updated_at < DATE_SUB(NOW(), INTERVAL 7 DAY)
				)
		`, "`"+sanitizeDBName(dbName)+"`")

		rows, err := db.Query(query)
		if err != nil {
			continue
		}
		for rows.Next() {
			var branchName string
			if err := rows.Scan(&branchName); err == nil {
				stale = append(stale, staleBranch{dbName, branchName})
			}
		}
		rows.Close()
	}

	if len(stale) == 0 {
		fmt.Println("No stale convoy branches found.")
		return
	}

	fmt.Printf("Found %d stale convoy branch(es) (closed >7 days):\n", len(stale))
	for _, s := range stale {
		fmt.Printf("  %s: %s\n", s.Database, s.Branch)
	}

	if !dryRun {
		fmt.Println("Escalation: stale branches should be reviewed and cleaned up.")
		fmt.Println("Tags are never deleted — they are immutable and cheap.")
	}
}

// convoyEvent is the minimal structure we parse from events.jsonl.
type convoyEvent struct {
	Type    string                 `json:"type"`
	Payload map[string]interface{} `json:"payload"`
}

// watchEvents tails ~/.events.jsonl and runs a snapshot cycle immediately
// when convoy lifecycle events are detected. This gives sub-second latency
// compared to the ~60s deacon patrol polling approach.
func watchEvents(host, port, routesFile string, cleanup bool) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot determine home dir: %w", err)
	}
	eventsPath := filepath.Join(home, "gt", ".events.jsonl")

	file, err := os.Open(eventsPath)
	if err != nil {
		return fmt.Errorf("opening events file: %w", err)
	}
	defer file.Close()

	// Seek to end — only process new events from this point forward
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("seeking to end: %w", err)
	}

	reader := bufio.NewReader(file)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	dsn := fmt.Sprintf("root@tcp(%s:%s)/information_schema?parseTime=true&timeout=5s&readTimeout=30s&writeTimeout=30s", host, port)

	log.Printf("Watching %s for convoy events...", eventsPath)

	for range ticker.C {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				break // no more data available
			}

			var ev convoyEvent
			if json.Unmarshal([]byte(line), &ev) != nil {
				continue
			}

			switch ev.Type {
			case "convoy.created", "convoy.staged", "convoy.launched", "convoy.closed":
				convoyID, _ := ev.Payload["convoy_id"].(string)
				log.Printf("Event: %s (convoy %s) — running snapshot cycle", ev.Type, convoyID)

				db, err := sql.Open("mysql", dsn)
				if err != nil {
					log.Printf("ERROR opening DB: %v", err)
					continue
				}
				db.SetMaxOpenConns(2)
				db.SetConnMaxLifetime(time.Minute)

				if err := db.Ping(); err != nil {
					log.Printf("ERROR pinging DB: %v", err)
					db.Close()
					continue
				}

				databases, err := listDatabases(db)
				if err != nil {
					log.Printf("ERROR listing databases: %v", err)
					db.Close()
					continue
				}

				routes := loadRoutes(routesFile)
				stats, err := snapshotConvoys(db, databases, routes, false)
				if err != nil {
					log.Printf("ERROR in snapshot cycle: %v", err)
				} else {
					log.Printf("Snapshot complete: %d tags, %d branches created", stats.tagsCreated, stats.branchesCreated)
				}

				if cleanup {
					escalateStale(db, databases, false)
				}

				db.Close()
			}
		}
	}
	return nil
}
