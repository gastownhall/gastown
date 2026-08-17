package rig

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/git"
)

// AddLocalRig registers a local git repository as a rig without a network clone.
// Polecat worktrees later share objects from a local bare clone of srcRepo.
func (m *Manager) AddLocalRig(ctx context.Context, name, srcRepo string) (*Rig, error) {
	if m.RigExists(name) {
		return nil, ErrRigExists
	}
	if strings.ContainsAny(name, "-. /\\") {
		return nil, fmt.Errorf("rig name %q contains invalid characters", name)
	}
	for _, reserved := range reservedRigNames {
		if strings.EqualFold(name, reserved) {
			return nil, fmt.Errorf("rig name %q is reserved for town-level infrastructure", reserved)
		}
	}

	absSrc, err := filepath.Abs(srcRepo)
	if err != nil {
		return nil, fmt.Errorf("resolving local repo: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(absSrc); err == nil {
		absSrc = resolved
	}

	srcGit := git.NewGit(absSrc)
	if !srcGit.IsRepo() {
		return nil, fmt.Errorf("not a git repository: %s", absSrc)
	}
	empty, err := srcGit.IsEmpty()
	if err != nil {
		return nil, fmt.Errorf("checking repository: %w", err)
	}
	if empty {
		return nil, fmt.Errorf("repository %s is empty (no commits). Push at least one commit before adding it as a rig", absSrc)
	}

	gitURL := fileURL(absSrc)
	rigPath := filepath.Join(m.townRoot, name)
	if _, err := os.Stat(rigPath); err == nil {
		return nil, fmt.Errorf("directory already exists: %s", rigPath)
	}

	if err := os.MkdirAll(rigPath, 0755); err != nil {
		return nil, fmt.Errorf("creating rig directory: %w", err)
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(rigPath)
		}
	}()

	prefix := deriveBeadsPrefix(name)
	rigConfig := &RigConfig{
		Type:      "rig",
		Version:   CurrentRigConfigVersion,
		Name:      name,
		GitURL:    gitURL,
		LocalRepo: absSrc,
		CreatedAt: time.Now(),
		Beads:     &BeadsConfig{Prefix: prefix},
	}
	if branch := srcGit.DefaultBranch(); branch != "" {
		rigConfig.DefaultBranch = branch
	}
	if err := m.saveRigConfig(rigPath, rigConfig); err != nil {
		return nil, fmt.Errorf("saving rig config: %w", err)
	}

	if err := os.MkdirAll(filepath.Join(rigPath, "polecats"), 0755); err != nil {
		return nil, fmt.Errorf("creating polecats dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(rigPath, "crew"), 0755); err != nil {
		return nil, fmt.Errorf("creating crew dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(rigPath, "settings"), 0755); err != nil {
		return nil, fmt.Errorf("creating settings dir: %w", err)
	}

	bareRepoPath := filepath.Join(rigPath, ".repo.git")
	if err := m.git.CloneBareLocal(ctx, absSrc, bareRepoPath); err != nil {
		return nil, fmt.Errorf("sharing local git objects: %w", err)
	}

	if m.config.Rigs == nil {
		m.config.Rigs = make(map[string]config.RigEntry)
	}
	m.config.Rigs[name] = config.RigEntry{
		GitURL:    gitURL,
		LocalRepo: absSrc,
		AddedAt:   time.Now(),
		BeadsConfig: &config.BeadsConfig{
			Prefix: prefix,
		},
	}
	rigsPath := filepath.Join(m.townRoot, "mayor", "rigs.json")
	if err := config.SaveRigsConfig(rigsPath, m.config); err != nil {
		return nil, fmt.Errorf("registering rig in rigs.json: %w", err)
	}

	success = true
	return m.loadRig(name, m.config.Rigs[name])
}

// FindByLocalRepo returns the registered rig whose LocalRepo matches srcRepo.
func (m *Manager) FindByLocalRepo(srcRepo string) (string, bool) {
	want, err := canonicalRepoPath(srcRepo)
	if err != nil {
		return "", false
	}
	for name, entry := range m.config.Rigs {
		got, err := canonicalRepoPath(entry.LocalRepo)
		if err != nil {
			continue
		}
		if got == want {
			return name, true
		}
	}
	return "", false
}

func canonicalRepoPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	return abs, nil
}

func fileURL(absPath string) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(absPath)}
	return u.String()
}
