package tmux

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSubmitNeedle(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 100)
	tests := []struct {
		name    string
		message string
		want    string
	}{
		{"short message", "resume the patrol", "resume the patrol"},
		{"long message truncated", long, long[:submitNeedleMaxRunes]},
		{"multiline uses first line", "first line\nsecond line", "first line"},
		{"leading blanks skipped", "\n  \nreal content", "real content"},
		{"space trimmed", "  hello  ", "hello"},
		{"empty", "", ""},
		{"unicode rune boundary", strings.Repeat("é", 50), strings.Repeat("é", submitNeedleMaxRunes)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := submitNeedle(tt.message); got != tt.want {
				t.Errorf("submitNeedle(%q) = %q, want %q", tt.message, got, tt.want)
			}
		})
	}
}

func TestStripAnsiTrackDim(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		input     string
		wantPlain string
		wantDim   string
	}{
		{"plain", "hello", "hello", "....."},
		{"dim span", "ab\x1b[2mcd\x1b[0mef", "abcdef", "..dd.."},
		{"dim off", "\x1b[2mab\x1b[22mcd", "abcd", "dd.."},
		{"256 color arg 2 is not dim", "\x1b[38;5;2mgreen\x1b[0m", "green", "....."},
		{"truecolor args are not dim", "\x1b[38;2;10;20;30mrgb\x1b[0m", "rgb", "..."},
		{"osc stripped", "\x1b]0;title\x07text", "text", "...."},
		{"unicode preserved", "❯ \x1b[2mghost\x1b[0m", "❯ ghost", "..ddddd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plain, dim := stripAnsiTrackDim(tt.input)
			if string(plain) != tt.wantPlain {
				t.Errorf("plain = %q, want %q", string(plain), tt.wantPlain)
			}
			var got strings.Builder
			for _, d := range dim {
				if d {
					got.WriteByte('d')
				} else {
					got.WriteByte('.')
				}
			}
			if got.String() != tt.wantDim {
				t.Errorf("dim = %q, want %q", got.String(), tt.wantDim)
			}
		})
	}
}

func TestAnalyzeSubmission(t *testing.T) {
	t.Parallel()
	const needle = "resume the patrol"
	const prompt = DefaultReadyPromptPrefix

	tests := []struct {
		name    string
		content string
		want    submitProbe
	}{
		{
			name:    "normal text beats stale busy indicator",
			content: "transcript\n❯ resume the patrol\n· Thinking... (esc to interrupt)",
			want:    probeStranded,
		},
		{
			name:    "busy without composer means turn started",
			content: "agent output\n· Thinking... (esc to interrupt)",
			want:    probeTurnStarted,
		},
		{
			name:    "dim ghost text is cleared",
			content: "transcript\n❯ \x1b[2mresume the patrol\x1b[0m\n",
			want:    probeComposerCleared,
		},
		{
			name:    "empty composer is cleared",
			content: "transcript\n❯\n",
			want:    probeComposerCleared,
		},
		{
			name:    "different normal composer text is dirty",
			content: "transcript\n❯ unrelated draft\n",
			want:    probeComposerDirty,
		},
		{
			name:    "wrapped prefix counts as stranded",
			content: "transcript\n❯ resume the\n patrol\n",
			want:    probeStranded,
		},
		{
			name:    "short prefix is too ambiguous",
			content: "transcript\n❯ resu\n",
			want:    probeComposerDirty,
		},
		{
			name:    "bottom-most prompt line wins",
			content: "❯ resume the patrol\nagent response\n❯\n",
			want:    probeComposerCleared,
		},
		{
			name:    "typed text echoed below prompt is not composer content",
			content: "❯ \nresume the patrol\nresume the patrol\n",
			want:    probeComposerCleared,
		},
		{
			name:    "no prompt or busy is unknown",
			content: "shell output\nwithout prompt",
			want:    probeUnknown,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := analyzeSubmission(tt.content, needle, prompt); got != tt.want {
				t.Errorf("analyzeSubmission() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestErrSubmitNotVerifiedWrapping(t *testing.T) {
	t.Parallel()
	wrapped := fmt.Errorf("nudge to session: %w", fmt.Errorf("submit: %w", ErrSubmitNotVerified))
	if !errors.Is(wrapped, ErrSubmitNotVerified) {
		t.Fatal("errors.Is did not recognize wrapped ErrSubmitNotVerified")
	}
}
