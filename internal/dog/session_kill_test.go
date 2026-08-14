package dog

import (
	"testing"
	"time"
)

func TestShouldKillCompletedDogSession(t *testing.T) {
	tests := []struct {
		name    string
		current *Dog
		want    bool
	}{
		{name: "idle with no work", current: &Dog{State: StateIdle}, want: true},
		{name: "working assignment", current: &Dog{State: StateWorking, Work: "gt-new"}, want: false},
		{name: "idle but already reassigned", current: &Dog{State: StateIdle, Work: "gt-new"}, want: false},
		{name: "nil dog", current: nil, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShouldKillCompletedDogSession(tt.current); got != tt.want {
				t.Fatalf("ShouldKillCompletedDogSession() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestKillCompletedDogSession_SkipsNewerAssignment(t *testing.T) {
	m, _ := testManagerNoRigs(t)
	now := time.Now().UTC().Truncate(time.Second)
	setupDogWithState(t, m, "alpha", &DogState{
		Name:          "alpha",
		State:         StateWorking,
		Work:          "gt-new",
		WorkKind:      WorkKindBead,
		WorkStartedAt: now,
		LastActive:    now,
		CreatedAt:     now,
		UpdatedAt:     now,
	})

	killed := false
	if err := KillCompletedDogSession(m, "alpha", "hq-dog-alpha", func(string) error {
		killed = true
		return nil
	}); err != nil {
		t.Fatalf("KillCompletedDogSession() error = %v", err)
	}
	if killed {
		t.Fatal("kill ran against a dog that already had a newer assignment")
	}
}

func TestKillCompletedDogSession_KillsIdleDog(t *testing.T) {
	m, _ := testManagerNoRigs(t)
	now := time.Now().UTC().Truncate(time.Second)
	setupDogWithState(t, m, "alpha", &DogState{
		Name:       "alpha",
		State:      StateIdle,
		LastActive: now,
		CreatedAt:  now,
		UpdatedAt:  now,
	})

	var gotSession string
	if err := KillCompletedDogSession(m, "alpha", "hq-dog-alpha", func(sessionID string) error {
		gotSession = sessionID
		return nil
	}); err != nil {
		t.Fatalf("KillCompletedDogSession() error = %v", err)
	}
	if gotSession != "hq-dog-alpha" {
		t.Fatalf("killed session = %q, want hq-dog-alpha", gotSession)
	}
}
