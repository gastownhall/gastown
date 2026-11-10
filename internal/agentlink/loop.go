// This file holds the machine agent loop (spec §7.1): hello, snapshot when
// asked, then flush queued events, long-poll requests, apply them, post
// results and heartbeat, reconnecting with backoff when the server is away.
package agentlink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"runtime"
	"time"
)

const (
	// MaxBatch is the most events per POST /events and results per POST
	// /results the server accepts.
	MaxBatch = 500

	// Body budgets, below the server's limits (8 MiB for snapshot and
	// events, 1 MiB for results) with room for the JSON envelope.
	maxEventsBody   = 7 << 20
	maxSnapshotBody = 7 << 20
	maxResultsBody  = 900 << 10

	// ErrCodeTooLarge replaces a result too large to post.
	ErrCodeTooLarge = "too_large"

	defaultPollWait   = 30 * time.Second
	defaultHeartbeat  = 30 * time.Second
	defaultBackoffMin = time.Second
	defaultBackoffMax = 60 * time.Second
)

// LoopConfig configures Run. TownRoot, ServerURL, MachineID, Token, Collect
// and Apply are required; zero durations take the defaults (30 s long-poll
// and heartbeat, 1 s to 60 s backoff).
type LoopConfig struct {
	TownRoot, ServerURL, MachineID, Token string
	GtbVersion                            string

	// Collect builds a full snapshot of the machine. The loop overwrites its
	// Cursor (see Run).
	Collect func() (Snapshot, error)
	// Apply applies requests that are neither expired nor already applied.
	Apply Applier
	// Scan, when set, runs before each heartbeat; production uses it to
	// queue artifact events for artifacts whose sha changed.
	Scan func() error
	// Now is the machine clock used for request expiry; nil means time.Now.
	Now func() time.Time
	// Logf receives log lines; nil discards them. The token is never logged.
	Logf func(format string, args ...any)
	// HTTPClient overrides the HTTP client (per-call timeouts still apply).
	HTTPClient *http.Client

	PollWait, HeartbeatEvery, BackoffMin, BackoffMax time.Duration
}

// Run runs the agent loop until ctx is cancelled (it then returns nil) or the
// server rejects the token (it logs "agentlink: token rejected; run gtb join
// again" and returns ErrTokenRejected).
//
// Session:
//  1. POST hello. If the server answers resync, send a snapshot.
//  2. Loop: flush pending events in batches of up to 500 and Ack the cursor
//     the server returns; long-poll GET requests; apply each request and post
//     all results in one batch; heartbeat every HeartbeatEvery.
//
// A 409 resync from POST /events sends a snapshot and carries on. Network
// errors and 5xx answers end the session; Run reconnects after an
// exponential backoff (BackoffMin doubling to BackoffMax, with jitter). The
// backoff resets only after a full productive round (snapshot if needed,
// flush, poll, results), not merely after hello.
// Nothing queued is dropped: events stay in the outbox until acked.
//
// Snapshot cursor: a snapshot is the machine's whole state, so it supersedes
// every queued state event. The loop sets Snapshot.Cursor to the queue's last
// ID, posts it, re-queues the pending events that are not state (evidence;
// they get new IDs above that cursor), then acks the cursor. If the machine
// stops between the post and the ack, the next POST /events gets 409 and
// resyncs again, so at worst an evidence event is sent twice.
//
// Requests: every request delivered gets a Result posted (R33), including
// ones that are dropped. A mutating request (approve, request_changes,
// item.create, item.sling, item.cancel) whose key is in the applied set
// (<townRoot>/.runtime/agentlink/applied.jsonl, last 10k keys) gets its
// stored result re-posted without calling Apply, even after ExpiresAt.
// Otherwise a request whose ExpiresAt is not after Now answers
// {ok:false, error:"expired"} without calling Apply. Every result is stored
// before it is posted for mutating requests; read-only ones are not recorded
// and are re-run if re-delivered. A successful "snapshot"
// request makes the loop resync.
func Run(ctx context.Context, cfg LoopConfig) error {
	if cfg.TownRoot == "" || cfg.ServerURL == "" || cfg.MachineID == "" || cfg.Token == "" || cfg.Collect == nil || cfg.Apply == nil {
		return errors.New("agentlink: Run: TownRoot, ServerURL, MachineID, Token, Collect and Apply are required")
	}
	l, err := newLoop(cfg)
	if err != nil {
		return err
	}
	backoff := l.cfg.BackoffMin
	for {
		productive, err := l.session(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, ErrTokenRejected) {
			l.logf("agentlink: token rejected; run gtb join again")
			return ErrTokenRejected
		}
		if productive {
			backoff = l.cfg.BackoffMin
		}
		wait := backoff
		if !transient(err) {
			// A refusal that retrying cannot fix quickly (protocol mismatch,
			// forbidden, bad request): keep trying, but slowly.
			wait = l.cfg.BackoffMax
		}
		l.logf("agentlink: disconnected: %v; retrying in %s", err, wait.Round(time.Millisecond))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(jitter(wait)):
		}
		backoff = min(backoff*2, l.cfg.BackoffMax)
	}
}

// jitter returns a duration in [d/2, d].
func jitter(d time.Duration) time.Duration {
	if d <= 1 {
		return d
	}
	return d/2 + rand.N(d/2+1)
}

type loop struct {
	cfg     LoopConfig
	c       *client
	q       *Queue
	applied *AppliedSet

	resync        bool // send a snapshot before the next flush
	lastHeartbeat time.Time
}

func newLoop(cfg LoopConfig) (*loop, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.PollWait <= 0 {
		cfg.PollWait = defaultPollWait
	}
	if cfg.HeartbeatEvery <= 0 {
		cfg.HeartbeatEvery = defaultHeartbeat
	}
	if cfg.BackoffMin <= 0 {
		cfg.BackoffMin = defaultBackoffMin
	}
	if cfg.BackoffMax < cfg.BackoffMin {
		cfg.BackoffMax = max(defaultBackoffMax, cfg.BackoffMin)
	}
	applied, err := OpenAppliedSet(cfg.TownRoot, DefaultAppliedKeys)
	if err != nil {
		return nil, err
	}
	return &loop{
		cfg:     cfg,
		c:       newClient(cfg.ServerURL, cfg.Token, cfg.HTTPClient),
		q:       OpenQueue(cfg.TownRoot),
		applied: applied,
	}, nil
}

func (l *loop) logf(format string, args ...any) { l.cfg.Logf(format, args...) }

// session runs one connection until an error. productive reports whether at
// least one full round succeeded: the snapshot when one was needed, the
// flush, a requests poll and posting its results. Only then does the caller reset its backoff
// (R35b), so a session that says hello but keeps failing (Collect, snapshot
// or events) backs off like any other failure instead of retrying, and
// re-collecting, every second.
func (l *loop) session(ctx context.Context) (productive bool, err error) {
	host, _ := os.Hostname()
	resync, err := l.c.hello(ctx, Hello{
		MachineID: l.cfg.MachineID, Hostname: host, OS: runtime.GOOS, Arch: runtime.GOARCH,
		GtbVersion: l.cfg.GtbVersion, Protocol: ProtocolVersion,
	})
	if err != nil {
		return false, err
	}
	l.logf("agentlink: connected to %s", l.cfg.ServerURL)
	l.resync = l.resync || resync
	for {
		if l.resync {
			if err := l.sendSnapshot(ctx); err != nil {
				return productive, err
			}
		}
		if err := l.flush(ctx); err != nil {
			return productive, err
		}
		if l.resync {
			continue // a 409 during flush: snapshot, then flush again
		}
		if time.Since(l.lastHeartbeat) >= l.cfg.HeartbeatEvery { // wall clock; Now is for expiry
			if l.cfg.Scan != nil {
				if err := l.cfg.Scan(); err != nil {
					l.logf("agentlink: scan: %v", err)
				}
			}
			if err := l.c.heartbeat(ctx); err != nil {
				return productive, err
			}
			l.lastHeartbeat = time.Now()
			if err := l.flush(ctx); err != nil { // events the scan queued
				return productive, err
			}
			if l.resync {
				continue
			}
		}
		reqs, err := l.c.requests(ctx, l.cfg.PollWait)
		if err != nil {
			return productive, err
		}
		if err := l.handle(ctx, reqs); err != nil {
			return productive, err
		}
		productive = true // snapshot, flush, poll and results all succeeded
	}
}

// flush sends every pending event in batches of at most MaxBatch events and
// maxEventsBody bytes, acking the cursor the server returns after each batch.
// A 409 sets l.resync and returns nil.
//
// A batch the server refuses as malformed or too large (400, 413), and a
// single event larger than the body budget, can never be delivered; retrying
// would wedge the queue. Such events are dropped with a log line, acked
// locally, and the loop resyncs: the snapshot carries the machine's state,
// so only the dropped events themselves are lost.
func (l *loop) flush(ctx context.Context) error {
	for {
		after, err := l.q.Cursor()
		if err != nil {
			return err
		}
		evs, err := l.q.Pending(MaxBatch)
		if err != nil {
			return err
		}
		if len(evs) == 0 {
			return nil
		}
		evs = fitEvents(evs)
		if len(evs) == 1 && jsonSize(evs[0]) > maxEventsBody {
			return l.drop(evs, "larger than the request body limit")
		}
		cursor, err := l.c.events(ctx, after, evs)
		var ae *apiError
		switch {
		case errors.Is(err, errResync):
			l.resync = true
			return nil
		case errors.As(err, &ae) && (ae.Status == http.StatusBadRequest || ae.Status == http.StatusRequestEntityTooLarge):
			return l.drop(evs, fmt.Sprintf("refused by the server (%d %s)", ae.Status, ae.Code))
		case err != nil:
			return err
		}
		if err := l.q.Ack(cursor); err != nil {
			return err
		}
	}
}

// drop acks evs locally without delivering them and schedules a resync.
func (l *loop) drop(evs []Event, why string) error {
	l.logf("agentlink: dropped %d event(s) %s..%s: %s", len(evs), evs[0].ID, evs[len(evs)-1].ID, why)
	if err := l.q.Ack(evs[len(evs)-1].ID); err != nil {
		return err
	}
	l.resync = true
	return nil
}

// fitEvents returns the longest prefix of evs (at least one event) whose
// encoding fits maxEventsBody.
func fitEvents(evs []Event) []Event {
	total := 0
	for i, e := range evs {
		total += jsonSize(e) + 1
		if total > maxEventsBody && i > 0 {
			return evs[:i]
		}
	}
	return evs
}

func jsonSize(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b)
}

// sendSnapshot collects and posts a snapshot (see Run for the cursor rule).
func (l *loop) sendSnapshot(ctx context.Context) error {
	last, err := l.q.LastID()
	if err != nil {
		return err
	}
	acked, err := l.q.Cursor()
	if err != nil {
		return err
	}
	var keep []Event
	if last > acked {
		pend, err := l.q.Pending(int(^uint(0) >> 1))
		if err != nil {
			return err
		}
		for _, e := range pend {
			if e.ID <= last && e.Type == EventEvidence {
				keep = append(keep, e)
			}
		}
	}
	snap, err := l.cfg.Collect()
	if err != nil {
		return fmt.Errorf("agentlink: collect snapshot: %w", err)
	}
	snap.Cursor = last
	// A snapshot over the body budget is sent without artifacts, which
	// follow as artifact events (each artifact is capped by the collector).
	var deferred []Event
	if jsonSize(snap) > maxSnapshotBody {
		items := make([]ItemView, len(snap.Items))
		for i, iv := range snap.Items {
			for _, a := range iv.Artifacts {
				b, _ := json.Marshal(a)
				deferred = append(deferred, Event{Type: EventArtifact, ItemID: iv.ID, Data: b})
			}
			iv.Artifacts = nil
			items[i] = iv
		}
		snap.Items = items
		l.logf("agentlink: snapshot too large; sending %d artifacts as events", len(deferred))
	}
	if err := l.c.snapshot(ctx, snap); err != nil {
		return err
	}
	keep = append(keep, deferred...)
	for _, e := range keep {
		e.Data = append(e.Data[:0:0], e.Data...)
		if err := l.q.Append(e); err != nil {
			return err
		}
	}
	if err := l.q.Ack(last); err != nil {
		return err
	}
	l.resync = false
	l.logf("agentlink: sent snapshot (%d items, cursor %s)", len(snap.Items), last)
	return nil
}

// handle applies reqs and posts their results.
func (l *loop) handle(ctx context.Context, reqs []Request) error {
	if len(reqs) == 0 {
		return nil
	}
	results := make([]Result, 0, len(reqs))
	for _, r := range reqs {
		results = append(results, l.apply(ctx, r))
	}
	for len(results) > 0 {
		n, size := 0, 2
		for n < len(results) && n < MaxBatch {
			sz := jsonSize(results[n]) + 1
			if n > 0 && size+sz > maxResultsBody {
				break
			}
			size += sz
			n++
		}
		if err := l.c.results(ctx, results[:n]); err != nil {
			return err
		}
		results = results[n:]
	}
	return nil
}

// mutatingTypes are the request types whose full result is kept in the
// applied set and re-posted on re-delivery instead of re-running them.
var mutatingTypes = map[string]bool{
	"approve": true, "request_changes": true, "item.create": true, "item.sling": true, "item.cancel": true,
}

// apply answers one request (see Run). The applied-set lookup comes before
// the expiry check (R35a): a mutating request that ran but whose result was
// lost is re-delivered until it has one, possibly after ExpiresAt, and must
// get the result of the run, not "expired". Read-only requests (snapshot,
// inspect, terminal.peek) are not recorded at all and are re-run when
// re-delivered before expiry (R35c, R37); re-running them is harmless.
func (l *loop) apply(ctx context.Context, r Request) Result {
	if res, ok := l.applied.Get(r.Key); ok && mutatingTypes[r.Type] {
		l.logf("agentlink: request %s already applied; re-posting its result", r.Key)
		res.Key = r.Key
		return res
	}
	if !r.ExpiresAt.After(l.cfg.Now()) {
		l.logf("agentlink: request %s (%s) expired; dropped", r.Key, r.Type)
		return Fail(r.Key, ErrCodeExpired)
	}
	res := l.safeApply(ctx, r)
	res.Key = r.Key
	if jsonSize(res) > maxResultsBody {
		l.logf("agentlink: result of request %s (%s) is too large to post", r.Key, r.Type)
		res = Fail(r.Key, ErrCodeTooLarge)
	}
	// Only mutating requests are recorded: read-only ones are re-run on
	// re-delivery and must not push mutating keys out of the window.
	if mutatingTypes[r.Type] {
		if err := l.applied.Put(res); err != nil {
			l.logf("agentlink: record applied request %s: %v", r.Key, err)
		}
	}
	if r.Type == "snapshot" && res.OK {
		l.resync = true
	}
	return res
}

func (l *loop) safeApply(ctx context.Context, r Request) (res Result) {
	defer func() {
		if p := recover(); p != nil {
			l.logf("agentlink: request %s (%s) panicked: %v", r.Key, r.Type, p)
			res = Fail(r.Key, ErrCodeFailed)
		}
	}()
	return l.cfg.Apply.Apply(ctx, r)
}
