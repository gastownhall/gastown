// This file holds the gtb server's top-level handler (App), its HTTP server
// settings and the hourly maintenance loop (spec §8).
package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

const (
	// MaintenanceInterval is how often RunMaintenance prunes the store.
	MaintenanceInterval = time.Hour

	defaultTermPoll    = 200 * time.Millisecond
	defaultTermWait    = 10 * time.Second
	defaultSSEDebounce = time.Second
	defaultSSEPing     = 25 * time.Second
)

// Config configures an App.
type Config struct {
	Store *Store
	// Now is the clock for sessions, freshness, request expiry and the login
	// limiter. nil means time.Now.
	Now func() time.Time
	// PublicURL is the dashboard's public base URL. An https:// URL makes the
	// session cookie Secure.
	PublicURL string
	// TrustedProxies are reverse proxies whose X-Forwarded-For identifies
	// the client for the login and enroll limiters (R41). Empty means the
	// immediate peer is always the client.
	TrustedProxies []netip.Prefix
}

// App serves every route of the gtb server: /agent/v1/ (machines), /login,
// /auth/*, /api/v1/* (the browser), /health and the embedded UI.
type App struct {
	store        *Store
	now          func() time.Time
	publicURL    string
	agent        http.Handler
	ui           fs.FS
	loginLimiter *failureLimiter
	trusted      []netip.Prefix
	mux          *http.ServeMux // /api/v1 routes

	// termPoll and termWait control how GET .../terminal polls for the
	// machine's answer. sseDebounce is the minimum gap between board.changed
	// events on one stream, and ssePing the keep-alive interval.
	termPoll    time.Duration
	termWait    time.Duration
	sseDebounce time.Duration
	ssePing     time.Duration
}

// NewApp returns the server's root handler.
func NewApp(c Config) *App {
	now := c.Now
	if now == nil {
		now = time.Now
	}
	a := &App{
		store:        c.Store,
		now:          now,
		publicURL:    strings.TrimRight(c.PublicURL, "/"),
		agent:        NewAgentAPI(c.Store, now, c.TrustedProxies...),
		trusted:      c.TrustedProxies,
		ui:           uiFS(),
		loginLimiter: newFailureLimiter(loginMaxFailures, loginFailWindow),
		termPoll:     defaultTermPoll,
		termWait:     defaultTermWait,
		sseDebounce:  defaultSSEDebounce,
		ssePing:      defaultSSEPing,
	}
	a.mux = a.apiMux()
	return a
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch {
	case p == "/health":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte("ok"))
	case strings.HasPrefix(p, "/agent/"):
		a.agent.ServeHTTP(w, r)
	case p == "/login":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		a.loginPage(w, http.StatusOK, "")
	case p == "/auth/login" || p == "/auth/logout":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if p == "/auth/login" {
			a.login(w, r)
		} else {
			a.logout(w, r)
		}
	case strings.HasPrefix(p, "/api/v1/"):
		a.serveAPI(w, r)
	case !isSPAPath(p):
		writeError(w, http.StatusNotFound, "not_found")
	case r.Method == http.MethodGet || r.Method == http.MethodHead:
		a.serveStatic(w, r)
	default:
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

// RunMaintenance runs Prune, ExpireRequests and DeleteExpiredSessions at start
// and then every interval, until ctx is cancelled. Errors go to logf.
func RunMaintenance(ctx context.Context, st *Store, now func() time.Time, interval time.Duration, logf func(string, ...any)) {
	if now == nil {
		now = time.Now
	}
	if interval <= 0 {
		interval = MaintenanceInterval
	}
	run := func() {
		t := now()
		if _, err := st.Prune(t); err != nil {
			logf("maintenance: %v", err)
		}
		if _, err := st.ExpireRequests(t); err != nil {
			logf("maintenance: %v", err)
		}
		if _, err := st.DeleteExpiredSessions(t); err != nil {
			logf("maintenance: %v", err)
		}
	}
	run()
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			run()
		}
	}
}

// NewHTTPServer returns the http.Server for h: ReadHeaderTimeout 10s,
// IdleTimeout 120s and no WriteTimeout (SSE streams and long-polls are long).
func NewHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

// ServeOptions configures Serve.
type ServeOptions struct {
	Listen         string
	PublicURL      string
	TrustedProxies []netip.Prefix
	Logf           func(string, ...any)
	// Ready, when set, is called with the bound address once listening.
	Ready func(addr string)
}

// Serve runs the gtb server on st until ctx is cancelled, then shuts down
// gracefully (up to 30s for in-flight requests). The maintenance loop runs
// alongside.
func Serve(ctx context.Context, st *Store, o ServeOptions) error {
	logf := o.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	ln, err := net.Listen("tcp", o.Listen)
	if err != nil {
		return fmt.Errorf("server: listen %s: %w", o.Listen, err)
	}
	if publicURLInsecure(o.PublicURL, o.Listen) {
		logf("warning: public URL is not https; session cookies will not be marked Secure")
	}
	app := NewApp(Config{Store: st, PublicURL: o.PublicURL, TrustedProxies: o.TrustedProxies})
	srv := NewHTTPServer(o.Listen, app)
	srv.BaseContext = func(net.Listener) context.Context { return ctx }

	mctx, stopMaint := context.WithCancel(ctx)
	defer stopMaint()
	go RunMaintenance(mctx, st, nil, MaintenanceInterval, logf)

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	logf("gtb server listening on %s (public URL %s)", ln.Addr(), o.PublicURL)
	if o.Ready != nil {
		o.Ready(ln.Addr().String())
	}
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	logf("gtb server shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
