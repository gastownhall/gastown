package polecat

import (
	"fmt"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/session"
)

func brokenIdleReclaimDispositionBlocker(d WorkstateDisposition) string {
	if d.Reason != "git-check-failed" {
		return fmt.Sprintf("workstate=%s reason=%s", d.Verdict, d.Reason)
	}
	if len(d.Blockers) != 1 || d.Blockers[0] != "git_state=unknown" {
		return fmt.Sprintf("workstate blockers=%s", strings.Join(d.Blockers, ","))
	}
	return ""
}

func brokenIdleReclaimAgentBlocker(fields *beads.AgentFields) string {
	if fields == nil {
		return "agent_fields=<missing>"
	}
	if state := beads.AgentState(fields.AgentState); state != beads.AgentStateIdle {
		if state == "" {
			return "agent_state=<missing>"
		}
		return "agent_state=" + string(state)
	}
	if status := CleanupStatus(fields.CleanupStatus); status != CleanupClean {
		if status == "" {
			return "cleanup_status=<missing>"
		}
		return "cleanup_status=" + string(status)
	}
	if strings.TrimSpace(fields.HookBead) != "" {
		return "hook_bead=" + fields.HookBead
	}
	if strings.TrimSpace(fields.ActiveMR) != "" {
		return "active_mr=" + fields.ActiveMR
	}
	if fields.PushFailed {
		return "push_failed=true"
	}
	if fields.MRFailed {
		return "mr_failed=true"
	}
	if strings.TrimSpace(fields.Branch) == "" {
		return "branch=<missing>"
	}
	return ""
}

func brokenIdleReclaimMRBlocker(branch string, mr *beads.Issue, err error) string {
	if strings.TrimSpace(branch) == "" {
		return "branch=<missing>"
	}
	if err != nil {
		return fmt.Sprintf("checking MR for branch %s: %v", branch, err)
	}
	if mr != nil {
		return fmt.Sprintf("branch %s has open MR %s status=%s", branch, mr.ID, mr.Status)
	}
	return ""
}

func (m *Manager) brokenIdleReclaimSessionBlocker(name string) string {
	if m.tmux == nil {
		return "session_state=unverified"
	}
	sessionName := session.PolecatSessionName(session.PrefixFor(m.rig.Name), name)
	running, err := m.tmux.HasSession(sessionName)
	if err != nil {
		return fmt.Sprintf("session_state=lookup_error: %v", err)
	}
	if running {
		return "session_state=running"
	}
	return ""
}
