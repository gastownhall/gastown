package refinery

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/rig"
)

func TestEnsureCanonicalWorktreeNeverFallsBackToMayor(t *testing.T) {
	rigPath := filepath.Join(t.TempDir(), "broken")
	mayorPath := filepath.Join(rigPath, "mayor", "rig")
	if err := os.MkdirAll(mayorPath, 0755); err != nil {
		t.Fatalf("create Mayor path: %v", err)
	}
	markerPath := filepath.Join(mayorPath, "active-wip.txt")
	marker := []byte("Mayor work must remain untouched\n")
	if err := os.WriteFile(markerPath, marker, 0644); err != nil {
		t.Fatalf("write Mayor marker: %v", err)
	}

	manager := NewManager(&rig.Rig{Name: "broken", Path: rigPath})
	var output bytes.Buffer
	manager.SetOutput(&output)
	workDir, err := manager.ensureCanonicalWorktree()
	if err == nil {
		t.Fatal("ensureCanonicalWorktree succeeded without canonical rig config")
	}
	if workDir != "" {
		t.Fatalf("workDir = %q, want empty on canonical topology failure", workDir)
	}
	if strings.Contains(output.String(), "mayor/rig") || strings.Contains(output.String(), "falling back") {
		t.Fatalf("refinery reported a Mayor fallback: %q", output.String())
	}
	got, readErr := os.ReadFile(markerPath)
	if readErr != nil {
		t.Fatalf("read Mayor marker: %v", readErr)
	}
	if !bytes.Equal(got, marker) {
		t.Fatalf("Mayor WIP changed: got %q, want %q", got, marker)
	}
}
