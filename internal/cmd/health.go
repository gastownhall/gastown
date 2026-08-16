package cmd

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jonbaldie/gastown/internal/doltserver"
	"github.com/jonbaldie/gastown/internal/health"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var (
	healthJSON bool
)

// HealthReport is the machine-readable output of gt health --json.
type HealthReport struct {
	Timestamp string            `json:"timestamp"`
	Server    *ServerHealth     `json:"server"`
	Databases []DatabaseHealth  `json:"databases"`
	Pollution []PollutionRecord `json:"pollution,omitempty"`
	Backups   *BackupHealth     `json:"backups"`
	Processes *ProcessHealth    `json:"processes"`
	Orphans   []OrphanDB        `json:"orphans,omitempty"`
}

type ServerHealth struct {
	Running          bool    `json:"running"`
	PID              int     `json:"pid,omitempty"`
	Port             int     `json:"port,omitempty"`
	LatencyMs        int64   `json:"latency_ms,omitempty"`
	Connections      int     `json:"connections,omitempty"`
	MaxConnections   int     `json:"max_connections,omitempty"`
	DiskUsageBytes   int64   `json:"disk_usage_bytes,omitempty"`
	DiskUsageHuman   string  `json:"disk_usage_human,omitempty"`
	LastCommitAgeSec float64 `json:"last_commit_age_seconds,omitempty"`
	LastCommitDB     string  `json:"last_commit_db,omitempty"`
}

type DatabaseHealth struct {
	Name       string `json:"name"`
	Issues     int    `json:"issues"`
	OpenIssues int    `json:"open_issues"`
	Wisps      int    `json:"wisps"`
	OpenWisps  int    `json:"open_wisps"`
	Commits    int    `json:"commits"`
}

type PollutionRecord struct {
	Database string `json:"database"`
	ID       string `json:"id"`
	Title    string `json:"title"`
	Pattern  string `json:"pattern"`
}

type BackupHealth struct {
	DoltFreshness   string `json:"dolt_freshness,omitempty"`
	DoltAgeSeconds  int    `json:"dolt_age_seconds,omitempty"`
	DoltStale       bool   `json:"dolt_stale"`
	JSONLFreshness  string `json:"jsonl_freshness,omitempty"`
	JSONLAgeSeconds int    `json:"jsonl_age_seconds,omitempty"`
	JSONLStale      bool   `json:"jsonl_stale"`
}

type ProcessHealth struct {
	ZombieCount int   `json:"zombie_count"`
	ZombiePIDs  []int `json:"zombie_pids,omitempty"`
}

type OrphanDB struct {
	Name string `json:"name"`
	Size string `json:"size,omitempty"`
}

var healthCmd = &cobra.Command{
	Use:   "health",
	Short: "Show comprehensive system health",
	Long: `Display a comprehensive health report for the Gas Town data plane.

Sections:
  1. Dolt Server: status, PID, port, latency
  2. Databases: per-DB counts of issues, wisps, commits
  3. Pollution: scan for known test/garbage patterns
  4. Backups: Dolt filesystem and JSONL git freshness
  5. Processes: zombie dolt servers
  6. Orphan DBs: databases not referenced by any rig

Use --json for machine-readable output.`,
	RunE: runHealth,
}

func init() {
	healthCmd.Flags().BoolVar(&healthJSON, "json", false, "Output as JSON")
	rootCmd.AddCommand(healthCmd)
}

func runHealth(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	report := &HealthReport{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	// 1. Dolt Server
	report.Server = checkServerHealth(townRoot)

	// 2. Databases (only if server is running)
	if report.Server.Running {
		report.Databases = checkDatabaseHealth(report.Server.Port)
	}

	// 3. Pollution scan
	if report.Server.Running {
		report.Pollution = checkPollution(report.Server.Port)
	}

	// 4. Backups
	report.Backups = checkBackupHealth(townRoot)

	// 5. Processes
	report.Processes = checkProcessHealth(report.Server.Port)

	// 6. Orphans
	report.Orphans = checkOrphanDBs(townRoot)

	if healthJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}

	printHealthReport(report)
	return nil
}

func checkServerHealth(townRoot string) *ServerHealth {
	sh := &ServerHealth{}

	running, pid, err := doltserver.IsRunning(townRoot)
	if err != nil || !running {
		sh.Running = false
		return sh
	}

	sh.Running = true
	sh.PID = pid

	state, err := doltserver.LoadState(townRoot)
	if err == nil {
		sh.Port = state.Port
	}

	metrics := doltserver.GetHealthMetrics(townRoot)
	sh.LatencyMs = metrics.QueryLatency.Milliseconds()
	sh.Connections = metrics.Connections
	sh.MaxConnections = metrics.MaxConnections
	sh.DiskUsageBytes = metrics.DiskUsageBytes
	sh.DiskUsageHuman = metrics.DiskUsageHuman
	if metrics.LastCommitAge > 0 {
		sh.LastCommitAgeSec = metrics.LastCommitAge.Seconds()
		sh.LastCommitDB = metrics.LastCommitDB
	}

	return sh
}

func checkDatabaseHealth(port int) []DatabaseHealth {
	productionDBs := []string{"hq", "gt", "mo"}
	var results []DatabaseHealth

	for _, dbName := range productionDBs {
		dh := DatabaseHealth{Name: dbName}

		// wa-d6f: socket-first DSN (TCP fallback) to avoid TIME_WAIT churn
		// from short-lived gt-CLI calls into Dolt.
		dsn := buildDoltDSN("root", port, dbName, dsnOpts{
			ParseTime:   true,
			Timeout:     "5s",
			ReadTimeout: "10s",
		})
		db, err := sql.Open("mysql", dsn)
		if err != nil {
			results = append(results, dh)
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)

		// Issue counts
		_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues").Scan(&dh.Issues)
		_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE status IN ('open','in_progress')").Scan(&dh.OpenIssues)

		// Wisp counts
		_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM wisps").Scan(&dh.Wisps)
		_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM wisps WHERE status IN ('open','hooked','in_progress')").Scan(&dh.OpenWisps)

		// Commit count
		_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log").Scan(&dh.Commits)

		cancel()
		db.Close()
		results = append(results, dh)
	}

	return results
}

func checkPollution(port int) []PollutionRecord {
	productionDBs := []string{"hq", "gt", "mo"}
	var records []PollutionRecord

	// Known pollution patterns to check in the issues table.
	type check struct {
		where   string
		pattern string
	}
	checks := []check{
		{"title LIKE '--%'", "--help artifacts"},
		{"title LIKE 'Usage: %'", "CLI usage output"},
		{"id LIKE 'offlinebrew-%'", "offlinebrew test prefix"},
		{"id LIKE '%-wisp-%' AND (ephemeral IS NULL OR ephemeral = false)", "non-ephemeral wisp ID in issues table"},
		{"title LIKE 'Test Issue%'", "test issue title"},
		{"id LIKE 'test%'", "test ID prefix"},
	}

	for _, dbName := range productionDBs {
		// wa-d6f: socket-first DSN (TCP fallback) — same rationale as above.
		dsn := buildDoltDSN("root", port, dbName, dsnOpts{
			ParseTime:   true,
			Timeout:     "5s",
			ReadTimeout: "10s",
		})
		db, err := sql.Open("mysql", dsn)
		if err != nil {
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)

		for _, c := range checks {
			query := fmt.Sprintf("SELECT id, COALESCE(title,'') FROM issues WHERE (%s) AND status != 'closed' LIMIT 10", c.where)
			rows, err := db.QueryContext(ctx, query)
			if err != nil {
				continue
			}
			for rows.Next() {
				var id, title string
				if err := rows.Scan(&id, &title); err != nil {
					continue
				}
				records = append(records, PollutionRecord{
					Database: dbName,
					ID:       id,
					Title:    title,
					Pattern:  c.pattern,
				})
			}
			rows.Close()
		}

		cancel()
		db.Close()
	}

	return records
}

func checkBackupHealth(townRoot string) *BackupHealth {
	bh := &BackupHealth{}

	// Dolt filesystem backup freshness.
	backupDir := filepath.Join(townRoot, ".dolt-backup")
	if _, err := os.Stat(backupDir); err == nil {
		newest := findNewestFile(backupDir)
		if !newest.IsZero() {
			age := time.Since(newest)
			bh.DoltAgeSeconds = int(age.Seconds())
			bh.DoltFreshness = age.Round(time.Second).String()
			bh.DoltStale = age > 30*time.Minute
		}
	}

	// JSONL git backup freshness.
	homeDir, err := os.UserHomeDir()
	if err == nil {
		gitRepo := filepath.Join(homeDir, ".dolt-archive", "git")
		if _, err := os.Stat(filepath.Join(gitRepo, ".git")); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "git", "-C", gitRepo, "log", "-1", "--format=%ci")
			output, err := cmd.Output()
			if err == nil {
				commitTimeStr := strings.TrimSpace(string(output))
				if commitTime, err := time.Parse("2006-01-02 15:04:05 -0700", commitTimeStr); err == nil {
					age := time.Since(commitTime)
					bh.JSONLAgeSeconds = int(age.Seconds())
					bh.JSONLFreshness = age.Round(time.Second).String()
					bh.JSONLStale = age > 30*time.Minute
				}
			}
		}
	}

	return bh
}

// checkProcessHealth finds zombie Dolt servers (not on the expected port).
// Uses lsof-based port discovery instead of pgrep/ps string matching (ZFC fix: gt-fj87).
func checkProcessHealth(expectedPort int) *ProcessHealth {
	result := health.FindZombieServers([]int{expectedPort})
	return &ProcessHealth{
		ZombieCount: result.Count,
		ZombiePIDs:  result.PIDs,
	}
}

func checkOrphanDBs(townRoot string) []OrphanDB {
	orphans, err := doltserver.FindOrphanedDatabases(townRoot)
	if err != nil {
		return nil
	}

	var results []OrphanDB
	for _, o := range orphans {
		results = append(results, OrphanDB{
			Name: o.Name,
			Size: formatBytes(o.SizeBytes),
		})
	}
	return results
}

func printHealthReport(r *HealthReport) {
	// 1. Server
	fmt.Printf("\n%s Dolt Server\n", style.Bold.Render("●"))
	if r.Server.Running {
		fmt.Printf("  Status: %s (PID %d)\n", style.Bold.Render("running"), r.Server.PID)
		if r.Server.Port > 0 {
			fmt.Printf("  Port: %d\n", r.Server.Port)
		}
		fmt.Printf("  Latency: %dms\n", r.Server.LatencyMs)
		fmt.Printf("  Connections: %d / %d\n", r.Server.Connections, r.Server.MaxConnections)
		if r.Server.DiskUsageHuman != "" {
			fmt.Printf("  Disk: %s\n", r.Server.DiskUsageHuman)
		}
	} else {
		fmt.Printf("  Status: %s\n", style.Dim.Render("not running"))
	}

	// 2. Databases
	if len(r.Databases) > 0 {
		fmt.Printf("\n%s Databases\n", style.Bold.Render("●"))
		for _, db := range r.Databases {
			fmt.Printf("  %s: %d issues (%d open), %d wisps (%d open), %d commits\n",
				style.Bold.Render(db.Name), db.Issues, db.OpenIssues,
				db.Wisps, db.OpenWisps, db.Commits)
		}
	}

	// 3. Pollution
	fmt.Printf("\n%s Pollution\n", style.Bold.Render("●"))
	if len(r.Pollution) == 0 {
		fmt.Printf("  %s No pollution detected\n", style.Bold.Render("✓"))
	} else {
		fmt.Printf("  %s %d suspicious record(s):\n", style.Bold.Render("!"), len(r.Pollution))
		for _, p := range r.Pollution {
			fmt.Printf("    %s/%s: %q (%s)\n", p.Database, p.ID, p.Title, p.Pattern)
		}
	}

	// 4. Backups
	fmt.Printf("\n%s Backups\n", style.Bold.Render("●"))
	if r.Backups.DoltFreshness != "" {
		icon := style.Bold.Render("✓")
		if r.Backups.DoltStale {
			icon = style.Bold.Render("!")
		}
		fmt.Printf("  %s Dolt filesystem: %s ago\n", icon, r.Backups.DoltFreshness)
	} else {
		fmt.Printf("  %s Dolt filesystem: not found\n", style.Dim.Render("○"))
	}
	if r.Backups.JSONLFreshness != "" {
		icon := style.Bold.Render("✓")
		if r.Backups.JSONLStale {
			icon = style.Bold.Render("!")
		}
		fmt.Printf("  %s JSONL git: %s ago\n", icon, r.Backups.JSONLFreshness)
	} else {
		fmt.Printf("  %s JSONL git: not found\n", style.Dim.Render("○"))
	}

	// 5. Processes
	fmt.Printf("\n%s Processes\n", style.Bold.Render("●"))
	if r.Processes.ZombieCount == 0 {
		fmt.Printf("  %s No zombie processes\n", style.Bold.Render("✓"))
	} else {
		fmt.Printf("  %s %d zombie(s): %v\n", style.Bold.Render("!"),
			r.Processes.ZombieCount, r.Processes.ZombiePIDs)
	}

	// 6. Orphans
	fmt.Printf("\n%s Orphan DBs\n", style.Bold.Render("●"))
	if len(r.Orphans) == 0 {
		fmt.Printf("  %s None\n", style.Bold.Render("✓"))
	} else {
		for _, o := range r.Orphans {
			fmt.Printf("  %s %s (%s)\n", style.Bold.Render("!"), o.Name, o.Size)
		}
	}

	fmt.Println()
}
