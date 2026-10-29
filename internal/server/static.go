package server

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// distFS holds the built dashboard UI. `make web` copies web/dist into
// internal/server/dist/, which git keeps empty (only .keep). Without a build
// the server serves placeholderHTML, which says "UI not built".
//
//go:embed all:dist
var distFS embed.FS

//go:embed placeholder.html
var placeholderHTML []byte

func uiFS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic("server: embedded dist: " + err.Error())
	}
	return sub
}

// isSPAPath reports whether a GET for p falls back to the UI. Paths under the
// API, agent, auth, login and health prefixes never do.
func isSPAPath(p string) bool {
	for _, pre := range []string{"/api/", "/agent/", "/auth/", "/login", "/health"} {
		if strings.HasPrefix(p, pre) {
			return false
		}
	}
	return true
}

// serveStatic serves a file from the UI, or index.html for any path that is
// not a file (client-side routes). Dotfiles are never served.
func (a *App) serveStatic(w http.ResponseWriter, r *http.Request) {
	setHTMLHeaders(w)
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name != "" && name != "index.html" && !hasDotSegment(name) {
		if st, err := fs.Stat(a.ui, name); err == nil && st.Mode().IsRegular() {
			w.Header().Del("Cache-Control")
			http.ServeFileFS(w, r, a.ui, name)
			return
		}
	}
	b, err := fs.ReadFile(a.ui, "index.html")
	if err != nil {
		b = placeholderHTML
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(b)
	}
}

func hasDotSegment(name string) bool {
	for _, seg := range strings.Split(name, "/") {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}
