package pathboundary

import (
	"path/filepath"
	"testing"
)

func TestTestCeiling(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "tmp", "case")
	t.Setenv("GT_TEST_DISCOVERY_CEILING", root)

	if got := TestCeiling(inside); got != root {
		t.Fatalf("TestCeiling(%q) = %q, want %q", inside, got, root)
	}
	if got := TestCeiling(filepath.Dir(root)); got != "" {
		t.Fatalf("outside path unexpectedly bounded by %q", got)
	}
}
