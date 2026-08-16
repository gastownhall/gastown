package witness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/worker"
)

func writeStoppedWithoutDoneTown(t *testing.T, beadID string) string {
	t.Helper()
	town := t.TempDir()
	if err := os.MkdirAll(filepath.Join(town, "mayor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(town, "mayor", "town.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code := 1
	if err := worker.PersistRun(town, &worker.Run{
		RunID:     "run-" + beadID,
		SessionID: "gt-test-" + beadID,
		BeadID:    beadID,
		State:     worker.StateStopped,
		Done:      false,
		ExitCode:  &code,
		StartedAt: time.Now().UTC().Add(-time.Minute),
		UpdatedAt: time.Now().UTC(),
		StoppedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	return town
}

func TestGetMountainFailureCount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		labels []string
		want   int
	}{
		{"no labels", nil, 0},
		{"no failure label", []string{"mountain", "task"}, 0},
		{"failure count 1", []string{"mountain:failures:1"}, 1},
		{"failure count 3", []string{"other", "mountain:failures:3", "task"}, 3},
		{"invalid count", []string{"mountain:failures:abc"}, 0},
		{"empty count", []string{"mountain:failures:"}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := getMountainFailureCount(tt.labels)
			if got != tt.want {
				t.Errorf("getMountainFailureCount(%v) = %d, want %d", tt.labels, got, tt.want)
			}
		})
	}
}

func TestHasLabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		labels []string
		target string
		want   bool
	}{
		{"found", []string{"mountain", "task"}, "mountain", true},
		{"not found", []string{"task", "bug"}, "mountain", false},
		{"empty labels", nil, "mountain", false},
		{"exact match required", []string{"mountain:failures:1"}, "mountain", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := hasLabel(tt.labels, tt.target)
			if got != tt.want {
				t.Errorf("hasLabel(%v, %q) = %v, want %v", tt.labels, tt.target, got, tt.want)
			}
		})
	}
}

// mockBdCli creates a BdCli with controllable responses for testing.
// execResponses maps "command args" to (output, error) pairs.
// runResponses maps "command args" to error.
type mockBdResponses struct {
	execCalls   []string
	runCalls    []string
	execResults map[string]mockExecResult
	runResults  map[string]error
}

type mockExecResult struct {
	output string
	err    error
}

func newMockBd() *mockBdResponses {
	return &mockBdResponses{
		execResults: make(map[string]mockExecResult),
		runResults:  make(map[string]error),
	}
}

func (m *mockBdResponses) toBdCli() *BdCli {
	return &BdCli{
		Exec: func(workDir string, args ...string) (string, error) {
			key := strings.Join(args, " ")
			m.execCalls = append(m.execCalls, key)
			if r, ok := m.execResults[key]; ok {
				return r.output, r.err
			}
			// Check for prefix matches (for commands with variable args)
			for k, r := range m.execResults {
				if strings.HasPrefix(key, k) {
					return r.output, r.err
				}
			}
			return "", nil
		},
		Run: func(workDir string, args ...string) error {
			key := strings.Join(args, " ")
			m.runCalls = append(m.runCalls, key)
			if err, ok := m.runResults[key]; ok {
				return err
			}
			return nil
		},
	}
}

func TestTrackConvoyFailure_NoConvoy(t *testing.T) {
	t.Parallel()
	mock := newMockBd()
	// dep list returns empty
	mock.execResults["dep list gt-abc --direction=up --type=tracks --json"] = mockExecResult{output: "[]"}

	result := TrackConvoyFailure(mock.toBdCli(), "/tmp", "gt-abc")
	if result != nil {
		t.Errorf("expected nil for issue with no convoy, got %+v", result)
	}
}

func TestTrackConvoyFailure_EmptyIssueID(t *testing.T) {
	t.Parallel()
	mock := newMockBd()
	result := TrackConvoyFailure(mock.toBdCli(), "/tmp", "")
	if result != nil {
		t.Errorf("expected nil for empty issue ID, got %+v", result)
	}
}

func TestTrackConvoyFailure_RegularConvoy(t *testing.T) {
	t.Parallel()
	mock := newMockBd()

	// dep list returns a convoy
	deps, _ := json.Marshal([]struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}{{ID: "hq-cv-abc", Type: "tracks"}})
	mock.execResults["dep list gt-xyz --direction=up --type=tracks --json"] = mockExecResult{output: string(deps)}

	// Convoy has no mountain label
	convoyShow, _ := json.Marshal([]struct {
		Labels []string `json:"labels"`
	}{{Labels: []string{"convoy"}}})
	mock.execResults["show hq-cv-abc --json"] = mockExecResult{output: string(convoyShow)}

	result := TrackConvoyFailure(mock.toBdCli(), "/tmp", "gt-xyz")
	if result == nil {
		t.Fatal("expected non-nil result for convoy-tracked issue")
	}
	if result.IsMountain {
		t.Error("expected IsMountain=false for regular convoy")
	}
	if result.Warning == "" {
		t.Error("expected warning for regular convoy failure")
	}
	if !strings.Contains(result.Warning, "gt-xyz") {
		t.Errorf("warning should mention issue ID, got: %s", result.Warning)
	}
}

func TestTrackConvoyFailure_MountainFirstFailure(t *testing.T) {
	t.Parallel()
	mock := newMockBd()

	// dep list returns a mountain convoy
	deps, _ := json.Marshal([]struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}{{ID: "hq-cv-mtn", Type: "tracks"}})
	mock.execResults["dep list gt-task1 --direction=up --type=tracks --json"] = mockExecResult{output: string(deps)}

	// Convoy has mountain label
	convoyShow, _ := json.Marshal([]struct {
		Labels []string `json:"labels"`
	}{{Labels: []string{"convoy", "mountain"}}})
	mock.execResults["show hq-cv-mtn --json"] = mockExecResult{output: string(convoyShow)}

	// Issue has no existing failure labels
	issueShow, _ := json.Marshal([]struct {
		Labels []string `json:"labels"`
	}{{Labels: []string{"task"}}})
	mock.execResults["show gt-task1 --json"] = mockExecResult{output: string(issueShow)}

	result := TrackConvoyFailure(mock.toBdCli(), "/tmp", "gt-task1")
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if !result.IsMountain {
		t.Error("expected IsMountain=true")
	}
	if result.FailureCount != 1 {
		t.Errorf("FailureCount = %d, want 1", result.FailureCount)
	}
	if result.Skipped {
		t.Error("should not be skipped after 1 failure")
	}

	// Verify the right bd update was called
	found := false
	for _, call := range mock.runCalls {
		if strings.Contains(call, "mountain:failures:1") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected bd update with mountain:failures:1, got calls: %v", mock.runCalls)
	}
}

func TestTrackConvoyFailure_MountainThirdFailure_AutoSkip(t *testing.T) {
	t.Parallel()
	mock := newMockBd()

	// dep list returns a mountain convoy
	deps, _ := json.Marshal([]struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}{{ID: "hq-cv-mtn", Type: "tracks"}})
	mock.execResults["dep list gt-task2 --direction=up --type=tracks --json"] = mockExecResult{output: string(deps)}

	// Convoy has mountain label
	convoyShow, _ := json.Marshal([]struct {
		Labels []string `json:"labels"`
	}{{Labels: []string{"mountain"}}})
	mock.execResults["show hq-cv-mtn --json"] = mockExecResult{output: string(convoyShow)}

	// Issue already has 2 failures
	issueShow, _ := json.Marshal([]struct {
		Labels []string `json:"labels"`
	}{{Labels: []string{"mountain:failures:2"}}})
	mock.execResults["show gt-task2 --json"] = mockExecResult{output: string(issueShow)}

	result := TrackConvoyFailure(mock.toBdCli(), "/tmp", "gt-task2")
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.FailureCount != 3 {
		t.Errorf("FailureCount = %d, want 3", result.FailureCount)
	}
	if !result.Skipped {
		t.Error("expected Skipped=true after 3 failures")
	}

	// Verify skip action: status=blocked + mountain:skipped label
	foundSkip := false
	for _, call := range mock.runCalls {
		if strings.Contains(call, "--status=blocked") && strings.Contains(call, "mountain:skipped") {
			foundSkip = true
		}
	}
	if !foundSkip {
		t.Errorf("expected bd update with --status=blocked and mountain:skipped, got calls: %v", mock.runCalls)
	}
}

func TestTrackConvoyFailure_MountainSecondFailure_NoSkip(t *testing.T) {
	t.Parallel()
	mock := newMockBd()

	// dep list returns a mountain convoy
	deps, _ := json.Marshal([]struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}{{ID: "hq-cv-mtn", Type: "tracks"}})
	mock.execResults["dep list gt-task3 --direction=up --type=tracks --json"] = mockExecResult{output: string(deps)}

	// Convoy has mountain label
	convoyShow, _ := json.Marshal([]struct {
		Labels []string `json:"labels"`
	}{{Labels: []string{"mountain"}}})
	mock.execResults["show hq-cv-mtn --json"] = mockExecResult{output: string(convoyShow)}

	// Issue already has 1 failure
	issueShow, _ := json.Marshal([]struct {
		Labels []string `json:"labels"`
	}{{Labels: []string{"mountain:failures:1"}}})
	mock.execResults["show gt-task3 --json"] = mockExecResult{output: string(issueShow)}

	result := TrackConvoyFailure(mock.toBdCli(), "/tmp", "gt-task3")
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.FailureCount != 2 {
		t.Errorf("FailureCount = %d, want 2", result.FailureCount)
	}
	if result.Skipped {
		t.Error("should not be skipped after 2 failures")
	}

	// Verify old label removed and new one added
	foundRemove := false
	foundAdd := false
	for _, call := range mock.runCalls {
		if strings.Contains(call, "--remove-label") && strings.Contains(call, "mountain:failures:1") {
			foundRemove = true
		}
		if strings.Contains(call, "--add-label") && strings.Contains(call, "mountain:failures:2") {
			foundAdd = true
		}
	}
	if !foundRemove {
		t.Errorf("expected --remove-label mountain:failures:1, got calls: %v", mock.runCalls)
	}
	if !foundAdd {
		t.Errorf("expected --add-label mountain:failures:2, got calls: %v", mock.runCalls)
	}
}

func TestTrackConvoyFailures_Integration(t *testing.T) {
	t.Parallel()

	// Test the trackConvoyFailures function that processes zombie results
	mock := newMockBd()

	// Set up zombie results
	result := &DetectZombiePolecatsResult{
		Checked: 3,
		Zombies: []ZombieResult{
			{
				PolecatName:    "alpha",
				Classification: ZombieSessionDeadActive,
				HookBead:       "gt-task-a",
			},
			{
				// Completed bead — should NOT be tracked
				PolecatName:    "beta",
				Classification: ZombieBeadClosedStillRunning,
				HookBead:       "gt-task-b",
			},
			{
				// No hook bead — should NOT be tracked
				PolecatName:    "gamma",
				Classification: ZombieAgentDeadInSession,
				HookBead:       "",
			},
			{
				// Submitted/orphan cleanup — should NOT be tracked as a failure
				PolecatName:    "delta",
				Classification: ZombieSubmittedStillRunning,
				HookBead:       "gt-task-d",
				WasActive:      false,
			},
		},
	}

	// Only gt-task-a should be checked for convoy tracking
	// No convoys for it
	mock.execResults["dep list gt-task-a --direction=up --type=tracks --json"] = mockExecResult{output: "[]"}

	town := writeStoppedWithoutDoneTown(t, "gt-task-a")
	trackConvoyFailures(mock.toBdCli(), town, result)

	// Should have queried only gt-task-a (not gt-task-b, empty, or submitted idle)
	if len(mock.execCalls) != 1 {
		t.Errorf("expected 1 exec call, got %d: %v", len(mock.execCalls), mock.execCalls)
	}
	if len(result.ConvoyFailures) != 0 {
		t.Errorf("expected 0 convoy failures for non-tracked issues, got %d", len(result.ConvoyFailures))
	}
}

func TestTrackConvoyFailures_MountainZombie(t *testing.T) {
	t.Parallel()
	mock := newMockBd()

	result := &DetectZombiePolecatsResult{
		Checked: 1,
		Zombies: []ZombieResult{
			{
				PolecatName:    "nux",
				Classification: ZombieSessionDeadActive,
				HookBead:       "gt-mtn-task",
			},
		},
	}

	// dep list returns mountain convoy
	deps, _ := json.Marshal([]struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}{{ID: "hq-cv-mtn", Type: "tracks"}})
	mock.execResults["dep list gt-mtn-task --direction=up --type=tracks --json"] = mockExecResult{output: string(deps)}

	// Convoy has mountain label
	convoyShow, _ := json.Marshal([]struct {
		Labels []string `json:"labels"`
	}{{Labels: []string{"mountain"}}})
	mock.execResults["show hq-cv-mtn --json"] = mockExecResult{output: string(convoyShow)}

	// Issue has no existing failures
	issueShow, _ := json.Marshal([]struct {
		Labels []string `json:"labels"`
	}{{Labels: []string{}}})
	mock.execResults["show gt-mtn-task --json"] = mockExecResult{output: string(issueShow)}

	town := writeStoppedWithoutDoneTown(t, "gt-mtn-task")
	trackConvoyFailures(mock.toBdCli(), town, result)

	if len(result.ConvoyFailures) != 1 {
		t.Fatalf("expected 1 convoy failure, got %d", len(result.ConvoyFailures))
	}

	cf := result.ConvoyFailures[0]
	if cf.IssueID != "gt-mtn-task" {
		t.Errorf("IssueID = %q, want %q", cf.IssueID, "gt-mtn-task")
	}
	if !cf.IsMountain {
		t.Error("expected IsMountain=true")
	}
	if cf.FailureCount != 1 {
		t.Errorf("FailureCount = %d, want 1", cf.FailureCount)
	}
	if cf.Skipped {
		t.Error("should not be skipped after 1 failure")
	}
}

func TestTrackConvoyFailures_PaneMissDoesNotIncrement(t *testing.T) {
	t.Parallel()
	mock := newMockBd()

	result := &DetectZombiePolecatsResult{
		Checked: 1,
		Zombies: []ZombieResult{{
			PolecatName:    "nux",
			Classification: ZombieSessionDeadActive,
			HookBead:       "gt-pane-miss",
		}},
	}
	deps, _ := json.Marshal([]struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}{{ID: "hq-cv-mtn", Type: "tracks"}})
	mock.execResults["dep list gt-pane-miss --direction=up --type=tracks --json"] = mockExecResult{output: string(deps)}

	town := t.TempDir()
	if err := os.MkdirAll(filepath.Join(town, "mayor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(town, "mayor", "town.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	trackConvoyFailures(mock.toBdCli(), town, result)
	if len(result.ConvoyFailures) != 0 {
		t.Fatalf("pane miss must not raise mountain failures, got %d", len(result.ConvoyFailures))
	}
	if len(mock.execCalls) != 0 {
		t.Fatalf("pane miss must not query convoys, got %v", mock.execCalls)
	}
}
