package session

import (
	"fmt"
	"strings"
	"time"

	"github.com/jonbaldie/gastown/internal/tmux"
)

// SessionCreatedAt returns the time a tmux session was created.
func SessionCreatedAt(sessionName string) (time.Time, error) {
	t := tmux.NewTmux()
	info, err := t.GetSessionInfo(sessionName)
	if err != nil {
		return time.Time{}, err
	}

	return ParseTmuxSessionCreated(info.Created)
}

// ParseTmuxSessionCreated parses the tmux session created timestamp.
func ParseTmuxSessionCreated(created string) (time.Time, error) {
	created = strings.TrimSpace(created)
	if created == "" {
		return time.Time{}, fmt.Errorf("empty session created time")
	}
	return time.ParseInLocation("2006-01-02 15:04:05", created, time.Local)
}

// StaleReasonForTimes compares message time to session creation and returns staleness info.
func StaleReasonForTimes(messageTime, sessionCreated time.Time) (bool, string) {
	if messageTime.IsZero() || sessionCreated.IsZero() {
		return false, ""
	}

	if messageTime.Before(sessionCreated) {
		reason := fmt.Sprintf("message=%s session_started=%s",
			messageTime.Format(time.RFC3339),
			sessionCreated.Format(time.RFC3339),
		)
		return true, reason
	}

	return false, ""
}
