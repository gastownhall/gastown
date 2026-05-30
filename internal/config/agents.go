// Package config provides configuration types and serialization for Gas Town.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type rawAgentRegistry struct {
	Version int                        `json:"version"`
	Agents  map[string]json.RawMessage `json:"agents"`
}

// AgentPreset identifies a supported LLM agent runtime.
// These presets provide sensible defaults that can be overridden in config.
type AgentPreset string

// Supported agent presets (built-in, E2E tested).
const (
	// AgentClaude is Claude Code (default).
	AgentClaude AgentPreset = "claude"
	// AgentGemini is Gemini CLI.
	AgentGemini AgentPreset = "gemini"
	// AgentCodex is OpenAI Codex.
	AgentCodex AgentPreset = "codex"
	// AgentCursor is Cursor Agent.
	AgentCursor AgentPreset = "cursor"
	// AgentAuggie is Auggie CLI.
	AgentAuggie AgentPreset = "auggie"
	// AgentAmp is Sourcegraph AMP.
	AgentAmp AgentPreset = "amp"
	// AgentOpenCode is OpenCode multi-model CLI.
	AgentOpenCode AgentPreset = "opencode"
	// AgentCopilot is GitHub Copilot CLI.
	AgentCopilot AgentPreset = "copilot"
	// AgentPi is Pi Coding Agent (extension-based lifecycle).
	AgentPi AgentPreset = "pi"
	// AgentOmp is Oh My Pi (OMP) — Pi fork with hook-based lifecycle.
	// Inspired by github.com/ProbabilityEngineer/pi-mono gastown integration.
	AgentOmp AgentPreset = "omp"
	// AgentMistral is Mistral Vibe CLI.
	AgentMistral AgentPreset = "vibe"
	// AgentGroqCompound routes the Claude CLI to Groq's compound-beta model via
	// Groq's OpenAI-compatible API endpoint. The claude binary acts as the SDK
	// proxy; ANTHROPIC_BASE_URL and ANTHROPIC_API_KEY are overridden at runtime
	// to redirect traffic to api.groq.com. GROQ_API_KEY must be set in the shell
	// environment — it is read dynamically and never stored in config files.
	AgentGroqCompound AgentPreset = "groq-compound"
)

// AgentPresetInfo contains the configuration details for an agent preset.
// This is the single source of truth for all agent-specific behavior.
// Adding a new agent = adding a builtinPresets entry + optional hook installer.
// No provider-string switch statements should exist outside this registry.
type AgentPresetInfo struct {
	// Name is the preset identifier (e.g., "claude", "gemini", "codex", "cursor", "auggie", "amp", "copilot").
	Name AgentPreset `json:"name"`

	// Command is the CLI binary to invoke.
	Command string `json:"command"`

	// Args are the default command-line arguments for autonomous mode.
	Args []string `json:"args"`

	// Env are environment variables to set when starting the agent.
	// These are merged with the standard GT_* variables.
	// Used for agent-specific configuration like OPENCODE_PERMISSION.
	Env map[string]string `json:"env,omitempty"`

	// ProcessNames are the process names to look for when detecting if the agent is running.
	// Used by tmux.IsAgentRunning to check pane_current_command.
	// E.g., ["node"] for Claude, ["cursor-agent", "agent"] for Cursor (install script symlinks both names).
	ProcessNames []string `json:"process_names,omitempty"`

	// SessionIDEnv is the environment variable for session ID.
	// Used for resuming sessions across restarts.
	SessionIDEnv string `json:"session_id_env,omitempty"`

	// ResumeFlag is the flag/subcommand for resuming a specific session.
	// For claude/gemini: "--resume"
	// For codex: "resume" (subcommand)
	ResumeFlag string `json:"resume_flag,omitempty"`

	// ContinueFlag is the flag for auto-resuming the most recent session.
	// For claude: "--continue" (--resume without args opens interactive picker)
	// If empty, --resume without a session ID is rejected with a clear error.
	ContinueFlag string `json:"continue_flag,omitempty"`

	// ResumeStyle indicates how to invoke resume:
	// "flag" - pass as --resume <id> argument
	// "subcommand" - pass as 'codex resume <id>'
	ResumeStyle string `json:"resume_style,omitempty"`

	// SupportsHooks indicates if the agent supports hooks system.
	SupportsHooks bool `json:"supports_hooks,omitempty"`

	// SupportsForkSession indicates if --fork-session is available.
	// Used by the seance command for session forking.
	SupportsForkSession bool `json:"supports_fork_session,omitempty"`

	// NonInteractive contains settings for non-interactive mode.
	NonInteractive *NonInteractiveConfig `json:"non_interactive,omitempty"`

	// --- Runtime default fields (replaces scattered default*() switch statements) ---

	// PromptMode controls how the initial prompt is delivered: "arg" or "none".
	// Defaults to "arg" if empty.
	PromptMode string `json:"prompt_mode,omitempty"`

	// ConfigDirEnv is the env var for the agent's config directory (e.g., "CLAUDE_CONFIG_DIR").
	ConfigDirEnv string `json:"config_dir_env,omitempty"`

	// ConfigDir is the top-level config directory (e.g., ".claude", ".opencode").
	// Used for slash command provisioning. Empty means no command provisioning.
	ConfigDir string `json:"config_dir,omitempty"`

	// HooksProvider is the hooks framework provider type (e.g., "claude", "opencode").
	// Empty or "none" means no hooks support.
	HooksProvider string `json:"hooks_provider,omitempty"`

	// HooksDir is the directory for hooks/settings (e.g., ".claude", ".opencode/plugins").
	HooksDir string `json:"hooks_dir,omitempty"`

	// HooksSettingsFile is the settings/plugin filename (e.g., "settings.json", "gastown.js").
	HooksSettingsFile string `json:"hooks_settings_file,omitempty"`

	// HooksInformational indicates hooks are instructions-only (not executable lifecycle hooks).
	// For these providers, Gas Town sends startup fallback commands via nudge.
	HooksInformational bool `json:"hooks_informational,omitempty"`

	// HooksUseSettingsDir indicates the agent supports a separate settings directory
	// (e.g., Claude's --settings flag). When true, hook templates are installed in
	// settingsDir; when false, they're installed in workDir.
	HooksUseSettingsDir bool `json:"hooks_use_settings_dir,omitempty"`

	// ReadyPromptPrefix is the prompt prefix for tmux readiness detection (e.g., "❯ ").
	// Empty means delay-based detection only.
	ReadyPromptPrefix string `json:"ready_prompt_prefix,omitempty"`

	// ReadyDelayMs is the delay-based readiness fallback in milliseconds.
	ReadyDelayMs int `json:"ready_delay_ms,omitempty"`

	// InstructionsFile is the instructions file for this agent (e.g., "CLAUDE.md", "AGENTS.md").
	// Defaults to "AGENTS.md" if empty.
	InstructionsFile string `json:"instructions_file,omitempty"`

	// EmitsPermissionWarning indicates the agent shows a bypass-permissions warning on startup
	// that needs to be acknowledged via tmux.
	EmitsPermissionWarning bool `json:"emits_permission_warning,omitempty"`

	// HasTurnBoundaryDrain indicates the agent's hooks system drains the nudge
	// queue on every turn boundary (like Claude's UserPromptSubmit hook). When
	// false, a background nudge-poller process is started to periodically drain
	// the queue and inject via tmux.
	HasTurnBoundaryDrain bool `json:"has_turn_boundary_drain,omitempty"`

	// EscapeCancelsRequest indicates that sending an Escape keystroke to this
	// agent cancels its in-flight generation. NudgeSession normally sends
	// Escape (step 5) to exit vim INSERT mode — harmless for bash/Claude Code,
	// but destructive for agents like Gemini CLI where Escape aborts the
	// active request. When true, NudgeSessionWithOpts skips the Escape
	// keystroke and the 600ms readline timeout that follows it.
	EscapeCancelsRequest bool `json:"escape_cancels_request,omitempty"`

	// ACP is the configuration for ACP (Agent Communication Protocol) support.
	// nil means the agent does not support ACP.
	ACP *ACPConfig `json:"acp,omitempty"`
}

// ACPConfig contains configuration for ACP (Agent Communication Protocol) support.
type ACPConfig struct {
	// Mode specifies how ACP is invoked:
	// - "subcommand" (default): Agent has ACP as a subcommand (e.g., "opencode acp")
	// - "native": Agent is a native ACP binary (e.g., "claude-agent-acp")
	// - "flag": Agent uses a flag to enable ACP (e.g., "gemini --acp")
	// If empty, defaults to "subcommand" if Command is set, otherwise "native".
	Mode string `json:"mode,omitempty"`

	// Command is the subcommand for ACP (e.g., "acp").
	// Used when Mode is "subcommand".
	Command string `json:"command,omitempty"`

	// Args are additional arguments for the ACP command.
	// For "subcommand" mode, appended after the subcommand.
	// For "flag" mode, these are the flags to enable ACP.
	// For "native" mode, ignored (binary is already ACP).
	Args []string `json:"args,omitempty"`
}

// ACP mode constants.
const (
	ACPModeSubcommand = "subcommand" // Agent has ACP as subcommand (e.g., "opencode acp")
	ACPModeNative     = "native"     // Agent is native ACP binary (e.g., "claude-agent-acp")
	ACPModeFlag       = "flag"       // Agent uses flag to enable ACP (e.g., "gemini --acp")
)

// NonInteractiveConfig contains settings for running agents non-interactively.
type NonInteractiveConfig struct {
	// Subcommand is the subcommand for non-interactive execution (e.g., "exec" for codex).
	Subcommand string `json:"subcommand,omitempty"`

	// PromptFlag is the flag for passing prompts (e.g., "-p" for gemini).
	PromptFlag string `json:"prompt_flag,omitempty"`

	// OutputFlag is the flag for structured output (e.g., "--json", "--output-format json").
	OutputFlag string `json:"output_flag,omitempty"`
}

// AgentRegistry contains all known agent presets.
// Can be loaded from JSON config or use built-in defaults.
type AgentRegistry struct {
	// Version is the schema version for the registry.
	Version int `json:"version"`

	// Agents maps agent names to their configurations.
	Agents map[string]*AgentPresetInfo `json:"agents"`
}

// CurrentAgentRegistryVersion is the current schema version.
const CurrentAgentRegistryVersion = 1

// builtinPresets contains the default presets for supported agents.
// Each preset is the single source of truth for its agent's behavior.
var builtinPresets = map[AgentPreset]*AgentPresetInfo{
	AgentClaude: {
		Name:                AgentClaude,
		Command:             "claude",
		Args:                []string{"--dangerously-skip-permissions"},
		ProcessNames:        []string{"node", "claude"}, // Claude runs as Node.js
		SessionIDEnv:        "CLAUDE_SESSION_ID",
		ResumeFlag:          "--resume",
		ContinueFlag:        "--continue",
		ResumeStyle:         "flag",
		SupportsHooks:       true,
		SupportsForkSession: true,
		NonInteractive:      nil, // Claude is native non-interactive
		// Runtime defaults
		PromptMode:             "arg",
		ConfigDirEnv:           "CLAUDE_CONFIG_DIR",
		ConfigDir:              ".claude",
		HooksProvider:          "claude",
		HooksDir:               ".claude",
		HooksSettingsFile:      "settings.json",
		HooksUseSettingsDir:    true,
		ReadyPromptPrefix:      "❯ ",
		ReadyDelayMs:           10000,
		InstructionsFile:       "CLAUDE.md",
		EmitsPermissionWarning: true,
		HasTurnBoundaryDrain:   true,
	},
	AgentGemini: {
		Name:                AgentGemini,
		Command:             "gemini",
		Args:                []string{"--approval-mode", "yolo"},
		ProcessNames:        []string{"gemini"}, // Gemini CLI binary
		SessionIDEnv:        "GEMINI_SESSION_ID",
		ResumeFlag:          "--resume",
		ResumeStyle:         "flag",
		SupportsHooks:       true,
		SupportsForkSession: false,
		NonInteractive: &NonInteractiveConfig{
			PromptFlag: "-p",
			OutputFlag: "--output-format json",
		},
		// Runtime defaults
		PromptMode:           "arg",
		ConfigDir:            ".gemini",
		HooksProvider:        "gemini",
		HooksDir:             ".gemini",
		HooksSettingsFile:    "settings.json",
		ReadyDelayMs:         5000,
		InstructionsFile:     "AGENTS.md",
		EscapeCancelsRequest: true, // Gemini CLI uses Escape to abort active generation
	},
	AgentCodex: {
		Name:                AgentCodex,
		Command:             "codex",
		Args:                []string{"--dangerously-bypass-approvals-and-sandbox"},
		ProcessNames:        []string{"codex"}, // Codex CLI binary
		SessionIDEnv:        "",                // Codex captures from JSONL output
		ResumeFlag:          "resume",
		ResumeStyle:         "subcommand",
		SupportsHooks:       false, // Use env/files instead
		SupportsForkSession: false,
		NonInteractive: &NonInteractiveConfig{
			Subcommand: "exec",
			OutputFlag: "--json",
		},
		// Runtime defaults
		PromptMode:        "none",
		ReadyPromptPrefix: "› ",
		ReadyDelayMs:      3000,
		InstructionsFile:  "AGENTS.md",
	},
	AgentCursor: {
		Name:    AgentCursor,
		Command: "cursor-agent",
		// -f/--force: auto-approve tool use (see cursor-agent --help). Install script also symlinks "agent" -> same binary.
		Args: []string{"-f"},
		// cursor-agent + agent (install symlinks). Pane matching for "agent" requires session env (GT_AGENT=cursor or GT_PROCESS_NAMES includes cursor-agent); see internal/tmux processNamesForSession.
		ProcessNames:        []string{"cursor-agent", "agent"},
		SessionIDEnv:        "", // Uses --resume with chatId directly
		ResumeFlag:          "--resume",
		ResumeStyle:         "flag",
		SupportsHooks:       true,
		SupportsForkSession: false,
		// Non-interactive/headless: -p/--print + --output-format json (matches cursor-agent --help).
		NonInteractive: &NonInteractiveConfig{
			PromptFlag: "-p",
			OutputFlag: "--output-format json",
		},
		// Runtime defaults
		PromptMode:        "arg",
		ConfigDir:         ".cursor",
		HooksProvider:     "cursor",
		HooksDir:          ".cursor",
		HooksSettingsFile: "hooks.json", // installed path: .cursor/hooks.json
		InstructionsFile:  "AGENTS.md",
		// No stable ReadyPromptPrefix yet; delay before nudge poller / early input (HasTurnBoundaryDrain is false — see Copilot).
		ReadyDelayMs: 5000,
	},
	AgentAuggie: {
		Name:                AgentAuggie,
		Command:             "auggie",
		Args:                []string{"--allow-indexing"},
		ProcessNames:        []string{"auggie"},
		SessionIDEnv:        "",
		ResumeFlag:          "--resume",
		ResumeStyle:         "flag",
		SupportsHooks:       false,
		SupportsForkSession: false,
		// Runtime defaults
		PromptMode:       "arg",
		InstructionsFile: "AGENTS.md",
	},
	AgentAmp: {
		Name:                AgentAmp,
		Command:             "amp",
		Args:                []string{"--dangerously-allow-all", "--no-ide"},
		ProcessNames:        []string{"amp"},
		SessionIDEnv:        "",
		ResumeFlag:          "threads continue",
		ResumeStyle:         "subcommand", // 'amp threads continue <threadId>'
		SupportsHooks:       false,
		SupportsForkSession: false,
		// Runtime defaults
		PromptMode:       "arg",
		InstructionsFile: "AGENTS.md",
	},
	AgentOpenCode: {
		Name:    AgentOpenCode,
		Command: "opencode",
		Args:    []string{}, // No CLI flags needed, YOLO via OPENCODE_PERMISSION env
		Env: map[string]string{
			// Auto-approve all tool calls (equivalent to --dangerously-skip-permissions)
			"OPENCODE_PERMISSION":     `{"*":"allow"}`,
			"OPENCODE_CONFIG_CONTENT": `{"lsp":true}`,
		},
		ProcessNames:        []string{"opencode", "node", "bun"}, // Runs as Node.js or Bun
		SessionIDEnv:        "",                                  // OpenCode manages sessions internally
		ResumeFlag:          "",                                  // No resume support yet
		ResumeStyle:         "",
		SupportsHooks:       true, // Uses .opencode/plugins/gastown.js
		SupportsForkSession: false,
		NonInteractive: &NonInteractiveConfig{
			Subcommand: "run",
			OutputFlag: "--format json",
		},
		// Runtime defaults
		PromptMode:        "arg",
		ConfigDir:         ".opencode",
		HooksProvider:     "opencode",
		HooksDir:          ".opencode/plugins",
		HooksSettingsFile: "gastown.js",
		ReadyDelayMs:      8000,
		InstructionsFile:  "AGENTS.md",
		// ACP support
		ACP: &ACPConfig{
			Command: "acp",
		},
	},
	AgentCopilot: {
		Name:                AgentCopilot,
		Command:             "copilot",
		Args:                []string{"--yolo"},
		ProcessNames:        []string{"copilot"}, // Copilot CLI binary (Node.js but reports as "copilot")
		SessionIDEnv:        "",                  // Session IDs stored on disk (~/.copilot/session-state/), not in env
		ResumeFlag:          "--resume",
		ContinueFlag:        "--continue", // GA: resumes most recent session without picker
		ResumeStyle:         "flag",
		SupportsHooks:       true, // Copilot CLI supports .github/hooks/*.json lifecycle hooks
		SupportsForkSession: false,
		NonInteractive: &NonInteractiveConfig{
			PromptFlag: "-p",
		},
		// Runtime defaults
		PromptMode:         "arg",
		ConfigDirEnv:       "COPILOT_HOME", // GA: overrides ~/.copilot/ config directory
		ConfigDir:          ".copilot",
		HooksProvider:      "copilot",
		HooksDir:           ".github/hooks",
		HooksSettingsFile:  "gastown.json",
		HooksInformational: false,
		ReadyPromptPrefix:  "",   // GA: no ❯ prompt; Copilot uses hint text, not a detectable prefix
		ReadyDelayMs:       5000, // Delay-based readiness detection (no prompt prefix)
		InstructionsFile:   "AGENTS.md",
	},
	AgentPi: {
		Name:                AgentPi,
		Command:             "pi",
		Args:                []string{"-e", ".pi/extensions/gastown-hooks.js"},
		ProcessNames:        []string{"pi", "node", "bun"}, // Pi runs as Node.js
		SessionIDEnv:        "PI_SESSION_ID",
		ResumeFlag:          "", // No resume support yet
		ResumeStyle:         "",
		SupportsHooks:       true, // Uses .pi/extensions/gastown-hooks.js
		HooksProvider:       "pi",
		HooksDir:            ".pi/extensions",
		HooksSettingsFile:   "gastown-hooks.js",
		SupportsForkSession: false,
		NonInteractive: &NonInteractiveConfig{
			PromptFlag: "-p",
			OutputFlag: "--no-session",
		},
		// Pi's Node.js TUI takes several seconds to initialize before it can
		// receive tmux input. Without a readiness delay, the startup nudge
		// arrives before the TUI is ready and gets dropped silently.
		PromptMode:   "arg",
		ReadyDelayMs: 8000,
	},
	AgentOmp: {
		Name:                AgentOmp,
		Command:             "omp",
		Args:                []string{"--hook", ".omp/hooks/gastown-hook.ts"},
		ProcessNames:        []string{"omp", "node", "bun"},
		SessionIDEnv:        "OMP_SESSION_ID",
		SupportsHooks:       true,
		HooksProvider:       "omp",
		HooksDir:            ".omp/hooks",
		HooksSettingsFile:   "gastown-hook.ts",
		SupportsForkSession: false,
		NonInteractive: &NonInteractiveConfig{
			PromptFlag: "--prompt",
		},
	},
	AgentMistral: {
		Name:                AgentMistral,
		Command:             "vibe",
		Args:                []string{"--agent", "auto-approve"},
		ProcessNames:        []string{"vibe"},
		SessionIDEnv:        "VIBE_SESSION_ID",
		ResumeFlag:          "--resume",
		ContinueFlag:        "--continue",
		ResumeStyle:         "flag",
		SupportsHooks:       true,
		SupportsForkSession: false,
		NonInteractive: &NonInteractiveConfig{
			PromptFlag: "-p",
			OutputFlag: "json",
		},
		PromptMode:        "arg",
		ConfigDir:         ".vibe",
		HooksProvider:     "vibe",
		HooksDir:          ".vibe",
		HooksSettingsFile: "config.toml",
		ReadyPromptPrefix: "❯ ",
		ReadyDelayMs:      5000,
		InstructionsFile:  "AGENTS.md",
	},
	// AgentGroqCompound uses the Claude CLI as an SDK proxy but routes all
	// requests to Groq's OpenAI-compatible endpoint by overriding the two
	// Anthropic SDK environment variables that control the backend:
	//
	//   ANTHROPIC_BASE_URL  → https://api.groq.com/openai/v1
	//   ANTHROPIC_API_KEY   → $GROQ_API_KEY  (read from the shell env at spawn time)
	//
	// The model flag --model groq/compound-beta selects Groq's compound
	// reasoning model. Because the transport is the Claude binary, all Gas
	// Town hooks, session tracking, tmux readiness detection, and Claude-SDK
	// lifecycle events work identically to the standard claude preset.
	//
	// Prerequisites:
	//   export GROQ_API_KEY=gsk_...
	//
	// The key is resolved at agent spawn time — never stored in config files.
	AgentGroqCompound: {
		Name:    AgentGroqCompound,
		Command: "claude",
		Args: []string{
			"--dangerously-skip-permissions",
		},
		Env: map[string]string{
			"ANTHROPIC_BASE_URL": "https://api.groq.com/openai/v1",
			"ANTHROPIC_MODEL":    "compound-beta",
			"ANTHROPIC_API_KEY":  "$GROQ_API_KEY",
		},
		ProcessNames:         []string{"node", "claude"},
		SessionIDEnv:         "CLAUDE_SESSION_ID",
		ResumeFlag:           "--resume",
		ContinueFlag:         "--continue",
		ResumeStyle:          "flag",
		SupportsHooks:        true,
		PromptMode:           "arg",
		ConfigDirEnv:         "CLAUDE_CONFIG_DIR",
		ConfigDir:            ".claude",
		HooksProvider:        "claude",
		HooksDir:             ".claude",
		HooksSettingsFile:    "settings.json",
		HooksUseSettingsDir:  true,
		ReadyPromptPrefix:    "❯ ",
		ReadyDelayMs:         10000,
		InstructionsFile:     "CLAUDE.md",
		HasTurnBoundaryDrain: true,
	},
}

// Registry state with proper synchronization.
var (
	// registryMu protects all registry state.
	registryMu sync.RWMutex
	// globalRegistry is the merged registry of built-in and user-defined agents.
	globalRegistry *AgentRegistry
	// loadedPaths tracks which config files have been loaded to avoid redundant reads.
	loadedPaths = make(map[string]bool)
	// registryInitialized tracks if builtins have been copied.
	registryInitialized bool
)

// initRegistry initializes the global registry with built-in presets.
// Caller must hold registryMu write lock.
func initRegistryLocked() {
	if registryInitialized {
		return
	}
	globalRegistry = &AgentRegistry{
		Version: CurrentAgentRegistryVersion,
		Agents:  make(map[string]*AgentPresetInfo),
	}
	// Copy built-in presets
	for name, preset := range builtinPresets {
		globalRegistry.Agents[string(name)] = preset
	}
	registryInitialized = true
}

// loadAgentRegistryFromPath loads agent definitions from a JSON file and merges with built-ins.
// Caller must hold registryMu write lock.
func loadAgentRegistryFromPathLocked(path string) error {
	initRegistryLocked()

	if loadedPaths[path] {
		return nil
	}

	data, err := os.ReadFile(path) //nolint:gosec // G304: path is from config
	if err != nil {
		if os.IsNotExist(err) {
			// Don't cache non-existent paths — the file may be created later
			// and we need to pick it up on the next load call.
			return nil
		}
		return err
	}

	var userRegistry rawAgentRegistry
	if err := json.Unmarshal(data, &userRegistry); err != nil {
		return err
	}

	for name, rawPreset := range userRegistry.Agents {
		merged := cloneAgentPresetInfo(globalRegistry.Agents[name])
		if merged == nil {
			merged = &AgentPresetInfo{}
		}
		if err := json.Unmarshal(rawPreset, merged); err != nil {
			return err
		}
		merged.Name = AgentPreset(name)
		globalRegistry.Agents[name] = merged
	}

	loadedPaths[path] = true
	return nil
}

func cloneAgentPresetInfo(src *AgentPresetInfo) *AgentPresetInfo {
	if src == nil {
		return nil
	}
	clone := *src
	if src.Args != nil {
		clone.Args = append([]string(nil), src.Args...)
	}
	if src.Env != nil {
		clone.Env = make(map[string]string, len(src.Env))
		for k, v := range src.Env {
			clone.Env[k] = v
		}
	}
	if src.ProcessNames != nil {
		clone.ProcessNames = append([]string(nil), src.ProcessNames...)
	}
	if src.NonInteractive != nil {
		nonInteractive := *src.NonInteractive
		clone.NonInteractive = &nonInteractive
	}
	if src.ACP != nil {
		acp := *src.ACP
		if src.ACP.Args != nil {
			acp.Args = append([]string(nil), src.ACP.Args...)
		}
		clone.ACP = &acp
	}
	return &clone
}

// LoadAgentRegistry loads agent definitions from a JSON file and merges with built-ins.
// User-defined agents override built-in presets with the same name.
// This function caches loaded paths to avoid redundant file reads.
func LoadAgentRegistry(path string) error {
	registryMu.Lock()
	defer registryMu.Unlock()
	return loadAgentRegistryFromPathLocked(path)
}

// DefaultAgentRegistryPath returns the default path for agent registry.
// Located alongside other town settings.
func DefaultAgentRegistryPath(townRoot string) string {
	return filepath.Join(townRoot, "settings", "agents.json")
}

// DefaultRigAgentRegistryPath returns the default path for rig-level agent registry.
// Located in <rig>/settings/agents.json.
func DefaultRigAgentRegistryPath(rigPath string) string {
	return filepath.Join(rigPath, "settings", "agents.json")
}

// RigAgentRegistryPath returns the path for rig-level agent registry.
// Alias for DefaultRigAgentRegistryPath for consistency with other path functions.
func RigAgentRegistryPath(rigPath string) string {
	return DefaultRigAgentRegistryPath(rigPath)
}

// LoadRigAgentRegistry loads agent definitions from a rig-level JSON file and merges with built-ins.
// This function works similarly to LoadAgentRegistry but for rig-level configurations.
func LoadRigAgentRegistry(path string) error {
	registryMu.Lock()
	defer registryMu.Unlock()
	return loadAgentRegistryFromPathLocked(path)
}

// GetAgentPreset returns the preset info for a given agent name.
// Returns nil if the preset is not found.
func GetAgentPreset(name AgentPreset) *AgentPresetInfo {
	registryMu.Lock()
	initRegistryLocked()
	defer registryMu.Unlock()
	return globalRegistry.Agents[string(name)]
}

// GetAgentPresetByName returns the preset info by string name.
// Returns nil if not found, allowing caller to fall back to defaults.
func GetAgentPresetByName(name string) *AgentPresetInfo {
	registryMu.Lock()
	initRegistryLocked()
	defer registryMu.Unlock()
	return globalRegistry.Agents[name]
}

// ListAgentPresets returns all known agent preset names.
func ListAgentPresets() []string {
	registryMu.Lock()
	initRegistryLocked()
	defer registryMu.Unlock()
	names := make([]string, 0, len(globalRegistry.Agents))
	for name := range globalRegistry.Agents {
		names = append(names, name)
	}
	return names
}

// BuiltInAgentPresetSummary returns a sorted, comma-separated list of built-in preset names
// for CLI help text (gt config agent list, default-agent, --provider, etc.).
func BuiltInAgentPresetSummary() string {
	names := ListAgentPresets()
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// DefaultAgentPreset returns the default agent preset (Claude).
func DefaultAgentPreset() AgentPreset {
	return AgentClaude
}

// RuntimeConfigFromPreset creates a RuntimeConfig from an agent preset.
// This provides the basic Command/Args/Env; additional fields from AgentPresetInfo
// can be accessed separately for extended functionality.
func RuntimeConfigFromPreset(preset AgentPreset) *RuntimeConfig {
	info := GetAgentPreset(preset)
	return runtimeConfigFromAgentInfo(preset, info)
}

func runtimeConfigFromAgentInfo(preset AgentPreset, info *AgentPresetInfo) *RuntimeConfig {
	if info == nil {
		// Fall back to Claude defaults
		return DefaultRuntimeConfig()
	}

	// Copy Env map to avoid mutation
	var envCopy map[string]string
	if len(info.Env) > 0 {
		envCopy = make(map[string]string, len(info.Env))
		for k, v := range info.Env {
			envCopy[k] = v
		}
	}

	rc := &RuntimeConfig{
		Provider: string(info.Name),
		Command:  info.Command,
		Args:     append([]string(nil), info.Args...),
		Env:      envCopy,
	}

	if preset == AgentClaude && rc.Command == "claude" {
		rc.Command = resolveClaudePath()
	}

	return normalizeRuntimeConfig(rc)
}

// BuildResumeCommand builds a command to resume an agent session.
// Returns the full command string including any YOLO/autonomous flags.
// If sessionID is empty or the agent doesn't support resume, returns empty string.
func BuildResumeCommand(agentName, sessionID string) string {
	if sessionID == "" {
		return ""
	}

	info := GetAgentPresetByName(agentName)
	if info == nil || info.ResumeFlag == "" {
		return ""
	}

	// Build base command with args
	args := append([]string(nil), info.Args...)

	// Add resume based on style
	switch info.ResumeStyle {
	case "subcommand":
		// e.g., "codex resume <session_id> --dangerously-bypass-approvals-and-sandbox"
		return info.Command + " " + info.ResumeFlag + " " + sessionID + " " + strings.Join(args, " ")
	case "flag":
		fallthrough
	default:
		// e.g., "claude --dangerously-skip-permissions --resume <session_id>"
		args = append(args, info.ResumeFlag, sessionID)
		return info.Command + " " + strings.Join(args, " ")
	}
}

// SupportsSessionResume checks if an agent supports session resumption.
func SupportsSessionResume(agentName string) bool {
	info := GetAgentPresetByName(agentName)
	return info != nil && info.ResumeFlag != ""
}

// GetSessionIDEnvVar returns the environment variable name for storing session IDs
// for a given agent. Returns empty string if the agent doesn't use env vars for this.
func GetSessionIDEnvVar(agentName string) string {
	info := GetAgentPresetByName(agentName)
	if info == nil {
		return ""
	}
	return info.SessionIDEnv
}

// GetProcessNames returns the process names used to detect if an agent is running.
// Used by tmux.IsAgentRunning to check pane_current_command.
// Returns ["node"] for Claude (default) if agent is not found or has no ProcessNames.
func GetProcessNames(agentName string) []string {
	info := GetAgentPresetByName(agentName)
	if info == nil || len(info.ProcessNames) == 0 {
		// Default to Claude's process names for backwards compatibility
		return []string{"node", "claude"}
	}
	return info.ProcessNames
}

// wrapperCommands lists process wrappers that exec into another binary listed
// in their arguments (e.g., `env -u VAR claude ...`). When the agent's Command
// is one of these, ResolveProcessNames must look past the wrapper to find the
// real agent binary in Args, otherwise liveness detection greps for a process
// named after the wrapper that no longer exists post-exec.
var wrapperCommands = map[string]bool{
	"env":    true,
	"nohup":  true,
	"setsid": true,
	"exec":   true,
	"sudo":   true,
	"doas":   true,
	"stdbuf": true,
	"time":   true,
}

// wrapperFlagsTakeValue returns the set of short flags that consume the
// next argument as a value, for the given wrapper. Used by extractWrappedBinary
// to skip "-flag value" pairs when scanning args for the real binary.
func wrapperFlagsTakeValue(wrapper string) map[string]bool {
	switch wrapper {
	case "env":
		// env: -u VAR (unset), -C DIR (chdir), -S STRING (split-string)
		return map[string]bool{"-u": true, "-C": true, "-S": true}
	case "sudo":
		// sudo: most options take a value; list the common ones
		return map[string]bool{
			"-u": true, "-g": true, "-h": true, "-p": true,
			"-U": true, "-A": true, "-c": true, "-r": true,
			"-t": true, "-T": true, "-D": true, "-R": true,
		}
	case "stdbuf":
		// stdbuf: -i, -o, -e all take a value (mode)
		return map[string]bool{"-i": true, "-o": true, "-e": true}
	}
	return nil
}

// lookupProcessNamesByBinary returns the ProcessNames of the preset whose
// Command (or its basename) matches realBin. Searches the runtime registry
// first, then the canonical builtinPresets so shadowed builtins still resolve.
// Caller must hold registryMu.
func lookupProcessNamesByBinary(realBin string) []string {
	for _, preset := range globalRegistry.Agents {
		if len(preset.ProcessNames) == 0 {
			continue
		}
		if preset.Command == realBin || filepath.Base(preset.Command) == realBin {
			return preset.ProcessNames
		}
	}
	for _, preset := range builtinPresets {
		if len(preset.ProcessNames) == 0 {
			continue
		}
		if preset.Command == realBin || filepath.Base(preset.Command) == realBin {
			return preset.ProcessNames
		}
	}
	return nil
}

// extractWrappedBinary scans wrapper args for the first non-flag token that
// looks like a binary, and returns its basename. Returns "" if no real binary
// is found (e.g., args malformed or wrapper unsupported).
//
// Walks args left-to-right: skips short flags (and their values for known
// value-taking flags), long flags (--foo=bar or --foo), env-style VAR=value
// assignments (env only), and treats `--` as an end-of-options separator.
func extractWrappedBinary(wrapper string, args []string) string {
	flagsTakeValue := wrapperFlagsTakeValue(wrapper)
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			if i+1 < len(args) {
				return filepath.Base(args[i+1])
			}
			return ""
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			// Long option `--foo=bar` carries its own value
			if strings.HasPrefix(a, "--") && strings.Contains(a, "=") {
				continue
			}
			// Long option `--foo` and unknown short options: assume no value
			if flagsTakeValue[a] && i+1 < len(args) {
				i++ // skip the flag's value
			}
			continue
		}
		// env permits VAR=value assignments interspersed with flags
		if wrapper == "env" && strings.Contains(a, "=") {
			continue
		}
		return filepath.Base(a)
	}
	return ""
}

// ResolveProcessNames determines the correct process names for liveness detection
// given an agent name and the actual command binary. This handles custom agents
// that shadow built-in preset names (e.g., a custom "codex" agent that runs
// "opencode" instead of the built-in "codex" binary), and custom agents wrapped
// in process launchers like `env -u VAR <real-binary>`.
//
// args (variadic, optional) is the actual command-line argument slice for the
// agent invocation. When command is a wrapper (env, sudo, nohup, ...), the
// real binary is found in args, not on the registered preset's Args (which
// may belong to the canonical built-in preset, not the user's wrapper).
//
// Resolution order:
//  1. If agentName matches a built-in preset AND the preset's Command matches
//     the actual command → use the preset's ProcessNames (no mismatch).
//  2. If command is a known wrapper, scan args (or the registered preset's Args
//     as a fallback) for the real binary and resolve to its preset's ProcessNames.
//  3. Otherwise, find a built-in preset whose Command matches the actual command
//     and use its ProcessNames (custom agent using a known launcher).
//  4. Fallback: [command] (fully custom binary).
func ResolveProcessNames(agentName, command string, args ...string) []string {
	registryMu.Lock()
	initRegistryLocked()
	defer registryMu.Unlock()

	// Normalize command to basename for comparison. Commands may be
	// path-resolved (e.g., "/home/user/.claude/local/claude" from
	// resolveClaudePath), but built-in presets store bare names ("claude").
	// Process matching (processMatchesNames, pgrep) also uses basenames.
	cmdBase := command
	if command != "" {
		cmdBase = filepath.Base(command)
	}
	unwrappedCmdBase := strings.TrimPrefix(cmdBase, "gt-")

	// Check if agentName matches a built-in/registered preset with matching command.
	// Compare against both the raw command and basename to handle registry entries
	// that store absolute-path commands (e.g., "/opt/bin/my-tool").
	info, infoOK := globalRegistry.Agents[agentName]
	if infoOK {
		if len(info.ProcessNames) > 0 &&
			(info.Command == command ||
				info.Command == cmdBase ||
				filepath.Base(info.Command) == cmdBase ||
				(info.Command == unwrappedCmdBase && strings.HasPrefix(cmdBase, "gt-")) ||
				cmdBase == "") {
			return info.ProcessNames
		}
	}

	// Wrapper case: command is `env`/`sudo`/etc. — find the real binary in
	// args (caller-supplied) or the registered preset's Args, and resolve to
	// THAT binary's preset ProcessNames. Caller args take precedence because
	// the registered preset may be the canonical built-in (not the wrapper).
	if wrapperCommands[cmdBase] {
		argSources := [][]string{args}
		if infoOK && len(info.Args) > 0 {
			argSources = append(argSources, info.Args)
		}
		for _, src := range argSources {
			realBin := extractWrappedBinary(cmdBase, src)
			if realBin == "" {
				continue
			}
			if names := lookupProcessNamesByBinary(realBin); len(names) > 0 {
				return names
			}
			return []string{realBin}
		}
	}

	// Agent name doesn't match or command differs — look up by command
	if cmdBase != "" {
		for _, info := range globalRegistry.Agents {
			if len(info.ProcessNames) == 0 {
				continue
			}
			if info.Command == command ||
				filepath.Base(info.Command) == cmdBase ||
				(strings.HasPrefix(cmdBase, "gt-") && filepath.Base(info.Command) == unwrappedCmdBase) {
				return info.ProcessNames
			}
		}
		// Unknown command — use the binary basename itself
		return []string{cmdBase}
	}

	// No command provided, agent not in registry — Claude defaults
	return []string{"node", "claude"}
}

// MergeWithPreset applies preset defaults to a RuntimeConfig.
// User-specified values take precedence over preset defaults.
// Returns a new RuntimeConfig without modifying the original.
func (rc *RuntimeConfig) MergeWithPreset(preset AgentPreset) *RuntimeConfig {
	if rc == nil {
		return RuntimeConfigFromPreset(preset)
	}

	info := GetAgentPreset(preset)
	if info == nil {
		return rc
	}

	result := &RuntimeConfig{
		Command:       rc.Command,
		Args:          append([]string(nil), rc.Args...),
		InitialPrompt: rc.InitialPrompt,
	}

	// Apply preset defaults only if not overridden
	if result.Command == "" {
		result.Command = info.Command
	}
	if len(result.Args) == 0 {
		result.Args = append([]string(nil), info.Args...)
	}

	return result
}

// IsKnownPreset checks if a string is a known agent preset name.
func IsKnownPreset(name string) bool {
	registryMu.Lock()
	initRegistryLocked()
	defer registryMu.Unlock()
	_, ok := globalRegistry.Agents[name]
	return ok
}

// SaveAgentRegistry writes the agent registry to a file.
func SaveAgentRegistry(path string, registry *AgentRegistry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644) //nolint:gosec // G306: config file
}

// NewExampleAgentRegistry creates an example registry with comments.
func NewExampleAgentRegistry() *AgentRegistry {
	return &AgentRegistry{
		Version: CurrentAgentRegistryVersion,
		Agents: map[string]*AgentPresetInfo{
			// Include one example custom agent
			"my-custom-agent": {
				Name:         "my-custom-agent",
				Command:      "my-agent-cli",
				Args:         []string{"--autonomous", "--no-confirm"},
				SessionIDEnv: "MY_AGENT_SESSION_ID",
				ResumeFlag:   "--resume",
				ResumeStyle:  "flag",
				NonInteractive: &NonInteractiveConfig{
					PromptFlag: "-p",
					OutputFlag: "--json",
				},
			},
		},
	}
}

// ResetRegistryForTesting clears all registry state.
// This is intended for use in tests only to ensure test isolation.
func ResetRegistryForTesting() {
	registryMu.Lock()
	defer registryMu.Unlock()
	globalRegistry = nil
	loadedPaths = make(map[string]bool)
	registryInitialized = false
}

// RegisterAgentForTesting adds a custom agent preset to the registry.
// The registry is initialized first if needed. Intended for test use only.
func RegisterAgentForTesting(name string, info AgentPresetInfo) {
	registryMu.Lock()
	initRegistryLocked()
	defer registryMu.Unlock()
	globalRegistry.Agents[name] = &info
}

// ResolveACPConfig determines the correct ACP configuration for an agent
// given its name and the actual command binary. This handles custom agents
// that use an ACP-compatible launcher like "opencode".
func ResolveACPConfig(agentName, command string) *ACPConfig {
	registryMu.Lock()
	initRegistryLocked()
	defer registryMu.Unlock()

	// 1. Check if agentName matches a registered preset with ACP config.
	if info, ok := globalRegistry.Agents[agentName]; ok && info.ACP != nil && info.ACP.Command != "" {
		return info.ACP
	}

	// 2. Otherwise, find a registered preset whose Command matches and has ACP.
	if command != "" {
		cmdBase := filepath.Base(command)
		for _, info := range globalRegistry.Agents {
			if (info.Command == command || filepath.Base(info.Command) == cmdBase) && info.ACP != nil && info.ACP.Command != "" {
				return info.ACP
			}
		}
	}

	return nil
}

// SupportsACP checks if an agent supports ACP (Agent Communication Protocol).
// Returns true if the agent has ACP configured.
func SupportsACP(agentName string) bool {
	info := GetAgentPresetByName(agentName)
	if info == nil {
		return false
	}
	if info.ACP != nil && info.ACP.Command != "" {
		return true
	}
	// Fallback: check if the command itself supports ACP
	return ResolveACPConfig(agentName, info.Command) != nil
}

// GetACPConfig returns the ACP configuration for an agent.
// Returns nil if the agent doesn't support ACP.
func GetACPConfig(agentName string) *ACPConfig {
	info := GetAgentPresetByName(agentName)
	if info == nil {
		return nil
	}
	return ResolveACPConfig(agentName, info.Command)
}

// GetACPCommand returns the ACP subcommand for an agent.
// Returns empty string if the agent doesn't support ACP.
func GetACPCommand(agentName string) string {
	config := GetACPConfig(agentName)
	if config == nil {
		return ""
	}
	return config.Command
}

// GetACPArgs returns the ACP arguments for an agent.
// Returns nil if the agent doesn't support ACP.
func GetACPArgs(agentName string) []string {
	config := GetACPConfig(agentName)
	if config == nil {
		return nil
	}
	return config.Args
}

// RuntimeConfigSupportsACP checks if a RuntimeConfig supports ACP.
// This is used for custom agents defined in config.json that may have
// their own ACP configuration or inherit from a preset.
// Returns true if the RuntimeConfig has ACP configured.
//
// ACP can be configured in three ways:
//  1. Native mode: { "command": "claude-agent-acp", "acp": { "mode": "native" } }
//     The binary is already an ACP adapter, no transformation needed.
//  2. Subcommand pattern: { "command": "opencode", "acp": { "command": "acp" } }
//     Results in: opencode acp
//  3. Flag pattern: { "command": "gemini", "acp": { "args": ["--acp"] } }
//     Results in: gemini --acp
func RuntimeConfigSupportsACP(rc *RuntimeConfig) bool {
	if rc == nil {
		return false
	}
	// If the RuntimeConfig has explicit ACP config, use it
	if rc.ACP != nil {
		// Native mode: the binary is already an ACP adapter
		if rc.ACP.Mode == ACPModeNative {
			return true
		}
		// Subcommand mode: has a subcommand to invoke ACP
		if rc.ACP.Command != "" {
			return true
		}
		// Flag mode: has flags to enable ACP
		if len(rc.ACP.Args) > 0 {
			return true
		}
	}
	// Fallback: check if the command matches a preset with ACP support
	if rc.Command != "" {
		return ResolveACPConfig("", rc.Command) != nil
	}
	return false
}

// GetACPConfigFromRuntime returns the ACP configuration from a RuntimeConfig.
// This is used for custom agents defined in config.json that may have
// their own ACP configuration or inherit from a preset.
// Returns nil if the RuntimeConfig doesn't support ACP.
//
// ACP can be configured in three ways:
//  1. Native mode: { "command": "claude-agent-acp", "acp": { "mode": "native" } }
//     The binary is already an ACP adapter, no transformation needed.
//  2. Subcommand pattern: { "command": "opencode", "acp": { "command": "acp" } }
//     Results in: opencode acp
//  3. Flag pattern: { "command": "gemini", "acp": { "args": ["--experimental-acp"] } }
//     Results in: gemini --experimental-acp
func GetACPConfigFromRuntime(rc *RuntimeConfig) *ACPConfig {
	if rc == nil {
		return nil
	}
	// If the RuntimeConfig has explicit ACP config, use it
	if rc.ACP != nil {
		// Native mode: the binary is already an ACP adapter
		if rc.ACP.Mode == ACPModeNative {
			return rc.ACP
		}
		// Subcommand mode: has a subcommand to invoke ACP
		if rc.ACP.Command != "" {
			return rc.ACP
		}
		// Flag mode: has flags to enable ACP
		if len(rc.ACP.Args) > 0 {
			return rc.ACP
		}
	}
	// Fallback: check if the command matches a preset with ACP support
	if rc.Command != "" {
		return ResolveACPConfig("", rc.Command)
	}
	return nil
}
