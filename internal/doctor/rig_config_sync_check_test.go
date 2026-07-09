package doctor

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRigConfigSyncCheck_MissingConfig(t *testing.T) {
	// Create temp town root
	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create rigs.json with one rig
	rigsJSON := `{
		"version": 1,
		"rigs": {
			"testrig": {
				"git_url": "https://github.com/test/test.git",
				"added_at": "2026-03-01T00:00:00Z",
				"beads": {
					"prefix": "tr"
				}
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte(rigsJSON), 0644); err != nil {
		t.Fatal(err)
	}

	// Create rig directory WITHOUT config.json
	rigDir := filepath.Join(tmpDir, "testrig")
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Run check
	ctx := &CheckContext{TownRoot: tmpDir}
	check := NewRigConfigSyncCheck()
	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning, got %v", result.Status)
	}
	if len(check.missingConfig) != 1 {
		t.Errorf("expected 1 missing config, got %d", len(check.missingConfig))
	}
}

func TestRigConfigSyncCheck_FixCreatesConfig(t *testing.T) {
	// Create temp town root
	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create rigs.json with one rig
	rigsJSON := `{
		"version": 1,
		"rigs": {
			"testrig": {
				"git_url": "https://github.com/test/test.git",
				"added_at": "2026-03-01T00:00:00Z",
				"beads": {
					"prefix": "tr"
				}
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte(rigsJSON), 0644); err != nil {
		t.Fatal(err)
	}

	// Create rig directory WITHOUT config.json
	rigDir := filepath.Join(tmpDir, "testrig")
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Run check and fix
	ctx := &CheckContext{TownRoot: tmpDir}
	check := NewRigConfigSyncCheck()
	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning, got %v", result.Status)
	}

	// Fix
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("fix failed: %v", err)
	}

	// Verify config.json was created
	configPath := filepath.Join(rigDir, "config.json")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Error("config.json was not created")
	}

	// Re-run check - should pass now
	result = check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK after fix, got %v: %s", result.Status, result.Message)
	}
}

func TestRigConfigSyncCheck_AllConfigsPresent(t *testing.T) {
	// Create temp town root
	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create rigs.json with one rig
	rigsJSON := `{
		"version": 1,
		"rigs": {
			"testrig": {
				"git_url": "https://github.com/test/test.git",
				"added_at": "2026-03-01T00:00:00Z",
				"beads": {
					"prefix": "tr"
				}
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte(rigsJSON), 0644); err != nil {
		t.Fatal(err)
	}

	// Create rig directory WITH config.json
	rigDir := filepath.Join(tmpDir, "testrig")
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatal(err)
	}
	configJSON := `{
		"type": "rig",
		"version": 1,
		"name": "testrig",
		"git_url": "https://github.com/test/test.git",
		"created_at": "2026-03-01T00:00:00Z",
		"beads": {
			"prefix": "tr"
		}
	}`
	if err := os.WriteFile(filepath.Join(rigDir, "config.json"), []byte(configJSON), 0644); err != nil {
		t.Fatal(err)
	}

	// Run check
	ctx := &CheckContext{TownRoot: tmpDir}
	check := NewRigConfigSyncCheck()
	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("expected StatusOK, got %v: %s", result.Status, result.Message)
	}
}

func TestRigConfigSyncCheck_FixDisablesRigAutoExport(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake bd stub is shell-specific")
	}

	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}
	rigsJSON := `{
		"version": 1,
		"rigs": {
			"testrig": {
				"git_url": "https://github.com/test/test.git",
				"beads": {"prefix": "tr"}
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte(rigsJSON), 0644); err != nil {
		t.Fatal(err)
	}

	rigDir := filepath.Join(tmpDir, "testrig")
	beadsDir := filepath.Join(rigDir, "mayor", "rig", ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	configJSON := `{
		"type": "rig",
		"version": 1,
		"name": "testrig",
		"git_url": "https://github.com/test/test.git",
		"beads": {"prefix": "tr"}
	}`
	if err := os.WriteFile(filepath.Join(rigDir, "config.json"), []byte(configJSON), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("prefix: tr\nissue-prefix: tr\nexport.auto: true\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"dolt_mode":"embedded"}`), 0644); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte("#!/usr/bin/env bash\nif [ \"$1\" = show ]; then echo \"[{\\\"id\\\":\\\"$2\\\"}]\"; fi\nexit 0\n"), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx := &CheckContext{TownRoot: tmpDir}
	check := NewRigConfigSyncCheck()
	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning, got %v: %s", result.Status, result.Message)
	}
	if len(check.missingExportCfg) != 1 || check.missingExportCfg[0] != "testrig" {
		t.Fatalf("missingExportCfg = %#v, want [testrig]", check.missingExportCfg)
	}

	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix failed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(beadsDir, "config.yaml"))
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	if !strings.Contains(string(data), "export.auto: \"false\"\n") {
		t.Fatalf("config.yaml did not disable export.auto:\n%s", string(data))
	}
	result = check.Run(ctx)
	if result.Status != StatusOK {
		t.Fatalf("Status after fix = %v, want %v: %s", result.Status, StatusOK, result.Message)
	}
}

func TestRigConfigSyncCheck_FixCreatesMissingMetadata(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake bd stub is shell-specific")
	}

	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}
	rigsJSON := `{
		"version": 1,
		"rigs": {
			"testrig": {
				"git_url": "https://github.com/test/test.git",
				"beads": {"prefix": "tr"}
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte(rigsJSON), 0644); err != nil {
		t.Fatal(err)
	}

	rigDir := filepath.Join(tmpDir, "testrig")
	beadsDir := filepath.Join(rigDir, "mayor", "rig", ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	configJSON := `{
		"type": "rig",
		"version": 1,
		"name": "testrig",
		"git_url": "https://github.com/test/test.git",
		"beads": {"prefix": "tr"}
	}`
	if err := os.WriteFile(filepath.Join(rigDir, "config.json"), []byte(configJSON), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("prefix: tr\nissue-prefix: tr\n"), 0644); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte("#!/usr/bin/env bash\nexit 0\n"), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx := &CheckContext{TownRoot: tmpDir}
	check := NewRigConfigSyncCheck()
	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning, got %v: %s", result.Status, result.Message)
	}
	if len(check.missingMetadata) != 1 || check.missingMetadata[0] != "testrig" {
		t.Fatalf("missingMetadata = %#v, want [testrig]", check.missingMetadata)
	}

	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix failed: %v", err)
	}
	metadataBytes, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		t.Fatalf("reading metadata.json: %v", err)
	}
	metadata := string(metadataBytes)
	if !strings.Contains(metadata, `"dolt_mode": "server"`) || !strings.Contains(metadata, `"dolt_database": "testrig"`) {
		t.Fatalf("metadata.json missing server config: %s", metadata)
	}
}

func TestRigConfigSyncCheck_FixCreatesMissingRootMetadata(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake bd stub is shell-specific")
	}

	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}
	rigsJSON := `{
		"version": 1,
		"rigs": {
			"testrig": {
				"git_url": "https://github.com/test/test.git",
				"beads": {"prefix": "tr"}
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte(rigsJSON), 0644); err != nil {
		t.Fatal(err)
	}

	rigDir := filepath.Join(tmpDir, "testrig")
	beadsDir := filepath.Join(rigDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	configJSON := `{
		"type": "rig",
		"version": 1,
		"name": "testrig",
		"git_url": "https://github.com/test/test.git",
		"beads": {"prefix": "tr"}
	}`
	if err := os.WriteFile(filepath.Join(rigDir, "config.json"), []byte(configJSON), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("prefix: tr\nissue-prefix: tr\n"), 0644); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte("#!/usr/bin/env bash\nexit 0\n"), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx := &CheckContext{TownRoot: tmpDir}
	check := NewRigConfigSyncCheck()
	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning, got %v: %s", result.Status, result.Message)
	}
	if len(check.missingMetadata) != 1 || check.missingMetadata[0] != "testrig" {
		t.Fatalf("missingMetadata = %#v, want [testrig]", check.missingMetadata)
	}

	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix failed: %v", err)
	}
	metadataBytes, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		t.Fatalf("reading root metadata.json: %v", err)
	}
	metadata := string(metadataBytes)
	if !strings.Contains(metadata, `"dolt_mode": "server"`) || !strings.Contains(metadata, `"dolt_database": "testrig"`) {
		t.Fatalf("metadata.json missing server config: %s", metadata)
	}
}

func TestRigConfigSyncCheck_DoltListErrorDoesNotMeanMissingDB(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake dolt stub is shell-specific")
	}

	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}
	rigsJSON := `{
		"version": 1,
		"rigs": {
			"testrig": {
				"git_url": "https://github.com/test/test.git",
				"beads": {"prefix": "tr"}
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte(rigsJSON), 0644); err != nil {
		t.Fatal(err)
	}

	rigDir := filepath.Join(tmpDir, "testrig")
	beadsDir := filepath.Join(rigDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	configJSON := `{
		"type": "rig",
		"version": 1,
		"name": "testrig",
		"git_url": "https://github.com/test/test.git"
	}`
	if err := os.WriteFile(filepath.Join(rigDir, "config.json"), []byte(configJSON), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("prefix: tr\nissue-prefix: tr\n"), 0644); err != nil {
		t.Fatal(err)
	}
	metadata := `{"backend":"dolt","database":"dolt","dolt_mode":"server","dolt_database":"testrig"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0644); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "dolt"), []byte("#!/usr/bin/env bash\necho unreachable >&2\nexit 1\n"), 0755); err != nil {
		t.Fatalf("write fake dolt: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GT_DOLT_HOST", "192.0.2.1")

	ctx := &CheckContext{TownRoot: tmpDir}
	check := NewRigConfigSyncCheck()
	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning, got %v: %s", result.Status, result.Message)
	}
	if len(check.missingDoltDB) != 0 {
		t.Fatalf("missingDoltDB = %#v, want none when DB listing fails", check.missingDoltDB)
	}
	if len(check.dbCheckErrors) != 1 || check.dbCheckErrors[0] != "testrig" {
		t.Fatalf("dbCheckErrors = %#v, want [testrig]", check.dbCheckErrors)
	}
}

func TestRigConfigSyncCheck_FixMissingDoltDBUsesCanonicalDatabase(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake bd arg/env logging is shell-specific")
	}

	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}
	rigsJSON := `{
		"version": 1,
		"rigs": {
			"testrig": {
				"git_url": "https://github.com/test/test.git",
				"beads": {"prefix": "tr"}
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte(rigsJSON), 0644); err != nil {
		t.Fatal(err)
	}
	mayorRigDir := filepath.Join(tmpDir, "testrig", "mayor", "rig")
	if err := os.MkdirAll(mayorRigDir, 0755); err != nil {
		t.Fatal(err)
	}

	cmdLog := filepath.Join(t.TempDir(), "bd-cmds.log")
	binDir := t.TempDir()
	script := `#!/usr/bin/env bash
set -e
printf 'args=%s env=%s beads=%s db=%s\n' "$*" "${BEADS_DOLT_SERVER_DATABASE:-<unset>}" "${BEADS_DIR:-<unset>}" "${BEADS_DB:-<unset>}" >> "$BD_CMD_LOG"
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_CMD_LOG", cmdLog)
	t.Setenv("BEADS_DIR", filepath.Join(tmpDir, "wrong", ".beads"))
	t.Setenv("BEADS_DB", filepath.Join(tmpDir, "wrong.db"))
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "wrong_db")

	ctx := &CheckContext{TownRoot: tmpDir}
	check := NewRigConfigSyncCheck()
	check.missingDoltDB = []string{"testrig"}

	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix failed: %v", err)
	}

	logData, err := os.ReadFile(cmdLog)
	if err != nil {
		t.Fatalf("reading command log: %v", err)
	}
	cmds := string(logData)
	if !strings.Contains(cmds, "args=init --prefix tr --database testrig --server --server-port") {
		t.Fatalf("bd init did not use canonical database; log:\n%s", cmds)
	}
	if !strings.Contains(cmds, "env=testrig") {
		t.Fatalf("bd init did not receive canonical database env; log:\n%s", cmds)
	}
	if !strings.Contains(cmds, "beads="+filepath.Join(mayorRigDir, ".beads")) {
		t.Fatalf("bd init did not receive mayor/rig BEADS_DIR; log:\n%s", cmds)
	}
	if strings.Contains(cmds, "wrong_db") || strings.Contains(cmds, "wrong.db") || strings.Contains(cmds, filepath.Join(tmpDir, "wrong", ".beads")) {
		t.Fatalf("stale BEADS env leaked into bd subprocess; log:\n%s", cmds)
	}
}

// TestRigConfigSyncCheck_PrefixNamedDoltDBNoMismatch reproduces gt-5hd2: a rig
// whose Dolt data physically lives in a PREFIX-named directory (.dolt-data/bd)
// rather than a rig-name directory (.dolt-data/beads) must NOT be reported as a
// "DB name mismatch", and --fix must NOT revert its metadata.json back to the
// non-existent rig-name database.
func TestRigConfigSyncCheck_PrefixNamedDoltDBNoMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake dolt stub is shell-specific")
	}

	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Rig "beads" with prefix "bd"; its DB lives at .dolt-data/bd.
	rigsJSON := `{
		"version": 1,
		"rigs": {
			"beads": {
				"git_url": "https://github.com/test/beads.git",
				"beads": {"prefix": "bd"}
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte(rigsJSON), 0644); err != nil {
		t.Fatal(err)
	}

	// Physical Dolt data lives under the prefix name, not the rig name. Lay down
	// the minimal valid-database layout (.dolt/noms/manifest) so ListDatabases'
	// local filesystem scan recognizes it.
	bdManifestDir := filepath.Join(tmpDir, ".dolt-data", "bd", ".dolt", "noms")
	if err := os.MkdirAll(bdManifestDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bdManifestDir, "manifest"), []byte("0"), 0644); err != nil {
		t.Fatal(err)
	}

	rigDir := filepath.Join(tmpDir, "beads")
	beadsDir := filepath.Join(rigDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	configJSON := `{
		"type": "rig",
		"version": 1,
		"name": "beads",
		"git_url": "https://github.com/test/beads.git",
		"beads": {"prefix": "bd"}
	}`
	if err := os.WriteFile(filepath.Join(rigDir, "config.json"), []byte(configJSON), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("prefix: bd\nissue-prefix: bd\nexport.auto: \"false\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// metadata points at the real prefix-named DB "bd".
	metadata := `{"backend":"dolt","database":"dolt","dolt_mode":"server","dolt_database":"bd"}`
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	if err := os.WriteFile(metadataPath, []byte(metadata), 0644); err != nil {
		t.Fatal(err)
	}

	// Fake bd so the identity-bead lookup succeeds.
	binDir := t.TempDir()
	bdScript := "#!/usr/bin/env bash\nif [ \"$1\" = show ]; then echo \"[{\\\"id\\\":\\\"$2\\\"}]\"; fi\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx := &CheckContext{TownRoot: tmpDir}
	check := NewRigConfigSyncCheck()
	check.Run(ctx)

	// Core regression assertion: no false DB name mismatch for the prefix-named DB.
	if len(check.dbNameMismatches) != 0 {
		t.Fatalf("dbNameMismatches = %#v, want none for prefix-named DB", check.dbNameMismatches)
	}
	if len(check.missingDoltDB) != 0 {
		t.Fatalf("missingDoltDB = %#v, want none", check.missingDoltDB)
	}

	// Fix must not revert metadata.json back to the rig-name DB.
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix failed: %v", err)
	}
	after, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("read metadata.json: %v", err)
	}
	if !strings.Contains(string(after), `"dolt_database":"bd"`) {
		t.Fatalf("Fix reverted metadata away from prefix-named DB: %s", string(after))
	}
}

func TestRigConfigSyncCheck_DeaconTownDoltDBNoMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake bd stub is shell-specific")
	}

	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}
	rigsJSON := `{
		"version": 1,
		"rigs": {
			"deacon": {
				"git_url": "https://github.com/test/deacon.git",
				"beads": {"prefix": "dc"}
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte(rigsJSON), 0644); err != nil {
		t.Fatal(err)
	}

	townBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townBeadsDir, "metadata.json"), []byte(`{"backend":"dolt","dolt_mode":"server","dolt_database":"hq"}`), 0644); err != nil {
		t.Fatal(err)
	}
	writeTestDoltDatabase(t, tmpDir, "hq")

	rigDir := filepath.Join(tmpDir, "deacon")
	beadsDir := filepath.Join(rigDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	configJSON := `{"type":"rig","version":1,"name":"deacon","git_url":"https://github.com/test/deacon.git","beads":{"prefix":"dc"}}`
	if err := os.WriteFile(filepath.Join(rigDir, "config.json"), []byte(configJSON), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("prefix: dc\nissue-prefix: dc\nexport.auto: \"false\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	metadata := `{"backend":"dolt","database":"dolt","dolt_mode":"server","dolt_database":"hq"}`
	if err := os.WriteFile(metadataPath, []byte(metadata), 0644); err != nil {
		t.Fatal(err)
	}
	setFakeBDShow(t)

	ctx := &CheckContext{TownRoot: tmpDir}
	check := NewRigConfigSyncCheck()
	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Fatalf("Status = %v, want StatusOK: %s %#v", result.Status, result.Message, result.Details)
	}
	if len(check.dbNameMismatches) != 0 {
		t.Fatalf("dbNameMismatches = %#v, want none for deacon town DB", check.dbNameMismatches)
	}
	if len(check.missingDoltDB) != 0 {
		t.Fatalf("missingDoltDB = %#v, want none", check.missingDoltDB)
	}

	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix failed: %v", err)
	}
	after, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("read metadata.json: %v", err)
	}
	if !strings.Contains(string(after), `"dolt_database":"hq"`) {
		t.Fatalf("Fix rewrote deacon away from town DB: %s", string(after))
	}
}

func TestRigConfigSyncCheck_OrdinaryRigTownDoltDBMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake bd stub is shell-specific")
	}

	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}
	rigsJSON := `{
		"version": 1,
		"rigs": {
			"testrig": {
				"git_url": "https://github.com/test/test.git",
				"beads": {"prefix": "tr"}
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte(rigsJSON), 0644); err != nil {
		t.Fatal(err)
	}

	townBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townBeadsDir, "metadata.json"), []byte(`{"backend":"dolt","dolt_mode":"server","dolt_database":"hq"}`), 0644); err != nil {
		t.Fatal(err)
	}
	writeTestDoltDatabase(t, tmpDir, "hq")
	writeTestDoltDatabase(t, tmpDir, "testrig")

	rigDir := filepath.Join(tmpDir, "testrig")
	beadsDir := filepath.Join(rigDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	configJSON := `{"type":"rig","version":1,"name":"testrig","git_url":"https://github.com/test/test.git","beads":{"prefix":"tr"}}`
	if err := os.WriteFile(filepath.Join(rigDir, "config.json"), []byte(configJSON), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("prefix: tr\nissue-prefix: tr\nexport.auto: \"false\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	metadata := `{"backend":"dolt","database":"dolt","dolt_mode":"server","dolt_database":"hq"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0644); err != nil {
		t.Fatal(err)
	}
	setFakeBDShow(t)

	ctx := &CheckContext{TownRoot: tmpDir}
	check := NewRigConfigSyncCheck()
	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Fatalf("Status = %v, want StatusWarning", result.Status)
	}
	if len(check.dbNameMismatches) != 1 {
		t.Fatalf("dbNameMismatches = %#v, want one mismatch", check.dbNameMismatches)
	}
	mismatch := check.dbNameMismatches[0]
	if mismatch.rigName != "testrig" || mismatch.currentDB != "hq" || mismatch.expectedDB != "testrig" {
		t.Fatalf("dbNameMismatch = %#v, want testrig hq -> testrig", mismatch)
	}
}

func writeTestDoltDatabase(t *testing.T, townRoot, dbName string) {
	t.Helper()
	manifestDir := filepath.Join(townRoot, ".dolt-data", dbName, ".dolt", "noms")
	if err := os.MkdirAll(manifestDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "manifest"), []byte("0"), 0644); err != nil {
		t.Fatal(err)
	}
}

func setFakeBDShow(t *testing.T) {
	t.Helper()
	binDir := t.TempDir()
	script := "#!/usr/bin/env bash\nif [ \"$1\" = show ]; then echo \"[{\\\"id\\\":\\\"$2\\\"}]\"; fi\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestStaleRuntimeFilesCheck_StalePIDFiles(t *testing.T) {
	// Create temp town root
	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create rigs.json with no rigs
	rigsJSON := `{"version": 1, "rigs": {}}`
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte(rigsJSON), 0644); err != nil {
		t.Fatal(err)
	}

	// Create stale PID file for removed rig "pir"
	pidsDir := filepath.Join(tmpDir, ".runtime", "pids")
	if err := os.MkdirAll(pidsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pidsDir, "pir-witness.pid"), []byte("12345"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create valid PID file for town agent
	if err := os.WriteFile(filepath.Join(pidsDir, "hq-deacon.pid"), []byte("12346"), 0644); err != nil {
		t.Fatal(err)
	}

	// Run check
	ctx := &CheckContext{TownRoot: tmpDir}
	check := NewStaleRuntimeFilesCheck()
	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning, got %v", result.Status)
	}
	if len(check.stalePIDFiles) != 1 {
		t.Errorf("expected 1 stale PID file, got %d", len(check.stalePIDFiles))
	}
}

func TestStaleRuntimeFilesCheck_StaleWispConfig(t *testing.T) {
	// Create temp town root
	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create rigs.json with no rigs
	rigsJSON := `{"version": 1, "rigs": {}}`
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte(rigsJSON), 0644); err != nil {
		t.Fatal(err)
	}

	// Create stale wisp config for removed rig "pir"
	wispDir := filepath.Join(tmpDir, ".beads-wisp", "config")
	if err := os.MkdirAll(wispDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wispDir, "pir.json"), []byte(`{"rig": "pir"}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Run check
	ctx := &CheckContext{TownRoot: tmpDir}
	check := NewStaleRuntimeFilesCheck()
	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning, got %v", result.Status)
	}
	if len(check.staleWispConfigs) != 1 {
		t.Errorf("expected 1 stale wisp config, got %d", len(check.staleWispConfigs))
	}
}

func TestStaleRuntimeFilesCheck_Fix(t *testing.T) {
	// Create temp town root
	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create rigs.json with no rigs
	rigsJSON := `{"version": 1, "rigs": {}}`
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte(rigsJSON), 0644); err != nil {
		t.Fatal(err)
	}

	// Create stale files
	pidsDir := filepath.Join(tmpDir, ".runtime", "pids")
	if err := os.MkdirAll(pidsDir, 0755); err != nil {
		t.Fatal(err)
	}
	stalePID := filepath.Join(pidsDir, "pir-witness.pid")
	if err := os.WriteFile(stalePID, []byte("12345"), 0644); err != nil {
		t.Fatal(err)
	}

	wispDir := filepath.Join(tmpDir, ".beads-wisp", "config")
	if err := os.MkdirAll(wispDir, 0755); err != nil {
		t.Fatal(err)
	}
	staleWisp := filepath.Join(wispDir, "pir.json")
	if err := os.WriteFile(staleWisp, []byte(`{"rig": "pir"}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Run check and fix
	ctx := &CheckContext{TownRoot: tmpDir}
	check := NewStaleRuntimeFilesCheck()
	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning, got %v", result.Status)
	}

	// Fix
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("fix failed: %v", err)
	}

	// Verify files were removed
	if _, err := os.Stat(stalePID); !os.IsNotExist(err) {
		t.Error("stale PID file was not removed")
	}
	if _, err := os.Stat(staleWisp); !os.IsNotExist(err) {
		t.Error("stale wisp config was not removed")
	}

	// Re-run check - should pass
	result = check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK after fix, got %v", result.Status)
	}
}
