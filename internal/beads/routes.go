// Package beads provides routing helpers for prefix-based beads resolution.
package beads

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/config"
)

// Route represents a prefix-to-path routing rule.
// This mirrors the structure in bd's internal/routing package.
type Route struct {
	Prefix string `json:"prefix"` // Issue ID prefix (e.g., "gt-")
	Path   string `json:"path"`   // Relative path to .beads directory from town root
}

// RoutesFileName is the name of the routes configuration file.
const RoutesFileName = "routes.jsonl"

// LoadRoutes loads routes from routes.jsonl in the given beads directory.
// Returns an empty slice if the file doesn't exist.
func LoadRoutes(beadsDir string) ([]Route, error) {
	routesPath := filepath.Join(beadsDir, RoutesFileName)
	file, err := os.Open(routesPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No routes file is not an error
		}
		return nil, err
	}
	defer file.Close()

	var routes []Route
	scanner := bufio.NewScanner(file)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue // Skip empty lines and comments
		}

		var route Route
		if err := json.Unmarshal([]byte(line), &route); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: skipping malformed route at %s:%d: %v\n", routesPath, lineNum, err)
			continue
		}
		if route.Prefix != "" && route.Path != "" {
			routes = append(routes, route)
		}
	}

	return routes, scanner.Err()
}

// AppendRoute appends a route to routes.jsonl in the town's beads directory.
// If the prefix already exists, it updates the path.
func AppendRoute(townRoot string, route Route) error {
	beadsDir := filepath.Join(townRoot, ".beads")
	return AppendRouteToDir(beadsDir, route)
}

// AppendRouteToDir appends a route to routes.jsonl in the given beads directory.
// If the prefix already exists, it updates the path.
func AppendRouteToDir(beadsDir string, route Route) error {
	// Load existing routes
	routes, err := LoadRoutes(beadsDir)
	if err != nil {
		return fmt.Errorf("loading routes: %w", err)
	}

	// Check if prefix already exists
	found := false
	for i, r := range routes {
		if r.Prefix == route.Prefix {
			routes[i].Path = route.Path
			found = true
			break
		}
	}

	if !found {
		routes = append(routes, route)
	}

	// Write back
	return WriteRoutes(beadsDir, routes)
}

// RemoveRoute removes a route by prefix from routes.jsonl.
func RemoveRoute(townRoot string, prefix string) error {
	beadsDir := filepath.Join(townRoot, ".beads")

	// Load existing routes
	routes, err := LoadRoutes(beadsDir)
	if err != nil {
		return fmt.Errorf("loading routes: %w", err)
	}

	// Filter out the prefix
	var filtered []Route
	for _, r := range routes {
		if r.Prefix != prefix {
			filtered = append(filtered, r)
		}
	}

	// Write back
	return WriteRoutes(beadsDir, filtered)
}

// WriteRoutes writes routes to routes.jsonl, overwriting existing content.
func WriteRoutes(beadsDir string, routes []Route) error {
	// Ensure beads directory exists
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		return fmt.Errorf("creating beads directory: %w", err)
	}

	routesPath := filepath.Join(beadsDir, RoutesFileName)

	tmp, err := os.CreateTemp(beadsDir, ".routes-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp routes file: %w", err)
	}
	tmpPath := tmp.Name()

	for _, r := range routes {
		data, err := json.Marshal(r)
		if err != nil {
			tmp.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("marshaling route: %w", err)
		}
		if _, err := tmp.Write(data); err != nil {
			tmp.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("writing route: %w", err)
		}
		if _, err := tmp.WriteString("\n"); err != nil {
			tmp.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("writing newline: %w", err)
		}
	}

	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("syncing routes file: %w", err)
	}

	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("closing routes file: %w", err)
	}

	return os.Rename(tmpPath, routesPath)
}

// GetTownBeadsPath returns the path to town-level beads directory.
// Town beads store hq-* prefixed issues including Mayor, Deacon, and role beads.
// The townRoot should be the Gas Town root directory (e.g., ~/gt).
func GetTownBeadsPath(townRoot string) string {
	return filepath.Join(townRoot, ".beads")
}

// GetPrefixForRig returns the beads prefix for a given rig name.
// The prefix is returned without the trailing hyphen (e.g., "bd" not "bd-").
// If the rig is not found in routes, returns "gt" as the default.
// The townRoot should be the Gas Town root directory (e.g., ~/gt).
func GetPrefixForRig(townRoot, rigName string) string {
	beadsDir := filepath.Join(townRoot, ".beads")
	routes, err := LoadRoutes(beadsDir)
	if err != nil || routes == nil {
		return config.GetRigPrefix(townRoot, rigName)
	}

	// Look for a route where the path starts with the rig name
	// Routes paths are like "gastown/mayor/rig" or "beads/mayor/rig"
	for _, r := range routes {
		parts := strings.SplitN(r.Path, "/", 2)
		if len(parts) > 0 && parts[0] == rigName {
			// Return prefix without trailing hyphen
			return strings.TrimSuffix(r.Prefix, "-")
		}
	}

	return config.GetRigPrefix(townRoot, rigName)
}

// CheckPrefixAvailable verifies that a prefix is not already used by a different rig.
// The prefix should include the trailing hyphen (e.g., "gt-").
// newPath is the path of the rig being added (e.g., "gastown" or "gastown/mayor/rig").
// Returns nil if the prefix is available or already maps to the same rig.
func CheckPrefixAvailable(townRoot string, prefix string, newPath string) error {
	beadsDir := filepath.Join(townRoot, ".beads")
	routes, err := LoadRoutes(beadsDir)
	if err != nil {
		return fmt.Errorf("loading routes: %w", err)
	}

	// Extract the rig name (first path component) for comparison,
	// since the same rig can have different path variants (e.g., "gastown" vs "gastown/mayor/rig").
	newRig := strings.SplitN(newPath, "/", 2)[0]

	for _, r := range routes {
		if r.Prefix == prefix {
			existingRig := strings.SplitN(r.Path, "/", 2)[0]
			if existingRig != newRig {
				return fmt.Errorf("prefix %q is already used by %s (path: %s); use --prefix to specify a different prefix", prefix, existingRig, r.Path)
			}
		}
	}

	return nil
}

// FindConflictingPrefixes checks for duplicate prefixes in routes.
// Returns a map of prefix -> list of paths that use it.
func FindConflictingPrefixes(beadsDir string) (map[string][]string, error) {
	routes, err := LoadRoutes(beadsDir)
	if err != nil {
		return nil, err
	}

	// Group by prefix
	prefixPaths := make(map[string][]string)
	for _, r := range routes {
		prefixPaths[r.Prefix] = append(prefixPaths[r.Prefix], r.Path)
	}

	// Filter to only conflicts (more than one path per prefix)
	conflicts := make(map[string][]string)
	for prefix, paths := range prefixPaths {
		if len(paths) > 1 {
			conflicts[prefix] = paths
		}
	}

	return conflicts, nil
}

// ExtractPrefix extracts the prefix from a bead ID.
// For example, "ap-qtsup.16" returns "ap-", "hq-cv-abc" returns "hq-".
// Returns empty string if no valid prefix found (empty input, no hyphen,
// or hyphen at position 0 which would indicate an invalid prefix).
func ExtractPrefix(beadID string) string {
	if beadID == "" {
		return ""
	}

	idx := strings.Index(beadID, "-")
	if idx <= 0 {
		return ""
	}

	return beadID[:idx+1]
}

// GetRigPathForPrefix returns the rig path for a given bead ID prefix.
// The townRoot should be the Gas Town root directory (e.g., ~/gt).
// Returns the full absolute path to the rig directory, or empty string if not found.
// For town-level beads (path="."), returns townRoot.
func GetRigPathForPrefix(townRoot, prefix string) string {
	beadsDir := filepath.Join(townRoot, ".beads")
	routes, err := LoadRoutes(beadsDir)
	if err != nil || routes == nil {
		return ""
	}

	for _, r := range routes {
		if r.Prefix == prefix {
			if r.Path == "." {
				return townRoot // Town-level beads
			}
			return filepath.Join(townRoot, r.Path)
		}
	}

	return ""
}

// GetRigDirForName returns the rig directory path for a named rig.
// The rig directory is the parent of the rig's .beads database and is the
// directory that contains the rig's .beads database. Returns empty string if the rig is not
// found in routes or is town-level (path=".").
func GetRigDirForName(townRoot, rigName string) string {
	beadsDir := filepath.Join(townRoot, ".beads")
	routes, err := LoadRoutes(beadsDir)
	if err != nil || routes == nil {
		return ""
	}
	for _, r := range routes {
		if r.Path == "." {
			continue // town-level, not a specific rig dir
		}
		parts := strings.SplitN(r.Path, "/", 2)
		if len(parts) > 0 && parts[0] == rigName {
			return filepath.Join(townRoot, r.Path)
		}
	}
	return ""
}

// GetRigNameForPrefix returns the rig name that owns a given bead prefix.
// For example, "gt-" returns "gastown", "bd-" returns "beads".
// Returns empty string if the prefix is town-level (path=".") or not found in routes.
func GetRigNameForPrefix(townRoot, prefix string) string {
	beadsDir := filepath.Join(townRoot, ".beads")
	routes, err := LoadRoutes(beadsDir)
	if err != nil || routes == nil {
		return ""
	}

	for _, r := range routes {
		if r.Prefix == prefix {
			if r.Path == "." {
				return "" // Town-level bead, no specific rig
			}
			parts := strings.SplitN(r.Path, "/", 2)
			if len(parts) > 0 {
				return parts[0]
			}
		}
	}

	return ""
}

// ResolveBeadsDirForID resolves the correct .beads directory for a given bead ID
// based on prefix routing. currentBeadsDir is the caller's default beads directory
// (typically the town-level .beads). If the bead ID's prefix maps to a different
// rig via routes.jsonl, the resolved rig's beads directory is returned.
// Returns currentBeadsDir if no routing is needed or prefix can't be resolved.
func ResolveBeadsDirForID(currentBeadsDir, beadID string) string {
	prefix := ExtractPrefix(beadID)
	if prefix == "" {
		return currentBeadsDir
	}

	routesBeadsDir := currentBeadsDir
	routes, err := LoadRoutes(routesBeadsDir)
	if (err != nil || routes == nil) && currentBeadsDir != "" {
		if townRoot := FindTownRoot(filepath.Dir(currentBeadsDir)); townRoot != "" {
			townBeadsDir := filepath.Join(townRoot, ".beads")
			if townBeadsDir != currentBeadsDir {
				routesBeadsDir = townBeadsDir
				routes, err = LoadRoutes(routesBeadsDir)
			}
		}
	}
	if err != nil || routes == nil {
		return currentBeadsDir
	}

	for _, r := range routes {
		if r.Prefix == prefix {
			if r.Path == "." {
				return routesBeadsDir
			}
			// Rig-level bead — resolve to rig's beads directory.
			// Derive town root from the routes directory we actually used.
			townRoot := filepath.Dir(routesBeadsDir)
			rigDir := filepath.Join(townRoot, r.Path)
			return ResolveBeadsDir(rigDir)
		}
	}

	return currentBeadsDir
}

// ValidateRigPrefix checks that a newly created bead landed in the expected rig's
// database (gt-gpy). This is a POST-creation guard: the bead already exists, so
// callers MUST treat a non-nil return as a warning, not a hard failure.
//
// A mismatch means the bead's prefix doesn't match the expected rig prefix, which
// typically indicates the bd create routing resolved to the town-level database
// instead of the rig's database. Callers should log the warning and continue.
func ValidateRigPrefix(townRoot, rigName, beadID string) error {
	expectedPrefix := GetPrefixForRig(townRoot, rigName)           // e.g., "gt"
	actualPrefix := strings.TrimSuffix(ExtractPrefix(beadID), "-") // e.g., "gt"
	if actualPrefix == "" {
		return nil // Can't determine prefix — not an error
	}
	if actualPrefix != expectedPrefix {
		return fmt.Errorf("bead %s has prefix %q but rig %q expects prefix %q — bead may have landed in wrong database",
			beadID, actualPrefix, rigName, expectedPrefix)
	}
	return nil
}

// ResolveHookDir determines the directory for running bd update on a bead.
// Since bd update doesn't support routing or redirects, we must resolve the
// actual rig directory from the bead's prefix. hookWorkDir is only used as
// a fallback if prefix resolution fails.
func ResolveHookDir(townRoot, beadID, hookWorkDir string) string {
	// Always try prefix resolution first - bd update needs the actual rig dir
	prefix := ExtractPrefix(beadID)
	if rigPath := GetRigPathForPrefix(townRoot, prefix); rigPath != "" {
		return rigPath
	}
	// Fallback to hookWorkDir if provided
	if hookWorkDir != "" {
		return hookWorkDir
	}
	return townRoot
}
