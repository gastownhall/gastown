package config

import (
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultAutoCompactWindowTokens is the town-wide auto-compaction cap.
// The effective window is min(this, the model's default context window).
// Override with GT_AUTO_COMPACT_WINDOW or `gt config set auto_compact_window N`.
const DefaultAutoCompactWindowTokens = 150_000

// AutoCompactWindowEnv is the provider-neutral env var applied to every agent type.
const AutoCompactWindowEnv = "GT_AUTO_COMPACT_WINDOW"

// ClaudeAutoCompactWindowEnv is Claude Code's native auto-compact window env var.
const ClaudeAutoCompactWindowEnv = "CLAUDE_CODE_AUTO_COMPACT_WINDOW"

// ClaudeAutoCompactFlag is Claude Code's one-launch auto-compact window flag.
const ClaudeAutoCompactFlag = "--autocompact"

// ResolveAutoCompactWindow returns the effective auto-compaction window.
// A missing or invalid cap falls back to DefaultAutoCompactWindowTokens.
// A missing model default leaves the cap unchanged.
func ResolveAutoCompactWindow(cap, modelDefault int) int {
	if cap <= 0 {
		cap = DefaultAutoCompactWindowTokens
	}
	if modelDefault > 0 && modelDefault < cap {
		return modelDefault
	}
	return cap
}

// ParseTokenCount parses a token count such as "150000" or "150k".
func ParseTokenCount(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	multiplier := 1
	if strings.HasSuffix(s, "k") || strings.HasSuffix(s, "K") {
		multiplier = 1000
		s = strings.TrimSpace(s[:len(s)-1])
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n * multiplier, true
}

// ResolveAutoCompactCap returns the configured auto-compaction cap.
// Priority: envOverride (GT_AUTO_COMPACT_WINDOW), then town settings, then 150k.
func ResolveAutoCompactCap(townRoot, envOverride string) int {
	if n, ok := ParseTokenCount(envOverride); ok {
		return n
	}
	if townRoot != "" {
		settings, err := LoadOrCreateTownSettings(TownSettingsPath(townRoot))
		if err == nil && settings != nil && settings.AutoCompactWindow > 0 {
			return settings.AutoCompactWindow
		}
	}
	return DefaultAutoCompactWindowTokens
}

// ModelFromArgs returns the --model value from a runtime argument list.
func ModelFromArgs(args []string) string {
	for i, arg := range args {
		if arg == "--model" && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(arg, "--model=") {
			return strings.TrimPrefix(arg, "--model=")
		}
	}
	return ""
}

// ModelDefaultContextWindow returns the model's native context window in tokens.
// Unknown models return 0 so the configured cap is used unchanged.
func ModelDefaultContextWindow(model string) int {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return 0
	}
	if strings.Contains(model, "[1m]") || strings.HasSuffix(model, "-1m") {
		return 1_000_000
	}
	switch {
	case strings.Contains(model, "sonnet"),
		strings.Contains(model, "opus"),
		strings.Contains(model, "haiku"):
		return 200_000
	case model == "gpt-4o", strings.HasPrefix(model, "gpt-4o-"),
		model == "gpt-4", strings.HasPrefix(model, "gpt-4-"):
		return 128_000
	default:
		return 0
	}
}

// ApplyAutoCompactWindow writes the effective window onto a runtime config.
// Every agent type receives GT_AUTO_COMPACT_WINDOW. Agents that declare a
// native flag or env var also receive those controls.
func ApplyAutoCompactWindow(rc *RuntimeConfig, window int) {
	if rc == nil || window <= 0 {
		return
	}
	if rc.Env == nil {
		rc.Env = make(map[string]string)
	}
	value := strconv.Itoa(window)
	rc.Env[AutoCompactWindowEnv] = value

	flag, envName := autoCompactControls(rc)
	if envName != "" {
		rc.Env[envName] = value
	}
	if flag != "" {
		replaceOrAppendArg(rc, flag, value)
	}
}

func applyResolvedAutoCompact(rc *RuntimeConfig, townRoot, envOverride string) {
	if rc == nil {
		return
	}
	window := ResolveAutoCompactWindow(
		ResolveAutoCompactCap(townRoot, envOverride),
		ModelDefaultContextWindow(ModelFromArgs(rc.Args)),
	)
	ApplyAutoCompactWindow(rc, window)
}

func autoCompactControls(rc *RuntimeConfig) (flag, env string) {
	if rc == nil {
		return "", ""
	}
	for _, name := range []string{rc.Provider, rc.ResolvedAgent, commandBase(rc.Command)} {
		if name == "" {
			continue
		}
		if preset, ok := builtinPresets[AgentPreset(name)]; ok {
			if preset.AutoCompactFlag != "" || preset.AutoCompactEnv != "" {
				return preset.AutoCompactFlag, preset.AutoCompactEnv
			}
		}
	}
	if isClaudeAgent(rc) {
		return ClaudeAutoCompactFlag, ClaudeAutoCompactWindowEnv
	}
	return "", ""
}

func commandBase(command string) string {
	if command == "" {
		return ""
	}
	base := filepath.Base(command)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func replaceOrAppendArg(rc *RuntimeConfig, flag, value string) {
	var args []string
	for i := 0; i < len(rc.Args); i++ {
		arg := rc.Args[i]
		if arg == flag {
			i++
			continue
		}
		if strings.HasPrefix(arg, flag+"=") {
			continue
		}
		args = append(args, arg)
	}
	rc.Args = append(args, flag, value)
}
