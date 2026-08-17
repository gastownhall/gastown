package session

import "github.com/jonbaldie/gastown/internal/constants"

// rolePolicy is the start behavior for one Worker role.
// Callers do not set these flags; StartSession reads them from rolePolicies.
type rolePolicy struct {
	WaitForAgent   bool
	WaitFatal      bool
	AcceptBypass   bool
	ReadyDelay     bool
	ReadyFatal     bool
	AutoRespawn    bool
	RemainOnExit   bool
	TrackPID       bool
	VerifySurvived bool
}

// rolePolicies is the single start-policy table. A new role is added here.
var rolePolicies = map[string]rolePolicy{
	constants.RoleBoot: {
		// Boot is ephemeral, but Codex still shows a workspace-trust dialog
		// that blocks triage until someone accepts it.
		AcceptBypass: true,
	},
	constants.RoleMayor: {
		WaitForAgent: true,
		WaitFatal:    true,
		AcceptBypass: true,
		AutoRespawn:  true,
	},
	constants.RoleDeacon: {
		WaitForAgent: true,
		WaitFatal:    true,
		AcceptBypass: true,
		AutoRespawn:  true,
		RemainOnExit: true,
		TrackPID:     true,
	},
	constants.RoleWitness: {
		WaitForAgent: true,
		WaitFatal:    true,
		AcceptBypass: true,
		TrackPID:     true,
	},
	constants.RoleRefinery: {
		// Refinery does not wait for the agent pane command.
		// Its ready wait is fatal so a half-started merge runner cannot proceed.
		AcceptBypass: true,
		TrackPID:     true,
		ReadyFatal:   true,
	},
	constants.RolePolecat: {
		WaitForAgent:   true,
		AcceptBypass:   true,
		ReadyDelay:     true,
		TrackPID:       true,
		VerifySurvived: true,
	},
	constants.RoleCrew: {
		WaitForAgent: true,
		AcceptBypass: true,
		TrackPID:     true,
	},
	"dog": {
		WaitForAgent:   true,
		WaitFatal:      true,
		AcceptBypass:   true,
		ReadyDelay:     true,
		TrackPID:       true,
		VerifySurvived: true,
	},
}

func policyFor(role string, work Work) rolePolicy {
	p, ok := rolePolicies[role]
	if !ok {
		p = rolePolicy{
			WaitForAgent: true,
			AcceptBypass: true,
			TrackPID:     true,
		}
	}
	if role == constants.RoleCrew && work.Interactive {
		p.WaitForAgent = false
		p.AcceptBypass = false
	}
	return p
}
