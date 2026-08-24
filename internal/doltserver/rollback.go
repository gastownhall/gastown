package doltserver

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/steveyegge/gastown/internal/fsutil"
)

// Backup represents a discovered migration backup directory.
type Backup struct {
	// Path is the absolute path to the backup directory.
	Path string

	// Timestamp is the YYYYMMDD-HHMMSS suffix from the directory name.
	Timestamp string

	// Metadata is the parsed backup metadata, if available.
	Metadata map[string]interface{}
}

// FindBackups finds all migration backup directories in the town root.
// Returns them sorted newest-first by timestamp suffix.
func FindBackups(townRoot string) ([]Backup, error) {
	entries, err := os.ReadDir(townRoot)
	if err != nil {
		return nil, fmt.Errorf("reading town root: %w", err)
	}

	const prefix = "migration-backup-"
	var backups []Backup

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}

		timestamp := strings.TrimPrefix(name, prefix)
		if timestamp == "" {
			continue
		}

		b := Backup{
			Path:      filepath.Join(townRoot, name),
			Timestamp: timestamp,
		}

		// Load metadata if present
		metaPath := filepath.Join(b.Path, "metadata.json")
		if data, err := os.ReadFile(metaPath); err == nil {
			var meta map[string]interface{}
			if err := json.Unmarshal(data, &meta); err == nil {
				b.Metadata = meta
			}
		}

		backups = append(backups, b)
	}

	// Sort newest-first by timestamp string (lexicographic works for YYYYMMDD-HHMMSS)
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].Timestamp > backups[j].Timestamp
	})

	return backups, nil
}

// RollbackResult tracks what was restored during rollback.
type RollbackResult struct {
	BackupPath    string
	RestoredTown  bool
	RestoredRigs  []string
	SkippedRigs   []string
	MetadataReset []string
}

// RestoreFromBackup restores .beads directories from a migration backup.
// The backup directory is expected to have the structure created by the
// migration formula's backup step:
//
//	migration-backup-TIMESTAMP/
//	├── town-beads/          → restored to <townRoot>/.beads
//	└── <rigname>-beads/     → restored to <townRoot>/<rigname>/.beads
//
// It also handles the test-backup structure:
//
//	.migration-test-backup/
//	├── town-beads/          → restored to <townRoot>/.beads
//	└── rigs/<rigname>/.beads → restored to <townRoot>/<rigname>/.beads
//
// For each restored .beads directory, the existing one is removed first.
// This resets metadata.json to the pre-migration state since the backup
// contains the original metadata.json files.
func RestoreFromBackup(townRoot, backupPath string) (*RollbackResult, error) {
	// Verify backup directory exists
	info, err := os.Stat(backupPath)
	if err != nil {
		return nil, fmt.Errorf("backup not found: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("backup path is not a directory: %s", backupPath)
	}

	result := &RollbackResult{
		BackupPath: backupPath,
	}

	// Restore town-level beads
	townBackup := filepath.Join(backupPath, "town-beads")
	if _, err := os.Stat(townBackup); err == nil {
		townBeads := filepath.Join(townRoot, ".beads")
		if err := replaceDir(townBeads, townBackup); err != nil {
			return result, fmt.Errorf("restoring town beads: %w", err)
		}
		result.RestoredTown = true
		result.MetadataReset = append(result.MetadataReset, "town (.beads)")
	}

	// Restore rig-level beads: try formula-style first (<rigname>-beads/)
	entries, err := os.ReadDir(backupPath)
	if err != nil {
		return result, fmt.Errorf("reading backup directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()

		// Skip known non-rig directories
		if name == "town-beads" || name == "rigs" {
			continue
		}

		// Formula-style backup: <rigname>-beads/
		if strings.HasSuffix(name, "-beads") {
			rigName := strings.TrimSuffix(name, "-beads")
			rigBeads := filepath.Join(townRoot, rigName, ".beads")
			rigBackup := filepath.Join(backupPath, name)
			if err := replaceDir(rigBeads, rigBackup); err != nil {
				result.SkippedRigs = append(result.SkippedRigs, rigName)
				continue
			}
			result.RestoredRigs = append(result.RestoredRigs, rigName)
			result.MetadataReset = append(result.MetadataReset, rigName)
		}
	}

	// Also check test-backup style: rigs/<rigname>/.beads
	rigsDir := filepath.Join(backupPath, "rigs")
	if rigEntries, err := os.ReadDir(rigsDir); err == nil {
		for _, entry := range rigEntries {
			if !entry.IsDir() {
				continue
			}
			rigName := entry.Name()
			rigBackupBeads := filepath.Join(rigsDir, rigName, ".beads")
			if _, err := os.Stat(rigBackupBeads); err != nil {
				continue
			}
			rigBeads := filepath.Join(townRoot, rigName, ".beads")
			if err := replaceDir(rigBeads, rigBackupBeads); err != nil {
				result.SkippedRigs = append(result.SkippedRigs, rigName)
				continue
			}
			result.RestoredRigs = append(result.RestoredRigs, rigName)
			result.MetadataReset = append(result.MetadataReset, rigName)
		}
	}

	return result, nil
}

// replaceDir removes dst (if it exists) and copies src to dst.
func replaceDir(dst, src string) error {
	// Verify source exists
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("source not found: %w", err)
	}

	// Remove existing destination
	if _, err := os.Stat(dst); err == nil {
		if err := os.RemoveAll(dst); err != nil {
			return fmt.Errorf("removing existing %s: %w", dst, err)
		}
	}

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("creating parent directory: %w", err)
	}

	// Restores land next to the live database, so this is normally a
	// same-filesystem copy and fsutil clones the extents instead of
	// duplicating them. A Dolt database directory is large enough for that to
	// matter; the previous implementation also read every file fully into
	// memory before writing it back out.
	if err := fsutil.CopyDir(src, dst); err != nil {
		return fmt.Errorf("copying %s to %s: %w", src, dst, err)
	}

	return nil
}
