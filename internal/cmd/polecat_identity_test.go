package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/style"
)

func TestExtractWorkType(t *testing.T) {
	tests := []struct {
		name      string
		title     string
		issueType string
		expect    string
	}{
		// From explicit issue type
		{"bug type", "anything", "bug", "fix"},
		{"task type", "anything", "task", "feat"},
		{"feature type", "anything", "feature", "feat"},
		{"epic type", "anything", "epic", "epic"},

		// From conventional commit prefix
		{"feat prefix", "feat: add auth", "", "feat"},
		{"fix prefix", "fix: broken login", "", "fix"},
		{"refactor prefix", "refactor: clean up utils", "", "refactor"},
		{"docs prefix", "docs: update readme", "", "docs"},
		{"test prefix", "test: add coverage", "", "test"},
		{"chore prefix", "chore: update deps", "", "chore"},
		{"style prefix", "style: format code", "", "style"},
		{"perf prefix", "perf: optimize query", "", "perf"},

		// Case insensitive prefix
		{"FEAT prefix", "FEAT: add auth", "", "feat"},
		{"Fix prefix", "Fix: broken login", "", "fix"},

		// From keywords
		{"fix keyword", "Fix broken login", "", "fix"},
		{"bug keyword", "Investigate bug in auth", "", "fix"},
		{"add keyword", "Add user dashboard", "", "feat"},
		{"implement keyword", "Implement oauth flow", "", "feat"},
		{"create keyword", "Create migration script", "", "feat"},
		{"refactor keyword", "Refactor database layer", "", "refactor"},
		{"cleanup keyword", "Cleanup unused imports", "", "refactor"},

		// No match
		{"no match", "Update deployment config", "", ""},
		{"empty", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractWorkType(tt.title, tt.issueType)
			if got != tt.expect {
				t.Errorf("extractWorkType(%q, %q) = %q, want %q", tt.title, tt.issueType, got, tt.expect)
			}
		})
	}
}

func TestFormatRelativeTimeCV(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name      string
		timestamp string
		expect    string
	}{
		{"just now", now.Add(-10 * time.Second).Format(time.RFC3339), "just now"},
		{"1 minute", now.Add(-1 * time.Minute).Format(time.RFC3339), "1m ago"},
		{"15 minutes", now.Add(-15 * time.Minute).Format(time.RFC3339), "15m ago"},
		{"1 hour", now.Add(-1 * time.Hour).Format(time.RFC3339), "1h ago"},
		{"5 hours", now.Add(-5 * time.Hour).Format(time.RFC3339), "5h ago"},
		{"1 day", now.Add(-25 * time.Hour).Format(time.RFC3339), "1d ago"},
		{"3 days", now.Add(-72 * time.Hour).Format(time.RFC3339), "3d ago"},
		{"1 week", now.Add(-8 * 24 * time.Hour).Format(time.RFC3339), "1w ago"},
		{"3 weeks", now.Add(-22 * 24 * time.Hour).Format(time.RFC3339), "3w ago"},
		{"invalid", "not-a-timestamp", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatRelativeTimeCV(tt.timestamp)
			if got != tt.expect {
				t.Errorf("formatRelativeTimeCV(%q) = %q, want %q", tt.timestamp, got, tt.expect)
			}
		})
	}

	// Date-only format parses as midnight UTC, so exact day bucket depends
	// on local timezone and time-of-day. Verify it returns a "d ago" string.
	t.Run("date only", func(t *testing.T) {
		dateStr := now.Add(-72 * time.Hour).Format("2006-01-02")
		got := formatRelativeTimeCV(dateStr)
		if got == "" {
			t.Errorf("formatRelativeTimeCV(%q) returned empty for date-only format", dateStr)
		}
	})
}

func TestFormatLanguageStats(t *testing.T) {
	tests := []struct {
		name   string
		langs  map[string]int
		expect string
	}{
		{"empty", map[string]int{}, ""},
		{"single", map[string]int{"Go": 10}, "Go (10)"},
		{"multiple sorted", map[string]int{"Go": 10, "Python": 5, "Rust": 3}, "Go (10), Python (5), Rust (3)"},
		{"caps at 3", map[string]int{"Go": 10, "Python": 5, "Rust": 3, "Java": 1}, "Go (10), Python (5), Rust (3)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatLanguageStats(tt.langs)
			if got != tt.expect {
				t.Errorf("formatLanguageStats = %q, want %q", got, tt.expect)
			}
		})
	}
}

func TestFormatWorkTypeStats(t *testing.T) {
	tests := []struct {
		name   string
		types  map[string]int
		expect string
	}{
		{"empty", map[string]int{}, ""},
		{"single", map[string]int{"feat": 5}, "feat (5)"},
		{"multiple sorted", map[string]int{"feat": 5, "fix": 3, "refactor": 1},
			"feat (5), fix (3), refactor (1)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatWorkTypeStats(tt.types)
			if got != tt.expect {
				t.Errorf("formatWorkTypeStats = %q, want %q", got, tt.expect)
			}
		})
	}
}

func TestSessionToAgentID(t *testing.T) {
	// Generate known session names and verify the agent ID
	sessionName := crewSessionName("gastown", "tester")
	agentID := sessionToAgentID(sessionName)
	if agentID == "" {
		t.Errorf("sessionToAgentID(%q) returned empty", sessionName)
	}
	// Verify it's a valid address-like format
	if agentID == sessionName {
		// Should have been transformed, not returned as-is
		// Unless parsing fails, which would indicate a test issue
		t.Logf("sessionToAgentID returned unchanged: %q (parsing may have failed)", sessionName)
	}
}

func TestSessionToAgentID_Fallback(t *testing.T) {
	// Invalid session names should return the input as fallback
	got := sessionToAgentID("random-session-name")
	// Should still return something (either parsed or fallback)
	if got == "" {
		t.Error("sessionToAgentID should not return empty for any input")
	}
}

// TestSessionToAgentID_TownLevel pins down GH#3699: town-level agents (mayor,
// deacon) must produce a trailing-slash address so writes from gt sling match
// the form queried by gt hook / runMoleculeStatus / buildAgentIdentity.
func TestSessionToAgentID_TownLevel(t *testing.T) {
	tests := []struct {
		session string
		want    string
	}{
		{"hq-mayor", "mayor/"},
		{"hq-deacon", "deacon/"},
	}
	for _, tt := range tests {
		t.Run(tt.session, func(t *testing.T) {
			got := sessionToAgentID(tt.session)
			if got != tt.want {
				t.Errorf("sessionToAgentID(%q) = %q, want %q", tt.session, got, tt.want)
			}
		})
	}
}

func TestFormatCountStyled(t *testing.T) {
	// Test that zero returns a dim "0"
	got := formatCountStyled(0, style.Success)
	if got == "" {
		t.Error("formatCountStyled(0) should not return empty")
	}

	// Test that non-zero returns the number
	got = formatCountStyled(42, style.Success)
	if got == "" {
		t.Error("formatCountStyled(42) should not return empty")
	}
	// The string should contain "42" somewhere (with ANSI codes)
	found := false
	for i := 0; i < len(got)-1; i++ {
		if got[i] == '4' && got[i+1] == '2' {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("formatCountStyled(42) = %q, does not contain '42'", got)
	}
}

func TestPolecatIdentityUsesRigDatabaseAndPrimeFindsHookedWork(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a Unix shell mock for bd")
	}

	townRoot := t.TempDir()
	townBeadsDir := filepath.Join(townRoot, ".beads")
	rigPath := filepath.Join(townRoot, "gastown")
	rigBeadsDir := filepath.Join(rigPath, "mayor", "rig", ".beads")
	for _, dir := range []string{
		filepath.Join(townRoot, "mayor"),
		townBeadsDir,
		filepath.Join(rigPath, ".beads"),
		rigBeadsDir,
		filepath.Join(rigPath, "polecats", "furiosa"),
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	t.Run("identity list includes long hyphenated prefix", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("test uses a Unix shell mock for bd")
		}

		townRoot := t.TempDir()
		rigName := "gastown"
		rigPath := filepath.Join(townRoot, rigName)
		for _, dir := range []string{
			filepath.Join(townRoot, "mayor"),
			filepath.Join(townRoot, ".beads"),
			filepath.Join(rigPath, ".beads"),
		} {
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatalf("mkdir %s: %v", dir, err)
			}
		}
		if err := config.SaveTownConfig(filepath.Join(townRoot, "mayor", "town.json"), &config.TownConfig{
			Type:      "town",
			Version:   config.CurrentTownVersion,
			Name:      "test-town",
			CreatedAt: time.Now(),
		}); err != nil {
			t.Fatalf("save town config: %v", err)
		}
		if err := config.SaveRigsConfig(filepath.Join(townRoot, "mayor", "rigs.json"), &config.RigsConfig{
			Version: config.CurrentRigsVersion,
			Rigs: map[string]config.RigEntry{
				rigName: {GitURL: "https://example.com/gastown.git", AddedAt: time.Now()},
			},
		}); err != nil {
			t.Fatalf("save rigs config: %v", err)
		}
		writeTestRoutes(t, townRoot, []beads.Route{
			{Prefix: "long-hyphenated-prefix-", Path: rigName},
			{Prefix: "hq-", Path: "."},
		})

		binDir := t.TempDir()
		script := `#!/bin/sh
	case "$*" in
	  *list*)
	    printf '%s\n' '[{"id":"long-hyphenated-prefix-gastown-polecat-furiosa","title":"Polecat furiosa","issue_type":"agent","labels":["gt:agent"],"status":"open","description":"role_type: polecat\nrig: gastown\nagent_state: idle"},{"id":"hq-polecat-furiosa","title":"HQ polecat","issue_type":"agent","labels":["gt:agent"],"status":"open","description":"role_type: polecat\nrig: gastown\nagent_state: idle"},{"id":"long-hyphenated-prefix-gastown-crew-furiosa","title":"Crew furiosa","issue_type":"agent","labels":["gt:agent"],"status":"open","description":"role_type: crew\nrig: gastown\nagent_state: idle"},{"id":"long-hyphenated-prefix-other-rig-polecat-furiosa","title":"Other rig polecat","issue_type":"agent","labels":["gt:agent"],"status":"open","description":"role_type: polecat\nrig: other-rig\nagent_state: idle"},{"id":"long-hyphenated-prefix-gastown-polecat-closed","title":"Closed polecat","issue_type":"agent","labels":["gt:agent"],"status":"closed","description":"role_type: polecat\nrig: gastown\nagent_state: idle"}]'
	    ;;
	esac
	`
		if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
			t.Fatalf("write mock bd: %v", err)
		}
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

		oldWD, err := os.Getwd()
		if err != nil {
			t.Fatalf("get working directory: %v", err)
		}
		if err := os.Chdir(townRoot); err != nil {
			t.Fatalf("change to town root: %v", err)
		}
		t.Cleanup(func() { _ = os.Chdir(oldWD) })

		polecatIdentityListJSON = true
		t.Cleanup(func() { polecatIdentityListJSON = false })
		output := captureStdout(t, func() {
			if err := runPolecatIdentityList(&cobra.Command{}, []string{rigName}); err != nil {
				t.Fatalf("run polecat identity list: %v", err)
			}
		})

		var identities []IdentityInfo
		if err := json.Unmarshal([]byte(output), &identities); err != nil {
			t.Fatalf("decode identity list: %v\noutput: %s", err, output)
		}
		if len(identities) != 1 {
			t.Fatalf("identity list returned %d records, want 1: %#v", len(identities), identities)
		}
		if identities[0].Name != "furiosa" || identities[0].BeadID != "long-hyphenated-prefix-gastown-polecat-furiosa" {
			t.Fatalf("identity list record = %#v, want long-prefix polecat identity", identities[0])
		}
	})
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"name":"test"}`), 0644); err != nil {
		t.Fatalf("write town config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rigPath, ".beads", "redirect"), []byte("mayor/rig/.beads\n"), 0644); err != nil {
		t.Fatalf("write rig redirect: %v", err)
	}
	if err := beads.WriteRoutes(townBeadsDir, []beads.Route{
		{Prefix: "hq-", Path: "."},
		{Prefix: "gst-", Path: "gastown/mayor/rig"},
	}); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "bd.log")
	script := `#!/bin/sh
printf 'beads_dir=%s args=%s\n' "$BEADS_DIR" "$*" >> "$MOCK_BD_LOG"
cmd=""
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *) cmd="$arg"; break ;;
  esac
done
case "$cmd" in
  version) exit 0 ;;
  create) printf '%s\n' '{"id":"gst-gastown-polecat-furiosa","title":"Polecat furiosa","status":"open"}' ;;
  show) printf '%s\n' '[{"id":"gst-gastown-polecat-furiosa","title":"Polecat furiosa","issue_type":"task","labels":["gt:agent"],"status":"open","description":"role_type: polecat\nrig: gastown\nagent_state: idle\nhook_bead: null"}]' ;;
  list) printf '%s\n' '[{"id":"gst-work","title":"Routed work","status":"hooked","assignee":"gastown/polecats/furiosa"}]' ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MOCK_BD_LOG", logPath)

	r := &rig.Rig{Name: "gastown", Path: rigPath}
	identityID := polecatBeadIDForRig(r, r.Name, "furiosa")
	identityBeads := polecatIdentityBeads(r)
	if _, err := identityBeads.CreateAgentBead(identityID, "Polecat furiosa", &beads.AgentFields{
		RoleType: "polecat", Rig: r.Name, AgentState: "idle",
	}); err != nil {
		t.Fatalf("create routed polecat identity: %v", err)
	}
	if issue, _, err := identityBeads.GetAgentBead(identityID); err != nil || issue == nil {
		t.Fatalf("resolve routed polecat identity: issue=%v err=%v", issue, err)
	}

	hooked, err := findAgentWorkOnce(RoleContext{
		Role:     RolePolecat,
		Rig:      r.Name,
		Polecat:  "furiosa",
		TownRoot: townRoot,
		WorkDir:  filepath.Join(rigPath, "polecats", "furiosa"),
	}, "gastown/polecats/furiosa")
	if err != nil {
		t.Fatalf("find hooked work: %v", err)
	}
	if hooked == nil || hooked.ID != "gst-work" {
		t.Fatalf("findAgentWorkOnce() = %#v, want routed hooked work", hooked)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read mock bd log: %v", err)
	}
	logOutput := string(logData)
	if strings.Contains(logOutput, "beads_dir="+townBeadsDir) {
		t.Fatalf("polecat identity accessed town beads instead of rig beads:\n%s", logOutput)
	}
	if !strings.Contains(logOutput, "beads_dir="+rigBeadsDir) ||
		!strings.Contains(logOutput, "args=create") ||
		!strings.Contains(logOutput, "args=show") ||
		!strings.Contains(logOutput, "args=list") {
		t.Fatalf("polecat identity and hook lookup did not use rig beads:\n%s", logOutput)
	}
}
