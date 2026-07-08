package formula

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

// Formulas live in internal/formula/formulas/ (source of truth).
// They are embedded into the binary and provisioned to .beads/formulas/ at install time.

//go:embed formulas/*.formula.toml
var formulasFS embed.FS

// InstalledRecord tracks which formulas were installed and their checksums.
// Stored in .beads/formulas/.installed.json
type InstalledRecord struct {
	Formulas map[string]string `json:"formulas"` // filename -> sha256 at install time
}

// FormulaStatus represents the status of a single formula during health check.
type FormulaStatus struct {
	Name          string
	Status        string // "ok", "outdated", "modified", "missing", "new", "untracked"
	EmbeddedHash  string // hash computed from embedded content
	InstalledHash string // hash we installed (from .installed.json)
	CurrentHash   string // hash of current file on disk
}

// HealthReport contains the results of checking formula health.
type HealthReport struct {
	Formulas []FormulaStatus
	// Counts
	OK        int
	Outdated  int // embedded changed, user hasn't modified
	Modified  int // user modified the file (tracked in .installed.json)
	Missing   int // file was deleted
	New       int // new formula not yet installed
	Untracked int // file exists but not in .installed.json (safe to update)
	Error     int // file could not be read (e.g. permission denied)
}

// ErrFormulaNotFound marks a named formula lookup miss across disk and embedded tiers.
var ErrFormulaNotFound = errors.New("formula not found")

// ResolveFormulaContent resolves formula content using the three-tier precedence
// defined in docs/design/formula-resolution.md: rig > town > embedded.
//
// Tier 1 (rig): route-resolved rig .beads/formulas, then legacy direct rig path
// Tier 2 (town): townRoot/.beads/formulas/<name>.formula.toml
// Tier 3 (embedded): compiled into the binary
//
// Either townRoot or rigName may be empty; those tiers are skipped.
func ResolveFormulaContent(name, townRoot, rigName string) ([]byte, error) {
	filename, err := formulaFilename(name)
	if err != nil {
		return nil, err
	}

	paths := make([]string, 0, 3)
	seen := make(map[string]struct{})
	addPath := func(path string) {
		if path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}

	// Tier 1: rig-level (most specific)
	if townRoot != "" && rigName != "" {
		if err := validateFormulaPathComponent("rig", rigName); err != nil {
			return nil, err
		}
		if rigDir := beads.GetRigDirForName(townRoot, rigName); rigDir != "" {
			addPath(filepath.Join(beads.ResolveBeadsDir(rigDir), "formulas", filename))
		}
		addPath(filepath.Join(townRoot, rigName, ".beads", "formulas", filename))
	}

	// Tier 2: town-level
	if townRoot != "" {
		addPath(filepath.Join(townRoot, ".beads", "formulas", filename))
	}

	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err == nil {
			return content, nil
		}
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("reading formula %s: %w", path, err)
		}
	}

	// Tier 3: embedded (system fallback)
	return GetEmbeddedFormulaContent(name)
}

// GetEmbeddedFormulaContent returns the raw content of an embedded formula by name.
// The name can be with or without the .formula.toml suffix.
// Returns the content bytes, or an error if the formula is not found.
func GetEmbeddedFormulaContent(name string) ([]byte, error) {
	// Normalize: ensure the filename has the correct suffix
	filename, err := formulaFilename(name)
	if err != nil {
		return nil, err
	}
	content, err := formulasFS.ReadFile("formulas/" + filename)
	if err != nil {
		return nil, fmt.Errorf("%w: embedded formula %q: %w", ErrFormulaNotFound, name, err)
	}
	return content, nil
}

func formulaFilename(name string) (string, error) {
	if err := validateFormulaPathComponent("formula", name); err != nil {
		return "", err
	}
	if hasFormulaSuffix(name) {
		return name, nil
	}
	return name + ".formula.toml", nil
}

func validateFormulaPathComponent(kind, value string) error {
	if value == "" || filepath.IsAbs(value) || strings.Contains(value, "..") || strings.Contains(value, "/") || strings.Contains(value, "\\") {
		return fmt.Errorf("invalid %s name %q", kind, value)
	}
	return nil
}

// hasFormulaSuffix checks if a name already has a formula file suffix.
func hasFormulaSuffix(name string) bool {
	return len(name) > len(".formula.toml") &&
		name[len(name)-len(".formula.toml"):] == ".formula.toml"
}

// computeHash computes SHA256 hash of data.
func computeHash(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

// getEmbeddedFormulas returns a map of filename -> sha256 for all embedded formulas.
func getEmbeddedFormulas() (map[string]string, error) {
	entries, err := formulasFS.ReadDir("formulas")
	if err != nil {
		return nil, fmt.Errorf("reading formulas directory: %w", err)
	}

	result := make(map[string]string)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		content, err := formulasFS.ReadFile("formulas/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", entry.Name(), err)
		}
		result[entry.Name()] = computeHash(content)
	}
	return result, nil
}

// loadInstalledRecord loads the installed record from disk.
func loadInstalledRecord(formulasDir string) (*InstalledRecord, error) {
	path := filepath.Join(formulasDir, ".installed.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &InstalledRecord{Formulas: make(map[string]string)}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading installed record: %w", err)
	}
	var r InstalledRecord
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("parsing installed record: %w", err)
	}
	if r.Formulas == nil {
		r.Formulas = make(map[string]string)
	}
	return &r, nil
}

// saveInstalledRecord saves the installed record to disk.
func saveInstalledRecord(formulasDir string, record *InstalledRecord) error {
	path := filepath.Join(formulasDir, ".installed.json")
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding installed record: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

// computeFileHash computes SHA256 hash of a file.
func computeFileHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return computeHash(data), nil
}

// ProvisionFormulas creates the .beads/formulas/ directory with embedded formulas.
// This is called during gt install for fresh installations.
// If a formula already exists, it is skipped (no overwrite).
// Returns the number of formulas provisioned.
func ProvisionFormulas(beadsPath string) (int, error) {
	embedded, err := getEmbeddedFormulas()
	if err != nil {
		return 0, err
	}

	entries, err := formulasFS.ReadDir("formulas")
	if err != nil {
		return 0, fmt.Errorf("reading formulas directory: %w", err)
	}

	// Create .beads/formulas/ directory
	formulasDir := filepath.Join(beadsPath, ".beads", "formulas")
	if err := os.MkdirAll(formulasDir, 0755); err != nil {
		return 0, fmt.Errorf("creating formulas directory: %w", err)
	}

	// Load existing installed record (or create new)
	installed, err := loadInstalledRecord(formulasDir)
	if err != nil {
		return 0, err
	}

	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		destPath := filepath.Join(formulasDir, entry.Name())

		// Skip if formula already exists (don't overwrite user customizations)
		if _, err := os.Stat(destPath); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return count, fmt.Errorf("checking %s: %w", entry.Name(), err)
		}

		content, err := formulasFS.ReadFile("formulas/" + entry.Name())
		if err != nil {
			return count, fmt.Errorf("reading %s: %w", entry.Name(), err)
		}

		if err := os.WriteFile(destPath, content, 0644); err != nil {
			return count, fmt.Errorf("writing %s: %w", entry.Name(), err)
		}

		// Record the hash we installed
		if hash, ok := embedded[entry.Name()]; ok {
			installed.Formulas[entry.Name()] = hash
		}
		count++
	}

	// Save updated installed record
	if err := saveInstalledRecord(formulasDir, installed); err != nil {
		return count, fmt.Errorf("saving installed record: %w", err)
	}

	return count, nil
}

// CheckFormulaHealth checks the status of all formulas.
// Returns a report of which formulas are ok, outdated, modified, or missing.
func CheckFormulaHealth(beadsPath string) (*HealthReport, error) {
	embedded, err := getEmbeddedFormulas()
	if err != nil {
		return nil, err
	}

	formulasDir := filepath.Join(beadsPath, ".beads", "formulas")
	installed, err := loadInstalledRecord(formulasDir)
	if err != nil {
		return nil, err
	}

	report := &HealthReport{}

	for filename, embeddedHash := range embedded {
		status := FormulaStatus{
			Name:         filename,
			EmbeddedHash: embeddedHash,
		}

		installedHash, wasInstalled := installed.Formulas[filename]
		status.InstalledHash = installedHash

		destPath := filepath.Join(formulasDir, filename)
		currentHash, err := computeFileHash(destPath)

		if os.IsNotExist(err) {
			// File doesn't exist
			if wasInstalled {
				// We installed it before, user deleted it
				status.Status = "missing"
				report.Missing++
			} else {
				// New formula, never installed
				status.Status = "new"
				report.New++
			}
		} else if err != nil {
			// Some other error reading file (e.g. permission denied)
			status.Status = "error"
			report.Error++
		} else {
			status.CurrentHash = currentHash

			if currentHash == embeddedHash {
				// File matches embedded - all good
				status.Status = "ok"
				report.OK++
			} else if wasInstalled && currentHash == installedHash {
				// File matches what we installed, but embedded has changed
				// User hasn't modified, safe to update
				status.Status = "outdated"
				report.Outdated++
			} else if wasInstalled {
				// File was tracked and user modified it - don't overwrite
				status.Status = "modified"
				report.Modified++
			} else {
				// File exists but not tracked (e.g., from older gt version)
				// Safe to update since we have no record of user modification
				status.Status = "untracked"
				report.Untracked++
			}
		}

		report.Formulas = append(report.Formulas, status)
	}

	return report, nil
}

// UpdateFormulas updates formulas that are safe to update (outdated, missing, or untracked).
// Skips user-modified formulas (tracked files that user changed).
// Returns counts of updated, skipped (modified), and reinstalled (missing).
func UpdateFormulas(beadsPath string) (updated, skipped, reinstalled int, err error) {
	embedded, err := getEmbeddedFormulas()
	if err != nil {
		return 0, 0, 0, err
	}

	formulasDir := filepath.Join(beadsPath, ".beads", "formulas")
	if err := os.MkdirAll(formulasDir, 0755); err != nil {
		return 0, 0, 0, fmt.Errorf("creating formulas directory: %w", err)
	}

	installed, err := loadInstalledRecord(formulasDir)
	if err != nil {
		return 0, 0, 0, err
	}

	for filename, embeddedHash := range embedded {
		installedHash, wasInstalled := installed.Formulas[filename]
		destPath := filepath.Join(formulasDir, filename)
		currentHash, fileErr := computeFileHash(destPath)

		shouldInstall := false
		isMissing := false
		isModified := false

		if os.IsNotExist(fileErr) {
			// File doesn't exist - install it
			shouldInstall = true
			if wasInstalled {
				isMissing = true
			}
		} else if fileErr != nil {
			// Error reading file, skip
			continue
		} else if currentHash == embeddedHash {
			// Already up to date
			continue
		} else if wasInstalled && currentHash == installedHash {
			// User hasn't modified, safe to update
			shouldInstall = true
		} else if wasInstalled {
			// Tracked file was modified by user - skip
			isModified = true
		} else {
			// Untracked file (e.g., from older gt version) - safe to update
			shouldInstall = true
		}

		if isModified {
			skipped++
			continue
		}

		if shouldInstall {
			content, err := formulasFS.ReadFile("formulas/" + filename)
			if err != nil {
				return updated, skipped, reinstalled, fmt.Errorf("reading %s: %w", filename, err)
			}

			if err := os.WriteFile(destPath, content, 0644); err != nil {
				return updated, skipped, reinstalled, fmt.Errorf("writing %s: %w", filename, err)
			}

			// Update installed record
			installed.Formulas[filename] = embeddedHash

			if isMissing {
				reinstalled++
			} else {
				updated++
			}
		}
	}

	// Save updated installed record
	if err := saveInstalledRecord(formulasDir, installed); err != nil {
		return updated, skipped, reinstalled, fmt.Errorf("saving installed record: %w", err)
	}

	return updated, skipped, reinstalled, nil
}
