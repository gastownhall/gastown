//go:build darwin

package quota

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ProbeStatus represents the outcome of an account probe.
type ProbeStatus int

const (
	// ProbeOK means the account authenticated successfully.
	ProbeOK ProbeStatus = iota
	// ProbeRateLimited means the account is rate-limited (HTTP 429).
	ProbeRateLimited
	// ProbeAuthError means the token was rejected (HTTP 401/403).
	ProbeAuthError
	// ProbeNoToken means no keychain token could be read.
	ProbeNoToken
	// ProbeNetworkError means the API was unreachable.
	ProbeNetworkError
)

// ProbeResult holds the outcome of a non-LLM API probe.
type ProbeResult struct {
	Status   ProbeStatus
	ResetsAt string // human-readable reset time (e.g., "7pm (America/Los_Angeles)")
	Err      error  // underlying error, if any
}

// OK returns true if the probe succeeded (account is functional).
func (r *ProbeResult) OK() bool {
	return r.Status == ProbeOK
}

const (
	probeTimeout = 10 * time.Second
	probeURL     = "https://api.anthropic.com/v1/messages/count_tokens"
	// Minimal valid-shaped request body — model is required, messages must be
	// non-empty. The endpoint validates auth before checking the payload, so
	// even a bad model name still exercises the auth layer.
	probeBody       = `{"model":"claude-haiku-4-5-20251001","messages":[{"role":"user","content":"x"}]}`
	anthropicVersion = "2023-06-01"
)

// ProbeAccount performs a lightweight, non-LLM API probe for the given account.
// It reads the account's OAuth token from the macOS Keychain and sends a
// count_tokens request to the Anthropic API. This validates authentication and
// detects rate limits without consuming any LLM quota.
//
// configDir is the account's CLAUDE_CONFIG_DIR path (may contain ~).
func ProbeAccount(configDir string) *ProbeResult {
	return ProbeAccountWithContext(context.Background(), configDir)
}

// ProbeAccountWithContext is like ProbeAccount but accepts a context for cancellation.
func ProbeAccountWithContext(ctx context.Context, configDir string) *ProbeResult {
	expanded := expandTilde(configDir)
	svc := KeychainServiceName(expanded)
	token, err := ReadKeychainToken(svc)
	if err != nil || token == "" {
		return &ProbeResult{
			Status: ProbeNoToken,
			Err:    fmt.Errorf("no keychain token for %s: %w", svc, err),
		}
	}

	return probeWithToken(ctx, token)
}

// probeWithToken sends a count_tokens request using the given token.
func probeWithToken(ctx context.Context, token string) *ProbeResult {
	return probeHTTP(ctx, token, probeURL)
}

// probeHTTP sends a count_tokens request to the given URL with the given token.
// Separated from probeWithToken to allow tests to inject a test server URL.
func probeHTTP(ctx context.Context, token, url string) *ProbeResult {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(probeBody))
	if err != nil {
		return &ProbeResult{Status: ProbeNetworkError, Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", token)
	req.Header.Set("anthropic-version", anthropicVersion)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return &ProbeResult{Status: ProbeNetworkError, Err: fmt.Errorf("probe request failed: %w", err)}
	}
	defer func() { _ = resp.Body.Close() }()

	// Read body for error details (limit to 4KB to avoid unbounded reads).
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	switch {
	case resp.StatusCode == http.StatusOK:
		return &ProbeResult{Status: ProbeOK}

	case resp.StatusCode == http.StatusBadRequest:
		// 400 means auth succeeded but request body was invalid — account works.
		return &ProbeResult{Status: ProbeOK}

	case resp.StatusCode == http.StatusTooManyRequests:
		resetsAt := parseRetryAfter(resp, string(body))
		return &ProbeResult{
			Status:   ProbeRateLimited,
			ResetsAt: resetsAt,
			Err:      fmt.Errorf("rate-limited (HTTP 429)"),
		}

	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return &ProbeResult{
			Status: ProbeAuthError,
			Err:    fmt.Errorf("authentication failed (HTTP %d): %s", resp.StatusCode, firstNBytes(body, 200)),
		}

	default:
		// Unexpected status — treat as OK (don't penalize accounts for server errors).
		return &ProbeResult{Status: ProbeOK}
	}
}

// parseRetryAfter extracts reset time from a 429 response.
// Checks Retry-After header first, then falls back to body parsing.
func parseRetryAfter(resp *http.Response, body string) string {
	// Try Retry-After header (seconds until retry).
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil && secs > 0 {
			resetTime := time.Now().Add(time.Duration(secs) * time.Second)
			return resetTime.Format("3:04pm") + " (local)"
		}
	}

	// Try to find a human-readable reset time in the response body.
	// Anthropic rate-limit responses often include "resets at <time>".
	lower := strings.ToLower(body)
	patterns := []string{"resets at ", "resets ", "try again at "}
	for _, pat := range patterns {
		if idx := strings.Index(lower, pat); idx >= 0 {
			after := body[idx+len(pat):]
			// Take up to the next period, comma, or newline.
			end := strings.IndexAny(after, ".,\n\r")
			if end > 0 {
				after = after[:end]
			}
			if len(after) > 40 {
				after = after[:40]
			}
			return strings.TrimSpace(after)
		}
	}
	return ""
}

// firstNBytes returns the first n bytes of a byte slice as a string.
func firstNBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
