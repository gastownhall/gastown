package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// Client talks to a running Worker server as the town.
type Client struct {
	townRoot string
	http     *http.Client
	base     string
}

func newClient(townRoot string) *Client {
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", SocketPath(townRoot))
		},
	}
	return &Client{
		townRoot: townRoot,
		http:     &http.Client{Transport: transport, Timeout: 30 * time.Second},
		base:     "http://worker",
	}
}

func newHTTPClient(townRoot string) (*Client, error) {
	data, err := os.ReadFile(PortPath(townRoot))
	if err != nil {
		return nil, fmt.Errorf("reading worker port: %w", err)
	}
	port := strings.TrimSpace(string(data))
	if port == "" {
		return nil, fmt.Errorf("empty worker port file")
	}
	return &Client{
		townRoot: townRoot,
		http:     &http.Client{Timeout: 30 * time.Second},
		base:     "http://127.0.0.1:" + port,
	}, nil
}

func (c *Client) call(ctx context.Context, req TownRequest) (TownResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return TownResponse{}, fmt.Errorf("marshaling town request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/v1/town", bytes.NewReader(body))
	if err != nil {
		return TownResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return TownResponse{}, fmt.Errorf("worker town call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return TownResponse{}, fmt.Errorf("reading town response: %w", err)
	}
	var out TownResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return TownResponse{}, fmt.Errorf("decoding town response: %w", err)
	}
	if !out.OK {
		return out, fmt.Errorf("%s", out.Error)
	}
	return out, nil
}

func (c *Client) ping(ctx context.Context) error {
	_, err := c.call(ctx, TownRequest{Op: opPing})
	return err
}

// AgentClient is used by a runtime or test-agent to speak the protocol.
type AgentClient struct {
	http *http.Client
	base string
}

// DialAgent connects an agent to the town Worker. Unix socket first.
// HTTP localhost plus the port file is the fallback.
func DialAgent(townRoot string) (*AgentClient, error) {
	if err := pingUnix(townRoot); err == nil {
		dialer := &net.Dialer{Timeout: 2 * time.Second}
		return &AgentClient{
			http: &http.Client{
				Transport: &http.Transport{
					DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
						return dialer.DialContext(ctx, "unix", SocketPath(townRoot))
					},
				},
				Timeout: 30 * time.Second,
			},
			base: "http://worker",
		}, nil
	}
	c, err := newHTTPClient(townRoot)
	if err != nil {
		return nil, fmt.Errorf("dial worker: %w", err)
	}
	return &AgentClient{http: c.http, base: c.base}, nil
}

func pingUnix(townRoot string) error {
	conn, err := net.DialTimeout("unix", SocketPath(townRoot), time.Second)
	if err != nil {
		return err
	}
	return conn.Close()
}

func (a *AgentClient) post(ctx context.Context, path string, v any) ([]byte, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return data, fmt.Errorf("worker %s: %s: %s", path, resp.Status, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func (a *AgentClient) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.base+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return data, fmt.Errorf("worker %s: %s: %s", path, resp.Status, strings.TrimSpace(string(data)))
	}
	return data, nil
}

// Hello registers the agent connection.
func (a *AgentClient) Hello(ctx context.Context, runID, sessionID string) error {
	_, err := a.post(ctx, "/v1/hello", Envelope{Kind: KindIdentity, Peer: PeerAgent, RunID: runID, SessionID: sessionID})
	return err
}

// ReportLifecycle pushes a lifecycle event.
func (a *AgentClient) ReportLifecycle(ctx context.Context, lc Lifecycle) error {
	if lc.Timestamp.IsZero() {
		lc.Timestamp = nowUTC()
	}
	_, err := a.post(ctx, "/v1/lifecycle", lc)
	return err
}

// ReportTelemetry pushes cost and turn events.
func (a *AgentClient) ReportTelemetry(ctx context.Context, batch TelemetryBatch) error {
	_, err := a.post(ctx, "/v1/telemetry", batch)
	return err
}

// ReportHealth pushes a health sample.
func (a *AgentClient) ReportHealth(ctx context.Context, h Health) error {
	_, err := a.post(ctx, "/v1/health", h)
	return err
}

// AskAuthorize asks the town before a dangerous tool. Fail closed on error.
func (a *AgentClient) AskAuthorize(ctx context.Context, req AuthorizeRequest) AuthorizeDecision {
	data, err := a.post(ctx, "/v1/authorize", req)
	if err != nil {
		return AuthorizeDecision{Allowed: false, Reason: "town authorize unreachable: " + err.Error()}
	}
	dec, err := decodePayload[AuthorizeDecision](data)
	if err != nil {
		return AuthorizeDecision{Allowed: false, Reason: "town authorize unreadable"}
	}
	if !dec.Allowed && dec.Reason == "" {
		dec.Reason = "town denied with no reason"
	}
	return dec
}

// Poll waits for the next town message.
func (a *AgentClient) Poll(ctx context.Context, runID string) (Envelope, error) {
	data, err := a.get(ctx, "/v1/poll?run_id="+runID)
	if err != nil {
		return Envelope{}, err
	}
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return Envelope{}, err
	}
	return env, nil
}

// AckPrompt reports accepted or queued after a prompt.
func (a *AgentClient) AckPrompt(ctx context.Context, d Delivery) error {
	_, err := a.post(ctx, "/v1/prompt-ack", d)
	return err
}
