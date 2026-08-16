package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jonbaldie/gastown/internal/config"
)

// DeprecatedMergeQueueKeysCheck detects stale deprecated keys in merge_queue config.
// These keys are silently ignored by json.Unmarshal, so rigs with them may have
// config values that appear set but have no effect.
type DeprecatedMergeQueueKeysCheck struct {
	FixableCheck
	// affectedFiles maps settings file path → list of deprecated keys found
	affectedFiles map[string][]string
}

// NewDeprecatedMergeQueueKeysCheck creates a new deprecated merge queue keys check.
func NewDeprecatedMergeQueueKeysCheck() *DeprecatedMergeQueueKeysCheck {
	return &DeprecatedMergeQueueKeysCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "deprecated-merge-queue-keys",
				CheckDescription: "Check for deprecated keys in merge_queue config",
				CheckCategory:    CategoryConfig,
			},
		},
	}
}

// Run scans all rigs for deprecated merge_queue keys in settings/config.json.
func (c *DeprecatedMergeQueueKeysCheck) Run(ctx *CheckContext) *CheckResult {
	rigs := findAllRigs(ctx.TownRoot)
	if len(rigs) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No rigs found",
		}
	}

	c.affectedFiles = make(map[string][]string)
	var details []string

	for _, rigPath := range rigs {
		settingsPath := filepath.Join(rigPath, "settings", "config.json")
		found := findDeprecatedKeys(settingsPath)
		if len(found) > 0 {
			c.affectedFiles[settingsPath] = found
			rigName := filepath.Base(rigPath)
			for _, key := range found {
				details = append(details, fmt.Sprintf("%s: merge_queue.%s is deprecated and has no effect", rigName, key))
			}
		}
	}

	if len(c.affectedFiles) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: fmt.Sprintf("No deprecated merge_queue keys in %d rig(s)", len(rigs)),
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: fmt.Sprintf("Found deprecated merge_queue keys in %d rig(s)", len(c.affectedFiles)),
		Details: details,
		FixHint: "Run 'gt doctor --fix' to remove deprecated keys from settings",
	}
}

// Fix removes deprecated keys from all affected settings files.
func (c *DeprecatedMergeQueueKeysCheck) Fix(ctx *CheckContext) error {
	for settingsPath, keys := range c.affectedFiles {
		if err := removeDeprecatedKeys(settingsPath, keys); err != nil {
			return fmt.Errorf("fixing %s: %w", settingsPath, err)
		}
	}
	// Clear cache so re-run picks up fixed state
	c.affectedFiles = nil
	return nil
}

// findDeprecatedKeys reads a settings file and returns any deprecated merge_queue keys found.
func findDeprecatedKeys(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var raw struct {
		MergeQueue map[string]json.RawMessage `json:"merge_queue"`
	}
	if err := json.Unmarshal(data, &raw); err != nil || raw.MergeQueue == nil {
		return nil
	}

	var found []string
	for _, key := range config.DeprecatedMergeQueueKeys {
		if _, ok := raw.MergeQueue[key]; ok {
			found = append(found, key)
		}
	}
	return found
}

// removeDeprecatedKeys reads a settings file, removes deprecated keys from
// the merge_queue section, and writes it back preserving other fields.
func removeDeprecatedKeys(path string, keys []string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	// Parse into generic structure to preserve all other fields
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("parsing settings: %w", err)
	}

	mqRaw, ok := settings["merge_queue"]
	if !ok {
		return nil // Nothing to fix
	}

	var mq map[string]json.RawMessage
	if err := json.Unmarshal(mqRaw, &mq); err != nil {
		return fmt.Errorf("parsing merge_queue: %w", err)
	}

	for _, key := range keys {
		delete(mq, key)
	}

	// Re-marshal merge_queue back into settings
	mqData, err := json.Marshal(mq)
	if err != nil {
		return fmt.Errorf("marshaling merge_queue: %w", err)
	}
	settings["merge_queue"] = mqData

	// Write back with indentation
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling settings: %w", err)
	}

	return os.WriteFile(path, append(out, '\n'), 0o644)
}
