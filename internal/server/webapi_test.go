package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/steveyegge/gastown/internal/agentlink"
)

// seedItems registers machine id with a snapshot of items and sets its
// last_seen_at to seen.
func (f *webFixture) seedItems(id string, seen time.Time, items ...agentlink.ItemView) {
	f.t.Helper()
	addMachine(f.t, f.store, id)
	if err := f.store.ApplySnapshot(id, agentlink.Snapshot{Items: items, Rigs: []agentlink.Rig{{Name: "r1", Gated: true}}}); err != nil {
		f.t.Fatal(err)
	}
	if err := f.store.TouchMachine(id, seen); err != nil {
		f.t.Fatal(err)
	}
}

func (f *webFixture) setUpdated(machineID, id string, at time.Time) {
	f.t.Helper()
	if _, err := f.store.db.Exec(`UPDATE items SET updated_at = ? WHERE machine_id = ? AND id = ?`, ms(at), machineID, id); err != nil {
		f.t.Fatal(err)
	}
}

type boardResp struct {
	Lanes []struct {
		Step  string    `json:"step"`
		Items []webItem `json:"items"`
	} `json:"lanes"`
	NeedsYou []webItem `json:"needs_you"`
}

func TestWebAPI_ApproveRequiresSession(t *testing.T) {
	f := newWebFixture(t)
	tok := f.seedMachineToken("m1")
	f.seedItems("m1", f.clock.Now(), agentlink.ItemView{ID: "i1", Step: "spec"})
	s := f.login()
	body := map[string]string{"artifact": "spec", "sha": strings.Repeat("a", 64)}

	for _, path := range []string{"/api/v1/items/m1/i1/approve", "/api/v1/items/m1/i1/request-changes"} {
		// Bearer alone: 403.
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"artifact":"spec","sha":"`+strings.Repeat("a", 64)+`"}`))
		req.Header.Set("Authorization", "Bearer "+tok)
		wantError(t, f.serve(req), http.StatusForbidden, "bearer_not_allowed")

		// Bearer alongside a valid session and CSRF: still 403.
		req = httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"artifact":"spec","sha":"`+strings.Repeat("a", 64)+`"}`))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: s.cookie.Value})
		req.Header.Set("X-CSRF-Token", s.csrf)
		wantError(t, f.serve(req), http.StatusForbidden, "bearer_not_allowed")

		// Any Authorization header, whatever its scheme, case or separator,
		// is refused even with a valid session and CSRF.
		for _, auth := range []string{"Bearer\t" + tok, "bearer " + tok, "  BEARER  " + tok, "Basic Zm9vOmJhcg==", "Token x", tok} {
			req = httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"artifact":"spec","sha":"`+strings.Repeat("a", 64)+`"}`))
			req.Header.Set("Authorization", auth)
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: s.cookie.Value})
			req.Header.Set("X-CSRF-Token", s.csrf)
			wantError(t, f.serve(req), http.StatusForbidden, "bearer_not_allowed")
		}

		// No credentials: 401.
		wantError(t, f.api(http.MethodPost, path, nil, body), http.StatusUnauthorized, "unauthorized")
	}
	if reqs, _ := f.store.PendingRequests("m1", f.clock.Now()); len(reqs) != 0 {
		t.Fatalf("requests enqueued: %+v", reqs)
	}
}

func TestWebAPI_ApproveRequiresCSRF(t *testing.T) {
	f := newWebFixture(t)
	f.seedItems("m1", f.clock.Now(), agentlink.ItemView{ID: "i1", Step: "spec"})
	s := f.login()
	body := map[string]string{"artifact": "spec", "sha": strings.Repeat("a", 64)}

	noCSRF := webSession{cookie: s.cookie}
	wantError(t, f.api(http.MethodPost, "/api/v1/items/m1/i1/approve", &noCSRF, body), http.StatusForbidden, "csrf")
	bad := webSession{cookie: s.cookie, csrf: strings.Repeat("0", 64)}
	wantError(t, f.api(http.MethodPost, "/api/v1/items/m1/i1/approve", &bad, body), http.StatusForbidden, "csrf")

	// Another session's CSRF token does not work for this session.
	other := f.login()
	mixed := webSession{cookie: s.cookie, csrf: other.csrf}
	wantError(t, f.api(http.MethodPost, "/api/v1/items/m1/i1/approve", &mixed, body), http.StatusForbidden, "csrf")

	if reqs, _ := f.store.PendingRequests("m1", f.clock.Now()); len(reqs) != 0 {
		t.Fatalf("requests enqueued: %+v", reqs)
	}
	wantStatus(t, f.api(http.MethodPost, "/api/v1/items/m1/i1/approve", &s, body), http.StatusAccepted)
}

func TestWebAPI_AgentTokenGets401(t *testing.T) {
	f := newWebFixture(t)
	tok := f.seedMachineToken("m1")
	for _, path := range []string{"/api/v1/me", "/api/v1/board", "/api/v1/machines", "/api/v1/events"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		wantError(t, f.serve(req), http.StatusUnauthorized, "unauthorized")
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/enroll-codes", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	wantError(t, f.serve(req), http.StatusUnauthorized, "unauthorized")
}

func TestBoard_NeedsYouOldestFirst(t *testing.T) {
	f := newWebFixture(t)
	now := f.clock.Now()
	f.seedItems("m1", now,
		agentlink.ItemView{ID: "a", Rig: "r1", Kind: "task", Step: "spec", WaitingForHuman: "approve"},
		agentlink.ItemView{ID: "b", Rig: "r1", Kind: "task", Step: "plan", WaitingForHuman: "approve"},
		agentlink.ItemView{ID: "c", Rig: "r1", Kind: "task", Step: "execute"},
		agentlink.ItemView{ID: "d", Rig: "r1", Kind: "fix", Step: "done"},
	)
	f.seedItems("m2", now, agentlink.ItemView{ID: "e", Rig: "r2", Kind: "task", Step: "review", WaitingForHuman: "review"})
	f.setUpdated("m1", "a", now.Add(-1*time.Minute))
	f.setUpdated("m1", "b", now.Add(-3*time.Minute))
	f.setUpdated("m2", "e", now.Add(-2*time.Minute))
	s := f.login()

	rec := f.api(http.MethodGet, "/api/v1/board", &s, nil)
	wantStatus(t, rec, http.StatusOK)
	b := decode[boardResp](t, rec)
	var got []string
	for _, it := range b.NeedsYou {
		got = append(got, it.MachineID+"/"+it.ID)
	}
	if strings.Join(got, ",") != "m1/b,m2/e,m1/a" {
		t.Fatalf("needs_you = %v", got)
	}
	var steps []string
	for _, l := range b.Lanes {
		steps = append(steps, l.Step)
		if len(l.Items) == 0 {
			t.Fatalf("empty lane %s returned", l.Step)
		}
	}
	if strings.Join(steps, ",") != "spec,plan,execute,review,done" {
		t.Fatalf("lanes = %v", steps)
	}

	// Filters.
	b = decode[boardResp](t, f.api(http.MethodGet, "/api/v1/board?machine=m1&kind=task", &s, nil))
	n := 0
	for _, l := range b.Lanes {
		for _, it := range l.Items {
			n++
			if it.MachineID != "m1" || it.Kind != "task" {
				t.Fatalf("filter leaked %+v", it)
			}
		}
	}
	if n != 3 || len(b.NeedsYou) != 2 {
		t.Fatalf("filtered: %d items, %d needs_you", n, len(b.NeedsYou))
	}
	b = decode[boardResp](t, f.api(http.MethodGet, "/api/v1/board?rig=r2", &s, nil))
	if len(b.Lanes) != 1 || b.Lanes[0].Items[0].ID != "e" {
		t.Fatalf("rig filter: %+v", b)
	}
}

func TestBoard_StaleFlag(t *testing.T) {
	f := newWebFixture(t)
	now := f.clock.Now()
	f.seedItems("old", now.Add(-121*time.Second), agentlink.ItemView{ID: "x", Step: "spec"})
	f.seedItems("new", now.Add(-120*time.Second), agentlink.ItemView{ID: "y", Step: "spec"})
	s := f.login()
	b := decode[boardResp](t, f.api(http.MethodGet, "/api/v1/board", &s, nil))
	if len(b.Lanes) != 1 || len(b.Lanes[0].Items) != 2 {
		t.Fatalf("board = %+v", b)
	}
	for _, it := range b.Lanes[0].Items {
		switch it.MachineID {
		case "old":
			if !it.Stale || it.FreshnessSeconds != 121 {
				t.Fatalf("old: %+v", it)
			}
		case "new":
			if it.Stale || it.FreshnessSeconds != 120 {
				t.Fatalf("new: %+v", it)
			}
		}
	}
}

func TestApprove_EnqueuesWith24hExpiry(t *testing.T) {
	f := newWebFixture(t)
	f.seedItems("m1", f.clock.Now(), agentlink.ItemView{ID: "i1", Step: "spec"})
	s := f.login()
	sha := strings.Repeat("b", 64)
	rec := f.api(http.MethodPost, "/api/v1/items/m1/i1/approve", &s, map[string]string{"artifact": "spec", "sha": sha, "actor": "mallory"})
	wantStatus(t, rec, http.StatusAccepted)
	resp := decode[map[string]string](t, rec)
	if resp["status"] != "pending" || len(resp["key"]) != 32 {
		t.Fatalf("resp = %v", resp)
	}
	reqs, err := f.store.PendingRequests("m1", f.clock.Now())
	if err != nil || len(reqs) != 1 {
		t.Fatalf("pending = %+v, %v", reqs, err)
	}
	r := reqs[0]
	if r.Key != resp["key"] || r.Type != "approve" || r.ItemID != "i1" || !r.ExpiresAt.Equal(f.clock.Now().Add(24*time.Hour)) {
		t.Fatalf("request = %+v", r)
	}
	var p agentlink.ApproveParams
	if err := json.Unmarshal(r.Params, &p); err != nil || p != (agentlink.ApproveParams{Artifact: "spec", SHA: sha, Actor: "admin"}) {
		t.Fatalf("params = %+v, %v", p, err)
	}

	rec = f.api(http.MethodPost, "/api/v1/items/m1/i1/request-changes", &s, map[string]string{"artifact": "spec", "sha": sha})
	wantStatus(t, rec, http.StatusAccepted)
	reqs, _ = f.store.PendingRequests("m1", f.clock.Now())
	// Same created_at under the fake clock, so order is by key: look up by type.
	if len(reqs) != 2 || (reqs[0].Type != "request_changes" && reqs[1].Type != "request_changes") {
		t.Fatalf("pending = %+v", reqs)
	}
}

func TestWebAPI_EnqueueValidation(t *testing.T) {
	f := newWebFixture(t)
	f.seedItems("m1", f.clock.Now(), agentlink.ItemView{ID: "i1", Step: "spec"})
	f.seedItems("gone", f.clock.Now(), agentlink.ItemView{ID: "i1", Step: "spec"})
	if err := f.store.RevokeMachine("gone"); err != nil {
		t.Fatal(err)
	}
	s := f.login()
	sha := strings.Repeat("c", 64)
	ok := map[string]string{"artifact": "spec", "sha": sha}
	cases := []struct {
		name, method, path string
		body               any
		code               int
		err                string
	}{
		{"unknown machine", "POST", "/api/v1/items/nope/i1/approve", ok, 404, "machine_not_found"},
		{"revoked machine", "POST", "/api/v1/items/gone/i1/approve", ok, 409, "machine_revoked"},
		{"unknown item", "POST", "/api/v1/items/m1/zz/approve", ok, 404, "item_not_found"},
		{"bad artifact", "POST", "/api/v1/items/m1/i1/approve", map[string]string{"artifact": "../x", "sha": sha}, 400, "invalid_artifact"},
		{"bad sha", "POST", "/api/v1/items/m1/i1/approve", map[string]string{"artifact": "spec", "sha": "xyz"}, 400, "invalid_sha"},
		{"bad json", "POST", "/api/v1/items/m1/i1/approve", []byte("{"), 400, "bad_request"},
		{"sling revoked", "POST", "/api/v1/items/gone/i1/sling", nil, 409, "machine_revoked"},
		{"cancel unknown item", "POST", "/api/v1/items/m1/zz/cancel", nil, 404, "item_not_found"},
		{"create unknown machine", "POST", "/api/v1/items", map[string]string{"machine": "nope", "rig": "r1", "kind": "task", "title": "t"}, 404, "machine_not_found"},
		{"create revoked", "POST", "/api/v1/items", map[string]string{"machine": "gone", "rig": "r1", "kind": "task", "title": "t"}, 409, "machine_revoked"},
		{"create bad rig", "POST", "/api/v1/items", map[string]string{"machine": "m1", "rig": "r 1", "kind": "task", "title": "t"}, 400, "invalid_rig"},
		{"create bad kind", "POST", "/api/v1/items", map[string]string{"machine": "m1", "rig": "r1", "kind": "story", "title": "t"}, 400, "invalid_kind"},
		{"create no title", "POST", "/api/v1/items", map[string]string{"machine": "m1", "rig": "r1", "kind": "task", "title": "  "}, 400, "invalid_title"},
		{"create long title", "POST", "/api/v1/items", map[string]string{"machine": "m1", "rig": "r1", "kind": "task", "title": strings.Repeat("t", 501)}, 400, "invalid_title"},
		{"create bad parent", "POST", "/api/v1/items", map[string]string{"machine": "m1", "rig": "r1", "kind": "task", "title": "t", "parent": "../p"}, 400, "invalid_parent"},
		{"revoke unknown", "DELETE", "/api/v1/machines/nope", nil, 404, "machine_not_found"},
		{"terminal unknown machine", "GET", "/api/v1/items/nope/i1/terminal", nil, 404, "machine_not_found"},
		{"status unknown", "GET", "/api/v1/requests/m1/nokey", nil, 404, "request_not_found"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantError(t, f.api(c.method, c.path, &s, c.body), c.code, c.err)
		})
	}
	if reqs, _ := f.store.PendingRequests("m1", f.clock.Now()); len(reqs) != 0 {
		t.Fatalf("invalid inputs enqueued: %+v", reqs)
	}
}

func TestWebAPI_SlingCancelCreate(t *testing.T) {
	f := newWebFixture(t)
	f.seedItems("m1", f.clock.Now(), agentlink.ItemView{ID: "i1", Step: "spec"})
	s := f.login()
	wantStatus(t, f.api(http.MethodPost, "/api/v1/items/m1/i1/sling", &s, nil), http.StatusAccepted)
	wantStatus(t, f.api(http.MethodPost, "/api/v1/items/m1/i1/cancel", &s, nil), http.StatusAccepted)
	wantStatus(t, f.api(http.MethodPost, "/api/v1/items", &s, map[string]string{"machine": "m1", "rig": "r1", "kind": "epic", "title": "  big   thing ", "parent": "p-1"}), http.StatusAccepted)
	reqs, _ := f.store.PendingRequests("m1", f.clock.Now())
	if len(reqs) != 3 {
		t.Fatalf("pending = %+v", reqs)
	}
	types := map[string]agentlink.Request{}
	for _, r := range reqs {
		types[r.Type] = r
		if !r.ExpiresAt.Equal(f.clock.Now().Add(24 * time.Hour)) {
			t.Fatalf("expiry %v", r.ExpiresAt)
		}
	}
	if types["item.sling"].ItemID != "i1" || types["item.cancel"].ItemID != "i1" {
		t.Fatalf("types = %+v", types)
	}
	var p map[string]string
	_ = json.Unmarshal(types["item.create"].Params, &p)
	if p["rig"] != "r1" || p["kind"] != "epic" || p["title"] != "big thing" || p["parent"] != "p-1" {
		t.Fatalf("create params = %v", p)
	}
}

func TestWebAPI_RequestStatusPolling(t *testing.T) {
	f := newWebFixture(t)
	f.seedItems("m1", f.clock.Now(), agentlink.ItemView{ID: "i1", Step: "spec"})
	s := f.login()
	rec := f.api(http.MethodPost, "/api/v1/items/m1/i1/sling", &s, nil)
	key := decode[map[string]string](t, rec)["key"]

	type statusResp struct {
		Status string            `json:"status"`
		Result *agentlink.Result `json:"result"`
	}
	st := decode[statusResp](t, f.api(http.MethodGet, "/api/v1/requests/m1/"+key, &s, nil))
	if st.Status != "pending" || st.Result != nil {
		t.Fatalf("status = %+v", st)
	}
	if err := f.store.addResult("m1", agentlink.Result{Key: key, OK: true}, f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	st = decode[statusResp](t, f.api(http.MethodGet, "/api/v1/requests/m1/"+key, &s, nil))
	if st.Status != "done" || st.Result == nil || !st.Result.OK {
		t.Fatalf("status = %+v", st)
	}
}

func TestWebAPI_TerminalAnswered(t *testing.T) {
	f := newWebFixture(t)
	f.seedItems("m1", f.clock.Now(), agentlink.ItemView{ID: "i1", Step: "execute"})
	f.app.termPoll = 5 * time.Millisecond
	f.app.termWait = 2 * time.Second
	s := f.login()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 400; i++ {
			reqs, _ := f.store.PendingRequests("m1", f.clock.Now())
			if len(reqs) == 1 {
				r := reqs[0]
				if r.Type != "terminal.peek" || r.ItemID != "i1" || !r.ExpiresAt.Equal(f.clock.Now().Add(60*time.Second)) {
					t.Errorf("peek request = %+v", r)
				}
				_ = f.store.addResult("m1", agentlink.Result{Key: r.Key, OK: true, Data: json.RawMessage(`{"lines":"$ go test\nok"}`)}, f.clock.Now())
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
		t.Error("no peek request seen")
	}()
	rec := f.api(http.MethodGet, "/api/v1/items/m1/i1/terminal", &s, nil)
	<-done
	wantStatus(t, rec, http.StatusOK)
	if got := decode[map[string]string](t, rec)["lines"]; got != "$ go test\nok" {
		t.Fatalf("lines = %q", got)
	}
}

func TestWebAPI_Terminal504(t *testing.T) {
	f := newWebFixture(t)
	f.seedItems("m1", f.clock.Now(), agentlink.ItemView{ID: "i1", Step: "execute"})
	f.app.termPoll = 5 * time.Millisecond
	f.app.termWait = 50 * time.Millisecond
	s := f.login()
	rec := f.api(http.MethodGet, "/api/v1/items/m1/i1/terminal", &s, nil)
	wantError(t, rec, http.StatusGatewayTimeout, "machine did not answer")
}

func TestWebAPI_ItemDetail(t *testing.T) {
	f := newWebFixture(t)
	f.seedItems("m1", f.clock.Now(), agentlink.ItemView{
		ID: "i1", Step: "execute", Title: "T",
		Artifacts: []agentlink.ArtifactView{{Name: "spec", SHA: "s1", Content: "# spec"}},
		Diffstat:  []agentlink.FileStat{{Path: "a.go", Added: 3, Deleted: 1}},
	})
	var evs []agentlink.Event
	base := f.clock.Now().Add(-time.Hour)
	for i := 0; i < 205; i++ {
		at := base.Add(time.Duration(i) * time.Second)
		evs = append(evs, agentlink.Event{ID: agentlink.NewEventID(at), At: at, Type: agentlink.EventEvidence, ItemID: "i1", Data: json.RawMessage(`{"n":1}`)})
	}
	evs = append(evs, agentlink.Event{ID: agentlink.NewEventID(base.Add(time.Hour)), At: base.Add(time.Hour), Type: agentlink.EventEvidence, ItemID: "other"})
	if _, err := f.store.AppendEvents("m1", evs); err != nil {
		t.Fatal(err)
	}
	s := f.login()
	rec := f.api(http.MethodGet, "/api/v1/items/m1/i1", &s, nil)
	wantStatus(t, rec, http.StatusOK)
	var d struct {
		Item      webItem                  `json:"item"`
		Artifacts []agentlink.ArtifactView `json:"artifacts"`
		Events    []agentlink.Event        `json:"events"`
		Diffstat  []agentlink.FileStat     `json:"diffstat"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Item.ID != "i1" || d.Item.MachineID != "m1" || d.Item.Title != "T" {
		t.Fatalf("item = %+v", d.Item)
	}
	if len(d.Artifacts) != 1 || d.Artifacts[0].Content != "# spec" {
		t.Fatalf("artifacts = %+v", d.Artifacts)
	}
	if len(d.Diffstat) != 1 || d.Diffstat[0].Path != "a.go" {
		t.Fatalf("diffstat = %+v", d.Diffstat)
	}
	if len(d.Events) != 200 {
		t.Fatalf("%d events, want 200", len(d.Events))
	}
	if d.Events[0].ID != evs[204].ID || d.Events[199].ID != evs[5].ID {
		t.Fatalf("events not newest first: first %s last %s", d.Events[0].ID, d.Events[199].ID)
	}
	wantError(t, f.api(http.MethodGet, "/api/v1/items/m1/nope", &s, nil), http.StatusNotFound, "item_not_found")
}

func TestWebAPI_MachinesEnrollRevoke(t *testing.T) {
	f := newWebFixture(t)
	f.seedItems("m1", f.clock.Now().Add(-200*time.Second), agentlink.ItemView{ID: "i1", Step: "spec"})
	s := f.login()

	type machine struct {
		ID               string          `json:"id"`
		Hostname         string          `json:"hostname"`
		FreshnessSeconds int64           `json:"freshness_seconds"`
		Stale            bool            `json:"stale"`
		RevokedAt        *time.Time      `json:"revoked_at"`
		Rigs             []agentlink.Rig `json:"rigs"`
	}
	ms := decode[[]machine](t, f.api(http.MethodGet, "/api/v1/machines", &s, nil))
	if len(ms) != 1 || ms[0].ID != "m1" || ms[0].Hostname != "h-m1" || !ms[0].Stale || ms[0].FreshnessSeconds != 200 || len(ms[0].Rigs) != 1 || ms[0].RevokedAt != nil {
		t.Fatalf("machines = %+v", ms)
	}

	rec := f.api(http.MethodPost, "/api/v1/enroll-codes", &s, nil)
	wantStatus(t, rec, http.StatusOK)
	var ec struct {
		Code      string    `json:"code"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ec); err != nil || len(ec.Code) != 26 || !ec.ExpiresAt.Equal(f.clock.Now().Add(15*time.Minute)) {
		t.Fatalf("enroll code = %+v, %v", ec, err)
	}
	var exp int64
	if err := f.store.db.QueryRow(`SELECT expires_at FROM enroll_codes WHERE sha = ?`, sha(ec.Code)).Scan(&exp); err != nil || !fromMS(exp).Equal(ec.ExpiresAt) {
		t.Fatalf("stored code expiry %v, %v", fromMS(exp), err)
	}

	wantStatus(t, f.api(http.MethodDelete, "/api/v1/machines/m1", &s, nil), http.StatusOK)
	if m, _ := f.store.Machine("m1"); m.RevokedAt == nil {
		t.Fatal("machine not revoked")
	}
	noCSRF := webSession{cookie: s.cookie}
	wantError(t, f.api(http.MethodDelete, "/api/v1/machines/m1", &noCSRF, nil), http.StatusForbidden, "csrf")
}

func TestStatic_SPAFallbackAndAPI404(t *testing.T) {
	f := newWebFixture(t)
	f.app.ui = fstest.MapFS{} // no UI build: the placeholder is served
	rec := f.serve(httptest.NewRequest(http.MethodGet, "/items/x/y", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "text/html") || !strings.Contains(rec.Body.String(), "UI not built") {
		t.Fatalf("SPA fallback: %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	wantSecurityHeaders(t, rec)
	rec = f.serve(httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "UI not built") {
		t.Fatalf("GET /: %d", rec.Code)
	}

	s := f.login()
	wantError(t, f.api(http.MethodGet, "/api/v1/nope", &s, nil), http.StatusNotFound, "not_found")
	for _, p := range []string{"/api/v2/x", "/auth/nope", "/agent/v2/x", "/healthz/x"} {
		rec := f.serve(httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "UI not built") {
			t.Fatalf("%s: %d %q", p, rec.Code, rec.Body.String())
		}
	}

	rec = f.serve(httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("health: %d %q", rec.Code, rec.Body.String())
	}
	// The agent API is mounted.
	wantError(t, f.serve(httptest.NewRequest(http.MethodPost, "/agent/v1/heartbeat", nil)), http.StatusUnauthorized, "unauthorized")
}

func TestSSE_BoardChangedAfterAppendEvents(t *testing.T) {
	f := newWebFixture(t)
	f.seedItems("m1", f.clock.Now(), agentlink.ItemView{ID: "i1", Step: "spec"})
	f.app.sseDebounce = 20 * time.Millisecond
	s := f.login()
	srv := httptest.NewServer(f.app)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/events", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: s.cookie.Value})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("events: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	waitFor(t, func() bool { return f.store.board.count() == 1 })

	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()

	at := f.clock.Now()
	if _, err := f.store.AppendEvents("m1", []agentlink.Event{{ID: agentlink.NewEventID(at), At: at, Type: agentlink.EventHeartbeat}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for got := false; !got; {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatal("stream closed")
			}
			got = l == "event: board.changed"
		case <-deadline:
			t.Fatal("no board.changed")
		}
	}

	cancel()
	waitFor(t, func() bool { return f.store.board.count() == 0 })
}

func TestSSE_RequiresSession(t *testing.T) {
	f := newWebFixture(t)
	wantError(t, f.api(http.MethodGet, "/api/v1/events", nil, nil), http.StatusUnauthorized, "unauthorized")
}

func TestMaintenance_DeletesExpiredSessions(t *testing.T) {
	f := newWebFixture(t)
	s := f.login() // expires at clock+7d
	f.clock.Advance(8 * 24 * time.Hour)
	fresh := f.login()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunMaintenance(ctx, f.store, f.clock.Now, time.Hour, func(string, ...any) {})
		close(done)
	}()
	waitFor(t, func() bool {
		var n int
		_ = f.store.db.QueryRow(`SELECT count(*) FROM web_sessions WHERE sha = ?`, sha(s.cookie.Value)).Scan(&n)
		return n == 0
	})
	var n int
	if err := f.store.db.QueryRow(`SELECT count(*) FROM web_sessions WHERE sha = ?`, sha(fresh.cookie.Value)).Scan(&n); err != nil || n != 1 {
		t.Fatalf("fresh session deleted (n=%d, %v)", n, err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("maintenance did not stop on cancel")
	}
}

func TestMaintenance_ExpiresRequests(t *testing.T) {
	f := newWebFixture(t)
	addMachine(t, f.store, "m1")
	if err := f.store.enqueueRequest("m1", agentlink.Request{Key: "k", Type: "inspect", ExpiresAt: f.clock.Now().Add(time.Minute)}, f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(2 * time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunMaintenance(ctx, f.store, f.clock.Now, time.Hour, func(string, ...any) {})
	waitFor(t, func() bool {
		var st string
		_ = f.store.db.QueryRow(`SELECT status FROM requests WHERE key = 'k'`).Scan(&st)
		return st == RequestExpired
	})
}

func (f *webFixture) seedMachineToken(id string) string {
	f.t.Helper()
	addMachine(f.t, f.store, id)
	tok := "machine-token-" + id
	if err := f.store.AddToken(id, sha256Hex(tok)); err != nil {
		f.t.Fatal(err)
	}
	return tok
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in 5s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
