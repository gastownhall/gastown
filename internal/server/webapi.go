// This file holds the browser API (spec §8): the /api/v1/ routes the
// dashboard calls. Every route needs a dashboard session cookie; Bearer
// tokens (machine tokens) never grant access. Mutating routes also need the
// session's X-CSRF-Token.
package server

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/agentlink"
)

const (
	webMaxBody = 64 << 10
	// staleAfter is the freshness beyond which a machine's values are stale.
	staleAfter = 120 * time.Second
	// requestTTL is the expiry of requests the dashboard enqueues.
	requestTTL = 24 * time.Hour
	// peekTTL is the expiry of a terminal.peek request.
	peekTTL = 60 * time.Second
	// itemEventLimit is how many events GET /items/{m}/{id} returns.
	itemEventLimit = 200
	maxTitleLen    = 500
)

// laneOrder is the fixed board lane order (task, fix and epic steps).
var laneOrder = []string{"spec", "plan", "execute", "verify", "review", "merge", "report", "reproduce", "rca", "fix", "epic", "breakdown", "tasks", "verification", "done"}

var (
	// Mirrors workflow.ValidateID: ^[A-Za-z0-9][A-Za-z0-9._-]*$ and no "..".
	webIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,199}$`)
	// Mirrors workflow.ValidateArtifactName.
	webArtifactRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	// Mirrors the applier's rig name check.
	webRigRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,100}$`)
	webSHARe = regexp.MustCompile(`^[0-9a-f]{64}$`)
	webKinds = map[string]bool{"task": true, "fix": true, "epic": true}
)

func validID(s string) bool { return webIDRe.MatchString(s) && !strings.Contains(s, "..") }

// webItem is an item as the browser sees it.
type webItem struct {
	agentlink.ItemView
	MachineID        string    `json:"machine_id"`
	UpdatedAt        time.Time `json:"updated_at"`
	LastSeenAt       time.Time `json:"last_seen_at"`
	FreshnessSeconds int64     `json:"freshness_seconds"`
	Stale            bool      `json:"stale"`
}

// freshness returns now - seen in whole seconds (never negative) and whether
// that is beyond staleAfter.
func freshness(now, seen time.Time) (int64, bool) {
	d := now.Sub(seen)
	if d < 0 {
		d = 0
	}
	return int64(d / time.Second), d > staleAfter
}

func (a *App) toWebItem(r ItemRow, now time.Time) webItem {
	f, stale := freshness(now, r.MachineLastSeenAt)
	return webItem{ItemView: r.ItemView, MachineID: r.MachineID, UpdatedAt: r.UpdatedAt, LastSeenAt: r.MachineLastSeenAt, FreshnessSeconds: f, Stale: stale}
}

// Route flags.
const (
	routeMutating = 1 << iota // needs X-CSRF-Token
	routeNoBearer             // refused with 403 when any Authorization header is present
)

type apiHandler func(a *App, w http.ResponseWriter, r *http.Request, sessionID string)

func (a *App) apiMux() *http.ServeMux {
	mux := http.NewServeMux()
	add := func(pattern string, flags int, h apiHandler) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) { a.guard(w, r, flags, h) })
	}
	add("GET /api/v1/me", 0, (*App).me)
	add("GET /api/v1/board", 0, (*App).board)
	add("GET /api/v1/items/{machine}/{id}", 0, (*App).itemDetail)
	add("POST /api/v1/items/{machine}/{id}/approve", routeMutating|routeNoBearer, (*App).approve)
	add("POST /api/v1/items/{machine}/{id}/request-changes", routeMutating|routeNoBearer, (*App).requestChanges)
	add("POST /api/v1/items/{machine}/{id}/sling", routeMutating, (*App).sling)
	add("POST /api/v1/items/{machine}/{id}/cancel", routeMutating, (*App).cancel)
	add("POST /api/v1/items", routeMutating, (*App).createItem)
	add("GET /api/v1/items/{machine}/{id}/terminal", 0, (*App).terminal)
	add("GET /api/v1/requests/{machine}/{key}", 0, (*App).requestStatus)
	add("GET /api/v1/machines", 0, (*App).machines)
	add("DELETE /api/v1/machines/{id}", routeMutating, (*App).revokeMachine)
	add("POST /api/v1/enroll-codes", routeMutating, (*App).enrollCode)
	add("GET /api/v1/events", 0, (*App).events)
	return mux
}

// serveAPI routes /api/v1/*. Unknown paths (and wrong methods) get a JSON
// 404 rather than the SPA.
func (a *App) serveAPI(w http.ResponseWriter, r *http.Request) {
	if _, pat := a.mux.Handler(r); pat == "" {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	a.mux.ServeHTTP(w, r)
}

// hasAuthorization reports whether the request carries any non-blank
// Authorization header.
func hasAuthorization(r *http.Request) bool {
	for _, v := range r.Header.Values("Authorization") {
		if strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}

// guard enforces auth for one route: refusal of any Authorization header
// (approvals), then a valid
// session cookie (401), then CSRF for mutating routes (403).
func (a *App) guard(w http.ResponseWriter, r *http.Request, flags int, h apiHandler) {
	if flags&routeNoBearer != 0 {
		// Any Authorization header at all, whatever its scheme, case or
		// separator: approvals come only from a browser session.
		if hasAuthorization(r) {
			writeError(w, http.StatusForbidden, "bearer_not_allowed")
			return
		}
	}
	sid := a.session(r)
	if sid == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if flags&routeMutating != 0 && !a.checkCSRF(r, sid) {
		writeError(w, http.StatusForbidden, "csrf")
		return
	}
	h(a, w, r, sid)
}

type lane struct {
	Step  string    `json:"step"`
	Items []webItem `json:"items"`
}

// board: GET /api/v1/board?machine=&rig=&kind=.
func (a *App) board(w http.ResponseWriter, r *http.Request, _ string) {
	q := r.URL.Query()
	rows, err := a.store.Items(ItemFilter{MachineID: q.Get("machine"), Rig: q.Get("rig"), Kind: q.Get("kind")})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	now := a.now()
	byStep := map[string][]webItem{}
	needs := []webItem{}
	for _, row := range rows {
		it := a.toWebItem(row, now)
		byStep[it.Step] = append(byStep[it.Step], it)
		if it.WaitingForHuman != "" {
			needs = append(needs, it)
		}
	}
	sort.SliceStable(needs, func(i, j int) bool { return needs[i].UpdatedAt.Before(needs[j].UpdatedAt) })
	lanes := []lane{}
	for _, step := range laneOrder {
		if items := byStep[step]; len(items) > 0 {
			lanes = append(lanes, lane{Step: step, Items: items})
			delete(byStep, step)
		}
	}
	// Steps outside laneOrder are not dropped: they follow, by name.
	var rest []string
	for step := range byStep {
		rest = append(rest, step)
	}
	sort.Strings(rest)
	for _, step := range rest {
		lanes = append(lanes, lane{Step: step, Items: byStep[step]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"lanes": lanes, "needs_you": needs})
}

// itemDetail: GET /api/v1/items/{machine}/{id}.
func (a *App) itemDetail(w http.ResponseWriter, r *http.Request, _ string) {
	mid, id := r.PathValue("machine"), r.PathValue("id")
	row, arts, err := a.store.Item(mid, id)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "item_not_found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	evs, err := a.store.ItemEvents(mid, id, itemEventLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	if arts == nil {
		arts = []agentlink.ArtifactView{}
	}
	diff := row.Diffstat
	if diff == nil {
		diff = []agentlink.FileStat{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"item":      a.toWebItem(*row, a.now()),
		"artifacts": arts,
		"events":    evs,
		"diffstat":  diff,
	})
}

// enqueueTarget checks that the machine exists (404) and is not revoked
// (409), and, when itemID is non-empty, that the item exists (404). On
// failure it writes the response and returns false.
func (a *App) enqueueTarget(w http.ResponseWriter, machineID, itemID string) bool {
	m, err := a.store.Machine(machineID)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "machine_not_found")
		return false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return false
	}
	if m.RevokedAt != nil {
		writeError(w, http.StatusConflict, "machine_revoked")
		return false
	}
	if itemID == "" {
		return true
	}
	if !validID(itemID) {
		writeError(w, http.StatusBadRequest, "invalid_item")
		return false
	}
	if _, _, err := a.store.Item(machineID, itemID); errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "item_not_found")
		return false
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return false
	}
	return true
}

func newRequestKey() (string, error) {
	b, err := randomBytes(16)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// enqueue queues a request for the machine and returns its key.
func (a *App) enqueue(machineID, typ, itemID string, params any, ttl time.Duration) (string, error) {
	key, err := newRequestKey()
	if err != nil {
		return "", err
	}
	var raw json.RawMessage
	if params != nil {
		if raw, err = json.Marshal(params); err != nil {
			return "", err
		}
	}
	now := a.now()
	req := agentlink.Request{Key: key, Type: typ, ItemID: itemID, Params: raw, ExpiresAt: now.Add(ttl)}
	if err := a.store.enqueueRequest(machineID, req, now); err != nil {
		return "", err
	}
	return key, nil
}

// enqueueAndRespond enqueues with the 24h expiry and writes 202 {key, status}.
func (a *App) enqueueAndRespond(w http.ResponseWriter, machineID, typ, itemID string, params any) {
	key, err := a.enqueue(machineID, typ, itemID, params, requestTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"key": key, "status": RequestPending})
}

func (a *App) approve(w http.ResponseWriter, r *http.Request, _ string) {
	a.approval(w, r, "approve")
}

func (a *App) requestChanges(w http.ResponseWriter, r *http.Request, _ string) {
	a.approval(w, r, "request_changes")
}

// approval handles approve and request-changes: body {artifact, sha}. The
// Actor is always "admin"; any actor in the body is ignored.
func (a *App) approval(w http.ResponseWriter, r *http.Request, typ string) {
	mid, id := r.PathValue("machine"), r.PathValue("id")
	var body struct {
		Artifact string `json:"artifact"`
		SHA      string `json:"sha"`
	}
	if !decodeBody(w, r, webMaxBody, &body) {
		return
	}
	if !webArtifactRe.MatchString(body.Artifact) {
		writeError(w, http.StatusBadRequest, "invalid_artifact")
		return
	}
	if !webSHARe.MatchString(body.SHA) {
		writeError(w, http.StatusBadRequest, "invalid_sha")
		return
	}
	if !a.enqueueTarget(w, mid, id) {
		return
	}
	a.enqueueAndRespond(w, mid, typ, id, agentlink.ApproveParams{Artifact: body.Artifact, SHA: body.SHA, Actor: adminLogin})
}

func (a *App) sling(w http.ResponseWriter, r *http.Request, _ string) {
	mid, id := r.PathValue("machine"), r.PathValue("id")
	if !a.enqueueTarget(w, mid, id) {
		return
	}
	a.enqueueAndRespond(w, mid, "item.sling", id, nil)
}

func (a *App) cancel(w http.ResponseWriter, r *http.Request, _ string) {
	mid, id := r.PathValue("machine"), r.PathValue("id")
	if !a.enqueueTarget(w, mid, id) {
		return
	}
	a.enqueueAndRespond(w, mid, "item.cancel", id, nil)
}

// createItem: POST /api/v1/items, body {machine, rig, kind, title, parent}.
func (a *App) createItem(w http.ResponseWriter, r *http.Request, _ string) {
	var body struct {
		Machine string `json:"machine"`
		Rig     string `json:"rig"`
		Kind    string `json:"kind"`
		Title   string `json:"title"`
		Parent  string `json:"parent"`
	}
	if !decodeBody(w, r, webMaxBody, &body) {
		return
	}
	if !validID(body.Machine) {
		writeError(w, http.StatusNotFound, "machine_not_found")
		return
	}
	if !webRigRe.MatchString(body.Rig) {
		writeError(w, http.StatusBadRequest, "invalid_rig")
		return
	}
	if !webKinds[body.Kind] {
		writeError(w, http.StatusBadRequest, "invalid_kind")
		return
	}
	title := strings.Join(strings.Fields(body.Title), " ")
	if title == "" || len(title) > maxTitleLen {
		writeError(w, http.StatusBadRequest, "invalid_title")
		return
	}
	if body.Parent != "" && !validID(body.Parent) {
		writeError(w, http.StatusBadRequest, "invalid_parent")
		return
	}
	if !a.enqueueTarget(w, body.Machine, "") {
		return
	}
	rigs, err := a.store.Rigs(body.Machine)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	known := false
	for _, rg := range rigs {
		known = known || rg.Name == body.Rig
	}
	if !known {
		writeError(w, http.StatusBadRequest, "unknown_rig")
		return
	}
	a.enqueueAndRespond(w, body.Machine, "item.create", "", map[string]string{
		"rig": body.Rig, "kind": body.Kind, "title": title, "parent": body.Parent,
	})
}

// terminal: GET /api/v1/items/{machine}/{id}/terminal enqueues terminal.peek
// (60s expiry), polls its status every termPoll for up to termWait, and
// returns {lines}, or 504 when the machine does not answer in time.
func (a *App) terminal(w http.ResponseWriter, r *http.Request, _ string) {
	mid, id := r.PathValue("machine"), r.PathValue("id")
	if !a.enqueueTarget(w, mid, id) {
		return
	}
	key, err := a.enqueue(mid, "terminal.peek", id, nil, peekTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	deadline := time.NewTimer(a.termWait)
	defer deadline.Stop()
	tick := time.NewTicker(a.termPoll)
	defer tick.Stop()
	for {
		status, res, err := a.store.RequestStatus(mid, key, a.now())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		if res != nil && status == RequestDone {
			if !res.OK {
				writeError(w, http.StatusBadGateway, res.Error)
				return
			}
			var data struct {
				Lines string `json:"lines"`
			}
			_ = json.Unmarshal(res.Data, &data)
			writeJSON(w, http.StatusOK, map[string]string{"lines": data.Lines})
			return
		}
		if status == RequestExpired {
			writeError(w, http.StatusGatewayTimeout, "machine did not answer")
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			writeError(w, http.StatusGatewayTimeout, "machine did not answer")
			return
		case <-tick.C:
		}
	}
}

// requestStatus: GET /api/v1/requests/{machine}/{key} returns {status, result}.
func (a *App) requestStatus(w http.ResponseWriter, r *http.Request, _ string) {
	status, res, err := a.store.RequestStatus(r.PathValue("machine"), r.PathValue("key"), a.now())
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "request_not_found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": status, "result": res})
}

type webMachine struct {
	ID               string          `json:"id"`
	Hostname         string          `json:"hostname"`
	OS               string          `json:"os"`
	Arch             string          `json:"arch"`
	GtbVersion       string          `json:"gtb_version"`
	Protocol         string          `json:"protocol"`
	CreatedAt        time.Time       `json:"created_at"`
	LastSeenAt       time.Time       `json:"last_seen_at"`
	RevokedAt        *time.Time      `json:"revoked_at"`
	FreshnessSeconds int64           `json:"freshness_seconds"`
	Stale            bool            `json:"stale"`
	Rigs             []agentlink.Rig `json:"rigs"`
}

// machines: GET /api/v1/machines.
func (a *App) machines(w http.ResponseWriter, _ *http.Request, _ string) {
	ms, err := a.store.Machines()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	now := a.now()
	out := make([]webMachine, 0, len(ms))
	for _, m := range ms {
		rigs, err := a.store.Rigs(m.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		f, stale := freshness(now, m.LastSeenAt)
		out = append(out, webMachine{
			ID: m.ID, Hostname: m.Hostname, OS: m.OS, Arch: m.Arch, GtbVersion: m.GtbVersion,
			Protocol: m.Protocol, CreatedAt: m.CreatedAt, LastSeenAt: m.LastSeenAt, RevokedAt: m.RevokedAt,
			FreshnessSeconds: f, Stale: stale, Rigs: rigs,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// revokeMachine: DELETE /api/v1/machines/{id} revokes the machine and its
// tokens.
func (a *App) revokeMachine(w http.ResponseWriter, r *http.Request, _ string) {
	err := a.store.RevokeMachine(r.PathValue("id"))
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "machine_not_found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// enrollCode: POST /api/v1/enroll-codes returns {code, expires_at} (15 min).
func (a *App) enrollCode(w http.ResponseWriter, _ *http.Request, _ string) {
	now := a.now()
	code, err := a.store.createEnrollCode(DefaultEnrollCodeTTL, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": code, "expires_at": now.Add(DefaultEnrollCodeTTL)})
}

// events: GET /api/v1/events is an SSE stream. It sends "event:
// board.changed" when the store applies events or a snapshot, at most once
// per sseDebounce, and a ": ping" comment every ssePing. The subscriber is
// removed when the client disconnects.
func (a *App) events(w http.ResponseWriter, r *http.Request, _ string) {
	rc := http.NewResponseController(w)
	wake, unsubscribe := a.store.board.subscribe()
	defer unsubscribe()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(": connected\n\n")); err != nil {
		return
	}
	if rc.Flush() != nil {
		return
	}

	ctx := r.Context()
	ping := time.NewTicker(a.ssePing)
	defer ping.Stop()
	var last time.Time
	send := func(msg string) bool {
		if _, err := w.Write([]byte(msg)); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			if !send(": ping\n\n") {
				return
			}
		case <-wake:
			if wait := a.sseDebounce - time.Since(last); !last.IsZero() && wait > 0 {
				t := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					t.Stop()
					return
				case <-t.C:
				}
				select { // changes during the wait are covered by this send
				case <-wake:
				default:
				}
			}
			last = time.Now()
			if !send("event: board.changed\ndata: {}\n\n") {
				return
			}
		}
	}
}
