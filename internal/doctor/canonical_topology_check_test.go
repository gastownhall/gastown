package doctor

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/git"
	rigpkg "github.com/steveyegge/gastown/internal/rig"
)

func TestBareRepoExistsCheckFixBootstrapsAdoptedRigWithoutLinkedWorktrees(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "adopted"
	rigPath := filepath.Join(townRoot, rigName)
	remote := createDoctorTopologySourceRepo(t)
	writeDoctorTopologyConfig(t, rigPath, rigName, remote)

	check := NewBareRepoExistsCheck()
	ctx := &CheckContext{TownRoot: townRoot, RigName: rigName}
	if result := check.Run(ctx); result.Status != StatusError {
		t.Fatalf("Run status = %v, want error: %s", result.Status, result.Message)
	}
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix: %v", err)
	}
	if result := check.Run(ctx); result.Status != StatusOK {
		t.Fatalf("Run after Fix status = %v, want OK: %s (%v)", result.Status, result.Message, result.Details)
	}

	barePath := filepath.Join(rigPath, ".repo.git")
	if err := git.NewGitWithDir(barePath, "").ValidateBareRepository(); err != nil {
		t.Fatalf("restored bare repo invalid: %v", err)
	}
	refineryPath := filepath.Join(rigPath, "refinery", "rig")
	if got, err := git.NewGit(refineryPath).CurrentBranch(); err != nil || got != "main" {
		t.Fatalf("restored refinery branch = %q, %v; want main", got, err)
	}
}

func TestRefineryExistsCheckFixRestoresMissingDirectoryAndWorktree(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "repair"
	rigPath := filepath.Join(townRoot, rigName)
	remote := createDoctorTopologySourceRepo(t)
	writeDoctorTopologyConfig(t, rigPath, rigName, remote)

	barePath := filepath.Join(rigPath, ".repo.git")
	if err := git.NewGit(rigPath).CloneBareWithBranch(remote, barePath, "main"); err != nil {
		t.Fatalf("create canonical bare repo: %v", err)
	}

	check := NewRefineryExistsCheck()
	ctx := &CheckContext{TownRoot: townRoot, RigName: rigName}
	if result := check.Run(ctx); result.Status != StatusWarning {
		t.Fatalf("Run status = %v, want warning: %s", result.Status, result.Message)
	}
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix: %v", err)
	}
	if result := check.Run(ctx); result.Status != StatusOK {
		t.Fatalf("Run after Fix status = %v, want OK: %s (%v)", result.Status, result.Message, result.Details)
	}
	if info, err := os.Stat(filepath.Join(rigPath, "refinery", "rig", ".git")); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("refinery worktree link invalid: info=%v err=%v", info, err)
	}
	if _, err := os.Stat(filepath.Join(rigPath, "refinery", "mail", "inbox.jsonl")); err != nil {
		t.Fatalf("refinery inbox was not restored: %v", err)
	}
}

func createDoctorTopologySourceRepo(t *testing.T) string {
	t.Helper()
	repoPath := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(repoPath, 0755); err != nil {
		t.Fatalf("create source repo: %v", err)
	}
	commands := [][]string{
		{"init", "--initial-branch=main"},
		{"config", "user.email", "doctor@example.com"},
		{"config", "user.name", "Doctor Test"},
		{"commit", "--allow-empty", "-m", "initial"},
	}
	for _, args := range commands {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoPath
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return repoPath
}

func writeDoctorTopologyConfig(t *testing.T, rigPath, rigName, remote string) {
	t.Helper()
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatalf("create rig path: %v", err)
	}
	data, err := json.Marshal(&rigpkg.RigConfig{
		Type:          "rig",
		Version:       rigpkg.CurrentRigConfigVersion,
		Name:          rigName,
		GitURL:        remote,
		DefaultBranch: "main",
		CreatedAt:     time.Now(),
	})
	if err != nil {
		t.Fatalf("marshal rig config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rigPath, "config.json"), data, 0644); err != nil {
		t.Fatalf("write rig config: %v", err)
	}
}
