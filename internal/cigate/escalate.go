package cigate

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// EscalationEvent identifies why the gate needs a human.
type EscalationEvent string

const (
	// EventCIStatusError: the gate could not determine CI state and failed
	// open. A human must confirm the completion was actually green.
	EventCIStatusError EscalationEvent = "ci_status_error"
	// EventPendingTimeout: checks were still pending when the configured
	// pending_timeout elapsed and the completion was aborted.
	EventPendingTimeout EscalationEvent = "pending_timeout"
)

// Escalation carries the context handed to the configured escalation command.
type Escalation struct {
	Event  EscalationEvent
	Ticket string // external ticket key (e.g. Jira "AA-851"); may be empty
	Detail string // human-readable description of what happened
	PRURL  string
	Branch string
	Agent  string // e.g. "openclaw/polecats/furiosa"
}

// ticketLineRe matches the `Jira: AA-123 ...` (or `Ticket: AA-123 ...`)
// convention used in bead descriptions to link Gas Town work to an external
// tracker. The key may be followed by a title (`Jira: AA-851 — Implement …`).
var ticketLineRe = regexp.MustCompile(`(?mi)^\s*(?:jira|ticket):\s*([A-Za-z][A-Za-z0-9]*-\d+)\b`)

// ExtractTicket returns the external ticket key referenced by a bead
// description, or "" when the bead carries no ticket line.
func ExtractTicket(description string) string {
	m := ticketLineRe.FindStringSubmatch(description)
	if m == nil {
		return ""
	}
	return strings.ToUpper(m[1])
}

// escalationTimeout bounds the external escalation command so a hung tracker
// integration cannot wedge gt done / the refinery.
const escalationTimeout = 60 * time.Second

// RunEscalationCmd runs the rig-configured ci_gate.escalation_cmd via
// `sh -c` with GT_CIGATE_* environment variables describing the event.
// The command is the rig's bridge to its external tracker — e.g. a script
// that comments on the ticket and transitions it to a human-attention
// status. Errors are returned for logging but must never block completion:
// escalation paths are fail-open by design.
func RunEscalationCmd(cmdStr, dir string, esc Escalation) error {
	if strings.TrimSpace(cmdStr) == "" {
		return fmt.Errorf("no ci_gate.escalation_cmd configured")
	}
	cmd := exec.Command("sh", "-c", cmdStr)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GT_CIGATE_EVENT="+string(esc.Event),
		"GT_CIGATE_TICKET="+esc.Ticket,
		"GT_CIGATE_DETAIL="+esc.Detail,
		"GT_CIGATE_PR_URL="+esc.PRURL,
		"GT_CIGATE_BRANCH="+esc.Branch,
		"GT_CIGATE_AGENT="+esc.Agent,
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("escalation_cmd failed to start: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("escalation_cmd failed: %w (%s)", err, strings.TrimSpace(out.String()))
		}
		return nil
	case <-time.After(escalationTimeout):
		_ = cmd.Process.Kill()
		<-done
		return fmt.Errorf("escalation_cmd timed out after %s", escalationTimeout)
	}
}
