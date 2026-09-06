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
	return []ConvoyRow{{ID: "hq-canonical", Title: title, Status: "open", TotalBeads: 5, ClosedBeads: 2}}, nil
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
	if data.Convoys[0].Title != "changed" || data.Convoys[0].TotalBeads != 5 || data.Convoys[0].ClosedBeads != 2 {
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
	expireSnapshot(h)
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
		if field.Type.Kind() != reflect.Slice {
			continue
		}
		t.Run(field.Name, func(t *testing.T) {
			data := ConvoyData{}
			reflect.ValueOf(&data).Elem().Field(i).Set(reflect.MakeSlice(field.Type, 1, 1))
			if hashDashboardSnapshot(&data) == first {
				t.Fatal("panel omitted from hash")
			}
		})
	}
}
