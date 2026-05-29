package beads

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func doltEnvHasKey(env []string, key string) bool {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}

// TestDoltTargetEnvFromBeadsDir_ServerModeOmitsDataDir verifies that in server
// mode (metadata.json has dolt_server_host/port) the shared, multi-database
// BEADS_DOLT_DATA_DIR is NOT emitted. Emitting it makes bd's mol resolution
// select a database other than the one named in the pinned .beads metadata, so
// a routed cross-rig bead (e.g. mi-) fails to resolve in `bd mol bond`
// (gastownhall/gastown#4140). The host/port is the connection target in server
// mode; the data dir is only meaningful for embedded mode.
func TestDoltTargetEnvFromBeadsDir_ServerModeOmitsDataDir(t *testing.T) {
	town := t.TempDir()
	if err := os.MkdirAll(filepath.Join(town, "mayor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(town, "mayor", "town.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	beadsDir := filepath.Join(town, "minerals", ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"dolt_mode":"server","dolt_server_host":"127.0.0.1","dolt_server_port":3307,"dolt_database":"minerals"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}

	env := doltTargetEnvFromBeadsDir(beadsDir)

	if !doltEnvHasKey(env, "BEADS_DOLT_SERVER_HOST") || !doltEnvHasKey(env, "BEADS_DOLT_SERVER_PORT") {
		t.Errorf("server-mode env missing host/port: %v", env)
	}
	if doltEnvHasKey(env, "BEADS_DOLT_DATA_DIR") {
		t.Errorf("server-mode env must NOT include BEADS_DOLT_DATA_DIR (causes wrong-db resolution for routed beads): %v", env)
	}
}

// TestDoltTargetEnvFromBeadsDir_EmbeddedModeSetsDataDir verifies that embedded
// mode (no server host/port configured) still points bd at the town data dir.
func TestDoltTargetEnvFromBeadsDir_EmbeddedModeSetsDataDir(t *testing.T) {
	town := t.TempDir()
	if err := os.MkdirAll(filepath.Join(town, "mayor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(town, "mayor", "town.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	beadsDir := filepath.Join(town, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"dolt_mode":"embedded","dolt_database":"hq"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}

	env := doltTargetEnvFromBeadsDir(beadsDir)

	if doltEnvHasKey(env, "BEADS_DOLT_SERVER_HOST") || doltEnvHasKey(env, "BEADS_DOLT_SERVER_PORT") {
		t.Errorf("embedded-mode env must not include server host/port: %v", env)
	}
	if !doltEnvHasKey(env, "BEADS_DOLT_DATA_DIR") {
		t.Errorf("embedded-mode env should include BEADS_DOLT_DATA_DIR: %v", env)
	}
}
