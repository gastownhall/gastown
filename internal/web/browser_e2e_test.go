//go:build browser

package web

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/jonbaldie/gastown/internal/activity"
)

// =============================================================================
// Browser-based E2E Tests using Rod
//
// These tests launch a real browser (Chromium) to verify the convoy dashboard
// works correctly in an actual browser environment.
//
// Run with: go test -tags=browser -v ./internal/web -run TestBrowser
//
// By default, tests run headless. Set BROWSER_VISIBLE=1 to watch:
//   BROWSER_VISIBLE=1 go test -tags=browser -v ./internal/web -run TestBrowser
//
// =============================================================================

// browserTestConfig holds configuration for browser tests
type browserTestConfig struct {
	headless bool
	slowMo   time.Duration
}

// getBrowserConfig returns test configuration based on environment
func getBrowserConfig() browserTestConfig {
	cfg := browserTestConfig{
		headless: true,
		slowMo:   0,
	}

	if os.Getenv("BROWSER_VISIBLE") == "1" {
		cfg.headless = false
		cfg.slowMo = 300 * time.Millisecond
	}

	return cfg
}

// launchBrowser creates a browser instance with the given configuration.
func launchBrowser(cfg browserTestConfig) (*rod.Browser, func()) {
	l := launcher.New().
		NoSandbox(true).
		Headless(cfg.headless)

	if !cfg.headless {
		l = l.Devtools(false)
	}

	u := l.MustLaunch()
	browser := rod.New().ControlURL(u).MustConnect()

	if !cfg.headless {
		browser = browser.SlowMotion(cfg.slowMo)
	}

	cleanup := func() {
		browser.MustClose()
		l.Cleanup()
	}

	return browser, cleanup
}

// mockFetcher implements ConvoyFetcher for testing
type mockFetcher struct {
	convoys []ConvoyRow
}

func (m *mockFetcher) FetchConvoys() ([]ConvoyRow, error) {
	return m.convoys, nil
}

// TestBrowser_ConvoyListLoads tests that the convoy list page loads correctly
func TestBrowser_ConvoyListLoads(t *testing.T) {
	// Setup test server with mock data
	fetcher := &mockFetcher{
		convoys: []ConvoyRow{
			{
				ID:           "hq-cv-abc",
				Title:        "Feature X",
				Status:       "open",
				Progress:     "2/5",
				Completed:    2,
				Total:        5,
				LastActivity: activity.Calculate(time.Now().Add(-1 * time.Minute)),
			},
			{
				ID:           "hq-cv-def",
				Title:        "Bugfix Y",
				Status:       "closed",
				Progress:     "3/3",
				Completed:    3,
				Total:        3,
				LastActivity: activity.Calculate(time.Now().Add(-10 * time.Minute)),
			},
		},
	}

	handler, err := NewConvoyHandler(fetcher, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}

	ts := httptest.NewServer(handler)
	defer ts.Close()

	cfg := getBrowserConfig()
	browser, cleanup := launchBrowser(cfg)
	defer cleanup()

	page := browser.MustPage(ts.URL).Timeout(30 * time.Second)
	defer page.MustClose()

	page.MustWaitLoad()

	// Verify page title
	title := page.MustElement("title").MustText()
	if !strings.Contains(title, "Gas Town") {
		t.Fatalf("Expected title to contain 'Gas Town', got: %s", title)
	}

	// Verify convoy IDs are displayed
	bodyText := page.MustElement("body").MustText()
	if !strings.Contains(bodyText, "hq-cv-abc") {
		t.Error("Expected convoy ID hq-cv-abc in page")
	}
	if !strings.Contains(bodyText, "hq-cv-def") {
		t.Error("Expected convoy ID hq-cv-def in page")
	}

	// Verify titles are displayed
	if !strings.Contains(bodyText, "Feature X") {
		t.Error("Expected title 'Feature X' in page")
	}
	if !strings.Contains(bodyText, "Bugfix Y") {
		t.Error("Expected title 'Bugfix Y' in page")
	}

	t.Log("PASSED: Convoy list loads correctly")
}

// TestBrowser_LastActivityColors tests that activity colors are displayed correctly
func TestBrowser_LastActivityColors(t *testing.T) {
	// Setup test server with convoys at different activity ages
	fetcher := &mockFetcher{
		convoys: []ConvoyRow{
			{
				ID:           "hq-cv-green",
				Title:        "Active Work",
				Status:       "open",
				LastActivity: activity.Calculate(time.Now().Add(-1 * time.Minute)), // Green: <5min
			},
			{
				ID:           "hq-cv-yellow",
				Title:        "Stale Work",
				Status:       "open",
				LastActivity: activity.Calculate(time.Now().Add(-6 * time.Minute)), // Yellow: 5-10min
			},
			{
				ID:           "hq-cv-red",
				Title:        "Stuck Work",
				Status:       "open",
				LastActivity: activity.Calculate(time.Now().Add(-11 * time.Minute)), // Red: >=10min
			},
		},
	}

	handler, err := NewConvoyHandler(fetcher, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}

	ts := httptest.NewServer(handler)
	defer ts.Close()

	cfg := getBrowserConfig()
	browser, cleanup := launchBrowser(cfg)
	defer cleanup()

	page := browser.MustPage(ts.URL).Timeout(30 * time.Second)
	defer page.MustClose()

	page.MustWaitLoad()

	// Check for activity color classes in the HTML
	html := page.MustHTML()

	if !strings.Contains(html, "activity-green") {
		t.Error("Expected activity-green class for recent activity")
	}
	if !strings.Contains(html, "activity-yellow") {
		t.Error("Expected activity-yellow class for stale activity")
	}
	if !strings.Contains(html, "activity-red") {
		t.Error("Expected activity-red class for stuck activity")
	}

	t.Log("PASSED: Activity colors display correctly")
}

// TestBrowser_HtmxAutoRefresh tests that htmx auto-refresh attributes are present
func TestBrowser_HtmxAutoRefresh(t *testing.T) {
	fetcher := &mockFetcher{
		convoys: []ConvoyRow{
			{
				ID:     "hq-cv-test",
				Title:  "Test Convoy",
				Status: "open",
			},
		},
	}

	handler, err := NewConvoyHandler(fetcher, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}

	ts := httptest.NewServer(handler)
	defer ts.Close()

	cfg := getBrowserConfig()
	browser, cleanup := launchBrowser(cfg)
	defer cleanup()

	page := browser.MustPage(ts.URL).Timeout(30 * time.Second)
	defer page.MustClose()

	page.MustWaitLoad()

	// Check for htmx attributes
	html := page.MustHTML()

	if !strings.Contains(html, "hx-get") {
		t.Error("Expected hx-get attribute for auto-refresh")
	}
	if !strings.Contains(html, "hx-trigger") {
		t.Error("Expected hx-trigger attribute for auto-refresh")
	}
	if !strings.Contains(html, "every 30s") {
		t.Error("Expected 'every 30s' trigger for auto-refresh")
	}

	// Verify htmx library is loaded
	if !strings.Contains(html, "htmx.org") {
		t.Error("Expected htmx library to be loaded")
	}

	t.Log("PASSED: htmx auto-refresh attributes present")
}

// TestBrowser_EmptyState tests the empty state when no convoys exist
func TestBrowser_EmptyState(t *testing.T) {
	fetcher := &mockFetcher{
		convoys: []ConvoyRow{}, // Empty convoy list
	}

	handler, err := NewConvoyHandler(fetcher, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}

	ts := httptest.NewServer(handler)
	defer ts.Close()

	cfg := getBrowserConfig()
	browser, cleanup := launchBrowser(cfg)
	defer cleanup()

	page := browser.MustPage(ts.URL).Timeout(30 * time.Second)
	defer page.MustClose()

	page.MustWaitLoad()

	// Check for empty state message
	bodyText := page.MustElement("body").MustText()

	if !strings.Contains(bodyText, "No convoys") {
		t.Errorf("Expected 'No convoys' empty state message, got: %s", bodyText[:min(len(bodyText), 500)])
	}

	// Verify help text is shown
	if !strings.Contains(bodyText, "gt convoy create") {
		t.Error("Expected help text with 'gt convoy create' command")
	}

	t.Log("PASSED: Empty state displays correctly")
}

// TestBrowser_StatusIndicators tests open/closed status indicators
func TestBrowser_StatusIndicators(t *testing.T) {
	fetcher := &mockFetcher{
		convoys: []ConvoyRow{
			{
				ID:     "hq-cv-open",
				Title:  "Open Convoy",
				Status: "open",
			},
			{
				ID:     "hq-cv-closed",
				Title:  "Closed Convoy",
				Status: "closed",
			},
		},
	}

	handler, err := NewConvoyHandler(fetcher, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}

	ts := httptest.NewServer(handler)
	defer ts.Close()

	cfg := getBrowserConfig()
	browser, cleanup := launchBrowser(cfg)
	defer cleanup()

	page := browser.MustPage(ts.URL).Timeout(30 * time.Second)
	defer page.MustClose()

	page.MustWaitLoad()

	html := page.MustHTML()

	// Check for status classes
	if !strings.Contains(html, "status-open") {
		t.Error("Expected status-open class for open convoy")
	}
	if !strings.Contains(html, "status-closed") {
		t.Error("Expected status-closed class for closed convoy")
	}

	t.Log("PASSED: Status indicators display correctly")
}

// TestBrowser_ProgressDisplay tests progress bar rendering
func TestBrowser_ProgressDisplay(t *testing.T) {
	fetcher := &mockFetcher{
		convoys: []ConvoyRow{
			{
				ID:        "hq-cv-progress",
				Title:     "Progress Convoy",
				Status:    "open",
				Progress:  "3/7",
				Completed: 3,
				Total:     7,
			},
		},
	}

	handler, err := NewConvoyHandler(fetcher, 8*time.Second, "test-token")
	if err != nil {
		t.Fatalf("Failed to create handler: %v", err)
	}

	ts := httptest.NewServer(handler)
	defer ts.Close()

	cfg := getBrowserConfig()
	browser, cleanup := launchBrowser(cfg)
	defer cleanup()

	page := browser.MustPage(ts.URL).Timeout(30 * time.Second)
	defer page.MustClose()

	page.MustWaitLoad()

	bodyText := page.MustElement("body").MustText()

	// Verify progress text
	if !strings.Contains(bodyText, "3/7") {
		t.Errorf("Expected progress '3/7' in page, got: %s", bodyText[:min(len(bodyText), 500)])
	}

	// Verify progress bar elements exist
	html := page.MustHTML()
	if !strings.Contains(html, "progress-bar") {
		t.Error("Expected progress-bar class in page")
	}
	if !strings.Contains(html, "progress-fill") {
		t.Error("Expected progress-fill class in page")
	}

	t.Log("PASSED: Progress display works correctly")
}
