package cmd

import (
	"testing"
)

func TestAutoKey(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "basic words",
			content: "Refinery uses worktree for merges",
			want:    "refinery-uses-worktree-for-merges",
		},
		{
			name:    "more than 5 words truncated",
			content: "Always use stdin for multi line mail messages",
			want:    "always-use-stdin-for-multi",
		},
		{
			name:    "strips punctuation",
			content: "Don't use rm -rf on .dolt-data/",
			want:    "dont-use-rm-rf-on",
		},
		{
			name:    "single word",
			content: "important",
			want:    "important",
		},
		{
			name:    "mixed case",
			content: "Hooks Package Structure",
			want:    "hooks-package-structure",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := autoKey(tt.content)
			if got != tt.want {
				t.Errorf("autoKey(%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

func TestSanitizeKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{
			name: "already clean",
			key:  "refinery-worktree",
			want: "refinery-worktree",
		},
		{
			name: "spaces to hyphens",
			key:  "refinery worktree",
			want: "refinery-worktree",
		},
		{
			name: "dots to hyphens",
			key:  "memory.slug",
			want: "memory-slug",
		},
		{
			name: "uppercase to lower",
			key:  "MyKey",
			want: "mykey",
		},
		{
			name: "strip special chars",
			key:  "key@#$%value",
			want: "keyvalue",
		},
		{
			name: "collapse multiple hyphens",
			key:  "key---value",
			want: "key-value",
		},
		{
			name: "trim leading/trailing hyphens",
			key:  "-key-value-",
			want: "key-value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeKey(tt.key)
			if got != tt.want {
				t.Errorf("sanitizeKey(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

func TestParseMemoryKey(t *testing.T) {
	tests := []struct {
		name     string
		kvKey    string
		wantType string
		wantKey  string
	}{
		{
			name:     "typed feedback key",
			kvKey:    "memory.feedback.dont-mock-db",
			wantType: "feedback",
			wantKey:  "dont-mock-db",
		},
		{
			name:     "typed project key",
			kvKey:    "memory.project.merge-freeze",
			wantType: "project",
			wantKey:  "merge-freeze",
		},
		{
			name:     "typed user key",
			kvKey:    "memory.user.senior-go-dev",
			wantType: "user",
			wantKey:  "senior-go-dev",
		},
		{
			name:     "typed reference key",
			kvKey:    "memory.reference.grafana-dashboard",
			wantType: "reference",
			wantKey:  "grafana-dashboard",
		},
		{
			name:     "typed general key",
			kvKey:    "memory.general.some-insight",
			wantType: "general",
			wantKey:  "some-insight",
		},
		{
			name:     "legacy untyped key",
			kvKey:    "memory.refinery-worktree",
			wantType: "general",
			wantKey:  "refinery-worktree",
		},
		{
			name:     "legacy key with dots in slug",
			kvKey:    "memory.hooks-package-structure",
			wantType: "general",
			wantKey:  "hooks-package-structure",
		},
		{
			name:     "unknown type treated as legacy",
			kvKey:    "memory.banana.split",
			wantType: "general",
			wantKey:  "banana.split",
		},
		{
			name:     "typed key with hyphens in value",
			kvKey:    "memory.feedback.always-use-race-flag",
			wantType: "feedback",
			wantKey:  "always-use-race-flag",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotType, gotKey := parseMemoryKey(tt.kvKey)
			if gotType != tt.wantType {
				t.Errorf("parseMemoryKey(%q) type = %q, want %q", tt.kvKey, gotType, tt.wantType)
			}
			if gotKey != tt.wantKey {
				t.Errorf("parseMemoryKey(%q) key = %q, want %q", tt.kvKey, gotKey, tt.wantKey)
			}
		})
	}
}

func TestMemTypeRank(t *testing.T) {
	// feedback should come before general
	if memTypeRank("feedback") >= memTypeRank("general") {
		t.Error("feedback should rank before general")
	}
	// user should come before project
	if memTypeRank("user") >= memTypeRank("project") {
		t.Error("user should rank before project")
	}
	// unknown types should sort last
	if memTypeRank("unknown") <= memTypeRank("general") {
		t.Error("unknown type should rank after general")
	}
}

func TestParseBdKvListJSON(t *testing.T) {
	got, err := parseBdKvListJSON([]byte(`{
		"memory.project.note":"keep me",
		"memory.project.empty":"",
		"memory.project.count":12,
		"memory.project.enabled":true,
		"memory.project.tags":["one"],
		"memory.project.config":{"nested":"value"},
		"schema_version":1,
		"other":"keep string kvs",
		"enabled":true,
		"tags":["one"],
		"config":{"nested":"value"},
		"memory.project.null":null
	}`))
	if err != nil {
		t.Fatalf("parseBdKvListJSON() error = %v", err)
	}

	want := map[string]string{
		"memory.project.note":    "keep me",
		"memory.project.empty":   "",
		"memory.project.count":   "12",
		"memory.project.enabled": "true",
		"memory.project.tags":    `["one"]`,
		"memory.project.config":  `{"nested":"value"}`,
		"other":                  "keep string kvs",
	}
	if len(got) != len(want) {
		t.Fatalf("parseBdKvListJSON() returned %d entries, want %d: %#v", len(got), len(want), got)
	}
	for k, wantValue := range want {
		if got[k] != wantValue {
			t.Errorf("parseBdKvListJSON()[%q] = %q, want %q", k, got[k], wantValue)
		}
	}
	if _, ok := got["memory.project.null"]; ok {
		t.Error("parseBdKvListJSON() kept null memory value")
	}
	for _, k := range []string{"schema_version", "enabled", "tags", "config"} {
		if _, ok := got[k]; ok {
			t.Errorf("parseBdKvListJSON() kept non-memory non-string value for %q", k)
		}
	}
}

func TestParseBdKvListJSONMalformed(t *testing.T) {
	if _, err := parseBdKvListJSON([]byte(`{"memory.project.note":`)); err == nil {
		t.Fatal("parseBdKvListJSON() error = nil, want malformed JSON error")
	}
}
