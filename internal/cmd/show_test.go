package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestExtractBeadIDFromArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"simple", []string{"myproject-abc"}, "myproject-abc"},
		{"with flags after", []string{"gt-abc123", "--json"}, "gt-abc123"},
		{"with flags before", []string{"--json", "hq-xyz"}, "hq-xyz"},
		{"with id flag equals", []string{"--json", "--id=gt-abc123"}, "gt-abc123"},
		{"with id flag value", []string{"--id", "hq-xyz", "--json"}, "hq-xyz"},
		{"positional before id flag value", []string{"gt-abc123", "--id", "hq-xyz"}, "gt-abc123"},
		{"positional before id flag equals", []string{"gt-abc123", "--id=hq-xyz"}, "gt-abc123"},
		{"positional after id flag value", []string{"--id", "hq-xyz", "gt-abc123"}, "gt-abc123"},
		{"positional after id flag equals", []string{"--id=hq-xyz", "gt-abc123"}, "gt-abc123"},
		{"flag-like id fallback", []string{"--id=--gt-abc123"}, "--gt-abc123"},
		{"with as-of before id", []string{"--as-of", "main", "gt-abc123"}, "gt-abc123"},
		{"with directory before id", []string{"-C", "/tmp/work", "gt-abc123"}, "gt-abc123"},
		{"with format before id", []string{"--format", "json", "gt-abc123"}, "gt-abc123"},
		{"with end of flags marker", []string{"--", "gt-abc123"}, "gt-abc123"},
		{"flags only", []string{"--json", "-v"}, ""},
		{"value flag only", []string{"--as-of", "main"}, ""},
		{"empty", []string{}, ""},
		{"mixed", []string{"-v", "bd-def456", "--json"}, "bd-def456"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractBeadIDFromArgs(tc.args)
			if got != tc.want {
				t.Errorf("extractBeadIDFromArgs(%v) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

func TestBdShowInvocationPinsRoutedMetadataDatabase(t *testing.T) {
	townRoot := setupShowInvocationTown(t)
	rigDir := filepath.Join(townRoot, "gastown", "mayor", "rig")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	baseEnv := []string{
		"PATH=/usr/bin",
		"BEADS_DIR=/stale/.beads",
		"BEADS_DB=/stale.db",
		"BD_DB=/stale.bd",
		"BEADS_DOLT_SERVER_DATABASE=stale",
		"BEADS_DOLT_SERVER_HOST=wrong-host",
		"BEADS_DOLT_SERVER_PORT=9999",
		"BEADS_DOLT_PORT=9999",
		"BEADS_DOLT_DATA_DIR=/wrong/data",
		"BD_EXPORT_AUTO=true",
	}

	tests := []struct {
		name     string
		args     []string
		wantDir  string
		wantDB   string
		wantHost string
		wantPort string
	}{
		{
			name:     "gt bead pins gastown despite stale ambient db",
			args:     []string{"gt-abc", "--json"},
			wantDir:  rigDir,
			wantDB:   "gastown",
			wantHost: "127.0.0.2",
			wantPort: "4407",
		},
		{
			name:     "hq bead remains town hq despite stale ambient db",
			args:     []string{"--json", "hq-abc"},
			wantDir:  townRoot,
			wantDB:   "hq",
			wantHost: "127.0.0.1",
			wantPort: "3307",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			invocation := newBdShowInvocation(tc.args, baseEnv)
			if invocation.Dir != tc.wantDir {
				t.Fatalf("Dir = %q, want %q", invocation.Dir, tc.wantDir)
			}
			if got, want := invocation.ExecArgs, append([]string{"bd", "show"}, tc.args...); !reflect.DeepEqual(got, want) {
				t.Fatalf("ExecArgs = %v, want %v", got, want)
			}
			if got, want := invocation.CommandArgs, append([]string{"show"}, tc.args...); !reflect.DeepEqual(got, want) {
				t.Fatalf("CommandArgs = %v, want %v", got, want)
			}

			envMap := parseEnv(invocation.Env)
			wantBeadsDir := filepath.Join(tc.wantDir, ".beads")
			if envMap["BEADS_DIR"] != wantBeadsDir {
				t.Fatalf("BEADS_DIR = %q, want %q in %v", envMap["BEADS_DIR"], wantBeadsDir, invocation.Env)
			}
			if envMap["BEADS_DOLT_SERVER_DATABASE"] != tc.wantDB {
				t.Fatalf("BEADS_DOLT_SERVER_DATABASE = %q, want %q in %v", envMap["BEADS_DOLT_SERVER_DATABASE"], tc.wantDB, invocation.Env)
			}
			if envMap["BEADS_DOLT_SERVER_HOST"] != tc.wantHost {
				t.Fatalf("BEADS_DOLT_SERVER_HOST = %q, want %q in %v", envMap["BEADS_DOLT_SERVER_HOST"], tc.wantHost, invocation.Env)
			}
			if envMap["BEADS_DOLT_SERVER_PORT"] != tc.wantPort || envMap["BEADS_DOLT_PORT"] != tc.wantPort {
				t.Fatalf("ports = server:%q legacy:%q, want %s in %v", envMap["BEADS_DOLT_SERVER_PORT"], envMap["BEADS_DOLT_PORT"], tc.wantPort, invocation.Env)
			}
			if countEnvKey(invocation.Env, "BEADS_DIR") != 1 || countEnvKey(invocation.Env, "BEADS_DOLT_SERVER_DATABASE") != 1 {
				t.Fatalf("expected single BEADS_DIR and DB env, got %v", invocation.Env)
			}
			for _, key := range []string{"BEADS_DB", "BD_DB", "BEADS_DOLT_DATA_DIR"} {
				if value, ok := envMap[key]; ok {
					t.Fatalf("%s should be stripped, got %q in %v", key, value, invocation.Env)
				}
			}
			if envMap["BD_EXPORT_AUTO"] != "false" {
				t.Fatalf("BD_EXPORT_AUTO = %q, want false in %v", envMap["BD_EXPORT_AUTO"], invocation.Env)
			}
		})
	}
}

func setupShowInvocationTown(t *testing.T) string {
	t.Helper()
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "gastown", "mayor", "rig")
	for _, dir := range []string{
		filepath.Join(townRoot, "mayor"),
		filepath.Join(townRoot, ".beads"),
		filepath.Join(rigDir, ".beads"),
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"type":"town","name":"test"}`), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	routes := strings.Join([]string{
		`{"prefix":"gt-","path":"gastown/mayor/rig"}`,
		`{"prefix":"hq-","path":"."}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "metadata.json"), []byte(`{"dolt_database":"hq","dolt_server_host":"127.0.0.1","dolt_server_port":3307}`), 0644); err != nil {
		t.Fatalf("write town metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rigDir, ".beads", "metadata.json"), []byte(`{"dolt_database":"gastown","dolt_server_host":"127.0.0.2","dolt_server_port":4407}`), 0644); err != nil {
		t.Fatalf("write rig metadata: %v", err)
	}
	return townRoot
}

func countEnvKey(env []string, key string) int {
	prefix := key + "="
	count := 0
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			count++
		}
	}
	return count
}
