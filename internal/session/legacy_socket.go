package session

import (
	"sort"
	"strings"

	"github.com/jonbaldie/gastown/internal/tmux"
)

// legacySocketTmux is the subset of tmux.Tmux used by legacy socket cleanup.
// It is extracted to allow tests to avoid real tmux calls.
type legacySocketTmux interface {
	ListSessions() ([]string, error)
	KillSessionWithProcesses(name string) error
}

// Test hooks; nil in production.
var (
	legacyTmuxForTest   func(socket string) legacySocketTmux
	legacySocketForTest func() string
)

func getDefaultSocket() string {
	if legacySocketForTest != nil {
		return legacySocketForTest()
	}
	return tmux.GetDefaultSocket()
}

func newLegacyTmux(socket string) legacySocketTmux {
	if legacyTmuxForTest != nil {
		return legacyTmuxForTest(socket)
	}
	return tmux.NewTmuxWithSocket(socket)
}

// CleanupLegacyDefaultSocket removes Gas Town sessions left on the "default"
// tmux socket by old binaries. Returns the number of sessions cleaned.
func CleanupLegacyDefaultSocket() int {
	currentSocket := getDefaultSocket()
	if currentSocket == "" || currentSocket == "default" {
		return 0 // Already on the default socket, nothing to clean up.
	}

	legacyTmux := newLegacyTmux("default")
	return cleanupLegacySessions(legacyTmux)
}

// CountLegacyDefaultSocketSessions counts Gas Town sessions on the "default"
// tmux socket for dry-run output.
func CountLegacyDefaultSocketSessions() int {
	currentSocket := getDefaultSocket()
	if currentSocket == "" || currentSocket == "default" {
		return 0
	}

	legacyTmux := newLegacyTmux("default")
	sessions, err := legacyTmux.ListSessions()
	if err != nil {
		return 0
	}

	var count int
	for _, sess := range sessions {
		if isLegacyCleanupSession(sess) {
			count++
		}
	}
	return count
}

// CleanupLegacyBaseSocket removes Gas Town sessions left on the old
// basename-only tmux socket by binaries from before path-hashed socket names
// were introduced. Returns the number of sessions cleaned.
func CleanupLegacyBaseSocket(townRoot string) int {
	currentSocket := getDefaultSocket()
	legacySocket := LegacySocketName(townRoot)
	if currentSocket == legacySocket {
		return 0 // Same socket, no migration needed.
	}

	legacyTmux := newLegacyTmux(legacySocket)
	return cleanupLegacySessions(legacyTmux)
}

// CountLegacyBaseSocketSessions counts Gas Town sessions on the old
// basename-only tmux socket for dry-run output.
func CountLegacyBaseSocketSessions(townRoot string) int {
	currentSocket := getDefaultSocket()
	legacySocket := LegacySocketName(townRoot)
	if currentSocket == legacySocket {
		return 0
	}

	legacyTmux := newLegacyTmux(legacySocket)
	sessions, err := legacyTmux.ListSessions()
	if err != nil {
		return 0
	}

	var count int
	for _, sess := range sessions {
		if isLegacyCleanupSession(sess) {
			count++
		}
	}
	return count
}

func cleanupLegacySessions(legacyTmux legacySocketTmux) int {
	var cleaned int
	for range 3 {
		sessions, err := legacyTmux.ListSessions()
		if err != nil {
			return cleaned // No server on legacy socket.
		}
		targets := legacyCleanupTargets(sessions)
		if len(targets) == 0 {
			return cleaned
		}
		for _, sess := range targets {
			if err := legacyTmux.KillSessionWithProcesses(sess); err == nil {
				cleaned++
			}
		}
	}
	return cleaned
}

func legacyCleanupTargets(sessions []string) []string {
	targets := make([]string, 0, len(sessions))
	for _, sess := range sessions {
		if isLegacyCleanupSession(sess) {
			targets = append(targets, sess)
		}
	}
	sort.SliceStable(targets, func(i, j int) bool {
		return legacyCleanupPriority(targets[i]) < legacyCleanupPriority(targets[j])
	})
	return targets
}

func legacyCleanupPriority(sess string) int {
	switch sess {
	case DeaconSessionName(), BootSessionName():
		return 0
	case MayorSessionName():
		return 1
	}
	if strings.HasPrefix(sess, HQPrefix+"dog-") {
		return 2
	}
	if strings.HasSuffix(sess, "-witness") || strings.HasSuffix(sess, "-refinery") {
		return 3
	}
	if strings.Contains(sess, "-crew-") {
		return 4
	}
	return 5
}

func isLegacyCleanupSession(sess string) bool {
	switch sess {
	case MayorSessionName(), DeaconSessionName(), BootSessionName():
		return true
	}
	if strings.HasPrefix(sess, HQPrefix+"dog-") && strings.TrimPrefix(sess, HQPrefix+"dog-") != "" {
		return true
	}
	if DefaultRegistry().HasPrefix(sess) {
		return true
	}
	for _, p := range LegacyPrefixes {
		if p == strings.TrimSuffix(HQPrefix, "-") {
			continue
		}
		if strings.HasPrefix(sess, p+"-") {
			return true
		}
	}
	return false
}
