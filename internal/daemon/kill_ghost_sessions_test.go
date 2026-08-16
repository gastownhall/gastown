package daemon

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jonbaldie/gastown/internal/session"
	"github.com/jonbaldie/gastown/internal/tmux"
)

// writeFakeTmuxGhost creates a fake tmux that uses environment variables to
// control which sessions "exist" and logs kill-session calls to TMUX_LOG.
//
// Session existence is controlled via env vars: TMUX_HAS_<name> = "1"
// where <name> has dashes replaced with underscores. For example,
// TMUX_HAS_gt_witness=1 makes "gt-witness" appear to exist.
func writeFakeTmuxGhost(t *testing.T, dir string) {
	t.Helper()
	script := `#!/usr/bin/env bash
# Fake tmux for killDefaultPrefixGhosts tests.
# Session existence controlled by a file listing session names (one per line).
# Kill commands logged to TMUX_LOG.

# Parse args: find tmux subcommand and -t target.
cmd=""
target=""
skip_next=0
for arg in "$@"; do
  if [[ "$skip_next" -eq 1 ]]; then
    if [[ "$skip_flag" == "-t" ]]; then target="$arg"; fi
    skip_next=0
    continue
  fi
  case "$arg" in
    -L|-F) skip_flag="$arg"; skip_next=1 ;;
    -t)    skip_flag="-t"; skip_next=1 ;;
    -u)    ;;
    *)     if [[ -z "$cmd" ]]; then cmd="$arg"; fi ;;
  esac
done

# Strip tmux exact-match prefix "=" from target (HasSession passes "=name").
target="${target#=}"

case "$cmd" in
  has-session)
    if [[ -n "${TMUX_SESSIONS_FILE:-}" ]] && grep -qxF "$target" "$TMUX_SESSIONS_FILE" 2>/dev/null; then
      exit 0
    fi
    exit 1
    ;;
  kill-session)
    if [[ -n "${TMUX_LOG:-}" ]]; then
      echo "kill-session $target" >> "$TMUX_LOG"
    fi
    exit 0
    ;;
  list-panes)
    exit 1
    ;;
esac
exit 0
`
	path := filepath.Join(dir, "tmux")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
}

// ghostTestEnv holds the test environment for killDefaultPrefixGhosts tests.
type ghostTestEnv struct {
	daemon       *Daemon
	logBuf       *strings.Builder
	tmuxLog      string
	sessionsFile string
}

// setupGhostTest creates the common test infrastructure: fake tmux, temp dirs,
// and a Daemon.
func setupGhostTest(t *testing.T) *ghostTestEnv {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows — fake tmux requires bash")
	}

	townRoot := t.TempDir()
	fakeBinDir := t.TempDir()
	tmuxLog := filepath.Join(t.TempDir(), "tmux.log")
	sessionsFile := filepath.Join(t.TempDir(), "sessions.txt")
	if err := os.WriteFile(tmuxLog, []byte{}, 0o644); err != nil {
		t.Fatalf("create tmux log: %v", err)
	}
	if err := os.WriteFile(sessionsFile, []byte{}, 0o644); err != nil {
		t.Fatalf("create sessions file: %v", err)
	}

	writeFakeTmuxGhost(t, fakeBinDir)
	t.Setenv("PATH", fakeBinDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_LOG", tmuxLog)
	t.Setenv("TMUX_SESSIONS_FILE", sessionsFile)

	var logBuf strings.Builder
	d := &Daemon{
		config: &Config{TownRoot: townRoot},
		logger: log.New(&logBuf, "", 0),
		tmux:   tmux.NewTmux(),
	}

	// Clean registry state after test.
	t.Cleanup(func() { session.SetDefaultRegistry(session.NewPrefixRegistry()) })

	return &ghostTestEnv{
		daemon:       d,
		logBuf:       &logBuf,
		tmuxLog:      tmuxLog,
		sessionsFile: sessionsFile,
	}
}

// addSessions writes session names to the sessions file (one per line).
func (e *ghostTestEnv) addSessions(t *testing.T, names ...string) {
	t.Helper()
	content := strings.Join(names, "\n") + "\n"
	if err := os.WriteFile(e.sessionsFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeRigsJSON writes a rigs.json for getKnownRigs().
func writeRigsJSON(t *testing.T, townRoot string, rigs []string) {
	t.Helper()
	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	entries := make([]string, len(rigs))
	for i, r := range rigs {
		entries[i] = `"` + r + `":{}`
	}
	content := `{"rigs":{` + strings.Join(entries, ",") + `}}`
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// readKills returns the session names passed to kill-session from the log.
func readKills(t *testing.T, tmuxLog string) []string {
	t.Helper()
	data, err := os.ReadFile(tmuxLog)
	if err != nil {
		t.Fatalf("read tmux log: %v", err)
	}
	var kills []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.HasPrefix(line, "kill-session ") {
			kills = append(kills, strings.TrimPrefix(line, "kill-session "))
		}
	}
	return kills
}

func TestKillDefaultPrefixGhosts_EmptyRegistry(t *testing.T) {
	env := setupGhostTest(t)

	// Empty registry → allRigs is empty → bail immediately.
	session.SetDefaultRegistry(session.NewPrefixRegistry())

	env.daemon.killDefaultPrefixGhosts()

	kills := readKills(t, env.tmuxLog)
	if len(kills) > 0 {
		t.Errorf("expected no kills with empty registry, got: %v", kills)
	}
	if strings.Contains(env.logBuf.String(), "Killing") {
		t.Error("should not log any kill messages with empty registry")
	}
}

func TestKillDefaultPrefixGhosts_GTIsLegitimate(t *testing.T) {
	env := setupGhostTest(t)

	// Register gastown with "gt" prefix — makes gt-* sessions legitimate.
	reg := session.NewPrefixRegistry()
	reg.Register("gt", "gastown")
	session.SetDefaultRegistry(reg)

	// Even if gt-witness exists, it should NOT be killed.
	env.addSessions(t, "gt-witness", "gt-refinery")

	env.daemon.killDefaultPrefixGhosts()

	kills := readKills(t, env.tmuxLog)
	if len(kills) > 0 {
		t.Errorf("expected no kills when gt is legitimate, got: %v", kills)
	}
	if strings.Contains(env.logBuf.String(), "Killing") {
		t.Error("should not kill anything when a rig owns the gt prefix")
	}
}

func TestKillDefaultPrefixGhosts_KillsGhostPatrolSessions(t *testing.T) {
	env := setupGhostTest(t)

	// Register a rig with non-gt prefix. No rig owns "gt".
	reg := session.NewPrefixRegistry()
	reg.Register("ti", "titanium")
	session.SetDefaultRegistry(reg)

	// Ghost sessions exist with default "gt" prefix.
	env.addSessions(t, "gt-witness", "gt-refinery")

	env.daemon.killDefaultPrefixGhosts()

	kills := readKills(t, env.tmuxLog)
	if len(kills) != 2 {
		t.Fatalf("expected 2 kills, got %d: %v", len(kills), kills)
	}
	killSet := map[string]bool{}
	for _, k := range kills {
		killSet[k] = true
	}
	if !killSet["gt-witness"] {
		t.Error("expected gt-witness to be killed")
	}
	if !killSet["gt-refinery"] {
		t.Error("expected gt-refinery to be killed")
	}
}

func TestKillDefaultPrefixGhosts_NoKillWhenGhostsAbsent(t *testing.T) {
	env := setupGhostTest(t)

	// Non-gt registry but no ghost sessions exist.
	reg := session.NewPrefixRegistry()
	reg.Register("ti", "titanium")
	session.SetDefaultRegistry(reg)

	// No sessions file entries — nothing exists.

	env.daemon.killDefaultPrefixGhosts()

	kills := readKills(t, env.tmuxLog)
	if len(kills) > 0 {
		t.Errorf("expected no kills when ghost sessions don't exist, got: %v", kills)
	}
}

func TestKillDefaultPrefixGhosts_PolecatDuplicate_Killed(t *testing.T) {
	env := setupGhostTest(t)

	// Register rig with non-gt prefix.
	reg := session.NewPrefixRegistry()
	reg.Register("ti", "titanium")
	session.SetDefaultRegistry(reg)

	// Set up rigs.json and polecat directory.
	writeRigsJSON(t, env.daemon.config.TownRoot, []string{"titanium"})
	if err := os.MkdirAll(filepath.Join(env.daemon.config.TownRoot, "titanium", "polecats", "furiosa"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Both ghost and correct sessions exist → ghost is a confirmed duplicate.
	env.addSessions(t, "gt-furiosa", "ti-furiosa")

	env.daemon.killDefaultPrefixGhosts()

	kills := readKills(t, env.tmuxLog)
	found := false
	for _, k := range kills {
		if k == "gt-furiosa" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected gt-furiosa to be killed (duplicate), kills: %v", kills)
	}
	if !strings.Contains(env.logBuf.String(), "Killing duplicate ghost polecat session gt-furiosa") {
		t.Errorf("expected duplicate kill log message, got: %s", env.logBuf.String())
	}
}

func TestKillDefaultPrefixGhosts_PolecatSolo_NotKilled(t *testing.T) {
	env := setupGhostTest(t)

	// Register rig with non-gt prefix.
	reg := session.NewPrefixRegistry()
	reg.Register("ti", "titanium")
	session.SetDefaultRegistry(reg)

	// Set up rigs.json and polecat directory.
	writeRigsJSON(t, env.daemon.config.TownRoot, []string{"titanium"})
	if err := os.MkdirAll(filepath.Join(env.daemon.config.TownRoot, "titanium", "polecats", "furiosa"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Only ghost session exists — correct one is absent.
	// Should log warning but NOT kill (may have active work).
	env.addSessions(t, "gt-furiosa")

	env.daemon.killDefaultPrefixGhosts()

	kills := readKills(t, env.tmuxLog)
	for _, k := range kills {
		if k == "gt-furiosa" {
			t.Error("should NOT kill solo ghost polecat gt-furiosa (may have active work)")
		}
	}
	if !strings.Contains(env.logBuf.String(), "not killing") {
		t.Errorf("expected 'not killing' log message for solo ghost, got: %s", env.logBuf.String())
	}
}

func TestKillDefaultPrefixGhosts_PolecatSkippedWhenRigUsesDefaultPrefix(t *testing.T) {
	env := setupGhostTest(t)

	// If any rig uses "gt", gtIsLegitimate is true and the whole function bails.
	reg := session.NewPrefixRegistry()
	reg.Register("gt", "gastown")
	reg.Register("ti", "titanium")
	session.SetDefaultRegistry(reg)

	writeRigsJSON(t, env.daemon.config.TownRoot, []string{"gastown", "titanium"})
	if err := os.MkdirAll(filepath.Join(env.daemon.config.TownRoot, "gastown", "polecats", "alice"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(env.daemon.config.TownRoot, "titanium", "polecats", "bob"), 0o755); err != nil {
		t.Fatal(err)
	}

	env.addSessions(t, "gt-alice", "gt-bob")

	env.daemon.killDefaultPrefixGhosts()

	// gtIsLegitimate should cause early return — nothing killed.
	kills := readKills(t, env.tmuxLog)
	if len(kills) > 0 {
		t.Errorf("expected no kills when a rig owns gt prefix, got: %v", kills)
	}
}
