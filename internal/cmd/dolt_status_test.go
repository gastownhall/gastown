package cmd

import (
	"os"
	"path/filepath"
	"strings"
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

	cfg, ok := readBeadsRuntimeConfig(beadsDir)
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
	t.Setenv("GT_DOLT_PORT", "32769")

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

	cfg, ok := readBeadsRuntimeConfig(beadsDir)
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

func TestReadBeadsRuntimeConfigPortFileFallback(t *testing.T) {
	t.Setenv("GT_DOLT_PORT", "32769")

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
	if err := os.WriteFile(filepath.Join(beadsDir, "dolt-server.port"), []byte("43113\n"), 0600); err != nil {
		t.Fatalf("write port file: %v", err)
	}

	cfg, ok := readBeadsRuntimeConfig(beadsDir)
	if !ok {
		t.Fatal("readBeadsRuntimeConfig did not detect server metadata")
	}
	if cfg.Port != 43113 {
		t.Fatalf("Port = %d, want port file 43113", cfg.Port)
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

	if _, ok := readBeadsRuntimeConfig(beadsDir); ok {
		t.Fatal("embedded metadata should not be reported as shared-server config")
	}
}

func TestBeadsScopeHint_HQWarnsAgainstGlobal(t *testing.T) {
	townRoot := filepath.Join(string(filepath.Separator), "custom", "town")
	hint := beadsScopeHint("hq", townRoot)

	for _, want := range []string{"database hq", "bd -C " + townRoot, "bd --global", "beads_global"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("beadsScopeHint() missing %q in:\n%s", want, hint)
		}
	}
	if strings.Contains(hint, "~/gt") {
		t.Fatalf("beadsScopeHint() should not hardcode ~/gt:\n%s", hint)
	}
}

func TestBeadsScopeHint_NonHQEmpty(t *testing.T) {
	if hint := beadsScopeHint("gastown", "/custom/town"); hint != "" {
		t.Fatalf("beadsScopeHint() = %q, want empty", hint)
	}
}
