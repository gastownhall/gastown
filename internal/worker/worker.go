package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jonbaldie/gastown/internal/events"
)

// Worker is the town module for session talk. Nudge, prime, mail, sling,
// witness, cost ingest, and session start call this type.
type Worker struct {
	townRoot string
	store    *Store
	client   *Client
	local    *Server
}

// Open dials a running Worker server. If none is up, it starts one.
func Open(townRoot string) (*Worker, error) {
	if townRoot == "" {
		return nil, fmt.Errorf("worker: town root is required")
	}
	store := newStore(townRoot)
	if err := store.ensure(); err != nil {
		return nil, err
	}
	if err := EnsureServer(townRoot); err != nil {
		return nil, err
	}
	client := newClient(townRoot)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.ping(ctx); err != nil {
		httpClient, httpErr := newHTTPClient(townRoot)
		if httpErr != nil {
			return nil, fmt.Errorf("worker open: %w", err)
		}
		if pingErr := httpClient.ping(ctx); pingErr != nil {
			return nil, fmt.Errorf("%w: %v", ErrServerDown, pingErr)
		}
		client = httpClient
	}
	return &Worker{townRoot: townRoot, store: store, client: client}, nil
}

// Listen starts an in-process Worker server. Tests and `gt worker serve` use this.
func Listen(townRoot string, tmux TmuxSession) (*Worker, error) {
	store := newStore(townRoot)
	srv := newServer(store, tmux)
	if err := srv.Listen(); err != nil {
		return nil, err
	}
	return &Worker{
		townRoot: townRoot,
		store:    store,
		client:   newClient(townRoot),
		local:    srv,
	}, nil
}

// Close stops an in-process server.
func (w *Worker) Close() error {
	if w.local != nil {
		return w.local.Close()
	}
	return nil
}

// StartRun registers a run_id from spawn. A second live run for the same
// bead fails.
func (w *Worker) StartRun(ctx context.Context, spec StartSpec) (*Run, error) {
	payload, err := marshalPayload(spec)
	if err != nil {
		return nil, err
	}
	resp, err := w.client.call(ctx, TownRequest{Op: opStartRun, Payload: payload})
	if err != nil {
		return nil, err
	}
	return decodePayload[*Run](resp.Payload)
}

// WaitReady blocks until the agent reports ready, or the tmux adapter
// finishes its ready delay.
func (w *Worker) WaitReady(ctx context.Context, runID string) error {
	_, err := w.client.call(ctx, TownRequest{Op: opWaitReady, RunID: runID})
	return err
}

// Deliver sends a prompt with priority and source. It returns accepted or
// queued and a queue position. Unknown state fails closed.
func (w *Worker) Deliver(ctx context.Context, p Prompt) (*Delivery, error) {
	payload, err := marshalPayload(p)
	if err != nil {
		return nil, err
	}
	resp, err := w.client.call(ctx, TownRequest{Op: opDeliver, RunID: p.RunID, Payload: payload})
	if err != nil {
		return nil, err
	}
	return decodePayload[*Delivery](resp.Payload)
}

// State returns the last known lifecycle state.
func (w *Worker) State(ctx context.Context, runID, sessionID string) (State, error) {
	resp, err := w.client.call(ctx, TownRequest{Op: opState, RunID: runID, Session: sessionID})
	if err != nil {
		return StateUnknown, err
	}
	st, err := decodePayload[State](resp.Payload)
	if err != nil {
		// JSON may encode the state as a quoted string already handled, or as
		// a raw string without quotes if decodePayload saw a JSON string.
		var s string
		if json.Unmarshal(resp.Payload, &s) == nil {
			return State(s), nil
		}
		return StateUnknown, err
	}
	return st, nil
}

// Health returns the last health report. No reply after the grace time
// means unhealthy.
func (w *Worker) Health(ctx context.Context, runID string) (*Health, error) {
	resp, err := w.client.call(ctx, TownRequest{Op: opHealth, RunID: runID})
	if err != nil {
		return nil, err
	}
	return decodePayload[*Health](resp.Payload)
}

// Kill stops a session. Unknown state fails closed.
func (w *Worker) Kill(ctx context.Context, runID, sessionID string) error {
	_, err := w.client.call(ctx, TownRequest{Op: opKill, RunID: runID, Session: sessionID})
	return err
}

// PushIdentity replaces extra env vars for a protocol session.
func (w *Worker) PushIdentity(ctx context.Context, id Identity) error {
	payload, err := marshalPayload(id)
	if err != nil {
		return err
	}
	_, err = w.client.call(ctx, TownRequest{Op: opIdentity, RunID: id.RunID, Payload: payload})
	return err
}

// PushContext sends prime or handoff sections. Protocol sessions do not
// get prime from the pane.
func (w *Worker) PushContext(ctx context.Context, push ContextPush) error {
	payload, err := marshalPayload(push)
	if err != nil {
		return err
	}
	_, err = w.client.call(ctx, TownRequest{Op: opContext, RunID: push.RunID, Payload: payload})
	return err
}

// LiveRun returns the live run for a bead, if any.
func (w *Worker) LiveRun(ctx context.Context, beadID string) (*Run, error) {
	resp, err := w.client.call(ctx, TownRequest{Op: opLiveBead, BeadID: beadID})
	if err != nil {
		return nil, err
	}
	if len(resp.Payload) == 0 || string(resp.Payload) == "null" {
		return nil, nil
	}
	return decodePayload[*Run](resp.Payload)
}

// Events returns persisted lifecycle and activity events.
func (w *Worker) Events(ctx context.Context) ([]Event, error) {
	resp, err := w.client.call(ctx, TownRequest{Op: opEvents})
	if err != nil {
		return nil, err
	}
	return decodePayload[[]Event](resp.Payload)
}

// Costs returns persisted cost records from runtime telemetry.
func (w *Worker) Costs(ctx context.Context) ([]CostRecord, error) {
	resp, err := w.client.call(ctx, TownRequest{Op: opCosts})
	if err != nil {
		return nil, err
	}
	return decodePayload[[]CostRecord](resp.Payload)
}

// ReadCosts reads the production cost store without a live server.
func ReadCosts(townRoot string) ([]CostRecord, error) {
	return newStore(townRoot).readCosts()
}

// ReadEvents reads persisted Worker events without a live server.
func ReadEvents(townRoot string) ([]Event, error) {
	return newStore(townRoot).readEvents()
}

// ReadRun loads one run from the production store.
func ReadRun(townRoot, runID string) (*Run, error) {
	return newStore(townRoot).getRun(runID)
}

// LiveRunFromStore returns the live run for a bead from the production store.
func LiveRunFromStore(townRoot, beadID string) (*Run, error) {
	return newStore(townRoot).liveRunForBead(beadID)
}

// LatestRunForBead returns the most recently updated run for a bead.
func LatestRunForBead(townRoot, beadID string) (*Run, error) {
	return newStore(townRoot).latestRunForBead(beadID)
}

// StoppedWithoutDone reports whether the bead's latest run stopped without
// gt done. Mountain failure counts rise only in this case.
func StoppedWithoutDone(townRoot, beadID string) bool {
	run, err := newStore(townRoot).latestRunForBead(beadID)
	if err != nil || run == nil {
		return false
	}
	return run.State == StateStopped && !run.Done
}

// RunBySession returns the live run for a session from the production store.
func RunBySession(townRoot, sessionID string) (*Run, error) {
	return newStore(townRoot).getRunBySession(sessionID)
}

// LatestRunForSession returns the most recently updated run for a session.
func LatestRunForSession(townRoot, sessionID string) (*Run, error) {
	return newStore(townRoot).latestRunForSession(sessionID)
}

// MarkSessionStopped closes the latest live run for a session after its runtime
// has been terminated outside Worker. Missing and already-stopped runs are safe
// no-ops so legacy sessions can use the same shutdown path.
func MarkSessionStopped(townRoot, sessionID string) error {
	store := newStore(townRoot)
	run, err := store.latestRunForSession(sessionID)
	if errors.Is(err, ErrRunNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return markRunStopped(store, run)
}

func markRunStopped(store *Store, run *Run) error {
	if !run.State.known() {
		return ErrUnknownState
	}
	if !run.State.live() {
		return nil
	}
	now := nowUTC()
	run.State = StateStopped
	run.UpdatedAt = now
	run.StoppedAt = now
	if err := store.putRun(run); err != nil {
		return err
	}
	return store.appendEvent(Event{
		Type: EventStopped, RunID: run.RunID, BeadID: run.BeadID,
		SessionID: run.SessionID, Timestamp: now,
	})
}

// RefuseLiveBead fails closed when the bead already has a live run.
func RefuseLiveBead(townRoot, beadID string) error {
	if townRoot == "" || beadID == "" {
		return nil
	}
	live, err := LiveRunFromStore(townRoot, beadID)
	if err != nil {
		return err
	}
	if live != nil {
		return fmt.Errorf("%w: bead %s already has live run %s", ErrLiveRun, beadID, live.RunID)
	}
	return nil
}

// PersistRun writes a run to the production store. Town callers and tests
// use this when they already have a complete record.
func PersistRun(townRoot string, run *Run) error {
	if run == nil {
		return fmt.Errorf("worker: run is required")
	}
	s := newStore(townRoot)
	if err := s.ensure(); err != nil {
		return err
	}
	return s.putRun(run)
}

// StoreHealth reports health from persisted run data. No health reply after
// grace means unhealthy. Busy is a known live state, not a stall.
func StoreHealth(townRoot, sessionID string, grace time.Duration) (*Health, error) {
	if grace <= 0 {
		grace = healthGrace()
	}
	run, err := newStore(townRoot).getRunBySession(sessionID)
	if err != nil {
		return nil, err
	}
	h := &Health{RunID: run.RunID, CurrentState: string(run.State), LastActivity: run.UpdatedAt}
	if !run.State.known() {
		h.Status = HealthUnhealthy
		h.Error = ErrUnknownState.Error()
		return h, nil
	}
	if !run.LastHealth.IsZero() && time.Since(run.LastHealth) > grace {
		h.Status = HealthUnhealthy
		h.Error = ErrUnhealthy.Error()
		return h, nil
	}
	if run.LastHealth.IsZero() && time.Since(run.UpdatedAt) > grace && run.State != StateStarted {
		h.Status = HealthUnhealthy
		h.Error = ErrUnhealthy.Error()
		return h, nil
	}
	h.Status = HealthHealthy
	return h, nil
}

// EnsureServer starts `gt worker serve` when the socket is not live.
func EnsureServer(townRoot string) error {
	if pingUnix(townRoot) == nil {
		return nil
	}
	if _, err := os.Stat(PortPath(townRoot)); err == nil {
		if c, err := newHTTPClient(townRoot); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if c.ping(ctx) == nil {
				return nil
			}
		}
	}
	bin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding gt binary: %w", err)
	}
	if isTestExecutable(bin) {
		return fmt.Errorf("%w: refusing to start test binary as worker server", ErrServerDown)
	}
	cmd := exec.Command(bin, "worker", "serve", "--town", townRoot)
	cmd.SysProcAttr = serveSysProcAttr()
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%w: starting worker server: %v", ErrServerDown, err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if pingUnix(townRoot) == nil {
			_ = cmd.Process.Release()
			return nil
		}
		select {
		case err := <-exited:
			if err != nil {
				return fmt.Errorf("%w: server exited: %v", ErrServerDown, err)
			}
			return fmt.Errorf("%w: server exited", ErrServerDown)
		case <-time.After(20 * time.Millisecond):
		}
	}
	_ = cmd.Process.Kill()
	return fmt.Errorf("%w: server did not become ready", ErrServerDown)
}

func isTestExecutable(bin string) bool {
	base := filepath.Base(bin)
	return strings.HasSuffix(base, ".test") || strings.HasSuffix(base, ".test.exe")
}

// LogTownLifecycle writes a feed-visible lifecycle event when a town root
// is the current workspace.
func LogTownLifecycle(run *Run, event string) {
	if run == nil {
		return
	}
	payload := map[string]any{
		"run_id":     run.RunID,
		"bead_id":    run.BeadID,
		"session_id": run.SessionID,
		"event":      event,
		"role":       run.Role,
		"agent_type": run.AgentType,
	}
	_ = events.LogFeed("worker_lifecycle", run.Role, payload)
}
