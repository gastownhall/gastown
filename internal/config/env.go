// Package config provides configuration loading and environment variable management.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/steveyegge/gastown/internal/constants"
)

// IdentityEnvVars are agent identity env vars that must not leak across
// process or session boundaries. Used by daemon sanitization (clearing
// inherited vars), tmux global cleanup, and prime session env repair.
// See GH#3006.
var IdentityEnvVars = []string{
	"GT_ROLE", "GT_RIG", "GT_CREW", "GT_POLECAT", "GT_DOG_NAME",
	"GT_SESSION", "GT_AGENT", "BD_ACTOR", "GIT_AUTHOR_NAME", "BEADS_AGENT_NAME",
}

// OpenCodeOverrideEnvVars configure the native OpenCode server worker and must
// survive tmux startup, daemon restarts, and self-handoffs.
var OpenCodeOverrideEnvVars = []string{
	"GT_OPENCODE_COMMAND",
	"GT_OPENCODE_AGENT",
	"GT_OPENCODE_MODEL",
	"GT_OPENCODE_VARIANT",
}

var bdTargetSelectorEnvVars = []string{
	"BEADS_DIR",
	"BEADS_DB",
	"BD_DB",
	"BEADS_SHARED_SERVER_DIR",
	"BEADS_DOLT_DATA_DIR",
	"BEADS_DOLT_DATABASE",
	"BEADS_DOLT_SERVER_DATABASE",
	"BEADS_DOLT_HOST",
	"BEADS_DOLT_SHARED_SERVER",
	"BEADS_DOLT_SERVER_MODE",
	"BEADS_DOLT_SERVER_SOCKET",
	"GT_DOLT_DATA",
}

// AgentEnvConfig specifies the configuration for generating agent environment variables.
// This is the single source of truth for all agent environment configuration.
type AgentEnvConfig struct {
	// Role is the agent role: mayor, deacon, witness, refinery, crew, polecat, dog, boot
	Role string

	// Rig is the rig name (empty for town-level agents like mayor/deacon)
	Rig string

	// AgentName is the specific agent name (empty for singletons like witness/refinery)
	// For polecats, this is the polecat name. For crew, this is the crew member name.
	AgentName string

	// TownRoot is the root of the Gas Town workspace.
	// Sets GT_ROOT environment variable.
	TownRoot string

	// RuntimeConfigDir is the optional CLAUDE_CONFIG_DIR path
	RuntimeConfigDir string

	// SessionIDEnv is the environment variable name that holds the session ID.
	// Sets GT_SESSION_ID_ENV so the runtime knows where to find the session ID.
	SessionIDEnv string

	// Agent is the agent override (e.g., "codex", "gemini").
	// If set, GT_AGENT is written to the tmux session table via SetEnvironment
	// so that IsAgentAlive and waitForPolecatReady can read it via GetEnvironment.
	// Without this, GetEnvironment returns empty (tmux show-environment reads the
	// session table, not the process env set via exec env in the startup command).
	Agent string

	// Prompt is the initial startup prompt/beacon given to the agent.
	// When set, the first line (truncated) is added as gt.prompt to OTEL_RESOURCE_ATTRIBUTES
	// so logs can be correlated to the specific task the agent was working on.
	Prompt string

	// Issue is the molecule/bead ID being worked (e.g., "gt-abc12").
	// Added as gt.issue to OTEL_RESOURCE_ATTRIBUTES for filtering by ticket.
	Issue string

	// Topic is the beacon topic describing why the session was started.
	// Examples: "assigned", "patrol", "start", "restart", "handoff".
	// Added as gt.topic to OTEL_RESOURCE_ATTRIBUTES for filtering by work type.
	Topic string

	// SessionName is the tmux session name for this agent (e.g., "hq-mayor", "gt-witness").
	// Added as gt.session to OTEL_RESOURCE_ATTRIBUTES so all Claude logs from a
	// single GT session can be correlated, and as GT_SESSION env var.
	SessionName string
}

// AgentEnv returns all environment variables for an agent based on the config.
// This is the single source of truth for agent environment variables.
func AgentEnv(cfg AgentEnvConfig) map[string]string {
	env := make(map[string]string)

	// Set role-specific variables
	// GT_ROLE is set in compound format (e.g., "beads/crew/jane") so that
	// beads can parse it without knowing about Gas Town role types.
	switch cfg.Role {
	case constants.RoleMayor:
		env["GT_ROLE"] = constants.RoleMayor
		env["BD_ACTOR"] = constants.RoleMayor
		env["GIT_AUTHOR_NAME"] = constants.RoleMayor

	case constants.RoleDeacon:
		env["GT_ROLE"] = constants.RoleDeacon
		env["BD_ACTOR"] = constants.RoleDeacon
		env["GIT_AUTHOR_NAME"] = constants.RoleDeacon

	case "boot":
		env["GT_ROLE"] = "deacon/boot"
		env["BD_ACTOR"] = "deacon-boot"
		env["GIT_AUTHOR_NAME"] = "boot"

	case constants.RoleWitness:
		env["GT_ROLE"] = fmt.Sprintf("%s/witness", cfg.Rig)
		env["GT_RIG"] = cfg.Rig
		env["BD_ACTOR"] = fmt.Sprintf("%s/witness", cfg.Rig)
		env["GIT_AUTHOR_NAME"] = fmt.Sprintf("%s/witness", cfg.Rig)

	case constants.RoleRefinery:
		env["GT_ROLE"] = fmt.Sprintf("%s/refinery", cfg.Rig)
		env["GT_RIG"] = cfg.Rig
		env["BD_ACTOR"] = fmt.Sprintf("%s/refinery", cfg.Rig)
		env["GIT_AUTHOR_NAME"] = fmt.Sprintf("%s/refinery", cfg.Rig)

	case constants.RolePolecat:
		env["GT_ROLE"] = fmt.Sprintf("%s/polecats/%s", cfg.Rig, cfg.AgentName)
		env["GT_RIG"] = cfg.Rig
		env["GT_POLECAT"] = cfg.AgentName
		env["BD_ACTOR"] = fmt.Sprintf("%s/polecats/%s", cfg.Rig, cfg.AgentName)
		env["GIT_AUTHOR_NAME"] = cfg.AgentName
		// Disable Dolt auto-commit for polecats. With branch-per-polecat,
		// individual commits are pointless — all changes merge at gt done time
		// via DOLT_MERGE. Without this, concurrent polecats cause manifest
		// contention leading to Dolt read-only mode (gt-5cc2p).
		env["BD_DOLT_AUTO_COMMIT"] = "off"

	case constants.RoleCrew:
		env["GT_ROLE"] = fmt.Sprintf("%s/crew/%s", cfg.Rig, cfg.AgentName)
		env["GT_RIG"] = cfg.Rig
		env["GT_CREW"] = cfg.AgentName
		env["BD_ACTOR"] = fmt.Sprintf("%s/crew/%s", cfg.Rig, cfg.AgentName)
		env["GIT_AUTHOR_NAME"] = cfg.AgentName

	case "dog":
		// Dogs are town-level workers with role_agents key "dog".
		// GT_ROLE must be set so startup command resolution can honor role_agents.dog.
		env["GT_ROLE"] = "dog"
		if cfg.AgentName != "" {
			env["GT_DOG_NAME"] = cfg.AgentName
			env["BD_ACTOR"] = fmt.Sprintf("deacon/dogs/%s", cfg.AgentName)
			env["GIT_AUTHOR_NAME"] = cfg.AgentName
		} else {
			env["BD_ACTOR"] = "dog"
			env["GIT_AUTHOR_NAME"] = "dog"
		}
	}

	// Only set GT_ROOT if provided
	// Empty values would override tmux session environment
	if cfg.TownRoot != "" {
		env["GT_ROOT"] = cfg.TownRoot
		// Prevent git from walking up to umbrella repo when running in rig worktrees.
		// This stops accidental commits to the umbrella when running git commands from
		// intermediate directories (e.g., polecats/) that don't have their own .git.
		env["GIT_CEILING_DIRECTORIES"] = cfg.TownRoot
	}

	// Set BEADS_AGENT_NAME for polecat/crew (uses same format as BD_ACTOR)
	if cfg.Role == constants.RolePolecat || cfg.Role == constants.RoleCrew {
		env["BEADS_AGENT_NAME"] = fmt.Sprintf("%s/%s", cfg.Rig, cfg.AgentName)
	}

	// Add optional runtime config directory
	if cfg.RuntimeConfigDir != "" {
		env["CLAUDE_CONFIG_DIR"] = cfg.RuntimeConfigDir
	}

	// Add session ID env var name if provided
	if cfg.SessionIDEnv != "" {
		env["GT_SESSION_ID_ENV"] = cfg.SessionIDEnv
	}

	// Set GT_SESSION when a session name is provided, so gt commands and
	// cost reports can correlate activity to a specific tmux session.
	if cfg.SessionName != "" {
		env["GT_SESSION"] = cfg.SessionName
	}

	// Set GT_AGENT when an agent override is in use.
	// This makes the override visible via tmux show-environment so that
	// IsAgentAlive and waitForPolecatReady use the correct process names.
	if cfg.Agent != "" {
		env["GT_AGENT"] = cfg.Agent
	}
	for _, name := range OpenCodeOverrideEnvVars {
		if value, ok := os.LookupEnv(name); ok {
			env[name] = value
		}
	}

	// Disable bd's per-repo JSONL auto-backup for all Gas Town agents.
	// bd auto-enables backup when a git remote exists, then force-adds
	// .beads/backup/ files (bypassing .gitignore) and commits/pushes them
	// to the project repo. In Gas Town, Dolt is the persistent data store
	// and the daemon provides centralized backup patrols (dolt_backup,
	// jsonl_git_backup), making per-repo backup redundant and harmful —
	// it pollutes rig git history on both main and feature branches.
	// See: https://github.com/steveyegge/beads/issues/2241
	env["BD_BACKUP_ENABLED"] = "false"

	// Clear NODE_OPTIONS to prevent debugger flags (e.g., --inspect from VSCode)
	// from being inherited through tmux into Claude's Node.js runtime.
	// This is the PRIMARY guard: setting it here (the single source of truth
	// for agent env) protects all AgentEnv-based paths automatically — tmux
	// SetEnvironment, EnvForExecCommand, PrependEnv. SanitizeAgentEnv provides
	// a SUPPLEMENTAL guard for non-AgentEnv paths (lifecycle default, handoff).
	// In BuildStartupCommand, rc.Env is merged after AgentEnv and can override
	// this empty value with intentional settings like --max-old-space-size.
	env["NODE_OPTIONS"] = ""

	// Resolve effort level from per-role config (role_effort in town/rig settings,
	// or cost-tier presets). Falls back to "high" when no config exists.
	// The CLAUDE_CODE_EFFORT_LEVEL env var is deprecated — effort is now configured
	// per-role through config, matching the pattern used for model selection.
	rigPath := ""
	if cfg.Rig != "" && cfg.TownRoot != "" {
		rigPath = filepath.Join(cfg.TownRoot, cfg.Rig)
	}
	effort := ResolveRoleEffort(cfg.Role, cfg.TownRoot, rigPath)
	if effort == "" {
		effort = "high"
	}
	env["CLAUDE_CODE_EFFORT_LEVEL"] = effort

	// Clear CLAUDECODE to prevent nested session detection in Claude Code v2.x.
	// When gt sling is invoked from within a Claude Code session, CLAUDECODE=1
	// leaks through tmux's global environment into new polecat sessions, causing
	// Claude Code to refuse to start with a "nested sessions" error.
	// See: https://github.com/steveyegge/gastown/issues/1666
	env["CLAUDECODE"] = ""

	clearBDTargetSelectorEnv(env)

	// Propagate Claude Code's own OTEL telemetry when GT telemetry is enabled.
	// Reuses the same VictoriaMetrics endpoint as gastown's telemetry so all
	// metrics (gt + claude) land in the same store.
	// Opt-in: only active when GT_OTEL_METRICS_URL is explicitly set.
	if metricsURL := os.Getenv("GT_OTEL_METRICS_URL"); metricsURL != "" {
		env["CLAUDE_CODE_ENABLE_TELEMETRY"] = "1"
		env["OTEL_METRICS_EXPORTER"] = "otlp"
		env["OTEL_METRIC_EXPORT_INTERVAL"] = "1000"
		env["OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"] = metricsURL
		// VictoriaMetrics rejects JSON encoding ("json encoding isn't supported
		// for opentelemetry format"). The Node.js OTEL SDK defaults to JSON;
		// force protobuf so the push succeeds.
		env["OTEL_EXPORTER_OTLP_METRICS_PROTOCOL"] = "http/protobuf"
		// Mirror into bd's own var names so any `bd` call inside the Claude
		// session emits metrics/logs to the same VictoriaMetrics instance.
		env["BD_OTEL_METRICS_URL"] = metricsURL
		if logsURL := os.Getenv("GT_OTEL_LOGS_URL"); logsURL != "" {
			env["BD_OTEL_LOGS_URL"] = logsURL
			// Claude Code supports OTLP log export; route to the same VictoriaLogs
			// instance. Uses protobuf (VictoriaLogs rejects JSON).
			env["OTEL_LOGS_EXPORTER"] = "otlp"
			env["OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"] = logsURL
			env["OTEL_EXPORTER_OTLP_LOGS_PROTOCOL"] = "http/protobuf"
			// Log tool usage details (which tools ran, their status).
			env["OTEL_LOG_TOOL_DETAILS"] = "true"
			// Log tool output content (e.g. gt prime stdout as it reaches Claude).
			env["OTEL_LOG_TOOL_CONTENT"] = "true"
			// Log user-turn messages (initial beacon + any human prompts to Claude).
			env["OTEL_LOG_USER_PROMPTS"] = "true"
		}

		// Attach GT context as OTEL resource attributes so Claude's metrics
		// can be correlated with gastown's own telemetry in VictoriaMetrics.
		// Claude Code's Node.js SDK picks up OTEL_RESOURCE_ATTRIBUTES automatically.
		var attrs []string
		if v := env["GT_ROLE"]; v != "" {
			attrs = append(attrs, "gt.role="+v)
		}
		if cfg.Rig != "" {
			attrs = append(attrs, "gt.rig="+cfg.Rig)
		}
		if v := env["BD_ACTOR"]; v != "" {
			attrs = append(attrs, "gt.actor="+v)
		}
		if cfg.AgentName != "" {
			attrs = append(attrs, "gt.agent="+cfg.AgentName)
		}
		if cfg.TownRoot != "" {
			attrs = append(attrs, "gt.town="+filepath.Base(cfg.TownRoot))
		}
		if cfg.Prompt != "" {
			attrs = append(attrs, "gt.prompt="+sanitizeOTELAttrValue(cfg.Prompt, 120))
		}
		if cfg.Issue != "" {
			attrs = append(attrs, "gt.issue="+sanitizeOTELAttrValue(cfg.Issue, 40))
		}
		if cfg.Topic != "" {
			attrs = append(attrs, "gt.topic="+sanitizeOTELAttrValue(cfg.Topic, 40))
		}
		if cfg.SessionName != "" {
			attrs = append(attrs, "gt.session="+sanitizeOTELAttrValue(cfg.SessionName, 80))
		}
		if len(attrs) > 0 {
			env["OTEL_RESOURCE_ATTRIBUTES"] = strings.Join(attrs, ",")
		}
	}

	// Inject Dolt server endpoint so agents' direct bd invocations connect to
	// gt's central server instead of auto-starting rogue per-rig servers.
	// BEADS_DOLT_* values are output aliases only; they are never authoritative.
	if cfg.TownRoot != "" {
		if port := ResolveDoltPort(cfg.TownRoot); port > 0 {
			setDoltPortEnv(env, strconv.Itoa(port))
		}
	}
	if _, ok := env["GT_DOLT_PORT"]; !ok {
		if v := os.Getenv("GT_DOLT_PORT"); v != "" {
			setDoltPortEnv(env, v)
		}
	}
	// Suppress bd's Dolt auto-start for all Gas Town agents (GH#2930).
	// Gas Town manages its own Dolt server (gt dolt start/stop). When the
	// server is momentarily unreachable (restart, journal hiccup), bd's
	// auto-start tries to launch a shadow server in the agent's .beads/dolt/
	// directory — which conflicts with the real server on the same port and
	// triggers an escalation flood loop. Dogs are especially affected because
	// their kennel's .beads/ has no explicit dolt_server_port in metadata.json.
	if cfg.TownRoot != "" {
		env["BEADS_DOLT_AUTO_START"] = "0"
	}

	// Propagate Dolt server host. GT/config host is authoritative; stale Beads
	// aliases from the parent shell are intentionally ignored.
	if host := ResolveDoltHost(cfg.TownRoot); host != "" {
		env["GT_DOLT_HOST"] = host
		env["BEADS_DOLT_SERVER_HOST"] = host
	}

	// Pass through cloud API credentials and provider configuration from the parent shell.
	// Only variables explicitly listed here are forwarded; all others are blocked for isolation.
	for _, key := range []string{
		// Anthropic API (direct)
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
		// ANTHROPIC_BASE_URL intentionally excluded — agents that need a custom
		// base URL (MiniMax, Groq, etc.) get it from their agent config's Env
		// block, not from the parent process. Passthrough caused cross-provider
		// contamination: a MiniMax deacon's base URL leaked into Claude polecats.
		"ANTHROPIC_CUSTOM_HEADERS",

		// Model selection
		"ANTHROPIC_MODEL",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL",
		"ANTHROPIC_DEFAULT_SONNET_MODEL",
		"ANTHROPIC_DEFAULT_OPUS_MODEL",
		"CLAUDE_CODE_SUBAGENT_MODEL",

		// AWS Bedrock
		"CLAUDE_CODE_USE_BEDROCK",
		"CLAUDE_CODE_SKIP_BEDROCK_AUTH",
		"AWS_ACCESS_KEY_ID",
		"AWS_SECRET_ACCESS_KEY",
		"AWS_SESSION_TOKEN",
		"AWS_REGION",
		"AWS_PROFILE",
		"AWS_BEARER_TOKEN_BEDROCK",
		"ANTHROPIC_SMALL_FAST_MODEL_AWS_REGION",

		// Microsoft Foundry
		"CLAUDE_CODE_USE_FOUNDRY",
		"CLAUDE_CODE_SKIP_FOUNDRY_AUTH",
		"ANTHROPIC_FOUNDRY_API_KEY",
		"ANTHROPIC_FOUNDRY_BASE_URL",
		"ANTHROPIC_FOUNDRY_RESOURCE",

		// Google Vertex AI
		"CLAUDE_CODE_USE_VERTEX",
		"CLAUDE_CODE_SKIP_VERTEX_AUTH",
		"GOOGLE_APPLICATION_CREDENTIALS",
		"GOOGLE_CLOUD_PROJECT",
		"VERTEX_PROJECT",
		"VERTEX_LOCATION",
		"VERTEX_REGION_CLAUDE_3_5_HAIKU",
		"VERTEX_REGION_CLAUDE_3_7_SONNET",
		"VERTEX_REGION_CLAUDE_4_0_OPUS",
		"VERTEX_REGION_CLAUDE_4_0_SONNET",
		"VERTEX_REGION_CLAUDE_4_1_OPUS",

		// Proxy / network
		"HTTP_PROXY",
		"HTTPS_PROXY",
		"NO_PROXY",

		// mTLS
		"CLAUDE_CODE_CLIENT_CERT",
		"CLAUDE_CODE_CLIENT_KEY",
		"CLAUDE_CODE_CLIENT_KEY_PASSPHRASE",
	} {
		if val := os.Getenv(key); val != "" {
			env[key] = val
		}
	}

	return env
}

func setDoltPortEnv(env map[string]string, port string) {
	env["GT_DOLT_PORT"] = port
	env["BEADS_DOLT_SERVER_PORT"] = port
	env["BEADS_DOLT_PORT"] = port
}

func clearBDTargetSelectorEnv(env map[string]string) {
	for _, key := range bdTargetSelectorEnvVars {
		env[key] = ""
	}
}

// sanitizeOTELAttrValue prepares a string for use as a value in OTEL_RESOURCE_ATTRIBUTES.
// It takes the first line only, replaces commas (which break key=value,key=value parsing),
// and truncates to maxLen bytes.
func sanitizeOTELAttrValue(s string, maxLen int) string {
	// First line only — beacons are multi-line; the first line is the structured header.
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[:idx]
	}
	// Commas separate key=value pairs in OTEL_RESOURCE_ATTRIBUTES — replace with |.
	s = strings.ReplaceAll(s, ",", "|")
	s = strings.TrimSpace(s)
	if len(s) > maxLen {
		s = s[:maxLen]
	}
	return s
}

// ResolveDoltPort determines the Dolt server port for the given town root.
//
// Resolution order:
//  1. GT_DOLT_PORT environment variable (explicit operator intent)
//  2. .dolt-data/config.yaml listener.port
//  3. mayor/daemon.json env.GT_DOLT_PORT
//  4. 0 (caller should skip injection — DefaultPort 3307 remains the default)
func ResolveDoltPort(townRoot string) int {
	if port := resolveDoltPortFromEnv(); port > 0 {
		return port
	}
	if townRoot == "" {
		return 0
	}

	if port := resolveDoltPortFromConfigYAML(townRoot); port > 0 {
		return port
	}

	if port := resolveDoltPortFromDaemonJSON(townRoot); port > 0 {
		return port
	}

	return 0
}

// ResolveConfiguredDoltPort determines the durable configured Dolt port for
// initializing a target town. Unlike ResolveDoltPort, it does not consult
// ambient GT_DOLT_PORT until after the target town's managed config, which may
// be stale in long-lived agent sessions.
//
// Resolution order:
//  1. .dolt-data/config.yaml listener.port unless GT_DOLT_IGNORE_CONFIG=1
//  2. GT_DOLT_PORT environment variable
//  3. mayor/daemon.json env.GT_DOLT_PORT
//  4. 0 (caller should use its default)
func ResolveConfiguredDoltPort(townRoot string) int {
	if _, port, ok := ManagedDoltEndpoint(townRoot); ok {
		return port
	}
	if port := resolveDoltPortFromEnv(); port > 0 {
		return port
	}
	if port := resolveDoltPortFromDaemonJSON(townRoot); port > 0 {
		return port
	}
	return 0
}

// ResolveConfiguredDoltHost determines the durable configured Dolt host for
// initializing a target town. The target town's managed config beats ambient
// GT_DOLT_HOST, which may describe the caller's current town instead.
//
// Resolution order:
//  1. .dolt-data/config.yaml listener.host unless GT_DOLT_IGNORE_CONFIG=1
//  2. GT_DOLT_HOST environment variable
//  3. mayor/daemon.json env.GT_DOLT_HOST
//  4. "" (caller should use its default)
func ResolveConfiguredDoltHost(townRoot string) string {
	if host, _, ok := ManagedDoltEndpoint(townRoot); ok {
		return host
	}
	if host := strings.TrimSpace(os.Getenv("GT_DOLT_HOST")); host != "" {
		return host
	}
	return resolveDoltHostFromDaemonJSON(townRoot)
}

// ManagedDoltEndpoint reads the target town's managed Dolt config.yaml without
// falling back to ambient environment. The boolean reports whether the managed
// config exists and is not disabled by GT_DOLT_IGNORE_CONFIG.
func ManagedDoltEndpoint(townRoot string) (host string, port int, ok bool) {
	if townRoot == "" || os.Getenv("GT_DOLT_IGNORE_CONFIG") == "1" {
		return "", 0, false
	}
	configPath := filepath.Join(townRoot, ".dolt-data", "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", 0, false
	}
	return parseHostFromConfigYAML(data), parsePortFromConfigYAML(data), true
}

// NormalizeConfiguredDoltEnv strips inherited Dolt endpoint env at target-town
// boundaries and injects the target town's managed endpoint when present.
func NormalizeConfiguredDoltEnv(base []string, townRoot string) []string {
	host, port, ok := ManagedDoltEndpoint(townRoot)
	if !ok {
		return base
	}
	base = stripDoltEndpointEnv(base)
	if host != "" {
		base = append(base, "GT_DOLT_HOST="+host)
	}
	if port > 0 {
		base = append(base, "GT_DOLT_PORT="+strconv.Itoa(port))
	}
	return base
}

// ApplyConfiguredDoltEnv applies NormalizeConfiguredDoltEnv to the current
// process environment for target-town startup boundaries.
func ApplyConfiguredDoltEnv(townRoot string) {
	normalized := NormalizeConfiguredDoltEnv(os.Environ(), townRoot)
	for _, key := range []string{"GT_DOLT_HOST", "GT_DOLT_PORT", "BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT"} {
		_ = os.Unsetenv(key)
	}
	for _, entry := range normalized {
		key, value, ok := strings.Cut(entry, "=")
		if ok && isDoltEndpointEnvKey(key) {
			os.Setenv(key, value)
		}
	}
}

func stripDoltEndpointEnv(env []string) []string {
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if ok && isDoltEndpointEnvKey(key) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func isDoltEndpointEnvKey(key string) bool {
	for _, want := range []string{"GT_DOLT_HOST", "GT_DOLT_PORT", "BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT"} {
		if runtime.GOOS == "windows" {
			if strings.EqualFold(key, want) {
				return true
			}
			continue
		}
		if key == want {
			return true
		}
	}
	return false
}

func resolveDoltPort(townRoot string) int {
	return ResolveDoltPort(townRoot)
}

func resolveDoltPortFromEnv() int {
	if p := os.Getenv("GT_DOLT_PORT"); p != "" {
		if port, err := strconv.Atoi(p); err == nil && port > 0 {
			return port
		}
	}
	return 0
}

func resolveDoltPortFromConfigYAML(townRoot string) int {
	if townRoot == "" || os.Getenv("GT_DOLT_IGNORE_CONFIG") == "1" {
		return 0
	}
	configPath := filepath.Join(townRoot, ".dolt-data", "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return 0
	}
	return parsePortFromConfigYAML(data)
}

func resolveDoltPortFromDaemonJSON(townRoot string) int {
	if townRoot == "" {
		return 0
	}
	daemonJSONPath := filepath.Join(townRoot, "mayor", "daemon.json")
	data, err := os.ReadFile(daemonJSONPath)
	if err != nil {
		return 0
	}
	var daemonEnv struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(data, &daemonEnv); err != nil {
		return 0
	}
	if v, ok := daemonEnv.Env["GT_DOLT_PORT"]; ok {
		if port, err := strconv.Atoi(v); err == nil && port > 0 {
			return port
		}
	}
	return 0
}

// ResolveDoltHost determines the Dolt server host for the given town root.
// BEADS_DOLT_* aliases are derived outputs and are intentionally ignored.
//
// Resolution order:
//  1. GT_DOLT_HOST environment variable (explicit operator intent)
//  2. .dolt-data/config.yaml listener.host unless GT_DOLT_IGNORE_CONFIG=1
//  3. mayor/daemon.json env.GT_DOLT_HOST
//  4. "" (caller should use its default localhost behavior)
func ResolveDoltHost(townRoot string) string {
	if host := strings.TrimSpace(os.Getenv("GT_DOLT_HOST")); host != "" {
		return host
	}
	if townRoot == "" {
		return ""
	}
	if host := resolveDoltHostFromConfigYAML(townRoot); host != "" {
		return host
	}
	return resolveDoltHostFromDaemonJSON(townRoot)
}

func resolveDoltHostFromConfigYAML(townRoot string) string {
	if townRoot == "" || os.Getenv("GT_DOLT_IGNORE_CONFIG") == "1" {
		return ""
	}
	configPath := filepath.Join(townRoot, ".dolt-data", "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return ""
	}
	return parseHostFromConfigYAML(data)
}

func resolveDoltHostFromDaemonJSON(townRoot string) string {
	if townRoot == "" {
		return ""
	}
	daemonJSONPath := filepath.Join(townRoot, "mayor", "daemon.json")
	data, err := os.ReadFile(daemonJSONPath)
	if err != nil {
		return ""
	}
	var daemonEnv struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(data, &daemonEnv); err != nil {
		return ""
	}
	return strings.TrimSpace(daemonEnv.Env["GT_DOLT_HOST"])
}

// parsePortFromConfigYAML extracts the listener port from a Dolt config.yaml
// without a yaml dependency. The file is machine-generated by gt dolt start
// with the format:
//
//	listener:
//	  port: 3307
func parsePortFromConfigYAML(data []byte) int {
	lines := strings.Split(string(data), "\n")
	inListener := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "listener:" {
			inListener = true
			continue
		}
		if inListener {
			if strings.HasPrefix(trimmed, "port:") {
				portStr := strings.TrimSpace(strings.TrimPrefix(trimmed, "port:"))
				if port, err := strconv.Atoi(portStr); err == nil {
					return port
				}
			}
			// Any non-indented line ends the listener block
			if len(line) > 0 && line[0] != ' ' && line[0] != '\t' {
				inListener = false
			}
		}
	}
	return 0
}

func parseHostFromConfigYAML(data []byte) string {
	lines := strings.Split(string(data), "\n")
	inListener := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "listener:" {
			inListener = true
			continue
		}
		if inListener {
			if strings.HasPrefix(trimmed, "host:") {
				return strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "host:")), `"'`)
			}
			// Any non-indented line ends the listener block
			if len(line) > 0 && line[0] != ' ' && line[0] != '\t' {
				inListener = false
			}
		}
	}
	return ""
}

// AgentEnvSimple is a convenience function for simple role-based env var lookup.
// Use this when you only need role, rig, and agentName without advanced options.
func AgentEnvSimple(role, rig, agentName string) map[string]string {
	return AgentEnv(AgentEnvConfig{
		Role:      role,
		Rig:       rig,
		AgentName: agentName,
	})
}

// ShellQuote returns a shell-safe quoted string.
// Values containing special characters are wrapped in single quotes.
// Single quotes within the value are escaped using the '\” idiom.
func ShellQuote(s string) string {
	// Check if quoting is needed (contains shell special chars)
	needsQuoting := false
	for _, c := range s {
		switch c {
		case ' ', '\t', '\n', '"', '\'', '`', '$', '\\', '!', '*', '?',
			'[', ']', '{', '}', '(', ')', '<', '>', '|', '&', ';', '#':
			needsQuoting = true
		}
		if needsQuoting {
			break
		}
	}

	if !needsQuoting {
		return s
	}

	// Use single quotes, escaping any embedded single quotes
	// 'foo'\''bar' means: 'foo' + escaped-single-quote + 'bar'
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// psQuote quotes a value for use in PowerShell $env: assignments.
// Uses single quotes and doubles embedded single quotes.
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// ExportPrefix builds an export statement prefix for shell commands.
// Returns a string like "export GT_ROLE=mayor BD_ACTOR=mayor && "
// The keys are sorted for deterministic output.
// Values containing special characters are properly shell-quoted.
func ExportPrefix(env map[string]string) string {
	if len(env) == 0 {
		return ""
	}

	// Sort keys for deterministic output
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	if runtime.GOOS == "windows" {
		var parts []string
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("$env:%s=%s", k, psQuote(env[k])))
		}
		return strings.Join(parts, "; ") + "; "
	}

	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, ShellQuote(env[k])))
	}
	return "export " + strings.Join(parts, " ") + " && "
}

// BuildStartupCommandWithEnv builds a startup command with the given environment variables.
// This combines the export prefix with the agent command and optional prompt.
func BuildStartupCommandWithEnv(env map[string]string, agentCmd, prompt string) string {
	prefix := ExportPrefix(env)

	if prompt != "" {
		// Include prompt as argument to agent command
		return fmt.Sprintf("%s%s %q", prefix, agentCmd, prompt)
	}
	return prefix + agentCmd
}

// MergeEnv merges multiple environment maps, with later maps taking precedence.
func MergeEnv(maps ...map[string]string) map[string]string {
	result := make(map[string]string)
	for _, m := range maps {
		for k, v := range m {
			result[k] = v
		}
	}
	return result
}

// FilterEnv returns a new map with only the specified keys.
func FilterEnv(env map[string]string, keys ...string) map[string]string {
	result := make(map[string]string)
	for _, k := range keys {
		if v, ok := env[k]; ok {
			result[k] = v
		}
	}
	return result
}

// WithoutEnv returns a new map without the specified keys.
func WithoutEnv(env map[string]string, keys ...string) map[string]string {
	result := make(map[string]string)
	exclude := make(map[string]bool)
	for _, k := range keys {
		exclude[k] = true
	}
	for k, v := range env {
		if !exclude[k] {
			result[k] = v
		}
	}
	return result
}

// EnvForExecCommand returns os.Environ() with the given env vars appended.
// This is useful for setting cmd.Env on exec.Command.
func EnvForExecCommand(env map[string]string) []string {
	result := os.Environ()
	for k, v := range env {
		result = append(result, k+"="+v)
	}
	return result
}

// EnvToSlice converts an env map to a slice of "K=V" strings.
// Useful for appending to os.Environ() manually.
func EnvToSlice(env map[string]string) []string {
	result := make([]string, 0, len(env))
	for k, v := range env {
		result = append(result, k+"="+v)
	}
	return result
}

// ClaudeConfigDir resolves the Claude Code configuration directory.
// Resolution order:
//  1. CLAUDE_CONFIG_DIR env var (if set and non-empty)
//  2. $HOME/.claude (fallback)
func ClaudeConfigDir() (string, error) {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude"), nil
}
