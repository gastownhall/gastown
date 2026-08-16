// Package worker is the town seam for talking to an agent session.
//
// Callers send prompts, read lifecycle and health, take telemetry, push
// identity and context, and answer authorize through Worker. They do not
// talk to tmux, pane text, or vendor logs.
//
// A session uses the protocol adapter when the agent is connected.
// A session uses the tmux adapter when the agent is not connected.
package worker

import (
	"errors"
	"time"
)

// Lifecycle events the runtime reports. The protocol adapter does not infer
// these. The tmux adapter may infer them.
const (
	EventStarted  = "started"
	EventReady    = "ready"
	EventBusy     = "busy"
	EventIdle     = "idle"
	EventStopping = "stopping"
	EventStopped  = "stopped"
)

// State is the last known lifecycle state of a run.
type State string

const (
	StateUnknown  State = "unknown"
	StateStarted  State = "started"
	StateReady    State = "ready"
	StateBusy     State = "busy"
	StateIdle     State = "idle"
	StateStopping State = "stopping"
	StateStopped  State = "stopped"
)

// Priority controls when a prompt is delivered.
const (
	PrioritySystem = "system" // wait for the end of a turn
	PriorityNormal = "normal" // wait for idle; queue if busy
	PriorityUrgent = "urgent" // interrupt
)

// Source names the town caller that sent a prompt.
const (
	SourceNudge = "nudge"
	SourceMail  = "mail"
	SourceSling = "sling"
	SourcePrime = "prime"
)

// Context section types stay role, work, mail, checkpoint, and directive.
const (
	SectionRole       = "role"
	SectionWork       = "work"
	SectionMail       = "mail"
	SectionCheckpoint = "checkpoint"
	SectionDirective  = "directive"
)

// Context modes for handoff and prime.
const (
	ContextFull    = "full"
	ContextCompact = "compact"
	ContextResume  = "resume"
)

// Health statuses reported by the runtime or inferred after a grace timeout.
const (
	HealthHealthy   = "healthy"
	HealthDegraded  = "degraded"
	HealthUnhealthy = "unhealthy"
)

// Adapter names persisted on delivery and run records.
const (
	AdapterProtocol = "protocol"
	AdapterTmux     = "tmux"
)

// Message kinds. This contract has seven kinds and does not add an eighth.
const (
	KindLifecycle = "lifecycle"
	KindPrompt    = "prompt"
	KindContext   = "context"
	KindAuthorize = "authorize"
	KindTelemetry = "telemetry"
	KindIdentity  = "identity"
	KindHealth    = "health"
)

// Peer kinds on the Worker socket.
const (
	PeerAgent = "agent"
	PeerTown  = "town"
)

var (
	// ErrUnknownState is returned when the town does not know the run state.
	// Fail closed: no deliver, no kill, no authorize allow.
	ErrUnknownState = errors.New("worker: unknown state")

	// ErrNotConnected means no protocol client is attached and the tmux
	// adapter cannot be used for this call.
	ErrNotConnected = errors.New("worker: no protocol client")

	// ErrLiveRun means the bead already has a live run.
	ErrLiveRun = errors.New("worker: bead already has a live run")

	// ErrRunNotFound means the run_id is not in the store.
	ErrRunNotFound = errors.New("worker: run not found")

	// ErrUnauthorized is returned when authorize denies a tool.
	ErrUnauthorized = errors.New("worker: authorize denied")

	// ErrQueueExpired means a queued prompt expired before delivery.
	ErrQueueExpired = errors.New("worker: queued prompt expired")

	// ErrUnhealthy means no health reply arrived within the grace time.
	ErrUnhealthy = errors.New("worker: unhealthy")

	// ErrServerDown means the Worker server is not running and could not start.
	ErrServerDown = errors.New("worker: server unavailable")
)

// Run is one session from spawn to stopped. Hook bead, role, rig, and
// agent name are identity fields on that run.
type Run struct {
	RunID      string    `json:"run_id"`
	SessionID  string    `json:"session_id"`
	BeadID     string    `json:"bead_id,omitempty"`
	Role       string    `json:"role,omitempty"`
	Rig        string    `json:"rig,omitempty"`
	AgentName  string    `json:"agent_name,omitempty"`
	AgentType  string    `json:"agent_type,omitempty"`
	State      State     `json:"state"`
	Adapter    string    `json:"adapter,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	StoppedAt  time.Time `json:"stopped_at,omitempty"`
	ExitCode   *int      `json:"exit_code,omitempty"`
	Done       bool      `json:"done,omitempty"`
	LastHealth time.Time `json:"last_health,omitempty"`
}

// Prompt is a town-to-agent message with priority and source.
type Prompt struct {
	RunID    string `json:"run_id"`
	Content  string `json:"content"`
	Priority string `json:"priority"`
	Source   string `json:"source"`
	From     string `json:"from,omitempty"`
	BeadID   string `json:"bead_id,omitempty"`
	Kind     string `json:"kind,omitempty"`
}

// Delivery is the result of prompt deliver.
type Delivery struct {
	Accepted bool   `json:"accepted"`
	Queued   bool   `json:"queued"`
	Position int    `json:"position"`
	Adapter  string `json:"adapter,omitempty"`
	RunID    string `json:"run_id,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// Lifecycle is an agent-reported state change.
type Lifecycle struct {
	Event     string         `json:"event"`
	RunID     string         `json:"run_id"`
	SessionID string         `json:"session_id,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// Health is a bidirectional liveness report.
type Health struct {
	Status       string    `json:"status"`
	RunID        string    `json:"run_id"`
	UptimeSecs   int64     `json:"uptime_seconds,omitempty"`
	CurrentState string    `json:"current_state,omitempty"`
	LastActivity time.Time `json:"last_activity,omitempty"`
	ContextUse   float64   `json:"context_usage,omitempty"`
	Error        string    `json:"error,omitempty"`
}

// Identity is pushed to a protocol session so the agent does not scrape env.
type Identity struct {
	RunID       string            `json:"run_id"`
	Role        string            `json:"role"`
	Rig         string            `json:"rig,omitempty"`
	AgentName   string            `json:"agent_name,omitempty"`
	SessionID   string            `json:"session_id"`
	Credentials *Credentials      `json:"credentials,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
}

// Credentials are rotated without restarting the session.
type Credentials struct {
	Type      string    `json:"type"`
	Value     string    `json:"value"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

// ContextSection is one prime/handoff part.
type ContextSection struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

// ContextPush is structured prime for protocol sessions.
type ContextPush struct {
	RunID    string           `json:"run_id"`
	Sections []ContextSection `json:"sections"`
	Mode     string           `json:"mode"`
}

// AuthorizeRequest is one tool call the runtime asks the town to allow.
type AuthorizeRequest struct {
	RunID   string         `json:"run_id"`
	Tool    string         `json:"tool"`
	Input   map[string]any `json:"input,omitempty"`
	Context map[string]any `json:"context,omitempty"`
}

// AuthorizeDecision is fail-closed. A deny always includes a reason.
type AuthorizeDecision struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
}

// TelemetryEvent is one turn or tool report from the runtime.
type TelemetryEvent struct {
	Type        string         `json:"type"`
	Timestamp   time.Time      `json:"timestamp"`
	Usage       *Usage         `json:"usage,omitempty"`
	ToolsCalled []ToolCall     `json:"tools_called,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// Usage is token and cost data computed at the runtime.
type Usage struct {
	InputTokens         int     `json:"input_tokens"`
	OutputTokens        int     `json:"output_tokens"`
	CacheReadTokens     int     `json:"cache_read_tokens,omitempty"`
	CacheCreationTokens int     `json:"cache_creation_tokens,omitempty"`
	Model               string  `json:"model,omitempty"`
	CostUSD             float64 `json:"cost_usd"`
}

// ToolCall is one tool invocation on a turn.
type ToolCall struct {
	Name       string `json:"name"`
	Success    bool   `json:"success"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

// TelemetryBatch is the runtime push of one or more events.
type TelemetryBatch struct {
	RunID  string           `json:"run_id"`
	Events []TelemetryEvent `json:"events"`
}

// CostRecord is a persisted cost line from a telemetry event.
type CostRecord struct {
	RunID     string    `json:"run_id"`
	BeadID    string    `json:"bead_id,omitempty"`
	Role      string    `json:"role,omitempty"`
	Rig       string    `json:"rig,omitempty"`
	AgentName string    `json:"agent_name,omitempty"`
	AgentType string    `json:"agent_type,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	CostUSD   float64   `json:"cost_usd"`
	Model     string    `json:"model,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// Event is a persisted lifecycle or activity record.
type Event struct {
	Type      string         `json:"type"`
	RunID     string         `json:"run_id"`
	BeadID    string         `json:"bead_id,omitempty"`
	SessionID string         `json:"session_id,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
	Payload   map[string]any `json:"payload,omitempty"`
}

// StartSpec is the town-side registration for a new run.
type StartSpec struct {
	RunID     string
	SessionID string
	BeadID    string
	Role      string
	Rig       string
	AgentName string
	AgentType string
}

// QueuedPrompt is the Worker backlog for busy+normal/system deliver.
type QueuedPrompt struct {
	ID        string    `json:"id"`
	Prompt    Prompt    `json:"prompt"`
	Enqueued  time.Time `json:"enqueued"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	Reason    string    `json:"reason,omitempty"`
}

func validPriority(p string) bool {
	switch p {
	case PrioritySystem, PriorityNormal, PriorityUrgent:
		return true
	default:
		return false
	}
}

func stateFromEvent(event string) State {
	switch event {
	case EventStarted:
		return StateStarted
	case EventReady:
		return StateReady
	case EventBusy:
		return StateBusy
	case EventIdle:
		return StateIdle
	case EventStopping:
		return StateStopping
	case EventStopped:
		return StateStopped
	default:
		return StateUnknown
	}
}

func (s State) known() bool {
	return s != "" && s != StateUnknown
}

func (s State) live() bool {
	switch s {
	case StateStarted, StateReady, StateBusy, StateIdle, StateStopping:
		return true
	default:
		return false
	}
}
