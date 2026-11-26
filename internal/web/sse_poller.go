package web

import (
	"context"
	"sync"
	"time"
)

// sseHashPollInterval is how often the shared poller recomputes the dashboard
// hash. It matches defaultCacheTTL: the dashboard render is cached for that
// long, so detecting changes more often only produces cache hits.
const sseHashPollInterval = defaultCacheTTL

// dashboardHashPoller computes the dashboard change-detection hash once per
// interval and shares it with every connected SSE client.
//
// Previously each /api/events connection ran its own 2s ticker that spawned
// gt status, gt hooks list and gt mail inbox, so cost scaled with the number
// of open tabs (and reconnects). The poller runs only while at least one
// client is subscribed, so an idle dashboard still costs nothing.
type dashboardHashPoller struct {
	interval time.Duration
	compute  func(ctx context.Context) string

	mu          sync.Mutex
	subscribers int
	hash        string
	// changed is closed (and replaced) whenever hash changes, waking all
	// subscribers waiting on it.
	changed chan struct{}
	cancel  context.CancelFunc
	// generation guards against a stopped poll loop publishing a result after
	// a new loop has started.
	generation uint64
}

func newDashboardHashPoller(interval time.Duration, compute func(ctx context.Context) string) *dashboardHashPoller {
	return &dashboardHashPoller{
		interval: interval,
		compute:  compute,
		changed:  make(chan struct{}),
	}
}

// subscribe registers a client and starts the poll loop if this is the first
// one. Callers must call the returned unsubscribe function exactly once.
func (p *dashboardHashPoller) subscribe() (unsubscribe func()) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.subscribers++
	if p.subscribers == 1 {
		ctx, cancel := context.WithCancel(context.Background())
		p.cancel = cancel
		p.generation++
		go p.run(ctx, p.generation)
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.subscribers--
			if p.subscribers == 0 && p.cancel != nil {
				p.cancel()
				p.cancel = nil
				// Drop the stale hash so the next first subscriber waits for
				// a fresh one rather than seeing old state.
				p.hash = ""
			}
		})
	}
}

// current returns the latest hash and a channel that is closed when it next
// changes.
func (p *dashboardHashPoller) current() (string, <-chan struct{}) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.hash, p.changed
}

func (p *dashboardHashPoller) run(ctx context.Context, gen uint64) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		p.poll(ctx, gen)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (p *dashboardHashPoller) poll(ctx context.Context, gen uint64) {
	hash := p.compute(ctx)
	if hash == "" || ctx.Err() != nil {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if gen != p.generation || hash == p.hash {
		return
	}
	p.hash = hash
	close(p.changed)
	p.changed = make(chan struct{})
}

// dashboardPoller returns the handler's shared poller, creating it on first
// use (APIHandler is sometimes constructed as a struct literal in tests).
func (h *APIHandler) dashboardPoller() *dashboardHashPoller {
	h.hashPollerOnce.Do(func() {
		h.hashPoller = newDashboardHashPoller(sseHashPollInterval, h.computeDashboardHash)
	})
	return h.hashPoller
}
