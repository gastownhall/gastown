package mail

import (
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

func TestMatchPattern(t *testing.T) {
	tests := []struct {
		pattern string
		address string
		want    bool
	}{
		// Exact matches
		{"gastown/witness", "gastown/witness", true},
		{"mayor/", "mayor/", true},

		// Wildcard matches
		{"*/witness", "gastown/witness", true},
		{"*/witness", "beads/witness", true},
		{"gastown/*", "gastown/witness", true},
		{"gastown/*", "gastown/refinery", true},
		{"gastown/crew/*", "gastown/crew/max", true},

		// Non-matches
		{"*/witness", "gastown/refinery", false},
		{"gastown/*", "beads/witness", false},
		{"gastown/crew/*", "gastown/polecats/Toast", false},

		// Different path lengths
		{"gastown/*", "gastown/crew/max", false},  // * matches single segment
		{"gastown/*/*", "gastown/crew/max", true}, // Multiple wildcards
		{"*/*", "gastown/witness", true},          // Both wildcards
		{"*/*/*", "gastown/crew/max", true},       // Three-level wildcard
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.address, func(t *testing.T) {
			got := matchPattern(tt.pattern, tt.address)
			if got != tt.want {
				t.Errorf("matchPattern(%q, %q) = %v, want %v", tt.pattern, tt.address, got, tt.want)
			}
		})
	}
}

func TestAgentBeadIDToAddress(t *testing.T) {
	tests := []struct {
		id   string
		want string
	}{
		// Town-level agents (gt- prefix)
		{"gt-mayor", "mayor/"},
		{"gt-deacon", "deacon/"},

		// Town-level agents (hq- prefix)
		{"hq-mayor", "mayor/"},
		{"hq-deacon", "deacon/"},

		// Rig singletons
		{"gt-gastown-witness", "gastown/witness"},
		{"gt-gastown-refinery", "gastown/refinery"},
		{"gt-beads-witness", "beads/witness"},

		// Named agents
		{"gt-gastown-crew-max", "gastown/crew/max"},
		{"gt-gastown-polecat-Toast", "gastown/polecats/Toast"},
		{"gt-beads-crew-wolf", "beads/crew/wolf"},

		// Agent with hyphen in name
		{"gt-gastown-crew-max-v2", "gastown/crew/max-v2"},
		{"gt-gastown-polecat-my-agent", "gastown/polecats/my-agent"},

		// Invalid
		{"invalid", ""},
		{"not-gt-prefix", ""},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			got := AgentBeadIDToAddress(tt.id)
			if got != tt.want {
				t.Errorf("AgentBeadIDToAddress(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}

func TestResolverResolve_DirectAddresses(t *testing.T) {
	resolver := NewResolver(nil, "")

	tests := []struct {
		name    string
		address string
		want    RecipientType
		wantLen int
	}{
		// Direct agent addresses
		{"direct agent", "gastown/witness", RecipientAgent, 1},
		{"direct crew", "gastown/crew/max", RecipientAgent, 1},
		{"mayor", "mayor/", RecipientAgent, 1},

		// Legacy prefixes (pass-through)
		{"list prefix", "list:oncall", RecipientAgent, 1},
		{"announce prefix", "announce:alerts", RecipientAgent, 1},

		// Explicit type prefixes
		{"queue prefix", "queue:work", RecipientQueue, 1},
		{"channel prefix", "channel:alerts", RecipientChannel, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolver.Resolve(tt.address)
			if err != nil {
				t.Fatalf("Resolve(%q) error: %v", tt.address, err)
			}
			if len(got) != tt.wantLen {
				t.Errorf("Resolve(%q) returned %d recipients, want %d", tt.address, len(got), tt.wantLen)
			}
			if len(got) > 0 && got[0].Type != tt.want {
				t.Errorf("Resolve(%q)[0].Type = %v, want %v", tt.address, got[0].Type, tt.want)
			}
		})
	}
}

func TestResolverResolve_AtPatterns(t *testing.T) {
	// Without beads, @patterns are passed through for existing router
	resolver := NewResolver(nil, "")

	tests := []struct {
		address string
	}{
		{"@town"},
		{"@witnesses"},
		{"@rig/gastown"},
		{"@overseer"},
	}

	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			got, err := resolver.Resolve(tt.address)
			if err != nil {
				t.Fatalf("Resolve(%q) error: %v", tt.address, err)
			}
			if len(got) != 1 {
				t.Errorf("Resolve(%q) returned %d recipients, want 1", tt.address, len(got))
			}
			// Without beads, @patterns pass through unchanged
			if got[0].Address != tt.address {
				t.Errorf("Resolve(%q) = %q, want pass-through", tt.address, got[0].Address)
			}
		})
	}
}

func TestResolverResolve_UnknownName(t *testing.T) {
	resolver := NewResolver(nil, "")

	// A bare name without prefix should fail if not found
	_, err := resolver.Resolve("unknown-name")
	if err == nil {
		t.Error("Resolve(\"unknown-name\") should return error for unknown name")
	}
}

// Regression test for gt-64wh5: cycle detection bypassed through Resolve fallback.
// Before the fix, resolveMemberWithVisited called r.Resolve() for @-prefixed and
// /-containing members, which created a fresh visited map and lost cycle detection.
// Now it calls resolveWithVisited to thread the visited map through all paths.

func TestExpandGroupMembersWithVisited_CycleDetection(t *testing.T) {
	resolver := NewResolver(nil, "")

	t.Run("direct self-cycle is detected", func(t *testing.T) {
		// Group "A" is already in the visited set — expanding it again should return nil
		fields := &beads.GroupFields{
			Name:    "A",
			Members: []string{"gastown/witness"},
		}
		visited := map[string]bool{"A": true}
		got, err := resolver.expandGroupMembersWithVisited(fields, visited)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("expected 0 recipients for cycle, got %d", len(got))
		}
	})

	t.Run("non-cyclic group expands normally", func(t *testing.T) {
		fields := &beads.GroupFields{
			Name:    "ops",
			Members: []string{"gastown/witness", "gastown/refinery"},
		}
		visited := make(map[string]bool)
		got, err := resolver.expandGroupMembersWithVisited(fields, visited)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Errorf("expected 2 recipients, got %d", len(got))
		}
		// Verify visited map was updated
		if !visited["ops"] {
			t.Error("expected 'ops' to be marked as visited")
		}
	})

	t.Run("visited map propagates to at-pattern members", func(t *testing.T) {
		// Group with @-prefixed member. Without beads, @patterns pass through,
		// but this verifies the visited map is threaded (not recreated).
		fields := &beads.GroupFields{
			Name:    "team",
			Members: []string{"@town", "gastown/witness"},
		}
		visited := make(map[string]bool)
		got, err := resolver.expandGroupMembersWithVisited(fields, visited)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Errorf("expected 2 recipients, got %d", len(got))
		}
		// Verify the group was marked as visited
		if !visited["team"] {
			t.Error("expected 'team' to be marked as visited")
		}
	})

	t.Run("deduplication within group", func(t *testing.T) {
		fields := &beads.GroupFields{
			Name:    "dupes",
			Members: []string{"gastown/witness", "gastown/witness", "gastown/refinery"},
		}
		visited := make(map[string]bool)
		got, err := resolver.expandGroupMembersWithVisited(fields, visited)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Errorf("expected 2 deduplicated recipients, got %d", len(got))
		}
	})
}

func TestResolveMemberWithVisited_ThreadsVisitedMap(t *testing.T) {
	resolver := NewResolver(nil, "")

	t.Run("at-pattern member uses shared visited map", func(t *testing.T) {
		// Before fix: r.Resolve("@town") would create fresh visited map.
		// After fix: r.resolveWithVisited("@town", visited) threads the same map.
		// With nil beads, @town passes through — but the visited map is preserved.
		visited := map[string]bool{"some-group": true}
		got, err := resolver.resolveMemberWithVisited("@town", visited)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 || got[0].Address != "@town" {
			t.Errorf("expected [@town], got %v", got)
		}
		// The original visited map should still contain its entries
		if !visited["some-group"] {
			t.Error("visited map was replaced instead of threaded")
		}
	})

	t.Run("slash-containing member uses shared visited map", func(t *testing.T) {
		visited := map[string]bool{"some-group": true}
		got, err := resolver.resolveMemberWithVisited("gastown/witness", visited)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 || got[0].Address != "gastown/witness" {
			t.Errorf("expected [gastown/witness], got %v", got)
		}
		if !visited["some-group"] {
			t.Error("visited map was replaced instead of threaded")
		}
	})
}

func TestResolveWithVisited_ThreadsCycleDetection(t *testing.T) {
	resolver := NewResolver(nil, "")

	t.Run("group prefix threads visited", func(t *testing.T) {
		// group: prefix with nil beads returns error, but visited map is preserved
		visited := map[string]bool{"existing": true}
		_, err := resolver.resolveWithVisited("group:test", visited)
		if err == nil {
			t.Fatal("expected error for group: with nil beads")
		}
		if !visited["existing"] {
			t.Error("visited map was replaced instead of threaded")
		}
	})

	t.Run("at-pattern threads visited", func(t *testing.T) {
		visited := map[string]bool{"existing": true}
		got, err := resolver.resolveWithVisited("@town", visited)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Errorf("expected 1 recipient, got %d", len(got))
		}
		if !visited["existing"] {
			t.Error("visited map was replaced instead of threaded")
		}
	})
}
