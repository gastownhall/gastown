package cmd

import "testing"

func TestChooseSourceIssue(t *testing.T) {
	tests := []struct {
		name    string
		branch  string
		hooked  []string
		want    string
		wantErr bool
	}{
		{
			name:   "no hook falls back to branch name",
			branch: "polecat/obsidian/fl-d6w@mrvvvwcz",
			hooked: nil,
			want:   "fl-d6w",
		},
		{
			name:   "no hook and unparseable branch yields no issue",
			branch: "polecat/obsidian-mrvvvwcz",
			hooked: nil,
			want:   "",
		},
		{
			name:   "hook wins over stale branch name",
			branch: "polecat/obsidian/fl-d6w@mrvvvwcz", // previous assignment
			hooked: []string{"fl-tph"},                 // current assignment
			want:   "fl-tph",
		},
		{
			name:   "hook and branch agree",
			branch: "polecat/obsidian/fl-tph@mrvvvwcz",
			hooked: []string{"fl-tph"},
			want:   "fl-tph",
		},
		{
			name:   "hook supplies issue when branch has none",
			branch: "polecat/obsidian-mrvvvwcz",
			hooked: []string{"fl-tph"},
			want:   "fl-tph",
		},
		{
			name:   "branch disambiguates multiple hooks",
			branch: "polecat/obsidian/fl-wmq@mrvvvwcz",
			hooked: []string{"fl-tph", "fl-wmq"},
			want:   "fl-wmq",
		},
		{
			name:    "multiple hooks and branch matches none is ambiguous",
			branch:  "polecat/obsidian/fl-d6w@mrvvvwcz",
			hooked:  []string{"fl-tph", "fl-wmq"},
			wantErr: true,
		},
		{
			name:    "multiple hooks and no branch issue is ambiguous",
			branch:  "polecat/obsidian-mrvvvwcz",
			hooked:  []string{"fl-tph", "fl-wmq"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := chooseSourceIssue(tt.branch, "fastlio2_ros2/polecats/obsidian", tt.hooked)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("chooseSourceIssue() = %q, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("chooseSourceIssue() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("chooseSourceIssue() = %q, want %q", got, tt.want)
			}
		})
	}
}

// The regression this whole path exists for: obsidian finished fl-tph while its
// worktree was still on the branch named after fl-d6w, a bead closed in a prior
// session. Branch-name derivation attributed the MR to fl-d6w (hq-szhze).
func TestChooseSourceIssueDoesNotAttributeToStalePriorBead(t *testing.T) {
	got, err := chooseSourceIssue(
		"polecat/obsidian/fl-d6w@mrvvvwcz",
		"fastlio2_ros2/polecats/obsidian",
		[]string{"fl-tph"},
	)
	if err != nil {
		t.Fatalf("chooseSourceIssue() error = %v", err)
	}
	if got == "fl-d6w" {
		t.Fatal("MR attributed to the stale branch's bead fl-d6w instead of the hooked bead fl-tph")
	}
	if got != "fl-tph" {
		t.Errorf("chooseSourceIssue() = %q, want %q", got, "fl-tph")
	}
}
