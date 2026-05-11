package beads

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// IsCanonicalRuntimeDir reports whether beadsDir is the fleet-wide canonical
// Beads runtime. Gas Town may read canonical tasks through explicit bridge
// code, but local town/rig setup must never mutate its issue_prefix.
func IsCanonicalRuntimeDir(beadsDir string) bool {
	if strings.TrimSpace(beadsDir) == "" {
		return false
	}

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}

	got := cleanResolvedPath(beadsDir)
	want := cleanResolvedPath(filepath.Join(home, ".beads-runtime", ".beads"))
	return got == want
}

// GuardNonCanonicalRuntime rejects local Gas Town config writes pointed at the
// canonical Beads runtime.
func GuardNonCanonicalRuntime(beadsDir, operation string) error {
	if !IsCanonicalRuntimeDir(beadsDir) {
		return nil
	}
	return fmt.Errorf("refusing %s on canonical Beads runtime %s; use a town/rig .beads directory for Gas Town prefixes", operation, beadsDir)
}

// EnvWithBeadsDir returns env with inherited Beads routing removed and replaced
// by the explicit target. This avoids getenv-first duplicate BEADS_DIR bugs.
func EnvWithBeadsDir(environ []string, beadsDir string) []string {
	filtered := make([]string, 0, len(environ)+2)
	for _, entry := range environ {
		if strings.HasPrefix(entry, "BEADS_DIR=") ||
			strings.HasPrefix(entry, "BEADS_DB=") ||
			strings.HasPrefix(entry, "BEADS_DOLT_SERVER_DATABASE=") {
			continue
		}
		filtered = append(filtered, entry)
	}
	filtered = append(filtered, "BEADS_DIR="+beadsDir)
	if dbEnv := DatabaseEnv(beadsDir); dbEnv != "" {
		filtered = append(filtered, dbEnv)
	}
	return filtered
}

func cleanResolvedPath(path string) string {
	clean := filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		return filepath.Clean(resolved)
	}
	return clean
}
