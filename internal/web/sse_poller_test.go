package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingCompute returns a compute func that counts calls and returns a
// hash taken from *value.
func countingCompute(calls *atomic.Int64, value *atomic.Value) func(context.Context) string {
	return func(context.Context) string {
		calls.Add(1)
		return value.Load().(string)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestDashboardHashPoller_IdleDoesNotPoll(t *testing.T) {
	var calls atomic.Int64
	var value atomic.Value
	value.Store("h1")
	_ = newDashboardHashPoller(5*time.Millisecond, countingCompute(&calls, &value))

	time.Sleep(30 * time.Millisecond)
	if n := calls.Load(); n != 0 {
		t.Fatalf("poller with no subscribers computed %d times, want 0", n)
	}
}

func TestDashboardHashPoller_SharedAcrossSubscribers(t *testing.T) {
	var calls atomic.Int64
	var value atomic.Value
	value.Store("h1")
	p := newDashboardHashPoller(20*time.Millisecond, countingCompute(&calls, &value))

	var unsubs []func()
	for i := 0; i < 10; i++ {
		unsubs = append(unsubs, p.subscribe())
	}
	time.Sleep(110 * time.Millisecond)
	for _, u := range unsubs {
		u()
	}

	// One loop: ~1 immediate + ~5 ticks. Ten per-connection loops would be ~60.
	if n := calls.Load(); n < 1 || n > 10 {
		t.Fatalf("10 subscribers caused %d computes in ~5 intervals, want a single shared loop", n)
	}
}

func TestDashboardHashPoller_StopsAfterLastUnsubscribe(t *testing.T) {
	var calls atomic.Int64
	var value atomic.Value
	value.Store("h1")
	p := newDashboardHashPoller(5*time.Millisecond, countingCompute(&calls, &value))

	u1 := p.subscribe()
	u2 := p.subscribe()
	waitFor(t, "first compute", func() bool { return calls.Load() > 0 })
	u1()
	u1() // idempotent: must not drop the other subscriber
	before := calls.Load()
	time.Sleep(30 * time.Millisecond)
	if calls.Load() == before {
		t.Fatal("poller stopped while a subscriber remained")
	}

	u2()
	time.Sleep(20 * time.Millisecond) // let an in-flight poll finish
	stopped := calls.Load()
	time.Sleep(30 * time.Millisecond)
	if n := calls.Load(); n != stopped {
		t.Fatalf("poller kept computing after last unsubscribe (%d -> %d)", stopped, n)
	}
	if h, _ := p.current(); h != "" {
		t.Fatalf("hash after last unsubscribe = %q, want empty", h)
	}
}

func TestDashboardHashPoller_NotifiesOnChange(t *testing.T) {
	var calls atomic.Int64
	var value atomic.Value
	value.Store("h1")
	p := newDashboardHashPoller(5*time.Millisecond, countingCompute(&calls, &value))
	defer p.subscribe()()

	waitFor(t, "initial hash", func() bool { h, _ := p.current(); return h == "h1" })
	_, changed := p.current()

	// Unchanged hash must not wake subscribers.
	select {
	case <-changed:
		t.Fatal("changed fired without a hash change")
	case <-time.After(30 * time.Millisecond):
	}

	value.Store("h2")
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("changed did not fire after hash change")
	}
	if h, _ := p.current(); h != "h2" {
		t.Fatalf("hash = %q, want h2", h)
	}
}

// flushRecorder is an http.ResponseWriter + Flusher whose body can be read
// while the handler is still running.
type flushRecorder struct {
	mu     sync.Mutex
	header http.Header
	body   strings.Builder
}

func (r *flushRecorder) Header() http.Header { return r.header }
func (r *flushRecorder) WriteHeader(int)     {}
func (r *flushRecorder) Flush()              {}
func (r *flushRecorder) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.Write(b)
}
func (r *flushRecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.String()
}

func TestHandleSSE_ClientsShareOnePoller(t *testing.T) {
	var calls atomic.Int64
	var value atomic.Value
	value.Store("h1")

	h := NewAPIHandler(30*time.Second, 60*time.Second, "test-token")
	h.hashPollerOnce.Do(func() {
		h.hashPoller = newDashboardHashPoller(10*time.Millisecond, countingCompute(&calls, &value))
	})

	ctx, cancel := context.WithCancel(context.Background())
	const clients = 5
	recs := make([]*flushRecorder, clients)
	var wg sync.WaitGroup
	for i := range recs {
		recs[i] = &flushRecorder{header: http.Header{}}
		req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
		wg.Add(1)
		go func(rec *flushRecorder) {
			defer wg.Done()
			h.ServeHTTP(rec, req)
		}(recs[i])
	}

	waitFor(t, "every client to get the initial update", func() bool {
		for _, r := range recs {
			if !strings.Contains(r.String(), "data: h1") {
				return false
			}
		}
		return true
	})

	value.Store("h2")
	waitFor(t, "every client to get the changed update", func() bool {
		for _, r := range recs {
			if !strings.Contains(r.String(), "data: h2") {
				return false
			}
		}
		return true
	})

	cancel()
	wg.Wait()

	for i, r := range recs {
		if n := strings.Count(r.String(), "event: dashboard-update"); n != 2 {
			t.Errorf("client %d got %d updates, want exactly 2 (one per distinct hash):\n%s", i, n, r.String())
		}
	}
	h.hashPoller.mu.Lock()
	subs := h.hashPoller.subscribers
	h.hashPoller.mu.Unlock()
	if subs != 0 {
		t.Errorf("subscribers after all clients disconnected = %d, want 0", subs)
	}
}
