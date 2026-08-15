package opencodestate

import "testing"

func TestAcquireSessionLockRejectsSecondOwner(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	release, err := AcquireSessionLock(townRoot, "gt-rig-worker")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer release()
	if held, err := SessionLockHeld(townRoot, "gt-rig-worker"); err != nil || !held {
		t.Fatalf("SessionLockHeld = %v, %v, want true", held, err)
	}

	if _, err := AcquireSessionLock(townRoot, "gt-rig-worker"); err == nil {
		t.Fatal("second acquire succeeded")
	}
	release()
	if held, err := SessionLockHeld(townRoot, "gt-rig-worker"); err != nil || held {
		t.Fatalf("SessionLockHeld after release = %v, %v, want false", held, err)
	}
	if nextRelease, err := AcquireSessionLock(townRoot, "gt-rig-worker"); err != nil {
		t.Fatalf("acquire after release: %v", err)
	} else {
		nextRelease()
	}
}

func TestSessionLockHeldWithoutLockFile(t *testing.T) {
	t.Parallel()
	if held, err := SessionLockHeld(t.TempDir(), "gt-rig-worker"); err != nil || held {
		t.Fatalf("SessionLockHeld = %v, %v, want false", held, err)
	}
}
