// Package templates provides embedded templates for role contexts and messages.
package templates

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"text/template"

	"github.com/jonbaldie/gastown/internal/instructions"
	"github.com/jonbaldie/gastown/internal/templates/commands"
)

var (
	cmdName     string
	cmdNameOnce sync.Once
)

// CmdName returns the Gas Town CLI command name.
// Defaults to "gt", but can be overridden with GT_COMMAND env var.
// This allows coexistence with other tools that use "gt" (e.g., Graphite).
func CmdName() string {
	cmdNameOnce.Do(func() {
		cmdName = os.Getenv("GT_COMMAND")
		if cmdName == "" {
			cmdName = "gt"
		}
	})
	return cmdName
}

// templateFuncs provides custom functions for templates.
var templateFuncs = template.FuncMap{
	"cmd": CmdName, // {{ cmd }} returns the CLI command name
}

//go:embed roles/*.md.tmpl messages/*.md.tmpl
var templateFS embed.FS

//go:embed launchd/*.plist systemd/*.service
var supervisorFS embed.FS

//go:embed polecat-CLAUDE.md
var polecatAgentsMD string

// Templates manages role and message templates.
type Templates struct {
	roleTemplates    *template.Template
	messageTemplates *template.Template
}

// RoleData contains information for rendering role contexts.
type RoleData struct {
	Role          string   // mayor, witness, refinery, polecat, crew, deacon
	RigName       string   // e.g., "greenplace"
	TownRoot      string   // e.g., "/Users/steve/ai"
	TownName      string   // e.g., "ai" - the town identifier for session names
	WorkDir       string   // current working directory
	DefaultBranch string   // default branch for merges (e.g., "main", "develop")
	IsForkRig     bool     // true when rig config has upstream_url
	UpstreamURL   string   // redacted upstream URL for display only
	Polecat       string   // polecat name (for polecat role)
	Polecats      []string // list of polecats (for witness role)
	DogName       string   // dog name (for dog role)
	BeadsDir      string   // BEADS_DIR path
	IssuePrefix   string   // beads issue prefix
	MayorSession  string   // e.g., "gt-ai-mayor" - dynamic mayor session name
	DeaconSession string   // e.g., "gt-ai-deacon" - dynamic deacon session name
}

// SpawnData contains information for spawn assignment messages.
type SpawnData struct {
	Issue       string
	Title       string
	Priority    int
	Description string
	Branch      string
	RigName     string
	Polecat     string
}

// NudgeData contains information for nudge messages.
type NudgeData struct {
	Polecat    string
	Reason     string
	NudgeCount int
	MaxNudges  int
	Issue      string
	Status     string
}

// EscalationData contains information for escalation messages.
type EscalationData struct {
	Polecat     string
	Issue       string
	Reason      string
	NudgeCount  int
	LastStatus  string
	Suggestions []string
}

// HandoffData contains information for session handoff messages.
type HandoffData struct {
	Role        string
	CurrentWork string
	Status      string
	NextSteps   []string
	Notes       string
	PendingMail int
	GitBranch   string
	GitDirty    bool
}

// SupervisorData contains information for rendering supervisor templates.
type SupervisorData struct {
	GTPath   string // Path to the gt binary
	TownRoot string // Path to the Gas Town workspace
}

// New creates a new Templates instance.
func New() (*Templates, error) {
	t := &Templates{}

	// Parse role templates with custom functions
	roleTempl, err := template.New("").Funcs(templateFuncs).ParseFS(templateFS, "roles/*.md.tmpl")
	if err != nil {
		return nil, fmt.Errorf("parsing role templates: %w", err)
	}
	t.roleTemplates = roleTempl

	// Parse message templates with custom functions
	msgTempl, err := template.New("").Funcs(templateFuncs).ParseFS(templateFS, "messages/*.md.tmpl")
	if err != nil {
		return nil, fmt.Errorf("parsing message templates: %w", err)
	}
	t.messageTemplates = msgTempl

	return t, nil
}

// RenderRole renders a role context template.
func (t *Templates) RenderRole(role string, data RoleData) (string, error) {
	templateName := role + ".md.tmpl"

	var buf bytes.Buffer
	if err := t.roleTemplates.ExecuteTemplate(&buf, templateName, data); err != nil {
		return "", fmt.Errorf("rendering role template %s: %w", templateName, err)
	}

	return buf.String(), nil
}

// RenderMessage renders a message template.
func (t *Templates) RenderMessage(name string, data interface{}) (string, error) {
	templateName := name + ".md.tmpl"

	var buf bytes.Buffer
	if err := t.messageTemplates.ExecuteTemplate(&buf, templateName, data); err != nil {
		return "", fmt.Errorf("rendering message template %s: %w", templateName, err)
	}

	return buf.String(), nil
}

// RoleNames returns the list of available role templates.
func (t *Templates) RoleNames() []string {
	return []string{"mayor", "witness", "refinery", "polecat", "crew", "deacon", "boot"}
}

// MessageNames returns the list of available message templates.
func (t *Templates) MessageNames() []string {
	return []string{"spawn", "nudge", "escalation", "handoff"}
}

// PolecatLifecycleMarker is a unique string present in the polecat overlay.
// Used to detect whether the Gas Town pair already contains lifecycle rules.
const PolecatLifecycleMarker = instructions.LifecycleMarker

// CreatePolecatAgentsMD writes polecat lifecycle rules through the shared
// instruction-file provisioner. AGENTS.md is the canonical file. CLAUDE.md is
// a symlink to it. A constitution file keeps the Rig file unchanged and uses
// the local pair instead.
func CreatePolecatAgentsMD(worktreePath, rigName, polecatName string) (bool, error) {
	content := polecatAgentsMD
	content = strings.ReplaceAll(content, "{{rig}}", rigName)
	content = strings.ReplaceAll(content, "{{name}}", polecatName)
	return instructions.Provision(worktreePath, content, PolecatLifecycleMarker)
}

// ProvisionCommands creates the .claude/commands/ directory with standard slash commands.
// This ensures crew/polecat workspaces have the handoff command and other utilities
// even if the source repo doesn't have them tracked.
// If a command already exists, it is skipped (no overwrite).
func ProvisionCommands(workspacePath string) error {
	return commands.ProvisionFor(workspacePath, "claude")
}

// ProvisionCommandsFor provisions commands for a specific agent.
func ProvisionCommandsFor(workspacePath, agent string) error {
	return commands.ProvisionFor(workspacePath, agent)
}

// CommandNames returns the list of embedded slash commands.
func CommandNames() []string {
	return commands.Names()
}

// HasCommands checks if a workspace has the .claude/commands/ directory provisioned.
func HasCommands(workspacePath string) bool {
	return HasCommandsFor(workspacePath, "claude")
}

// HasCommandsFor checks if a workspace has commands provisioned for an agent.
func HasCommandsFor(workspacePath, agent string) bool {
	return len(commands.MissingFor(workspacePath, agent)) == 0
}

// MissingCommands returns the list of embedded commands missing from the workspace.
func MissingCommands(workspacePath string) []string {
	return commands.MissingFor(workspacePath, "claude")
}

// MissingCommandsFor returns missing commands for a specific agent.
func MissingCommandsFor(workspacePath, agent string) []string {
	return commands.MissingFor(workspacePath, agent)
}

// ProvisionSupervisor creates and configures supervisor files for the daemon.
// On macOS: creates and loads a launchd plist.
// On Linux: creates and enables a systemd user unit.
// Returns a message indicating what action was taken (or skipped).
func ProvisionSupervisor(townRoot string) (string, error) {
	gtPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("finding gt executable: %w", err)
	}

	data := SupervisorData{
		GTPath:   gtPath,
		TownRoot: townRoot,
	}

	switch runtime.GOOS {
	case "darwin":
		return provisionLaunchd(data)
	case "linux":
		return provisionSystemd(data)
	default:
		return fmt.Sprintf("Supervisor auto-configuration skipped on %s (not supported yet)", runtime.GOOS), nil
	}
}

// provisionLaunchd creates and loads a launchd plist on macOS.
func provisionLaunchd(data SupervisorData) (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding home directory: %w", err)
	}

	agentsDir := filepath.Join(homeDir, "Library", "LaunchAgents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		return "", fmt.Errorf("creating LaunchAgents directory: %w", err)
	}

	plistPath := filepath.Join(agentsDir, "com.gastown.daemon.plist")

	// Read the template
	templateContent, err := supervisorFS.ReadFile("launchd/com.gastown.daemon.plist")
	if err != nil {
		return "", fmt.Errorf("reading launchd template: %w", err)
	}

	// Parse and execute template
	tmpl, err := template.New("launchd").Parse(string(templateContent))
	if err != nil {
		return "", fmt.Errorf("parsing launchd template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("rendering launchd template: %w", err)
	}

	// Write plist file
	if err := os.WriteFile(plistPath, buf.Bytes(), 0644); err != nil {
		return "", fmt.Errorf("writing plist file: %w", err)
	}

	// Unload if already loaded (ignore errors)
	_ = exec.Command("launchctl", "unload", plistPath).Run()

	// Load the service
	if output, err := exec.Command("launchctl", "load", plistPath).CombinedOutput(); err != nil {
		return "", fmt.Errorf("loading launchd service: %s", string(output))
	}

	return "Created and loaded launchd service: com.gastown.daemon", nil
}

// provisionSystemd creates and enables a systemd user unit on Linux.
func provisionSystemd(data SupervisorData) (string, error) {
	// Get XDG_DATA_HOME or use ~/.local/share
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("finding home directory: %w", err)
		}
		dataHome = filepath.Join(homeDir, ".local", "share")
	}

	systemdDir := filepath.Join(dataHome, "systemd", "user")
	if err := os.MkdirAll(systemdDir, 0755); err != nil {
		return "", fmt.Errorf("creating systemd user directory: %w", err)
	}

	servicePath := filepath.Join(systemdDir, "gastown-daemon.service")

	// Read the template
	templateContent, err := supervisorFS.ReadFile("systemd/gastown-daemon.service")
	if err != nil {
		return "", fmt.Errorf("reading systemd template: %w", err)
	}

	// Parse and execute template
	tmpl, err := template.New("systemd").Parse(string(templateContent))
	if err != nil {
		return "", fmt.Errorf("parsing systemd template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("rendering systemd template: %w", err)
	}

	// Write service file
	if err := os.WriteFile(servicePath, buf.Bytes(), 0644); err != nil {
		return "", fmt.Errorf("writing service file: %w", err)
	}

	// Reload systemd daemon
	if output, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return "", fmt.Errorf("reloading systemd: %s", string(output))
	}

	// Enable the service
	if output, err := exec.Command("systemctl", "--user", "enable", "gastown-daemon.service").CombinedOutput(); err != nil {
		return "", fmt.Errorf("enabling systemd service: %s", string(output))
	}

	// Start the service
	if output, err := exec.Command("systemctl", "--user", "start", "gastown-daemon.service").CombinedOutput(); err != nil {
		return "", fmt.Errorf("starting systemd service: %s", string(output))
	}

	return "Created and enabled systemd user service: gastown-daemon.service", nil
}
