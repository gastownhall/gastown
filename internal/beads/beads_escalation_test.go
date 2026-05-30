package beads

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFormatEscalationDescription(t *testing.T) {
	tests := []struct {
		name   string
		title  string
		fields *EscalationFields
		want   []string
		notIn  []string
	}{
		{
			name:   "nil fields returns title only",
			title:  "Test Escalation",
			fields: nil,
			want:   []string{"Test Escalation"},
			notIn:  []string{"severity:"},
		},
		{
			name:  "basic escalation",
			title: "Build failure",
			fields: &EscalationFields{
				Severity:    "high",
				Reason:      "Build failed 3 times",
				Source:      "patrol:deacon",
				EscalatedBy: "gastown/deacon",
				EscalatedAt: "2024-01-15T10:00:00Z",
			},
			want: []string{
				"Build failure",
				"severity: high",
				"reason: Build failed 3 times",
				"source: patrol:deacon",
				"escalated_by: gastown/deacon",
				"escalated_at: 2024-01-15T10:00:00Z",
			},
		},
		{
			name:  "acknowledged escalation",
			title: "Agent stuck",
			fields: &EscalationFields{
				Severity:    "medium",
				Reason:      "Agent not responding",
				EscalatedBy: "gastown/witness",
				EscalatedAt: "2024-01-15T10:00:00Z",
				AckedBy:     "gastown/crew/joe",
				AckedAt:     "2024-01-15T10:05:00Z",
			},
			want: []string{
				"severity: medium",
				"acked_by: gastown/crew/joe",
				"acked_at: 2024-01-15T10:05:00Z",
			},
		},
		{
			name:  "closed escalation",
			title: "Disk full",
			fields: &EscalationFields{
				Severity:     "critical",
				Reason:       "Disk >95%",
				EscalatedBy:  "gastown/deacon",
				EscalatedAt:  "2024-01-15T10:00:00Z",
				ClosedBy:     "human",
				ClosedReason: "Cleaned up temp files",
			},
			want: []string{
				"closed_by: human",
				"closed_reason: Cleaned up temp files",
			},
		},
		{
			name:  "null fields formatted explicitly",
			title: "New escalation",
			fields: &EscalationFields{
				Severity:    "low",
				Reason:      "Minor issue",
				EscalatedBy: "test",
				EscalatedAt: "2024-01-01T00:00:00Z",
			},
			want: []string{
				"acked_by: null",
				"acked_at: null",
				"closed_by: null",
				"closed_reason: null",
				"related_bead: null",
				"original_severity: null",
			},
		},
		{
			name:  "reescalation fields",
			title: "Bumped escalation",
			fields: &EscalationFields{
				Severity:          "high",
				Reason:            "Stale for 2h",
				EscalatedBy:       "patrol",
				EscalatedAt:       "2024-01-15T08:00:00Z",
				OriginalSeverity:  "low",
				ReescalationCount: 2,
				LastReescalatedAt: "2024-01-15T10:00:00Z",
				LastReescalatedBy: "deacon",
			},
			want: []string{
				"original_severity: low",
				"reescalation_count: 2",
				"last_reescalated_at: 2024-01-15T10:00:00Z",
				"last_reescalated_by: deacon",
			},
		},
		{
			name:  "fingerprint field",
			title: "Repeated alert",
			fields: &EscalationFields{
				Severity:    "medium",
				Reason:      "control-plane timeout",
				EscalatedBy: "deacon",
				EscalatedAt: "2024-01-15T10:00:00Z",
				Fingerprint: "escalation-fp:abc123def456",
			},
			want: []string{
				"fingerprint: escalation-fp:abc123def456",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatEscalationDescription(tt.title, tt.fields)
			for _, line := range tt.want {
				if !strings.Contains(got, line) {
					t.Errorf("missing line %q in output:\n%s", line, got)
				}
			}
			for _, line := range tt.notIn {
				if strings.Contains(got, line) {
					t.Errorf("unexpected %q in output:\n%s", line, got)
				}
			}
		})
	}
}

func TestParseEscalationFields(t *testing.T) {
	tests := []struct {
		name string
		desc string
		want *EscalationFields
	}{
		{
			name: "empty description",
			desc: "",
			want: &EscalationFields{},
		},
		{
			name: "full escalation",
			desc: `Escalation: Build failure

severity: high
reason: Build failed 3 times
source: patrol:deacon
escalated_by: gastown/deacon
escalated_at: 2024-01-15T10:00:00Z
acked_by: gastown/crew/joe
acked_at: 2024-01-15T10:05:00Z
closed_by: null
closed_reason: null
related_bead: gt-abc123
original_severity: medium
reescalation_count: 1
last_reescalated_at: 2024-01-15T09:30:00Z
last_reescalated_by: deacon
fingerprint: escalation-fp:abc123def456`,
			want: &EscalationFields{
				Severity:          "high",
				Reason:            "Build failed 3 times",
				Source:            "patrol:deacon",
				EscalatedBy:       "gastown/deacon",
				EscalatedAt:       "2024-01-15T10:00:00Z",
				AckedBy:           "gastown/crew/joe",
				AckedAt:           "2024-01-15T10:05:00Z",
				ClosedBy:          "",
				ClosedReason:      "",
				RelatedBead:       "gt-abc123",
				OriginalSeverity:  "medium",
				ReescalationCount: 1,
				LastReescalatedAt: "2024-01-15T09:30:00Z",
				LastReescalatedBy: "deacon",
				Fingerprint:       "escalation-fp:abc123def456",
			},
		},
		{
			name: "null values become empty strings",
			desc: "severity: critical\nsource: null\nacked_by: null",
			want: &EscalationFields{
				Severity: "critical",
				Source:   "",
				AckedBy:  "",
			},
		},
		{
			name: "invalid reescalation_count ignored",
			desc: "severity: low\nreescalation_count: not-a-number",
			want: &EscalationFields{
				Severity:          "low",
				ReescalationCount: 0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseEscalationFields(tt.desc)
			if got.Severity != tt.want.Severity {
				t.Errorf("Severity = %q, want %q", got.Severity, tt.want.Severity)
			}
			if got.Reason != tt.want.Reason {
				t.Errorf("Reason = %q, want %q", got.Reason, tt.want.Reason)
			}
			if got.Source != tt.want.Source {
				t.Errorf("Source = %q, want %q", got.Source, tt.want.Source)
			}
			if got.EscalatedBy != tt.want.EscalatedBy {
				t.Errorf("EscalatedBy = %q, want %q", got.EscalatedBy, tt.want.EscalatedBy)
			}
			if got.EscalatedAt != tt.want.EscalatedAt {
				t.Errorf("EscalatedAt = %q, want %q", got.EscalatedAt, tt.want.EscalatedAt)
			}
			if got.AckedBy != tt.want.AckedBy {
				t.Errorf("AckedBy = %q, want %q", got.AckedBy, tt.want.AckedBy)
			}
			if got.AckedAt != tt.want.AckedAt {
				t.Errorf("AckedAt = %q, want %q", got.AckedAt, tt.want.AckedAt)
			}
			if got.ClosedBy != tt.want.ClosedBy {
				t.Errorf("ClosedBy = %q, want %q", got.ClosedBy, tt.want.ClosedBy)
			}
			if got.ClosedReason != tt.want.ClosedReason {
				t.Errorf("ClosedReason = %q, want %q", got.ClosedReason, tt.want.ClosedReason)
			}
			if got.RelatedBead != tt.want.RelatedBead {
				t.Errorf("RelatedBead = %q, want %q", got.RelatedBead, tt.want.RelatedBead)
			}
			if got.OriginalSeverity != tt.want.OriginalSeverity {
				t.Errorf("OriginalSeverity = %q, want %q", got.OriginalSeverity, tt.want.OriginalSeverity)
			}
			if got.ReescalationCount != tt.want.ReescalationCount {
				t.Errorf("ReescalationCount = %d, want %d", got.ReescalationCount, tt.want.ReescalationCount)
			}
			if got.LastReescalatedAt != tt.want.LastReescalatedAt {
				t.Errorf("LastReescalatedAt = %q, want %q", got.LastReescalatedAt, tt.want.LastReescalatedAt)
			}
			if got.LastReescalatedBy != tt.want.LastReescalatedBy {
				t.Errorf("LastReescalatedBy = %q, want %q", got.LastReescalatedBy, tt.want.LastReescalatedBy)
			}
			if got.Fingerprint != tt.want.Fingerprint {
				t.Errorf("Fingerprint = %q, want %q", got.Fingerprint, tt.want.Fingerprint)
			}
		})
	}
}

func TestEscalationFieldsRoundTrip(t *testing.T) {
	original := &EscalationFields{
		Severity:          "high",
		Reason:            "Agent stuck for 1h",
		Source:            "patrol:witness",
		EscalatedBy:       "gastown/witness",
		EscalatedAt:       "2024-06-15T12:00:00Z",
		AckedBy:           "gastown/crew/joe",
		AckedAt:           "2024-06-15T12:05:00Z",
		RelatedBead:       "gt-stuck123",
		OriginalSeverity:  "medium",
		ReescalationCount: 1,
		LastReescalatedAt: "2024-06-15T11:30:00Z",
		LastReescalatedBy: "deacon",
		Fingerprint:       "escalation-fp:feedface1234",
	}

	formatted := FormatEscalationDescription("Escalation: Agent stuck", original)
	parsed := ParseEscalationFields(formatted)

	if parsed.Severity != original.Severity {
		t.Errorf("Severity: got %q, want %q", parsed.Severity, original.Severity)
	}
	if parsed.Reason != original.Reason {
		t.Errorf("Reason: got %q, want %q", parsed.Reason, original.Reason)
	}
	if parsed.Source != original.Source {
		t.Errorf("Source: got %q, want %q", parsed.Source, original.Source)
	}
	if parsed.EscalatedBy != original.EscalatedBy {
		t.Errorf("EscalatedBy: got %q, want %q", parsed.EscalatedBy, original.EscalatedBy)
	}
	if parsed.EscalatedAt != original.EscalatedAt {
		t.Errorf("EscalatedAt: got %q, want %q", parsed.EscalatedAt, original.EscalatedAt)
	}
	if parsed.AckedBy != original.AckedBy {
		t.Errorf("AckedBy: got %q, want %q", parsed.AckedBy, original.AckedBy)
	}
	if parsed.AckedAt != original.AckedAt {
		t.Errorf("AckedAt: got %q, want %q", parsed.AckedAt, original.AckedAt)
	}
	if parsed.RelatedBead != original.RelatedBead {
		t.Errorf("RelatedBead: got %q, want %q", parsed.RelatedBead, original.RelatedBead)
	}
	if parsed.OriginalSeverity != original.OriginalSeverity {
		t.Errorf("OriginalSeverity: got %q, want %q", parsed.OriginalSeverity, original.OriginalSeverity)
	}
	if parsed.ReescalationCount != original.ReescalationCount {
		t.Errorf("ReescalationCount: got %d, want %d", parsed.ReescalationCount, original.ReescalationCount)
	}
	if parsed.LastReescalatedAt != original.LastReescalatedAt {
		t.Errorf("LastReescalatedAt: got %q, want %q", parsed.LastReescalatedAt, original.LastReescalatedAt)
	}
	if parsed.LastReescalatedBy != original.LastReescalatedBy {
		t.Errorf("LastReescalatedBy: got %q, want %q", parsed.LastReescalatedBy, original.LastReescalatedBy)
	}
	if parsed.Fingerprint != original.Fingerprint {
		t.Errorf("Fingerprint: got %q, want %q", parsed.Fingerprint, original.Fingerprint)
	}
}

func TestFilterEscalationRecordsSkipsMailMessages(t *testing.T) {
	issues := []*Issue{
		{ID: "hq-root", Labels: []string{"gt:escalation"}},
		{ID: "hq-mail", Labels: []string{"gt:escalation", "gt:message"}},
	}

	got := filterEscalationRecords(issues)
	if len(got) != 1 || got[0].ID != "hq-root" {
		t.Fatalf("filterEscalationRecords() = %#v, want only root escalation", got)
	}
}

func TestBumpSeverity(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"low", "medium"},
		{"medium", "high"},
		{"high", "critical"},
		{"critical", "critical"}, // already at max
		{"unknown", "critical"},  // default fallthrough
		{"", "critical"},         // empty defaults to critical
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := bumpSeverity(tt.input)
			if got != tt.want {
				t.Errorf("bumpSeverity(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestCreateEscalationBead_PassesDescriptionViaStdin verifies that
// CreateEscalationBead passes the multi-line description through bd's stdin
// (--body-file=-) rather than embedding newlines in --description=...
//
// Regression test for dc-1bxe: bd 1.0.3+ rejects newlines inside --description
// flag values, which broke `gt escalate` for any escalation containing the
// structured YAML metadata block (severity, reason, escalated_by, etc.).
func TestCreateEscalationBead_PassesDescriptionViaStdin(t *testing.T) {
	stubDir := t.TempDir()
	argsPath := filepath.Join(stubDir, "args.txt")
	stdinPath := filepath.Join(stubDir, "stdin.txt")

	// Stub bd: write each arg on its own line to args.txt, capture stdin to
	// stdin.txt, and emit a minimal valid issue JSON so unmarshal succeeds.
	stubScript := `#!/bin/sh
for a in "$@"; do
  printf '%s\n' "$a" >> "` + argsPath + `"
done
cat > "` + stdinPath + `"
echo '{"id":"dc-test1","title":"x","status":"open","priority":2,"type":"task","labels":["gt:escalation"]}'
exit 0
`
	stubPath := filepath.Join(stubDir, "bd")
	if err := os.WriteFile(stubPath, []byte(stubScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Reset --allow-stale capability cache so the stub gets probed fresh.
	ResetBdAllowStaleCacheForTest()

	b := New(t.TempDir())
	fields := &EscalationFields{
		Severity:    "high",
		Reason:      "multi-line\nreason\nwith embedded newlines",
		EscalatedBy: "test/agent",
		EscalatedAt: "2026-05-08T15:00:00Z",
		Fingerprint: "escalation-fp:abc123def456",
	}

	if _, err := b.CreateEscalationBead("Test escalation", fields); err != nil {
		t.Fatalf("CreateEscalationBead: %v", err)
	}

	argsData, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read stub args: %v", err)
	}
	args := string(argsData)

	// Must use --body-file=- to read description from stdin.
	if !strings.Contains(args, "--body-file=-") {
		t.Errorf("expected --body-file=- in bd args, got:\n%s", args)
	}
	if !strings.Contains(args, "--labels=escalation-fp:abc123def456") {
		t.Errorf("expected fingerprint label in bd args, got:\n%s", args)
	}
	// Must NOT pass --description=... at all (any --description value would
	// embed the newline-containing structured description and fail bd 1.0.3+).
	for _, line := range strings.Split(args, "\n") {
		if strings.HasPrefix(line, "--description=") {
			t.Errorf("--description=... must not be used (bd rejects newlines), got %q", line)
		}
	}

	stdinData, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatalf("read stub stdin: %v", err)
	}
	stdin := string(stdinData)
	// The structured description must reach bd via stdin.
	wantInStdin := []string{
		"Test escalation",
		"severity: high",
		"escalated_by: test/agent",
	}
	for _, want := range wantInStdin {
		if !strings.Contains(stdin, want) {
			t.Errorf("expected stdin to contain %q, got:\n%s", want, stdin)
		}
	}
	// Sanity: stdin must contain newlines (it's the multi-line description).
	if !strings.Contains(stdin, "\n") {
		t.Errorf("expected stdin to be multi-line, got %q", stdin)
	}
}
