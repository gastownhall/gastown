package daemon

import (
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
	dm := &dogMol{rootID: ""}

	// These should not panic or error — graceful degradation.
	dm.closeStep("scan")
	dm.failStep("scan", "test failure")
	dm.close()

	// Nothing is recorded without a root wisp, so there is nothing to report.
	if len(dm.steps) != 0 {
		t.Errorf("steps recorded without a root wisp: %v", dm.steps)
	}
	if got := dm.stepSummary(); got != "" {
		t.Errorf("stepSummary() = %q, want empty", got)
	}
}

func TestDogMolStepSummary(t *testing.T) {
	tests := []struct {
		name string
		run  func(dm *dogMol)
		want string
	}{
		{
			name: "no steps",
			run:  func(*dogMol) {},
			want: "",
		},
		{
			name: "all steps ok, in call order",
			run: func(dm *dogMol) {
				dm.closeStep("scan")
				dm.closeStep("reap")
				dm.closeStep("report")
			},
			want: "scan ok, reap ok, report ok",
		},
		{
			name: "failure carries its reason",
			run: func(dm *dogMol) {
				dm.closeStep("inspect")
				dm.failStep("compact", "2 databases had errors")
			},
			want: "inspect ok, compact failed (2 databases had errors)",
		},
		{
			name: "failure without a reason",
			run: func(dm *dogMol) {
				dm.failStep("sync", "")
			},
			want: "sync failed",
		},
		{
			name: "retried step reports its final outcome once",
			run: func(dm *dogMol) {
				dm.failStep("push", "transient")
				dm.closeStep("push")
			},
			want: "push ok",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dm := &dogMol{rootID: "hq-wisp-abc123"}
			tt.run(dm)
			if got := dm.stepSummary(); got != tt.want {
				t.Errorf("stepSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDogMolStepSummaryTruncates(t *testing.T) {
	dm := &dogMol{rootID: "hq-wisp-abc123"}
	dm.failStep("export", strings.Repeat("x", dogSummaryMaxLen*2))

	got := dm.stepSummary()
	if runes := []rune(got); len(runes) != dogSummaryMaxLen+1 {
		t.Errorf("stepSummary() length = %d runes, want %d", len(runes), dogSummaryMaxLen+1)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("stepSummary() = %q, want an ellipsis suffix", got)
	}
}

func TestTruncateSummaryCountsRunesNotBytes(t *testing.T) {
	// A multi-byte summary must not be cut mid-rune.
	s := strings.Repeat("é", 10)
	if got := truncateSummary(s, 10); got != s {
		t.Errorf("truncateSummary(10 runes, max 10) = %q, want unchanged", got)
	}
	if got := truncateSummary(s, 4); got != strings.Repeat("é", 4)+"…" {
		t.Errorf("truncateSummary(10 runes, max 4) = %q, want 4 runes plus ellipsis", got)
	}
}
