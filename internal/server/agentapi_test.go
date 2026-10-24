package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/agentlink"
)

// fakeClock is an injectable clock for the agent API.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type apiFixture struct {
	t     *testing.T
	store *Store
	clock *fakeClock
	api   *AgentAPI
	h     http.Handler
}

func newAPIFixture(t *testing.T) *apiFixture {
	t.Helper()
	st := openTest(t)
	clk := newFakeClock()
	h := NewAgentAPI(st, clk.Now)
	api, ok := h.(*AgentAPI)
	if !ok {
		t.Fatalf("NewAgentAPI returned %T, want *AgentAPI", h)
	}
	return &apiFixture{t: t, store: st, clock: clk, api: api, h: h}
}

// machine registers a machine with a token and returns the raw token.
func (f *apiFixture) machine(id string) string {
	f.t.Helper()
	addMachine(f.t, f.store, id)
	tok := "tok-" + id
	if err := f.store.AddToken(id, sha256Hex(tok)); err != nil {
		f.t.Fatal(err)
	}
	return tok
}

func (f *apiFixture) do(method, path, token string, body any) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.doCtx(context.Background(), method, path, token, body)
}

func (f *apiFixture) doCtx(ctx context.Context, method, path, token string, body any) *httptest.ResponseRecorder {
	f.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	case string:
		rd = strings.NewReader(b)
	default:
		rd = bytes.NewReader(mustJSON(f.t, b))
	}
	req := httptest.NewRequest(method, path, rd).WithContext(ctx)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

func (f *apiFixture) enqueue(machineID, key string, ttl time.Duration) {
	f.t.Helper()
	r := agentlink.Request{Key: key, Type: "inspect", ExpiresAt: f.clock.Now().Add(ttl)}
	if err := f.store.enqueueRequest(machineID, r, f.clock.Now()); err != nil {
		f.t.Fatal(err)
	}
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, code int) {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
}

func wantError(t *testing.T, rec *httptest.ResponseRecorder, code int, errCode string) {
	t.Helper()
	wantStatus(t, rec, code)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	if body["error"] != errCode {
		t.Fatalf("error = %v, want %q", body["error"], errCode)
	}
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

func evs(ids ...string) []agentlink.Event {
	out := make([]agentlink.Event, len(ids))
	for i, id := range ids {
		out[i] = agentlink.Event{ID: id, At: time.Now(), Type: agentlink.EventHeartbeat}
	}
	return out
}

func eventsBody(after string, e []agentlink.Event) map[string]any {
	return map[string]any{"after": after, "events": e}
}

func TestAgentAPI_NoToken401(t *testing.T) {
	f := newAPIFixture(t)
	f.machine("m1")
	routes := []struct{ method, path string }{
		{http.MethodPost, "/agent/v1/hello"},
		{http.MethodPost, "/agent/v1/snapshot"},
		{http.MethodPost, "/agent/v1/events"},
		{http.MethodGet, "/agent/v1/requests"},
		{http.MethodPost, "/agent/v1/results"},
		{http.MethodPost, "/agent/v1/heartbeat"},
	}
	headers := []string{"", "Bearer", "Bearer ", "Basic dG9rLW0x", "tok-m1", "Bearer wrong", "Bearer tok-m1 extra"}
	for _, rt := range routes {
		for _, hdr := range headers {
			req := httptest.NewRequest(rt.method, rt.path, strings.NewReader("{}"))
			if hdr != "" {
				req.Header.Set("Authorization", hdr)
			}
			rec := httptest.NewRecorder()
			f.h.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized || strings.TrimSpace(rec.Body.String()) != `{"error":"unauthorized"}` {
				t.Errorf("%s %s auth=%q: %d %s", rt.method, rt.path, hdr, rec.Code, rec.Body.String())
			}
		}
	}
}

func TestAgentAPI_RevokedToken401(t *testing.T) {
	f := newAPIFixture(t)
	tok := f.machine("m1")
	wantStatus(t, f.do(http.MethodPost, "/agent/v1/heartbeat", tok, nil), http.StatusOK)
	if err := f.store.RevokeToken(sha256Hex(tok)); err != nil {
		t.Fatal(err)
	}
	wantError(t, f.do(http.MethodPost, "/agent/v1/heartbeat", tok, nil), http.StatusUnauthorized, "unauthorized")

	tok2 := f.machine("m2")
	if err := f.store.RevokeMachine("m2"); err != nil {
		t.Fatal(err)
	}
	wantError(t, f.do(http.MethodPost, "/agent/v1/heartbeat", tok2, nil), http.StatusUnauthorized, "unauthorized")
}

func TestAgentAPI_BearerSchemeCaseInsensitive(t *testing.T) {
	f := newAPIFixture(t)
	tok := f.machine("m1")
	req := httptest.NewRequest(http.MethodPost, "/agent/v1/heartbeat", nil)
	req.Header.Set("Authorization", "bearer "+tok)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	wantStatus(t, rec, http.StatusOK)
}

func TestAgentAPI_TouchesLastSeen(t *testing.T) {
	f := newAPIFixture(t)
	tok := f.machine("m1")
	f.clock.Advance(time.Hour)
	wantStatus(t, f.do(http.MethodPost, "/agent/v1/heartbeat", tok, nil), http.StatusOK)
	m, err := f.store.Machine("m1")
	if err != nil {
		t.Fatal(err)
	}
	if !m.LastSeenAt.Equal(f.clock.Now()) {
		t.Fatalf("last_seen_at = %v, want %v", m.LastSeenAt, f.clock.Now())
	}
	if got := decode[map[string]bool](t, f.do(http.MethodPost, "/agent/v1/heartbeat", tok, nil)); !got["ok"] {
		t.Fatalf("heartbeat body = %v", got)
	}
}

func TestAgentAPI_WrongMethod405(t *testing.T) {
	f := newAPIFixture(t)
	tok := f.machine("m1")
	cases := []struct{ method, path, allow string }{
		{http.MethodGet, "/agent/v1/hello", "POST"},
		{http.MethodGet, "/agent/v1/snapshot", "POST"},
		{http.MethodPut, "/agent/v1/events", "POST"},
		{http.MethodPost, "/agent/v1/requests", "GET"},
		{http.MethodGet, "/agent/v1/results", "POST"},
		{http.MethodDelete, "/agent/v1/heartbeat", "POST"},
		{http.MethodGet, "/agent/v1/enroll", "POST"},
	}
	for _, c := range cases {
		rec := f.do(c.method, c.path, tok, nil)
		wantStatus(t, rec, http.StatusMethodNotAllowed)
		if got := rec.Header().Get("Allow"); got != c.allow {
			t.Errorf("%s %s Allow = %q, want %q", c.method, c.path, got, c.allow)
		}
	}
}

func TestAgentAPI_UnknownPath404(t *testing.T) {
	f := newAPIFixture(t)
	tok := f.machine("m1")
	wantStatus(t, f.do(http.MethodGet, "/agent/v1/nope", tok, nil), http.StatusNotFound)
}

func TestAgentAPI_EnrollMountedWithoutToken(t *testing.T) {
	f := newAPIFixture(t)
	code, err := f.store.CreateEnrollCode(0)
	if err != nil {
		t.Fatal(err)
	}
	rec := f.do(http.MethodPost, "/agent/v1/enroll", "", map[string]any{"code": code, "hello": testHello()})
	wantStatus(t, rec, http.StatusOK)
	resp := decode[EnrollResponse](t, rec)
	if resp.MachineID == "" || resp.Token == "" {
		t.Fatalf("enroll response = %+v", resp)
	}
	// The issued token authenticates the agent API.
	h := testHello()
	h.MachineID = resp.MachineID
	wantStatus(t, f.do(http.MethodPost, "/agent/v1/hello", resp.Token, h), http.StatusOK)
}

func TestAgentAPI_MalformedJSON400(t *testing.T) {
	f := newAPIFixture(t)
	tok := f.machine("m1")
	for _, p := range []string{"/agent/v1/hello", "/agent/v1/snapshot", "/agent/v1/events", "/agent/v1/results"} {
		wantError(t, f.do(http.MethodPost, p, tok, "{not json"), http.StatusBadRequest, "bad_request")
		wantError(t, f.do(http.MethodPost, p, tok, `{} {}`), http.StatusBadRequest, "bad_request")
	}
}

func TestAgentAPI_OversizeBody413(t *testing.T) {
	f := newAPIFixture(t)
	tok := f.machine("m1")
	// 1 MiB limit for hello and results.
	big := `{"hostname":"` + strings.Repeat("a", 1<<20) + `"}`
	wantError(t, f.do(http.MethodPost, "/agent/v1/hello", tok, big), http.StatusRequestEntityTooLarge, "body_too_large")
	bigRes := `[{"key":"` + strings.Repeat("a", 1<<20) + `"}]`
	wantError(t, f.do(http.MethodPost, "/agent/v1/results", tok, bigRes), http.StatusRequestEntityTooLarge, "body_too_large")
	// 8 MiB limit for snapshot and events: 2 MiB passes, 9 MiB does not.
	mid := `{"cursor":"","rigs":[{"name":"` + strings.Repeat("r", 2<<20) + `"}]}`
	wantStatus(t, f.do(http.MethodPost, "/agent/v1/snapshot", tok, mid), http.StatusOK)
	huge := `{"cursor":"","rigs":[{"name":"` + strings.Repeat("r", 9<<20) + `"}]}`
	wantError(t, f.do(http.MethodPost, "/agent/v1/snapshot", tok, huge), http.StatusRequestEntityTooLarge, "body_too_large")
	hugeEv := `{"after":"","events":[{"id":"` + strings.Repeat("e", 9<<20) + `"}]}`
	wantError(t, f.do(http.MethodPost, "/agent/v1/events", tok, hugeEv), http.StatusRequestEntityTooLarge, "body_too_large")
}

func TestHello_OtherMachine403(t *testing.T) {
	f := newAPIFixture(t)
	tok := f.machine("m1")
	f.machine("m2")
	h := testHello()
	h.MachineID = "m2"
	wantError(t, f.do(http.MethodPost, "/agent/v1/hello", tok, h), http.StatusForbidden, "forbidden")
	m2, _ := f.store.Machine("m2")
	if m2.Hostname != "h-m2" {
		t.Fatalf("m2 hostname changed to %q", m2.Hostname)
	}
}

func TestHello_ProtocolMismatch(t *testing.T) {
	f := newAPIFixture(t)
	tok := f.machine("m1")
	h := testHello()
	h.MachineID = "m1"
	h.Protocol = "2.0.0"
	wantError(t, f.do(http.MethodPost, "/agent/v1/hello", tok, h), http.StatusBadRequest, "protocol_mismatch")
	h.Protocol = "1.9.3" // minor differences are fine
	wantStatus(t, f.do(http.MethodPost, "/agent/v1/hello", tok, h), http.StatusOK)
}

func TestHello_ResyncUntilSnapshot(t *testing.T) {
	f := newAPIFixture(t)
	tok := f.machine("m1")
	h := testHello()
	h.MachineID = "m1"
	h.Hostname = "renamed"
	h.GtbVersion = "0.2.0"
	rec := f.do(http.MethodPost, "/agent/v1/hello", tok, h)
	wantStatus(t, rec, http.StatusOK)
	if got := decode[map[string]bool](t, rec); !got["resync"] {
		t.Fatalf("before snapshot resync = %v, want true", got)
	}
	m, _ := f.store.Machine("m1")
	if m.Hostname != "renamed" || m.GtbVersion != "0.2.0" {
		t.Fatalf("hello did not refresh machine: %+v", m)
	}

	// A snapshot with an empty cursor still counts as a snapshot.
	rec = f.do(http.MethodPost, "/agent/v1/snapshot", tok, agentlink.Snapshot{Cursor: ""})
	wantStatus(t, rec, http.StatusOK)
	if got := decode[map[string]string](t, rec); got["cursor"] != "" {
		t.Fatalf("snapshot response = %v", got)
	}
	rec = f.do(http.MethodPost, "/agent/v1/hello", tok, h)
	if got := decode[map[string]bool](t, rec); got["resync"] {
		t.Fatalf("after snapshot resync = %v, want false", got)
	}
}

func TestSnapshot_SetsCursor(t *testing.T) {
	f := newAPIFixture(t)
	tok := f.machine("m1")
	snap := agentlink.Snapshot{Cursor: "01C", Rigs: []agentlink.Rig{{Name: "r1"}}, Items: []agentlink.ItemView{{ID: "i1", Rig: "r1"}}}
	rec := f.do(http.MethodPost, "/agent/v1/snapshot", tok, snap)
	wantStatus(t, rec, http.StatusOK)
	if got := decode[map[string]string](t, rec); got["cursor"] != "01C" {
		t.Fatalf("snapshot response = %v", got)
	}
	m, _ := f.store.Machine("m1")
	if m.Cursor != "01C" || !m.HasSnapshot {
		t.Fatalf("machine = %+v", m)
	}
	if _, _, err := f.store.Item("m1", "i1"); err != nil {
		t.Fatalf("item not stored: %v", err)
	}
}

func TestEvents_Over500_413(t *testing.T) {
	f := newAPIFixture(t)
	tok := f.machine("m1")
	ids := make([]string, 501)
	for i := range ids {
		ids[i] = fmt.Sprintf("E%04d", i)
	}
	wantError(t, f.do(http.MethodPost, "/agent/v1/events", tok, eventsBody("", evs(ids...))), http.StatusRequestEntityTooLarge, "too_many_events")
	// Exactly 500 is fine.
	rec := f.do(http.MethodPost, "/agent/v1/events", tok, eventsBody("", evs(ids[:500]...)))
	wantStatus(t, rec, http.StatusOK)
	if got := decode[map[string]string](t, rec); got["cursor"] != "E0499" {
		t.Fatalf("cursor = %v", got)
	}
}

func TestEvents_CursorMismatch_409Resync(t *testing.T) {
	f := newAPIFixture(t)
	tok := f.machine("m1")
	wantStatus(t, f.do(http.MethodPost, "/agent/v1/snapshot", tok, agentlink.Snapshot{Cursor: "C5"}), http.StatusOK)

	wantError(t, f.do(http.MethodPost, "/agent/v1/events", tok, eventsBody("C4", evs("C6"))), http.StatusConflict, "resync")
	wantError(t, f.do(http.MethodPost, "/agent/v1/events", tok, eventsBody("", evs("C6"))), http.StatusConflict, "resync")

	rec := f.do(http.MethodPost, "/agent/v1/events", tok, eventsBody("C5", evs("C6", "C7")))
	wantStatus(t, rec, http.StatusOK)
	if got := decode[map[string]string](t, rec); got["cursor"] != "C7" {
		t.Fatalf("cursor = %v", got)
	}
	// Re-sending the applied batch with the old after is a 409 (the machine
	// resyncs), and nothing is applied twice.
	wantError(t, f.do(http.MethodPost, "/agent/v1/events", tok, eventsBody("C5", evs("C6", "C7"))), http.StatusConflict, "resync")
	var n int
	if err := f.store.db.QueryRow(`SELECT count(*) FROM events WHERE machine_id='m1'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("events = %d (%v), want 2", n, err)
	}
	m, _ := f.store.Machine("m1")
	if m.Cursor != "C7" {
		t.Fatalf("stored cursor = %q", m.Cursor)
	}
}

func TestEvents_OrderValidation400(t *testing.T) {
	f := newAPIFixture(t)
	tok := f.machine("m1")
	wantStatus(t, f.do(http.MethodPost, "/agent/v1/snapshot", tok, agentlink.Snapshot{Cursor: "C5"}), http.StatusOK)
	for name, ids := range map[string][]string{
		"descending":   {"C7", "C6"},
		"duplicate":    {"C6", "C6"},
		"equals after": {"C5"},
		"below after":  {"C4"},
		"empty id":     {""},
	} {
		rec := f.do(http.MethodPost, "/agent/v1/events", tok, eventsBody("C5", evs(ids...)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400; %s", name, rec.Code, rec.Body.String())
		}
	}
	// An unknown event type is rejected by the store as a bad request.
	bad := []agentlink.Event{{ID: "C6", Type: "nope"}}
	wantError(t, f.do(http.MethodPost, "/agent/v1/events", tok, eventsBody("C5", bad)), http.StatusBadRequest, "bad_request")
	m, _ := f.store.Machine("m1")
	if m.Cursor != "C5" {
		t.Fatalf("cursor moved to %q", m.Cursor)
	}
}

func TestEvents_EmptyBatchKeepsCursor(t *testing.T) {
	f := newAPIFixture(t)
	tok := f.machine("m1")
	wantStatus(t, f.do(http.MethodPost, "/agent/v1/snapshot", tok, agentlink.Snapshot{Cursor: "C5"}), http.StatusOK)
	rec := f.do(http.MethodPost, "/agent/v1/events", tok, eventsBody("C5", nil))
	wantStatus(t, rec, http.StatusOK)
	if got := decode[map[string]string](t, rec); got["cursor"] != "C5" {
		t.Fatalf("cursor = %v", got)
	}
}

func TestRequests_ImmediateAndMarkedDelivered(t *testing.T) {
	f := newAPIFixture(t)
	tok := f.machine("m1")
	f.enqueue("m1", "k1", time.Minute)
	rec := f.do(http.MethodGet, "/agent/v1/requests?wait=10s", tok, nil)
	wantStatus(t, rec, http.StatusOK)
	got := decode[[]agentlink.Request](t, rec)
	if len(got) != 1 || got[0].Key != "k1" {
		t.Fatalf("requests = %+v", got)
	}
	st, _, _ := f.store.RequestStatus("m1", "k1", f.clock.Now())
	if st != RequestDelivered {
		t.Fatalf("status = %q, want delivered", st)
	}
	// Delivered requests are re-delivered until they have a result.
	got = decode[[]agentlink.Request](t, f.do(http.MethodGet, "/agent/v1/requests", tok, nil))
	if len(got) != 1 {
		t.Fatalf("redelivery = %+v", got)
	}
}

func TestRequests_EmptyIsArray(t *testing.T) {
	f := newAPIFixture(t)
	tok := f.machine("m1")
	rec := f.do(http.MethodGet, "/agent/v1/requests", tok, nil)
	wantStatus(t, rec, http.StatusOK)
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("body = %q, want []", rec.Body.String())
	}
}

func TestRequests_InvalidWait400(t *testing.T) {
	f := newAPIFixture(t)
	tok := f.machine("m1")
	for _, w := range []string{"abc", "-1s", "10"} {
		wantError(t, f.do(http.MethodGet, "/agent/v1/requests?wait="+w, tok, nil), http.StatusBadRequest, "bad_request")
	}
}

func TestRequests_WaitCap(t *testing.T) {
	f := newAPIFixture(t)
	if f.api.maxWait != 30*time.Second {
		t.Fatalf("default maxWait = %v, want 30s", f.api.maxWait)
	}
	for in, want := range map[time.Duration]time.Duration{
		0: 0, time.Second: time.Second, 30 * time.Second: 30 * time.Second, 120 * time.Second: 30 * time.Second,
	} {
		if got := f.api.effectiveWait(in); got != want {
			t.Errorf("effectiveWait(%v) = %v, want %v", in, got, want)
		}
	}
}

func TestRequests_LongPollCapped(t *testing.T) {
	f := newAPIFixture(t)
	tok := f.machine("m1")
	f.api.maxWait = 200 * time.Millisecond
	start := time.Now()
	rec := f.do(http.MethodGet, "/agent/v1/requests?wait=120s", tok, nil)
	el := time.Since(start)
	wantStatus(t, rec, http.StatusOK)
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if el < 150*time.Millisecond || el > 2*time.Second {
		t.Fatalf("long-poll took %v, want about maxWait (200ms)", el)
	}
}

func TestRequests_WokenByEnqueue(t *testing.T) {
	f := newAPIFixture(t)
	tok := f.machine("m1")
	f.api.recheck = time.Hour // only the notifier can wake it in time
	done := make(chan *httptest.ResponseRecorder, 1)
	start := time.Now()
	go func() { done <- f.do(http.MethodGet, "/agent/v1/requests?wait=20s", tok, nil) }()
	time.Sleep(100 * time.Millisecond)
	// Enqueue through the public store method: it must wake the long-poll.
	if err := f.store.EnqueueRequest("m1", agentlink.Request{Key: "k1", Type: "inspect", ExpiresAt: time.Now().Add(48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	select {
	case rec := <-done:
		if el := time.Since(start); el > time.Second {
			t.Fatalf("woke after %v, want < 1s", el)
		}
		got := decode[[]agentlink.Request](t, rec)
		if len(got) != 1 || got[0].Key != "k1" {
			t.Fatalf("requests = %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("long-poll not woken by enqueue")
	}
}

func TestRequests_NotifyWakes(t *testing.T) {
	f := newAPIFixture(t)
	tok := f.machine("m1")
	f.api.recheck = time.Hour
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- f.do(http.MethodGet, "/agent/v1/requests?wait=20s", tok, nil) }()
	time.Sleep(100 * time.Millisecond)
	// Insert directly (no notification), then wake via AgentAPI.Notify.
	if _, err := f.store.db.Exec(`INSERT INTO requests(machine_id, key, type, expires_at, created_at, status) VALUES ('m1','k9','inspect',?,?,'pending')`,
		ms(f.clock.Now().Add(time.Hour)), ms(f.clock.Now())); err != nil {
		t.Fatal(err)
	}
	f.api.Notify("m1")
	select {
	case rec := <-done:
		if got := decode[[]agentlink.Request](t, rec); len(got) != 1 || got[0].Key != "k9" {
			t.Fatalf("requests = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("Notify did not wake the long-poll")
	}
}

func TestRequests_FallbackRecheck(t *testing.T) {
	f := newAPIFixture(t)
	tok := f.machine("m1")
	f.api.recheck = 50 * time.Millisecond
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- f.do(http.MethodGet, "/agent/v1/requests?wait=20s", tok, nil) }()
	time.Sleep(100 * time.Millisecond)
	// Missed wake: insert without notifying.
	if _, err := f.store.db.Exec(`INSERT INTO requests(machine_id, key, type, expires_at, created_at, status) VALUES ('m1','k9','inspect',?,?,'pending')`,
		ms(f.clock.Now().Add(time.Hour)), ms(f.clock.Now())); err != nil {
		t.Fatal(err)
	}
	select {
	case rec := <-done:
		if got := decode[[]agentlink.Request](t, rec); len(got) != 1 {
			t.Fatalf("requests = %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fallback re-check did not find the request")
	}
}

func TestRequests_ClientDisconnectEndsPoll(t *testing.T) {
	f := newAPIFixture(t)
	tok := f.machine("m1")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		f.doCtx(ctx, http.MethodGet, "/agent/v1/requests?wait=20s", tok, nil)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("long-poll did not end on client disconnect")
	}
	// The waiter was unsubscribed.
	f.store.notifier.mu.Lock()
	n := len(f.store.notifier.waiters["m1"])
	f.store.notifier.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d waiters left after disconnect", n)
	}
}

func TestRequests_OnlyOwnMachine(t *testing.T) {
	f := newAPIFixture(t)
	tokA := f.machine("mA")
	f.machine("mB")
	f.enqueue("mB", "kB", time.Minute)
	f.api.maxWait = 100 * time.Millisecond
	rec := f.do(http.MethodGet, "/agent/v1/requests?wait=1s", tokA, nil)
	if got := decode[[]agentlink.Request](t, rec); len(got) != 0 {
		t.Fatalf("A got B's requests: %+v", got)
	}
	// A notification for B does not hand B's request to A either.
	f.api.recheck = time.Hour
	f.api.maxWait = 300 * time.Millisecond
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- f.do(http.MethodGet, "/agent/v1/requests?wait=1s", tokA, nil) }()
	time.Sleep(50 * time.Millisecond)
	f.enqueue("mB", "kB2", time.Minute)
	f.api.Notify("mB")
	if got := decode[[]agentlink.Request](t, <-done); len(got) != 0 {
		t.Fatalf("A got B's requests after wake: %+v", got)
	}
	st, _, _ := f.store.RequestStatus("mB", "kB", f.clock.Now())
	if st != RequestPending {
		t.Fatalf("B's request status = %q, want pending", st)
	}
	// A cannot answer B's request either.
	rec = f.do(http.MethodPost, "/agent/v1/results", tokA, []agentlink.Result{{Key: "kB", OK: true}})
	if got := decode[map[string]int](t, rec); got["accepted"] != 0 || got["ignored"] != 1 {
		t.Fatalf("results = %v", got)
	}
	if st, res, _ := f.store.RequestStatus("mB", "kB", f.clock.Now()); st != RequestPending || res != nil {
		t.Fatalf("B's request = %q %+v", st, res)
	}
}

func TestRequests_DeliveredThenExpiredNotReturned(t *testing.T) {
	f := newAPIFixture(t)
	tok := f.machine("m1")
	f.enqueue("m1", "k1", time.Minute)
	if got := decode[[]agentlink.Request](t, f.do(http.MethodGet, "/agent/v1/requests", tok, nil)); len(got) != 1 {
		t.Fatalf("first poll = %+v", got)
	}
	f.clock.Advance(2 * time.Minute) // past expiry, no sweep has run
	if got := decode[[]agentlink.Request](t, f.do(http.MethodGet, "/agent/v1/requests", tok, nil)); len(got) != 0 {
		t.Fatalf("expired request returned: %+v", got)
	}
}

func TestResults_AcceptedAndUnknownKey(t *testing.T) {
	f := newAPIFixture(t)
	tok := f.machine("m1")
	f.enqueue("m1", "k1", time.Minute)
	f.enqueue("m1", "k2", time.Minute)
	rec := f.do(http.MethodPost, "/agent/v1/results", tok, []agentlink.Result{
		{Key: "k1", OK: true}, {Key: "nope", OK: true}, {Key: "k2", OK: false, Error: "boom"},
	})
	wantStatus(t, rec, http.StatusOK)
	if got := decode[map[string]int](t, rec); got["accepted"] != 2 || got["ignored"] != 1 {
		t.Fatalf("results = %v", got)
	}
	if st, res, _ := f.store.RequestStatus("m1", "k1", f.clock.Now()); st != RequestDone || res == nil || !res.OK {
		t.Fatalf("k1 = %q %+v", st, res)
	}
	if st, res, _ := f.store.RequestStatus("m1", "k2", f.clock.Now()); st != RequestDone || res == nil || res.Error != "boom" {
		t.Fatalf("k2 = %q %+v", st, res)
	}
	// Answered requests are no longer delivered.
	if got := decode[[]agentlink.Request](t, f.do(http.MethodGet, "/agent/v1/requests", tok, nil)); len(got) != 0 {
		t.Fatalf("answered requests returned: %+v", got)
	}
}

func TestResults_LateUsesInjectedClock(t *testing.T) {
	f := newAPIFixture(t)
	tok := f.machine("m1")
	f.enqueue("m1", "k1", time.Minute)
	f.clock.Advance(2 * time.Minute)
	rec := f.do(http.MethodPost, "/agent/v1/results", tok, []agentlink.Result{{Key: "k1", OK: true}})
	if got := decode[map[string]int](t, rec); got["accepted"] != 1 {
		t.Fatalf("results = %v", got)
	}
	if st, _, _ := f.store.RequestStatus("m1", "k1", f.clock.Now()); st != RequestExpired {
		t.Fatalf("late result status = %q, want expired", st)
	}
}

func TestResults_Over500_413(t *testing.T) {
	f := newAPIFixture(t)
	tok := f.machine("m1")
	rs := make([]agentlink.Result, 501)
	for i := range rs {
		rs[i] = agentlink.Result{Key: fmt.Sprintf("k%d", i)}
	}
	wantError(t, f.do(http.MethodPost, "/agent/v1/results", tok, rs), http.StatusRequestEntityTooLarge, "too_many_results")
}

// deliver polls once without waiting so the machine's pending requests become
// delivered.
func (f *apiFixture) deliver(tok string, want int) {
	f.t.Helper()
	if got := decode[[]agentlink.Request](f.t, f.do(http.MethodGet, "/agent/v1/requests", tok, nil)); len(got) != want {
		f.t.Fatalf("deliver: got %d requests, want %d", len(got), want)
	}
}

// R33: delivered-but-unanswered requests do not end the wait, so the agent
// cannot hot-loop; they are re-delivered when the wait ends.
func TestRequests_DeliveredOnlyWaits(t *testing.T) {
	f := newAPIFixture(t)
	tok := f.machine("m1")
	f.api.maxWait = 300 * time.Millisecond
	f.api.recheck = 50 * time.Millisecond // rechecks must not end the wait either
	f.enqueue("m1", "k1", time.Hour)
	f.deliver(tok, 1)

	start := time.Now()
	rec := f.do(http.MethodGet, "/agent/v1/requests?wait=10s", tok, nil)
	el := time.Since(start)
	wantStatus(t, rec, http.StatusOK)
	if el < 280*time.Millisecond {
		t.Fatalf("returned after %v; delivered-only poll must wait ~300ms", el)
	}
	if el > 2*time.Second {
		t.Fatalf("returned after %v, want about 300ms", el)
	}
	if got := decode[[]agentlink.Request](t, rec); len(got) != 1 || got[0].Key != "k1" {
		t.Fatalf("re-delivery = %+v", got)
	}
}

func TestRequests_NewPendingReturnsAllImmediately(t *testing.T) {
	f := newAPIFixture(t)
	tok := f.machine("m1")
	f.api.maxWait = 5 * time.Second
	f.enqueue("m1", "k1", time.Hour)
	f.deliver(tok, 1)
	f.clock.Advance(time.Millisecond) // k2 sorts after k1 by created_at
	f.enqueue("m1", "k2", time.Hour)

	start := time.Now()
	rec := f.do(http.MethodGet, "/agent/v1/requests?wait=5s", tok, nil)
	if el := time.Since(start); el > time.Second {
		t.Fatalf("returned after %v, want immediately", el)
	}
	got := decode[[]agentlink.Request](t, rec)
	if len(got) != 2 || got[0].Key != "k1" || got[1].Key != "k2" {
		t.Fatalf("requests = %+v, want [k1 k2]", got)
	}
}

func TestRequests_DeliveredOnlyWokenByEnqueue(t *testing.T) {
	f := newAPIFixture(t)
	tok := f.machine("m1")
	f.api.recheck = time.Hour
	f.enqueue("m1", "k1", time.Hour)
	f.deliver(tok, 1)

	done := make(chan *httptest.ResponseRecorder, 1)
	start := time.Now()
	go func() { done <- f.do(http.MethodGet, "/agent/v1/requests?wait=20s", tok, nil) }()
	time.Sleep(100 * time.Millisecond)
	select {
	case rec := <-done:
		t.Fatalf("delivered-only poll returned early: %s", rec.Body.String())
	default:
	}
	if err := f.store.EnqueueRequest("m1", agentlink.Request{Key: "k2", Type: "inspect", ExpiresAt: time.Now().Add(48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	select {
	case rec := <-done:
		if el := time.Since(start); el > time.Second {
			t.Fatalf("woke after %v, want < 1s", el)
		}
		got := decode[[]agentlink.Request](t, rec)
		if len(got) != 2 {
			t.Fatalf("requests = %+v, want k1 and k2", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("enqueue did not wake the delivered-only poll")
	}
}

func TestRequests_NonPositiveRecheckNoPanic(t *testing.T) {
	f := newAPIFixture(t)
	tok := f.machine("m1")
	f.api.maxWait = 50 * time.Millisecond
	for _, rc := range []time.Duration{0, -time.Second} {
		f.api.recheck = rc
		wantStatus(t, f.do(http.MethodGet, "/agent/v1/requests?wait=1s", tok, nil), http.StatusOK)
	}
}
