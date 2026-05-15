package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestTrackingDependsOnID_CrossRigWrapsExternal(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte("{\"prefix\":\"ag-\",\"path\":\"agentcompany/.beads\"}\n"), 0o644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	got := trackingDependsOnID(townRoot, "ag-95s.1")
	want := "external:ag:ag-95s.1"
	if got != want {
		t.Fatalf("trackingDependsOnID() = %q, want %q", got, want)
	}
}

func TestTrackingDependsOnID_HQStaysLocal(t *testing.T) {
	townRoot := t.TempDir()
	got := trackingDependsOnID(townRoot, "hq-cv-test")
	if got != "hq-cv-test" {
		t.Fatalf("trackingDependsOnID() = %q, want %q", got, "hq-cv-test")
	}
}

func TestIsBeadNotVisibleErr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"not found", fmt.Errorf("issue ah-leg-abcde not found"), true},
		{"does not exist", fmt.Errorf("bead does not exist yet"), true},
		{"no such issue", fmt.Errorf("no such issue: ah-syn-x"), true},
		{"unrelated", fmt.Errorf("connection refused"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isBeadNotVisibleErr(tt.err); got != tt.want {
				t.Fatalf("isBeadNotVisibleErr(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// stubTracking swaps addTrackingRelationFn and zeroes the backoff for the
// duration of a test. Not parallel-safe — these tests mutate package globals.
func stubTracking(t *testing.T, fn func(townRoot, trackerID, issueID string) error) *int {
	t.Helper()
	calls := 0
	oldFn := addTrackingRelationFn
	oldDelay := trackingRetryBaseDelay
	addTrackingRelationFn = func(townRoot, trackerID, issueID string) error {
		calls++
		return fn(townRoot, trackerID, issueID)
	}
	trackingRetryBaseDelay = 0
	t.Cleanup(func() {
		addTrackingRelationFn = oldFn
		trackingRetryBaseDelay = oldDelay
	})
	return &calls
}

// TestAddTrackingRelationWithRetry_SucceedsAfterNotVisible locks gt-4032-C:
// a freshly-created leg bead may not be visible to the tracking write yet
// (Dolt read-after-write lag); the retry must ride out the not-found window.
func TestAddTrackingRelationWithRetry_SucceedsAfterNotVisible(t *testing.T) {
	const succeedOnAttempt = 4
	calls := stubTracking(t, func(_, _, issueID string) error {
		return fmt.Errorf("issue %s not found", issueID)
	})
	// Re-stub with a counter that succeeds once the not-found window passes.
	addTrackingRelationFn = func(_, _, issueID string) error {
		*calls++
		if *calls >= succeedOnAttempt {
			return nil
		}
		return fmt.Errorf("issue %s not found", issueID)
	}

	if err := addTrackingRelationWithRetry("/town", "ah-cv-x", "ah-leg-abcde"); err != nil {
		t.Fatalf("addTrackingRelationWithRetry() = %v, want nil after retries", err)
	}
	if *calls != succeedOnAttempt {
		t.Fatalf("attempts = %d, want %d", *calls, succeedOnAttempt)
	}
}

// TestAddTrackingRelationWithRetry_GivesUp verifies the retry is bounded:
// a persistently-invisible bead exhausts attempts and returns the error.
func TestAddTrackingRelationWithRetry_GivesUp(t *testing.T) {
	calls := stubTracking(t, func(_, _, issueID string) error {
		return fmt.Errorf("issue %s not found", issueID)
	})
	if err := addTrackingRelationWithRetry("/town", "ah-cv-x", "ah-leg-gone"); err == nil {
		t.Fatal("addTrackingRelationWithRetry() = nil, want error after exhausting retries")
	}
	if *calls != trackingRetryMaxAttempts {
		t.Fatalf("attempts = %d, want %d", *calls, trackingRetryMaxAttempts)
	}
}

// TestAddTrackingRelationWithRetry_FailsFastOnOtherErrors verifies that
// non-visibility errors are not retried — only the read-after-write race is.
func TestAddTrackingRelationWithRetry_FailsFastOnOtherErrors(t *testing.T) {
	calls := stubTracking(t, func(_, _, _ string) error {
		return fmt.Errorf("connection refused")
	})
	if err := addTrackingRelationWithRetry("/town", "ah-cv-x", "ah-leg-y"); err == nil {
		t.Fatal("addTrackingRelationWithRetry() = nil, want error")
	}
	if *calls != 1 {
		t.Fatalf("attempts = %d, want 1 (no retry on non-visibility error)", *calls)
	}
}
