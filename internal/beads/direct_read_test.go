package beads

import (
	"context"
	"errors"
	"testing"

	beadsdk "github.com/steveyegge/beads"
)

// withDirectRead sets GT_BD_DIRECT_READ for the duration of a test and resets
// the process-wide read-store cache before and after so tests don't leak state.
func withDirectRead(t *testing.T, value string) {
	t.Helper()
	ResetReadStoreCacheForTest()
	t.Setenv(directReadEnvVar, value)
	t.Cleanup(ResetReadStoreCacheForTest)
}

func TestDirectReadEnabled(t *testing.T) {
	cases := map[string]bool{
		"":      false,
		"0":     false,
		"false": false,
		"off":   false,
		"no":    false,
		"1":     true,
		"true":  true,
		"TRUE":  true,
		"On":    true,
		"yes":   true,
	}
	for val, want := range cases {
		t.Setenv(directReadEnvVar, val)
		if got := DirectReadEnabled(); got != want {
			t.Errorf("DirectReadEnabled() with %q = %v, want %v", val, got, want)
		}
	}
}

func TestReadStoreHonorsExplicitStoreRegardlessOfEnv(t *testing.T) {
	// An explicitly-set store must always be returned, even when direct reads
	// are disabled and the instance is isolated — this preserves daemon/tracking
	// behavior unchanged.
	t.Setenv(directReadEnvVar, "0")
	store := newMockStorage()
	b := &Beads{workDir: "/tmp/test", store: store, isolated: true}
	if got := b.readStore(); got != beadsdk.Storage(store) {
		t.Fatalf("readStore() = %v, want explicit store", got)
	}
}

func TestReadStoreNilWhenDisabled(t *testing.T) {
	withDirectRead(t, "0")
	b := &Beads{beadsDir: t.TempDir()}
	if got := b.readStore(); got != nil {
		t.Fatalf("readStore() = %v, want nil when disabled", got)
	}
}

func TestReadStoreNilWhenIsolated(t *testing.T) {
	withDirectRead(t, "1")
	b := &Beads{beadsDir: t.TempDir(), isolated: true}
	if got := b.readStore(); got != nil {
		t.Fatalf("readStore() = %v, want nil for isolated instance", got)
	}
}

func TestReadStoreOpensAndCaches(t *testing.T) {
	withDirectRead(t, "1")

	mock := newMockStorage()
	calls := 0
	orig := openReadStoreFunc
	openReadStoreFunc = func(_ context.Context, _ string) (beadsdk.Storage, error) {
		calls++
		return mock, nil
	}
	t.Cleanup(func() { openReadStoreFunc = orig })

	dir := t.TempDir()
	b := &Beads{beadsDir: dir}

	if got := b.readStore(); got != beadsdk.Storage(mock) {
		t.Fatalf("readStore() = %v, want mock", got)
	}
	// Second call (even from a different Beads instance with the same dir) must
	// reuse the cached store, not re-open.
	b2 := &Beads{beadsDir: dir}
	if got := b2.readStore(); got != beadsdk.Storage(mock) {
		t.Fatalf("readStore() second call = %v, want cached mock", got)
	}
	if calls != 1 {
		t.Fatalf("openReadStoreFunc called %d times, want 1 (cached)", calls)
	}
}

func TestReadStoreNegativeCachesOpenFailure(t *testing.T) {
	withDirectRead(t, "1")

	calls := 0
	orig := openReadStoreFunc
	openReadStoreFunc = func(_ context.Context, _ string) (beadsdk.Storage, error) {
		calls++
		return nil, errors.New("dolt unreachable")
	}
	t.Cleanup(func() { openReadStoreFunc = orig })

	b := &Beads{beadsDir: t.TempDir()}
	if got := b.readStore(); got != nil {
		t.Fatalf("readStore() = %v, want nil on open failure", got)
	}
	if got := b.readStore(); got != nil {
		t.Fatalf("readStore() second call = %v, want nil (negative cached)", got)
	}
	if calls != 1 {
		t.Fatalf("openReadStoreFunc called %d times, want 1 (failure negative-cached)", calls)
	}
}

// TestListUsesReadStoreWhenEnabled verifies the end-to-end wiring: with direct
// reads enabled and no explicit store, List() routes through the cached read
// store instead of shelling out to bd.
func TestListUsesReadStoreWhenEnabled(t *testing.T) {
	withDirectRead(t, "1")

	mock := newMockStorage()
	mock.CreateIssue(context.Background(), &beadsdk.Issue{Title: "alpha"}, "test")
	mock.CreateIssue(context.Background(), &beadsdk.Issue{Title: "beta"}, "test")

	orig := openReadStoreFunc
	openReadStoreFunc = func(_ context.Context, _ string) (beadsdk.Storage, error) {
		return mock, nil
	}
	t.Cleanup(func() { openReadStoreFunc = orig })

	b := &Beads{beadsDir: t.TempDir()}
	issues, err := b.List(ListOptions{Priority: -1})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("List returned %d issues, want 2 (via read store)", len(issues))
	}
}

// TestEphemeralListDoesNotUseReadStore verifies ephemeral (wisp) lists never use
// the read store path — wisps live in a separate table handled by listEphemeral.
func TestEphemeralListDoesNotUseReadStore(t *testing.T) {
	withDirectRead(t, "1")

	called := false
	orig := openReadStoreFunc
	openReadStoreFunc = func(_ context.Context, _ string) (beadsdk.Storage, error) {
		called = true
		return newMockStorage(), nil
	}
	t.Cleanup(func() { openReadStoreFunc = orig })

	// isolated avoids touching a real bd binary; listEphemeral will fail/return
	// via the subprocess path, which is fine — we only assert the read store was
	// never opened for an ephemeral list.
	b := &Beads{beadsDir: t.TempDir(), isolated: true}
	_, _ = b.List(ListOptions{Ephemeral: true, Priority: -1})
	if called {
		t.Fatal("ephemeral List opened a read store; it must use the wisp path")
	}
}
