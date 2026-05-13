package deacon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHeartbeatFile(t *testing.T) {
	townRoot := "/tmp/test-town"
	expected := filepath.Join(townRoot, "deacon", "heartbeat.json")

	result := HeartbeatFile(townRoot)
	if result != expected {
		t.Errorf("HeartbeatFile() = %q, want %q", result, expected)
	}
}

func TestWriteReadHeartbeat(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "deacon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	hb := &Heartbeat{
		Timestamp:       time.Now().UTC(),
		Cycle:           42,
		LastAction:      "health check",
		HealthyAgents:   3,
		UnhealthyAgents: 1,
	}

	// Write heartbeat
	if err := WriteHeartbeat(tmpDir, hb); err != nil {
		t.Fatalf("WriteHeartbeat error: %v", err)
	}

	// Verify file exists
	hbFile := HeartbeatFile(tmpDir)
	if _, err := os.Stat(hbFile); err != nil {
		t.Errorf("heartbeat file not created: %v", err)
	}

	// Read heartbeat
	loaded := ReadHeartbeat(tmpDir)
	if loaded == nil {
		t.Fatal("ReadHeartbeat returned nil")
	}

	if loaded.Cycle != 42 {
		t.Errorf("Cycle = %d, want 42", loaded.Cycle)
	}
	if loaded.LastAction != "health check" {
		t.Errorf("LastAction = %q, want 'health check'", loaded.LastAction)
	}
	if loaded.HealthyAgents != 3 {
		t.Errorf("HealthyAgents = %d, want 3", loaded.HealthyAgents)
	}
	if loaded.UnhealthyAgents != 1 {
		t.Errorf("UnhealthyAgents = %d, want 1", loaded.UnhealthyAgents)
	}
}

func TestReadHeartbeat_NonExistent(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "deacon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Read from non-existent file
	hb := ReadHeartbeat(tmpDir)
	if hb != nil {
		t.Error("expected nil for non-existent heartbeat")
	}
}

func TestHeartbeat_Age(t *testing.T) {
	// Test nil heartbeat
	var nilHb *Heartbeat
	if nilHb.Age() < 24*time.Hour {
		t.Error("nil heartbeat should have very large age")
	}

	// Test recent heartbeat
	hb := &Heartbeat{
		Timestamp: time.Now().Add(-30 * time.Second),
	}
	if hb.Age() > time.Minute {
		t.Errorf("Age() = %v, expected < 1 minute", hb.Age())
	}
}

func TestHeartbeat_IsFresh(t *testing.T) {
	tests := []struct {
		name     string
		hb       *Heartbeat
		expected bool
	}{
		{
			name:     "nil heartbeat",
			hb:       nil,
			expected: false,
		},
		{
			name: "just now",
			hb: &Heartbeat{
				Timestamp: time.Now(),
			},
			expected: true,
		},
		{
			name: "3 minutes old",
			hb: &Heartbeat{
				Timestamp: time.Now().Add(-3 * time.Minute),
			},
			expected: true, // Fresh is <5 minutes
		},
		{
			name: "6 minutes old",
			hb: &Heartbeat{
				Timestamp: time.Now().Add(-6 * time.Minute),
			},
			expected: false, // Not fresh (>=5 minutes)
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.hb.IsFresh()
			if result != tc.expected {
				t.Errorf("IsFresh() = %v, want %v", result, tc.expected)
			}
		})
	}
}

func TestHeartbeat_IsStale(t *testing.T) {
	tests := []struct {
		name     string
		hb       *Heartbeat
		expected bool
	}{
		{
			name:     "nil heartbeat",
			hb:       nil,
			expected: false,
		},
		{
			name: "3 minutes old",
			hb: &Heartbeat{
				Timestamp: time.Now().Add(-3 * time.Minute),
			},
			expected: false, // Fresh (<5 minutes)
		},
		{
			name: "7 minutes old",
			hb: &Heartbeat{
				Timestamp: time.Now().Add(-7 * time.Minute),
			},
			expected: true, // Stale (5-20 minutes)
		},
		{
			name: "16 minutes old",
			hb: &Heartbeat{
				Timestamp: time.Now().Add(-16 * time.Minute),
			},
			expected: true, // Stale (5-20 minutes)
		},
		{
			name: "21 minutes old",
			hb: &Heartbeat{
				Timestamp: time.Now().Add(-21 * time.Minute),
			},
			expected: false, // Very stale, not stale (>20 minutes)
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.hb.IsStale()
			if result != tc.expected {
				t.Errorf("IsStale() = %v, want %v", result, tc.expected)
			}
		})
	}
}

func TestHeartbeat_IsVeryStale(t *testing.T) {
	tests := []struct {
		name     string
		hb       *Heartbeat
		expected bool
	}{
		{
			name:     "nil heartbeat",
			hb:       nil,
			expected: true,
		},
		{
			name: "3 minutes old",
			hb: &Heartbeat{
				Timestamp: time.Now().Add(-3 * time.Minute),
			},
			expected: false, // Fresh
		},
		{
			name: "10 minutes old",
			hb: &Heartbeat{
				Timestamp: time.Now().Add(-10 * time.Minute),
			},
			expected: false, // Stale but not very stale
		},
		{
			name: "16 minutes old",
			hb: &Heartbeat{
				Timestamp: time.Now().Add(-16 * time.Minute),
			},
			expected: false, // Stale but not very stale (threshold is 20m)
		},
		{
			name: "21 minutes old",
			hb: &Heartbeat{
				Timestamp: time.Now().Add(-21 * time.Minute),
			},
			expected: true, // Very stale (>20 minutes)
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.hb.IsVeryStale()
			if result != tc.expected {
				t.Errorf("IsVeryStale() = %v, want %v", result, tc.expected)
			}
		})
	}
}

func TestTouch(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "deacon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// First touch
	if err := Touch(tmpDir); err != nil {
		t.Fatalf("Touch error: %v", err)
	}

	hb := ReadHeartbeat(tmpDir)
	if hb == nil {
		t.Fatal("expected heartbeat after Touch")
	}
	if hb.Cycle != 1 {
		t.Errorf("first Touch: Cycle = %d, want 1", hb.Cycle)
	}

	// Second touch should increment cycle
	if err := Touch(tmpDir); err != nil {
		t.Fatalf("Touch error: %v", err)
	}

	hb = ReadHeartbeat(tmpDir)
	if hb.Cycle != 2 {
		t.Errorf("second Touch: Cycle = %d, want 2", hb.Cycle)
	}
}

func TestTouchWithAction(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "deacon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	if err := TouchWithAction(tmpDir, "health scan", 5, 2); err != nil {
		t.Fatalf("TouchWithAction error: %v", err)
	}

	hb := ReadHeartbeat(tmpDir)
	if hb == nil {
		t.Fatal("expected heartbeat after TouchWithAction")
	}
	if hb.Cycle != 1 {
		t.Errorf("Cycle = %d, want 1", hb.Cycle)
	}
	if hb.LastAction != "health scan" {
		t.Errorf("LastAction = %q, want 'health scan'", hb.LastAction)
	}
	if hb.HealthyAgents != 5 {
		t.Errorf("HealthyAgents = %d, want 5", hb.HealthyAgents)
	}
	if hb.UnhealthyAgents != 2 {
		t.Errorf("UnhealthyAgents = %d, want 2", hb.UnhealthyAgents)
	}
}

func TestConsumeHeartbeatCreditBootstrapOnce(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "deacon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	if err := ConsumeHeartbeatCredit(tmpDir); err != nil {
		t.Fatalf("first bootstrap consume failed: %v", err)
	}
	state, exists, err := readHeartbeatCredits(tmpDir)
	if err != nil {
		t.Fatalf("read credits: %v", err)
	}
	if !exists {
		t.Fatal("expected credit state to be persisted")
	}
	if !state.BootstrapGranted {
		t.Fatal("expected bootstrap to be marked granted")
	}
	if state.Credits != 0 {
		t.Fatalf("credits after bootstrap consume = %d, want 0", state.Credits)
	}

	if err := ConsumeHeartbeatCredit(tmpDir); err == nil {
		t.Fatal("second consume without patrol credit succeeded, want failure")
	}
}

func TestGrantHeartbeatCreditsCapsAtOnePatrol(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "deacon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	if err := GrantHeartbeatCredits(tmpDir, "hq-wisp-one", HeartbeatCreditsPerPatrol); err != nil {
		t.Fatalf("grant credits: %v", err)
	}
	if err := GrantHeartbeatCredits(tmpDir, "hq-wisp-two", HeartbeatCreditsPerPatrol); err != nil {
		t.Fatalf("second grant credits: %v", err)
	}

	state, _, err := readHeartbeatCredits(tmpDir)
	if err != nil {
		t.Fatalf("read credits: %v", err)
	}
	if state.Credits != HeartbeatCreditsPerPatrol {
		t.Fatalf("credits = %d, want cap %d", state.Credits, HeartbeatCreditsPerPatrol)
	}

	for i := 0; i < HeartbeatCreditsPerPatrol; i++ {
		if err := ConsumeHeartbeatCredit(tmpDir); err != nil {
			t.Fatalf("consume credit %d: %v", i+1, err)
		}
	}
	if err := ConsumeHeartbeatCredit(tmpDir); err == nil {
		t.Fatal("consume after granted credits were exhausted succeeded, want failure")
	}
}

func TestGrantHeartbeatCreditsSamePatrolDoesNotTopUp(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "deacon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	if err := GrantHeartbeatCredits(tmpDir, "hq-wisp-one", HeartbeatCreditsPerPatrol); err != nil {
		t.Fatalf("grant credits: %v", err)
	}
	if err := ConsumeHeartbeatCredit(tmpDir); err != nil {
		t.Fatalf("consume credit: %v", err)
	}
	if err := GrantHeartbeatCredits(tmpDir, "hq-wisp-one", HeartbeatCreditsPerPatrol); err != nil {
		t.Fatalf("repeat grant same patrol: %v", err)
	}

	state, _, err := readHeartbeatCredits(tmpDir)
	if err != nil {
		t.Fatalf("read credits: %v", err)
	}
	if state.Credits != HeartbeatCreditsPerPatrol-1 {
		t.Fatalf("credits = %d, want %d after repeated same-patrol grant", state.Credits, HeartbeatCreditsPerPatrol-1)
	}
}

func TestWriteHeartbeat_CreatesDirectory(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "deacon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Ensure deacon directory doesn't exist
	deaconDir := filepath.Join(tmpDir, "deacon")
	if _, err := os.Stat(deaconDir); !os.IsNotExist(err) {
		t.Fatal("deacon directory should not exist initially")
	}

	// Write heartbeat should create directory
	hb := &Heartbeat{Cycle: 1}
	if err := WriteHeartbeat(tmpDir, hb); err != nil {
		t.Fatalf("WriteHeartbeat error: %v", err)
	}

	// Verify directory was created
	if _, err := os.Stat(deaconDir); err != nil {
		t.Errorf("deacon directory should exist: %v", err)
	}
}

func TestWriteHeartbeat_TouchesLegacyFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "deacon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	hb := &Heartbeat{Cycle: 1}
	if err := WriteHeartbeat(tmpDir, hb); err != nil {
		t.Fatalf("WriteHeartbeat error: %v", err)
	}

	// Legacy .deacon-heartbeat should also exist so shell scripts (stuck-agent-dog)
	// that check this file's mtime get accurate data.
	legacyFile := filepath.Join(tmpDir, "deacon", ".deacon-heartbeat")
	info, err := os.Stat(legacyFile)
	if err != nil {
		t.Errorf(".deacon-heartbeat not created: %v", err)
		return
	}
	if time.Since(info.ModTime()) > time.Minute {
		t.Error(".deacon-heartbeat mtime should be recent")
	}
}

func TestWriteHeartbeat_SetsTimestamp(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "deacon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Write heartbeat without timestamp
	hb := &Heartbeat{Cycle: 1}
	if err := WriteHeartbeat(tmpDir, hb); err != nil {
		t.Fatalf("WriteHeartbeat error: %v", err)
	}

	// Read back and verify timestamp was set
	loaded := ReadHeartbeat(tmpDir)
	if loaded == nil {
		t.Fatal("expected heartbeat")
	}
	if loaded.Timestamp.IsZero() {
		t.Error("expected Timestamp to be set")
	}
	if time.Since(loaded.Timestamp) > time.Minute {
		t.Error("Timestamp should be recent")
	}
}
