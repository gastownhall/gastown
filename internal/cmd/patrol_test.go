package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestValidateStepAudit(t *testing.T) {
	allDeaconSteps := "heartbeat:OK,inbox-check:OK,orphan-process-cleanup:SKIP,test-pollution-cleanup:OK,gate-evaluation:OK,dispatch-gated-molecules:OK,check-convoy-completion:OK,resolve-external-deps:OK,fire-notifications:OK,heartbeat-mid:OK,health-scan:OK,dolt-health:OK,zombie-scan:OK,plugin-run:OK,dog-pool-maintenance:OK,dog-health-check:OK,orphan-check:OK,session-gc:OK,wisp-compact:OK,compact-report:OK,costs-digest:OK,patrol-digest:OK,log-maintenance:OK,patrol-cleanup:OK,context-check:OK,loop-or-exit:OK"

	tests := []struct {
		name      string
		formula   string
		steps     string
		wantError string
	}{
		{name: "complete audit accepted", formula: "mol-deacon-patrol", steps: allDeaconSteps},
		{name: "empty audit rejected", formula: "mol-deacon-patrol", wantError: "required"},
		{name: "partial audit rejected", formula: "mol-deacon-patrol", steps: "heartbeat:OK", wantError: "missing patrol steps"},
		{name: "unknown step rejected", formula: "mol-deacon-patrol", steps: allDeaconSteps + ",invented:OK", wantError: "unknown patrol step"},
		{name: "malformed entry rejected", formula: "mol-deacon-patrol", steps: "heartbeat", wantError: "step:STATUS"},
		{name: "invalid status rejected", formula: "mol-deacon-patrol", steps: strings.Replace(allDeaconSteps, "heartbeat:OK", "heartbeat:MAYBE", 1), wantError: "invalid patrol step status"},
		{name: "formula lookup failure rejected", formula: "mol-nonexistent", steps: "heartbeat:OK", wantError: "loading patrol formula"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateStepAudit(tt.formula, tt.steps)
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("validateStepAudit() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("validateStepAudit() error = %v, want substring %q", err, tt.wantError)
			}
		})
	}
}

func TestWritePatrolReceipt(t *testing.T) {
	townRoot := t.TempDir()
	info := RoleInfo{Role: RoleWitness, Rig: "sizer", TownRoot: townRoot}
	checkedAt := time.Date(2026, time.July, 15, 2, 15, 0, 0, time.UTC)
	steps := "heartbeat:OK,inbox-check:SKIP"

	path, err := writePatrolReceipt(info, "mol-witness-patrol", "sz-wisp-123", steps, checkedAt)
	if err != nil {
		t.Fatalf("writePatrolReceipt() error = %v", err)
	}
	wantPath := filepath.Join(townRoot, ".runtime", "patrol-receipts", "sizer-witness.json")
	if path != wantPath {
		t.Fatalf("writePatrolReceipt() path = %q, want %q", path, wantPath)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading receipt: %v", err)
	}
	var receipt PatrolReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatalf("decoding receipt: %v", err)
	}
	if !receipt.Complete || receipt.Role != "sizer/witness" || receipt.PatrolID != "sz-wisp-123" {
		t.Fatalf("receipt = %+v", receipt)
	}
	if receipt.CheckedAt != checkedAt.Format(time.RFC3339) {
		t.Fatalf("receipt checked_at = %q", receipt.CheckedAt)
	}
	if receipt.Steps["heartbeat"] != "OK" || receipt.Steps["inbox-check"] != "SKIP" {
		t.Fatalf("receipt steps = %#v", receipt.Steps)
	}
}

func TestPatrolReceiptPathRejectsUnsafeRig(t *testing.T) {
	_, err := patrolReceiptPath(RoleInfo{Role: RoleWitness, Rig: "../other", TownRoot: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "invalid rig") {
		t.Fatalf("patrolReceiptPath() error = %v, want invalid rig", err)
	}
}
