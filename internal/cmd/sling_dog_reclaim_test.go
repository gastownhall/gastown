package cmd

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/dog"
)

func dogFormulaWisp(status, formulaName string, attachedAt time.Time) *beads.Issue {
	desc := "attached_formula: " + formulaName
	if !attachedAt.IsZero() {
		desc += "\nattached_at: " + attachedAt.UTC().Format(time.RFC3339Nano)
	}
	return &beads.Issue{ID: "hq-wisp-root", Status: status, Assignee: "deacon/dogs/alpha", Description: desc}
}

func TestDogFormulaMoleculeActive(t *testing.T) {
	started := time.Date(2026, 6, 16, 3, 6, 38, 0, time.UTC)
	tests := []struct {
		name  string
		wisps []*beads.Issue
		want  bool
	}{
		{name: "no molecules", want: false},
		{name: "root closed by the agent", wisps: []*beads.Issue{dogFormulaWisp("closed", "mol-dog-reaper", started.Add(time.Second))}, want: false},
		{name: "root still hooked", wisps: []*beads.Issue{dogFormulaWisp(beads.StatusHooked, "mol-dog-reaper", started.Add(time.Second))}, want: true},
		{name: "root in progress", wisps: []*beads.Issue{dogFormulaWisp("in_progress", "mol-dog-reaper", started.Add(time.Second))}, want: true},
		{name: "open root from an earlier assignment", wisps: []*beads.Issue{dogFormulaWisp(beads.StatusHooked, "mol-dog-reaper", started.Add(-time.Hour))}, want: false},
		{name: "open root without attached_at", wisps: []*beads.Issue{dogFormulaWisp(beads.StatusHooked, "mol-dog-reaper", time.Time{})}, want: true},
		{name: "other formula", wisps: []*beads.Issue{dogFormulaWisp(beads.StatusHooked, "mol-dog-doctor", started.Add(time.Second))}, want: false},
		{name: "plain bead", wisps: []*beads.Issue{{ID: "hq-plain", Status: beads.StatusHooked, Description: "no fields"}}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dogFormulaMoleculeActive(tt.wisps, "mol-dog-reaper", started); got != tt.want {
				t.Fatalf("dogFormulaMoleculeActive() = %v, want %v", got, tt.want)
			}
		})
	}
}

func stubDogReclaimSeams(t *testing.T, now time.Time, isFormula bool, wisps []*beads.Issue, listErr error) *int {
	t.Helper()
	oldIsFormula, oldList, oldNow := isDogFormulaWorkFn, listDogWispsFn, dogReclaimNowFn
	t.Cleanup(func() {
		isDogFormulaWorkFn, listDogWispsFn, dogReclaimNowFn = oldIsFormula, oldList, oldNow
	})
	listCalls := 0
	isDogFormulaWorkFn = func(_, work string) bool { return isFormula && strings.HasPrefix(work, "mol-") }
	listDogWispsFn = func(_, agentID string) ([]*beads.Issue, error) {
		listCalls++
		if agentID != "deacon/dogs/alpha" {
			t.Errorf("listed molecules of %q, want deacon/dogs/alpha", agentID)
		}
		return wisps, listErr
	}
	dogReclaimNowFn = func() time.Time { return now }
	return &listCalls
}

func TestReclaimFinishedFormulaDog(t *testing.T) {
	now := time.Date(2026, 6, 16, 3, 36, 40, 0, time.UTC)
	started := now.Add(-30 * time.Minute)
	listErr := errors.New("bd unavailable")

	tests := []struct {
		name          string
		work          string
		startedAt     time.Time
		isFormula     bool
		wisps         []*beads.Issue
		listErr       error
		wantReclaimed bool
		wantErr       bool
		wantListed    bool
	}{
		{
			name:          "formula root closed without gt dog done",
			work:          "mol-dog-reaper",
			startedAt:     started,
			isFormula:     true,
			wisps:         []*beads.Issue{dogFormulaWisp("closed", "mol-dog-reaper", started.Add(time.Second))},
			wantReclaimed: true,
			wantListed:    true,
		},
		{
			name:          "formula root gone after wisp gc",
			work:          "mol-dog-reaper",
			startedAt:     started,
			isFormula:     true,
			wantReclaimed: true,
			wantListed:    true,
		},
		{
			name:       "formula still hooked",
			work:       "mol-dog-reaper",
			startedAt:  started,
			isFormula:  true,
			wisps:      []*beads.Issue{dogFormulaWisp(beads.StatusHooked, "mol-dog-reaper", started.Add(time.Second))},
			wantListed: true,
		},
		{
			name:      "assignment inside the grace period",
			work:      "mol-dog-reaper",
			startedAt: now.Add(-time.Minute),
			isFormula: true,
		},
		{
			name:      "plugin work",
			work:      "plugin:rebuild-gt",
			startedAt: started,
			isFormula: true,
		},
		{
			name:      "slung bead",
			work:      "gt-abc",
			startedAt: started,
			isFormula: false,
		},
		{
			name:       "listing fails",
			work:       "mol-dog-reaper",
			startedAt:  started,
			isFormula:  true,
			listErr:    listErr,
			wantErr:    true,
			wantListed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			townRoot := t.TempDir()
			rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
			writeDogStateForDispatchTest(t, townRoot, "alpha", &dog.DogState{
				Name:          "alpha",
				State:         dog.StateWorking,
				Work:          tt.work,
				WorkStartedAt: tt.startedAt,
				LastActive:    tt.startedAt,
				CreatedAt:     tt.startedAt,
				UpdatedAt:     tt.startedAt,
			})
			listCalls := stubDogReclaimSeams(t, now, tt.isFormula, tt.wisps, tt.listErr)

			mgr := dog.NewManager(townRoot, rigsConfig)
			d, err := mgr.Get("alpha")
			if err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			reclaimed, err := reclaimFinishedFormulaDog(mgr, townRoot, d)
			if (err != nil) != tt.wantErr {
				t.Fatalf("reclaimFinishedFormulaDog() error = %v, wantErr %v", err, tt.wantErr)
			}
			if reclaimed != tt.wantReclaimed {
				t.Fatalf("reclaimFinishedFormulaDog() = %v, want %v", reclaimed, tt.wantReclaimed)
			}
			if got := *listCalls > 0; got != tt.wantListed {
				t.Fatalf("listed dog molecules = %v, want %v", got, tt.wantListed)
			}

			got, err := mgr.Get("alpha")
			if err != nil {
				t.Fatalf("Get() after reclaim error = %v", err)
			}
			if tt.wantReclaimed {
				if got.State != dog.StateIdle || got.Work != "" || !got.WorkStartedAt.IsZero() {
					t.Fatalf("dog after reclaim = state %q work %q started %v, want idle with no work", got.State, got.Work, got.WorkStartedAt)
				}
				return
			}
			if got.State != dog.StateWorking || got.Work != tt.work || !got.WorkStartedAt.Equal(tt.startedAt) {
				t.Fatalf("dog changed without reclaim: state %q work %q started %v", got.State, got.Work, got.WorkStartedAt)
			}
		})
	}
}

func TestReclaimFinishedFormulaDogKeepsNewerAssignment(t *testing.T) {
	now := time.Date(2026, 6, 16, 3, 36, 40, 0, time.UTC)
	started := now.Add(-30 * time.Minute)
	townRoot := t.TempDir()
	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	writeDogStateForDispatchTest(t, townRoot, "alpha", &dog.DogState{
		Name:          "alpha",
		State:         dog.StateWorking,
		Work:          "mol-dog-reaper",
		WorkStartedAt: started,
		CreatedAt:     started,
		UpdatedAt:     started,
	})
	stubDogReclaimSeams(t, now, true, nil, nil)

	mgr := dog.NewManager(townRoot, rigsConfig)
	stale, err := mgr.Get("alpha")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	// Another dispatch reassigns the dog between the read and the reclaim.
	if err := mgr.ClearWork("alpha"); err != nil {
		t.Fatalf("ClearWork() error = %v", err)
	}
	if _, err := mgr.AssignWorkIfIdle("alpha", "mol-dog-reaper"); err != nil {
		t.Fatalf("AssignWorkIfIdle() error = %v", err)
	}

	reclaimed, err := reclaimFinishedFormulaDog(mgr, townRoot, stale)
	if err != nil {
		t.Fatalf("reclaimFinishedFormulaDog() error = %v", err)
	}
	if reclaimed {
		t.Fatal("reclaimFinishedFormulaDog() cleared a newer assignment")
	}
	got, err := mgr.Get("alpha")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.State != dog.StateWorking || got.Work != "mol-dog-reaper" {
		t.Fatalf("newer assignment lost: state %q work %q", got.State, got.Work)
	}
}

func TestReclaimFinishedFormulaDogBestEffortUpdatesDog(t *testing.T) {
	now := time.Date(2026, 6, 16, 3, 36, 40, 0, time.UTC)
	started := now.Add(-30 * time.Minute)
	townRoot := t.TempDir()
	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	writeDogStateForDispatchTest(t, townRoot, "alpha", &dog.DogState{
		Name:          "alpha",
		State:         dog.StateWorking,
		Work:          "mol-dog-reaper",
		WorkStartedAt: started,
		CreatedAt:     started,
		UpdatedAt:     started,
	})
	stubDogReclaimSeams(t, now, true, nil, nil)

	mgr := dog.NewManager(townRoot, rigsConfig)
	d, err := mgr.Get("alpha")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	reclaimFinishedFormulaDogBestEffort(mgr, townRoot, d)
	if d.State != dog.StateIdle || d.Work != "" || !d.WorkStartedAt.IsZero() {
		t.Fatalf("in-memory dog = state %q work %q started %v, want idle", d.State, d.Work, d.WorkStartedAt)
	}
	idle, err := mgr.GetIdleDog()
	if err != nil {
		t.Fatalf("GetIdleDog() error = %v", err)
	}
	if idle == nil || idle.Name != "alpha" {
		t.Fatalf("GetIdleDog() = %v, want alpha back in the pool", idle)
	}
}

func TestStartDelayedSessionReplacesLeftoverSessionOnlyForNewAssignment(t *testing.T) {
	for _, ownsWork := range []bool{true, false} {
		oldStart := startDogSessionFn
		var gotFresh []bool
		startDogSessionFn = func(_ *dog.SessionManager, dogName string, opts dog.SessionStartOptions, fresh bool) (string, error) {
			if dogName != "alpha" || opts.WorkDesc != "mol-dog-reaper" {
				t.Errorf("started %q for %q, want alpha for mol-dog-reaper", dogName, opts.WorkDesc)
			}
			gotFresh = append(gotFresh, fresh)
			return "%1", nil
		}

		info := &DogDispatchInfo{
			DogName:        "alpha",
			sessionDelayed: true,
			townRoot:       t.TempDir(),
			workDesc:       "mol-dog-reaper",
			ownsWork:       ownsWork,
			rigsConfig:     &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}},
		}
		pane, err := info.StartDelayedSession()
		startDogSessionFn = oldStart
		if err != nil {
			t.Fatalf("ownsWork=%v: StartDelayedSession() error = %v", ownsWork, err)
		}
		if pane != "%1" {
			t.Fatalf("ownsWork=%v: pane = %q, want %%1", ownsWork, pane)
		}
		if len(gotFresh) != 1 || gotFresh[0] != ownsWork {
			t.Fatalf("ownsWork=%v: fresh = %v, want [%v]", ownsWork, gotFresh, ownsWork)
		}
	}
}

func TestDogFormulaPromptRequiresDogDone(t *testing.T) {
	prompt := dogFormulaPrompt(formulaSlingPrompt("mol-dog-reaper"))
	for _, want := range []string{"mol-dog-reaper", "dog done", "do not close the molecule yourself"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("dog formula prompt %q missing %q", prompt, want)
		}
	}
}
