package cmd

// AA-851 hard CI gate for the reap funnel: a polecat cannot be nuked while
// its branch's PR has any CI check red or pending. This single hook at the
// top of nukePolecatFullWithOptions covers every deliberate destroy path —
// `gt polecat nuke` (including --force), `gt polecat stale --cleanup`, and
// the witness's auto-nuke (which shells out to `gt polecat nuke`).
//
// Unlike the gt done gate, the nuke gate never waits: it refuses immediately
// and lets the caller retry later. Repeated refusals past
// ci_gate.mayor_alert_after nudge the mayor. Explicit overrides:
// `gt polecat nuke --ignore-ci` or GT_CI_GATE=off.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/cigate"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/rig"
)

// ciGateBlockedLabelPrefix marks an agent bead whose nuke was refused by the
// CI gate: ci-gate-blocked:<unix-ts-of-first-refusal>. Cleared when the gate
// passes; used to alert the mayor when a nuke stays blocked too long.
const ciGateBlockedLabelPrefix = "ci-gate-blocked:"

// checkNukeCIGate refuses the nuke (returns an error) when the polecat's
// branch has an open PR with red or pending checks. Pass-through: no PR,
// merged PR, closed-unmerged PR, CI-status errors (fail-open with warning),
// no resolvable branch, gate disabled.
func checkNukeCIGate(polecatName, rigName string, mgr *polecat.Manager, r *rig.Rig) error {
	cfg := loadCIGateConfig(r.Path)
	if !cfg.IsEnabled() || cigate.EnvDisabled() {
		return nil
	}

	info, err := mgr.Get(polecatName)
	if err != nil || info == nil || info.Branch == "" {
		return nil // no branch to check — nothing to gate
	}

	res := cigate.New().CheckBranch(ciGateDirForPolecat(info.ClonePath, r.Path), info.Branch)
	agentBd := beads.New(r.Path).ForAgentBead()
	agentBeadID := polecatBeadIDForRig(r, rigName, polecatName)

	if res.Verdict == cigate.VerdictError {
		fmt.Fprintf(os.Stderr, "Warning: CI gate could not determine CI status for %s/%s branch %s (%v) — failing open (AA-851)\n",
			rigName, polecatName, info.Branch, res.Err)
		return nil
	}
	if !res.Verdict.Blocks() {
		clearCIGateBlockedLabel(agentBd, agentBeadID)
		return nil
	}

	// Blocked. Track the first refusal so long-standing blocks alert the mayor.
	firstBlocked := markCIGateBlocked(agentBd, agentBeadID)
	if !firstBlocked.IsZero() && time.Since(firstBlocked) > cfg.MayorAlertAfterOrDefault() {
		nudgeMayorBestEffort(fmt.Sprintf("CI_GATE_BLOCKED: nuke of %s/%s blocked >%s — %s (branch %s). Investigate the PR or override with --ignore-ci.",
			rigName, polecatName, cfg.MayorAlertAfterOrDefault(), res.Summary(), info.Branch))
	}
	return fmt.Errorf("CI gate (AA-851): refusing to nuke %s/%s — %s.\n"+
		"The polecat's work is not landed; nuking would strand a red/pending PR.\n"+
		"Wait for CI (or fix the PR), or override with --ignore-ci / GT_CI_GATE=off",
		rigName, polecatName, res.Summary())
}

// ciGateDirForPolecat picks the directory for gh invocations: gh resolves
// the repo from the git remotes of its working directory, so prefer the
// polecat worktree and fall back to the rig's mayor clone when it's gone.
func ciGateDirForPolecat(clonePath, rigPath string) string {
	if clonePath != "" {
		if _, err := os.Stat(clonePath); err == nil {
			return clonePath
		}
	}
	return filepath.Join(rigPath, "mayor", "rig")
}

// markCIGateBlocked records the first CI-gate refusal timestamp on the agent
// bead and returns it (zero time when the bead is unavailable). Subsequent
// refusals reuse the existing timestamp so the mayor-alert threshold measures
// total blocked time, not time since the latest attempt.
func markCIGateBlocked(bd *beads.Beads, agentBeadID string) time.Time {
	if agentBeadID == "" {
		return time.Time{}
	}
	issue, err := bd.Show(agentBeadID)
	if err != nil || issue == nil {
		return time.Time{}
	}
	for _, label := range issue.Labels {
		if ts, ok := parseCIGateBlockedLabel(label); ok {
			return ts
		}
	}
	now := time.Now()
	label := fmt.Sprintf("%s%d", ciGateBlockedLabelPrefix, now.Unix())
	if err := bd.Update(agentBeadID, beads.UpdateOptions{AddLabels: []string{label}}); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: couldn't set ci-gate-blocked label on %s: %v\n", agentBeadID, err)
	}
	return now
}

// clearCIGateBlockedLabel removes any ci-gate-blocked:* label (gate passed).
func clearCIGateBlockedLabel(bd *beads.Beads, agentBeadID string) {
	if agentBeadID == "" {
		return
	}
	issue, err := bd.Show(agentBeadID)
	if err != nil || issue == nil {
		return
	}
	var toRemove []string
	for _, label := range issue.Labels {
		if strings.HasPrefix(label, ciGateBlockedLabelPrefix) {
			toRemove = append(toRemove, label)
		}
	}
	if len(toRemove) == 0 {
		return
	}
	if err := bd.Update(agentBeadID, beads.UpdateOptions{RemoveLabels: toRemove}); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: couldn't clear ci-gate-blocked label on %s: %v\n", agentBeadID, err)
	}
}

// parseCIGateBlockedLabel extracts the first-refusal time from a
// ci-gate-blocked:<unix> label.
func parseCIGateBlockedLabel(label string) (time.Time, bool) {
	if !strings.HasPrefix(label, ciGateBlockedLabelPrefix) {
		return time.Time{}, false
	}
	ts, err := strconv.ParseInt(strings.TrimPrefix(label, ciGateBlockedLabelPrefix), 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(ts, 0), true
}
