package cmd

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CONTEXT.paths is an optional operator-owned list of instruction files, one path
// per line, relative to the town root or absolute. No shell expansion occurs.
// Read fresh on every full prime and compact/resume; missing configured sources
// fail the prime instead of silently emitting an incomplete contract.
func renderOperatorContext(root string, workDir string) (string, error) {
	var out strings.Builder
	context, err := os.ReadFile(filepath.Join(root, "CONTEXT.md"))
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	out.Write(context)
	manifest, err := os.ReadFile(filepath.Join(root, "CONTEXT.paths"))
	if os.IsNotExist(err) {
		return out.String(), nil
	}
	if err != nil {
		return "", err
	}
	paths := strings.Split(string(manifest), "\n")
	// Configured paired repositories supply cross-repo context. The actual worker
	// checkout wins for its own instructions, even when it is an isolated worktree.
	for dir := workDir; dir != ""; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			paths = append(paths, filepath.Join(dir, "CLAUDE.md"), filepath.Join(dir, "AGENTS.md"))
			break
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	seen := map[string]bool{}
	for _, line := range paths {
		path := strings.TrimSpace(line)
		if path == "" || strings.HasPrefix(path, "#") {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			return "", fmt.Errorf("required context %q: %w", line, err)
		}
		if seen[path] {
			continue
		}
		seen[path] = true
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return "", fmt.Errorf("required context %q: %w", path, readErr)
		}
		fmt.Fprintf(&out, "\n\n## Required instruction source: %s\nSHA-256: %x\n\n%s\n", path, sha256.Sum256(content), content)
	}
	return out.String(), nil
}
