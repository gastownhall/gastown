// Package beads provides custom type management for agent beads.
package beads

import (
	"encoding/json"
	"errors"
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
	ensuredDirs        = make(map[string]bool)
	ensuredMu          sync.Mutex
	schemaMigratedDirs = make(map[string]bool)
	schemaMigratedMu   sync.Mutex
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
	if ensuredDirs[beadsDir] {
		ensuredMu.Unlock()
		return nil
	}
	ensuredMu.Unlock()

	// Verify beads directory exists before touching cache/sentinel state.
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return fmt.Errorf("beads directory does not exist: %s", beadsDir)
	}

	// Ensure database and schema before any fast path. A stale sentinel must
	// not bypass migrations needed by a newer bd runtime.
	if err := ensureDatabaseInitialized(beadsDir); err != nil {
		return fmt.Errorf("ensure database initialized: %w", err)
	}

	ensuredMu.Lock()
	defer ensuredMu.Unlock()

	// Fast path: in-memory cache (same CLI invocation). Check again after
	// initialization in case another caller populated it while we migrated.
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

	// Configure custom types via bd CLI, pinned to this database and forced
	// into mutation mode so server-mode writes are committed before callers
	// create typed beads.
	bdEnv := BuildMutationPinnedBDEnv(os.Environ(), beadsDir)
	cmd := exec.Command("bd", "config", "set", "types.custom", typesList)
	cmd.Dir = beadsDir
	util.SetDetachedProcessGroup(cmd)
	// Set BEADS_DIR and BEADS_DOLT_SERVER_DATABASE explicitly to ensure bd
	// operates on the correct database. Strip inherited values first —
	// getenv() returns the first match (gt-uygpe).
	cmd.Env = bdEnv
	snapshot := snapshotTrackedConfigYAML(beadsDir)
	output, err := cmd.CombinedOutput()
	restoreErr := restoreTrackedConfigYAML(snapshot)
	if err != nil {
		if restoreErr != nil {
			err = errors.Join(err, fmt.Errorf("restoring tracked config.yaml in %s: %w", beadsDir, restoreErr))
		}
		return fmt.Errorf("configure custom types in %s: %s: %w",
			beadsDir, strings.TrimSpace(string(output)), err)
	}
	if restoreErr != nil {
		return fmt.Errorf("restoring tracked config.yaml in %s: %w", beadsDir, restoreErr)
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
	cacheKey := beadsDir + ":statuses"

	ensuredMu.Lock()
	if ensuredDirs[cacheKey] {
		ensuredMu.Unlock()
		return nil
	}
	ensuredMu.Unlock()

	// Verify beads directory exists before touching cache/sentinel state.
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return fmt.Errorf("beads directory does not exist: %s", beadsDir)
	}

	// Ensure database and schema before any fast path. A stale sentinel must
	// not bypass migrations needed by a newer bd runtime.
	if err := ensureDatabaseInitialized(beadsDir); err != nil {
		return fmt.Errorf("ensure database initialized: %w", err)
	}

	ensuredMu.Lock()
	defer ensuredMu.Unlock()

	// Fast path: in-memory cache (same CLI invocation). Check again after
	// initialization in case another caller populated it while we migrated.
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

	// Read current custom statuses and merge with required ones
	getCmd := exec.Command("bd", "config", "get", "status.custom")
	getCmd.Dir = beadsDir
	util.SetDetachedProcessGroup(getCmd)
	getCmd.Env = BuildReadOnlyPinnedBDEnv(os.Environ(), beadsDir)
	existingOutput, _ := getCmd.Output()

	// Build merged set: existing + required
	statusSet := make(map[string]bool)
	if existing := ParseConfigOutput(existingOutput); existing != "" {
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
	cmd.Env = BuildMutationPinnedBDEnv(os.Environ(), beadsDir)
	snapshot := snapshotTrackedConfigYAML(beadsDir)
	output, err := cmd.CombinedOutput()
	restoreErr := restoreTrackedConfigYAML(snapshot)
	if err != nil {
		if restoreErr != nil {
			err = errors.Join(err, fmt.Errorf("restoring tracked config.yaml in %s: %w", beadsDir, restoreErr))
		}
		return fmt.Errorf("configure custom statuses in %s: %s: %w",
			beadsDir, strings.TrimSpace(string(output)), err)
	}
	if restoreErr != nil {
		return fmt.Errorf("restoring tracked config.yaml in %s: %w", beadsDir, restoreErr)
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
	forceInit := false

	// If this beads dir has a redirect, the database lives elsewhere.
	// Never create a new database for a redirected location (polecats, crew, refinery).
	redirectFile := filepath.Join(beadsDir, "redirect")
	if _, err := os.Stat(redirectFile); err == nil {
		return nil
	}

	// Check for metadata.json (server mode — gastown's exclusive mode).
	// In server mode, .beads/ may contain only metadata.json with no local dolt/ dir.
	// This mirrors the deep check in bdDatabaseExists (internal/rig/manager.go):
	// parse metadata.json and verify the referenced database exists in .dolt-data/.
	// metadata.json can be git-tracked from another workspace where the Dolt server
	// had this database, but this may be a fresh server without it.
	// This must run before the local dolt/ shortcut because schema migration creates
	// dolt/ as a server-mode discovery directory even when the server database is
	// still missing.
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
			if err := EnsureSchemaMigrated(beadsDir); err == nil {
				return nil
			} else if !isMissingServerDatabaseMigrationError(err) {
				return err
			} else if exists, checked := serverDatabaseExistsInTownDataDir(beadsDir, meta.DoltDatabase); exists {
				return err
			} else if !checked {
				return fmt.Errorf("cannot force-init server database %q without verifying local .dolt-data state: %w", meta.DoltDatabase, err)
			}
			// Metadata can be written before bd init succeeds. If the referenced
			// server database is confirmed absent on disk, fall through to bd init
			// so setup can self-heal instead of retrying a migration against a
			// missing database. If the database exists on disk, the migration
			// error is treated as a transient/catalog problem and must not trigger
			// remote-discarding reinit flags.
			forceInit = true
		} else {
			return nil // Non-server mode or no database ref — assume initialized
		}
	}

	// Check for Dolt database directory (embedded mode)
	doltDir := filepath.Join(beadsDir, "dolt")
	if !forceInit {
		if _, err := os.Stat(doltDir); err == nil {
			return EnsureSchemaMigrated(beadsDir)
		}
	}

	// No database found — need to initialize.
	prefix := detectPrefix(beadsDir)

	// bd setup/repair commands must run from the parent directory (not inside
	// .beads/). The init path uses --server to match all production callers
	// (rig/manager.go, doctor/rig_check.go, cmd/install.go).
	parentDir := filepath.Dir(beadsDir)
	initArgs := []string{"init"}
	if forceInit && configYAMLHasSyncRemote(beadsDir) {
		// Routine self-heal must preserve configured remotes; bd bootstrap
		// recovers from sync.remote without authorizing remote history discard.
		initArgs = []string{"bootstrap", "--yes"}
	} else {
		if prefix != "" {
			initArgs = append(initArgs, "--prefix", prefix)
		}
		initArgs = append(initArgs, "--server")
		if forceInit {
			initArgs = append(initArgs, "--force")
		}
	}
	cmd := exec.Command("bd", initArgs...)
	cmd.Dir = parentDir
	util.SetDetachedProcessGroup(cmd)
	cmd.Env = BuildMutationPinnedBDEnv(os.Environ(), beadsDir)
	snapshot := snapshotTrackedConfigYAML(beadsDir)
	output, err := cmd.CombinedOutput()
	restoreErr := restoreTrackedConfigYAML(snapshot)
	if err != nil {
		// Handle "already initialized" gracefully, matching install.go behavior.
		// This can happen due to race conditions or if detection heuristics miss
		// a valid database state.
		outputStr := string(output)
		if strings.Contains(outputStr, "already initialized") {
			if restoreErr != nil {
				return fmt.Errorf("restoring tracked config.yaml in %s: %w", beadsDir, restoreErr)
			}
			return EnsureSchemaMigrated(beadsDir)
		}
		initErr := fmt.Errorf("bd %s: %s: %w", initArgs[0], strings.TrimSpace(outputStr), err)
		if restoreErr != nil {
			return errors.Join(initErr, fmt.Errorf("restoring tracked config.yaml in %s: %w", beadsDir, restoreErr))
		}
		return initErr
	}
	if restoreErr != nil {
		return fmt.Errorf("restoring tracked config.yaml in %s: %w", beadsDir, restoreErr)
	}

	if err := EnsureSchemaMigrated(beadsDir); err != nil {
		return err
	}

	// Explicitly set issue_prefix — bd init --prefix may not persist it
	// in newer versions (see rig/manager.go InitBeads).
	if prefix != "" {
		pfxCmd := exec.Command("bd", "config", "set", "issue_prefix", prefix)
		pfxCmd.Dir = parentDir
		util.SetDetachedProcessGroup(pfxCmd)
		pfxCmd.Env = BuildMutationPinnedBDEnv(os.Environ(), beadsDir)
		pfxSnapshot := snapshotTrackedConfigYAML(beadsDir)
		_, _ = pfxCmd.CombinedOutput() // Best effort — crash prevention guard
		if restoreErr := restoreTrackedConfigYAML(pfxSnapshot); restoreErr != nil {
			return fmt.Errorf("restoring tracked config.yaml in %s: %w", beadsDir, restoreErr)
		}
	}

	return nil
}

// EnsureSchemaMigrated runs beads schema migrations for a target .beads
// directory before Gastown writes config rows or issues. It is intentionally
// backed by the installed bd CLI so the schema matches the runtime bd that will
// perform later writes.
func EnsureSchemaMigrated(beadsDir string) error {
	if beadsDir == "" {
		return fmt.Errorf("empty beads directory")
	}

	resolved := ResolveBeadsDir(beadsDir)
	if _, err := os.Stat(resolved); os.IsNotExist(err) {
		return fmt.Errorf("beads directory does not exist: %s", resolved)
	}

	cacheKey := resolved
	if abs, err := filepath.Abs(resolved); err == nil {
		cacheKey = abs
	}
	schemaMigratedMu.Lock()
	defer schemaMigratedMu.Unlock()
	if schemaMigratedDirs[cacheKey] {
		return nil
	}

	if err := runSchemaMigration(resolved); err != nil {
		return err
	}

	schemaMigratedDirs[cacheKey] = true
	return nil
}

func runSchemaMigration(beadsDir string) error {
	const maxRetries = 5
	const retryDelay = 500 * time.Millisecond

	var output []byte
	var err error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		output, err = runSchemaMigrationOnce(beadsDir)
		if err == nil {
			return nil
		}
		if !isRetryableSchemaMigrationError(output, err) || attempt == maxRetries {
			break
		}
		// After server-side database creation or restart, the Dolt SQL catalog
		// may need a brief moment before the database is visible.
		time.Sleep(retryDelay)
	}
	return fmt.Errorf("bd migrate --yes in %s: %s: %w", beadsDir, strings.TrimSpace(string(output)), err)
}

func runSchemaMigrationOnce(beadsDir string) ([]byte, error) {
	cmd := exec.Command("bd", "migrate", "--yes")
	cmd.Dir = filepath.Dir(beadsDir)
	cmd.Env = schemaMigrationEnv(beadsDir)
	util.SetDetachedProcessGroup(cmd)
	snapshot := snapshotTrackedConfigYAML(beadsDir)
	output, err := cmd.CombinedOutput()
	restoreErr := restoreTrackedConfigYAML(snapshot)
	if restoreErr != nil {
		if err != nil {
			return output, errors.Join(err, fmt.Errorf("restoring tracked config.yaml in %s: %w", beadsDir, restoreErr))
		}
		return output, restoreErr
	}
	return output, err
}

func serverDatabaseExistsInTownDataDir(beadsDir, dbName string) (exists bool, checked bool) {
	townRoot := FindTownRoot(filepath.Dir(beadsDir))
	if townRoot == "" || dbName == "" {
		return false, false
	}
	dbDir := filepath.Join(townRoot, ".dolt-data", dbName)
	if _, err := os.Stat(dbDir); err == nil {
		return true, true
	} else if os.IsNotExist(err) {
		return false, true
	}
	return false, false
}

func isRetryableSchemaMigrationError(output []byte, err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error() + "\n" + string(output))
	for _, needle := range []string{
		"unknown database",
		"database is read only",
		"cannot update manifest",
		"optimistic lock",
		"serialization failure",
		"lock wait timeout",
		"try restarting transaction",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

func schemaMigrationEnv(beadsDir string) []string {
	env := BuildMutationPinnedBDEnv(os.Environ(), beadsDir)
	// Migration intentionally re-pins to the database named by metadata.json
	// after the shared builder runs. A metadata-present server rig must fail
	// against a missing server database so ensureDatabaseInitialized can force a
	// server init; falling back to a local data dir would hide that state.
	env = stripEnvKey(env, "BEADS_DOLT_DATA_DIR")
	env = stripEnvKey(env, "BEADS_DOLT_SERVER_DATABASE")
	if dbEnv := DatabaseEnv(beadsDir); dbEnv != "" {
		env = append(env, dbEnv)
	}
	return env
}

// TrackedConfigSnapshot captures the contents and mode of a git-tracked
// .beads/config.yaml so bd side effects can be undone after a subprocess.
type TrackedConfigSnapshot struct {
	path string
	data []byte
	mode os.FileMode
}

// SnapshotTrackedConfigYAML snapshots .beads/config.yaml only when git tracks
// it. Untracked local config files remain under caller control.
func SnapshotTrackedConfigYAML(beadsDir string) *TrackedConfigSnapshot {
	return snapshotTrackedConfigYAML(beadsDir)
}

func snapshotTrackedConfigYAML(beadsDir string) *TrackedConfigSnapshot {
	configPath := filepath.Join(beadsDir, "config.yaml")
	info, err := os.Stat(configPath)
	if err != nil || info.IsDir() {
		return nil
	}
	if !gitTracksFile(filepath.Dir(beadsDir), configPath) {
		return nil
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil
	}
	return &TrackedConfigSnapshot{
		path: configPath,
		data: data,
		mode: info.Mode().Perm(),
	}
}

// RestoreTrackedConfigYAML restores a snapshot captured by
// SnapshotTrackedConfigYAML.
func RestoreTrackedConfigYAML(snapshot *TrackedConfigSnapshot) error {
	return restoreTrackedConfigYAML(snapshot)
}

func restoreTrackedConfigYAML(snapshot *TrackedConfigSnapshot) error {
	if snapshot == nil {
		return nil
	}
	current, err := os.ReadFile(snapshot.path)
	if err != nil {
		if os.IsNotExist(err) {
			return os.WriteFile(snapshot.path, snapshot.data, snapshot.mode)
		}
		return err
	}
	info, statErr := os.Stat(snapshot.path)
	if string(current) == string(snapshot.data) {
		if statErr == nil && info.Mode().Perm() != snapshot.mode {
			return os.Chmod(snapshot.path, snapshot.mode)
		}
		return nil
	}
	if err := os.WriteFile(snapshot.path, snapshot.data, snapshot.mode); err != nil {
		return err
	}
	return os.Chmod(snapshot.path, snapshot.mode)
}

func isMissingServerDatabaseMigrationError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "unknown database") {
		return true
	}
	missingDatabasePattern := regexp.MustCompile(`\bdatabase\s+['"]?[a-z0-9_-]+['"]?\s+(not found|does not exist)\b`)
	return missingDatabasePattern.MatchString(msg)
}

func gitTracksFile(worktreeDir, path string) bool {
	rootOutput, err := exec.Command("git", "-C", worktreeDir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return false
	}
	root := strings.TrimSpace(string(rootOutput))
	rel, err := filepath.Rel(root, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return false
	}
	return exec.Command("git", "-C", root, "ls-files", "--error-unmatch", rel).Run() == nil
}

// detectPrefix determines the beads prefix for a directory.
// Resolution order:
//  1. Routed rig path local config.yaml: issue-prefix or prefix field
//  2. Town-level rig registry: mayor/rigs.json
//  3. Non-routed local config.yaml: issue-prefix or prefix field
//  4. Default: "gt"
//
// All candidates are validated against prefixRe before use. Local config.yaml
// intentionally wins only for routed mayor/rig paths where filepath-based rig
// discovery does not name the actual rig.
func detectPrefix(beadsDir string) string {
	routed := isRoutedRigBeadsDir(beadsDir)
	if routed {
		if prefix := detectPrefixFromConfigYAML(beadsDir); prefix != "" {
			return prefix
		}
	}

	if prefix := detectPrefixFromTownConfig(beadsDir); prefix != "" {
		return prefix
	}

	if !routed {
		if prefix := detectPrefixFromConfigYAML(beadsDir); prefix != "" {
			return prefix
		}
	}

	return "gt"
}

func detectPrefixFromTownConfig(beadsDir string) string {
	rigName := rigNameForBeadsDir(beadsDir)
	if rigName == "" {
		return ""
	}
	if townRoot := FindTownRoot(filepath.Dir(beadsDir)); townRoot != "" {
		rigsConfig, err := config.LoadRigsConfig(filepath.Join(townRoot, "mayor", "rigs.json"))
		if err != nil {
			return ""
		}
		entry, ok := rigsConfig.Rigs[rigName]
		if !ok || entry.BeadsConfig == nil {
			return ""
		}
		prefix := strings.TrimSuffix(strings.TrimSpace(entry.BeadsConfig.Prefix), "-")
		if prefix != "" && prefixRe.MatchString(prefix) {
			return prefix
		}
	}
	return ""
}

func rigNameForBeadsDir(beadsDir string) string {
	rigDir := filepath.Dir(filepath.Clean(beadsDir))
	if isRoutedRigBeadsDir(beadsDir) {
		return filepath.Base(filepath.Dir(filepath.Dir(rigDir)))
	}
	return filepath.Base(rigDir)
}

func isRoutedRigBeadsDir(beadsDir string) bool {
	rigDir := filepath.Dir(filepath.Clean(beadsDir))
	return filepath.Base(rigDir) == "rig" && filepath.Base(filepath.Dir(rigDir)) == "mayor"
}

func detectPrefixFromConfigYAML(beadsDir string) string {
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
	return ""
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

	schemaMigratedMu.Lock()
	defer schemaMigratedMu.Unlock()
	schemaMigratedDirs = make(map[string]bool)
}

// ParseConfigOutput extracts the config value from `bd config get <key>` output,
// filtering out informational lines (`Note: ...`) and the unset sentinel
// (`<key> (not set)`). Returns "" when no value line is present.
//
// Without this filter, callers that merge the parsed value back into a
// `bd config set` would pollute the config with strings like
// "status.custom (not set)", which fail bd's regex validation (gt-kbi).
func ParseConfigOutput(output []byte) string {
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "Note:") && !strings.Contains(line, "(not set)") {
			return line
		}
	}
	return ""
}
