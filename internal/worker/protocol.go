package worker

import (
	"encoding/json"
	"fmt"
	"time"
)

// Envelope is one Worker message. The seven kinds are lifecycle, prompt,
// context, authorize, telemetry, identity, and health.
type Envelope struct {
	Kind      string          `json:"kind"`
	ID        string          `json:"id,omitempty"`
	Peer      string          `json:"peer,omitempty"`
	RunID     string          `json:"run_id,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// TownRequest is a control call from a gt command to the Worker server.
type TownRequest struct {
	Op      string          `json:"op"`
	RunID   string          `json:"run_id,omitempty"`
	Session string          `json:"session_id,omitempty"`
	BeadID  string          `json:"bead_id,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// TownResponse is the server reply to a town control call.
type TownResponse struct {
	OK      bool            `json:"ok"`
	Error   string          `json:"error,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

const (
	opStartRun    = "start_run"
	opDeliver     = "deliver"
	opState       = "state"
	opHealth      = "health"
	opKill        = "kill"
	opIdentity    = "identity"
	opContext     = "context"
	opLiveBead    = "live_bead"
	opEvents      = "events"
	opCosts       = "costs"
	opWaitReady   = "wait_ready"
	opExpireQueue = "expire_queue"
	opPing        = "ping"
)

func marshalPayload(v any) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshaling worker payload: %w", err)
	}
	return data, nil
}

func decodePayload[T any](raw json.RawMessage) (T, error) {
	var zero T
	if len(raw) == 0 {
		return zero, nil
	}
	if err := json.Unmarshal(raw, &zero); err != nil {
		return zero, fmt.Errorf("decoding worker payload: %w", err)
	}
	return zero, nil
}

func nowUTC() time.Time {
	return time.Now().UTC()
}
