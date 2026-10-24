package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// With a UI build present, files are served with their content types and
// client-side routes fall back to the built index.html, both under the CSP.
func TestStatic_BuiltAssets(t *testing.T) {
	f := newWebFixture(t)
	f.app.ui = fstest.MapFS{
		"index.html":           {Data: []byte(`<!doctype html><script type="module" src="/assets/index-abc.js"></script>`)},
		"assets/index-abc.js":  {Data: []byte(`console.log("gtb")`)},
		"assets/style-abc.css": {Data: []byte(`body{}`)},
		".keep":                {Data: nil},
	}

	for _, tc := range []struct{ path, ctype, body string }{
		{"/assets/index-abc.js", "javascript", `console.log("gtb")`},
		{"/assets/style-abc.css", "text/css", "body{}"},
		{"/", "text/html", "/assets/index-abc.js"},
		{"/items/m1/gt-1", "text/html", "/assets/index-abc.js"},
	} {
		rec := f.serve(httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), tc.ctype) || !strings.Contains(rec.Body.String(), tc.body) {
			t.Fatalf("%s: %d %q %q", tc.path, rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "UI not built") {
			t.Fatalf("%s: placeholder served with a build present", tc.path)
		}
		wantSecurityHeaders(t, rec)
	}

	// index.html is never cached; hashed assets may be.
	if rec := f.serve(httptest.NewRequest(http.MethodGet, "/", nil)); rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("index Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
	// Dotfiles are never served; the path falls back to index.html.
	if rec := f.serve(httptest.NewRequest(http.MethodGet, "/.keep", nil)); !strings.Contains(rec.Body.String(), "/assets/index-abc.js") {
		t.Fatalf("/.keep: %q", rec.Body.String())
	}
}
