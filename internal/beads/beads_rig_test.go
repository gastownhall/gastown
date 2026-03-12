package beads

import (
	"strings"
	"testing"
)

func TestFormatRigDescription(t *testing.T) {
	tests := []struct {
		name   string
		rigName string
		fields *RigFields
		want   []string
	}{
		{
			name:    "nil fields",
			rigName: "gastown",
			fields:  nil,
			want:    nil, // empty string
		},
		{
			name:    "all fields",
			rigName: "gastown",
			fields: &RigFields{
				Repo:   "git@github.com:user/gastown.git",
				Prefix: "gt",
				State:  RigStateActive,
			},
			want: []string{
				"Rig identity bead for gastown.",
				"repo: git@github.com:user/gastown.git",
				"prefix: gt",
				"state: active",
			},
		},
		{
			name:    "partial fields",
			rigName: "beads",
			fields: &RigFields{
				Prefix: "bd",
			},
			want: []string{
				"Rig identity bead for beads.",
				"prefix: bd",
			},
		},
		{
			name:    "empty fields no repo/prefix/state lines",
			rigName: "empty",
			fields:  &RigFields{},
			want: []string{
				"Rig identity bead for empty.",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatRigDescription(tt.rigName, tt.fields)
			if tt.want == nil {
				if got != "" {
					t.Errorf("expected empty string, got %q", got)
				}
				return
			}
			for _, line := range tt.want {
				if !strings.Contains(got, line) {
					t.Errorf("missing line %q in output:\n%s", line, got)
				}
			}
		})
	}
}

func TestParseRigFields(t *testing.T) {
	tests := []struct {
		name string
		desc string
		want *RigFields
	}{
		{
			name: "empty description",
			desc: "",
			want: &RigFields{},
		},
		{
			name: "full rig description",
			desc: `Rig identity bead for gastown.

repo: git@github.com:user/gastown.git
prefix: gt
state: active`,
			want: &RigFields{
				Repo:   "git@github.com:user/gastown.git",
				Prefix: "gt",
				State:  RigStateActive,
			},
		},
		{
			name: "null values become empty",
			desc: "repo: null\nprefix: bd\nstate: null",
			want: &RigFields{
				Repo:   "",
				Prefix: "bd",
				State:  "",
			},
		},
		{
			name: "only prefix",
			desc: "prefix: bd",
			want: &RigFields{
				Prefix: "bd",
			},
		},
		{
			name: "state maintenance",
			desc: "state: maintenance\nprefix: gt",
			want: &RigFields{
				State:  RigStateMaintenance,
				Prefix: "gt",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseRigFields(tt.desc)
			if got.Repo != tt.want.Repo {
				t.Errorf("Repo = %q, want %q", got.Repo, tt.want.Repo)
			}
			if got.Prefix != tt.want.Prefix {
				t.Errorf("Prefix = %q, want %q", got.Prefix, tt.want.Prefix)
			}
			if got.State != tt.want.State {
				t.Errorf("State = %q, want %q", got.State, tt.want.State)
			}
		})
	}
}

func TestRigFieldsRoundTrip(t *testing.T) {
	original := &RigFields{
		Repo:   "git@github.com:user/gastown.git",
		Prefix: "gt",
		State:  RigStateActive,
	}

	formatted := FormatRigDescription("gastown", original)
	parsed := ParseRigFields(formatted)

	if parsed.Repo != original.Repo {
		t.Errorf("Repo: got %q, want %q", parsed.Repo, original.Repo)
	}
	if parsed.Prefix != original.Prefix {
		t.Errorf("Prefix: got %q, want %q", parsed.Prefix, original.Prefix)
	}
	if parsed.State != original.State {
		t.Errorf("State: got %q, want %q", parsed.State, original.State)
	}
}

func TestRigBeadID(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"gastown", "gt-rig-gastown"},
		{"beads", "gt-rig-beads"},
		{"my-rig", "gt-rig-my-rig"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RigBeadID(tt.name); got != tt.want {
				t.Errorf("RigBeadID(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestRigBeadIDWithPrefix(t *testing.T) {
	tests := []struct {
		prefix string
		name   string
		want   string
	}{
		{"gt", "gastown", "gt-rig-gastown"},
		{"bd", "beads", "bd-rig-beads"},
		{"hq", "town", "hq-rig-town"},
	}

	for _, tt := range tests {
		t.Run(tt.prefix+"-"+tt.name, func(t *testing.T) {
			if got := RigBeadIDWithPrefix(tt.prefix, tt.name); got != tt.want {
				t.Errorf("RigBeadIDWithPrefix(%q, %q) = %q, want %q", tt.prefix, tt.name, got, tt.want)
			}
		})
	}
}

func TestValidRigState(t *testing.T) {
	tests := []struct {
		state RigState
		want  bool
	}{
		{RigStateActive, true},
		{RigStateArchived, true},
		{RigStateMaintenance, true},
		{"", false},
		{"invalid", false},
		{"ACTIVE", false},
	}

	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			if got := ValidRigState(tt.state); got != tt.want {
				t.Errorf("ValidRigState(%q) = %v, want %v", tt.state, got, tt.want)
			}
		})
	}
}

func TestIsRigBead(t *testing.T) {
	tests := []struct {
		name  string
		issue *Issue
		want  bool
	}{
		{"nil", nil, false},
		{"rig by ID pattern", &Issue{ID: "gt-rig-gastown", Type: "task"}, true},
		{"rig by ID pattern other prefix", &Issue{ID: "bd-rig-beads", Type: "task"}, true},
		{"rig by gt:rig label", &Issue{ID: "gt-xyz", Type: "task", Labels: []string{"gt:rig"}}, true},
		{"real task", &Issue{ID: "gt-abc123", Type: "task"}, false},
		{"bug", &Issue{ID: "gt-bug", Type: "bug"}, false},
		{"epic is not a rig", &Issue{ID: "gt-epic", Type: "epic"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRigBead(tt.issue); got != tt.want {
				t.Errorf("IsRigBead() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsEpicBead(t *testing.T) {
	tests := []struct {
		name  string
		issue *Issue
		want  bool
	}{
		{"nil", nil, false},
		{"epic by type", &Issue{ID: "gt-2ejqk", Type: "epic"}, true},
		{"epic by gt:epic label", &Issue{ID: "gt-xyz", Type: "task", Labels: []string{"gt:epic"}}, true},
		{"real task", &Issue{ID: "gt-abc123", Type: "task"}, false},
		{"bug", &Issue{ID: "gt-bug", Type: "bug"}, false},
		{"rig bead is not epic", &Issue{ID: "gt-rig-gastown", Type: "task"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsEpicBead(tt.issue); got != tt.want {
				t.Errorf("IsEpicBead() = %v, want %v", got, tt.want)
			}
		})
	}
}
