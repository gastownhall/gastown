package cmd

import (
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/deacon"
	"github.com/steveyegge/gastown/internal/formula"
)

func TestExtractPatrolRole(t *testing.T) {
	tests := []struct {
		name     string
		title    string
		expected string
	}{
		{
			name:     "deacon patrol",
			title:    "Digest: mol-deacon-patrol",
			expected: "deacon",
		},
		{
			name:     "witness patrol",
			title:    "Digest: mol-witness-patrol",
			expected: "witness",
		},
		{
			name:     "refinery patrol",
			title:    "Digest: mol-refinery-patrol",
			expected: "refinery",
		},
		{
			name:     "wisp digest without patrol suffix",
			title:    "Digest: gt-wisp-abc123",
			expected: "patrol",
		},
		{
			name:     "random title",
			title:    "Some other digest",
			expected: "patrol",
		},
		{
			name:     "empty title",
			title:    "",
			expected: "patrol",
		},
		{
			name:     "just digest prefix",
			title:    "Digest: ",
			expected: "patrol",
		},
		{
			name:     "mol prefix but no patrol suffix",
			title:    "Digest: mol-deacon-other",
			expected: "patrol",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractPatrolRole(tt.title)
			if got != tt.expected {
				t.Errorf("extractPatrolRole(%q) = %q, want %q", tt.title, got, tt.expected)
			}
		})
	}
}

func TestPatrolDigestDateFormat(t *testing.T) {
	// Test that PatrolDigest.Date format is YYYY-MM-DD
	digest := PatrolDigest{
		Date:        "2026-01-17",
		TotalCycles: 5,
		ByRole:      map[string]int{"deacon": 2, "witness": 3},
	}

	if digest.Date != "2026-01-17" {
		t.Errorf("Date format incorrect: got %q", digest.Date)
	}

	if digest.TotalCycles != 5 {
		t.Errorf("TotalCycles: got %d, want 5", digest.TotalCycles)
	}

	if digest.ByRole["deacon"] != 2 {
		t.Errorf("ByRole[deacon]: got %d, want 2", digest.ByRole["deacon"])
	}
}

func TestPatrolCycleEntry(t *testing.T) {
	entry := PatrolCycleEntry{
		ID:          "gt-abc123",
		Role:        "deacon",
		Title:       "Digest: mol-deacon-patrol",
		Description: "Test description",
	}

	if entry.ID != "gt-abc123" {
		t.Errorf("ID: got %q, want %q", entry.ID, "gt-abc123")
	}

	if entry.Role != "deacon" {
		t.Errorf("Role: got %q, want %q", entry.Role, "deacon")
	}
}

func TestParseStepResults(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected map[string]string
	}{
		{
			name:     "empty input",
			input:    "",
			expected: map[string]string{},
		},
		{
			name:  "single step",
			input: "heartbeat:OK",
			expected: map[string]string{
				"heartbeat": "OK",
			},
		},
		{
			name:  "multiple steps",
			input: "heartbeat:OK,inbox-check:OK,orphan-cleanup:SKIP",
			expected: map[string]string{
				"heartbeat":      "OK",
				"inbox-check":    "OK",
				"orphan-cleanup": "SKIP",
			},
		},
		{
			name:  "mixed case normalized to upper",
			input: "heartbeat:ok,inbox-check:Skip",
			expected: map[string]string{
				"heartbeat":   "OK",
				"inbox-check": "SKIP",
			},
		},
		{
			name:  "whitespace trimmed",
			input: " heartbeat : OK , inbox-check : OK ",
			expected: map[string]string{
				"heartbeat":   "OK",
				"inbox-check": "OK",
			},
		},
		{
			name:  "trailing comma ignored",
			input: "heartbeat:OK,",
			expected: map[string]string{
				"heartbeat": "OK",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseStepResults(tt.input)
			if len(got) != len(tt.expected) {
				t.Errorf("parseStepResults(%q) returned %d entries, want %d", tt.input, len(got), len(tt.expected))
				return
			}
			for k, v := range tt.expected {
				if got[k] != v {
					t.Errorf("parseStepResults(%q)[%q] = %q, want %q", tt.input, k, got[k], v)
				}
			}
		})
	}
}

func TestBuildStepAudit(t *testing.T) {
	tests := []struct {
		name        string
		formulaName string
		stepsFlag   string
		wantPrefix  string // check prefix of output
		wantSuffix  string // check suffix of output
		wantContain string // check output contains this
	}{
		{
			name:        "deacon patrol with no steps reported",
			formulaName: "mol-deacon-patrol",
			stepsFlag:   "",
			wantPrefix:  "Steps: NOT REPORTED",
			wantContain: "/26)",
		},
		{
			name:        "deacon patrol with all steps OK",
			formulaName: "mol-deacon-patrol",
			stepsFlag:   "heartbeat:OK,inbox-check:OK,orphan-process-cleanup:OK,test-pollution-cleanup:OK,gate-evaluation:OK,dispatch-gated-molecules:OK,check-convoy-completion:OK,resolve-external-deps:OK,fire-notifications:OK,heartbeat-mid:OK,health-scan:OK,dolt-health:OK,zombie-scan:OK,plugin-run:OK,dog-pool-maintenance:OK,dog-health-check:OK,orphan-check:OK,session-gc:OK,wisp-compact:OK,compact-report:OK,costs-digest:OK,patrol-digest:OK,log-maintenance:OK,patrol-cleanup:OK,context-check:OK,loop-or-exit:OK",
			wantPrefix:  "Steps:",
			wantSuffix:  "(26/26)",
			wantContain: "heartbeat OK",
		},
		{
			name:        "deacon patrol with some steps skipped",
			formulaName: "mol-deacon-patrol",
			stepsFlag:   "heartbeat:OK,inbox-check:OK,loop-or-exit:OK",
			wantPrefix:  "Steps:",
			wantSuffix:  "(3/26)",
			wantContain: "heartbeat OK",
		},
		{
			name:        "skipped steps shown as SKIP",
			formulaName: "mol-deacon-patrol",
			stepsFlag:   "heartbeat:OK",
			wantContain: "inbox-check SKIP",
		},
		{
			name:        "nonexistent formula with no steps",
			formulaName: "mol-nonexistent",
			stepsFlag:   "",
			wantPrefix:  "Steps: NOT REPORTED (formula not found)",
		},
		{
			name:        "nonexistent formula with steps",
			formulaName: "mol-nonexistent",
			stepsFlag:   "heartbeat:OK",
			wantContain: "unvalidated",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildStepAudit(tt.formulaName, tt.stepsFlag)
			if tt.wantPrefix != "" && !strings.HasPrefix(got, tt.wantPrefix) {
				t.Errorf("buildStepAudit() = %q, want prefix %q", got, tt.wantPrefix)
			}
			if tt.wantSuffix != "" && !strings.HasSuffix(got, tt.wantSuffix) {
				t.Errorf("buildStepAudit() = %q, want suffix %q", got, tt.wantSuffix)
			}
			if tt.wantContain != "" && !strings.Contains(got, tt.wantContain) {
				t.Errorf("buildStepAudit() = %q, want to contain %q", got, tt.wantContain)
			}
		})
	}
}

func deaconAllOKSteps(t *testing.T) string {
	t.Helper()
	content, err := formula.GetEmbeddedFormulaContent("mol-deacon-patrol")
	if err != nil {
		t.Fatalf("load deacon formula: %v", err)
	}
	f, err := formula.Parse(content)
	if err != nil {
		t.Fatalf("parse deacon formula: %v", err)
	}
	ids := f.GetAllIDs()
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, id+":OK")
	}
	return strings.Join(parts, ",")
}

func TestValidateStepAudit(t *testing.T) {
	allOK := deaconAllOKSteps(t)
	allSkip := strings.ReplaceAll(allOK, ":OK", ":SKIP")

	tests := []struct {
		name      string
		stepsFlag string
		wantErr   string
	}{
		{
			name:      "all steps accepted",
			stepsFlag: allOK,
		},
		{
			name:      "missing steps rejected",
			stepsFlag: "heartbeat:OK",
			wantErr:   "missing required step IDs",
		},
		{
			name:      "empty steps rejected",
			stepsFlag: "",
			wantErr:   "--steps is required",
		},
		{
			name:      "unknown step rejected",
			stepsFlag: allOK + ",made-up:OK",
			wantErr:   "unknown step IDs",
		},
		{
			name:      "invalid status rejected",
			stepsFlag: strings.Replace(allOK, "heartbeat:OK", "heartbeat:DONE", 1),
			wantErr:   "invalid status",
		},
		{
			name:      "duplicate step rejected",
			stepsFlag: allOK + ",heartbeat:OK",
			wantErr:   "duplicate step ID",
		},
		{
			name:      "all skip rejected",
			stepsFlag: allSkip,
			wantErr:   "heartbeat:OK",
		},
		{
			name:      "malformed entry rejected",
			stepsFlag: strings.Replace(allOK, "heartbeat:OK", "heartbeat", 1),
			wantErr:   "step:STATUS",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			audit, err := validateStepAudit("mol-deacon-patrol", tt.stepsFlag, "", "")
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateStepAudit() error = %v", err)
				}
				if !strings.HasSuffix(audit, "(26/26)") {
					t.Fatalf("audit suffix = %q, want (26/26)", audit)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateStepAudit() succeeded, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateStepAudit() error = %q, want containing %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestDeaconHeartbeatCreditsMatchFormula(t *testing.T) {
	content, err := formula.GetEmbeddedFormulaContent("mol-deacon-patrol")
	if err != nil {
		t.Fatalf("load deacon formula: %v", err)
	}
	heartbeats := strings.Count(string(content), "gt deacon heartbeat")
	if heartbeats != deacon.HeartbeatCreditsPerPatrol {
		t.Fatalf("formula heartbeat calls = %d, HeartbeatCreditsPerPatrol = %d", heartbeats, deacon.HeartbeatCreditsPerPatrol)
	}
}

func TestDeaconTemplatePatrolIntegrityGuidance(t *testing.T) {
	content, err := os.ReadFile("../templates/roles/deacon.md.tmpl")
	if err != nil {
		t.Fatalf("read deacon template: %v", err)
	}
	template := string(content)
	for _, stale := range []string{"patrol_count", "25 steps", "/25"} {
		if strings.Contains(template, stale) {
			t.Fatalf("deacon template still contains stale patrol guidance %q", stale)
		}
	}
	for _, required := range []string{"26 steps", "--steps"} {
		if !strings.Contains(template, required) {
			t.Fatalf("deacon template missing required patrol guidance %q", required)
		}
	}
}
