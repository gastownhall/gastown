// Package web provides HTTP server and templates for the Gas Town dashboard.
package web

import (
	"embed"
	"html/template"
	"io/fs"
	"strings"

	"github.com/steveyegge/gastown/internal/activity"
)

//go:embed templates/*.html
var templateFS embed.FS

// ConvoyData represents data passed to the convoy template.
type ConvoyData struct {
	Convoys     []ConvoyRow
	MergeQueue  []MergeQueueRow
	Workers     []WorkerRow
	Mail        []MailRow
	Rigs        []RigRow
	Dogs        []DogRow
	Escalations []EscalationRow
	Health      *HealthRow
	Queues      []QueueRow
	Sessions    []SessionRow
	Hooks       []HookRow
	Mayor       *MayorStatus
	Issues      []IssueRow
	Activity    []ActivityRow
	Summary     *DashboardSummary
	Expand      string // Panel to show fullscreen (from ?expand=name)
	CSRFToken   string // Token for CSRF protection on POST requests
}

// RigRow represents a registered rig in the dashboard.
type RigRow struct {
	Name         string
	GitURL       string
	PolecatCount int
	CrewCount    int
	HasWitness   bool
	HasRefinery  bool
}

// DogStateDebris marks a kennel entry that occupies a name without being a
// valid dog (a directory without .dog.json, left behind by a failed Add).
const DogStateDebris = "debris"

// DogRow represents a Deacon helper worker.
type DogRow struct {
	Name       string // Dog name (e.g., "alpha")
	State      string // idle, working, debris
	Work       string // Current work assignment
	LastActive string // Formatted age (e.g., "5m ago")
	RigCount   int    // Number of worktrees
}

// EscalationRow represents an escalation needing attention.
type EscalationRow struct {
	ID          string
	Title       string
	Severity    string // critical, high, medium, low
	EscalatedBy string
	Age         string
	Acked       bool
}

// HealthRow represents system health status.
type HealthRow struct {
	DeaconHeartbeat string // Age of heartbeat (e.g., "2m ago")
	DeaconCycle     int64
	HealthyAgents   int
	UnhealthyAgents int
	IsPaused        bool
	PauseReason     string
	HeartbeatFresh  bool // true if < 5min old
}

// QueueRow represents a work queue.
type QueueRow struct {
	Name       string
	Status     string // active, paused, closed
	Available  int
	Processing int
	Completed  int
	Failed     int
}

// SessionRow represents a tmux session.
type SessionRow struct {
	Name     string // Session name (e.g., "gt-gastown-witness")
	Role     string // witness, refinery, polecat, crew, deacon
	Rig      string // Rig name if applicable
	Worker   string // Worker name for polecats/crew
	Activity string // Age since last activity
	IsAlive  bool   // Whether Claude is running in session
}

// HookRow represents a hooked bead (work pinned to an agent).
type HookRow struct {
	ID       string // Bead ID (e.g., "gt-abc12")
	Title    string // Work item title
	Assignee string // Agent address (e.g., "gastown/polecats/nux")
	Agent    string // Formatted agent name
	Age      string // Time since hooked
	IsStale  bool   // True if hooked > 1 hour (potentially stuck)
}

// MayorStatus represents the Mayor's current state.
type MayorStatus struct {
	IsAttached   bool   // True if gt-mayor tmux session exists
	SessionName  string // Tmux session name
	LastActivity string // Age since last activity
	IsActive     bool   // True if activity < 5 min (likely working)
	Runtime      string // Which runtime (claude, codex, etc.)
}

// IssueRow represents an open issue in the backlog.
type IssueRow struct {
	ID       string // Bead ID (e.g., "gt-abc12")
	Title    string // Issue title
	Type     string // issue, bug, feature, task
	Priority int    // 1=critical, 2=high, 3=medium, 4=low
	Age      string // Time since created
	Labels   string // Comma-separated labels
	Assignee string // Who it's hooked to (empty if unassigned)
}

// ActivityRow represents an event in the activity feed.
type ActivityRow struct {
	Time         string // Formatted time (e.g., "2m ago")
	Icon         string // Emoji for event type
	Type         string // Event type (sling, done, mail, etc.)
	Category     string // Event category for filtering (agent, work, comms, system)
	Actor        string // Who did it
	Rig          string // Rig name extracted from actor (e.g., "gastown")
	Summary      string // Human-readable description
	RawTimestamp string // ISO 8601 timestamp for JS sorting/filtering
}

// DashboardSummary provides at-a-glance stats and alerts.
type DashboardSummary struct {
	// Stats
	PolecatCount    int
	HookCount       int
	IssueCount      int
	ConvoyCount     int
	EscalationCount int

	// Alerts (things needing attention)
	StuckPolecats      int // No activity > 5 min
	StaleHooks         int // Hooked > 1 hour
	UnackedEscalations int
	DeadSessions       int // Sessions that died recently
	HighPriorityIssues int // P1/P2 issues

	// Computed
	HasAlerts bool
}

// MailRow represents a mail message in the dashboard.
type MailRow struct {
	ID        string // Message ID (e.g., "hq-msg-abc123")
	From      string // Sender (e.g., "gastown/polecats/Toast")
	FromRaw   string // Raw sender address for color hashing
	To        string // Recipient (e.g., "mayor/")
	Subject   string // Message subject
	Timestamp string // Formatted timestamp
	Age       string // Human-readable age (e.g., "5m ago")
	Priority  string // low, normal, high, urgent
	Type      string // task, notification, reply
	Read      bool   // Whether message has been read
	SortKey   int64  // Unix timestamp for sorting
}

// WorkerRow represents a worker (polecat or refinery) in the dashboard.
type WorkerRow struct {
	Name         string        // e.g., "dag", "nux", "refinery"
	Rig          string        // e.g., "roxas", "gastown"
	SessionID    string        // e.g., "gt-roxas-dag"
	LastActivity activity.Info // Colored activity display
	StatusHint   string        // Last line from pane (optional)
	IssueID      string        // Currently assigned issue ID (e.g., "hq-1234")
	IssueTitle   string        // Issue title (truncated)
	WorkStatus   string        // working, stale, stuck, idle
	AgentType    string        // "polecat" (ephemeral sessions) or "refinery" (permanent)
}

// MergeQueueRow represents a PR in the merge queue.
type MergeQueueRow struct {
	Number     int
	Repo       string // Short repo name (e.g., "roxas", "gastown")
	Title      string
	URL        string
	CIStatus   string // "pass", "fail", "pending"
	Mergeable  string // "ready", "conflict", "pending"
	ColorClass string // "mq-green", "mq-yellow", "mq-red"
}

// ConvoyRow represents a single convoy in the dashboard.
type ConvoyRow struct {
	ID            string
	Title         string
	Status        string // "open" or "closed" (raw beads status)
	WorkStatus    string // Computed: "complete", "active", "stale", "stuck", "waiting"
	Progress      string // e.g., "2/5"
	Completed     int
	Total         int
	ProgressPct   int      // 0-100, computed from Completed/Total
	ReadyBeads    int      // open beads with no assignee (available to pick up)
	InProgress    int      // beads currently being worked on
	Assignees     []string // unique assignees across tracked issues
	LastActivity  activity.Info
	TrackedIssues []TrackedIssue
}

// TrackedIssue represents an issue tracked by a convoy.
type TrackedIssue struct {
	ID       string
	Title    string
	Status   string
	Assignee string
}

// LoadTemplates loads and parses all HTML templates.
func LoadTemplates() (*template.Template, error) {
	// Define template functions
	funcMap := template.FuncMap{
		"activityClass":      activityClass,
		"statusClass":        statusClass,
		"workStatusClass":    workStatusClass,
		"senderColorClass":   senderColorClass,
		"severityClass":      severityClass,
		"dogStateClass":      dogStateClass,
		"queueStatusClass":   queueStatusClass,
		"polecatStatusClass": polecatStatusClass,
		"activityTypeClass": activityTypeClass,
		"contains": func(s, substr string) bool {
			return strings.Contains(s, substr)
		},
	}

	// Get the templates subdirectory
	subFS, err := fs.Sub(templateFS, "templates")
	if err != nil {
		return nil, err
	}

	// Parse all templates
	tmpl, err := template.New("").Funcs(funcMap).ParseFS(subFS, "*.html")
	if err != nil {
		return nil, err
	}

	return tmpl, nil
}

// activityClass returns the CSS class for an activity color.
func activityClass(info activity.Info) string {
	switch info.ColorClass {
	case activity.ColorGreen:
		return "activity-green"
	case activity.ColorYellow:
		return "activity-yellow"
	case activity.ColorRed:
		return "activity-red"
	default:
		return "activity-unknown"
	}
}

// statusClass returns the CSS class for a convoy status.
func statusClass(status string) string {
	switch status {
	case "open":
		return "status-open"
	case "closed":
		return "status-closed"
	default:
		return "status-unknown"
	}
}

// workStatusClass returns the CSS class for a computed work status.
func workStatusClass(workStatus string) string {
	switch workStatus {
	case "complete":
		return "work-complete"
	case "active":
		return "work-active"
	case "stale":
		return "work-stale"
	case "stuck":
		return "work-stuck"
	case "waiting":
		return "work-waiting"
	default:
		return "work-unknown"
	}
}

// senderColorClass returns a CSS class for sender-based color coding.
// Uses a simple hash to assign consistent colors to each sender.
func senderColorClass(fromRaw string) string {
	if fromRaw == "" {
		return "sender-default"
	}
	// Simple hash: sum of bytes mod number of colors
	var sum int
	for _, b := range []byte(fromRaw) {
		sum += int(b)
	}
	colors := []string{
		"sender-cyan",
		"sender-purple",
		"sender-green",
		"sender-yellow",
		"sender-orange",
		"sender-blue",
		"sender-red",
		"sender-pink",
	}
	return colors[sum%len(colors)]
}

// severityClass returns CSS class for escalation severity.
func severityClass(severity string) string {
	switch severity {
	case "critical":
		return "severity-critical"
	case "high":
		return "severity-high"
	case "medium":
		return "severity-medium"
	case "low":
		return "severity-low"
	default:
		return "severity-unknown"
	}
}

// dogStateClass returns CSS class for dog state.
func dogStateClass(state string) string {
	switch state {
	case "idle":
		return "dog-idle"
	case "working":
		return "dog-working"
	default:
		return "dog-unknown"
	}
}

// queueStatusClass returns CSS class for queue status.
func queueStatusClass(status string) string {
	switch status {
	case "active":
		return "queue-active"
	case "paused":
		return "queue-paused"
	case "closed":
		return "queue-closed"
	default:
		return "queue-unknown"
	}
}

// polecatStatusClass returns CSS class for polecat work status.
func polecatStatusClass(status string) string {
	switch status {
	case "working":
		return "polecat-working"
	case "stale":
		return "polecat-stale"
	case "stuck":
		return "polecat-stuck"
	case "idle":
		return "polecat-idle"
	default:
		return "polecat-unknown"
	}
}

// activityTypeClass returns CSS class for an activity event category.
func activityTypeClass(category string) string {
	switch category {
	case "agent":
		return "tl-cat-agent"
	case "work":
		return "tl-cat-work"
	case "comms":
		return "tl-cat-comms"
	case "system":
		return "tl-cat-system"
	default:
		return "tl-cat-default"
	}
}
