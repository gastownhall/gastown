package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
)

func installAgentLedgerBD(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fake bd")
	}

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "bd.log")
	script := `#!/bin/sh
cmd=""
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *) cmd="$arg"; break ;;
  esac
done
printf 'cmd=%s BEADS_DIR=%s args=%s\n' "$cmd" "${BEADS_DIR-}" "$*" >> "$AGENT_LEDGER_LOG"
case "$cmd" in
  version)
    printf 'bd test\n'
    ;;
  list)
    if [ "${BEADS_DIR-}" = "${TEST_RIG_BEADS-}" ]; then
      printf '%s\n' "${TEST_RIG_LIST-[]}"
    elif [ "${BEADS_DIR-}" = "${TEST_TOWN_BEADS-}" ]; then
      printf '%s\n' "${TEST_TOWN_LIST-[]}"
    else
      printf '[]\n'
    fi
    ;;
  mol)
    printf '{"wisps":[]}\n'
    ;;
  show)
    if [ "${BEADS_DIR-}" = "${TEST_RIG_BEADS-}" ]; then
      printf '%s\n' "${TEST_RIG_SHOW-${TEST_RIG_LIST-[]}}"
    elif [ "${BEADS_DIR-}" = "${TEST_TOWN_BEADS-}" ]; then
      printf '%s\n' "${TEST_TOWN_SHOW-${TEST_TOWN_LIST-[]}}"
    else
      printf '[]\n'
    fi
    ;;
  update)
    ;;
  *)
    printf 'unexpected bd command: %s\n' "$cmd" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AGENT_LEDGER_LOG", logPath)
	beads.ResetBdAllowStaleCacheForTest()
	return logPath
}

func setupAgentLedgerTown(t *testing.T, rigName, prefix string) (townRoot, rigBeads string) {
	t.Helper()
	townRoot = filepath.Join(t.TempDir(), "gt")
	rigDir := filepath.Join(townRoot, rigName, "mayor", "rig")
	rigBeads = filepath.Join(rigDir, ".beads")
	for _, dir := range []string{filepath.Join(townRoot, "mayor"), filepath.Join(townRoot, ".beads"), rigBeads} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"name":"test"}`), 0o644); err != nil {
		t.Fatalf("write town marker: %v", err)
	}
	route := `{"prefix":"` + prefix + `-","path":"` + rigName + `/mayor/rig"}` + "\n"
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(route), 0o644); err != nil {
		t.Fatalf("write routes: %v", err)
	}
	return townRoot, rigBeads
}

func TestAgentBeadMatchesDescriptionAndIDFallback(t *testing.T) {
	tests := []struct {
		name  string
		issue *beads.Issue
		role  string
		rig   string
		want  bool
	}{
		{
			name: "description matches legacy random wisp ID",
			issue: &beads.Issue{
				ID:          "au-wisp-0ti",
				Description: "Agent\n\nrole_type: refinery\nrig: alleago_ui",
			},
			role: "refinery",
			rig:  "alleago_ui",
			want: true,
		},
		{
			name: "canonical ID fallback matches sparse wisp metadata",
			issue: &beads.Issue{
				ID: "gt-gastown-witness",
			},
			role: "witness",
			rig:  "gastown",
			want: true,
		},
		{
			name: "collapsed prefix-rig ID fallback matches sparse metadata",
			issue: &beads.Issue{
				ID: "cp-refinery",
			},
			role: "refinery",
			rig:  "cp",
			want: true,
		},
		{
			name: "registered long prefix ID fallback matches legacy metadata",
			issue: &beads.Issue{
				ID:          "flext-refinery",
				Description: `role_type: refinery\nrig: flext`,
			},
			role: "refinery",
			rig:  "flext",
			want: true,
		},
		{
			name: "role mismatch",
			issue: &beads.Issue{
				ID:          "gt-gastown-witness",
				Description: "Agent\n\nrole_type: witness\nrig: gastown",
			},
			role: "refinery",
			rig:  "gastown",
			want: false,
		},
		{
			name: "rig mismatch",
			issue: &beads.Issue{
				ID:          "gt-gastown-refinery",
				Description: "Agent\n\nrole_type: refinery\nrig: gastown",
			},
			role: "refinery",
			rig:  "other",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := agentBeadMatches(tt.issue, tt.role, tt.rig)
			if got != tt.want {
				t.Fatalf("agentBeadMatches() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPickBestAgentBead(t *testing.T) {
	candidates := []agentBeadCandidate{
		candidate("town-issue", agentSourceTownIssues, "open"),
		candidate("rig-issue", agentSourceRigIssues, "open"),
		candidate("town-wisp", agentSourceTownWisps, "open"),
		candidate("rig-wisp", agentSourceRigWisps, "open"),
	}

	got, err := pickBestAgentBead(candidates)
	if err != nil {
		t.Fatalf("pickBestAgentBead returned error: %v", err)
	}
	if got == nil || got.ID != "rig-wisp" {
		t.Fatalf("pickBestAgentBead picked %v, want rig-wisp", got)
	}
}

func TestPickBestAgentBeadSkipsClosed(t *testing.T) {
	candidates := []agentBeadCandidate{
		candidate("closed-rig-wisp", agentSourceRigWisps, "closed"),
		candidate("open-rig-issue", agentSourceRigIssues, "open"),
	}

	got, err := pickBestAgentBead(candidates)
	if err != nil {
		t.Fatalf("pickBestAgentBead returned error: %v", err)
	}
	if got == nil || got.ID != "open-rig-issue" {
		t.Fatalf("pickBestAgentBead picked %v, want open-rig-issue", got)
	}
}

func TestPickBestAgentBeadRejectsSameRankDuplicates(t *testing.T) {
	candidates := []agentBeadCandidate{
		candidate("rig-wisp-a", agentSourceRigWisps, "open"),
		candidate("rig-wisp-b", agentSourceRigWisps, "open"),
		candidate("rig-issue", agentSourceRigIssues, "open"),
	}

	got, err := pickBestAgentBead(candidates)
	if err == nil {
		t.Fatalf("pickBestAgentBead picked %v, want duplicate error", got)
	}
	if !strings.Contains(err.Error(), "multiple matching agent beads") {
		t.Fatalf("error = %q, want duplicate diagnostic", err)
	}
}

func TestPickBestAgentBeadPrefersStructuredIdentityOverIDFallback(t *testing.T) {
	candidates := []agentBeadCandidate{
		{
			ID:     "flext-cemk3",
			Source: agentSourceRigIssues,
			Status: "open",
			Issue: &beads.Issue{
				ID:          "flext-cemk3",
				Description: "role_type: witness\nrig: flext",
			},
		},
		{
			ID:     "flext-witness",
			Source: agentSourceRigIssues,
			Status: "open",
			Issue: &beads.Issue{
				ID:          "flext-witness",
				Description: `role_type: witness\nrig: flext`,
			},
		},
	}

	var matches []agentBeadCandidate
	for _, candidate := range candidates {
		if identityRank, ok := agentBeadMatchRank(candidate.Issue, "witness", "flext"); ok {
			candidate.IdentityRank = identityRank
			matches = append(matches, candidate)
		}
	}

	got, err := pickBestAgentBead(matches)
	if err != nil {
		t.Fatalf("pickBestAgentBead returned error: %v", err)
	}
	if got == nil || got.ID != "flext-cemk3" {
		t.Fatalf("pickBestAgentBead picked %v, want structured flext-cemk3 identity", got)
	}
}

func TestFindAgentBeadCandidatesSelectsRequestedRigLedger(t *testing.T) {
	townRoot, rigBeads := setupAgentLedgerTown(t, "ccs", "ccs")
	currentBeads := filepath.Join(townRoot, "other", ".beads")
	if err := os.MkdirAll(currentBeads, 0o755); err != nil {
		t.Fatal(err)
	}
	installAgentLedgerBD(t)
	t.Setenv("TEST_RIG_BEADS", rigBeads)
	t.Setenv("TEST_TOWN_BEADS", filepath.Join(townRoot, ".beads"))
	t.Setenv("TEST_RIG_LIST", `[{"id":"ccs-witness","issue_type":"task","labels":["gt:agent","idle:2"],"status":"open","description":"role_type: witness\nrig: ccs"}]`)
	t.Setenv("TEST_TOWN_LIST", `[{"id":"hq-mayor","issue_type":"agent","labels":["gt:agent"],"status":"open","description":"role_type: mayor\nrig: null"}]`)

	candidates, err := findAgentBeadCandidates(townRoot, currentBeads, "ccs")
	if err != nil {
		t.Fatalf("findAgentBeadCandidates: %v", err)
	}
	var matches []agentBeadCandidate
	for _, candidate := range candidates {
		if agentBeadMatches(candidate.Issue, "witness", "ccs") {
			matches = append(matches, candidate)
		}
	}
	match, err := pickBestAgentBead(matches)
	if err != nil {
		t.Fatalf("pickBestAgentBead: %v", err)
	}
	if match == nil || match.ID != "ccs-witness" || match.Source != agentSourceRigIssues || match.BeadsDir != rigBeads {
		t.Fatalf("match = %#v, want ccs rig issue in %s", match, rigBeads)
	}
}

func TestRunAgentsResolveAllowsTownFallback(t *testing.T) {
	townRoot, rigBeads := setupAgentLedgerTown(t, "ccs", "ccs")
	installAgentLedgerBD(t)
	t.Setenv("TEST_RIG_BEADS", rigBeads)
	t.Setenv("TEST_TOWN_BEADS", filepath.Join(townRoot, ".beads"))
	t.Setenv("TEST_RIG_LIST", `[]`)
	t.Setenv("TEST_TOWN_LIST", `[{"id":"ccs-witness","issue_type":"agent","labels":["gt:agent"],"status":"open","description":"role_type: witness\nrig: ccs"}]`)

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(townRoot); err != nil {
		t.Fatal(err)
	}

	oldRole, oldRig, oldJSON, oldQuiet := agentsResolveRole, agentsResolveRig, agentsResolveJSON, agentsResolveQuiet
	t.Cleanup(func() {
		agentsResolveRole, agentsResolveRig, agentsResolveJSON, agentsResolveQuiet = oldRole, oldRig, oldJSON, oldQuiet
	})
	agentsResolveRole, agentsResolveRig, agentsResolveJSON, agentsResolveQuiet = "witness", "ccs", false, false
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	if err := runAgentsResolve(cmd, nil); err != nil {
		t.Fatalf("runAgentsResolve rejected town fallback: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "ccs-witness" {
		t.Fatalf("output = %q, want ccs-witness", got)
	}
}

func candidate(id string, source agentBeadSource, status string) agentBeadCandidate {
	return agentBeadCandidate{
		ID:     id,
		Source: source,
		Status: status,
		Issue:  &beads.Issue{ID: id, Status: status},
	}
}
