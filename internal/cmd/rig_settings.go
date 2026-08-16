// Package cmd provides CLI commands for the gt tool.
// This file implements the gt rig settings commands for viewing and manipulating
// rig settings/config.json files.
package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/spf13/cobra"
)

var rigSettingsCmd = &cobra.Command{
	Use:   "settings",
	Short: "View and manage rig settings",
	Long: `View and manage rig settings (settings/config.json).

Rig settings control behavioral configuration for a rig:
- Agent selection and overrides
- Merge queue settings
- Theme configuration
- Namepool settings
- Crew startup settings
- Workflow settings

Settings are stored in settings/config.json within each rig directory.
Use dot notation to access nested keys (e.g., role_agents.witness or
role_effort.witness). For role configuration, 'gt rig role' provides a simpler
validated interface.`,
	RunE: requireSubcommand,
}

var rigSettingsShowCmd = &cobra.Command{
	Use:     "show <rig>",
	Aliases: []string{"get"},
	Short:   "Display all settings",
	Long: `Display all settings for a rig.

Shows the complete settings/config.json file as formatted JSON.

Example:
  gt rig settings show gastown`,
	Args: cobra.ExactArgs(1),
	RunE: runRigSettingsShow,
}

var rigSettingsSetCmd = &cobra.Command{
	Use:   "set <rig> <key-path> <value>",
	Short: "Set a settings value",
	Long: `Set a settings value using dot notation for nested keys.

The value type is automatically inferred:
- "true"/"false" → boolean
- Numbers → number
- Valid JSON → parsed as JSON
- Otherwise → string

If the settings file doesn't exist, it will be created with a valid scaffold.

Examples:
  gt rig settings set gastown agent claude
  gt rig settings set gastown role_agents.witness gemini
  gt rig settings set gastown role_effort.witness low
  gt rig settings set gastown merge_queue.max_concurrent 5
  gt rig settings set gastown theme.disabled true
  gt rig settings set gastown theme.name forest
  gt rig settings set gastown theme.custom '{"bg":"#111111","fg":"#eeeeee"}'

Prefer 'gt rig role set' when changing role_agents and role_effort together.`,
	Args: cobra.ExactArgs(3),
	RunE: runRigSettingsSet,
}

var rigSettingsUnsetCmd = &cobra.Command{
	Use:   "unset <rig> <key-path>",
	Short: "Remove a settings value",
	Long: `Remove a settings value using dot notation for nested keys.

This removes the key from the settings file. For nested keys, only the
specified key is removed (parent objects remain if they have other keys).

Examples:
  gt rig settings unset gastown agent
  gt rig settings unset gastown role_agents.witness
  gt rig settings unset gastown role_effort.witness`,
	Args: cobra.ExactArgs(2),
	RunE: runRigSettingsUnset,
}

func init() {
	rigCmd.AddCommand(rigSettingsCmd)
	rigSettingsCmd.AddCommand(rigSettingsShowCmd)
	rigSettingsCmd.AddCommand(rigSettingsSetCmd)
	rigSettingsCmd.AddCommand(rigSettingsUnsetCmd)
}

func runRigSettingsShow(cmd *cobra.Command, args []string) error {
	rigName := args[0]

	_, r, err := getRig(rigName)
	if err != nil {
		return err
	}

	settingsPath := filepath.Join(r.Path, "settings", "config.json")
	settings, err := config.LoadRigSettings(settingsPath)
	if err != nil {
		if errors.Is(err, config.ErrNotFound) {
			fmt.Printf("No settings file found at %s\n", settingsPath)
			fmt.Printf("Use 'gt rig settings set' to create one.\n")
			return nil
		}
		return fmt.Errorf("loading settings: %w", err)
	}

	// Format as JSON
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("formatting settings: %w", err)
	}

	fmt.Println(string(data))
	return nil
}

func runRigSettingsSet(_ *cobra.Command, args []string) error {
	rigName := args[0]
	keyPath := args[1]
	valueStr := args[2]

	townRoot, r, err := getRig(rigName)
	if err != nil {
		return err
	}

	settingsPath := filepath.Join(r.Path, "settings", "config.json")

	// Load existing settings or create new
	settings, err := config.LoadRigSettings(settingsPath)
	if err != nil {
		if errors.Is(err, config.ErrNotFound) {
			// Create new settings with scaffold
			settings = config.NewRigSettings()
		} else {
			return fmt.Errorf("loading settings: %w", err)
		}
	}

	if err := validateRigRoleSetting(townRoot, r.Path, keyPath, valueStr); err != nil {
		return err
	}

	// Parse the value
	value := parseValue(valueStr)

	// Set the value using dot notation
	if err := setNestedValue(settings, keyPath, value); err != nil {
		return fmt.Errorf("setting %s: %w", keyPath, err)
	}

	// Save the settings
	if err := config.SaveRigSettings(settingsPath, settings); err != nil {
		return fmt.Errorf("saving settings: %w", err)
	}

	fmt.Printf("%s Set %s=%v in settings for rig %s\n",
		style.Success.Render("✓"), keyPath, formatValueForDisplay(value), rigName)
	return nil
}

func validateRigRoleSetting(townRoot, rigPath, keyPath, value string) error {
	parts := strings.Split(keyPath, ".")
	if len(parts) != 2 {
		return nil
	}
	switch parts[0] {
	case "role_agents":
		return config.ValidateRigRoleAgent(townRoot, rigPath, parts[1], value)
	case "role_effort":
		return config.ValidateRigRoleEffort(townRoot, rigPath, parts[1], value)
	default:
		return nil
	}
}

func runRigSettingsUnset(cmd *cobra.Command, args []string) error {
	rigName := args[0]
	keyPath := args[1]

	_, r, err := getRig(rigName)
	if err != nil {
		return err
	}

	settingsPath := filepath.Join(r.Path, "settings", "config.json")

	// Load existing settings
	settings, err := config.LoadRigSettings(settingsPath)
	if err != nil {
		if errors.Is(err, config.ErrNotFound) {
			return fmt.Errorf("settings file not found at %s", settingsPath)
		}
		return fmt.Errorf("loading settings: %w", err)
	}

	// Unset the value using dot notation
	if err := unsetNestedValue(settings, keyPath); err != nil {
		return fmt.Errorf("unsetting %s: %w", keyPath, err)
	}

	// Save the settings
	if err := config.SaveRigSettings(settingsPath, settings); err != nil {
		return fmt.Errorf("saving settings: %w", err)
	}

	fmt.Printf("%s Unset %s from settings for rig %s\n",
		style.Success.Render("✓"), keyPath, rigName)
	return nil
}

// parseValue attempts to parse a string value into the appropriate type.
// Tries: bool → number → JSON → string
func parseValue(s string) interface{} {
	// Try boolean
	if s == "true" {
		return true
	}
	if s == "false" {
		return false
	}

	// Try integer
	if i, err := strconv.Atoi(s); err == nil {
		return i
	}

	// Try float
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}

	// Try JSON
	var jsonValue interface{}
	if err := json.Unmarshal([]byte(s), &jsonValue); err == nil {
		return jsonValue
	}

	// Default to string
	return s
}

// setNestedValue sets a value in a nested structure using dot notation.
// For example, "role_agents.witness" sets settings.RoleAgents["witness"].
func setNestedValue(obj interface{}, keyPath string, value interface{}) error {
	keys := strings.Split(keyPath, ".")
	if len(keys) == 0 || (len(keys) == 1 && keys[0] == "") {
		return fmt.Errorf("empty key path")
	}

	// Convert to map for manipulation
	data, err := json.Marshal(obj)
	if err != nil {
		return fmt.Errorf("marshaling object: %w", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("unmarshaling object: %w", err)
	}

	// Navigate to the parent of the target key
	current := m
	for i := 0; i < len(keys)-1; i++ {
		key := keys[i]
		if val, ok := current[key]; ok {
			// Check if it's a map
			if nestedMap, ok := val.(map[string]interface{}); ok {
				current = nestedMap
			} else {
				// Convert to map if it's not already
				valData, err := json.Marshal(val)
				if err != nil {
					return fmt.Errorf("marshaling nested value: %w", err)
				}
				var newMap map[string]interface{}
				if err := json.Unmarshal(valData, &newMap); err != nil {
					return fmt.Errorf("cannot set nested key %s: parent is not an object", key)
				}
				current[key] = newMap
				current = newMap
			}
		} else {
			// Create new nested map
			newMap := make(map[string]interface{})
			current[key] = newMap
			current = newMap
		}
	}

	// Set the final value
	finalKey := keys[len(keys)-1]
	current[finalKey] = value

	// Convert back to the original type
	data, err = json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshaling result: %w", err)
	}

	// Unmarshal back into the original struct.
	// If this fails (e.g., parseValue inferred bool/number but the target field
	// is a string), fall back to the string representation of the value.
	if err := json.Unmarshal(data, obj); err != nil {
		if _, isString := value.(string); !isString {
			strVal := fmt.Sprintf("%v", value)
			current[finalKey] = strVal
			value = strVal
			data, marshalErr := json.Marshal(m)
			if marshalErr != nil {
				return fmt.Errorf("marshaling result: %w", marshalErr)
			}
			if retryErr := json.Unmarshal(data, obj); retryErr != nil {
				return fmt.Errorf("unmarshaling result: %w", err)
			}
		} else {
			return fmt.Errorf("unmarshaling result: %w", err)
		}
	}

	// Verify the value was actually set by marshaling back and checking.
	// Unknown fields are silently dropped by json.Unmarshal, so we need to
	// confirm the key exists in the output.
	var verifyMap map[string]interface{}
	checkData, err := json.Marshal(obj)
	if err != nil {
		return fmt.Errorf("marshaling for verification: %w", err)
	}
	if err := json.Unmarshal(checkData, &verifyMap); err != nil {
		return fmt.Errorf("unmarshaling for verification: %w", err)
	}

	// Navigate to the key in the verified output
	verifyCurrent := verifyMap
	for i, key := range keys {
		if i == len(keys)-1 {
			// This is the final key - check if it exists
			if _, exists := verifyCurrent[key]; !exists {
				// The field doesn't exist in the struct definition
				// List all valid top-level keys from RigSettings
				validKeys := []string{
					"type", "version",
					"merge_queue", "theme", "namepool", "crew", "workflow",
					"runtime", "agent", "agents", "role_agents", "role_effort",
				}
				return fmt.Errorf("unknown key %q (valid top-level keys: %s)", keyPath, strings.Join(validKeys, ", "))
			}
			break
		}
		// Navigate deeper
		next, ok := verifyCurrent[key].(map[string]interface{})
		if !ok {
			return fmt.Errorf("invalid key path %q: %s is not an object", keyPath, key)
		}
		verifyCurrent = next
	}

	return nil
}

// unsetNestedValue removes a value from a nested structure using dot notation.
func unsetNestedValue(obj interface{}, keyPath string) error {
	keys := strings.Split(keyPath, ".")
	if len(keys) == 0 {
		return fmt.Errorf("empty key path")
	}

	// Convert to map for manipulation
	data, err := json.Marshal(obj)
	if err != nil {
		return fmt.Errorf("marshaling object: %w", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("unmarshaling object: %w", err)
	}

	// Navigate to the parent of the target key
	current := m
	for i := 0; i < len(keys)-1; i++ {
		key := keys[i]
		if val, ok := current[key]; ok {
			if nestedMap, ok := val.(map[string]interface{}); ok {
				current = nestedMap
			} else {
				return fmt.Errorf("cannot unset nested key %s: parent is not an object", key)
			}
		} else {
			return fmt.Errorf("key path %s not found", keyPath)
		}
	}

	// Remove the final key
	finalKey := keys[len(keys)-1]
	if _, ok := current[finalKey]; !ok {
		return fmt.Errorf("key %s not found", keyPath)
	}
	delete(current, finalKey)

	// Convert back to the original type
	data, err = json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshaling result: %w", err)
	}

	// Zero the struct before unmarshaling so that deleted keys don't persist.
	// json.Unmarshal only sets fields present in the JSON; absent fields keep
	// their prior values. Zeroing first ensures removed keys become zero-valued
	// (and omitempty fields are truly absent on re-serialization).
	reflect.ValueOf(obj).Elem().Set(reflect.Zero(reflect.ValueOf(obj).Elem().Type()))

	// Unmarshal back into the zeroed struct
	if err := json.Unmarshal(data, obj); err != nil {
		return fmt.Errorf("unmarshaling result: %w", err)
	}

	return nil
}

// formatValueForDisplay formats a value for display in success messages.
func formatValueForDisplay(v interface{}) string {
	switch val := v.(type) {
	case string:
		return fmt.Sprintf("%q", val)
	case bool:
		if val {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", v)
	}
}
