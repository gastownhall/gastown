package witness

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
)

func TestRecordBeadRespawn_Increments(t *testing.T) {
	tmpDir := t.TempDir()
	// Create the witness subdirectory so the state file path is valid.
	if err := os.MkdirAll(filepath.Join(tmpDir, "witness"), 0755); err != nil {
		t.Fatal(err)
	}

	count := RecordBeadRespawn(tmpDir, "bead-1")
	if count != 1 {
		t.Errorf("first RecordBeadRespawn = %d, want 1", count)
	}

	count = RecordBeadRespawn(tmpDir, "bead-1")
	if count != 2 {
		t.Errorf("second RecordBeadRespawn = %d, want 2", count)
	}
}

func TestShouldBlockRespawn_Threshold(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "witness"), 0755); err != nil {
		t.Fatal(err)
	}

	// Below threshold.
	for i := 0; i < config.DefaultWitnessMaxBeadRespawns-1; i++ {
		RecordBeadRespawn(tmpDir, "bead-2")
	}
	if ShouldBlockRespawn(tmpDir, "bead-2") {
		t.Error("ShouldBlockRespawn = true before reaching threshold")
	}

	// At threshold.
	RecordBeadRespawn(tmpDir, "bead-2")
	if !ShouldBlockRespawn(tmpDir, "bead-2") {
		t.Error("ShouldBlockRespawn = false at threshold")
	}
}

func TestResetBeadRespawnCount(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "witness"), 0755); err != nil {
		t.Fatal(err)
	}

	RecordBeadRespawn(tmpDir, "bead-3")
	RecordBeadRespawn(tmpDir, "bead-3")

	if err := ResetBeadRespawnCount(tmpDir, "bead-3"); err != nil {
		t.Fatalf("ResetBeadRespawnCount error: %v", err)
	}

	if ShouldBlockRespawn(tmpDir, "bead-3") {
		t.Error("ShouldBlockRespawn = true after reset")
	}

	// Re-increment should start from 1.
	count := RecordBeadRespawn(tmpDir, "bead-3")
	if count != 1 {
		t.Errorf("RecordBeadRespawn after reset = %d, want 1", count)
	}
}

func TestRecordBeadRespawn_ConcurrentSafe(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "witness"), 0755); err != nil {
		t.Fatal(err)
	}

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			RecordBeadRespawn(tmpDir, "bead-race")
		}()
	}
	wg.Wait()

	// After all goroutines, the count must equal the number of increments.
	state := loadBeadRespawnState(tmpDir)
	rec, ok := state.Beads["bead-race"]
	if !ok {
		t.Fatal("bead-race record not found")
	}
	if rec.Count != goroutines {
		t.Errorf("concurrent count = %d, want %d", rec.Count, goroutines)
	}
}

func TestShouldBlockRespawn_UnknownBead(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "witness"), 0755); err != nil {
		t.Fatal(err)
	}

	if ShouldBlockRespawn(tmpDir, "nonexistent") {
		t.Error("ShouldBlockRespawn = true for unknown bead")
	}
}

// A dispatch that is charged but never established must be refundable, so that
// failed preflight/allocation/startup does not burn the circuit breaker.
func TestRefundBeadRespawn_UnchargesUnestablishedAttempt(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "witness"), 0755); err != nil {
		t.Fatal(err)
	}

	RecordBeadRespawn(tmpDir, "bead-refund")
	RecordBeadRespawn(tmpDir, "bead-refund")

	if got := RefundBeadRespawn(tmpDir, "bead-refund"); got != 1 {
		t.Errorf("RefundBeadRespawn = %d, want 1", got)
	}
	if got := RecordBeadRespawn(tmpDir, "bead-refund"); got != 2 {
		t.Errorf("count after refund+record = %d, want 2", got)
	}
}

// Refunding every charge must clear the breaker completely, not leave a
// zero-count record that ShouldBlockRespawn or a future migration could
// misinterpret.
func TestRefundBeadRespawn_FloorsAtZeroAndClearsBlock(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "witness"), 0755); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < config.DefaultWitnessMaxBeadRespawns; i++ {
		RecordBeadRespawn(tmpDir, "bead-floor")
	}
	if !ShouldBlockRespawn(tmpDir, "bead-floor") {
		t.Fatal("precondition: bead should be blocked at threshold")
	}

	for i := 0; i < config.DefaultWitnessMaxBeadRespawns; i++ {
		RefundBeadRespawn(tmpDir, "bead-floor")
	}
	if ShouldBlockRespawn(tmpDir, "bead-floor") {
		t.Error("bead still blocked after refunding every charge")
	}

	// Extra refunds must not drive the count negative.
	if got := RefundBeadRespawn(tmpDir, "bead-floor"); got != 0 {
		t.Errorf("RefundBeadRespawn below zero = %d, want 0", got)
	}
	if got := RecordBeadRespawn(tmpDir, "bead-floor"); got != 1 {
		t.Errorf("count after over-refund then record = %d, want 1", got)
	}
}

// Refunding a bead that was never charged is a no-op, not a panic or a
// negative count.
func TestRefundBeadRespawn_UnknownBeadIsNoop(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "witness"), 0755); err != nil {
		t.Fatal(err)
	}

	if got := RefundBeadRespawn(tmpDir, "never-charged"); got != 0 {
		t.Errorf("RefundBeadRespawn on unknown bead = %d, want 0", got)
	}
}
