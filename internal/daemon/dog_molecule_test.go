package daemon

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseWispID(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		wantID string
	}{
		{
			name:   "standard wisp output",
			input:  "✓ Spawned wisp: gt-wisp-abc123 — Reap stale wisps",
			wantID: "gt-wisp-abc123",
		},
		{
			name:   "wisp ID with ANSI codes",
			input:  "\033[32m✓\033[0m Spawned wisp: \033[1mgt-wisp-xyz789\033[0m — Title",
			wantID: "gt-wisp-xyz789",
		},
		{
			name:   "empty output",
			input:  "",
			wantID: "",
		},
		{
			name:   "no wisp ID in output",
			input:  "Error: something went wrong",
			wantID: "",
		},
		{
			name:   "wisp ID at end of line",
			input:  "Created gt-wisp-def456",
			wantID: "gt-wisp-def456",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseWispID(tt.input)
			if got != tt.wantID {
				t.Errorf("parseWispID(%q) = %q, want %q", tt.input, got, tt.wantID)
			}
		})
	}
}

func TestStripANSI(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no ANSI", "hello", "hello"},
		{"color code", "\033[32mgreen\033[0m", "green"},
		{"bold", "\033[1mbold\033[0m", "bold"},
		{"multiple codes", "\033[32m✓\033[0m \033[1mtext\033[0m", "✓ text"},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripANSI(tt.input)
			if got != tt.want {
				t.Errorf("stripANSI(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseChildrenJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantIDs []string
		wantErr bool
	}{
		{
			name:    "bare array",
			input:   `[{"id":"a","title":"Probe","status":"open"}]`,
			wantIDs: []string{"a"},
		},
		{
			name:    "map wrapper from bd show",
			input:   `{"hq-wisp-root":[{"id":"hq-wisp-a","title":"Probe","status":"open"},{"id":"hq-wisp-b","title":"Report","status":"open"}]}`,
			wantIDs: []string{"hq-wisp-a", "hq-wisp-b"},
		},
		{
			name:    "empty map wrapper",
			input:   `{"hq-wisp-root":[]}`,
			wantIDs: []string{},
		},
		{
			name:    "schema metadata with children",
			input:   `{"hq-wisp-root":[{"id":"hq-wisp-a","title":"Probe","status":"open"}],"schema_version":1}`,
			wantIDs: []string{"hq-wisp-a"},
		},
		{
			name:    "schema metadata with empty children",
			input:   `{"hq-wisp-root":[],"schema_version":1}`,
			wantIDs: []string{},
		},
		{
			name:    "multiple child arrays are deterministic",
			input:   `{"hq-wisp-b":[{"id":"b-step","title":"Report","status":"open"}],"schema_version":1,"hq-wisp-a":[{"id":"a-step","title":"Probe","status":"open"}]}`,
			wantIDs: []string{"a-step", "b-step"},
		},
		{
			name:    "schema key is metadata even if array-valued",
			input:   `{"schema_version":[{"id":"metadata","title":"Ignore","status":"open"}],"hq-wisp-root":[{"id":"hq-wisp-a","title":"Probe","status":"open"}]}`,
			wantIDs: []string{"hq-wisp-a"},
		},
		{
			name:    "empty array",
			input:   `[]`,
			wantIDs: []string{},
		},
		{
			name:    "empty input",
			input:   `   `,
			wantErr: true,
		},
		{
			name:    "malformed bare array",
			input:   `[`,
			wantErr: true,
		},
		{
			name:    "malformed object envelope",
			input:   `{"hq-wisp-root":[`,
			wantErr: true,
		},
		{
			name:    "invalid json",
			input:   `not json`,
			wantErr: true,
		},
		{
			name:    "malformed child array",
			input:   `{"hq-wisp-root":[{"id":1}],"schema_version":1}`,
			wantErr: true,
		},
		{
			name:    "non-array child payload",
			input:   `{"hq-wisp-root":1,"schema_version":1}`,
			wantErr: true,
		},
		{
			name:    "metadata only is not silent skip-all",
			input:   `{"schema_version":1}`,
			wantErr: true,
		},
		{
			name:    "empty object is not silent skip-all",
			input:   `{}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseChildrenJSON(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			gotIDs := make([]string, 0, len(got))
			for _, child := range got {
				gotIDs = append(gotIDs, child.ID)
			}
			if !reflect.DeepEqual(gotIDs, tt.wantIDs) {
				t.Errorf("got child IDs %v, want %v", gotIDs, tt.wantIDs)
			}
		})
	}
}

func TestDogMolGracefulDegradation(t *testing.T) {
	// A dogMol with empty rootID should be a no-op for all operations.
	dm := &dogMol{
		rootID:  "",
		stepIDs: make(map[string]string),
	}

	// These should not panic or error — graceful degradation.
	dm.closeStep("scan")
	dm.failStep("scan", "test failure")
	dm.close()
}

// TestFormulaStepIDsByTitleMolDogBackup pins the fix for upstream #4142's
// residual bug: registration matched child titles against a fixed-order
// keyword switch, and mol-dog-backup's "sync" step is titled "Sync databases
// to backup remotes" — which also contains the "backup" keyword checked
// earlier in the switch — so the step registered under the wrong slug and
// "sync" was left unknown. Exact title matching against the real formula
// must map every step to its own ID, not a keyword collision.
func TestFormulaStepIDsByTitleMolDogBackup(t *testing.T) {
	got := formulaStepIDsByTitle("mol-dog-backup", "")

	want := map[string]string{
		"sync databases to backup remotes":     "sync",
		"sync backups to offsite storage":      "offsite",
		"report findings and return to kennel": "report",
	}
	for title, wantID := range want {
		if gotID := got[title]; gotID != wantID {
			t.Errorf("formulaStepIDsByTitle(mol-dog-backup)[%q] = %q, want %q", title, gotID, wantID)
		}
	}
}

// TestFormulaStepIDsByTitleUnknownFormula pins graceful degradation: an
// unresolvable formula name (empty, or not embedded) must return nil so
// discoverSteps falls back to the keyword heuristic instead of erroring.
func TestFormulaStepIDsByTitleUnknownFormula(t *testing.T) {
	if got := formulaStepIDsByTitle("", ""); got != nil {
		t.Errorf("formulaStepIDsByTitle(\"\") = %v, want nil", got)
	}
	if got := formulaStepIDsByTitle("mol-does-not-exist", ""); got != nil {
		t.Errorf("formulaStepIDsByTitle(mol-does-not-exist) = %v, want nil", got)
	}
}

// TestFormulaStepIDsByTitlePrefersTownOverride pins the rig/town/embedded
// precedence: if a town-level formula override exists on disk at
// <townRoot>/.beads/formulas/<name>.formula.toml, its step titles/IDs must
// win over the compiled-in default. Without this, a customized (or merely
// drifted) on-disk formula would pour children whose titles no longer match
// the embedded copy's titles, silently falling back to the keyword-collision
// heuristic this fix exists to eliminate.
func TestFormulaStepIDsByTitlePrefersTownOverride(t *testing.T) {
	townRoot := t.TempDir()
	formulasDir := filepath.Join(townRoot, ".beads", "formulas")
	if err := os.MkdirAll(formulasDir, 0o755); err != nil {
		t.Fatalf("mkdir formulas dir: %v", err)
	}
	override := `formula = "mol-dog-backup"
description = "overridden"

[[steps]]
id = "custom-sync"
title = "Custom sync step title"
`
	if err := os.WriteFile(filepath.Join(formulasDir, "mol-dog-backup.formula.toml"), []byte(override), 0o644); err != nil {
		t.Fatalf("write override formula: %v", err)
	}

	got := formulaStepIDsByTitle("mol-dog-backup", townRoot)
	if gotID, ok := got["custom sync step title"]; !ok || gotID != "custom-sync" {
		t.Errorf("formulaStepIDsByTitle with town override = %v, want title mapped to %q", got, "custom-sync")
	}
	if _, ok := got["sync databases to backup remotes"]; ok {
		t.Errorf("formulaStepIDsByTitle should not contain embedded-default titles once a town override exists; got %v", got)
	}
}

// TestDiscoverStepsExactTitleMatchAvoidsKeywordCollision reproduces the
// production log signature from upstream #4142's residual bug: pours
// mol-dog-backup with a fake bd, then verifies discoverSteps registers
// "sync" and "offsite" under their own IDs rather than losing them to the
// "backup" keyword collision (see TestFormulaStepIDsByTitleMolDogBackup).
func TestDiscoverStepsExactTitleMatchAvoidsKeywordCollision(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	script := `#!/bin/sh
case "$1" in
  show)
    printf '{"gtwn-wisp-root":[{"id":"gtwn-wisp-sync","title":"Sync databases to backup remotes","status":"open"},{"id":"gtwn-wisp-offsite","title":"Sync backups to offsite storage","status":"open"},{"id":"gtwn-wisp-report","title":"Report findings and return to kennel","status":"open"}],"schema_version":1}\n'
    ;;
  *) exit 0 ;;
esac
`
	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}

	var logBuf bytes.Buffer
	dm := &dogMol{
		rootID:      "gtwn-wisp-root",
		stepIDs:     make(map[string]string),
		formulaName: "mol-dog-backup",
		bdPath:      bdPath,
		townRoot:    dir,
		logger:      log.New(&logBuf, "", 0),
	}

	dm.discoverSteps()

	wantSteps := map[string]string{
		"sync":    "gtwn-wisp-sync",
		"offsite": "gtwn-wisp-offsite",
		"report":  "gtwn-wisp-report",
	}
	for slug, wantID := range wantSteps {
		if gotID := dm.stepIDs[slug]; gotID != wantID {
			t.Errorf("stepIDs[%q] = %q, want %q (known: %v)", slug, gotID, wantID, dm.knownSteps())
		}
	}
	if _, ok := dm.stepIDs["backup"]; ok {
		t.Errorf("stepIDs[\"backup\"] should not exist — mol-dog-backup has no \"backup\" step slug; got %v", dm.stepIDs)
	}
}

// TestCloseRemainingStepsForceClosesBlockedSteps pins the fix for the patrol
// wisp leak (gt-92jh). Molecule steps are sequenced, so bd refuses a plain
// `close` on a step whose predecessor is still open, and `bd show --children`
// does not hand them back in dependency order. Without --force the backstop
// deterministically failed on those steps and orphaned them as OPEN wisps.
func TestCloseRemainingStepsForceClosesBlockedSteps(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	binDir := filepath.Join(dir, "bin")
	for _, d := range []string{stateDir, binDir, filepath.Join(dir, ".beads")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	// Fake bd that models bd's real dependency gating: step2 is blocked while
	// step1 is open, step3 while step2 is open, and --force overrides. Children
	// are returned out of dependency order, as bd does.
	script := `#!/bin/sh
STATE="` + stateDir + `"
st() { if [ -f "$STATE/$1" ]; then echo closed; else echo open; fi; }
case "$1" in
  show)
    printf '{"hq-wisp-root":[{"id":"step2","title":"Inspect","status":"%s"},{"id":"step3","title":"Report","status":"%s"},{"id":"step1","title":"Probe","status":"%s"}],"schema_version":1}\n' "$(st step2)" "$(st step3)" "$(st step1)"
    ;;
  close)
    id="$2"
    force=0
    for a in "$@"; do
      case "$a" in --force|-f) force=1 ;; esac
    done
    pred=""
    case "$id" in
      step2) pred=step1 ;;
      step3) pred=step2 ;;
    esac
    if [ -n "$pred" ] && [ ! -f "$STATE/$pred" ] && [ "$force" -eq 0 ]; then
      echo "cannot close $id: blocked by open issues [$pred] (use --force to override)" >&2
      exit 1
    fi
    touch "$STATE/$id"
    ;;
  *) exit 0 ;;
esac
`
	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}

	var logBuf bytes.Buffer
	dm := &dogMol{
		rootID:   "hq-wisp-root",
		stepIDs:  make(map[string]string),
		bdPath:   bdPath,
		townRoot: dir,
		logger:   log.New(&logBuf, "", 0),
	}

	dm.closeRemainingSteps()

	for _, step := range []string{"step1", "step2", "step3"} {
		if _, err := os.Stat(filepath.Join(stateDir, step)); err != nil {
			t.Errorf("%s was not closed (leaked as an open wisp); log:\n%s", step, logBuf.String())
		}
	}
	if strings.Contains(logBuf.String(), "failed") {
		t.Errorf("closeRemainingSteps logged a failure; log:\n%s", logBuf.String())
	}
}
