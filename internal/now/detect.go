package now

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/jonbaldie/gastown/internal/config"
)

var preferredRuntimes = []string{"cursor", "claude", "pi"}

// DetectRuntime returns the first built-in runtime whose binary is on PATH.
// Order: cursor, claude, pi, then remaining builtins.
func DetectRuntime() (string, error) {
	var lookedFor []string
	try := func(name string) bool {
		info := config.GetAgentPresetByName(name)
		if info == nil {
			return false
		}
		lookedFor = append(lookedFor, info.Command)
		if binaryPresent(info) {
			return true
		}
		return false
	}

	for _, name := range preferredRuntimes {
		if try(name) {
			return name, nil
		}
	}

	names := config.ListAgentPresets()
	sort.Strings(names)
	for _, name := range names {
		if isPreferred(name) {
			continue
		}
		if try(name) {
			return name, nil
		}
	}

	if len(lookedFor) == 0 {
		lookedFor = []string{"cursor-agent", "claude", "pi"}
	}
	return "", fmt.Errorf("no agent CLI on PATH (looked for %s)", strings.Join(unique(lookedFor), ", "))
}

// RuntimePresent reports whether the runtime's binary is on PATH.
func RuntimePresent(name string) bool {
	info := config.GetAgentPresetByName(name)
	if info == nil {
		return false
	}
	return binaryPresent(info)
}

// RuntimeCommand returns the CLI binary for a built-in runtime.
func RuntimeCommand(name string) string {
	info := config.GetAgentPresetByName(name)
	if info == nil {
		return name
	}
	return info.Command
}

func binaryPresent(info *config.AgentPresetInfo) bool {
	if _, err := exec.LookPath(info.Command); err == nil {
		return true
	}
	for _, name := range info.ProcessNames {
		if name == "" || name == "node" || name == "bun" {
			continue
		}
		if _, err := exec.LookPath(name); err == nil {
			return true
		}
	}
	return false
}

func isPreferred(name string) bool {
	for _, candidate := range preferredRuntimes {
		if name == candidate {
			return true
		}
	}
	return false
}

func unique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
