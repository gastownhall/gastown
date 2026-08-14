package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/dog"
)

func writeDogStateForDispatchTest(t *testing.T, townRoot, name string, state *dog.DogState) {
	t.Helper()
	dogPath := filepath.Join(townRoot, "deacon", "dogs", name)
	if err := os.MkdirAll(dogPath, 0755); err != nil {
		t.Fatalf("mkdir dog path: %v", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatalf("marshal dog state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dogPath, ".dog.json"), data, 0644); err != nil {
		t.Fatalf("write dog state: %v", err)
	}
}

func TestDogDispatchInfoClearWorkIfMatchesUsesAssignmentTimestamp(t *testing.T) {
	townRoot := t.TempDir()
	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	now := time.Now().Truncate(time.Second)
	workStarted := now.Add(-time.Minute)
	writeDogStateForDispatchTest(t, townRoot, "alpha", &dog.DogState{
		Name:          "alpha",
		State:         dog.StateWorking,
		Work:          "mol-dog-reaper",
		WorkStartedAt: workStarted,
		LastActive:    now,
		CreatedAt:     now,
		UpdatedAt:     now,
	})

	staleDispatch := &DogDispatchInfo{
		DogName:       "alpha",
		townRoot:      townRoot,
		workDesc:      "mol-dog-reaper",
		workStartedAt: workStarted.Add(time.Second),
		ownsWork:      true,
		rigsConfig:    rigsConfig,
	}
	if err := staleDispatch.clearWorkIfMatches(); err != nil {
		t.Fatalf("clearWorkIfMatches stale dispatch error = %v", err)
	}

	mgr := dog.NewManager(townRoot, rigsConfig)
	got, err := mgr.Get("alpha")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.State != dog.StateWorking || got.Work != "mol-dog-reaper" || !got.WorkStartedAt.Equal(workStarted) {
		t.Fatalf("stale dispatch mutated dog state: state=%q work=%q started=%v", got.State, got.Work, got.WorkStartedAt)
	}

	matchingDispatch := &DogDispatchInfo{
		DogName:       "alpha",
		townRoot:      townRoot,
		workDesc:      "mol-dog-reaper",
		workStartedAt: workStarted,
		ownsWork:      true,
		rigsConfig:    rigsConfig,
	}
	if err := matchingDispatch.clearWorkIfMatches(); err != nil {
		t.Fatalf("clearWorkIfMatches matching dispatch error = %v", err)
	}
	got, err = mgr.Get("alpha")
	if err != nil {
		t.Fatalf("Get() after clear error = %v", err)
	}
	if got.State != dog.StateIdle || got.Work != "" || !got.WorkStartedAt.IsZero() {
		t.Fatalf("matching dispatch did not clear state: state=%q work=%q started=%v", got.State, got.Work, got.WorkStartedAt)
	}
}

func TestDogDispatchInfoClearWorkIfMatchesSkipsReusedWork(t *testing.T) {
	townRoot := t.TempDir()
	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	now := time.Now().Truncate(time.Second)
	workStarted := now.Add(-time.Minute)
	writeDogStateForDispatchTest(t, townRoot, "alpha", &dog.DogState{
		Name:          "alpha",
		State:         dog.StateWorking,
		Work:          "mol-dog-reaper",
		WorkStartedAt: workStarted,
		LastActive:    now,
		CreatedAt:     now,
		UpdatedAt:     now,
	})

	reusedDispatch := &DogDispatchInfo{
		DogName:       "alpha",
		townRoot:      townRoot,
		workDesc:      "mol-dog-reaper",
		workStartedAt: workStarted,
		ownsWork:      false,
		rigsConfig:    rigsConfig,
	}
	if err := reusedDispatch.clearWorkIfMatches(); err != nil {
		t.Fatalf("clearWorkIfMatches reused dispatch error = %v", err)
	}

	got, err := dog.NewManager(townRoot, rigsConfig).Get("alpha")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.State != dog.StateWorking || got.Work != "mol-dog-reaper" || !got.WorkStartedAt.Equal(workStarted) {
		t.Fatalf("reused dispatch mutated dog state: state=%q work=%q started=%v", got.State, got.Work, got.WorkStartedAt)
	}
}

func TestReusableHookedDogFormulaSkipsStaleDogHooks(t *testing.T) {
	hooked := []*beads.Issue{
		{ID: "not-a-dog", Assignee: "gastown/polecats/alpha", Description: "attached_formula: mol-dog-reaper"},
		{ID: "bad-dog-name", Assignee: "deacon/dogs/nested/name", Description: "attached_formula: mol-dog-reaper"},
		{ID: "missing-dog", Assignee: "deacon/dogs/missing", Description: "attached_formula: mol-dog-reaper"},
		{ID: "wrong-work", Assignee: "deacon/dogs/beta", Description: "attached_formula: mol-dog-reaper"},
		{ID: "wrong-formula", Assignee: "deacon/dogs/alpha", Description: "attached_formula: mol-dog-backup"},
		{ID: "older-live-match", Assignee: "deacon/dogs/alpha", Description: "attached_formula: mol-dog-reaper\nattached_at: 2026-06-16T20:30:15Z"},
		{ID: "live-match", Assignee: "deacon/dogs/alpha", Description: "attached_formula: mol-dog-reaper\nattached_at: 2026-06-16T20:30:16Z"},
	}

	bead, dogName := reusableHookedDogFormula(hooked, "mol-dog-reaper", func(_ *beads.Issue, candidate string) bool {
		return candidate == "alpha"
	})
	if bead == nil || bead.ID != "live-match" || dogName != "alpha" {
		t.Fatalf("reusableHookedDogFormula() = (%v, %q), want live-match alpha", bead, dogName)
	}

	bead, dogName = reusableHookedDogFormula(hooked, "mol-dog-reaper", func(*beads.Issue, string) bool { return false })
	if bead != nil || dogName != "" {
		t.Fatalf("reusableHookedDogFormula() with no live dog = (%v, %q), want nil", bead, dogName)
	}
}

func TestNewestHookedFormulaPrefersLatestAttachedAt(t *testing.T) {
	hooked := []*beads.Issue{
		{ID: "stale", Description: "attached_formula: mol-dog-reaper\nattached_at: 2026-06-16T20:30:15Z"},
		{ID: "undated", Description: "attached_formula: mol-dog-reaper"},
		{ID: "other", Description: "attached_formula: mol-dog-backup\nattached_at: 2026-06-16T20:30:17Z"},
		{ID: "fresh", Description: "attached_formula: mol-dog-reaper\nattached_at: 2026-06-16T20:30:16Z"},
	}

	got := newestHookedFormula(hooked, "mol-dog-reaper")
	if got == nil || got.ID != "fresh" {
		t.Fatalf("newestHookedFormula() = %v, want fresh", got)
	}
}

func TestDogWorksOnHookRequiresFreshAttachment(t *testing.T) {
	startedAt := time.Date(2026, 6, 16, 20, 30, 15, 900_000_000, time.UTC)
	workingDog := &dog.Dog{
		Name:          "alpha",
		State:         dog.StateWorking,
		Work:          "mol-dog-reaper",
		WorkStartedAt: startedAt,
	}

	freshHook := &beads.Issue{Description: "attached_formula: mol-dog-reaper\nattached_at: " + startedAt.Format(time.RFC3339Nano)}
	if !dogWorksOnHook(workingDog, "mol-dog-reaper", freshHook) {
		t.Fatal("same-instant hook should match current dog assignment")
	}

	staleHook := &beads.Issue{Description: "attached_formula: mol-dog-reaper\nattached_at: " + startedAt.Add(-time.Nanosecond).Format(time.RFC3339Nano)}
	if dogWorksOnHook(workingDog, "mol-dog-reaper", staleHook) {
		t.Fatal("older hook must not match current dog assignment")
	}

	if dogWorksOnHook(workingDog, "mol-dog-backup", freshHook) {
		t.Fatal("different work must not match")
	}
}

func TestShouldReuseExistingFormulaSkipsStaleHookAfterFreshDogAssignment(t *testing.T) {
	startedAt := time.Date(2026, 6, 16, 20, 30, 15, 900_000_000, time.UTC)
	existing := &beads.Issue{ID: "gt-dispatch-stale", Labels: []string{formulaDispatchLabel}}
	freshDogHook := &beads.Issue{
		ID:          "gt-dispatch-fresh",
		Labels:      []string{formulaDispatchLabel},
		Description: "attached_formula: mol-dog-reaper\nattached_at: " + startedAt.Format(time.RFC3339Nano),
	}
	reusedDog := &DogDispatchInfo{
		DogName:       "alpha",
		workDesc:      "mol-dog-reaper",
		workStartedAt: startedAt,
		ownsWork:      false,
	}

	if !shouldReuseExistingFormula(existing, nil, false) {
		t.Fatal("non-dog existing formula should be reused")
	}
	if !shouldReuseExistingFormula(freshDogHook, reusedDog, false) {
		t.Fatal("dog dispatch that already reused work should no-op")
	}
	if shouldReuseExistingFormula(existing, reusedDog, false) {
		t.Fatal("dog dispatch must revalidate reused work against the current hooked formula")
	}
	if shouldReuseExistingFormula(existing, &DogDispatchInfo{ownsWork: true}, false) {
		t.Fatal("fresh dog assignment must not resurrect a stale hooked formula")
	}
	if shouldReuseExistingFormula(existing, nil, true) {
		t.Fatal("force should bypass existing formula reuse")
	}
	if shouldReuseExistingFormula(nil, nil, false) {
		t.Fatal("nil existing formula cannot be reused")
	}
}
