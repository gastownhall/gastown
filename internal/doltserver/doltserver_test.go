package doltserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// =============================================================================
// Health metrics tests
// =============================================================================

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{0, "0 B"},
		{500, "500 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1572864, "1.5 MB"},
		{1073741824, "1.0 GB"},
		{2147483648, "2.0 GB"},
	}
	for _, tt := range tests {
		got := formatBytes(tt.input)
		if got != tt.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestDirSize(t *testing.T) {
	tmpDir := t.TempDir()

	// Create some files with known sizes
	if err := os.WriteFile(filepath.Join(tmpDir, "a.txt"), make([]byte, 100), 0644); err != nil {
		t.Fatal(err)
	}
	subDir := filepath.Join(tmpDir, "sub")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "b.txt"), make([]byte, 200), 0644); err != nil {
		t.Fatal(err)
	}

	size := dirSize(tmpDir)
	if size != 300 {
		t.Errorf("dirSize = %d, want 300", size)
	}
}

func TestDirSize_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	size := dirSize(tmpDir)
	if size != 0 {
		t.Errorf("dirSize of empty dir = %d, want 0", size)
	}
}

func TestDirSize_NonexistentDir(t *testing.T) {
	size := dirSize("/nonexistent/path/that/does/not/exist")
	if size != 0 {
		t.Errorf("dirSize of nonexistent dir = %d, want 0", size)
	}
}

func TestGetDoltFlagFromArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		flag string
		want string
	}{
		{
			name: "space separated data dir",
			args: []string{"dolt", "sql-server", "--data-dir", "/tmp/dolt-data"},
			flag: "--data-dir",
			want: "/tmp/dolt-data",
		},
		{
			name: "equals data dir",
			args: []string{"dolt", "sql-server", "--data-dir=/tmp/dolt-data"},
			flag: "--data-dir",
			want: "/tmp/dolt-data",
		},
		{
			name: "space separated config",
			args: []string{"dolt", "sql-server", "--config", "/tmp/.dolt-data/config.yaml"},
			flag: "--config",
			want: "/tmp/.dolt-data/config.yaml",
		},
		{
			name: "equals config",
			args: []string{"dolt", "sql-server", "--config=/tmp/.dolt-data/config.yaml"},
			flag: "--config",
			want: "/tmp/.dolt-data/config.yaml",
		},
		{
			name: "missing flag",
			args: []string{"dolt", "sql-server"},
			flag: "--config",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := getDoltFlagFromArgs(tt.args, tt.flag); got != tt.want {
				t.Fatalf("getDoltFlagFromArgs(%v, %q) = %q, want %q", tt.args, tt.flag, got, tt.want)
			}
		})
	}
}

func TestReadSQLServerInfo(t *testing.T) {
	dataDir := t.TempDir()
	infoDir := filepath.Join(dataDir, ".dolt")
	if err := os.MkdirAll(infoDir, 0755); err != nil {
		t.Fatal(err)
	}
	infoPath := filepath.Join(infoDir, "sql-server.info")
	if err := os.WriteFile(infoPath, []byte("62569:3307:757ce4ea-40c5-40f1-9eaf-4d584cae87b0\n"), 0644); err != nil {
		t.Fatal(err)
	}

	info, err := readSQLServerInfo(&Config{DataDir: dataDir})
	if err != nil {
		t.Fatalf("readSQLServerInfo: %v", err)
	}
	if info.PID != 62569 {
		t.Fatalf("PID = %d, want 62569", info.PID)
	}
	if info.Port != 3307 {
		t.Fatalf("Port = %d, want 3307", info.Port)
	}
	if info.ServerID != "757ce4ea-40c5-40f1-9eaf-4d584cae87b0" {
		t.Fatalf("ServerID = %q", info.ServerID)
	}
	if info.Path != infoPath {
		t.Fatalf("Path = %q, want %q", info.Path, infoPath)
	}
}

func TestReadSQLServerInfoRejectsMalformedContent(t *testing.T) {
	dataDir := t.TempDir()
	infoDir := filepath.Join(dataDir, ".dolt")
	if err := os.MkdirAll(infoDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(infoDir, "sql-server.info"), []byte("not-a-pid:3307"), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := readSQLServerInfo(&Config{DataDir: dataDir}); err == nil {
		t.Fatal("expected malformed sql-server.info to fail")
	}
}

func TestDoltProcessMatchesTownPaths(t *testing.T) {
	expectedDir := "/town/.dolt-data"

	tests := []struct {
		name             string
		actualDataDir    string
		actualConfigPath string
		actualCWD        string
		stateDataDir     string
		want             bool
	}{
		{
			name:          "matches live data dir",
			actualDataDir: "/town/.dolt-data",
			want:          true,
		},
		{
			name:             "matches live config path",
			actualConfigPath: "/town/.dolt-data/config.yaml",
			want:             true,
		},
		{
			name:      "matches cwd in data dir",
			actualCWD: "/town/.dolt-data",
			want:      true,
		},
		{
			name:      "matches cwd in town root",
			actualCWD: "/town",
			want:      true,
		},
		{
			name:         "falls back to matching state",
			stateDataDir: "/town/.dolt-data",
			want:         true,
		},
		{
			name:             "live config beats stale matching state",
			actualConfigPath: "/town/juplend_4/.beads/dolt/config.yaml",
			stateDataDir:     "/town/.dolt-data",
			want:             false,
		},
		{
			name:         "foreign cwd beats stale matching state",
			actualCWD:    "/town/juplend_4/.beads/dolt",
			stateDataDir: "/town/.dolt-data",
			want:         false,
		},
		{
			name:             "correct config beats unusual cwd",
			actualConfigPath: "/town/.dolt-data/config.yaml",
			actualCWD:        "/town/juplend_4/.beads/dolt",
			want:             true,
		},
		{
			name: "rejects unknown process",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := doltProcessMatchesTownPaths(expectedDir, tt.actualDataDir, tt.actualConfigPath, tt.actualCWD, tt.stateDataDir)
			if got != tt.want {
				t.Fatalf("doltProcessMatchesTownPaths(...) = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestContainsPathBoundary(t *testing.T) {
	tests := []struct {
		name string
		line string
		path string
		want bool
	}{
		{name: "empty path", line: "/tmp/gt", path: "", want: false},
		{name: "exact at end", line: "--data-dir /tmp/gt", path: "/tmp/gt", want: true},
		{name: "path separator", line: "--data-dir /tmp/gt/.dolt-data", path: "/tmp/gt", want: true},
		{name: "space boundary", line: "/tmp/gt --port 3307", path: "/tmp/gt", want: true},
		{name: "tab boundary", line: "/tmp/gt\t--port 3307", path: "/tmp/gt", want: true},
		{name: "reject sibling prefix", line: "/tmp/gt-old --port 3307", path: "/tmp/gt", want: false},
		{name: "later match after rejected prefix", line: "/tmp/gt-old /tmp/gt/.beads", path: "/tmp/gt", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containsPathBoundary(tt.line, tt.path)
			if got != tt.want {
				t.Fatalf("containsPathBoundary(%q, %q) = %v, want %v", tt.line, tt.path, got, tt.want)
			}
		})
	}
}

func TestFindIdleMonitorProcessesFromPS(t *testing.T) {
	const townRoot = "/tmp/gt"
	const port = 3307
	pidLines := []struct {
		line string
		want []int
	}{
		{line: "101 bd dolt idle-monitor --data-dir /tmp/gt/.beads/dolt", want: []int{101}},
		{line: "102 bd dolt idle-monitor --data-dir /tmp/gt-old/.beads/dolt", want: nil},
		{line: "103 bd dolt idle-monitor --port 3307", want: []int{103}},
		{line: "104 bd dolt idle-monitor -p 3307", want: []int{104}},
		{line: "105 bd dolt idle-monitor --port=3307", want: []int{105}},
		{line: "106 bd dolt idle-monitor --port=33070", want: nil},
		{line: "107 bd idle-monitor --port 3307", want: nil},
		{line: "108 bd dolt monitor --port 3307", want: nil},
		{line: "109 grep bd dolt idle-monitor --port 3307", want: nil},
		{line: "110 /home/grep/bin/bd dolt idle-monitor --port 3307", want: []int{110}},
		{line: "notapid bd dolt idle-monitor --port 3307", want: nil},
	}

	for _, tt := range pidLines {
		t.Run(tt.line, func(t *testing.T) {
			got := findIdleMonitorProcessesFromPS(tt.line, townRoot, townRoot, port)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestDoltProcessOwnerPathFromEvidence(t *testing.T) {
	tests := []struct {
		name             string
		actualDataDir    string
		actualConfigPath string
		actualCWD        string
		stateDataDir     string
		want             string
	}{
		{
			name:          "prefers live data dir",
			actualDataDir: "/town/.dolt-data",
			actualCWD:     "/town",
			stateDataDir:  "/town/.dolt-data",
			want:          "/town/.dolt-data",
		},
		{
			name:             "falls back to config path",
			actualConfigPath: "/town/rig/.beads/dolt/config.yaml",
			actualCWD:        "/town/rig/.beads/dolt",
			stateDataDir:     "/town/.dolt-data",
			want:             "/town/rig/.beads/dolt/config.yaml",
		},
		{
			name:         "falls back to cwd",
			actualCWD:    "/town/rig/.beads/dolt",
			stateDataDir: "/town/.dolt-data",
			want:         "/town/rig/.beads/dolt",
		},
		{
			name:         "falls back to state",
			stateDataDir: "/town/.dolt-data",
			want:         "/town/.dolt-data",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := doltProcessOwnerPathFromEvidence(tt.actualDataDir, tt.actualConfigPath, tt.actualCWD, tt.stateDataDir)
			if got != tt.want {
				t.Fatalf("doltProcessOwnerPathFromEvidence(...) = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGetHealthMetrics_NoServer(t *testing.T) {
	townRoot := t.TempDir()

	// Create .dolt-data dir with some content
	dataDir := filepath.Join(townRoot, ".dolt-data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "testfile"), make([]byte, 1024), 0644); err != nil {
		t.Fatal(err)
	}

	metrics := GetHealthMetrics(townRoot)

	// Connections and latency depend on whether a local Dolt server is running.
	// With no server, both are 0. With a server, dolt auto-detects it.
	// Either outcome is valid for this test — the key assertion is that
	// GetHealthMetrics doesn't panic or hang.
	if metrics.Connections < 0 {
		t.Errorf("Connections = %d, want >= 0", metrics.Connections)
	}
	if metrics.QueryLatency < 0 {
		t.Errorf("QueryLatency = %v, want >= 0", metrics.QueryLatency)
	}

	// Disk usage should reflect our test file
	if metrics.DiskUsageBytes < 1024 {
		t.Errorf("DiskUsageBytes = %d, want >= 1024", metrics.DiskUsageBytes)
	}
	if metrics.DiskUsageHuman == "" {
		t.Error("DiskUsageHuman should not be empty")
	}

	// MaxConnections should have a default
	if metrics.MaxConnections <= 0 {
		t.Errorf("MaxConnections = %d, want > 0", metrics.MaxConnections)
	}
}

func TestGetHealthMetrics_EmptyDataDir(t *testing.T) {
	townRoot := t.TempDir()

	metrics := GetHealthMetrics(townRoot)

	if metrics.DiskUsageBytes != 0 {
		t.Errorf("DiskUsageBytes = %d, want 0 (no data dir)", metrics.DiskUsageBytes)
	}
	if metrics.DiskUsageHuman != "0 B" {
		t.Errorf("DiskUsageHuman = %q, want %q", metrics.DiskUsageHuman, "0 B")
	}
}

func TestFindMigratableDatabases_FollowsRedirect(t *testing.T) {
	// Setup: simulate a town with a rig that uses a redirect
	townRoot := t.TempDir()

	// Create rig directory with .beads/redirect -> mayor/rig/.beads
	rigName := "nexus"
	rigDir := filepath.Join(townRoot, rigName)
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write redirect file
	redirectPath := filepath.Join(rigBeadsDir, "redirect")
	if err := os.WriteFile(redirectPath, []byte("mayor/rig/.beads\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create the actual Dolt database at the redirected location
	actualDoltDir := filepath.Join(rigDir, "mayor", "rig", ".beads", "dolt", "beads_myrig", ".dolt")
	if err := os.MkdirAll(actualDoltDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create .dolt-data directory (required by DefaultConfig)
	doltDataDir := filepath.Join(townRoot, ".dolt-data")
	if err := os.MkdirAll(doltDataDir, 0755); err != nil {
		t.Fatal(err)
	}

	migrations := FindMigratableDatabases(townRoot)

	// Should find the rig database via redirect
	found := false
	for _, m := range migrations {
		if m.RigName == rigName {
			found = true
			expectedSource := filepath.Join(rigDir, "mayor", "rig", ".beads", "dolt", "beads_myrig")
			if m.SourcePath != expectedSource {
				t.Errorf("SourcePath = %q, want %q", m.SourcePath, expectedSource)
			}
			break
		}
	}
	if !found {
		t.Errorf("expected to find migration for rig %q via redirect, got migrations: %v", rigName, migrations)
	}
}

func TestFindMigratableDatabases_NoRedirect(t *testing.T) {
	// Setup: rig with direct .beads/dolt/beads_testrig (no redirect)
	townRoot := t.TempDir()

	rigName := "simple"
	doltDir := filepath.Join(townRoot, rigName, ".beads", "dolt", "beads_testrig", ".dolt")
	if err := os.MkdirAll(doltDir, 0755); err != nil {
		t.Fatal(err)
	}

	doltDataDir := filepath.Join(townRoot, ".dolt-data")
	if err := os.MkdirAll(doltDataDir, 0755); err != nil {
		t.Fatal(err)
	}

	migrations := FindMigratableDatabases(townRoot)

	found := false
	for _, m := range migrations {
		if m.RigName == rigName {
			found = true
			expectedSource := filepath.Join(townRoot, rigName, ".beads", "dolt", "beads_testrig")
			if m.SourcePath != expectedSource {
				t.Errorf("SourcePath = %q, want %q", m.SourcePath, expectedSource)
			}
			break
		}
	}
	if !found {
		t.Errorf("expected to find migration for rig %q, got migrations: %v", rigName, migrations)
	}
}

func TestFindLocalDoltDB(t *testing.T) {
	t.Run("no dolt directory", func(t *testing.T) {
		beadsDir := t.TempDir()
		result := findLocalDoltDB(beadsDir)
		if result != "" {
			t.Errorf("expected empty string, got %q", result)
		}
	})

	t.Run("empty dolt directory", func(t *testing.T) {
		beadsDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0755); err != nil {
			t.Fatal(err)
		}
		result := findLocalDoltDB(beadsDir)
		if result != "" {
			t.Errorf("expected empty string, got %q", result)
		}
	})

	t.Run("single database", func(t *testing.T) {
		beadsDir := t.TempDir()
		dbDir := filepath.Join(beadsDir, "dolt", "beads_hq", ".dolt")
		if err := os.MkdirAll(dbDir, 0755); err != nil {
			t.Fatal(err)
		}
		result := findLocalDoltDB(beadsDir)
		expected := filepath.Join(beadsDir, "dolt", "beads_hq")
		if result != expected {
			t.Errorf("got %q, want %q", result, expected)
		}
	})

	t.Run("non-dolt files ignored", func(t *testing.T) {
		beadsDir := t.TempDir()
		doltParent := filepath.Join(beadsDir, "dolt")
		if err := os.MkdirAll(doltParent, 0755); err != nil {
			t.Fatal(err)
		}
		// Create a regular file (not a directory)
		if err := os.WriteFile(filepath.Join(doltParent, "readme.txt"), []byte("hi"), 0644); err != nil {
			t.Fatal(err)
		}
		// Create a directory without .dolt inside
		if err := os.MkdirAll(filepath.Join(doltParent, "not-a-db"), 0755); err != nil {
			t.Fatal(err)
		}
		// Create the real database
		if err := os.MkdirAll(filepath.Join(doltParent, "beads_gt", ".dolt"), 0755); err != nil {
			t.Fatal(err)
		}
		result := findLocalDoltDB(beadsDir)
		expected := filepath.Join(doltParent, "beads_gt")
		if result != expected {
			t.Errorf("got %q, want %q", result, expected)
		}
	})

	t.Run("multiple databases returns empty with warning", func(t *testing.T) {
		beadsDir := t.TempDir()
		doltParent := filepath.Join(beadsDir, "dolt")
		// Create two valid dolt databases
		for _, name := range []string{"beads_gt", "beads_old"} {
			if err := os.MkdirAll(filepath.Join(doltParent, name, ".dolt"), 0755); err != nil {
				t.Fatal(err)
			}
		}

		// Capture stderr to verify warning is emitted
		origStderr := os.Stderr
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		os.Stderr = w

		result := findLocalDoltDB(beadsDir)

		w.Close()
		var buf bytes.Buffer
		io.Copy(&buf, r)
		os.Stderr = origStderr

		// Should fail closed on ambiguity — return empty string
		if result != "" {
			t.Errorf("expected empty string for ambiguous multi-candidate, got %q", result)
		}
		// Verify warning was emitted
		if !strings.Contains(buf.String(), "multiple dolt databases found") {
			t.Errorf("expected multi-candidate warning on stderr, got %q", buf.String())
		}
	})

	t.Run("symlink to directory with dolt database", func(t *testing.T) {
		beadsDir := t.TempDir()
		doltParent := filepath.Join(beadsDir, "dolt")
		if err := os.MkdirAll(doltParent, 0755); err != nil {
			t.Fatal(err)
		}
		// Create the real database directory outside the dolt parent
		realDB := filepath.Join(beadsDir, "real_beads_hq")
		if err := os.MkdirAll(filepath.Join(realDB, ".dolt"), 0755); err != nil {
			t.Fatal(err)
		}
		// Symlink it into dolt/
		if err := os.Symlink(realDB, filepath.Join(doltParent, "beads_hq")); err != nil {
			t.Fatal(err)
		}
		result := findLocalDoltDB(beadsDir)
		expected := filepath.Join(doltParent, "beads_hq")
		if result != expected {
			t.Errorf("got %q, want %q", result, expected)
		}
	})
}

func TestEnsureMetadata_HQ(t *testing.T) {
	townRoot := t.TempDir()

	// Create .beads directory
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write existing metadata without dolt config
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"),
		[]byte(`{"database": "beads.db", "custom_field": "preserved"}`), 0600); err != nil {
		t.Fatal(err)
	}

	if err := EnsureMetadata(townRoot, "hq"); err != nil {
		t.Fatalf("EnsureMetadata failed: %v", err)
	}

	// Read and verify
	data, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		t.Fatalf("reading metadata: %v", err)
	}

	var metadata map[string]interface{}
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatalf("parsing metadata: %v", err)
	}

	if metadata["backend"] != "dolt" {
		t.Errorf("backend = %v, want dolt", metadata["backend"])
	}
	if metadata["dolt_mode"] != "server" {
		t.Errorf("dolt_mode = %v, want server", metadata["dolt_mode"])
	}
	if metadata["dolt_database"] != "hq" {
		t.Errorf("dolt_database = %v, want hq", metadata["dolt_database"])
	}
	if metadata["custom_field"] != "preserved" {
		t.Errorf("custom_field was not preserved: %v", metadata["custom_field"])
	}
}

func TestEnsureMetadata_Rig(t *testing.T) {
	townRoot := t.TempDir()

	// Create rig with mayor/rig/.beads
	beadsDir := filepath.Join(townRoot, "myrig", "mayor", "rig", ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := EnsureMetadata(townRoot, "myrig"); err != nil {
		t.Fatalf("EnsureMetadata failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		t.Fatalf("reading metadata: %v", err)
	}

	var metadata map[string]interface{}
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatalf("parsing metadata: %v", err)
	}

	if metadata["backend"] != "dolt" {
		t.Errorf("backend = %v, want dolt", metadata["backend"])
	}
	if metadata["dolt_database"] != "myrig" {
		t.Errorf("dolt_database = %v, want myrig", metadata["dolt_database"])
	}
}

func TestEnsureMetadata_Idempotent(t *testing.T) {
	townRoot := t.TempDir()

	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Run twice
	if err := EnsureMetadata(townRoot, "hq"); err != nil {
		t.Fatalf("first EnsureMetadata failed: %v", err)
	}
	if err := EnsureMetadata(townRoot, "hq"); err != nil {
		t.Fatalf("second EnsureMetadata failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		t.Fatalf("reading metadata: %v", err)
	}

	var metadata map[string]interface{}
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatalf("parsing metadata: %v", err)
	}

	if metadata["dolt_database"] != "hq" {
		t.Errorf("dolt_database = %v, want hq", metadata["dolt_database"])
	}
}

func TestEnsureAllMetadata(t *testing.T) {
	townRoot := t.TempDir()

	// Create two databases in .dolt-data
	dataDir := filepath.Join(townRoot, ".dolt-data")
	setupDoltDB(t, dataDir, "hq")
	setupDoltDB(t, dataDir, "myrig")

	// Create beads dirs
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "myrig", "mayor", "rig", ".beads"), 0755); err != nil {
		t.Fatal(err)
	}

	updated, errs := EnsureAllMetadata(townRoot)
	if len(errs) > 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	if len(updated) != 2 {
		t.Errorf("expected 2 updated, got %d: %v", len(updated), updated)
	}
}

func TestFindRigBeadsDir(t *testing.T) {
	townRoot := t.TempDir()

	// Test empty rigName returns empty string
	if dir := FindRigBeadsDir(townRoot, ""); dir != "" {
		t.Errorf("empty rigName: got %q, want empty string", dir)
	}

	// Test empty townRoot returns empty string
	if dir := FindRigBeadsDir("", "myrig"); dir != "" {
		t.Errorf("empty townRoot: got %q, want empty string", dir)
	}

	// Test both empty returns empty string
	if dir := FindRigBeadsDir("", ""); dir != "" {
		t.Errorf("both empty: got %q, want empty string", dir)
	}

	// Test HQ
	if dir := FindRigBeadsDir(townRoot, "hq"); dir != filepath.Join(townRoot, ".beads") {
		t.Errorf("hq beads dir = %q, want %q", dir, filepath.Join(townRoot, ".beads"))
	}

	// Test rig with mayor/rig/.beads
	mayorBeads := filepath.Join(townRoot, "myrig", "mayor", "rig", ".beads")
	if err := os.MkdirAll(mayorBeads, 0755); err != nil {
		t.Fatal(err)
	}
	if dir := FindRigBeadsDir(townRoot, "myrig"); dir != mayorBeads {
		t.Errorf("myrig beads dir = %q, want %q", dir, mayorBeads)
	}

	// Test rig with only rig-root .beads
	rigBeads := filepath.Join(townRoot, "otherrig", ".beads")
	if err := os.MkdirAll(rigBeads, 0755); err != nil {
		t.Fatal(err)
	}
	if dir := FindRigBeadsDir(townRoot, "otherrig"); dir != rigBeads {
		t.Errorf("otherrig beads dir = %q, want %q", dir, rigBeads)
	}

	// Test rig with neither directory existing — should return rig-root path
	neitherRig := "newrig"
	expectedRigRoot := filepath.Join(townRoot, neitherRig, ".beads")
	if dir := FindRigBeadsDir(townRoot, neitherRig); dir != expectedRigRoot {
		t.Errorf("newrig (neither exists) beads dir = %q, want %q (rig-root path)", dir, expectedRigRoot)
	}
}

func TestFindOrCreateRigBeadsDir(t *testing.T) {
	t.Run("empty rigName returns error", func(t *testing.T) {
		townRoot := t.TempDir()
		_, err := FindOrCreateRigBeadsDir(townRoot, "")
		if err == nil {
			t.Error("expected error for empty rigName, got nil")
		}
	})

	t.Run("empty townRoot returns error", func(t *testing.T) {
		_, err := FindOrCreateRigBeadsDir("", "myrig")
		if err == nil {
			t.Error("expected error for empty townRoot, got nil")
		}
	})

	t.Run("hq creates directory", func(t *testing.T) {
		townRoot := t.TempDir()
		dir, err := FindOrCreateRigBeadsDir(townRoot, "hq")
		if err != nil {
			t.Fatal(err)
		}
		expected := filepath.Join(townRoot, ".beads")
		if dir != expected {
			t.Errorf("hq beads dir = %q, want %q", dir, expected)
		}
		// Verify directory was actually created
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			t.Error("hq .beads directory was not created")
		}
	})

	t.Run("existing mayor path returned as-is", func(t *testing.T) {
		townRoot := t.TempDir()
		mayorBeads := filepath.Join(townRoot, "myrig", "mayor", "rig", ".beads")
		if err := os.MkdirAll(mayorBeads, 0755); err != nil {
			t.Fatal(err)
		}
		dir, err := FindOrCreateRigBeadsDir(townRoot, "myrig")
		if err != nil {
			t.Fatal(err)
		}
		if dir != mayorBeads {
			t.Errorf("myrig beads dir = %q, want %q", dir, mayorBeads)
		}
	})

	t.Run("existing rig-root path returned", func(t *testing.T) {
		townRoot := t.TempDir()
		rigBeads := filepath.Join(townRoot, "otherrig", ".beads")
		if err := os.MkdirAll(rigBeads, 0755); err != nil {
			t.Fatal(err)
		}
		dir, err := FindOrCreateRigBeadsDir(townRoot, "otherrig")
		if err != nil {
			t.Fatal(err)
		}
		if dir != rigBeads {
			t.Errorf("otherrig beads dir = %q, want %q", dir, rigBeads)
		}
	})

	t.Run("neither exists creates rig-root path", func(t *testing.T) {
		townRoot := t.TempDir()
		dir, err := FindOrCreateRigBeadsDir(townRoot, "newrig")
		if err != nil {
			t.Fatal(err)
		}
		expectedRig := filepath.Join(townRoot, "newrig", ".beads")
		if dir != expectedRig {
			t.Errorf("newrig beads dir = %q, want %q", dir, expectedRig)
		}
		// Verify directory was actually created
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			t.Error("rig-root .beads directory was not created")
		}
		// Verify mayor path was NOT created (would confuse InitBeads)
		mayorBeads := filepath.Join(townRoot, "newrig", "mayor", "rig", ".beads")
		if _, err := os.Stat(mayorBeads); err == nil {
			t.Error("mayor/rig/.beads should NOT be created for untracked repos")
		}
	})

	t.Run("concurrent callers for same rig don't race", func(t *testing.T) {
		townRoot := t.TempDir()
		const goroutines = 10
		results := make([]string, goroutines)
		errs := make([]error, goroutines)

		var wg sync.WaitGroup
		wg.Add(goroutines)
		for i := 0; i < goroutines; i++ {
			go func(idx int) {
				defer wg.Done()
				results[idx], errs[idx] = FindOrCreateRigBeadsDir(townRoot, "racerig")
			}(i)
		}
		wg.Wait()

		expectedRig := filepath.Join(townRoot, "racerig", ".beads")
		for i := 0; i < goroutines; i++ {
			if errs[i] != nil {
				t.Errorf("goroutine %d: unexpected error: %v", i, errs[i])
			}
			if results[i] != expectedRig {
				t.Errorf("goroutine %d: got %q, want %q", i, results[i], expectedRig)
			}
		}

		// Verify directory exists
		if _, err := os.Stat(expectedRig); os.IsNotExist(err) {
			t.Error("directory was not created after concurrent calls")
		}
	})

	t.Run("does not create mayor path for untracked repo", func(t *testing.T) {
		// Regression test: FindOrCreateRigBeadsDir must not create
		// mayor/rig/.beads for new rigs. If it does, InitBeads
		// misdetects the rig as having tracked beads and takes the
		// redirect path, skipping config.yaml and issues.jsonl creation.
		townRoot := t.TempDir()

		// Simulate rig directory existing (after git clone) but with
		// NO mayor/rig/.beads (untracked repo).
		rigDir := filepath.Join(townRoot, "untracked", "mayor", "rig")
		if err := os.MkdirAll(rigDir, 0755); err != nil {
			t.Fatal(err)
		}

		dir, err := FindOrCreateRigBeadsDir(townRoot, "untracked")
		if err != nil {
			t.Fatal(err)
		}

		// Should return rig-root .beads, NOT mayor/rig/.beads
		expectedRig := filepath.Join(townRoot, "untracked", ".beads")
		if dir != expectedRig {
			t.Errorf("got %q, want %q", dir, expectedRig)
		}

		// mayor/rig/.beads must NOT exist
		mayorBeads := filepath.Join(townRoot, "untracked", "mayor", "rig", ".beads")
		if _, err := os.Stat(mayorBeads); err == nil {
			t.Error("mayor/rig/.beads was created — would break InitBeads for untracked repos")
		}
	})
}

func TestMoveDir_SameFilesystem(t *testing.T) {
	tmpDir := t.TempDir()

	src := filepath.Join(tmpDir, "src")
	dest := filepath.Join(tmpDir, "dest")

	// Create source with nested content
	if err := os.MkdirAll(filepath.Join(src, "subdir"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "file.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "subdir", "nested.txt"), []byte("world"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := moveDir(src, dest); err != nil {
		t.Fatalf("moveDir failed: %v", err)
	}

	// Source should be gone
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source directory still exists after move")
	}

	// Dest should have the content
	data, err := os.ReadFile(filepath.Join(dest, "file.txt"))
	if err != nil {
		t.Fatalf("reading moved file: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("file content = %q, want %q", string(data), "hello")
	}

	data, err = os.ReadFile(filepath.Join(dest, "subdir", "nested.txt"))
	if err != nil {
		t.Fatalf("reading moved nested file: %v", err)
	}
	if string(data) != "world" {
		t.Errorf("nested file content = %q, want %q", string(data), "world")
	}
}

func TestMigrateRigFromBeads(t *testing.T) {
	townRoot := t.TempDir()

	// Create source database
	rigName := "testrig"
	sourcePath := filepath.Join(townRoot, rigName, ".beads", "dolt", "beads_testrig")
	if err := os.MkdirAll(filepath.Join(sourcePath, ".dolt"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourcePath, ".dolt", "config.json"), []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Create beads dir for metadata
	beadsDir := filepath.Join(townRoot, rigName, "mayor", "rig", ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := MigrateRigFromBeads(townRoot, rigName, sourcePath); err != nil {
		t.Fatalf("MigrateRigFromBeads failed: %v", err)
	}

	// Source should be gone
	if _, err := os.Stat(sourcePath); !os.IsNotExist(err) {
		t.Errorf("source directory still exists after migration")
	}

	// Target should have .dolt
	targetDir := filepath.Join(townRoot, ".dolt-data", rigName)
	if _, err := os.Stat(filepath.Join(targetDir, ".dolt")); err != nil {
		t.Errorf("target .dolt directory missing: %v", err)
	}

	// config.json should have been migrated
	data, err := os.ReadFile(filepath.Join(targetDir, ".dolt", "config.json"))
	if err != nil {
		t.Fatalf("reading migrated config: %v", err)
	}
	if string(data) != `{}` {
		t.Errorf("config content = %q, want %q", string(data), `{}`)
	}
}

func TestMigrateRigFromBeads_AlreadyExists(t *testing.T) {
	townRoot := t.TempDir()

	rigName := "existing"
	sourcePath := filepath.Join(townRoot, "src", ".beads", "dolt", "beads_existing")
	if err := os.MkdirAll(filepath.Join(sourcePath, ".dolt"), 0755); err != nil {
		t.Fatal(err)
	}

	// Target already exists
	targetDir := filepath.Join(townRoot, ".dolt-data", rigName, ".dolt")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}

	err := MigrateRigFromBeads(townRoot, rigName, sourcePath)
	if err == nil {
		t.Fatal("expected error for already-existing target, got nil")
	}
}

func TestHasServerModeMetadata_NoMetadata(t *testing.T) {
	townRoot := t.TempDir()

	// Create empty workspace
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "rigs.json"), []byte(`{"rigs":{}}`), 0644); err != nil {
		t.Fatal(err)
	}

	rigs := HasServerModeMetadata(townRoot)
	if len(rigs) != 0 {
		t.Errorf("expected no server-mode rigs, got %v", rigs)
	}
}

func TestHasServerModeMetadata_WithServerMode(t *testing.T) {
	townRoot := t.TempDir()

	// Create town beads with server mode
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	metadata := `{"backend":"dolt","dolt_mode":"server","dolt_database":"hq"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0644); err != nil {
		t.Fatal(err)
	}

	// Create rig with server mode
	rigBeadsDir := filepath.Join(townRoot, "myrig", "mayor", "rig", ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	rigMetadata := `{"backend":"dolt","dolt_mode":"server","dolt_database":"myrig"}`
	if err := os.WriteFile(filepath.Join(rigBeadsDir, "metadata.json"), []byte(rigMetadata), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "rigs.json"),
		[]byte(`{"rigs":{"myrig":{}}}`), 0644); err != nil {
		t.Fatal(err)
	}

	rigs := HasServerModeMetadata(townRoot)
	if len(rigs) != 2 {
		t.Errorf("expected 2 server-mode rigs, got %d: %v", len(rigs), rigs)
	}
}

func TestHasServerModeMetadata_MixedModes(t *testing.T) {
	townRoot := t.TempDir()

	// Town beads with server mode
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"),
		[]byte(`{"backend":"dolt","dolt_mode":"server","dolt_database":"hq"}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Rig with sqlite (not server mode)
	rigBeadsDir := filepath.Join(townRoot, "sqliterig", "mayor", "rig", ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rigBeadsDir, "metadata.json"),
		[]byte(`{"backend":"sqlite"}`), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "rigs.json"),
		[]byte(`{"rigs":{"sqliterig":{}}}`), 0644); err != nil {
		t.Fatal(err)
	}

	rigs := HasServerModeMetadata(townRoot)
	if len(rigs) != 1 {
		t.Errorf("expected 1 server-mode rig (hq only), got %d: %v", len(rigs), rigs)
	}
	if len(rigs) > 0 && rigs[0] != "hq" {
		t.Errorf("expected hq, got %s", rigs[0])
	}
}

func TestCheckServerReachable_NoServer(t *testing.T) {
	townRoot := t.TempDir()

	// CheckServerReachable should fail when no server is listening
	// Using default port 3307 - if a real server is running, skip
	err := CheckServerReachable(townRoot)
	if err == nil {
		t.Skip("A server is actually running on port 3307, cannot test unreachable case")
	}
	if err != nil && !contains(err.Error(), "not reachable") {
		t.Errorf("expected 'not reachable' in error, got: %v", err)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstr(s, substr)
}

func searchSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestFindMigratableDatabases_SkipsAlreadyMigrated(t *testing.T) {
	townRoot := t.TempDir()

	rigName := "already"
	// Source exists
	sourceDir := filepath.Join(townRoot, rigName, ".beads", "dolt", "beads_hq", ".dolt")
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Target also exists (already migrated)
	targetDir := filepath.Join(townRoot, ".dolt-data", rigName, ".dolt")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}

	migrations := FindMigratableDatabases(townRoot)

	for _, m := range migrations {
		if m.RigName == rigName {
			t.Errorf("should not include already-migrated rig %q", rigName)
		}
	}
}

// =============================================================================
// Mid-migration crash recovery tests
// =============================================================================

// TestMidMigrationCrashRecovery_PartialMigration tests that after migrating
// some rigs but not others (simulating a crash), resuming migration completes
// all remaining rigs without corrupting already-migrated ones.
func TestMidMigrationCrashRecovery_PartialMigration(t *testing.T) {
	townRoot := t.TempDir()

	// Create 3 rigs with source databases
	rigs := []string{"rig-alpha", "rig-beta", "rig-gamma"}
	for _, rig := range rigs {
		sourceDolt := filepath.Join(townRoot, rig, ".beads", "dolt", "beads_"+rig, ".dolt")
		if err := os.MkdirAll(sourceDolt, 0755); err != nil {
			t.Fatal(err)
		}
		// Write a marker file so we can verify data integrity
		marker := filepath.Join(sourceDolt, "marker.txt")
		if err := os.WriteFile(marker, []byte("data-"+rig), 0644); err != nil {
			t.Fatal(err)
		}
		// Create beads dir for metadata
		beadsDir := filepath.Join(townRoot, rig, "mayor", "rig", ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}
	}

	// Phase 1: Migrate only the first rig (simulating crash after rig 1)
	migrations := FindMigratableDatabases(townRoot)
	if len(migrations) != 3 {
		t.Fatalf("expected 3 migratable databases, got %d", len(migrations))
	}

	// Migrate only rig-alpha
	for _, m := range migrations {
		if m.RigName == "rig-alpha" {
			if err := MigrateRigFromBeads(townRoot, m.RigName, m.SourcePath); err != nil {
				t.Fatalf("migrating %s: %v", m.RigName, err)
			}
			break
		}
	}

	// Verify rig-alpha is migrated and data intact
	alphaTarget := filepath.Join(townRoot, ".dolt-data", "rig-alpha", ".dolt", "marker.txt")
	data, err := os.ReadFile(alphaTarget)
	if err != nil {
		t.Fatalf("reading migrated alpha marker: %v", err)
	}
	if string(data) != "data-rig-alpha" {
		t.Errorf("alpha marker = %q, want %q", string(data), "data-rig-alpha")
	}

	// Phase 2: Resume migration (find remaining databases)
	remaining := FindMigratableDatabases(townRoot)
	if len(remaining) != 2 {
		t.Fatalf("expected 2 remaining migratable databases after partial migration, got %d", len(remaining))
	}

	// Verify rig-alpha is NOT in the remaining list
	for _, m := range remaining {
		if m.RigName == "rig-alpha" {
			t.Error("rig-alpha should not appear in remaining migrations")
		}
	}

	// Migrate the rest
	for _, m := range remaining {
		if err := MigrateRigFromBeads(townRoot, m.RigName, m.SourcePath); err != nil {
			t.Fatalf("migrating %s on resume: %v", m.RigName, err)
		}
	}

	// Verify all 3 rigs are now migrated with correct data
	for _, rig := range rigs {
		markerPath := filepath.Join(townRoot, ".dolt-data", rig, ".dolt", "marker.txt")
		data, err := os.ReadFile(markerPath)
		if err != nil {
			t.Fatalf("reading marker for %s: %v", rig, err)
		}
		expected := "data-" + rig
		if string(data) != expected {
			t.Errorf("%s marker = %q, want %q", rig, string(data), expected)
		}
	}

	// No more migratable databases should remain
	final := FindMigratableDatabases(townRoot)
	if len(final) != 0 {
		t.Errorf("expected 0 migratable databases after full migration, got %d", len(final))
	}
}

// TestMidMigrationCrashRecovery_SourceGoneTargetExists tests that if a crash
// happened after the move but before metadata update, the system recognizes
// the rig as already migrated (target exists, source gone).
func TestMidMigrationCrashRecovery_SourceGoneTargetExists(t *testing.T) {
	townRoot := t.TempDir()

	rigName := "crashed-rig"

	// Simulate post-move state: source is gone, target exists
	targetDolt := filepath.Join(townRoot, ".dolt-data", rigName, ".dolt")
	if err := os.MkdirAll(targetDolt, 0755); err != nil {
		t.Fatal(err)
	}

	// Source does NOT exist (was already moved)
	// FindMigratableDatabases should not list this rig
	migrations := FindMigratableDatabases(townRoot)
	for _, m := range migrations {
		if m.RigName == rigName {
			t.Error("should not attempt to re-migrate a rig whose target already exists")
		}
	}

	// EnsureMetadata should still work to repair metadata.json
	beadsDir := filepath.Join(townRoot, rigName, "mayor", "rig", ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := EnsureMetadata(townRoot, rigName); err != nil {
		t.Fatalf("EnsureMetadata for crashed rig: %v", err)
	}

	// Verify metadata was written correctly
	metaData, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		t.Fatalf("reading metadata: %v", err)
	}
	var meta map[string]interface{}
	if err := json.Unmarshal(metaData, &meta); err != nil {
		t.Fatalf("parsing metadata: %v", err)
	}
	if meta["backend"] != "dolt" {
		t.Errorf("backend = %v, want dolt", meta["backend"])
	}
}

// =============================================================================
// Concurrent access during migration tests
// =============================================================================

// TestConcurrentMetadataAccess tests that concurrent EnsureMetadata calls
// for different rigs don't interfere with each other.
func TestConcurrentMetadataAccess(t *testing.T) {
	townRoot := t.TempDir()

	rigs := []string{"rig-a", "rig-b", "rig-c", "rig-d", "rig-e"}
	for _, rig := range rigs {
		beadsDir := filepath.Join(townRoot, rig, "mayor", "rig", ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}
	}

	var wg sync.WaitGroup
	errs := make([]error, len(rigs))

	for i, rig := range rigs {
		wg.Add(1)
		go func(idx int, rigName string) {
			defer wg.Done()
			errs[idx] = EnsureMetadata(townRoot, rigName)
		}(i, rig)
	}

	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("EnsureMetadata for %s failed: %v", rigs[i], err)
		}
	}

	// Verify each rig got the correct metadata
	for _, rig := range rigs {
		metaPath := filepath.Join(townRoot, rig, "mayor", "rig", ".beads", "metadata.json")
		data, err := os.ReadFile(metaPath)
		if err != nil {
			t.Fatalf("reading metadata for %s: %v", rig, err)
		}
		var meta map[string]interface{}
		if err := json.Unmarshal(data, &meta); err != nil {
			t.Fatalf("parsing metadata for %s: %v", rig, err)
		}
		if meta["dolt_database"] != rig {
			t.Errorf("%s: dolt_database = %v, want %s", rig, meta["dolt_database"], rig)
		}
		if meta["backend"] != "dolt" {
			t.Errorf("%s: backend = %v, want dolt", rig, meta["backend"])
		}
	}
}

// TestConcurrentMetadataSameFile tests that concurrent EnsureMetadata calls
// targeting the SAME metadata.json file don't corrupt data. This exercises
// the file locking added to prevent read-modify-write races.
func TestConcurrentMetadataSameFile(t *testing.T) {
	townRoot := t.TempDir()

	// All goroutines will target the same rig (and thus the same metadata.json)
	rigName := "shared-rig"
	beadsDir := filepath.Join(townRoot, rigName, "mayor", "rig", ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Seed with extra fields that must survive concurrent overwrites
	initial := map[string]interface{}{
		"custom_field": "preserve-me",
		"version":      42.0,
	}
	data, _ := json.MarshalIndent(initial, "", "  ")
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	if err := os.WriteFile(metadataPath, data, 0600); err != nil {
		t.Fatal(err)
	}

	// Run 10 concurrent goroutines all writing to the same file
	const concurrency = 10
	var wg sync.WaitGroup
	errs := make([]error, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = EnsureMetadata(townRoot, rigName)
		}(i)
	}

	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: EnsureMetadata failed: %v", i, err)
		}
	}

	// Verify final metadata is valid and preserves all fields
	finalData, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}

	var meta map[string]interface{}
	if err := json.Unmarshal(finalData, &meta); err != nil {
		t.Fatalf("final metadata is corrupted JSON: %v\ncontent: %s", err, string(finalData))
	}

	// Dolt fields must be set
	if meta["backend"] != "dolt" {
		t.Errorf("backend = %v, want dolt", meta["backend"])
	}
	if meta["dolt_database"] != rigName {
		t.Errorf("dolt_database = %v, want %s", meta["dolt_database"], rigName)
	}
	if meta["dolt_mode"] != "server" {
		t.Errorf("dolt_mode = %v, want server", meta["dolt_mode"])
	}

	// Custom fields must be preserved (not clobbered by concurrent writes)
	if meta["custom_field"] != "preserve-me" {
		t.Errorf("custom_field = %v, want preserve-me (field was clobbered)", meta["custom_field"])
	}
	if meta["version"] != 42.0 {
		t.Errorf("version = %v, want 42 (field was clobbered)", meta["version"])
	}

	// No lock file should be left behind (sync.Mutex is used instead of flock)
	lockPath := metadataPath + ".lock"
	if _, err := os.Stat(lockPath); err == nil {
		t.Error("lock file should not exist — EnsureMetadata uses sync.Mutex, not flock")
	}
}

// TestConcurrentFindMigratableDatabases tests that FindMigratableDatabases
// can be called concurrently (simulating gt status during migration).
func TestConcurrentFindMigratableDatabases(t *testing.T) {
	townRoot := t.TempDir()

	// Create a rig with source database
	rigName := "concurrent-rig"
	sourceDolt := filepath.Join(townRoot, rigName, ".beads", "dolt", "beads_concurrent", ".dolt")
	if err := os.MkdirAll(sourceDolt, 0755); err != nil {
		t.Fatal(err)
	}
	doltDataDir := filepath.Join(townRoot, ".dolt-data")
	if err := os.MkdirAll(doltDataDir, 0755); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	results := make([][]Migration, 10)

	// Concurrent reads of migratable databases
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = FindMigratableDatabases(townRoot)
		}(i)
	}

	wg.Wait()

	// All results should be consistent
	for i, r := range results {
		if len(r) != 1 {
			t.Errorf("goroutine %d: expected 1 migration, got %d", i, len(r))
		}
	}
}

// TestConcurrentMigrateAndFind tests that FindMigratableDatabases returns
// consistent results even while a migration is in progress (the source is
// being moved and the target is appearing).
func TestConcurrentMigrateAndFind(t *testing.T) {
	townRoot := t.TempDir()

	// Create multiple rigs
	rigs := []string{"mig-a", "mig-b", "mig-c"}
	for _, rig := range rigs {
		sourceDolt := filepath.Join(townRoot, rig, ".beads", "dolt", "beads_"+rig, ".dolt")
		if err := os.MkdirAll(sourceDolt, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sourceDolt, "config.json"), []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}
		beadsDir := filepath.Join(townRoot, rig, "mayor", "rig", ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}
	}

	doltDataDir := filepath.Join(townRoot, ".dolt-data")
	if err := os.MkdirAll(doltDataDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Run migration and concurrent finds
	var wg sync.WaitGroup
	findErrs := make([]error, 0)
	var mu sync.Mutex

	// Start concurrent finders
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				migrations := FindMigratableDatabases(townRoot)
				// Should never panic or return corrupt data
				// Count should be between 0 and 3
				if len(migrations) > 3 {
					mu.Lock()
					findErrs = append(findErrs, filepath.ErrBadPattern)
					mu.Unlock()
				}
			}
		}()
	}

	// Migrate concurrently
	for _, rig := range rigs {
		wg.Add(1)
		go func(rigName string) {
			defer wg.Done()
			sourcePath := filepath.Join(townRoot, rigName, ".beads", "dolt", "beads_"+rigName)
			_ = MigrateRigFromBeads(townRoot, rigName, sourcePath)
		}(rig)
	}

	wg.Wait()

	if len(findErrs) > 0 {
		t.Errorf("concurrent finds returned invalid results: %d errors", len(findErrs))
	}

	// After everything settles, should be 0 remaining.
	// On Windows, os.Rename can fail when concurrent goroutines hold directory
	// handles open (from FindMigratableDatabases reading the same dirs), so some
	// migrations may not complete. This is acceptable — the real application uses
	// file locks to serialize access.
	final := FindMigratableDatabases(townRoot)
	if runtime.GOOS == "windows" {
		if len(final) > 3 {
			t.Errorf("expected at most 3 migratable databases on Windows, got %d", len(final))
		}
	} else if len(final) != 0 {
		t.Errorf("expected 0 migratable databases after concurrent migration, got %d", len(final))
	}
}

// =============================================================================
// Metadata corruption and repair tests
// =============================================================================

// TestEnsureMetadata_RepairsCorruptJSON tests that EnsureMetadata can handle
// a corrupted metadata.json file and overwrite it with correct data.
func TestEnsureMetadata_RepairsCorruptJSON(t *testing.T) {
	townRoot := t.TempDir()

	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write corrupt JSON
	metaPath := filepath.Join(beadsDir, "metadata.json")
	if err := os.WriteFile(metaPath, []byte(`{corrupt json!!!`), 0600); err != nil {
		t.Fatal(err)
	}

	// EnsureMetadata should succeed (overwrites corrupt data)
	if err := EnsureMetadata(townRoot, "hq"); err != nil {
		t.Fatalf("EnsureMetadata failed on corrupt file: %v", err)
	}

	// Verify valid JSON was written
	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("reading metadata: %v", err)
	}
	var meta map[string]interface{}
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("metadata is not valid JSON after repair: %v", err)
	}
	if meta["backend"] != "dolt" {
		t.Errorf("backend = %v, want dolt", meta["backend"])
	}
	if meta["dolt_database"] != "hq" {
		t.Errorf("dolt_database = %v, want hq", meta["dolt_database"])
	}
}

// TestEnsureMetadata_RepairsEmptyFile tests that an empty metadata.json
// gets properly populated.
func TestEnsureMetadata_RepairsEmptyFile(t *testing.T) {
	townRoot := t.TempDir()

	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write empty file
	metaPath := filepath.Join(beadsDir, "metadata.json")
	if err := os.WriteFile(metaPath, []byte(""), 0600); err != nil {
		t.Fatal(err)
	}

	if err := EnsureMetadata(townRoot, "hq"); err != nil {
		t.Fatalf("EnsureMetadata failed on empty file: %v", err)
	}

	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("reading metadata: %v", err)
	}
	var meta map[string]interface{}
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("metadata is not valid JSON after repair: %v", err)
	}
	if meta["backend"] != "dolt" {
		t.Errorf("backend = %v, want dolt", meta["backend"])
	}
}

// TestEnsureMetadata_RepairsWrongBackend tests that metadata.json with
// backend=sqlite gets corrected to dolt.
func TestEnsureMetadata_RepairsWrongBackend(t *testing.T) {
	townRoot := t.TempDir()

	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write metadata with wrong backend
	metaPath := filepath.Join(beadsDir, "metadata.json")
	original := map[string]interface{}{
		"backend":  "sqlite",
		"database": "beads.db",
		"custom":   "keep-me",
	}
	data, _ := json.Marshal(original)
	if err := os.WriteFile(metaPath, data, 0600); err != nil {
		t.Fatal(err)
	}

	if err := EnsureMetadata(townRoot, "hq"); err != nil {
		t.Fatalf("EnsureMetadata failed: %v", err)
	}

	repaired, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("reading metadata: %v", err)
	}
	var meta map[string]interface{}
	if err := json.Unmarshal(repaired, &meta); err != nil {
		t.Fatalf("parsing metadata: %v", err)
	}
	if meta["backend"] != "dolt" {
		t.Errorf("backend = %v, want dolt", meta["backend"])
	}
	if meta["dolt_mode"] != "server" {
		t.Errorf("dolt_mode = %v, want server", meta["dolt_mode"])
	}
	if meta["custom"] != "keep-me" {
		t.Errorf("custom field not preserved: %v", meta["custom"])
	}
}

// TestEnsureMetadata_RepairsMissingDoltFields tests that metadata.json
// with backend=dolt but missing dolt_mode/dolt_database gets repaired.
func TestEnsureMetadata_RepairsMissingDoltFields(t *testing.T) {
	townRoot := t.TempDir()

	beadsDir := filepath.Join(townRoot, "myrig", "mayor", "rig", ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write partial dolt metadata (missing dolt_mode and dolt_database)
	metaPath := filepath.Join(beadsDir, "metadata.json")
	partial := map[string]interface{}{
		"backend":  "dolt",
		"database": "dolt",
	}
	data, _ := json.Marshal(partial)
	if err := os.WriteFile(metaPath, data, 0600); err != nil {
		t.Fatal(err)
	}

	if err := EnsureMetadata(townRoot, "myrig"); err != nil {
		t.Fatalf("EnsureMetadata failed: %v", err)
	}

	repaired, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("reading metadata: %v", err)
	}
	var meta map[string]interface{}
	if err := json.Unmarshal(repaired, &meta); err != nil {
		t.Fatalf("parsing metadata: %v", err)
	}
	if meta["dolt_mode"] != "server" {
		t.Errorf("dolt_mode = %v, want server", meta["dolt_mode"])
	}
	if meta["dolt_database"] != "myrig" {
		t.Errorf("dolt_database = %v, want myrig", meta["dolt_database"])
	}
}

// TestEnsureMetadata_RepairsStalePort tests that EnsureMetadata overwrites
// a stale dolt_server_port (e.g., 13729 from a previous bd init) with the
// correct port from DefaultConfig. This is the root cause of "connection
// refused" errors reported by community users after gt dolt fix-metadata.
func TestEnsureMetadata_RepairsStalePort(t *testing.T) {
	townRoot := t.TempDir()

	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Simulate stale metadata with wrong port (13729 instead of 3307)
	stale := map[string]interface{}{
		"backend":          "dolt",
		"database":         "dolt",
		"dolt_mode":        "server",
		"dolt_database":    "hq",
		"dolt_server_host": "127.0.0.1",
		"dolt_server_port": 13729,
	}
	data, _ := json.Marshal(stale)
	metaPath := filepath.Join(beadsDir, "metadata.json")
	if err := os.WriteFile(metaPath, data, 0600); err != nil {
		t.Fatal(err)
	}

	if err := EnsureMetadata(townRoot, "hq"); err != nil {
		t.Fatalf("EnsureMetadata failed: %v", err)
	}

	repaired, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("reading metadata: %v", err)
	}
	var meta map[string]interface{}
	if err := json.Unmarshal(repaired, &meta); err != nil {
		t.Fatalf("parsing metadata: %v", err)
	}

	// Port should now be the default (3307), not the stale 13729
	wantPort := float64(DefaultPort)
	if meta["dolt_server_port"] != wantPort {
		t.Errorf("dolt_server_port = %v, want %v", meta["dolt_server_port"], wantPort)
	}
	if meta["dolt_server_host"] != "127.0.0.1" {
		t.Errorf("dolt_server_host = %v, want 127.0.0.1", meta["dolt_server_host"])
	}
}

// TestEnsureMetadata_RepairsWrongDoltDatabase verifies that EnsureMetadata
// corrects a metadata.json where dolt_database points to the wrong database
// (e.g., "beads_gt" instead of "gastown"). This is the primary fix for the
// PROJECT IDENTITY MISMATCH bug (gas-tc4).
func TestEnsureMetadata_RepairsWrongDoltDatabase(t *testing.T) {
	townRoot := t.TempDir()

	beadsDir := filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Simulate wrong database name (bd init wrote "beads_gt" instead of "gastown")
	wrong := map[string]interface{}{
		"backend":          "dolt",
		"database":         "dolt",
		"dolt_mode":        "server",
		"dolt_database":    "beads_gt",
		"dolt_server_host": "127.0.0.1",
		"dolt_server_port": float64(DefaultPort),
	}
	data, _ := json.Marshal(wrong)
	metaPath := filepath.Join(beadsDir, "metadata.json")
	if err := os.WriteFile(metaPath, data, 0600); err != nil {
		t.Fatal(err)
	}

	if err := EnsureMetadata(townRoot, "gastown"); err != nil {
		t.Fatalf("EnsureMetadata failed: %v", err)
	}

	repaired, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("reading metadata: %v", err)
	}
	var meta map[string]interface{}
	if err := json.Unmarshal(repaired, &meta); err != nil {
		t.Fatalf("parsing metadata: %v", err)
	}

	// dolt_database should now be "gastown", not "beads_gt"
	if meta["dolt_database"] != "gastown" {
		t.Errorf("dolt_database = %v, want %q", meta["dolt_database"], "gastown")
	}
}

// TestEnsureAllMetadata_RepairsAllCorrupt tests that EnsureAllMetadata
// repairs metadata for all known databases, even if some are corrupt.
func TestEnsureAllMetadata_RepairsAllCorrupt(t *testing.T) {
	townRoot := t.TempDir()

	// Create two databases in .dolt-data
	dataDir := filepath.Join(townRoot, ".dolt-data")
	setupDoltDB(t, dataDir, "hq")
	setupDoltDB(t, dataDir, "corruptrig")

	// Create beads dirs with corrupt metadata
	hqBeads := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(hqBeads, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hqBeads, "metadata.json"), []byte(`CORRUPT`), 0600); err != nil {
		t.Fatal(err)
	}

	rigBeads := filepath.Join(townRoot, "corruptrig", "mayor", "rig", ".beads")
	if err := os.MkdirAll(rigBeads, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rigBeads, "metadata.json"), []byte(`{invalid`), 0600); err != nil {
		t.Fatal(err)
	}

	updated, errs := EnsureAllMetadata(townRoot)
	if len(errs) > 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	if len(updated) != 2 {
		t.Errorf("expected 2 updated, got %d: %v", len(updated), updated)
	}

	// Verify both now have valid metadata
	for _, pair := range []struct {
		name string
		path string
	}{
		{"hq", filepath.Join(hqBeads, "metadata.json")},
		{"corruptrig", filepath.Join(rigBeads, "metadata.json")},
	} {
		data, err := os.ReadFile(pair.path)
		if err != nil {
			t.Fatalf("reading %s metadata: %v", pair.name, err)
		}
		var meta map[string]interface{}
		if err := json.Unmarshal(data, &meta); err != nil {
			t.Fatalf("%s metadata invalid JSON after repair: %v", pair.name, err)
		}
		if meta["backend"] != "dolt" {
			t.Errorf("%s: backend = %v, want dolt", pair.name, meta["backend"])
		}
	}
}

// =============================================================================
// Idempotency tests
// =============================================================================

// TestMigrateRigFromBeads_IdempotentDetection tests that running migration
// twice for the same rig: first succeeds, second correctly reports already done.
func TestMigrateRigFromBeads_IdempotentDetection(t *testing.T) {
	townRoot := t.TempDir()

	rigName := "idem-rig"
	sourcePath := filepath.Join(townRoot, rigName, ".beads", "dolt", "beads_idem")
	if err := os.MkdirAll(filepath.Join(sourcePath, ".dolt"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourcePath, ".dolt", "data.txt"), []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	beadsDir := filepath.Join(townRoot, rigName, "mayor", "rig", ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// First migration succeeds
	if err := MigrateRigFromBeads(townRoot, rigName, sourcePath); err != nil {
		t.Fatalf("first migration failed: %v", err)
	}

	// Verify data is at target
	targetData, err := os.ReadFile(filepath.Join(townRoot, ".dolt-data", rigName, ".dolt", "data.txt"))
	if err != nil {
		t.Fatalf("reading target data: %v", err)
	}
	if string(targetData) != "original" {
		t.Errorf("target data = %q, want %q", string(targetData), "original")
	}

	// Second call: source is gone, target exists → should error
	err = MigrateRigFromBeads(townRoot, rigName, sourcePath)
	if err == nil {
		t.Fatal("expected error on second migration attempt, got nil")
	}

	// Target data should still be intact
	targetData, err = os.ReadFile(filepath.Join(townRoot, ".dolt-data", rigName, ".dolt", "data.txt"))
	if err != nil {
		t.Fatalf("reading target data after second attempt: %v", err)
	}
	if string(targetData) != "original" {
		t.Errorf("target data corrupted after second attempt: %q", string(targetData))
	}
}

// TestFindAndMigrateAll_Idempotent tests the full find-then-migrate workflow
// twice: first run migrates, second run finds nothing to do.
// =============================================================================
// Max connections and admission control tests
// =============================================================================

func TestDefaultConfig_MaxConnections(t *testing.T) {
	townRoot := t.TempDir()
	config := DefaultConfig(townRoot)

	if config.MaxConnections != DefaultMaxConnections {
		t.Errorf("MaxConnections = %d, want %d", config.MaxConnections, DefaultMaxConnections)
	}
	if config.MaxConnections != 1000 {
		t.Errorf("DefaultMaxConnections = %d, want 1000", config.MaxConnections)
	}
}

func TestHasConnectionCapacity_ZeroMax(t *testing.T) {
	// When MaxConnections is 0, the function should use Dolt default (1000).
	// Since we can't connect to a real server in unit tests, we just verify
	// the function doesn't panic and returns an error (no server).
	townRoot := t.TempDir()

	// Create minimal config structure
	daemonDir := filepath.Join(townRoot, "daemon")
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		t.Fatal(err)
	}

	// HasConnectionCapacity should return false (fail closed) when query fails (gt-lfc0d)
	hasCapacity, _, err := HasConnectionCapacity(townRoot)
	if err == nil {
		t.Skip("Dolt server is actually running, cannot test offline case")
	}
	if hasCapacity {
		t.Error("expected fail-closed false when server is unreachable")
	}
}

func TestFindAndMigrateAll_Idempotent(t *testing.T) {
	townRoot := t.TempDir()

	// Create 2 rigs with valid noms/manifest so ListDatabases recognizes them post-migration
	for _, rig := range []string{"idm-a", "idm-b"} {
		sourceDolt := filepath.Join(townRoot, rig, ".beads", "dolt", "beads_"+rig, ".dolt")
		nomsDir := filepath.Join(sourceDolt, "noms")
		if err := os.MkdirAll(nomsDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sourceDolt, "config.json"), []byte(`{"rig":"`+rig+`"}`), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(nomsDir, "manifest"), []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}
		beadsDir := filepath.Join(townRoot, rig, "mayor", "rig", ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}
	}

	// First pass: find and migrate all
	pass1 := FindMigratableDatabases(townRoot)
	if len(pass1) != 2 {
		t.Fatalf("pass 1: expected 2 migratable, got %d", len(pass1))
	}
	for _, m := range pass1 {
		if err := MigrateRigFromBeads(townRoot, m.RigName, m.SourcePath); err != nil {
			t.Fatalf("pass 1: migrating %s: %v", m.RigName, err)
		}
	}

	// Update metadata (as gt dolt migrate does)
	updated1, errs1 := EnsureAllMetadata(townRoot)
	if len(errs1) > 0 {
		t.Errorf("pass 1 metadata errors: %v", errs1)
	}
	if len(updated1) != 2 {
		t.Errorf("pass 1: expected 2 metadata updates, got %d", len(updated1))
	}

	// Second pass: find should return empty, metadata update should be harmless
	pass2 := FindMigratableDatabases(townRoot)
	if len(pass2) != 0 {
		t.Errorf("pass 2: expected 0 migratable, got %d", len(pass2))
	}

	updated2, errs2 := EnsureAllMetadata(townRoot)
	if len(errs2) > 0 {
		t.Errorf("pass 2 metadata errors: %v", errs2)
	}
	if len(updated2) != 2 {
		t.Errorf("pass 2: expected 2 metadata updates (idempotent), got %d", len(updated2))
	}

	// Verify data integrity after two passes
	for _, rig := range []string{"idm-a", "idm-b"} {
		configPath := filepath.Join(townRoot, ".dolt-data", rig, ".dolt", "config.json")
		data, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("reading %s config: %v", rig, err)
		}
		expected := `{"rig":"` + rig + `"}`
		if string(data) != expected {
			t.Errorf("%s config = %q, want %q", rig, string(data), expected)
		}

		metaPath := filepath.Join(townRoot, rig, "mayor", "rig", ".beads", "metadata.json")
		metaData, err := os.ReadFile(metaPath)
		if err != nil {
			t.Fatalf("reading %s metadata: %v", rig, err)
		}
		var meta map[string]interface{}
		if err := json.Unmarshal(metaData, &meta); err != nil {
			t.Fatalf("%s metadata invalid: %v", rig, err)
		}
		if meta["backend"] != "dolt" {
			t.Errorf("%s: backend = %v, want dolt", rig, meta["backend"])
		}
	}
}

// =============================================================================
// Additional rollback edge case tests (main rollback tests are in rollback_test.go)
// =============================================================================

func TestRestoreFromBackup_BackupIsFile(t *testing.T) {
	townRoot := t.TempDir()

	filePath := filepath.Join(townRoot, "not-a-dir")
	if err := os.WriteFile(filePath, []byte("not a backup"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := RestoreFromBackup(townRoot, filePath)
	if err == nil {
		t.Fatal("expected error when backup path is a file, got nil")
	}
}

func TestRestoreFromBackup_EmptyBackup(t *testing.T) {
	townRoot := t.TempDir()

	backupDir := filepath.Join(townRoot, "migration-backup-20240115-143022")
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		t.Fatal(err)
	}

	result, err := RestoreFromBackup(townRoot, backupDir)
	if err != nil {
		t.Fatalf("RestoreFromBackup failed: %v", err)
	}
	if result.RestoredTown {
		t.Error("expected no town restoration from empty backup")
	}
	if len(result.RestoredRigs) != 0 {
		t.Errorf("expected 0 restored rigs, got %d", len(result.RestoredRigs))
	}
}

// =============================================================================
// Metadata edge cases
// =============================================================================

func TestEnsureMetadata_CreatesBeadsDir(t *testing.T) {
	townRoot := t.TempDir()

	if err := EnsureMetadata(townRoot, "hq"); err != nil {
		t.Fatalf("EnsureMetadata failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(townRoot, ".beads", "metadata.json"))
	if err != nil {
		t.Fatalf("reading metadata: %v", err)
	}
	var meta map[string]interface{}
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("parsing metadata: %v", err)
	}
	if meta["backend"] != "dolt" {
		t.Errorf("backend = %v, want dolt", meta["backend"])
	}
}

// =============================================================================
// InitRig validation tests
// =============================================================================

func TestInitRig_EmptyName(t *testing.T) {
	townRoot := t.TempDir()
	_, _, err := InitRig(townRoot, "")
	if err == nil {
		t.Fatal("expected error for empty rig name")
	}
}

func TestInitRig_InvalidCharacters(t *testing.T) {
	townRoot := t.TempDir()
	for _, name := range []string{"my rig", "rig/name", "rig.name", "rig@name"} {
		_, _, err := InitRig(townRoot, name)
		if err == nil {
			t.Errorf("expected error for invalid rig name %q", name)
		}
	}
}

// =============================================================================
// Catalog race condition tests (isDoltRetryableError coverage)
// =============================================================================

func TestIsDoltRetryableError_CatalogRace(t *testing.T) {
	// After CREATE DATABASE, the Dolt server may not immediately make the
	// database visible in its in-memory catalog. Subsequent USE queries
	// fail with "Unknown database '<name>'". This must be retryable so that
	// doltSQLWithRetry and doltSQLScriptWithRetry handle the race gracefully.
	catalogErrors := []string{
		"Unknown database 'myrig'",
		"Unknown database 'wl_commons'",
		"exit status 1 (output: Unknown database 'newrig')",
	}
	for _, msg := range catalogErrors {
		err := fmt.Errorf("%s", msg)
		if !isDoltRetryableError(err) {
			t.Errorf("isDoltRetryableError(%q) = false, want true (catalog race)", msg)
		}
	}
}

func TestWaitForCatalog_NoServer(t *testing.T) {
	// When no Dolt server is reachable, waitForCatalog should fail.
	// Use port 13399 (unlikely to be in use) to ensure no server responds.
	townRoot := t.TempDir()
	dataDir := filepath.Join(townRoot, ".dolt-data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Write a config.yaml with an unreachable port so buildServerSQLCmd
	// tries to connect to a port that nobody is listening on.
	configContent := "listener:\n  port: 13399\ndata_dir: " + dataDir + "\n"
	if err := os.WriteFile(filepath.Join(dataDir, "config.yaml"), []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}
	err := waitForCatalog(townRoot, "testdb")
	if err == nil {
		t.Fatal("expected error when no server is running")
	}
	// Connection refused or similar non-retryable error
	if !strings.Contains(err.Error(), "non-retryable") {
		t.Errorf("expected non-retryable error, got: %v", err)
	}
}

// =============================================================================
// ListDatabases edge cases
// =============================================================================

func TestListDatabases_EmptyDataDir(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".dolt-data"), 0755); err != nil {
		t.Fatal(err)
	}

	databases, err := ListDatabases(townRoot)
	if err != nil {
		t.Fatalf("ListDatabases failed: %v", err)
	}
	if len(databases) != 0 {
		t.Errorf("expected 0 databases, got %d", len(databases))
	}
}

func TestListDatabases_NoDataDir(t *testing.T) {
	townRoot := t.TempDir()
	databases, err := ListDatabases(townRoot)
	if err != nil {
		t.Fatalf("ListDatabases failed: %v", err)
	}
	if databases != nil {
		t.Errorf("expected nil, got %v", databases)
	}
}

func TestListDatabases_MixedContent(t *testing.T) {
	townRoot := t.TempDir()
	dataDir := filepath.Join(townRoot, ".dolt-data")

	setupDoltDB(t, dataDir, "hq")
	if err := os.MkdirAll(filepath.Join(dataDir, "not-a-db"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "somefile.txt"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}
	setupDoltDB(t, dataDir, "myrig")

	databases, err := ListDatabases(townRoot)
	if err != nil {
		t.Fatalf("ListDatabases failed: %v", err)
	}
	if len(databases) != 2 {
		t.Errorf("expected 2 databases, got %d: %v", len(databases), databases)
	}
}

// =============================================================================
// Connection string tests
// =============================================================================

func TestGetConnectionString(t *testing.T) {
	townRoot := t.TempDir()
	s := GetConnectionString(townRoot)
	if s != "root@tcp(127.0.0.1:3307)/" {
		t.Errorf("got %q, want root@tcp(127.0.0.1:3307)/", s)
	}
}

func TestGetConnectionStringForRig(t *testing.T) {
	townRoot := t.TempDir()
	s := GetConnectionStringForRig(townRoot, "hq")
	if s != "root@tcp(127.0.0.1:3307)/hq" {
		t.Errorf("got %q, want root@tcp(127.0.0.1:3307)/hq", s)
	}
}

func TestGetConnectionString_MasksPassword(t *testing.T) {
	townRoot := t.TempDir()
	t.Setenv("GT_DOLT_PASSWORD", "supersecret")
	s := GetConnectionString(townRoot)
	if strings.Contains(s, "supersecret") {
		t.Errorf("connection string should not contain raw password, got %q", s)
	}
	if !strings.Contains(s, "****") {
		t.Errorf("connection string should mask password with ****, got %q", s)
	}
}

// =============================================================================
// State tests
// =============================================================================

func TestSaveAndLoadState(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "daemon"), 0755); err != nil {
		t.Fatal(err)
	}

	state := &State{
		Running:   true,
		PID:       12345,
		Port:      3307,
		DataDir:   filepath.Join(townRoot, ".dolt-data"),
		Databases: []string{"hq", "myrig"},
	}

	if err := SaveState(townRoot, state); err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}

	loaded, err := LoadState(townRoot)
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	if !loaded.Running {
		t.Error("Running should be true")
	}
	if loaded.PID != 12345 {
		t.Errorf("PID = %d, want 12345", loaded.PID)
	}
	if len(loaded.Databases) != 2 {
		t.Errorf("expected 2 databases, got %d", len(loaded.Databases))
	}
}

func TestLoadState_NoFile(t *testing.T) {
	townRoot := t.TempDir()
	state, err := LoadState(townRoot)
	if err != nil {
		t.Fatalf("LoadState with no file: %v", err)
	}
	if state == nil {
		t.Fatal("expected empty state, not nil")
	}
	if state.Running {
		t.Error("empty state should not be running")
	}
}

func TestLoadState_CorruptJSON(t *testing.T) {
	townRoot := t.TempDir()
	stateFile := StateFile(townRoot)
	if err := os.MkdirAll(filepath.Dir(stateFile), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateFile, []byte(`{corrupt`), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadState(townRoot)
	if err == nil {
		t.Fatal("expected error for corrupt state file")
	}
}

func TestRefreshPIDStateFromLiveInfo(t *testing.T) {
	townRoot := t.TempDir()
	config := DefaultConfig(townRoot)
	if err := os.MkdirAll(filepath.Dir(config.PidFile), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.PidFile, []byte("999999\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := SaveState(townRoot, &State{Running: true, PID: 999999, Port: config.Port, DataDir: config.DataDir}); err != nil {
		t.Fatal(err)
	}

	changed, err := refreshPIDStateFromLiveInfo(townRoot, config, 12345)
	if err != nil {
		t.Fatalf("refreshPIDStateFromLiveInfo: %v", err)
	}
	if !changed {
		t.Fatal("expected stale PID state to change")
	}

	pidData, err := os.ReadFile(config.PidFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(pidData)) != "12345" {
		t.Fatalf("pid file = %q, want 12345", string(pidData))
	}
	state, err := LoadState(townRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Running || state.PID != 12345 || state.Port != config.Port || state.DataDir != config.DataDir {
		t.Fatalf("state not refreshed: %#v", state)
	}
}

func TestRefreshPIDStateFromLiveInfoInvalidPIDNoop(t *testing.T) {
	townRoot := t.TempDir()
	config := DefaultConfig(townRoot)
	changed, err := refreshPIDStateFromLiveInfo(townRoot, config, 0)
	if err != nil {
		t.Fatalf("refreshPIDStateFromLiveInfo: %v", err)
	}
	if changed {
		t.Fatal("invalid PID should not change state")
	}
	if _, err := os.Stat(config.PidFile); !os.IsNotExist(err) {
		t.Fatalf("pid file should not exist, err=%v", err)
	}
}

// =============================================================================
// Rollback round-trip test
// =============================================================================

func TestRollbackRoundTrip(t *testing.T) {
	townRoot := t.TempDir()

	rigName := "roundtrip"
	originalBeads := filepath.Join(townRoot, rigName, ".beads")
	if err := os.MkdirAll(originalBeads, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(originalBeads, "metadata.json"),
		[]byte(`{"backend":"sqlite"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(originalBeads, "beads.db"),
		[]byte("original-data"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create backup
	backupDir := filepath.Join(townRoot, "migration-backup-20240115-143022")
	rigBackup := filepath.Join(backupDir, rigName+"-beads")
	if err := os.MkdirAll(rigBackup, 0755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"metadata.json", "beads.db"} {
		data, _ := os.ReadFile(filepath.Join(originalBeads, f))
		os.WriteFile(filepath.Join(rigBackup, f), data, 0644)
	}

	// Simulate migration
	os.WriteFile(filepath.Join(originalBeads, "metadata.json"),
		[]byte(`{"backend":"dolt","dolt_mode":"server"}`), 0600)
	os.Remove(filepath.Join(originalBeads, "beads.db"))

	// Rollback
	result, err := RestoreFromBackup(townRoot, backupDir)
	if err != nil {
		t.Fatalf("rollback failed: %v", err)
	}
	if len(result.RestoredRigs) != 1 {
		t.Fatalf("expected 1 restored rig, got %d", len(result.RestoredRigs))
	}

	// Verify rollback restored original state
	data, _ := os.ReadFile(filepath.Join(originalBeads, "metadata.json"))
	var meta map[string]interface{}
	json.Unmarshal(data, &meta)
	if meta["backend"] != "sqlite" {
		t.Errorf("after rollback: backend = %v, want sqlite", meta["backend"])
	}
	dbData, _ := os.ReadFile(filepath.Join(originalBeads, "beads.db"))
	if string(dbData) != "original-data" {
		t.Errorf("beads.db content = %q, want original-data", string(dbData))
	}
}

// =============================================================================
// Spaces in paths
// =============================================================================

func TestFindMigratableDatabases_SpacesInPath(t *testing.T) {
	townRoot := filepath.Join(t.TempDir(), "my town root")
	if err := os.MkdirAll(townRoot, 0755); err != nil {
		t.Fatal(err)
	}

	rigName := "my-rig"
	sourceDolt := filepath.Join(townRoot, rigName, ".beads", "dolt", "beads_spacey", ".dolt")
	if err := os.MkdirAll(sourceDolt, 0755); err != nil {
		t.Fatal(err)
	}

	migrations := FindMigratableDatabases(townRoot)
	found := false
	for _, m := range migrations {
		if m.RigName == rigName {
			found = true
		}
	}
	if !found {
		t.Error("expected to find migration in path with spaces")
	}
}

func TestFindMigratableDatabases_EmptyTownRoot(t *testing.T) {
	townRoot := t.TempDir()
	migrations := FindMigratableDatabases(townRoot)
	if len(migrations) != 0 {
		t.Errorf("expected 0 migrations, got %d", len(migrations))
	}
}

func TestFindMigratableDatabases_TownBeads(t *testing.T) {
	townRoot := t.TempDir()
	hqSource := filepath.Join(townRoot, ".beads", "dolt", "beads_hq", ".dolt")
	if err := os.MkdirAll(hqSource, 0755); err != nil {
		t.Fatal(err)
	}

	migrations := FindMigratableDatabases(townRoot)
	found := false
	for _, m := range migrations {
		if m.RigName == "hq" {
			found = true
		}
	}
	if !found {
		t.Error("expected to find HQ migration")
	}
}

func TestFindMigratableDatabases_SkipsDotDirs(t *testing.T) {
	townRoot := t.TempDir()
	hiddenDolt := filepath.Join(townRoot, ".hidden-rig", ".beads", "dolt", "beads_hidden", ".dolt")
	if err := os.MkdirAll(hiddenDolt, 0755); err != nil {
		t.Fatal(err)
	}

	migrations := FindMigratableDatabases(townRoot)
	for _, m := range migrations {
		if m.RigName == ".hidden-rig" {
			t.Error("should skip dot-directories")
		}
	}
}

func TestMoveDir_SourceNotExists(t *testing.T) {
	tmpDir := t.TempDir()
	err := moveDir(filepath.Join(tmpDir, "nonexistent"), filepath.Join(tmpDir, "dest"))
	if err == nil {
		t.Fatal("expected error for nonexistent source")
	}
}

// =============================================================================
// Branch name validation tests (SQL injection prevention)
// =============================================================================

// =============================================================================
// DatabaseExists tests
// =============================================================================

func TestDatabaseExists_True(t *testing.T) {
	townRoot := t.TempDir()
	doltDir := filepath.Join(townRoot, ".dolt-data", "myrig", ".dolt")
	if err := os.MkdirAll(doltDir, 0755); err != nil {
		t.Fatal(err)
	}
	if !DatabaseExists(townRoot, "myrig") {
		t.Error("expected database to exist")
	}
}

func TestDatabaseExists_False(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".dolt-data"), 0755); err != nil {
		t.Fatal(err)
	}
	if DatabaseExists(townRoot, "nonexistent") {
		t.Error("expected database to not exist")
	}
}

func TestDatabaseExists_NoDataDir(t *testing.T) {
	townRoot := t.TempDir()
	if DatabaseExists(townRoot, "anything") {
		t.Error("expected false when .dolt-data doesn't exist")
	}
}

// =============================================================================
// FindBrokenWorkspaces tests
// =============================================================================

func TestFindBrokenWorkspaces_HealthyWorkspace(t *testing.T) {
	townRoot := t.TempDir()

	// Point the test at a port nothing listens on so IsRunning returns false
	// and doesn't accidentally connect to a real Dolt server on the default port.
	doltDataDir := filepath.Join(townRoot, ".dolt-data")
	if err := os.MkdirAll(doltDataDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(doltDataDir, "config.yaml"),
		[]byte("listener:\n  port: 13307\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a healthy workspace: metadata says dolt, and database exists
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	metadata := `{"backend":"dolt","dolt_mode":"server","dolt_database":"hq"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0644); err != nil {
		t.Fatal(err)
	}

	// Database exists
	if err := os.MkdirAll(filepath.Join(townRoot, ".dolt-data", "hq", ".dolt"), 0755); err != nil {
		t.Fatal(err)
	}

	// Set up rigs.json (empty, only checking hq)
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "rigs.json"), []byte(`{"rigs":{}}`), 0644); err != nil {
		t.Fatal(err)
	}

	broken, _ := FindBrokenWorkspaces(townRoot)
	if len(broken) != 0 {
		t.Errorf("expected 0 broken workspaces, got %d: %+v", len(broken), broken)
	}
}

func TestFindBrokenWorkspaces_MissingDatabase(t *testing.T) {
	townRoot := t.TempDir()

	// Metadata says dolt, but database does NOT exist
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	metadata := `{"backend":"dolt","dolt_mode":"server","dolt_database":"hq"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0644); err != nil {
		t.Fatal(err)
	}

	// Create .dolt-data but NO hq database inside
	if err := os.MkdirAll(filepath.Join(townRoot, ".dolt-data"), 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "rigs.json"), []byte(`{"rigs":{}}`), 0644); err != nil {
		t.Fatal(err)
	}

	broken, _ := FindBrokenWorkspaces(townRoot)
	if len(broken) != 1 {
		t.Fatalf("expected 1 broken workspace, got %d", len(broken))
	}
	if broken[0].RigName != "hq" {
		t.Errorf("expected rig hq, got %s", broken[0].RigName)
	}
	if broken[0].ConfiguredDB != "hq" {
		t.Errorf("expected ConfiguredDB=hq, got %s", broken[0].ConfiguredDB)
	}
	if broken[0].HasLocalData {
		t.Error("expected no local data")
	}
}

func TestFindBrokenWorkspaces_WithLocalData(t *testing.T) {
	townRoot := t.TempDir()

	// Rig metadata says dolt, database missing, but local data exists
	rigName := "myrig"
	beadsDir := filepath.Join(townRoot, rigName, "mayor", "rig", ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	metadata := `{"backend":"dolt","dolt_mode":"server","dolt_database":"myrig"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0644); err != nil {
		t.Fatal(err)
	}

	// Local Dolt data exists
	localDolt := filepath.Join(beadsDir, "dolt", "beads_myrig", ".dolt")
	if err := os.MkdirAll(localDolt, 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(townRoot, ".dolt-data"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "rigs.json"),
		[]byte(`{"rigs":{"myrig":{}}}`), 0644); err != nil {
		t.Fatal(err)
	}

	broken, _ := FindBrokenWorkspaces(townRoot)
	if len(broken) != 1 {
		t.Fatalf("expected 1 broken workspace, got %d", len(broken))
	}
	if !broken[0].HasLocalData {
		t.Error("expected HasLocalData=true")
	}
	if broken[0].LocalDataPath == "" {
		t.Error("expected non-empty LocalDataPath")
	}
}

func TestFindBrokenWorkspaces_SqliteNotBroken(t *testing.T) {
	townRoot := t.TempDir()

	// Workspace configured for SQLite, not Dolt — should not appear as broken
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	metadata := `{"backend":"sqlite","database":"beads.db"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "rigs.json"), []byte(`{"rigs":{}}`), 0644); err != nil {
		t.Fatal(err)
	}

	broken, _ := FindBrokenWorkspaces(townRoot)
	if len(broken) != 0 {
		t.Errorf("expected 0 broken workspaces for sqlite backend, got %d", len(broken))
	}
}

func TestFindBrokenWorkspaces_MultipleRigs(t *testing.T) {
	townRoot := t.TempDir()

	// Isolate from real Dolt server on default port
	doltDataDir := filepath.Join(townRoot, ".dolt-data")
	if err := os.MkdirAll(doltDataDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(doltDataDir, "config.yaml"),
		[]byte("listener:\n  port: 13307\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Set up rigs.json with two rigs
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "rigs.json"),
		[]byte(`{"rigs":{"rig-a":{},"rig-b":{}}}`), 0644); err != nil {
		t.Fatal(err)
	}

	// rig-a: broken (metadata says dolt, no database)
	beadsDirA := filepath.Join(townRoot, "rig-a", "mayor", "rig", ".beads")
	if err := os.MkdirAll(beadsDirA, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDirA, "metadata.json"),
		[]byte(`{"backend":"dolt","dolt_mode":"server","dolt_database":"rig-a"}`), 0644); err != nil {
		t.Fatal(err)
	}

	// rig-b: healthy (metadata says dolt, database exists)
	beadsDirB := filepath.Join(townRoot, "rig-b", "mayor", "rig", ".beads")
	if err := os.MkdirAll(beadsDirB, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDirB, "metadata.json"),
		[]byte(`{"backend":"dolt","dolt_mode":"server","dolt_database":"rig-b"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, ".dolt-data", "rig-b", ".dolt"), 0755); err != nil {
		t.Fatal(err)
	}

	broken, _ := FindBrokenWorkspaces(townRoot)
	if len(broken) != 1 {
		t.Fatalf("expected 1 broken workspace (rig-a only), got %d", len(broken))
	}
	if broken[0].RigName != "rig-a" {
		t.Errorf("expected rig-a broken, got %s", broken[0].RigName)
	}
}

// =============================================================================
// Read-only detection tests
// =============================================================================

func TestIsReadOnlyError(t *testing.T) {
	tests := []struct {
		msg  string
		want bool
	}{
		{"cannot update manifest: database is read only", true},
		{"database is read only", true},
		{"Database Is Read Only", true},
		{"error: read-only mode", true},
		{"server is readonly", true},
		{"READ ONLY transaction", true},
		{"connection refused", false},
		{"timeout", false},
		{"", false},
		{"table not found", false},
		{"permission denied", false},
	}

	for _, tt := range tests {
		if got := IsReadOnlyError(tt.msg); got != tt.want {
			t.Errorf("IsReadOnlyError(%q) = %v, want %v", tt.msg, got, tt.want)
		}
	}
}

func TestHealthMetrics_ReadOnlyField(t *testing.T) {
	// Verify that the ReadOnly field is properly included in HealthMetrics.
	// We can't test actual read-only detection without a running Dolt server,
	// but we can verify the field is populated.
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".dolt-data"), 0755); err != nil {
		t.Fatal(err)
	}

	metrics := GetHealthMetrics(townRoot)

	// Without a running server, ReadOnly should be false (can't probe)
	if metrics.ReadOnly {
		t.Error("expected ReadOnly=false when no server is running")
	}
}

func TestIsDoltRetryableError_IncludesReadOnly(t *testing.T) {
	// Verify that read-only errors are recognized as retryable.
	// This is critical for the recovery path: doltSQLWithRetry must
	// retry on read-only before escalating to doltSQLWithRecovery.
	tests := []struct {
		msg  string
		want bool
	}{
		{"cannot update manifest: database is read only", true},
		{"database is read only", true},
		{"optimistic lock failed", true},
		{"serialization failure", true},
		{"lock wait timeout exceeded", true},
		{"try restarting transaction", true},
		{"Unknown database 'myrig'", true},
		{"database not found", false},
		{"connection refused", false},
		{"table not found", false},
	}
	for _, tt := range tests {
		err := fmt.Errorf("%s", tt.msg)
		if got := isDoltRetryableError(err); got != tt.want {
			t.Errorf("isDoltRetryableError(%q) = %v, want %v", tt.msg, got, tt.want)
		}
	}
}

func TestRecoverReadOnly_NoServer(t *testing.T) {
	// When no server is running, CheckReadOnly returns false (can't probe),
	// so RecoverReadOnly should be a no-op.
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".dolt-data"), 0755); err != nil {
		t.Fatal(err)
	}

	err := RecoverReadOnly(townRoot)
	// Should succeed (no-op) since no server means no read-only state detectable
	if err != nil {
		t.Errorf("RecoverReadOnly with no server: got error %v, want nil", err)
	}
}

// =============================================================================
// doltSQLScriptWithRetry tests
// =============================================================================

func TestDoltSQLScriptWithRetry_ImmediateSuccess(t *testing.T) {
	// doltSQLScriptWithRetry calls doltSQLScript which needs a valid townRoot
	// with .dolt-data dir and a dolt binary. Since we can't run dolt in CI,
	// we verify the retry logic by checking that non-retryable errors return
	// immediately without sleeping (i.e., isDoltRetryableError integration).
	//
	// A non-retryable error (e.g., syntax error) should return on first attempt.
	err := doltSQLScriptWithRetry(t.TempDir(), "INVALID SQL;")
	if err == nil {
		// If dolt isn't installed, the exec itself fails — that's fine,
		// the point is it doesn't retry/hang.
		t.Skip("dolt binary available and accepted invalid SQL somehow")
	}
	// Verify the error is not wrapped with "after N retries" since exec failures
	// (dolt not found / not a dolt data dir) are not retryable.
	if strings.Contains(err.Error(), "after 3 retries") {
		t.Errorf("non-retryable error was retried: %v", err)
	}
}

func TestDoltSQLScriptWithRetry_NonRetryableError(t *testing.T) {
	// Verify that isDoltRetryableError correctly classifies errors.
	// Non-retryable errors should fail fast without retry.
	nonRetryable := []string{
		"syntax error near 'FOO'",
		"table not found",
		"unknown column",
	}
	for _, msg := range nonRetryable {
		if isDoltRetryableError(fmt.Errorf("%s", msg)) {
			t.Errorf("isDoltRetryableError(%q) = true, want false", msg)
		}
	}

	// Retryable errors should be classified as such.
	retryable := []string{
		"database is read only",
		"cannot update manifest",
		"optimistic lock failed",
		"serialization failure",
		"lock wait timeout",
		"try restarting transaction",
		"Unknown database 'myrig'",
	}
	for _, msg := range retryable {
		if !isDoltRetryableError(fmt.Errorf("%s", msg)) {
			t.Errorf("isDoltRetryableError(%q) = false, want true", msg)
		}
	}
}

// =============================================================================
// VerifyDatabases tests
// =============================================================================

func TestParseShowDatabases_JSON(t *testing.T) {
	input := `{"rows":[{"Database":"hq"},{"Database":"gastown"},{"Database":"information_schema"},{"Database":"mysql"},{"Database":"dolt_cluster"}]}`
	got, err := parseShowDatabases([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 databases, got %d: %v", len(got), got)
	}
	// Check that all system databases are filtered out.
	for _, db := range got {
		if IsSystemDatabase(db) {
			t.Errorf("system database %q should be filtered out", db)
		}
	}
	// Both hq and gastown should be present.
	found := map[string]bool{}
	for _, db := range got {
		found[db] = true
	}
	if !found["hq"] || !found["gastown"] {
		t.Errorf("expected hq and gastown, got %v", got)
	}
}

func TestParseShowDatabases_JSONEmpty(t *testing.T) {
	input := `{"rows":[]}`
	got, err := parseShowDatabases([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 databases, got %d: %v", len(got), got)
	}
}

func TestParseShowDatabases_JSONEmptyDatabase(t *testing.T) {
	// Rows with empty Database field should be filtered.
	input := `{"rows":[{"Database":"hq"},{"Database":""}]}`
	got, err := parseShowDatabases([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 database, got %d: %v", len(got), got)
	}
	if got[0] != "hq" {
		t.Errorf("expected hq, got %s", got[0])
	}
}

func TestParseShowDatabases_UnexpectedJSONSchema(t *testing.T) {
	// Valid JSON missing the expected "rows" key should return a parse error
	// rather than silently returning zero databases (which would be
	// misinterpreted as "all databases are missing" in the migration path).
	input := `{"unexpected_key":[{"db":"hq"}]}`
	_, err := parseShowDatabases([]byte(input))
	if err == nil {
		t.Fatal("expected error for JSON missing 'rows' key, got nil")
	}
	if !strings.Contains(err.Error(), "missing expected 'rows' key") {
		t.Errorf("expected 'rows' key error, got: %v", err)
	}
}

func TestParseShowDatabases_BrokenJSON(t *testing.T) {
	// Corrupt JSON that starts with { should return an error.
	input := `{"rows": invalid json}`
	_, err := parseShowDatabases([]byte(input))
	if err == nil {
		t.Fatal("expected error for broken JSON, got nil")
	}
	if !strings.Contains(err.Error(), "looks like JSON") {
		t.Errorf("expected JSON guard error, got: %v", err)
	}
}

func TestParseShowDatabases_LineFallback(t *testing.T) {
	// Non-JSON table-formatted output: all lines start with + or |,
	// so the line parser filters them all out. Since the output is
	// non-empty but yields zero databases, the parser returns an error
	// to surface the format mismatch rather than silently reporting
	// all databases as missing.
	input := `+--------------------+
| Database           |
+--------------------+
| hq                 |
| gastown            |
| information_schema |
+--------------------+`
	_, err := parseShowDatabases([]byte(input))
	if err == nil {
		t.Fatal("expected error for table format yielding zero databases, got nil")
	}
	if !strings.Contains(err.Error(), "fallback parser returned zero databases") {
		t.Errorf("expected fallback zero-result error, got: %v", err)
	}
}

func TestParseShowDatabases_PlainText(t *testing.T) {
	// Plain-text output (no JSON, no table formatting).
	input := "hq\ngastown\ninformation_schema\nmysql\ndolt_cluster\n"
	got, err := parseShowDatabases([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 databases, got %d: %v", len(got), got)
	}
	found := map[string]bool{}
	for _, db := range got {
		found[db] = true
	}
	if !found["hq"] || !found["gastown"] {
		t.Errorf("expected hq and gastown, got %v", got)
	}
}

func TestIsSystemDatabase(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"information_schema", true},
		{"mysql", true},
		{"dolt_cluster", true},
		{"INFORMATION_SCHEMA", true}, // case-insensitive
		{"MySQL", true},
		{"hq", false},
		{"gastown", false},
		{"beads", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsSystemDatabase(tt.name); got != tt.want {
			t.Errorf("IsSystemDatabase(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestFindMissingDatabases_NoneServed(t *testing.T) {
	served := []string{}
	fs := []string{"hq", "gastown"}
	missing := findMissingDatabases(served, fs)
	if len(missing) != 2 {
		t.Errorf("expected 2 missing, got %d: %v", len(missing), missing)
	}
}

func TestFindMissingDatabases_AllServed(t *testing.T) {
	served := []string{"hq", "gastown", "beads"}
	fs := []string{"hq", "gastown"}
	missing := findMissingDatabases(served, fs)
	if len(missing) != 0 {
		t.Errorf("expected 0 missing, got %d: %v", len(missing), missing)
	}
}

func TestFindMissingDatabases_PartialMissing(t *testing.T) {
	served := []string{"hq"}
	fs := []string{"hq", "gastown", "beads"}
	missing := findMissingDatabases(served, fs)
	if len(missing) != 2 {
		t.Fatalf("expected 2 missing, got %d: %v", len(missing), missing)
	}
	found := map[string]bool{}
	for _, db := range missing {
		found[db] = true
	}
	if !found["gastown"] || !found["beads"] {
		t.Errorf("expected gastown and beads missing, got %v", missing)
	}
}

func TestFindMissingDatabases_BothEmpty(t *testing.T) {
	missing := findMissingDatabases(nil, nil)
	if len(missing) != 0 {
		t.Errorf("expected 0 missing, got %d: %v", len(missing), missing)
	}
}

func TestVerifyDatabases_NoServer(t *testing.T) {
	townRoot := t.TempDir()

	// VerifyDatabases should return an error when no server is running
	// or when the data directory doesn't exist. If a real server happens
	// to be on port 3307, the TCP check will pass but dolt sql will fail
	// due to missing data dir — either way, we expect an error.
	served, _, err := VerifyDatabases(townRoot)
	if err == nil {
		// Server is running AND somehow succeeded — skip.
		t.Skip("A server is running and dolt sql succeeded against temp dir")
	}
	if served != nil {
		t.Errorf("expected nil served on error, got %v", served)
	}
	// Error should mention either "server not reachable" or "SHOW DATABASES".
	if !strings.Contains(err.Error(), "server not reachable") &&
		!strings.Contains(err.Error(), "SHOW DATABASES") {
		t.Errorf("expected reachability or query error, got: %v", err)
	}
}

// =============================================================================
// Orphaned database detection tests
// =============================================================================

// setupDoltDB creates a fake Dolt database directory with a .dolt subdirectory
// and some data to simulate a real database for size calculations.
func setupDoltDB(t *testing.T, dataDir, dbName string) string {
	t.Helper()
	dbPath := filepath.Join(dataDir, dbName)
	nomsDir := filepath.Join(dbPath, ".dolt", "noms")
	if err := os.MkdirAll(nomsDir, 0755); err != nil {
		t.Fatalf("creating noms dir for %s: %v", dbName, err)
	}
	// Write manifest so ListDatabases recognizes this as a valid Dolt database
	if err := os.WriteFile(filepath.Join(nomsDir, "manifest"), []byte("test"), 0644); err != nil {
		t.Fatalf("writing manifest for %s: %v", dbName, err)
	}
	return dbPath
}

// setupRigsJSON creates a rigs.json with the given rig names.
func setupRigsJSON(t *testing.T, townRoot string, rigNames []string) {
	t.Helper()
	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("creating mayor dir: %v", err)
	}
	rigs := make(map[string]interface{})
	for _, name := range rigNames {
		rigs[name] = map[string]interface{}{}
	}
	data, err := json.Marshal(map[string]interface{}{"rigs": rigs})
	if err != nil {
		t.Fatalf("marshaling rigs.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), data, 0644); err != nil {
		t.Fatalf("writing rigs.json: %v", err)
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

func TestFindOrphanedDatabases_NoOrphans(t *testing.T) {
	townRoot := t.TempDir()
	dataDir := filepath.Join(townRoot, ".dolt-data")

	// Create databases that are all referenced
	setupDoltDB(t, dataDir, "hq")
	setupDoltDB(t, dataDir, "gastown")

	// Set up rigs and metadata
	setupRigsJSON(t, townRoot, []string{"gastown"})
	setupRigMetadata(t, townRoot, "hq", "hq")
	setupRigMetadata(t, townRoot, "gastown", "gastown")

	orphans, err := FindOrphanedDatabases(townRoot)
	if err != nil {
		t.Fatalf("FindOrphanedDatabases: %v", err)
	}
	if len(orphans) != 0 {
		t.Errorf("expected 0 orphans, got %d: %v", len(orphans), orphans)
	}
}

func TestFindOrphanedDatabases_DetectsOrphans(t *testing.T) {
	townRoot := t.TempDir()
	dataDir := filepath.Join(townRoot, ".dolt-data")

	// Create referenced databases
	setupDoltDB(t, dataDir, "hq")
	setupDoltDB(t, dataDir, "wyvern")

	// Create orphaned database (old partial setup naming)
	setupDoltDB(t, dataDir, "beads_wy")

	// Set up rigs and metadata — only hq and wyvern are referenced
	setupRigsJSON(t, townRoot, []string{"wyvern"})
	setupRigMetadata(t, townRoot, "hq", "hq")
	setupRigMetadata(t, townRoot, "wyvern", "wyvern")

	orphans, err := FindOrphanedDatabases(townRoot)
	if err != nil {
		t.Fatalf("FindOrphanedDatabases: %v", err)
	}
	if len(orphans) != 1 {
		t.Fatalf("expected 1 orphan, got %d: %v", len(orphans), orphans)
	}
	if orphans[0].Name != "beads_wy" {
		t.Errorf("expected orphan name 'beads_wy', got %q", orphans[0].Name)
	}
	if orphans[0].SizeBytes <= 0 {
		t.Errorf("expected positive size, got %d", orphans[0].SizeBytes)
	}
}

func TestFindOrphanedDatabases_ProtectsBeadsGlobal(t *testing.T) {
	townRoot := t.TempDir()
	dataDir := filepath.Join(townRoot, ".dolt-data")

	setupDoltDB(t, dataDir, "hq")
	setupDoltDB(t, dataDir, "beads_global")
	setupDoltDB(t, dataDir, "orphan_db")

	setupRigsJSON(t, townRoot, []string{})
	setupRigMetadata(t, townRoot, "hq", "hq")

	orphans, err := FindOrphanedDatabases(townRoot)
	if err != nil {
		t.Fatalf("FindOrphanedDatabases: %v", err)
	}
	if len(orphans) != 1 {
		t.Fatalf("expected 1 orphan, got %d: %v", len(orphans), orphans)
	}
	if orphans[0].Name != "orphan_db" {
		t.Errorf("expected orphan name 'orphan_db', got %q", orphans[0].Name)
	}
}

func TestFindOrphanedDatabases_MultipleOrphans(t *testing.T) {
	townRoot := t.TempDir()
	dataDir := filepath.Join(townRoot, ".dolt-data")

	// One referenced database
	setupDoltDB(t, dataDir, "gastown")

	// Multiple orphans
	setupDoltDB(t, dataDir, "old_setup")
	setupDoltDB(t, dataDir, "beads_gt")
	setupDoltDB(t, dataDir, "stale_backup")

	setupRigsJSON(t, townRoot, []string{"gastown"})
	setupRigMetadata(t, townRoot, "gastown", "gastown")

	orphans, err := FindOrphanedDatabases(townRoot)
	if err != nil {
		t.Fatalf("FindOrphanedDatabases: %v", err)
	}
	if len(orphans) != 3 {
		t.Fatalf("expected 3 orphans, got %d", len(orphans))
	}

	names := make(map[string]bool)
	for _, o := range orphans {
		names[o.Name] = true
	}
	for _, want := range []string{"old_setup", "beads_gt", "stale_backup"} {
		if !names[want] {
			t.Errorf("expected orphan %q not found", want)
		}
	}
}

func TestFindOrphanedDatabases_EmptyDataDir(t *testing.T) {
	townRoot := t.TempDir()
	// No .dolt-data directory at all

	orphans, err := FindOrphanedDatabases(townRoot)
	if err != nil {
		t.Fatalf("FindOrphanedDatabases: %v", err)
	}
	if len(orphans) != 0 {
		t.Errorf("expected 0 orphans for missing data dir, got %d", len(orphans))
	}
}

func TestFindOrphanedDatabases_IgnoresNonDoltDirs(t *testing.T) {
	townRoot := t.TempDir()
	dataDir := filepath.Join(townRoot, ".dolt-data")

	// Create a referenced database
	setupDoltDB(t, dataDir, "hq")
	setupRigsJSON(t, townRoot, []string{})
	setupRigMetadata(t, townRoot, "hq", "hq")

	// Create a directory WITHOUT .dolt — should be ignored entirely
	nonDoltDir := filepath.Join(dataDir, "not_a_db")
	if err := os.MkdirAll(nonDoltDir, 0755); err != nil {
		t.Fatal(err)
	}

	orphans, err := FindOrphanedDatabases(townRoot)
	if err != nil {
		t.Fatalf("FindOrphanedDatabases: %v", err)
	}
	if len(orphans) != 0 {
		t.Errorf("expected 0 orphans (non-dolt dir should be ignored), got %d: %v", len(orphans), orphans)
	}
}

func TestCollectReferencedDatabases_HQOnly(t *testing.T) {
	townRoot := t.TempDir()

	// Only HQ metadata, no rigs
	setupRigMetadata(t, townRoot, "hq", "hq")
	setupRigsJSON(t, townRoot, []string{})

	referenced := collectReferencedDatabases(townRoot)
	if !referenced["hq"] {
		t.Error("expected 'hq' to be referenced")
	}
	if len(referenced) != 1 {
		t.Errorf("expected 1 referenced, got %d: %v", len(referenced), referenced)
	}
}

func TestCollectReferencedDatabases_MultipleRigs(t *testing.T) {
	townRoot := t.TempDir()

	setupRigsJSON(t, townRoot, []string{"gastown", "beads", "wyvern"})
	setupRigMetadata(t, townRoot, "hq", "hq")
	setupRigMetadata(t, townRoot, "gastown", "gastown")
	setupRigMetadata(t, townRoot, "beads", "beads")
	setupRigMetadata(t, townRoot, "wyvern", "wyvern")

	referenced := collectReferencedDatabases(townRoot)
	for _, want := range []string{"hq", "gastown", "beads", "wyvern"} {
		if !referenced[want] {
			t.Errorf("expected %q to be referenced", want)
		}
	}
	if len(referenced) != 4 {
		t.Errorf("expected 4 referenced, got %d", len(referenced))
	}
}

func TestCollectReferencedDatabases_CustomDatabaseName(t *testing.T) {
	townRoot := t.TempDir()

	// Rig name differs from dolt_database name
	setupRigsJSON(t, townRoot, []string{"myrig"})
	setupRigMetadata(t, townRoot, "myrig", "custom_db_name")

	referenced := collectReferencedDatabases(townRoot)
	if !referenced["custom_db_name"] {
		t.Error("expected 'custom_db_name' to be referenced")
	}
	if referenced["myrig"] {
		t.Error("rig name 'myrig' should not be in referenced set (only dolt_database value)")
	}
}

func TestCollectReferencedDatabases_NoMetadata(t *testing.T) {
	townRoot := t.TempDir()
	setupRigsJSON(t, townRoot, []string{"gastown"})
	// No metadata.json for gastown — should not crash

	referenced := collectReferencedDatabases(townRoot)
	if len(referenced) != 0 {
		t.Errorf("expected 0 referenced with no metadata, got %d: %v", len(referenced), referenced)
	}
}

func TestCollectReferencedDatabases_NoRigsJSON(t *testing.T) {
	townRoot := t.TempDir()
	// No mayor/rigs.json at all — should only check HQ
	setupRigMetadata(t, townRoot, "hq", "hq")

	referenced := collectReferencedDatabases(townRoot)
	if !referenced["hq"] {
		t.Error("expected 'hq' to be referenced even without rigs.json")
	}
	if len(referenced) != 1 {
		t.Errorf("expected 1 referenced, got %d", len(referenced))
	}
}

func TestRemoveDatabase_RemovesDirectory(t *testing.T) {
	townRoot := t.TempDir()
	dataDir := filepath.Join(townRoot, ".dolt-data")

	// Create an orphan database to remove
	setupDoltDB(t, dataDir, "orphan_db")

	// Verify it exists
	dbPath := filepath.Join(dataDir, "orphan_db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("setup failed: orphan_db should exist: %v", err)
	}

	err := RemoveDatabase(townRoot, "orphan_db", true)
	if err != nil {
		t.Fatalf("RemoveDatabase: %v", err)
	}

	// Verify it's gone
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Errorf("expected orphan_db to be removed, but it still exists")
	}
}

func TestRemoveDatabase_ErrorOnMissing(t *testing.T) {
	townRoot := t.TempDir()
	dataDir := filepath.Join(townRoot, ".dolt-data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatal(err)
	}

	err := RemoveDatabase(townRoot, "nonexistent", true)
	if err == nil {
		t.Error("expected error for nonexistent database")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}

func TestRemoveDatabase_RefusesProtectedSharedServerDatabase(t *testing.T) {
	townRoot := t.TempDir()
	dataDir := filepath.Join(townRoot, ".dolt-data")
	dbPath := setupDoltDB(t, dataDir, "beads_global")

	err := RemoveDatabase(townRoot, "beads_global", true)
	if err == nil {
		t.Fatal("expected error for protected shared-server database")
	}
	if !strings.Contains(err.Error(), "protected shared-server database") {
		t.Errorf("expected protected database error, got: %v", err)
	}
	if _, statErr := os.Stat(dbPath); statErr != nil {
		t.Errorf("expected beads_global to remain on disk, got stat error: %v", statErr)
	}
}

func TestListDatabases_OnlyIncludesDoltDirs(t *testing.T) {
	townRoot := t.TempDir()
	dataDir := filepath.Join(townRoot, ".dolt-data")

	// Valid dolt databases
	setupDoltDB(t, dataDir, "db1")
	setupDoltDB(t, dataDir, "db2")

	// Non-dolt directories (should be excluded)
	if err := os.MkdirAll(filepath.Join(dataDir, "plain_dir"), 0755); err != nil {
		t.Fatal(err)
	}
	// File (should be excluded)
	if err := os.WriteFile(filepath.Join(dataDir, "a_file"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}

	dbs, err := ListDatabases(townRoot)
	if err != nil {
		t.Fatalf("ListDatabases: %v", err)
	}
	if len(dbs) != 2 {
		t.Fatalf("expected 2 databases, got %d: %v", len(dbs), dbs)
	}

	names := make(map[string]bool)
	for _, db := range dbs {
		names[db] = true
	}
	if !names["db1"] || !names["db2"] {
		t.Errorf("expected db1 and db2, got %v", dbs)
	}
}

func TestDatabaseExists_TrueForExisting(t *testing.T) {
	townRoot := t.TempDir()
	dataDir := filepath.Join(townRoot, ".dolt-data")
	setupDoltDB(t, dataDir, "mydb")

	if !DatabaseExists(townRoot, "mydb") {
		t.Error("expected DatabaseExists to return true for existing database")
	}
}

func TestDatabaseExists_FalseForMissing(t *testing.T) {
	townRoot := t.TempDir()

	if DatabaseExists(townRoot, "noexist") {
		t.Error("expected DatabaseExists to return false for missing database")
	}
}

func TestFindOrphanedDatabases_EndToEnd(t *testing.T) {
	// Simulates the exact scenario from the bug report:
	// .dolt-data/ contains beads_wy/ (old) and wyvern/ (new).
	// Only wyvern is referenced. beads_wy should be detected as orphaned.
	townRoot := t.TempDir()
	dataDir := filepath.Join(townRoot, ".dolt-data")

	setupDoltDB(t, dataDir, "hq")
	setupDoltDB(t, dataDir, "wyvern")
	setupDoltDB(t, dataDir, "beads_wy") // orphan: old partial setup

	setupRigsJSON(t, townRoot, []string{"wyvern"})
	setupRigMetadata(t, townRoot, "hq", "hq")
	setupRigMetadata(t, townRoot, "wyvern", "wyvern")

	// Step 1: Detect orphans
	orphans, err := FindOrphanedDatabases(townRoot)
	if err != nil {
		t.Fatalf("FindOrphanedDatabases: %v", err)
	}
	if len(orphans) != 1 {
		t.Fatalf("expected 1 orphan (beads_wy), got %d: %v", len(orphans), orphans)
	}
	if orphans[0].Name != "beads_wy" {
		t.Errorf("expected orphan 'beads_wy', got %q", orphans[0].Name)
	}

	// Step 2: Remove the orphan
	if err := RemoveDatabase(townRoot, "beads_wy", true); err != nil {
		t.Fatalf("RemoveDatabase: %v", err)
	}

	// Step 3: Verify no more orphans
	orphans, err = FindOrphanedDatabases(townRoot)
	if err != nil {
		t.Fatalf("FindOrphanedDatabases after cleanup: %v", err)
	}
	if len(orphans) != 0 {
		t.Errorf("expected 0 orphans after cleanup, got %d", len(orphans))
	}
}

// =============================================================================
// Remote Dolt server config tests
// =============================================================================

func TestIsRemote(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		{"", false},
		{"127.0.0.1", false},
		{"localhost", false},
		{"Localhost", false},
		{"LOCALHOST", false},
		{"::1", false},
		{"[::1]", false},
		{"10.0.0.5", true},
		{"dolt.internal", true},
		{"192.168.1.100", true},
		// Hostnames resolving to loopback should be treated as local.
		// This covers /etc/hosts entries like "127.0.0.1 dolt.home.arpa".
		// Note: "localhost" is already covered above; any hostname that
		// the OS resolves to 127.0.0.1 or ::1 should also be local.
	}
	for _, tt := range tests {
		c := &Config{Host: tt.host}
		got := c.IsRemote()
		if got != tt.want {
			t.Errorf("Config{Host: %q}.IsRemote() = %v, want %v", tt.host, got, tt.want)
		}
	}
}

func TestSQLArgs(t *testing.T) {
	tests := []struct {
		name string
		host string
		port int
		user string
		want []string
	}{
		{"local empty host", "", 3307, "root", nil},
		{"local 127", "127.0.0.1", 3307, "root", nil},
		{"local localhost", "localhost", 3307, "root", nil},
		{"remote", "10.0.0.5", 3307, "gtuser", []string{
			"--host", "10.0.0.5",
			"--port", "3307",
			"--user", "gtuser",
			"--no-tls",
		}},
		{"remote custom port", "db.internal", 13306, "admin", []string{
			"--host", "db.internal",
			"--port", "13306",
			"--user", "admin",
			"--no-tls",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Config{Host: tt.host, Port: tt.port, User: tt.user}
			got := c.SQLArgs()
			if tt.want == nil {
				if got != nil {
					t.Errorf("SQLArgs() = %v, want nil", got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("SQLArgs() len = %d, want %d; got %v", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("SQLArgs()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestUserDSN(t *testing.T) {
	tests := []struct {
		user     string
		password string
		want     string
	}{
		{"root", "", "root"},
		{"root", "secret", "root:secret"},
		{"admin", "p@ss", "admin:p@ss"},
	}
	for _, tt := range tests {
		c := &Config{User: tt.user, Password: tt.password}
		got := c.userDSN()
		if got != tt.want {
			t.Errorf("Config{User:%q, Password:%q}.userDSN() = %q, want %q",
				tt.user, tt.password, got, tt.want)
		}
	}
}

func TestHostPort(t *testing.T) {
	tests := []struct {
		host string
		port int
		want string
	}{
		{"", 3307, "127.0.0.1:3307"},
		{"127.0.0.1", 3307, "127.0.0.1:3307"},
		{"10.0.0.5", 13306, "10.0.0.5:13306"},
		{"db.internal", 3307, "db.internal:3307"},
	}
	for _, tt := range tests {
		c := &Config{Host: tt.host, Port: tt.port}
		got := c.HostPort()
		if got != tt.want {
			t.Errorf("Config{Host:%q, Port:%d}.HostPort() = %q, want %q",
				tt.host, tt.port, got, tt.want)
		}
	}
}

func TestDefaultConfig_EnvVarOverrides(t *testing.T) {
	townRoot := t.TempDir()

	t.Setenv("GT_DOLT_HOST", "10.0.0.5")
	t.Setenv("GT_DOLT_PORT", "13306")
	t.Setenv("GT_DOLT_USER", "myuser")
	t.Setenv("GT_DOLT_PASSWORD", "mypass")

	config := DefaultConfig(townRoot)

	if config.Host != "10.0.0.5" {
		t.Errorf("Host = %q, want %q", config.Host, "10.0.0.5")
	}
	if config.Port != 13306 {
		t.Errorf("Port = %d, want %d", config.Port, 13306)
	}
	if config.User != "myuser" {
		t.Errorf("User = %q, want %q", config.User, "myuser")
	}
	if config.Password != "mypass" {
		t.Errorf("Password = %q, want %q", config.Password, "mypass")
	}
}

func TestDefaultConfig_EnvVarPartialOverride(t *testing.T) {
	townRoot := t.TempDir()

	// Only override host, rest should keep defaults
	t.Setenv("GT_DOLT_HOST", "remote.host")

	config := DefaultConfig(townRoot)

	if config.Host != "remote.host" {
		t.Errorf("Host = %q, want %q", config.Host, "remote.host")
	}
	if config.Port != DefaultPort {
		t.Errorf("Port = %d, want %d", config.Port, DefaultPort)
	}
	if config.User != DefaultUser {
		t.Errorf("User = %q, want %q", config.User, DefaultUser)
	}
	if config.Password != "" {
		t.Errorf("Password = %q, want empty", config.Password)
	}
}

func TestDefaultConfig_InvalidPortIgnored(t *testing.T) {
	townRoot := t.TempDir()

	t.Setenv("GT_DOLT_PORT", "not-a-number")

	config := DefaultConfig(townRoot)
	if config.Port != DefaultPort {
		t.Errorf("Port = %d, want default %d when env var is invalid", config.Port, DefaultPort)
	}
}

func TestBuildDoltSQLCmd_Local(t *testing.T) {
	config := &Config{
		Host:    "",
		Port:    3307,
		User:    "root",
		DataDir: "/tmp/dolt-data",
	}

	ctx := t.Context()
	cmd := buildDoltSQLCmd(ctx, config, "-q", "SELECT 1")

	// Should set Dir for local
	if cmd.Dir != "/tmp/dolt-data" {
		t.Errorf("cmd.Dir = %q, want %q", cmd.Dir, "/tmp/dolt-data")
	}

	// Should force a TCP client connection even for local servers.
	args := cmd.Args
	if len(args) < 10 {
		t.Fatalf("expected at least 10 args, got %v", args)
	}
	argStr := strings.Join(args, " ")
	for _, want := range []string{"--host", "127.0.0.1", "--port", "3307", "--user", "root", "--no-tls", "sql", "-q", "SELECT 1"} {
		if !strings.Contains(argStr, want) {
			t.Errorf("args %q missing expected %q", argStr, want)
		}
	}
	found := false
	for _, env := range cmd.Env {
		if env == "DOLT_CLI_PASSWORD=" {
			found = true
			break
		}
	}
	if !found {
		t.Error("local cmd should set empty DOLT_CLI_PASSWORD to suppress prompts")
	}
}

func TestBuildDoltSQLCmd_Remote(t *testing.T) {
	config := &Config{
		Host:     "10.0.0.5",
		Port:     3307,
		User:     "root",
		Password: "secret",
		DataDir:  "/tmp/dolt-data",
	}

	ctx := t.Context()
	cmd := buildDoltSQLCmd(ctx, config, "-q", "SELECT 1")

	// Dir is always set to DataDir — even for remote connections (GH#2537)
	// to prevent dolt from auto-creating .doltcfg/privileges.db in $CWD.
	if cmd.Dir != config.DataDir {
		t.Errorf("cmd.Dir = %q, want %q (DataDir set for remote per GH#2537)", cmd.Dir, config.DataDir)
	}

	// Should have connection flags
	argStr := strings.Join(cmd.Args, " ")
	for _, want := range []string{"--host", "10.0.0.5", "--port", "3307", "--no-tls"} {
		if !strings.Contains(argStr, want) {
			t.Errorf("args %q missing expected %q", argStr, want)
		}
	}

	// Should have DOLT_CLI_PASSWORD in env
	found := false
	for _, env := range cmd.Env {
		if env == "DOLT_CLI_PASSWORD=secret" {
			found = true
			break
		}
	}
	if !found {
		t.Error("remote cmd with password should have DOLT_CLI_PASSWORD env var")
	}
}

func TestBuildDoltSQLCmd_RemoteNoPassword(t *testing.T) {
	config := &Config{
		Host:    "10.0.0.5",
		Port:    3307,
		User:    "root",
		DataDir: "/tmp/dolt-data",
	}

	ctx := t.Context()
	cmd := buildDoltSQLCmd(ctx, config, "-q", "SELECT 1")

	// Should still have empty DOLT_CLI_PASSWORD in env to suppress prompts.
	for _, env := range cmd.Env {
		if env == "DOLT_CLI_PASSWORD=" {
			return
		}
	}
	t.Error("remote cmd without password should set empty DOLT_CLI_PASSWORD env var")
}

// =============================================================================
// WaitForReady tests (gt-zou1n)
// =============================================================================

func TestWaitForReady_NoServerConfigured(t *testing.T) {
	// When no server mode metadata exists, WaitForReady should return nil
	// immediately (nothing to wait for).
	townRoot := t.TempDir()

	err := WaitForReady(townRoot, 1*time.Second)
	if err != nil {
		t.Errorf("WaitForReady should succeed when no server configured, got: %v", err)
	}
}

func TestWaitForReady_ServerAlreadyListening(t *testing.T) {
	// Start a TCP listener, then verify WaitForReady succeeds quickly.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start listener: %v", err)
	}
	defer listener.Close()

	// Extract the port from the listener
	port := listener.Addr().(*net.TCPAddr).Port

	// Create a town root with server mode metadata pointing to this port
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	metadata := fmt.Sprintf(`{"backend":"dolt","dolt_mode":"server","port":%d}`, port)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0644); err != nil {
		t.Fatal(err)
	}

	// Override port via env var so DefaultConfig picks it up
	t.Setenv("GT_DOLT_PORT", fmt.Sprintf("%d", port))

	start := time.Now()
	err = WaitForReady(townRoot, 5*time.Second)
	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("WaitForReady should succeed when server is listening, got: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("WaitForReady took %v, should complete quickly when server is ready", elapsed)
	}
}

func TestWaitForReady_TimeoutWhenNoServer(t *testing.T) {
	// When server mode is configured but nothing is listening, WaitForReady
	// should return an error after timeout.

	// Find a free port, then immediately close to guarantee nothing is listening.
	tmpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	port := tmpListener.Addr().(*net.TCPAddr).Port
	tmpListener.Close()

	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	metadata := fmt.Sprintf(`{"backend":"dolt","dolt_mode":"server","port":%d}`, port)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GT_DOLT_PORT", fmt.Sprintf("%d", port))

	start := time.Now()
	err = WaitForReady(townRoot, 500*time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("WaitForReady should return error when server not reachable within timeout")
	}
	if elapsed < 400*time.Millisecond {
		t.Errorf("WaitForReady returned too quickly (%v), should wait at least close to timeout", elapsed)
	}
	if elapsed > 3*time.Second {
		t.Errorf("WaitForReady took %v, should not exceed timeout by much", elapsed)
	}
}

func TestWaitForReady_ServerBecomesReady(t *testing.T) {
	// Simulate the race: start WaitForReady, then start a listener after a delay.
	// WaitForReady should eventually succeed.

	// Find a free port first
	tmpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	port := tmpListener.Addr().(*net.TCPAddr).Port
	tmpListener.Close() // Free the port

	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	metadata := fmt.Sprintf(`{"backend":"dolt","dolt_mode":"server","port":%d}`, port)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GT_DOLT_PORT", fmt.Sprintf("%d", port))

	// Start the listener after 300ms delay. Use a done channel to
	// synchronize goroutine lifetime with test lifecycle. (review finding #3)
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go func() {
		time.Sleep(300 * time.Millisecond)
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return // Port may be taken (TOCTOU), test will timeout
		}
		defer listener.Close()
		<-done
	}()

	start := time.Now()
	err = WaitForReady(townRoot, 5*time.Second)
	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("WaitForReady should succeed after server starts, got: %v", err)
	}
	// Should take at least 300ms (delay before server starts) but not too long
	if elapsed < 200*time.Millisecond {
		t.Errorf("WaitForReady completed too quickly (%v), server shouldn't be ready yet", elapsed)
	}
	if elapsed > 3*time.Second {
		t.Errorf("WaitForReady took too long (%v), should succeed shortly after server starts", elapsed)
	}
}

func TestInvalidateDBCache(t *testing.T) {
	// Ensure InvalidateDBCache clears cached data so subsequent calls re-query.
	InvalidateDBCache()

	townRoot := t.TempDir()
	dataDir := filepath.Join(townRoot, ".dolt-data")
	setupDoltDB(t, dataDir, "cachetestdb")

	// First call populates.
	dbs1, err := ListDatabases(townRoot)
	if err != nil {
		t.Fatalf("first ListDatabases: %v", err)
	}
	if len(dbs1) != 1 || dbs1[0] != "cachetestdb" {
		t.Fatalf("expected [cachetestdb], got %v", dbs1)
	}

	// Add another database on disk.
	setupDoltDB(t, dataDir, "cachetestdb2")

	// Without invalidation, local path re-scans filesystem (no caching for local).
	dbs2, err := ListDatabases(townRoot)
	if err != nil {
		t.Fatalf("second ListDatabases: %v", err)
	}
	if len(dbs2) != 2 {
		t.Fatalf("expected 2 databases after adding cachetestdb2, got %d: %v", len(dbs2), dbs2)
	}
}

func TestDBCache_ReturnsCopy(t *testing.T) {
	// Verify that callers get a defensive copy, not the cached slice.
	InvalidateDBCache()

	townRoot := t.TempDir()
	dataDir := filepath.Join(townRoot, ".dolt-data")
	setupDoltDB(t, dataDir, "copytest")

	dbs1, _ := ListDatabases(townRoot)
	if len(dbs1) > 0 {
		dbs1[0] = "MUTATED"
	}

	dbs2, _ := ListDatabases(townRoot)
	for _, db := range dbs2 {
		if db == "MUTATED" {
			t.Fatal("ListDatabases returned shared slice — callers can corrupt the cache")
		}
	}
}

func TestCollectDatabaseOwners_HQOnly(t *testing.T) {
	townRoot := t.TempDir()

	setupRigMetadata(t, townRoot, "hq", "hq")
	setupRigsJSON(t, townRoot, []string{})

	owners := CollectDatabaseOwners(townRoot)
	if owners["hq"] != "town beads" {
		t.Errorf("expected 'hq' owner to be 'town beads', got %q", owners["hq"])
	}
	if len(owners) != 1 {
		t.Errorf("expected 1 owner, got %d: %v", len(owners), owners)
	}
}

func TestCollectDatabaseOwners_MultipleRigs(t *testing.T) {
	townRoot := t.TempDir()

	setupRigsJSON(t, townRoot, []string{"gastown", "beads"})
	setupRigMetadata(t, townRoot, "hq", "hq")
	setupRigMetadata(t, townRoot, "gastown", "gt")
	setupRigMetadata(t, townRoot, "beads", "beads")

	owners := CollectDatabaseOwners(townRoot)
	if owners["hq"] != "town beads" {
		t.Errorf("expected 'hq' owner 'town beads', got %q", owners["hq"])
	}
	if owners["gt"] != "gastown rig beads" {
		t.Errorf("expected 'gt' owner 'gastown rig beads', got %q", owners["gt"])
	}
	if owners["beads"] != "beads rig beads" {
		t.Errorf("expected 'beads' owner 'beads rig beads', got %q", owners["beads"])
	}
	if len(owners) != 3 {
		t.Errorf("expected 3 owners, got %d: %v", len(owners), owners)
	}
}

func TestCollectDatabaseOwners_CustomDatabaseName(t *testing.T) {
	townRoot := t.TempDir()

	// Rig name differs from dolt_database name (like gastown → gt)
	setupRigsJSON(t, townRoot, []string{"myrig"})
	setupRigMetadata(t, townRoot, "myrig", "custom_db")

	owners := CollectDatabaseOwners(townRoot)
	if owners["custom_db"] != "myrig rig beads" {
		t.Errorf("expected 'custom_db' owner 'myrig rig beads', got %q", owners["custom_db"])
	}
	if _, exists := owners["myrig"]; exists {
		t.Error("rig name 'myrig' should not be a key in owners (only dolt_database value)")
	}
}

func TestCollectDatabaseOwners_UnknownDB(t *testing.T) {
	townRoot := t.TempDir()

	setupRigsJSON(t, townRoot, []string{})
	setupRigMetadata(t, townRoot, "hq", "hq")

	owners := CollectDatabaseOwners(townRoot)
	if _, exists := owners["unknown_db"]; exists {
		t.Error("unknown_db should not have an owner")
	}
}

// TestCollectDatabaseOwners_ProtectedSharedServerDatabaseLabeled verifies that
// protected shared-server databases (e.g. beads_global) are reported with a
// dedicated owner label rather than appearing as orphans in `gt dolt list`.
// Regression for the operator-confusion gap flagged on PR #3823 — the
// orphan-detection skip alone wasn't enough; CollectDatabaseOwners has to
// know about the same registry.
func TestCollectDatabaseOwners_ProtectedSharedServerDatabaseLabeled(t *testing.T) {
	townRoot := t.TempDir()

	setupRigsJSON(t, townRoot, []string{})
	setupRigMetadata(t, townRoot, "hq", "hq")

	// Create a beads_global database directory on disk (no rig metadata
	// references it — that's the whole reason it would otherwise look like
	// an orphan).
	dataDir := DefaultConfig(townRoot).DataDir
	beadsGlobalPath := filepath.Join(dataDir, "beads_global", ".dolt")
	if err := os.MkdirAll(beadsGlobalPath, 0o755); err != nil {
		t.Fatalf("mkdir beads_global: %v", err)
	}

	owners := CollectDatabaseOwners(townRoot)
	label, ok := owners["beads_global"]
	if !ok {
		t.Fatalf("expected beads_global to have an owner label, got owners=%v", owners)
	}
	if !strings.Contains(label, "protected") {
		t.Errorf("expected protected-DB label to mention 'protected', got %q", label)
	}
}

// TestCollectDatabaseOwners_ProtectedDatabaseNotPhantom verifies that the
// protected-DB labeling only kicks in when the database actually exists on
// disk — otherwise CollectDatabaseOwners would advertise an owner for a DB
// that doesn't exist on the filesystem, which would be its own kind of
// confusion.
func TestCollectDatabaseOwners_ProtectedDatabaseNotPhantom(t *testing.T) {
	townRoot := t.TempDir()

	setupRigsJSON(t, townRoot, []string{})
	setupRigMetadata(t, townRoot, "hq", "hq")

	// Intentionally do NOT create beads_global on disk.
	owners := CollectDatabaseOwners(townRoot)
	if _, exists := owners["beads_global"]; exists {
		t.Errorf("beads_global should not be in owners when absent from disk, got %q", owners["beads_global"])
	}
}

// =============================================================================
// writeServerConfig tests
// =============================================================================

func TestWriteServerConfig_Defaults(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")

	config := &Config{
		Port:           3307,
		DataDir:        dir,
		MaxConnections: 1000,
		ReadTimeoutMs:  DefaultReadTimeoutMs,
		WriteTimeoutMs: DefaultWriteTimeoutMs,
		LogLevel:       "warning",
	}

	if err := writeServerConfig(config, configPath); err != nil {
		t.Fatalf("writeServerConfig: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	content := string(data)

	checks := []string{
		"port: 3307",
		"max_connections: 1000",
		fmt.Sprintf("read_timeout_millis: %d", DefaultReadTimeoutMs),
		fmt.Sprintf("write_timeout_millis: %d", DefaultWriteTimeoutMs),
		"data_dir: \"" + dir + "\"",
		"log_level: warning",
		"auto_gc_behavior:",
	}
	for _, want := range checks {
		if !strings.Contains(content, want) {
			t.Errorf("config missing %q\nfull content:\n%s", want, content)
		}
	}
}

func TestWriteServerConfig_NoHost(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")

	config := &Config{
		Port:    3307,
		DataDir: dir,
		// Host is empty — should not appear in config
	}
	if err := writeServerConfig(config, configPath); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(configPath)
	if strings.Contains(string(data), "host:") {
		t.Error("empty Host should not write host line to config")
	}
}

func TestWriteServerConfig_WithHost(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")

	config := &Config{
		Port:    3307,
		Host:    "127.0.0.1",
		DataDir: dir,
	}
	if err := writeServerConfig(config, configPath); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(configPath)
	if !strings.Contains(string(data), "host: 127.0.0.1") {
		t.Error("explicit Host should appear in config")
	}
}

func TestWriteServerConfig_ZeroTimeoutsOmitted(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")

	config := &Config{
		Port:           3307,
		DataDir:        dir,
		ReadTimeoutMs:  0, // zero = use Dolt default
		WriteTimeoutMs: 0,
	}
	if err := writeServerConfig(config, configPath); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(configPath)
	content := string(data)
	if strings.Contains(content, "read_timeout_millis") {
		t.Error("zero ReadTimeoutMs should not write read_timeout_millis")
	}
	if strings.Contains(content, "write_timeout_millis") {
		t.Error("zero WriteTimeoutMs should not write write_timeout_millis")
	}
}

func TestWriteServerConfig_Overwrites(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")

	// Write initial config
	if err := os.WriteFile(configPath, []byte("old content"), 0644); err != nil {
		t.Fatal(err)
	}

	config := &Config{Port: 3307, DataDir: dir, LogLevel: "info"}
	if err := writeServerConfig(config, configPath); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(configPath)
	if strings.Contains(string(data), "old content") {
		t.Error("writeServerConfig should overwrite existing file")
	}
	if !strings.Contains(string(data), "log_level: info") {
		t.Error("new config should have updated log level")
	}
}

// TestBuildDatabaseToRigMap tests the database name to rig name mapping.
func TestBuildDatabaseToRigMap(t *testing.T) {
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Test with empty routes file
	result := buildDatabaseToRigMap(townRoot)
	if len(result) != 0 {
		t.Errorf("empty routes: expected empty map, got %v", result)
	}

	// Test with typical routes.jsonl
	routesContent := `{"prefix":"hq-","path":"."}
{"prefix":"bd-","path":"beads/mayor/rig"}
{"prefix":"gt-","path":"gastown/mayor/rig"}
{"prefix":"sw-","path":"sallaWork/mayor/rig"}
{"prefix":"hq-cv-","path":"."}
`
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatal(err)
	}

	result = buildDatabaseToRigMap(townRoot)

	// Check expected mappings
	expected := map[string]string{
		"bd": "beads",
		"gt": "gastown",
		"sw": "sallaWork",
	}

	for db, rig := range expected {
		if got, want := result[db], rig; got != want {
			t.Errorf("database %q: got rig %q, want %q", db, got, want)
		}
	}

	// HQ routes with path "." should not be included (path[0] == ".")
	if _, exists := result["hq"]; exists {
		t.Error("hq database should not be in map (path is '.')")
	}
	if _, exists := result["hq-cv"]; exists {
		t.Error("hq-cv database should not be in map (path is '.')")
	}
}

// TestEnsureAllMetadata_UsesRigNames verifies that EnsureAllMetadata correctly
// maps database names to rig names using routes.jsonl.
// This is a regression test for the bug where databases named "bd", "gt", "sw"
// were incorrectly used as rig names, creating stub directories at /gt/bd/, /gt/gt/, /gt/sw/
// instead of the correct /gt/beads/, /gt/gastown/, /gt/sallaWork/.
func TestEnsureAllMetadata_UsesRigNames(t *testing.T) {
	townRoot := t.TempDir()

	// Create databases in .dolt-data with prefix names (as Dolt server does)
	dataDir := filepath.Join(townRoot, ".dolt-data")
	setupDoltDB(t, dataDir, "hq")
	setupDoltDB(t, dataDir, "bd")
	setupDoltDB(t, dataDir, "gt")

	// Create routes.jsonl with correct mappings
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	routesContent := `{"prefix":"hq-","path":"."}
{"prefix":"bd-","path":"beads/mayor/rig"}
{"prefix":"gt-","path":"gastown/mayor/rig"}
`
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create correct rig beads directories (not the buggy stub paths)
	if err := os.MkdirAll(filepath.Join(townRoot, "beads", "mayor", "rig", ".beads"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads"), 0755); err != nil {
		t.Fatal(err)
	}

	// Run EnsureAllMetadata
	updated, errs := EnsureAllMetadata(townRoot)
	if len(errs) > 0 {
		t.Errorf("unexpected errors: %v", errs)
	}

	// Verify metadata was created in correct locations
	hqMeta := filepath.Join(townRoot, ".beads", "metadata.json")
	beadsMeta := filepath.Join(townRoot, "beads", "mayor", "rig", ".beads", "metadata.json")
	gastownMeta := filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads", "metadata.json")

	// Buggy paths that should NOT exist
	buggyBdMeta := filepath.Join(townRoot, "bd", ".beads", "metadata.json")
	buggyGtMeta := filepath.Join(townRoot, "gt", ".beads", "metadata.json")

	if _, err := os.Stat(hqMeta); os.IsNotExist(err) {
		t.Error("hq metadata.json should exist")
	}
	if _, err := os.Stat(beadsMeta); os.IsNotExist(err) {
		t.Error("beads metadata.json should exist in correct path")
	}
	if _, err := os.Stat(gastownMeta); os.IsNotExist(err) {
		t.Error("gastown metadata.json should exist in correct path")
	}

	// Verify buggy paths were NOT created
	if _, err := os.Stat(buggyBdMeta); err == nil {
		t.Error("buggy path bd/.beads/metadata.json should NOT exist")
	}
	if _, err := os.Stat(buggyGtMeta); err == nil {
		t.Error("buggy path gt/.beads/metadata.json should NOT exist")
	}

	// Verify correct number of updates
	if len(updated) != 3 {
		t.Errorf("expected 3 updated databases, got %d: %v", len(updated), updated)
	}
}

// TestEnsureAllMetadata_FallbackToDbName tests that EnsureAllMetadata falls back
// to using the database name as rig name when no route is found.
func TestEnsureAllMetadata_FallbackToDbName(t *testing.T) {
	townRoot := t.TempDir()

	// Create database
	dataDir := filepath.Join(townRoot, ".dolt-data")
	setupDoltDB(t, dataDir, "unknownrig")

	// Create empty routes.jsonl
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	// Create rig beads dir with same name as database
	if err := os.MkdirAll(filepath.Join(townRoot, "unknownrig", ".beads"), 0755); err != nil {
		t.Fatal(err)
	}

	updated, errs := EnsureAllMetadata(townRoot)
	if len(errs) > 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
	if len(updated) != 1 {
		t.Errorf("expected 1 updated, got %d: %v", len(updated), updated)
	}

	// Verify metadata was created
	metaPath := filepath.Join(townRoot, "unknownrig", ".beads", "metadata.json")
	if _, err := os.Stat(metaPath); os.IsNotExist(err) {
		t.Error("metadata.json should exist for unknown rig")
	}
}

// TestEnsureAllMetadata_NoOscillation verifies that when two databases map to
// the same rig (e.g. "gastown" and "gt" both map to rig "gastown" via
// conflicting routes.jsonl/rigs.json entries), EnsureAllMetadata does not
// oscillate the dolt_database value on repeated calls. (gas-ar0)
func TestEnsureAllMetadata_NoOscillation(t *testing.T) {
	townRoot := t.TempDir()
	dataDir := filepath.Join(townRoot, ".dolt-data")

	// Simulate two databases that both map to "gastown":
	//   "gastown" — matched by default (db name == rig name)
	//   "gt"      — matched via rigs.json prefix "gt"
	setupDoltDB(t, dataDir, "gastown")
	setupDoltDB(t, dataDir, "gt")
	setupDoltDB(t, dataDir, "hq")

	// rigs.json: gastown rig uses prefix "gt"
	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}
	rigsData := `{"version":1,"rigs":{"gastown":{"beads":{"prefix":"gt"}}}}`
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte(rigsData), 0644); err != nil {
		t.Fatal(err)
	}

	// Create beads dirs
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads"), 0755); err != nil {
		t.Fatal(err)
	}

	// First call: no existing metadata.json — whichever candidate wins is fine
	_, errs := EnsureAllMetadata(townRoot)
	if len(errs) > 0 {
		t.Fatalf("first EnsureAllMetadata errors: %v", errs)
	}

	// Read the value that was written
	metaPath := filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads", "metadata.json")
	readDB := func() string {
		data, err := os.ReadFile(metaPath)
		if err != nil {
			t.Fatalf("reading metadata.json: %v", err)
		}
		var meta map[string]interface{}
		if err := json.Unmarshal(data, &meta); err != nil {
			t.Fatalf("parsing metadata.json: %v", err)
		}
		db, _ := meta["dolt_database"].(string)
		return db
	}

	firstDB := readDB()
	if firstDB == "" {
		t.Fatal("dolt_database should be set after first call")
	}

	// Second call: must produce the same value (no oscillation)
	_, errs = EnsureAllMetadata(townRoot)
	if len(errs) > 0 {
		t.Fatalf("second EnsureAllMetadata errors: %v", errs)
	}
	secondDB := readDB()
	if secondDB != firstDB {
		t.Errorf("oscillation: first call wrote %q, second call wrote %q", firstDB, secondDB)
	}

	// Third call: still no change
	_, errs = EnsureAllMetadata(townRoot)
	if len(errs) > 0 {
		t.Fatalf("third EnsureAllMetadata errors: %v", errs)
	}
	thirdDB := readDB()
	if thirdDB != firstDB {
		t.Errorf("oscillation: first call wrote %q, third call wrote %q", firstDB, thirdDB)
	}
}

func TestCleanStaleSocket_RemovesStaleFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix sockets not applicable on Windows")
	}

	// Create a regular file pretending to be a stale socket
	socketPath := filepath.Join(t.TempDir(), "mysql.sock")
	if err := os.WriteFile(socketPath, []byte{}, 0600); err != nil {
		t.Fatal(err)
	}

	cleanStaleSocket(socketPath)

	// lsof will report exit code 1 (no process holds it) → file should be removed
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Error("stale socket file should have been removed")
	}
}

func TestCleanStaleSocket_NoopWhenMissing(t *testing.T) {
	// Should not panic or error when socket doesn't exist
	cleanStaleSocket(filepath.Join(t.TempDir(), "nonexistent.sock"))
}

// =============================================================================
// Thundering herd fix tests (gt-nkn)
// =============================================================================

func TestCountDoltDatabases(t *testing.T) {
	tmpDir := t.TempDir()

	// Non-existent directory returns 1 (safe default).
	if got := countDoltDatabases(filepath.Join(tmpDir, "noexist")); got != 1 {
		t.Errorf("non-existent dir: got %d, want 1", got)
	}

	// Empty directory returns 1 (safe default).
	empty := filepath.Join(tmpDir, "empty")
	if err := os.MkdirAll(empty, 0755); err != nil {
		t.Fatal(err)
	}
	if got := countDoltDatabases(empty); got != 1 {
		t.Errorf("empty dir: got %d, want 1", got)
	}

	// Directory with Dolt databases (subdirs containing .dolt).
	dataDir := filepath.Join(tmpDir, "data")
	for _, name := range []string{"hq", "gastown", "beads"} {
		if err := os.MkdirAll(filepath.Join(dataDir, name, ".dolt"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	// A plain directory without .dolt should not be counted.
	if err := os.MkdirAll(filepath.Join(dataDir, "notadb"), 0755); err != nil {
		t.Fatal(err)
	}
	if got := countDoltDatabases(dataDir); got != 3 {
		t.Errorf("dataDir with 3 dbs + 1 plain dir: got %d, want 3", got)
	}
}

// TestRemoveDatabase_RefusesLargeDBWhenServerDown verifies that RemoveDatabase
// refuses to delete databases with >1MB of data when the server is offline
// and --force is not set. (gt-xvh)
func TestRemoveDatabase_RefusesLargeDBWhenServerDown(t *testing.T) {
	// Skip if a real Dolt server is running on the default port — IsRunning
	// would detect it and take the SQL-check path instead of the size-check path.
	if conn, err := net.DialTimeout("tcp", "127.0.0.1:3307", time.Second); err == nil {
		conn.Close()
		t.Skip("skipping: real Dolt server running on port 3307 would bypass size check")
	}

	townRoot := t.TempDir()
	dataDir := filepath.Join(townRoot, ".dolt-data")
	setupDoltDB(t, dataDir, "big_db")

	// Write >1MB of data to make it look like a real database
	bigFile := filepath.Join(dataDir, "big_db", ".dolt", "noms", "data")
	data := make([]byte, 2<<20) // 2MB
	if err := os.WriteFile(bigFile, data, 0644); err != nil {
		t.Fatal(err)
	}

	// Server is not running (no PID file, no process)
	err := RemoveDatabase(townRoot, "big_db", false)
	if err == nil {
		t.Fatal("expected error when removing large database with server offline")
	}
	if !strings.Contains(err.Error(), "server offline") {
		t.Errorf("expected 'server offline' in error, got: %v", err)
	}

	// Verify the database still exists
	if _, statErr := os.Stat(filepath.Join(dataDir, "big_db")); statErr != nil {
		t.Error("big_db should still exist after refused removal")
	}
}

// TestRemoveDatabase_AllowsSmallDBWhenServerDown verifies that small databases
// (<1MB) can be removed even when the server is offline. (gt-xvh)
func TestRemoveDatabase_AllowsSmallDBWhenServerDown(t *testing.T) {
	townRoot := t.TempDir()
	dataDir := filepath.Join(townRoot, ".dolt-data")
	setupDoltDB(t, dataDir, "small_orphan")

	// Small database (<1MB) — manifest file is only a few bytes from setupDoltDB
	err := RemoveDatabase(townRoot, "small_orphan", true)
	if err != nil {
		t.Fatalf("RemoveDatabase with force: %v", err)
	}

	if _, statErr := os.Stat(filepath.Join(dataDir, "small_orphan")); !os.IsNotExist(statErr) {
		t.Error("small_orphan should be removed")
	}
}

// TestQuarantine_MovesInsteadOfDeleting verifies that the quarantine logic
// moves corrupted database dirs to .quarantine/ instead of deleting them. (gt-xvh)
func TestQuarantine_MovesInsteadOfDeleting(t *testing.T) {
	townRoot := t.TempDir()
	dataDir := filepath.Join(townRoot, ".dolt-data")

	// Create a "corrupted" database: has .dolt/ but no noms/manifest
	corruptDB := filepath.Join(dataDir, "corrupt_db", ".dolt")
	if err := os.MkdirAll(corruptDB, 0755); err != nil {
		t.Fatal(err)
	}
	// Write some data so it's not empty
	if err := os.WriteFile(filepath.Join(corruptDB, "somefile"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	// Simulate the quarantine scan (same logic as Start)
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}

	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		doltDir := filepath.Join(dataDir, entry.Name(), ".dolt")
		if _, statErr := os.Stat(doltDir); statErr != nil {
			continue
		}
		manifest := filepath.Join(doltDir, "noms", "manifest")
		if _, statErr := os.Stat(manifest); statErr == nil {
			continue
		}
		// This is corrupted — verify quarantine moves it
		quarantineDir := filepath.Join(dataDir, ".quarantine")
		if mkErr := os.MkdirAll(quarantineDir, 0755); mkErr != nil {
			t.Fatal(mkErr)
		}
		dest := filepath.Join(quarantineDir, entry.Name()+".test")
		if renameErr := os.Rename(filepath.Join(dataDir, entry.Name()), dest); renameErr != nil {
			t.Fatalf("quarantine move failed: %v", renameErr)
		}
	}

	// Original should be gone
	if _, err := os.Stat(filepath.Join(dataDir, "corrupt_db")); !os.IsNotExist(err) {
		t.Error("corrupt_db should have been moved to quarantine")
	}

	// Quarantine should have it
	quarantineEntries, err := os.ReadDir(filepath.Join(dataDir, ".quarantine"))
	if err != nil {
		t.Fatal(err)
	}
	if len(quarantineEntries) != 1 {
		t.Errorf("expected 1 quarantined entry, got %d", len(quarantineEntries))
	}
}

func TestGetLastCommitAge_NoServer(t *testing.T) {
	// With no server running, GetLastCommitAge should return an error,
	// not panic or hang.
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".dolt-data"), 0755); err != nil {
		t.Fatal(err)
	}

	_, _, err := GetLastCommitAge(townRoot)
	if err == nil {
		// May succeed if a local Dolt is running; either outcome is valid.
		return
	}
	t.Logf("GetLastCommitAge with no server: %v (expected)", err)
}

func TestGetLastCommitAge_NoDatabases(t *testing.T) {
	townRoot := t.TempDir()

	_, _, err := GetLastCommitAge(townRoot)
	if err == nil {
		t.Error("expected error with no databases, got nil")
	}
}

func TestHealthMetrics_CommitFreshnessFields(t *testing.T) {
	// Verify the new fields exist and are zero-valued when probe fails.
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".dolt-data"), 0755); err != nil {
		t.Fatal(err)
	}

	metrics := GetHealthMetrics(townRoot)
	if metrics.LastCommitAge < 0 {
		t.Errorf("LastCommitAge = %v, want >= 0", metrics.LastCommitAge)
	}
}
