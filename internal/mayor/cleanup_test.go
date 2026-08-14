package mayor

import (
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

func TestWriteAndRemoveACPPid(t *testing.T) {
	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")

	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("failed to create mayor dir: %v", err)
	}

	if err := WriteACPPid(tmpDir); err != nil {
		t.Fatalf("WriteACPPid failed: %v", err)
	}

	pidPath := ACPPidFilePath(tmpDir)
	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("failed to read PID file: %v", err)
	}

	expectedPid := strconv.Itoa(os.Getpid())
	if got := string(data); got != expectedPid {
		t.Errorf("PID file content = %q, want %q", got, expectedPid)
	}

	if err := RemoveACPPid(tmpDir); err != nil {
		t.Fatalf("RemoveACPPid failed: %v", err)
	}

	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("PID file should be removed, but exists or error: %v", err)
	}
}

func TestRemoveACPPid_NonExistent(t *testing.T) {
	tmpDir := t.TempDir()

	err := RemoveACPPid(tmpDir)
	if err != nil {
		t.Errorf("RemoveACPPid on non-existent file should return nil, got: %v", err)
	}
}

func TestIsACPActive_NoPidFile(t *testing.T) {
	tmpDir := t.TempDir()

	if IsACPActive(tmpDir) {
		t.Error("IsACPActive should return false when PID file doesn't exist")
	}
}

func TestIsACPActive_InvalidPid(t *testing.T) {
	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("failed to create mayor dir: %v", err)
	}

	pidPath := ACPPidFilePath(tmpDir)
	if err := os.WriteFile(pidPath, []byte("invalid"), 0644); err != nil {
		t.Fatalf("failed to write PID file: %v", err)
	}

	if IsACPActive(tmpDir) {
		t.Error("IsACPActive should return false for invalid PID")
	}
}

func TestIsACPActive_DeadProcess(t *testing.T) {
	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("failed to create mayor dir: %v", err)
	}

	pidPath := ACPPidFilePath(tmpDir)
	deadPid := 999999
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(deadPid)), 0644); err != nil {
		t.Fatalf("failed to write PID file: %v", err)
	}

	// IsACPActive should return false for dead process but NOT remove the file
	// (removing stale PID files is a side effect that can trigger unexpected shutdowns)
	if IsACPActive(tmpDir) {
		t.Error("IsACPActive should return false for non-existent process")
	}

	// PID file should still exist - IsACPActive should NOT have side effects
	if _, err := os.Stat(pidPath); os.IsNotExist(err) {
		t.Error("IsACPActive should NOT remove stale PID files - that's CleanupStaleACP's job")
	}
}

func TestIsACPActive_CurrentProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ACP process detection not yet reliable on Windows")
	}

	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("failed to create mayor dir: %v", err)
	}

	if err := WriteACPPid(tmpDir); err != nil {
		t.Fatalf("WriteACPPid failed: %v", err)
	}

	if !IsACPActive(tmpDir) {
		t.Error("IsACPActive should return true for current process")
	}

	_ = RemoveACPPid(tmpDir)
}

func TestCleanupVetoChecker_ShouldVetoCleanup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ACP process detection not yet reliable on Windows")
	}

	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("failed to create mayor dir: %v", err)
	}

	checker := NewCleanupVetoChecker(tmpDir)

	vetoed, reason := checker.ShouldVetoCleanup()
	if vetoed {
		t.Errorf("ShouldVetoCleanup should return false when no ACP active, got: %s", reason)
	}

	if err := WriteACPPid(tmpDir); err != nil {
		t.Fatalf("WriteACPPid failed: %v", err)
	}

	vetoed, reason = checker.ShouldVetoCleanup()
	if !vetoed {
		t.Error("ShouldVetoCleanup should return true when ACP is active")
	}
	if reason == "" {
		t.Error("ShouldVetoCleanup should provide a reason when vetoing")
	}

	_ = RemoveACPPid(tmpDir)
}

func TestCleanupVetoChecker_VetoIfActive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ACP process detection not yet reliable on Windows")
	}

	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("failed to create mayor dir: %v", err)
	}

	checker := NewCleanupVetoChecker(tmpDir)

	if err := checker.VetoIfActive(); err != nil {
		t.Errorf("VetoIfActive should return nil when no ACP active, got: %v", err)
	}

	if err := WriteACPPid(tmpDir); err != nil {
		t.Fatalf("WriteACPPid failed: %v", err)
	}

	err := checker.VetoIfActive()
	if err == nil {
		t.Error("VetoIfActive should return error when ACP is active")
	}
	if !os.IsNotExist(err) && !isCleanupVetoed(err) {
		t.Errorf("VetoIfActive should return ErrCleanupVetoed, got: %v", err)
	}

	_ = RemoveACPPid(tmpDir)
}

func TestCleanupVetoChecker_GetACPExpiry(t *testing.T) {
	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("failed to create mayor dir: %v", err)
	}

	checker := NewCleanupVetoChecker(tmpDir)

	_, ok := checker.GetACPExpiry()
	if ok {
		t.Error("GetACPExpiry should return false when no PID file exists")
	}

	if err := WriteACPPid(tmpDir); err != nil {
		t.Fatalf("WriteACPPid failed: %v", err)
	}

	expiry, ok := checker.GetACPExpiry()
	if !ok {
		t.Error("GetACPExpiry should return true when PID file exists")
	}
	if expiry.IsZero() {
		t.Error("GetACPExpiry should return non-zero time")
	}

	_ = RemoveACPPid(tmpDir)
}

func isCleanupVetoed(err error) bool {
	return err != nil && err.Error() != "" && containsSubstring(err.Error(), "cleanup vetoed")
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestRemoveACPPid_RemovesStalePid(t *testing.T) {
	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("failed to create mayor dir: %v", err)
	}

	// Getpid()-10000 can be live (or collapse to PID 1). Probe for a PID
	// that is actually unused — 999999 can still be allocated on Linux.
	stalePid := unusedACPPid(t)
	pidPath := ACPPidFilePath(tmpDir)
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(stalePid)), 0644); err != nil {
		t.Fatalf("failed to write stale PID file: %v", err)
	}

	// IsACPActive should return false for stale PID but NOT remove the file
	// (removing stale PID files is a side effect that can trigger unexpected shutdowns)
	if IsACPActive(tmpDir) {
		t.Error("IsACPActive should return false for stale PID")
	}

	// PID file should still exist after IsACPActive check
	if _, err := os.Stat(pidPath); os.IsNotExist(err) {
		t.Error("IsACPActive should NOT remove stale PID files - that's CleanupStaleACP's job")
	}

	// CleanupStaleACP should remove the stale PID file
	checker := NewCleanupVetoChecker(tmpDir)
	if err := checker.CleanupStaleACP(); err != nil {
		t.Fatalf("CleanupStaleACP failed: %v", err)
	}

	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("CleanupStaleACP should have removed the stale PID file")
	}
}

// unusedACPPid returns a PID that is not currently alive. Getpid()-N can still
// refer to a live process on a busy CI runner, which makes "stale PID" tests
// fail closed.
func unusedACPPid(t *testing.T) int {
	t.Helper()
	for pid := math.MaxInt32; pid > 1; pid-- {
		if !acpProcessAlive(pid) {
			return pid
		}
	}
	t.Fatal("could not find an unused PID")
	return 0
}
