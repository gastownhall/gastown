package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBdReadOnlyEnv verifies that bdReadOnlyEnv returns an environment slice
// containing exactly one BD_DOLT_AUTO_COMMIT=off entry, regardless of any
// pre-existing BD_DOLT_AUTO_COMMIT in the parent process env.
func TestBdReadOnlyEnv(t *testing.T) {
	tests := []struct {
		name    string
		preset  string
		setting bool
	}{
		{name: "unset parent", setting: false},
		{name: "parent has off", preset: "off", setting: true},
		{name: "parent has on", preset: "on", setting: true},
		{name: "parent has stale value", preset: "batched", setting: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setting {
				t.Setenv("BD_DOLT_AUTO_COMMIT", tc.preset)
			} else {
				t.Setenv("BD_DOLT_AUTO_COMMIT", "")
			}

			env := bdReadOnlyEnv()

			assertSingleEnvValue(t, env, "BD_DOLT_AUTO_COMMIT", "off")
			assertSingleEnvValue(t, env, "BD_READONLY", "true")
		})
	}
}

func TestBdReadOnlyPinnedEnvUsesSelectedBeadsDir(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	metadata := []byte(`{"dolt_database":"rigdb","dolt_server_host":"127.0.0.1","dolt_server_port":4407}`)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), metadata, 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", "/wrong")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "hq")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "9999")
	t.Setenv("BD_DOLT_AUTO_COMMIT", "on")

	env := bdReadOnlyPinnedEnv(beadsDir)
	assertSingleEnvValue(t, env, "BEADS_DIR", beadsDir)
	assertSingleEnvValue(t, env, "BEADS_DOLT_SERVER_DATABASE", "rigdb")
	assertSingleEnvValue(t, env, "BEADS_DOLT_SERVER_PORT", "4407")
	assertSingleEnvValue(t, env, "BEADS_DOLT_PORT", "4407")
	assertSingleEnvValue(t, env, "BD_DOLT_AUTO_COMMIT", "off")
	assertSingleEnvValue(t, env, "BD_READONLY", "true")
	assertSingleEnvValue(t, env, "BD_EXPORT_AUTO", "false")
}

func TestBdReadOnlyRoutingEnvDoesNotPinDatabase(t *testing.T) {
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	metadata := []byte(`{"dolt_database":"hq","dolt_server_host":"127.0.0.1","dolt_server_port":4407}`)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), metadata, 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", "/wrong")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "wrong")

	env := bdReadOnlyRoutingEnv(townRoot)
	assertEnvAbsent(t, env, "BEADS_DIR")
	assertEnvAbsent(t, env, "BEADS_DOLT_SERVER_DATABASE")
	assertSingleEnvValue(t, env, "BEADS_DOLT_SERVER_PORT", "4407")
	assertSingleEnvValue(t, env, "BD_DOLT_AUTO_COMMIT", "off")
	assertSingleEnvValue(t, env, "BD_READONLY", "true")
}

func assertSingleEnvValue(t *testing.T, env []string, key, want string) {
	t.Helper()
	var count int
	var value string
	for _, e := range env {
		if strings.HasPrefix(e, key+"=") {
			count++
			value = strings.TrimPrefix(e, key+"=")
		}
	}
	if count != 1 || value != want {
		t.Fatalf("%s count/value = %d/%q, want 1/%q in %v", key, count, value, want, env)
	}
}

func assertEnvAbsent(t *testing.T, env []string, key string) {
	t.Helper()
	for _, e := range env {
		if strings.HasPrefix(e, key+"=") {
			t.Fatalf("%s should be absent, got %q in %v", key, e, env)
		}
	}
}
