// Package beads provides custom type management for agent beads.
package beads

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/util"
)

// typesSentinel is a marker file indicating custom types have been configured.
// This persists across CLI invocations to avoid redundant bd config calls.
const typesSentinel = ".gt-types-configured"

// statusesSentinel is a marker file indicating custom statuses have been configured.
const statusesSentinel = ".gt-statuses-configured"

// ensuredDirs tracks which beads directories have been ensured this session.
// This provides fast in-memory caching for multiple creates in the same CLI run.
var (
	ensuredDirs = make(map[string]bool)
	ensuredMu   sync.Mutex
)

// FindTownRoot walks up from startDir to find the Gas Town root directory.
// The town root is identified by the presence of mayor/town.json.
// Returns the outermost town root found, so that rig repos which were
// originally standalone towns (and still contain mayor/town.json) don't
// shadow the real town root above them.
// Returns empty string if not found (reached filesystem root).
func FindTownRoot(startDir string) string {
	dir := startDir
	candidate := ""
	for {
		townFile := filepath.Join(dir, "mayor", "town.json")
		if _, err := os.Stat(townFile); err == nil {
			candidate = dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return candidate // Reached filesystem root — return outermost found
		}
		dir = parent
	}
}

// ResolveRoutingTarget determines which beads directory a bead ID will route to.
// It extracts the prefix from the bead ID and looks up the corresponding route.
// Returns the resolved beads directory path, following any redirects.
//
// If townRoot is empty or prefix is not found, falls back to the provided fallbackDir.
func ResolveRoutingTarget(townRoot, beadID, fallbackDir string) string {
	if townRoot == "" {
		return fallbackDir
	}

	// Extract prefix from bead ID (e.g., "gt-gastown-polecat-Toast" -> "gt-")
	prefix := ExtractPrefix(beadID)
	if prefix == "" {
		return fallbackDir
	}

	// Look up rig path for this prefix
	rigPath := GetRigPathForPrefix(townRoot, prefix)
	if rigPath == "" {
		fmt.Fprintf(os.Stderr, "Warning: no route found for prefix %q (bead %s), falling back to %s\n", prefix, beadID, fallbackDir)
		return fallbackDir
	}

	// Resolve redirects and get final beads directory
	beadsDir := ResolveBeadsDir(rigPath)
	if beadsDir == "" {
		fmt.Fprintf(os.Stderr, "Warning: could not resolve beads dir for rig %s (bead %s), falling back to %s\n", rigPath, beadID, fallbackDir)
		return fallbackDir
	}

	return beadsDir
}

// EnsureCustomTypes ensures the target beads directory has custom types configured.
// Uses a two-level caching strategy:
//   - In-memory cache for multiple creates in the same CLI invocation
//   - Sentinel file on disk for persistence across CLI invocations
//
// The sentinel file stores the configured types list. When the types list changes
// (e.g., new types added in a gastown upgrade), the sentinel is detected as stale
// and types are re-configured automatically (gt-zmy, gt-26f).
//
// This function is thread-safe and idempotent.
//
// If the beads database does not exist (e.g., after a fresh rig add), this function
// will attempt to initialize it automatically using bd init --server.
func EnsureCustomTypes(beadsDir string) error {
	if beadsDir == "" {
		return fmt.Errorf("empty beads directory")
	}

	typesList := strings.Join(constants.BeadsCustomTypesList(), ",")

	ensuredMu.Lock()
	defer ensuredMu.Unlock()

	// Fast path: in-memory cache (same CLI invocation)
	if ensuredDirs[beadsDir] {
		return nil
	}

	// Fast path: sentinel file matches current types list (previous CLI invocation).
	// The sentinel stores the types that were configured. If types have changed
	// (e.g., "queue" and "event" added), the sentinel won't match and we'll
	// re-configure. Legacy "v1\n" sentinels also won't match.
	sentinelPath := filepath.Join(beadsDir, typesSentinel)
	if data, err := os.ReadFile(sentinelPath); err == nil {
		if strings.TrimSpace(string(data)) == typesList {
			ensuredDirs[beadsDir] = true
			return nil
		}
		// Sentinel exists but is stale — fall through to re-configure
	}

	// Verify beads directory exists
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return fmt.Errorf("beads directory does not exist: %s", beadsDir)
	}

	// Check if database exists and initialize if needed
	if err := ensureDatabaseInitialized(beadsDir); err != nil {
		return fmt.Errorf("ensure database initialized: %w", err)
	}

	// Configure custom types via bd CLI
	bdEnv := append(stripEnvPrefixes(os.Environ(), "BEADS_DIR=", "BEADS_DB=", "BEADS_DOLT_SERVER_DATABASE="), "BEADS_DIR="+beadsDir)
	if dbEnv := DatabaseEnv(beadsDir); dbEnv != "" {
		bdEnv = append(bdEnv, dbEnv)
	}
	cmd := exec.Command("bd", "config", "set", "types.custom", typesList)
	cmd.Dir = beadsDir
	util.SetDetachedProcessGroup(cmd)
	// Set BEADS_DIR and BEADS_DOLT_SERVER_DATABASE explicitly to ensure bd
	// operates on the correct database. Strip inherited values first —
	// getenv() returns the first match (gt-uygpe).
	cmd.Env = bdEnv
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("configure custom types in %s: %s: %w",
			beadsDir, strings.TrimSpace(string(output)), err)
	}

	// Verify the config was actually persisted in the database (GH#2637).
	// bd config set can exit 0 but fail to write if it targets the wrong
	// database (redirect mismatch, stale metadata, server not running).
	// Without this check, the sentinel file below would cache a lie,
	// causing all future EnsureCustomTypes calls to skip re-configuration.
	verifyCmd := exec.Command("bd", "config", "get", "types.custom")
	verifyCmd.Dir = beadsDir
	verifyCmd.Env = bdEnv
	util.SetDetachedProcessGroup(verifyCmd)
	if verifyOutput, err := verifyCmd.Output(); err != nil || !strings.Contains(string(verifyOutput), "agent") {
		return fmt.Errorf("types.custom not persisted in %s after bd config set (verify returned %q): db may be misconfigured",
			beadsDir, strings.TrimSpace(string(verifyOutput)))
	}

	// Write sentinel file with the types list for staleness detection.
	// On next invocation, if types have changed, the sentinel won't match
	// and we'll re-configure automatically.
	_ = os.WriteFile(sentinelPath, []byte(typesList+"\n"), 0644)

	ensuredDirs[beadsDir] = true
	return nil
}

// EnsureCustomStatuses ensures the target beads directory has custom statuses configured.
// Uses the same two-level caching strategy as EnsureCustomTypes:
//   - In-memory cache for multiple operations in the same CLI invocation
//   - Sentinel file on disk for persistence across CLI invocations
//
// This function is thread-safe and idempotent.
func EnsureCustomStatuses(beadsDir string) error {
	if beadsDir == "" {
		return fmt.Errorf("empty beads directory")
	}

	statusesList := strings.Join(constants.BeadsCustomStatusesList(), ",")

	ensuredMu.Lock()
	defer ensuredMu.Unlock()

	cacheKey := beadsDir + ":statuses"

	// Fast path: in-memory cache (same CLI invocation)
	if ensuredDirs[cacheKey] {
		return nil
	}

	// Fast path: sentinel file matches current statuses list
	sentinelPath := filepath.Join(beadsDir, statusesSentinel)
	if data, err := os.ReadFile(sentinelPath); err == nil {
		if strings.TrimSpace(string(data)) == statusesList {
			ensuredDirs[cacheKey] = true
			return nil
		}
		// Sentinel exists but is stale — fall through to re-configure
	}

	// Verify beads directory exists
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return fmt.Errorf("beads directory does not exist: %s", beadsDir)
	}

	// Check if database exists and initialize if needed
	if err := ensureDatabaseInitialized(beadsDir); err != nil {
		return fmt.Errorf("ensure database initialized: %w", err)
	}

	// Read current custom statuses and merge with required ones
	getCmd := exec.Command("bd", "config", "get", "status.custom")
	getCmd.Dir = beadsDir
	util.SetDetachedProcessGroup(getCmd)
	getEnv := append(stripEnvPrefixes(os.Environ(), "BEADS_DIR=", "BEADS_DB=", "BEADS_DOLT_SERVER_DATABASE="), "BEADS_DIR="+beadsDir)
	if dbEnv := DatabaseEnv(beadsDir); dbEnv != "" {
		getEnv = append(getEnv, dbEnv)
	}
	getCmd.Env = getEnv
	existingOutput, _ := getCmd.Output()

	// Build merged set: existing + required
	statusSet := make(map[string]bool)
	if existing := strings.TrimSpace(string(existingOutput)); existing != "" {
		for _, s := range strings.Split(existing, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				statusSet[s] = true
			}
		}
	}
	for _, s := range constants.BeadsCustomStatusesList() {
		statusSet[s] = true
	}

	// Build merged list (sorted for deterministic output)
	var merged []string
	for s := range statusSet {
		merged = append(merged, s)
	}
	sort.Strings(merged)
	mergedStr := strings.Join(merged, ",")

	// Configure custom statuses via bd CLI
	cmd := exec.Command("bd", "config", "set", "status.custom", mergedStr)
	cmd.Dir = beadsDir
	util.SetDetachedProcessGroup(cmd)
	setEnv := append(stripEnvPrefixes(os.Environ(), "BEADS_DIR=", "BEADS_DB=", "BEADS_DOLT_SERVER_DATABASE="), "BEADS_DIR="+beadsDir)
	if dbEnv := DatabaseEnv(beadsDir); dbEnv != "" {
		setEnv = append(setEnv, dbEnv)
	}
	cmd.Env = setEnv
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("configure custom statuses in %s: %s: %w",
			beadsDir, strings.TrimSpace(string(output)), err)
	}

	// Write sentinel file
	_ = os.WriteFile(sentinelPath, []byte(statusesList+"\n"), 0644)

	ensuredDirs[cacheKey] = true
	return nil
}

// prefixRe validates beads prefix format. Must start with a letter, contain only
// alphanumerics and hyphens, max 20 chars.
// NOTE: This MUST stay in sync with beadsPrefixRegexp in internal/rig/manager.go.
// Both exist because rig/manager.go cannot import internal/beads (circular dep).
var prefixRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9-]{0,19}$`)

// ensureDatabaseInitialized checks if a beads database exists and initializes it if needed.
// This handles the case where a rig was added but the database was never created,
// which causes Dolt panics when trying to create agent beads.
//
// Uses --server mode to match all production bd init callers (gastown uses a
// centralized Dolt sql-server). JSONL auto-import is handled by bd init itself.
func ensureDatabaseInitialized(beadsDir string) error {
	// If this beads dir has a redirect, the database lives elsewhere.
	// Never create a new database for a redirected location (polecats, crew, refinery).
	redirectFile := filepath.Join(beadsDir, "redirect")
	if _, err := os.Stat(redirectFile); err == nil {
		return nil
	}

	// Check for Dolt database directory (embedded mode)
	doltDir := filepath.Join(beadsDir, "dolt")
	if _, err := os.Stat(doltDir); err == nil {
		return nil
	}

	// Check for metadata.json (server mode — gastown's exclusive mode).
	// In server mode, .beads/ may contain only metadata.json with no local dolt/ dir.
	// This mirrors the deep check in bdDatabaseExists (internal/rig/manager.go):
	// parse metadata.json and verify the referenced database exists in .dolt-data/.
	// metadata.json can be git-tracked from another workspace where the Dolt server
	// had this database, but this may be a fresh server without it.
	metadataFile := filepath.Join(beadsDir, "metadata.json")
	if data, err := os.ReadFile(metadataFile); err == nil {
		var meta struct {
			DoltMode     string `json:"dolt_mode"`
			DoltDatabase string `json:"dolt_database"`
		}
		if err := json.Unmarshal(data, &meta); err != nil {
			return nil // Can't parse — assume initialized (backward compat)
		}
		if meta.DoltMode == "server" && meta.DoltDatabase != "" {
			townRoot := FindTownRoot(filepath.Dir(beadsDir))
			if townRoot == "" {
				return nil // Can't find town root — assume initialized
			}
			dbDir := filepath.Join(townRoot, ".dolt-data", meta.DoltDatabase)
			if _, err := os.Stat(dbDir); !os.IsNotExist(err) {
				return nil // Database exists (or stat error — assume initialized)
			}
			// metadata.json exists but database doesn't — fall through to init
		} else {
			return nil // Non-server mode or no database ref — assume initialized
		}
	}

	// No database found — need to initialize.
	prefix := detectPrefix(beadsDir)

	// bd init must run from the parent directory (not inside .beads/).
	// Use --server to match all production callers (rig/manager.go, doctor/rig_check.go, cmd/install.go).
	parentDir := filepath.Dir(beadsDir)
	initArgs := []string{"init"}
	if prefix != "" {
		initArgs = append(initArgs, "--prefix", prefix)
	}
	initArgs = append(initArgs, "--server")
	cmd := exec.Command("bd", initArgs...)
	cmd.Dir = parentDir
	util.SetDetachedProcessGroup(cmd)
	initEnv := append(stripEnvPrefixes(os.Environ(), "BEADS_DIR=", "BEADS_DB=", "BEADS_DOLT_SERVER_DATABASE="), "BEADS_DIR="+beadsDir)
	if dbEnv := DatabaseEnv(beadsDir); dbEnv != "" {
		initEnv = append(initEnv, dbEnv)
	}
	cmd.Env = initEnv
	if output, err := cmd.CombinedOutput(); err != nil {
		// Handle "already initialized" gracefully, matching install.go behavior.
		// This can happen due to race conditions or if detection heuristics miss
		// a valid database state.
		outputStr := string(output)
		if strings.Contains(outputStr, "already initialized") {
			return nil
		}
		return fmt.Errorf("bd init: %s: %w", strings.TrimSpace(outputStr), err)
	}

	// Explicitly set issue_prefix — bd init --prefix may not persist it
	// in newer versions (see rig/manager.go InitBeads).
	if prefix != "" {
		pfxCmd := exec.Command("bd", "config", "set", "issue_prefix", prefix)
		pfxCmd.Dir = parentDir
		util.SetDetachedProcessGroup(pfxCmd)
		pfxEnv := append(stripEnvPrefixes(os.Environ(), "BEADS_DIR=", "BEADS_DB=", "BEADS_DOLT_SERVER_DATABASE="), "BEADS_DIR="+beadsDir)
		if dbEnv := DatabaseEnv(beadsDir); dbEnv != "" {
			pfxEnv = append(pfxEnv, dbEnv)
		}
		pfxCmd.Env = pfxEnv
		_, _ = pfxCmd.CombinedOutput() // Best effort — crash prevention guard
	}

	// Run bd migrate to ensure the wisps table and auxiliary tables exist.
	// Without this, bd create --ephemeral crashes with a Dolt nil pointer
	// dereference when the wisps table is missing (GH#1769).
	//
	// After bd init --server, the Dolt SQL server may need time to register
	// the new database in its catalog. Retry once after a short delay if the
	// first migrate attempt fails (GH#1769).
	migrateEnv := append(stripEnvPrefixes(os.Environ(), "BEADS_DIR=", "BEADS_DB=", "BEADS_DOLT_SERVER_DATABASE="), "BEADS_DIR="+beadsDir)
	if dbEnv := DatabaseEnv(beadsDir); dbEnv != "" {
		migrateEnv = append(migrateEnv, dbEnv)
	}
	migrateCmd := exec.Command("bd", "migrate", "--yes")
	migrateCmd.Dir = parentDir
	migrateCmd.Env = migrateEnv
	util.SetDetachedProcessGroup(migrateCmd)
	if _, err := migrateCmd.CombinedOutput(); err != nil {
		// First attempt failed — server may not have registered the database yet.
		// Wait briefly and retry once.
		time.Sleep(500 * time.Millisecond)
		retryCmd := exec.Command("bd", "migrate", "--yes")
		retryCmd.Dir = parentDir
		retryCmd.Env = migrateEnv
		util.SetDetachedProcessGroup(retryCmd)
		_, _ = retryCmd.CombinedOutput() // Best effort on retry — CreateAgentBead fallback handles failure
	}

	return nil
}

// detectPrefix determines the beads prefix for a directory.
// Resolution order:
//  1. Town-level config: FindTownRoot → config.GetRigPrefix (authoritative source from rigs.json)
//  2. Local config.yaml: issue-prefix or prefix field
//  3. Default: "gt"
//
// All candidates are validated against prefixRe before use.
//
// Known limitation: when beadsDir is a routed path (e.g., mayor/rig/.beads
// via beads routing), filepath.Base(filepath.Dir(beadsDir)) yields "rig" not
// the actual rig name. GetRigPrefix will not find "rig" in rigs.json and
// returns the default "gt". This is a safe fallback — "gt" is the universal
// default prefix — but rigs with custom prefixes accessed via routed paths
// will silently use "gt" instead. Fixing this would require walking up the
// directory tree to resolve the actual rig name, which is out of scope for
// this crash-prevention guard.
func detectPrefix(beadsDir string) string {
	// 1. Try authoritative source: rigs.json via town root
	rigDir := filepath.Dir(beadsDir)
	if townRoot := FindTownRoot(rigDir); townRoot != "" {
		rigName := filepath.Base(rigDir)
		if prefix := config.GetRigPrefix(townRoot, rigName); prefix != "" && prefixRe.MatchString(prefix) {
			return prefix
		}
	}

	// 2. Fallback: read from config.yaml.
	// NOTE: Inside towns, this is typically unreachable because GetRigPrefix
	// always returns at least "gt" (the default) when a rig isn't found in
	// rigs.json. This fallback is primarily for standalone rigs outside towns.
	configPath := filepath.Join(beadsDir, "config.yaml")
	if data, err := os.ReadFile(configPath); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			for _, key := range []string{"issue-prefix:", "prefix:"} {
				if strings.HasPrefix(line, key) {
					parts := strings.SplitN(line, ":", 2)
					if len(parts) == 2 {
						candidate := strings.TrimSpace(parts[1])
						// Strip quotes first, then trailing dash — matches
						// detectBeadsPrefixFromConfig in rig/manager.go.
						candidate = stripYAMLQuotes(candidate)
						candidate = strings.TrimSuffix(candidate, "-")
						if candidate != "" && prefixRe.MatchString(candidate) {
							return candidate
						}
					}
				}
			}
		}
	}

	// 3. Default
	return "gt"
}

// stripYAMLQuotes removes surrounding single or double quotes from a string.
// Note: unlike strings.Trim in detectBeadsPrefixFromConfig (rig/manager.go),
// this only strips matching pairs — arguably more correct for well-formed YAML.
func stripYAMLQuotes(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// ResetEnsuredDirs clears the in-memory cache of ensured directories.
// This is primarily useful for testing.
func ResetEnsuredDirs() {
	ensuredMu.Lock()
	defer ensuredMu.Unlock()
	ensuredDirs = make(map[string]bool)
}
