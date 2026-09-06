package web

import (
	"errors"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/activity"
)

// Test error for simulating fetch failures
var errFetchFailed = errors.New("fetch failed")

// MockConvoyFetcher is a mock implementation for testing.
type MockConvoyFetcher struct {
	Convoys     []ConvoyRow
	MergeQueue  []MergeQueueRow
	Workers     []WorkerRow
	Mail        []MailRow
	Rigs        []RigRow
	Dogs        []DogRow
	Escalations []EscalationRow
	Health      *HealthRow
	Queues      []QueueRow
	Sessions    []SessionRow
	Hooks       []HookRow
	Mayor       *MayorStatus
	Issues      []IssueRow
	Activity    []ActivityRow
	Error       error
}

func (m *MockConvoyFetcher) FetchConvoys() ([]ConvoyRow, error) {
	return m.Convoys, m.Error
}

func (m *MockConvoyFetcher) FetchMergeQueue() ([]MergeQueueRow, error) {
	return m.MergeQueue, nil
}

func (m *MockConvoyFetcher) FetchWorkers() ([]WorkerRow, error) {
	return m.Workers, nil
}

func (m *MockConvoyFetcher) FetchMail() ([]MailRow, error) {
	return m.Mail, nil
}

func (m *MockConvoyFetcher) FetchRigs() ([]RigRow, error) {
	return m.Rigs, nil
}

func (m *MockConvoyFetcher) FetchDogs() ([]DogRow, error) {
	return m.Dogs, nil
}

func (m *MockConvoyFetcher) FetchEscalations() ([]EscalationRow, error) {
	return m.Escalations, nil
}

func (m *MockConvoyFetcher) FetchHealth() (*HealthRow, error) {
	return m.Health, nil
}

func (m *MockConvoyFetcher) FetchQueues() ([]QueueRow, error) {
	return m.Queues, nil
}

func (m *MockConvoyFetcher) FetchSessions() ([]SessionRow, error) {
	return m.Sessions, nil
}

func (m *MockConvoyFetcher) FetchHooks() ([]HookRow, error) {
	return m.Hooks, nil
}

func (m *MockConvoyFetcher) FetchMayor() (*MayorStatus, error) {
	return m.Mayor, nil
}

func (m *MockConvoyFetcher) FetchIssues() ([]IssueRow, error) {
	return m.Issues, nil
}

func (m *MockConvoyFetcher) FetchActivity() ([]ActivityRow, error) {
	return m.Activity, nil
}

func TestConvoyHandler_RendersTemplate(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys: []ConvoyRow{
			{
				ID:           "hq-cv-abc",
				Title:        "Test Convoy",
				Status:       "open",
				Progress:     "2/5",
				Completed:    2,
				Total:        5,
				LastActivity: activity.Calculate(time.Now().Add(-1 * time.Minute)),
			},
		},
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}

	body := w.Body.String()

	// Check convoy data is rendered
	if !strings.Contains(body, "hq-cv-abc") {
		t.Error("Response should contain convoy ID")
	}
	// Note: Convoy titles are no longer shown in the simplified dashboard table view
	if !strings.Contains(body, "2/5") {
		t.Error("Response should contain progress")
	}
}

func TestConvoyHandler_LastActivityColors(t *testing.T) {
	tests := []struct {
		name      string
		age       time.Duration
		wantClass string
	}{
		{"green for active", 30 * time.Second, "activity-green"},
		{"yellow for stale", 6 * time.Minute, "activity-yellow"},
		{"red for stuck", 11 * time.Minute, "activity-red"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &MockConvoyFetcher{
				Convoys: []ConvoyRow{
					{
						ID:           "hq-cv-test",
						Title:        "Test",
						Status:       "open",
						LastActivity: activity.Calculate(time.Now().Add(-tt.age)),
					},
				},
			}

			handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
			if err != nil {
				t.Fatalf("NewConvoyHandler() error = %v", err)
			}

			req := httptest.NewRequest("GET", "/", nil)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			body := w.Body.String()
			if !strings.Contains(body, tt.wantClass) {
				t.Errorf("Response should contain %q", tt.wantClass)
			}
		})
	}
}

func TestConvoyHandler_EmptyConvoys(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys: []ConvoyRow{},
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}

	body := w.Body.String()
	if !strings.Contains(body, "No active convoys") {
		t.Error("Response should show empty state message")
	}
}

func TestConvoyHandler_ContentType(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys: []ConvoyRow{},
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	contentType := w.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", contentType)
	}
}

func TestConvoyHandler_MultipleConvoys(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys: []ConvoyRow{
			{ID: "hq-cv-1", Title: "First Convoy", Status: "open"},
			{ID: "hq-cv-2", Title: "Second Convoy", Status: "closed"},
			{ID: "hq-cv-3", Title: "Third Convoy", Status: "open"},
		},
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	body := w.Body.String()

	// Check all convoys are rendered
	for _, id := range []string{"hq-cv-1", "hq-cv-2", "hq-cv-3"} {
		if !strings.Contains(body, id) {
			t.Errorf("Response should contain convoy %s", id)
		}
	}
}

// Integration tests for error handling
// Note: The refactored dashboard handler treats fetch errors as non-fatal,
// rendering an empty section instead of returning an error.

func TestConvoyHandler_FetchConvoysError(t *testing.T) {
	mock := &MockConvoyFetcher{
		Error: errFetchFailed,
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	// Fetch errors are now non-fatal - the dashboard still renders
	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d (fetch errors are non-fatal)", w.Code, http.StatusOK)
	}

	body := w.Body.String()
	// Should show the empty state for convoys section
	if !strings.Contains(body, "No active convoys") {
		t.Error("Response should show empty state when fetch fails")
	}
}

// Integration tests for merge queue rendering

func TestConvoyHandler_MergeQueueRendering(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys: []ConvoyRow{},
		MergeQueue: []MergeQueueRow{
			{
				Number:     123,
				Repo:       "roxas",
				Title:      "Fix authentication bug",
				URL:        "https://github.com/test/repo/pull/123",
				CIStatus:   "pass",
				Mergeable:  "ready",
				ColorClass: "mq-green",
			},
			{
				Number:     456,
				Repo:       "gastown",
				Title:      "Add dashboard feature",
				URL:        "https://github.com/test/repo/pull/456",
				CIStatus:   "pending",
				Mergeable:  "pending",
				ColorClass: "mq-yellow",
			},
		},
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}

	body := w.Body.String()

	// Check merge queue section header
	if !strings.Contains(body, "Merge Queue") {
		t.Error("Response should contain merge queue section header")
	}

	// Check PR numbers are rendered
	if !strings.Contains(body, "#123") {
		t.Error("Response should contain PR #123")
	}
	if !strings.Contains(body, "#456") {
		t.Error("Response should contain PR #456")
	}

	// Check repo names
	if !strings.Contains(body, "roxas") {
		t.Error("Response should contain repo 'roxas'")
	}

	// Check CI status badges (now display text, not classes)
	if !strings.Contains(body, "CI Pass") {
		t.Error("Response should contain 'CI Pass' text for passing PR")
	}
	if !strings.Contains(body, "CI Running") {
		t.Error("Response should contain 'CI Running' text for pending PR")
	}
}

func TestConvoyHandler_EmptyMergeQueue(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys:    []ConvoyRow{},
		MergeQueue: []MergeQueueRow{},
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	body := w.Body.String()

	// Should show empty state for merge queue
	if !strings.Contains(body, "No PRs in queue") {
		t.Error("Response should show empty merge queue message")
	}
}

// Integration tests for polecat workers rendering

func TestConvoyHandler_PolecatWorkersRendering(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys: []ConvoyRow{},
		Workers: []WorkerRow{
			{
				Name:         "dag",
				Rig:          "roxas",
				SessionID:    "gt-roxas-dag",
				LastActivity: activity.Calculate(time.Now().Add(-30 * time.Second)),
				StatusHint:   "Running tests...",
			},
			{
				Name:         "nux",
				Rig:          "roxas",
				SessionID:    "gt-roxas-nux",
				LastActivity: activity.Calculate(time.Now().Add(-5 * time.Minute)),
				StatusHint:   "Waiting for input",
			},
		},
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}

	body := w.Body.String()

	// Check polecat section header
	if !strings.Contains(body, "Polecats") {
		t.Error("Response should contain polecats section header")
	}

	// Check polecat names
	if !strings.Contains(body, "dag") {
		t.Error("Response should contain polecat 'dag'")
	}
	if !strings.Contains(body, "nux") {
		t.Error("Response should contain polecat 'nux'")
	}

	// Check rig names
	if !strings.Contains(body, "roxas") {
		t.Error("Response should contain rig 'roxas'")
	}

	// Note: StatusHint is no longer displayed in the simplified dashboard view

	// Check activity colors (dag should be green, nux should be yellow/red)
	if !strings.Contains(body, "activity-green") {
		t.Error("Response should contain activity-green for recent activity")
	}
}

// Integration tests for work status rendering

func TestConvoyHandler_WorkStatusRendering(t *testing.T) {
	tests := []struct {
		name           string
		workStatus     string
		wantClass      string
		wantStatusText string
	}{
		{"complete status", "complete", "badge-green", "✓"},
		{"active status", "active", "badge-green", "Active"},
		{"stale status", "stale", "badge-yellow", "Stale"},
		{"stuck status", "stuck", "badge-red", "Stuck"},
		{"waiting status", "waiting", "badge-muted", "Wait"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &MockConvoyFetcher{
				Convoys: []ConvoyRow{
					{
						ID:           "hq-cv-test",
						Title:        "Test Convoy",
						Status:       "open",
						WorkStatus:   tt.workStatus,
						Progress:     "1/2",
						Completed:    1,
						Total:        2,
						LastActivity: activity.Calculate(time.Now()),
					},
				},
			}

			handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
			if err != nil {
				t.Fatalf("NewConvoyHandler() error = %v", err)
			}

			req := httptest.NewRequest("GET", "/", nil)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			body := w.Body.String()

			// Check work status class is applied
			if !strings.Contains(body, tt.wantClass) {
				t.Errorf("Response should contain class %q for work status %q", tt.wantClass, tt.workStatus)
			}

			// Check work status text is displayed
			if !strings.Contains(body, tt.wantStatusText) {
				t.Errorf("Response should contain status text %q", tt.wantStatusText)
			}
		})
	}
}

// Integration tests for progress bar rendering

func TestConvoyHandler_ProgressBarRendering(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys: []ConvoyRow{
			{
				ID:           "hq-cv-progress",
				Title:        "Progress Test",
				Status:       "open",
				WorkStatus:   "active",
				Progress:     "3/4",
				Completed:    3,
				Total:        4,
				ProgressPct:  75,
				LastActivity: activity.Calculate(time.Now()),
			},
		},
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	body := w.Body.String()

	// Check progress text
	if !strings.Contains(body, "3/4") {
		t.Error("Response should contain progress '3/4'")
	}

	// Check progress bar element
	if !strings.Contains(body, "progress-bar") {
		t.Error("Response should contain progress-bar class")
	}

	// Check progress fill with percentage (75%)
	if !strings.Contains(body, "progress-fill") {
		t.Error("Response should contain progress-fill class")
	}
	if !strings.Contains(body, "width: 75%") {
		t.Error("Response should contain 75% width for 3/4 progress")
	}
}

// Integration test for HTMX auto-refresh

func TestConvoyHandler_HTMXAutoRefresh(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys: []ConvoyRow{},
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	body := w.Body.String()

	// Check htmx attributes for auto-refresh
	if !strings.Contains(body, "hx-get") {
		t.Error("Response should contain hx-get attribute for HTMX")
	}
	if !strings.Contains(body, "hx-trigger") {
		t.Error("Response should contain hx-trigger attribute for HTMX")
	}
	if !strings.Contains(body, "sse:dashboard-update") {
		t.Error("Response should contain 'sse:dashboard-update' trigger for SSE")
	}
	if !strings.Contains(body, "every 30s") {
		t.Error("Response should contain 'every 30s' polling fallback")
	}
}

// Integration test for full dashboard with all sections

func TestConvoyHandler_FullDashboard(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys: []ConvoyRow{
			{
				ID:           "hq-cv-full",
				Title:        "Full Test Convoy",
				Status:       "open",
				WorkStatus:   "active",
				Progress:     "2/3",
				Completed:    2,
				Total:        3,
				LastActivity: activity.Calculate(time.Now().Add(-1 * time.Minute)),
			},
		},
		MergeQueue: []MergeQueueRow{
			{
				Number:     789,
				Repo:       "testrig",
				Title:      "Test PR",
				CIStatus:   "pass",
				Mergeable:  "ready",
				ColorClass: "mq-green",
			},
		},
		Workers: []WorkerRow{
			{
				Name:         "worker1",
				Rig:          "testrig",
				SessionID:    "gt-testrig-worker1",
				LastActivity: activity.Calculate(time.Now()),
				StatusHint:   "Working...",
			},
		},
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d", w.Code, http.StatusOK)
	}

	body := w.Body.String()

	// Verify all three sections are present
	if !strings.Contains(body, "Convoys") {
		t.Error("Response should contain convoy section")
	}
	if !strings.Contains(body, "hq-cv-full") {
		t.Error("Response should contain convoy data")
	}
	if !strings.Contains(body, "Merge Queue") {
		t.Error("Response should contain merge queue section")
	}
	if !strings.Contains(body, "#789") {
		t.Error("Response should contain PR data")
	}
	if !strings.Contains(body, "Polecats") {
		t.Error("Response should contain polecats section")
	}
	if !strings.Contains(body, "worker1") {
		t.Error("Response should contain polecat data")
	}
}

// =============================================================================
// End-to-End Tests with httptest.Server
// =============================================================================

// TestE2E_Server_FullDashboard tests the full dashboard using a real HTTP server.
func TestE2E_Server_FullDashboard(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys: []ConvoyRow{
			{
				ID:           "hq-cv-e2e",
				Title:        "E2E Test Convoy",
				Status:       "open",
				WorkStatus:   "active",
				Progress:     "2/4",
				Completed:    2,
				Total:        4,
				LastActivity: activity.Calculate(time.Now().Add(-45 * time.Second)),
			},
		},
		MergeQueue: []MergeQueueRow{
			{
				Number:     101,
				Repo:       "roxas",
				Title:      "E2E Test PR",
				URL:        "https://github.com/test/roxas/pull/101",
				CIStatus:   "pass",
				Mergeable:  "ready",
				ColorClass: "mq-green",
			},
		},
		Workers: []WorkerRow{
			{
				Name:         "furiosa",
				Rig:          "roxas",
				SessionID:    "gt-roxas-furiosa",
				LastActivity: activity.Calculate(time.Now().Add(-30 * time.Second)),
				StatusHint:   "Running E2E tests",
			},
		},
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	// Create a real HTTP server
	server := httptest.NewServer(handler)
	defer server.Close()

	// Make HTTP request to the server
	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("HTTP GET failed: %v", err)
	}
	defer resp.Body.Close()

	// Verify status code
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	// Verify content type
	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", contentType)
	}

	// Read and verify body
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read response body: %v", err)
	}
	body := string(bodyBytes)

	// Verify all three sections render
	checks := []struct {
		name    string
		content string
	}{
		{"Convoy section", "Convoys"},
		{"Convoy ID", "hq-cv-e2e"},
		{"Convoy progress", "2/4"},
		{"Merge queue section", "Merge Queue"},
		{"PR number", "#101"},
		{"PR repo", "roxas"},
		{"Polecats section", "Polecats"},
		{"Polecat name", "furiosa"},
		{"HTMX SSE trigger", `hx-trigger="sse:dashboard-update`},
	}

	for _, check := range checks {
		if !strings.Contains(body, check.content) {
			t.Errorf("%s: should contain %q", check.name, check.content)
		}
	}
}

// TestE2E_Server_ActivityColors tests activity color rendering via HTTP server.
func TestE2E_Server_ActivityColors(t *testing.T) {
	tests := []struct {
		name      string
		age       time.Duration
		wantClass string
	}{
		{"green for recent", 20 * time.Second, "activity-green"},
		{"yellow for stale", 6 * time.Minute, "activity-yellow"},
		{"red for stuck", 11 * time.Minute, "activity-red"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &MockConvoyFetcher{
				Workers: []WorkerRow{
					{
						Name:         "test-worker",
						Rig:          "test-rig",
						SessionID:    "gt-test-rig-test-worker",
						LastActivity: activity.Calculate(time.Now().Add(-tt.age)),
						StatusHint:   "Testing",
					},
				},
			}

			handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
			if err != nil {
				t.Fatalf("NewConvoyHandler() error = %v", err)
			}

			server := httptest.NewServer(handler)
			defer server.Close()

			resp, err := http.Get(server.URL)
			if err != nil {
				t.Fatalf("HTTP GET failed: %v", err)
			}
			defer resp.Body.Close()

			bodyBytes, _ := io.ReadAll(resp.Body)
			body := string(bodyBytes)

			if !strings.Contains(body, tt.wantClass) {
				t.Errorf("Should contain activity class %q for age %v", tt.wantClass, tt.age)
			}
		})
	}
}

// TestE2E_Server_MergeQueueEmpty tests that empty merge queue shows message.
func TestE2E_Server_MergeQueueEmpty(t *testing.T) {
	mock := &MockConvoyFetcher{
		Convoys:    []ConvoyRow{},
		MergeQueue: []MergeQueueRow{},
		Workers:    []WorkerRow{},
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("HTTP GET failed: %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	body := string(bodyBytes)

	// Section header should always be visible
	if !strings.Contains(body, "Merge Queue") {
		t.Error("Merge queue section should always be visible")
	}

	// Empty state message
	if !strings.Contains(body, "No PRs in queue") {
		t.Error("Should show 'No PRs in queue' when empty")
	}
}

// TestE2E_Server_MergeQueueStatuses tests all PR status combinations.
func TestE2E_Server_MergeQueueStatuses(t *testing.T) {
	tests := []struct {
		name       string
		ciStatus   string
		mergeable  string
		colorClass string
		wantCI     string
		wantMerge  string
	}{
		{"green when ready", "pass", "ready", "mq-green", "CI Pass", "Ready"},
		{"red when CI fails", "fail", "ready", "mq-red", "CI Fail", "Ready"},
		{"red when conflict", "pass", "conflict", "mq-red", "CI Pass", "Conflict"},
		{"yellow when pending", "pending", "pending", "mq-yellow", "CI Running", "Pending"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &MockConvoyFetcher{
				MergeQueue: []MergeQueueRow{
					{
						Number:     42,
						Repo:       "test",
						Title:      "Test PR",
						URL:        "https://github.com/test/test/pull/42",
						CIStatus:   tt.ciStatus,
						Mergeable:  tt.mergeable,
						ColorClass: tt.colorClass,
					},
				},
			}

			handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
			if err != nil {
				t.Fatalf("NewConvoyHandler() error = %v", err)
			}

			server := httptest.NewServer(handler)
			defer server.Close()

			resp, err := http.Get(server.URL)
			if err != nil {
				t.Fatalf("HTTP GET failed: %v", err)
			}
			defer resp.Body.Close()

			bodyBytes, _ := io.ReadAll(resp.Body)
			body := string(bodyBytes)

			if !strings.Contains(body, tt.colorClass) {
				t.Errorf("Should contain row class %q", tt.colorClass)
			}
			if !strings.Contains(body, tt.wantCI) {
				t.Errorf("Should contain CI text %q", tt.wantCI)
			}
			if !strings.Contains(body, tt.wantMerge) {
				t.Errorf("Should contain merge text %q", tt.wantMerge)
			}
		})
	}
}

// TestE2E_Server_HTMLStructure validates HTML document structure.
func TestE2E_Server_HTMLStructure(t *testing.T) {
	mock := &MockConvoyFetcher{Convoys: []ConvoyRow{}}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("HTTP GET failed: %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	body := string(bodyBytes)

	// Validate HTML structure
	elements := []string{
		"<!DOCTYPE html>",
		"<html",
		"<head>",
		"<title>Gas Town Control Center</title>",
		"htmx.org",
		"<body>",
		"</body>",
		"</html>",
	}

	for _, elem := range elements {
		if !strings.Contains(body, elem) {
			t.Errorf("Should contain HTML element %q", elem)
		}
	}

	// Validate CSS file is linked (CSS variables are now in external file)
	if !strings.Contains(body, `href="/static/dashboard.css"`) {
		t.Error("Should link to external CSS file dashboard.css")
	}
}

// TestE2E_Server_RefineryInPolecats tests that refinery appears in polecat workers.
func TestE2E_Server_RefineryInPolecats(t *testing.T) {
	mock := &MockConvoyFetcher{
		Workers: []WorkerRow{
			{
				Name:         "refinery",
				Rig:          "roxas",
				SessionID:    "gt-roxas-refinery",
				LastActivity: activity.Calculate(time.Now().Add(-10 * time.Second)),
				StatusHint:   "Idle - Waiting for PRs",
			},
			{
				Name:         "dag",
				Rig:          "roxas",
				SessionID:    "gt-roxas-dag",
				LastActivity: activity.Calculate(time.Now().Add(-30 * time.Second)),
				StatusHint:   "Working on feature",
			},
		},
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("HTTP GET failed: %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	body := string(bodyBytes)

	// Refinery should appear in polecat workers
	if !strings.Contains(body, "refinery") {
		t.Error("Refinery should appear in polecat workers section")
	}
	// Note: StatusHint is no longer displayed in the simplified dashboard view

	// Regular polecats should also appear
	if !strings.Contains(body, "dag") {
		t.Error("Regular polecat 'dag' should appear")
	}
}

// Test that merge queue and polecat errors are non-fatal

type MockConvoyFetcherWithErrors struct {
	Convoys         []ConvoyRow
	MergeQueueError error
	WorkersError    error
}

func (m *MockConvoyFetcherWithErrors) FetchConvoys() ([]ConvoyRow, error) {
	return m.Convoys, nil
}

func (m *MockConvoyFetcherWithErrors) FetchMergeQueue() ([]MergeQueueRow, error) {
	return nil, m.MergeQueueError
}

func (m *MockConvoyFetcherWithErrors) FetchWorkers() ([]WorkerRow, error) {
	return nil, m.WorkersError
}

func (m *MockConvoyFetcherWithErrors) FetchMail() ([]MailRow, error) {
	return nil, nil
}

func (m *MockConvoyFetcherWithErrors) FetchRigs() ([]RigRow, error) {
	return nil, nil
}

func (m *MockConvoyFetcherWithErrors) FetchDogs() ([]DogRow, error) {
	return nil, nil
}

func (m *MockConvoyFetcherWithErrors) FetchEscalations() ([]EscalationRow, error) {
	return nil, nil
}

func (m *MockConvoyFetcherWithErrors) FetchHealth() (*HealthRow, error) {
	return nil, nil
}

func (m *MockConvoyFetcherWithErrors) FetchQueues() ([]QueueRow, error) {
	return nil, nil
}

func (m *MockConvoyFetcherWithErrors) FetchSessions() ([]SessionRow, error) {
	return nil, nil
}

func (m *MockConvoyFetcherWithErrors) FetchHooks() ([]HookRow, error) {
	return nil, nil
}

func (m *MockConvoyFetcherWithErrors) FetchMayor() (*MayorStatus, error) {
	return nil, nil
}

func (m *MockConvoyFetcherWithErrors) FetchIssues() ([]IssueRow, error) {
	return nil, nil
}

func (m *MockConvoyFetcherWithErrors) FetchActivity() ([]ActivityRow, error) {
	return nil, nil
}

// TestConvoyHandler_TemplateErrorReturns500 verifies that template execution errors
// return a proper 500 status code, not 200 (which would happen if we wrote directly
// to the ResponseWriter and it failed mid-execution).
func TestConvoyHandler_TemplateErrorReturns500(t *testing.T) {
	// Create a template that writes some output, then fails
	failingFuncCalled := false
	tmpl := template.Must(template.New("convoy.html").Funcs(template.FuncMap{
		"failAfterOutput": func() (string, error) {
			failingFuncCalled = true
			return "", errors.New("intentional template error")
		},
	}).Parse(`<!DOCTYPE html><html>{{failAfterOutput}}</html>`))

	// Create handler with the failing template
	handler := &ConvoyHandler{
		fetcher:      &MockConvoyFetcher{Convoys: []ConvoyRow{}},
		template:     tmpl,
		fetchTimeout: 5 * time.Second,
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if !failingFuncCalled {
		t.Fatal("Template function was not called")
	}

	// The key assertion: status should be 500, not 200
	// If we write directly to ResponseWriter and it fails mid-execution,
	// headers (with 200) are already sent, so http.Error can't change it
	if w.Code != http.StatusInternalServerError {
		t.Errorf("Status = %d, want %d (template error should return 500, not 200)", w.Code, http.StatusInternalServerError)
	}

	// Error message should be in the body, not partial template content
	body := w.Body.String()
	if !strings.Contains(body, "Failed to render template") {
		t.Errorf("Response should contain error message, got: %q", body)
	}
	if strings.Contains(body, "<!DOCTYPE") {
		t.Error("Error response should not contain partial template output")
	}
}

// TestConvoyHandler_CachePreventsDuplicateFetches verifies that rapid requests
// reuse the cached response instead of spawning fresh fetches (GH#2618).
func TestConvoyHandler_CachePreventsDuplicateFetches(t *testing.T) {
	fetchCount := 0
	mock := &CountingMockFetcher{
		inner:      &MockConvoyFetcher{Convoys: []ConvoyRow{{ID: "hq-cv-cache", Title: "Cache Test", Status: "open"}}},
		fetchCount: &fetchCount,
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}
	handler.cacheTTL = 5 * time.Second // Explicit TTL for test

	// First request — should trigger a fetch
	req1 := httptest.NewRequest("GET", "/", nil)
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req1)

	if w1.Code != http.StatusOK {
		t.Fatalf("First request status = %d, want 200", w1.Code)
	}
	if fetchCount != 1 {
		t.Fatalf("After first request, fetchCount = %d, want 1", fetchCount)
	}

	// Second request — should use cache (within TTL)
	req2 := httptest.NewRequest("GET", "/", nil)
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("Second request status = %d, want 200", w2.Code)
	}
	if fetchCount != 1 {
		t.Errorf("After second request, fetchCount = %d, want 1 (should use cache)", fetchCount)
	}

	// Verify both responses contain the same content
	if w1.Body.String() != w2.Body.String() {
		t.Error("Cached response should match original response")
	}
}

// TestConvoyHandler_CacheBypassOnExpand verifies that ?expand= requests bypass
// the normal response cache but have their own per-panel expand cache (GH#3117).
func TestConvoyHandler_CacheBypassOnExpand(t *testing.T) {
	fetchCount := 0
	mock := &CountingMockFetcher{
		inner:      &MockConvoyFetcher{Convoys: []ConvoyRow{{ID: "hq-cv-expand", Title: "Expand Test", Status: "open"}}},
		fetchCount: &fetchCount,
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}
	handler.cacheTTL = 5 * time.Second

	// Normal request to populate cache
	req1 := httptest.NewRequest("GET", "/", nil)
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req1)

	if fetchCount != 1 {
		t.Fatalf("After first request, fetchCount = %d, want 1", fetchCount)
	}

	// First expand request — should bypass normal cache (different template)
	req2 := httptest.NewRequest("GET", "/?expand=convoys", nil)
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)

	if fetchCount != 1 {
		t.Errorf("First expand request fetchCount = %d, want 1 (shared snapshot)", fetchCount)
	}

	// Second identical expand request — should hit expand cache (GH#3117)
	req3 := httptest.NewRequest("GET", "/?expand=convoys", nil)
	w3 := httptest.NewRecorder()
	handler.ServeHTTP(w3, req3)

	if fetchCount != 1 {
		t.Errorf("Second expand request fetchCount = %d, want 1 (shared snapshot)", fetchCount)
	}
}

func TestConvoyHandler_ExpandCachePreventsRepeatedFetchConvoysErrors(t *testing.T) {
	fetchCount := 0
	mock := &CountingMockFetcher{
		inner:      &MockConvoyFetcher{Error: errFetchFailed},
		fetchCount: &fetchCount,
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}
	handler.cacheTTL = 5 * time.Second

	req1 := httptest.NewRequest("GET", "/?expand=convoys", nil)
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("First expand request status = %d, want 200", w1.Code)
	}
	if fetchCount != 1 {
		t.Fatalf("After first expand request, fetchCount = %d, want 1", fetchCount)
	}

	req2 := httptest.NewRequest("GET", "/?expand=convoys", nil)
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("Second expand request status = %d, want 200", w2.Code)
	}
	if fetchCount != 1 {
		t.Fatalf("Second expand request should use cached error response; fetchCount = %d, want 1", fetchCount)
	}
	if w1.Body.String() != w2.Body.String() {
		t.Fatal("Cached expand error response should match original response")
	}
}

// CountingMockFetcher wraps a ConvoyFetcher and counts FetchConvoys calls.
type CountingMockFetcher struct {
	inner      ConvoyFetcher
	fetchCount *int
}

func (m *CountingMockFetcher) FetchConvoys() ([]ConvoyRow, error) {
	*m.fetchCount++
	return m.inner.FetchConvoys()
}
func (m *CountingMockFetcher) FetchMergeQueue() ([]MergeQueueRow, error) {
	return m.inner.FetchMergeQueue()
}
func (m *CountingMockFetcher) FetchWorkers() ([]WorkerRow, error) { return m.inner.FetchWorkers() }
func (m *CountingMockFetcher) FetchMail() ([]MailRow, error)      { return m.inner.FetchMail() }
func (m *CountingMockFetcher) FetchRigs() ([]RigRow, error)       { return m.inner.FetchRigs() }
func (m *CountingMockFetcher) FetchDogs() ([]DogRow, error)       { return m.inner.FetchDogs() }
func (m *CountingMockFetcher) FetchEscalations() ([]EscalationRow, error) {
	return m.inner.FetchEscalations()
}
func (m *CountingMockFetcher) FetchHealth() (*HealthRow, error)     { return m.inner.FetchHealth() }
func (m *CountingMockFetcher) FetchQueues() ([]QueueRow, error)     { return m.inner.FetchQueues() }
func (m *CountingMockFetcher) FetchSessions() ([]SessionRow, error) { return m.inner.FetchSessions() }
func (m *CountingMockFetcher) FetchHooks() ([]HookRow, error)       { return m.inner.FetchHooks() }
func (m *CountingMockFetcher) FetchMayor() (*MayorStatus, error)    { return m.inner.FetchMayor() }
func (m *CountingMockFetcher) FetchIssues() ([]IssueRow, error)     { return m.inner.FetchIssues() }
func (m *CountingMockFetcher) FetchActivity() ([]ActivityRow, error) {
	return m.inner.FetchActivity()
}

func TestConvoyHandler_NonFatalErrors(t *testing.T) {
	mock := &MockConvoyFetcherWithErrors{
		Convoys: []ConvoyRow{
			{ID: "hq-cv-test", Title: "Test", Status: "open", WorkStatus: "active"},
		},
		MergeQueueError: errFetchFailed,
		WorkersError:    errFetchFailed,
	}

	handler, err := NewConvoyHandler(mock, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	// Should still return OK even if merge queue and polecats fail
	if w.Code != http.StatusOK {
		t.Errorf("Status = %d, want %d (non-fatal errors should not fail request)", w.Code, http.StatusOK)
	}

	body := w.Body.String()

	// Convoys should still render
	if !strings.Contains(body, "hq-cv-test") {
		t.Error("Response should contain convoy data even when other fetches fail")
	}
}

// TestFetchCircuitBreaker verifies exponential backoff on consecutive failures (GH#3117).
func TestFetchCircuitBreaker(t *testing.T) {
	var cb fetchCircuitBreaker

	// Initially allowed
	if !cb.allow() {
		t.Fatal("circuit breaker should allow first attempt")
	}
	if cb.allow() {
		t.Fatal("circuit breaker should reserve the in-flight attempt")
	}

	// Record a failure — should block immediate retry
	cb.recordFailure()
	if cb.allow() {
		t.Fatal("circuit breaker should block after first failure (within backoff)")
	}

	// Verify failure count and backoff are set
	cb.mu.Lock()
	if cb.failures != 1 {
		t.Errorf("failures = %d, want 1", cb.failures)
	}
	if cb.backoff < 5*time.Second {
		t.Errorf("backoff = %v, want >= 5s", cb.backoff)
	}
	cb.mu.Unlock()

	// Record success — should reset
	cb.recordSuccess()
	if !cb.allow() {
		t.Fatal("circuit breaker should allow after success reset")
	}
	cb.mu.Lock()
	if cb.failures != 0 {
		t.Errorf("failures after reset = %d, want 0", cb.failures)
	}
	cb.mu.Unlock()

	// Multiple failures should increase backoff
	cb.recordFailure()
	cb.mu.Lock()
	backoff1 := cb.backoff
	cb.mu.Unlock()

	cb.recordFailure()
	cb.mu.Lock()
	backoff2 := cb.backoff
	cb.mu.Unlock()

	if backoff2 <= backoff1 {
		t.Errorf("backoff should increase: first=%v, second=%v", backoff1, backoff2)
	}

	// Backoff should cap at maxBackoff
	for i := 0; i < 20; i++ {
		cb.recordFailure()
	}
	cb.mu.Lock()
	if cb.backoff > maxBackoff {
		t.Errorf("backoff %v exceeds maxBackoff %v", cb.backoff, maxBackoff)
	}
	cb.mu.Unlock()
}
