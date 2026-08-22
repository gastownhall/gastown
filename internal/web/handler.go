package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/config"
)

//go:embed static
var staticFiles embed.FS

// ConvoyFetcher defines the interface for fetching convoy data.
type ConvoyFetcher interface {
	FetchConvoys() ([]ConvoyRow, error)
	FetchMergeQueue() ([]MergeQueueRow, error)
	FetchWorkers() ([]WorkerRow, error)
	FetchMail() ([]MailRow, error)
	FetchRigs() ([]RigRow, error)
	FetchDogs() ([]DogRow, error)
	FetchEscalations() ([]EscalationRow, error)
	FetchHealth() (*HealthRow, error)
	FetchQueues() ([]QueueRow, error)
	FetchSessions() ([]SessionRow, error)
	FetchHooks() ([]HookRow, error)
	FetchMayor() (*MayorStatus, error)
	FetchIssues() ([]IssueRow, error)
	FetchActivity() ([]ActivityRow, error)
}

// expandCacheEntry holds a cached expanded-view response.
type expandCacheEntry struct {
	body []byte
	time time.Time
}

// ConvoyHandler handles HTTP requests for the convoy dashboard.
type ConvoyHandler struct {
	fetcher      ConvoyFetcher
	template     *template.Template
	fetchTimeout time.Duration
	csrfToken    string

	// Response cache: prevents cascading bd process storms when multiple
	// browser tabs or htmx auto-refresh requests arrive faster than fetches
	// complete. See GH#2618.
	cacheMu    sync.Mutex
	cacheBody  []byte
	cacheTime  time.Time
	cacheTTL   time.Duration
	cacheInUse sync.Mutex // serializes concurrent fetches (only one runs at a time)

	// Expanded-view cache: expanded views previously bypassed the response
	// cache entirely, allowing process storms via repeated ?expand= requests.
	// See GH#3117.
	expandCacheMu sync.Mutex
	expandCache   map[string]expandCacheEntry
}

// defaultCacheTTL is the minimum interval between full dashboard fetches.
// Requests arriving within this window get the cached response.
const defaultCacheTTL = 10 * time.Second

// NewConvoyHandler creates a new convoy handler with the given fetcher, fetch timeout, and CSRF token.
func NewConvoyHandler(fetcher ConvoyFetcher, fetchTimeout time.Duration, csrfToken string) (*ConvoyHandler, error) {
	tmpl, err := LoadTemplates()
	if err != nil {
		return nil, err
	}

	return &ConvoyHandler{
		fetcher:      fetcher,
		template:     tmpl,
		fetchTimeout: fetchTimeout,
		csrfToken:    csrfToken,
		cacheTTL:     defaultCacheTTL,
	}, nil
}

// ServeHTTP handles GET / requests and renders the convoy dashboard.
// Uses a response cache to prevent bd process storms from overlapping
// requests (htmx auto-refresh, multiple tabs). Only one fetch cycle
// runs at a time; concurrent requests get the cached response.
func (h *ConvoyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Check for expand parameter — expanded views render a different template
	// variant but are still cached to prevent process storms (GH#3117).
	expandPanel := r.URL.Query().Get("expand")

	// Fast path: serve from cache if fresh.
	if expandPanel == "" {
		h.cacheMu.Lock()
		if len(h.cacheBody) > 0 && time.Since(h.cacheTime) < h.cacheTTL {
			body := h.cacheBody
			h.cacheMu.Unlock()
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if _, err := w.Write(body); err != nil {
				log.Printf("dashboard: cached response write failed: %v", err)
			}
			return
		}
		h.cacheMu.Unlock()
	} else {
		// Expanded views: check per-panel cache to prevent process storms
		h.expandCacheMu.Lock()
		if entry, ok := h.expandCache[expandPanel]; ok && time.Since(entry.time) < h.cacheTTL {
			body := entry.body
			h.expandCacheMu.Unlock()
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if _, err := w.Write(body); err != nil {
				log.Printf("dashboard: cached expand response write failed: %v", err)
			}
			return
		}
		h.expandCacheMu.Unlock()
	}

	// Serialize fetch cycles: only one request triggers a full fetch at a time.
	// Others wait and will likely hit the cache when this one finishes.
	h.cacheInUse.Lock()
	defer h.cacheInUse.Unlock()

	// Double-check cache after acquiring lock (another request may have populated it).
	if expandPanel == "" {
		h.cacheMu.Lock()
		if len(h.cacheBody) > 0 && time.Since(h.cacheTime) < h.cacheTTL {
			body := h.cacheBody
			h.cacheMu.Unlock()
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if _, err := w.Write(body); err != nil {
				log.Printf("dashboard: cached response write failed: %v", err)
			}
			return
		}
		h.cacheMu.Unlock()
	} else {
		h.expandCacheMu.Lock()
		if entry, ok := h.expandCache[expandPanel]; ok && time.Since(entry.time) < h.cacheTTL {
			body := entry.body
			h.expandCacheMu.Unlock()
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if _, err := w.Write(body); err != nil {
				log.Printf("dashboard: cached expand response write failed: %v", err)
			}
			return
		}
		h.expandCacheMu.Unlock()
	}

	body := h.fetchAndRender(r, expandPanel)
	if body == nil {
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
		return
	}

	// Update cache
	if expandPanel == "" {
		h.cacheMu.Lock()
		h.cacheBody = body
		h.cacheTime = time.Now()
		h.cacheMu.Unlock()
	} else {
		h.expandCacheMu.Lock()
		if h.expandCache == nil {
			h.expandCache = make(map[string]expandCacheEntry)
		}
		h.expandCache[expandPanel] = expandCacheEntry{body: body, time: time.Now()}
		h.expandCacheMu.Unlock()
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := w.Write(body); err != nil {
		log.Printf("dashboard: response write failed: %v", err)
	}
}

// fetchAndRender runs all 14 fetchers in parallel and renders the template.
// Returns the rendered HTML bytes, or nil on template error.
func (h *ConvoyHandler) fetchAndRender(r *http.Request, expandPanel string) []byte {
	ctx, cancel := context.WithTimeout(r.Context(), h.fetchTimeout)
	defer cancel()

	var (
		convoys     []ConvoyRow
		mergeQueue  []MergeQueueRow
		workers     []WorkerRow
		mail        []MailRow
		rigs        []RigRow
		dogs        []DogRow
		escalations []EscalationRow
		health      *HealthRow
		queues      []QueueRow
		sessions    []SessionRow
		hooks       []HookRow
		mayor       *MayorStatus
		issues      []IssueRow
		activity    []ActivityRow
		wg          sync.WaitGroup
	)

	// Run all fetches in parallel with error logging
	wg.Add(14)

	go func() {
		defer wg.Done()
		var err error
		convoys, err = h.fetcher.FetchConvoys()
		if err != nil {
			log.Printf("dashboard: FetchConvoys failed: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		var err error
		mergeQueue, err = h.fetcher.FetchMergeQueue()
		if err != nil {
			log.Printf("dashboard: FetchMergeQueue failed: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		var err error
		workers, err = h.fetcher.FetchWorkers()
		if err != nil {
			log.Printf("dashboard: FetchWorkers failed: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		var err error
		mail, err = h.fetcher.FetchMail()
		if err != nil {
			log.Printf("dashboard: FetchMail failed: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		var err error
		rigs, err = h.fetcher.FetchRigs()
		if err != nil {
			log.Printf("dashboard: FetchRigs failed: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		var err error
		dogs, err = h.fetcher.FetchDogs()
		if err != nil {
			log.Printf("dashboard: FetchDogs failed: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		var err error
		escalations, err = h.fetcher.FetchEscalations()
		if err != nil {
			log.Printf("dashboard: FetchEscalations failed: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		var err error
		health, err = h.fetcher.FetchHealth()
		if err != nil {
			log.Printf("dashboard: FetchHealth failed: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		var err error
		queues, err = h.fetcher.FetchQueues()
		if err != nil {
			log.Printf("dashboard: FetchQueues failed: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		var err error
		sessions, err = h.fetcher.FetchSessions()
		if err != nil {
			log.Printf("dashboard: FetchSessions failed: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		var err error
		hooks, err = h.fetcher.FetchHooks()
		if err != nil {
			log.Printf("dashboard: FetchHooks failed: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		var err error
		mayor, err = h.fetcher.FetchMayor()
		if err != nil {
			log.Printf("dashboard: FetchMayor failed: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		var err error
		issues, err = h.fetcher.FetchIssues()
		if err != nil {
			log.Printf("dashboard: FetchIssues failed: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		var err error
		activity, err = h.fetcher.FetchActivity()
		if err != nil {
			log.Printf("dashboard: FetchActivity failed: %v", err)
		}
	}()

	// Wait for fetches or timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// All fetches completed
	case <-ctx.Done():
		log.Printf("dashboard: fetch timeout after %v", h.fetchTimeout)
		// Goroutines may still be writing to shared result variables.
		// Wait for them to finish to avoid a data race on read below.
		<-done
	}

	// Compute summary from already-fetched data
	summary := computeSummary(workers, hooks, issues, convoys, escalations, activity)

	data := ConvoyData{
		Convoys:     convoys,
		MergeQueue:  mergeQueue,
		Workers:     workers,
		Mail:        mail,
		Rigs:        rigs,
		Dogs:        dogs,
		Escalations: escalations,
		Health:      health,
		Queues:      queues,
		Sessions:    sessions,
		Hooks:       hooks,
		Mayor:       mayor,
		Issues:      enrichIssuesWithAssignees(issues, hooks),
		Activity:    activity,
		Summary:     summary,
		Expand:      expandPanel,
		CSRFToken:   h.csrfToken,
	}

	var buf bytes.Buffer
	if err := h.template.ExecuteTemplate(&buf, "convoy.html", data); err != nil {
		log.Printf("dashboard: template execution failed: %v", err)
		return nil
	}

	return buf.Bytes()
}

// computeSummary calculates dashboard stats and alerts from fetched data.
func computeSummary(workers []WorkerRow, hooks []HookRow, issues []IssueRow,
	convoys []ConvoyRow, escalations []EscalationRow, activity []ActivityRow) *DashboardSummary {

	summary := &DashboardSummary{
		PolecatCount:    len(workers),
		HookCount:       len(hooks),
		IssueCount:      len(issues),
		ConvoyCount:     len(convoys),
		EscalationCount: len(escalations),
	}

	// Count stuck workers (status = "stuck")
	for _, w := range workers {
		if w.WorkStatus == "stuck" {
			summary.StuckPolecats++
		}
	}

	// Count stale hooks (IsStale = true)
	for _, h := range hooks {
		if h.IsStale {
			summary.StaleHooks++
		}
	}

	// Count unacked escalations
	for _, e := range escalations {
		if !e.Acked {
			summary.UnackedEscalations++
		}
	}

	// Count high priority issues (P1 or P2)
	for _, i := range issues {
		if i.Priority == 1 || i.Priority == 2 {
			summary.HighPriorityIssues++
		}
	}

	// Count recent session deaths from activity
	for _, a := range activity {
		if a.Type == "session_death" || a.Type == "mass_death" {
			summary.DeadSessions++
		}
	}

	// Set HasAlerts flag
	summary.HasAlerts = summary.StuckPolecats > 0 ||
		summary.StaleHooks > 0 ||
		summary.UnackedEscalations > 0 ||
		summary.DeadSessions > 0 ||
		summary.HighPriorityIssues > 0

	return summary
}

// enrichIssuesWithAssignees adds Assignee info to issues by cross-referencing hooks.
func enrichIssuesWithAssignees(issues []IssueRow, hooks []HookRow) []IssueRow {
	// Build a map of issue ID -> assignee from hooks
	hookMap := make(map[string]string)
	for _, hook := range hooks {
		hookMap[hook.ID] = hook.Agent
	}

	// Enrich issues with assignee info
	for i := range issues {
		if assignee, ok := hookMap[issues[i].ID]; ok {
			issues[i].Assignee = assignee
		}
	}
	return issues
}

// generateCSRFToken creates a cryptographically random token for CSRF protection.
func generateCSRFToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		log.Fatalf("failed to generate CSRF token: %v", err)
	}
	return hex.EncodeToString(b)
}

// NewDashboardMux creates an HTTP handler that serves both the dashboard and API.
// webCfg may be nil, in which case defaults are used.
func NewDashboardMux(fetcher ConvoyFetcher, webCfg *config.WebTimeoutsConfig) (http.Handler, error) {
	if webCfg == nil {
		webCfg = config.DefaultWebTimeoutsConfig()
	}

	csrfToken := generateCSRFToken()

	fetchTimeout := config.ParseDurationOrDefault(webCfg.FetchTimeout, 8*time.Second)
	convoyHandler, err := NewConvoyHandler(fetcher, fetchTimeout, csrfToken)
	if err != nil {
		return nil, err
	}

	defaultRunTimeout := config.ParseDurationOrDefault(webCfg.DefaultRunTimeout, 30*time.Second)
	maxRunTimeout := config.ParseDurationOrDefault(webCfg.MaxRunTimeout, 60*time.Second)
	apiHandler := NewAPIHandler(defaultRunTimeout, maxRunTimeout, csrfToken)

	// Create static file server from embedded files
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return nil, err
	}
	staticHandler := http.FileServer(http.FS(staticFS))

	mux := http.NewServeMux()
	mux.Handle("/api/", apiHandler)
	mux.Handle("/static/", http.StripPrefix("/static/", staticHandler))
	mux.Handle("/", convoyHandler)

	return mux, nil
}
