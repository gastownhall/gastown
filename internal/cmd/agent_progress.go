package cmd

import (
	"path/filepath"
	"time"

	"github.com/steveyegge/gastown/internal/agenthealth"
	"github.com/steveyegge/gastown/internal/beads"
)

// assessWitnessProgress judges a rig's witness on work progress rather than on
// session existence alone (gt-o57).
//
// sessionAlive/sessionErr come from the caller's tmux check so the two surfaces
// that report witness health agree on liveness. Any failure to read the hook
// yields StateUnknown, never a green verdict.
func assessWitnessProgress(townRoot, rigName string, sessionAlive bool, sessionErr error) agenthealth.Assessment {
	in := agenthealth.Input{
		SessionAlive: sessionAlive,
		SessionErr:   sessionErr,
		Threshold:    agenthealth.HookIdleThreshold(townRoot),
	}

	// Only the alive case needs the hook read; a stopped witness has no hook to
	// advance and the bd round-trip would be wasted.
	if sessionErr == nil && sessionAlive {
		in.HookedWork, in.WorkErr = agenthealth.LookupHookedWork(
			beads.New(rigBeadsDir(townRoot, rigName)),
			rigName+"/witness",
		)
	}

	return agenthealth.Assess(in)
}

// rigBeadsDir resolves the working directory whose beads database holds a rig's
// agent work, falling back to the conventional <townRoot>/<rig> layout.
func rigBeadsDir(townRoot, rigName string) string {
	if dir := beads.GetRigDirForName(townRoot, rigName); dir != "" {
		return dir
	}
	return filepath.Join(townRoot, rigName)
}

// witnessHookLabel renders the degraded witness label for status output, e.g.
// "running, hook idle 21h".
func witnessHookLabel(hookIdleSeconds int) string {
	if hookIdleSeconds <= 0 {
		return "running, hook not advancing"
	}
	return "running, hook idle " + agenthealth.FormatIdle(time.Duration(hookIdleSeconds)*time.Second)
}
