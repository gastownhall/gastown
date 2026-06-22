package daemon

import (
	"context"
	"fmt"
	"io"
	"log"
	"sync"
	"testing"

	beadsdk "github.com/steveyegge/beads"
	"github.com/steveyegge/gastown/internal/beads"
)

// recordingStore is a beadsdk.Storage that records CloseIssue calls and nothing
// else. Embedding the interface means only the overridden method is implemented;
// the dog close path calls only CloseIssue, so no other method is exercised.
type recordingStore struct {
	beadsdk.Storage
	mu       sync.Mutex
	closed   []closeCall
	closeErr error
}

type closeCall struct {
	id     string
	reason string
	actor  string
}

func (r *recordingStore) CloseIssue(_ context.Context, id, reason, actor, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closeErr != nil {
		return r.closeErr
	}
	r.closed = append(r.closed, closeCall{id: id, reason: reason, actor: actor})
	return nil
}

func (r *recordingStore) calls() []closeCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]closeCall(nil), r.closed...)
}

func discardLogger() *log.Logger { return log.New(io.Discard, "", 0) }

// TestDogClose_RoutesThroughStore_NoSubprocess is the gt-ye21/#4292 regression:
// when a dogMol has an in-process store, closing wisps must go through the store
// and spawn ZERO bd subprocesses. Each bd subprocess opens its own short-lived
// Dolt connection pool, so spawn count is a direct proxy for the connection
// amplification this fix removes. The pre-fix path spawned one `bd close` (up to
// dogCloseMaxAttempts on transient Dolt error) per wisp.
func TestDogClose_RoutesThroughStore_NoSubprocess(t *testing.T) {
	store := &recordingStore{}
	dm := &dogMol{
		rootID:  "gt-wisp-root",
		stepIDs: map[string]string{"scan": "gt-wisp-scan"},
		store:   store,
		logger:  discardLogger(),
	}

	beads.ResetBDSpawnCount()

	dm.closeStep("scan") // -> closeWisp("gt-wisp-scan")
	if err := dm.closeWisp("gt-wisp-root"); err != nil {
		t.Fatalf("closeWisp root: %v", err)
	}

	if got := beads.BDSpawnCount(); got != 0 {
		t.Fatalf("close path spawned %d bd subprocess(es); want 0 (store should be used)", got)
	}

	calls := store.calls()
	if len(calls) != 2 {
		t.Fatalf("store recorded %d closes; want 2 (step + root): %+v", len(calls), calls)
	}
	got := map[string]bool{calls[0].id: true, calls[1].id: true}
	for _, want := range []string{"gt-wisp-scan", "gt-wisp-root"} {
		if !got[want] {
			t.Errorf("expected close of %s through store; got %+v", want, calls)
		}
	}
}

// TestDogFailStep_PassesReasonThroughStore verifies failStep's --reason reaches
// the store close reason, and the close is attributed to dogCloseActor.
func TestDogFailStep_PassesReasonThroughStore(t *testing.T) {
	store := &recordingStore{}
	dm := &dogMol{
		rootID:  "gt-wisp-root",
		stepIDs: map[string]string{"scan": "gt-wisp-scan"},
		store:   store,
		logger:  discardLogger(),
	}

	dm.failStep("scan", "boom")

	calls := store.calls()
	if len(calls) != 1 {
		t.Fatalf("want 1 close, got %d: %+v", len(calls), calls)
	}
	if calls[0].reason != "boom" {
		t.Errorf("close reason = %q; want %q", calls[0].reason, "boom")
	}
	if calls[0].actor != dogCloseActor {
		t.Errorf("close actor = %q; want %q", calls[0].actor, dogCloseActor)
	}
}

func TestCloseReasonFromArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"none", nil, ""},
		{"force only", []string{"--force"}, ""},
		{"reason", []string{"--reason", "stale"}, "stale"},
		{"reason after force", []string{"--force", "--reason", "x"}, "x"},
		{"dangling reason", []string{"--reason"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := closeReasonFromArgs(tc.args); got != tc.want {
				t.Errorf("closeReasonFromArgs(%v) = %q; want %q", tc.args, got, tc.want)
			}
		})
	}
}

func TestIsBenignCloseErr(t *testing.T) {
	if !isBenignCloseErr(fmt.Errorf("issue not found: gt-wisp-x")) {
		t.Error("not-found should be benign")
	}
	if isBenignCloseErr(fmt.Errorf("connection refused")) {
		t.Error("connection error should not be benign")
	}
	if isBenignCloseErr(nil) {
		t.Error("nil should not be benign")
	}
}
