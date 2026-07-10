package tmux

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSubmitNeedle(t *testing.T) {
	long := strings.Repeat("x", 100)
	tests := []struct {
		name    string
		message string
		want    string
	}{
		{"short message", "resume the patrol", "resume the patrol"},
		{"long message truncated", long, long[:submitNeedleMaxRunes]},
		{"multiline uses first line", "first line\nsecond line", "first line"},
		{"leading blank lines skipped", "\n\n  \nreal content", "real content"},
		{"surrounding space trimmed", "  hello  ", "hello"},
		{"empty message", "", ""},
		{"whitespace only", " \n \n ", ""},
		{"unicode truncated at rune boundary", strings.Repeat("é", 50), strings.Repeat("é", submitNeedleMaxRunes)},
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
	tests := []struct {
		name      string
		input     string
		wantPlain string
		wantDim   string // 'd' for dim, '.' for normal, per rune
	}{
		{
			name:      "plain text",
			input:     "hello",
			wantPlain: "hello",
			wantDim:   ".....",
		},
		{
			name:      "dim span with reset",
			input:     "ab\x1b[2mcd\x1b[0mef",
			wantPlain: "abcdef",
			wantDim:   "..dd..",
		},
		{
			name:      "dim off via 22",
			input:     "\x1b[2mab\x1b[22mcd",
			wantPlain: "abcd",
			wantDim:   "dd..",
		},
		{
			name:      "256-color arg 2 is not dim",
			input:     "\x1b[38;5;2mgreen\x1b[0m",
			wantPlain: "green",
			wantDim:   ".....",
		},
		{
			name:      "truecolor args not dim",
			input:     "\x1b[38;2;10;20;30mrgb\x1b[0m",
			wantPlain: "rgb",
			wantDim:   "...",
		},
		{
			name:      "combined params",
			input:     "\x1b[1;2;31mx\x1b[my",
			wantPlain: "xy",
			wantDim:   "d.",
		},
		{
			name:      "OSC sequence stripped",
			input:     "\x1b]0;title\x07text",
			wantPlain: "text",
			wantDim:   "....",
		},
		{
			name:      "cursor movement stripped",
			input:     "a\x1b[2Ab",
			wantPlain: "ab",
			wantDim:   "..",
		},
		{
			name:      "unicode preserved",
			input:     "❯ \x1b[2mghost\x1b[0m",
			wantPlain: "❯ ghost",
			wantDim:   "..ddddd",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plain, dim := stripAnsiTrackDim(tt.input)
			if string(plain) != tt.wantPlain {
				t.Errorf("plain = %q, want %q", string(plain), tt.wantPlain)
			}
			if len(dim) != len(plain) {
				t.Fatalf("dim length %d != plain length %d", len(dim), len(plain))
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
	const needle = "resume the patrol"
	const prompt = DefaultReadyPromptPrefix // "❯ "

	tests := []struct {
		name    string
		content string
		want    submitProbe
	}{
		{
			name:    "busy indicator means turn started",
			content: "some transcript\n❯ resume the patrol\n· Thinking… (esc to interrupt)",
			want:    probeTurnStarted,
		},
		{
			name:    "normal-intensity text in composer is stranded",
			content: "transcript above\n❯ resume the patrol\n⏵⏵ bypass permissions",
			want:    probeStranded,
		},
		{
			name:    "dim ghost text is not stranded",
			content: "transcript above\n❯ \x1b[2mresume the patrol\x1b[0m\n⏵⏵ bypass permissions",
			want:    probeComposerCleared,
		},
		{
			name:    "empty composer is cleared",
			content: "transcript above\n❯\n⏵⏵ bypass permissions",
			want:    probeComposerCleared,
		},
		{
			name:    "different composer content is cleared",
			content: "transcript above\n❯ something else entirely\n",
			want:    probeComposerCleared,
		},
		{
			name:    "no composer line found is unknown",
			content: "just some\nshell output\nwithout a prompt",
			want:    probeUnknown,
		},
		{
			name: "wrapped input detected via prefix fallback",
			// Composer wraps: only the first part of the needle fits on the
			// prompt line; the visible content is a prefix of the needle.
			content: "transcript\n❯ resume the\n patrol\n",
			want:    probeStranded,
		},
		{
			name: "typed text echoed below prompt is not composer content",
			// A fake idle pane (printf prompt + cat) tty-echoes delivered
			// input below the prompt line; the composer itself is empty.
			// Regression: internal/mail TestNotifyRecipient_IdleAgent.
			content: "❯ \nresume the patrol\nresume the patrol\n",
			want:    probeComposerCleared,
		},
		{
			name:    "dim wrapped prefix is ghost text",
			content: "transcript\n❯ \x1b[2mresume the\x1b[0m\n",
			want:    probeComposerCleared,
		},
		{
			name: "bottom-most prompt line wins",
			// An old prompt with text sits in the transcript; the live
			// composer below it is empty → cleared.
			content: "❯ resume the patrol\nagent response here\n❯\n",
			want:    probeComposerCleared,
		},
		{
			name:    "needle above composer in transcript does not strand",
			content: "> resume the patrol\nagent working on it\n❯\n",
			want:    probeComposerCleared,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := analyzeSubmission(tt.content, needle, prompt); got != tt.want {
				t.Errorf("analyzeSubmission(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}

	t.Run("empty needle is unknown", func(t *testing.T) {
		if got := analyzeSubmission("❯ anything", "", prompt); got != probeUnknown {
			t.Errorf("analyzeSubmission with empty needle = %v, want probeUnknown", got)
		}
	})
}

// TestAnalyzeSubmission_PrefixFallback pins the narrow-pane fallback: composer
// content that is a prefix of the needle counts as stranded only when it is
// long enough (minStrandPrefixRunes) to be unambiguous.
func TestAnalyzeSubmission_PrefixFallback(t *testing.T) {
	// 8-rune prefix "abcdefgh" of a longer needle → stranded.
	if got := analyzeSubmission("❯ abcdefgh\n", "abcdefghij", DefaultReadyPromptPrefix); got != probeStranded {
		t.Errorf("long prefix = %v, want probeStranded", got)
	}
	// 4-rune fragment is too short to be conclusive → cleared.
	if got := analyzeSubmission("❯ abcd\n", "abcdefghij", DefaultReadyPromptPrefix); got != probeComposerCleared {
		t.Errorf("short fragment = %v, want probeComposerCleared", got)
	}
	// Non-prefix content of qualifying length → cleared.
	if got := analyzeSubmission("❯ zzzzzzzzzz\n", "abcdefghij", DefaultReadyPromptPrefix); got != probeComposerCleared {
		t.Errorf("non-prefix content = %v, want probeComposerCleared", got)
	}
}

// TestErrSubmitNotVerified_Wrapping pins the errors.Is contract callers rely
// on for the queue fallback.
func TestErrSubmitNotVerified_Wrapping(t *testing.T) {
	wrapped := fmt.Errorf("nudge to session %q: %w",
		"gt-deacon", fmt.Errorf("nudge submit to %q: %w", "gt-deacon", ErrSubmitNotVerified))
	if !errors.Is(wrapped, ErrSubmitNotVerified) {
		t.Fatal("double-wrapped ErrSubmitNotVerified not detected by errors.Is")
	}
}

// TestProbeSubmission_LivePane exercises the composer probe against a real
// tmux pane (capture-only). A fake composer is rendered with printf so the
// probe's -e capture and dim detection run against genuine tmux output.
func TestProbeSubmission_LivePane(t *testing.T) {
	tm := newTestTmux(t)
	session := "gt-test-probe-submit"

	_ = tm.KillSession(session)
	if err := tm.NewSession(session, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = tm.KillSession(session) }()

	const needle = "resume the patrol"
	prompt := DefaultReadyPromptPrefix

	// The town shell hook can show an interactive "Add to Gas Town?" prompt
	// on startup that swallows the first typed command. Send a probe command
	// until the shell is demonstrably executing input.
	readyDeadline := time.Now().Add(15 * time.Second)
	for {
		_ = tm.SendKeys(session, `echo shell-ready-$((40+2))`)
		time.Sleep(300 * time.Millisecond)
		out, _ := tm.CapturePane(session, 30)
		if strings.Contains(out, "shell-ready-42") {
			break
		}
		if time.Now().After(readyDeadline) {
			t.Skipf("shell never became ready for input; pane:\n%s", out)
		}
		time.Sleep(700 * time.Millisecond)
	}

	waitForProbe := func(desc string, want submitProbe) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		var got submitProbe
		for time.Now().Before(deadline) {
			got = tm.probeSubmission(session, needle, prompt)
			if got == want {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		out, _ := tm.CapturePane(session, 20)
		t.Fatalf("%s: probe = %v, want %v; pane:\n%s", desc, got, want, out)
	}

	// No composer line yet (plain shell prompt) → unknown.
	waitForProbe("plain shell", probeUnknown)

	// Render a stranded composer: normal-intensity text after ❯.
	// (\342\235\257 is the UTF-8 octal encoding of ❯.)
	if err := tm.SendKeys(session, `printf '\342\235\257 resume the patrol'`); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}
	waitForProbe("stranded composer", probeStranded)

	// Render a dim ghost composer → cleared, not stranded.
	if err := tm.SendKeys(session, "clear"); err != nil {
		t.Fatalf("SendKeys clear: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := tm.SendKeys(session, `printf '\342\235\257 \033[2mresume the patrol\033[0m'`); err != nil {
		t.Fatalf("SendKeys dim: %v", err)
	}
	waitForProbe("dim ghost composer", probeComposerCleared)

	// Render a busy indicator → turn started.
	if err := tm.SendKeys(session, "clear"); err != nil {
		t.Fatalf("SendKeys clear: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := tm.SendKeys(session, `printf 'esc to interrupt'`); err != nil {
		t.Fatalf("SendKeys busy: %v", err)
	}
	waitForProbe("busy indicator", probeTurnStarted)
}
