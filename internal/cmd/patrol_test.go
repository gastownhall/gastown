package cmd

import (
	"strings"
	"testing"
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
		name          string
		input         string
		expected      map[string]string
		wantMalformed []string
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
		{
			name:  "entry without a status is malformed",
			input: "heartbeat:OK,inbox-check",
			expected: map[string]string{
				"heartbeat": "OK",
			},
			wantMalformed: []string{"inbox-check"},
		},
		{
			name:  "repeated id keeps last status and one order slot",
			input: "heartbeat:OK,heartbeat:SKIP",
			expected: map[string]string{
				"heartbeat": "SKIP",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseStepResults(tt.input)
			if len(got.status) != len(tt.expected) {
				t.Errorf("parseStepResults(%q) returned %d entries, want %d", tt.input, len(got.status), len(tt.expected))
				return
			}
			for k, v := range tt.expected {
				if got.status[k] != v {
					t.Errorf("parseStepResults(%q).status[%q] = %q, want %q", tt.input, k, got.status[k], v)
				}
			}
			if len(got.order) != len(tt.expected) {
				t.Errorf("parseStepResults(%q).order = %v, want %d ids", tt.input, got.order, len(tt.expected))
			}
			if len(got.malformed) != len(tt.wantMalformed) {
				t.Errorf("parseStepResults(%q).malformed = %v, want %v", tt.input, got.malformed, tt.wantMalformed)
				return
			}
			for i, want := range tt.wantMalformed {
				if got.malformed[i] != want {
					t.Errorf("parseStepResults(%q).malformed[%d] = %q, want %q", tt.input, i, got.malformed[i], want)
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
		wantErr     bool   // expect an error instead of an audit line
		wantErrHas  []string
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
			// hq-6jz: these four aliases were the real incident. Silently
			// dropping them recorded 18 executed steps as SKIP.
			name:        "unknown step aliases are rejected, not downgraded to SKIP",
			formulaName: "mol-deacon-patrol",
			stepsFlag:   "heartbeat:OK,orphan-cleanup:OK,test-pollution:OK,zombie-detect:OK,dog-pool:OK",
			wantErr:     true,
			wantErrHas: []string{
				"orphan-cleanup", "test-pollution", "zombie-detect", "dog-pool",
				"orphan-process-cleanup", "test-pollution-cleanup", "zombie-scan", "dog-pool-maintenance",
			},
		},
		{
			name:        "known ids alongside an unknown one still fail",
			formulaName: "mol-deacon-patrol",
			stepsFlag:   "heartbeat:OK,not-a-step:OK,inbox-check:OK",
			wantErr:     true,
			wantErrHas:  []string{"not-a-step"},
		},
		{
			name:        "entry missing a status is rejected",
			formulaName: "mol-deacon-patrol",
			stepsFlag:   "heartbeat:OK,inbox-check",
			wantErr:     true,
			wantErrHas:  []string{"inbox-check"},
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
			got, err := buildStepAudit(tt.formulaName, tt.stepsFlag)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("buildStepAudit() = %q, want error", got)
				}
				for _, want := range tt.wantErrHas {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("buildStepAudit() error = %q, want to contain %q", err, want)
					}
				}
				if got != "" {
					t.Errorf("buildStepAudit() = %q, want empty audit alongside error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("buildStepAudit() error = %v, want nil", err)
			}
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
