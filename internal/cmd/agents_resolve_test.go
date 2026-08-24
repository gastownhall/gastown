package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

func TestFindAgentBeadCandidatesResolvesSameTownIdentityFromTownAndRigCwd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fake bd")
	}

	townRoot, _ := filepath.EvalSymlinks(t.TempDir())
	townBeadsDir := filepath.Join(townRoot, ".beads")
	rigDir := filepath.Join(townRoot, "gastown", "refinery", "rig")
	for _, dir := range []string{filepath.Join(townRoot, "mayor"), townBeadsDir, rigDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"name":"test"}`), 0o644); err != nil {
		t.Fatalf("write town marker: %v", err)
	}

	binDir := t.TempDir()
	script := `#!/bin/sh
cmd=""
for arg in "$@"; do case "$arg" in --*) ;; *) cmd="$arg"; break ;; esac; done
case "$cmd" in
  version) printf 'bd version 1.2.2\n' ;;
  list) printf '%s\n' '[{"id":"gt-gastown-refinery","status":"open","labels":["gt:agent"],"description":"role_type: refinery\nrig: gastown"}]' ;;
  query) printf '%s\n' '[]' ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	for _, cwd := range []string{townRoot, rigDir} {
		candidates, err := findAgentBeadCandidates(cwd, townBeadsDir)
		if err != nil {
			t.Fatalf("find candidates from %s: %v", cwd, err)
		}
		var matches []agentBeadCandidate
		for _, candidate := range candidates {
			if agentBeadMatches(candidate.Issue, "refinery", "gastown") {
				matches = append(matches, candidate)
			}
		}
		got, err := pickBestAgentBead(matches)
		if err != nil {
			t.Fatalf("pick candidate from %s: %v", cwd, err)
		}
		if got == nil || got.ID != "gt-gastown-refinery" || got.Source != agentSourceTownIssues {
			t.Fatalf("candidate from %s = %#v, want canonical town identity", cwd, got)
		}
	}
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
	if got == nil || got.ID != "town-wisp" {
		t.Fatalf("pickBestAgentBead picked %v, want town-wisp", got)
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

func candidate(id string, source agentBeadSource, status string) agentBeadCandidate {
	return agentBeadCandidate{
		ID:     id,
		Source: source,
		Status: status,
		Issue:  &beads.Issue{ID: id, Status: status},
	}
}
