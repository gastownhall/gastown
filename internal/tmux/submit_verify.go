package tmux

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// ErrSubmitNotVerified reports that a nudge payload was typed, but the
// transport could not prove it left the target composer.
var ErrSubmitNotVerified = errors.New("submit not verified: message stranded in composer")

type submitProbe int

const (
	probeUnknown submitProbe = iota
	probeTurnStarted
	probeComposerCleared
	probeStranded
	probeComposerDirty
)

func (p submitProbe) String() string {
	switch p {
	case probeTurnStarted:
		return "turn-started"
	case probeComposerCleared:
		return "composer-cleared"
	case probeStranded:
		return "stranded"
	case probeComposerDirty:
		return "composer-dirty"
	default:
		return "unknown"
	}
}

const (
	submitProbeAttempts  = 3
	submitProbeInterval  = 700 * time.Millisecond
	submitNeedleMaxRunes = 32
	minStrandPrefixRunes = 8
)

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

func applySGR(params string, dim bool) bool {
	if params == "" {
		return false
	}
	fields := strings.Split(params, ";")
	for i := 0; i < len(fields); i++ {
		switch fields[i] {
		case "", "0":
			dim = false
		case "2":
			dim = true
		case "22":
			dim = false
		case "38", "48", "58":
			if i+1 >= len(fields) {
				continue
			}
			switch fields[i+1] {
			case "5":
				i += 2
			case "2":
				i += 4
			}
		}
	}
	return dim
}

func stripAnsiTrackDim(s string) ([]rune, []bool) {
	var plain []rune
	var dim []bool
	curDim := false
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			if i+1 < len(s) && s[i+1] == '[' {
				j := i + 2
				for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
					j++
				}
				if j >= len(s) {
					break
				}
				if s[j] == 'm' {
					curDim = applySGR(s[i+2:j], curDim)
				}
				i = j + 1
				continue
			}
			if i+1 < len(s) && s[i+1] == ']' {
				j := i + 2
				for j < len(s) && s[j] != 0x07 && !(s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\') {
					j++
				}
				if j >= len(s) {
					break
				}
				if s[j] == 0x1b {
					j++
				}
				i = j + 1
				continue
			}
			i += 2
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		plain = append(plain, r)
		dim = append(dim, curDim)
		i += size
	}
	return plain, dim
}

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

func allDim(flags []bool) bool {
	if len(flags) == 0 {
		return false
	}
	for _, flag := range flags {
		if !flag {
			return false
		}
	}
	return true
}

func splitRunesAndDim(plain []rune, dim []bool) ([][]rune, [][]bool) {
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
	return lines, lineDims
}

func trimRunesAndDim(runes []rune, dim []bool) ([]rune, []bool) {
	isSpace := func(r rune) bool { return r == ' ' || r == '\t' || r == '\u00a0' }
	for len(runes) > 0 && isSpace(runes[0]) {
		runes = runes[1:]
		dim = dim[1:]
	}
	for len(runes) > 0 && isSpace(runes[len(runes)-1]) {
		runes = runes[:len(runes)-1]
		dim = dim[:len(dim)-1]
	}
	return runes, dim
}

func composerContent(line []rune, dim []bool, promptPrefix string) ([]rune, []bool) {
	prefix := []rune(strings.TrimSpace(strings.ReplaceAll(promptPrefix, "\u00a0", " ")))
	if len(prefix) == 0 {
		return nil, nil
	}
	idx := runeIndex(line, prefix)
	if idx < 0 {
		idx = runeIndex(line, prefix[:1])
	}
	if idx < 0 {
		return nil, nil
	}
	after := line[idx+len(prefix):]
	afterDim := dim[idx+len(prefix):]
	return trimRunesAndDim(after, afterDim)
}

func analyzeComposerLine(line []rune, dim []bool, needle, promptPrefix string) submitProbe {
	needleRunes := []rune(needle)
	if idx := runeIndex(line, needleRunes); idx >= 0 {
		if allDim(dim[idx : idx+len(needleRunes)]) {
			return probeComposerCleared
		}
		return probeStranded
	}

	content, contentDim := composerContent(line, dim, promptPrefix)
	if len(content) == 0 {
		return probeComposerCleared
	}
	if allDim(contentDim) {
		return probeComposerCleared
	}
	if len(content) >= minStrandPrefixRunes && strings.HasPrefix(needle, string(content)) {
		return probeStranded
	}
	return probeComposerDirty
}

func analyzeSubmission(escContent, needle, promptPrefix string) submitProbe {
	if needle == "" || promptPrefix == "" {
		return probeUnknown
	}
	plain, dim := stripAnsiTrackDim(escContent)
	lines, lineDims := splitRunesAndDim(plain, dim)

	for i := len(lines) - 1; i >= 0; i-- {
		if matchesPromptPrefix(string(lines[i]), promptPrefix) {
			return analyzeComposerLine(lines[i], lineDims[i], needle, promptPrefix)
		}
	}

	for _, line := range lines {
		if hasBusyIndicator(string(line)) {
			return probeTurnStarted
		}
	}
	return probeUnknown
}

func (t *Tmux) probeSubmission(target, needle, promptPrefix string) submitProbe {
	content, err := t.run("capture-pane", "-p", "-e", "-t", target, "-S", "-25")
	if err != nil {
		return probeUnknown
	}
	return analyzeSubmission(content, needle, promptPrefix)
}

func (t *Tmux) pollSubmission(target, needle, promptPrefix string, attempts int) submitProbe {
	last := probeUnknown
	stranded := false
	for i := 0; i < attempts; i++ {
		if i > 0 {
			time.Sleep(submitProbeInterval)
		}
		probe := t.probeSubmission(target, needle, promptPrefix)
		switch probe {
		case probeTurnStarted, probeComposerCleared:
			return probe
		case probeComposerDirty:
			return probe
		case probeStranded:
			stranded = true
		}
		last = probe
	}
	if stranded {
		return probeStranded
	}
	return last
}

func (t *Tmux) submitComposer(target, message, promptPrefix string) error {
	enterErr := t.sendEnterVerified(target)
	needle := submitNeedle(message)
	if needle == "" {
		return enterErr
	}

	switch t.pollSubmission(target, needle, promptPrefix, submitProbeAttempts) {
	case probeTurnStarted, probeComposerCleared:
		return nil
	case probeUnknown:
		return enterErr
	case probeComposerDirty:
		return fmt.Errorf("%w (composer contains other text after Enter)", ErrSubmitNotVerified)
	case probeStranded:
		return t.recoverStrandedComposer(target, message, needle, promptPrefix)
	default:
		return enterErr
	}
}

func (t *Tmux) recoverStrandedComposer(target, message, needle, promptPrefix string) error {
	if _, err := t.run("send-keys", "-t", target, "C-j"); err != nil {
		return fmt.Errorf("%w (C-j reset failed: %v)", ErrSubmitNotVerified, err)
	}
	time.Sleep(500 * time.Millisecond)

	switch probe := t.probeSubmission(target, needle, promptPrefix); probe {
	case probeTurnStarted:
		return nil
	case probeComposerCleared:
		if err := t.sendMessageToTarget(target, message); err != nil {
			return fmt.Errorf("%w (retype failed: %v)", ErrSubmitNotVerified, err)
		}
		time.Sleep(adaptiveTextDelay(len(message)))
		_ = t.sendEnterVerified(target)
	case probeStranded, probeComposerDirty, probeUnknown:
		return fmt.Errorf("%w (composer state after C-j: %s)", ErrSubmitNotVerified, probe)
	}

	switch probe := t.pollSubmission(target, needle, promptPrefix, submitProbeAttempts); probe {
	case probeTurnStarted, probeComposerCleared:
		return nil
	default:
		return fmt.Errorf("nudge submit to %q: %w (final state: %s)", target, ErrSubmitNotVerified, probe)
	}
}
