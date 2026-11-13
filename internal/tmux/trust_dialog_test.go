package tmux

import "testing"

// claudeTrustDialog is captured verbatim from a live polecat session that died
// during startup. Claude Code pre-selects "No, exit", so confirming the
// pre-selected option quits the agent.
const claudeTrustDialog = `────────────────────────────────────────────────────
 Accessing workspace:

 /Users/x/gt/gastown/polecats/obsidian/gastown

 Quick safety check: Is this a project you created or one you trust? (Like your
 own code, a well-known open source project, or work from your team). If not,
 take a moment to review what's in this folder first.

 Claude Code'll be able to read, edit, and execute files here.

 Security guide

 ❯ No, exit
   Yes, I trust this folder

 Enter to confirm · Esc to cancel
`

// affirmativeFirst covers an agent that lists the trusting option first.
const affirmativeFirst = `Do you trust the contents of this directory?

 ❯ Yes, I trust this folder
   No, exit
`

func TestTrustDialogDownPresses(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int
	}{
		{"claude puts No first, must move down one", claudeTrustDialog, 1},
		{"affirmative already selected, do not move", affirmativeFirst, 0},
		{"unparseable content falls back to confirming", "some unrelated output", 0},
		{"no selection marker falls back to confirming", "No, exit\nYes, I trust this folder\n", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := trustDialogDownPresses(tc.content); got != tc.want {
				t.Fatalf("trustDialogDownPresses() = %d, want %d", got, tc.want)
			}
		})
	}
}

// The regression this guards: the old code pressed Enter unconditionally, which
// selected "No, exit" and killed every polecat in a freshly forked rig.
func TestClaudeTrustDialogDoesNotConfirmDecline(t *testing.T) {
	options, selected := trustDialogOptionLines(claudeTrustDialog)
	if selected < 0 {
		t.Fatal("selection marker not detected in captured Claude dialog")
	}
	if got := options[selected]; got != "no, exit" {
		t.Fatalf("expected the decline option to start selected, got %q", got)
	}
	moved := selected + trustDialogDownPresses(claudeTrustDialog)
	if moved >= len(options) {
		t.Fatalf("moved selection out of range: %d of %d", moved, len(options))
	}
	if got := options[moved]; got != "yes, i trust this folder" {
		t.Fatalf("selection landed on %q, want the affirmative option", got)
	}
}

func TestWorkspaceTrustDialogStillDetected(t *testing.T) {
	if !containsWorkspaceTrustDialog(claudeTrustDialog) {
		t.Fatal("captured Claude trust dialog no longer detected")
	}
}
