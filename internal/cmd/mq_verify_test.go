package cmd

import (
	"errors"
	"testing"

	"github.com/jonbaldie/gastown/internal/beads"
)

// mockBranchVerifier implements branchVerifier for testing.
type mockBranchVerifier struct {
	localBranches  map[string]bool
	remoteBranches map[string]bool
	localErr       error
	remoteErr      error
}

func (m *mockBranchVerifier) BranchExists(branch string) (bool, error) {
	if m.localErr != nil {
		return false, m.localErr
	}
	return m.localBranches[branch], nil
}

func (m *mockBranchVerifier) RemoteTrackingBranchExists(remote, branch string) (bool, error) {
	if m.remoteErr != nil {
		return false, m.remoteErr
	}
	key := remote + "/" + branch
	return m.remoteBranches[key], nil
}

func TestVerifyBranch(t *testing.T) {
	tests := []struct {
		name        string
		verify      bool
		client      branchVerifier
		fields      *beads.MRFields
		wantMissing bool
		wantErr     bool
	}{
		{
			name:        "verify disabled",
			verify:      false,
			client:      &mockBranchVerifier{},
			fields:      &beads.MRFields{Branch: "polecat/Nux/gt-abc"},
			wantMissing: false,
			wantErr:     false,
		},
		{
			name:        "nil client",
			verify:      true,
			client:      nil,
			fields:      &beads.MRFields{Branch: "polecat/Nux/gt-abc"},
			wantMissing: false,
			wantErr:     false,
		},
		{
			name:        "empty branch",
			verify:      true,
			client:      &mockBranchVerifier{},
			fields:      &beads.MRFields{Branch: ""},
			wantMissing: false,
			wantErr:     false,
		},
		{
			name:   "local branch exists",
			verify: true,
			client: &mockBranchVerifier{
				localBranches: map[string]bool{"polecat/Nux/gt-abc": true},
			},
			fields:      &beads.MRFields{Branch: "polecat/Nux/gt-abc"},
			wantMissing: false,
			wantErr:     false,
		},
		{
			name:   "remote-only branch exists",
			verify: true,
			client: &mockBranchVerifier{
				localBranches:  map[string]bool{},
				remoteBranches: map[string]bool{"origin/polecat/Nux/gt-abc": true},
			},
			fields:      &beads.MRFields{Branch: "polecat/Nux/gt-abc"},
			wantMissing: false,
			wantErr:     false,
		},
		{
			name:   "both missing",
			verify: true,
			client: &mockBranchVerifier{
				localBranches:  map[string]bool{},
				remoteBranches: map[string]bool{},
			},
			fields:      &beads.MRFields{Branch: "polecat/Nux/gt-abc"},
			wantMissing: true,
			wantErr:     false,
		},
		{
			name:   "local check errors",
			verify: true,
			client: &mockBranchVerifier{
				localErr: errors.New("permission denied"),
			},
			fields:      &beads.MRFields{Branch: "polecat/Nux/gt-abc"},
			wantMissing: false,
			wantErr:     true,
		},
		{
			name:   "remote check errors after local miss",
			verify: true,
			client: &mockBranchVerifier{
				localBranches: map[string]bool{},
				remoteErr:     errors.New("corrupt repo"),
			},
			fields:      &beads.MRFields{Branch: "polecat/Nux/gt-abc"},
			wantMissing: false,
			wantErr:     true,
		},
		{
			name:        "nil fields",
			verify:      true,
			client:      &mockBranchVerifier{},
			fields:      nil,
			wantMissing: false,
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMissing, gotErr := verifyBranch(tt.verify, tt.client, tt.fields)
			if gotMissing != tt.wantMissing {
				t.Errorf("verifyBranch() missing = %v, want %v", gotMissing, tt.wantMissing)
			}
			if gotErr != tt.wantErr {
				t.Errorf("verifyBranch() verifyErr = %v, want %v", gotErr, tt.wantErr)
			}
		})
	}
}
