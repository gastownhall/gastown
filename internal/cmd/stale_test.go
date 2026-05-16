package cmd

import (
	"strings"
	"testing"
)

// TestOutputStaleText exercises the pure text renderer for `gt stale`,
// covering the Skipped / Stale / Fresh branches added for GH#4034.
// It asserts on the unstyled literal substrings (style.Render only wraps
// the leading glyph, not the message text) so it is colour-agnostic.
//
// Note: outputStaleText is a plain function; this test never executes the
// cobra command tree, so the macOS unsigned-binary guard in
// persistentPreRun is not tripped. Run targeted (`-run TestOutputStaleText`)
// to avoid sibling tests that do execute commands.
func TestOutputStaleText(t *testing.T) {
	tests := []struct {
		name    string
		output  StaleOutput
		want    []string // substrings that must be present
		notWant []string // substrings that must be absent
	}{
		{
			name: "skipped names the reason and binary",
			output: StaleOutput{
				Skipped:      true,
				SkipReason:   "source worktree not on a build branch",
				BinaryCommit: "abc1234567890",
			},
			want: []string{
				"Binary staleness check skipped",
				"source worktree not on a build branch",
				"abc123456789",
			},
			notWant: []string{"Binary is stale", "Binary is fresh"},
		},
		{
			name: "stale, behind, diverged, off build branch, unsafe",
			output: StaleOutput{
				Stale:         true,
				Forward:       false,
				OnMainBranch:  false,
				SafeToRebuild: false,
				BinaryCommit:  "abc1234567890",
				RepoCommit:    "def4567890123",
				CompareRef:    "main",
				CommitsBehind: 3,
			},
			want: []string{
				"Binary is stale",
				"Build ref (main): def456789012",
				"(3 commits behind main)",
				"main is NOT a descendant of binary commit",
				"source worktree is not on a build branch (compared against main)",
				"NOT safe for automated rebuild (forward=false, main=false)",
			},
			notWant: []string{"Safe to rebuild: run"},
		},
		{
			name: "stale, forward, on build branch, safe to rebuild",
			output: StaleOutput{
				Stale:         true,
				Forward:       true,
				OnMainBranch:  true,
				SafeToRebuild: true,
				BinaryCommit:  "abc1234567890",
				RepoCommit:    "def4567890123",
				CompareRef:    "carry/ops",
			},
			want: []string{
				"Binary is stale",
				"Build ref (carry/ops): def456789012",
				"Safe to rebuild: run 'make build && make install'",
			},
			notWant: []string{
				"commits behind",
				"NOT a descendant",
				"not on a build branch",
				"NOT safe for automated rebuild",
			},
		},
		{
			name: "fresh with compare ref",
			output: StaleOutput{
				Stale:        false,
				BinaryCommit: "abc1234567890",
				CompareRef:   "origin/main",
			},
			want: []string{
				"Binary is fresh",
				"Commit: abc123456789",
				"(compared against origin/main)",
			},
			notWant: []string{"Binary is stale", "skipped"},
		},
		{
			name: "fresh without compare ref omits the comparison line",
			output: StaleOutput{
				Stale:        false,
				BinaryCommit: "abc1234567890",
			},
			want:    []string{"Binary is fresh", "Commit: abc123456789"},
			notWant: []string{"compared against"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			out := captureStdout(t, func() { err = outputStaleText(tt.output) })
			if err != nil {
				t.Fatalf("outputStaleText returned error: %v", err)
			}
			for _, w := range tt.want {
				if !strings.Contains(out, w) {
					t.Errorf("output missing %q\n--- got ---\n%s", w, out)
				}
			}
			for _, nw := range tt.notWant {
				if strings.Contains(out, nw) {
					t.Errorf("output unexpectedly contains %q\n--- got ---\n%s", nw, out)
				}
			}
		})
	}
}
