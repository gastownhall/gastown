package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/doltserver"
)

func TestReadBeadsRuntimeConfigServerMetadata(t *testing.T) {
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}
	metadata := `{
  "backend": "dolt",
  "dolt_mode": "server",
  "dolt_server_host": "192.0.2.10",
  "dolt_server_port": 4311,
  "dolt_database": "gastown"
}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	cfg, ok := readBeadsRuntimeConfig(beadsDir, townRoot)
	if !ok {
		t.Fatal("readBeadsRuntimeConfig did not detect server metadata")
	}
	if cfg.Database != "gastown" {
		t.Fatalf("Database = %q, want gastown", cfg.Database)
	}
	if cfg.Host != "192.0.2.10" {
		t.Fatalf("Host = %q, want 192.0.2.10", cfg.Host)
	}
	if cfg.Port != 4311 {
		t.Fatalf("Port = %d, want 4311", cfg.Port)
	}
}

func TestReadBeadsRuntimeConfigDefaultServerAddr(t *testing.T) {
	t.Setenv("GT_DOLT_PORT", "")

	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}
	metadata := `{
  "backend": "dolt",
  "dolt_mode": "server",
  "database": "dolt"
}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	cfg, ok := readBeadsRuntimeConfig(beadsDir, townRoot)
	if !ok {
		t.Fatal("readBeadsRuntimeConfig did not detect server metadata")
	}
	if cfg.Host != "127.0.0.1" {
		t.Fatalf("Host = %q, want 127.0.0.1", cfg.Host)
	}
	if cfg.Port != doltserver.DefaultPort {
		t.Fatalf("Port = %d, want default %d", cfg.Port, doltserver.DefaultPort)
	}
}

func TestReadBeadsRuntimeConfigIgnoresEmbeddedMetadata(t *testing.T) {
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}
	metadata := `{
  "backend": "dolt",
  "dolt_mode": "embedded",
  "dolt_database": "gastown"
}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	if _, ok := readBeadsRuntimeConfig(beadsDir, townRoot); ok {
		t.Fatal("embedded metadata should not be reported as shared-server config")
	}
}
