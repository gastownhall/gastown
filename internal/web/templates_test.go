package web

import (
	"bytes"
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

// TestConvoyTemplate_AgentsPanelRendersAllRoles (fl-t33l) is the
// regression guard for the unified Agents panel. The fixture mixes
// a crew session, a polecat worker, and a refinery worker — all
// three must land in the Agents table in a single, flat list.
// Before fl-t33l these would have rendered into three separate
// panels (Crew / Polecats / Sessions), with the gascity-side
// invisible-bucket issue (fl-kwmk) also applying here.
func TestConvoyTemplate_AgentsPanelRendersAllRoles(t *testing.T) {
	tmpl, err := LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates() error = %v", err)
	}

	data := ConvoyData{
		Agents: []AgentView{
			{
				Name:       "fontaine",
				Role:       "crew",
				Rig:        "gastown",
				WorkStatus: "idle",
				State:      "idle",
				Activity:   activity.Calculate(time.Now().Add(-1 * time.Minute)),
				IsAlive:    true,
			},
			{
				Name:       "nux",
				Role:       "polecat",
				Rig:        "gastown",
				BeadID:     "hq-1234",
				BeadTitle:  "Fix the build",
				WorkStatus: "working",
				State:      "spinning",
				Activity:   activity.Calculate(time.Now().Add(-2 * time.Minute)),
				IsAlive:    true,
			},
			{
				Name:       "refinery",
				Role:       "refinery",
				Rig:        "gastown",
				WorkStatus: "idle",
				State:      "idle",
				Activity:   activity.Calculate(time.Now().Add(-30 * time.Second)),
				IsAlive:    true,
			},
		},
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "convoy.html", data); err != nil {
		t.Fatalf("ExecuteTemplate() error = %v", err)
	}
	output := buf.String()

	for _, want := range []string{"fontaine", "nux", "refinery", `id="agents-table"`, "hq-1234"} {
		if !strings.Contains(output, want) {
			t.Errorf("Agents panel missing %q in output", want)
		}
	}
	// Mayor banner is gone — assert that none of its DOM markers
	// leak back into the page.
	for _, gone := range []string{"mayor-banner", "mayor-info", "mayor-title"} {
		if strings.Contains(output, gone) {
			t.Errorf("Mayor banner not removed: found %q in output", gone)
		}
	}
}

// TestToAgentViews_EmptyDashesPropagate covers the gascity-aligned
// "render — when empty" contract in the consolidator, before it
// hits the template.
func TestToAgentViews_EmptyDashesPropagate(t *testing.T) {
	workers := []WorkerRow{
		{Name: "nux", Rig: "gastown", AgentType: "polecat"},
		{Name: "floating", AgentType: "polecat"}, // no Rig
	}
	sessions := []SessionRow{
		{Name: "gt-gastown-witness", Role: "witness", Rig: "gastown", IsAlive: true},
	}
	agents := toAgentViews(workers, sessions, nil)
	if len(agents) != 3 {
		t.Fatalf("got %d agents, want 3 (no mayor + 2 workers + 1 session)", len(agents))
	}
	if agents[1].Rig != "" {
		t.Errorf("worker with no rig should keep empty Rig (template renders —), got %q", agents[1].Rig)
	}
}

// TestToAgentViews_DedupesWorkerSession confirms that when a session
// also represents a worker (same SessionID), we don't double-list it.
func TestToAgentViews_DedupesWorkerSession(t *testing.T) {
	workers := []WorkerRow{{Name: "nux", Rig: "gastown", SessionID: "gt-gastown-nux", AgentType: "polecat"}}
	sessions := []SessionRow{{Name: "gt-gastown-nux", Role: "polecat", Rig: "gastown", IsAlive: true}}
	agents := toAgentViews(workers, sessions, nil)
	if len(agents) != 1 {
		t.Errorf("got %d agents, want 1 (worker + matching session = single row)", len(agents))
	}
}
