package web

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Every invocation uses an absolute fixture executable, never a live gt or bd.
func convoyFixtureFetcher(t *testing.T, script string) *LiveConvoyFetcher {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell command fixtures")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "gt")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0755); err != nil {
		t.Fatal(err)
	}
	return &LiveConvoyFetcher{townRoot: dir, gtBin: path, cmdTimeout: 5 * time.Second}
}

func TestConvoyListReturnsStructuredErrorOnCommandFailure(t *testing.T) {
	f := convoyFixtureFetcher(t, "exit 1\n")
	_, err := f.FetchConvoys()
	if err == nil || !strings.Contains(err.Error(), "gt convoy list failed") {
		t.Fatalf("expected command failure context, got: %v", err)
	}
}

func TestConvoyListReturnsStructuredErrorOnInvalidJSON(t *testing.T) {
	f := convoyFixtureFetcher(t, "printf '{invalid'\n")
	_, err := f.FetchConvoys()
	if err == nil || !strings.Contains(err.Error(), "parsing convoy list") {
		t.Fatalf("expected parse context, got: %v", err)
	}
}

func TestConvoyListRejectsMissingHydration(t *testing.T) {
	f := convoyFixtureFetcher(t, `echo '[{"id":"hq-cv-missing","title":"Missing tracked data"}]'`)
	_, err := f.FetchConvoys()
	if err == nil || !strings.Contains(err.Error(), "missing tracked issue data") {
		t.Fatalf("missing data must not become a false 0/0 convoy: %v", err)
	}
}

func TestConvoyListBoundsCommandAndClearsCallerDatabase(t *testing.T) {
	t.Setenv("BEADS_DIR", "/wrong/rig/.beads")
	t.Setenv("BEADS_DB", "/wrong/database")
	t.Setenv("BD_DB", "/wrong/database")
	f := convoyFixtureFetcher(t, `
if [ -n "$BEADS_DIR$BEADS_DB$BD_DB" ]; then exit 1; fi
if [ "$PWD/gt" != "$0" ]; then exit 1; fi
echo '[]'
`)
	if _, err := f.FetchConvoys(); err != nil {
		t.Fatal(err)
	}
	f = convoyFixtureFetcher(t, "exec sleep 10\n")
	f.cmdTimeout = 100 * time.Millisecond
	_, err := f.FetchConvoys()
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected bounded command, got: %v", err)
	}
}
