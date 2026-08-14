package beads

import "testing"

func TestResolveMergeStrategy(t *testing.T) {
	productionProse := "DO NOT push to GitHub — local commit only"

	tests := []struct {
		name        string
		cliMerge    string
		storedMerge string
		issueText   string
		want        string
	}{
		{
			name:     "explicit CLI local wins",
			cliMerge: "local",
			want:     "local",
		},
		{
			name:        "CLI wins over stored and prose",
			cliMerge:    "mr",
			storedMerge: "local",
			issueText:   productionProse,
			want:        "mr",
		},
		{
			name:        "stored local is reused without CLI flag",
			storedMerge: "local",
			issueText:   "ordinary work",
			want:        "local",
		},
		{
			name:        "stored mr is reused without CLI flag",
			storedMerge: "mr",
			issueText:   productionProse,
			want:        "mr",
		},
		{
			name:      "production prose becomes local without CLI or stored field",
			issueText: productionProse,
			want:      "local",
		},
		{
			name:      "local commit only phrase",
			issueText: "Keep this on the machine: local commit only.",
			want:      "local",
		},
		{
			name:      "do not push phrase",
			issueText: "Please do not push this branch.",
			want:      "local",
		},
		{
			name:      "don't push phrase",
			issueText: "Don't push to origin.",
			want:      "local",
		},
		{
			name:      "ordinary issue text stays default",
			issueText: "Fix the login button.",
			want:      "",
		},
		{
			name:     "whitespace-only CLI is treated as unset",
			cliMerge: "   ",
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveMergeStrategy(tt.cliMerge, tt.storedMerge, tt.issueText)
			if got != tt.want {
				t.Fatalf("ResolveMergeStrategy() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHasLocalMergeStrategy(t *testing.T) {
	if HasLocalMergeStrategy(nil) {
		t.Fatal("nil fields should not be local")
	}
	if HasLocalMergeStrategy(&AttachmentFields{MergeStrategy: "mr"}) {
		t.Fatal("mr should not be local")
	}
	if !HasLocalMergeStrategy(&AttachmentFields{MergeStrategy: "local"}) {
		t.Fatal("local should be local")
	}
	if !HasLocalMergeStrategy(&AttachmentFields{MergeStrategy: " LOCAL "}) {
		t.Fatal("LOCAL with space should be local")
	}
}
