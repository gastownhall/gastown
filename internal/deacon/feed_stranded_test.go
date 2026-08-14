package deacon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/dog"
)

func TestFeedStrandedStateFile(t *testing.T) {
	got := FeedStrandedStateFile("/tmp/town")
	want := filepath.Join("/tmp/town", "deacon", "feed-stranded-state.json")
	if got != want {
		t.Errorf("FeedStrandedStateFile = %q, want %q", got, want)
	}
}

func TestLoadFeedStrandedState_FileNotExist(t *testing.T) {
	state, err := LoadFeedStrandedState(t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Convoys == nil {
		t.Fatal("expected initialized Convoys map")
	}
	if len(state.Convoys) != 0 {
		t.Errorf("expected empty Convoys, got %d", len(state.Convoys))
	}
}

func TestSaveThenLoadFeedStrandedState(t *testing.T) {
	tmpDir := t.TempDir()
	// Ensure deacon dir exists
	os.MkdirAll(filepath.Join(tmpDir, "deacon"), 0755)

	state := &FeedStrandedState{
		Convoys: map[string]*ConvoyFeedState{
			"hq-cv-test1": {
				ConvoyID:     "hq-cv-test1",
				FeedCount:    2,
				LastFeedTime: time.Now().UTC().Add(-5 * time.Minute),
			},
		},
	}

	if err := SaveFeedStrandedState(tmpDir, state); err != nil {
		t.Fatalf("save error: %v", err)
	}

	loaded, err := LoadFeedStrandedState(tmpDir)
	if err != nil {
		t.Fatalf("load error: %v", err)
	}

	if len(loaded.Convoys) != 1 {
		t.Fatalf("expected 1 convoy, got %d", len(loaded.Convoys))
	}

	cs := loaded.Convoys["hq-cv-test1"]
	if cs == nil {
		t.Fatal("missing hq-cv-test1")
	}
	if cs.FeedCount != 2 {
		t.Errorf("FeedCount = %d, want 2", cs.FeedCount)
	}
}

func TestConvoyFeedState_IsInCooldown(t *testing.T) {
	tests := []struct {
		name     string
		lastFeed time.Time
		cooldown time.Duration
		want     bool
	}{
		{
			name:     "zero time, not in cooldown",
			lastFeed: time.Time{},
			cooldown: 10 * time.Minute,
			want:     false,
		},
		{
			name:     "recent feed, in cooldown",
			lastFeed: time.Now().Add(-2 * time.Minute),
			cooldown: 10 * time.Minute,
			want:     true,
		},
		{
			name:     "old feed, not in cooldown",
			lastFeed: time.Now().Add(-20 * time.Minute),
			cooldown: 10 * time.Minute,
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &ConvoyFeedState{LastFeedTime: tt.lastFeed}
			if got := s.IsInCooldown(tt.cooldown); got != tt.want {
				t.Errorf("IsInCooldown() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConvoyFeedState_CooldownRemaining(t *testing.T) {
	// Zero time = no cooldown
	s := &ConvoyFeedState{}
	if got := s.CooldownRemaining(10 * time.Minute); got != 0 {
		t.Errorf("expected 0 remaining for zero time, got %v", got)
	}

	// Expired cooldown
	s.LastFeedTime = time.Now().Add(-20 * time.Minute)
	if got := s.CooldownRemaining(10 * time.Minute); got != 0 {
		t.Errorf("expected 0 remaining for expired cooldown, got %v", got)
	}

	// Active cooldown
	s.LastFeedTime = time.Now().Add(-2 * time.Minute)
	remaining := s.CooldownRemaining(10 * time.Minute)
	if remaining <= 0 || remaining > 10*time.Minute {
		t.Errorf("expected remaining between 0 and 10m, got %v", remaining)
	}
}

func TestConvoyFeedState_RecordFeed(t *testing.T) {
	s := &ConvoyFeedState{ConvoyID: "hq-cv-test"}

	if s.FeedCount != 0 {
		t.Errorf("initial FeedCount = %d, want 0", s.FeedCount)
	}

	s.RecordFeed()
	if s.FeedCount != 1 {
		t.Errorf("after RecordFeed, FeedCount = %d, want 1", s.FeedCount)
	}
	if s.LastFeedTime.IsZero() {
		t.Error("LastFeedTime should be set after RecordFeed")
	}

	s.RecordFeed()
	if s.FeedCount != 2 {
		t.Errorf("after second RecordFeed, FeedCount = %d, want 2", s.FeedCount)
	}
}

func TestGetConvoyState_CreatesNew(t *testing.T) {
	state := &FeedStrandedState{
		Convoys: make(map[string]*ConvoyFeedState),
	}

	cs := state.GetConvoyState("hq-cv-new")
	if cs == nil {
		t.Fatal("expected non-nil ConvoyFeedState")
	}
	if cs.ConvoyID != "hq-cv-new" {
		t.Errorf("ConvoyID = %q, want %q", cs.ConvoyID, "hq-cv-new")
	}
	if cs.FeedCount != 0 {
		t.Errorf("FeedCount = %d, want 0", cs.FeedCount)
	}
}

func TestGetConvoyState_ReturnsExisting(t *testing.T) {
	state := &FeedStrandedState{
		Convoys: map[string]*ConvoyFeedState{
			"hq-cv-exist": {ConvoyID: "hq-cv-exist", FeedCount: 5},
		},
	}

	cs := state.GetConvoyState("hq-cv-exist")
	if cs.FeedCount != 5 {
		t.Errorf("FeedCount = %d, want 5", cs.FeedCount)
	}
}

func TestGetConvoyState_NilMap(t *testing.T) {
	state := &FeedStrandedState{}

	cs := state.GetConvoyState("hq-cv-test")
	if cs == nil {
		t.Fatal("expected non-nil ConvoyFeedState even with nil map")
	}
	if state.Convoys == nil {
		t.Fatal("Convoys map should be initialized")
	}
}

func TestLoadFeedStrandedState_CorruptedFile(t *testing.T) {
	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, "deacon")
	os.MkdirAll(stateDir, 0755)

	// Write invalid JSON
	stateFile := filepath.Join(stateDir, "feed-stranded-state.json")
	os.WriteFile(stateFile, []byte("not json"), 0600)

	_, err := LoadFeedStrandedState(tmpDir)
	if err == nil {
		t.Fatal("expected error for corrupted file")
	}
}

func TestSaveFeedStrandedState_CreatesDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	// Don't pre-create deacon dir — SaveFeedStrandedState should create it

	state := &FeedStrandedState{
		Convoys: make(map[string]*ConvoyFeedState),
	}

	if err := SaveFeedStrandedState(tmpDir, state); err != nil {
		t.Fatalf("save error: %v", err)
	}

	// Verify file was created
	stateFile := FeedStrandedStateFile(tmpDir)
	if _, err := os.Stat(stateFile); os.IsNotExist(err) {
		t.Fatal("state file not created")
	}
}

func TestAdmitConvoyFeed_RejectsBlockedHookedOrAssigned(t *testing.T) {
	tests := []struct {
		name     string
		children []ConvoyChild
		wantOK   bool
	}{
		{name: "all open unassigned", children: []ConvoyChild{{ID: "gt-a", Status: "open"}}, wantOK: true},
		{name: "blocked sibling", children: []ConvoyChild{{ID: "gt-a", Status: "open"}, {ID: "gt-b", Blocked: true}}, wantOK: false},
		{name: "hooked sibling", children: []ConvoyChild{{ID: "gt-a", Status: "open"}, {ID: "gt-b", Status: "hooked"}}, wantOK: false},
		{name: "assigned sibling", children: []ConvoyChild{{ID: "gt-a", Status: "open"}, {ID: "gt-b", Status: "open", Assignee: "gastown/polecats/nux"}}, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, _ := AdmitConvoyFeed(tt.children)
			if ok != tt.wantOK {
				t.Fatalf("AdmitConvoyFeed() = %v, want %v", ok, tt.wantOK)
			}
		})
	}
}

func TestFeedStranded_GuardianDenied_NoDispatch(t *testing.T) {
	townRoot := t.TempDir()
	path := dog.GuardianFile(townRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"dispatch_allowed":false,"activation_allowed":false,"reason":"red"}`), 0600); err != nil {
		t.Fatal(err)
	}

	result := FeedStranded(townRoot, 3, time.Minute)
	if result.Fed != 0 {
		t.Fatalf("Fed = %d, want 0 when guardian denies dispatch", result.Fed)
	}
	if result.Closed != 0 {
		t.Fatalf("Closed = %d, want 0 when guardian denies dispatch", result.Closed)
	}
	if len(result.Details) == 0 || result.Details[0].Action != "blocked" {
		t.Fatalf("details = %+v, want blocked guardian result", result.Details)
	}
}

func TestFeedStranded_RejectsConvoyWithAssignedChild(t *testing.T) {
	prevFind := findStrandedConvoys
	prevList := listConvoyChildren
	prevDispatch := dispatchFeedDog
	t.Cleanup(func() {
		findStrandedConvoys = prevFind
		listConvoyChildren = prevList
		dispatchFeedDog = prevDispatch
	})

	dispatched := 0
	findStrandedConvoys = func(string) ([]StrandedConvoy, error) {
		return []StrandedConvoy{{ID: "hq-cv-1", ReadyCount: 1, TrackedCount: 2, ReadyIssues: []string{"gt-a"}}}, nil
	}
	listConvoyChildren = func(string, string) ([]ConvoyChild, error) {
		return []ConvoyChild{
			{ID: "gt-a", Status: "open"},
			{ID: "gt-b", Status: "open", Assignee: "deacon/dogs/alpha"},
		}, nil
	}
	dispatchFeedDog = func(string, string) error {
		dispatched++
		return nil
	}

	result := FeedStranded(t.TempDir(), 3, time.Minute)
	if dispatched != 0 {
		t.Fatalf("dispatched = %d, want 0 for assigned sibling", dispatched)
	}
	if len(result.Details) != 1 || result.Details[0].Action != "rejected" {
		t.Fatalf("details = %+v, want rejected", result.Details)
	}
}
