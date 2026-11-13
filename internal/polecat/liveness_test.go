package polecat

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/tmux"
)

type fakeLiveness struct {
	health   tmux.ZombieStatus
	pane     string
	paneErr  error
	gotBound time.Duration
}

func (f *fakeLiveness) CheckSessionHealth(_ string, maxInactivity time.Duration) tmux.ZombieStatus {
	f.gotBound = maxInactivity
	return f.health
}

func (f *fakeLiveness) CapturePane(_ string, _ int) (string, error) {
	return f.pane, f.paneErr
}

// The verbatim pane text measured on rho-obsidian, rho-onyx and rho-quartz
// during hq-3l8r, hard-wrapped the way tmux wraps a pane. All three polecats
// reported "working" while showing this.
//
// Every wrap deliberately falls MID-PHRASE, so no single line matches any
// pattern on its own. That is not contrived: tmux wraps at the pane width, which
// nothing here controls, and a matcher that tests one line at a time silently
// misses these panes.
const codexAuthPane = `> go test ./internal/polygon/settlement/...
ok  	github.com/4rho/4rho/internal/polygon/settlement	10.770s

Your access token could not
be refreshed because you have since logged out or signed
in to another account. Please sign
in again.

  Ask Codex to do anything
`

func TestSessionBlockedReason(t *testing.T) {
	t.Parallel()

	healthyPane := `⏺ Running tests…
  Bash: go test ./internal/scheduler/...
· Working (10s · esc to interrupt)
`

	tests := []struct {
		name       string
		fake       fakeLiveness
		wantReason bool
		wantSubstr string
	}{{
		// The exact production condition: process alive, TUI redrawing, agent dead.
		name:       "auth error in pane while session looks healthy",
		fake:       fakeLiveness{health: tmux.SessionHealthy, pane: codexAuthPane},
		wantReason: true,
		wantSubstr: "access token could not be refreshed",
	}, {
		name:       "quota wall in pane",
		fake:       fakeLiveness{health: tmux.SessionHealthy, pane: "You've hit your usage limit. Visit chatgpt.com to purchase more credits.\n"},
		wantReason: true,
		wantSubstr: "hit your usage limit",
	}, {
		// The general arm: no pane output at all. Cause unknown and irrelevant.
		name:       "no pane output",
		fake:       fakeLiveness{health: tmux.AgentHung, pane: healthyPane},
		wantReason: true,
		wantSubstr: "no pane output",
	}, {
		name:       "agent process gone",
		fake:       fakeLiveness{health: tmux.AgentDead, pane: healthyPane},
		wantReason: true,
		wantSubstr: "agent process is gone",
	}, {
		// No session is the caller's "stalled"; different remedy, not ours to claim.
		name: "no session at all",
		fake: fakeLiveness{health: tmux.SessionDead, pane: codexAuthPane},
	}, {
		name: "genuinely working",
		fake: fakeLiveness{health: tmux.SessionHealthy, pane: healthyPane},
	}, {
		// An unreadable pane must not manufacture a verdict.
		name: "pane capture fails",
		fake: fakeLiveness{health: tmux.SessionHealthy, paneErr: errors.New("no server")},
	}, {
		// A resolved error scrolled above the check window must stop firing.
		name: "auth error scrolled out of the check window",
		fake: fakeLiveness{health: tmux.SessionHealthy, pane: codexAuthPane + strings.Repeat("recovered and working\n", blockedPaneCheckLines+2)},
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := tc.fake
			got := SessionBlockedReason(&fake, "rho-obsidian")
			if tc.wantReason {
				if got == "" {
					t.Fatalf("SessionBlockedReason() = %q, want a blocked reason", got)
				}
				if !strings.Contains(got, tc.wantSubstr) {
					t.Fatalf("SessionBlockedReason() = %q, want it to contain %q", got, tc.wantSubstr)
				}
				return
			}
			if got != "" {
				t.Fatalf("SessionBlockedReason() = %q, want \"\"", got)
			}
		})
	}
}

// TestSessionBlockedReason_PassesInactivityBound guards the one-character
// regression that recreates this bug: passing 0 makes tmux skip the activity
// check entirely, which is what every polecat caller did before hq-3l8r.
func TestSessionBlockedReason_PassesInactivityBound(t *testing.T) {
	t.Parallel()
	fake := fakeLiveness{health: tmux.SessionHealthy}
	SessionBlockedReason(&fake, "rho-obsidian")
	if fake.gotBound <= 0 {
		t.Fatalf("SessionBlockedReason passed maxInactivity=%v to CheckSessionHealth; 0 disables hang detection", fake.gotBound)
	}
}

// TestSessionBlockedReason_NilTmux keeps the tmux-less paths (tests, hosts with
// no tmux) reporting "unknown", never "blocked".
func TestSessionBlockedReason_NilTmux(t *testing.T) {
	t.Parallel()
	if got := SessionBlockedReason(nil, "rho-obsidian"); got != "" {
		t.Fatalf("SessionBlockedReason(nil) = %q, want \"\"", got)
	}
}

// TestSessionBlockedReason_RealTmuxPane runs the pane check against a real tmux
// server, because the risk this fix carries is not the regex — it is whether
// capture-pane output still carries the phrase after the terminal has broken it
// across lines. The message is printed pre-split mid-phrase (deterministic at any
// pane width), which is precisely the case a per-line matcher would miss.
func TestSessionBlockedReason_RealTmuxPane(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	tm := tmux.NewTmux()
	sessionName := fmt.Sprintf("gt-test-blocked-%d", os.Getpid())
	// Print the measured error split mid-phrase, then exec sleep so
	// pane_current_command is a process we can declare as the agent (keeping the
	// general arm green, so only the pane text can produce a verdict).
	cmd := `printf '%s\n' 'Your access token could not' 'be refreshed because you have since logged out or signed' 'in to another account. Please sign' 'in again.' '' '  Ask Codex to do anything'; exec sleep 60`
	if err := tm.NewSessionWithCommand(sessionName, "", "sh -c "+shellQuote(cmd)); err != nil {
		t.Skipf("cannot create tmux session: %v", err)
	}
	defer tm.KillSession(sessionName)
	if err := tm.SetEnvironment(sessionName, "GT_PROCESS_NAMES", "sleep"); err != nil {
		t.Fatalf("SetEnvironment: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	if !tm.IsAgentAlive(sessionName) {
		t.Skip("tmux did not report the declared pane process as alive")
	}
	// Sanity: the general arm must be green, so the only thing that can produce a
	// verdict below is the pane text.
	if status := tm.CheckSessionHealth(sessionName, time.Hour); status != tmux.SessionHealthy {
		t.Skipf("session not healthy (%v); pane-text arm not exercised", status)
	}

	got := SessionBlockedReason(tm, sessionName)
	if got == "" {
		pane, _ := tm.CapturePane(sessionName, 30)
		t.Fatalf("SessionBlockedReason() = \"\" for a pane carrying the measured Codex auth error; pane was:\n%s", pane)
	}
	if !strings.Contains(got, "provider error:") {
		t.Fatalf("SessionBlockedReason() = %q, want a provider-error reason", got)
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
