package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// UAT Low #12: operators followed docs/INSTALLING.md Step 4 inside a
// disposable town and ran `gt enable`, which writes the host-global
// ~/.local/state/gastown/state.json. A test/second town must not look
// like it has to flip machine-wide Gas Town enablement.
func TestInstallingDocsDoNotTreatEnableAsTownScoped(t *testing.T) {
	root := findModuleRoot(t)
	body := readRepoFile(t, filepath.Join(root, "docs", "INSTALLING.md"))

	step4, ok := markdownSection(body, "### Step 4: Verify Installation")
	if !ok {
		t.Fatal("docs/INSTALLING.md is missing Step 4: Verify Installation")
	}

	verifyBlock, ok := firstFencedBlock(step4)
	if !ok {
		t.Fatal("docs/INSTALLING.md Step 4 is missing a command block")
	}
	if strings.Contains(verifyBlock, "gt enable") {
		t.Fatal("docs/INSTALLING.md Step 4 treats `gt enable` as a town verify step; enable is machine-wide and must not be required to boot a test town")
	}
	if !strings.Contains(verifyBlock, "gt up") {
		t.Fatal("docs/INSTALLING.md Step 4 should still boot the town with `gt up`")
	}
	if !strings.Contains(step4, "machine-wide") {
		t.Fatal("docs/INSTALLING.md Step 4 must say `gt enable` is machine-wide")
	}
}

func TestEnableHelpSaysMachineWideNotTownScoped(t *testing.T) {
	help := enableCmd.Short + "\n" + enableCmd.Long
	if !strings.Contains(help, "machine-wide") && !strings.Contains(help, "this machine") {
		t.Fatalf("gt enable help should say enablement is machine-wide, got:\n%s", help)
	}
	if strings.Contains(help, "current town") || strings.Contains(help, "this town") {
		t.Fatalf("gt enable help must not sound town-scoped:\n%s", help)
	}
	if !strings.Contains(help, "gt up") {
		t.Fatal("gt enable help should say `gt up` does not require a prior enable")
	}
}

func TestInstallWithoutShellDoesNotWriteHostEnableState(t *testing.T) {
	tmpDir := t.TempDir()
	hqPath := filepath.Join(tmpDir, "throwaway-town")
	stateHome := filepath.Join(tmpDir, "xdg-state")
	gtBinary := buildGT(t)

	env := installTestEnvWithFakeBD(t, tmpDir)
	env = append(env, "XDG_STATE_HOME="+stateHome)

	cmd := exec.Command(gtBinary, "install", hqPath, "--no-beads", "--name", "throwaway")
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gt install --no-beads should succeed without writing host enable state: %v\nOutput:\n%s", err, output)
	}

	statePath := filepath.Join(stateHome, "gastown", "state.json")
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("throwaway gt install wrote host-global %s; test towns must not flip machine enablement", statePath)
	}
}

func findModuleRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := wd
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			return root
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("could not find module root (go.mod)")
		}
		root = parent
	}
}

func readRepoFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func markdownSection(body, heading string) (string, bool) {
	start := strings.Index(body, heading)
	if start < 0 {
		return "", false
	}
	rest := body[start+len(heading):]
	next := strings.Index(rest, "\n### ")
	if next < 0 {
		return rest, true
	}
	return rest[:next], true
}

func firstFencedBlock(body string) (string, bool) {
	start := strings.Index(body, "```")
	if start < 0 {
		return "", false
	}
	rest := body[start+3:]
	if nl := strings.Index(rest, "\n"); nl >= 0 {
		rest = rest[nl+1:]
	}
	end := strings.Index(rest, "```")
	if end < 0 {
		return "", false
	}
	return rest[:end], true
}
