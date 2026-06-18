package workitem

import "testing"

func TestAssessConcrete(t *testing.T) {
	tests := []struct {
		name     string
		snapshot Snapshot
		want     bool
	}{
		{"task", Snapshot{ID: "gt-abc", Type: "task"}, true},
		{"bug", Snapshot{ID: "gt-bug", Type: "bug"}, true},
		{"feature", Snapshot{ID: "gt-feature", Type: "feature"}, true},
		{"missing id", Snapshot{}, false},
		{"formula id", Snapshot{ID: "mol-polecat-work", Type: "task"}, false},
		{"ephemeral", Snapshot{ID: "gt-abc", Type: "task", Ephemeral: true}, false},
		{"wisp id", Snapshot{ID: "gt-wisp-abc", Type: "task"}, false},
		{"sling context label", Snapshot{ID: "gt-ctx", Type: "task", Labels: []string{"gt:sling-context"}}, false},
		{"merge request label", Snapshot{ID: "gt-mr", Type: "task", Labels: []string{"gt:merge-request"}}, false},
		{"agent label", Snapshot{ID: "gt-agent", Type: "task", Labels: []string{"gt:agent"}}, false},
		{"convoy label", Snapshot{ID: "gt-convoy", Type: "task", Labels: []string{"gt:convoy"}}, false},
		{"agent type", Snapshot{ID: "gt-agent", Type: "agent"}, false},
		{"epic type", Snapshot{ID: "gt-epic", Type: "epic"}, false},
		{"wisp type", Snapshot{ID: "gt-wisp", Type: "wisp"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AssessConcrete(tt.snapshot)
			if got.Concrete != tt.want {
				t.Fatalf("Concrete = %v, want %v (reason %q)", got.Concrete, tt.want, got.Reason)
			}
			if !tt.want && got.Reason == "" {
				t.Fatalf("non-concrete assessment should include a reason")
			}
		})
	}
}
