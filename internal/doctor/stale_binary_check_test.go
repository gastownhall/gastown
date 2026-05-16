package doctor

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/version"
)

// TestStaleResult covers the pure info -> CheckResult mapping. Run() itself is
// not unit-tested because it depends on version.GetRepoRoot() (env/git driven);
// the testable logic lives in staleResult.
func TestStaleResult(t *testing.T) {
	const name = "stale-binary"

	tests := []struct {
		name        string
		info        *version.StaleBinaryInfo
		wantStatus  CheckStatus
		wantMessage string
		wantDetail  string // substring expected in Details[0]; "" => no details required
		wantFixHint bool
	}{
		{
			name:        "error -> OK dev-build message",
			info:        &version.StaleBinaryInfo{Error: errors.New("cannot determine binary commit")},
			wantStatus:  StatusOK,
			wantMessage: "Cannot determine binary version (dev build?)",
			wantDetail:  "cannot determine binary commit",
		},
		{
			name:        "skipped -> OK with skip reason",
			info:        &version.StaleBinaryInfo{Skipped: true, SkipReason: "no build-branch ref found"},
			wantStatus:  StatusOK,
			wantMessage: "Binary staleness check skipped",
			wantDetail:  "no build-branch ref found",
		},
		{
			name: "stale with count -> Warning + fix hint",
			info: &version.StaleBinaryInfo{
				IsStale: true, CommitsBehind: 2, CompareRef: "main",
				BinaryCommit: "abc1234567890", RepoCommit: "def4567890123",
			},
			wantStatus:  StatusWarning,
			wantMessage: "Binary is 2 commits behind main (built from abc123456789, main at def456789012)",
			wantFixHint: true,
		},
		{
			name: "stale without count -> Warning, stale wording",
			info: &version.StaleBinaryInfo{
				IsStale: true, CompareRef: "origin/main",
				BinaryCommit: "abc1234567890", RepoCommit: "def4567890123",
			},
			wantStatus:  StatusWarning,
			wantMessage: "Binary is stale (built from abc123456789, origin/main at def456789012)",
			wantFixHint: true,
		},
		{
			name:        "fresh -> OK up to date",
			info:        &version.StaleBinaryInfo{BinaryCommit: "abc1234567890"},
			wantStatus:  StatusOK,
			wantMessage: "Binary is up to date (abc123456789)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := staleResult(name, tt.info)
			if got.Name != name {
				t.Errorf("Name = %q, want %q", got.Name, name)
			}
			if got.Status != tt.wantStatus {
				t.Errorf("Status = %v, want %v", got.Status, tt.wantStatus)
			}
			if got.Message != tt.wantMessage {
				t.Errorf("Message = %q, want %q", got.Message, tt.wantMessage)
			}
			if tt.wantDetail != "" {
				if len(got.Details) == 0 || !strings.Contains(got.Details[0], tt.wantDetail) {
					t.Errorf("Details = %v, want one containing %q", got.Details, tt.wantDetail)
				}
			}
			if tt.wantFixHint && got.FixHint == "" {
				t.Error("expected a FixHint for a stale binary, got none")
			}
			if !tt.wantFixHint && got.FixHint != "" {
				t.Errorf("unexpected FixHint %q", got.FixHint)
			}
		})
	}
}
