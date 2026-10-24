package web

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestFetchConvoysHydratesExternalTracks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell command fixtures")
	}
	// Both executables are fixtures. No real gt, bd, tmux or database is used.
	withMayorFetcherHooks(t, nil, func(time.Duration, string, ...string) (*bytes.Buffer, error) {
		return bytes.NewBufferString(""), nil
	})
	dir := t.TempDir()
	bdPath, gtPath := filepath.Join(dir, "bd"), filepath.Join(dir, "gt")
	legacy := `#!/bin/sh
case "$1" in
list) echo '[{"id":"hq-cv-cross","title":"Cross-rig convoy","issue_type":"convoy","status":"open"}]';;
dep) echo '[]';;
*) exit 1;;
esac
`
	canonical := `#!/bin/sh
if [ "$*" != "convoy list --status=open --json" ]; then exit 1; fi
echo '[{"id":"hq-cv-cross","title":"Cross-rig convoy","status":"open","completed":6,"total":7,"tracked":[
{"id":"rig-task.1","title":"One","status":"closed"},
{"id":"rig-task.2","title":"Two","status":"closed"},
{"id":"rig-task.3","title":"Three","status":"closed"},
{"id":"rig-task.4","title":"Four","status":"closed"},
{"id":"rig-task.5","title":"Five","status":"open"},
{"id":"rig-task.6","title":"Six","status":"closed"},
{"id":"rig-task.7","title":"Seven","status":"closed"}]}]'
`
	for path, script := range map[string]string{bdPath: legacy, gtPath: canonical} {
		if err := os.WriteFile(path, []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
	}
	f := &LiveConvoyFetcher{townRoot: dir, bdBin: bdPath, gtBin: gtPath, cmdTimeout: 5 * time.Second}
	rows, err := f.FetchConvoys()
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	row := rows[0]
	if row.Progress != "6/7" || row.Completed != 6 || row.Total != 7 || row.ReadyBeads != 1 || len(row.TrackedIssues) != 7 {
		t.Fatalf("lost cross-rig tracked issues: %+v", row)
	}
	for i, issue := range row.TrackedIssues {
		if issue.ID != fmt.Sprintf("rig-task.%d", i+1) {
			t.Fatalf("lost real identity: %+v", issue)
		}
	}
}

func TestFetchConvoysPreservesUnavailableTrackedIDs(t *testing.T) {
	withMayorFetcherHooks(t, nil, func(time.Duration, string, ...string) (*bytes.Buffer, error) {
		return bytes.NewBufferString(""), nil
	})
	f := convoyFixtureFetcher(t, `echo '[{"id":"hq-cv-partial","title":"Partial","status":"open","tracked":[
{"id":"hq-complete","title":"Done","status":"closed"},
{"id":"external:rig:rig-missing","status":"unknown"},
{"id":"rig-blocked","title":"Blocked","status":"open","blocked":true}]}]'`)
	rows, err := f.FetchConvoys()
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	row := rows[0]
	if row.Total != 3 || row.Completed != 1 || row.UnknownBeads != 1 || row.ReadyBeads != 0 || row.WorkStatus != "unknown" {
		t.Fatalf("unavailable/blocked work became ready or disappeared: %+v", row)
	}
	if row.TrackedIssues[1].ID != "rig-missing" || row.TrackedIssues[1].Status != "unknown" {
		t.Fatalf("lost unresolved cross-rig identity: %+v", row.TrackedIssues[1])
	}
	h, err := NewConvoyHandler(&MockConvoyFetcher{Convoys: rows}, time.Second, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(w.Body.String(), "Unknown (1)") || !strings.Contains(w.Body.String(), "1/3") {
		t.Fatal("partial hydration must be visible with its true total")
	}
}
