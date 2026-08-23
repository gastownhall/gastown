package doctor

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/doltserver"
)

// setupDoltDB creates a fake Dolt database directory under .dolt-data/.
func setupDoltDB(t *testing.T, townRoot, dbName string) {
	t.Helper()
	nomsDir := filepath.Join(townRoot, ".dolt-data", dbName, ".dolt", "noms")
	if err := os.MkdirAll(nomsDir, 0755); err != nil {
		t.Fatalf("creating noms dir for %s: %v", dbName, err)
	}
	if err := os.WriteFile(filepath.Join(nomsDir, "manifest"), []byte("test"), 0644); err != nil {
		t.Fatalf("writing manifest for %s: %v", dbName, err)
	}
}

// setupRigMetadata creates a .beads/metadata.json for a rig with Dolt server config.
func setupRigMetadata(t *testing.T, townRoot, rigName, doltDatabase string) {
	t.Helper()
	var beadsDir string
	if rigName == "hq" {
		beadsDir = filepath.Join(townRoot, ".beads")
	} else {
		beadsDir = filepath.Join(townRoot, rigName, "mayor", "rig", ".beads")
	}
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("creating beads dir for %s: %v", rigName, err)
	}
	meta := map[string]interface{}{
		"backend":       "dolt",
		"dolt_mode":     "server",
		"dolt_database": doltDatabase,
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshaling metadata for %s: %v", rigName, err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0644); err != nil {
		t.Fatalf("writing metadata for %s: %v", rigName, err)
	}
}

// setupServerMetadata creates a .beads/metadata.json with optional host/port fields.
func setupServerMetadata(t *testing.T, beadsDir, host string, port int) {
	t.Helper()
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("creating beads dir: %v", err)
	}
	meta := map[string]interface{}{
		"backend":       "dolt",
		"dolt_mode":     "server",
		"dolt_database": "test",
	}
	if host != "" {
		meta["dolt_server_host"] = host
	}
	if port != 0 {
		meta["dolt_server_port"] = port
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshaling metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0644); err != nil {
		t.Fatalf("writing metadata: %v", err)
	}
}

// setupRigsJSON creates a minimal mayor/rigs.json for tests.
func setupRigsJSON(t *testing.T, townRoot string, rigNames []string) {
	t.Helper()
	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}
	rigs := "{"
	for i, name := range rigNames {
		if i > 0 {
			rigs += ","
		}
		rigs += `"` + name + `":{"git_url":"https://example.com/` + name + `.git","added_at":"2025-01-01T00:00:00Z"}`
	}
	rigs += "}"
	content := `{"version":1,"rigs":` + rigs + `}`
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestGetServerAddr(t *testing.T) {
	check := NewDoltServerReachableCheck()

	tests := []struct {
		name     string
		host     string
		port     int
		wantAddr string
		wantOK   bool
	}{
		{
			name:     "defaults to 127.0.0.1:3307",
			wantAddr: "127.0.0.1:3307",
			wantOK:   true,
		},
		{
			name:     "explicit IPv4 host and port",
			host:     "10.0.0.5",
			port:     3308,
			wantAddr: "10.0.0.5:3308",
			wantOK:   true,
		},
		{
			name:     "IPv6 host gets bracketed",
			host:     "::1",
			wantAddr: "[::1]:3307",
			wantOK:   true,
		},
		{
			name:     "IPv6 host with explicit port",
			host:     "::1",
			port:     3309,
			wantAddr: "[::1]:3309",
			wantOK:   true,
		},
		{
			name:     "explicit host with default port",
			host:     "dolt.example.com",
			wantAddr: "dolt.example.com:3307",
			wantOK:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			townRoot := t.TempDir()
			beadsDir := filepath.Join(townRoot, ".beads")
			setupServerMetadata(t, beadsDir, tt.host, tt.port)

			addr, ok := check.getServerAddr(beadsDir, townRoot)
			if ok != tt.wantOK {
				t.Fatalf("getServerAddr() ok = %v, want %v", ok, tt.wantOK)
			}
			if addr != tt.wantAddr {
				t.Errorf("getServerAddr() = %q, want %q", addr, tt.wantAddr)
			}
		})
	}
}

func TestGetServerAddr_NotServerMode(t *testing.T) {
	check := NewDoltServerReachableCheck()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	meta := map[string]interface{}{
		"backend":   "dolt",
		"dolt_mode": "local",
	}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	_, ok := check.getServerAddr(beadsDir, t.TempDir())
	if ok {
		t.Error("getServerAddr() returned ok=true for local mode, want false")
	}
}

func TestGetServerAddr_NoMetadata(t *testing.T) {
	check := NewDoltServerReachableCheck()
	_, ok := check.getServerAddr(filepath.Join(t.TempDir(), "nonexistent"), t.TempDir())
	if ok {
		t.Error("getServerAddr() returned ok=true for missing metadata, want false")
	}
}

func TestGetServerAddr_UsesConfigYAMLPort(t *testing.T) {
	t.Setenv("GT_DOLT_HOST", "")
	t.Setenv("GT_DOLT_PORT", "")
	t.Setenv("GT_DOLT_IGNORE_CONFIG", "")

	check := NewDoltServerReachableCheck()
	townRoot := t.TempDir()

	// Create config.yaml with custom port
	doltDataDir := filepath.Join(townRoot, ".dolt-data")
	if err := os.MkdirAll(doltDataDir, 0755); err != nil {
		t.Fatal(err)
	}
	configYAML := "listener:\n  port: 13527\n  max_connections: 1000\n"
	if err := os.WriteFile(filepath.Join(doltDataDir, "config.yaml"), []byte(configYAML), 0644); err != nil {
		t.Fatal(err)
	}

	// Create metadata.json with no port (should read from config.yaml)
	beadsDir := filepath.Join(townRoot, ".beads")
	setupServerMetadata(t, beadsDir, "", 0)

	addr, ok := check.getServerAddr(beadsDir, townRoot)
	if !ok {
		t.Fatal("getServerAddr() returned ok=false, want true")
	}
	// Should use port from config.yaml, not default 3307
	if addr != "127.0.0.1:13527" {
		t.Errorf("getServerAddr() = %q, want %q (from config.yaml)", addr, "127.0.0.1:13527")
	}
}

func TestGetServerAddr_ExternalEndpointOverridesStaleMetadata(t *testing.T) {
	check := NewDoltServerReachableCheck()
	townRoot := t.TempDir()
	t.Setenv("GT_DOLT_EXTERNAL", "")
	t.Setenv("GT_DOLT_HOST", "")
	t.Setenv("GT_DOLT_PORT", "")

	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}
	daemonJSON := `{"env":{"GT_DOLT_EXTERNAL":"true","GT_DOLT_HOST":"127.0.0.1","GT_DOLT_PORT":"3308"}}`
	if err := os.WriteFile(filepath.Join(mayorDir, "daemon.json"), []byte(daemonJSON), 0644); err != nil {
		t.Fatal(err)
	}

	doltDataDir := filepath.Join(townRoot, ".dolt-data")
	if err := os.MkdirAll(doltDataDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(doltDataDir, "config.yaml"), []byte("listener:\n  port: 3307\n"), 0644); err != nil {
		t.Fatal(err)
	}

	beadsDir := filepath.Join(townRoot, ".beads")
	setupServerMetadata(t, beadsDir, "127.0.0.1", 3307)

	addr, ok := check.getServerAddr(beadsDir, townRoot)
	if !ok {
		t.Fatal("getServerAddr() returned ok=false, want true")
	}
	if addr != "127.0.0.1:3308" {
		t.Errorf("getServerAddr() = %q, want 127.0.0.1:3308 from external endpoint", addr)
	}
}

func TestDoltServerReachableCheck_FailsWhenExpectedRigDatabaseMissing(t *testing.T) {
	check := NewDoltServerReachableCheck()
	townRoot := t.TempDir()

	setupRigsJSON(t, townRoot, []string{"gastown"})
	setupRigMetadata(t, townRoot, "hq", "hq")
	setupRigMetadata(t, townRoot, "gastown", "gastown")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	host, portStr, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	beadsDir := filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads")
	setupServerMetadata(t, beadsDir, host, mustAtoi(t, portStr))
	writeServerMetadata(t, beadsDir, "gastown", host, mustAtoi(t, portStr))
	hqBeadsDir := filepath.Join(townRoot, ".beads")
	setupServerMetadata(t, hqBeadsDir, host, mustAtoi(t, portStr))
	writeServerMetadata(t, hqBeadsDir, "hq", host, mustAtoi(t, portStr))

	origVerify := verifyExpectedDatabasesAtConfig
	verifyExpectedDatabasesAtConfig = func(_ *doltserver.Config, expected []string) ([]string, []string, error) {
		if len(expected) != 2 || expected[0] != "hq" || expected[1] != "gastown" {
			t.Fatalf("unexpected expected database list: %#v", expected)
		}
		return []string{"hq"}, []string{"gastown"}, nil
	}
	defer func() { verifyExpectedDatabasesAtConfig = origVerify }()

	result := check.Run(&CheckContext{TownRoot: townRoot})
	if result.Status != StatusError {
		t.Fatalf("expected StatusError, got %v: %s", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, "expected rig database") {
		t.Fatalf("expected missing database message, got %q", result.Message)
	}
	if len(result.Details) != 1 || result.Details[0] != "gastown (gastown)" {
		t.Fatalf("expected gastown missing detail, got %#v", result.Details)
	}
}

func TestDoltServerReachableCheck_FailsWhenDatabaseVerificationErrors(t *testing.T) {
	check := NewDoltServerReachableCheck()
	townRoot := t.TempDir()

	setupRigsJSON(t, townRoot, []string{"gastown"})
	setupRigMetadata(t, townRoot, "hq", "hq")
	setupRigMetadata(t, townRoot, "gastown", "gastown")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	host, portStr, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	beadsDir := filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads")
	setupServerMetadata(t, beadsDir, host, mustAtoi(t, portStr))
	writeServerMetadata(t, beadsDir, "gastown", host, mustAtoi(t, portStr))
	hqBeadsDir := filepath.Join(townRoot, ".beads")
	setupServerMetadata(t, hqBeadsDir, host, mustAtoi(t, portStr))
	writeServerMetadata(t, hqBeadsDir, "hq", host, mustAtoi(t, portStr))

	origVerify := verifyExpectedDatabasesAtConfig
	verifyExpectedDatabasesAtConfig = func(_ *doltserver.Config, expected []string) ([]string, []string, error) {
		if len(expected) != 2 || expected[0] != "hq" || expected[1] != "gastown" {
			t.Fatalf("unexpected expected database list: %#v", expected)
		}
		return nil, nil, fmt.Errorf("panic from sibling db")
	}
	defer func() { verifyExpectedDatabasesAtConfig = origVerify }()

	result := check.Run(&CheckContext{TownRoot: townRoot})
	if result.Status != StatusError {
		t.Fatalf("expected StatusError, got %v: %s", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, "database verification failed") {
		t.Fatalf("expected verification failure message, got %q", result.Message)
	}
	if len(result.Details) != 1 || !strings.Contains(result.Details[0], "panic from sibling db") {
		t.Fatalf("expected verification error detail, got %#v", result.Details)
	}
}

func TestDoltServerReachableCheck_UsesConfiguredDatabaseNameNotRigName(t *testing.T) {
	check := NewDoltServerReachableCheck()
	townRoot := t.TempDir()

	setupRigsJSON(t, townRoot, []string{"laneassist"})
	setupRigMetadata(t, townRoot, "hq", "hq")
	setupRigMetadata(t, townRoot, "laneassist", "lc")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	host, portStr, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	beadsDir := filepath.Join(townRoot, "laneassist", "mayor", "rig", ".beads")
	writeServerMetadata(t, beadsDir, "lc", host, mustAtoi(t, portStr))
	hqBeadsDir := filepath.Join(townRoot, ".beads")
	writeServerMetadata(t, hqBeadsDir, "hq", host, mustAtoi(t, portStr))

	origVerify := verifyExpectedDatabasesAtConfig
	verifyExpectedDatabasesAtConfig = func(_ *doltserver.Config, expected []string) ([]string, []string, error) {
		if len(expected) != 2 || expected[0] != "hq" || expected[1] != "lc" {
			t.Fatalf("unexpected expected database list: %#v", expected)
		}
		return []string{"hq", "lc"}, nil, nil
	}
	defer func() { verifyExpectedDatabasesAtConfig = origVerify }()

	result := check.Run(&CheckContext{TownRoot: townRoot})
	if result.Status != StatusOK {
		t.Fatalf("expected StatusOK, got %v: %s", result.Status, result.Message)
	}
}

func mustAtoi(t *testing.T, value string) int {
	t.Helper()
	port, err := strconv.Atoi(value)
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func writeServerMetadata(t *testing.T, beadsDir, database, host string, port int) {
	t.Helper()
	meta := map[string]interface{}{
		"backend":          "dolt",
		"dolt_mode":        "server",
		"dolt_database":    database,
		"dolt_server_host": host,
		"dolt_server_port": port,
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestDoltOrphanedDatabaseCheck_NoOrphans(t *testing.T) {
	townRoot := t.TempDir()

	setupDoltDB(t, townRoot, "hq")
	setupDoltDB(t, townRoot, "gastown")

	setupRigsJSON(t, townRoot, []string{"gastown"})
	setupRigMetadata(t, townRoot, "hq", "hq")
	setupRigMetadata(t, townRoot, "gastown", "gastown")

	check := NewDoltOrphanedDatabaseCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	result := check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK, got %v: %s", result.Status, result.Message)
	}
}

func TestDoltOrphanedDatabaseCheck_DetectsOrphans(t *testing.T) {
	townRoot := t.TempDir()

	setupDoltDB(t, townRoot, "hq")
	setupDoltDB(t, townRoot, "wyvern")
	setupDoltDB(t, townRoot, "beads_wy") // orphan

	setupRigsJSON(t, townRoot, []string{"wyvern"})
	setupRigMetadata(t, townRoot, "hq", "hq")
	setupRigMetadata(t, townRoot, "wyvern", "wyvern")

	check := NewDoltOrphanedDatabaseCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning, got %v: %s", result.Status, result.Message)
	}
	if result.Message != "1 orphaned database(s) in .dolt-data/" {
		t.Errorf("unexpected message: %s", result.Message)
	}
	if len(result.Details) != 1 {
		t.Fatalf("expected 1 detail, got %d", len(result.Details))
	}
	if result.FixHint == "" {
		t.Error("expected a fix hint")
	}
}

func TestDoltOrphanedDatabaseCheck_Fix(t *testing.T) {
	townRoot := t.TempDir()

	setupDoltDB(t, townRoot, "hq")
	setupDoltDB(t, townRoot, "orphan1")
	setupDoltDB(t, townRoot, "orphan2")

	setupRigsJSON(t, townRoot, []string{})
	setupRigMetadata(t, townRoot, "hq", "hq")

	check := NewDoltOrphanedDatabaseCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	// Run to populate orphan names
	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning, got %v: %s", result.Status, result.Message)
	}
	if len(check.orphanNames) != 2 {
		t.Fatalf("expected 2 cached orphan names, got %d", len(check.orphanNames))
	}

	// Fix should remove the orphans
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix: %v", err)
	}

	// Verify orphans are gone
	for _, name := range []string{"orphan1", "orphan2"} {
		path := filepath.Join(townRoot, ".dolt-data", name)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("expected %s to be removed after Fix", name)
		}
	}

	// Verify referenced database still exists
	hqPath := filepath.Join(townRoot, ".dolt-data", "hq")
	if _, err := os.Stat(hqPath); err != nil {
		t.Errorf("expected hq database to survive Fix, but got error: %v", err)
	}
}

func TestDoltOrphanedDatabaseCheck_NoDoltData(t *testing.T) {
	townRoot := t.TempDir()

	check := NewDoltOrphanedDatabaseCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	result := check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK for missing .dolt-data/, got %v: %s", result.Status, result.Message)
	}
}

func TestDoltOrphanedDatabaseCheck_CanFix(t *testing.T) {
	check := NewDoltOrphanedDatabaseCheck()
	if !check.CanFix() {
		t.Error("expected CanFix to return true")
	}
}

func TestDoltOrphanedDatabaseCheck_Name(t *testing.T) {
	check := NewDoltOrphanedDatabaseCheck()
	if check.Name() != "dolt-orphaned-databases" {
		t.Errorf("expected name 'dolt-orphaned-databases', got %q", check.Name())
	}
}
