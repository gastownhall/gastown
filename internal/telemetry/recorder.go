// Package telemetry — recorder.go
// Recording helper functions for all GT telemetry events.
// Each function emits both an OTel log event (→ VictoriaLogs) and increments
// a metric counter (→ VictoriaMetrics).
package telemetry

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/metric"
)

// runIDKey is the context key for the GASTOWN run identifier.
type runIDKey struct{}

// WithRunID returns a context carrying the given run ID.
// The run ID is automatically injected into every telemetry event emitted
// with that context, enabling waterfall correlation across all events in a
// single agent session (GASTOWN run).
func WithRunID(ctx context.Context, runID string) context.Context {
	return context.WithValue(ctx, runIDKey{}, runID)
}

// RunIDFromCtx extracts the run ID from ctx. Falls back to the GT_RUN
// environment variable so that subprocess telemetry (bd, mail, …) is
// correlated even when the ctx has no injected run ID.
func RunIDFromCtx(ctx context.Context) string {
	if v, ok := ctx.Value(runIDKey{}).(string); ok && v != "" {
		return v
	}
	return os.Getenv("GT_RUN")
}

// instanceID derives a human-readable Gastown instance identifier from the
// town root path and the machine hostname.
// Format: "<hostname>:<basename(townRoot)>" (e.g. "laptop:gt").
// Falls back to basename alone when hostname is unavailable.
func instanceID(townRoot string) string {
	base := filepath.Base(townRoot)
	if base == "." || base == "" {
		base = townRoot
	}
	hostname, _ := os.Hostname()
	if hostname == "" {
		return base
	}
	return hostname + ":" + base
}

// MailMessageInfo carries enriched mail message metadata for telemetry.
// All fields are optional; pass zero values for unknown fields.
type MailMessageInfo struct {
	ID       string // message ID
	From     string // sender address
	To       string // recipient address(es), comma-separated
	Subject  string // message subject
	Body     string // message body — logged only when GT_LOG_MAIL_BODY=true (truncated to 256 bytes)
	ThreadID string // thread / conversation ID
	Priority string // "high", "normal", "low"
	MsgType  string // message type label (e.g. "work", "notify", "queue")
}

const (
	meterRecorderName = "github.com/jonbaldie/gastown"
	loggerName        = "gastown"
)

// recorderInstruments holds all lazy-initialized OTel metric instruments.
type recorderInstruments struct {
	// Counters
	bdTotal               metric.Int64Counter
	sessionTotal          metric.Int64Counter
	sessionStopTotal      metric.Int64Counter
	promptTotal           metric.Int64Counter
	paneOutputTotal       metric.Int64Counter
	agentEventTotal       metric.Int64Counter
	agentInstantiateTotal metric.Int64Counter
	primeTotal            metric.Int64Counter
	agentStateTotal       metric.Int64Counter
	polecatTotal          metric.Int64Counter
	polecatRemoveTotal    metric.Int64Counter
	slingTotal            metric.Int64Counter
	mailTotal             metric.Int64Counter
	nudgeTotal            metric.Int64Counter
	doneTotal             metric.Int64Counter
	daemonRestartTotal    metric.Int64Counter
	formulaTotal          metric.Int64Counter
	convoyTotal           metric.Int64Counter
	molCookTotal          metric.Int64Counter
	molWispTotal          metric.Int64Counter
	molSquashTotal        metric.Int64Counter
	molBurnTotal          metric.Int64Counter
	beadCreateTotal       metric.Int64Counter

	// Histograms
	bdDurationHist metric.Float64Histogram
}

var (
	instOnce sync.Once
	inst     recorderInstruments
)

// contentLimits caches configurable truncation limits parsed from env at first use.
// Env vars are read once — changing them at runtime has no effect.
var (
	limitsOnce      sync.Once
	agentContentLim int // GT_LOG_AGENT_CONTENT_LIMIT, default 512
	bdContentLim    int // GT_LOG_BD_CONTENT_LIMIT, default 2048
	paneContentLim  int // GT_LOG_PANE_CONTENT_LIMIT, default 8192
)

func initContentLimits() {
	limitsOnce.Do(func() {
		agentContentLim = envInt("GT_LOG_AGENT_CONTENT_LIMIT", 512)
		bdContentLim = envInt("GT_LOG_BD_CONTENT_LIMIT", 2048)
		paneContentLim = envInt("GT_LOG_PANE_CONTENT_LIMIT", 8192)
	})
}

// envInt returns the integer value of the named env var, or defaultVal if unset or unparseable.
func envInt(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return defaultVal
}

// initInstruments registers all recorder metric instruments against the current
// global MeterProvider. Must be called after telemetry.Init so the real
// provider is set. Also called lazily on first use as a safety net.
func initInstruments() {
	instOnce.Do(func() {
		m := otel.GetMeterProvider().Meter(meterRecorderName)

		// Counters
		inst.bdTotal, _ = m.Int64Counter("gastown.bd.calls.total",
			metric.WithDescription("Total bd CLI command invocations"),
		)
		inst.sessionTotal, _ = m.Int64Counter("gastown.session.starts.total",
			metric.WithDescription("Total agent session starts"),
		)
		inst.sessionStopTotal, _ = m.Int64Counter("gastown.session.stops.total",
			metric.WithDescription("Total agent session terminations"),
		)
		inst.promptTotal, _ = m.Int64Counter("gastown.prompt.sends.total",
			metric.WithDescription("Total tmux SendKeys prompt dispatches"),
		)
		inst.paneOutputTotal, _ = m.Int64Counter("gastown.pane.output.total",
			metric.WithDescription("Total pane output chunks emitted to VictoriaLogs"),
		)
		inst.agentEventTotal, _ = m.Int64Counter("gastown.agent.events.total",
			metric.WithDescription("Total agent conversation events emitted to VictoriaLogs"),
		)
		inst.agentInstantiateTotal, _ = m.Int64Counter("gastown.agent.instantiations.total",
			metric.WithDescription("Total agent session instantiations (one per agent spawn)"),
		)
		inst.primeTotal, _ = m.Int64Counter("gastown.prime.total",
			metric.WithDescription("Total gt prime invocations"),
		)
		inst.agentStateTotal, _ = m.Int64Counter("gastown.agent.state_changes.total",
			metric.WithDescription("Total agent state transitions"),
		)
		inst.polecatTotal, _ = m.Int64Counter("gastown.polecat.spawns.total",
			metric.WithDescription("Total polecat spawns"),
		)
		inst.polecatRemoveTotal, _ = m.Int64Counter("gastown.polecat.removes.total",
			metric.WithDescription("Total polecat removals"),
		)
		inst.slingTotal, _ = m.Int64Counter("gastown.sling.dispatches.total",
			metric.WithDescription("Total sling work dispatches"),
		)
		inst.mailTotal, _ = m.Int64Counter("gastown.mail.operations.total",
			metric.WithDescription("Total mail/bd SDK operations"),
		)
		inst.nudgeTotal, _ = m.Int64Counter("gastown.nudge.total",
			metric.WithDescription("Total gt nudge invocations"),
		)
		inst.doneTotal, _ = m.Int64Counter("gastown.done.total",
			metric.WithDescription("Total gt done invocations (polecat work completions)"),
		)
		inst.daemonRestartTotal, _ = m.Int64Counter("gastown.daemon.agent_restarts.total",
			metric.WithDescription("Total daemon-initiated agent session restarts"),
		)
		inst.formulaTotal, _ = m.Int64Counter("gastown.formula.instantiations.total",
			metric.WithDescription("Total formula→wisp instantiations"),
		)
		inst.convoyTotal, _ = m.Int64Counter("gastown.convoy.creates.total",
			metric.WithDescription("Total auto-convoy creations"),
		)
		inst.molCookTotal, _ = m.Int64Counter("gastown.mol.cooks.total",
			metric.WithDescription("Total formula cook operations (formula → proto)"),
		)
		inst.molWispTotal, _ = m.Int64Counter("gastown.mol.wisps.total",
			metric.WithDescription("Total molecule wisp creations (proto → wisp)"),
		)
		inst.molSquashTotal, _ = m.Int64Counter("gastown.mol.squashes.total",
			metric.WithDescription("Total molecule squash operations (mol → digest)"),
		)
		inst.molBurnTotal, _ = m.Int64Counter("gastown.mol.burns.total",
			metric.WithDescription("Total molecule burn operations (destroy)"),
		)
		inst.beadCreateTotal, _ = m.Int64Counter("gastown.bead.creates.total",
			metric.WithDescription("Total bead creations from molecule instantiation"),
		)

		// Histograms
		inst.bdDurationHist, _ = m.Float64Histogram("gastown.bd.duration_ms",
			metric.WithDescription("bd CLI call round-trip latency in milliseconds"),
			metric.WithUnit("ms"),
		)
	})
}

// statusStr returns "ok" or "error" depending on whether err is nil.
func statusStr(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}

// addRunID injects the run.id attribute from ctx (or GT_RUN env) into r.
// Called by emit and RecordAgentEvent so every telemetry event carries the
// GASTOWN run identifier for waterfall correlation.
func addRunID(ctx context.Context, r *otellog.Record) {
	if runID := RunIDFromCtx(ctx); runID != "" {
		r.AddAttributes(otellog.String("run.id", runID))
	}
}

// emit sends an OTel log event with the given body and key-value attributes.
// Automatically injects run.id from ctx when present.
func emit(ctx context.Context, body string, sev otellog.Severity, attrs ...otellog.KeyValue) {
	logger := global.GetLoggerProvider().Logger(loggerName)
	var r otellog.Record
	r.SetBody(otellog.StringValue(body))
	r.SetSeverity(sev)
	r.AddAttributes(attrs...)
	addRunID(ctx, &r)
	logger.Emit(ctx, r)
}

// errKV returns a log KeyValue with the error message, or empty string if nil.
func errKV(err error) otellog.KeyValue {
	if err != nil {
		return otellog.String("error", err.Error())
	}
	return otellog.String("error", "")
}

// severity returns SeverityInfo on success, SeverityError on failure.
func severity(err error) otellog.Severity {
	if err != nil {
		return otellog.SeverityError
	}
	return otellog.SeverityInfo
}

// truncateOutput trims s to max bytes and appends "…" when truncated.
// Avoids splitting multi-byte UTF-8 characters at the boundary.
// Pass max ≤ 0 to disable truncation entirely.
func truncateOutput(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	// Walk back from the cut point to avoid splitting a multi-byte rune.
	truncated := s[:max]
	for len(truncated) > 0 && !utf8.ValidString(truncated) {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated + "…"
}

// RecordBDCall records a bd CLI invocation with duration (metrics + log event).
// args is the full argument list; args[0] is used as the subcommand label.
// durationMs is the wall-clock time of the subprocess in milliseconds.
// stdout and stderr are the raw process outputs; both are truncated before logging.
//
// stdout and stderr are only included in the log event when GT_LOG_BD_OUTPUT=true.
// They are opt-in because bd output may contain sensitive data (API tokens, PII).
// See docs/OTEL-ENV-VARS.md for details.
func RecordBDCall(ctx context.Context, args []string, durationMs float64, err error, stdout []byte, stderr string) {
	initInstruments()
	subcommand := ""
	if len(args) > 0 {
		subcommand = args[0]
	}
	status := statusStr(err)
	attrs := metric.WithAttributes(
		attribute.String("status", status),
		attribute.String("subcommand", subcommand),
	)
	inst.bdTotal.Add(ctx, 1, attrs)
	inst.bdDurationHist.Record(ctx, durationMs, attrs)
	kvs := []otellog.KeyValue{
		otellog.String("subcommand", subcommand),
		otellog.String("args", strings.Join(args, " ")),
		otellog.Float64("duration_ms", durationMs),
		otellog.String("status", status),
		errKV(err),
	}
	// stdout/stderr are opt-in (may contain tokens or PII returned by bd).
	// Truncated to GT_LOG_BD_CONTENT_LIMIT bytes (default 2048).
	if os.Getenv("GT_LOG_BD_OUTPUT") == "true" {
		initContentLimits()
		kvs = append(kvs,
			otellog.String("stdout", truncateOutput(string(stdout), bdContentLim)),
			otellog.String("stderr", truncateOutput(stderr, bdContentLim)),
		)
	}
	emit(ctx, "bd.call", severity(err), kvs...)
}

// RecordSessionStart records an agent session start (metrics + log event).
func RecordSessionStart(ctx context.Context, sessionID, role string, err error) {
	initInstruments()
	status := statusStr(err)
	inst.sessionTotal.Add(ctx, 1,
		metric.WithAttributes(
			attribute.String("status", status),
			attribute.String("role", role),
		),
	)
	emit(ctx, "session.start", severity(err),
		otellog.String("session_id", sessionID),
		otellog.String("role", role),
		otellog.String("status", status),
		errKV(err),
	)
}

// RecordSessionStop records an agent session termination (metrics + log event).
func RecordSessionStop(ctx context.Context, sessionID string, err error) {
	initInstruments()
	status := statusStr(err)
	inst.sessionStopTotal.Add(ctx, 1,
		metric.WithAttributes(attribute.String("status", status)),
	)
	emit(ctx, "session.stop", severity(err),
		otellog.String("session_id", sessionID),
		otellog.String("status", status),
		errKV(err),
	)
}

// RecordPromptSend records a tmux SendKeys prompt dispatch (metrics + log event).
// keys content is opt-in: set GT_LOG_PROMPT_KEYS=true to include it (truncated
// to 256 bytes). Default off because prompts may contain secrets or PII.
func RecordPromptSend(ctx context.Context, session, keys string, debounceMs int, err error) {
	initInstruments()
	status := statusStr(err)
	inst.promptTotal.Add(ctx, 1,
		metric.WithAttributes(attribute.String("status", status)),
	)
	kvs := []otellog.KeyValue{
		otellog.String("session", session),
		otellog.Int64("keys_len", int64(len(keys))),
		otellog.Int64("debounce_ms", int64(debounceMs)),
		otellog.String("status", status),
		errKV(err),
	}
	if os.Getenv("GT_LOG_PROMPT_KEYS") == "true" {
		kvs = append(kvs, otellog.String("keys", truncateOutput(keys, 256)))
	}
	emit(ctx, "prompt.send", severity(err), kvs...)
}

// AgentInstantiateInfo carries all fields for the root agent.instantiate event.
// All fields except RunID, AgentType, Role, AgentName, SessionID, and TownRoot
// are optional; pass empty strings for unknown fields.
type AgentInstantiateInfo struct {
	// RunID is the GASTOWN run UUID (GT_RUN), the waterfall primary key.
	RunID string
	// AgentType is the runtime adapter name ("claudecode", "opencode", …).
	AgentType string
	// Role is the Gastown agent role ("polecat", "witness", "mayor", "refinery",
	// "crew", "deacon", "dog", "boot").
	Role string
	// AgentName is the specific agent name within its role (e.g. "wyvern-Toast").
	// For singletons (mayor, deacon) this equals the role name.
	AgentName string
	// SessionID is the tmux session name (TIMOX pane).
	SessionID string
	// RigName is the rig name; empty for town-level agents (mayor, deacon).
	RigName string
	// TownRoot is the absolute path to the Gastown town root (~/gt); used to
	// derive the instance identifier "hostname:basename(townRoot)".
	TownRoot string
	// IssueID is the bead ID of the work item assigned to this agent.
	// Empty for agents not started with an explicit issue (witness, mayor, …).
	IssueID string
	// GitBranch is the current git branch of the working directory at spawn time.
	GitBranch string
	// GitCommit is the HEAD commit SHA of the working directory at spawn time.
	GitCommit string
}

// RecordAgentInstantiate records the creation of a new agent session — the
// root GASTOWN event that anchors all downstream waterfall telemetry.
func RecordAgentInstantiate(ctx context.Context, info AgentInstantiateInfo) {
	initInstruments()
	inst.agentInstantiateTotal.Add(ctx, 1,
		metric.WithAttributes(
			attribute.String("agent_type", info.AgentType),
			attribute.String("role", info.Role),
			attribute.String("rig", info.RigName),
		),
	)
	emit(ctx, "agent.instantiate", otellog.SeverityInfo,
		otellog.String("run.id", info.RunID),
		otellog.String("instance", instanceID(info.TownRoot)),
		otellog.String("town_root", info.TownRoot),
		otellog.String("agent_type", info.AgentType),
		otellog.String("role", info.Role),
		otellog.String("agent_name", info.AgentName),
		otellog.String("session_id", info.SessionID),
		otellog.String("rig", info.RigName),
		otellog.String("issue_id", info.IssueID),
		otellog.String("git_branch", info.GitBranch),
		otellog.String("git_commit", info.GitCommit),
	)
}

// RecordMailMessage records a mail send/read/archive operation.
// All MailMessageInfo fields are optional; pass zero values for unknown fields.
// msg.body is opt-in: set GT_LOG_MAIL_BODY=true to include it (truncated to
// 256 bytes). Default off because mail bodies may contain secrets or PII.
func RecordMailMessage(ctx context.Context, operation string, msg MailMessageInfo, err error) {
	initInstruments()
	status := statusStr(err)
	inst.mailTotal.Add(ctx, 1,
		metric.WithAttributes(
			attribute.String("status", status),
			attribute.String("operation", operation),
		),
	)
	kvs := []otellog.KeyValue{
		otellog.String("operation", operation),
		otellog.String("msg.id", msg.ID),
		otellog.String("msg.from", msg.From),
		otellog.String("msg.to", msg.To),
		otellog.String("msg.subject", msg.Subject),
		otellog.String("msg.thread_id", msg.ThreadID),
		otellog.String("msg.priority", msg.Priority),
		otellog.String("msg.type", msg.MsgType),
		otellog.String("status", status),
		errKV(err),
	}
	if os.Getenv("GT_LOG_MAIL_BODY") == "true" {
		kvs = append(kvs, otellog.String("msg.body", truncateOutput(msg.Body, 256)))
	}
	emit(ctx, "mail", severity(err), kvs...)
}

// RecordPrime records a gt prime invocation (metrics + log event).
func RecordPrime(ctx context.Context, role string, hookMode bool, err error) {
	initInstruments()
	status := statusStr(err)
	inst.primeTotal.Add(ctx, 1,
		metric.WithAttributes(
			attribute.String("status", status),
			attribute.String("role", role),
			attribute.Bool("hook_mode", hookMode),
		),
	)
	emit(ctx, "prime", severity(err),
		otellog.String("role", role),
		otellog.Bool("hook_mode", hookMode),
		otellog.String("status", status),
		errKV(err),
	)
}

// RecordPrimeContext logs the formula/context rendered by gt prime.
// Opt-in: set GT_LOG_PRIME_CONTEXT=true to enable. Default off because the
// rendered formula may contain secrets injected by gt prime (API keys, tokens).
// Only emits when telemetry is active and the env var is set.
func RecordPrimeContext(ctx context.Context, formula, role string, hookMode bool) {
	if formula == "" || os.Getenv("GT_LOG_PRIME_CONTEXT") != "true" {
		return
	}
	initInstruments()
	emit(ctx, "prime.context", otellog.SeverityInfo,
		otellog.String("role", role),
		otellog.Bool("hook_mode", hookMode),
		otellog.String("formula", formula),
	)
}

// RecordAgentStateChange records an agent state transition (metrics + log event).
func RecordAgentStateChange(ctx context.Context, agentID, newState string, hookBead *string, err error) {
	initInstruments()
	status := statusStr(err)
	hookBeadID := ""
	if hookBead != nil {
		hookBeadID = *hookBead
	}
	inst.agentStateTotal.Add(ctx, 1,
		metric.WithAttributes(
			attribute.String("status", status),
			attribute.String("new_state", newState),
		),
	)
	emit(ctx, "agent.state_change", severity(err),
		otellog.String("agent_id", agentID),
		otellog.String("new_state", newState),
		otellog.String("hook_bead", hookBeadID),
		otellog.String("status", status),
		errKV(err),
	)
}

// RecordPolecatSpawn records a polecat spawn attempt (metrics + log event).
func RecordPolecatSpawn(ctx context.Context, name string, err error) {
	initInstruments()
	status := statusStr(err)
	inst.polecatTotal.Add(ctx, 1,
		metric.WithAttributes(attribute.String("status", status)),
	)
	emit(ctx, "polecat.spawn", severity(err),
		otellog.String("name", name),
		otellog.String("status", status),
		errKV(err),
	)
}

// RecordPolecatRemove records a polecat removal (metrics + log event).
func RecordPolecatRemove(ctx context.Context, name string, err error) {
	initInstruments()
	status := statusStr(err)
	inst.polecatRemoveTotal.Add(ctx, 1,
		metric.WithAttributes(attribute.String("status", status)),
	)
	emit(ctx, "polecat.remove", severity(err),
		otellog.String("name", name),
		otellog.String("status", status),
		errKV(err),
	)
}

// RecordSling records a sling work dispatch (metrics + log event).
func RecordSling(ctx context.Context, bead, target string, err error) {
	initInstruments()
	status := statusStr(err)
	inst.slingTotal.Add(ctx, 1,
		metric.WithAttributes(attribute.String("status", status)),
	)
	emit(ctx, "sling", severity(err),
		otellog.String("bead", bead),
		otellog.String("target", target),
		otellog.String("status", status),
		errKV(err),
	)
}

// RecordMail records a mail/bd SDK operation (metrics + log event).
func RecordMail(ctx context.Context, operation string, err error) {
	initInstruments()
	status := statusStr(err)
	inst.mailTotal.Add(ctx, 1,
		metric.WithAttributes(
			attribute.String("status", status),
			attribute.String("operation", operation),
		),
	)
	emit(ctx, "mail", severity(err),
		otellog.String("operation", operation),
		otellog.String("status", status),
		errKV(err),
	)
}

// RecordNudge records a gt nudge invocation (metrics + log event).
func RecordNudge(ctx context.Context, target string, err error) {
	initInstruments()
	status := statusStr(err)
	inst.nudgeTotal.Add(ctx, 1,
		metric.WithAttributes(attribute.String("status", status)),
	)
	emit(ctx, "nudge", severity(err),
		otellog.String("target", target),
		otellog.String("status", status),
		errKV(err),
	)
}

// RecordDone records a gt done invocation — polecat work completion (metrics + log event).
// exitType is one of COMPLETED, ESCALATED, DEFERRED.
func RecordDone(ctx context.Context, exitType string, err error) {
	initInstruments()
	status := statusStr(err)
	inst.doneTotal.Add(ctx, 1,
		metric.WithAttributes(
			attribute.String("status", status),
			attribute.String("exit_type", exitType),
		),
	)
	emit(ctx, "done", severity(err),
		otellog.String("exit_type", exitType),
		otellog.String("status", status),
		errKV(err),
	)
}

// RecordDaemonRestart records a daemon-initiated agent session restart (metrics + log event).
// agentType is e.g. "deacon", "witness-myrig", "refinery-myrig".
func RecordDaemonRestart(ctx context.Context, agentType string) {
	initInstruments()
	inst.daemonRestartTotal.Add(ctx, 1,
		metric.WithAttributes(attribute.String("agent_type", agentType)),
	)
	emit(ctx, "daemon.restart", otellog.SeverityInfo,
		otellog.String("agent_type", agentType),
	)
}

// RecordFormulaInstantiate records a formula→wisp instantiation (metrics + log event).
func RecordFormulaInstantiate(ctx context.Context, formulaName, beadID string, err error) {
	initInstruments()
	status := statusStr(err)
	inst.formulaTotal.Add(ctx, 1,
		metric.WithAttributes(
			attribute.String("status", status),
			attribute.String("formula", formulaName),
		),
	)
	emit(ctx, "formula.instantiate", severity(err),
		otellog.String("formula_name", formulaName),
		otellog.String("bead_id", beadID),
		otellog.String("status", status),
		errKV(err),
	)
}

// RecordConvoyCreate records an auto-convoy creation (metrics + log event).
func RecordConvoyCreate(ctx context.Context, beadID string, err error) {
	initInstruments()
	status := statusStr(err)
	inst.convoyTotal.Add(ctx, 1,
		metric.WithAttributes(attribute.String("status", status)),
	)
	emit(ctx, "convoy.create", severity(err),
		otellog.String("bead_id", beadID),
		otellog.String("status", status),
		errKV(err),
	)
}

// RecordAgentTokenUsage emits a token usage event for an assistant turn.
// Called once per assistant message (not per content block) to avoid double-counting.
// inputTokens, outputTokens, cacheReadTokens, cacheCreationTokens map directly to
// the Claude API usage fields: input_tokens, output_tokens,
// cache_read_input_tokens, cache_creation_input_tokens.
func RecordAgentTokenUsage(ctx context.Context, sessionID, nativeSessionID string, inputTokens, outputTokens, cacheReadTokens, cacheCreationTokens int) {
	initInstruments()
	inst.agentEventTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("session", sessionID),
		attribute.String("event_type", "usage"),
		attribute.String("role", "assistant"),
	))
	logger := global.GetLoggerProvider().Logger(loggerName)
	var r otellog.Record
	r.SetBody(otellog.StringValue("agent.usage"))
	r.SetSeverity(otellog.SeverityInfo)
	r.AddAttributes(
		otellog.String("session", sessionID),
		otellog.String("native_session_id", nativeSessionID),
		otellog.Int64("input_tokens", int64(inputTokens)),
		otellog.Int64("output_tokens", int64(outputTokens)),
		otellog.Int64("cache_read_tokens", int64(cacheReadTokens)),
		otellog.Int64("cache_creation_tokens", int64(cacheCreationTokens)),
	)
	addRunID(ctx, &r)
	logger.Emit(ctx, r)
}

// RecordMolCook records a formula cook operation (formula → proto).
func RecordMolCook(ctx context.Context, formulaName string, err error) {
	initInstruments()
	status := statusStr(err)
	inst.molCookTotal.Add(ctx, 1,
		metric.WithAttributes(
			attribute.String("status", status),
			attribute.String("formula", formulaName),
		),
	)
	emit(ctx, "mol.cook", severity(err),
		otellog.String("formula_name", formulaName),
		otellog.String("status", status),
		errKV(err),
	)
}

// RecordMolWisp records a molecule wisp creation (proto → wisp).
// beadID is the base bead the wisp is bonded to; empty for standalone formula slinging.
func RecordMolWisp(ctx context.Context, formulaName, wispRootID, beadID string, err error) {
	initInstruments()
	status := statusStr(err)
	inst.molWispTotal.Add(ctx, 1,
		metric.WithAttributes(
			attribute.String("status", status),
			attribute.String("formula", formulaName),
		),
	)
	emit(ctx, "mol.wisp", severity(err),
		otellog.String("formula_name", formulaName),
		otellog.String("wisp_root_id", wispRootID),
		otellog.String("bead_id", beadID),
		otellog.String("status", status),
		errKV(err),
	)
}

// RecordMolSquash records a molecule squash operation (mol → digest).
// doneSteps/totalSteps describe execution progress; digestCreated indicates
// whether a digest bead was produced (false when --no-digest is set).
func RecordMolSquash(ctx context.Context, molID string, doneSteps, totalSteps int, digestCreated bool, err error) {
	initInstruments()
	status := statusStr(err)
	inst.molSquashTotal.Add(ctx, 1,
		metric.WithAttributes(attribute.String("status", status)),
	)
	emit(ctx, "mol.squash", severity(err),
		otellog.String("mol_id", molID),
		otellog.Int64("done_steps", int64(doneSteps)),
		otellog.Int64("total_steps", int64(totalSteps)),
		otellog.Bool("digest_created", digestCreated),
		otellog.String("status", status),
		errKV(err),
	)
}

// RecordMolBurn records a molecule burn (destroy) operation.
// childrenClosed is the number of descendant step beads closed in the process.
func RecordMolBurn(ctx context.Context, molID string, childrenClosed int, err error) {
	initInstruments()
	status := statusStr(err)
	inst.molBurnTotal.Add(ctx, 1,
		metric.WithAttributes(attribute.String("status", status)),
	)
	emit(ctx, "mol.burn", severity(err),
		otellog.String("mol_id", molID),
		otellog.Int64("children_closed", int64(childrenClosed)),
		otellog.String("status", status),
		errKV(err),
	)
}

// RecordBeadCreate records the creation of a child bead during molecule instantiation.
// parentID is the base/wisp bead the child is attached to;
// molSource is the molecule template (proto) ID that drove the instantiation.
func RecordBeadCreate(ctx context.Context, beadID, parentID, molSource string) {
	initInstruments()
	inst.beadCreateTotal.Add(ctx, 1,
		metric.WithAttributes(attribute.String("mol_source", molSource)),
	)
	emit(ctx, "bead.create", otellog.SeverityInfo,
		otellog.String("bead_id", beadID),
		otellog.String("parent_id", parentID),
		otellog.String("mol_source", molSource),
	)
}

// RecordPaneOutput emits a chunk of raw pane output (ANSI already stripped) to VictoriaLogs.
// Opt-in: only called when GT_LOG_PANE_OUTPUT=true.
// Content is truncated to GT_LOG_PANE_CONTENT_LIMIT bytes (default 8192).
func RecordPaneOutput(ctx context.Context, sessionID, content string) {
	initInstruments()
	initContentLimits()
	inst.paneOutputTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("session", sessionID),
	))
	emit(ctx, "pane.output", otellog.SeverityInfo,
		otellog.String("session", sessionID),
		otellog.String("content", truncateOutput(content, paneContentLim)),
	)
}

// RecordAgentEvent emits a structured agent conversation event.
// Opt-in: requires GT_LOG_AGENT_OUTPUT=true (enforced here so callers can't bypass it).
//
// agentType is the adapter name ("claudecode", "opencode", …).
// eventType is one of "text", "tool_use", "tool_result", "thinking".
// role is "assistant" or "user".
// nativeSessionID is the agent-native session UUID (e.g. Claude Code JSONL filename UUID).
// ts is the original timestamp from the conversation log.
// content is truncated to 512 bytes to limit PII exposure; set
// GT_LOG_AGENT_CONTENT_LIMIT=0 to disable truncation (experts only).
func RecordAgentEvent(ctx context.Context, sessionID, agentType, eventType, role, content, nativeSessionID string, ts time.Time) {
	if os.Getenv("GT_LOG_AGENT_OUTPUT") != "true" {
		return
	}
	initInstruments()
	inst.agentEventTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("session", sessionID),
		attribute.String("event_type", eventType),
		attribute.String("role", role),
	))
	logger := global.GetLoggerProvider().Logger(loggerName)
	var r otellog.Record
	r.SetBody(otellog.StringValue("agent.event"))
	r.SetSeverity(otellog.SeverityInfo)
	if !ts.IsZero() {
		r.SetTimestamp(ts)
	}
	// Truncate content to limit PII/secret exposure in telemetry backends.
	// Limit is cached at first call; default 512 bytes. GT_LOG_AGENT_CONTENT_LIMIT=0 disables.
	initContentLimits()
	r.AddAttributes(
		otellog.String("session", sessionID),
		otellog.String("agent_type", agentType),
		otellog.String("event_type", eventType),
		otellog.String("role", role),
		otellog.String("content", truncateOutput(content, agentContentLim)),
		otellog.String("native_session_id", nativeSessionID),
	)
	addRunID(ctx, &r)
	logger.Emit(ctx, r)
}
