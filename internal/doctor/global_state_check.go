// ABOUTME: Doctor check for Gas Town global state configuration.
// ABOUTME: Validates that state directories and shell integration are properly configured.

package doctor

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jonbaldie/gastown/internal/shell"
	"github.com/jonbaldie/gastown/internal/state"
)

type GlobalStateCheck struct {
	BaseCheck
}

func NewGlobalStateCheck() *GlobalStateCheck {
	return &GlobalStateCheck{
		BaseCheck: BaseCheck{
			CheckName:        "global-state",
			CheckDescription: "Validates Gas Town global state and shell integration",
			CheckCategory:    CategoryCore,
		},
	}
}

func (c *GlobalStateCheck) Run(ctx *CheckContext) *CheckResult {
	result := &CheckResult{
		Name:   c.Name(),
		Status: StatusOK,
	}

	var details []string
	var warnings []string
	var errors []string

	s, err := state.Load()
	if err != nil {
		if os.IsNotExist(err) {
			result.Message = "Global state not initialized"
			result.FixHint = "Run: gt enable"
			result.Status = StatusWarning
			return result
		}
		result.Message = "Cannot read global state"
		result.Details = []string{err.Error()}
		result.Status = StatusError
		return result
	}

	if s.Enabled {
		details = append(details, "Gas Town: enabled")
	} else {
		details = append(details, "Gas Town: disabled")
		warnings = append(warnings, "Gas Town is disabled globally")
	}

	if s.Version != "" {
		details = append(details, "Version: "+s.Version)
	}

	if s.MachineID != "" {
		details = append(details, "Machine ID: "+s.MachineID)
	}

	rcPath := shell.RCFilePath(shell.DetectShell())
	if hasShellIntegration(rcPath) {
		details = append(details, "Shell integration: installed ("+rcPath+")")
	} else {
		warnings = append(warnings, "Shell integration not installed")
	}

	hookPath := filepath.Join(state.ConfigDir(), "shell-hook.sh")
	if _, err := os.Stat(hookPath); err == nil {
		details = append(details, "Hook script: present")
	} else {
		if hasShellIntegration(rcPath) {
			errors = append(errors, "Hook script missing but shell integration installed")
		}
	}

	result.Details = details

	if len(errors) > 0 {
		result.Status = StatusError
		result.Message = errors[0]
		result.FixHint = "Run: gt shell install"
	} else if len(warnings) > 0 {
		result.Status = StatusWarning
		result.Message = warnings[0]
		if !s.Enabled {
			result.FixHint = "Run: gt enable"
		} else {
			result.FixHint = "Run: gt shell install"
		}
	} else {
		result.Message = "Global state healthy"
	}

	return result
}

func hasShellIntegration(rcPath string) bool {
	// Look for official marker (from gt shell install) or manual sourcing of the hook script.
	markers := []string{"Gas Town Integration", "shell-hook.sh"}
	return checkSourceChain(rcPath, markers, make(map[string]bool), 0)
}

// checkSourceChain reads filePath, checks for any marker string, and
// recursively follows source/. directives found in the file. This handles
// users with modular shell configs (e.g. .zshrc sources profile-specific
// files that source the Gas Town hook script).
func checkSourceChain(filePath string, markers []string, visited map[string]bool, depth int) bool {
	if depth > 5 {
		return false
	}

	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return false
	}
	if visited[absPath] {
		return false
	}
	visited[absPath] = true

	data, err := os.ReadFile(absPath)
	if err != nil {
		return false
	}
	content := string(data)

	for _, marker := range markers {
		if strings.Contains(content, marker) {
			return true
		}
	}

	homeDir, _ := os.UserHomeDir()
	vars := extractShellVars(content, homeDir)

	for _, line := range strings.Split(content, "\n") {
		for _, sourced := range resolveSourcePaths(line, homeDir, vars) {
			if checkSourceChain(sourced, markers, visited, depth+1) {
				return true
			}
		}
	}

	return false
}

// extractShellVars extracts simple variable assignments (VAR="val" or
// export VAR="val") from shell content for resolving paths in source
// directives. Command substitutions and complex expressions are ignored.
func extractShellVars(content, homeDir string) map[string]string {
	vars := map[string]string{"HOME": homeDir}

	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")

		eqIdx := strings.Index(line, "=")
		if eqIdx == -1 {
			continue
		}

		name := strings.TrimSpace(line[:eqIdx])
		if !isShellVarName(name) {
			continue
		}

		value := strings.TrimSpace(line[eqIdx+1:])
		value = unquoteShell(value)

		// Skip command substitutions and complex expressions
		if strings.Contains(value, "$(") || strings.Contains(value, "`") {
			continue
		}

		value = expandShellVars(value, vars, homeDir)
		vars[name] = value
	}

	return vars
}

func isShellVarName(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		if c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
			continue
		}
		if i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return false
	}
	return true
}

func unquoteShell(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// expandShellVars expands ~, ${VAR}, and $VAR references. Longer variable
// names are replaced first to avoid partial prefix matches.
func expandShellVars(s string, vars map[string]string, homeDir string) string {
	if strings.HasPrefix(s, "~/") {
		s = homeDir + s[1:]
	}

	for name, value := range vars {
		s = strings.ReplaceAll(s, "${"+name+"}", value)
	}

	names := make([]string, 0, len(vars))
	for name := range vars {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		return len(names[i]) > len(names[j])
	})
	for _, name := range names {
		s = strings.ReplaceAll(s, "$"+name, vars[name])
	}

	return s
}

// resolveSourcePaths extracts file paths from source/. directives,
// expanding variables and falling back to glob patterns for unresolved
// variables.
func resolveSourcePaths(line, homeDir string, vars map[string]string) []string {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "#") {
		return nil
	}

	// Strip conditional prefixes: [[ ... ]] && source ..., [[ ! ... ]] || source ...
	for _, sep := range []string{"&& source ", "|| source ", "&& . ", "|| . "} {
		if idx := strings.Index(line, sep); idx != -1 {
			line = strings.TrimSpace(line[idx+3:])
			break
		}
	}

	var raw string
	switch {
	case strings.HasPrefix(line, "source "):
		raw = strings.TrimSpace(line[7:])
	case strings.HasPrefix(line, ". "):
		raw = strings.TrimSpace(line[2:])
	default:
		return nil
	}

	raw = unquoteShell(raw)

	// Strip trailing inline comment
	if idx := strings.Index(raw, " #"); idx != -1 {
		raw = strings.TrimSpace(raw[:idx])
	}

	resolved := expandShellVars(raw, vars, homeDir)

	if !strings.Contains(resolved, "$") {
		return []string{resolved}
	}

	// Unresolved variables remain — try glob by replacing $VAR with *
	globbed := replaceUnresolvedVars(resolved)
	if strings.ContainsAny(globbed, "?[") {
		return nil
	}
	matches, err := filepath.Glob(globbed)
	if err != nil || len(matches) == 0 {
		return nil
	}
	return matches
}

// replaceUnresolvedVars replaces remaining $VAR and ${VAR} patterns with *
// so the path can be used as a glob pattern.
func replaceUnresolvedVars(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '$' {
			if i+1 < len(s) && s[i+1] == '{' {
				end := strings.Index(s[i:], "}")
				if end != -1 {
					b.WriteByte('*')
					i += end + 1
					continue
				}
			}
			j := i + 1
			for j < len(s) && (s[j] == '_' || (s[j] >= 'A' && s[j] <= 'Z') || (s[j] >= 'a' && s[j] <= 'z') || (j > i+1 && s[j] >= '0' && s[j] <= '9')) {
				j++
			}
			if j > i+1 {
				b.WriteByte('*')
				i = j
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
