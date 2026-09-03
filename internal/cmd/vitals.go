package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

var vitalsCmd = &cobra.Command{
	Use:     "vitals",
	GroupID: GroupDiag,
	Short:   "Show unified health dashboard",
	RunE:    runVitals,
}

func init() { rootCmd.AddCommand(vitalsCmd) }

func runVitals(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}
	printVitalsDoltServers(townRoot)
	fmt.Println()
	printVitalsDatabases(townRoot)
	fmt.Println()
	printVitalsBackups(townRoot)
	return nil
}

func printVitalsDoltServers(townRoot string) {
	fmt.Println(style.Bold.Render("Dolt Servers"))
	config := doltserver.DefaultConfig(townRoot)
	running, pid, _ := doltserver.IsRunning(townRoot)

	if running {
		m := doltserver.GetHealthMetrics(townRoot)
		fmt.Printf("  %s :%d  production  PID %d  %s  %d/%d conn  %v\n",
			style.Success.Render("●"), config.Port, pid,
			m.DiskUsageHuman, m.Connections, m.MaxConnections,
			beadsLatencySummary(m))
		for _, w := range m.Warnings {
			fmt.Printf("    %s %s\n", style.Warning.Render("!"), w)
		}
	} else {
		fmt.Printf("  %s :%d  production  %s\n",
			style.Dim.Render("○"), config.Port, style.Dim.Render("not running"))
	}

	// Zombie dolt processes (test servers not cleaned up)
	for _, z := range findVitalsZombies(townRoot, config.Port) {
		if z.foreign {
			fmt.Printf("  %s :%s foreign workspace PID %s\n", style.Dim.Render("○"), z.port, z.pid)
		} else {
			fmt.Printf("  %s :%s test zombie PID %s\n", style.Warning.Render("○"), z.port, z.pid)
		}
	}
}

type vitalsZombie struct {
	pid, port string
	foreign   bool // true if process belongs to another Gas Town workspace
}

// findVitalsZombies finds Dolt servers not on the production port.
// Uses lsof-based port discovery instead of pgrep/ps string matching (ZFC fix: gt-fj87).
// Checks workspace ownership to avoid flagging sibling Gas Town instances as zombies.
func findVitalsZombies(townRoot string, prodPort int) []vitalsZombie {
	listeners := doltserver.FindAllDoltListeners()
	expectedDataDir, _ := filepath.Abs(filepath.Join(townRoot, ".dolt-data"))
	var zombies []vitalsZombie
	for _, l := range listeners {
		if l.Port == prodPort {
			continue
		}
		// Check if this Dolt process belongs to a Gas Town workspace.
		// If its --data-dir is a .dolt-data directory under a valid workspace,
		// it's a sibling instance, not a test zombie.
		dataDir := doltserver.GetDoltDataDirFromProcess(l.PID)
		if dataDir != "" {
			absDataDir, _ := filepath.Abs(dataDir)
			if absDataDir == expectedDataDir {
				// Our own workspace on a different port — still a zombie
			} else if filepath.Base(absDataDir) == ".dolt-data" {
				parentDir := filepath.Dir(absDataDir)
				if isWs, _ := workspace.IsWorkspace(parentDir); isWs {
					// Legitimate sibling Gas Town workspace
					zombies = append(zombies, vitalsZombie{
						pid:     strconv.Itoa(l.PID),
						port:    strconv.Itoa(l.Port),
						foreign: true,
					})
					continue
				}
			}
		}
		zombies = append(zombies, vitalsZombie{
			pid:  strconv.Itoa(l.PID),
			port: strconv.Itoa(l.Port),
		})
	}
	return zombies
}

func printVitalsDatabases(townRoot string) {
	databases, _ := doltserver.ListDatabases(townRoot)
	orphans, _ := doltserver.FindOrphanedDatabases(townRoot)

	if len(orphans) > 0 {
		fmt.Printf("%s (%d registered, %d orphan)\n",
			style.Bold.Render("Databases"), len(databases), len(orphans))
	} else {
		fmt.Printf("%s (%d registered)\n",
			style.Bold.Render("Databases"), len(databases))
	}

	orphanSet := make(map[string]bool)
	for _, o := range orphans {
		orphanSet[o.Name] = true
	}

	fmt.Printf("  %-12s %5s  %4s  %6s  %4s\n",
		style.Dim.Render("Rig"), style.Dim.Render("Total"),
		style.Dim.Render("Open"), style.Dim.Render("Closed"), style.Dim.Render("%"))

	config := doltserver.DefaultConfig(townRoot)
	for _, db := range databases {
		if orphanSet[db] {
			continue
		}
		s := queryVitalsStats(config, db)
		if s == nil {
			fmt.Printf("  %-12s %5s  %4s  %6s  %4s\n", db, "-", "-", "-", "-")
			continue
		}
		pct := "-"
		if s.total > 0 {
			pct = fmt.Sprintf("%d%%", s.closed*100/s.total)
		}
		fmt.Printf("  %-12s %5d  %4d  %6d  %4s\n",
			db, s.total, s.open+s.inProgress, s.closed, pct)
	}
}

type vitalsStats struct{ total, open, inProgress, closed int }

func queryVitalsStats(config *doltserver.Config, dbName string) *vitalsStats {
	q := fmt.Sprintf("SELECT COUNT(*),"+
		"SUM(CASE WHEN status='open' THEN 1 ELSE 0 END),"+
		"SUM(CASE WHEN status='in_progress' THEN 1 ELSE 0 END),"+
		"SUM(CASE WHEN status='closed' THEN 1 ELSE 0 END) "+
		"FROM %s.issues", dbName)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "dolt",
		"--host", "127.0.0.1", "--port", strconv.Itoa(config.Port),
		"--user", config.User, "--no-tls", "sql", "-r", "csv", "-q", q)
	cmd.Env = append(os.Environ(), "DOLT_CLI_PASSWORD="+config.Password)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return nil
	}
	f := strings.Split(lines[1], ",")
	if len(f) < 4 {
		return nil
	}
	total, _ := strconv.Atoi(strings.TrimSpace(f[0]))
	open, _ := strconv.Atoi(strings.TrimSpace(f[1]))
	ip, _ := strconv.Atoi(strings.TrimSpace(f[2]))
	closed, _ := strconv.Atoi(strings.TrimSpace(f[3]))
	return &vitalsStats{total, open, ip, closed}
}

func printVitalsBackups(townRoot string) {
	fmt.Println(style.Bold.Render("Backups"))

	// Local Dolt backup
	backupDir := filepath.Join(townRoot, ".dolt-backup")
	if entries, err := os.ReadDir(backupDir); err == nil {
		var count int
		var latest time.Time
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			count++
			if info, err := e.Info(); err == nil && info.ModTime().After(latest) {
				latest = info.ModTime()
			}
		}
		if count > 0 {
			fmt.Printf("  Local:  %s  last sync %s (%d DBs)\n",
				vitalsShortHome(backupDir), latest.Format("2006-01-02 15:04"), count)
		} else {
			fmt.Printf("  Local:  %s  %s\n", vitalsShortHome(backupDir), style.Dim.Render("empty"))
		}
	} else {
		fmt.Printf("  Local:  %s\n", style.Dim.Render("not found"))
	}

	// JSONL git archive
	archiveDir := filepath.Join(townRoot, ".dolt-archive", "git")
	out, err := exec.Command("git", "-C", archiveDir, "log", "-1", "--format=%ci").Output()
	if err != nil {
		fmt.Printf("  JSONL:  %s\n", style.Dim.Render("not available"))
		return
	}
	ts := strings.TrimSpace(string(out))
	if ts == "" {
		fmt.Printf("  JSONL:  %s\n", style.Dim.Render("no commits"))
		return
	}
	// Count records across per-rig issues.jsonl
	var records int
	if dirs, err := os.ReadDir(archiveDir); err == nil {
		for _, d := range dirs {
			if !d.IsDir() {
				continue
			}
			if data, err := os.ReadFile(filepath.Join(archiveDir, d.Name(), "issues.jsonl")); err == nil {
				if s := strings.TrimSpace(string(data)); s != "" {
					records += len(strings.Split(s, "\n"))
				}
			}
		}
	}
	if t, err := time.Parse("2006-01-02 15:04:05 -0700", ts); err == nil {
		ts = t.Format("2006-01-02 15:04")
	}
	fmt.Printf("  JSONL:  last push %s", ts)
	if records > 0 {
		fmt.Printf(" (%s records)", vitalsFormatCount(records))
	}
	fmt.Println()
}

func vitalsFormatCount(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%d,%03d", n/1000, n%1000)
}

func vitalsShortHome(path string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home) {
		return "~" + filepath.ToSlash(path[len(home):])
	}
	return path
}

// beadsLatencySummary renders bd's read latency for the one-line vitals view.
//
// It shows the MAX rather than the median on purpose. The median sits on a
// tight ~200ms floor and barely moves while the town is suffering; the max is
// the number that corresponds to an agent actually waiting.
func beadsLatencySummary(m *doltserver.HealthMetrics) string {
	if m.BeadsRead.Err != nil {
		return "bd:unmeasured"
	}
	return fmt.Sprintf("bd:%v max", m.BeadsRead.Max.Round(time.Millisecond))
}
