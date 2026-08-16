package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/config"
)

// determineRigBeadsPath returns the correct route path for a rig based on its actual layout.
// Uses ResolveBeadsDir to follow any redirects (e.g., rig/.beads/redirect -> mayor/rig/.beads).
// Falls back to the default mayor layout path if the resolved path is invalid or escapes the town root.
func determineRigBeadsPath(townRoot, rigName string) string {
	defaultPath := rigName + "/mayor/rig"
	rigPath := filepath.Join(townRoot, rigName)
	resolved := beads.ResolveBeadsDir(rigPath)

	rel, err := filepath.Rel(townRoot, resolved)
	if err != nil {
		return defaultPath
	}

	// Normalize to forward slashes for consistent string operations on all platforms
	rel = filepath.ToSlash(rel)

	// Validate the resolved path stays within the town root
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return defaultPath
	}

	return strings.TrimSuffix(rel, "/.beads")
}

// RoutesCheck verifies that beads routing is properly configured.
// It checks that routes.jsonl exists, all rigs have routing entries,
// and all routes point to valid locations.
type RoutesCheck struct {
	FixableCheck
}

// NewRoutesCheck creates a new routes configuration check.
func NewRoutesCheck() *RoutesCheck {
	return &RoutesCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "routes-config",
				CheckDescription: "Check beads routing configuration",
				CheckCategory:    CategoryConfig,
			},
		},
	}
}

// Run checks the beads routing configuration.
func (c *RoutesCheck) Run(ctx *CheckContext) *CheckResult {
	beadsDir := filepath.Join(ctx.TownRoot, ".beads")
	routesPath := filepath.Join(beadsDir, beads.RoutesFileName)

	// Check if .beads directory exists
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "No .beads directory at town root",
			FixHint: "Run 'bd init' to initialize beads",
		}
	}

	// Check if routes.jsonl exists
	if _, err := os.Stat(routesPath); os.IsNotExist(err) {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "No routes.jsonl file (prefix routing not configured)",
			FixHint: "Run 'gt doctor --fix' to create routes.jsonl",
		}
	}

	// Load existing routes
	routes, err := beads.LoadRoutes(beadsDir)
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("Failed to load routes.jsonl: %v", err),
		}
	}

	// Build maps of existing routes
	routeByPrefix := make(map[string]string) // prefix -> path
	routeByPath := make(map[string]string)   // path -> prefix
	for _, r := range routes {
		routeByPrefix[r.Prefix] = r.Path
		routeByPath[r.Path] = r.Prefix
	}

	var details []string
	var missingTownRoute bool
	var missingConvoyRoute bool

	// Check town root route exists (hq- -> .)
	if _, hasTownRoute := routeByPrefix["hq-"]; !hasTownRoute {
		missingTownRoute = true
		details = append(details, "Town root route (hq- -> .) is missing")
	}

	// Check convoy route exists (hq-cv- -> .)
	if _, hasConvoyRoute := routeByPrefix["hq-cv-"]; !hasConvoyRoute {
		missingConvoyRoute = true
		details = append(details, "Convoy route (hq-cv- -> .) is missing")
	}

	// Load rigs registry
	rigsPath := filepath.Join(ctx.TownRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		// No rigs config - check for missing town/convoy routes and validate existing routes
		if missingTownRoute || missingConvoyRoute {
			return &CheckResult{
				Name:    c.Name(),
				Status:  StatusWarning,
				Message: "Required town routes are missing",
				Details: details,
				FixHint: "Run 'gt doctor --fix' to add missing routes",
			}
		}
		return c.checkRoutesValid(ctx, routes)
	}

	var missingRigs []string
	var invalidRoutes []string
	var suboptimalRoutes []string

	// Check each rig has a route (by path, not just prefix from rigs.json)
	for rigName, rigEntry := range rigsConfig.Rigs {
		// Determine the correct path based on actual rig layout
		expectedPath := determineRigBeadsPath(ctx.TownRoot, rigName)

		prefix := ""
		if rigEntry.BeadsConfig != nil && rigEntry.BeadsConfig.Prefix != "" {
			prefix = rigEntry.BeadsConfig.Prefix + "-"
		}

		// Check if there's already a route for this rig (by path)
		if _, hasRoute := routeByPath[expectedPath]; hasRoute {
			// Rig already has a route with the correct path
			continue
		}

		// No route with expected path — check if there's one by prefix
		if prefix != "" {
			if existingPath, found := routeByPrefix[prefix]; found {
				// Route exists but points to a different path than expected.
				// Only flag as suboptimal if the existing path relies on a
				// .beads/redirect file — this is the specific legacy pattern
				// broken by beads#1749. Intentional non-canonical routes
				// (without redirect) are left alone.
				if existingPath != expectedPath && isRedirectDependent(ctx.TownRoot, existingPath) {
					suboptimalRoutes = append(suboptimalRoutes, prefix)
					details = append(details, fmt.Sprintf("Route %s -> %s should be %s -> %s (avoids redirect resolution bug)", prefix, existingPath, prefix, expectedPath))
				}
			} else {
				missingRigs = append(missingRigs, rigName)
				details = append(details, fmt.Sprintf("Rig '%s' (prefix: %s) has no routing entry", rigName, prefix))
			}
		}
	}

	// Build set of suboptimal prefixes to avoid double-counting in validity check
	suboptimalSet := make(map[string]bool, len(suboptimalRoutes))
	for _, p := range suboptimalRoutes {
		suboptimalSet[p] = true
	}

	// Check each route points to a valid location
	for _, r := range routes {
		rigPath := filepath.Join(ctx.TownRoot, r.Path)
		beadsPath := filepath.Join(rigPath, ".beads")

		// Special case: "." path is town root, already checked
		if r.Path == "." {
			continue
		}

		// Skip routes already flagged as suboptimal to avoid double-counting
		if suboptimalSet[r.Prefix] {
			continue
		}

		// Check if the path exists
		if _, err := os.Stat(rigPath); os.IsNotExist(err) {
			invalidRoutes = append(invalidRoutes, r.Prefix)
			details = append(details, fmt.Sprintf("Route %s -> %s: path does not exist", r.Prefix, r.Path))
			continue
		}

		// Check if .beads directory exists (or redirect file)
		redirectPath := filepath.Join(beadsPath, "redirect")
		_, beadsErr := os.Stat(beadsPath)
		_, redirectErr := os.Stat(redirectPath)

		if os.IsNotExist(beadsErr) && os.IsNotExist(redirectErr) {
			invalidRoutes = append(invalidRoutes, r.Prefix)
			details = append(details, fmt.Sprintf("Route %s -> %s: no .beads directory", r.Prefix, r.Path))
		}
	}

	// Determine result
	if missingTownRoute || missingConvoyRoute || len(missingRigs) > 0 || len(invalidRoutes) > 0 || len(suboptimalRoutes) > 0 {
		status := StatusWarning
		var messageParts []string

		if missingTownRoute {
			messageParts = append(messageParts, "town root route missing")
		}
		if missingConvoyRoute {
			messageParts = append(messageParts, "convoy route missing")
		}
		if len(missingRigs) > 0 {
			messageParts = append(messageParts, fmt.Sprintf("%d rig(s) missing routes", len(missingRigs)))
		}
		if len(invalidRoutes) > 0 {
			messageParts = append(messageParts, fmt.Sprintf("%d invalid route(s)", len(invalidRoutes)))
		}
		if len(suboptimalRoutes) > 0 {
			messageParts = append(messageParts, fmt.Sprintf("%d route(s) using redirect instead of canonical path", len(suboptimalRoutes)))
		}

		return &CheckResult{
			Name:    c.Name(),
			Status:  status,
			Message: strings.Join(messageParts, ", "),
			Details: details,
			FixHint: "Run 'gt doctor --fix' to fix routing issues",
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: fmt.Sprintf("Routes configured correctly (%d routes)", len(routes)),
	}
}

// checkRoutesValid checks that existing routes point to valid locations.
func (c *RoutesCheck) checkRoutesValid(ctx *CheckContext, routes []beads.Route) *CheckResult {
	var details []string
	var invalidCount int

	for _, r := range routes {
		if r.Path == "." {
			continue // Town root is valid
		}

		rigPath := filepath.Join(ctx.TownRoot, r.Path)
		if _, err := os.Stat(rigPath); os.IsNotExist(err) {
			invalidCount++
			details = append(details, fmt.Sprintf("Route %s -> %s: path does not exist", r.Prefix, r.Path))
		}
	}

	if invalidCount > 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("%d invalid route(s) in routes.jsonl", invalidCount),
			Details: details,
			FixHint: "Remove invalid routes or recreate the missing rigs",
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: fmt.Sprintf("Routes configured correctly (%d routes)", len(routes)),
	}
}

// hasRealBeadsDir checks whether a route target path has a real .beads directory
// (not just a redirect). This is used by Fix to ensure we only rewrite routes
// to paths that have an actual beads database, since the whole point of the
// rewrite is to bypass redirect resolution (beads#1749).
func hasRealBeadsDir(targetPath string) bool {
	beadsPath := filepath.Join(targetPath, ".beads")
	_, err := os.Stat(beadsPath)
	return err == nil
}

// isRedirectDependent checks whether a route path relies on a .beads/redirect
// file for resolution. This identifies the specific legacy pattern where the
// route points to a rig root that has .beads/redirect instead of a real
// .beads database — exactly the pattern broken by beads#1749.
func isRedirectDependent(townRoot, routePath string) bool {
	fullPath := filepath.Join(townRoot, routePath)
	redirectPath := filepath.Join(fullPath, ".beads", "redirect")
	_, err := os.Stat(redirectPath)
	return err == nil
}

// Fix attempts to add missing routing entries and rewrite suboptimal ones.
func (c *RoutesCheck) Fix(ctx *CheckContext) error {
	beadsDir := filepath.Join(ctx.TownRoot, ".beads")

	// Ensure .beads directory exists
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return fmt.Errorf(".beads directory does not exist; run 'bd init' first")
	}

	// Load existing routes
	routes, err := beads.LoadRoutes(beadsDir)
	if err != nil {
		routes = []beads.Route{} // Start fresh if can't load
	}

	// Build map of existing prefixes to route index for fast lookup.
	// NOTE: routeMap indices are only valid as long as routes is append-only
	// (no removals or reordering within this method).
	routeMap := make(map[string]int) // prefix -> index in routes slice
	for i, r := range routes {
		routeMap[r.Prefix] = i
	}

	// Ensure town root route exists (hq- -> .)
	// This is normally created by gt install but may be missing if routes.jsonl was corrupted
	modified := false
	if _, exists := routeMap["hq-"]; !exists {
		routeMap["hq-"] = len(routes)
		routes = append(routes, beads.Route{Prefix: "hq-", Path: "."})
		modified = true
	}

	// Ensure convoy route exists (hq-cv- -> .)
	// Convoys use hq-cv-* IDs for visual distinction from other town beads
	if _, exists := routeMap["hq-cv-"]; !exists {
		routeMap["hq-cv-"] = len(routes)
		routes = append(routes, beads.Route{Prefix: "hq-cv-", Path: "."})
		modified = true
	}

	// Load rigs registry
	rigsPath := filepath.Join(ctx.TownRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		// No rigs config - just write town root route if we added it
		if modified {
			return beads.WriteRoutes(beadsDir, routes)
		}
		return nil
	}

	// Collect prefixes from rigs to detect duplicates (finding #5).
	// If rigs.json has duplicate prefixes, skip auto-fix for those prefixes
	// to avoid non-deterministic behavior from map iteration order.
	prefixCount := make(map[string]int)
	for _, rigEntry := range rigsConfig.Rigs {
		if rigEntry.BeadsConfig != nil && rigEntry.BeadsConfig.Prefix != "" {
			prefixCount[rigEntry.BeadsConfig.Prefix+"-"]++
		}
	}

	// Add missing routes and rewrite redirect-dependent ones for each rig.
	// Only rewrites routes that rely on .beads/redirect at the rig root —
	// the specific legacy pattern broken by beads#1749. Routes are rewritten
	// to the canonical path (e.g., "crom/mayor/rig") which has a real .beads
	// directory and needs no redirect resolution.
	for rigName, rigEntry := range rigsConfig.Rigs {
		prefix := ""
		if rigEntry.BeadsConfig != nil && rigEntry.BeadsConfig.Prefix != "" {
			prefix = rigEntry.BeadsConfig.Prefix + "-"
		}

		if prefix == "" {
			continue
		}

		// Skip duplicate prefixes to avoid non-deterministic rewrites
		if prefixCount[prefix] > 1 {
			fmt.Fprintf(os.Stderr, "Warning: skipping route fix for duplicate prefix %s (%d rigs share it)\n",
				prefix, prefixCount[prefix])
			continue
		}

		// Determine the correct canonical path based on actual rig layout
		rigRoutePath := determineRigBeadsPath(ctx.TownRoot, rigName)
		canonicalPath := filepath.Join(ctx.TownRoot, rigRoutePath)

		if idx, exists := routeMap[prefix]; exists {
			// Route exists — only rewrite if current path is redirect-dependent
			// and canonical target has a real .beads directory (not a redirect).
			if routes[idx].Path != rigRoutePath && isRedirectDependent(ctx.TownRoot, routes[idx].Path) {
				if hasRealBeadsDir(canonicalPath) {
					routes[idx].Path = rigRoutePath
					modified = true
				} else {
					fmt.Fprintf(os.Stderr, "Warning: cannot rewrite route %s -> %s to %s (canonical path has no .beads directory)\n",
						prefix, routes[idx].Path, rigRoutePath)
				}
			}
		} else {
			// Route missing — add it if the canonical path has a real .beads dir
			if hasRealBeadsDir(canonicalPath) {
				routeMap[prefix] = len(routes)
				routes = append(routes, beads.Route{
					Prefix: prefix,
					Path:   rigRoutePath,
				})
				modified = true
			}
		}
	}

	if modified {
		return beads.WriteRoutes(beadsDir, routes)
	}

	return nil
}
