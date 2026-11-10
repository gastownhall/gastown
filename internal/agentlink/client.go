// This file holds the HTTP client for the server's /agent/v1/ API (spec §8).
package agentlink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// callTimeout bounds every call except the long-poll.
	callTimeout = 15 * time.Second
	// pollSlack is added to the long-poll wait for the long-poll timeout
	// (30 s wait + 15 s = 45 s).
	pollSlack = 15 * time.Second
	// maxResponse caps a response body.
	maxResponse = 16 << 20
)

// ErrTokenRejected is returned by Run when the server answers 401: the
// machine's token is unknown or revoked and only `gtb join` can fix it.
var ErrTokenRejected = errors.New("agentlink: token rejected")

// errResync is a 409 {"error":"resync"} from POST /events.
var errResync = errors.New("agentlink: server requested resync")

// apiError is any other non-2xx answer.
type apiError struct {
	Status int
	Code   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("agentlink: server answered %d %s", e.Status, e.Code)
}

// transient reports whether err is worth retrying with backoff: a network
// error, a timeout, or a 5xx/429 answer.
func transient(err error) bool {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.Status >= 500 || ae.Status == http.StatusTooManyRequests
	}
	return err != nil && !errors.Is(err, ErrTokenRejected) && !errors.Is(err, errResync)
}

// client calls /agent/v1/ on one server with one token. The token is only
// ever placed in the Authorization header; it is never logged or wrapped
// into an error.
type client struct {
	base  string // server URL without trailing slash
	token string
	http  *http.Client
}

func newClient(serverURL, token string, hc *http.Client) *client {
	if hc == nil {
		// No client-wide timeout: each call sets its own deadline.
		hc = &http.Client{}
	}
	return &client{base: strings.TrimRight(serverURL, "/"), token: token, http: hc}
}

// call sends method path with body encoded as JSON (nil: no body) and
// decodes a 2xx response into out (nil: discarded).
func (c *client) call(ctx context.Context, timeout time.Duration, method, path string, body, out any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("agentlink: encode %s: %w", path, err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+"/agent/v1/"+path, rd)
	if err != nil {
		return fmt.Errorf("agentlink: build %s: %w", path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// url.Error carries the URL (never the token) and the cause.
		return fmt.Errorf("agentlink: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return fmt.Errorf("agentlink: read %s: %w", path, err)
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		switch {
		case resp.StatusCode == http.StatusUnauthorized:
			return ErrTokenRejected
		case resp.StatusCode == http.StatusConflict && e.Error == "resync":
			return errResync
		}
		return &apiError{Status: resp.StatusCode, Code: e.Error}
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("agentlink: decode %s: %w", path, err)
		}
	}
	return nil
}

func (c *client) hello(ctx context.Context, h Hello) (resync bool, err error) {
	var out struct {
		Resync bool `json:"resync"`
	}
	err = c.call(ctx, callTimeout, http.MethodPost, "hello", h, &out)
	return out.Resync, err
}

func (c *client) snapshot(ctx context.Context, s Snapshot) error {
	return c.call(ctx, callTimeout, http.MethodPost, "snapshot", s, nil)
}

func (c *client) events(ctx context.Context, after string, evs []Event) (cursor string, err error) {
	var out struct {
		Cursor string `json:"cursor"`
	}
	body := struct {
		After  string  `json:"after"`
		Events []Event `json:"events"`
	}{after, evs}
	err = c.call(ctx, callTimeout, http.MethodPost, "events", body, &out)
	return out.Cursor, err
}

func (c *client) requests(ctx context.Context, wait time.Duration) ([]Request, error) {
	var out []Request
	err := c.call(ctx, wait+pollSlack, http.MethodGet, "requests?wait="+wait.String(), nil, &out)
	return out, err
}

func (c *client) results(ctx context.Context, rs []Result) error {
	return c.call(ctx, callTimeout, http.MethodPost, "results", rs, nil)
}

func (c *client) heartbeat(ctx context.Context) error {
	return c.call(ctx, callTimeout, http.MethodPost, "heartbeat", struct{}{}, nil)
}
