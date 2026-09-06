//go:build embedbrowser

package web

import (
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
)

// Real EventSource, HTMX and Idiomorph, production HTML/JS and shared snapshot;
// all dashboard data and auxiliary APIs are fixtures, never live commands.
func TestDashboardSnapshotBrowserAutomaticallyRefreshesDOM(t *testing.T) {
	chrome, ok := launcher.LookPath()
	if !ok {
		t.Skip("Chrome is required")
	}
	launch := launcher.New().Bin(chrome).Headless(true)
	browser := rod.New().ControlURL(launch.MustLaunch()).MustConnect()
	defer launch.Cleanup()
	defer browser.MustClose()
	f := &snapshotFixture{title: "before browser update"}
	h, api := newSnapshotHarness(t, f)
	h.cacheTTL = 50 * time.Millisecond
	assets, err := fs.Sub(staticFiles, "static")
	if err != nil {
		t.Fatal(err)
	}
	static := http.StripPrefix("/static/", http.FileServer(http.FS(assets)))
	var pages atomic.Int32
	var connections atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/events":
			connections.Add(1)
			api.ServeHTTP(w, r)
		case strings.HasPrefix(r.URL.Path, "/api/"):
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{}`)
		case strings.HasPrefix(r.URL.Path, "/static/"):
			static.ServeHTTP(w, r)
		case r.URL.Path == "/":
			pages.Add(1)
			rendered := httptest.NewRecorder()
			h.ServeHTTP(rendered, r)
			html := strings.Replace(rendered.Body.String(), "</head>", `<script>
window.__sseUpdates=0;window.__swaps=0;window.__htmxErrors=[];
const NativeEventSource=window.EventSource;
window.EventSource=function(url){const source=new NativeEventSource(url);source.addEventListener('dashboard-update',()=>window.__sseUpdates++);return source;};
document.addEventListener('htmx:afterSwap',()=>window.__swaps++);
document.addEventListener('htmx:noSSESourceError',()=>window.__htmxErrors.push('noSSESourceError'));
</script></head>`, 1)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, html)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	page := browser.MustPage(server.URL).Timeout(15 * time.Second)
	defer page.MustClose()
	page.MustWait(`() => window.sseConnected === true && typeof htmx !== 'undefined'`)
	f.mu.Lock()
	f.title = "after automatic browser update"
	f.mu.Unlock()
	err = page.Timeout(6 * time.Second).Wait(rod.Eval(`() => document.querySelector('.convoy-row').textContent.includes('after automatic browser update')`))
	if err != nil {
		t.Fatalf("DOM stayed stale without manual reload: pages=%d browser=%s wait=%v", pages.Load(), page.MustEval(`() => JSON.stringify({events:window.__sseUpdates,swaps:window.__swaps,pause:!!window.pauseRefresh,connected:window.sseConnected,errors:window.__htmxErrors,trigger:document.querySelector('#dashboard-main').getAttribute('hx-trigger')})`).Str(), err)
	}
	if pages.Load() < 2 {
		t.Fatal("DOM changed without fetching the new snapshot")
	}
	f.mu.Lock()
	f.title = "second automatic browser update"
	f.mu.Unlock()
	if err := page.Timeout(6 * time.Second).Wait(rod.Eval(`() => document.querySelector('.convoy-row').textContent.includes('second automatic browser update')`)); err != nil {
		t.Fatal("second automatic DOM update failed", err)
	}
	stablePages := pages.Load()
	time.Sleep(2200 * time.Millisecond) // A stable snapshot must not create an update/reconnect loop.
	if pages.Load() != stablePages {
		t.Fatal("unchanged snapshot caused more page fetches")
	}
	if connections.Load() != 1 {
		t.Fatalf("swaps duplicated EventSource: %d connections", connections.Load())
	}
	if got := page.MustEval(`() => window.__htmxErrors.length`).Int(); got != 0 {
		t.Fatalf("HTMX SSE-source errors: %d", got)
	}
	if got := page.MustEval(`() => document.querySelectorAll('#dashboard-main').length`).Int(); got != 1 {
		t.Fatalf("swaps duplicated dashboard containers: %d", got)
	}
	page.MustEval(`() => document.querySelector('.convoy-row').click()`)
	page.MustWait(`() => document.querySelector('#convoy-detail-id').textContent === 'hq-canonical'`)
	t.Logf("automatic changes=2 page fetches=%d SSE connections=%d swaps=%d; unchanged state and post-swap canonical navigation passed", pages.Load(), connections.Load(), page.MustEval(`() => window.__swaps`).Int())
}
