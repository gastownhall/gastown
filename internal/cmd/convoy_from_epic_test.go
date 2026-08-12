package cmd

import (
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	convoyops "github.com/steveyegge/gastown/internal/convoy"
)

// ---------------------------------------------------------------------------
// Flag registration tests
// ---------------------------------------------------------------------------

func TestConvoyCreate_FromEpicFlagExists(t *testing.T) {
	flag := convoyCreateCmd.Flags().Lookup("from-epic")
	if flag == nil {
		t.Fatal("convoyCreateCmd should have --from-epic flag")
	}
	if flag.DefValue != "" {
		t.Errorf("--from-epic default = %q, want empty string", flag.DefValue)
	}
}

// ---------------------------------------------------------------------------
// collectEpicChildren tests
// ---------------------------------------------------------------------------

func TestCollectEpicChildren_NotAnEpic(t *testing.T) {
	err := validateEpicType("gt-task-1", "task")
	if err == nil {
		t.Fatal("expected error for non-epic type")
	}
	if !strings.Contains(err.Error(), "not an epic") {
		t.Errorf("error should mention 'not an epic', got: %s", err)
	}
	if !strings.Contains(err.Error(), "task") {
		t.Errorf("error should mention actual type 'task', got: %s", err)
	}
}

// ---------------------------------------------------------------------------
// Merge validation (shared by create and from-epic paths)
// ---------------------------------------------------------------------------

func TestConvoyCreate_InvalidMergeFlag(t *testing.T) {
	tests := []struct {
		value   string
		wantErr bool
	}{
		{"direct", false},
		{"mr", false},
		{"local", false},
		{"", false},
		{"invalid", true},
		{"DIRECT", true},
		{"merge", true},
	}

	for _, tt := range tests {
		err := validateConvoyMerge(tt.value)

		if tt.wantErr && err == nil {
			t.Errorf("merge=%q: expected error, got nil", tt.value)
		}
		if !tt.wantErr && err != nil {
			t.Errorf("merge=%q: unexpected error: %v", tt.value, err)
		}
	}
	convoyMerge = "" // reset
}

// ---------------------------------------------------------------------------
// Args validation
// ---------------------------------------------------------------------------

func TestConvoyCreate_NoArgsNoFlag(t *testing.T) {
	// Reset flags
	convoyFromEpic = ""
	convoyMerge = ""

	err := runConvoyCreate(convoyCreateCmd, []string{})
	if err == nil {
		t.Fatal("expected error with no args and no --from-epic")
	}
	if !strings.Contains(err.Error(), "at least one argument") {
		t.Errorf("error should mention missing args, got: %s", err)
	}
}

func TestConvoyCreate_FromEpicWithOverrideName(t *testing.T) {
	// Verify the command accepts positional args alongside --from-epic
	// (ArbitraryArgs allows this). The actual from-epic logic needs bd,
	// so we just verify the command definition allows it.
	if convoyCreateCmd.Args == nil {
		t.Fatal("convoyCreateCmd.Args should not be nil")
	}
	// cobra.ArbitraryArgs accepts any number of args including zero
	if err := convoyCreateCmd.Args(convoyCreateCmd, []string{}); err != nil {
		t.Errorf("ArbitraryArgs should accept zero args, got: %v", err)
	}
	if err := convoyCreateCmd.Args(convoyCreateCmd, []string{"Custom Name"}); err != nil {
		t.Errorf("ArbitraryArgs should accept one arg, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// IsSlingableType coverage for from-epic filtering
// ---------------------------------------------------------------------------

func TestFromEpic_SlingableTypeFiltering(t *testing.T) {
	// Verify the types that collectEpicChildren would include/exclude
	slingable := []string{"task", "bug", "feature", "chore"}
	nonSlingable := []string{"epic", "decision"}

	for _, typ := range slingable {
		if !convoyops.IsSlingableType(typ) {
			t.Errorf("type %q should be slingable", typ)
		}
	}
	for _, typ := range nonSlingable {
		if convoyops.IsSlingableType(typ) {
			t.Errorf("type %q should NOT be slingable", typ)
		}
	}
}

// ---------------------------------------------------------------------------
// Flag-like title guard (shared path)
// ---------------------------------------------------------------------------

func TestFromEpic_FlagLikeTitleGuard(t *testing.T) {
	tests := []struct {
		title    string
		flagLike bool
	}{
		{"Implement auth system", false},
		{"--drop-database", true},
		{"-f", true},
		{"Normal title", false},
	}

	for _, tt := range tests {
		got := beads.IsFlagLikeTitle(tt.title)
		if got != tt.flagLike {
			t.Errorf("IsFlagLikeTitle(%q) = %v, want %v", tt.title, got, tt.flagLike)
		}
	}
}
