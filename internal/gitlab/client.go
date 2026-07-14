package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
)

const defaultRESTBase = "https://gitlab.com/api/v4"

// Client wraps HTTP interactions with GitLab's REST API v4.
type Client struct {
	httpClient *http.Client
	token      string
	restBase   string
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient sets the underlying HTTP client (useful for testing).
func WithHTTPClient(c *http.Client) Option {
	return func(cl *Client) { cl.httpClient = c }
}

// WithToken overrides the token (default: GITLAB_TOKEN env var).
func WithToken(t string) Option {
	return func(cl *Client) { cl.token = t }
}

// WithRESTBase overrides the REST API base URL — set it from
// APIBaseFromHost for a self-managed instance, or for testing.
func WithRESTBase(url string) Option {
	return func(cl *Client) { cl.restBase = url }
}

// NewClient creates a GitLab API client. By default it reads GITLAB_TOKEN from
// the environment and targets gitlab.com; use WithRESTBase for self-managed
// instances.
func NewClient(opts ...Option) (*Client, error) {
	c := &Client{
		httpClient: http.DefaultClient,
		token:      os.Getenv("GITLAB_TOKEN"),
		restBase:   defaultRESTBase,
	}
	for _, o := range opts {
		o(c)
	}
	if c.token == "" {
		return nil, fmt.Errorf("gitlab: GITLAB_TOKEN is required (set env var or use WithToken)")
	}
	return c, nil
}

// restRequest makes an authenticated REST API request and decodes the JSON response.
func (c *Client) restRequest(ctx context.Context, method, path string, body any, result any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("gitlab: marshal request: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	url := c.restBase + path
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return fmt.Errorf("gitlab: create request: %w", err)
	}
	// GitLab access tokens (personal, project, group) authenticate with the
	// PRIVATE-TOKEN header.
	req.Header.Set("PRIVATE-TOKEN", c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("gitlab: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("gitlab: read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{
			Method:     method,
			Path:       path,
			StatusCode: resp.StatusCode,
			Body:       string(respBody),
		}
	}

	if result != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("gitlab: decode response: %w", err)
		}
	}
	return nil
}

// APIError represents a non-2xx response from the GitLab API.
type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("gitlab: %s %s returned %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

// StatusCodeOf returns the HTTP status code if err wraps an *APIError, else 0.
func StatusCodeOf(err error) int {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode
	}
	return 0
}
