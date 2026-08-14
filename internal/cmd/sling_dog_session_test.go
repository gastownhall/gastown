package cmd

import (
	"errors"
	"testing"

	"github.com/steveyegge/gastown/internal/dog"
)

func TestStartDelayedSession_FailsClosedOnStaleSession(t *testing.T) {
	info := &DogDispatchInfo{
		DogName:        "alpha",
		sessionDelayed: true,
		townRoot:       t.TempDir(),
		workDesc:       "gt-new",
	}
	prev := ensureDogSession
	t.Cleanup(func() { ensureDogSession = prev })

	ensureDogSession = func(*DogDispatchInfo) (string, error) {
		return "", dog.ErrSessionRunning
	}
	if _, err := info.StartDelayedSession(); !errors.Is(err, dog.ErrSessionRunning) {
		t.Fatalf("StartDelayedSession() error = %v, want ErrSessionRunning", err)
	}

	info.sessionDelayed = true
	ensureDogSession = func(*DogDispatchInfo) (string, error) {
		return "", nil
	}
	if _, err := info.StartDelayedSession(); err == nil {
		t.Fatal("empty pane must fail closed")
	}

	info.sessionDelayed = true
	ensureDogSession = func(*DogDispatchInfo) (string, error) {
		return "%5", nil
	}
	pane, err := info.StartDelayedSession()
	if err != nil {
		t.Fatalf("StartDelayedSession() error = %v", err)
	}
	if pane != "%5" || info.Pane != "%5" || info.sessionDelayed {
		t.Fatalf("pane=%q delayed=%v, want %%5 started", pane, info.sessionDelayed)
	}
}

func TestStartDelayedSession_AlreadyStartedRequiresPane(t *testing.T) {
	info := &DogDispatchInfo{DogName: "alpha", Pane: ""}
	if _, err := info.StartDelayedSession(); err == nil {
		t.Fatal("already-started dispatch with no pane must fail")
	}
}

func TestVerifyConvoyRelation_RequiresMatchingConvoy(t *testing.T) {
	d := &DogDispatchInfo{requireConvoy: true, expectedConvoy: "hq-cv-1"}
	if err := d.verifyConvoyRelation(&beadInfo{Description: "title\n"}, "gt-a"); err == nil {
		t.Fatal("missing convoy must fail when required")
	}
	if err := d.verifyConvoyRelation(&beadInfo{Description: "convoy_id: hq-cv-other\n"}, "gt-a"); err == nil {
		t.Fatal("mismatched convoy must fail")
	}
	if err := d.verifyConvoyRelation(&beadInfo{Description: "convoy_id: hq-cv-1\n"}, "gt-a"); err != nil {
		t.Fatalf("matching convoy error = %v", err)
	}
}
