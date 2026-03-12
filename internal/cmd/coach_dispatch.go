package cmd

import (
	"os"

	"github.com/steveyegge/gastown/internal/daemon"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/workspace"
)

// notifyCoachDispatch fires a push-based dispatch trigger to the daemon.
// It is best-effort: all errors are silently ignored.
//
// Two mechanisms ensure the daemon wakes promptly (gt-9uumi0):
//  1. Lifecycle mail to deacon/ — durable intent, processed on next lifecycle check.
//  2. SIGUSR1 to the daemon process — triggers immediate lifecycle processing.
//
// Called from gt close (bead close) and gt done (polecat idle) so the scheduler
// can dispatch queued work without waiting for the next recovery heartbeat.
func notifyCoachDispatch(townRoot string) {
	if townRoot == "" {
		return
	}
	sendDispatchMail(townRoot)
	signalDaemonDispatch(townRoot)
}

// notifyCoachDispatchFromCwd fires a push-based dispatch trigger, auto-detecting
// the town root from the current working directory. Best-effort: errors are ignored.
func notifyCoachDispatchFromCwd() {
	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		return
	}
	notifyCoachDispatch(townRoot)
}

// sendDispatchMail sends a LIFECYCLE: dispatch wisp message to the deacon inbox.
// Wisp messages are ephemeral (not synced to git) to minimize Dolt overhead.
func sendDispatchMail(townRoot string) {
	router := mail.NewRouterWithTownRoot(townRoot, townRoot)
	msg := mail.NewMessage(
		"scheduler", // from: conceptual scheduler identity
		"deacon/",   // to: daemon lifecycle inbox
		"LIFECYCLE: dispatch",
		`{"action":"dispatch"}`,
	)
	msg.Wisp = true          // ephemeral — no git history needed for dispatch triggers
	_ = router.Send(msg)    // best-effort
}

// signalDaemonDispatch sends SIGUSR1 to the daemon process so it processes
// lifecycle requests immediately rather than waiting for the next heartbeat.
func signalDaemonDispatch(townRoot string) {
	running, pid, err := daemon.IsRunning(townRoot)
	if err != nil || !running {
		return
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = signalDaemonLifecycle(process) // best-effort
}
