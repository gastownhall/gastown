package opencodeserver

import "sync"

type sessionLifecycle struct {
	mu                      sync.Mutex
	sessionID               string
	status                  Status
	inFlight                bool
	seenBusy                bool
	idleConfirmationPending bool
	idleConfirmationChecks  int
}

func newSessionLifecycle(sessionID string, status Status) *sessionLifecycle {
	return &sessionLifecycle{sessionID: sessionID, status: status}
}

func (l *sessionLifecycle) Ready() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return !l.inFlight && l.status.Idle()
}

func (l *sessionLifecycle) BeginPrompt() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inFlight || !l.status.Idle() {
		return false
	}
	l.inFlight = true
	l.seenBusy = false
	l.idleConfirmationPending = false
	l.idleConfirmationChecks = 0
	l.status = Status{Type: "busy"}
	return true
}

func (l *sessionLifecycle) PromptFailed() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.inFlight = false
	l.seenBusy = false
	l.idleConfirmationPending = false
	l.idleConfirmationChecks = 0
	l.status = Status{Type: "idle"}
}

// AwaitRecoveredPrompt keeps an idempotently recovered turn in flight until
// status polling confirms it is either running or already complete.
func (l *sessionLifecycle) AwaitRecoveredPrompt() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.inFlight {
		return
	}
	l.idleConfirmationPending = true
	l.idleConfirmationChecks = 0
}

func (l *sessionLifecycle) Reconcile(status Status) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.inFlight {
		l.status = status
		return
	}
	if l.idleConfirmationPending {
		if !status.Idle() {
			l.status = status
			l.seenBusy = true
			l.idleConfirmationPending = false
			l.idleConfirmationChecks = 0
			return
		}
		l.idleConfirmationChecks++
		if l.idleConfirmationChecks >= 2 {
			l.inFlight = false
			l.seenBusy = false
			l.idleConfirmationPending = false
			l.idleConfirmationChecks = 0
			l.status = status
		}
		return
	}
	if !status.Idle() {
		l.status = status
		l.seenBusy = true
	}
}

func (l *sessionLifecycle) Observe(event Event) bool {
	if event.SessionID() != l.sessionID {
		return false
	}
	if event.Type != "session.status" && event.Type != "session.idle" && event.Type != "session.error" {
		return false
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if event.Type == "session.error" {
		if l.inFlight {
			l.idleConfirmationPending = true
			l.idleConfirmationChecks = 0
		}
		return false
	}
	status := event.Status()
	if !status.Idle() {
		l.status = status
		if l.inFlight {
			l.seenBusy = true
			l.idleConfirmationPending = false
			l.idleConfirmationChecks = 0
		}
		return false
	}
	if l.inFlight && !l.seenBusy {
		return false
	}
	l.inFlight = false
	l.seenBusy = false
	l.idleConfirmationPending = false
	l.idleConfirmationChecks = 0
	l.status = status
	return true
}

func (l *sessionLifecycle) InFlight() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inFlight
}
