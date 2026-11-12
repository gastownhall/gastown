package tmux

import "testing"

func TestTrustDialogSelectsExit(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"claude 2.1.252 default", "Quick safety check: ...\n ❯ No, exit\n   Yes, I trust this folder\n", true},
		{"ascii caret", "> No, exit\n  Yes, I trust this folder", true},
		{"trust option highlighted", "   No, exit\n ❯ Yes, I trust this folder", false},
		{"no dialog", "❯ ", false},
	}
	for _, c := range cases {
		if got := trustDialogSelectsExit(c.content); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
