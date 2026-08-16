package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jonbaldie/gastown/internal/style"
)

type handshakeState int

const (
	handshakeInit handshakeState = iota
	handshakeWaitingForInit
	handshakeWaitingForSessionNew
	handshakeComplete
)

const (
	startupPromptStateIdle      = ""
	startupPromptStatePending   = "pending"
	startupPromptStateInjecting = "injecting"
	startupPromptStateComplete  = "complete"
	startupPromptStateFailed    = "failed"
)

// startupPromptTimeout is the maximum time to wait for the agent to respond
// to the startup prompt. If the agent doesn't respond within this time,
// the startup prompt is marked as failed and the proxy continues.
const startupPromptTimeout = 60 * time.Second

type Proxy struct {
	cmd                *exec.Cmd
	agentStdin         io.WriteCloser
	agentStdout        io.ReadCloser
	agentStderr        io.ReadCloser
	stdin              io.Reader
	stdout             io.Writer
	sessionID          string
	sessionMux         sync.RWMutex
	done               chan struct{}
	doneOnce           sync.Once
	ctx                context.Context
	cancel             context.CancelFunc
	wg                 sync.WaitGroup
	handshakeState     handshakeState
	handshakeMux       sync.Mutex
	promptMux          sync.Mutex
	activePromptID     string
	stdinMux           sync.Mutex
	stdoutMux          sync.Mutex
	uiEncoder          *json.Encoder
	startupPrompt      string
	startupPromptState string
	startupPromptMux   sync.RWMutex
	shutdownOnce       sync.Once
	isShuttingDown     atomic.Bool
	lastActivity       atomic.Int64
	pidFilePath        string
	townRoot           string
	// Heartbeat support
	currentModeID      string
	modeMux            sync.RWMutex
	heartbeatMethod    string // "custom_ping", "set_mode", or "disabled"
	heartbeatSupported atomic.Bool
	// Propulsion state
	Propelled        atomic.Bool
	propulsionBuffer string
	// Stderr monitoring for pipe saturation
	stderrBytesDropped   atomic.Int64
	stderrLinesTruncated atomic.Int64
	stderrLastLogTime    atomic.Int64
}

// SetTownRoot sets the town root for logging important events to town.log.
func (p *Proxy) SetTownRoot(townRoot string) {
	p.townRoot = townRoot
}

func (p *Proxy) SetPropelled(propelled bool) {
	p.Propelled.Store(propelled)
}

type JSONRPCMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// SessionMode represents an available agent mode
type SessionMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// SessionModeState represents the current mode state
type SessionModeState struct {
	CurrentModeID  string        `json:"currentModeId"`
	AvailableModes []SessionMode `json:"availableModes"`
}

// SessionNewResult represents the result of session/new
type SessionNewResult struct {
	SessionID string            `json:"sessionId"`
	Modes     *SessionModeState `json:"modes,omitempty"`
}

func NewProxy() *Proxy {
	debugLog("", "[Proxy] Created new proxy, initial handshakeState=%d", handshakeInit)
	p := &Proxy{
		done:           make(chan struct{}),
		handshakeState: handshakeInit,
		stdin:          os.Stdin,
		stdout:         os.Stdout,
	}
	p.uiEncoder = json.NewEncoder(p.stdout)
	p.lastActivity.Store(time.Now().UnixNano())
	return p
}

// setStreams sets the standard streams for the proxy.
func (p *Proxy) setStreams(in io.Reader, out io.Writer) {
	p.stdin = in
	p.stdout = out
	p.uiEncoder = json.NewEncoder(out)
}

// SetPIDFilePath sets the path to the PID file for monitoring.
func (p *Proxy) SetPIDFilePath(path string) {
	p.pidFilePath = path
}

func (p *Proxy) SetStartupPrompt(prompt string) {
	p.startupPromptMux.Lock()
	p.startupPrompt = prompt
	if prompt == "" {
		p.startupPromptState = startupPromptStateIdle
	} else {
		p.startupPromptState = startupPromptStatePending
	}
	p.startupPromptMux.Unlock()
}

func (p *Proxy) getStartupPrompt() string {
	p.startupPromptMux.RLock()
	defer p.startupPromptMux.RUnlock()
	return p.startupPrompt
}

func (p *Proxy) setStartupPromptState(state string) {
	p.startupPromptMux.Lock()
	p.startupPromptState = state
	p.startupPromptMux.Unlock()
}

func (p *Proxy) getStartupPromptState() string {
	p.startupPromptMux.RLock()
	defer p.startupPromptMux.RUnlock()
	return p.startupPromptState
}

func (p *Proxy) Start(ctx context.Context, agentPath string, agentArgs []string, cwd string) error {
	childCtx, cancel := context.WithCancel(ctx)
	p.ctx = childCtx
	p.cancel = cancel

	p.cmd = exec.CommandContext(childCtx, agentPath, agentArgs...)
	p.cmd.Dir = cwd

	// Platform-specific process group setup
	p.setupProcessGroup()

	var err error
	p.agentStdin, err = p.cmd.StdinPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("creating stdin pipe: %w", err)
	}

	p.agentStdout, err = p.cmd.StdoutPipe()
	if err != nil {
		cancel()
		p.stdinMux.Lock()
		if p.agentStdin != nil {
			_ = p.agentStdin.Close()
			p.agentStdin = nil
		}
		p.stdinMux.Unlock()
		_ = p.cmd.Wait()
		return fmt.Errorf("creating stdout pipe: %w", err)
	}

	// Capture agent stderr for debugging when GT_ACP_DEBUG=1
	p.agentStderr, err = p.cmd.StderrPipe()
	if err != nil {
		cancel()
		p.stdinMux.Lock()
		if p.agentStdin != nil {
			_ = p.agentStdin.Close()
			p.agentStdin = nil
		}
		p.stdinMux.Unlock()
		_ = p.agentStdout.Close()
		return fmt.Errorf("creating stderr pipe: %w", err)
	}

	if err := p.cmd.Start(); err != nil {
		cancel()
		p.stdinMux.Lock()
		if p.agentStdin != nil {
			_ = p.agentStdin.Close()
			p.agentStdin = nil
		}
		p.stdinMux.Unlock()
		return fmt.Errorf("starting agent: %w", err)
	}

	// Start goroutine to capture agent stderr and write to acp.log
	p.wg.Add(1)
	go p.forwardAgentStderr()

	return nil
}

func (p *Proxy) writeToAgent(msg any) error {
	method := "unknown"
	var id any
	if m, ok := msg.(*JSONRPCMessage); ok {
		method = m.Method
		id = m.ID
	}

	if p.isShuttingDown.Load() {
		debugLog(p.townRoot, "[Proxy] writeToAgent: dropped write during shutdown (method=%s id=%v)", method, id)
		return fmt.Errorf("proxy is shutting down")
	}

	p.stdinMux.Lock()
	defer p.stdinMux.Unlock()

	if p.isShuttingDown.Load() {
		return fmt.Errorf("proxy is shutting down")
	}

	if p.agentStdin == nil {
		return fmt.Errorf("agent stdin is nil")
	}

	if !p.isProcessAlive() {
		debugLog(p.townRoot, "[Proxy] writeToAgent: failed (process dead) (method=%s id=%v)", method, id)
		return fmt.Errorf("agent process is not running")
	}

	isPrompt := false
	if m, ok := msg.(*JSONRPCMessage); ok && m.Method == "session/prompt" && m.ID != nil {
		isPrompt = true
		p.promptMux.Lock()
		if idStr, ok := m.ID.(string); ok {
			p.activePromptID = idStr
		} else {
			p.activePromptID = fmt.Sprintf("%v", m.ID)
		}
		debugLog(p.townRoot, "[Proxy] writeToAgent: marking busy (id=%s)", p.activePromptID)
		p.promptMux.Unlock()
	}

	p.lastActivity.Store(time.Now().UnixNano())
	debugLog(p.townRoot, "[Proxy] writeToAgent: encoding message (method=%s id=%v)", method, id)

	err := json.NewEncoder(p.agentStdin).Encode(msg)
	if err != nil {
		debugLog(p.townRoot, "[Proxy] writeToAgent: encode failed: %v", err)
		if isPrompt {
			p.promptMux.Lock()
			p.activePromptID = ""
			p.promptMux.Unlock()
		}
		return fmt.Errorf("writing to agent: %w", err)
	}

	return nil
}

func (p *Proxy) Forward() error {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, signalsToHandle()...)
	defer signal.Stop(sigChan)

	defer p.Shutdown()

	errChan := make(chan error, 1)
	p.wg.Add(3)
	go p.forwardToAgent()
	go p.forwardFromAgent()

	keepAliveTicker := time.NewTicker(30 * time.Second)
	defer keepAliveTicker.Stop()
	go p.runKeepAlive(keepAliveTicker.C)

	if p.pidFilePath != "" {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.monitorPIDFile(p.ctx)
		}()
	}

	go func() {
		errChan <- p.cmd.Wait()
	}()

	var exitErr error
	select {
	case <-sigChan:
		debugLog(p.townRoot, "[Proxy] Forward: received signal")
	case <-p.done:
		debugLog(p.townRoot, "[Proxy] Forward: done channel signaled")
	case err := <-errChan:
		exitErr = err
		debugLog(p.townRoot, "[Proxy] Forward: agent process exited: %v", err)
	}

	p.Shutdown()

	doneChan := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(doneChan)
	}()

	select {
	case <-doneChan:
		debugLog(p.townRoot, "[Proxy] Forward: all goroutines exited")
	case <-time.After(200 * time.Millisecond):
		debugLog(p.townRoot, "[Proxy] Forward: wait timeout, proceeding with exit")
	}

	if exitErr != nil {
		logEvent(p.townRoot, "acp_error", fmt.Sprintf("agent exited with error: %v", exitErr))
		debugLog(p.townRoot, "[Proxy] Agent exited with error: %v", exitErr)
		return exitErr
	}
	return nil
}

func (p *Proxy) forwardToAgent() {
	defer p.wg.Done()
	defer func() {
		debugLog(p.townRoot, "[Proxy] forwardToAgent: exiting, triggering Shutdown()")
		p.Shutdown()
	}()

	// Use large buffer to handle large JSON messages from the UI
	reader := bufio.NewReaderSize(p.stdin, 1024*1024)
	receivedInput := false

	for {
		select {
		case <-p.done:
			debugLog(p.townRoot, "[Proxy] forwardToAgent: done channel closed, exiting")
			return
		default:
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				if !receivedInput && p.handshakeState == handshakeInit {
					logEvent(p.townRoot, "acp_error", "stdin closed before handshake - no ACP client connected")
					debugLog(p.townRoot, "[Proxy] stdin closed before handshake - no ACP client connected?")
				} else {
					logEvent(p.townRoot, "acp_shutdown", "stdin EOF - ACP client disconnected")
					debugLog(p.townRoot, "[Proxy] forwardToAgent: stdin EOF (client disconnected)")
				}
			} else {
				debugLog(p.townRoot, "[Proxy] forwardToAgent: stdin read error: %v", err)
				p.markDone()
			}
			return
		}

		receivedInput = true

		var msg JSONRPCMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}

		// Log large messages that might cause issues
		if len(line) > 50000 {
			debugLog(p.townRoot, "[Proxy] forwardToAgent: large message received (size=%d, method=%s)", len(line), msg.Method)
		}

		p.trackHandshakeRequest(&msg)

		if err := p.writeToAgent(&msg); err != nil {
			debugLog(p.townRoot, "[Proxy] forwardToAgent: writeToAgent failed: %v", err)
			p.markDone()
			return
		}
	}
}

func (p *Proxy) trackHandshakeRequest(msg *JSONRPCMessage) {
	if msg.Method == "" {
		return
	}

	p.handshakeMux.Lock()
	defer p.handshakeMux.Unlock()

	if msg.Method == "initialize" && p.handshakeState == handshakeInit {
		debugLog(p.townRoot, "[Proxy] Handshake: initialize request received from UI")
		p.handshakeState = handshakeWaitingForInit
	}
}

func (p *Proxy) forwardFromAgent() {
	defer p.wg.Done()

	// Use large buffer to handle bursts of large JSON messages (e.g. build logs)
	reader := bufio.NewReaderSize(p.agentStdout, 1024*1024)

	for {
		select {
		case <-p.done:
			debugLog(p.townRoot, "[Proxy] forwardFromAgent: done channel closed, exiting")
			return
		default:
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				debugLog(p.townRoot, "[Proxy] forwardFromAgent: agent stdout EOF (agent terminated)")
				p.logCrashDiagnostics("agent stdout EOF")
				logEvent(p.townRoot, "acp_shutdown", "agent stdout EOF - agent terminated gracefully")
				p.markDone()
			} else {
				logEvent(p.townRoot, "acp_error", fmt.Sprintf("agent stdout read error: %v", err))
				debugLog(p.townRoot, "[Proxy] forwardFromAgent: agent stdout read error: %v", err)
				p.logCrashDiagnostics(fmt.Sprintf("read error: %v", err))
				p.markDone()
			}
			return
		}

		var msg JSONRPCMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			// Check for raw propulsion triggers if not valid JSON
			p.propulsionBuffer += line
			if len(p.propulsionBuffer) > 2000 {
				p.propulsionBuffer = p.propulsionBuffer[len(p.propulsionBuffer)-2000:]
			}

			if isPropulsionTrigger(p.propulsionBuffer) {
				debugLog(p.townRoot, "[Proxy] forwardFromAgent: propulsion trigger detected in raw output")
				p.SetPropelled(true)
				p.propulsionBuffer = "" // Reset after detection
			}
			debugLog(p.townRoot, "[Proxy] forwardFromAgent: failed to parse JSON (size=%d): %v", len(line), err)
			continue
		}

		// Log large messages that might cause issues
		if len(line) > 50000 {
			debugLog(p.townRoot, "[Proxy] forwardFromAgent: large message received (size=%d, method=%s)", len(line), msg.Method)
		}

		p.lastActivity.Store(time.Now().UnixNano())
		p.extractSessionID(&msg)
		shouldInjectPrompt := p.trackHandshakeResponse(&msg)
		p.trackPromptResponse(&msg)

		// Check for propulsion triggers in JSON messages (e.g. session/update)
		if checkPropulsionTrigger(&msg) {
			debugLog(p.townRoot, "[Proxy] forwardFromAgent: propulsion trigger detected in JSON message")
			p.SetPropelled(true)
		}

		// Filter out responses to injected prompts so the UI doesn't get confused
		isInjectedResponse := false
		idStr := ""
		if id, ok := msg.ID.(string); ok && strings.HasPrefix(id, "gt-inject-") {
			isInjectedResponse = true
			idStr = id
		}

		if isInjectedResponse && msg.Error != nil {
			debugLog(p.townRoot, "[Proxy] Injected prompt %v failed: %d %s", msg.ID, msg.Error.Code, msg.Error.Message)

			// If heartbeat method fails, disable heartbeat to avoid repeated failures
			if strings.Contains(idStr, "keepalive") {
				debugLog(p.townRoot, "[Proxy] Heartbeat method failed, disabling heartbeat")
				p.heartbeatSupported.Store(false)
			}
		}

		// Log successful heartbeat responses at debug level
		if isInjectedResponse && msg.Error == nil {
			debugLog(p.townRoot, "[Proxy] Heartbeat successful (id=%v)", msg.ID)
		}

		// Filter out redacted thought chunks - they shouldn't be shown to the UI
		// as they create a confusing "Thinking" state when the agent has finished
		if isRedactedThought(&msg) {
			debugLog(p.townRoot, "[Proxy] forwardFromAgent: filtering out redacted thought chunk")
			continue
		}

		if !isInjectedResponse && !p.Propelled.Load() {
			p.stdoutMux.Lock()
			err = p.uiEncoder.Encode(&msg)
			p.stdoutMux.Unlock()
		}
		if err != nil {
			logEvent(p.townRoot, "acp_error", fmt.Sprintf("failed to forward message to UI: %v", err))
			debugLog(p.townRoot, "[Proxy] forwardFromAgent: failed to forward to UI: %v", err)
			p.markDone()
			return
		}

		if shouldInjectPrompt {
			if err := p.injectStartupPrompt(); err != nil {
				style.PrintWarning("failed to inject startup prompt: %v", err)
			}
		}
	}
}

func (p *Proxy) forwardAgentStderr() {
	defer p.wg.Done()
	reader := bufio.NewReader(p.agentStderr)

	// Log stderr statistics periodically to detect pipe saturation
	statsTicker := time.NewTicker(30 * time.Second)
	defer statsTicker.Stop()

	for {
		select {
		case <-p.done:
			// Log final statistics on exit
			dropped := p.stderrBytesDropped.Load()
			truncated := p.stderrLinesTruncated.Load()
			if dropped > 0 || truncated > 0 {
				debugLog(p.townRoot, "[Proxy] Stderr statistics: %d lines truncated, %d bytes dropped", truncated, dropped)
			}
			return
		case <-statsTicker.C:
			// Log statistics periodically if there's activity
			dropped := p.stderrBytesDropped.Load()
			truncated := p.stderrLinesTruncated.Load()
			if dropped > 0 || truncated > 0 {
				debugLog(p.townRoot, "[Proxy] Stderr statistics: %d lines truncated, %d bytes dropped", truncated, dropped)
			}
		default:
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				debugLog(p.townRoot, "[Agent stderr] read error: %v", err)
			}
			return
		}

		line = strings.TrimSuffix(line, "\n")
		if line == "" {
			continue
		}

		lineLen := len(line)

		// DROP very large lines entirely (likely permission ruleset dumps)
		// These can be 50KB+ and serve no debugging purpose
		if lineLen > 50000 {
			p.stderrBytesDropped.Add(int64(lineLen))
			p.stderrLinesTruncated.Add(1)

			// Only log the first few drops to avoid cascading saturation
			if p.stderrLinesTruncated.Load() <= 3 {
				debugLog(p.townRoot, "[Proxy] Dropping massive stderr line (%d bytes) to prevent pipe saturation", lineLen)
			}
			continue
		}

		// Truncate large lines to prevent pipe saturation
		// Keep more context than debug logs (5000 vs 2000 chars)
		outputLine := line
		if lineLen > 5000 {
			outputLine = line[:5000] + fmt.Sprintf("... (truncated from %d bytes)", lineLen)
			p.stderrLinesTruncated.Add(1)
		}

		// ALWAYS use truncated/tracked version to prevent pipe saturation
		fmt.Fprintln(os.Stderr, outputLine)

		// For debug log, use more aggressive truncation
		debugLine := line
		if lineLen > 2000 {
			debugLine = line[:2000] + "... (truncated)"
		}
		debugLog(p.townRoot, "[Agent] %s", debugLine)
	}
}

func (p *Proxy) runKeepAlive(tickerChan <-chan time.Time) {
	defer p.wg.Done()
	debugLog(p.townRoot, "[Proxy] runKeepAlive: loop started")

	for {
		select {
		case <-p.done:
			debugLog(p.townRoot, "[Proxy] runKeepAlive: done channel closed, exiting loop")
			return
		case <-tickerChan:
			if p.isShuttingDown.Load() {
				debugLog(p.townRoot, "[Proxy] runKeepAlive: skipping (shutting down)")
				return
			}

			if p.Propelled.Load() {
				debugLog(p.townRoot, "[Proxy] runKeepAlive: skipping heartbeat (propelled=true)")
				continue
			}

			// Don't send heartbeat if we're currently in a turn
			p.promptMux.Lock()
			busyID := p.activePromptID
			p.promptMux.Unlock()

			last := p.lastActivity.Load()
			idleTime := time.Since(time.Unix(0, last))

			if busyID != "" {
				// FORCE RECOVERY: If busy but no activity for 60s, clear state and heartbeat
				if idleTime > 60*time.Second {
					debugLog(p.townRoot, "[Proxy] runKeepAlive: busy state stuck (id=%s) for %v, forcing recovery", busyID, idleTime)
					p.promptMux.Lock()
					p.activePromptID = ""
					p.promptMux.Unlock()
				} else {
					debugLog(p.townRoot, "[Proxy] runKeepAlive: skipping heartbeat, agent is busy (id=%s)", busyID)
					continue
				}
			}

			// If idle for more than 45 seconds, send a heartbeat
			if idleTime > 45*time.Second {
				p.sessionMux.RLock()
				sid := p.sessionID
				p.sessionMux.RUnlock()

				if sid == "" {
					debugLog(p.townRoot, "[Proxy] runKeepAlive: skipping heartbeat, no sessionID available")
					continue
				}

				// Check if heartbeat is supported and which method to use
				if !p.heartbeatSupported.Load() {
					debugLog(p.townRoot, "[Proxy] runKeepAlive: heartbeat not supported by agent, skipping")
					continue
				}

				p.modeMux.RLock()
				method := p.heartbeatMethod
				currentMode := p.currentModeID
				p.modeMux.RUnlock()

				id := fmt.Sprintf("gt-inject-keepalive-%d", time.Now().UnixNano())

				var msg *JSONRPCMessage

				// Try session/set_mode with current mode (no-op that resets timer)
				if method == "set_mode" && currentMode != "" {
					params := map[string]any{
						"sessionId": sid,
						"modeId":    currentMode, // Set to current mode = no-op
					}
					paramsBytes, _ := json.Marshal(params)

					msg = &JSONRPCMessage{
						JSONRPC: "2.0",
						Method:  "session/set_mode",
						ID:      id,
						Params:  paramsBytes,
					}
					debugLog(p.townRoot, "[Proxy] runKeepAlive: sending heartbeat (session/set_mode mode=%s, idle=%v)", currentMode, idleTime)
				} else {
					// Fallback: try custom _ping method (ACP allows custom methods prefixed with _)
					msg = &JSONRPCMessage{
						JSONRPC: "2.0",
						Method:  "_ping",
						ID:      id,
						Params:  json.RawMessage("{}"),
					}
					debugLog(p.townRoot, "[Proxy] runKeepAlive: sending heartbeat (_ping, idle=%v)", idleTime)
				}

				if err := p.writeToAgent(msg); err != nil {
					debugLog(p.townRoot, "[Proxy] runKeepAlive: heartbeat failed: %v", err)
				}
			} else {
				debugLog(p.townRoot, "[Proxy] runKeepAlive: skipping heartbeat, idle time (%v) < threshold (45s)", idleTime)
			}
		}
	}
}

func (p *Proxy) trackPromptResponse(msg *JSONRPCMessage) {
	if msg.ID == nil {
		return
	}

	p.promptMux.Lock()
	defer p.promptMux.Unlock()

	if p.activePromptID == "" {
		return
	}

	var idStr string
	if s, ok := msg.ID.(string); ok {
		idStr = s
	} else {
		idStr = fmt.Sprintf("%v", msg.ID)
	}

	if idStr == p.activePromptID {
		debugLog(p.townRoot, "[Proxy] trackPromptResponse: prompt completed (id=%s)", idStr)
		p.activePromptID = ""

		// Reset propulsion mode when a prompt completes (Turn ends)
		if p.Propelled.Load() {
			debugLog(p.townRoot, "[Proxy] trackPromptResponse: resetting Propelled flag and buffer")
			p.SetPropelled(false)
			p.propulsionBuffer = ""
		}

		if idStr == "gastown-startup-prompt" {
			p.setStartupPromptState(startupPromptStateComplete)
		}
	}
}

func (p *Proxy) trackHandshakeResponse(msg *JSONRPCMessage) bool {
	if msg.ID == nil || msg.Result == nil {
		return false
	}

	p.handshakeMux.Lock()
	defer p.handshakeMux.Unlock()

	if p.handshakeState == handshakeWaitingForInit {
		p.handshakeState = handshakeWaitingForSessionNew
		return false
	}

	if p.handshakeState == handshakeWaitingForSessionNew && p.sessionID != "" {
		p.handshakeState = handshakeComplete
		return p.getStartupPrompt() != ""
	}

	return false
}

func (p *Proxy) injectStartupPrompt() error {
	prompt := p.getStartupPrompt()
	if prompt == "" {
		p.setStartupPromptState(startupPromptStateIdle)
		return nil
	}

	p.setStartupPromptState(startupPromptStateInjecting)

	p.sessionMux.RLock()
	sessionID := p.sessionID
	p.sessionMux.RUnlock()

	params := map[string]any{
		"sessionId": sessionID,
		"prompt": []map[string]string{
			{"type": "text", "text": prompt},
		},
	}
	paramsBytes, _ := json.Marshal(params)

	req := JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      "gastown-startup-prompt",
		Method:  "session/prompt",
		Params:  paramsBytes,
	}

	if err := p.writeToAgent(&req); err != nil {
		p.setStartupPromptState(startupPromptStateFailed)
		return fmt.Errorf("sending startup prompt: %w", err)
	}

	// We no longer block here. The response will be handled by forwardFromAgent
	// and trackPromptResponse will update the startupPromptState to complete
	// when the response is received.
	return nil
}

func (p *Proxy) extractSessionID(msg *JSONRPCMessage) {
	if msg.ID != nil && msg.Result != nil {
		var result SessionNewResult
		if err := json.Unmarshal(msg.Result, &result); err == nil && result.SessionID != "" {
			p.sessionMux.Lock()
			p.sessionID = result.SessionID
			p.sessionMux.Unlock()
			debugLog(p.townRoot, "[Proxy] extractSessionID: extracted sessionID=%s", result.SessionID)

			// Extract mode information for heartbeat support
			if result.Modes != nil && result.Modes.CurrentModeID != "" {
				p.modeMux.Lock()
				p.currentModeID = result.Modes.CurrentModeID
				p.modeMux.Unlock()
				debugLog(p.townRoot, "[Proxy] extractSessionID: extracted currentModeId=%s", result.Modes.CurrentModeID)

				// Enable heartbeat using session/set_mode with current mode
				if p.heartbeatMethod == "" {
					p.heartbeatMethod = "set_mode"
					p.heartbeatSupported.Store(true)
					debugLog(p.townRoot, "[Proxy] extractSessionID: enabling heartbeat via session/set_mode")
				}
			}
		}
	}
}

func (p *Proxy) InjectNotificationToUI(method string, params any) error {
	if p.isShuttingDown.Load() {
		return fmt.Errorf("proxy is shutting down")
	}

	p.sessionMux.RLock()
	sessionID := p.sessionID
	p.sessionMux.RUnlock()

	if method == "session/update" && sessionID == "" {
		return fmt.Errorf("cannot inject session/update: empty sessionID")
	}

	msg := JSONRPCMessage{
		JSONRPC: "2.0",
		Method:  method,
	}

	if sessionID != "" || params != nil {
		paramMap := make(map[string]any)
		if sessionID != "" {
			paramMap["sessionId"] = sessionID
		}
		if params != nil {
			if v, ok := params.(map[string]any); ok {
				for k, val := range v {
					paramMap[k] = val
				}
			} else {
				paramMap["params"] = params
			}
		}
		rawParams, _ := json.Marshal(paramMap)
		msg.Params = rawParams
	}

	debugLog(p.townRoot, "[Proxy] Injecting notification to UI: method=%s sessionId=%s", method, sessionID)
	p.stdoutMux.Lock()
	err := p.uiEncoder.Encode(&msg)
	p.stdoutMux.Unlock()
	return err
}

func (p *Proxy) InjectPrompt(prompt string) error {
	if p.isShuttingDown.Load() {
		return fmt.Errorf("proxy is shutting down")
	}

	p.sessionMux.RLock()
	sessionID := p.sessionID
	p.sessionMux.RUnlock()

	if sessionID == "" {
		return fmt.Errorf("cannot inject prompt: empty sessionID")
	}

	// Check if agent is busy to prevent race conditions.
	// If startup prompt is still in-flight, wait briefly for readiness.
	if p.IsBusy() {
		state := p.getStartupPromptState()
		if state == startupPromptStatePending || state == startupPromptStateInjecting {
			waitCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := p.WaitForReady(waitCtx); err != nil {
				debugLog(p.townRoot, "[Proxy] InjectPrompt: agent still busy after waiting for startup readiness: %v", err)
				return fmt.Errorf("agent is busy processing another prompt")
			}
		} else {
			debugLog(p.townRoot, "[Proxy] InjectPrompt: agent is busy, skipping injection to prevent race condition")
			return fmt.Errorf("agent is busy processing another prompt")
		}
	}

	params := map[string]any{
		"sessionId": sessionID,
		"prompt": []map[string]string{
			{"type": "text", "text": prompt},
		},
	}
	paramsBytes, _ := json.Marshal(params)

	req := JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      fmt.Sprintf("gt-inject-prompt-%d", time.Now().UnixNano()),
		Method:  "session/prompt",
		Params:  paramsBytes,
	}

	logEvent(p.townRoot, "acp_prompt", fmt.Sprintf("injecting prompt: %s", truncateStr(prompt, 100)))
	debugLog(p.townRoot, "[Proxy] Injecting prompt to agent: sessionId=%s text=%q", sessionID, truncateStr(prompt, 50))
	return p.writeToAgent(&req)
}

func (p *Proxy) SessionID() string {
	p.sessionMux.RLock()
	defer p.sessionMux.RUnlock()
	return p.sessionID
}

func (p *Proxy) WaitForSessionID(ctx context.Context) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		p.sessionMux.RLock()
		sid := p.sessionID
		p.sessionMux.RUnlock()

		if sid != "" {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.done:
			return fmt.Errorf("proxy shutting down")
		case <-ticker.C:
		}
	}
}

func (p *Proxy) WaitForReady(ctx context.Context) error {
	if err := p.WaitForSessionID(ctx); err != nil {
		return err
	}

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		if p.isShuttingDown.Load() {
			return fmt.Errorf("proxy is shutting down")
		}

		p.promptMux.Lock()
		busy := p.activePromptID != ""
		p.promptMux.Unlock()

		state := p.getStartupPromptState()
		if !busy && (state == startupPromptStateIdle || state == startupPromptStateComplete || state == startupPromptStateFailed) {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.done:
			return fmt.Errorf("proxy shutting down")
		case <-ticker.C:
		}
	}
}

func (p *Proxy) IsBusy() bool {
	p.promptMux.Lock()
	defer p.promptMux.Unlock()
	return p.activePromptID != ""
}

func (p *Proxy) SendCancelNotification() error {
	p.sessionMux.RLock()
	sessionID := p.sessionID
	p.sessionMux.RUnlock()

	if sessionID == "" {
		return nil
	}

	debugLog(p.townRoot, "[Proxy] Sending session/cancel notification for session %s", sessionID)
	params := map[string]any{"sessionId": sessionID}
	paramsBytes, _ := json.Marshal(params)

	notification := JSONRPCMessage{
		JSONRPC: "2.0",
		Method:  "session/cancel",
		Params:  paramsBytes,
	}

	return p.writeToAgent(&notification)
}

func (p *Proxy) monitorPIDFile(ctx context.Context) {
	if p.pidFilePath == "" {
		return
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-p.done:
			return
		case <-ticker.C:
			if _, err := os.Stat(p.pidFilePath); os.IsNotExist(err) {
				logEvent(p.townRoot, "acp_shutdown", "PID file removed, initiating graceful shutdown")
				debugLog(p.townRoot, "[Proxy] PID file removed, initiating graceful shutdown")
				_ = p.SendCancelNotification()
				p.Shutdown()
				return
			}
		}
	}
}

func (p *Proxy) Shutdown() {
	p.shutdownOnce.Do(func() {
		debugLog(p.townRoot, "[Proxy] Shutdown: initiating graceful shutdown")
		p.isShuttingDown.Store(true)
		p.markDone()

		if p.cancel != nil {
			p.cancel()
		}

		p.stdinMux.Lock()
		if p.agentStdin != nil {
			_ = p.agentStdin.Close()
			p.agentStdin = nil
		}
		p.stdinMux.Unlock()

		if p.agentStdout != nil {
			_ = p.agentStdout.Close()
		}

		// Platform-specific process termination
		p.terminateProcess()
	})
}

func (p *Proxy) logCrashDiagnostics(reason string) {
	// Gather comprehensive crash diagnostics
	p.sessionMux.RLock()
	sessionID := p.sessionID
	p.sessionMux.RUnlock()

	p.modeMux.RLock()
	currentMode := p.currentModeID
	heartbeatMethod := p.heartbeatMethod
	p.modeMux.RUnlock()

	p.promptMux.Lock()
	activePromptID := p.activePromptID
	p.promptMux.Unlock()

	lastActivity := time.Since(time.Unix(0, p.lastActivity.Load()))
	heartbeatSupported := p.heartbeatSupported.Load()
	isShuttingDown := p.isShuttingDown.Load()

	// Check if process is still alive
	processAlive := p.isProcessAlive()

	debugLog(p.townRoot, "[Proxy] === CRASH DIAGNOSTICS ===")
	debugLog(p.townRoot, "[Proxy] Reason: %s", reason)
	debugLog(p.townRoot, "[Proxy] Process alive: %v, Shutting down: %v", processAlive, isShuttingDown)
	debugLog(p.townRoot, "[Proxy] Agent busy: %v, Active prompt: %s", activePromptID != "", activePromptID)
	debugLog(p.townRoot, "[Proxy] Last activity: %v ago", lastActivity)
	debugLog(p.townRoot, "[Proxy] Session ID: %s", sessionID)
	debugLog(p.townRoot, "[Proxy] Current mode: %s", currentMode)
	debugLog(p.townRoot, "[Proxy] Heartbeat: method=%s, supported=%v", heartbeatMethod, heartbeatSupported)
	debugLog(p.townRoot, "[Proxy] =========================")
}

func (p *Proxy) markDone() {
	p.doneOnce.Do(func() {
		close(p.done)
	})
}

func (p *Proxy) agentDone() <-chan error {
	ch := make(chan error, 1)
	go func() {
		err := p.cmd.Wait()
		ch <- err
	}()
	return ch
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func isRedactedThought(msg *JSONRPCMessage) bool {
	if msg.Method != "session/update" {
		return false
	}

	// Check if Params is empty
	if len(msg.Params) == 0 {
		return false
	}

	// Unmarshal params into a generic map
	var params map[string]interface{}
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return false
	}

	update, ok := params["update"].(map[string]interface{})
	if !ok {
		return false
	}

	sessionUpdate, ok := update["sessionUpdate"].(string)
	if !ok || sessionUpdate != "agent_thought_chunk" {
		return false
	}

	content, ok := update["content"].(map[string]interface{})
	if !ok {
		return false
	}

	text, ok := content["text"].(string)
	if !ok {
		return false
	}

	return text == "[REDACTED]"
}

// checkPropulsionTrigger checks if the given JSON-RPC message contains
// a propulsion trigger in its params.
func checkPropulsionTrigger(msg *JSONRPCMessage) bool {
	if msg.Method != "session/update" || len(msg.Params) == 0 {
		return false
	}

	// Triggers only make sense in session/update messages where the agent
	// is sending text or thought chunks to the UI.
	return isPropulsionTrigger(string(msg.Params))
}

// isPropulsionTrigger checks if the given line of agent output should
// trigger autonomous propulsion mode (suppressing output to UI).
func isPropulsionTrigger(line string) bool {
	// Standard GUPP propulsion triggers from prime_output.go and prime_molecule.go
	triggers := []string{
		"AUTONOMOUS WORK MODE",
		"PROPULSION PRINCIPLE: Work is on your hook. RUN IT.",
		"EXECUTE THIS STEP NOW.",
	}

	upperLine := strings.ToUpper(line)
	// Replace any sequence of whitespace characters (including newlines) with a single space
	// to handle multi-line triggers within a single read buffer.
	normalizedLine := strings.Join(strings.Fields(upperLine), " ")

	for _, trigger := range triggers {
		if strings.Contains(normalizedLine, strings.ToUpper(trigger)) {
			return true
		}
	}
	return false
}
