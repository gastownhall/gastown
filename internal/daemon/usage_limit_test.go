package daemon

import (
	"testing"
	"time"
)

func TestIsClaudeUsageLimit(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		expect bool
	}{
		{
			name:   "empty input is not a usage limit",
			input:  "",
			expect: false,
		},
		{
			name:   "primary Claude usage limit message",
			input:  "Claude usage limit reached. Resets at 7pm.",
			expect: true,
		},
		{
			name:   "Claude AI usage limit variant",
			input:  "Claude AI usage limit reached",
			expect: true,
		},
		{
			name:   "TUI hit-your-limit message",
			input:  "You've hit your weekly limit · resets 7pm (America/Los_Angeles)",
			expect: true,
		},
		{
			name:   "TUI option to wait for reset",
			input:  "  > Stop and wait for limit to reset\n",
			expect: true,
		},
		{
			name:   "API error rate limit",
			input:  "API Error: Rate limit reached for organization",
			expect: true,
		},
		{
			name:   "HTTP 429 too many requests",
			input:  "request failed: 429 Too Many Requests",
			expect: true,
		},
		{
			name:   "JSON rate_limit_error type",
			input:  `{"error":{"type":"rate_limit_error","message":"slow down"}}`,
			expect: true,
		},
		{
			name:   "regular crash with stack trace is not a usage limit",
			input:  "panic: runtime error: invalid memory address\ngoroutine 1 [running]",
			expect: false,
		},
		{
			name:   "tmux pane content with normal output is not a usage limit",
			input:  "✓ Patrol cycle complete\n> awaiting next signal",
			expect: false,
		},
		{
			name:   "EOF / pipe closed is not a usage limit",
			input:  "read /dev/stdin: i/o timeout",
			expect: false,
		},
		{
			name:   "agent comment mentioning rate limit is NOT classified (conservative)",
			input:  "// TODO: handle rate limit responses better",
			expect: false,
		},
		{
			name:   "case-insensitive primary message",
			input:  "CLAUDE USAGE LIMIT REACHED",
			expect: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsClaudeUsageLimit(tc.input)
			if got != tc.expect {
				t.Fatalf("IsClaudeUsageLimit(%q) = %v, want %v", tc.input, got, tc.expect)
			}
		})
	}
}

func TestRestartTracker_RecordPause_DoesNotEscalate(t *testing.T) {
	rt := NewRestartTracker(t.TempDir(), RestartTrackerConfig{
		InitialBackoff:    30 * time.Second,
		MaxBackoff:        10 * time.Minute,
		BackoffMultiplier: 2.0,
		CrashLoopWindow:   15 * time.Minute,
		CrashLoopCount:    3,
		StabilityPeriod:   30 * time.Minute,
		PauseBackoff:      45 * time.Second,
	})

	const id = "deacon"

	// Many pauses in a row should not escalate or trigger crash loop.
	for i := 0; i < 10; i++ {
		rt.RecordPause(id)
	}

	if rt.IsInCrashLoop(id) {
		t.Fatalf("RecordPause should never trigger crash-loop, but did after 10 calls")
	}

	info := rt.state.Agents[id]
	if info == nil {
		t.Fatalf("agent state missing after RecordPause")
	}
	if info.RestartCount != 0 {
		t.Errorf("RecordPause must not increment RestartCount, got %d", info.RestartCount)
	}
	if !info.CrashLoopSince.IsZero() {
		t.Errorf("RecordPause must not set CrashLoopSince, got %v", info.CrashLoopSince)
	}

	remaining := rt.GetBackoffRemaining(id)
	if remaining <= 0 || remaining > 45*time.Second {
		t.Errorf("expected pause backoff ~45s, got %v", remaining)
	}
	if rt.CanRestart(id) {
		t.Errorf("CanRestart should be false during pause backoff window")
	}
}

func TestRestartTracker_RecordRestart_StillEscalates(t *testing.T) {
	// Regression guard: confirm true-crash path is unaffected.
	rt := NewRestartTracker(t.TempDir(), RestartTrackerConfig{
		InitialBackoff:    30 * time.Second,
		MaxBackoff:        10 * time.Minute,
		BackoffMultiplier: 2.0,
		CrashLoopWindow:   15 * time.Minute,
		CrashLoopCount:    3,
		StabilityPeriod:   30 * time.Minute,
		PauseBackoff:      60 * time.Second,
	})

	const id = "deacon"
	for i := 0; i < 3; i++ {
		rt.RecordRestart(id)
	}

	if !rt.IsInCrashLoop(id) {
		t.Fatalf("RecordRestart should trigger crash-loop after CrashLoopCount restarts")
	}
	info := rt.state.Agents[id]
	if info.RestartCount != 3 {
		t.Errorf("expected RestartCount=3, got %d", info.RestartCount)
	}
}
