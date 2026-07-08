package cmd

// AA-851 hard CI gate for gt done: a polecat cannot transition to COMPLETED
// while its branch's PR has any CI check red or pending. The gate runs after
// the branch push is verified and BEFORE any issue close or MR-bead creation,
// so an abort leaves the polecat assigned with its hook bead open — it keeps
// iterating until the PR is green.
//
// Pass-through verdicts: no PR (merge-strategy=direct rigs), PR already
// merged, PR closed-unmerged (deliberate human action, warn only).
// Fail-open verdict: ERROR (CI state unknown) — completion proceeds, but the
// event escalates to a human via ci_gate.escalation_cmd so a silently
// broken gate is loudly visible.

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/cigate"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/style"
)

// needsCIGreenLabelPrefix marks an agent bead whose last gt done was blocked
// by the CI gate: needs-ci-green:<pr>:<unix-ts>. Cleared on the next gt done
// that passes the gate.
const needsCIGreenLabelPrefix = "needs-ci-green:"

// loadCIGateConfig reads the rig's merge_queue.ci_gate settings.
// Missing settings file or block yields the enabled defaults.
func loadCIGateConfig(rigPath string) *config.CIGateConfig {
	settingsPath := filepath.Join(rigPath, "settings", "config.json")
	if settings, err := config.LoadRigSettings(settingsPath); err == nil && settings.MergeQueue != nil {
		return settings.MergeQueue.CIGateSettings()
	}
	return &config.CIGateConfig{}
}

// doneCIGateDeps carries the gate inputs plus injectable side effects so the
// decision logic is unit-testable without beads/tmux/gh.
type doneCIGateDeps struct {
	gate   *cigate.Gate
	cfg    *config.CIGateConfig
	dir    string
	branch string
	agent  string
	out    io.Writer

	setNeedsCIGreen   func(prNumber int)
	clearNeedsCIGreen func()
	clearDoneIntent   func()
	escalate          func(esc cigate.Escalation)
	nudgeMayor        func(msg string)
	// wait overrides gate.WaitForGreen in tests; nil = real wait.
	wait func(timeout, poll time.Duration) (cigate.CheckResult, bool)
}

// enforce runs the gate. A nil return means gt done may proceed; a non-nil
// error aborts gt done with the polecat still assigned.
func (d *doneCIGateDeps) enforce() error {
	res := d.gate.CheckBranch(d.dir, d.branch)

	if res.Verdict == cigate.VerdictPending {
		timeout := d.cfg.PendingTimeoutOrDefault()
		poll := d.cfg.PollIntervalOrDefault()
		fmt.Fprintf(d.out, "%s CI gate: %s — waiting up to %s for checks to settle\n",
			style.Bold.Render("→"), res.Summary(), timeout)
		var timedOut bool
		if d.wait != nil {
			res, timedOut = d.wait(timeout, poll)
		} else {
			res, timedOut = d.gate.WaitForGreen(d.dir, d.branch, cigate.WaitOptions{
				Timeout:      timeout,
				PollInterval: poll,
				Progress:     d.out,
			})
		}
		if timedOut {
			detail := fmt.Sprintf("CI gate (AA-851): gt done aborted — %s after waiting %s. "+
				"Agent %s on branch %s stays assigned and must re-run gt done once CI completes.",
				res.Summary(), timeout, d.agent, d.branch)
			d.escalate(cigate.Escalation{
				Event:  cigate.EventPendingTimeout,
				Detail: detail,
				PRURL:  res.PRURL,
				Branch: d.branch,
				Agent:  d.agent,
			})
			d.nudgeMayor(fmt.Sprintf("NEEDS_CI_GREEN: %s stuck pending >%s for %s (branch %s)",
				res.Summary(), timeout, d.agent, d.branch))
			d.setNeedsCIGreen(res.PRNumber)
			d.clearDoneIntent()
			return fmt.Errorf("NEEDS_CI_GREEN: %s — still pending after %s.\n"+
				"Poll `gh pr checks %d` and re-run `gt done` when CI completes.\n"+
				"(Escalated for human attention; override: GT_CI_GATE=off)",
				res.Summary(), timeout, res.PRNumber)
		}
	}

	switch res.Verdict {
	case cigate.VerdictNoPR:
		// No PR for this branch — the gate only applies when a PR exists.
		d.clearNeedsCIGreen()
		return nil
	case cigate.VerdictMerged:
		fmt.Fprintf(d.out, "%s CI gate: %s\n", style.Bold.Render("✓"), res.Summary())
		d.clearNeedsCIGreen()
		return nil
	case cigate.VerdictClosedUnmerged:
		fmt.Fprintf(d.out, "%s CI gate: %s — passing through (deliberate close?)\n",
			style.Warning.Render("⚠"), res.Summary())
		d.clearNeedsCIGreen()
		return nil
	case cigate.VerdictGreen:
		fmt.Fprintf(d.out, "%s CI gate: %s\n", style.Bold.Render("✓"), res.Summary())
		d.clearNeedsCIGreen()
		return nil
	case cigate.VerdictError:
		// Fail-open: a GitHub outage must not brick every completion, but a
		// human must verify — escalate via the rig's tracker integration.
		fmt.Fprintf(d.out, "%s CI gate: could not determine CI status (%v) — FAILING OPEN (AA-851)\n",
			style.Warning.Render("⚠"), res.Err)
		d.escalate(cigate.Escalation{
			Event: cigate.EventCIStatusError,
			Detail: fmt.Sprintf("CI gate (AA-851): could not determine CI status for branch %s (%v). "+
				"gt done by %s proceeded FAIL-OPEN — a human must verify the PR's checks were green.",
				d.branch, res.Err, d.agent),
			Branch: d.branch,
			Agent:  d.agent,
		})
		return nil
	case cigate.VerdictRed:
		d.setNeedsCIGreen(res.PRNumber)
		d.clearDoneIntent()
		return fmt.Errorf("NEEDS_CI_GREEN: %s.\n"+
			"Fix the failures, push, and re-run `gt done`. You stay assigned to this work.\n"+
			"(Inspect: `gh pr checks %d`; override: GT_CI_GATE=off)",
			res.Summary(), res.PRNumber)
	}
	// Unknown verdict — treat like fail-open but warn.
	fmt.Fprintf(d.out, "%s CI gate: unexpected verdict %s — failing open\n",
		style.Warning.Render("⚠"), res.Verdict)
	return nil
}

// runDoneCIGate wires the gate with real side effects and enforces it.
// Called from the COMPLETED path of runDone after push verification.
func runDoneCIGate(bd *beads.Beads, townRoot, rigName, cwd, branch, issueID, agentBeadID, sender string) error {
	cfg := loadCIGateConfig(filepath.Join(townRoot, rigName))
	if !cfg.IsEnabled() || cigate.EnvDisabled() {
		return nil
	}

	agentBd := beads.New(cwd).ForAgentBead()
	deps := &doneCIGateDeps{
		gate:   cigate.New(cfg.HumanGateChecksOrDefault()...),
		cfg:    cfg,
		dir:    cwd,
		branch: branch,
		agent:  sender,
		out:    os.Stdout,
		setNeedsCIGreen: func(prNumber int) {
			setNeedsCIGreenLabel(agentBd, agentBeadID, prNumber)
		},
		clearNeedsCIGreen: func() {
			clearNeedsCIGreenLabel(agentBd, agentBeadID)
		},
		clearDoneIntent: func() {
			clearDoneIntentLabel(agentBd, agentBeadID)
		},
		escalate: func(esc cigate.Escalation) {
			esc.Ticket = ticketForIssue(bd, issueID)
			escalateCIGate(cfg, cwd, esc)
		},
		nudgeMayor: nudgeMayorBestEffort,
	}
	return deps.enforce()
}

// ticketForIssue extracts the external tracker key (`Jira: AA-123` line)
// from the worked issue's description. Best-effort: "" when unavailable.
func ticketForIssue(bd *beads.Beads, issueID string) string {
	if bd == nil || issueID == "" {
		return ""
	}
	issue, err := bd.Show(issueID)
	if err != nil || issue == nil {
		return ""
	}
	return cigate.ExtractTicket(issue.Description)
}

// escalateCIGate runs the rig's ci_gate.escalation_cmd (tracker integration:
// comment on the ticket + move it to a human-attention status). Best-effort —
// escalation failures are logged, never fatal.
func escalateCIGate(cfg *config.CIGateConfig, dir string, esc cigate.Escalation) {
	cmdStr := cfg.EscalationCmdOrEmpty()
	if cmdStr == "" {
		fmt.Fprintf(os.Stderr, "Warning: CI gate %s event but no ci_gate.escalation_cmd configured (ticket=%s): %s\n",
			esc.Event, esc.Ticket, esc.Detail)
		return
	}
	if err := cigate.RunEscalationCmd(cmdStr, dir, esc); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: CI gate escalation failed: %v\n", err)
		return
	}
	fmt.Printf("%s CI gate: escalated %s to human attention (ticket %s)\n",
		style.Bold.Render("→"), esc.Event, esc.Ticket)
}

// nudgeMayorBestEffort sends a gt nudge to the mayor, mirroring the
// refinery's BRANCH_MISSING escalation precedent. Best-effort.
func nudgeMayorBestEffort(msg string) {
	cmd := exec.Command("gt", "nudge", "mayor/", msg)
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to nudge mayor: %v\n", err)
	}
}

// setNeedsCIGreenLabel marks the agent bead as CI-gate-blocked:
// needs-ci-green:<pr>:<unix-ts>. Replaces any prior needs-ci-green label.
func setNeedsCIGreenLabel(bd *beads.Beads, agentBeadID string, prNumber int) {
	if agentBeadID == "" {
		return
	}
	label := fmt.Sprintf("%s%d:%d", needsCIGreenLabelPrefix, prNumber, time.Now().Unix())
	toRemove := needsCIGreenLabels(bd, agentBeadID)
	if err := bd.Update(agentBeadID, beads.UpdateOptions{
		AddLabels:    []string{label},
		RemoveLabels: toRemove,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: couldn't set needs-ci-green label on %s: %v\n", agentBeadID, err)
	}
}

// clearNeedsCIGreenLabel removes any needs-ci-green:* label from the agent
// bead (called when a later gt done passes the gate).
func clearNeedsCIGreenLabel(bd *beads.Beads, agentBeadID string) {
	if agentBeadID == "" {
		return
	}
	toRemove := needsCIGreenLabels(bd, agentBeadID)
	if len(toRemove) == 0 {
		return
	}
	if err := bd.Update(agentBeadID, beads.UpdateOptions{RemoveLabels: toRemove}); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: couldn't clear needs-ci-green label on %s: %v\n", agentBeadID, err)
	}
}

// needsCIGreenLabels lists the agent bead's needs-ci-green:* labels.
func needsCIGreenLabels(bd *beads.Beads, agentBeadID string) []string {
	issue, err := bd.Show(agentBeadID)
	if err != nil || issue == nil {
		return nil
	}
	var labels []string
	for _, label := range issue.Labels {
		if strings.HasPrefix(label, needsCIGreenLabelPrefix) {
			labels = append(labels, label)
		}
	}
	return labels
}
