package beads

import (
	"reflect"
	"testing"
)

func TestInjectDefaultLimit(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "bare list gets unlimited",
			args: []string{"list", "--json"},
			want: []string{"list", "--json", "--limit=0"},
		},
		{
			name: "empty args untouched",
			args: nil,
			want: nil,
		},
		{
			name: "non-list command untouched",
			args: []string{"show", "hq-1234", "--json"},
			want: []string{"show", "hq-1234", "--json"},
		},
		{
			// The critical exclusion: bd mol wisp list rejects --limit outright
			// (exit 1, "unknown flag", no data returned).
			name: "mol wisp list untouched",
			args: []string{"mol", "wisp", "list", "--json"},
			want: []string{"mol", "wisp", "list", "--json"},
		},
		{
			name: "dep list untouched",
			args: []string{"dep", "list", "hq-1234", "--json"},
			want: []string{"dep", "list", "hq-1234", "--json"},
		},
		{
			name: "kv list untouched",
			args: []string{"kv", "list", "--json"},
			want: []string{"kv", "list", "--json"},
		},
		{
			name: "explicit --limit=N wins",
			args: []string{"list", "--limit=5", "--json"},
			want: []string{"list", "--limit=5", "--json"},
		},
		{
			name: "explicit --limit N wins",
			args: []string{"list", "--limit", "5", "--json"},
			want: []string{"list", "--limit", "5", "--json"},
		},
		{
			name: "explicit -n shorthand wins",
			args: []string{"list", "-n", "5", "--json"},
			want: []string{"list", "-n", "5", "--json"},
		},
		{
			name: "explicit glued -n5 shorthand wins",
			args: []string{"list", "-n5", "--json"},
			want: []string{"list", "-n5", "--json"},
		},
		{
			name: "explicit --limit=0 not doubled",
			args: []string{"list", "--limit=0", "--json"},
			want: []string{"list", "--limit=0", "--json"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := InjectDefaultLimit(tt.args)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("InjectDefaultLimit(%q) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

// TestInjectDefaultLimitDoesNotAliasCaller guards the reason InjectDefaultLimit
// copies instead of appending in place. Callers chain it with the other Inject*
// helpers, which share a backing array; appending to an aliased slice can clobber
// a neighbouring argument.
func TestInjectDefaultLimitDoesNotAliasCaller(t *testing.T) {
	backing := make([]string, 2, 4)
	backing[0] = "list"
	backing[1] = "--json"

	// A second view over the same backing array, as a chained helper would hold.
	sibling := append(backing[:2:4], "--flat") //nolint:gocritic // deliberately aliased for the test

	got := InjectDefaultLimit(backing)
	if want := []string{"list", "--json", "--limit=0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("InjectDefaultLimit = %q, want %q", got, want)
	}
	if sibling[2] != "--flat" {
		t.Errorf("InjectDefaultLimit clobbered an aliased slice: sibling[2] = %q, want %q",
			sibling[2], "--flat")
	}
}

func TestHasExplicitLimit(t *testing.T) {
	tests := []struct {
		args []string
		want bool
	}{
		{[]string{"--json"}, false},
		{[]string{"--limit"}, true},
		{[]string{"--limit=0"}, true},
		{[]string{"--limit=50"}, true},
		{[]string{"-n"}, true},
		{[]string{"-n=5"}, true},
		{[]string{"-n5"}, true},
		{[]string{"-n0"}, true},
		// Must not mistake a long flag that merely starts with -n for the
		// glued -nNUM shorthand.
		{[]string{"--name=foo"}, false},
		{[]string{"-name"}, false},
		{[]string{"--no-color"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.args[0], func(t *testing.T) {
			if got := HasExplicitLimit(tt.args); got != tt.want {
				t.Errorf("HasExplicitLimit(%q) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}
