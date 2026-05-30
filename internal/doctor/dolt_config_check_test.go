package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoltConfigCheck_DetectsMissingSharedKeys(t *testing.T) {
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"dolt_mode":"server","dolt_server_host":"127.0.0.1","dolt_server_port":3307,"dolt_database":"gastown"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("prefix:\nissue-prefix:\ndolt.idle-timeout: \"0\"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewDoltConfigCheck()
	result := check.Run(&CheckContext{TownRoot: townRoot})
	if result.Status != StatusError {
		t.Fatalf("Status = %v, want %v", result.Status, StatusError)
	}
	if len(result.Details) != 1 || !strings.Contains(result.Details[0], "storage.backend unset") || !strings.Contains(result.Details[0], "dolt.server unset") || !strings.Contains(result.Details[0], "dolt.port unset") {
		t.Fatalf("Details = %#v, want missing Dolt keys", result.Details)
	}
}

func TestDoltConfigCheck_DetectsPolecatRedirectConfig(t *testing.T) {
	townRoot := t.TempDir()
	targetBeads := filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads")
	polecatBeads := filepath.Join(townRoot, "gastown", "polecats", "guzzle", "gastown", ".beads")
	if err := os.MkdirAll(targetBeads, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(polecatBeads, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetBeads, "metadata.json"), []byte(`{"dolt_mode":"server","dolt_server_host":"127.0.0.1","dolt_server_port":3307,"dolt_database":"gastown"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetBeads, "config.yaml"), []byte("storage.backend: dolt\ndolt.server: \"127.0.0.1\"\ndolt.port: 3307\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(polecatBeads, "redirect"), []byte("../../../mayor/rig/.beads\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(polecatBeads, "config.yaml"), []byte("prefix:\nissue-prefix:\n"), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewDoltConfigCheck()
	result := check.Run(&CheckContext{TownRoot: townRoot})
	if result.Status != StatusError {
		t.Fatalf("Status = %v, want %v", result.Status, StatusError)
	}
	if len(check.targets) != 1 || check.targets[0].beadsDir != polecatBeads {
		t.Fatalf("targets = %#v, want only polecat beads dir", check.targets)
	}
}

func TestDoltConfigCheck_FixAddsMissingKeysOnly(t *testing.T) {
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"dolt_mode":"server","dolt_server_host":"127.0.0.1","dolt_server_port":3307,"dolt_database":"hq"}`), 0644); err != nil {
		t.Fatal(err)
	}
	initial := "prefix: hq\nissue-prefix: hq\nexport.auto: \"false\"\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(initial), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewDoltConfigCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	result := check.Run(ctx)
	if result.Status != StatusError {
		t.Fatalf("Status = %v, want %v", result.Status, StatusError)
	}
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(beadsDir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{"prefix: hq\n", "issue-prefix: hq\n", "export.auto: \"false\"\n", "storage.backend: dolt\n", "dolt.server: \"127.0.0.1\"\n", "dolt.port: 3307\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("config.yaml missing %q after fix:\n%s", want, got)
		}
	}

	result = check.Run(ctx)
	if result.Status != StatusOK {
		t.Fatalf("Status after fix = %v, want %v: %s details=%v", result.Status, StatusOK, result.Message, result.Details)
	}
}
