package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestIsInternalBead verifies that ephemeral, event-type, and system-prefix
// beads are filtered out, while normal beads pass through.
func TestIsInternalBead(t *testing.T) {
	tests := []struct {
		name string
		b    closedBead
		want bool
	}{
		{
			name: "ephemeral bead is internal",
			b:    closedBead{ID: "gt-xyz", Title: "some bead", Ephemeral: true},
			want: true,
		},
		{
			name: "event type is internal",
			b:    closedBead{ID: "gt-abc", Title: "some event", IssueType: "event"},
			want: true,
		},
		{
			name: "wisp prefix is internal",
			b:    closedBead{ID: "gt-wisp-abc", Title: "wisp-abc: cleanup", IssueType: "task"},
			want: true,
		},
		{
			name: "mol prefix is internal",
			b:    closedBead{ID: "gt-mol-123", Title: "mol-polecat-work", IssueType: "task"},
			want: true,
		},
		{
			name: "plugin run prefix is internal",
			b:    closedBead{ID: "gt-p1", Title: "plugin run: backup", IssueType: "task"},
			want: true,
		},
		{
			name: "cost report prefix is internal",
			b:    closedBead{ID: "gt-c1", Title: "cost report 2026-03-17", IssueType: "task"},
			want: true,
		},
		{
			name: "prefix match is case-insensitive",
			b:    closedBead{ID: "gt-w1", Title: "Wisp-cleanup", IssueType: "task"},
			want: true,
		},
		{
			name: "normal task bead is not internal",
			b:    closedBead{ID: "gt-5jf", Title: "Add tests for gt changelog", IssueType: "task"},
			want: false,
		},
		{
			name: "normal bug bead is not internal",
			b:    closedBead{ID: "gt-bug1", Title: "Fix nil pointer in convoy", IssueType: "bug"},
			want: false,
		},
		{
			name: "non-ephemeral feature bead is not internal",
			b:    closedBead{ID: "gt-f1", Title: "Add changelog command", IssueType: "feature", Ephemeral: false},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isInternalBead(tt.b)
			if got != tt.want {
				t.Errorf("isInternalBead(%+v) = %v, want %v", tt.b, got, tt.want)
			}
		})
	}
}

// TestChangelogSinceTime verifies --today, --since, and default weekly cutoff behavior.
func TestChangelogSinceTime(t *testing.T) {
	now := time.Date(2026, 1, 18, 14, 30, 0, 0, time.Local) // Sunday

	tests := []struct {
		name       string
		today      bool
		since      string
		wantTime   time.Time
		wantErrSub string
	}{
		{
			name:     "--today returns start of today",
			today:    true,
			wantTime: time.Date(2026, 1, 18, 0, 0, 0, 0, time.Local),
		},
		{
			name:     "default (no flags) also returns Monday of current week",
			wantTime: time.Date(2026, 1, 12, 0, 0, 0, 0, time.Local),
		},
		{
			name:     "--since parses YYYY-MM-DD",
			since:    "2026-01-15",
			wantTime: time.Date(2026, 1, 15, 0, 0, 0, 0, time.Local),
		},
		{
			name:       "--since with invalid date returns error",
			since:      "not-a-date",
			wantErrSub: "invalid date",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := changelogSinceTimeAt(now, tt.today, tt.since)
			if tt.wantErrSub != "" {
				if err == nil {
					t.Fatalf("changelogSinceTimeAt() returned nil error, want error containing %q", tt.wantErrSub)
				}
				if !strings.Contains(err.Error(), tt.wantErrSub) {
					t.Errorf("error = %q, want substring %q", err.Error(), tt.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("changelogSinceTimeAt() unexpected error: %v", err)
			}
			if !got.Equal(tt.wantTime) {
				t.Errorf("changelogSinceTimeAt() = %v, want %v", got, tt.wantTime)
			}
		})
	}
}

// TestFormatPeriod checks the three output cases: Today, Week of, and Since.
func TestFormatPeriod(t *testing.T) {
	now := time.Now()
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.Local)

	weekday := int(now.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	monday := now.AddDate(0, 0, -(weekday - 1))
	monY, monM, monD := monday.Date()
	weekStart := time.Date(monY, monM, monD, 0, 0, 0, 0, time.Local)

	arbitraryPast := time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local)

	// On Mondays, today == weekStart. "Week of ..." takes priority over
	// "Today" because it's more informative for changelog headers.
	todayWant := "Today"
	if today.Equal(weekStart) {
		todayWant = fmt.Sprintf("Week of %s", today.Format("Jan 02, 2006"))
	}

	tests := []struct {
		name  string
		since time.Time
		want  string
	}{
		{
			name:  "today returns 'Today' (or 'Week of ...' on Mondays)",
			since: today,
			want:  todayWant,
		},
		{
			name:  "week start returns 'Week of ...'",
			since: weekStart,
			want:  fmt.Sprintf("Week of %s", weekStart.Format("Jan 02, 2006")),
		},
		{
			name:  "arbitrary past date returns 'Since ...'",
			since: arbitraryPast,
			want:  fmt.Sprintf("Since %s", arbitraryPast.Format("Jan 02, 2006")),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatPeriod(tt.since)
			if got != tt.want {
				t.Errorf("formatPeriod(%v) = %q, want %q", tt.since, got, tt.want)
			}
		})
	}
}

// TestFetchClosedBeads_DateCutoff verifies that beads closed before the cutoff
// are excluded and beads closed after the cutoff are included.
func TestFetchClosedBeads_DateCutoff(t *testing.T) {
	binDir := t.TempDir()
	workDir := t.TempDir()

	now := time.Now().UTC()
	recentTime := now.Add(-1 * time.Hour)
	oldTime := now.Add(-48 * time.Hour)

	// Build JSON with one recent and one old bead.
	bdOutput := fmt.Sprintf(`[
		{"id":"gt-new","title":"Recent fix","issue_type":"bug","ephemeral":false,"closed_at":"%s","close_reason":"done"},
		{"id":"gt-old","title":"Old task","issue_type":"task","ephemeral":false,"closed_at":"%s","close_reason":"done"}
	]`, recentTime.Format(time.RFC3339), oldTime.Format(time.RFC3339))

	writeFakeBD(t, binDir, bdOutput)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Cutoff is 24 hours ago — only the recent bead should pass.
	cutoff := now.Add(-24 * time.Hour)
	entries, err := fetchClosedBeads(workDir, "gastown", cutoff)
	if err != nil {
		t.Fatalf("fetchClosedBeads() error: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("fetchClosedBeads() returned %d entries, want 1; entries: %v", len(entries), entries)
	}
	if entries[0].ID != "gt-new" {
		t.Errorf("fetchClosedBeads() returned ID %q, want %q", entries[0].ID, "gt-new")
	}
	if entries[0].Rig != "gastown" {
		t.Errorf("fetchClosedBeads() rig = %q, want %q", entries[0].Rig, "gastown")
	}
}

// TestFetchClosedBeads_FiltersInternalBeads verifies that ephemeral and
// system-prefix beads are excluded from the results.
func TestFetchClosedBeads_FiltersInternalBeads(t *testing.T) {
	binDir := t.TempDir()
	workDir := t.TempDir()

	now := time.Now().UTC()
	recentTime := now.Add(-1 * time.Hour)

	bdOutput := fmt.Sprintf(`[
		{"id":"gt-real","title":"Real work item","issue_type":"task","ephemeral":false,"closed_at":"%s","close_reason":"done"},
		{"id":"gt-wisp-x","title":"wisp-cleanup","issue_type":"task","ephemeral":false,"closed_at":"%s","close_reason":"done"},
		{"id":"gt-ev","title":"An event","issue_type":"event","ephemeral":false,"closed_at":"%s","close_reason":"done"},
		{"id":"gt-eph","title":"Ephemeral thing","issue_type":"task","ephemeral":true,"closed_at":"%s","close_reason":"done"}
	]`,
		recentTime.Format(time.RFC3339),
		recentTime.Format(time.RFC3339),
		recentTime.Format(time.RFC3339),
		recentTime.Format(time.RFC3339),
	)

	writeFakeBD(t, binDir, bdOutput)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cutoff := now.Add(-24 * time.Hour)
	entries, err := fetchClosedBeads(workDir, "gastown", cutoff)
	if err != nil {
		t.Fatalf("fetchClosedBeads() error: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("fetchClosedBeads() returned %d entries, want 1; entries: %v", len(entries), entries)
	}
	if entries[0].ID != "gt-real" {
		t.Errorf("fetchClosedBeads() returned ID %q, want %q", entries[0].ID, "gt-real")
	}
}

// writeFakeBD writes a fake bd script to binDir that always outputs the given JSON.
func writeFakeBD(t *testing.T, binDir, output string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		// Write JSON to a data file; use `type` to print it (echo can't handle multi-line JSON)
		dataPath := filepath.Join(binDir, "bd_output.json")
		if err := os.WriteFile(dataPath, []byte(output), 0644); err != nil {
			t.Fatalf("write fake bd data: %v", err)
		}
		path := filepath.Join(binDir, "bd.cmd")
		script := fmt.Sprintf("@echo off\ntype \"%s\"\n", dataPath)
		if err := os.WriteFile(path, []byte(script), 0644); err != nil {
			t.Fatalf("write fake bd: %v", err)
		}
		return
	}
	path := filepath.Join(binDir, "bd")
	script := fmt.Sprintf("#!/bin/sh\ncat <<'EOF'\n%s\nEOF\n", output)
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
}
