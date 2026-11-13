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

func TestListReportWispsIncludesInfrastructure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script command stubs not supported on Windows")
	}

	binDir := t.TempDir()
	argsLog := filepath.Join(t.TempDir(), "bd-args.log")
	bdScript := `#!/bin/sh
printf '%s\n' "$*" >> "$BD_ARGS_LOG"
case "$*" in
  *list*)
    printf '[{"id":"hq-wisp-patrol","title":"mol-deacon-patrol","status":"hooked","issue_type":"molecule","ephemeral":true,"wisp_type":"patrol"}]\n'
    ;;
  *)
    printf 'bd test stub\n'
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_ARGS_LOG", argsLog)
	beads.ResetBdAllowStaleCacheForTest()
	t.Cleanup(beads.ResetBdAllowStaleCacheForTest)

	wisps, err := listReportWisps(beads.New(t.TempDir()))
	if err != nil {
		t.Fatalf("listReportWisps: %v", err)
	}
	if len(wisps) != 1 || wisps[0].ID != "hq-wisp-patrol" {
		t.Fatalf("wisps = %#v, want patrol infrastructure wisp", wisps)
	}

	args, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatalf("read bd args: %v", err)
	}
	if !strings.Contains(string(args), "--include-infra") {
		t.Fatalf("bd args = %q, want --include-infra", string(args))
	}
}

func TestQueryCompactionReportsReadsPayloadField(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script command stubs not supported on Windows")
	}

	binDir := t.TempDir()
	bdScript := `#!/bin/sh
printf '%s\n' '[{"id":"hq-report","title":"Compaction Report 2026-07-12","payload":"{\"date\":\"2026-07-12\",\"categories\":{\"Patrols\":{\"active\":2}}}"}]'
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	reports, err := queryCompactionReports("2026-07-05", "2026-07-12")
	if err != nil {
		t.Fatalf("queryCompactionReports: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("len(reports) = %d, want 1", len(reports))
	}
	if got := reports[0].Categories["Patrols"].Active; got != 2 {
		t.Fatalf("Patrols.Active = %d, want 2", got)
	}
}

func TestQueryCompactionReportsIncludesClosedEvents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script command stubs not supported on Windows")
	}

	binDir := t.TempDir()
	argsLog := filepath.Join(t.TempDir(), "bd-args.log")
	bdScript := `#!/bin/sh
printf '%s\n' "$*" > "$BD_ARGS_LOG"
printf '%s\n' '[{"id":"hq-report","title":"Compaction Report 2026-07-12","payload":"{\"date\":\"2026-07-12\",\"categories\":{}}"}]'
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_ARGS_LOG", argsLog)

	reports, err := queryCompactionReports("2026-07-05", "2026-07-12")
	if err != nil {
		t.Fatalf("queryCompactionReports: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("len(reports) = %d, want 1 closed event", len(reports))
	}
	args, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatalf("read bd args: %v", err)
	}
	if !strings.Contains(string(args), "--status=all") {
		t.Fatalf("bd args = %q, want --status=all", string(args))
	}
}

func TestQueryCompactionReportsDeduplicatesReportDates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script command stubs not supported on Windows")
	}

	binDir := t.TempDir()
	bdScript := `#!/bin/sh
printf '%s\n' '[{"id":"hq-old","title":"Compaction Report 2026-07-08","created_at":"2026-07-08T00:00:00Z","payload":"{\"date\":\"2026-07-08\",\"categories\":{\"Patrols\":{\"active\":1}}}"},{"id":"hq-new","title":"Compaction Report 2026-07-08","created_at":"2026-07-08T01:00:00Z","payload":"{\"date\":\"2026-07-08\",\"categories\":{\"Patrols\":{\"active\":2}}}"}]'
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	reports, err := queryCompactionReports("2026-07-05", "2026-07-12")
	if err != nil {
		t.Fatalf("queryCompactionReports: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("len(reports) = %d, want 1 unique report date", len(reports))
	}
	if got := reports[0].Categories["Patrols"].Active; got != 2 {
		t.Fatalf("Patrols.Active = %d, want first/latest report value 2", got)
	}
}

func TestQueryCompactionReportsRejectsMatchingEventsWithoutUsablePayload(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script command stubs not supported on Windows")
	}

	binDir := t.TempDir()
	bdScript := `#!/bin/sh
printf '%s\n' '[{"id":"hq-report","title":"Compaction Report 2026-07-12","payload":"not-json"}]'
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := queryCompactionReports("2026-07-05", "2026-07-12")
	if err == nil || !strings.Contains(err.Error(), "no usable payload") {
		t.Fatalf("error = %v, want no usable payload diagnostic", err)
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
			if strings.Contains(a, "0 eligible patrol wisps") {
				found = true
			}
		}
		if !found {
			t.Error("expected zero patrol anomaly, got none")
		}
		for _, a := range anomalies {
			if strings.Contains(a, "patrol agents may be down") {
				t.Fatalf("reporting gap must not claim agent health: %q", a)
			}
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

func TestFormatWeeklyRollupExplainsZeroDayCoverage(t *testing.T) {
	rollup := &weeklyRollup{
		WeekStart: "2026-07-05",
		WeekEnd:   "2026-07-12",
		Totals: map[string]*categoryStats{
			"Heartbeats": {},
			"Patrols":    {},
			"Errors":     {},
			"Untyped":    {},
		},
	}

	md := formatWeeklyRollup(rollup)
	if !strings.Contains(md, "No eligible daily compaction reports") {
		t.Fatalf("zero-day rollup lacks coverage explanation:\n%s", md)
	}
}

func TestNormalizeCompactionAnomalyRemovesUnsupportedHealthClaim(t *testing.T) {
	got := normalizeCompactionAnomaly("0 patrol wisps (patrol agents may be down)")
	if strings.Contains(got, "agents may be down") {
		t.Fatalf("normalized anomaly still claims agent health: %q", got)
	}
	if !strings.Contains(got, "patrol health not assessed") {
		t.Fatalf("normalized anomaly = %q, want reporting-scope explanation", got)
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
			name:  "long hyphenated prefix",
			input: "document-intelligence-0sa\n",
			want:  "document-intelligence-0sa",
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

// writeBdListStub installs a fake `bd` on PATH that logs its args to
// $BD_ARGS_LOG and answers any `list` invocation with listJSON.
//
// The stub honors --status=closed by returning an empty list, mirroring real
// bd: a status-filtered query genuinely cannot see beads on the other side of
// the filter. Without that, a caller that still filters would keep matching
// the fixture and the open-audit-bead cases would pass vacuously.
//
// Returns the args log path.
func writeBdListStub(t *testing.T, listJSON string) string {
	t.Helper()
	binDir := t.TempDir()
	argsLog := filepath.Join(t.TempDir(), "bd-args.log")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$BD_ARGS_LOG"
case "$*" in
  *--status=closed*) printf '[]\n'; exit 0 ;;
esac
printf '%s\n' '` + listJSON + `'
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_ARGS_LOG", argsLog)
	return argsLog
}

func assertBdListArgs(t *testing.T, argsLog string, want ...string) {
	t.Helper()
	args, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatalf("read bd args: %v", err)
	}
	for _, w := range want {
		if !strings.Contains(string(args), w) {
			t.Fatalf("bd args = %q, want %q", string(args), w)
		}
	}
}

// An audit bead is created open and closed a moment later. If that close does
// not stick, a status-filtered lookup cannot see the bead and the digest
// re-fires every patrol cycle — the duplicate-digest bug (hq-3k3).
func TestFindExistingCompactReportMatchesOpenAuditBead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script command stubs not supported on Windows")
	}

	argsLog := writeBdListStub(t, `[{"id":"hq-0m4","title":"Compaction Report 2026-08-26","status":"open","event_kind":"wisp.compaction"}]`)

	id, err := findExistingCompactReport("2026-08-26")
	if err != nil {
		t.Fatalf("findExistingCompactReport: %v", err)
	}
	if id != "hq-0m4" {
		t.Fatalf("id = %q, want hq-0m4 (open audit bead must still suppress the digest)", id)
	}
	// --limit=0 matters too: report beads accumulate one per day forever, so a
	// fixed cap eventually pages today's entry out of the result set.
	assertBdListArgs(t, argsLog, "--status=all", "--limit=0", "--type=event")
}

func TestFindExistingCompactReportMatchesPayloadDateWhenTitleDiffers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script command stubs not supported on Windows")
	}

	writeBdListStub(t, `[{"id":"hq-jg6","title":"Wisp Compaction: 2026-08-26","status":"closed","event_kind":"wisp.compaction","payload":"{\"date\":\"2026-08-26\"}"}]`)

	id, err := findExistingCompactReport("2026-08-26")
	if err != nil {
		t.Fatalf("findExistingCompactReport: %v", err)
	}
	if id != "hq-jg6" {
		t.Fatalf("id = %q, want hq-jg6 from payload date fallback", id)
	}
}

func TestFindExistingCompactReportIgnoresOtherDatesAndKinds(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script command stubs not supported on Windows")
	}

	writeBdListStub(t, `[`+
		`{"id":"hq-yesterday","title":"Compaction Report 2026-08-25","status":"closed","event_kind":"wisp.compaction","payload":"{\"date\":\"2026-08-25\"}"},`+
		`{"id":"hq-weekly","title":"Weekly Compaction Rollup 2026-08-19 to 2026-08-26","status":"closed","event_kind":"wisp.compaction.weekly","payload":"{\"week_start\":\"2026-08-19\",\"week_end\":\"2026-08-26\"}"},`+
		`{"id":"hq-junk","title":"Something else","status":"open","event_kind":"wisp.compaction","payload":"not-json"}`+
		`]`)

	id, err := findExistingCompactReport("2026-08-26")
	if err != nil {
		t.Fatalf("findExistingCompactReport: %v", err)
	}
	if id != "" {
		t.Fatalf("id = %q, want empty (no digest recorded for 2026-08-26)", id)
	}
}

func TestFindExistingWeeklyRollupMatchesOpenBead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script command stubs not supported on Windows")
	}

	argsLog := writeBdListStub(t, `[{"id":"hq-wk","title":"Weekly Compaction Rollup 2026-08-19 to 2026-08-26","status":"open","event_kind":"wisp.compaction.weekly"}]`)

	id, err := findExistingWeeklyRollup("2026-08-19", "2026-08-26")
	if err != nil {
		t.Fatalf("findExistingWeeklyRollup: %v", err)
	}
	if id != "hq-wk" {
		t.Fatalf("id = %q, want hq-wk", id)
	}
	assertBdListArgs(t, argsLog, "--status=all", "--limit=0")
}

func TestFindExistingWeeklyRollupMatchesPayloadWeekWhenTitleDiffers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script command stubs not supported on Windows")
	}

	writeBdListStub(t, `[{"id":"hq-wk2","title":"Weekly Rollup (renamed)","status":"closed","event_kind":"wisp.compaction.weekly","payload":"{\"week_start\":\"2026-08-19\",\"week_end\":\"2026-08-26\"}"}]`)

	id, err := findExistingWeeklyRollup("2026-08-19", "2026-08-26")
	if err != nil {
		t.Fatalf("findExistingWeeklyRollup: %v", err)
	}
	if id != "hq-wk2" {
		t.Fatalf("id = %q, want hq-wk2 from payload week fallback", id)
	}
}

func TestFindExistingWeeklyRollupIgnoresDailyReports(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script command stubs not supported on Windows")
	}

	writeBdListStub(t, `[{"id":"hq-daily","title":"Compaction Report 2026-08-26","status":"closed","event_kind":"wisp.compaction","payload":"{\"date\":\"2026-08-26\"}"}]`)

	id, err := findExistingWeeklyRollup("2026-08-19", "2026-08-26")
	if err != nil {
		t.Fatalf("findExistingWeeklyRollup: %v", err)
	}
	if id != "" {
		t.Fatalf("id = %q, want empty", id)
	}
}
