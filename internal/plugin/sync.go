package plugin

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/fsutil"
)

// SyncResult records the outcome of a plugin sync operation.
type SyncResult struct {
	Copied  []string // plugin names that were copied/updated
	Removed []string // plugin names that were removed (clean mode)
	Skipped []string // plugin names that were already up-to-date
	Errors  []string // errors encountered
}

// SyncPlugins copies plugin directories from source to target.
// If clean is true, removes plugins from target that don't exist in source.
func SyncPlugins(sourceDir, targetDir string, clean bool) (*SyncResult, error) {
	result := &SyncResult{}

	srcInfo, err := os.Stat(sourceDir)
	if err != nil {
		return nil, fmt.Errorf("source directory %s: %w", sourceDir, err)
	}
	if !srcInfo.IsDir() {
		return nil, fmt.Errorf("source is not a directory: %s", sourceDir)
	}

	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return nil, fmt.Errorf("creating target directory: %w", err)
	}

	srcEntries, err := os.ReadDir(sourceDir)
	if err != nil {
		return nil, fmt.Errorf("reading source directory: %w", err)
	}

	srcPlugins := make(map[string]bool)
	for _, entry := range srcEntries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		pluginMD := filepath.Join(sourceDir, entry.Name(), "plugin.md")
		if _, err := os.Stat(pluginMD); err != nil {
			continue // Not a plugin directory
		}
		srcPlugins[entry.Name()] = true

		srcPluginDir := filepath.Join(sourceDir, entry.Name())
		dstPluginDir := filepath.Join(targetDir, entry.Name())

		if dirsMatch(srcPluginDir, dstPluginDir) {
			result.Skipped = append(result.Skipped, entry.Name())
			continue
		}

		if err := copyDir(srcPluginDir, dstPluginDir); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", entry.Name(), err))
			continue
		}
		result.Copied = append(result.Copied, entry.Name())
	}

	if clean {
		dstEntries, err := os.ReadDir(targetDir)
		if err == nil {
			for _, entry := range dstEntries {
				if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
					continue
				}
				if !srcPlugins[entry.Name()] {
					dstPath := filepath.Join(targetDir, entry.Name())
					if err := os.RemoveAll(dstPath); err != nil {
						result.Errors = append(result.Errors, fmt.Sprintf("removing %s: %v", entry.Name(), err))
					} else {
						result.Removed = append(result.Removed, entry.Name())
					}
				}
			}
		}
	}

	return result, nil
}

// dirsMatch checks if two plugin directories have identical contents.
func dirsMatch(src, dst string) bool {
	srcHash := DirHash(src)
	dstHash := DirHash(dst)
	return srcHash != "" && srcHash == dstHash
}

// DirHash computes a content hash of all files in a directory.
func DirHash(dir string) string {
	h := sha256.New()
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		h.Write([]byte(rel))
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path) //nolint:gosec // G304: walking trusted plugin directory
		if err != nil {
			return err
		}
		h.Write(data)
		return nil
	})
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// copyDir recursively copies a directory, replacing the destination atomically.
// It copies to a temp directory in the same parent, then swaps via rename.
func copyDir(src, dst string) error {
	tmpDir, err := os.MkdirTemp(filepath.Dir(dst), ".plugin-sync-*")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	// Clean up temp dir on failure; on success it's been renamed away.
	defer os.RemoveAll(tmpDir)

	if err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		tmpPath := filepath.Join(tmpDir, rel)
		if d.IsDir() {
			return os.MkdirAll(tmpPath, 0755)
		}
		// The staging directory shares a parent with the destination, so this
		// is a same-filesystem copy and fsutil clones rather than duplicates
		// where the filesystem supports it.
		return fsutil.CopyFile(path, tmpPath)
	}); err != nil {
		return err
	}

	// Atomic swap: remove old dst, rename temp into place.
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("removing old destination: %w", err)
	}
	return os.Rename(tmpDir, dst)
}

// FindGastownSource locates the gastown source repo's plugins directory.
// Search order:
//  1. Walk up from CWD for a gastown go.mod with plugins/
//  2. <townRoot>/gastown/crew/den/plugins/
//  3. <townRoot>/gastown/plugins/
func FindGastownSource(townRoot string) (string, error) {
	if cwd, err := os.Getwd(); err == nil {
		if src := findSourceFromDir(cwd); src != "" {
			return src, nil
		}
	}

	candidates := []string{
		filepath.Join(townRoot, "gastown", "crew", "den", "plugins"),
		filepath.Join(townRoot, "gastown", "plugins"),
	}
	for _, candidate := range candidates {
		if hasPlugins(candidate) {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("could not locate gastown plugin source; use --source to specify")
}

func findSourceFromDir(dir string) string {
	current := dir
	for {
		pluginsDir := filepath.Join(current, "plugins")
		goMod := filepath.Join(current, "go.mod")
		if hasPlugins(pluginsDir) {
			if isGastownModule(goMod) {
				return pluginsDir
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return ""
}

// isGastownModule checks if a go.mod file declares a gastown module path.
// Matches "module .../gastown" on the module directive line to avoid
// false-positives from comments or dependency names.
func isGastownModule(goModPath string) bool {
	f, err := os.Open(goModPath) //nolint:gosec // G304: path from traversal
	if err != nil {
		return false
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "module ") {
			return strings.HasSuffix(line, "/gastown") || line == "module gastown"
		}
	}
	return false
}

func hasPlugins(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			if _, err := os.Stat(filepath.Join(dir, entry.Name(), "plugin.md")); err == nil {
				return true
			}
		}
	}
	return false
}

// DriftReport describes differences between source and runtime plugins.
type DriftReport struct {
	Source  string       `json:"source"`
	Target string       `json:"target"`
	Drifted []DriftEntry `json:"drifted,omitempty"`
	Missing []string     `json:"missing,omitempty"` // in source but not target
	Extra   []string     `json:"extra,omitempty"`   // in target but not source
}

// DriftEntry describes a single plugin that differs between source and runtime.
type DriftEntry struct {
	Name       string `json:"name"`
	SourceHash string `json:"source_hash"`
	TargetHash string `json:"target_hash"`
}

// DetectDrift compares plugin directories between source and target.
func DetectDrift(sourceDir, targetDir string) (*DriftReport, error) {
	report := &DriftReport{
		Source: sourceDir,
		Target: targetDir,
	}

	srcEntries, err := os.ReadDir(sourceDir)
	if err != nil {
		return nil, fmt.Errorf("reading source: %w", err)
	}

	tgtPlugins := make(map[string]bool)
	if tgtEntries, err := os.ReadDir(targetDir); err == nil {
		for _, entry := range tgtEntries {
			if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
				tgtPlugins[entry.Name()] = true
			}
		}
	}

	for _, entry := range srcEntries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if _, err := os.Stat(filepath.Join(sourceDir, entry.Name(), "plugin.md")); err != nil {
			continue
		}

		srcDir := filepath.Join(sourceDir, entry.Name())
		dstDir := filepath.Join(targetDir, entry.Name())

		if !tgtPlugins[entry.Name()] {
			report.Missing = append(report.Missing, entry.Name())
			continue
		}
		delete(tgtPlugins, entry.Name())

		srcHash := DirHash(srcDir)
		dstHash := DirHash(dstDir)
		if srcHash != dstHash {
			report.Drifted = append(report.Drifted, DriftEntry{
				Name:       entry.Name(),
				SourceHash: srcHash,
				TargetHash: dstHash,
			})
		}
	}

	for name := range tgtPlugins {
		if _, err := os.Stat(filepath.Join(targetDir, name, "plugin.md")); err == nil {
			report.Extra = append(report.Extra, name)
		}
	}

	return report, nil
}

// HasDrift returns true if the report indicates any differences.
func (r *DriftReport) HasDrift() bool {
	return len(r.Drifted) > 0 || len(r.Missing) > 0
}
