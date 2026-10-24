package polecat

import (
	"regexp"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/tmux"
)

// NoProgressTimeout is how long a live polecat session's pane may produce no
// output at all before the town reports it blocked instead of working.
//
// Agent TUIs redraw an elapsed-time counter roughly every second while they run
// ("Working (10s)"), so a pane that has not changed for this long is not
// thinking — it is stopped. The bound is deliberately the town's existing
// "no progress with hooked work" bound so there is one number, not two.
//
// ponytail: a constant, not a config knob. Add the knob only if a real agent is
// ever observed genuinely working with a completely silent pane for this long.
var NoProgressTimeout = constants.GUPPViolationTimeout

// SessionLiveness is the subset of tmux needed to judge whether a session is
// making progress. It exists so the judgement is testable without a tmux
// server; *tmux.Tmux is the only production implementation.
type SessionLiveness interface {
	CheckSessionHealth(session string, maxInactivity time.Duration) tmux.ZombieStatus
	CapturePane(session string, lines int) (string, error)
}

// blockedPaneScanLines / blockedPaneCheckLines mirror internal/quota's scan
// window: capture generously, match only the bottom, so an error that has since
// scrolled up out of view does not keep firing.
const (
	blockedPaneScanLines  = 30
	blockedPaneCheckLines = 20
)

// blockedPanePatterns are the provider errors that end a session's ability to
// act. Rate-limit messages count: an agent that is being refused is not working,
// whichever way the provider says no.
var blockedPanePatterns = compileBlockedPatterns()

func compileBlockedPatterns() []*regexp.Regexp {
	raw := make([]string, 0, len(constants.DefaultRateLimitPatterns)+len(constants.AgentBlockedPatterns))
	raw = append(raw, constants.DefaultRateLimitPatterns...)
	raw = append(raw, constants.AgentBlockedPatterns...)

	out := make([]*regexp.Regexp, 0, len(raw))
	for _, p := range raw {
		if re, err := regexp.Compile("(?i)" + p); err == nil {
			out = append(out, re)
		}
	}
	return out
}

// SessionBlockedReason reports why a live polecat session shows no evidence of
// progress, or "" when it shows some.
//
// The presence of a tmux session is not evidence of progress, and collapsing the
// two is the bug this closes (hq-3l8r). The primary check here is general and
// cause-blind — is the agent process still there, and has the pane emitted
// anything at all recently — so a failure mode nobody has seen yet still trips
// it. The pane-text check is a second, INDEPENDENT trigger for the case where a
// provider error stops the agent while its TUI keeps redrawing. It can only add
// a verdict, never suppress one.
//
// A missing session returns "" — that is the caller's "stalled", not this
// function's "blocked", and the two have different remedies.
//
// ponytail: two tmux round-trips per LIVE polecat session (a health probe, plus
// a pane capture only when the health probe is green). Callers already hold a
// single ListSessions result, so nothing is paid for polecats with no session.
// Known ceiling: tmux counts an operator ATTACHING as activity, so a session
// someone is watching cannot be judged hung while they watch it. The pane-text
// arm still fires there. Batch via `list-panes -a` if this ever shows up in
// `gt sling` admission latency.
func SessionBlockedReason(t SessionLiveness, sessionName string) string {
	if t == nil {
		return ""
	}
	switch t.CheckSessionHealth(sessionName, NoProgressTimeout) {
	case tmux.SessionDead:
		return ""
	case tmux.AgentDead:
		return "agent process is gone"
	case tmux.AgentHung:
		return "no pane output for " + NoProgressTimeout.String()
	}
	if line := blockedPaneLine(t, sessionName); line != "" {
		return "provider error: " + line
	}
	return ""
}

// blockedPaneLine returns the matched provider-error text from the bottom of the
// pane, or "" when there is none.
//
// The bottom lines are joined before matching, not tested one at a time: tmux
// hard-wraps a pane at its width, and every message worth matching here is long
// enough to be split mid-phrase in a narrow pane. Per-line matching would miss
// exactly the panes this exists to catch.
func blockedPaneLine(t SessionLiveness, sessionName string) string {
	content, err := t.CapturePane(sessionName, blockedPaneScanLines)
	if err != nil {
		// Cannot see the pane. Unknown is not blocked — the general check above
		// already had its say.
		return ""
	}
	lines := strings.Split(content, "\n")
	if n := len(lines) - blockedPaneCheckLines; n > 0 {
		lines = lines[n:]
	}
	bottom := strings.Join(strings.Fields(strings.Join(lines, " ")), " ")
	for _, re := range blockedPanePatterns {
		if match := re.FindString(bottom); match != "" {
			return match
		}
	}
	return ""
}
