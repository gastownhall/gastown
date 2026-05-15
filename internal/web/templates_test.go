package web

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/activity"
)

func TestConvoyTemplate_RendersConvoyList(t *testing.T) {
	tmpl, err := LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates() error = %v", err)
	}

	data := ConvoyData{
		Convoys: []ConvoyRow{
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
				Status:       "open",
				Progress:     "1/3",
				Completed:    1,
				Total:        3,
				LastActivity: activity.Calculate(time.Now().Add(-3 * time.Minute)),
			},
		},
	}

	var buf bytes.Buffer
	err = tmpl.ExecuteTemplate(&buf, "convoy.html", data)
	if err != nil {
		t.Fatalf("ExecuteTemplate() error = %v", err)
	}

	output := buf.String()

	// Check convoy IDs are rendered
	if !strings.Contains(output, "hq-cv-abc") {
		t.Error("Template should contain convoy ID hq-cv-abc")
	}
	if !strings.Contains(output, "hq-cv-def") {
		t.Error("Template should contain convoy ID hq-cv-def")
	}

	// The simplified dashboard no longer shows convoy titles in the table,
	// only the convoy IDs. Titles are shown in expanded view.
}

func TestDashboardScript_SlingUsesLongRunTimeout(t *testing.T) {
	js, err := os.ReadFile("static/dashboard.js")
	if err != nil {
		t.Fatalf("ReadFile(static/dashboard.js) error = %v", err)
	}

	if !strings.Contains(string(js), `JSON.stringify({ command: cmd, confirmed: true, timeout: 120 })`) {
		t.Error("Sling action should request a long /api/run timeout")
	}
}

func TestConvoyTemplate_LastActivityColors(t *testing.T) {
	tmpl, err := LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates() error = %v", err)
	}

	tests := []struct {
		name      string
		age       time.Duration
		wantClass string
	}{
		{"green for 1 minute", 1 * time.Minute, "activity-green"},
		{"yellow for 6 minutes", 6 * time.Minute, "activity-yellow"},
		{"red for 11 minutes", 11 * time.Minute, "activity-red"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := ConvoyData{
				Convoys: []ConvoyRow{
					{
						ID:           "hq-cv-test",
						Title:        "Test",
						Status:       "open",
						LastActivity: activity.Calculate(time.Now().Add(-tt.age)),
					},
				},
			}

			var buf bytes.Buffer
			err = tmpl.ExecuteTemplate(&buf, "convoy.html", data)
			if err != nil {
				t.Fatalf("ExecuteTemplate() error = %v", err)
			}

			output := buf.String()
			if !strings.Contains(output, tt.wantClass) {
				t.Errorf("Template should contain class %q for %v age", tt.wantClass, tt.age)
			}
		})
	}
}

func TestConvoyTemplate_HtmxAutoRefresh(t *testing.T) {
	tmpl, err := LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates() error = %v", err)
	}

	data := ConvoyData{
		Convoys: []ConvoyRow{
			{
				ID:     "hq-cv-test",
				Title:  "Test",
				Status: "open",
			},
		},
	}

	var buf bytes.Buffer
	err = tmpl.ExecuteTemplate(&buf, "convoy.html", data)
	if err != nil {
		t.Fatalf("ExecuteTemplate() error = %v", err)
	}

	output := buf.String()

	// Check for htmx attributes
	if !strings.Contains(output, "hx-get") {
		t.Error("Template should contain hx-get for auto-refresh")
	}
	if !strings.Contains(output, "hx-trigger") {
		t.Error("Template should contain hx-trigger for auto-refresh")
	}
	if !strings.Contains(output, "sse:dashboard-update") {
		t.Error("Template should contain SSE dashboard-update trigger")
	}
	if !strings.Contains(output, "every 30s") {
		t.Error("Template should contain polling fallback trigger")
	}
}

func TestConvoyTemplate_ProgressDisplay(t *testing.T) {
	tmpl, err := LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates() error = %v", err)
	}

	data := ConvoyData{
		Convoys: []ConvoyRow{
			{
				ID:        "hq-cv-test",
				Title:     "Test",
				Status:    "open",
				Progress:  "3/7",
				Completed: 3,
				Total:     7,
			},
		},
	}

	var buf bytes.Buffer
	err = tmpl.ExecuteTemplate(&buf, "convoy.html", data)
	if err != nil {
		t.Fatalf("ExecuteTemplate() error = %v", err)
	}

	output := buf.String()

	// Check progress is displayed
	if !strings.Contains(output, "3/7") {
		t.Error("Template should display progress '3/7'")
	}
}

func TestConvoyTemplate_StatusIndicators(t *testing.T) {
	tmpl, err := LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates() error = %v", err)
	}

	data := ConvoyData{
		Convoys: []ConvoyRow{
			{
				ID:         "hq-cv-active",
				Title:      "Active Convoy",
				Status:     "open",
				WorkStatus: "active",
			},
			{
				ID:         "hq-cv-stuck",
				Title:      "Stuck Convoy",
				Status:     "open",
				WorkStatus: "stuck",
			},
		},
	}

	var buf bytes.Buffer
	err = tmpl.ExecuteTemplate(&buf, "convoy.html", data)
	if err != nil {
		t.Fatalf("ExecuteTemplate() error = %v", err)
	}

	output := buf.String()

	// Check work status badges are rendered (replaced status-open/closed classes)
	if !strings.Contains(output, "badge-green") {
		t.Error("Template should contain badge-green class for active status")
	}
	if !strings.Contains(output, "badge-red") {
		t.Error("Template should contain badge-red class for stuck status")
	}
}

func TestConvoyTemplate_EmptyState(t *testing.T) {
	tmpl, err := LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates() error = %v", err)
	}

	data := ConvoyData{
		Convoys: []ConvoyRow{},
	}

	var buf bytes.Buffer
	err = tmpl.ExecuteTemplate(&buf, "convoy.html", data)
	if err != nil {
		t.Fatalf("ExecuteTemplate() error = %v", err)
	}

	output := buf.String()

	// Check for empty state message
	if !strings.Contains(output, "No active convoys") {
		t.Error("Template should show empty state message when no convoys")
	}
}
