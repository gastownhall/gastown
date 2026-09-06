//go:build embedbrowser

package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
)

// TestEmbedBrowser runs the production HTML and scripts in cross-origin frames.
// Only API data is stubbed; no production commands or live mutations are run.
// Run: go test -tags=embedbrowser ./internal/web -run TestEmbedBrowser -v
func TestEmbedBrowser(t *testing.T) {
	chrome, ok := launcher.LookPath()
	if !ok {
		t.Skip("Chrome is required")
	}
	l := launcher.New().Bin(chrome).Headless(true)
	u := l.MustLaunch()
	browser := rod.New().ControlURL(u).MustConnect()
	defer l.Cleanup()
	defer browser.MustClose()

	var dashboardURL string
	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<script>window.received=[];window.ready=[];addEventListener('message',e=>{if(e.origin===%q && e.source===document.querySelector('iframe').contentWindow) (e.data.type==='gastown:ready' ? ready : received).push(e.data)})</script><iframe style="width:1200px;height:900px" src="%s/?embed=1"></iframe>`, dashboardURL, dashboardURL)
	}))
	defer parent.Close()
	fetcher := &MockConvoyFetcher{
		Issues:  []IssueRow{{ID: "hq-xji2", Title: "HQ integration"}},
		Workers: []WorkerRow{{IssueID: "inktree-3r67i", IssueTitle: "Canvas frame"}},
		Hooks:   []HookRow{{ID: "gt-or0"}},
		Convoys: []ConvoyRow{{ID: "hq-cv-example", Title: "Cross-rig convoy"}},
	}
	mux, err := NewDashboardMuxWithOptions(fetcher, nil, DashboardOptions{EmbedParentOrigin: parent.URL})
	if err != nil {
		t.Fatal(err)
	}
	dashboard := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/api/issues/show":
				json.NewEncoder(w).Encode(map[string]any{"id": r.URL.Query().Get("id"), "title": "Resolved issue", "status": "open", "depends_on": []string{"inktree-d2rn2.6"}})
			case "/api/run":
				var req struct{ Command string }
				json.NewDecoder(r.Body).Decode(&req)
				output := "○ inktree-3r67i: Canvas frame [feature]"
				if strings.HasSuffix(req.Command, "--json") {
					output = `{"tracked":[{"id":"inktree-3r67i","title":"Canvas frame","status":"open"}],"completed":0,"total":1}`
				}
				json.NewEncoder(w).Encode(map[string]any{"success": true, "output": output})
			default:
				fmt.Fprint(w, `{}`)
			}
			return
		}
		mux.ServeHTTP(w, r)
	}))
	defer dashboard.Close()
	dashboardURL = dashboard.URL

	page := browser.MustPage(parent.URL).Timeout(30 * time.Second)
	defer page.MustClose()
	frame := page.MustElement("iframe").MustFrame()
	frame.MustWait(`() => typeof window.dashboardFocusBead === 'function'`)
	page.MustWait(`() => ready.length === 1`)
	if page.MustEval(`() => JSON.stringify(ready[0])`).Str() != `{"type":"gastown:ready","version":1}` {
		t.Fatal("unexpected bootstrap readiness payload")
	}
	// Wait until the full dashboard bundle has installed its click handlers.
	frame.MustWait(`() => typeof window.reopenIssue === 'function'`)
	for i, selector := range []string{`.issue-row`, `.polecat-issue [data-bead-focus]`, `.hook-id[data-bead-focus]`} {
		frame.MustEval(`s => document.querySelector(s).click()`, selector)
		page.MustWait(`n => received.length === n`, i+1)
	}
	frame.MustEval(`() => document.querySelector('.convoy-row').click()`)
	frame.MustWait(`() => document.querySelector('#convoy-issues-tbody [data-bead-focus]') !== null`)
	frame.MustEval(`() => document.querySelector('#convoy-issues-tbody [data-bead-focus]').click()`)
	page.MustWait(`() => received.length === 4`)
	got := page.MustEval(`() => JSON.stringify(received)`).Str()
	want := `[{"type":"gastown:focus-bead","version":1,"beadId":"hq-xji2"},{"type":"gastown:focus-bead","version":1,"beadId":"inktree-3r67i"},{"type":"gastown:focus-bead","version":1,"beadId":"gt-or0"},{"type":"gastown:focus-bead","version":1,"beadId":"inktree-3r67i"}]`
	if got != want {
		t.Fatalf("unexpected frame messages: %s", got)
	}
	frame.MustEval(`() => document.querySelector('#convoy-detail-id').click()`)
	page.MustWait(`() => received.length === 5`)
	if page.MustEval(`() => received[4].beadId`).Str() != "hq-cv-example" {
		t.Fatal("convoy context lost its actual HQ bead ID")
	}
	if frame.MustEval(`() => document.querySelector('#issue-detail').style.display`).Str() != "none" {
		t.Fatal("embedded click opened standalone issue detail")
	}

	// Standalone retains real detail navigation and renders dependency links.
	standalone := browser.MustPage(dashboard.URL).Timeout(30 * time.Second)
	defer standalone.MustClose()
	standalone.MustWait(`() => typeof window.reopenIssue === 'function'`)
	standalone.MustEval(`() => document.querySelector('.issue-row').click()`)
	standalone.MustWait(`() => document.querySelector('.issue-dep-item') !== null`)
	standalone.MustEval(`() => document.querySelector('.issue-dep-item').click()`)
	standalone.MustWait(`() => document.querySelector('#issue-detail-id').textContent === 'inktree-d2rn2.6'`)

	// Browser CSP enforces the parent origin, beyond JavaScript targetOrigin.
	untrusted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<iframe src="%s/?embed=1"></iframe>`, dashboard.URL)
	}))
	defer untrusted.Close()
	blocked := browser.MustPage(untrusted.URL).Timeout(30 * time.Second)
	defer blocked.MustClose()
	blocked.MustWaitLoad()
	blockedFrame := blocked.MustElement("iframe").MustFrame()
	if blockedFrame.MustEval(`() => typeof window.dashboardFocusBead`).Str() != "undefined" {
		t.Fatal("untrusted parent loaded dashboard scripts")
	}
}
