package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTempAuditAcceptsBoundedArtifactsAndRejectsClones(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "small.log"), []byte("diagnostic"), 0o600); err != nil {
		t.Fatal(err)
	}

	run := func() (string, error) {
		repoRoot, err := filepath.Abs("..")
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("bash", "scripts/audit-temp-workspaces.sh")
		cmd.Dir = repoRoot
		cmd.Env = append(os.Environ(), "GT_TEMP_AUDIT_ROOT="+root, "GT_TEMP_AUDIT_MAX_KB=64")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	if out, err := run(); err != nil {
		t.Fatalf("clean audit failed: %v\n%s", err, out)
	}

	clone := filepath.Join(root, "loose-clone")
	if err := os.MkdirAll(filepath.Join(clone, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := run()
	if err == nil {
		t.Fatalf("audit accepted a Git checkout:\n%s", out)
	}
	if !strings.Contains(out, "Git checkout") {
		t.Fatalf("audit did not identify the violation:\n%s", out)
	}
}
