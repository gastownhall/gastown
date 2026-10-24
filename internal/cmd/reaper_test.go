package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReaperDatabaseNamesTrimsConfiguredList(t *testing.T) {
	oldDB := reaperDB
	t.Cleanup(func() { reaperDB = oldDB })

	reaperDB = " hq, gastown ,, beads "
	got := reaperDatabaseNames()
	want := []string{"hq", "gastown", "beads"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reaperDatabaseNames() = %#v, want %#v", got, want)
	}
}

func TestWaitBeforeReaperDatabase(t *testing.T) {
	oldDelay := reaperDBDelay
	t.Cleanup(func() { reaperDBDelay = oldDelay })

	reaperDBDelay = "0s"
	if err := waitBeforeReaperDatabase(0); err != nil {
		t.Fatalf("first database wait returned error: %v", err)
	}
	if err := waitBeforeReaperDatabase(1); err != nil {
		t.Fatalf("zero-delay wait returned error: %v", err)
	}

	reaperDBDelay = "not-a-duration"
	if err := waitBeforeReaperDatabase(1); err == nil {
		t.Fatal("invalid delay should return an error")
	}
}

func TestDefaultReaperEndpointIgnoresStaleBeadsAliases(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)
	t.Setenv("GT_DOLT_HOST", "")
	t.Setenv("GT_DOLT_PORT", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "stale-host")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "9999")
	t.Setenv("BEADS_DOLT_PORT", "9999")

	host, port := defaultReaperEndpoint()
	if host != "127.0.0.1" || port != 3307 {
		t.Fatalf("defaultReaperEndpoint() = %s:%d, want 127.0.0.1:3307", host, port)
	}
}

func TestDefaultReaperEndpointUsesTownConfig(t *testing.T) {
	townRoot := t.TempDir()
	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "town.json"), []byte(`{"name":"test-town"}`), 0644); err != nil {
		t.Fatal(err)
	}
	doltDataDir := filepath.Join(townRoot, ".dolt-data")
	if err := os.MkdirAll(doltDataDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(doltDataDir, "config.yaml"), []byte("listener:\n  host: 127.0.0.2\n  port: 5507\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(townRoot)
	t.Setenv("GT_DOLT_IGNORE_CONFIG", "")
	t.Setenv("GT_DOLT_HOST", "")
	t.Setenv("GT_DOLT_PORT", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "stale-host")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "9999")
	t.Setenv("BEADS_DOLT_PORT", "9999")

	host, port := defaultReaperEndpoint()
	if host != "127.0.0.2" || port != 5507 {
		t.Fatalf("defaultReaperEndpoint() = %s:%d, want 127.0.0.2:5507", host, port)
	}
}

func TestCurrentBeadsDatabase(t *testing.T) {
	writeMetadata := func(t *testing.T, beadsDir, body string) {
		t.Helper()
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("walks up from cwd and follows redirect", func(t *testing.T) {
		root := t.TempDir()
		writeMetadata(t, filepath.Join(root, "mayor", "rig", ".beads"), `{"dolt_database":"myrig"}`)
		witnessBeads := filepath.Join(root, "witness", ".beads")
		if err := os.MkdirAll(witnessBeads, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(witnessBeads, "redirect"), []byte("../mayor/rig/.beads\n"), 0644); err != nil {
			t.Fatal(err)
		}
		sub := filepath.Join(root, "witness", "sub")
		if err := os.MkdirAll(sub, 0755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BEADS_DIR", "")
		t.Chdir(sub)

		got, err := currentBeadsDatabase()
		if err != nil {
			t.Fatalf("currentBeadsDatabase: %v", err)
		}
		if got != "myrig" {
			t.Fatalf("currentBeadsDatabase() = %q, want myrig", got)
		}
	})

	t.Run("BEADS_DIR wins over cwd", func(t *testing.T) {
		cwdRoot := t.TempDir()
		writeMetadata(t, filepath.Join(cwdRoot, ".beads"), `{"dolt_database":"cwd_db"}`)
		envBeads := filepath.Join(t.TempDir(), ".beads")
		writeMetadata(t, envBeads, `{"dolt_database":"env_db"}`)
		t.Setenv("BEADS_DIR", envBeads)
		t.Chdir(cwdRoot)

		got, err := currentBeadsDatabase()
		if err != nil {
			t.Fatalf("currentBeadsDatabase: %v", err)
		}
		if got != "env_db" {
			t.Fatalf("currentBeadsDatabase() = %q, want env_db", got)
		}
	})

	t.Run("no dolt_database is an error", func(t *testing.T) {
		root := t.TempDir()
		writeMetadata(t, filepath.Join(root, ".beads"), `{"backend":"dolt"}`)
		t.Setenv("BEADS_DIR", "")
		t.Chdir(root)

		if got, err := currentBeadsDatabase(); err == nil {
			t.Fatalf("currentBeadsDatabase() = %q, want error", got)
		}
	})
}
