package web

import (
	"fmt"
	"regexp"
	"strings"
)

// CommandMeta describes a command's properties for the dashboard.
type CommandMeta struct {
	// Safe commands can run without user confirmation
	Safe bool
	// Confirm commands require user confirmation before execution
	Confirm bool
	// Desc is a short description shown in the command palette
	Desc string
	// Category groups commands in the palette UI
	Category string
	// Args is a placeholder hint for required arguments (e.g., "<convoy-id>")
	Args string
	// ArgType specifies what kind of options to show (rigs, polecats, convoys, agents, hooks)
	ArgType string
}

// AllowedCommands defines which gt commands can be executed from the dashboard.
// Commands not in this list are blocked for security.
var AllowedCommands = map[string]CommandMeta{
	// === Read-only commands (always safe) ===
	"status":      {Safe: true, Desc: "Show town status", Category: "Status"},
	"agents list": {Safe: true, Desc: "List active agents", Category: "Status"},
	"convoy list": {Safe: true, Desc: "List convoys", Category: "Convoys"},
	"convoy show":   {Safe: true, Desc: "Show convoy details", Category: "Convoys", Args: "<convoy-id>", ArgType: "convoys"},
	"convoy status": {Safe: true, Desc: "Show convoy status with tracked issues", Category: "Convoys", Args: "<convoy-id> --json", ArgType: "convoys"},
	"mail inbox":  {Safe: true, Desc: "Check inbox", Category: "Mail"},
	"mail check":  {Safe: true, Desc: "Check for new mail", Category: "Mail"},
	"mail peek":   {Safe: true, Desc: "Peek at message", Category: "Mail", Args: "<message-id>"},
	"rig list":    {Safe: true, Desc: "List rigs", Category: "Rigs"},
	"rig show":    {Safe: true, Desc: "Show rig details", Category: "Rigs", Args: "<rig-name>", ArgType: "rigs"},
	"doctor":      {Safe: true, Desc: "Health check", Category: "Diagnostics"},
	"hooks list":  {Safe: true, Desc: "List hooks", Category: "Hooks"},
	"activity":    {Safe: true, Desc: "Show recent activity", Category: "Status"},
	"info":        {Safe: true, Desc: "Show workspace info", Category: "Status"},
	"log":         {Safe: true, Desc: "View logs", Category: "Diagnostics"},
	"audit":       {Safe: true, Desc: "View audit log", Category: "Diagnostics"},

	// Polecat read-only
	"polecat list --all": {Safe: true, Desc: "List all polecats", Category: "Polecats"},
	"polecat show":       {Safe: true, Desc: "Show polecat details", Category: "Polecats", Args: "<rig>/<name>", ArgType: "polecats"},

	// Crew read-only
	"crew list --all": {Safe: true, Desc: "List all crew members", Category: "Crew"},
	"crew show":       {Safe: true, Desc: "Show crew details", Category: "Crew", Args: "<rig>/<name>", ArgType: "crew"},

	// === Action commands (require confirmation) ===

	// Mail actions
	"mail send":      {Confirm: true, Desc: "Send message", Category: "Mail", Args: "<address> -s <subject> -m <message>", ArgType: "agents"},
	"mail mark-read": {Confirm: false, Desc: "Mark as read", Category: "Mail", Args: "<message-id>", ArgType: "messages"},
	"mail archive":   {Confirm: false, Desc: "Archive message", Category: "Mail", Args: "<message-id>", ArgType: "messages"},
	"mail reply":     {Confirm: true, Desc: "Reply to message", Category: "Mail", Args: "<message-id> -m <message>", ArgType: "messages"},

	// Escalation actions
	"escalate ack":      {Confirm: true, Desc: "Acknowledge escalation", Category: "Escalations", Args: "<escalation-id>", ArgType: "escalations"},
	"escalate close":    {Confirm: true, Desc: "Close escalation", Category: "Escalations", Args: "<escalation-id>", ArgType: "escalations"},
	"escalate reassign": {Confirm: true, Desc: "Reassign escalation", Category: "Escalations", Args: "<escalation-id> <agent>", ArgType: "escalations"},

	// Convoy actions
	"convoy create":  {Confirm: true, Desc: "Create convoy", Category: "Convoys", Args: "<name>"},
	"convoy refresh": {Confirm: false, Desc: "Refresh convoy", Category: "Convoys", Args: "<convoy-id>", ArgType: "convoys"},
	"convoy add":     {Confirm: true, Desc: "Add issue to convoy", Category: "Convoys", Args: "<convoy-id> <issue>", ArgType: "convoys"},

	// Rig actions
	"rig add":   {Confirm: true, Desc: "Add rig", Category: "Rigs", Args: "<rig-name> <git-url>"},
	"rig boot":  {Confirm: true, Desc: "Boot rig", Category: "Rigs", Args: "<rig-name>", ArgType: "rigs"},
	"rig start": {Confirm: true, Desc: "Start rig", Category: "Rigs", Args: "<rig-name>", ArgType: "rigs"},

	// Agent lifecycle (careful)
	"witness start":  {Confirm: true, Desc: "Start witness", Category: "Agents", Args: "<rig-name>", ArgType: "rigs"},
	"refinery start": {Confirm: true, Desc: "Start refinery", Category: "Agents", Args: "<rig-name>", ArgType: "rigs"},
	"mayor attach":   {Confirm: true, Desc: "Attach mayor", Category: "Agents"},
	"deacon start":   {Confirm: true, Desc: "Start deacon", Category: "Agents"},

	// Polecat actions
	"polecat add":    {Confirm: true, Desc: "Add polecat", Category: "Polecats", Args: "<rig> <name>", ArgType: "rigs"},
	"polecat remove": {Confirm: true, Desc: "Remove polecat", Category: "Polecats", Args: "<rig>/<name>", ArgType: "polecats"},

	// Work assignment
	"sling":       {Confirm: true, Desc: "Assign work to agent", Category: "Work", Args: "<bead> <rig>", ArgType: "hooks"},
	"unsling":     {Confirm: true, Desc: "Unassign work from agent", Category: "Work", Args: "<bead>", ArgType: "hooks"},
	"hook attach": {Confirm: true, Desc: "Attach hook", Category: "Hooks", Args: "<bead>", ArgType: "hooks"},
	"hook detach": {Confirm: true, Desc: "Detach hook", Category: "Hooks", Args: "<bead>", ArgType: "hooks"},

	// Notifications
	"notify":    {Confirm: true, Desc: "Send notification", Category: "Notifications", Args: "<message>"},
	"broadcast": {Confirm: true, Desc: "Broadcast message", Category: "Notifications", Args: "<message>"},
}

// BlockedPatterns are regex patterns for commands that should never run from the dashboard.
// These require terminal access for safety.
var BlockedPatterns = []*regexp.Regexp{
	regexp.MustCompile(`--force`),
	regexp.MustCompile(`--hard`),
	regexp.MustCompile(`\brm\b`),
	regexp.MustCompile(`\bdelete\b`),
	regexp.MustCompile(`\bkill\b`),
	regexp.MustCompile(`\bdestroy\b`),
	regexp.MustCompile(`\bpurge\b`),
	regexp.MustCompile(`\breset\b`),
	regexp.MustCompile(`\bclean\b`),
}

// ValidateCommand checks if a command is allowed to run from the dashboard.
// Returns the command metadata if allowed, or an error if blocked.
func ValidateCommand(rawCommand string) (*CommandMeta, error) {
	rawCommand = strings.TrimSpace(rawCommand)
	if rawCommand == "" {
		return nil, fmt.Errorf("empty command")
	}

	// Check blocked patterns first
	for _, pattern := range BlockedPatterns {
		if pattern.MatchString(rawCommand) {
			return nil, fmt.Errorf("command contains blocked pattern: %s", pattern.String())
		}
	}

	// Extract base command (first 1-2 words) for whitelist lookup
	baseCmd := extractBaseCommand(rawCommand)

	meta, ok := AllowedCommands[baseCmd]
	if !ok {
		return nil, fmt.Errorf("command not in whitelist: %s", baseCmd)
	}

	return &meta, nil
}

// extractBaseCommand gets the command prefix for whitelist matching.
// "mail send foo bar" -> "mail send"
// "status --json" -> "status"
// "polecat list --all" -> "polecat list --all"
func extractBaseCommand(cmd string) string {
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return ""
	}

	// Try three-word command first (e.g., "polecat list --all")
	if len(parts) >= 3 {
		threeWord := parts[0] + " " + parts[1] + " " + parts[2]
		if _, ok := AllowedCommands[threeWord]; ok {
			return threeWord
		}
	}

	// Try two-word command (e.g., "convoy list")
	if len(parts) >= 2 {
		twoWord := parts[0] + " " + parts[1]
		if _, ok := AllowedCommands[twoWord]; ok {
			return twoWord
		}
	}

	// Fall back to single-word command
	return parts[0]
}

// SanitizeArgs removes potentially dangerous characters from command arguments.
// This is a defense-in-depth measure; the whitelist is the primary protection.
func SanitizeArgs(args []string) []string {
	sanitized := make([]string, 0, len(args))
	for _, arg := range args {
		// Remove shell metacharacters
		clean := strings.Map(func(r rune) rune {
			switch r {
			case ';', '|', '&', '$', '`', '(', ')', '{', '}', '<', '>', '\n', '\r':
				return -1 // Remove character
			default:
				return r
			}
		}, arg)
		if clean != "" {
			sanitized = append(sanitized, clean)
		}
	}
	return sanitized
}

// GetCommandList returns all allowed commands for the command palette UI.
func GetCommandList() []CommandInfo {
	commands := make([]CommandInfo, 0, len(AllowedCommands))
	for name, meta := range AllowedCommands {
		commands = append(commands, CommandInfo{
			Name:     name,
			Desc:     meta.Desc,
			Category: meta.Category,
			Safe:     meta.Safe,
			Confirm:  meta.Confirm,
			Args:     meta.Args,
			ArgType:  meta.ArgType,
		})
	}
	return commands
}

// CommandInfo is the JSON-serializable form of a command for the UI.
type CommandInfo struct {
	Name     string `json:"name"`
	Desc     string `json:"desc"`
	Category string `json:"category"`
	Safe     bool   `json:"safe"`
	Confirm  bool   `json:"confirm"`
	Args     string `json:"args,omitempty"`
	ArgType  string `json:"argType,omitempty"`
}
