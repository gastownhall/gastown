// Package agentlink defines the wire types of the machine-to-server link
// (spec §7). It is shared by the machine agent and the server and must not
// import internal/workflow.
package agentlink

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"sync"
	"time"
)

// ProtocolVersion is the agentlink protocol version a machine reports in Hello.
const ProtocolVersion = "1.0.0"

// Hello is the first message of a machine session.
type Hello struct {
	MachineID  string `json:"machine_id"`
	Hostname   string `json:"hostname"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	GtbVersion string `json:"gtb_version"`
	Protocol   string `json:"protocol"`
}

// Snapshot is a machine's full state. The server replaces everything it holds
// for the machine with it.
type Snapshot struct {
	Rigs     []Rig         `json:"rigs"`
	Items    []ItemView    `json:"items"`
	Sessions []SessionView `json:"sessions"`
	Cursor   string        `json:"cursor"`
}

// Rig is a rig on a machine.
type Rig struct {
	Name   string `json:"name"`
	Remote string `json:"remote"`
	Gated  bool   `json:"gated"`
}

// ItemView is the projection of a workflow item sent to the server.
type ItemView struct {
	ID              string         `json:"id"`
	Rig             string         `json:"rig"`
	Kind            string         `json:"kind"`
	Step            string         `json:"step"`
	Title           string         `json:"title"`
	Parent          string         `json:"parent"`
	Branch          string         `json:"branch"`
	WaitingForHuman string         `json:"waiting_for_human"`
	DependsOn       []string       `json:"depends_on"`
	StepEnteredAt   time.Time      `json:"step_entered_at"`
	Artifacts       []ArtifactView `json:"artifacts,omitempty"`
	Diffstat        []FileStat     `json:"diffstat,omitempty"`
	Usage           *Usage         `json:"usage,omitempty"`
}

// ArtifactView is the latest version of an item artifact (spec, plan, rca, review).
type ArtifactView struct {
	Name    string `json:"name"`
	SHA     string `json:"sha"`
	Content string `json:"content"`
}

// FileStat is one file of an item's diffstat.
type FileStat struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
}

// Usage is token and cost usage reported by the agent CLI.
type Usage struct {
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// SessionView is a worker session on a machine.
type SessionView struct {
	Name         string    `json:"name"`
	Role         string    `json:"role"`
	Rig          string    `json:"rig"`
	ItemID       string    `json:"item_id"`
	State        string    `json:"state"`
	LastActivity time.Time `json:"last_activity"`
}

// Event types.
const (
	EventItemUpsert  = "item.upsert"
	EventItemDeleted = "item.deleted"
	EventEvidence    = "evidence"
	EventArtifact    = "artifact"
	EventSession     = "session"
	EventHeartbeat   = "heartbeat"
)

// EventTypes is the closed set of event types.
var EventTypes = []string{EventItemUpsert, EventItemDeleted, EventEvidence, EventArtifact, EventSession, EventHeartbeat}

// Event is one entry of a machine's event stream. ID is a ULID (see NewEventID).
type Event struct {
	ID     string          `json:"id"`
	At     time.Time       `json:"at"`
	Type   string          `json:"type"`
	ItemID string          `json:"item_id"`
	Data   json.RawMessage `json:"data"`
}

// Request is a server-to-machine request. Requests are idempotent by Key and
// must never run after ExpiresAt.
type Request struct {
	Key       string          `json:"key"`
	Type      string          `json:"type"`
	ItemID    string          `json:"item_id"`
	Params    json.RawMessage `json:"params"`
	ExpiresAt time.Time       `json:"expires_at"`
}

// Result is a machine's answer to a Request.
type Result struct {
	Key   string          `json:"key"`
	OK    bool            `json:"ok"`
	Error string          `json:"error"`
	Data  json.RawMessage `json:"data"`
}

// RequestTypes is the closed set of request types (spec §7.3).
var RequestTypes = []string{"approve", "request_changes", "item.create", "item.sling", "item.cancel", "snapshot", "inspect", "terminal.peek"}

// IsRequestType reports whether t is in RequestTypes.
func IsRequestType(t string) bool {
	for _, r := range RequestTypes {
		if r == t {
			return true
		}
	}
	return false
}

// ApproveParams are the Params of an approve or request_changes request.
type ApproveParams struct {
	Artifact string `json:"artifact"`
	SHA      string `json:"sha"`
	Actor    string `json:"actor"`
}

// ULID generation: 48-bit unix-millisecond timestamp plus 80 bits of
// crypto/rand entropy, Crockford base32 encoded. Within one millisecond the
// entropy is incremented, so IDs from this process are strictly increasing.

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var ulidState struct {
	sync.Mutex
	ms      uint64
	hi      uint16 // top 16 bits of entropy
	lo      uint64 // low 64 bits of entropy
	started bool
}

// NewEventID returns a ULID for an event at t. IDs sort by time, and IDs
// generated in the same millisecond are monotonic.
func NewEventID(t time.Time) string {
	ms := uint64(t.UnixMilli()) & (1<<48 - 1)

	ulidState.Lock()
	if ulidState.started && ms == ulidState.ms {
		ulidState.lo++
		if ulidState.lo == 0 {
			ulidState.hi++ // 80-bit overflow is practically impossible
		}
	} else {
		var b [10]byte
		if _, err := rand.Read(b[:]); err != nil {
			panic("agentlink: crypto/rand: " + err.Error())
		}
		ulidState.hi = binary.BigEndian.Uint16(b[:2])
		ulidState.lo = binary.BigEndian.Uint64(b[2:])
		ulidState.ms = ms
		ulidState.started = true
	}
	hi, lo := ulidState.hi, ulidState.lo
	ulidState.Unlock()

	var raw [16]byte
	raw[0] = byte(ms >> 40)
	raw[1] = byte(ms >> 32)
	raw[2] = byte(ms >> 24)
	raw[3] = byte(ms >> 16)
	raw[4] = byte(ms >> 8)
	raw[5] = byte(ms)
	binary.BigEndian.PutUint16(raw[6:8], hi)
	binary.BigEndian.PutUint64(raw[8:], lo)
	return encodeULID(raw)
}

// encodeULID encodes 128 bits as 26 Crockford base32 characters (the first
// character carries only 3 bits).
func encodeULID(raw [16]byte) string {
	hi := binary.BigEndian.Uint64(raw[:8])
	lo := binary.BigEndian.Uint64(raw[8:])
	var out [26]byte
	for i := 25; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out[:])
}
