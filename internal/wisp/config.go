// Package wisp provides utilities for working with the .beads-wisp directory.
// This file implements wisp-based config storage for transient/local settings.
package wisp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/jonbaldie/gastown/internal/atomicfile"
	"github.com/jonbaldie/gastown/internal/util"
)

// WispConfigDir is the directory for wisp config storage (never synced via git).
const WispConfigDir = ".beads-wisp"

// ConfigSubdir is the subdirectory within WispConfigDir for config files.
const ConfigSubdir = "config"

// ConfigFile represents the JSON structure for wisp config storage.
// Storage location: .beads-wisp/config/<rig>.json
type ConfigFile struct {
	Rig     string                 `json:"rig"`
	Values  map[string]interface{} `json:"values"`
	Blocked []string               `json:"blocked"`
}

// Config provides access to wisp-based config storage for a specific rig.
// This storage is local-only and never synced via git.
type Config struct {
	mu       sync.RWMutex
	townRoot string
	rigName  string
	filePath string
}

// NewConfig creates a new Config for the given rig.
// townRoot is the path to the town directory (e.g., ~/gt).
// rigName is the rig identifier (e.g., "gastown").
func NewConfig(townRoot, rigName string) *Config {
	filePath := filepath.Join(townRoot, WispConfigDir, ConfigSubdir, rigName+".json")
	return &Config{
		townRoot: townRoot,
		rigName:  rigName,
		filePath: filePath,
	}
}

// ConfigPath returns the path to the config file.
func (c *Config) ConfigPath() string {
	return c.filePath
}

// load reads the config file from disk.
// Returns a new empty ConfigFile if the file doesn't exist.
func (c *Config) load() (*ConfigFile, error) {
	data, err := os.ReadFile(c.filePath)
	if os.IsNotExist(err) {
		return &ConfigFile{
			Rig:     c.rigName,
			Values:  make(map[string]interface{}),
			Blocked: []string{},
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg ConfigFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	// Ensure maps are initialized
	if cfg.Values == nil {
		cfg.Values = make(map[string]interface{})
	}
	if cfg.Blocked == nil {
		cfg.Blocked = []string{}
	}

	return &cfg, nil
}

// save writes the config file to disk atomically.
// Uses atomicfile.EnsureDirAndWriteJSON for consistent behavior.
func (c *Config) save(cfg *ConfigFile) error {
	if err := atomicfile.EnsureDirAndWriteJSON(c.filePath, cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

// Get returns the value for the given key, or nil if not set.
// Returns nil for blocked keys.
func (c *Config) Get(key string) interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()

	cfg, err := c.load()
	if err != nil {
		return nil
	}

	// Blocked keys always return nil
	if c.isBlockedInternal(cfg, key) {
		return nil
	}

	return cfg.Values[key]
}

// GetString returns the value for the given key as a string.
// Returns empty string if not set, not a string, or blocked.
func (c *Config) GetString(key string) string {
	val := c.Get(key)
	if s, ok := val.(string); ok {
		return s
	}
	return ""
}

// GetBool returns the value for the given key as a bool.
// Returns false if not set, not a bool, or blocked.
func (c *Config) GetBool(key string) bool {
	val := c.Get(key)
	if b, ok := val.(bool); ok {
		return b
	}
	return false
}

// Set stores a value for the given key.
// Setting a blocked key has no effect (the block takes precedence).
func (c *Config) Set(key string, value interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	cfg, err := c.load()
	if err != nil {
		return err
	}

	// Cannot set a blocked key
	if c.isBlockedInternal(cfg, key) {
		return nil // silently ignore
	}

	cfg.Values[key] = value
	return c.save(cfg)
}

// Block marks a key as blocked (equivalent to NullValue).
// Blocked keys return nil on Get and cannot be Set.
func (c *Config) Block(key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	cfg, err := c.load()
	if err != nil {
		return err
	}

	// Don't add duplicate
	if c.isBlockedInternal(cfg, key) {
		return nil
	}

	// Remove from values and add to blocked
	delete(cfg.Values, key)
	cfg.Blocked = append(cfg.Blocked, key)
	return c.save(cfg)
}

// Unset removes a key from both values and blocked lists.
func (c *Config) Unset(key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	cfg, err := c.load()
	if err != nil {
		return err
	}

	// Remove from values
	delete(cfg.Values, key)

	// Remove from blocked
	cfg.Blocked = util.RemoveFromSlice(cfg.Blocked, key)

	return c.save(cfg)
}

// IsBlocked returns true if the key is blocked.
func (c *Config) IsBlocked(key string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	cfg, err := c.load()
	if err != nil {
		return false
	}

	return c.isBlockedInternal(cfg, key)
}

// isBlockedInternal checks if a key is in the blocked list (no locking).
func (c *Config) isBlockedInternal(cfg *ConfigFile, key string) bool {
	for _, k := range cfg.Blocked {
		if k == key {
			return true
		}
	}
	return false
}

// Keys returns all keys (both set and blocked).
func (c *Config) Keys() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	cfg, err := c.load()
	if err != nil {
		return nil
	}

	keys := make([]string, 0, len(cfg.Values)+len(cfg.Blocked))
	for k := range cfg.Values {
		keys = append(keys, k)
	}
	for _, k := range cfg.Blocked {
		// Only add if not already in values (shouldn't happen but be safe)
		found := false
		for _, existing := range keys {
			if existing == k {
				found = true
				break
			}
		}
		if !found {
			keys = append(keys, k)
		}
	}
	return keys
}

// All returns a copy of all values (excludes blocked keys).
func (c *Config) All() map[string]interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()

	cfg, err := c.load()
	if err != nil {
		return nil
	}

	result := make(map[string]interface{}, len(cfg.Values))
	for k, v := range cfg.Values {
		result[k] = v
	}
	return result
}

// BlockedKeys returns a copy of all blocked keys.
func (c *Config) BlockedKeys() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	cfg, err := c.load()
	if err != nil {
		return nil
	}

	result := make([]string, len(cfg.Blocked))
	copy(result, cfg.Blocked)
	return result
}

// Clear removes all values and blocked keys.
func (c *Config) Clear() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	cfg := &ConfigFile{
		Rig:     c.rigName,
		Values:  make(map[string]interface{}),
		Blocked: []string{},
	}
	return c.save(cfg)
}
