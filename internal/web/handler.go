package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"sort"
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

// ConvoyHandler shares one snapshot between HTML, expanded panels and SSE.
// Cache entries are immutable after publication.
type ConvoyHandler struct {
	fetcher           ConvoyFetcher
	template          *template.Template
	fetchTimeout      time.Duration
	csrfToken         string
	embedParentOrigin string
	cacheMu           sync.Mutex
	cacheData         *ConvoyData
	cacheHash         string
	cacheTime         time.Time // Last attempt, including failed refreshes.
	cacheTTL          time.Duration
	refresh           *dashboardRefresh
}

type dashboardRefresh struct {
	ready     chan struct{}
	cancel    context.CancelFunc
	waiters   int
	published bool
	abandoned bool
	drained   chan struct{}
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

// ServeHTTP renders every view from the same snapshot; expand never refetches.
func (h *ConvoyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	data, _ := h.snapshot(r.Context())
	if data == nil {
		http.Error(w, "Dashboard refresh unavailable; retry shortly", http.StatusServiceUnavailable)
		return
	}
	view := *data
	view.Expand = r.URL.Query().Get("expand")
	var buf bytes.Buffer
	if err := h.template.ExecuteTemplate(&buf, "convoy.html", view); err != nil {
		log.Printf("dashboard: template execution failed: %v", err)
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if len(view.PanelErrors) > 0 {
		w.Header().Set("X-Dashboard-State", "degraded")
	}
	if _, err := w.Write(buf.Bytes()); err != nil {
		log.Printf("dashboard: response write failed: %v", err)
	}
}

// snapshot starts a refresh even without a page load. All callers share it;
// disconnecting waiters leave promptly, and the final waiter cancels its work.
func (h *ConvoyHandler) snapshot(ctx context.Context) (*ConvoyData, string) {
	for {
		if ctx.Err() != nil {
			return nil, ""
		}
		h.cacheMu.Lock()
		if !h.cacheTime.IsZero() && time.Since(h.cacheTime) < h.cacheTTL {
			data, hash := h.cacheData, h.cacheHash
			h.cacheMu.Unlock()
			return data, hash
		}
		flight := h.refresh
		if flight != nil && flight.abandoned {
			// A new client must not join work cancelled by its last predecessor.
			// Wait for cleanup, then retry within this client's own deadline.
			h.cacheMu.Unlock()
			waitCtx, cancel := context.WithTimeout(ctx, h.fetchTimeout)
			select {
			case <-waitCtx.Done():
				cancel()
				return nil, ""
			case <-flight.drained:
				cancel()
				continue
			}
		}
		if flight == nil {
			workCtx, cancel := context.WithTimeout(context.Background(), h.fetchTimeout)
			flight = &dashboardRefresh{ready: make(chan struct{}), drained: make(chan struct{}), cancel: cancel}
			h.refresh = flight
			go h.refreshSnapshot(workCtx, flight, h.cacheData)
		}
		flight.waiters++
		h.cacheMu.Unlock()
		select {
		case <-ctx.Done():
		case <-flight.ready:
		}
		h.cacheMu.Lock()
		flight.waiters--
		if flight.waiters == 0 && !flight.published {
			flight.abandoned = true
			flight.cancel()
		}
		data, hash := h.cacheData, h.cacheHash
		h.cacheMu.Unlock()
		if ctx.Err() != nil {
			return nil, ""
		}
		return data, hash
	}
}

func (h *ConvoyHandler) refreshSnapshot(ctx context.Context, flight *dashboardRefresh, previous *ConvoyData) {
	data, drained := h.fetchSnapshot(ctx, previous)
	hash := hashDashboardSnapshot(data)
	h.cacheMu.Lock()
	if ctx.Err() != context.Canceled {
		h.cacheData, h.cacheHash, h.cacheTime = data, hash, time.Now()
	}
	flight.published = true
	close(flight.ready)
	h.cacheMu.Unlock()
	// Keep the single-flight reservation until every fetch has exited. Even a
	// non-context-aware fetcher cannot overlap a later refresh after timeout.
	<-drained
	flight.cancel()
	h.cacheMu.Lock()
	h.refresh = nil
	close(flight.drained)
	h.cacheMu.Unlock()
}

type dashboardPanelResult struct {
	name  string
	apply func(*ConvoyData)
	err   error
}

// fetchSnapshot keeps last-good panel data on errors, and publishes explicit
// unavailable/stale panel notices. An unavailable optional panel must neither
// erase useful data nor prevent the initial SSE event indefinitely.
func (h *ConvoyHandler) fetchSnapshot(ctx context.Context, previous *ConvoyData) (*ConvoyData, <-chan struct{}) {
	data := &ConvoyData{CSRFToken: h.csrfToken, EmbedParentOrigin: h.embedParentOrigin}
	if previous != nil {
		*data = *previous
	}
	data.PanelErrors = make(map[string]string)
	data.panelSuccess = make(map[string]bool)
	if previous != nil {
		for name, success := range previous.panelSuccess {
			data.panelSuccess[name] = success
		}
	}
	fetcher := h.fetcher
	if contextual, ok := fetcher.(interface {
		WithContext(context.Context) ConvoyFetcher
	}); ok {
		fetcher = contextual.WithContext(ctx)
	}
	results := make(chan dashboardPanelResult, 14)
	pending := make(map[string]bool)
	var wg sync.WaitGroup
	launch := func(name string, fetch func() (func(*ConvoyData), error)) {
		pending[name] = true
		wg.Add(1)
		go func() { defer wg.Done(); apply, err := fetch(); results <- dashboardPanelResult{name, apply, err} }()
	}
	launch("Convoys", func() (func(*ConvoyData), error) {
		value, err := fetcher.FetchConvoys()
		return func(d *ConvoyData) { d.Convoys = value }, err
	})
	launch("MergeQueue", func() (func(*ConvoyData), error) {
		value, err := fetcher.FetchMergeQueue()
		return func(d *ConvoyData) { d.MergeQueue = value }, err
	})
	launch("Workers", func() (func(*ConvoyData), error) {
		value, err := fetcher.FetchWorkers()
		return func(d *ConvoyData) { d.Workers = value }, err
	})
	launch("Mail", func() (func(*ConvoyData), error) {
		value, err := fetcher.FetchMail()
		return func(d *ConvoyData) { d.Mail = value }, err
	})
	launch("Rigs", func() (func(*ConvoyData), error) {
		value, err := fetcher.FetchRigs()
		return func(d *ConvoyData) { d.Rigs = value }, err
	})
	launch("Dogs", func() (func(*ConvoyData), error) {
		value, err := fetcher.FetchDogs()
		return func(d *ConvoyData) { d.Dogs = value }, err
	})
	launch("Escalations", func() (func(*ConvoyData), error) {
		value, err := fetcher.FetchEscalations()
		return func(d *ConvoyData) { d.Escalations = value }, err
	})
	launch("Health", func() (func(*ConvoyData), error) {
		value, err := fetcher.FetchHealth()
		return func(d *ConvoyData) { d.Health = value }, err
	})
	launch("Queues", func() (func(*ConvoyData), error) {
		value, err := fetcher.FetchQueues()
		return func(d *ConvoyData) { d.Queues = value }, err
	})
	launch("Sessions", func() (func(*ConvoyData), error) {
		value, err := fetcher.FetchSessions()
		return func(d *ConvoyData) { d.Sessions = value }, err
	})
	launch("Hooks", func() (func(*ConvoyData), error) {
		value, err := fetcher.FetchHooks()
		return func(d *ConvoyData) { d.Hooks = value }, err
	})
	launch("Mayor", func() (func(*ConvoyData), error) {
		value, err := fetcher.FetchMayor()
		return func(d *ConvoyData) { d.Mayor = value }, err
	})
	launch("Issues", func() (func(*ConvoyData), error) {
		value, err := fetcher.FetchIssues()
		return func(d *ConvoyData) { d.Issues = value }, err
	})
	launch("Activity", func() (func(*ConvoyData), error) {
		value, err := fetcher.FetchActivity()
		return func(d *ConvoyData) { d.Activity = value }, err
	})

	drained := make(chan struct{})
	go func() { wg.Wait(); close(drained) }()
	failed := func(name string, err error) {
		log.Printf("dashboard: %s refresh failed: %v", name, err)
		message := "Unavailable; refresh will retry."
		if data.panelSuccess[name] {
			message = "Refresh failed; showing last known data."
		}
		data.PanelErrors[name] = message
	}
	for len(pending) > 0 {
		select {
		case result := <-results:
			delete(pending, result.name)
			if result.err != nil {
				failed(result.name, result.err)
			} else {
				result.apply(data)
				data.panelSuccess[result.name] = true
			}
		case <-ctx.Done():
			for name := range pending {
				failed(name, ctx.Err())
			}
			pending = nil
		}
	}
	// Enrichment must not mutate slices belonging to the previous snapshot.
	data.Workers = append([]WorkerRow(nil), data.Workers...)
	for i := range data.Workers {
		if data.Workers[i].Name == "refinery" {
			if _, failed := data.PanelErrors["MergeQueue"]; failed {
				data.Workers[i].StatusHint = "Merge queue unavailable (see panel notice)"
			} else {
				data.Workers[i].StatusHint = refineryStatusHint(len(data.MergeQueue))
			}
		}
	}
	data.Issues = append([]IssueRow(nil), data.Issues...)
	data.Issues = enrichIssuesWithAssignees(data.Issues, data.Hooks)
	data.Summary = computeSummary(data.Workers, data.Hooks, data.Issues, data.Convoys, data.Escalations, data.Activity)
	return data, drained
}

// Hash every panel and its availability. Canonicalize top-level row order so
// unordered source results cannot cause a refresh loop. Only the unrendered
// continuously advancing activity duration is normalized.
func hashDashboardSnapshot(data *ConvoyData) string {
	// Duration is a continuously advancing calculation, not rendered state.
	// Keep source timestamps, formatted ages and colors so real changes remain visible.
	normalized := *data
	normalized.Workers = append([]WorkerRow(nil), data.Workers...)
	normalized.Convoys = append([]ConvoyRow(nil), data.Convoys...)
	for i := range normalized.Workers {
		normalized.Workers[i].LastActivity.Duration = 0
	}
	for i := range normalized.Convoys {
		normalized.Convoys[i].LastActivity.Duration = 0
	}
	raw, _ := json.Marshal(normalized)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	var parts []string
	for name, value := range fields {
		var rows []json.RawMessage
		if len(value) > 0 && value[0] == '[' && json.Unmarshal(value, &rows) == nil {
			sort.Slice(rows, func(i, j int) bool { return bytes.Compare(rows[i], rows[j]) < 0 })
			value, _ = json.Marshal(rows)
		}
		parts = append(parts, name+":"+string(value))
	}
	return hashDashboardParts(parts)
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
	return NewDashboardMuxWithOptions(fetcher, webCfg, DashboardOptions{})
}

// NewDashboardMuxWithOptions configures explicit trusted frame navigation.
func NewDashboardMuxWithOptions(fetcher ConvoyFetcher, webCfg *config.WebTimeoutsConfig, opts DashboardOptions) (http.Handler, error) {
	if err := ValidateEmbedParentOrigin(opts.EmbedParentOrigin); err != nil {
		return nil, err
	}
	if webCfg == nil {
		webCfg = config.DefaultWebTimeoutsConfig()
	}

	csrfToken := generateCSRFToken()

	fetchTimeout := config.ParseDurationOrDefault(webCfg.FetchTimeout, 8*time.Second)
	convoyHandler, err := NewConvoyHandler(fetcher, fetchTimeout, csrfToken)
	if err != nil {
		return nil, err
	}

	convoyHandler.embedParentOrigin = opts.EmbedParentOrigin

	defaultRunTimeout := config.ParseDurationOrDefault(webCfg.DefaultRunTimeout, 30*time.Second)
	maxRunTimeout := config.ParseDurationOrDefault(webCfg.MaxRunTimeout, 60*time.Second)
	apiHandler := NewAPIHandler(defaultRunTimeout, maxRunTimeout, csrfToken)
	apiHandler.dashboard = convoyHandler
	if opts.GTPath != "" {
		apiHandler.gtPath = opts.GTPath
		if live, ok := fetcher.(*LiveConvoyFetcher); ok {
			live.gtBin = opts.GTPath
		}
	}

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

	return dashboardFramePolicy(mux, opts.EmbedParentOrigin), nil
}
