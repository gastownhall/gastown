package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestStaleDoltPortCheck_ConsistentPorts verifies the check passes when all ports are consistent.
func TestStaleDoltPortCheck_ConsistentPorts(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .dolt-data/config.yaml with port 3307
	doltDataDir := filepath.Join(tmpDir, ".dolt-data")
	if err := os.MkdirAll(doltDataDir, 0755); err != nil {
		t.Fatal(err)
	}
	configYaml := `log_level: warning
listener:
  port: 3307
`
	if err := os.WriteFile(filepath.Join(doltDataDir, "config.yaml"), []byte(configYaml), 0644); err != nil {
		t.Fatal(err)
	}

	// Create town .beads/metadata.json with consistent port
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	metadata := map[string]interface{}{
		"dolt_mode":        "server",
		"dolt_server_host": "127.0.0.1",
		"dolt_server_port": 3307,
		"dolt_database":    "hq",
	}
	metadataBytes, _ := json.MarshalIndent(metadata, "", "  ")
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), metadataBytes, 0644); err != nil {
		t.Fatal(err)
	}

	// Create dolt-server.port file with consistent port
	if err := os.WriteFile(filepath.Join(beadsDir, "dolt-server.port"), []byte("3307"), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewStaleDoltPortCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("expected StatusOK for consistent ports, got %v: %s", result.Status, result.Message)
	}
}

// TestStaleDoltPortCheck_InconsistentMetadata verifies the check detects wrong port in metadata.json.
func TestStaleDoltPortCheck_InconsistentMetadata(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .dolt-data/config.yaml with port 3307
	doltDataDir := filepath.Join(tmpDir, ".dolt-data")
	if err := os.MkdirAll(doltDataDir, 0755); err != nil {
		t.Fatal(err)
	}
	configYaml := `log_level: warning
listener:
  port: 3307
`
	if err := os.WriteFile(filepath.Join(doltDataDir, "config.yaml"), []byte(configYaml), 0644); err != nil {
		t.Fatal(err)
	}

	// Create town .beads/metadata.json with WRONG port (3306 instead of 3307)
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	metadata := map[string]interface{}{
		"dolt_mode":        "server",
		"dolt_server_host": "127.0.0.1",
		"dolt_server_port": 3306, // Wrong port!
		"dolt_database":    "hq",
	}
	metadataBytes, _ := json.MarshalIndent(metadata, "", "  ")
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), metadataBytes, 0644); err != nil {
		t.Fatal(err)
	}

	check := NewStaleDoltPortCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning for inconsistent port, got %v: %s", result.Status, result.Message)
	}

	// Should mention the wrong port in details
	if len(result.Details) == 0 {
		t.Error("expected details to contain port mismatch info")
	}

	t.Logf("Result: status=%v, message=%s, details=%v", result.Status, result.Message, result.Details)
}

// TestStaleDoltPortCheck_FixUpdatesMetadata verifies that Fix() updates metadata.json with correct port.
func TestStaleDoltPortCheck_FixUpdatesMetadata(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .dolt-data/config.yaml with port 3307
	doltDataDir := filepath.Join(tmpDir, ".dolt-data")
	if err := os.MkdirAll(doltDataDir, 0755); err != nil {
		t.Fatal(err)
	}
	configYaml := `log_level: warning
listener:
  port: 3307
`
	if err := os.WriteFile(filepath.Join(doltDataDir, "config.yaml"), []byte(configYaml), 0644); err != nil {
		t.Fatal(err)
	}

	// Create town .beads/metadata.json with WRONG port
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	metadata := map[string]interface{}{
		"dolt_mode":        "server",
		"dolt_server_host": "127.0.0.1",
		"dolt_server_port": 3306, // Wrong port!
		"dolt_database":    "hq",
	}
	metadataBytes, _ := json.MarshalIndent(metadata, "", "  ")
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), metadataBytes, 0644); err != nil {
		t.Fatal(err)
	}

	check := NewStaleDoltPortCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	// Run check to detect the issue
	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning before fix, got %v", result.Status)
	}

	// Run fix
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix() failed: %v", err)
	}

	// Verify metadata.json was updated
	updatedBytes, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}

	var updatedMetadata map[string]interface{}
	if err := json.Unmarshal(updatedBytes, &updatedMetadata); err != nil {
		t.Fatal(err)
	}

	port := int(updatedMetadata["dolt_server_port"].(float64))
	if port != 3307 {
		t.Errorf("expected port 3307 after fix, got %d", port)
	}

	// Run check again - should pass now
	result = check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK after fix, got %v: %s", result.Status, result.Message)
	}
}

func TestStaleDoltPortCheck_PreservesExternalLoopbackEndpoint(t *testing.T) {
	// Given
	townRoot := t.TempDir()
	doltDataDir := filepath.Join(townRoot, ".dolt-data")
	if err := os.MkdirAll(doltDataDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(doltDataDir, "config.yaml"), []byte("listener:\n  port: 3307\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "daemon.json"), []byte(`{"env":{"GT_DOLT_EXTERNAL":"true","GT_DOLT_PORT":"3308"}}`), 0644); err != nil {
		t.Fatal(err)
	}
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	metadata := map[string]interface{}{
		"dolt_mode":        "server",
		"dolt_server_host": "127.0.0.1",
		"dolt_server_port": 3308,
		"dolt_database":    "hq",
	}
	metadataBytes, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), metadataBytes, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "dolt-server.port"), []byte("3308"), 0644); err != nil {
		t.Fatal(err)
	}

	// When
	result := NewStaleDoltPortCheck().Run(&CheckContext{TownRoot: townRoot})

	// Then
	if result.Status != StatusOK {
		t.Fatalf("external loopback endpoint was treated as stale: %s", result.Message)
	}
}
