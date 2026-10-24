package opencodeserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
)

const maxEventSize = 1 << 20

type EventProperties struct {
	SessionID string `json:"sessionID"`
	Status    Status `json:"status"`
}

type Event struct {
	Type       string          `json:"type"`
	Properties EventProperties `json:"properties"`
}

func (e Event) SessionID() string {
	return e.Properties.SessionID
}

func (e Event) Status() Status {
	if e.Type == "session.idle" {
		return Status{Type: "idle"}
	}
	return e.Properties.Status
}

type EventStream struct {
	body      io.ReadCloser
	cancel    context.CancelFunc
	events    chan Event
	errors    chan error
	closeOnce sync.Once
}

func (c *Client) Events(ctx context.Context) (*EventStream, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	requestURL := strings.TrimSuffix(c.baseURL.String(), "/") + "/event"
	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, requestURL, nil)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("creating OpenCode event request: %w", err)
	}
	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("X-OpenCode-Directory", c.directory)
	req.Header.Set("Accept", "text/event-stream")

	// http.Client.Timeout includes reading the response body, so it cannot be
	// used for a stream that is expected to remain open for the worker lifetime.
	httpClient := *c.http
	httpClient.Timeout = 0
	resp, err := httpClient.Do(req)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("subscribing to OpenCode events: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody+1))
		cancel()
		if readErr != nil {
			return nil, fmt.Errorf("OpenCode GET /event returned %s (reading error body: %v)", resp.Status, readErr)
		}
		if len(data) > maxErrorBody {
			data = append(data[:maxErrorBody], []byte("... (truncated)")...)
		}
		message := strings.TrimSpace(string(data))
		if message == "" {
			return nil, fmt.Errorf("OpenCode GET /event returned %s", resp.Status)
		}
		return nil, fmt.Errorf("OpenCode GET /event returned %s: %s", resp.Status, message)
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "text/event-stream" {
		resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("OpenCode GET /event returned content type %q, want text/event-stream", resp.Header.Get("Content-Type"))
	}

	stream := &EventStream{
		body:   resp.Body,
		cancel: cancel,
		events: make(chan Event, 16),
		errors: make(chan error, 1),
	}
	go stream.read(streamCtx)
	return stream, nil
}

func (s *EventStream) Events() <-chan Event {
	return s.events
}

func (s *EventStream) Errors() <-chan error {
	return s.errors
}

func (s *EventStream) Close() error {
	var closeErr error
	s.closeOnce.Do(func() {
		s.cancel()
		closeErr = s.body.Close()
	})
	return closeErr
}

func (s *EventStream) read(ctx context.Context) {
	defer close(s.events)
	defer close(s.errors)
	defer s.body.Close()

	scanner := bufio.NewScanner(s.body)
	scanner.Buffer(make([]byte, 64<<10), maxEventSize+64)
	data := make([]byte, 0, 4096)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if len(data) > 0 && !s.dispatch(ctx, data) {
				return
			}
			data = data[:0]
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		if field != "data" {
			continue
		}
		value = strings.TrimPrefix(value, " ")
		extra := len(value)
		if len(data) > 0 {
			extra++
		}
		if len(data)+extra > maxEventSize {
			s.report(ctx, fmt.Errorf("OpenCode event exceeds %d bytes", maxEventSize))
			return
		}
		if len(data) > 0 {
			data = append(data, '\n')
		}
		data = append(data, value...)
	}
	if err := scanner.Err(); err != nil {
		if ctx.Err() == nil {
			s.report(ctx, fmt.Errorf("reading OpenCode events: %w", err))
		}
		return
	}
	if len(data) > 0 && !s.dispatch(ctx, data) {
		return
	}
	if ctx.Err() == nil {
		s.report(ctx, io.ErrUnexpectedEOF)
	}
}

func (s *EventStream) dispatch(ctx context.Context, data []byte) bool {
	var event Event
	if err := json.Unmarshal(data, &event); err != nil {
		s.report(ctx, fmt.Errorf("decoding OpenCode event: %w", err))
		return false
	}
	select {
	case s.events <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *EventStream) report(ctx context.Context, err error) {
	select {
	case s.errors <- err:
	case <-ctx.Done():
	}
}
