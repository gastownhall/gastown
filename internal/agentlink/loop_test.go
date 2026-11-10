package agentlink_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/agentlink"
	"github.com/steveyegge/gastown/internal/server"
)

const testMachine = "m1"

// loopFixture is a real server.AgentAPI over a temp store behind httptest,
// with a middleware that records calls and can fail the first N of them.
type loopFixture struct {
	t     *testing.T
	store *server.Store
	srv   *httptest.Server
	token string
	town  string
	now   time.Time

	failFirst   atomic.Int32 // remaining calls to answer 503
	failResults atomic.Int32 // remaining POST /results calls to answer 503
	helloMu     sync.Mutex
	helloAt     []time.Time // time of every POST /hello that reached the fixture

	mu        sync.Mutex
	calls     []string // path of every call that reached the API
	accepted  []string // event IDs of POST /events answered 200
	snapshots int
	conflicts int
	results   []agentlink.Result // bodies of POST /results answered 200
}

func newLoopFixture(t *testing.T) *loopFixture {
	t.Helper()
	st, err := server.OpenStore(filepath.Join(t.TempDir(), "gtb.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &loopFixture{t: t, store: st, token: "tok-secret-m1", town: t.TempDir(), now: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
	if err := st.UpsertMachine(testMachine, agentlink.Hello{MachineID: testMachine, Protocol: agentlink.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(f.token))
	if err := st.AddToken(testMachine, hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	api := server.NewAgentAPI(st, func() time.Time { return f.now })
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/agent/v1/hello" {
			f.helloMu.Lock()
			f.helloAt = append(f.helloAt, time.Now())
			f.helloMu.Unlock()
		}
		if r.URL.Path == "/agent/v1/results" && f.failResults.Load() > 0 {
			f.failResults.Add(-1)
			http.Error(w, `{"error":"unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		if f.failFirst.Load() > 0 {
			f.failFirst.Add(-1)
			http.Error(w, `{"error":"unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		rec := &statusRecorder{ResponseWriter: w, code: 200}
		api.ServeHTTP(rec, r)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, r.URL.Path)
		switch r.URL.Path {
		case "/agent/v1/events":
			if rec.code == http.StatusConflict {
				f.conflicts++
			}
			if rec.code == 200 {
				var b struct{ Events []agentlink.Event }
				_ = json.Unmarshal(body, &b)
				for _, e := range b.Events {
					f.accepted = append(f.accepted, e.ID)
				}
			}
		case "/agent/v1/snapshot":
			if rec.code == 200 {
				f.snapshots++
			}
		case "/agent/v1/results":
			if rec.code == 200 {
				var rs []agentlink.Result
				_ = json.Unmarshal(body, &rs)
				f.results = append(f.results, rs...)
			}
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(c int) { s.code = c; s.ResponseWriter.WriteHeader(c) }
func (s *statusRecorder) Flush() {
	if fl, ok := s.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}

type logBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logBuf) Logf(format string, args ...any) {
	l.mu.Lock()
	fmt.Fprintf(&l.b, format+"\n", args...)
	l.mu.Unlock()
}

func (l *logBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func (f *loopFixture) config(apply agentlink.Applier, logs *logBuf) agentlink.LoopConfig {
	return agentlink.LoopConfig{
		TownRoot:       f.town,
		ServerURL:      f.srv.URL,
		MachineID:      testMachine,
		Token:          f.token,
		GtbVersion:     "test",
		Collect:        func() (agentlink.Snapshot, error) { return agentlink.Snapshot{}, nil },
		Apply:          apply,
		Now:            func() time.Time { return f.now },
		Logf:           logs.Logf,
		PollWait:       50 * time.Millisecond,
		HeartbeatEvery: time.Hour,
		BackoffMin:     5 * time.Millisecond,
		BackoffMax:     20 * time.Millisecond,
	}
}

// run starts the loop and returns a stop function that cancels it and
// returns Run's error.
func run(t *testing.T, cfg agentlink.LoopConfig) func() error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- agentlink.Run(ctx, cfg) }()
	var once sync.Once
	var err error
	stop := func() error {
		once.Do(func() {
			cancel()
			select {
			case err = <-done:
			case <-time.After(10 * time.Second):
				err = errors.New("loop did not stop")
			}
		})
		return err
	}
	t.Cleanup(func() { stop() })
	return stop
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func upsert(item string) agentlink.Event {
	iv, _ := json.Marshal(agentlink.ItemView{ID: item, Rig: "r", Kind: "task", Step: "spec"})
	return agentlink.Event{At: time.Now().UTC(), Type: agentlink.EventItemUpsert, ItemID: item, Data: iv}
}

// countingApplier records the requests it is asked to apply.
type countingApplier struct {
	mu   sync.Mutex
	seen []agentlink.Request
	fn   func(agentlink.Request) agentlink.Result
}

func (c *countingApplier) Apply(_ context.Context, r agentlink.Request) agentlink.Result {
	c.mu.Lock()
	c.seen = append(c.seen, r)
	c.mu.Unlock()
	if c.fn != nil {
		return c.fn(r)
	}
	return agentlink.Result{Key: r.Key, OK: true}
}

func (c *countingApplier) count() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.seen) }

func TestAgentLoop_OfflineQueuesAndReplays(t *testing.T) {
	f := newLoopFixture(t)
	if err := f.store.ApplySnapshot(testMachine, agentlink.Snapshot{}); err != nil {
		t.Fatal(err)
	}
	q := agentlink.OpenQueue(f.town)
	for i := 0; i < 5; i++ {
		if err := q.Append(upsert(fmt.Sprintf("it-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	want, _ := q.Pending(100)
	f.failFirst.Store(3) // server down for the first three calls
	logs := &logBuf{}
	stop := run(t, f.config(&countingApplier{}, logs))

	eventually(t, "events replayed", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.accepted) >= len(want)
	})
	// More events while connected are sent too, after the first batch.
	if err := q.Append(upsert("late")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "late event", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.accepted) == len(want)+1
	})
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	got := append([]string(nil), f.accepted...)
	f.mu.Unlock()
	for i, e := range want {
		if got[i] != e.ID {
			t.Fatalf("accepted[%d] = %s, want %s (all %v)", i, got[i], e.ID, got)
		}
	}
	seen := map[string]bool{}
	for _, id := range got {
		if seen[id] {
			t.Fatalf("event %s delivered twice", id)
		}
		seen[id] = true
	}
	m, err := f.store.Machine(testMachine)
	if err != nil {
		t.Fatal(err)
	}
	if m.Cursor != got[len(got)-1] {
		t.Fatalf("server cursor %s, want %s", m.Cursor, got[len(got)-1])
	}
	if c, _ := q.Cursor(); c != m.Cursor {
		t.Fatalf("local cursor %s, server %s", c, m.Cursor)
	}
	if p, _ := q.Pending(10); len(p) != 0 {
		t.Fatalf("still pending: %d", len(p))
	}
	items, _ := f.store.Items(server.ItemFilter{MachineID: testMachine})
	if len(items) != 6 {
		t.Fatalf("server items = %d, want 6", len(items))
	}
	if !strings.Contains(logs.String(), "agentlink: connected to "+f.srv.URL) {
		t.Fatalf("missing connected log: %s", logs.String())
	}
	if strings.Contains(logs.String(), f.token) {
		t.Fatal("token logged")
	}
}

func TestAgentLoop_ResyncOn409(t *testing.T) {
	f := newLoopFixture(t)
	// The server already has a snapshot (hello says no resync) but at a
	// cursor this machine never acked.
	other := agentlink.NewEventID(time.Now().Add(-time.Hour))
	if err := f.store.ApplySnapshot(testMachine, agentlink.Snapshot{Cursor: other}); err != nil {
		t.Fatal(err)
	}
	q := agentlink.OpenQueue(f.town)
	for i := 0; i < 3; i++ {
		q.Append(upsert(fmt.Sprintf("it-%d", i)))
	}
	ev := agentlink.Event{At: time.Now().UTC(), Type: agentlink.EventEvidence, ItemID: "it-1", Data: json.RawMessage(`{"kind":"tdd"}`)}
	q.Append(ev)
	collected := atomic.Int32{}
	cfg := f.config(&countingApplier{}, &logBuf{})
	cfg.Collect = func() (agentlink.Snapshot, error) {
		collected.Add(1)
		return agentlink.Snapshot{Rigs: []agentlink.Rig{{Name: "r", Gated: true}}, Items: []agentlink.ItemView{{ID: "it-0", Rig: "r"}}, Cursor: "ignored-by-loop"}, nil
	}
	stop := run(t, cfg)
	// The pending upserts are superseded by the snapshot; the evidence
	// event is kept (re-queued) and delivered after it.
	eventually(t, "evidence after resync", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.accepted) == 1
	})
	q.Append(upsert("after"))
	eventually(t, "new event after resync", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.accepted) == 2
	})
	stop()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.conflicts != 1 || f.snapshots != 1 || collected.Load() != 1 {
		t.Fatalf("conflicts=%d snapshots=%d collected=%d", f.conflicts, f.snapshots, collected.Load())
	}
	idx409, idxSnap := -1, -1
	for i, c := range f.calls {
		if c == "/agent/v1/events" && idx409 < 0 {
			idx409 = i
		}
		if c == "/agent/v1/snapshot" {
			idxSnap = i
		}
	}
	if idxSnap < idx409 {
		t.Fatalf("snapshot not after the 409: %v", f.calls)
	}
	m, _ := f.store.Machine(testMachine)
	if c, _ := q.Cursor(); c != m.Cursor || c != f.accepted[1] {
		t.Fatalf("local cursor %s, server %s, last accepted %s", c, m.Cursor, f.accepted[1])
	}
	items, _ := f.store.Items(server.ItemFilter{MachineID: testMachine})
	if len(items) != 2 { // it-0 from the snapshot, "after" from the event
		t.Fatalf("server items = %d, want 2", len(items))
	}
}

func TestAgentLoop_HelloResyncSendsSnapshot(t *testing.T) {
	f := newLoopFixture(t)
	q := agentlink.OpenQueue(f.town)
	q.Append(upsert("a"))
	pend, _ := q.Pending(1)
	q.Ack(pend[0].ID) // acked locally; server never saw a snapshot
	var snapCursor atomic.Value
	cfg := f.config(&countingApplier{}, &logBuf{})
	cfg.Collect = func() (agentlink.Snapshot, error) { return agentlink.Snapshot{}, nil }
	stop := run(t, cfg)
	eventually(t, "snapshot", func() bool {
		m, _ := f.store.Machine(testMachine)
		if m != nil && m.HasSnapshot {
			snapCursor.Store(m.Cursor)
			return true
		}
		return false
	})
	stop()
	if snapCursor.Load() != pend[0].ID {
		t.Fatalf("snapshot cursor %v, want acked %s", snapCursor.Load(), pend[0].ID)
	}
}

func enqueue(t *testing.T, f *loopFixture, r agentlink.Request) {
	t.Helper()
	if err := f.store.EnqueueRequest(testMachine, r); err != nil {
		t.Fatal(err)
	}
}

func resultFor(t *testing.T, f *loopFixture, key string) *agentlink.Result {
	t.Helper()
	_, res, err := f.store.RequestStatus(testMachine, key, f.now)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestAgentLoop_DropsExpired(t *testing.T) {
	f := newLoopFixture(t)
	f.now = time.Now().UTC() // EnqueueRequest checks expiry against the wall clock
	enqueue(t, f, agentlink.Request{Key: "k-exp", Type: "approve", ItemID: "gt-1", Params: json.RawMessage(`{}`), ExpiresAt: f.now.Add(time.Hour)})
	app := &countingApplier{}
	cfg := f.config(app, &logBuf{})
	cfg.Now = func() time.Time { return f.now.Add(2 * time.Hour) } // machine clock is past ExpiresAt
	stop := run(t, cfg)
	eventually(t, "expired result", func() bool { return resultFor(t, f, "k-exp") != nil })
	stop()
	if app.count() != 0 {
		t.Fatalf("applier called %d times for an expired request", app.count())
	}
	res := resultFor(t, f, "k-exp")
	if res.OK || res.Error != "expired" {
		t.Fatalf("result = %+v, want expired", res)
	}
}

func TestAgentLoop_DuplicateKeyRepostsStoredResult(t *testing.T) {
	f := newLoopFixture(t)
	f.now = time.Now().UTC()
	applied, err := agentlink.OpenAppliedSet(f.town, 100)
	if err != nil {
		t.Fatal(err)
	}
	stored := agentlink.Result{Key: "k-dup", OK: true, Data: json.RawMessage(`{"from":"before"}`)}
	if err := applied.Put(stored); err != nil {
		t.Fatal(err)
	}
	enqueue(t, f, agentlink.Request{Key: "k-dup", Type: "item.sling", ItemID: "gt-1", ExpiresAt: f.now.Add(time.Hour)})
	enqueue(t, f, agentlink.Request{Key: "k-new", Type: "item.sling", ItemID: "gt-2", ExpiresAt: f.now.Add(time.Hour)})
	app := &countingApplier{fn: func(r agentlink.Request) agentlink.Result {
		return agentlink.Result{Key: r.Key, OK: true, Data: json.RawMessage(`{"from":"apply"}`)}
	}}
	stop := run(t, f.config(app, &logBuf{}))
	eventually(t, "both results", func() bool { return resultFor(t, f, "k-dup") != nil && resultFor(t, f, "k-new") != nil })
	stop()
	if app.count() != 1 || app.seen[0].Key != "k-new" {
		t.Fatalf("applier saw %+v, want only k-new", app.seen)
	}
	if r := resultFor(t, f, "k-dup"); !r.OK || string(r.Data) != `{"from":"before"}` {
		t.Fatalf("dup result = %+v", r)
	}
	// The new key is now in the applied set with its result.
	again, _ := agentlink.OpenAppliedSet(f.town, 100)
	if r, ok := again.Get("k-new"); !ok || string(r.Data) != `{"from":"apply"}` {
		t.Fatalf("k-new not stored: %+v %v", r, ok)
	}
}

func TestAgentLoop_ResultPostedForEveryRequest(t *testing.T) {
	f := newLoopFixture(t)
	f.now = time.Now().UTC()
	enqueue(t, f, agentlink.Request{Key: "k-insp", Type: "inspect", ItemID: "gt-9", ExpiresAt: f.now.Add(time.Hour)})
	app := &countingApplier{fn: func(r agentlink.Request) agentlink.Result {
		return agentlink.Result{Key: r.Key, OK: false, Error: "unsupported"}
	}}
	stop := run(t, f.config(app, &logBuf{}))
	eventually(t, "result", func() bool { return resultFor(t, f, "k-insp") != nil })
	stop()
	if r := resultFor(t, f, "k-insp"); r.OK || r.Error != "unsupported" {
		t.Fatalf("result = %+v", r)
	}
}

func TestAgentLoop_SnapshotRequestResyncs(t *testing.T) {
	f := newLoopFixture(t)
	f.now = time.Now().UTC()
	if err := f.store.ApplySnapshot(testMachine, agentlink.Snapshot{}); err != nil {
		t.Fatal(err)
	}
	enqueue(t, f, agentlink.Request{Key: "k-snap", Type: "snapshot", ExpiresAt: f.now.Add(time.Hour)})
	stop := run(t, f.config(agentlink.ApplierFunc(func(_ context.Context, r agentlink.Request) agentlink.Result {
		return agentlink.Result{Key: r.Key, OK: true}
	}), &logBuf{}))
	eventually(t, "snapshot after request", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.snapshots == 1
	})
	stop()
	if r := resultFor(t, f, "k-snap"); r == nil || !r.OK {
		t.Fatalf("result = %+v", r)
	}
}

func TestAgentLoop_401Stops(t *testing.T) {
	f := newLoopFixture(t)
	logs := &logBuf{}
	cfg := f.config(&countingApplier{}, logs)
	cfg.Token = "not-the-token"
	done := make(chan error, 1)
	go func() { done <- agentlink.Run(context.Background(), cfg) }()
	select {
	case err := <-done:
		if !errors.Is(err, agentlink.ErrTokenRejected) {
			t.Fatalf("err = %v, want ErrTokenRejected", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("loop kept running after 401")
	}
	if !strings.Contains(logs.String(), "agentlink: token rejected; run gtb join again") {
		t.Fatalf("log = %q", logs.String())
	}
	if strings.Contains(logs.String(), "not-the-token") {
		t.Fatal("token logged")
	}
}

func TestAgentLoop_HeartbeatAndScan(t *testing.T) {
	f := newLoopFixture(t)
	scans := atomic.Int32{}
	q := agentlink.OpenQueue(f.town)
	cfg := f.config(&countingApplier{}, &logBuf{})
	cfg.HeartbeatEvery = 20 * time.Millisecond
	cfg.Scan = func() error {
		if scans.Add(1) == 1 {
			return q.Append(upsert("scanned"))
		}
		return nil
	}
	stop := run(t, cfg)
	eventually(t, "heartbeats and scanned event", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		hb := 0
		for _, c := range f.calls {
			if c == "/agent/v1/heartbeat" {
				hb++
			}
		}
		return hb >= 2 && len(f.accepted) == 1
	})
	stop()
}

// Bodies are kept under the server's limits (8 MiB snapshot/events, 1 MiB
// results), otherwise a 413 would wedge the loop forever.
func TestAgentLoop_LargeSnapshotAndResultsAreSplit(t *testing.T) {
	f := newLoopFixture(t)
	f.now = time.Now().UTC()
	big := strings.Repeat("a", 200<<10)
	var items []agentlink.ItemView // ~12 MiB of artifacts
	for i := 0; i < 60; i++ {
		items = append(items, agentlink.ItemView{ID: fmt.Sprintf("it-%d", i), Rig: "r", Kind: "task", Step: "spec",
			Artifacts: []agentlink.ArtifactView{{Name: "spec", SHA: fmt.Sprintf("sha%d", i), Content: big}}})
	}
	cfg := f.config(&countingApplier{fn: func(r agentlink.Request) agentlink.Result {
		return agentlink.Result{Key: r.Key, OK: true, Data: json.RawMessage(`{"x":"` + strings.Repeat("b", 400<<10) + `"}`)}
	}}, &logBuf{})
	cfg.Collect = func() (agentlink.Snapshot, error) { return agentlink.Snapshot{Items: items}, nil }
	for i := 0; i < 4; i++ { // ~1.6 MiB of results
		enqueue(t, f, agentlink.Request{Key: fmt.Sprintf("k%d", i), Type: "inspect", ItemID: "it-1", ExpiresAt: f.now.Add(time.Hour)})
	}
	q := agentlink.OpenQueue(f.town)
	stop := run(t, cfg)
	eventually(t, "all delivered", func() bool {
		for i := 0; i < 4; i++ {
			if resultFor(t, f, fmt.Sprintf("k%d", i)) == nil {
				return false
			}
		}
		m, _ := f.store.Machine(testMachine)
		p, _ := q.Pending(1)
		return m.HasSnapshot && len(p) == 0
	})
	stop()
	for _, id := range []string{"it-0", "it-59"} {
		_, arts, err := f.store.Item(testMachine, id)
		if err != nil || len(arts) != 1 || len(arts[0].Content) != len(big) {
			t.Fatalf("%s artifacts = %d %v", id, len(arts), err)
		}
	}
	if r := resultFor(t, f, "k3"); !r.OK {
		t.Fatalf("k3 = %+v", r)
	}
}

func TestAgentLoop_LargeEventBacklogIsSplit(t *testing.T) {
	f := newLoopFixture(t)
	f.store.ApplySnapshot(testMachine, agentlink.Snapshot{})
	big := strings.Repeat("a", 200<<10)
	q := agentlink.OpenQueue(f.town)
	for i := 0; i < 45; i++ { // ~9 MiB of events
		id := fmt.Sprintf("it-%d", i)
		e := agentlink.Event{Type: agentlink.EventItemUpsert, ItemID: id}
		e.Data, _ = json.Marshal(agentlink.ItemView{ID: id, Rig: "r", Kind: "task", Step: "spec",
			Artifacts: []agentlink.ArtifactView{{Name: "spec", SHA: fmt.Sprintf("new%d", i), Content: big}}})
		q.Append(e)
	}
	stop := run(t, f.config(&countingApplier{}, &logBuf{}))
	eventually(t, "drained", func() bool { p, _ := q.Pending(1); return len(p) == 0 })
	stop()
	f.mu.Lock()
	n, snaps := len(f.accepted), f.snapshots
	f.mu.Unlock()
	if n != 45 || snaps != 0 {
		t.Fatalf("accepted %d events, %d snapshots", n, snaps)
	}
	if _, arts, _ := f.store.Item(testMachine, "it-44"); len(arts) != 1 || arts[0].SHA != "new44" {
		t.Fatalf("it-44 artifact = %+v", arts)
	}
}

// A result too large to post is replaced by a too_large failure.
func TestAgentLoop_OversizedResult(t *testing.T) {
	f := newLoopFixture(t)
	f.now = time.Now().UTC()
	enqueue(t, f, agentlink.Request{Key: "k-big", Type: "inspect", ItemID: "x", ExpiresAt: f.now.Add(time.Hour)})
	stop := run(t, f.config(&countingApplier{fn: func(r agentlink.Request) agentlink.Result {
		return agentlink.Result{Key: r.Key, OK: true, Data: json.RawMessage(`"` + strings.Repeat("c", 2<<20) + `"`)}
	}}, &logBuf{}))
	eventually(t, "result", func() bool { return resultFor(t, f, "k-big") != nil })
	stop()
	if r := resultFor(t, f, "k-big"); r.OK || r.Error != "too_large" {
		t.Fatalf("result = %+v", r)
	}
}

// An event the server refuses as invalid (400) must not wedge the queue:
// the batch is dropped with a log line and later events still flow.
func TestAgentLoop_RejectedBatchDoesNotWedge(t *testing.T) {
	f := newLoopFixture(t)
	f.store.ApplySnapshot(testMachine, agentlink.Snapshot{})
	q := agentlink.OpenQueue(f.town)
	q.Append(agentlink.Event{Type: "bogus", ItemID: "x", Data: json.RawMessage(`{}`)})
	logs := &logBuf{}
	stop := run(t, f.config(&countingApplier{}, logs))
	eventually(t, "queue drained", func() bool { p, _ := q.Pending(1); return len(p) == 0 })
	q.Append(upsert("good"))
	eventually(t, "good event", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.accepted) == 1
	})
	stop()
	if !strings.Contains(logs.String(), "dropped 1 event") {
		t.Fatalf("log = %s", logs.String())
	}
}

// R35a: a request that was applied, but whose result never reached the
// server, is re-delivered; even after ExpiresAt the stored result (not
// "expired") is re-posted, and it is not applied again.
func TestAgentLoop_AppliedThenExpiredRepostsStoredResult(t *testing.T) {
	f := newLoopFixture(t)
	wall := time.Now().UTC()
	f.now = wall
	enqueue(t, f, agentlink.Request{Key: "k-ran", Type: "item.sling", ItemID: "gt-1", ExpiresAt: wall.Add(time.Hour)})
	f.failResults.Store(1) // the first results POST is lost
	var clock atomic.Int64
	clock.Store(wall.UnixNano())
	app := &countingApplier{fn: func(r agentlink.Request) agentlink.Result {
		clock.Store(wall.Add(2 * time.Hour).UnixNano()) // time passes past ExpiresAt
		return agentlink.Result{Key: r.Key, OK: true, Data: json.RawMessage(`{"ran":true}`)}
	}}
	cfg := f.config(app, &logBuf{})
	cfg.Now = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	stop := run(t, cfg)
	eventually(t, "result posted", func() bool { return resultFor(t, f, "k-ran") != nil })
	stop()
	if app.count() != 1 {
		t.Fatalf("applied %d times", app.count())
	}
	if r := resultFor(t, f, "k-ran"); !r.OK || string(r.Data) != `{"ran":true}` {
		t.Fatalf("result = %+v, want the stored result", r)
	}
}

// R35b: backoff resets only after a full productive round; a session that
// says hello but then keeps failing (here: Collect) backs off further.
func TestAgentLoop_BackoffGrowsWhenRoundsFail(t *testing.T) {
	f := newLoopFixture(t) // no snapshot yet: hello says resync
	var mu sync.Mutex
	var attempts []time.Time
	cfg := f.config(&countingApplier{}, &logBuf{})
	cfg.BackoffMin, cfg.BackoffMax = 20*time.Millisecond, 400*time.Millisecond
	cfg.Collect = func() (agentlink.Snapshot, error) {
		mu.Lock()
		attempts = append(attempts, time.Now())
		mu.Unlock()
		return agentlink.Snapshot{}, errors.New("dolt down")
	}
	stop := run(t, cfg)
	eventually(t, "6 attempts", func() bool { mu.Lock(); defer mu.Unlock(); return len(attempts) >= 6 })
	stop()
	mu.Lock()
	defer mu.Unlock()
	// Jitter is [d/2, d]: with doubling from 20ms, the 5th gap is >= 160ms.
	if gap := attempts[5].Sub(attempts[4]); gap < 150*time.Millisecond {
		t.Fatalf("backoff did not grow: gaps %v", gaps(attempts))
	}
}

func gaps(ts []time.Time) []time.Duration {
	var out []time.Duration
	for i := 1; i < len(ts); i++ {
		out = append(out, ts[i].Sub(ts[i-1]).Round(time.Millisecond))
	}
	return out
}

// R35c/R37: read-only requests are not recorded in the applied set at all
// (they must not push mutating keys out of the 10k window); mutating
// requests keep the full result.
func TestAgentLoop_AppliedSetStoresOnlyMutatingResults(t *testing.T) {
	f := newLoopFixture(t)
	f.now = time.Now().UTC()
	for _, typ := range []string{"inspect", "terminal.peek", "snapshot", "approve", "item.create"} {
		enqueue(t, f, agentlink.Request{Key: "k-" + typ, Type: typ, ItemID: "gt-1", ExpiresAt: f.now.Add(time.Hour)})
	}
	app := &countingApplier{fn: func(r agentlink.Request) agentlink.Result {
		return agentlink.Result{Key: r.Key, OK: true, Data: json.RawMessage(`{"big":"` + strings.Repeat("z", 1000) + `"}`)}
	}}
	stop := run(t, f.config(app, &logBuf{}))
	eventually(t, "results", func() bool { return resultFor(t, f, "k-item.create") != nil && resultFor(t, f, "k-inspect") != nil })
	stop()
	// The server still got the full read-only result.
	if r := resultFor(t, f, "k-inspect"); !r.OK || len(r.Data) < 1000 {
		t.Fatalf("inspect result posted = %+v", r)
	}
	set, _ := agentlink.OpenAppliedSet(f.town, 100)
	for _, typ := range []string{"inspect", "terminal.peek", "snapshot"} {
		if r, ok := set.Get("k-" + typ); ok {
			t.Fatalf("read-only %s recorded in the applied set: %+v", typ, r)
		}
	}
	if n := set.Len(); n != 2 {
		t.Fatalf("applied set holds %d keys, want 2 (mutating only)", n)
	}
	for _, typ := range []string{"approve", "item.create"} {
		if r, ok := set.Get("k-" + typ); !ok || len(r.Data) < 1000 {
			t.Fatalf("%s stored = %+v %v, want full result", typ, r, ok)
		}
	}
}

// R35c: a re-delivered read-only request is re-run (its data is not kept).
func TestAgentLoop_ReadOnlyRedeliveryReruns(t *testing.T) {
	f := newLoopFixture(t)
	f.now = time.Now().UTC()
	applied, _ := agentlink.OpenAppliedSet(f.town, 100)
	applied.Put(agentlink.Result{Key: "k-peek", OK: true}) // ran before; result lost
	enqueue(t, f, agentlink.Request{Key: "k-peek", Type: "terminal.peek", ItemID: "gt-1", ExpiresAt: f.now.Add(time.Hour)})
	app := &countingApplier{fn: func(r agentlink.Request) agentlink.Result {
		return agentlink.Result{Key: r.Key, OK: true, Data: json.RawMessage(`{"lines":"fresh"}`)}
	}}
	stop := run(t, f.config(app, &logBuf{}))
	eventually(t, "result", func() bool { return resultFor(t, f, "k-peek") != nil })
	stop()
	if app.count() != 1 {
		t.Fatalf("read-only request not re-run: %d", app.count())
	}
	if r := resultFor(t, f, "k-peek"); string(r.Data) != `{"lines":"fresh"}` {
		t.Fatalf("result = %+v", r)
	}
}

// R37: a session whose results POST keeps failing is not productive, so the
// backoff grows instead of resetting after every hello.
func TestAgentLoop_BackoffGrowsWhenResultsFail(t *testing.T) {
	f := newLoopFixture(t)
	f.now = time.Now().UTC()
	f.store.ApplySnapshot(testMachine, agentlink.Snapshot{})
	enqueue(t, f, agentlink.Request{Key: "k-i", Type: "inspect", ItemID: "x", ExpiresAt: f.now.Add(time.Hour)})
	f.failResults.Store(1 << 20)
	cfg := f.config(&countingApplier{}, &logBuf{})
	cfg.BackoffMin, cfg.BackoffMax = 20*time.Millisecond, 400*time.Millisecond
	stop := run(t, cfg)
	eventually(t, "6 hellos", func() bool { f.helloMu.Lock(); defer f.helloMu.Unlock(); return len(f.helloAt) >= 6 })
	stop()
	f.helloMu.Lock()
	defer f.helloMu.Unlock()
	// Each session also waits PollWait (50ms) for the re-delivery; without
	// the fix every gap is ~50-70ms. With doubling, the 5th gap is >= 210ms.
	if gap := f.helloAt[5].Sub(f.helloAt[4]); gap < 150*time.Millisecond {
		t.Fatalf("backoff did not grow: gaps %v", gaps(f.helloAt))
	}
}
