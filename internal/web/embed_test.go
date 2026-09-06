package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateEmbedParentOrigin(t *testing.T) {
	for _, origin := range []string{"", "https://canvas.example", "https://canvas.example:8443", "http://localhost:3000", "http://127.0.0.1:3000", "http://[::1]:3000"} {
		if err := ValidateEmbedParentOrigin(origin); err != nil {
			t.Errorf("valid origin %q: %v", origin, err)
		}
	}
	for _, origin := range []string{"*", "null", "https://*.example", "http://canvas.example", "https://user:pass@canvas.example", "https://canvas.example/", "https://canvas.example/path", "https://canvas.example?", "https://canvas.example#", "https://canvas.example:443", "https://canvas.example:0", "https://canvas.example:65536", "https://canvas.example:", "https://canvas.example:03000", "https://canvas.example;", "https://canvas.example 'unsafe-inline'", "//canvas.example", "javascript:alert(1)", "file:///tmp/canvas", "https://canvas.example\n", "https://CANVAS.example"} {
		if err := ValidateEmbedParentOrigin(origin); err == nil {
			t.Errorf("accepted unsafe or noncanonical origin %q", origin)
		}
	}
}

func TestDashboardEmbedPolicyAndCache(t *testing.T) {
	const parent = "http://127.0.0.1:3000"
	h, err := NewDashboardMuxWithOptions(&MockConvoyFetcher{
		Issues:  []IssueRow{{ID: "hq-xji2", Title: "Canvas integration"}},
		Workers: []WorkerRow{{IssueID: "inktree-3r67i"}},
	}, nil, DashboardOptions{EmbedParentOrigin: parent})
	if err != nil {
		t.Fatal(err)
	}
	// Alternate requests through the same response cache. Headers must never
	// inherit another request's framing permission or query-supplied origin.
	for _, path := range []string{"/", "/?embed=1", "/?embed=1&parentOrigin=https://evil.example", "/", "/?embed=1&expand=issues"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d", path, w.Code)
		}
		wantCSP, wantXFO := "frame-ancestors 'self'", "SAMEORIGIN"
		if strings.Contains(path, "embed=1") {
			wantCSP, wantXFO = "frame-ancestors "+parent, ""
		}
		if w.Header().Get("Content-Security-Policy") != wantCSP || w.Header().Get("X-Frame-Options") != wantXFO {
			t.Errorf("%s: wrong frame policy: %v", path, w.Header())
		}
		if !strings.Contains(w.Body.String(), `name="dashboard-parent-origin" content="`+parent+`"`) {
			t.Error("missing trusted origin metadata")
		}
		if !strings.Contains(w.Body.String(), `data-bead-focus="inktree-3r67i"`) {
			t.Error("worker context lost its actual bead ID")
		}
	}
	for _, token := range []string{"", "wrong-token"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/run?embed=1", strings.NewReader(`{"command":"status"}`))
		r.Header.Set("Origin", parent)
		r.Header.Set("X-Dashboard-Token", token)
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden || w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("trusted parent bypassed API authorization")
		}
	}
}

func TestDashboardEmbedRequiresConfiguration(t *testing.T) {
	h, err := NewDashboardMux(&MockConvoyFetcher{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/?embed=1&parentOrigin=https://evil.example", nil))
	if w.Code != http.StatusForbidden {
		t.Fatal("query enabled unconfigured embedding")
	}
	if _, err := NewDashboardMuxWithOptions(&MockConvoyFetcher{}, nil, DashboardOptions{EmbedParentOrigin: "*"}); err == nil {
		t.Fatal("unsafe configuration accepted")
	}
}
