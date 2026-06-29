package refinery

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

const safetyStopLabelPrefix = "safety_stop:"

// ErrSafetyStopped is returned when the refinery agent bead carries an active
// safety-stop label. The label is the durable operator-cleared guard.
var ErrSafetyStopped = errors.New("refinery safety-stopped")

// SafetyStop describes the active refinery safety-stop label.
type SafetyStop struct {
	AgentID string
	Label   string
	StopID  string
}

func (s *SafetyStop) Reason() string {
	if s == nil {
		return "refinery safety-stopped"
	}
	if s.StopID != "" {
		return fmt.Sprintf("refinery safety-stopped by %s", s.StopID)
	}
	return fmt.Sprintf("refinery safety-stopped by %s", s.Label)
}

// SafetyStopError wraps ErrSafetyStopped with the label that blocked startup.
type SafetyStopError struct {
	Stop *SafetyStop
}

func (e *SafetyStopError) Error() string {
	if e == nil || e.Stop == nil {
		return ErrSafetyStopped.Error()
	}
	return fmt.Sprintf("%s (label %s on %s)", e.Stop.Reason(), e.Stop.Label, e.Stop.AgentID)
}

func (e *SafetyStopError) Unwrap() error {
	return ErrSafetyStopped
}

// NewSafetyStoppedError returns a typed startup-blocking safety-stop error.
func NewSafetyStoppedError(stop *SafetyStop) error {
	return &SafetyStopError{Stop: stop}
}

// ActiveSafetyStop returns the active safety stop for a rig's refinery, if any.
// The refinery agent bead's safety_stop:* label is the single durable source of
// truth; the referenced ID is provenance, not an implicit clear condition.
func ActiveSafetyStop(townRoot, rigName string) (*SafetyStop, error) {
	townRoot = strings.TrimSpace(townRoot)
	rigName = strings.TrimSpace(rigName)
	if townRoot == "" || rigName == "" {
		return nil, nil
	}

	prefix := beads.GetPrefixForRig(townRoot, rigName)
	agentID := beads.RefineryBeadIDWithPrefix(prefix, rigName)
	rigPath := filepath.Join(townRoot, rigName)
	b := beads.NewWithBeadsDir(rigPath, filepath.Join(townRoot, ".beads")).ForAgentBead()

	issue, _, err := b.GetAgentBead(agentID)
	if err != nil {
		return nil, err
	}
	if issue == nil {
		return nil, nil
	}
	return safetyStopFromIssue(agentID, issue), nil
}

func safetyStopFromIssue(agentID string, issue *beads.Issue) *SafetyStop {
	if issue == nil {
		return nil
	}
	if agentID == "" {
		agentID = issue.ID
	}
	for _, label := range issue.Labels {
		if !strings.HasPrefix(label, safetyStopLabelPrefix) {
			continue
		}
		return &SafetyStop{
			AgentID: agentID,
			Label:   label,
			StopID:  strings.TrimPrefix(label, safetyStopLabelPrefix),
		}
	}
	return nil
}
