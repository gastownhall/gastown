package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/steveyegge/gastown/internal/nudge"
)

const (
	defaultHealthGrace = 30 * time.Second
	defaultReadyWait   = 3 * time.Minute
	defaultAckWait     = 15 * time.Second
	pollTimeout        = 25 * time.Second
	normalTTL          = 30 * time.Minute
	urgentTTL          = 2 * time.Hour
)

// Server is the town-side Worker listener. Agents connect in. Town commands
// call in over the same socket.
type Server struct {
	store       *Store
	healthGrace time.Duration
	readyWait   time.Duration
	ackWait     time.Duration
	tmux        TmuxSession

	mu      sync.Mutex
	conns   map[string]*agentConn
	httpSrv *http.Server
	unixLn  net.Listener
	tcpLn   net.Listener
	port    int
}

type agentConn struct {
	runID       string
	sessionID   string
	connected   bool
	lastSeen    time.Time
	outbound    chan Envelope
	ack         chan Delivery
	interrupt   bool
	prompts     []Prompt
	identity    *Identity
	contextPush *ContextPush
}

// TmuxSession is the fallback adapter surface. Production uses tmux.Tmux.
type TmuxSession interface {
	HasSession(session string) (bool, error)
	NudgeSession(session, message string) error
	WaitForRuntimeReady(session string, timeout time.Duration) error
	WaitForIdle(session string, timeout time.Duration) error
	IsAgentAlive(session string) bool
	KillSessionWithProcesses(session string) error
	CheckSessionHealth(session string, stale time.Duration) string
}

func envDuration(name string, fallback time.Duration) time.Duration {
	if v := os.Getenv(name); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return fallback
}

func healthGrace() time.Duration {
	return envDuration("GT_WORKER_HEALTH_GRACE", defaultHealthGrace)
}

func newServer(store *Store, tmux TmuxSession) *Server {
	return &Server{
		store:       store,
		healthGrace: healthGrace(),
		readyWait:   defaultReadyWait,
		ackWait:     envDuration("GT_WORKER_ACK_WAIT", defaultAckWait),
		tmux:        tmux,
		conns:       map[string]*agentConn{},
	}
}

// Listen starts the Unix socket and the localhost HTTP fallback.
func (s *Server) Listen() error {
	if err := s.store.ensure(); err != nil {
		return err
	}
	sock := SocketPath(s.store.townRoot)
	_ = os.Remove(sock)
	unixLn, err := net.Listen("unix", sock)
	if err != nil {
		return fmt.Errorf("listening on worker socket: %w", err)
	}
	if err := os.Chmod(sock, socketMode); err != nil {
		_ = unixLn.Close()
		return fmt.Errorf("setting worker socket mode: %w", err)
	}
	s.unixLn = unixLn

	tcpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = unixLn.Close()
		return fmt.Errorf("listening on worker http: %w", err)
	}
	s.tcpLn = tcpLn
	s.port = tcpLn.Addr().(*net.TCPAddr).Port
	if err := os.WriteFile(PortPath(s.store.townRoot), []byte(strconv.Itoa(s.port)+"\n"), fileMode); err != nil {
		_ = unixLn.Close()
		_ = tcpLn.Close()
		return fmt.Errorf("writing worker port file: %w", err)
	}

	mux := s.routes()
	s.httpSrv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = s.httpSrv.Serve(unixLn) }()
	go func() { _ = s.httpSrv.Serve(tcpLn) }()
	return nil
}

// Close stops the listeners.
func (s *Server) Close() error {
	var first error
	if s.httpSrv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.httpSrv.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, context.DeadlineExceeded) {
			first = err
		}
	}
	if s.unixLn != nil {
		_ = s.unixLn.Close()
	}
	if s.tcpLn != nil {
		_ = s.tcpLn.Close()
	}
	_ = os.Remove(SocketPath(s.store.townRoot))
	_ = os.Remove(PortPath(s.store.townRoot))
	return first
}

func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/hello", s.handleHello)
	mux.HandleFunc("/v1/lifecycle", s.handleLifecycle)
	mux.HandleFunc("/v1/telemetry", s.handleTelemetry)
	mux.HandleFunc("/v1/authorize", s.handleAuthorize)
	mux.HandleFunc("/v1/health", s.handleHealth)
	mux.HandleFunc("/v1/poll", s.handlePoll)
	mux.HandleFunc("/v1/prompt-ack", s.handlePromptAck)
	mux.HandleFunc("/v1/town", s.handleTown)
	return mux
}

func (s *Server) handleHello(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var env Envelope
	if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
		http.Error(w, "bad hello", http.StatusBadRequest)
		return
	}
	if env.RunID == "" || env.SessionID == "" {
		http.Error(w, "run_id and session_id required", http.StatusBadRequest)
		return
	}
	s.attach(env.RunID, env.SessionID)
	if err := s.writeSessionPort(env.SessionID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "run_id": env.RunID})
}

func (s *Server) handleLifecycle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	lc, err := decodeBody[Lifecycle](r.Body)
	if err != nil {
		http.Error(w, "bad lifecycle", http.StatusBadRequest)
		return
	}
	if err := s.applyLifecycle(lc); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleTelemetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	batch, err := decodeBody[TelemetryBatch](r.Body)
	if err != nil {
		http.Error(w, "bad telemetry", http.StatusBadRequest)
		return
	}
	if err := s.applyTelemetry(batch); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	req, err := decodeBody[AuthorizeRequest](r.Body)
	if err != nil {
		writeJSON(w, http.StatusOK, AuthorizeDecision{Allowed: false, Reason: "bad authorize request"})
		return
	}
	state := StateUnknown
	if run, err := s.store.getRun(req.RunID); err == nil {
		state = run.State
	}
	writeJSON(w, http.StatusOK, DecideAuthorize(state, req))
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h, err := decodeBody[Health](r.Body)
		if err != nil {
			http.Error(w, "bad health", http.StatusBadRequest)
			return
		}
		if err := s.applyHealth(h); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case http.MethodGet:
		runID := r.URL.Query().Get("run_id")
		h, err := s.healthOf(runID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, h)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handlePoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	runID := r.URL.Query().Get("run_id")
	conn := s.conn(runID)
	if conn == nil {
		http.Error(w, "not connected", http.StatusNotFound)
		return
	}
	s.touch(runID)
	timer := time.NewTimer(pollTimeout)
	defer timer.Stop()
	select {
	case env := <-conn.outbound:
		writeJSON(w, http.StatusOK, env)
	case <-timer.C:
		writeJSON(w, http.StatusOK, Envelope{Kind: KindHealth, RunID: runID})
	case <-r.Context().Done():
	}
}

func (s *Server) handlePromptAck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	d, err := decodeBody[Delivery](r.Body)
	if err != nil {
		http.Error(w, "bad ack", http.StatusBadRequest)
		return
	}
	conn := s.conn(d.RunID)
	if conn != nil {
		select {
		case conn.ack <- d:
		default:
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleTown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	req, err := decodeBody[TownRequest](r.Body)
	if err != nil {
		writeJSON(w, http.StatusOK, TownResponse{OK: false, Error: "bad town request"})
		return
	}
	resp := s.dispatchTown(r.Context(), req)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) dispatchTown(ctx context.Context, req TownRequest) TownResponse {
	switch req.Op {
	case opPing:
		return TownResponse{OK: true}
	case opStartRun:
		spec, err := decodePayload[StartSpec](req.Payload)
		if err != nil {
			return failTown(err)
		}
		run, err := s.startRun(spec)
		if err != nil {
			return failTown(err)
		}
		return okTown(run)
	case opDeliver:
		p, err := decodePayload[Prompt](req.Payload)
		if err != nil {
			return failTown(err)
		}
		d, err := s.deliver(ctx, p)
		if err != nil {
			return failTown(err)
		}
		return okTown(d)
	case opState:
		st, err := s.stateOf(req.RunID, req.Session)
		if err != nil {
			return failTown(err)
		}
		return okTown(st)
	case opHealth:
		h, err := s.healthOf(firstNonEmpty(req.RunID, req.Session))
		if err != nil {
			return failTown(err)
		}
		return okTown(h)
	case opKill:
		if err := s.kill(req.RunID, req.Session); err != nil {
			return failTown(err)
		}
		return TownResponse{OK: true}
	case opIdentity:
		id, err := decodePayload[Identity](req.Payload)
		if err != nil {
			return failTown(err)
		}
		if err := s.pushIdentity(id); err != nil {
			return failTown(err)
		}
		return TownResponse{OK: true}
	case opContext:
		push, err := decodePayload[ContextPush](req.Payload)
		if err != nil {
			return failTown(err)
		}
		if err := s.pushContext(push); err != nil {
			return failTown(err)
		}
		return TownResponse{OK: true}
	case opLiveBead:
		run, err := s.store.liveRunForBead(req.BeadID)
		if err != nil {
			return failTown(err)
		}
		return okTown(run)
	case opEvents:
		ev, err := s.store.readEvents()
		if err != nil {
			return failTown(err)
		}
		return okTown(ev)
	case opCosts:
		costs, err := s.store.readCosts()
		if err != nil {
			return failTown(err)
		}
		return okTown(costs)
	case opWaitReady:
		if err := s.waitReady(ctx, req.RunID); err != nil {
			return failTown(err)
		}
		return TownResponse{OK: true}
	case opExpireQueue:
		expired, err := s.store.expireStale(nowUTC())
		if err != nil {
			return failTown(err)
		}
		return okTown(expired)
	default:
		return TownResponse{OK: false, Error: "unknown op"}
	}
}

func (s *Server) startRun(spec StartSpec) (*Run, error) {
	if spec.RunID == "" {
		spec.RunID = uuid.NewString()
	}
	if spec.SessionID == "" {
		return nil, fmt.Errorf("session_id is required")
	}
	if spec.BeadID != "" {
		if live, err := s.store.liveRunForBead(spec.BeadID); err != nil {
			return nil, err
		} else if live != nil && live.RunID != spec.RunID {
			return nil, fmt.Errorf("%w: %s is %s", ErrLiveRun, spec.BeadID, live.RunID)
		}
		if latest, err := s.store.latestRunForBead(spec.BeadID); err == nil && latest != nil && !latest.State.known() {
			return nil, fmt.Errorf("%w: bead %s has unknown state; refusing extra spawn", ErrUnknownState, spec.BeadID)
		}
	}
	if spec.SessionID != "" {
		if existing, err := s.store.latestRunForSession(spec.SessionID); err == nil && existing != nil && existing.RunID != spec.RunID {
			if !existing.State.known() {
				return nil, fmt.Errorf("%w: session %s has unknown state; refusing extra spawn", ErrUnknownState, spec.SessionID)
			}
			if existing.State.live() {
				return nil, fmt.Errorf("session %s already has live run %s", spec.SessionID, existing.RunID)
			}
		}
	}
	now := nowUTC()
	run := &Run{
		RunID:     spec.RunID,
		SessionID: spec.SessionID,
		BeadID:    spec.BeadID,
		Role:      spec.Role,
		Rig:       spec.Rig,
		AgentName: spec.AgentName,
		AgentType: spec.AgentType,
		State:     StateStarted,
		StartedAt: now,
		UpdatedAt: now,
	}
	if s.connected(spec.RunID) {
		run.Adapter = AdapterProtocol
	} else {
		run.Adapter = AdapterTmux
	}
	if err := s.store.putRun(run); err != nil {
		return nil, err
	}
	_ = s.store.appendEvent(Event{
		Type: EventStarted, RunID: run.RunID, BeadID: run.BeadID,
		SessionID: run.SessionID, Timestamp: now,
		Payload: map[string]any{"role": run.Role, "agent_type": run.AgentType},
	})
	return run, nil
}

func (s *Server) applyLifecycle(lc Lifecycle) error {
	if lc.RunID == "" {
		return fmt.Errorf("run_id is required")
	}
	st := stateFromEvent(lc.Event)
	if !st.known() {
		return fmt.Errorf("unknown lifecycle event %q", lc.Event)
	}
	run, err := s.store.getRun(lc.RunID)
	if err != nil {
		if !errors.Is(err, ErrRunNotFound) {
			return err
		}
		run = &Run{RunID: lc.RunID, SessionID: lc.SessionID, StartedAt: nowUTC()}
	}
	run.State = st
	run.UpdatedAt = nowUTC()
	if lc.SessionID != "" {
		run.SessionID = lc.SessionID
	}
	if run.BeadID == "" && lc.Metadata != nil {
		if b, ok := lc.Metadata["bead_id"].(string); ok {
			run.BeadID = b
		}
	}
	if st == StateStopped {
		run.StoppedAt = run.UpdatedAt
		if lc.Metadata != nil {
			if v, ok := lc.Metadata["exit_code"]; ok {
				switch n := v.(type) {
				case float64:
					code := int(n)
					run.ExitCode = &code
				case int:
					run.ExitCode = &n
				}
			}
			if done, ok := lc.Metadata["done"].(bool); ok {
				run.Done = done
			}
		}
	}
	if s.connected(lc.RunID) {
		run.Adapter = AdapterProtocol
	}
	if err := s.store.putRun(run); err != nil {
		return err
	}
	_ = s.store.appendEvent(Event{
		Type: lc.Event, RunID: run.RunID, BeadID: run.BeadID,
		SessionID: run.SessionID, Timestamp: run.UpdatedAt,
		Payload: lc.Metadata,
	})
	if st == StateIdle {
		go s.drainQueue(lc.RunID)
	}
	return nil
}

func (s *Server) applyTelemetry(batch TelemetryBatch) error {
	if batch.RunID == "" {
		return fmt.Errorf("run_id is required")
	}
	run, err := s.store.getRun(batch.RunID)
	if err != nil {
		return err
	}
	for _, ev := range batch.Events {
		if ev.Usage == nil {
			continue
		}
		rec := CostRecord{
			RunID:     run.RunID,
			BeadID:    run.BeadID,
			Role:      run.Role,
			Rig:       run.Rig,
			AgentName: run.AgentName,
			AgentType: run.AgentType,
			SessionID: run.SessionID,
			CostUSD:   ev.Usage.CostUSD,
			Model:     ev.Usage.Model,
			Timestamp: ev.Timestamp,
		}
		if rec.Timestamp.IsZero() {
			rec.Timestamp = nowUTC()
		}
		if err := s.store.appendCost(rec); err != nil {
			return err
		}
		_ = s.store.appendEvent(Event{
			Type: "telemetry", RunID: run.RunID, BeadID: run.BeadID,
			SessionID: run.SessionID, Timestamp: rec.Timestamp,
			Payload: map[string]any{
				"cost_usd": rec.CostUSD, "agent_type": rec.AgentType, "model": rec.Model,
			},
		})
	}
	return nil
}

func (s *Server) applyHealth(h Health) error {
	if h.RunID == "" {
		return fmt.Errorf("run_id is required")
	}
	run, err := s.store.getRun(h.RunID)
	if err != nil {
		return err
	}
	run.LastHealth = nowUTC()
	run.UpdatedAt = run.LastHealth
	s.touch(h.RunID)
	return s.store.putRun(run)
}

func (s *Server) deliver(ctx context.Context, p Prompt) (*Delivery, error) {
	if p.Priority == "" {
		p.Priority = PriorityNormal
	}
	if !validPriority(p.Priority) {
		return nil, fmt.Errorf("invalid priority %q", p.Priority)
	}
	run, err := s.resolveRun(p.RunID, "")
	if err != nil {
		if errors.Is(err, ErrRunNotFound) {
			return nil, fmt.Errorf("%w: %s", ErrUnknownState, p.RunID)
		}
		return nil, err
	}
	p.RunID = run.RunID
	if p.BeadID == "" {
		p.BeadID = run.BeadID
	}
	if !run.State.known() {
		return nil, ErrUnknownState
	}
	if run.State == StateStopped {
		return nil, fmt.Errorf("run %s is stopped", run.RunID)
	}

	if s.connected(run.RunID) {
		return s.deliverProtocol(ctx, run, p)
	}
	return s.deliverTmux(run, p)
}

func (s *Server) deliverProtocol(ctx context.Context, run *Run, p Prompt) (*Delivery, error) {
	busy := run.State == StateBusy || run.State == StateStarted
	if busy && (p.Priority == PriorityNormal || p.Priority == PrioritySystem) {
		pos, err := s.enqueuePrompt(p)
		if err != nil {
			return nil, err
		}
		d := &Delivery{Accepted: false, Queued: true, Position: pos, Adapter: AdapterProtocol, RunID: run.RunID}
		_ = s.store.appendEvent(Event{
			Type: "queued", RunID: run.RunID, BeadID: run.BeadID,
			SessionID: run.SessionID, Timestamp: nowUTC(),
			Payload: map[string]any{"source": p.Source, "priority": p.Priority, "position": pos},
		})
		return d, nil
	}
	conn := s.conn(run.RunID)
	if conn == nil {
		return nil, ErrNotConnected
	}
	interrupt := p.Priority == PriorityUrgent
	env := Envelope{Kind: KindPrompt, ID: uuid.NewString(), RunID: run.RunID, SessionID: run.SessionID}
	payload, err := marshalPayload(map[string]any{
		"content":   p.Content,
		"priority":  p.Priority,
		"source":    p.Source,
		"from":      p.From,
		"bead_id":   p.BeadID,
		"interrupt": interrupt,
	})
	if err != nil {
		return nil, err
	}
	env.Payload = payload
	select {
	case conn.outbound <- env:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(s.ackWait):
		return nil, fmt.Errorf("deliver: agent did not accept prompt: %w", context.DeadlineExceeded)
	}
	select {
	case ack := <-conn.ack:
		ack.Adapter = AdapterProtocol
		ack.RunID = run.RunID
		_ = s.store.appendEvent(Event{
			Type: "delivered", RunID: run.RunID, BeadID: run.BeadID,
			SessionID: run.SessionID, Timestamp: nowUTC(),
			Payload: map[string]any{"source": p.Source, "priority": p.Priority, "accepted": ack.Accepted, "queued": ack.Queued},
		})
		return &ack, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(s.ackWait):
		return nil, fmt.Errorf("deliver: agent did not acknowledge prompt: %w", context.DeadlineExceeded)
	}
}

func (s *Server) deliverTmux(run *Run, p Prompt) (*Delivery, error) {
	if s.tmux == nil {
		return nil, fmt.Errorf("%w: tmux adapter unavailable", ErrNotConnected)
	}
	exists, err := s.tmux.HasSession(run.SessionID)
	if err != nil {
		return nil, fmt.Errorf("checking tmux session: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("%w: session %s missing", ErrUnknownState, run.SessionID)
	}
	if p.Priority == PriorityNormal || p.Priority == PrioritySystem {
		if run.State == StateBusy {
			pos, err := s.enqueuePrompt(p)
			if err != nil {
				return nil, err
			}
			return &Delivery{Queued: true, Position: pos, Adapter: AdapterTmux, RunID: run.RunID}, nil
		}
	}
	if err := s.tmux.NudgeSession(run.SessionID, p.Content); err != nil {
		return nil, fmt.Errorf("tmux deliver: %w", err)
	}
	_ = s.store.appendEvent(Event{
		Type: "delivered", RunID: run.RunID, BeadID: run.BeadID,
		SessionID: run.SessionID, Timestamp: nowUTC(),
		Payload: map[string]any{"source": p.Source, "adapter": AdapterTmux},
	})
	return &Delivery{Accepted: true, Adapter: AdapterTmux, RunID: run.RunID}, nil
}

func (s *Server) enqueuePrompt(p Prompt) (int, error) {
	ttl := normalTTL
	if p.Priority == PriorityUrgent {
		ttl = urgentTTL
	}
	item := QueuedPrompt{
		ID:        uuid.NewString(),
		Prompt:    p,
		Enqueued:  nowUTC(),
		ExpiresAt: nowUTC().Add(ttl),
	}
	pos, err := s.store.enqueue(item)
	if err != nil {
		return 0, err
	}
	if s.store.townRoot != "" && p.RunID != "" {
		if run, err := s.store.getRun(p.RunID); err == nil && run.SessionID != "" {
			_ = nudge.Enqueue(s.store.townRoot, run.SessionID, nudge.QueuedNudge{
				Sender:    p.From,
				Message:   p.Content,
				Priority:  p.Priority,
				Kind:      p.Source,
				Timestamp: item.Enqueued,
				ExpiresAt: item.ExpiresAt,
			})
		}
	}
	return pos, nil
}

func (s *Server) drainQueue(runID string) {
	due, err := s.store.drainDue(runID)
	if err != nil || len(due) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.ackWait)
	defer cancel()
	for _, item := range due {
		_, _ = s.deliverProtocol(ctx, mustRun(s, runID), item.Prompt)
	}
}

func mustRun(s *Server, runID string) *Run {
	run, err := s.store.getRun(runID)
	if err != nil {
		return &Run{RunID: runID, State: StateIdle}
	}
	return run
}

func (s *Server) stateOf(runID, sessionID string) (State, error) {
	run, err := s.resolveRun(runID, sessionID)
	if err != nil {
		return StateUnknown, err
	}
	if s.connected(run.RunID) {
		return run.State, nil
	}
	if run.State.known() {
		return run.State, nil
	}
	return StateUnknown, nil
}

func (s *Server) healthOf(id string) (*Health, error) {
	run, err := s.resolveRun(id, id)
	if err != nil {
		return nil, err
	}
	h := &Health{
		RunID:        run.RunID,
		CurrentState: string(run.State),
		LastActivity: run.UpdatedAt,
	}
	if run.State == StateUnknown {
		h.Status = HealthUnhealthy
		h.Error = ErrUnknownState.Error()
		return h, nil
	}
	if s.connected(run.RunID) {
		if !run.LastHealth.IsZero() && time.Since(run.LastHealth) > s.healthGrace {
			h.Status = HealthUnhealthy
			h.Error = ErrUnhealthy.Error()
			return h, nil
		}
		if run.LastHealth.IsZero() && time.Since(run.UpdatedAt) > s.healthGrace && run.State != StateStarted {
			h.Status = HealthUnhealthy
			h.Error = ErrUnhealthy.Error()
			return h, nil
		}
		h.Status = HealthHealthy
		return h, nil
	}
	if s.tmux != nil {
		alive := s.tmux.IsAgentAlive(run.SessionID)
		if !alive {
			h.Status = HealthUnhealthy
			h.Error = "tmux agent not alive"
			return h, nil
		}
		h.Status = HealthHealthy
		return h, nil
	}
	h.Status = HealthUnhealthy
	h.Error = ErrUnhealthy.Error()
	return h, nil
}

func (s *Server) kill(runID, sessionID string) error {
	run, err := s.resolveRun(runID, sessionID)
	if err != nil {
		return err
	}
	if !run.State.known() {
		return ErrUnknownState
	}
	if s.connected(run.RunID) {
		conn := s.conn(run.RunID)
		if conn != nil {
			env := Envelope{Kind: KindLifecycle, RunID: run.RunID, SessionID: run.SessionID}
			env.Payload, _ = marshalPayload(map[string]any{"event": EventStopping})
			select {
			case conn.outbound <- env:
			default:
			}
		}
		return nil
	}
	if s.tmux == nil {
		return ErrUnknownState
	}
	if err := s.tmux.KillSessionWithProcesses(run.SessionID); err != nil {
		return err
	}
	return markRunStopped(s.store, run)
}

func (s *Server) pushIdentity(id Identity) error {
	if _, err := s.store.getRun(id.RunID); err != nil {
		return err
	}
	conn := s.conn(id.RunID)
	if conn == nil {
		return nil
	}
	env := Envelope{Kind: KindIdentity, RunID: id.RunID, SessionID: id.SessionID}
	var err error
	env.Payload, err = marshalPayload(id)
	if err != nil {
		return err
	}
	select {
	case conn.outbound <- env:
	default:
		conn.identity = &id
	}
	return nil
}

func (s *Server) pushContext(push ContextPush) error {
	if _, err := s.store.getRun(push.RunID); err != nil {
		return err
	}
	conn := s.conn(push.RunID)
	if conn == nil {
		return nil
	}
	env := Envelope{Kind: KindContext, RunID: push.RunID}
	var err error
	env.Payload, err = marshalPayload(push)
	if err != nil {
		return err
	}
	select {
	case conn.outbound <- env:
	default:
		conn.contextPush = &push
	}
	return nil
}

func (s *Server) waitReady(ctx context.Context, runID string) error {
	deadline := time.Now().Add(s.readyWait)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	for time.Now().Before(deadline) {
		run, err := s.store.getRun(runID)
		if err != nil {
			return err
		}
		if run.State == StateReady || run.State == StateIdle {
			return nil
		}
		if run.State == StateStopped {
			return fmt.Errorf("run %s stopped before ready", runID)
		}
		if !s.connected(runID) && s.tmux != nil && run.Adapter == AdapterTmux {
			return s.tmux.WaitForRuntimeReady(run.SessionID, time.Until(deadline))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	return fmt.Errorf("wait ready: timeout for %s", runID)
}

func (s *Server) resolveRun(runID, sessionID string) (*Run, error) {
	if runID != "" {
		if run, err := s.store.getRun(runID); err == nil {
			return run, nil
		} else if !errors.Is(err, ErrRunNotFound) {
			return nil, err
		}
	}
	if sessionID != "" {
		return s.store.getRunBySession(sessionID)
	}
	if runID != "" {
		return s.store.getRunBySession(runID)
	}
	return nil, ErrRunNotFound
}

func (s *Server) attach(runID, sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	conn, ok := s.conns[runID]
	if !ok {
		conn = &agentConn{
			runID:     runID,
			sessionID: sessionID,
			outbound:  make(chan Envelope, 8),
			ack:       make(chan Delivery, 4),
		}
		s.conns[runID] = conn
	}
	conn.connected = true
	conn.lastSeen = nowUTC()
	conn.sessionID = sessionID
	if run, err := s.store.getRun(runID); err == nil {
		run.Adapter = AdapterProtocol
		run.SessionID = sessionID
		_ = s.store.putRunLocked(run)
	}
}

func (s *Server) connected(runID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	conn, ok := s.conns[runID]
	return ok && conn.connected
}

func (s *Server) conn(runID string) *agentConn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns[runID]
}

func (s *Server) touch(runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if conn, ok := s.conns[runID]; ok {
		conn.lastSeen = nowUTC()
		conn.connected = true
	}
}

func (s *Server) writeSessionPort(sessionID string) error {
	if s.port == 0 {
		return nil
	}
	path := SessionPortPath(s.store.townRoot, sessionID)
	if err := os.MkdirAll(filepath.Dir(path), runtimeMode); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.Itoa(s.port)+"\n"), fileMode)
}

func decodeBody[T any](r io.Reader) (T, error) {
	var v T
	if err := json.NewDecoder(r).Decode(&v); err != nil {
		return v, err
	}
	return v, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) { //nolint:unparam // status kept for error paths
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func failTown(err error) TownResponse {
	return TownResponse{OK: false, Error: err.Error()}
}

func okTown(v any) TownResponse {
	payload, err := marshalPayload(v)
	if err != nil {
		return failTown(err)
	}
	return TownResponse{OK: true, Payload: payload}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
