package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTrackingDependsOnID_CrossRigWrapsExternal(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte("{\"prefix\":\"ag-\",\"path\":\"agentcompany/.beads\"}\n"), 0o644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	got := trackingDependsOnID(townRoot, "ag-95s.1")
	want := "external:ag:ag-95s.1"
	if got != want {
		t.Fatalf("trackingDependsOnID() = %q, want %q", got, want)
	}
}

func TestTrackingDependsOnID_HQStaysLocal(t *testing.T) {
	townRoot := t.TempDir()
	got := trackingDependsOnID(townRoot, "hq-cv-test")
	if got != "hq-cv-test" {
		t.Fatalf("trackingDependsOnID() = %q, want %q", got, "hq-cv-test")
	}
}

func TestFallbackTrackingRelationUsesExternalTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte("{\"prefix\":\"ag-\",\"path\":\"agentcompany/.beads\"}\n"), 0o644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "bd.log")
	writeBDStub(t, binDir, `#!/usr/bin/env sh
{
	printf 'args:'
	for arg in "$@"; do
		printf '[%s]' "$arg"
	done
	printf '\n'
} >> "$BD_STUB_LOG"
`, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_STUB_LOG", logPath)

	storeErr := errors.New("store unavailable")
	if err := fallbackTrackingRelation(townRoot, "hq-cv-test", "ag-95s.1", true, storeErr); err != nil {
		t.Fatalf("fallback add: %v", err)
	}
	if err := fallbackTrackingRelation(townRoot, "hq-cv-test", "ag-95s.1", false, storeErr); err != nil {
		t.Fatalf("fallback remove: %v", err)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(logBytes)
	for _, want := range []string{
		"args:[dep][add][hq-cv-test][external:ag:ag-95s.1][--type=tracks]",
		"args:[dep][remove][hq-cv-test][external:ag:ag-95s.1][--type=tracks]",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("bd stub log missing %q:\n%s", want, log)
		}
	}
}
