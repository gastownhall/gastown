package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

func TestWispTypeToCategory(t *testing.T) {
	tests := []struct {
		wispType string
		title    string
		want     string
	}{
		{"heartbeat", "", "Heartbeats"},
		{"ping", "", "Heartbeats"},
		{"patrol", "", "Patrols"},
		{"gc_report", "", "Patrols"},
		{"error", "", "Errors"},
		{"recovery", "", "Errors"},
		{"escalation", "", "Errors"},
		{"", "", "Untyped"},
		{"unknown", "", "Untyped"},
		{"default", "", "Untyped"},
		{"", "Patrol report", "Patrols"},
	}

	for _, tc := range tests {
		t.Run(tc.wispType+"/"+tc.title, func(t *testing.T) {
			got := wispTypeToCategory(tc.wispType, tc.title)
			if got != tc.want {
				t.Errorf("wispTypeToCategory(%q, %q) = %q, want %q", tc.wispType, tc.title, got, tc.want)
			}
		})
	}
}

func TestWispTypeToCategory_TitlePatrolFallback(t *testing.T) {
	t.Parallel()
	got := wispTypeToCategory("", "nightly patrol sweep")
	if got != "Patrols" {
		t.Errorf("empty wisp_type + patrol in title = %q, want Patrols", got)
	}
}

func TestBuildReport(t *testing.T) {
	result := &compactResult{
		Deleted: []compactAction{
			{ID: "w-1", Title: "Heartbeat 1", WispType: "heartbeat"},
			{ID: "w-2", Title: "Heartbeat 2", WispType: "heartbeat"},
			{ID: "w-3", Title: "Patrol cycle", WispType: "patrol"},
		},
		Promoted: []compactAction{
			{ID: "w-4", Title: "Stuck error", WispType: "error", Reason: "open past TTL"},
		},
		Skipped: 5,
	}

	activeWisps := []*compactIssue{
		{Issue: beads.Issue{ID: "w-10"}, WispType: "heartbeat"},
		{Issue: beads.Issue{ID: "w-11"}, WispType: "patrol"},
		{Issue: beads.Issue{ID: "w-12"}, WispType: "patrol"},
		{Issue: beads.Issue{ID: "w-13"}, WispType: "error"},
	}

	report := buildReport("2026-02-09", result, activeWisps)

	if report.Date != "2026-02-09" {
		t.Errorf("Date = %q, want %q", report.Date, "2026-02-09")
	}

	// Check heartbeats category
	hb := report.Categories["Heartbeats"]
	if hb.Deleted != 2 {
		t.Errorf("Heartbeats.Deleted = %d, want 2", hb.Deleted)
	}
	if hb.Active != 1 {
		t.Errorf("Heartbeats.Active = %d, want 1", hb.Active)
	}

	// Check patrols category
	p := report.Categories["Patrols"]
	if p.Deleted != 1 {
		t.Errorf("Patrols.Deleted = %d, want 1", p.Deleted)
	}
	if p.Active != 2 {
		t.Errorf("Patrols.Active = %d, want 2", p.Active)
	}

	// Check errors category
	e := report.Categories["Errors"]
	if e.Promoted != 1 {
		t.Errorf("Errors.Promoted = %d, want 1", e.Promoted)
	}
	if e.Active != 1 {
		t.Errorf("Errors.Active = %d, want 1", e.Active)
	}

	// Check promotions list
	if len(report.Promotions) != 1 {
		t.Fatalf("len(Promotions) = %d, want 1", len(report.Promotions))
	}
	if report.Promotions[0].ID != "w-4" {
		t.Errorf("Promotions[0].ID = %q, want %q", report.Promotions[0].ID, "w-4")
	}
}

func TestDetectAnomalies(t *testing.T) {
	t.Run("high heartbeat volume", func(t *testing.T) {
		report := &compactReport{
			Categories: map[string]*categoryStats{
				"Heartbeats": {Deleted: 1500},
				"Patrols":    {Active: 5},
				"Errors":     {},
				"Untyped":    {},
			},
		}
		anomalies := detectAnomalies(report)
		found := false
		for _, a := range anomalies {
			if strings.Contains(a, "heartbeat volume") {
				found = true
			}
		}
		if !found {
			t.Error("expected heartbeat volume anomaly, got none")
		}
	})

	t.Run("zero patrols", func(t *testing.T) {
		report := &compactReport{
			Categories: map[string]*categoryStats{
				"Heartbeats": {Deleted: 100},
				"Patrols":    {Active: 0, Deleted: 0, Promoted: 0},
				"Errors":     {},
				"Untyped":    {},
			},
		}
		anomalies := detectAnomalies(report)
		found := false
		for _, a := range anomalies {
			if strings.Contains(a, "0 patrol wisps") {
				found = true
			}
		}
		if !found {
			t.Error("expected zero patrol anomaly, got none")
		}
	})

	t.Run("high promotion rate", func(t *testing.T) {
		report := &compactReport{
			Categories: map[string]*categoryStats{
				"Heartbeats": {Deleted: 3, Promoted: 15},
				"Patrols":    {Active: 5},
				"Errors":     {},
				"Untyped":    {},
			},
		}
		anomalies := detectAnomalies(report)
		found := false
		for _, a := range anomalies {
			if strings.Contains(a, "promotion rate") {
				found = true
			}
		}
		if !found {
			t.Error("expected high promotion rate anomaly, got none")
		}
	})

	t.Run("no anomalies", func(t *testing.T) {
		report := &compactReport{
			Categories: map[string]*categoryStats{
				"Heartbeats": {Deleted: 100, Active: 20},
				"Patrols":    {Active: 5, Deleted: 10},
				"Errors":     {Active: 2},
				"Untyped":    {},
			},
		}
		anomalies := detectAnomalies(report)
		if len(anomalies) != 0 {
			t.Errorf("expected no anomalies, got %v", anomalies)
		}
	})
}

func TestFormatDailyDigest(t *testing.T) {
	report := &compactReport{
		Date: "2026-02-09",
		Categories: map[string]*categoryStats{
			"Heartbeats": {Deleted: 2847, Promoted: 0, Active: 23},
			"Patrols":    {Deleted: 42, Promoted: 1, Active: 48},
			"Errors":     {Deleted: 2, Promoted: 3, Active: 7},
			"Untyped":    {Deleted: 15, Promoted: 0, Active: 4},
		},
		Promotions: []compactAction{
			{ID: "gt-wisp-abc", Title: "Polecat crash during convoy", Reason: "has comments"},
		},
		Anomalies: []string{"gastown: 3x normal heartbeat volume (possible restart loop)"},
	}

	md := formatDailyDigest(report)

	// Check structure
	if !strings.Contains(md, "## Wisp Compaction: 2026-02-09") {
		t.Error("missing header")
	}
	if !strings.Contains(md, "### Summary") {
		t.Error("missing summary section")
	}
	if !strings.Contains(md, "| Heartbeats | 2847 | 0 | 23 |") {
		t.Error("missing heartbeats row")
	}
	if !strings.Contains(md, "### Promotions") {
		t.Error("missing promotions section")
	}
	if !strings.Contains(md, "gt-wisp-abc") {
		t.Error("missing promotion entry")
	}
	if !strings.Contains(md, "### Anomalies") {
		t.Error("missing anomalies section")
	}
	if !strings.Contains(md, "heartbeat volume") {
		t.Error("missing anomaly entry")
	}
}

func TestFormatDailyDigestEmpty(t *testing.T) {
	report := &compactReport{
		Date: "2026-02-09",
		Categories: map[string]*categoryStats{
			"Heartbeats": {},
			"Patrols":    {},
			"Errors":     {},
			"Untyped":    {},
		},
	}

	md := formatDailyDigest(report)

	// Should have header and summary but no promotions/anomalies sections
	if !strings.Contains(md, "## Wisp Compaction: 2026-02-09") {
		t.Error("missing header")
	}
	if strings.Contains(md, "### Promotions") {
		t.Error("should not have promotions section when empty")
	}
	if strings.Contains(md, "### Anomalies") {
		t.Error("should not have anomalies section when empty")
	}
}

func TestFormatWeeklyRollup(t *testing.T) {
	rollup := &weeklyRollup{
		WeekStart: "2026-02-02",
		WeekEnd:   "2026-02-09",
		Days:      7,
		Totals: map[string]*categoryStats{
			"Heartbeats": {Deleted: 15000, Promoted: 0, Active: 25},
			"Patrols":    {Deleted: 280, Promoted: 5, Active: 50},
			"Errors":     {Deleted: 10, Promoted: 8, Active: 3},
			"Untyped":    {Deleted: 90, Promoted: 2, Active: 6},
		},
		Promotions: 15,
		Anomalies:  []string{"high heartbeat volume on 2026-02-05"},
	}

	md := formatWeeklyRollup(rollup)

	if !strings.Contains(md, "## Weekly Wisp Compaction: 2026-02-02 to 2026-02-09") {
		t.Error("missing header")
	}
	if !strings.Contains(md, "**Days reported:** 7") {
		t.Error("missing days count")
	}
	if !strings.Contains(md, "### Totals") {
		t.Error("missing totals section")
	}
	if !strings.Contains(md, "### Rates") {
		t.Error("missing rates section")
	}
	if !strings.Contains(md, "Promotion rate") {
		t.Error("missing promotion rate")
	}
	if !strings.Contains(md, "### Anomalies This Week") {
		t.Error("missing anomalies section")
	}
}

func TestExtractBeadID(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "clean output",
			input: "hq-1a2b\n",
			want:  "hq-1a2b",
		},
		{
			name: "noisy stdout — beads.role warning before id (regression: GH#2950)",
			// `bd` may print a multi-line warning on stdout when beads.role is
			// unset in the cwd's repo. Without extractBeadID, TrimSpace would
			// capture the whole blob, `bd close <blob>` would fail silently,
			// and the audit bead would stay open — causing one duplicate
			// compaction digest mail per patrol cycle.
			input: "warning: beads.role not configured (GH#2950).\n  Fix: git config beads.role maintainer\n  Or:  git config beads.role contributor\nhq-1a2b\n",
			want:  "hq-1a2b",
		},
		{
			name:  "trailing whitespace",
			input: "  hq-1a2b   \n",
			want:  "hq-1a2b",
		},
		{
			name:  "multi-rig prefix lengths",
			input: "co-rln\n",
			want:  "co-rln",
		},
		{
			name:  "prefix with digit",
			input: "h25-mrd\n",
			want:  "h25-mrd",
		},
		{
			name:  "hyphenated prefix",
			input: "my-rig-abc123\n",
			want:  "my-rig-abc123",
		},
		{
			name:    "no id present",
			input:   "warning: something broke\n",
			wantErr: true,
		},
		{
			name:    "empty",
			input:   "",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := extractBeadID(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil (id=%q)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRunDailyDigestStopsBeforeMailWhenAuditCloseFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script command stubs not supported on Windows")
	}
	mailLog := setupCompactReportCommandStubs(t)
	resetCompactReportFlags(t)
	compactReportDate = "2026-05-15"

	err := runDailyDigest()
	if err == nil {
		t.Fatal("want audit bead close error, got nil")
	}
	if !strings.Contains(err.Error(), "auto-closing report bead h25-mrd") {
		t.Fatalf("error = %v, want auto-close failure", err)
	}
	assertNoMailSent(t, mailLog)
}

func TestRunWeeklyRollupStopsBeforeMailWhenAuditCloseFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script command stubs not supported on Windows")
	}
	mailLog := setupCompactReportCommandStubs(t)
	resetCompactReportFlags(t)

	err := runWeeklyRollup()
	if err == nil {
		t.Fatal("want audit bead close error, got nil")
	}
	if !strings.Contains(err.Error(), "auto-closing rollup bead h25-mrd") {
		t.Fatalf("error = %v, want auto-close failure", err)
	}
	assertNoMailSent(t, mailLog)
}

func resetCompactReportFlags(t *testing.T) {
	oldDryRun := compactReportDryRun
	oldWeekly := compactReportWeekly
	oldVerbose := compactReportVerbose
	oldDate := compactReportDate
	oldJSON := compactReportJSON

	compactReportDryRun = false
	compactReportWeekly = false
	compactReportVerbose = false
	compactReportDate = ""
	compactReportJSON = false

	t.Cleanup(func() {
		compactReportDryRun = oldDryRun
		compactReportWeekly = oldWeekly
		compactReportVerbose = oldVerbose
		compactReportDate = oldDate
		compactReportJSON = oldJSON
	})
}

func setupCompactReportCommandStubs(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	mailLog := filepath.Join(t.TempDir(), "mail.log")

	bdScript := `#!/bin/sh
case "$1" in
  list)
    printf '[]\n'
    ;;
  create)
    printf 'warning: beads.role not configured (GH#2950).\n  Fix: git config beads.role maintainer\n  Or:  git config beads.role contributor\nh25-mrd\n'
    ;;
  close)
    echo 'close failed' >&2
    exit 1
    ;;
  *)
    echo "unexpected bd command: $*" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}

	gtScript := `#!/bin/sh
if [ "$1" = "compact" ]; then
  printf '{"promoted":[],"deleted":[],"skipped":0}\n'
  exit 0
fi
if [ "$1" = "mail" ]; then
  echo "$*" >> "$MAIL_LOG"
  exit 0
fi
echo "unexpected gt command: $*" >&2
exit 1
`
	if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte(gtScript), 0755); err != nil {
		t.Fatalf("write fake gt: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MAIL_LOG", mailLog)
	return mailLog
}

func assertNoMailSent(t *testing.T, mailLog string) {
	t.Helper()
	data, err := os.ReadFile(mailLog)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatalf("read mail log: %v", err)
	}
	if len(data) > 0 {
		t.Fatalf("mail was sent unexpectedly: %s", string(data))
	}
}
