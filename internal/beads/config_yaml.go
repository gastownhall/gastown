package beads

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnsureConfigYAML ensures config.yaml has both prefix keys set for the given
// beads namespace. Existing non-prefix settings are preserved.
func EnsureConfigYAML(beadsDir, prefix string) error {
	return ensureConfigYAML(beadsDir, prefix, false)
}

// EnsureConfigYAMLValue ensures config.yaml contains key: value while preserving
// existing unrelated settings. It is used for install-time settings that must be
// present before older bd binaries can safely open the Dolt schema.
func EnsureConfigYAMLValue(beadsDir, key, value string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("empty config key")
	}
	wantLine := key + ": " + value
	configPath := filepath.Join(beadsDir, "config.yaml")
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return os.WriteFile(configPath, []byte(wantLine+"\n"), 0644)
	}
	if err != nil {
		return err
	}

	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(content, "\n")
	found := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), key+":") {
			lines[i] = wantLine
			found = true
		}
	}
	if !found {
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		lines = append(lines, wantLine)
	}

	newContent := strings.Join(lines, "\n")
	if !strings.HasSuffix(newContent, "\n") {
		newContent += "\n"
	}
	if newContent == content {
		return nil
	}
	return os.WriteFile(configPath, []byte(newContent), 0644)
}

// EnsureConfigYAMLIfMissing creates config.yaml with the required defaults when
// it is missing. Existing files are left untouched.
func EnsureConfigYAMLIfMissing(beadsDir, prefix string) error {
	return ensureConfigYAML(beadsDir, prefix, true)
}

// EnsureConfigYAMLFromMetadataIfMissing creates config.yaml when missing using
// metadata-derived defaults for prefix when available.
func EnsureConfigYAMLFromMetadataIfMissing(beadsDir, fallbackPrefix string) error {
	prefix := ConfigDefaultsFromMetadata(beadsDir, fallbackPrefix)
	return ensureConfigYAML(beadsDir, prefix, true)
}

// ConfigDefaultsFromMetadata derives config.yaml defaults from metadata.json.
// Falls back to fallbackPrefix when fields are absent.
func ConfigDefaultsFromMetadata(beadsDir, fallbackPrefix string) string {
	prefix := strings.TrimSpace(strings.TrimSuffix(fallbackPrefix, "-"))
	if prefix == "" {
		prefix = fallbackPrefix
	}

	data, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		return prefix
	}

	var meta map[string]interface{}
	if err := json.Unmarshal(data, &meta); err != nil {
		return prefix
	}

	if derived := firstString(meta, "issue_prefix", "issue-prefix", "prefix"); derived != "" {
		prefix = strings.TrimSpace(strings.TrimSuffix(derived, "-"))
	} else if doltDB := firstString(meta, "dolt_database"); doltDB != "" {
		prefix = normalizeDoltDatabasePrefix(doltDB)
	}

	return prefix
}

func firstString(values map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		raw, ok := values[key]
		if !ok {
			continue
		}
		s, ok := raw.(string)
		if !ok {
			continue
		}
		s = strings.TrimSpace(s)
		if s != "" {
			return s
		}
	}
	return ""
}

func normalizeDoltDatabasePrefix(dbName string) string {
	name := strings.TrimSpace(strings.TrimSuffix(dbName, "-"))
	if strings.HasPrefix(name, "beads_") {
		trimmed := strings.TrimPrefix(name, "beads_")
		if trimmed != "" {
			return trimmed
		}
	}
	return name
}

// ConfigYAMLIssuePrefix returns the issue-prefix (falling back to prefix)
// declared in config.yaml content, or "" when neither key is set. Comments do
// not count as configuration.
func ConfigYAMLIssuePrefix(content string) string {
	fallback := ""
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if value, ok := strings.CutPrefix(trimmed, "issue-prefix:"); ok {
			if v := unquoteYAMLScalar(value); v != "" {
				return v
			}
		}
		if value, ok := strings.CutPrefix(trimmed, "prefix:"); ok {
			if v := unquoteYAMLScalar(value); v != "" && fallback == "" {
				fallback = v
			}
		}
	}
	return fallback
}

// unquoteYAMLScalar trims whitespace, inline comments and surrounding quotes
// from a simple YAML scalar value.
func unquoteYAMLScalar(value string) string {
	v := strings.TrimSpace(value)
	if idx := strings.Index(v, " #"); idx >= 0 {
		v = strings.TrimSpace(v[:idx])
	}
	v = strings.Trim(v, `"'`)
	return strings.TrimSpace(strings.TrimSuffix(v, "-"))
}

// ConfigPrefixMatchesMetadata reports whether beadsDir's config.yaml issue-prefix
// agrees with the owner declared by metadata.json (issue_prefix, else
// dolt_database). A mismatch means the tracker will route this directory's beads
// to a database that does not own them.
//
// Why: renaming or adopting a rig has repeatedly rewritten the *town* root
// .beads/config.yaml issue-prefix to the rig's prefix (e.g. hq -> dcdoc) while
// metadata.json kept pointing at the town database, so gt's internal bd queries
// resolved to a database that does not exist for that prefix and failed with
// "no beads database found" (gtf-k2k). Detect the divergence instead of relying
// on someone noticing the symptom.
//
// Returns (configPrefix, metadataPrefix, ok). ok is true when they agree or when
// there is not enough information on disk to judge.
func ConfigPrefixMatchesMetadata(beadsDir string) (string, string, bool) {
	data, err := os.ReadFile(filepath.Join(beadsDir, "config.yaml"))
	if err != nil {
		return "", "", true
	}
	configPrefix := ConfigYAMLIssuePrefix(string(data))
	if configPrefix == "" {
		return "", "", true
	}

	// Sentinel that cannot collide with a real prefix: when metadata.json is
	// absent or declares nothing, ConfigDefaultsFromMetadata echoes the
	// fallback and there is nothing to compare against.
	const noMetadata = "\x00none"
	metadataPrefix := ConfigDefaultsFromMetadata(beadsDir, noMetadata)
	if metadataPrefix == noMetadata {
		return configPrefix, "", true
	}

	return configPrefix, metadataPrefix, configPrefix == metadataPrefix
}

// ConfigYAMLDisablesAutoExport reports whether config.yaml content explicitly
// disables bd's post-run auto-export. Comments do not count as configuration.
func ConfigYAMLDisablesAutoExport(content string) bool {
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "export.auto:") {
			value := strings.TrimSpace(strings.TrimPrefix(trimmed, "export.auto:"))
			value = strings.Trim(value, `"'`)
			return strings.EqualFold(value, "false")
		}
	}
	return false
}

func ensureConfigYAML(beadsDir, prefix string, onlyIfMissing bool) error {
	configPath := filepath.Join(beadsDir, "config.yaml")
	wantPrefix := "prefix: " + prefix
	wantIssuePrefix := "issue-prefix: " + prefix
	// Gas Town rigs should disable idle-monitor to use centralized Dolt server
	wantIdleTimeout := "dolt.idle-timeout: \"0\""
	// Gas Town stores beads in Dolt/server-mode runtime directories that are often
	// redirected or gitignored; bd's post-run auto-export git-add is noisy there.
	wantExportAuto := "export.auto: \"false\""

	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		// New config: include all Gas Town defaults
		content := wantPrefix + "\n" + wantIssuePrefix + "\n" + wantIdleTimeout + "\n" + wantExportAuto + "\n"
		return os.WriteFile(configPath, []byte(content), 0644)
	}
	if err != nil {
		return err
	}
	if onlyIfMissing {
		return nil
	}

	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(content, "\n")
	foundPrefix := false
	foundIssuePrefix := false
	foundIdleTimeout := false
	foundExportAuto := false

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "prefix:") {
			lines[i] = wantPrefix
			foundPrefix = true
			continue
		}
		if strings.HasPrefix(trimmed, "issue-prefix:") {
			lines[i] = wantIssuePrefix
			foundIssuePrefix = true
			continue
		}
		if strings.HasPrefix(trimmed, "dolt.idle-timeout:") {
			lines[i] = wantIdleTimeout
			foundIdleTimeout = true
			continue
		}
		if strings.HasPrefix(trimmed, "export.auto:") {
			lines[i] = wantExportAuto
			foundExportAuto = true
			continue
		}
	}

	if !foundPrefix {
		lines = append(lines, wantPrefix)
	}
	if !foundIssuePrefix {
		lines = append(lines, wantIssuePrefix)
	}
	if !foundIdleTimeout {
		lines = append(lines, wantIdleTimeout)
	}
	if !foundExportAuto {
		lines = append(lines, wantExportAuto)
	}

	newContent := strings.Join(lines, "\n")
	if !strings.HasSuffix(newContent, "\n") {
		newContent += "\n"
	}
	if newContent == content {
		return nil
	}

	return os.WriteFile(configPath, []byte(newContent), 0644)
}
