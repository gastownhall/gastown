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

func TestNormalizeMemoryKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{
			name: "plain key",
			key:  "refinery-worktree",
			want: "refinery-worktree",
		},
		{
			name: "legacy memory prefix",
			key:  "memory.refinery-worktree",
			want: "refinery-worktree",
		},
		{
			name: "legacy typed kv key",
			key:  "memory.feedback.dont-mock-db",
			want: "feedback-dont-mock-db",
		},
		{
			name: "legacy type slash key",
			key:  "feedback/dont-mock-db",
			want: "dont-mock-db",
		},
		{
			name: "legacy slash key with memory prefix",
			key:  "memory.feedback/dont-mock-db",
			want: "dont-mock-db",
		},
		{
			name: "empty",
			key:  "",
			want: "",
		},
		{
			name: "prefix only",
			key:  "memory.",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeMemoryKey(tt.key)
			if got != tt.want {
				t.Errorf("normalizeMemoryKey(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

func TestParseBdMemoriesJSON(t *testing.T) {
	got, err := parseBdMemoriesJSON([]byte(`{
		"refinery-worktree":"Refinery uses worktree, cannot checkout main",
		"empty-mem":"",
		"count":12,
		"enabled":true,
		"tags":["one"],
		"config":{"nested":"value"},
		"null-mem":null,
		"schema_version":1
	}`))
	if err != nil {
		t.Fatalf("parseBdMemoriesJSON() error = %v", err)
	}

	want := map[string]string{
		"refinery-worktree": "Refinery uses worktree, cannot checkout main",
		"empty-mem":         "",
		"count":             "12",
		"enabled":           "true",
		"tags":              `["one"]`,
		"config":            `{"nested":"value"}`,
	}
	if len(got) != len(want) {
		t.Fatalf("parseBdMemoriesJSON() returned %d entries, want %d: %#v", len(got), len(want), got)
	}
	for k, wantValue := range want {
		if got[k] != wantValue {
			t.Errorf("parseBdMemoriesJSON()[%q] = %q, want %q", k, got[k], wantValue)
		}
	}
	if _, ok := got["null-mem"]; ok {
		t.Error("parseBdMemoriesJSON() kept null memory value")
	}
	if _, ok := got["schema_version"]; ok {
		t.Error("parseBdMemoriesJSON() kept schema_version")
	}
}

func TestParseBdMemoriesJSONEmptyStore(t *testing.T) {
	got, err := parseBdMemoriesJSON([]byte(`{"schema_version":1}`))
	if err != nil {
		t.Fatalf("parseBdMemoriesJSON() error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("parseBdMemoriesJSON() on empty store returned %d entries, want 0: %#v", len(got), got)
	}
}

func TestParseBdMemoriesJSONMalformed(t *testing.T) {
	if _, err := parseBdMemoriesJSON([]byte(`{"refinery-worktree":`)); err == nil {
		t.Fatal("parseBdMemoriesJSON() error = nil, want malformed JSON error")
	}
}
