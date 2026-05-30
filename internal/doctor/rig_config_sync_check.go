package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/rig"
)

// RigConfigSyncCheck verifies that all registered rigs have a config.json file,
// Dolt database, and rig identity bead. This prevents issues where the daemon
// can't find the beads prefix to check docked/parked status.
type RigConfigSyncCheck struct {
	FixableCheck
	missingConfig    []string         // Rig names missing config.json
	prefixMismatches []prefixMismatch // Prefix mismatches between config.json and registry
	missingRigBeads  []rigBeadInfo    // Rigs missing identity beads
	missingDoltDB    []string         // Rigs missing Dolt database
	missingMetadata  []string         // Rigs missing metadata.json
	missingPrefixCfg []string         // Rigs missing issue-prefix in config.yaml
	missingExportCfg []string         // Rigs missing export.auto=false in config.yaml
	dbNameMismatches []dbMismatch     // Dolt database name doesn't match prefix
	dbCheckErrors    []string         // Rigs whose Dolt DB status could not be verified
}

type prefixMismatch struct {
	rigName        string
	configPrefix   string
	registryPrefix string
}

type rigBeadInfo struct {
	rigName string
	prefix  string
	gitURL  string
}

type dbMismatch struct {
	rigName    string
	prefix     string
	currentDB  string
	expectedDB string
}

// NewRigConfigSyncCheck creates a new rig config sync check.
func NewRigConfigSyncCheck() *RigConfigSyncCheck {
	return &RigConfigSyncCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "rig-config-sync",
				CheckDescription: "Verify registered rigs have config.json, Dolt DB, and identity beads",
				CheckCategory:    CategoryConfig,
			},
		},
	}
}

// Run checks if all registered rigs have proper configuration.
func (c *RigConfigSyncCheck) Run(ctx *CheckContext) *CheckResult {
	rigsConfigPath := filepath.Join(ctx.TownRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "Could not load rigs registry",
			Details: []string{err.Error()},
		}
	}

	c.missingConfig = nil
	c.prefixMismatches = nil
	c.missingRigBeads = nil
	c.missingDoltDB = nil
	c.missingMetadata = nil
	c.missingPrefixCfg = nil
	c.missingExportCfg = nil
	c.dbNameMismatches = nil
	c.dbCheckErrors = nil
	var details []string

	for rigName, entry := range rigsConfig.Rigs {
		rigPath := filepath.Join(ctx.TownRoot, rigName)
		configPath := filepath.Join(rigPath, "config.json")

		// Check if rig directory exists
		if _, err := os.Stat(rigPath); os.IsNotExist(err) {
			details = append(details, fmt.Sprintf("Registered rig %s directory does not exist", rigName))
			continue
		}

		// Get expected prefix
		expectedPrefix := ""
		if entry.BeadsConfig != nil {
			expectedPrefix = entry.BeadsConfig.Prefix
		}

		// Check if config.json exists
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			c.missingConfig = append(c.missingConfig, rigName)
			details = append(details, fmt.Sprintf("Rig %s is registered but missing config.json", rigName))
			continue
		}

		// Check if config.json has correct prefix
		rigCfg, err := rig.LoadRigConfig(rigPath)
		if err != nil {
			details = append(details, fmt.Sprintf("Rig %s has unreadable config.json: %v", rigName, err))
			continue
		}

		configPrefix := ""
		if rigCfg.Beads != nil {
			configPrefix = rigCfg.Beads.Prefix
		}

		// Compare prefixes
		if expectedPrefix != "" && configPrefix != "" && expectedPrefix != configPrefix {
			c.prefixMismatches = append(c.prefixMismatches, prefixMismatch{
				rigName:        rigName,
				configPrefix:   configPrefix,
				registryPrefix: expectedPrefix,
			})
			details = append(details, fmt.Sprintf(
				"Rig %s prefix mismatch: config.json has %q, registry has %q",
				rigName, configPrefix, expectedPrefix))
		}

		beadsDir := doltserver.FindRigBeadsDir(ctx.TownRoot, rigName)
		if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
			details = append(details, fmt.Sprintf("Rig %s is missing .beads directory", rigName))
			continue
		}

		// Check required Gas Town defaults in config.yaml.
		configYamlPath := filepath.Join(beadsDir, "config.yaml")
		if data, err := os.ReadFile(configYamlPath); err == nil {
			content := string(data)
			if !strings.Contains(content, "issue-prefix:") && expectedPrefix != "" {
				c.missingPrefixCfg = append(c.missingPrefixCfg, rigName)
				details = append(details, fmt.Sprintf("Rig %s .beads/config.yaml missing issue-prefix", rigName))
			}
			if !beads.ConfigYAMLDisablesAutoExport(content) {
				c.missingExportCfg = append(c.missingExportCfg, rigName)
				details = append(details, fmt.Sprintf("Rig %s .beads/config.yaml must disable export.auto", rigName))
			}
		}

		// Check metadata.json for Dolt database
		metadataPath := filepath.Join(beadsDir, "metadata.json")
		if _, err := os.Stat(metadataPath); os.IsNotExist(err) {
			c.missingMetadata = append(c.missingMetadata, rigName)
			details = append(details, fmt.Sprintf("Rig %s is missing .beads/metadata.json", rigName))
			if exists, err := c.doltDatabaseExists(ctx, rigName); err != nil {
				c.dbCheckErrors = append(c.dbCheckErrors, rigName)
				details = append(details, fmt.Sprintf("Rig %s Dolt database status could not be verified: %v", rigName, err))
			} else if expectedPrefix != "" && !exists {
				c.missingDoltDB = append(c.missingDoltDB, rigName)
				details = append(details, fmt.Sprintf("Rig %s Dolt database '%s' not found on server", rigName, rigName))
			}
			continue
		}

		// Read database name from metadata.json
		metadataBytes, err := os.ReadFile(metadataPath)
		if err != nil {
			details = append(details, fmt.Sprintf("Rig %s could not read metadata.json: %v", rigName, err))
			continue
		}

		var metadata struct {
			DoltDatabase string `json:"dolt_database"`
			DoltMode     string `json:"dolt_mode"`
		}
		if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
			details = append(details, fmt.Sprintf("Rig %s has invalid metadata.json: %v", rigName, err))
			continue
		}

		// Check if Dolt database exists (only for server mode)
		if metadata.DoltMode == "server" {
			// Database name should match the rig directory name (rigName), not the beads
			// prefix. This is the convention established by doltserver.EnsureMetadata:
			// the Dolt database identifier is the rig's directory name so that rigs
			// with short prefixes (e.g. "ts" for trading_scripts) don't collide and
			// bd can always locate the right database without extra config.
			expectedDBName := rigName

			if expectedDBName != "" {
				// Check if database name matches the rig directory name
				if metadata.DoltDatabase != expectedDBName {
					c.dbNameMismatches = append(c.dbNameMismatches, dbMismatch{
						rigName:    rigName,
						prefix:     configPrefix,
						currentDB:  metadata.DoltDatabase,
						expectedDB: expectedDBName,
					})
					details = append(details, fmt.Sprintf(
						"Rig %s database name mismatch: metadata has '%s', should be '%s' (rig name)",
						rigName, metadata.DoltDatabase, expectedDBName))
				}

				if exists, err := c.doltDatabaseExists(ctx, metadata.DoltDatabase); err != nil {
					c.dbCheckErrors = append(c.dbCheckErrors, rigName)
					details = append(details, fmt.Sprintf("Rig %s Dolt database status could not be verified: %v", rigName, err))
				} else if !exists {
					if expectedExists, err := c.doltDatabaseExists(ctx, expectedDBName); err == nil && metadata.DoltDatabase != expectedDBName && expectedExists {
						// The canonical database exists; the mismatch repair below will
						// repoint metadata without re-initializing anything destructive.
					} else {
						c.missingDoltDB = append(c.missingDoltDB, rigName)
						details = append(details, fmt.Sprintf("Rig %s Dolt database '%s' not found on server", rigName, metadata.DoltDatabase))
					}
				}
			}
		}

		// Check if rig identity bead exists
		if configPrefix != "" {
			rigBeadID := fmt.Sprintf("%s-rig-%s", configPrefix, rigName)
			if !c.rigBeadExists(rigBeadID, rigPath, beadsDir) {
				c.missingRigBeads = append(c.missingRigBeads, rigBeadInfo{
					rigName: rigName,
					prefix:  configPrefix,
					gitURL:  entry.GitURL,
				})
				details = append(details, fmt.Sprintf("Rig %s is missing identity bead %s", rigName, rigBeadID))
			}
		}
	}

	// Check for summary
	issueCount := len(c.missingConfig) + len(c.prefixMismatches) + len(c.missingRigBeads) + len(c.missingDoltDB) + len(c.missingMetadata) + len(c.missingPrefixCfg) + len(c.missingExportCfg) + len(c.dbNameMismatches) + len(c.dbCheckErrors)
	if issueCount == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "All registered rigs have valid configuration",
		}
	}

	var parts []string
	if len(c.missingConfig) > 0 {
		parts = append(parts, fmt.Sprintf("%d missing config.json", len(c.missingConfig)))
	}
	if len(c.prefixMismatches) > 0 {
		parts = append(parts, fmt.Sprintf("%d prefix mismatch(es)", len(c.prefixMismatches)))
	}
	if len(c.missingRigBeads) > 0 {
		parts = append(parts, fmt.Sprintf("%d missing identity bead(s)", len(c.missingRigBeads)))
	}
	if len(c.missingDoltDB) > 0 {
		parts = append(parts, fmt.Sprintf("%d missing Dolt DB(s)", len(c.missingDoltDB)))
	}
	if len(c.missingMetadata) > 0 {
		parts = append(parts, fmt.Sprintf("%d missing metadata.json", len(c.missingMetadata)))
	}
	if len(c.missingPrefixCfg) > 0 {
		parts = append(parts, fmt.Sprintf("%d missing issue-prefix", len(c.missingPrefixCfg)))
	}
	if len(c.missingExportCfg) > 0 {
		parts = append(parts, fmt.Sprintf("%d export.auto drift", len(c.missingExportCfg)))
	}
	if len(c.dbNameMismatches) > 0 {
		parts = append(parts, fmt.Sprintf("%d DB name mismatch(es)", len(c.dbNameMismatches)))
	}
	if len(c.dbCheckErrors) > 0 {
		parts = append(parts, fmt.Sprintf("%d Dolt DB status unknown", len(c.dbCheckErrors)))
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: strings.Join(parts, ", "),
		Details: details,
		FixHint: "Run 'gt doctor --fix' to create missing config files and databases",
	}
}

// Fix creates missing config.json files, Dolt databases, and rig identity beads.
func (c *RigConfigSyncCheck) Fix(ctx *CheckContext) error {
	rigsConfigPath := filepath.Join(ctx.TownRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		return fmt.Errorf("could not load rigs registry: %w", err)
	}

	// Fix missing config.json files
	for _, rigName := range c.missingConfig {
		entry, ok := rigsConfig.Rigs[rigName]
		if !ok {
			continue
		}

		rigPath := filepath.Join(ctx.TownRoot, rigName)
		configPath := filepath.Join(rigPath, "config.json")

		prefix := ""
		if entry.BeadsConfig != nil {
			prefix = entry.BeadsConfig.Prefix
		}

		rigCfg := &rig.RigConfig{
			Type:      "rig",
			Version:   1,
			Name:      rigName,
			GitURL:    entry.GitURL,
			CreatedAt: entry.AddedAt,
		}
		if prefix != "" {
			rigCfg.Beads = &rig.BeadsConfig{Prefix: prefix}
		}

		data, err := json.MarshalIndent(rigCfg, "", "  ")
		if err != nil {
			return fmt.Errorf("could not serialize config for %s: %w", rigName, err)
		}

		if err := os.WriteFile(configPath, data, 0644); err != nil {
			return fmt.Errorf("could not write config.json for %s: %w", rigName, err)
		}
	}

	// Fix missing issue-prefix in config.yaml
	for _, rigName := range c.missingPrefixCfg {
		entry, ok := rigsConfig.Rigs[rigName]
		if !ok || entry.BeadsConfig == nil {
			continue
		}

		configYamlPath := filepath.Join(doltserver.FindRigBeadsDir(ctx.TownRoot, rigName), "config.yaml")

		// Read existing config
		data, err := os.ReadFile(configYamlPath)
		if err != nil {
			continue
		}

		// Add issue-prefix line if missing
		content := string(data)
		if !strings.Contains(content, "issue-prefix:") {
			newLine := fmt.Sprintf("\nissue-prefix: %q\n", entry.BeadsConfig.Prefix)
			// Find a good place to insert it
			if strings.Contains(content, "# issue-prefix:") {
				content = strings.Replace(content, "# issue-prefix: \"\"", fmt.Sprintf("issue-prefix: %q", entry.BeadsConfig.Prefix), 1)
			} else {
				content = content + newLine
			}
			if err := os.WriteFile(configYamlPath, []byte(content), 0644); err != nil {
				return fmt.Errorf("could not update config.yaml for %s: %w", rigName, err)
			}
		}
	}

	// Fix missing or enabled auto-export in config.yaml.
	for _, rigName := range c.missingExportCfg {
		entry, ok := rigsConfig.Rigs[rigName]
		if !ok || entry.BeadsConfig == nil {
			continue
		}
		beadsDir := doltserver.FindRigBeadsDir(ctx.TownRoot, rigName)
		if err := beads.EnsureConfigYAML(beadsDir, entry.BeadsConfig.Prefix); err != nil {
			return fmt.Errorf("could not update export.auto for %s: %w", rigName, err)
		}
	}

	// Fix missing metadata.json without requiring the Dolt database to be online.
	for _, rigName := range c.missingMetadata {
		beadsDir := doltserver.FindRigBeadsDir(ctx.TownRoot, rigName)
		if err := doltserver.EnsureMetadataForBeadsDir(ctx.TownRoot, beadsDir, rigName, rigName); err != nil {
			return fmt.Errorf("could not write metadata.json for %s: %w", rigName, err)
		}
	}

	// Fix missing Dolt databases by running bd init
	for _, rigName := range c.missingDoltDB {
		entry, ok := rigsConfig.Rigs[rigName]
		if !ok || entry.BeadsConfig == nil {
			continue
		}

		rigPath := filepath.Join(ctx.TownRoot, rigName)
		beadsDir := doltserver.FindRigBeadsDir(ctx.TownRoot, rigName)
		cmdDir := rigPath
		mayorRigPath := filepath.Join(rigPath, "mayor", "rig")
		if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
			if _, statErr := os.Stat(mayorRigPath); statErr == nil {
				beadsDir = filepath.Join(mayorRigPath, ".beads")
			}
		}
		if beadsDir == filepath.Join(mayorRigPath, ".beads") {
			cmdDir = mayorRigPath
		}
		if exists, err := c.doltDatabaseExists(ctx, rigName); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: skipping Dolt DB initialization for %s because database status could not be verified: %v\n", rigName, err)
			continue
		} else if exists {
			continue
		}

		// Run bd init against the rig-name database, not the prefix-derived default.
		doltCfg := doltserver.DefaultConfig(ctx.TownRoot)
		destroyToken := fmt.Sprintf("DESTROY-%s", entry.BeadsConfig.Prefix)
		cmd := exec.Command("bd", "init", "--prefix", entry.BeadsConfig.Prefix, "--database", rigName, "--server", "--server-port", strconv.Itoa(doltCfg.Port), "--force", "--destroy-token="+destroyToken)
		cmd.Dir = cmdDir
		cmd.Env = append(stripEnvPrefixes(os.Environ(), "BEADS_DIR=", "BEADS_DB=", "BEADS_DOLT_SERVER_DATABASE="),
			"BEADS_DIR="+beadsDir,
			"BEADS_DOLT_SERVER_DATABASE="+rigName,
		)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("could not initialize Dolt DB for %s: %w\n%s", rigName, err, string(output))
		}
	}

	// Fix database name mismatches - rename database to match rig directory name
	renamedDBs := false
	for _, mismatch := range c.dbNameMismatches {
		metadataPath := filepath.Join(doltserver.FindRigBeadsDir(ctx.TownRoot, mismatch.rigName), "metadata.json")

		// Read current metadata
		metadataBytes, err := os.ReadFile(metadataPath)
		if err != nil {
			return fmt.Errorf("could not read metadata.json for %s: %w", mismatch.rigName, err)
		}

		var metadata map[string]interface{}
		if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
			return fmt.Errorf("could not parse metadata.json for %s: %w", mismatch.rigName, err)
		}

		// Update database name to match rig directory name
		metadata["dolt_database"] = mismatch.expectedDB

		// Write updated metadata
		newMetadata, err := json.MarshalIndent(metadata, "", "  ")
		if err != nil {
			return fmt.Errorf("could not serialize metadata.json for %s: %w", mismatch.rigName, err)
		}

		if err := os.WriteFile(metadataPath, newMetadata, 0644); err != nil {
			return fmt.Errorf("could not write metadata.json for %s: %w", mismatch.rigName, err)
		}

		// Rename the Dolt database directory
		dataDir := filepath.Join(ctx.TownRoot, ".dolt-data")
		oldDBPath := filepath.Join(dataDir, mismatch.currentDB)
		newDBPath := filepath.Join(dataDir, mismatch.expectedDB)

		if _, err := os.Stat(oldDBPath); err == nil {
			// Check if new path already exists
			if _, err := os.Stat(newDBPath); err == nil {
				// New path exists - this is a conflict, skip rename
				// The database with the correct name already exists
			} else {
				// Rename the database directory
				if err := os.Rename(oldDBPath, newDBPath); err != nil {
					return fmt.Errorf("could not rename database %s to %s: %w", mismatch.currentDB, mismatch.expectedDB, err)
				}
				renamedDBs = true
			}
		}
	}

	// If we renamed databases, restart the Dolt server to pick up the changes.
	// Guard: skip restart if the server has been running less than 60s — restarting
	// during startup churn is a known crash trigger (gt-9bxzs: Dolt NomsBlockStore
	// panic when SIGTERM arrives mid-write). The server will pick up renamed databases
	// on its next natural restart or on the next doctor --fix run once stable.
	if renamedDBs {
		if running, pid, _ := doltserver.IsRunning(ctx.TownRoot); running && pid > 0 {
			const minStableAge = 60 * time.Second
			state, _ := doltserver.LoadState(ctx.TownRoot)
			if state != nil && !state.StartedAt.IsZero() && time.Since(state.StartedAt) < minStableAge {
				// Server started less than 60s ago — skip restart to avoid crash
				// during Dolt startup churn. Databases will be picked up on next restart.
			} else {
				// Stop the server
				if err := doltserver.Stop(ctx.TownRoot); err != nil {
					return fmt.Errorf("could not stop Dolt server for restart: %w", err)
				}
				// Start the server again
				if err := doltserver.Start(ctx.TownRoot); err != nil {
					return fmt.Errorf("could not restart Dolt server: %w", err)
				}
			}
		}
	}

	// Fix missing rig identity beads
	for _, info := range c.missingRigBeads {
		rigPath := filepath.Join(ctx.TownRoot, info.rigName)
		beadsDir := doltserver.FindRigBeadsDir(ctx.TownRoot, info.rigName)

		bd := beads.NewWithBeadsDir(rigPath, beadsDir)
		fields := &beads.RigFields{
			Repo:   info.gitURL,
			Prefix: info.prefix,
			State:  beads.RigStateActive,
		}

		if _, err := bd.CreateRigBead(info.rigName, fields); err != nil {
			return fmt.Errorf("could not create rig bead for %s: %w", info.rigName, err)
		}

		// Add status:docked label if the rig should be docked
		rigBeadID := fmt.Sprintf("%s-rig-%s", info.prefix, info.rigName)
		cmd := exec.Command("bd", "label", rigBeadID, "--add", "status:docked")
		cmd.Dir = rigPath
		cmd.Env = append(stripEnvPrefixes(os.Environ(), "BEADS_DIR="), "BEADS_DIR="+beadsDir)
		_ = cmd.Run() // Best effort - ignore errors
	}

	return nil
}

// doltDatabaseExists checks if a Dolt database exists. A listing error means
// status is unknown, not that the database is missing.
func (c *RigConfigSyncCheck) doltDatabaseExists(ctx *CheckContext, dbName string) (bool, error) {
	// Use the doltserver package to list databases
	databases, err := doltserver.ListDatabases(ctx.TownRoot)
	if err != nil {
		return false, err
	}

	for _, db := range databases {
		if db == dbName {
			return true, nil
		}
	}
	return false, nil
}

// rigBeadExists checks if a rig identity bead exists.
func (c *RigConfigSyncCheck) rigBeadExists(rigBeadID, rigPath, beadsDir string) bool {
	// Try to show the bead using bd
	cmd := exec.Command("bd", "show", rigBeadID, "--json")
	cmd.Dir = rigPath
	cmd.Env = append(stripEnvPrefixes(os.Environ(), "BEADS_DIR="), "BEADS_DIR="+beadsDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}

	// Check if the output contains the bead ID
	return strings.Contains(string(output), rigBeadID)
}
