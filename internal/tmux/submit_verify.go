package tmux

// submit_verify.go — composer-state submission verification for nudge delivery.
//
// Evidence (openclaw op-03ke; hq-zrpgf / hq-ygybv): the nudge/daemon-wake
// delivery path types the payload into the target Claude Code pane, but the
// trailing Enter is sometimes swallowed when the composer buffer is in a bad
// state (suspected lost/duplicated bracketed-paste boundary). The text then
// sits unsubmitted in the composer until something resets the buffer — five
// documented deacon strandings on 2026-07-09/10, each costing a patrol cycle.
//
// Live debugging of occurrence #5 (hq-zrpgf) showed:
//   - printable keys still echo, so the TUI event loop is alive
//   - Enter / C-m / CSI-u 13u / Escape+Enter are ALL swallowed
//   - C-j (LF) clears the parked buffer without submitting
//   - after that clear, retyping the text + plain Enter submits normally
//
// sendEnterVerified's "did the pane change" heuristic cannot catch this: the
// pane keeps repainting (spinner, clock, token counter) while the composer
// stays wedged, and its remedy — more Enters — is exactly the keystroke being
// swallowed. Verification here inspects the *composer state* instead: a nudge
// counts as submitted only when the turn visibly started (busy indicator) or
// the typed text is no longer sitting in the composer at normal intensity.
//
// Dim (SGR 2) composer text is Claude Code's ghost placeholder replaying a
// previous draft over an EMPTY composer and must NOT be treated as stranded
// input — boot's triage on hq-zrpgf documented a false-positive rescue caused
// by exactly that confusion. Capturing with escape sequences (-e) lets us
// tell the two apart.

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// ErrSubmitNotVerified reports that a nudge's payload was typed into the
// target composer but its submission could not be verified: the text was
// still sitting in the composer (normal intensity) after the Enter retries
// and the C-j buffer-reset remedy. Callers should fall back to queueing so
// the message is not lost.
var ErrSubmitNotVerified = errors.New("submit not verified: message stranded in composer")

// submitProbe classifies the state of a target composer after a submit
// keystroke was sent.
type submitProbe int

const (
	// probeUnknown means the pane could not be captured or no composer line
	// was found — verification is impossible, fall back to best-effort.
	probeUnknown submitProbe = iota
	// probeTurnStarted means the busy indicator is visible — the agent is
	// working, so the submission definitely went through.
	probeTurnStarted
	// probeComposerCleared means the composer no longer contains the typed
	// text at normal intensity (empty, different content, or dim ghost text).
	probeComposerCleared
	// probeStranded means the typed text is still sitting in the composer at
	// normal intensity — the submit keystroke was swallowed.
	probeStranded
)

// submitProbeAttempts and submitProbeInterval bound the post-Enter
// verification poll: attempt, sleep, attempt, ... (~1.4s total for 3
// attempts), matching the 1-2s x3 window from the op-03ke spec.
const (
	submitProbeAttempts = 3
	submitProbeInterval = 700 * time.Millisecond
)

// submitNeedleMaxRunes bounds the needle length so it fits on the composer's
// first rendered line even in narrow panes (composer wraps long input).
const submitNeedleMaxRunes = 32

// submitNeedle returns a short distinctive prefix of the typed message used
// to detect it sitting unsubmitted in the composer. Empty when the message
// has no printable content.
func submitNeedle(message string) string {
	for _, line := range strings.Split(message, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		runes := []rune(line)
		if len(runes) > submitNeedleMaxRunes {
			return string(runes[:submitNeedleMaxRunes])
		}
		return line
	}
	return ""
}

// applySGR updates the dim flag for one CSI ... m parameter string.
// Handles: 0/empty (reset all), 2 (dim on), 22 (dim/bold off), and skips the
// arguments of extended-color sequences (38;5;n / 38;2;r;g;b and the 48/58
// variants) so their literal "2" params don't read as dim.
func applySGR(params string, dim bool) bool {
	if params == "" {
		return false
	}
	fields := strings.Split(params, ";")
	for k := 0; k < len(fields); k++ {
		switch fields[k] {
		case "", "0":
			dim = false
		case "2":
			dim = true
		case "22":
			dim = false
		case "38", "48", "58":
			if k+1 < len(fields) {
				switch fields[k+1] {
				case "5":
					k += 2
				case "2":
					k += 4
				}
			}
		}
	}
	return dim
}

// stripAnsiTrackDim strips ANSI escape sequences from s, returning the plain
// runes plus a parallel slice marking which runes were rendered dim (SGR 2).
// Only SGR state relevant to dim detection is tracked; all other escape
// sequences (cursor movement, OSC titles, etc.) are dropped.
func stripAnsiTrackDim(s string) ([]rune, []bool) {
	var plain []rune
	var dim []bool
	cur := false
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			if i+1 < len(s) && s[i+1] == '[' {
				// CSI: ESC [ params final-byte
				j := i + 2
				for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
					j++
				}
				if j < len(s) {
					if s[j] == 'm' {
						cur = applySGR(s[i+2:j], cur)
					}
					i = j + 1
				} else {
					i = len(s)
				}
				continue
			}
			if i+1 < len(s) && s[i+1] == ']' {
				// OSC: ESC ] ... (BEL | ESC \)
				j := i + 2
				for j < len(s) && s[j] != 0x07 && !(s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\') {
					j++
				}
				if j < len(s) {
					if s[j] == 0x1b {
						j++ // consume the backslash of ST too
					}
					i = j + 1
				} else {
					i = len(s)
				}
				continue
			}
			// Other two-byte escape (ESC c, ESC =, ...)
			i += 2
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		plain = append(plain, r)
		dim = append(dim, cur)
		i += size
	}
	return plain, dim
}

// runeIndex returns the index of the first occurrence of needle in haystack,
// or -1 if absent.
func runeIndex(haystack, needle []rune) int {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return -1
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// minStrandPrefixRunes is the minimum length of composer content required for
// the prefix-match fallback (narrow panes that truncate the needle) to count
// as stranded input. Shorter fragments are too ambiguous.
const minStrandPrefixRunes = 8

// analyzeSubmission classifies a pane capture (taken with -e so escape
// sequences are preserved) after a submit keystroke:
//
//   - any busy indicator → probeTurnStarted
//   - no composer line (prompt prefix) found → probeUnknown
//   - needle present at normal intensity on the composer line (the
//     bottom-most prompt line) → probeStranded; a composer whose visible
//     content is a ≥8-rune prefix of the needle (input truncated by pane
//     width or wrap) also counts as stranded
//   - needle absent or rendered dim (ghost placeholder) → probeComposerCleared
//
// Only the composer line itself is inspected — text on lines below it is
// transcript/status output, not composer content (a fake idle pane that
// tty-echoes typed input below its prompt must not read as stranded).
func analyzeSubmission(escContent, needle, promptPrefix string) submitProbe {
	if needle == "" {
		return probeUnknown
	}
	plain, dim := stripAnsiTrackDim(escContent)

	// Split into lines while keeping the dim flags aligned.
	var lines [][]rune
	var lineDims [][]bool
	start := 0
	for i := 0; i <= len(plain); i++ {
		if i == len(plain) || plain[i] == '\n' {
			lines = append(lines, plain[start:i])
			lineDims = append(lineDims, dim[start:i])
			start = i + 1
		}
	}

	for _, ln := range lines {
		if hasBusyIndicator(string(ln)) {
			return probeTurnStarted
		}
	}

	// Bottom-most prompt line is the live composer; transcript echoes of past
	// messages render with a different prefix.
	composerIdx := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if matchesPromptPrefix(string(lines[i]), promptPrefix) {
			composerIdx = i
			break
		}
	}
	if composerIdx == -1 {
		return probeUnknown
	}

	line := lines[composerIdx]
	lineDim := lineDims[composerIdx]

	if idx := runeIndex(line, []rune(needle)); idx >= 0 {
		if lineDim[idx] {
			// Dim = Claude Code's ghost/draft placeholder over an empty
			// composer (hq-zrpgf boot triage), not stranded input.
			return probeComposerCleared
		}
		return probeStranded
	}

	// Prefix fallback: in a narrow pane the composer line truncates/wraps the
	// input before the full needle fits. If the visible content after the
	// prompt is a sufficiently long prefix of the needle, the message is
	// still sitting there.
	isSpace := func(r rune) bool { return r == ' ' || r == '\u00a0' || r == '\t' }
	promptRunes := []rune(strings.TrimSpace(promptPrefix))
	if len(promptRunes) == 0 {
		return probeComposerCleared
	}
	pos := runeIndex(line, promptRunes[:1])
	if pos < 0 {
		return probeComposerCleared
	}
	after := line[pos+1:]
	afterDim := lineDim[pos+1:]
	// Trim surrounding whitespace (incl. NBSP), keeping dim flags aligned.
	for len(after) > 0 && isSpace(after[0]) {
		after = after[1:]
		afterDim = afterDim[1:]
	}
	for len(after) > 0 && isSpace(after[len(after)-1]) {
		after = after[:len(after)-1]
		afterDim = afterDim[:len(afterDim)-1]
	}
	if len(after) >= minStrandPrefixRunes && strings.HasPrefix(needle, string(after)) {
		if afterDim[0] {
			return probeComposerCleared
		}
		return probeStranded
	}
	return probeComposerCleared
}

// probeSubmission captures the target pane with escape sequences (so dim
// ghost text is distinguishable from real input) and classifies the
// composer state.
func (t *Tmux) probeSubmission(target, needle, promptPrefix string) submitProbe {
	content, err := t.run("capture-pane", "-p", "-e", "-t", target, "-S", "-25")
	if err != nil {
		return probeUnknown
	}
	return analyzeSubmission(content, needle, promptPrefix)
}

// pollSubmission probes up to attempts times, returning early on any
// conclusive success (turn started / composer cleared). The first probe is
// immediate — callers have already slept after the submit keystroke.
func (t *Tmux) pollSubmission(target, needle, promptPrefix string, attempts int) submitProbe {
	last := probeUnknown
	for i := 0; i < attempts; i++ {
		if i > 0 {
			time.Sleep(submitProbeInterval)
		}
		last = t.probeSubmission(target, needle, promptPrefix)
		if last == probeTurnStarted || last == probeComposerCleared {
			return last
		}
	}
	return last
}

// submitComposer submits a just-typed message and verifies it actually left
// the composer. Layered defense:
//
//  1. sendEnterVerified — the existing load-race guard (GH#gt-0b5): Enter with
//     content-change polling and Enter retries.
//  2. Composer-strand verification (op-03ke): confirm the typed text is no
//     longer parked in the composer. If it is, Enter is being swallowed.
//  3. C-j remedy: reset the wedged input buffer, retype if C-j cleared the
//     text, submit, and re-verify.
//
// Returns an error wrapping ErrSubmitNotVerified when the message is still
// stranded after all remedies, so callers can fall back to queueing.
func (t *Tmux) submitComposer(target, message string) error {
	enterErr := t.sendEnterVerified(target)

	needle := submitNeedle(message)
	if needle == "" {
		return enterErr
	}
	promptPrefix := readyPromptPrefixForSession(t, target)

	switch t.pollSubmission(target, needle, promptPrefix, submitProbeAttempts) {
	case probeTurnStarted, probeComposerCleared:
		return nil
	case probeUnknown:
		// Composer state not observable — fall back to the legacy verdict.
		return enterErr
	}

	// Stranded: the composer still shows the message at normal intensity, so
	// every Enter so far was swallowed. Apply the C-j remedy.
	return t.recoverStrandedComposer(target, message, needle, promptPrefix)
}

// recoverStrandedComposer applies boot's evidenced remedy for a wedged
// composer (hq-zrpgf occurrence #5): C-j clears the parked buffer without
// submitting; after that reset, retyping the text and sending a plain Enter
// submits normally.
func (t *Tmux) recoverStrandedComposer(target, message, needle, promptPrefix string) error {
	if _, err := t.run("send-keys", "-t", target, "C-j"); err != nil {
		return fmt.Errorf("%w (C-j reset failed: %v)", ErrSubmitNotVerified, err)
	}
	time.Sleep(500 * time.Millisecond)

	switch t.probeSubmission(target, needle, promptPrefix) {
	case probeTurnStarted:
		return nil // C-j itself submitted (LF-as-submit state)
	case probeStranded:
		// Buffer didn't clear, but C-j may still have reset the wedged input
		// state — try one more plain Enter before giving up.
		if _, err := t.run("send-keys", "-t", target, "Enter"); err != nil {
			return fmt.Errorf("%w (Enter retry failed: %v)", ErrSubmitNotVerified, err)
		}
	case probeComposerCleared:
		// C-j cleared the parked text without submitting — retype and submit.
		if err := t.sendMessageToTarget(target, message); err != nil {
			return fmt.Errorf("%w (retype failed: %v)", ErrSubmitNotVerified, err)
		}
		time.Sleep(adaptiveTextDelay(len(message)))
		if _, err := t.run("send-keys", "-t", target, "Enter"); err != nil {
			return fmt.Errorf("%w (Enter after retype failed: %v)", ErrSubmitNotVerified, err)
		}
	case probeUnknown:
		// Pane no longer observable — best-effort Enter, can't verify further.
		_, _ = t.run("send-keys", "-t", target, "Enter")
		return nil
	}

	switch t.pollSubmission(target, needle, promptPrefix, submitProbeAttempts) {
	case probeTurnStarted, probeComposerCleared, probeUnknown:
		return nil
	}
	return fmt.Errorf("nudge submit to %q: %w", target, ErrSubmitNotVerified)
}
