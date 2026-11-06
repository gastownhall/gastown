package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// townBeadsFixture is a town with its own DB (where agent beads live by
// design) and one rig, greenplace, with its own DB.
type townBeadsFixture struct {
	townRoot  string
	townBeads string
	rigPath   string
	rigBeads  string
	logPath   string
}

// setupTownBeadsFixture writes the town and rig beads dirs, seeds each DB
// with the given agent-bead JSON (a bd list/show payload), and installs a fake
// bd that answers per BEADS_DIR and logs every update.
func setupTownBeadsFixture(t *testing.T, townJSON, rigJSON string) townBeadsFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fake bd")
	}

	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	f := townBeadsFixture{
		townRoot: filepath.Join(tmp, "gt"),
		logPath:  filepath.Join(tmp, "bd.log"),
	}
	f.townBeads = filepath.Join(f.townRoot, ".beads")
	f.rigPath = filepath.Join(f.townRoot, "greenplace")
	f.rigBeads = filepath.Join(f.rigPath, ".beads")

	for _, dir := range []string{filepath.Join(f.townRoot, "mayor"), f.townBeads, f.rigBeads} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.townRoot, "mayor", "town.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}

	townJSONPath := filepath.Join(tmp, "town.json")
	rigJSONPath := filepath.Join(tmp, "rig.json")
	if err := os.WriteFile(townJSONPath, []byte(townJSON), 0o644); err != nil {
		t.Fatalf("write town fixture: %v", err)
	}
	if err := os.WriteFile(rigJSONPath, []byte(rigJSON), 0o644); err != nil {
		t.Fatalf("write rig fixture: %v", err)
	}

	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	bdScript := `#!/bin/sh
for a in "$@"; do
  if [ "$a" = query ]; then printf '[]\n'; exit 0; fi
done
case "$BEADS_DIR" in
  "$TOWN_BEADS") DB_JSON="$TOWN_JSON" ;;
  "$RIG_BEADS") DB_JSON="$RIG_JSON" ;;
  *) printf 'unexpected BEADS_DIR: %s\n' "$BEADS_DIR" >&2; exit 1 ;;
esac
case "$1" in
  list)
    cat "$DB_JSON"
    ;;
  show)
    if grep -q "\"id\":\"$2\"" "$DB_JSON"; then
      printf '[{"id":"%s","labels":["gt:agent","idle:2"]}]\n' "$2"
    else
      printf 'not found: %s\n' "$2" >&2
      exit 1
    fi
    ;;
  update)
    printf 'update BEADS_DIR=%s\n' "$BEADS_DIR" >> "$BD_LOG"
    ;;
  *)
    printf 'unexpected bd command: %s\n' "$1" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TOWN_BEADS", f.townBeads)
	t.Setenv("RIG_BEADS", f.rigBeads)
	t.Setenv("TOWN_JSON", townJSONPath)
	t.Setenv("RIG_JSON", rigJSONPath)
	t.Setenv("BD_LOG", f.logPath)
	return f
}

func chdirForTest(t *testing.T, dir string) {
	t.Helper()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
}

const (
	testRefineryJSON = `{"id":"gp-greenplace-refinery","status":"open","description":"Agent\n\nrole_type: refinery\nrig: greenplace"}`
	testEmptyJSON    = `[]`
)

func TestAgentsResolveFromRigFolderFindsTownBead(t *testing.T) {
	polecatID := beads.PolecatBeadIDWithPrefix("gp", "greenplace", "furiosa")
	townJSON := "[" + testRefineryJSON + `,{"id":"` + polecatID + `","status":"open","description":"Agent\n\nrole_type: polecat\nrig: greenplace\ncleanup_status: clean"}]`
	f := setupTownBeadsFixture(t, townJSON, testEmptyJSON)

	// Run from the rig folder: the cwd's rig DB has no refinery bead.
	candidates, err := findAgentBeadCandidates(f.rigPath, f.rigBeads, "greenplace")
	if err != nil {
		t.Fatalf("findAgentBeadCandidates() error = %v", err)
	}
	match := resolveForTest(t, candidates, "refinery", "greenplace")
	if match == nil || match.ID != "gp-greenplace-refinery" {
		t.Fatalf("resolve refinery from rig folder = %+v, want gp-greenplace-refinery", match)
	}
	if match.Source != agentSourceTownIssues {
		t.Fatalf("resolve refinery from rig folder source = %s, want %s", match.Source, agentSourceTownIssues)
	}
}

func TestAgentsResolveFromTownRootFindsTownBead(t *testing.T) {
	f := setupTownBeadsFixture(t, "["+testRefineryJSON+"]", testEmptyJSON)

	for _, rig := range []string{"", "greenplace"} {
		candidates, err := findAgentBeadCandidates(f.townRoot, f.townBeads, rig)
		if err != nil {
			t.Fatalf("findAgentBeadCandidates(rig=%q) error = %v", rig, err)
		}
		match := resolveForTest(t, candidates, "refinery", rig)
		if match == nil || match.ID != "gp-greenplace-refinery" {
			t.Fatalf("resolve refinery from town root (rig=%q) = %+v, want gp-greenplace-refinery", rig, match)
		}
		if match.Source != agentSourceTownIssues {
			t.Fatalf("resolve refinery from town root (rig=%q) source = %s, want %s", rig, match.Source, agentSourceTownIssues)
		}
		for _, c := range candidates {
			if c.Source == agentSourceRigIssues || c.Source == agentSourceRigWisps {
				t.Fatalf("town-root run (rig=%q) labelled %s as rig-local: %+v", rig, c.Source, c)
			}
		}
	}
}

func TestAgentsResolvePrefersRigLocalBead(t *testing.T) {
	// The rig still has its own refinery bead; it must win over the town one.
	rigJSON := "[" + testRefineryJSON + "]"
	f := setupTownBeadsFixture(t, "["+testRefineryJSON+"]", rigJSON)

	fromRig, err := findAgentBeadCandidates(f.rigPath, f.rigBeads, "greenplace")
	if err != nil {
		t.Fatalf("findAgentBeadCandidates(rig folder) error = %v", err)
	}
	if match := resolveForTest(t, fromRig, "refinery", "greenplace"); match == nil || match.Source != agentSourceRigIssues {
		t.Fatalf("resolve from rig folder = %+v, want rig-issues match", match)
	}

	fromTown, err := findAgentBeadCandidates(f.townRoot, f.townBeads, "greenplace")
	if err != nil {
		t.Fatalf("findAgentBeadCandidates(town root) error = %v", err)
	}
	if match := resolveForTest(t, fromTown, "refinery", "greenplace"); match == nil || match.Source != agentSourceRigIssues {
		t.Fatalf("resolve from town root --rig greenplace = %+v, want rig-issues match", match)
	}
}

func TestAgentsStateFromRigFolderWritesTownBead(t *testing.T) {
	f := setupTownBeadsFixture(t, "["+testRefineryJSON+"]", testEmptyJSON)
	chdirForTest(t, f.rigPath)
	setAgentStateFlagsForTest(t, []string{"idle=0"})

	if err := runAgentState(nil, []string{"gp-greenplace-refinery"}); err != nil {
		t.Fatalf("runAgentState() error = %v", err)
	}
	assertUpdatesWentTo(t, f.logPath, f.townBeads)
}

func TestAgentsStatePrefersRigLocalBead(t *testing.T) {
	f := setupTownBeadsFixture(t, "["+testRefineryJSON+"]", "["+testRefineryJSON+"]")
	chdirForTest(t, f.rigPath)
	setAgentStateFlagsForTest(t, []string{"idle=0"})

	if err := runAgentState(nil, []string{"gp-greenplace-refinery"}); err != nil {
		t.Fatalf("runAgentState() error = %v", err)
	}
	assertUpdatesWentTo(t, f.logPath, f.rigBeads)
}

func TestPolecatAgentBeadsReadFromTownDB(t *testing.T) {
	polecatID := beads.PolecatBeadIDWithPrefix("gp", "greenplace", "furiosa")
	townJSON := `[{"id":"` + polecatID + `","status":"open","description":"Agent\n\nrole_type: polecat\nrig: greenplace\ncleanup_status: clean"}]`
	f := setupTownBeadsFixture(t, townJSON, testEmptyJSON)

	agents, err := loadPolecatAgentBeads(f.rigPath)
	if err != nil {
		t.Fatalf("loadPolecatAgentBeads() error = %v", err)
	}
	issue, ok := agents[polecatID]
	if !ok {
		t.Fatalf("polecat agent bead %s not found in %v", polecatID, agents)
	}
	if got := parsePolecatAgentFields(issue).CleanupStatus; got != "clean" {
		t.Fatalf("cleanup_status = %q, want %q", got, "clean")
	}
}

// resolveForTest applies the same matching and ranking runAgentsResolve uses.
func resolveForTest(t *testing.T, candidates []agentBeadCandidate, role, rig string) *agentBeadCandidate {
	t.Helper()
	var matches []agentBeadCandidate
	for _, c := range candidates {
		if agentBeadMatches(c.Issue, role, rig) {
			matches = append(matches, c)
		}
	}
	match, err := pickBestAgentBead(matches)
	if err != nil {
		t.Fatalf("pickBestAgentBead(%s) error = %v", role, err)
	}
	return match
}

func setAgentStateFlagsForTest(t *testing.T, set []string) {
	t.Helper()
	oldSet, oldIncr, oldDel, oldJSON := agentStateSet, agentStateIncr, agentStateDel, agentStateJSON
	t.Cleanup(func() {
		agentStateSet, agentStateIncr, agentStateDel, agentStateJSON = oldSet, oldIncr, oldDel, oldJSON
	})
	agentStateSet = set
	agentStateIncr = ""
	agentStateDel = nil
	agentStateJSON = false
}

func assertUpdatesWentTo(t *testing.T, logPath, wantBeadsDir string) {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake bd log: %v", err)
	}
	log := strings.TrimSpace(string(data))
	if log == "" {
		t.Fatal("no bd update was issued")
	}
	want := "update BEADS_DIR=" + wantBeadsDir
	for _, line := range strings.Split(log, "\n") {
		if line != want {
			t.Fatalf("bd update went to %q, want %q", line, want)
		}
	}
}
