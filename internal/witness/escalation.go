package witness

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/mail"
)

// tryEscalation attempts to advance to the next escalation tier and send
// an ESCALATED_BEAD mail to the deacon for re-dispatch with the appropriate
// override (upgraded agent or crew routing). Returns true if escalation
// was triggered (caller should return), false if all tiers are exhausted.
func tryEscalation(workDir, trRoot, rigName, hookBead, polecatName, status string, policy *config.EscalationPolicy, router *mail.Router) bool {
	currentTier := GetEscalationTier(workDir, hookBead)
	tiers := policy.EffectiveTiers()

	// Walk from current tier forward, skipping inapplicable tiers.
	for tierIdx := currentTier; tierIdx < len(tiers); tierIdx++ {
		action := tiers[tierIdx]

		// Advance to this tier (resets respawn count).
		newTier := AdvanceEscalationTier(workDir, hookBead)
		_ = newTier

		switch action {
		case "upgrade-model":
			agent := resolveUpgradeAgent(trRoot, rigName, policy)
			if agent == "" {
				// Already on the upgraded model — skip this tier.
				fmt.Fprintf(os.Stderr, "witness: escalation tier %d (upgrade-model) skipped for %s — already on target agent\n", tierIdx+1, hookBead)
				continue
			}
			if router != nil {
				sendEscalatedBeadMail(router, rigName, hookBead, polecatName, status, "upgrade-model", agent)
			}
			SetLastAgent(workDir, hookBead, agent)
			fmt.Fprintf(os.Stderr, "witness: escalated %s to tier %d (upgrade-model, agent=%s)\n", hookBead, tierIdx+1, agent)
			return true

		case "route-crew":
			if !rigHasCrew(trRoot, rigName) {
				// No crew members — skip this tier.
				fmt.Fprintf(os.Stderr, "witness: escalation tier %d (route-crew) skipped for %s — no crew in %s\n", tierIdx+1, hookBead, rigName)
				continue
			}
			if router != nil {
				sendEscalatedBeadMail(router, rigName, hookBead, polecatName, status, "route-crew", "")
			}
			fmt.Fprintf(os.Stderr, "witness: escalated %s to tier %d (route-crew)\n", hookBead, tierIdx+1)
			return true

		default:
			fmt.Fprintf(os.Stderr, "witness: unknown escalation tier action %q for %s, skipping\n", action, hookBead)
			continue
		}
	}

	// All tiers exhausted.
	return false
}

// sendEscalatedBeadMail sends an ESCALATED_BEAD mail to the deacon with
// the escalation type and optional agent override encoded in the body.
func sendEscalatedBeadMail(router *mail.Router, rigName, hookBead, polecatName, status, escalationType, agent string) {
	subject := fmt.Sprintf("ESCALATED_BEAD %s", hookBead)
	body := fmt.Sprintf(`Escalated bead from dead polecat via tiered escalation.

Bead: %s
Polecat: %s/%s
Previous Status: %s
Escalation Type: %s
Agent: %s

The bead respawn count has been reset for the new tier.
Please re-dispatch with the specified escalation parameters.`,
		hookBead, rigName, polecatName, status, escalationType, agent)

	msg := &mail.Message{
		From:     fmt.Sprintf("%s/witness", rigName),
		To:       "deacon/",
		Subject:  subject,
		Priority: mail.PriorityHigh,
		Body:     body,
	}
	if err := router.Send(msg); err != nil {
		fmt.Fprintf(os.Stderr, "witness: failed to send ESCALATED_BEAD mail for %s: %v\n", hookBead, err)
	}
}

// resolveUpgradeAgent determines the agent to upgrade to. Returns the agent
// alias if an upgrade is possible, or "" if the current agent is already at
// the target level (no upgrade available).
func resolveUpgradeAgent(trRoot, rigName string, policy *config.EscalationPolicy) string {
	target := policy.UpgradeAgent
	if target == "" {
		target = "opus" // default upgrade target
	}

	// Check if the rig's current default agent is already the upgrade target.
	// Load rig settings to find the configured agent.
	currentAgent := getRigDefaultAgent(trRoot, rigName)
	if currentAgent == "" {
		// No rig-level agent configured — town default is typically sonnet.
		// Upgrade is available.
		return target
	}

	// If already on the target agent (or a higher tier), no upgrade.
	if strings.EqualFold(currentAgent, target) || strings.Contains(strings.ToLower(currentAgent), strings.ToLower(target)) {
		return ""
	}

	return target
}

// getRigDefaultAgent returns the agent configured for a rig, or "" if using town default.
func getRigDefaultAgent(trRoot, rigName string) string {
	// Check rig-level settings/config.json for agent setting.
	rigSettingsPath := filepath.Join(trRoot, rigName, "settings", "config.json")
	rs, err := config.LoadRigSettings(rigSettingsPath)
	if err != nil || rs == nil {
		return ""
	}
	// Check role-specific agent for polecats first, then rig default.
	if rs.RoleAgents != nil {
		if agent, ok := rs.RoleAgents["polecat"]; ok && agent != "" {
			return agent
		}
	}
	return rs.Agent
}

// rigHasCrew returns true if the rig has at least one crew member directory.
func rigHasCrew(trRoot, rigName string) bool {
	crewDir := filepath.Join(trRoot, rigName, "crew")
	entries, err := os.ReadDir(crewDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			return true
		}
	}
	return false
}
