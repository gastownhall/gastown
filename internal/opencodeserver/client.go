package opencodeserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxErrorBody                 = 16 << 10
	maxResponseBody              = 4 << 20
	defaultRequestTimeout        = 15 * time.Second
	sessionInitializationTimeout = 2 * time.Minute
)

type Health struct {
	Healthy bool   `json:"healthy"`
	Version string `json:"version"`
}

type Session struct {
	ID        string `json:"id"`
	Directory string `json:"directory"`
	Title     string `json:"title"`
}

type Status struct {
	Type    string `json:"type"`
	Attempt int    `json:"attempt,omitempty"`
	Message string `json:"message,omitempty"`
}

func (s Status) Idle() bool {
	return s.Type == "" || s.Type == "idle"
}

type CreateSessionOptions struct {
	Title   string
	Agent   string
	Model   string
	Variant string
}

type PromptOptions struct {
	Agent     string
	Model     string
	Variant   string
	MessageID string
}

type Client struct {
	baseURL     *url.URL
	username    string
	password    string
	directory   string
	http        *http.Client
	sessionHTTP *http.Client
}

type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Status     string
	Body       string
}

func (e *APIError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("OpenCode %s %s returned %s", e.Method, e.Path, e.Status)
	}
	return fmt.Sprintf("OpenCode %s %s returned %s: %s", e.Method, e.Path, e.Status, e.Body)
}

func IsAPIStatus(err error, status int) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == status
}

func NewClient(baseURL, username, password, directory string, httpClient *http.Client) (*Client, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parsing OpenCode server URL: %w", err)
	}
	if parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("OpenCode server URL must be a loopback HTTP origin")
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("OpenCode server URL must use a loopback host")
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	if username == "" || password == "" {
		return nil, fmt.Errorf("OpenCode server credentials are required")
	}
	if directory == "" {
		return nil, fmt.Errorf("OpenCode directory is required")
	}
	sessionHTTP := httpClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultRequestTimeout}
		sessionHTTP = &http.Client{Timeout: sessionInitializationTimeout}
	}
	return &Client{
		baseURL:     parsed,
		username:    username,
		password:    password,
		directory:   directory,
		http:        httpClient,
		sessionHTTP: sessionHTTP,
	}, nil
}

func (c *Client) Health(ctx context.Context) (Health, error) {
	var result Health
	err := c.doJSON(ctx, http.MethodGet, "/global/health", nil, http.StatusOK, &result)
	return result, err
}

func (c *Client) CreateSession(ctx context.Context, opts CreateSessionOptions) (Session, error) {
	body := make(map[string]any)
	if opts.Title != "" {
		body["title"] = opts.Title
	}
	if opts.Agent != "" {
		body["agent"] = opts.Agent
	}
	if opts.Model != "" {
		providerID, modelID, ok := strings.Cut(opts.Model, "/")
		if !ok || providerID == "" || modelID == "" {
			return Session{}, fmt.Errorf("OpenCode model must use provider/model format")
		}
		model := map[string]any{"providerID": providerID, "id": modelID}
		if opts.Variant != "" {
			model["variant"] = opts.Variant
		}
		body["model"] = model
	}

	var result Session
	err := c.doSessionJSON(ctx, http.MethodPost, "/session", body, http.StatusOK, &result)
	if err == nil && result.ID == "" {
		return Session{}, fmt.Errorf("OpenCode create session returned an empty ID")
	}
	return result, err
}

func (c *Client) GetSession(ctx context.Context, sessionID string) (Session, error) {
	if sessionID == "" {
		return Session{}, fmt.Errorf("OpenCode session ID is required")
	}
	var result Session
	err := c.doSessionJSON(ctx, http.MethodGet, sessionPath(sessionID), nil, http.StatusOK, &result)
	return result, err
}

func (c *Client) DeleteSession(ctx context.Context, sessionID string) error {
	return c.doJSON(ctx, http.MethodDelete, sessionPath(sessionID), nil, http.StatusOK, nil)
}

func (c *Client) Status(ctx context.Context, sessionID string) (Status, error) {
	if sessionID == "" {
		return Status{}, fmt.Errorf("OpenCode session ID is required")
	}
	var statuses map[string]Status
	if err := c.doJSON(ctx, http.MethodGet, "/session/status", nil, http.StatusOK, &statuses); err != nil {
		return Status{}, err
	}
	return statuses[sessionID], nil
}

func (c *Client) PromptAsync(ctx context.Context, sessionID, prompt string, options ...PromptOptions) error {
	_, err := c.promptAsync(ctx, sessionID, prompt, options...)
	return err
}

// promptAsync reports whether this call submitted a new turn. A false result
// with no error means the stable message ID was already accepted previously.
func (c *Client) promptAsync(ctx context.Context, sessionID, prompt string, options ...PromptOptions) (bool, error) {
	if sessionID == "" {
		return false, fmt.Errorf("OpenCode session ID is required")
	}
	if strings.TrimSpace(prompt) == "" {
		return false, fmt.Errorf("OpenCode prompt is required")
	}
	var opts PromptOptions
	if len(options) > 0 {
		opts = options[0]
	}
	if opts.MessageID != "" {
		if !strings.HasPrefix(opts.MessageID, "msg") {
			return false, fmt.Errorf("OpenCode message ID must start with msg")
		}
		exists, err := c.MessageExists(ctx, sessionID, opts.MessageID)
		if err != nil {
			return false, fmt.Errorf("checking prior OpenCode prompt delivery: %w", err)
		}
		if exists {
			return false, nil
		}
	}
	body := map[string]any{
		"parts": []map[string]string{{"type": "text", "text": prompt}},
	}
	if opts.MessageID != "" {
		body["messageID"] = opts.MessageID
	}
	if len(options) > 0 {
		if opts.Agent != "" {
			body["agent"] = opts.Agent
		}
		if opts.Model != "" {
			providerID, modelID, ok := strings.Cut(opts.Model, "/")
			if !ok || providerID == "" || modelID == "" {
				return false, fmt.Errorf("OpenCode model must use provider/model format")
			}
			body["model"] = map[string]string{"providerID": providerID, "modelID": modelID}
		}
		if opts.Variant != "" {
			body["variant"] = opts.Variant
		}
	}
	err := c.doJSON(ctx, http.MethodPost, sessionPath(sessionID)+"/prompt_async", body, http.StatusNoContent, nil)
	if err == nil || opts.MessageID == "" {
		return err == nil, err
	}
	exists, checkErr := c.MessageExists(ctx, sessionID, opts.MessageID)
	if checkErr == nil && exists {
		return true, nil
	}
	return false, errors.Join(err, checkErr)
}

func (c *Client) MessageExists(ctx context.Context, sessionID, messageID string) (bool, error) {
	if sessionID == "" || messageID == "" {
		return false, fmt.Errorf("OpenCode session and message IDs are required")
	}
	err := c.doJSON(ctx, http.MethodGet, sessionPath(sessionID)+"/message/"+url.PathEscape(messageID), nil, http.StatusOK, nil)
	if IsAPIStatus(err, http.StatusNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (c *Client) Abort(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("OpenCode session ID is required")
	}
	return c.doJSON(ctx, http.MethodPost, sessionPath(sessionID)+"/abort", nil, http.StatusOK, nil)
}

func sessionPath(sessionID string) string {
	return "/session/" + url.PathEscape(sessionID)
}

func (c *Client) doJSON(ctx context.Context, method, path string, body any, wantStatus int, result any) error {
	return c.doJSONWithClient(ctx, c.http, method, path, body, wantStatus, result)
}

func (c *Client) doSessionJSON(ctx context.Context, method, path string, body any, wantStatus int, result any) error {
	return c.doJSONWithClient(ctx, c.sessionHTTP, method, path, body, wantStatus, result)
}

func (c *Client) doJSONWithClient(ctx context.Context, httpClient *http.Client, method, path string, body any, wantStatus int, result any) error {
	var bodyReader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding OpenCode request: %w", err)
		}
		bodyReader = bytes.NewReader(encoded)
	}

	requestURL := strings.TrimSuffix(c.baseURL.String(), "/") + path
	req, err := http.NewRequestWithContext(ctx, method, requestURL, bodyReader)
	if err != nil {
		return fmt.Errorf("creating OpenCode request: %w", err)
	}
	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("X-OpenCode-Directory", c.directory)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("calling OpenCode %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != wantStatus {
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody+1))
		if readErr != nil {
			return fmt.Errorf("OpenCode %s %s returned %s (reading error body: %v)", method, path, resp.Status, readErr)
		}
		truncated := len(data) > maxErrorBody
		if truncated {
			data = data[:maxErrorBody]
		}
		message := strings.TrimSpace(string(data))
		if truncated {
			message += "... (truncated)"
		}
		return &APIError{
			Method:     method,
			Path:       path,
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Body:       message,
		}
	}

	if result == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBody))
	if err := decoder.Decode(result); err != nil {
		return fmt.Errorf("decoding OpenCode %s %s response: %w", method, path, err)
	}
	return nil
}
