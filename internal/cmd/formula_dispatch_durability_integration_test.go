//go:build e2e

package cmd

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/jonbaldie/gastown/internal/testutil"
)

// TestStandaloneFormulaHookSurvivesCommittedReplica is the GH#4527 feedback
// loop. Run it inside a resource-capped Docker container; it needs bd, Dolt,
// and the repository's e2e toolchain.
func TestStandaloneFormulaHookSurvivesCommittedReplica(t *testing.T) {
	tmpDir := t.TempDir()
	hqPath := filepath.Join(tmpDir, "town")
	env, doltPort := isolatedE2EDoltEnv(t, tmpDir)
	configureGitIdentity(t, env)
	configureDoltIdentityForEnv(t, env)
	testutil.ReapOwnedDoltOnCleanup(t, hqPath)

	gtBinary := buildGT(t)
	runFormulaDurabilityCmd(t, "", env, gtBinary, "install", hqPath, "--name", "formula-durability", "--git", "--dolt-port", doltPort)
	t.Cleanup(func() {
		cmdEnv := append([]string(nil), env...)
		_ = runFormulaDurabilityCleanupCmd(hqPath, cmdEnv, gtBinary, "dolt", "stop")
	})

	deaconDir := filepath.Join(hqPath, "deacon")
	slingEnv := append(append([]string(nil), env...), "GT_ROLE=deacon", "GT_TEST_NO_NUDGE=1")
	runFormulaDurabilityCmd(t, deaconDir, slingEnv, gtBinary, "sling", "mol-dog-reaper", "--no-boot")

	writerHook := readFormulaHookStatus(t, deaconDir, env, gtBinary, "deacon")
	if writerHook.BeadID == "" || writerHook.Status != "hooked" {
		t.Fatalf("writer hook = %+v, want hooked formula work", writerHook)
	}

	// Push only committed Dolt state to a local remote, then serve a clone as a
	// second database. This models another machine reading refs/dolt/data rather
	// than sharing the writer's uncommitted working set.
	runFormulaDurabilityCmd(t, hqPath, env, gtBinary, "dolt", "stop")
	remoteDir := filepath.Join(tmpDir, "remote")
	if err := os.Mkdir(remoteDir, 0755); err != nil {
		t.Fatalf("mkdir remote: %v", err)
	}
	runFormulaDurabilityCmd(t, remoteDir, env, "dolt", "init")

	hqDatabaseDir := filepath.Join(hqPath, ".dolt-data", "hq")
	runFormulaDurabilityCmd(t, hqDatabaseDir, env, "dolt", "remote", "add", "durability-test", "file://"+remoteDir)
	runFormulaDurabilityCmd(t, hqDatabaseDir, env, "dolt", "push", "durability-test", "main")

	runFormulaDurabilityCmd(t, tmpDir, env, "dolt", "clone", "file://"+remoteDir, "reader")
	readerDatabaseDir := filepath.Join(hqPath, ".dolt-data", "reader")
	if err := os.Rename(filepath.Join(tmpDir, "reader"), readerDatabaseDir); err != nil {
		t.Fatalf("move reader database: %v", err)
	}

	readerTown := filepath.Join(tmpDir, "reader-town")
	runFormulaDurabilityCmd(t, tmpDir, env, "cp", "-a", hqPath, readerTown)
	if err := os.RemoveAll(filepath.Join(readerTown, ".dolt-data")); err != nil {
		t.Fatalf("remove copied Dolt data: %v", err)
	}
	setReplicaDatabase(t, filepath.Join(readerTown, ".beads", "metadata.json"), "reader")

	runFormulaDurabilityCmd(t, hqPath, env, gtBinary, "dolt", "start")
	replicaHook := readFormulaHookStatus(t, filepath.Join(readerTown, "deacon"), env, gtBinary, "deacon")
	if replicaHook.BeadID != writerHook.BeadID || replicaHook.Status != "hooked" {
		t.Fatalf("replica hook = %+v, want hooked bead %s from writer", replicaHook, writerHook.BeadID)
	}
}

type formulaHookStatus struct {
	BeadID string `json:"bead_id"`
	Status string `json:"status"`
}

func readFormulaHookStatus(t *testing.T, dir string, env []string, gtBinary, agent string) formulaHookStatus {
	t.Helper()
	out := runFormulaDurabilityOutputCmd(t, dir, env, gtBinary, "hook", "show", agent, "--json")
	var status formulaHookStatus
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		t.Fatalf("parse hook status: %v\n%s", err, out)
	}
	return status
}

func configureDoltIdentityForEnv(t *testing.T, env []string) {
	t.Helper()
	for _, args := range [][]string{
		{"config", "--global", "--add", "user.name", "Test User"},
		{"config", "--global", "--add", "user.email", "test@test.com"},
	} {
		runFormulaDurabilityCmd(t, "", env, "dolt", args...)
	}
}

func setReplicaDatabase(t *testing.T, metadataPath, database string) {
	t.Helper()
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatalf("parse metadata: %v", err)
	}
	metadata["dolt_database"] = database
	data, err = json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(metadataPath, data, 0644); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
}

func runFormulaDurabilityCmd(t *testing.T, dir string, env []string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
	}
	return string(out)
}

func runFormulaDurabilityOutputCmd(t *testing.T, dir string, env []string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("%s %v failed: %v\n%s", name, args, err, exitErr.Stderr)
		}
		t.Fatalf("%s %v failed: %v", name, args, err)
	}
	return string(out)
}

func runFormulaDurabilityCleanupCmd(dir string, env []string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = env
	return cmd.Run()
}
