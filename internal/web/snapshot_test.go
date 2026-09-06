package web

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/activity"
)

// The page snapshot is available even when the old independent status probe
// cannot complete. Exercise the actual public mux and SSE stream, not a hash stub.
func TestDashboardSnapshotInitialSSEWithoutStatusProbe(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "gt")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	mux, err := NewDashboardMuxWithOptions(&MockConvoyFetcher{Convoys: []ConvoyRow{{ID: "hq-canonical", Title: "Canonical convoy", Status: "open"}}}, nil, DashboardOptions{GTPath: bin})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	scan := bufio.NewScanner(resp.Body)
	for scan.Scan() {
		if strings.HasPrefix(scan.Text(), "event: dashboard-update") {
			return
		}
	}
	t.Fatalf("fresh SSE client received no complete initial dashboard update: %v", scan.Err())
}

// snapshotFixture reaches all real handler fetch calls but never a database.
type snapshotFixture struct {
	MockConvoyFetcher
	mu      sync.Mutex
	calls   int
	title   string
	fail    bool
	block   <-chan struct{}
	started chan struct{}
}

func (f *snapshotFixture) FetchConvoys() ([]ConvoyRow, error) {
	f.mu.Lock()
	f.calls++
	title, fail, block, started := f.title, f.fail, f.block, f.started
	f.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if block != nil {
		<-block
	}
	if fail {
		return nil, errors.New("private command error")
	}
	return []ConvoyRow{{ID: "hq-canonical", Title: title, Status: "open", Total: 5, Completed: 2}}, nil
}
func newSnapshotHarness(t *testing.T, f ConvoyFetcher) (*ConvoyHandler, *APIHandler) {
	t.Helper()
	h, err := NewConvoyHandler(f, time.Second, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	api := NewAPIHandler(time.Second, time.Second, "test-token")
	api.dashboard = h
	return h, api
}
func expireSnapshot(h *ConvoyHandler) {
	// Normal cache expiry happens long after worker cleanup. Wait for that
	// barrier before forcing time forward; the undrained test bypasses it.
	h.cacheMu.Lock()
	flight := h.refresh
	h.cacheMu.Unlock()
	if flight != nil {
		<-flight.drained
	}
	h.cacheMu.Lock()
	h.cacheTime = time.Time{}
	h.cacheMu.Unlock()
}

func TestDashboardSnapshotChangesAndFailureRecovery(t *testing.T) {
	f := &snapshotFixture{title: "first"}
	h, api := newSnapshotHarness(t, f)
	first := api.computeDashboardHash(context.Background())
	if first == "" {
		t.Fatal("missing initial snapshot")
	}
	expireSnapshot(h)
	if stable := api.computeDashboardHash(context.Background()); stable != first {
		t.Fatal("unchanged snapshot emitted a new hash")
	}
	f.mu.Lock()
	f.title = "changed"
	f.mu.Unlock()
	expireSnapshot(h)
	changed := api.computeDashboardHash(context.Background())
	if changed == first || changed == "" {
		t.Fatal("changed snapshot hidden")
	}
	f.mu.Lock()
	f.fail = true
	f.mu.Unlock()
	expireSnapshot(h)
	failed := api.computeDashboardHash(context.Background())
	if failed == changed {
		t.Fatal("failure must be observable")
	}
	data, _ := h.snapshot(context.Background())
	if data.Convoys[0].Title != "changed" || data.Convoys[0].Total != 5 || data.Convoys[0].Completed != 2 {
		t.Fatal("failure lost canonical last-good data")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(w.Body.String(), "showing last known data") || strings.Contains(w.Body.String(), "private command error") {
		t.Fatal("failure notice missing or leaked command details")
	}
	expireSnapshot(h)
	if got := api.computeDashboardHash(context.Background()); got != failed {
		t.Fatal("repeated failure caused update storm")
	}
	f.mu.Lock()
	f.fail = false
	f.mu.Unlock()
	expireSnapshot(h)
	if got := api.computeDashboardHash(context.Background()); got != changed {
		t.Fatal("recovery did not restore complete snapshot")
	}
}

func TestDashboardSnapshotCoalescesHTMLExpandAndSSE(t *testing.T) {
	block := make(chan struct{})
	f := &snapshotFixture{title: "shared", block: block, started: make(chan struct{}, 1)}
	h, api := newSnapshotHarness(t, f)
	first := make(chan string, 1)
	go func() { first <- api.computeDashboardHash(context.Background()) }()
	<-f.started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if got := api.computeDashboardHash(ctx); got != "" {
		t.Fatal("cancelled waiter returned a hash")
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/?expand=convoys", nil))
			if w.Code != 200 {
				t.Errorf("HTTP %d", w.Code)
			}
		}()
	}
	close(block)
	wg.Wait()
	if <-first == "" {
		t.Fatal("remaining client lost snapshot")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls != 1 {
		t.Fatalf("fanout refreshed %d times", f.calls)
	}
}

func TestDashboardSnapshotDeadlineDoesNotOverlapUndrainedFetcher(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	f := &snapshotFixture{block: block, started: make(chan struct{}, 1)}
	h, api := newSnapshotHarness(t, f)
	h.fetchTimeout = 30 * time.Millisecond
	start := time.Now()
	if got := api.computeDashboardHash(context.Background()); got == "" {
		t.Fatal("deadline hid initial unavailable state")
	}
	if time.Since(start) > 300*time.Millisecond {
		t.Fatal("waiter blocked waiting for fetcher drain")
	}
	h.cacheMu.Lock()
	h.cacheTime = time.Time{}
	h.cacheMu.Unlock()
	if got := api.computeDashboardHash(context.Background()); got == "" {
		t.Fatal("lost visible state during cleanup")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls != 1 {
		t.Fatal("timed-out fetch overlapped next refresh")
	}
}

func TestDashboardSnapshotInitialFailureIsVisibleAndCoalesced(t *testing.T) {
	f := &snapshotFixture{fail: true}
	h, api := newSnapshotHarness(t, f)
	for range 10 {
		if api.computeDashboardHash(context.Background()) == "" {
			t.Fatal("optional failure starved initial SSE")
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(w.Body.String(), "Unavailable; refresh will retry.") {
		t.Fatal("initial failure was silent")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls != 1 {
		t.Fatal("failed refresh was not coalesced")
	}
}

func TestDashboardSnapshotEveryPanelAffectsHash(t *testing.T) {
	// Reflection deliberately covers new fields too; adding a panel must not
	// accidentally leave it outside change detection.
	baseline := ConvoyData{}
	first := hashDashboardSnapshot(&baseline)
	typ := reflect.TypeOf(baseline)
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.Type.Kind() != reflect.Slice && field.Type.Kind() != reflect.Pointer {
			continue
		}
		t.Run(field.Name, func(t *testing.T) {
			data := ConvoyData{}
			value := reflect.New(field.Type.Elem())
			if field.Type.Kind() == reflect.Slice {
				value = reflect.MakeSlice(field.Type, 1, 1)
			}
			reflect.ValueOf(&data).Elem().Field(i).Set(value)
			if hashDashboardSnapshot(&data) == first {
				t.Fatal("panel omitted from hash")
			}
		})
	}
}

func TestDashboardSnapshotSSEEmitsChangedStateWithoutPageLoads(t *testing.T) {
	f := &snapshotFixture{title: "first"}
	h, api := newSnapshotHarness(t, f)
	h.cacheTTL = 30 * time.Millisecond
	mux := http.NewServeMux()
	mux.Handle("/api/", api)
	mux.Handle("/", h)
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	events := make(chan string, 10)
	go func() {
		defer close(events)
		scan := bufio.NewScanner(resp.Body)
		for scan.Scan() {
			if strings.HasPrefix(scan.Text(), "data: ") && scan.Text() != "data: ok" {
				events <- scan.Text()
			}
		}
	}()
	next := func() string {
		t.Helper()
		select {
		case value := <-events:
			if value == "" {
				t.Fatal("stream ended")
			}
			return value
		case <-time.After(3 * time.Second):
			t.Fatal("missing dashboard-update")
			return ""
		}
	}
	first := next()
	select {
	case value := <-events:
		t.Fatalf("unchanged snapshot emitted %s", value)
	case <-time.After(2200 * time.Millisecond):
	}
	f.mu.Lock()
	f.title = "changed without page load"
	f.mu.Unlock()
	changed := next()
	if changed == first {
		t.Fatal("change event reused initial hash")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(w.Body.String(), "changed without page load") {
		t.Fatal("SSE and HTML disagree")
	}
}

type activitySnapshotFixture struct {
	MockConvoyFetcher
	timestamp time.Time
}

func (f *activitySnapshotFixture) FetchWorkers() ([]WorkerRow, error) {
	return []WorkerRow{{LastActivity: activity.Calculate(f.timestamp)}}, nil
}
func (f *activitySnapshotFixture) FetchConvoys() ([]ConvoyRow, error) {
	return []ConvoyRow{{ID: "hq-live", LastActivity: activity.Calculate(f.timestamp)}}, nil
}

func TestDashboardSnapshotLiveActivityDoesNotInventChanges(t *testing.T) {
	h, _ := newSnapshotHarness(t, &activitySnapshotFixture{timestamp: time.Now().Add(-2 * time.Minute)})
	first, firstHash := h.snapshot(context.Background())
	h.cacheMu.Lock()
	flight := h.refresh
	h.cacheMu.Unlock()
	if flight != nil {
		<-flight.drained
	}
	expireSnapshot(h)
	second, secondHash := h.snapshot(context.Background())
	if first.Workers[0].LastActivity.Duration == second.Workers[0].LastActivity.Duration {
		t.Fatal("fixture failed to refresh live activity")
	}
	if firstHash != secondHash {
		t.Fatal("unrendered elapsed duration invented a dashboard change")
	}
	changed := *second
	changed.Workers = append([]WorkerRow(nil), second.Workers...)
	changed.Workers[0].LastActivity.ColorClass = "red"
	if hashDashboardSnapshot(first) == hashDashboardSnapshot(&changed) {
		t.Fatal("visible activity color change hidden")
	}
}

func TestDashboardSnapshotNewClientDuringCancelledDrain(t *testing.T) {
	block := make(chan struct{})
	f := &snapshotFixture{title: "after drain", block: block, started: make(chan struct{}, 1)}
	h, api := newSnapshotHarness(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan string, 1)
	go func() { first <- api.computeDashboardHash(ctx) }()
	<-f.started
	cancel()
	<-first
	newCtx, newCancel := context.WithTimeout(context.Background(), time.Second)
	defer newCancel()
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { h.ServeHTTP(response, httptest.NewRequest("GET", "/", nil).WithContext(newCtx)); close(done) }()
	// Allow the new client to join while the cancelled fetch deliberately drains.
	select {
	case <-done:
		close(block)
		t.Fatalf("new client returned during cancelled drain: status=%d bytes=%d", response.Code, response.Body.Len())
	case <-time.After(20 * time.Millisecond):
	}
	f.mu.Lock()
	f.block = nil
	f.mu.Unlock()
	close(block)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("new client did not retry after drain")
	}
	if response.Code != 200 || !strings.Contains(response.Body.String(), "after drain") {
		t.Fatalf("new client got status=%d bytes=%d", response.Code, response.Body.Len())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls != 2 {
		t.Fatalf("refreshes=%d, want cancelled plus retry", f.calls)
	}
}

func TestDashboardSnapshotAbandonedDrainReturnsHonestUnavailable(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	f := &snapshotFixture{block: block, started: make(chan struct{}, 1)}
	h, api := newSnapshotHarness(t, f)
	h.fetchTimeout = 40 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan string, 1)
	go func() { done <- api.computeDashboardHash(ctx) }()
	<-f.started
	cancel()
	<-done
	start := time.Now()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if time.Since(start) > 250*time.Millisecond {
		t.Fatal("new request waited unbounded for abandoned drain")
	}
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "unavailable") {
		t.Fatalf("unavailable refresh returned %d %q", w.Code, w.Body.String())
	}
}
