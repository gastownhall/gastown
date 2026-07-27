package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	beadsdk "github.com/steveyegge/beads"
)

func TestPermanentConvoyDispatchErrorClassification(t *testing.T) {
	tests := []struct {
		name string
		err  string
		want convoyDispatchFailureKind
	}{
		{
			name: "respawn exhausted",
			err:  "respawn limit reached for gt-123 (3 attempts)",
			want: convoyDispatchRespawnExhausted,
		},
		{
			name: "ordinary transient dispatch error",
			err:  "tmux server temporarily unavailable",
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyPermanentConvoyDispatchError(tc.err); got != tc.want {
				t.Fatalf("classification = %q, want %q", got, tc.want)
			}
		})
	}

	if got := classifyUnresolvedConvoyRig("old-", ""); got != convoyDispatchRigPrefixUnresolved {
		t.Fatalf("removed/unknown rig prefix classification = %q, want %q", got, convoyDispatchRigPrefixUnresolved)
	}
	if got := classifyUnresolvedConvoyRig("gt-", "gastown"); got != "" {
		t.Fatalf("resolved rig prefix classified permanent: %q", got)
	}
}

func TestConvoyDispatchCircuitPersistsAndRetriesOnlyAfterFingerprintChange(t *testing.T) {
	root := t.TempDir()
	clock := func() time.Time {
		return time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	}
	breaker := newConvoyDispatchCircuitBreaker(root, clock)

	const (
		convoyID    = "hq-convoy"
		issueID     = "gt-123"
		fingerprint = "respawn:3/3"
	)
	if duplicate, err := breaker.Record(
		convoyID, issueID, convoyDispatchRespawnExhausted, fingerprint,
		"respawn limit reached",
	); err != nil {
		t.Fatalf("Record: %v", err)
	} else if duplicate {
		t.Fatal("first permanent failure was marked duplicate")
	}

	statePath := filepath.Join(root, "daemon", "convoy-dispatch-circuits.json")
	if info, err := os.Stat(statePath); err != nil {
		t.Fatalf("persistent state missing: %v", err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("state mode = %o, want 600", info.Mode().Perm())
	}

	reloaded := newConvoyDispatchCircuitBreaker(root, clock)
	if suppress, kind := reloaded.ShouldSuppress(convoyID, issueID, fingerprint); !suppress || kind != convoyDispatchRespawnExhausted {
		t.Fatalf("persisted circuit = (%v, %q), want (true, %q)", suppress, kind, convoyDispatchRespawnExhausted)
	}
	if duplicate, err := reloaded.Record(
		convoyID, issueID, convoyDispatchRespawnExhausted, fingerprint,
		"same failure on next scan",
	); err != nil {
		t.Fatalf("duplicate Record: %v", err)
	} else if !duplicate {
		t.Fatal("same convoy+issue+fingerprint was not deduplicated")
	}

	if suppress, _ := reloaded.ShouldSuppress(convoyID, issueID, "respawn:0/3"); suppress {
		t.Fatal("circuit remained open after relevant respawn fingerprint changed")
	}
	if suppress, _ := reloaded.ShouldSuppress(convoyID, issueID, "respawn:0/3"); suppress {
		t.Fatal("changed fingerprint was not cleared for retry")
	}
}

func TestConvoyRouteFingerprintChangesOnlyForRelevantPrefix(t *testing.T) {
	root := t.TempDir()
	routesDir := filepath.Join(root, ".beads")
	if err := os.MkdirAll(routesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	routesFile := filepath.Join(routesDir, "routes.jsonl")
	if err := os.WriteFile(routesFile, []byte(`{"prefix":"bd-","path":"beads/.beads"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	before := convoyRouteFingerprint(root, "gt-")
	if err := os.WriteFile(routesFile, []byte(
		`{"prefix":"bd-","path":"renamed-beads/.beads"}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if afterIrrelevant := convoyRouteFingerprint(root, "gt-"); afterIrrelevant != before {
		t.Fatalf("unrelated route changed fingerprint: before %q after %q", before, afterIrrelevant)
	}

	if err := os.WriteFile(routesFile, []byte(
		`{"prefix":"bd-","path":"renamed-beads/.beads"}`+"\n"+
			`{"prefix":"gt-","path":"gastown/.beads"}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if afterRelevant := convoyRouteFingerprint(root, "gt-"); afterRelevant == before {
		t.Fatal("relevant route addition did not change fingerprint")
	}
}

func TestConvoyRespawnFingerprintUsesResolvedRigWitnessState(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, ".beads", "routes.jsonl"),
		[]byte(`{"prefix":"gt-","path":"gastown/.beads"}`+"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	for path, count := range map[string]int{
		filepath.Join(root, "witness", "bead-respawn-counts.json"):            1,
		filepath.Join(root, "gastown", "witness", "bead-respawn-counts.json"): 3,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		body := `{"beads":{"gt-123":{"count":` + strconv.Itoa(count) + `}}}`
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := convoyRespawnFingerprint(root, "gt-123"); got != "respawn:3/3" {
		t.Fatalf("fingerprint = %q, want resolved rig count 3/3", got)
	}
}

func TestConvoyTrackedStateFingerprintChangesForApprovedResetState(t *testing.T) {
	base := strandedConvoyInfo{
		ID:           "hq-convoy",
		TrackedCount: 2,
		ReadyCount:   1,
		ReadyIssues:  []string{"gt-123"},
		BaseBranch:   "main",
	}
	before := convoyTrackedStateFingerprint(base)
	changed := base
	changed.ReadyCount = 2
	changed.ReadyIssues = []string{"gt-123", "gt-456"}
	if after := convoyTrackedStateFingerprint(changed); after == before {
		t.Fatal("tracked-issue state change did not reopen fingerprint")
	}
}

func TestConvoyIssueStateFingerprintChangesOnIssueUpdate(t *testing.T) {
	issue := &beadsdk.Issue{
		ID:        "gt-123",
		Status:    beadsdk.StatusOpen,
		UpdatedAt: time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC),
	}
	before := convoyIssueStateFingerprint(issue)
	issue.UpdatedAt = issue.UpdatedAt.Add(time.Minute)
	if after := convoyIssueStateFingerprint(issue); after == before {
		t.Fatal("issue update did not change dispatch state fingerprint")
	}
}

func TestFeedFirstReadyUnknownPrefixCircuitRetriesOnlyAfterRelevantRouteChange(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires Unix")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	routesFile := filepath.Join(root, ".beads", "routes.jsonl")
	if err := os.WriteFile(routesFile, []byte(`{"prefix":"bd-","path":"beads/.beads"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(t.TempDir(), "gt.log")
	gtPath := filepath.Join(t.TempDir(), "gt")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + logPath + "\n"
	if err := os.WriteFile(gtPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	var logs []string
	m := NewConvoyManager(root, func(format string, args ...interface{}) {
		logs = append(logs, format)
	}, gtPath, time.Minute, nil, nil, nil)
	c := strandedConvoyInfo{ID: "hq-convoy", ReadyIssues: []string{"gt-123"}}

	m.feedFirstReady(c) // records and alerts once
	logsAfterLatch := len(logs)
	m.feedFirstReady(c) // unchanged state: quiet
	if len(logs) != logsAfterLatch {
		t.Fatalf("unchanged scan logged again: before=%d after=%d logs=%v", logsAfterLatch, len(logs), logs)
	}
	if err := os.WriteFile(routesFile, []byte(`{"prefix":"bd-","path":"renamed/.beads"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.feedFirstReady(c) // unrelated route change: still quiet
	if len(logs) != logsAfterLatch {
		t.Fatalf("unrelated route change reopened/logged circuit: before=%d after=%d logs=%v", logsAfterLatch, len(logs), logs)
	}

	if err := os.WriteFile(routesFile, []byte(
		`{"prefix":"bd-","path":"renamed/.beads"}`+"\n"+
			`{"prefix":"gt-","path":"gastown/.beads"}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	m.feedFirstReady(c) // relevant route change: retry and dispatch

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var slingCount, mailCount int
	for _, line := range lines {
		if strings.HasPrefix(line, "sling ") {
			slingCount++
		}
		if strings.HasPrefix(line, "mail send --human ") {
			mailCount++
		}
	}
	if slingCount != 1 {
		t.Fatalf("sling count = %d, want 1 after relevant route change; calls=%q", slingCount, data)
	}
	if mailCount != 1 {
		t.Fatalf("alert count = %d, want 1 for unchanged permanent failure; calls=%q", mailCount, data)
	}
}

func TestFeedFirstReadyRespawnCircuitRetriesAfterTargetReset(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires Unix")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, ".beads", "routes.jsonl"),
		[]byte(`{"prefix":"gt-","path":"gastown/.beads"}`+"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	respawnPath := filepath.Join(root, "gastown", "witness", "bead-respawn-counts.json")
	if err := os.MkdirAll(filepath.Dir(respawnPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeRespawnState := func(targetCount, unrelatedCount int) {
		t.Helper()
		body := `{"beads":{"gt-123":{"count":` + strconv.Itoa(targetCount) +
			`},"gt-other":{"count":` + strconv.Itoa(unrelatedCount) + `}}}`
		if err := os.WriteFile(respawnPath, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeRespawnState(3, 0)

	logPath := filepath.Join(t.TempDir(), "gt.log")
	gtPath := filepath.Join(t.TempDir(), "gt")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + logPath + "\n" +
		"if [ \"$1\" = sling ]; then echo 'respawn limit reached for gt-123 (3 attempts)' >&2; exit 1; fi\n"
	if err := os.WriteFile(gtPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	m := NewConvoyManager(root, func(string, ...interface{}) {}, gtPath, time.Minute, nil, nil, nil)
	c := strandedConvoyInfo{
		ID:           "hq-convoy",
		TrackedCount: 1,
		ReadyCount:   1,
		ReadyIssues:  []string{"gt-123"},
	}
	m.feedFirstReady(c)
	m.feedFirstReady(c)
	writeRespawnState(3, 2) // unrelated issue must not reopen
	m.feedFirstReady(c)
	writeRespawnState(0, 2) // explicit target reset reopens
	m.feedFirstReady(c)

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var slingCount, mailCount int
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.HasPrefix(line, "sling ") {
			slingCount++
		}
		if strings.HasPrefix(line, "mail send --human ") {
			mailCount++
		}
	}
	if slingCount != 2 {
		t.Fatalf("sling count = %d, want initial + target-reset retry; calls=%q", slingCount, data)
	}
	if mailCount != 2 {
		t.Fatalf("alert count = %d, want one per distinct fingerprint; calls=%q", mailCount, data)
	}
}

func TestConvoyFailedHumanAlertPersistsAndRetriesWithoutRedispatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires Unix")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, ".beads", "routes.jsonl"),
		[]byte(`{"prefix":"gt-","path":"gastown/.beads"}`+"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "gt.log")
	mailMarker := filepath.Join(t.TempDir(), "mail-failed")
	gtPath := filepath.Join(t.TempDir(), "gt")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + logPath + "\n" +
		"if [ \"$1\" = sling ]; then echo 'respawn limit reached for gt-123 (3 attempts)' >&2; exit 1; fi\n" +
		"if [ \"$1\" = mail ] && [ ! -f " + mailMarker + " ]; then touch " + mailMarker + "; exit 1; fi\n"
	if err := os.WriteFile(gtPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	m := NewConvoyManager(root, func(string, ...interface{}) {}, gtPath, time.Minute, nil, nil, nil)
	c := strandedConvoyInfo{ID: "hq-convoy", TrackedCount: 1, ReadyCount: 1, ReadyIssues: []string{"gt-123"}}

	m.feedFirstReady(c) // sling fails permanently; first mail fails
	m = NewConvoyManager(root, func(string, ...interface{}) {}, gtPath, time.Minute, nil, nil, nil)
	m.feedFirstReady(c) // no sling retry; mail retries successfully
	m.feedFirstReady(c) // circuit and delivered alert both quiet

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var slingCount, mailCount int
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.HasPrefix(line, "sling ") {
			slingCount++
		}
		if strings.HasPrefix(line, "mail send --human ") {
			mailCount++
		}
	}
	if slingCount != 1 || mailCount != 2 {
		t.Fatalf("calls: sling=%d mail=%d, want sling=1 mail=2; all=%q", slingCount, mailCount, data)
	}
}

func TestConvoyCorruptCircuitStateFailsClosedAndAlertsOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires Unix")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "daemon"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		convoyDispatchCircuitStateFile(root),
		[]byte(`{"circuits":`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "gt.log")
	gtPath := filepath.Join(t.TempDir(), "gt")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + logPath + "\n"
	if err := os.WriteFile(gtPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var logs []string
	m := NewConvoyManager(root, func(format string, args ...interface{}) {
		logs = append(logs, format)
	}, gtPath, time.Minute, nil, nil, nil)
	c := strandedConvoyInfo{ID: "hq-convoy", ReadyCount: 1, ReadyIssues: []string{"gt-123"}}

	m.feedFirstReady(c)
	// Recreate the manager to model a daemon restart. The unchanged corrupt
	// state must remain fail-closed without re-alerting.
	m = NewConvoyManager(root, func(format string, args ...interface{}) {
		logs = append(logs, format)
	}, gtPath, time.Minute, nil, nil, nil)
	m.feedFirstReady(c)

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var slingCount, mailCount int
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.HasPrefix(line, "sling ") {
			slingCount++
		}
		if strings.HasPrefix(line, "mail send --human ") {
			mailCount++
		}
	}
	if slingCount != 0 || mailCount != 1 {
		t.Fatalf("corrupt-state calls: sling=%d mail=%d, want sling=0 mail=1; all=%q", slingCount, mailCount, data)
	}
	foundSurface := false
	for _, entry := range logs {
		if strings.Contains(entry, "corrupt") || strings.Contains(entry, "load") {
			foundSurface = true
		}
	}
	if !foundSurface {
		t.Fatalf("corrupt state was not surfaced in logs: %v", logs)
	}
}

func TestConvoyCorruptCircuitAlertFingerprintPersistsAndChanges(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "daemon"), 0o755); err != nil {
		t.Fatal(err)
	}
	statePath := convoyDispatchCircuitStateFile(root)
	if err := os.WriteFile(statePath, []byte(`{"circuits":`), 0o600); err != nil {
		t.Fatal(err)
	}

	breaker := newConvoyDispatchCircuitBreaker(root, time.Now)
	if !breaker.CorruptionAlertPending() {
		t.Fatal("first corruption fingerprint did not require an alert")
	}
	if err := breaker.MarkCorruptionAlertDelivered(); err != nil {
		t.Fatalf("persist corruption alert marker: %v", err)
	}

	reloaded := newConvoyDispatchCircuitBreaker(root, time.Now)
	if reloaded.CorruptionAlertPending() {
		t.Fatal("unchanged corruption fingerprint re-alerted after reload")
	}

	if err := os.WriteFile(statePath, []byte(`{"different":`), 0o600); err != nil {
		t.Fatal(err)
	}
	changed := newConvoyDispatchCircuitBreaker(root, time.Now)
	if !changed.CorruptionAlertPending() {
		t.Fatal("changed corruption fingerprint did not reopen alert")
	}
}
