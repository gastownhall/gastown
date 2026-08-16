// Command gt-test-agent is a process that speaks the Worker protocol.
// Tests start this binary. It is not a mock of Worker.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jonbaldie/gastown/internal/worker"
)

func main() {
	var (
		townRoot    = flag.String("town", "", "Town root")
		runID       = flag.String("run-id", "", "Run ID")
		sessionID   = flag.String("session", "", "Session ID")
		beadID      = flag.String("bead", "", "Bead ID (informational)")
		agentType   = flag.String("agent-type", "test", "Agent type")
		state       = flag.String("state", "ready", "Initial lifecycle state")
		ctlDir      = flag.String("ctl-dir", "", "Control directory (default: <town>/.runtime/worker/agents/<session>)")
		httpOnly    = flag.Bool("http", false, "Prefer HTTP localhost")
		exitCode    = flag.Int("exit-code", 0, "Exit code to report on stop")
		noHealth    = flag.Bool("no-health", false, "Do not send health replies")
		healthEvery = flag.Duration("health-every", 200*time.Millisecond, "Health interval")
	)
	flag.Parse()
	if *townRoot == "" || *runID == "" || *sessionID == "" {
		fmt.Fprintln(os.Stderr, "gt-test-agent: --town, --run-id, and --session are required")
		os.Exit(2)
	}
	if *ctlDir == "" {
		*ctlDir = filepath.Join(*townRoot, ".runtime", "worker", "agents", strings.ReplaceAll(*sessionID, "/", "_"))
	}
	if err := os.MkdirAll(*ctlDir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "gt-test-agent: ctl dir: %v\n", err)
		os.Exit(1)
	}

	agent := &testAgent{
		townRoot:    *townRoot,
		runID:       *runID,
		sessionID:   *sessionID,
		beadID:      *beadID,
		agentType:   *agentType,
		state:       *state,
		ctlDir:      *ctlDir,
		exitCode:    *exitCode,
		noHealth:    *noHealth,
		healthEvery: *healthEvery,
		httpOnly:    *httpOnly,
	}
	if err := agent.run(); err != nil {
		fmt.Fprintf(os.Stderr, "gt-test-agent: %v\n", err)
		os.Exit(1)
	}
}

type testAgent struct {
	townRoot    string
	runID       string
	sessionID   string
	beadID      string
	agentType   string
	state       string
	ctlDir      string
	exitCode    int
	noHealth    bool
	healthEvery time.Duration
	httpOnly    bool

	mu         sync.Mutex
	client     *worker.AgentClient
	started    time.Time
	interrupt  bool
	lastPrompt *worker.Prompt
}

func (a *testAgent) run() error {
	a.started = time.Now()
	client, err := worker.DialAgent(a.townRoot)
	if err != nil {
		return err
	}
	a.client = client

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := client.Hello(ctx, a.runID, a.sessionID); err != nil {
		return fmt.Errorf("hello: %w", err)
	}
	initial := a.state
	if err := a.report(ctx, worker.EventStarted); err != nil {
		return err
	}
	if initial != "" && initial != worker.EventStarted {
		if err := a.report(ctx, initial); err != nil {
			return err
		}
	}
	_ = os.WriteFile(filepath.Join(a.ctlDir, "pid"), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600)
	_ = os.WriteFile(filepath.Join(a.ctlDir, "ready"), []byte("1\n"), 0o600)

	go a.watchCtl(ctx)
	if !a.noHealth {
		go a.healthLoop(ctx)
	}
	go a.pollLoop(ctx)

	<-ctx.Done()
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = a.report(stopCtx, worker.EventStopping)
	_ = a.client.ReportLifecycle(stopCtx, worker.Lifecycle{
		Event:     worker.EventStopped,
		RunID:     a.runID,
		SessionID: a.sessionID,
		Timestamp: time.Now().UTC(),
		Metadata:  map[string]any{"exit_code": a.exitCode},
	})
	return nil
}

func (a *testAgent) report(ctx context.Context, event string) error {
	a.mu.Lock()
	a.state = event
	a.mu.Unlock()
	return a.client.ReportLifecycle(ctx, worker.Lifecycle{
		Event:     event,
		RunID:     a.runID,
		SessionID: a.sessionID,
		Timestamp: time.Now().UTC(),
		Metadata:  map[string]any{"bead_id": a.beadID, "agent_type": a.agentType},
	})
}

func (a *testAgent) healthLoop(ctx context.Context) {
	t := time.NewTicker(a.healthEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.mu.Lock()
			st := a.state
			a.mu.Unlock()
			_ = a.client.ReportHealth(ctx, worker.Health{
				Status:       worker.HealthHealthy,
				RunID:        a.runID,
				UptimeSecs:   int64(time.Since(a.started).Seconds()),
				CurrentState: st,
				LastActivity: time.Now().UTC(),
				ContextUse:   0.1,
			})
		}
	}
}

func (a *testAgent) pollLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		pollCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		env, err := a.client.Poll(pollCtx, a.runID)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		a.handleEnvelope(ctx, env)
	}
}

func (a *testAgent) handleEnvelope(ctx context.Context, env worker.Envelope) {
	switch env.Kind {
	case worker.KindPrompt:
		var payload struct {
			Content   string `json:"content"`
			Priority  string `json:"priority"`
			Source    string `json:"source"`
			From      string `json:"from"`
			BeadID    string `json:"bead_id"`
			Interrupt bool   `json:"interrupt"`
		}
		_ = json.Unmarshal(env.Payload, &payload)
		a.mu.Lock()
		if payload.Interrupt {
			a.interrupt = true
			_ = os.WriteFile(filepath.Join(a.ctlDir, "interrupted"), []byte("1\n"), 0o600)
		}
		p := worker.Prompt{
			RunID:    a.runID,
			Content:  payload.Content,
			Priority: payload.Priority,
			Source:   payload.Source,
			From:     payload.From,
			BeadID:   payload.BeadID,
		}
		a.lastPrompt = &p
		a.mu.Unlock()
		appendJSONL(filepath.Join(a.ctlDir, "prompts.jsonl"), p)
		_ = a.client.AckPrompt(ctx, worker.Delivery{
			Accepted: true,
			Queued:   false,
			RunID:    a.runID,
		})
		if a.currentState() != worker.EventBusy {
			_ = a.report(ctx, worker.EventBusy)
			_ = a.report(ctx, worker.EventIdle)
		}
	case worker.KindIdentity, worker.KindContext:
		appendJSONL(filepath.Join(a.ctlDir, env.Kind+".jsonl"), env)
	}
}

func (a *testAgent) currentState() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state
}

func (a *testAgent) watchCtl(ctx context.Context) {
	inbox := filepath.Join(a.ctlDir, "inbox")
	seen := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(20 * time.Millisecond):
		}
		data, err := os.ReadFile(inbox) //nolint:gosec // ctl inbox is a local test-agent path
		if err != nil {
			continue
		}
		cmd := strings.TrimSpace(string(data))
		if cmd == "" || cmd == seen {
			continue
		}
		seen = cmd
		a.applyCtl(ctx, cmd)
	}
}

func (a *testAgent) applyCtl(ctx context.Context, cmd string) {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return
	}
	switch fields[0] {
	case worker.EventReady, worker.EventBusy, worker.EventIdle, worker.EventStopping:
		_ = a.report(ctx, fields[0])
	case worker.EventStopped:
		code := a.exitCode
		if len(fields) > 1 {
			if n, err := strconv.Atoi(fields[1]); err == nil {
				code = n
			}
		}
		done := false
		if len(fields) > 2 && fields[2] == "done" {
			done = true
		}
		_ = a.report(ctx, worker.EventStopping)
		_ = a.client.ReportLifecycle(ctx, worker.Lifecycle{
			Event:     worker.EventStopped,
			RunID:     a.runID,
			SessionID: a.sessionID,
			Timestamp: time.Now().UTC(),
			Metadata:  map[string]any{"exit_code": code, "done": done},
		})
		os.Exit(code)
	case "telemetry":
		cost := 0.25
		if len(fields) > 1 {
			if v, err := strconv.ParseFloat(fields[1], 64); err == nil {
				cost = v
			}
		}
		err := a.client.ReportTelemetry(ctx, worker.TelemetryBatch{
			RunID: a.runID,
			Events: []worker.TelemetryEvent{{
				Type:      "turn_complete",
				Timestamp: time.Now().UTC(),
				Usage: &worker.Usage{
					InputTokens:  1200,
					OutputTokens: 400,
					Model:        a.agentType + "-test",
					CostUSD:      cost,
				},
			}},
		})
		if err != nil {
			_ = os.WriteFile(filepath.Join(a.ctlDir, "telemetry.err"), []byte(err.Error()+"\n"), 0o600)
		} else {
			_ = os.WriteFile(filepath.Join(a.ctlDir, "telemetry"), []byte("1\n"), 0o600)
		}
	case "authorize":
		tool := "Bash"
		command := "git push --force"
		if len(fields) > 1 {
			command = strings.Join(fields[1:], " ")
		}
		dec := a.client.AskAuthorize(ctx, worker.AuthorizeRequest{
			RunID: a.runID,
			Tool:  tool,
			Input: map[string]any{"command": command},
			Context: map[string]any{
				"role":    "polecat",
				"bead_id": a.beadID,
			},
		})
		appendJSONL(filepath.Join(a.ctlDir, "authorize.jsonl"), dec)
	}
}

func appendJSONL(path string, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // ctl jsonl is a local test-agent path
	if err != nil {
		return
	}
	_, _ = f.Write(append(data, '\n'))
	_ = f.Close()
}
