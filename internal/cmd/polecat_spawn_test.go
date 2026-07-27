package cmd

import (
	"testing"

	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/witness"
)

func TestEffectivePolecatDirCap(t *testing.T) {
	tests := []struct {
		name       string
		configured int
		want       int
	}{
		{"negative uses floor", -1, minPolecatDirsPerRig},
		{"zero uses floor", 0, minPolecatDirsPerRig},
		{"default below floor uses floor", 10, minPolecatDirsPerRig},
		{"one below floor uses floor", minPolecatDirsPerRig - 1, minPolecatDirsPerRig},
		{"floor remains floor", minPolecatDirsPerRig, minPolecatDirsPerRig},
		{"above floor is honored", 45, 45},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectivePolecatDirCap(tt.configured); got != tt.want {
				t.Errorf("effectivePolecatDirCap(%d) = %d, want %d", tt.configured, got, tt.want)
			}
		})
	}
}

// The respawn circuit breaker must only charge attempts that actually produced
// a running polecat. Charging before allocation meant a spawn that died during
// reclaim, idle-polecat lookup, worktree setup or tmux startup silently burned
// an attempt, and three such failures permanently wedged the bead even though
// the task itself never misbehaved.
func TestSpawnPolecatForSlingDoesNotChargeRespawnWhenSpawnFails(t *testing.T) {
	townRoot := setupPolecatCapacityRig(t, 1)

	oldAcquire := acquirePolecatAdmissionFn
	oldChecks := preSpawnDoltChecksFn
	t.Cleanup(func() {
		acquirePolecatAdmissionFn = oldAcquire
		preSpawnDoltChecksFn = oldChecks
	})
	preSpawnDoltChecksFn = func(*polecat.Manager) error { return nil }
	acquirePolecatAdmissionFn = func(townRootArg, rigName, beadID, operation string) (*polecatAdmissionHandle, polecatCapacitySnapshot, error) {
		return &polecatAdmissionHandle{disabled: true}, polecatCapacitySnapshot{Max: 1, Free: 1}, nil
	}

	const bead = "test-bead-spawn-fails"

	// The rig has no real git repo or tmux server, so allocation cannot succeed.
	info, err := SpawnPolecatForSling("gastown", SlingSpawnOptions{
		TownRoot: townRoot,
		HookBead: bead,
		Create:   true,
	})
	if err == nil && info != nil {
		t.Fatalf("precondition: spawn unexpectedly succeeded (%+v)", info)
	}

	// RecordBeadRespawn returns the post-increment count, so the first charge
	// after a fully refunded failure must be 1.
	if got := witness.RecordBeadRespawn(townRoot, bead); got != 1 {
		t.Errorf("failed spawn consumed a respawn attempt: next count = %d, want 1", got)
	}
}
