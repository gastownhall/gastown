package tmux

import "testing"

func TestTrustDialogNavigation(t *testing.T) {
	cases := []struct {
		name        string
		content     string
		wantUpFirst int
		wantSteps   int
		wantOK      bool
	}{
		{
			// The real pane captured from liveop-refinery, 2026-08-31 (gt-ma1).
			// Decline is FIRST and selected — a bare Enter here declines trust.
			name:      "claude v2.1.252, decline preselected",
			content:   "Quick safety check\n\n❯ No, exit\n  Yes, I trust this folder\n\nEnter to confirm",
			wantSteps: 1,
			wantOK:    true,
		},
		{
			name:      "affirmative already selected",
			content:   "Quick safety check\n\n❯ Yes, I trust this folder\n  No, exit\n",
			wantSteps: 0,
			wantOK:    true,
		},
		{
			name:      "codex phrasing, decline preselected",
			content:   "Do you trust the contents of this directory?\n\n> No, exit\n  Yes, proceed\n",
			wantSteps: 1,
			wantOK:    true,
		},
		{
			// Cursor not rendered as a glyph: normalise to the top with Up
			// presses, then descend to the affirmative option.
			name:        "options visible, no cursor glyph",
			content:     "Quick safety check\n\n  No, exit\n  Yes, I trust this folder\n",
			wantUpFirst: 2,
			wantSteps:   1,
			wantOK:      true,
		},
		{
			// Banner text only — what the legacy tests echo. Must fall back to
			// the historical bare Enter rather than error.
			name:    "banner only, no options -> legacy fallback",
			content: "Quick safety check - do you trust this folder?\n",
			wantOK:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upFirst, steps, ok := trustDialogNavigation(tc.content)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if upFirst != tc.wantUpFirst || steps != tc.wantSteps {
				t.Fatalf("upFirst/steps = %d/%d, want %d/%d", upFirst, steps, tc.wantUpFirst, tc.wantSteps)
			}
		})
	}
}
