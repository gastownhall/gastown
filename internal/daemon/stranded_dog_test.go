package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/dog"
	"github.com/steveyegge/gastown/internal/tmux"
)

// fakeHookedWorkFinder returns canned hooked work per dog.
type fakeHookedWorkFinder struct {
	work map[string]*dog.HookedWork
	err  error
}

func (f *fakeHookedWorkFinder) HookedWork(name string) (*dog.HookedWork, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.work[name], nil
}

// stubStrandedDeps swaps the beads finder and session starter for the duration
// of a test and records which dogs had a session start attempted.
func stubStrandedDeps(t *testing.T, finder dog.HookedWorkFinder, startErr error) *[]string {
	t.Helper()

	origFinder := newHookedWorkFinder
	origStart := startDogSession
	t.Cleanup(func() {
		newHookedWorkFinder = origFinder
		startDogSession = origStart
	})

	started := []string{}
	newHookedWorkFinder = func(string) dog.HookedWorkFinder { return finder }
	startDogSession = func(_ *dog.SessionManager, name string, _ dog.SessionStartOptions) error {
		started = append(started, name)
		return startErr
	}
	return &started
}

// readDogState reads a dog's persisted state file.
func readDogState(t *testing.T, townRoot, name string) *dog.DogState {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(townRoot, "deacon", "dogs", name, ".dog.json"))
	if err != nil {
		t.Fatalf("reading dog state for %s: %v", name, err)
	}
	var ds dog.DogState
	if err := json.Unmarshal(data, &ds); err != nil {
		t.Fatalf("parsing dog state for %s: %v", name, err)
	}
	return &ds
}

func strandedTestHarness(t *testing.T, townRoot string) (*Daemon, *dog.Manager, *dog.SessionManager) {
	t.Helper()
	d := testHandlerDaemon(t, townRoot)
	mgr := dog.NewManager(townRoot, &config.RigsConfig{})
	sm := dog.NewSessionManager(tmux.NewTmux(), townRoot, mgr)
	return d, mgr, sm
}

// TestRecoverStrandedDogs_RestartsIdleDogHoldingHookedWork is the regression
// test for hq-xgq: a halt strands hooked work on an idle dog with no session,
// and nothing re-dispatched it. The daemon must now restart the session.
func TestRecoverStrandedDogs_RestartsIdleDogHoldingHookedWork(t *testing.T) {
	requireTmux(t)

	townRoot := t.TempDir()
	testSetupDogState(t, townRoot, "alpha", dog.StateIdle, time.Now())

	started := stubStrandedDeps(t, &fakeHookedWorkFinder{
		work: map[string]*dog.HookedWork{
			"alpha": {ID: "hq-wisp-03ph", Formula: "mol-dog-reaper"},
		},
	}, nil)

	d, mgr, sm := strandedTestHarness(t, townRoot)
	d.recoverStrandedDogs(mgr, sm)

	if len(*started) != 1 || (*started)[0] != "alpha" {
		t.Fatalf("expected a session start for alpha, got %v", *started)
	}

	ds := readDogState(t, townRoot, "alpha")
	if ds.State != dog.StateWorking {
		t.Errorf("state = %q, want working", ds.State)
	}
	if ds.Work != "hq-wisp-03ph" {
		t.Errorf("work = %q, want 'hq-wisp-03ph'", ds.Work)
	}
}

// TestRecoverStrandedDogs_RollsBackFailedStart verifies a failed session start
// returns the dog to idle rather than leaving it in working with no session,
// which the next tick would misreport as a zombie.
func TestRecoverStrandedDogs_RollsBackFailedStart(t *testing.T) {
	requireTmux(t)

	townRoot := t.TempDir()
	testSetupDogState(t, townRoot, "alpha", dog.StateIdle, time.Now())

	stubStrandedDeps(t, &fakeHookedWorkFinder{
		work: map[string]*dog.HookedWork{"alpha": {ID: "hq-wisp-03ph"}},
	}, errors.New("tmux refused"))

	d, mgr, sm := strandedTestHarness(t, townRoot)
	d.recoverStrandedDogs(mgr, sm)

	ds := readDogState(t, townRoot, "alpha")
	if ds.State != dog.StateIdle {
		t.Errorf("state = %q, want idle after failed start", ds.State)
	}
	if ds.Work != "" {
		t.Errorf("work = %q, want cleared after failed start", ds.Work)
	}
}

// TestRecoverStrandedDogs_SkipsIdleDogWithNothingHooked verifies the ordinary
// idle dog is left alone.
func TestRecoverStrandedDogs_SkipsIdleDogWithNothingHooked(t *testing.T) {
	requireTmux(t)

	townRoot := t.TempDir()
	testSetupDogState(t, townRoot, "alpha", dog.StateIdle, time.Now())

	started := stubStrandedDeps(t, &fakeHookedWorkFinder{work: map[string]*dog.HookedWork{}}, nil)

	d, mgr, sm := strandedTestHarness(t, townRoot)
	d.recoverStrandedDogs(mgr, sm)

	if len(*started) != 0 {
		t.Errorf("no session should start for an idle dog with nothing hooked, got %v", *started)
	}
	if ds := readDogState(t, townRoot, "alpha"); ds.State != dog.StateIdle {
		t.Errorf("state = %q, want idle", ds.State)
	}
}

// TestRecoverStrandedDogs_SkipsWorkingDogs verifies working dogs are left to
// cleanupStuckDogs / detectStaleWorkingDogs.
func TestRecoverStrandedDogs_SkipsWorkingDogs(t *testing.T) {
	requireTmux(t)

	townRoot := t.TempDir()
	testSetupWorkingDogState(t, townRoot, "alpha", "hq-wisp-03ph", time.Now())

	started := stubStrandedDeps(t, &fakeHookedWorkFinder{
		work: map[string]*dog.HookedWork{"alpha": {ID: "hq-wisp-03ph"}},
	}, nil)

	d, mgr, sm := strandedTestHarness(t, townRoot)
	d.recoverStrandedDogs(mgr, sm)

	if len(*started) != 0 {
		t.Errorf("working dogs must be skipped, got %v", *started)
	}
}

// TestRecoverStrandedDogs_BeadsFailureLeavesDogAlone verifies a beads outage
// does not disturb dog state.
func TestRecoverStrandedDogs_BeadsFailureLeavesDogAlone(t *testing.T) {
	requireTmux(t)

	townRoot := t.TempDir()
	testSetupDogState(t, townRoot, "alpha", dog.StateIdle, time.Now())

	started := stubStrandedDeps(t, &fakeHookedWorkFinder{err: errors.New("dolt unreachable")}, nil)

	d, mgr, sm := strandedTestHarness(t, townRoot)
	d.recoverStrandedDogs(mgr, sm)

	if len(*started) != 0 {
		t.Errorf("no session should start when beads is unreachable, got %v", *started)
	}
	if ds := readDogState(t, townRoot, "alpha"); ds.State != dog.StateIdle {
		t.Errorf("state = %q, want idle", ds.State)
	}
}

// TestRecoverStrandedDogs_EmptyKennel verifies the no-dogs case is a no-op.
func TestRecoverStrandedDogs_EmptyKennel(t *testing.T) {
	requireTmux(t)

	townRoot := t.TempDir()
	started := stubStrandedDeps(t, &fakeHookedWorkFinder{work: map[string]*dog.HookedWork{}}, nil)

	d, mgr, sm := strandedTestHarness(t, townRoot)
	d.recoverStrandedDogs(mgr, sm)

	if len(*started) != 0 {
		t.Errorf("empty kennel should start nothing, got %v", *started)
	}
}
