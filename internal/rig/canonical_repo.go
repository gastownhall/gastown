package rig

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/git"
)

// CanonicalBareFetchRefspec is the fetch mapping required by the shared bare
// repository. It keeps remote-tracking refs visible to refinery and polecat
// worktrees while their local branches remain in the shared repository.
const CanonicalBareFetchRefspec = "+refs/heads/*:refs/remotes/origin/*"

// CanonicalRepoResult reports which missing parts of a rig's canonical git
// topology were created.
type CanonicalRepoResult struct {
	BareRepoPath            string
	RefineryWorktreePath    string
	DefaultBranch           string
	BareRepoCreated         bool
	RefineryWorktreeCreated bool
}

// EnsureCanonicalRepoTopology creates or validates the shared .repo.git bare
// repository and the dedicated refinery/rig worktree for a registered rig.
// It deliberately does not inspect, rewrite, or re-parent mayor and polecat
// worktrees, so active work in those repositories is preserved.
func EnsureCanonicalRepoTopology(rigPath string) (*CanonicalRepoResult, error) {
	cfg, err := LoadRigConfig(rigPath)
	if err != nil {
		return nil, fmt.Errorf("loading rig config: %w", err)
	}

	result, err := ensureCanonicalRepoTopology(rigPath, cfg)
	if err != nil {
		return nil, err
	}

	if cfg.DefaultBranch != result.DefaultBranch {
		cfg.DefaultBranch = result.DefaultBranch
		if err := saveRigConfigFile(rigPath, cfg); err != nil {
			return nil, fmt.Errorf("persisting detected default branch: %w", err)
		}
	}

	return result, nil
}

func ensureCanonicalRepoTopology(rigPath string, cfg *RigConfig) (*CanonicalRepoResult, error) {
	rigPath = filepath.Clean(rigPath)
	if strings.TrimSpace(cfg.GitURL) == "" {
		return nil, fmt.Errorf("rig config has no git_url; cannot create canonical repository topology")
	}

	bareGit, barePath, branch, bareCreated, err := ensureCanonicalBareRepo(rigPath, cfg)
	if err != nil {
		return nil, err
	}

	refineryPath := filepath.Join(rigPath, "refinery", "rig")
	refineryCreated, err := ensureCanonicalRefineryWorktree(bareGit, barePath, refineryPath, branch)
	if err != nil {
		return nil, err
	}

	return &CanonicalRepoResult{
		BareRepoPath:            barePath,
		RefineryWorktreePath:    refineryPath,
		DefaultBranch:           branch,
		BareRepoCreated:         bareCreated,
		RefineryWorktreeCreated: refineryCreated,
	}, nil
}

func ensureCanonicalBareRepo(rigPath string, cfg *RigConfig) (*git.Git, string, string, bool, error) {
	barePath := filepath.Join(rigPath, ".repo.git")
	branch := strings.TrimSpace(cfg.DefaultBranch)
	created := false

	info, statErr := os.Stat(barePath)
	switch {
	case os.IsNotExist(statErr):
		cloneGit := git.NewGit(rigPath)
		localRepo, _ := resolveLocalRepo(cfg.LocalRepo, cfg.GitURL)
		var cloneErr error
		if localRepo != "" {
			cloneErr = cloneGit.CloneBareWithReferenceAndBranch(cfg.GitURL, barePath, localRepo, branch)
		} else {
			cloneErr = cloneGit.CloneBareWithBranch(cfg.GitURL, barePath, branch)
		}
		if cloneErr != nil {
			return nil, "", "", false, fmt.Errorf("creating shared bare repository: %w", cloneErr)
		}
		created = true
	case statErr != nil:
		return nil, "", "", false, fmt.Errorf("checking shared bare repository: %w", statErr)
	case !info.IsDir():
		return nil, "", "", false, fmt.Errorf("shared bare repository path is not a directory: %s", barePath)
	}

	bareGit := git.NewGitWithDir(barePath, "")
	if err := bareGit.ValidateBareRepository(); err != nil {
		return nil, "", "", false, fmt.Errorf("validating shared bare repository %s: %w", barePath, err)
	}

	remotes, err := bareGit.Remotes()
	if err != nil {
		return nil, "", "", false, fmt.Errorf("listing shared bare repository remotes: %w", err)
	}
	if containsString(remotes, "origin") {
		originURL, err := bareGit.RemoteURL("origin")
		if err != nil {
			return nil, "", "", false, fmt.Errorf("reading shared bare repository origin: %w", err)
		}
		if strings.TrimSpace(originURL) != strings.TrimSpace(cfg.GitURL) {
			if _, err := bareGit.SetRemoteURL("origin", cfg.GitURL); err != nil {
				return nil, "", "", false, fmt.Errorf("configuring shared bare repository origin: %w", err)
			}
		}
	} else if _, err := bareGit.AddRemote("origin", cfg.GitURL); err != nil {
		return nil, "", "", false, fmt.Errorf("adding shared bare repository origin: %w", err)
	}

	refspec, err := bareGit.ConfigGet("remote.origin.fetch")
	if err != nil {
		return nil, "", "", false, fmt.Errorf("reading shared bare repository refspec: %w", err)
	}
	if strings.TrimSpace(refspec) != CanonicalBareFetchRefspec {
		if err := bareGit.ConfigSet("remote.origin.fetch", CanonicalBareFetchRefspec); err != nil {
			return nil, "", "", false, fmt.Errorf("configuring shared bare repository refspec: %w", err)
		}
	}

	if cfg.PushURL != "" {
		if err := bareGit.ConfigurePushURL("origin", cfg.PushURL); err != nil {
			return nil, "", "", false, fmt.Errorf("configuring shared bare repository push URL: %w", err)
		}
	}
	if cfg.UpstreamURL != "" {
		if err := bareGit.AddUpstreamRemote(cfg.UpstreamURL); err != nil {
			return nil, "", "", false, fmt.Errorf("configuring shared bare repository upstream: %w", err)
		}
	}

	if branch == "" {
		branch, err = bareGit.CurrentBranch()
		if err != nil {
			return nil, "", "", false, fmt.Errorf("detecting shared bare repository default branch: %w", err)
		}
		branch = strings.TrimSpace(branch)
		if branch == "" || branch == "HEAD" {
			return nil, "", "", false, fmt.Errorf("shared bare repository has no symbolic default branch")
		}
	}

	trackingExists, err := bareGit.RemoteTrackingBranchExists("origin", branch)
	if err != nil {
		return nil, "", "", false, fmt.Errorf("checking origin/%s in shared bare repository: %w", branch, err)
	}
	if !trackingExists {
		if err := bareGit.FetchBranchShallow("origin", branch); err != nil {
			return nil, "", "", false, fmt.Errorf("fetching integration branch %s into shared bare repository: %w", branch, err)
		}
	}

	branchExists, err := bareGit.BranchExists(branch)
	if err != nil {
		return nil, "", "", false, fmt.Errorf("checking local integration branch %s: %w", branch, err)
	}
	if !branchExists {
		if err := bareGit.CreateBranchFrom(branch, "refs/remotes/origin/"+branch); err != nil {
			return nil, "", "", false, fmt.Errorf("creating local integration branch %s: %w", branch, err)
		}
	}

	return bareGit, barePath, branch, created, nil
}

func ensureCanonicalRefineryWorktree(bareGit *git.Git, barePath, refineryPath, branch string) (bool, error) {
	info, statErr := os.Lstat(refineryPath)
	if statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return false, fmt.Errorf("canonical refinery worktree path is not a directory: %s", refineryPath)
		}

		gitEntry := filepath.Join(refineryPath, ".git")
		gitInfo, err := os.Lstat(gitEntry)
		if err != nil {
			if !os.IsNotExist(err) {
				return false, fmt.Errorf("checking refinery worktree git entry: %w", err)
			}
			entries, readErr := os.ReadDir(refineryPath)
			if readErr != nil {
				return false, fmt.Errorf("reading incomplete refinery worktree: %w", readErr)
			}
			if len(entries) != 0 {
				return false, fmt.Errorf("refinery worktree path exists without .git and is not empty: %s", refineryPath)
			}
			if err := os.Remove(refineryPath); err != nil {
				return false, fmt.Errorf("removing empty refinery worktree directory: %w", err)
			}
		} else {
			if gitInfo.Mode()&os.ModeSymlink != 0 || !gitInfo.Mode().IsRegular() {
				return false, fmt.Errorf("refinery/rig must be a worktree linked to %s", barePath)
			}
			gitDir, err := resolveWorktreeGitDir(refineryPath, gitEntry)
			if err != nil {
				return false, err
			}
			worktreesRoot := filepath.Join(barePath, "worktrees")
			rel, err := filepath.Rel(worktreesRoot, gitDir)
			if err != nil || rel == "." || rel == "" || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return false, fmt.Errorf("refinery/rig is not linked to canonical bare repository %s", barePath)
			}

			refineryGit := git.NewGit(refineryPath)
			if !refineryGit.IsRepo() {
				return false, fmt.Errorf("refinery/rig has an unusable worktree link to %s", gitDir)
			}
			currentBranch, err := refineryGit.CurrentBranch()
			if err != nil {
				return false, fmt.Errorf("reading refinery integration branch: %w", err)
			}
			if strings.TrimSpace(currentBranch) != branch {
				return false, fmt.Errorf("refinery/rig is on branch %q; canonical integration branch is %q", currentBranch, branch)
			}
			if err := refineryGit.ConfigureHooksPath(); err != nil {
				return false, fmt.Errorf("configuring refinery hooks: %w", err)
			}
			return false, nil
		}
	} else if !os.IsNotExist(statErr) {
		return false, fmt.Errorf("checking refinery worktree path: %w", statErr)
	}

	if err := os.MkdirAll(filepath.Dir(refineryPath), 0755); err != nil {
		return false, fmt.Errorf("creating refinery directory: %w", err)
	}
	if err := bareGit.WorktreePrune(); err != nil {
		return false, fmt.Errorf("pruning stale refinery worktree metadata: %w", err)
	}
	if err := bareGit.WorktreeAddExisting(refineryPath, branch); err != nil {
		return false, fmt.Errorf("creating refinery worktree on %s: %w", branch, err)
	}
	refineryGit := git.NewGit(refineryPath)
	if err := refineryGit.ConfigureHooksPath(); err != nil {
		return false, fmt.Errorf("configuring refinery hooks: %w", err)
	}
	return true, nil
}

func resolveWorktreeGitDir(worktreePath, gitEntry string) (string, error) {
	data, err := os.ReadFile(gitEntry)
	if err != nil {
		return "", fmt.Errorf("reading refinery worktree git entry: %w", err)
	}
	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(line, "gitdir: ") {
		return "", fmt.Errorf("invalid refinery worktree git entry: %s", gitEntry)
	}
	gitDir := strings.TrimSpace(strings.TrimPrefix(line, "gitdir: "))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(worktreePath, gitDir)
	}
	return filepath.Clean(gitDir), nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
