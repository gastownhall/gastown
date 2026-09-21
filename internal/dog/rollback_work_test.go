package dog

import (
	"testing"
	"time"
)

// TestRollbackWorkPreservesLastActive proves the detector-blinding half of
// hq-bcowvq is fixed: a dispatch that fails must leave the dog's LastActive
// exactly where it was, so the age-based stuck detectors keep accruing.
//
// Under the old assign/ClearWork pair, each of these cycles stamped LastActive
// with "now", so a dog failing to start every 18s never looked stale to
// detectStaleWorkingDogs or old to reapIdleDogs.
func TestRollbackWorkPreservesLastActive(t *testing.T) {
	m, _ := testManager(t)

	origin := time.Now().Add(-3 * time.Hour).Round(0)
	setupDogWithState(t, m, "alpha", &DogState{
		Name:       "alpha",
		State:      StateIdle,
		LastActive: origin,
		CreatedAt:  origin,
		UpdatedAt:  origin,
	})

	// Ten failed dispatch cycles, the shape the daemon was stuck in.
	for i := 0; i < 10; i++ {
		assigned, err := m.AssignWorkIfIdle("alpha", "plugin:dolt-backup")
		if err != nil {
			t.Fatalf("cycle %d: AssignWorkIfIdle: %v", i, err)
		}
		restored, err := m.RollbackWork("alpha", "plugin:dolt-backup", assigned.WorkStartedAt, origin)
		if err != nil {
			t.Fatalf("cycle %d: RollbackWork: %v", i, err)
		}
		if !restored {
			t.Fatalf("cycle %d: RollbackWork reported no change", i)
		}
	}

	d, err := m.Get("alpha")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !d.LastActive.Equal(origin) {
		t.Errorf("LastActive = %v after 10 failed dispatches, want it held at %v", d.LastActive, origin)
	}
	if age := time.Since(d.LastActive); age < 3*time.Hour {
		t.Errorf("dog accrued only %v of idle age; the detectors are still blind", age)
	}
	if d.State != StateIdle {
		t.Errorf("State = %v, want %v", d.State, StateIdle)
	}
	if d.Work != "" {
		t.Errorf("Work = %q, want it cleared", d.Work)
	}
	if !d.WorkStartedAt.IsZero() {
		t.Errorf("WorkStartedAt = %v, want zero", d.WorkStartedAt)
	}
}

// TestRollbackWorkDoesNotClobberNewerAssignment is the negative half: a late
// rollback from a failed dispatch must not erase work that has since been
// assigned, nor rewind the LastActive that newer assignment set.
func TestRollbackWorkDoesNotClobberNewerAssignment(t *testing.T) {
	m, _ := testManager(t)

	origin := time.Now().Add(-3 * time.Hour).Round(0)
	setupDogWithState(t, m, "alpha", &DogState{
		Name:       "alpha",
		State:      StateIdle,
		LastActive: origin,
		CreatedAt:  origin,
		UpdatedAt:  origin,
	})

	stale, err := m.AssignWorkIfIdle("alpha", "plugin:dolt-backup")
	if err != nil {
		t.Fatalf("AssignWorkIfIdle: %v", err)
	}

	// The dog finishes and is re-dispatched before the stale rollback lands.
	if err := m.ClearWork("alpha"); err != nil {
		t.Fatalf("ClearWork: %v", err)
	}
	if _, err := m.AssignWorkIfIdle("alpha", "plugin:github-sheriff"); err != nil {
		t.Fatalf("AssignWorkIfIdle (second): %v", err)
	}

	restored, err := m.RollbackWork("alpha", "plugin:dolt-backup", stale.WorkStartedAt, origin)
	if err != nil {
		t.Fatalf("RollbackWork: %v", err)
	}
	if restored {
		t.Fatal("stale RollbackWork reported a change; it clobbered a newer assignment")
	}

	d, err := m.Get("alpha")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if d.Work != "plugin:github-sheriff" {
		t.Errorf("Work = %q, want the newer assignment preserved", d.Work)
	}
	if d.State != StateWorking {
		t.Errorf("State = %v, want %v", d.State, StateWorking)
	}
	if d.LastActive.Equal(origin) {
		t.Error("LastActive was rewound to the stale value by a rollback that should have been a no-op")
	}
}

// TestClearWorkStillStampsLastActive pins the behavior RollbackWork must not
// take with it: a dog that genuinely finished work IS active, and completion
// must keep refreshing the clock that reapIdleDogs reads.
func TestClearWorkStillStampsLastActive(t *testing.T) {
	m, _ := testManager(t)

	origin := time.Now().Add(-3 * time.Hour).Round(0)
	setupDogWithState(t, m, "alpha", &DogState{
		Name:       "alpha",
		State:      StateIdle,
		LastActive: origin,
		CreatedAt:  origin,
		UpdatedAt:  origin,
	})

	if _, err := m.AssignWorkIfIdle("alpha", "plugin:dolt-backup"); err != nil {
		t.Fatalf("AssignWorkIfIdle: %v", err)
	}
	if err := m.ClearWork("alpha"); err != nil {
		t.Fatalf("ClearWork: %v", err)
	}

	d, err := m.Get("alpha")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !d.LastActive.After(origin) {
		t.Errorf("LastActive = %v, want it advanced past %v on genuine completion", d.LastActive, origin)
	}
}
