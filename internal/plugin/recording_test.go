package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPluginRunRecord(t *testing.T) {
	record := PluginRunRecord{
		PluginName: "test-plugin",
		RigName:    "gastown",
		Result:     ResultSuccess,
		Body:       "Test run completed successfully",
	}

	if record.PluginName != "test-plugin" {
		t.Errorf("expected plugin name 'test-plugin', got %q", record.PluginName)
	}
	if record.RigName != "gastown" {
		t.Errorf("expected rig name 'gastown', got %q", record.RigName)
	}
	if record.Result != ResultSuccess {
		t.Errorf("expected result 'success', got %q", record.Result)
	}
}

func TestRecordRunCreatesAndClosesReceipt(t *testing.T) {
	townRoot := t.TempDir()
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "bd-args.log")
	bdPath := filepath.Join(binDir, "bd")
	fakeBD := "#!/usr/bin/env bash\n" +
		"printf '%s\\n' \"$*\" >> \"$BD_ARGS_LOG\"\n" +
		"case \"$1\" in\n" +
		"  create) printf '{\\\"id\\\":\\\"gt-test-run\\\"}\\n' ;;\n" +
		"  close) exit 0 ;;\n" +
		"  *) exit 2 ;;\n" +
		"esac\n"
	if err := os.WriteFile(bdPath, []byte(fakeBD), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_ARGS_LOG", logPath)

	recorder := NewRecorder(townRoot)
	id, err := recorder.RecordRun(PluginRunRecord{
		PluginName:  "tool-updater",
		RigName:     "gastown",
		Result:      RunResult("warning"),
		Title:       "tool-updater: failed=brew",
		Body:        "brew failed",
		ExtraLabels: []string{"source:test"},
	})
	if err != nil {
		t.Fatalf("RecordRun failed: %v", err)
	}
	if id != "gt-test-run" {
		t.Fatalf("RecordRun id = %q, want gt-test-run", id)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake bd log: %v", err)
	}
	log := string(data)
	for _, want := range []string{
		"create --ephemeral --json -t chore --title=tool-updater: failed=brew",
		"-l type:plugin-run",
		"-l plugin:tool-updater",
		"-l result:warning",
		"-l rig:gastown",
		"-l source:test",
		"--description=brew failed",
		"close gt-test-run --reason plugin run recorded",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("fake bd log missing %q in:\n%s", want, log)
		}
	}
}

func TestRunResultConstants(t *testing.T) {
	if ResultSuccess != "success" {
		t.Errorf("expected ResultSuccess to be 'success', got %q", ResultSuccess)
	}
	if ResultFailure != "failure" {
		t.Errorf("expected ResultFailure to be 'failure', got %q", ResultFailure)
	}
	if ResultSkipped != "skipped" {
		t.Errorf("expected ResultSkipped to be 'skipped', got %q", ResultSkipped)
	}
}

func TestNewRecorder(t *testing.T) {
	recorder := NewRecorder("/tmp/test-town")
	if recorder == nil {
		t.Fatal("NewRecorder returned nil")
	}
	if recorder.townRoot != "/tmp/test-town" {
		t.Errorf("expected townRoot '/tmp/test-town', got %q", recorder.townRoot)
	}
}

func TestCooldownDurationParsing(t *testing.T) {
	t.Parallel()
	// Verify that plugin gate durations (Go time.ParseDuration format)
	// are parsed correctly. This is critical because bd's compact duration
	// uses "m" for months, while Go uses "m" for minutes. The fix computes
	// an absolute RFC3339 cutoff instead of passing the raw duration to bd.
	cases := []struct {
		input   string
		wantDur time.Duration
		wantErr bool
	}{
		{"5m", 5 * time.Minute, false},
		{"30m", 30 * time.Minute, false},
		{"1h", 1 * time.Hour, false},
		{"24h", 24 * time.Hour, false},
		{"1h30m", 90 * time.Minute, false},
		{"500ms", 500 * time.Millisecond, false},
		{"bogus", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			d, err := time.ParseDuration(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error for %q, got nil", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.input, err)
			}
			if d != tc.wantDur {
				t.Errorf("ParseDuration(%q) = %v, want %v", tc.input, d, tc.wantDur)
			}
			// Verify the cutoff time is in the past and approximately correct.
			cutoff := time.Now().Add(-d)
			elapsed := time.Since(cutoff)
			if elapsed < d-time.Second || elapsed > d+time.Second {
				t.Errorf("cutoff drift: expected ~%v ago, got %v ago", d, elapsed)
			}
		})
	}
}

// Integration tests for RecordRun, GetLastRun, GetRunsSince require
// a working beads installation and are skipped in unit tests.
// These functions shell out to `bd` commands.

// fakeRunStores installs a fake bd that answers `bd list` (the issues table)
// and `bd sql` (the wisps table) from the given JSON, and returns the path of a
// log holding one line of arguments per invocation.
//
// The fake returns its canned rows whatever filter it is sent, so a test that
// plants a receipt outside the cooldown window is checking that the reader
// itself excludes it.
func fakeRunStores(t *testing.T, issuesJSON, wispsJSON string) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "bd-args.log")
	issuesPath := filepath.Join(dir, "issues.json")
	wispsPath := filepath.Join(dir, "wisps.json")
	if err := os.WriteFile(issuesPath, []byte(issuesJSON), 0644); err != nil {
		t.Fatalf("write issues fixture: %v", err)
	}
	if err := os.WriteFile(wispsPath, []byte(wispsJSON), 0644); err != nil {
		t.Fatalf("write wisps fixture: %v", err)
	}
	fakeBD := "#!/usr/bin/env bash\n" +
		"printf '%s\\n' \"$*\" >> \"$BD_ARGS_LOG\"\n" +
		"case \"$1\" in\n" +
		"  list) cat \"$BD_FAKE_ISSUES\" ;;\n" +
		"  sql)\n" +
		"    if [ -n \"$BD_FAKE_SQL_ERROR\" ]; then\n" +
		"      printf '{\"error\": \"%s\"}\\n' \"$BD_FAKE_SQL_ERROR\" | tee /dev/stderr\n" +
		"      exit 1\n" +
		"    fi\n" +
		"    cat \"$BD_FAKE_WISPS\" ;;\n" +
		"  *) exit 2 ;;\n" +
		"esac\n"
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(fakeBD), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_ARGS_LOG", logPath)
	t.Setenv("BD_FAKE_ISSUES", issuesPath)
	t.Setenv("BD_FAKE_WISPS", wispsPath)
	t.Setenv("BD_FAKE_SQL_ERROR", "")
	return logPath
}

// wispReceipt renders one row of the wisps query as `bd sql --json` emits it.
func wispReceipt(id, title string, created time.Time, labels string) string {
	return fmt.Sprintf(`{"id":%q,"title":%q,"created_at":%q,"labels_csv":%q}`,
		id, title, created.UTC().Format(time.RFC3339), labels)
}

// issueReceipt renders one row of `bd list --json`.
func issueReceipt(id, title string, created time.Time, labels ...string) string {
	quoted := make([]string, len(labels))
	for i, l := range labels {
		quoted[i] = fmt.Sprintf("%q", l)
	}
	return fmt.Sprintf(`{"id":%q,"title":%q,"created_at":%q,"labels":[%s]}`,
		id, title, created.UTC().Format(time.RFC3339), strings.Join(quoted, ","))
}

// TestCooldownGate_WispReceiptInsideWindowClosesGate is the case that shipped
// broken: the only receipt for a recent run is a wisp, `bd list` returns
// nothing, and the gate must still read closed (count > 0).
func TestCooldownGate_WispReceiptInsideWindowClosesGate(t *testing.T) {
	cases := []struct {
		name  string
		title string
	}{
		{"default title", "Plugin run: github-sheriff"},
		// A plugin script's own receipt carries its outcome as the title. It
		// is still a receipt for a run inside the window.
		{"script-authored title", "github-sheriff: found 6 CI failures"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recent := time.Now().Add(-10 * time.Minute)
			wisps := "[" + wispReceipt("hq-wisp-recent", tc.title, recent,
				"type:plugin-run,plugin:github-sheriff,result:success") + "]"
			logPath := fakeRunStores(t, "[]", wisps)

			count, err := NewRecorder(t.TempDir()).CountRunsSince("github-sheriff", "2h")
			if err != nil {
				t.Fatalf("CountRunsSince: %v", err)
			}
			if count != 1 {
				t.Fatalf("CountRunsSince = %d, want 1 (gate closed): a wisp receipt 10m old is inside a 2h cooldown", count)
			}

			log, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatalf("read fake bd log: %v", err)
			}
			for _, want := range []string{
				"sql --json SELECT",
				"FROM wisps w",
				"tl.label = 'type:plugin-run'",
				"pl.label = 'plugin:github-sheriff'",
			} {
				if !strings.Contains(string(log), want) {
					t.Errorf("wisp query missing %q in:\n%s", want, log)
				}
			}
		})
	}
}

// TestCooldownGate_WispReceiptOutsideWindowLeavesGateOpen is the negative: a
// wisp receipt older than the cooldown must not hold the gate closed.
func TestCooldownGate_WispReceiptOutsideWindowLeavesGateOpen(t *testing.T) {
	old := time.Now().Add(-3 * time.Hour)
	wisps := "[" + wispReceipt("hq-wisp-old", "Plugin run: github-sheriff", old,
		"type:plugin-run,plugin:github-sheriff,result:success") + "]"
	fakeRunStores(t, "[]", wisps)

	count, err := NewRecorder(t.TempDir()).CountRunsSince("github-sheriff", "2h")
	if err != nil {
		t.Fatalf("CountRunsSince: %v", err)
	}
	if count != 0 {
		t.Fatalf("CountRunsSince = %d, want 0 (gate open): a wisp receipt 3h old is outside a 2h cooldown", count)
	}
}

// TestCooldownGate_WispQuerySendsUTCCutoff checks the window sent to the wisps
// table: a zoneless UTC timestamp one cooldown in the past.
func TestCooldownGate_WispQuerySendsUTCCutoff(t *testing.T) {
	logPath := fakeRunStores(t, "[]", "[]")

	before := time.Now().Add(-2 * time.Hour).UTC()
	if _, err := NewRecorder(t.TempDir()).CountRunsSince("github-sheriff", "2h"); err != nil {
		t.Fatalf("CountRunsSince: %v", err)
	}
	after := time.Now().Add(-2 * time.Hour).UTC()

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake bd log: %v", err)
	}
	const marker = "w.created_at > '"
	i := strings.Index(string(log), marker)
	if i < 0 {
		t.Fatalf("wisp query has no created_at cutoff in:\n%s", log)
	}
	literal := string(log)[i+len(marker):]
	literal = literal[:strings.Index(literal, "'")]
	cutoff, err := time.Parse(wispSQLTimeLayout, literal)
	if err != nil {
		t.Fatalf("cutoff %q is not a zoneless DATETIME: %v", literal, err)
	}
	if cutoff.Before(before.Add(-time.Second)) || cutoff.After(after.Add(time.Second)) {
		t.Errorf("cutoff %s is not 2h ago in UTC (want between %s and %s)", cutoff, before, after)
	}
}

// TestCooldownGate_LegacyIssueReceiptStillCounts keeps the receipts recorded
// before the changeover readable.
func TestCooldownGate_LegacyIssueReceiptStillCounts(t *testing.T) {
	recent := time.Now().Add(-10 * time.Minute)
	issues := "[" + issueReceipt("hq-legacy", "github-sheriff: found 6 CI failures", recent,
		"type:plugin-run", "plugin:github-sheriff", "result:success") + "]"
	fakeRunStores(t, issues, "[]")

	count, err := NewRecorder(t.TempDir()).CountRunsSince("github-sheriff", "2h")
	if err != nil {
		t.Fatalf("CountRunsSince: %v", err)
	}
	if count != 1 {
		t.Fatalf("CountRunsSince = %d, want 1: an issue-table receipt inside the window must still count", count)
	}
}

// TestGetRunsSince_MergesBothStoresNewestFirst covers history: receipts from
// both stores, newest first, each listed once, results parsed from labels.
func TestGetRunsSince_MergesBothStoresNewestFirst(t *testing.T) {
	now := time.Now()
	issues := "[" +
		issueReceipt("hq-legacy", "legacy run", now.Add(-48*time.Hour), "type:plugin-run", "plugin:p", "result:failure") + "," +
		issueReceipt("hq-both", "seen twice", now.Add(-2*time.Hour), "type:plugin-run", "plugin:p", "result:success") +
		"]"
	wisps := "[" +
		wispReceipt("hq-wisp-new", "newest run", now.Add(-5*time.Minute), "type:plugin-run,plugin:p,result:skipped") + "," +
		wispReceipt("hq-both", "seen twice", now.Add(-2*time.Hour), "type:plugin-run,plugin:p,result:success") +
		"]"
	fakeRunStores(t, issues, wisps)

	runs, err := NewRecorder(t.TempDir()).GetRunsSince("p", "")
	if err != nil {
		t.Fatalf("GetRunsSince: %v", err)
	}
	var got []string
	for _, run := range runs {
		got = append(got, run.ID+"="+string(run.Result))
	}
	want := []string{"hq-wisp-new=skipped", "hq-both=success", "hq-legacy=failure"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("GetRunsSince = %v, want %v", got, want)
	}
}

// TestGetLastRun_ReturnsNewestAcrossStores guards against GetLastRun answering
// from the issues table alone, which is how "last run" froze at the changeover.
func TestGetLastRun_ReturnsNewestAcrossStores(t *testing.T) {
	now := time.Now()
	issues := "[" + issueReceipt("hq-legacy", "legacy run", now.Add(-48*time.Hour),
		"type:plugin-run", "plugin:p", "result:success") + "]"
	wisps := "[" + wispReceipt("hq-wisp-new", "Plugin run: p", now.Add(-5*time.Minute),
		"type:plugin-run,plugin:p,result:success") + "]"
	fakeRunStores(t, issues, wisps)

	last, err := NewRecorder(t.TempDir()).GetLastRun("p")
	if err != nil {
		t.Fatalf("GetLastRun: %v", err)
	}
	if last == nil || last.ID != "hq-wisp-new" {
		t.Fatalf("GetLastRun = %+v, want hq-wisp-new", last)
	}
}

// TestCooldownGate_WispQueryFailureIsAnError: when the wisps table cannot be
// read, the answer is unknown, not "no runs". Returning 0 here would open the
// gate; the daemon skips a plugin whose cooldown check errors.
func TestCooldownGate_WispQueryFailureIsAnError(t *testing.T) {
	fakeRunStores(t, "[]", "[]")
	t.Setenv("BD_FAKE_SQL_ERROR", "query error: dial tcp 127.0.0.1:3307: connect: connection refused")

	count, err := NewRecorder(t.TempDir()).CountRunsSince("github-sheriff", "2h")
	if err == nil {
		t.Fatalf("CountRunsSince = %d with nil error, want an error when the wisps table cannot be read", count)
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("error %q does not carry bd's reason", err)
	}
}

// TestCooldownGate_MissingWispTablesReadsIssuesOnly: a database that predates
// the wisps tables keeps every receipt in the issues table, so their absence
// is not a failure and must not block dispatch.
func TestCooldownGate_MissingWispTablesReadsIssuesOnly(t *testing.T) {
	recent := time.Now().Add(-10 * time.Minute)
	issues := "[" + issueReceipt("hq-legacy", "Plugin run: github-sheriff", recent,
		"type:plugin-run", "plugin:github-sheriff", "result:success") + "]"
	fakeRunStores(t, issues, "[]")
	t.Setenv("BD_FAKE_SQL_ERROR", "query error: Error 1146 (HY000): table not found: wisps")

	count, err := NewRecorder(t.TempDir()).CountRunsSince("github-sheriff", "2h")
	if err != nil {
		t.Fatalf("CountRunsSince: %v", err)
	}
	if count != 1 {
		t.Fatalf("CountRunsSince = %d, want 1 from the issues table", count)
	}
}

// TestQueryWispRuns_EscapesPluginName keeps a plugin name from breaking out of
// the SQL string literal.
func TestQueryWispRuns_EscapesPluginName(t *testing.T) {
	logPath := fakeRunStores(t, "[]", "[]")

	if _, err := NewRecorder(t.TempDir()).GetRunsSince("o'brien", ""); err != nil {
		t.Fatalf("GetRunsSince: %v", err)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake bd log: %v", err)
	}
	if !strings.Contains(string(log), "pl.label = 'plugin:o''brien'") {
		t.Errorf("plugin name not escaped in:\n%s", log)
	}
}
