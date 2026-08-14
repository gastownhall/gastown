package dog

import (
	"testing"
	"time"
)

func TestManager_SetWorkSourceIfMatches(t *testing.T) {
	m, _ := testManagerNoRigs(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	setupDogWithState(t, m, "alpha", &DogState{
		Name:          "alpha",
		State:         StateWorking,
		Work:          "code-review",
		WorkKind:      WorkKindFormula,
		WorkStartedAt: now,
		LastActive:    now,
		CreatedAt:     now,
		UpdatedAt:     now,
	})

	matched, err := m.SetWorkSourceIfMatches("alpha", "code-review", now, "gt-src-1")
	if err != nil {
		t.Fatalf("SetWorkSourceIfMatches() error = %v", err)
	}
	if !matched {
		t.Fatal("SetWorkSourceIfMatches() matched = false, want true")
	}

	got, err := m.Get("alpha")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.WorkSourceID != "gt-src-1" {
		t.Fatalf("WorkSourceID = %q, want gt-src-1", got.WorkSourceID)
	}
}

func TestManager_SetWorkSourceIfMatches_SkipsNewerAssignment(t *testing.T) {
	m, _ := testManagerNoRigs(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	setupDogWithState(t, m, "alpha", &DogState{
		Name:          "alpha",
		State:         StateWorking,
		Work:          "code-review",
		WorkKind:      WorkKindFormula,
		WorkStartedAt: now,
		LastActive:    now,
		CreatedAt:     now,
		UpdatedAt:     now,
	})

	matched, err := m.SetWorkSourceIfMatches("alpha", "code-review", now.Add(-time.Minute), "gt-old")
	if err != nil {
		t.Fatalf("SetWorkSourceIfMatches() error = %v", err)
	}
	if matched {
		t.Fatal("SetWorkSourceIfMatches() matched a stale timestamp")
	}
}

func TestManager_AssignWorkIfIdleWithKind_RecordsBeadSourceID(t *testing.T) {
	m, _ := testManagerNoRigs(t)
	now := time.Now().UTC()
	setupDogWithState(t, m, "alpha", &DogState{
		Name:       "alpha",
		State:      StateIdle,
		LastActive: now,
		CreatedAt:  now,
		UpdatedAt:  now,
	})

	assigned, err := m.AssignWorkIfIdleWithKind("alpha", "gt-abc", WorkKindBead)
	if err != nil {
		t.Fatalf("AssignWorkIfIdleWithKind() error = %v", err)
	}
	if assigned.WorkSourceID != "gt-abc" {
		t.Fatalf("WorkSourceID = %q, want gt-abc", assigned.WorkSourceID)
	}
}
