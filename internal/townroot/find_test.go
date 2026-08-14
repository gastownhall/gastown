package townroot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindPrefersOutermostTownJSON(t *testing.T) {
	outer := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outer, "mayor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outer, "mayor", "town.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	inner := filepath.Join(outer, "imported", "gastown")
	if err := os.MkdirAll(filepath.Join(inner, "mayor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inner, "mayor", "town.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	deep := filepath.Join(inner, "crew", "worker")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	if got := Find(deep); got != outer {
		t.Fatalf("Find(%q) = %q, want outermost %q", deep, got, outer)
	}
	if got := Find(inner); got != outer {
		t.Fatalf("Find(inner) = %q, want %q", got, outer)
	}
	if got := Find(outer); got != outer {
		t.Fatalf("Find(outer) = %q, want %q", got, outer)
	}
}

func TestFindReturnsEmptyWhenMissing(t *testing.T) {
	if got := Find(t.TempDir()); got != "" {
		t.Fatalf("Find(non-town) = %q, want empty", got)
	}
}
