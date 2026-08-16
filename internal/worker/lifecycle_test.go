package worker

import (
	"context"
	"os"
	"testing"
	"time"
)

type lifecycleTestTmux struct{}

func (*lifecycleTestTmux) HasSession(string) (bool, error)                 { return true, nil }
func (*lifecycleTestTmux) NudgeSession(string, string) error               { return nil }
func (*lifecycleTestTmux) WaitForRuntimeReady(string, time.Duration) error { return nil }
func (*lifecycleTestTmux) WaitForIdle(string, time.Duration) error         { return nil }
func (*lifecycleTestTmux) IsAgentAlive(string) bool                        { return true }
func (*lifecycleTestTmux) KillSessionWithProcesses(string) error           { return nil }
func (*lifecycleTestTmux) CheckSessionHealth(string, time.Duration) string { return "healthy" }

func TestMarkSessionStoppedAllowsReplacementRun(t *testing.T) {
	townRoot, err := os.MkdirTemp("/tmp", "gt-worker-lifecycle-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(townRoot) })

	w, err := Listen(townRoot, &lifecycleTestTmux{})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, err := w.StartRun(ctx, StartSpec{SessionID: "hq-mayor", Role: "mayor"})
	if err != nil {
		t.Fatalf("first StartRun: %v", err)
	}

	if err := MarkSessionStopped(townRoot, "hq-mayor"); err != nil {
		t.Fatalf("MarkSessionStopped: %v", err)
	}
	stopped, err := ReadRun(townRoot, first.RunID)
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	if stopped.State != StateStopped || stopped.StoppedAt.IsZero() {
		t.Fatalf("stopped run = %+v, want stopped state and timestamp", stopped)
	}

	if _, err := w.StartRun(ctx, StartSpec{SessionID: "hq-mayor", Role: "mayor"}); err != nil {
		t.Fatalf("replacement StartRun: %v", err)
	}
}
