package refinery

import (
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

type terminalMRCloseOptions struct {
	Reason        string
	MergeCommit   string
	AgentBeadHint string
	MissingOK     bool
}

type terminalMRCloseResult struct {
	MRID                  string
	SourceIssue           string
	AgentBead             string
	Closed                bool
	AlreadyTerminal       bool
	AgentActiveMRCleared  bool
	AgentActiveMRClearErr error
}

func closeTerminalMR(b *beads.Beads, mrID string, opts terminalMRCloseOptions) (*terminalMRCloseResult, error) {
	mrID = strings.TrimSpace(mrID)
	result := &terminalMRCloseResult{MRID: mrID}
	if b == nil || mrID == "" {
		return result, nil
	}

	issue, err := b.Show(mrID)
	if err != nil {
		if errors.Is(err, beads.ErrNotFound) && opts.MissingOK {
			return result, nil
		}
		return result, fmt.Errorf("fetch MR for close: %w", err)
	}
	if issue == nil {
		return result, nil
	}

	fields := beads.ParseMRFields(issue)
	if fields == nil {
		fields = &beads.MRFields{}
	}
	result.SourceIssue = strings.TrimSpace(fields.SourceIssue)
	result.AgentBead = firstNonEmpty(opts.AgentBeadHint, fields.AgentBead)

	status := beads.IssueStatus(strings.TrimSpace(issue.Status))
	switch {
	case status == beads.StatusOpen:
		if opts.MergeCommit != "" {
			fields.MergeCommit = opts.MergeCommit
		}
		if closeReason := normalizedMRCloseReason(opts.Reason); closeReason != "" {
			fields.CloseReason = closeReason
		}
		if result.AgentBead != "" && strings.TrimSpace(fields.AgentBead) == "" {
			fields.AgentBead = result.AgentBead
		}

		newDesc := beads.SetMRFields(issue, fields)
		if err := b.Update(mrID, beads.UpdateOptions{Description: &newDesc}); err != nil {
			return result, fmt.Errorf("record MR close metadata: %w", err)
		}
		if err := b.CloseWithReason(opts.Reason, mrID); err != nil {
			return result, fmt.Errorf("close MR: %w", err)
		}
		result.Closed = true
	case status.IsTerminal():
		result.AlreadyTerminal = true
	default:
		return result, nil
	}

	if result.AgentBead != "" {
		cleared, clearErr := b.ForAgentBeadID(result.AgentBead).ClearAgentActiveMRIfMatches(result.AgentBead, mrID)
		result.AgentActiveMRCleared = cleared
		result.AgentActiveMRClearErr = clearErr
	}
	return result, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
