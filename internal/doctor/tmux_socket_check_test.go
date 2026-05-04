package doctor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/session"
)

// mockSocketLister implements socketSessionLister for testing.
type mockSocketLister struct {
	sessions []string
	listErr  error
	killed   []string
	killErr  error
}

func (m *mockSocketLister) ListSessions() ([]string, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.sessions, nil
}

func (m *mockSocketLister) KillSessionWithProcesses(name string) error {
	m.killed = append(m.killed, name)
	return m.killErr
}

// setupSocketTestRegistry registers the "ga" prefix so session.IsKnownSession
// recognizes "ga-*" names, and returns a cleanup function.
func setupSocketTestRegistry(t *testing.T) {
	t.Helper()
	oldRegistry := session.DefaultRegistry()
	t.Cleanup(func() { session.SetDefaultRegistry(oldRegistry) })
	r := session.NewPrefixRegistry()
	r.Register("ga", "gastown")
	session.SetDefaultRegistry(r)
}

// --- Run() tests ---

func TestSocketSplitBrainCheck_EmptySocket(t *testing.T) {
	setupSocketTestRegistry(t)

	check := NewSocketSplitBrainCheck()
	check.useSocketForTest = true
	check.socketForTest = ""

	ctx := &CheckContext{TownRoot: t.TempDir()}
	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("expected OK, got %v: %s", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, "no split-brain possible") {
		t.Errorf("unexpected message: %s", result.Message)
	}
}

func TestSocketSplitBrainCheck_DefaultSocket(t *testing.T) {
	setupSocketTestRegistry(t)

	check := NewSocketSplitBrainCheck()
	check.useSocketForTest = true
	check.socketForTest = "default"

	ctx := &CheckContext{TownRoot: t.TempDir()}
	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("expected OK, got %v: %s", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, "no split-brain possible") {
		t.Errorf("unexpected message: %s", result.Message)
	}
}

func TestSocketSplitBrainCheck_NoTownServer(t *testing.T) {
	setupSocketTestRegistry(t)

	check := NewSocketSplitBrainCheck()
	check.useSocketForTest = true
	check.socketForTest = "gt-abc123"
	check.townListerForTest = &mockSocketLister{listErr: fmt.Errorf("no server running")}
	check.defaultListerForTest = &mockSocketLister{sessions: []string{}}

	ctx := &CheckContext{TownRoot: t.TempDir()}
	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("expected OK, got %v: %s", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, "server may not be running") {
		t.Errorf("unexpected message: %s", result.Message)
	}
}

func TestSocketSplitBrainCheck_NoDefaultServer(t *testing.T) {
	setupSocketTestRegistry(t)

	check := NewSocketSplitBrainCheck()
	check.useSocketForTest = true
	check.socketForTest = "gt-abc123"
	check.townListerForTest = &mockSocketLister{sessions: []string{"ga-witness"}}
	check.defaultListerForTest = &mockSocketLister{listErr: fmt.Errorf("no server running")}

	ctx := &CheckContext{TownRoot: t.TempDir()}
	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("expected OK, got %v: %s", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, "No default socket server") {
		t.Errorf("unexpected message: %s", result.Message)
	}
}

func TestSocketSplitBrainCheck_NoDuplicates(t *testing.T) {
	setupSocketTestRegistry(t)

	check := NewSocketSplitBrainCheck()
	check.useSocketForTest = true
	check.socketForTest = "gt-abc123"
	check.townListerForTest = &mockSocketLister{sessions: []string{"ga-witness"}}
	check.defaultListerForTest = &mockSocketLister{sessions: []string{"personal-stuff"}}

	ctx := &CheckContext{TownRoot: t.TempDir()}
	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("expected OK, got %v: %s", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, "No split-brain") {
		t.Errorf("unexpected message: %s", result.Message)
	}
}

func TestSocketSplitBrainCheck_DetectsDuplicates(t *testing.T) {
	setupSocketTestRegistry(t)

	check := NewSocketSplitBrainCheck()
	check.useSocketForTest = true
	check.socketForTest = "gt-abc123"
	check.townListerForTest = &mockSocketLister{sessions: []string{"ga-witness"}}
	check.defaultListerForTest = &mockSocketLister{sessions: []string{"ga-witness"}}

	ctx := &CheckContext{TownRoot: t.TempDir()}
	result := check.Run(ctx)

	if result.Status != StatusError {
		t.Fatalf("expected Error, got %v: %s", result.Status, result.Message)
	}
	if len(result.Details) == 0 {
		t.Fatal("expected details to be non-empty")
	}
	found := false
	for _, d := range result.Details {
		if strings.Contains(d, "DUPLICATE") && strings.Contains(d, "ga-witness") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected DUPLICATE detail for ga-witness, got: %v", result.Details)
	}
}

func TestSocketSplitBrainCheck_DetectsOrphans(t *testing.T) {
	setupSocketTestRegistry(t)

	check := NewSocketSplitBrainCheck()
	check.useSocketForTest = true
	check.socketForTest = "gt-abc123"
	check.townListerForTest = &mockSocketLister{sessions: []string{}}
	check.defaultListerForTest = &mockSocketLister{sessions: []string{"ga-refinery"}}

	ctx := &CheckContext{TownRoot: t.TempDir()}
	result := check.Run(ctx)

	if result.Status != StatusError {
		t.Fatalf("expected Error, got %v: %s", result.Status, result.Message)
	}
	found := false
	for _, d := range result.Details {
		if strings.Contains(d, "ORPHAN") && strings.Contains(d, "ga-refinery") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected ORPHAN detail for ga-refinery, got: %v", result.Details)
	}
}

func TestSocketSplitBrainCheck_MixedWithNonGastown(t *testing.T) {
	setupSocketTestRegistry(t)

	check := NewSocketSplitBrainCheck()
	check.useSocketForTest = true
	check.socketForTest = "gt-abc123"
	check.townListerForTest = &mockSocketLister{sessions: []string{}}
	check.defaultListerForTest = &mockSocketLister{sessions: []string{"ga-witness", "personal-stuff"}}

	ctx := &CheckContext{TownRoot: t.TempDir()}
	result := check.Run(ctx)

	if result.Status != StatusError {
		t.Fatalf("expected Error, got %v: %s", result.Status, result.Message)
	}

	// ga-witness should be flagged as orphan
	hasGaWitness := false
	for _, d := range result.Details {
		if strings.Contains(d, "ga-witness") {
			hasGaWitness = true
		}
		if strings.Contains(d, "personal-stuff") {
			t.Errorf("personal-stuff should be ignored, but appeared in details: %v", result.Details)
		}
	}
	if !hasGaWitness {
		t.Errorf("expected ga-witness in details, got: %v", result.Details)
	}

	// Only 1 stale session (personal-stuff is ignored)
	if len(check.staleSessions) != 1 {
		t.Errorf("expected 1 stale session, got %d: %v", len(check.staleSessions), check.staleSessions)
	}
}

// --- Fix() tests ---

func TestSocketSplitBrainCheck_Fix_NoStale(t *testing.T) {
	check := NewSocketSplitBrainCheck()
	mock := &mockSocketLister{}
	check.defaultListerForTest = mock

	ctx := &CheckContext{TownRoot: t.TempDir()}
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix() returned error: %v", err)
	}
	if len(mock.killed) != 0 {
		t.Errorf("expected no kills, got: %v", mock.killed)
	}
}

func TestSocketSplitBrainCheck_Fix_KillsStale(t *testing.T) {
	check := NewSocketSplitBrainCheck()
	check.staleSessions = []string{"ga-refinery", "ga-witness"}

	mock := &mockSocketLister{}
	check.defaultListerForTest = mock

	ctx := &CheckContext{TownRoot: t.TempDir()}
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix() returned error: %v", err)
	}

	if len(mock.killed) != 2 {
		t.Fatalf("expected 2 kills, got %d: %v", len(mock.killed), mock.killed)
	}
	// staleSessions is sorted, so kills should be in order
	if mock.killed[0] != "ga-refinery" || mock.killed[1] != "ga-witness" {
		t.Errorf("unexpected kill order: %v", mock.killed)
	}
}
