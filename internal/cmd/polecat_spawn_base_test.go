package cmd

import "testing"

func TestPolecatBaseRef(t *testing.T) {
	tests := []struct {
		name       string
		base       string
		local      bool
		wantRef    string
		wantBranch string
	}{
		{
			name:       "local merge uses shared local head",
			base:       "feature/rails-8.1-upgrade",
			local:      true,
			wantRef:    "refs/heads/feature/rails-8.1-upgrade",
			wantBranch: "feature/rails-8.1-upgrade",
		},
		{
			name:       "merge queue uses remote tracking head",
			base:       "feature/rails-8.1-upgrade",
			wantRef:    "origin/feature/rails-8.1-upgrade",
			wantBranch: "feature/rails-8.1-upgrade",
		},
		{
			name:       "explicit remote ref remains remote",
			base:       "origin/develop",
			local:      true,
			wantRef:    "origin/develop",
			wantBranch: "develop",
		},
		{
			name:       "remote-like local branch remains local",
			base:       "upstream/develop",
			local:      true,
			wantRef:    "refs/heads/upstream/develop",
			wantBranch: "upstream/develop",
		},
		{
			name:       "remote-like normal branch uses origin",
			base:       "upstream/develop",
			wantRef:    "origin/upstream/develop",
			wantBranch: "upstream/develop",
		},
		{
			name:       "explicit full ref remains unchanged",
			base:       "refs/heads/integration/epic",
			local:      true,
			wantRef:    "refs/heads/integration/epic",
			wantBranch: "integration/epic",
		},
		{
			name:       "explicit full origin ref remains unchanged",
			base:       "refs/remotes/origin/develop",
			local:      true,
			wantRef:    "refs/remotes/origin/develop",
			wantBranch: "develop",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotRef, gotBranch := polecatBaseRef(tt.base, tt.local)
			if gotRef != tt.wantRef {
				t.Errorf("polecatBaseRef ref = %q, want %q", gotRef, tt.wantRef)
			}
			if gotBranch != tt.wantBranch {
				t.Errorf("polecatBaseRef branch = %q, want %q", gotBranch, tt.wantBranch)
			}
		})
	}
}

func TestResolvePolecatSpawnBaseUsesExactLocalRefForSpawnPaths(t *testing.T) {
	const feature = "feature/rails-8.1-upgrade"

	ref, branch, worktreeRef := resolvePolecatSpawnBase(feature, "main", "", true)
	if ref != "refs/heads/"+feature {
		t.Fatalf("base ref = %q, want refs/heads/%s", ref, feature)
	}
	if branch != feature {
		t.Fatalf("base branch = %q, want %s", branch, feature)
	}
	if worktreeRef != ref {
		t.Fatalf("worktree base ref = %q, want resolved ref %q", worktreeRef, ref)
	}
}

func TestAppendSpawnBaseVarsIncludesStableRef(t *testing.T) {
	info := &SpawnedPolecatInfo{
		BaseBranch: "feature/rails-8.1-upgrade",
		BaseRef:    "refs/heads/feature/rails-8.1-upgrade",
	}

	got := appendSpawnBaseVars([]string{"test_command=go test ./..."}, info)
	want := []string{
		"test_command=go test ./...",
		"base_branch=feature/rails-8.1-upgrade",
		"base_ref=refs/heads/feature/rails-8.1-upgrade",
	}
	if len(got) != len(want) {
		t.Fatalf("appendSpawnBaseVars returned %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("appendSpawnBaseVars[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestResolvePolecatSpawnBaseKeepsResumeTarget(t *testing.T) {
	ref, branch, worktreeRef := resolvePolecatSpawnBase("", "main", "feature/existing-pr", false)
	if ref != "origin/main" {
		t.Errorf("base ref = %q, want origin/main", ref)
	}
	if branch != "main" {
		t.Errorf("base branch = %q, want main", branch)
	}
	if worktreeRef != "" {
		t.Errorf("worktree base ref = %q, want empty for resumed branch", worktreeRef)
	}
}
