package cmd

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
)

func TestGetNextSeverity(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"low", "medium"},
		{"medium", "high"},
		{"high", "critical"},
		{"critical", "critical"}, // already at max
		{"unknown", "critical"},  // default fallthrough
		{"", "critical"},         // empty defaults to critical
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := getNextSeverity(tt.input)
			if got != tt.want {
				t.Errorf("getNextSeverity(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestExtractMailTargetsFromActions(t *testing.T) {
	tests := []struct {
		name    string
		actions []string
		want    []string
	}{
		{
			name:    "empty actions",
			actions: []string{},
			want:    nil,
		},
		{
			name:    "nil actions",
			actions: nil,
			want:    nil,
		},
		{
			name:    "no mail actions",
			actions: []string{"bead", "log", "email:human"},
			want:    nil,
		},
		{
			name:    "single mail target",
			actions: []string{"bead", "mail:mayor"},
			want:    []string{"mayor"},
		},
		{
			name:    "multiple mail targets",
			actions: []string{"bead", "mail:mayor", "mail:gastown/witness", "email:human"},
			want:    []string{"mayor", "gastown/witness"},
		},
		{
			name:    "mail prefix with empty target ignored",
			actions: []string{"mail:"},
			want:    nil,
		},
		{
			name:    "mixed actions",
			actions: []string{"bead", "mail:mayor", "sms:human", "slack", "mail:deacon", "log"},
			want:    []string{"mayor", "deacon"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractMailTargetsFromActions(tt.actions)
			if len(got) != len(tt.want) {
				t.Fatalf("extractMailTargetsFromActions(%v) returned %d targets, want %d: got %v",
					tt.actions, len(got), len(tt.want), got)
			}
			for i, target := range got {
				if target != tt.want[i] {
					t.Errorf("target[%d] = %q, want %q", i, target, tt.want[i])
				}
			}
		})
	}
}

func TestExecuteExternalActionsReportsWarningsAndFailures(t *testing.T) {
	townRoot := t.TempDir()
	statuses := executeExternalActions([]string{"email:human", "log"}, &config.EscalationConfig{}, "hq-esc1", "high", "desc", townRoot)
	if len(statuses) != 2 {
		t.Fatalf("expected 2 statuses, got %d", len(statuses))
	}
	if statuses[0].Channel != "email" || statuses[0].Warning == "" {
		t.Fatalf("expected email warning status, got %#v", statuses[0])
	}
	if statuses[1].Channel != "log" || !statuses[1].RuntimeNotified {
		t.Fatalf("expected successful log delivery status, got %#v", statuses[1])
	}
}

func TestDeliveryStatusJSONContainsPartialFailure(t *testing.T) {
	statuses := []deliveryStatus{{Channel: "bead", Created: true}, {Channel: "mail", Target: "mayor", Error: "notify failed"}}
	hasFailure := false
	for _, status := range statuses {
		if status.Error != "" {
			hasFailure = true
			break
		}
	}
	result := map[string]interface{}{
		"id":       "hq-esc1",
		"severity": "critical",
		"actions":  []string{"bead", "mail:mayor"},
		"targets":  []string{"mayor"},
		"delivery": statuses,
		"status":   map[bool]string{true: "partial_failure", false: "ok"}[hasFailure],
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	text := string(data)
	for _, want := range []string{"\"status\":\"partial_failure\"", "\"delivery\"", "\"channel\":\"mail\"", "\"error\":\"notify failed\""} {
		if !strings.Contains(text, want) {
			t.Fatalf("json output missing %q: %s", want, text)
		}
	}
}

func TestDeliveryStatusJSONContainsSuccessfulMailPathDetails(t *testing.T) {
	statuses := []deliveryStatus{{Channel: "bead", Created: true, Severity: "critical"}, {Channel: "mail", Target: "mayor", Persisted: true, RuntimeNotified: true, Annotated: true, Severity: "critical", NotificationRoute: "mail+nudge"}}
	result := map[string]interface{}{
		"id":       "hq-esc2",
		"severity": "critical",
		"actions":  []string{"bead", "mail:mayor"},
		"targets":  []string{"mayor"},
		"delivery": statuses,
		"status":   "ok",
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	text := string(data)
	for _, want := range []string{"\"status\":\"ok\"", "\"runtime_notified\":true", "\"annotated\":true", "\"notification_route\":\"mail+nudge\""} {
		if !strings.Contains(text, want) {
			t.Fatalf("json output missing %q: %s", want, text)
		}
	}
}

func TestEscalationFingerprintLabel(t *testing.T) {
	got := escalationFingerprintLabel(" deacon:await-signal:hq-deacon ")
	trimmed := escalationFingerprintLabel("deacon:await-signal:hq-deacon")
	if got != trimmed {
		t.Fatalf("fingerprint should trim whitespace: %q != %q", got, trimmed)
	}
	if !strings.HasPrefix(got, "escalation-fp:") {
		t.Fatalf("fingerprint label %q missing prefix", got)
	}
	if len(got) != len("escalation-fp:")+12 {
		t.Fatalf("fingerprint label %q has length %d, want %d", got, len(got), len("escalation-fp:")+12)
	}
	if got == escalationFingerprintLabel("deacon:convoy-check:hq-cv-123") {
		t.Fatal("different raw fingerprints should produce different labels")
	}
	if escalationFingerprintLabel(" ") != "" {
		t.Fatal("blank fingerprint should produce empty label")
	}
}

func TestSeverityEmoji(t *testing.T) {
	tests := []struct {
		severity string
		want     string
	}{
		{config.SeverityCritical, "🚨"},
		{config.SeverityHigh, "⚠️"},
		{config.SeverityMedium, "📢"},
		{config.SeverityLow, "ℹ️"},
		{"unknown", "📋"},
		{"", "📋"},
	}

	for _, tt := range tests {
		t.Run(tt.severity, func(t *testing.T) {
			got := severityEmoji(tt.severity)
			if got != tt.want {
				t.Errorf("severityEmoji(%q) = %q, want %q", tt.severity, got, tt.want)
			}
		})
	}
}

func TestFormatRelativeTime(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name      string
		timestamp string
		want      string
	}{
		{
			name:      "just now",
			timestamp: now.Add(-10 * time.Second).Format(time.RFC3339),
			want:      "just now",
		},
		{
			name:      "1 minute ago",
			timestamp: now.Add(-1 * time.Minute).Format(time.RFC3339),
			want:      "1 minute ago",
		},
		{
			name:      "multiple minutes ago",
			timestamp: now.Add(-15 * time.Minute).Format(time.RFC3339),
			want:      "15 minutes ago",
		},
		{
			name:      "1 hour ago",
			timestamp: now.Add(-1 * time.Hour).Format(time.RFC3339),
			want:      "1 hour ago",
		},
		{
			name:      "multiple hours ago",
			timestamp: now.Add(-5 * time.Hour).Format(time.RFC3339),
			want:      "5 hours ago",
		},
		{
			name:      "1 day ago",
			timestamp: now.Add(-25 * time.Hour).Format(time.RFC3339),
			want:      "1 day ago",
		},
		{
			name:      "multiple days ago",
			timestamp: now.Add(-72 * time.Hour).Format(time.RFC3339),
			want:      "3 days ago",
		},
		{
			name:      "invalid timestamp returns raw",
			timestamp: "not-a-timestamp",
			want:      "not-a-timestamp",
		},
		{
			name:      "empty timestamp returns raw",
			timestamp: "",
			want:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatRelativeTime(tt.timestamp)
			if got != tt.want {
				t.Errorf("formatRelativeTime(%q) = %q, want %q", tt.timestamp, got, tt.want)
			}
		})
	}
}

func TestFormatEscalationMailBody(t *testing.T) {
	tests := []struct {
		name     string
		beadID   string
		severity string
		reason   string
		from     string
		related  string
		wantIn   []string
		notIn    []string
	}{
		{
			name:     "basic escalation",
			beadID:   "hq-abc123",
			severity: "high",
			reason:   "Build failing",
			from:     "gastown/witness",
			related:  "",
			wantIn: []string{
				"Escalation ID: hq-abc123",
				"Severity: high",
				"From: gastown/witness",
				"Reason:",
				"Build failing",
				"gt escalate ack hq-abc123",
				"gt escalate close hq-abc123",
			},
			notIn: []string{"Related:"},
		},
		{
			name:     "with related bead",
			beadID:   "hq-xyz789",
			severity: "critical",
			reason:   "Agent stuck",
			from:     "gastown/deacon",
			related:  "gt-stuck42",
			wantIn: []string{
				"Escalation ID: hq-xyz789",
				"Severity: critical",
				"Related: gt-stuck42",
			},
		},
		{
			name:     "no reason",
			beadID:   "hq-nnn",
			severity: "low",
			reason:   "",
			from:     "system",
			related:  "",
			wantIn: []string{
				"Escalation ID: hq-nnn",
				"Severity: low",
				"From: system",
			},
			notIn: []string{"Reason:"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatEscalationMailBody(tt.beadID, tt.severity, tt.reason, tt.from, tt.related)
			for _, s := range tt.wantIn {
				if !strings.Contains(got, s) {
					t.Errorf("missing %q in output:\n%s", s, got)
				}
			}
			for _, s := range tt.notIn {
				if strings.Contains(got, s) {
					t.Errorf("unexpected %q in output:\n%s", s, got)
				}
			}
		})
	}
}

func TestFormatReescalationMailBody(t *testing.T) {
	result := &beads.ReescalationResult{
		ID:              "hq-esc123",
		Title:           "Build blocked",
		OldSeverity:     "medium",
		NewSeverity:     "high",
		ReescalationNum: 2,
	}

	got := formatReescalationMailBody(result, "gastown/patrol")

	wantIn := []string{
		"Escalation ID: hq-esc123",
		"Severity bumped: medium → high",
		"Reescalation #2",
		"Reescalated by: gastown/patrol",
		"stale threshold",
		"gt escalate ack hq-esc123",
		"gt escalate close hq-esc123",
	}

	for _, s := range wantIn {
		if !strings.Contains(got, s) {
			t.Errorf("missing %q in output:\n%s", s, got)
		}
	}
}

func TestDetectSenderFallback(t *testing.T) {
	// Save original env vars
	origActor := os.Getenv("BD_ACTOR")
	origRole := os.Getenv("GT_ROLE")
	defer func() {
		os.Setenv("BD_ACTOR", origActor)
		os.Setenv("GT_ROLE", origRole)
	}()

	tests := []struct {
		name  string
		actor string
		role  string
		want  string
	}{
		{
			name:  "BD_ACTOR takes priority",
			actor: "gastown/polecats/alpha",
			role:  "gastown/witness",
			want:  "gastown/polecats/alpha",
		},
		{
			name:  "GT_ROLE used when BD_ACTOR empty",
			actor: "",
			role:  "gastown/witness",
			want:  "gastown/witness",
		},
		{
			name:  "empty when both unset",
			actor: "",
			role:  "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("BD_ACTOR", tt.actor)
			os.Setenv("GT_ROLE", tt.role)

			got := detectSenderFallback()
			if got != tt.want {
				t.Errorf("detectSenderFallback() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExecuteExternalActions(t *testing.T) {
	// executeExternalActions prints warnings/info but doesn't return errors.
	// We test that it doesn't panic with various configurations.

	tests := []struct {
		name    string
		actions []string
		cfg     *config.EscalationConfig
	}{
		{
			name:    "no external actions",
			actions: []string{"bead", "mail:mayor"},
			cfg:     &config.EscalationConfig{},
		},
		{
			name:    "email action without contact",
			actions: []string{"email:human"},
			cfg:     &config.EscalationConfig{},
		},
		{
			name:    "email action with contact",
			actions: []string{"email:human"},
			cfg: &config.EscalationConfig{
				Contacts: config.EscalationContacts{
					HumanEmail: "test@example.com",
				},
			},
		},
		{
			name:    "sms action without contact",
			actions: []string{"sms:human"},
			cfg:     &config.EscalationConfig{},
		},
		{
			name:    "sms action with contact",
			actions: []string{"sms:human"},
			cfg: &config.EscalationConfig{
				Contacts: config.EscalationContacts{
					HumanSMS: "+15551234567",
				},
			},
		},
		{
			name:    "slack action without webhook",
			actions: []string{"slack"},
			cfg:     &config.EscalationConfig{},
		},
		{
			name:    "slack action with webhook",
			actions: []string{"slack"},
			cfg: &config.EscalationConfig{
				Contacts: config.EscalationContacts{
					SlackWebhook: "https://hooks.slack.com/test",
				},
			},
		},
		{
			name:    "log action",
			actions: []string{"log"},
			cfg:     &config.EscalationConfig{},
		},
		{
			name:    "all external actions combined",
			actions: []string{"email:human", "sms:human", "slack", "log"},
			cfg: &config.EscalationConfig{
				Contacts: config.EscalationContacts{
					HumanEmail:   "test@example.com",
					HumanSMS:     "+15551234567",
					SlackWebhook: "https://hooks.slack.com/test",
				},
			},
		},
		{
			name:    "empty actions",
			actions: []string{},
			cfg:     &config.EscalationConfig{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			// Should not panic
			executeExternalActions(tt.actions, tt.cfg, "hq-test", "high", "Test escalation", tmpDir)
		})
	}
}

func TestWriteEscalationLog(t *testing.T) {
	tmpDir := t.TempDir()
	err := writeEscalationLog(tmpDir, "hq-abc", "critical", "Test failure")
	if err != nil {
		t.Fatalf("writeEscalationLog returned error: %v", err)
	}

	logPath := tmpDir + "/logs/escalations.log"
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading log file: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "[CRITICAL]") {
		t.Errorf("log entry missing severity, got: %s", content)
	}
	if !strings.Contains(content, "hq-abc") {
		t.Errorf("log entry missing bead ID, got: %s", content)
	}
	if !strings.Contains(content, "Test failure") {
		t.Errorf("log entry missing description, got: %s", content)
	}
}

func TestRunEscalateValidation(t *testing.T) {
	// Save and restore package-level flags
	origSeverity := escalateSeverity
	origReason := escalateReason
	origStdin := escalateStdin
	origDryRun := escalateDryRun
	defer func() {
		escalateSeverity = origSeverity
		escalateReason = origReason
		escalateStdin = origStdin
		escalateDryRun = origDryRun
	}()

	t.Run("stdin and reason conflict", func(t *testing.T) {
		escalateStdin = true
		escalateReason = "some reason"
		escalateSeverity = "medium"

		err := runEscalate(escalateCmd, []string{"test"})
		if err == nil {
			t.Fatal("expected error when --stdin and --reason are both set")
		}
		if !strings.Contains(err.Error(), "cannot use --stdin with --reason/-r") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("no args shows help", func(t *testing.T) {
		escalateStdin = false
		escalateReason = ""
		escalateSeverity = "medium"

		// No args should return nil (shows help)
		err := runEscalate(escalateCmd, []string{})
		if err != nil {
			t.Errorf("expected nil error for no args (help case), got: %v", err)
		}
	})

	t.Run("invalid severity", func(t *testing.T) {
		escalateStdin = false
		escalateReason = ""
		escalateSeverity = "emergency"

		err := runEscalate(escalateCmd, []string{"test escalation"})
		if err == nil {
			t.Fatal("expected error for invalid severity")
		}
		if !strings.Contains(err.Error(), "invalid severity") {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestFormatEscalationMailBodyNeutralSubjectStillCarriesStructuredBody(t *testing.T) {
	body := formatEscalationMailBody("hq-abc123", "high", "Database drift", "deacon/", "gt-xyz")
	for _, want := range []string{
		"Escalation ID: hq-abc123",
		"Severity: high",
		"From: deacon/",
		"Related: gt-xyz",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q: %s", want, body)
		}
	}
}

func TestGetNextSeverityMatchesConfig(t *testing.T) {
	// Verify getNextSeverity in escalate_impl.go matches config.NextSeverity
	// to catch if they ever diverge.
	severities := []string{"low", "medium", "high", "critical"}
	for _, s := range severities {
		cmdResult := getNextSeverity(s)
		configResult := config.NextSeverity(s)
		if cmdResult != configResult {
			t.Errorf("getNextSeverity(%q) = %q but config.NextSeverity(%q) = %q — they diverge!",
				s, cmdResult, s, configResult)
		}
	}
}
