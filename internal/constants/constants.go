// Package constants defines shared constant values used throughout Gas Town.
// Centralizing these magic strings improves maintainability and consistency.
package constants

import "time"

// Timing constants for session management and tmux operations.
//
// DEPRECATED as single source of truth: These constants are retained for
// backward compatibility. New code should use config.OperationalConfig
// accessors which support per-town overrides via settings/config.json.
// The compiled-in defaults in config/operational.go match these values.
const (
	// ShutdownNotifyDelay is the pause after sending shutdown notification.
	ShutdownNotifyDelay = 500 * time.Millisecond

	// ClaudeStartTimeout is how long to wait for Claude to start in a session.
	// 180s because the first turn must complete before ❯ appears: hooks fire
	// (gt prime injects patrol context), then the full API round-trip runs.
	// With large patrol formulas this regularly exceeds 60s, especially on Opus.
	// Configurable via operational.session.claude_start_timeout.
	ClaudeStartTimeout = 180 * time.Second

	// ShellReadyTimeout is how long to wait for shell prompt after command.
	// Configurable via operational.session.shell_ready_timeout.
	ShellReadyTimeout = 5 * time.Second

	// DefaultDebounceMs is the default debounce for SendKeys operations.
	DefaultDebounceMs = 500

	// DefaultDisplayMs is the default duration for tmux display-message.
	DefaultDisplayMs = 5000

	// PollInterval is the default polling interval for wait loops.
	PollInterval = 100 * time.Millisecond

	// ZombieKillGracePeriod is how long to wait after detecting a zombie
	// session before killing it, to mitigate TOCTOU races where a slow-
	// starting agent appears dead but is actually initializing.
	ZombieKillGracePeriod = 500 * time.Millisecond

	// GracefulShutdownTimeout is how long to wait after sending Ctrl-C before
	// forcefully killing a session.
	// Configurable via operational.session.graceful_shutdown_timeout.
	GracefulShutdownTimeout = 3 * time.Second

	// NudgeReadyTimeout is how long NudgeSession waits for the target pane to
	// accept input before giving up.
	// Configurable via operational.nudge.ready_timeout.
	NudgeReadyTimeout = 10 * time.Second

	// NudgeRetryInterval is the base interval between send-keys retry attempts.
	// Configurable via operational.nudge.retry_interval.
	NudgeRetryInterval = 500 * time.Millisecond

	// BdCommandTimeout is the default timeout for bd (beads CLI) command execution.
	// Configurable via operational.session.bd_command_timeout.
	BdCommandTimeout = 30 * time.Second

	// BdSubprocessTimeout is the timeout for bd subprocess calls in TUI panels.
	// Configurable via operational.session.bd_subprocess_timeout.
	BdSubprocessTimeout = 5 * time.Second

	// DialogPollInterval is the interval between pane content checks when
	// polling for startup dialogs (workspace trust, bypass permissions).
	DialogPollInterval = 500 * time.Millisecond

	// DialogPollTimeout is how long to poll for startup dialogs before giving up.
	// 8 seconds provides enough time for Claude to render dialogs on slow machines
	// while keeping startup fast when no dialog is present.
	DialogPollTimeout = 8 * time.Second

	// StartupNudgeVerifyDelay is how long to wait after sending a startup nudge
	// before checking if the agent started working. 25s because Claude may
	// still be processing gt prime output and preparing its first response;
	// the c2claude wrapper adds extra latency. 5s was consistently too short,
	// causing false retries that interrupted Claude mid-processing (GH#3031).
	// Configurable via operational.session.startup_nudge_verify_delay.
	StartupNudgeVerifyDelay = 25 * time.Second

	// StartupNudgeMaxRetries is the maximum number of times to retry a startup nudge.
	// With the 25s verify delay, 2 retries = 50s total before deferring to
	// witness zombie patrol. Reduced from 3 to limit interrupt risk (GH#3031).
	// Configurable via operational.session.startup_nudge_max_retries.
	StartupNudgeMaxRetries = 2

	// MinHandoffCooldown is the minimum time between handoffs for the same
	// component. Prevents tight restart loops when a patrol agent (e.g.,
	// witness) completes quickly on idle rigs and immediately hands off.
	// (gt-058d)
	// Configurable via operational.session.min_handoff_cooldown.
	MinHandoffCooldown = 2 * time.Minute

	// GUPPViolationTimeout is how long an agent can have work on hook without
	// progressing before it's considered a GUPP (Gas Town Universal Propulsion
	// Principle) violation. GUPP states: if you have work on your hook, you run it.
	//
	// Single source of truth — referenced by daemon lifecycle patrol,
	// TUI feed stuck detection, and web fetcher worker status.
	// Configurable via operational.session.gupp_violation_timeout.
	GUPPViolationTimeout = 30 * time.Minute

	// HungSessionThreshold is how long a tmux session can be inactive before
	// it's considered hung. Overridable per-role via RoleHealthConfig.
	// Configurable via operational.session.hung_session_threshold.
	HungSessionThreshold = 30 * time.Minute

	// MaxSessionProtectionDuration is the maximum time a session protection
	// lock remains valid. After this duration, the protection expires
	// automatically to prevent stale locks from blocking handoffs forever.
	MaxSessionProtectionDuration = 60 * time.Minute
)

// Directory names within a Gas Town workspace.
const (
	// DirMayor is the directory containing mayor configuration and state.
	DirMayor = "mayor"

	// DirPolecats is the directory containing polecat worktrees.
	DirPolecats = "polecats"

	// DirCrew is the directory containing crew workspaces.
	DirCrew = "crew"

	// DirRefinery is the directory containing the refinery clone.
	DirRefinery = "refinery"

	// DirWitness is the directory containing witness state.
	DirWitness = "witness"

	// DirRig is the subdirectory containing the actual git clone.
	DirRig = "rig"

	// DirBeads is the beads database directory.
	DirBeads = ".beads"

	// DirRuntime is the runtime state directory (gitignored).
	DirRuntime = ".runtime"

	// DirSettings is the rig settings directory (git-tracked).
	DirSettings = "settings"
)

// File names for configuration and state.
const (
	// FileRigsJSON is the rig registry file in mayor/.
	FileRigsJSON = "rigs.json"

	// FileTownJSON is the town configuration file in mayor/.
	FileTownJSON = "town.json"

	// FileConfigJSON is the general config file.
	FileConfigJSON = "config.json"

	// FileAccountsJSON is the accounts configuration file in mayor/.
	FileAccountsJSON = "accounts.json"

	// FileHandoffMarker is the marker file indicating a handoff just occurred.
	// Written by gt handoff before respawn, cleared by gt prime after detection.
	// This prevents the handoff loop bug where agents re-run /handoff from context.
	FileHandoffMarker = "handoff_to_successor"

	// FileLastHandoffTS records the timestamp of the last handoff.
	// Used to enforce MinHandoffCooldown and prevent tight restart loops.
	// (gt-058d)
	FileLastHandoffTS = "last_handoff_ts"

	// FileQuotaJSON is the quota state file in mayor/.
	FileQuotaJSON = "quota.json"
)

// Beads configuration constants.
const (
	// BeadsCustomTypes is the comma-separated list of custom issue types that
	// Gas Town registers with beads. These types were extracted from beads core
	// in v0.46.0 and now require explicit configuration.
	//
	// Type origins:
	//   agent         - Agent identity beads (gt install, rig init)
	//   role          - Agent role definitions (gt doctor role checks)
	//   rig           - Rig identity beads (gt rig init)
	//   convoy        - Cross-project work tracking
	//   slot          - Exclusive access / merge slots
	//   queue         - Message queue routing (gt mail queue)
	//   event         - Session/cost events (gt costs record)
	//   message       - Mail system (gt mail send, mailbox, router)
	//   molecule      - Work decomposition (patrol checks, gt swarm)
	//   gate          - Async coordination (bd gate wait, park/resume)
	//   merge-request - Refinery MR processing (gt done, refinery)
	BeadsCustomTypes = "agent,role,rig,convoy,slot,queue,event,message,molecule,gate,merge-request"
)

// BeadsCustomTypesList returns the custom types as a slice.
func BeadsCustomTypesList() []string {
	return []string{"agent", "role", "rig", "convoy", "slot", "queue", "event", "message", "molecule", "gate", "merge-request"}
}

// Beads custom status configuration constants.
const (
	// BeadsCustomStatuses is the comma-separated list of custom issue statuses
	// that Gas Town registers with beads. Convoy staging uses staged_ready and
	// staged_warnings to track convoy readiness before launch.
	//
	// Status origins:
	//   staged_ready    - Convoy staged with no warnings (ready to launch)
	//   staged_warnings - Convoy staged with warnings (requires --force to launch)
	BeadsCustomStatuses = "staged_ready,staged_warnings"
)

// BeadsCustomStatusesList returns the custom statuses as a slice.
func BeadsCustomStatusesList() []string {
	return []string{"staged_ready", "staged_warnings"}
}

// Git branch names.
const (
	// BranchMain is the default main branch name.
	BranchMain = "main"

	// BranchBeadsSync is the branch used for beads synchronization.
	BranchBeadsSync = "beads-sync"

	// BranchPolecatPrefix is the prefix for polecat work branches.
	BranchPolecatPrefix = "polecat/"

	// BranchIntegrationPrefix is the prefix for integration branches.
	BranchIntegrationPrefix = "integration/"
)

// Tmux session names.
// Mayor and Deacon use hq- prefix: hq-mayor, hq-deacon (town-level, one per machine).
// Rig-level services use gt- prefix: gt-<rig>-witness, gt-<rig>-refinery, etc.
// Use session.MayorSessionName() and session.DeaconSessionName().
const (
	// SessionPrefix is the prefix for rig-level Gas Town tmux sessions.
	SessionPrefix = "gt-"

	// HQSessionPrefix is the prefix for town-level services (Mayor, Deacon).
	HQSessionPrefix = "hq-"
)

// Agent role names.
const (
	// RoleMayor is the mayor agent role.
	RoleMayor = "mayor"

	// RoleWitness is the witness agent role.
	RoleWitness = "witness"

	// RoleRefinery is the refinery agent role.
	RoleRefinery = "refinery"

	// RolePolecat is the polecat agent role.
	RolePolecat = "polecat"

	// RoleCrew is the crew agent role.
	RoleCrew = "crew"

	// RoleDeacon is the deacon agent role.
	RoleDeacon = "deacon"

	// RoleBoot is the boot watchdog role (modeled as a deacon dog).
	RoleBoot = "boot"
)

// Role emojis - centralized for easy customization.
// These match the Gas Town visual identity (see ~/Desktop/Gas Town/ prompts).
const (
	// EmojiMayor is the mayor emoji (fox conductor).
	EmojiMayor = "🎩"

	// EmojiDeacon is the deacon emoji (wolf in the engine room).
	EmojiDeacon = "🐺"

	// EmojiWitness is the witness emoji (watchful owl).
	EmojiWitness = "🦉"

	// EmojiRefinery is the refinery emoji (industrial).
	EmojiRefinery = "🏭"

	// EmojiCrew is the crew emoji (established worker).
	EmojiCrew = "👷"

	// EmojiPolecat is the polecat emoji (transient worker).
	EmojiPolecat = "😺"

	// EmojiBoot is the boot watchdog emoji (dog).
	EmojiBoot = "🐾"
)

// Molecule formula names for patrol and dog workflows.
// These are used as formula identifiers in `bd mol wisp <name>` commands
// and to match active patrol wisps by title prefix.
const (
	// MolDeaconPatrol is the deacon patrol formula name.
	MolDeaconPatrol = "mol-deacon-patrol"

	// MolWitnessPatrol is the witness patrol formula name.
	MolWitnessPatrol = "mol-witness-patrol"

	// MolRefineryPatrol is the refinery patrol formula name.
	MolRefineryPatrol = "mol-refinery-patrol"

	// MolDogReaper is the wisp reaper dog formula name.
	MolDogReaper = "mol-dog-reaper"

	// MolDogJSONL is the JSONL git backup dog formula name.
	MolDogJSONL = "mol-dog-jsonl"

	// MolDogCompactor is the Dolt compactor dog formula name.
	MolDogCompactor = "mol-dog-compactor"

	// MolDogCheckpoint is the WIP checkpoint dog formula name.
	MolDogCheckpoint = "mol-dog-checkpoint"

	// MolDogDoctor is the health anomaly tracking dog formula name.
	MolDogDoctor = "mol-dog-doctor"

	// MolDogBackup is the Dolt backup dog formula name.
	MolDogBackup = "mol-dog-backup"

	// MolConvoyFeed is the convoy feeder formula name.
	MolConvoyFeed = "mol-convoy-feed"

	// MolConvoyCleanup is the convoy cleanup formula name.
	MolConvoyCleanup = "mol-convoy-cleanup"
)

// PatrolFormulas returns the list of patrol formula names.
func PatrolFormulas() []string {
	return []string{MolDeaconPatrol, MolWitnessPatrol, MolRefineryPatrol}
}

// RoleEmoji returns the emoji for a given role name.
func RoleEmoji(role string) string {
	switch role {
	case RoleMayor:
		return EmojiMayor
	case RoleDeacon:
		return EmojiDeacon
	case RoleWitness:
		return EmojiWitness
	case RoleRefinery:
		return EmojiRefinery
	case RoleCrew:
		return EmojiCrew
	case RolePolecat:
		return EmojiPolecat
	case RoleBoot:
		return EmojiBoot
	default:
		return "❓"
	}
}

// SupportedShells lists shell binaries that Gas Town can detect and work with.
// Used to identify if a tmux pane is at a shell prompt vs running a command.
var SupportedShells = []string{"bash", "zsh", "sh", "fish", "tcsh", "ksh", "pwsh", "powershell"}

// Path helpers construct common paths.

// MayorRigsPath returns the path to rigs.json within a town root.
func MayorRigsPath(townRoot string) string {
	return townRoot + "/" + DirMayor + "/" + FileRigsJSON
}

// MayorTownPath returns the path to town.json within a town root.
func MayorTownPath(townRoot string) string {
	return townRoot + "/" + DirMayor + "/" + FileTownJSON
}

// RigMayorPath returns the path to mayor/rig within a rig.
func RigMayorPath(rigPath string) string {
	return rigPath + "/" + DirMayor + "/" + DirRig
}

// RigBeadsPath returns the path to mayor/rig/.beads within a rig.
func RigBeadsPath(rigPath string) string {
	return rigPath + "/" + DirMayor + "/" + DirRig + "/" + DirBeads
}

// RigPolecatsPath returns the path to polecats/ within a rig.
func RigPolecatsPath(rigPath string) string {
	return rigPath + "/" + DirPolecats
}

// RigCrewPath returns the path to crew/ within a rig.
func RigCrewPath(rigPath string) string {
	return rigPath + "/" + DirCrew
}

// MayorConfigPath returns the path to mayor/config.json within a town root.
func MayorConfigPath(townRoot string) string {
	return townRoot + "/" + DirMayor + "/" + FileConfigJSON
}

// TownRuntimePath returns the path to .runtime/ at the town root.
func TownRuntimePath(townRoot string) string {
	return townRoot + "/" + DirRuntime
}

// RigRuntimePath returns the path to .runtime/ within a rig.
func RigRuntimePath(rigPath string) string {
	return rigPath + "/" + DirRuntime
}

// RigSettingsPath returns the path to settings/ within a rig.
func RigSettingsPath(rigPath string) string {
	return rigPath + "/" + DirSettings
}

// MayorAccountsPath returns the path to mayor/accounts.json within a town root.
func MayorAccountsPath(townRoot string) string {
	return townRoot + "/" + DirMayor + "/" + FileAccountsJSON
}

// MayorQuotaPath returns the path to mayor/quota.json within a town root.
func MayorQuotaPath(townRoot string) string {
	return townRoot + "/" + DirMayor + "/" + FileQuotaJSON
}

// DefaultRateLimitPatterns are the default patterns that indicate a session
// is rate-limited. These are matched against tmux pane content.
// Note: patterns are compiled with (?i) for case-insensitive matching.
// Patterns are intentionally specific to actual Claude rate-limit messages
// to avoid false positives from agent discussion or code comments.
var DefaultRateLimitPatterns = []string{
	`You've hit your .*limit`,                        // Claude's primary rate-limit message
	`limit\s*·\s*resets \d+[:\d]*(am|pm)\b`,         // "limit · resets 7pm" — requires limit context before resets
	`Stop and wait for limit to reset`,               // /rate-limit-options TUI prompt option 1
	`Add funds to continue with extra usage`,         // /rate-limit-options TUI prompt option 2
	`API Error: Rate limit reached`,                  // Mid-stream API 429 during tool use or generation
	`OAuth token revoked`,                            // Token invalidated after keychain swap
	`OAuth token has expired`,                        // Token expired — needs fresh auth
}

// DefaultNearLimitPatterns are patterns that indicate a session is approaching
// its rate limit but hasn't hit it yet. These enable proactive rotation before
// the hard 429. Matched with (?i) for case-insensitive matching.
var DefaultNearLimitPatterns = []string{
	`\d{2,3}%\s*(of\s*)?(your\s*)?(daily\s*)?(usage|limit|quota)`, // "80% of your daily usage"
	`usage\s+(is\s+)?(at|near|approaching)\s+\d+\s*%`,             // "usage is at 90%"
	`approaching\s+(your\s+)?(rate\s+)?limit`,                     // "approaching your rate limit"
	`nearing\s+(your\s+)?(rate\s+)?limit`,                         // "nearing your rate limit"
	`close\s+to\s+(your\s+)?(rate\s+)?limit`,                     // "close to your rate limit"
	`almost\s+(at|hit|reached)\s+(your\s+)?(rate\s+)?limit`,       // "almost reached your rate limit"
	`\d+\s*(messages?|requests?)\s*(left|remaining)`,               // "10 messages remaining"
}

