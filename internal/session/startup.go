// Package session provides polecat session lifecycle management.
package session

import (
	"fmt"
	"github.com/jonbaldie/gastown/internal/cli"
	"time"
)

// BeaconRecipient formats a human-readable, non-path-like recipient for the
// startup beacon. Uses "role name (rig: rigName)" format to prevent LLMs from
// misinterpreting the recipient as a filesystem path and constructing wrong
// cd commands. See github.com/steveyegge/gastown/issues/1716.
func BeaconRecipient(role, name, rig string) string {
	if name != "" && rig != "" {
		return fmt.Sprintf("%s %s (rig: %s)", role, name, rig)
	}
	if name != "" {
		return fmt.Sprintf("%s %s", role, name)
	}
	if rig != "" {
		return fmt.Sprintf("%s (rig: %s)", role, rig)
	}
	return role
}

// BeaconConfig configures a startup beacon message.
// The beacon is injected into the CLI prompt to identify sessions in /resume picker.
type BeaconConfig struct {
	// Recipient is the address of the agent being nudged.
	// Use BeaconRecipient() to format non-path-like addresses.
	// Examples: "polecat rust (rig: gastown)", "deacon", "witness (rig: gastown)"
	Recipient string

	// Sender is the agent initiating the nudge.
	// Examples: "mayor", "deacon", "self" (for handoff)
	Sender string

	// Topic describes why the session was started.
	// Examples: "cold-start", "handoff", "assigned", or a mol-id
	Topic string

	// MolID is an optional molecule ID being worked.
	// If provided, appended to topic as "topic:mol-id"
	MolID string

	// IncludePrimeInstruction adds "Run gt prime" to beacon for non-hook agents.
	// When true, the beacon tells the agent to manually run gt prime since
	// there's no SessionStart hook to do it automatically.
	IncludePrimeInstruction bool

	// ExcludeWorkInstructions omits work instructions from the beacon.
	// When true, work instructions will be sent as a separate nudge later.
	// Used for non-hook agents where gt prime must complete first.
	// Default (false) preserves backward compatible behavior.
	ExcludeWorkInstructions bool
}

// FormatStartupBeacon builds the formatted startup beacon message.
// The beacon is injected into the CLI prompt, making sessions identifiable
// in Claude Code's /resume picker for predecessor discovery.
//
// Format: [GAS TOWN] <recipient> <- <sender> • <timestamp> • <topic[:mol-id]>
//
// Examples:
//   - [GAS TOWN] gastown/crew/gus <- deacon • 2025-12-30T15:42 • assigned:gt-abc12
//   - [GAS TOWN] deacon <- daemon • 2025-12-30T08:00 • patrol
//   - [GAS TOWN] gastown/witness <- deacon • 2025-12-30T14:00 • patrol
func FormatStartupBeacon(cfg BeaconConfig) string {
	// Use local time in compact format
	timestamp := time.Now().Format("2006-01-02T15:04")

	// Build topic string - append mol-id if provided
	topic := cfg.Topic
	if cfg.MolID != "" && cfg.Topic != "" {
		topic = fmt.Sprintf("%s:%s", cfg.Topic, cfg.MolID)
	} else if cfg.MolID != "" {
		topic = cfg.MolID
	} else if topic == "" {
		topic = "ready"
	}

	// Build the beacon: [GAS TOWN] recipient <- sender • timestamp • topic
	beacon := fmt.Sprintf("[GAS TOWN] %s <- %s • %s • %s",
		cfg.Recipient, cfg.Sender, timestamp, topic)

	// For non-hook agents, add "Run gt prime" instruction since there's no
	// SessionStart hook to do it automatically. Work instructions will
	// come as a separate nudge after gt prime completes.
	if cfg.IncludePrimeInstruction {
		beacon += "\n\nRun `" + cli.Name() + " prime` to initialize your context."
		// Don't add work instructions here - they come as a delayed nudge after gt prime
		return beacon
	}

	// For handoff, cold-start, and attach, add explicit instructions so the agent knows
	// what to do even if hooks haven't loaded CLAUDE.md yet
	if cfg.Topic == "handoff" || cfg.Topic == "cold-start" || cfg.Topic == "attach" {
		beacon += "\n\nCheck your hook and mail, then act on the hook if present:\n" +
			"1. `" + cli.Name() + " hook` - shows hooked work (if any)\n" +
			"2. `" + cli.Name() + " mail inbox` - check for messages\n" +
			"3. If work is hooked → execute it immediately\n" +
			"4. If nothing hooked → wait for instructions"
	}

	// For assigned, tell agent to prime then work on the hook.
	// Prime must come first so the agent gets full role context (formula, commands, etc).
	// Matches refinery pattern: short instruction with prime before action.
	// Exclude work instructions only if explicitly set (non-hook agents get them via delayed nudge)
	if cfg.Topic == "assigned" && !cfg.ExcludeWorkInstructions {
		beacon += "\n\nRun `" + cli.Name() + " prime --hook` and begin work on your hook."
	}

	return beacon
}

// BuildStartupPrompt creates the CLI prompt for agent startup.
//
// GUPP (Gas Town Universal Propulsion Principle) implementation:
//   - Beacon identifies session for /resume predecessor discovery
//   - Instructions tell agent to start working immediately
//   - SessionStart hook runs `gt prime` which injects full context including
//     "AUTONOMOUS WORK MODE" instructions when work is hooked
//
// This replaces the old two-step StartupNudge + PropulsionNudge pattern.
// The beacon is processed in Claude's first turn along with gt prime context,
// so no separate propulsion nudge is needed.
func BuildStartupPrompt(cfg BeaconConfig, instructions string) string {
	return FormatStartupBeacon(cfg) + "\n\n" + instructions
}
