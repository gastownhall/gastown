package quota

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProbeHTTP_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-token" {
			t.Error("expected x-api-key header to be test-token")
		}
		if r.Header.Get("anthropic-version") == "" {
			t.Error("expected anthropic-version header")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"input_tokens":1}`))
	}))
	defer srv.Close()

	result := probeHTTP(context.Background(), "test-token", srv.URL)
	if result.Status != ProbeOK {
		t.Errorf("expected ProbeOK, got %v (err: %v)", result.Status, result.Err)
	}
	if !result.OK() {
		t.Error("OK() should return true for ProbeOK")
	}
}

func TestProbeHTTP_BadRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid model"}`))
	}))
	defer srv.Close()

	result := probeHTTP(context.Background(), "test-token", srv.URL)
	if result.Status != ProbeOK {
		t.Errorf("expected ProbeOK for 400 (auth passed), got %v", result.Status)
	}
}

func TestProbeHTTP_RateLimited_RetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer srv.Close()

	result := probeHTTP(context.Background(), "test-token", srv.URL)
	if result.Status != ProbeRateLimited {
		t.Errorf("expected ProbeRateLimited, got %v", result.Status)
	}
	if result.ResetsAt == "" {
		t.Error("expected non-empty ResetsAt from Retry-After header")
	}
	if result.OK() {
		t.Error("OK() should return false for ProbeRateLimited")
	}
}

func TestProbeHTTP_RateLimited_BodyParse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`Your rate limit resets at 7:00pm PST. Please wait.`))
	}))
	defer srv.Close()

	result := probeHTTP(context.Background(), "test-token", srv.URL)
	if result.Status != ProbeRateLimited {
		t.Errorf("expected ProbeRateLimited, got %v", result.Status)
	}
	if result.ResetsAt == "" {
		t.Error("expected non-empty ResetsAt from body parsing")
	}
}

func TestProbeHTTP_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid api key"}`))
	}))
	defer srv.Close()

	result := probeHTTP(context.Background(), "test-token", srv.URL)
	if result.Status != ProbeAuthError {
		t.Errorf("expected ProbeAuthError, got %v", result.Status)
	}
}

func TestProbeHTTP_Forbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
	}))
	defer srv.Close()

	result := probeHTTP(context.Background(), "test-token", srv.URL)
	if result.Status != ProbeAuthError {
		t.Errorf("expected ProbeAuthError for 403, got %v", result.Status)
	}
}

func TestProbeHTTP_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	result := probeHTTP(context.Background(), "test-token", srv.URL)
	if result.Status != ProbeOK {
		t.Errorf("expected ProbeOK for 500 (don't penalize for server errors), got %v", result.Status)
	}
}

func TestProbeHTTP_NetworkError(t *testing.T) {
	result := probeHTTP(context.Background(), "test-token", "http://localhost:1")
	if result.Status != ProbeNetworkError {
		t.Errorf("expected ProbeNetworkError, got %v", result.Status)
	}
}

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		name      string
		header    string
		body      string
		wantEmpty bool
	}{
		{name: "retry-after header", header: "60"},
		{name: "body resets at", body: "resets at 7pm PST."},
		{name: "body resets", body: "Rate limit resets 12:30pm."},
		{name: "body try again at", body: "try again at 3pm."},
		{name: "no retry info", body: "Something went wrong", wantEmpty: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{Header: http.Header{}}
			if tt.header != "" {
				resp.Header.Set("Retry-After", tt.header)
			}
			result := parseRetryAfter(resp, tt.body)
			if tt.wantEmpty && result != "" {
				t.Errorf("expected empty, got %q", result)
			}
			if !tt.wantEmpty && result == "" {
				t.Error("expected non-empty result")
			}
		})
	}
}

func TestFirstNBytes(t *testing.T) {
	if got := firstNBytes([]byte("hello"), 10); got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
	if got := firstNBytes([]byte("hello world"), 5); got != "hello..." {
		t.Errorf("got %q, want %q", got, "hello...")
	}
}
