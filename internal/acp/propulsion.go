package acp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/jonbaldie/gastown/internal/nudge"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/townlog"
)

// acpDebugLogger provides file-based debug logging for ACP when GT_ACP_DEBUG=1.
// It lazily opens the log file on first use and keeps it open for the session.
type acpDebugLogger struct {
	mu       sync.Mutex
	file     *os.File
	townRoot string
	enabled  bool
}

var debugLogger = &acpDebugLogger{
	enabled: os.Getenv("GT_ACP_DEBUG") != "",
}

// init opens the log file if debugging is enabled. It is called lazily
// when the first debug log is written, with the townRoot context.
func (l *acpDebugLogger) init(townRoot string) error {
	if !l.enabled {
		return nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file != nil {
		return nil
	}

	if townRoot == "" {
		return fmt.Errorf("townRoot is empty")
	}

	logDir := filepath.Join(townRoot, "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return fmt.Errorf("creating logs directory: %w", err)
	}

	logPath := filepath.Join(logDir, "acp.log")
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("opening acp.log: %w", err)
	}

	l.file = f
	l.townRoot = townRoot
	return nil
}

// log writes a debug message to the ACP log file if debugging is enabled.
func (l *acpDebugLogger) log(format string, args ...any) {
	if !l.enabled {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file == nil {
		return
	}

	timestamp := time.Now().Format("2006-01-02 15:04:05")
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(l.file, "%s %s\n", timestamp, msg)
}

// close closes the log file if it was opened.
func (l *acpDebugLogger) close() {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file != nil {
		l.file.Close()
		l.file = nil
	}
}

// debugLog logs to acp.log when GT_ACP_DEBUG=1. It lazily initializes
// the log file on first call with the given townRoot.
func debugLog(townRoot, format string, args ...any) {
	if !debugLogger.enabled {
		return
	}
	// Lazy init - only creates file on first debug log
	if debugLogger.file == nil {
		if err := debugLogger.init(townRoot); err != nil {
			// Can't log anywhere useful if file init fails, silently return
			return
		}
	}
	debugLogger.log(format, args...)
}

// logEvent logs an important event to town.log (failures, errors, lifecycle).
func logEvent(townRoot, eventType, context string) {
	if townRoot == "" {
		return
	}
	logger := townlog.NewLogger(townRoot)
	_ = logger.Log(townlog.EventType(eventType), "mayor/acp", context)
	// Also log to acp.log if debug mode is enabled
	debugLog(townRoot, "[%s] %s", eventType, context)
}

type Propeller struct {
	proxy       *Proxy
	townRoot    string
	session     string
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	warnedNoSID bool
}

func NewPropeller(proxy *Proxy, townRoot, session string) *Propeller {
	return &Propeller{
		proxy:    proxy,
		townRoot: townRoot,
		session:  session,
	}
}

func (p *Propeller) Start(ctx context.Context) {
	debugLog(p.townRoot, "[Propeller] Starting for session %q in town %q", p.session, p.townRoot)
	p.ctx, p.cancel = context.WithCancel(ctx)
	p.wg.Add(1)
	go p.waitForSessionAndStart()
}

// waitForSessionAndPoll waits for the ACP handshake to complete (sessionID available)
// before starting the poll loop. This ensures the proxy is ready to inject prompts.
// waitForSessionAndStart waits for the ACP handshake to complete (sessionID available)
// before starting the event-driven loop. This ensures the proxy is ready to inject prompts.
// If no IDE connects within the timeout, event loop continues with degraded functionality
// (notifications will be skipped since there's no session to inject into).
func (p *Propeller) waitForSessionAndStart() {
	defer p.wg.Done()

	// Wait for sessionID with a timeout
	waitCtx, cancel := context.WithTimeout(p.ctx, 30*time.Second)
	defer cancel()

	if p.proxy != nil {
		if err := p.proxy.WaitForSessionID(waitCtx); err != nil {
			// Log to town.log - this is a significant event (degraded mode)
			logEvent(p.townRoot, "acp_degraded", "sessionID not available: no IDE connected, notifications disabled")
			debugLog(p.townRoot, "[Propeller] SessionID not available after 30s: %v", err)
			debugLog(p.townRoot, "[Propeller] Continuing with degraded mode - mail/hook detection will work but notifications will be skipped")
			debugLog(p.townRoot, "[Propeller] This is expected if no ACP client (IDE) is connected to the proxy")
		} else {
			debugLog(p.townRoot, "[Propeller] SessionID available, starting event-driven loop with full notification support")
		}
	}

	p.eventLoop()
}

// eventLoop drives event-driven propulsion by listening to the nudge queue watcher.
// It drains queued nudges and injects them immediately when the watcher signals.
func (p *Propeller) eventLoop() {
	// Create watcher for the nudge queue
	watcher, err := nudge.WatcherForSession(p.townRoot, p.session)
	if err != nil {
		debugLog(p.townRoot, "[Propeller] Failed to create nudge watcher: %v", err)
		// If we can't watch, we can't deliver nudges. Log and exit.
		logEvent(p.townRoot, "acp_error", fmt.Sprintf("failed to create nudge watcher: %v", err))
		return
	}
	defer func() { _ = watcher.Close() }()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-watcher.Events():
			p.deliverNudges()
		}
	}
}

// deliverNudges drains queued nudges and injects them into the ACP session.
func (p *Propeller) deliverNudges() {
	nudges, err := nudge.Drain(p.townRoot, p.session)
	if err != nil {
		debugLog(p.townRoot, "[Propeller] deliverNudges: Drain error: %v", err)
		return
	}
	if len(nudges) == 0 {
		return
	}

	debugLog(p.townRoot, "[Propeller] deliverNudges: drained %d nudge(s)", len(nudges))

	text := formatNudgesForPropeller(nudges)

	// Determine urgency
	urgent := false
	for _, n := range nudges {
		if n.Priority == nudge.PriorityUrgent {
			urgent = true
			break
		}
	}

	meta := buildSessionUpdateMeta(nudges, p.session)
	requeue := func(reason string) {
		if err := nudge.Requeue(p.townRoot, p.session, nudges); err != nil {
			logEvent(p.townRoot, "acp_error", fmt.Sprintf("failed to requeue nudges after %s: %v", reason, err))
			style.PrintWarning("ACP Propeller failed to requeue nudges after %s: %v", reason, err)
			return
		}
		logEvent(p.townRoot, "acp_degraded", fmt.Sprintf("requeued %d nudges: %s", len(nudges), reason))
	}

	if p.proxy == nil || p.proxy.SessionID() == "" {
		requeue("session not ready")
		return
	}

	if err := p.notify(text, meta, urgent); err != nil {
		requeue(fmt.Sprintf("delivery failure: %v", err))
		style.PrintWarning("ACP Propeller failed to deliver nudge: %v", err)
	}
}

type escalationDeliveryMeta struct {
	Kind     string
	ThreadID string
	Severity string
}

func escalationMetaFromNudges(nudges []nudge.QueuedNudge) *escalationDeliveryMeta {
	for _, n := range nudges {
		if n.Kind != "escalation" {
			continue
		}
		return &escalationDeliveryMeta{
			Kind:     n.Kind,
			ThreadID: n.ThreadID,
			Severity: n.Severity,
		}
	}
	return nil
}

func formatNudgesForPropeller(nudges []nudge.QueuedNudge) string {
	return nudge.FormatForInjection(nudges)
}

func buildSessionUpdateMeta(nudges []nudge.QueuedNudge, session string) map[string]string {
	meta := map[string]string{
		"gt/eventType": "nudge",
		"gt/count":     strconv.Itoa(len(nudges)),
		"gt/drained":   "true",
		"gt/session":   session,
	}
	urgentCount := 0
	for _, n := range nudges {
		if n.Priority == nudge.PriorityUrgent {
			urgentCount++
		}
	}
	meta["gt/urgent"] = strconv.Itoa(urgentCount)
	if escalationMeta := escalationMetaFromNudges(nudges); escalationMeta != nil {
		meta["gt/escalation"] = "true"
		meta["gt/threadID"] = escalationMeta.ThreadID
		meta["gt/severity"] = escalationMeta.Severity
		meta["gt/kind"] = escalationMeta.Kind
	}
	return meta
}

func (p *Propeller) Stop() {
	debugLog(p.townRoot, "[Propeller] Stopping")
	// Close the debug log file if it was opened
	debugLogger.close()
	logEvent(p.townRoot, "acp_stop", "propeller stopped")
	if p.cancel != nil {
		p.cancel()
	}
	p.wg.Wait()
}

func (p *Propeller) notifyWithMeta(text string, meta map[string]string) {
	if p.proxy == nil || text == "" {
		return
	}

	sessionID := p.proxy.SessionID()
	if sessionID == "" {
		// Log once when sessionID is not available (e.g., no IDE connected to ACP proxy)
		if !p.warnedNoSID {
			p.warnedNoSID = true
			debugLog(p.townRoot, "[Propeller] notifyWithMeta: sessionID not available - ACP handshake may not have completed (no IDE connected?). Notifications will be skipped.")
		}
		return
	}

	debugLog(p.townRoot, "[Propeller] notifyWithMeta: sessionID=%q text=%q", sessionID, text)

	params := map[string]any{
		"update": map[string]any{
			"sessionUpdate": "agent_message_chunk",
			"content": map[string]any{
				"type": "text",
				"text": "\n\n" + text + "\n\n",
			},
			"_meta": meta,
		},
	}

	if err := p.proxy.InjectNotificationToUI("session/update", params); err != nil {
		style.PrintWarning("ACP Propeller failed to inject notification: %v", err)
	}
}

// notify sends a notification to both the UI (via session/update) and the Agent (via session/prompt).
// This couples the two operations, ensuring consistency with tmux session behavior where notifications
// are delivered as terminal input (UI sees update, Agent receives prompt).
// The SessionID and IsBusy checks ensure we don't interrupt an active agent turn, unless urgent is true.
func (p *Propeller) notify(text string, meta map[string]string, urgent bool) error {
	if p.proxy == nil || text == "" {
		return nil
	}
	if p.proxy.SessionID() == "" {
		return fmt.Errorf("sessionID not available")
	}

	// Always notify the UI
	p.notifyWithMeta(text, meta)

	// Notify the Agent only if session is ready.
	// We bypass the IsBusy check if urgent is true (e.g. nudges/escalations).
	if p.proxy.SessionID() != "" {
		if urgent || !p.proxy.IsBusy() {
			// Try a few times in case of transient turn-state changes
			var err error
			for i := 0; i < 3; i++ {
				err = p.proxy.InjectPrompt(text)
				if err == nil {
					return nil
				}
				debugLog(p.townRoot, "[Propeller] InjectPrompt attempt %d failed: %v", i+1, err)
				time.Sleep(100 * time.Millisecond)
			}
			// Log failure to town.log
			logEvent(p.townRoot, "acp_error", fmt.Sprintf("failed to inject prompt after retries: %v", err))
			style.PrintWarning("ACP Propeller failed to inject agent prompt after retries: %v", err)
			return err
		}
	}
	return nil
}
