// This file holds the agent API (spec §8): the /agent/v1/ HTTP routes that
// enrolled machines call. Every route except enroll requires
// "Authorization: Bearer <token>".
package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/agentlink"
)

const (
	agentMaxBodyLarge = 8 << 20 // snapshot, events
	agentMaxBodySmall = 1 << 20 // everything else
	agentMaxBatch     = 500     // events per POST /events, results per POST /results

	defaultMaxWait = 30 * time.Second
	defaultRecheck = 2 * time.Second
)

// AgentAPI serves /agent/v1/. Create it with NewAgentAPI.
type AgentAPI struct {
	store  *Store
	now    func() time.Time
	enroll http.Handler

	// maxWait caps the ?wait= of GET /requests. recheck is the fallback
	// interval at which a long-poll re-reads the database even without a
	// wake, so a missed notification cannot strand a request.
	maxWait time.Duration
	recheck time.Duration
}

// NewAgentAPI returns the handler for every /agent/v1/ route, including
// POST /agent/v1/enroll (delegated to st.EnrollHandler). now is the clock
// used wherever the API reasons about time; nil means time.Now. trusted lists
// reverse proxies whose X-Forwarded-For identifies the client for the enroll
// limiter (see clientIP).
func NewAgentAPI(st *Store, now func() time.Time, trusted ...netip.Prefix) http.Handler {
	if now == nil {
		now = time.Now
	}
	return &AgentAPI{
		store:   st,
		now:     now,
		enroll:  st.EnrollHandler(now, trusted...),
		maxWait: defaultMaxWait,
		recheck: defaultRecheck,
	}
}

// Notify wakes any GET /requests long-poll of machineID. Store.EnqueueRequest
// already notifies; call this after queueing requests by other means.
func (a *AgentAPI) Notify(machineID string) { a.store.notifier.notify(machineID) }

type agentRoute struct {
	method  string
	handler func(a *AgentAPI, w http.ResponseWriter, r *http.Request, m *Machine)
}

var agentRoutes = map[string]agentRoute{
	"/agent/v1/hello":     {http.MethodPost, (*AgentAPI).hello},
	"/agent/v1/snapshot":  {http.MethodPost, (*AgentAPI).snapshot},
	"/agent/v1/events":    {http.MethodPost, (*AgentAPI).events},
	"/agent/v1/requests":  {http.MethodGet, (*AgentAPI).requests},
	"/agent/v1/results":   {http.MethodPost, (*AgentAPI).results},
	"/agent/v1/heartbeat": {http.MethodPost, (*AgentAPI).heartbeat},
}

func (a *AgentAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/agent/v1/enroll" {
		a.enroll.ServeHTTP(w, r)
		return
	}
	rt, ok := agentRoutes[r.URL.Path]
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	m := a.authenticate(r)
	if m == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	// last_seen_at is set after the handler so it uses the injected clock
	// even when a store write stamped it with the wall clock. Best effort.
	defer func() { _ = a.store.TouchMachine(m.ID, a.now()) }()
	if r.Method != rt.method {
		w.Header().Set("Allow", rt.method)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	rt.handler(a, w, r, m)
}

// authenticate returns the machine for the request's Bearer token, or nil for
// a missing, malformed, unknown or revoked token. The token is never logged.
func (a *AgentAPI) authenticate(r *http.Request) *Machine {
	scheme, tok, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || tok == "" || strings.ContainsAny(tok, " \t") {
		return nil
	}
	m, err := a.store.MachineByToken(sha256Hex(tok))
	if err != nil {
		return nil
	}
	return m
}

// decodeBody decodes exactly one JSON value of at most limit bytes into v. On
// failure it writes the error response (413 body_too_large or 400
// bad_request) and returns false.
func decodeBody(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	err := dec.Decode(v)
	if err == nil {
		if extra := dec.Decode(&json.RawMessage{}); extra != io.EOF {
			err = extra
			if err == nil {
				err = errors.New("trailing data")
			}
		}
	}
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "body_too_large")
		} else {
			writeError(w, http.StatusBadRequest, "bad_request")
		}
		return false
	}
	return true
}

// hello: POST /agent/v1/hello, body Hello, returns {"resync": bool}.
func (a *AgentAPI) hello(w http.ResponseWriter, r *http.Request, m *Machine) {
	var h agentlink.Hello
	if !decodeBody(w, r, agentMaxBodySmall, &h) {
		return
	}
	if h.MachineID != m.ID {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if !sameProtocolMajor(h.Protocol, agentlink.ProtocolVersion) {
		writeError(w, http.StatusBadRequest, "protocol_mismatch")
		return
	}
	if err := a.store.UpsertMachine(m.ID, h); err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"resync": !m.HasSnapshot})
}

// snapshot: POST /agent/v1/snapshot, body Snapshot, returns {"cursor": c}.
func (a *AgentAPI) snapshot(w http.ResponseWriter, r *http.Request, m *Machine) {
	var snap agentlink.Snapshot
	if !decodeBody(w, r, agentMaxBodyLarge, &snap) {
		return
	}
	if err := a.store.ApplySnapshot(m.ID, snap); err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"cursor": snap.Cursor})
}

type eventsBatch struct {
	After  string            `json:"after"`
	Events []agentlink.Event `json:"events"`
}

// events: POST /agent/v1/events, body {"after", "events"}, returns
// {"cursor": lastID}.
//
// after must equal the stored cursor (both may be empty), else 409 resync.
// The check and the append run in one store transaction. A retry of a batch
// that was applied but whose response was lost carries the old after, so it
// gets 409 rather than being re-applied; the machine then resyncs with a
// snapshot. (Even without the check, AppendEvents is idempotent per event ID.)
func (a *AgentAPI) events(w http.ResponseWriter, r *http.Request, m *Machine) {
	var b eventsBatch
	if !decodeBody(w, r, agentMaxBodyLarge, &b) {
		return
	}
	if len(b.Events) > agentMaxBatch {
		writeError(w, http.StatusRequestEntityTooLarge, "too_many_events")
		return
	}
	prev := b.After
	for _, ev := range b.Events {
		if ev.ID <= prev { // also rejects an empty ID
			writeError(w, http.StatusBadRequest, "bad_request")
			return
		}
		prev = ev.ID
	}
	last, err := a.store.AppendEventsAfter(m.ID, b.After, b.Events)
	switch {
	case errors.Is(err, ErrCursorMismatch):
		writeError(w, http.StatusConflict, "resync")
	case errors.Is(err, ErrInvalidEvent):
		writeError(w, http.StatusBadRequest, "bad_request")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal")
	default:
		writeJSON(w, http.StatusOK, map[string]string{"cursor": last})
	}
}

// hasUndelivered reports whether any row is still pending (not yet delivered).
func hasUndelivered(rows []pendingRequestRow) bool {
	for _, r := range rows {
		if r.Status == RequestPending {
			return true
		}
	}
	return false
}

// effectiveWait is min(wait, maxWait).
func (a *AgentAPI) effectiveWait(wait time.Duration) time.Duration {
	return min(wait, a.maxWait)
}

// requests: GET /agent/v1/requests?wait=<duration>, returns []Request.
//
// It returns at once if the machine has at least one request still in status
// pending. Requests that were already delivered but have no result do not end
// the wait (R33): otherwise every poll would return at once and the agent
// would spin against the database until they expired. It waits until a
// request is enqueued for the machine, the wait (capped at maxWait) elapses,
// or the client goes away, then returns the full set, re-deliveries included,
// so a lost response is still re-delivered. No database handle is held while
// waiting. Returned requests are marked delivered; they stay deliverable
// until they have a result or expire.
//
// Contract (Task 14): the agent posts a Result for every delivered request it
// receives, including ones it drops, so delivered requests do not linger.
func (a *AgentAPI) requests(w http.ResponseWriter, r *http.Request, m *Machine) {
	var wait time.Duration
	if s := r.URL.Query().Get("wait"); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil || d < 0 {
			writeError(w, http.StatusBadRequest, "bad_request")
			return
		}
		wait = a.effectiveWait(d)
	}

	// Subscribe before the first read so a wake between read and wait is
	// not lost.
	wake, unsubscribe := a.store.notifier.subscribe(m.ID)
	defer unsubscribe()

	rows, err := a.store.pendingRequestRows(m.ID, a.now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	if !hasUndelivered(rows) && wait > 0 {
		recheck := a.recheck
		if recheck <= 0 {
			recheck = defaultRecheck
		}
		timer := time.NewTimer(wait)
		defer timer.Stop()
		ticker := time.NewTicker(recheck)
		defer ticker.Stop()
		for timedOut := false; !timedOut; {
			select {
			case <-r.Context().Done():
				return // client gone; nothing to deliver
			case <-timer.C:
				timedOut = true // re-read once more: return the current full set
			case <-wake:
			case <-ticker.C:
			}
			if rows, err = a.store.pendingRequestRows(m.ID, a.now()); err != nil {
				writeError(w, http.StatusInternalServerError, "internal")
				return
			}
			if hasUndelivered(rows) {
				break
			}
		}
	}
	reqs := make([]agentlink.Request, len(rows))
	for i, row := range rows {
		reqs[i] = row.Request
	}
	keys := make([]string, len(reqs))
	for i, q := range reqs {
		keys[i] = q.Key
	}
	if err := a.store.MarkDelivered(m.ID, keys); err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, reqs)
}

// results: POST /agent/v1/results, body []Result, returns
// {"accepted": n, "ignored": k}. Results for keys the machine has no request
// for are ignored and counted, not an error.
func (a *AgentAPI) results(w http.ResponseWriter, r *http.Request, m *Machine) {
	var rs []agentlink.Result
	if !decodeBody(w, r, agentMaxBodySmall, &rs) {
		return
	}
	if len(rs) > agentMaxBatch {
		writeError(w, http.StatusRequestEntityTooLarge, "too_many_results")
		return
	}
	accepted, ignored := 0, 0
	for _, res := range rs {
		err := a.store.addResult(m.ID, res, a.now())
		switch {
		case errors.Is(err, ErrNotFound):
			ignored++
		case err != nil:
			writeError(w, http.StatusInternalServerError, "internal")
			return
		default:
			accepted++
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"accepted": accepted, "ignored": ignored})
}

// heartbeat: POST /agent/v1/heartbeat, returns {"ok": true}. last_seen_at is
// touched by ServeHTTP.
func (a *AgentAPI) heartbeat(w http.ResponseWriter, _ *http.Request, _ *Machine) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// requestNotifier wakes long-polls waiting on a machine's requests. Each
// waiter has a 1-buffered channel, so a notify never blocks and a wake that
// arrives before the waiter selects is kept.
type requestNotifier struct {
	mu      sync.Mutex
	waiters map[string]map[chan struct{}]struct{}
}

func newRequestNotifier() *requestNotifier {
	return &requestNotifier{waiters: map[string]map[chan struct{}]struct{}{}}
}

func (n *requestNotifier) subscribe(machineID string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	n.mu.Lock()
	set := n.waiters[machineID]
	if set == nil {
		set = map[chan struct{}]struct{}{}
		n.waiters[machineID] = set
	}
	set[ch] = struct{}{}
	n.mu.Unlock()
	return ch, func() {
		n.mu.Lock()
		delete(set, ch)
		// set is still n.waiters[machineID]: a set is only dropped once empty,
		// and it held ch until now.
		if len(set) == 0 {
			delete(n.waiters, machineID)
		}
		n.mu.Unlock()
	}
}

func (n *requestNotifier) notify(machineID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for ch := range n.waiters[machineID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
