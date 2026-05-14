package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/session"
)

func TestValidateCommand(t *testing.T) {
	tests := []struct {
		name      string
		command   string
		wantErr   bool
		wantSafe  bool
		errSubstr string
	}{
		// Allowed safe commands
		{
			name:     "status command",
			command:  "status",
			wantErr:  false,
			wantSafe: true,
		},
		{
			name:     "status with flags",
			command:  "status --json",
			wantErr:  false,
			wantSafe: true,
		},
		{
			name:     "convoy list",
			command:  "convoy list",
			wantErr:  false,
			wantSafe: true,
		},
		{
			name:     "mail inbox",
			command:  "mail inbox",
			wantErr:  false,
			wantSafe: true,
		},

		// Allowed but requires confirmation
		{
			name:     "mail send",
			command:  "mail send foo bar",
			wantErr:  false,
			wantSafe: false,
		},
		{
			name:     "convoy create",
			command:  "convoy create myconvoy",
			wantErr:  false,
			wantSafe: false,
		},

		// Blocked patterns
		{
			name:      "force flag blocked",
			command:   "reset --force",
			wantErr:   true,
			errSubstr: "blocked pattern",
		},
		{
			name:      "delete blocked",
			command:   "delete something",
			wantErr:   true,
			errSubstr: "blocked pattern",
		},
		{
			name:      "kill blocked",
			command:   "kill session",
			wantErr:   true,
			errSubstr: "blocked pattern",
		},
		{
			name:      "rm blocked",
			command:   "rm -rf /",
			wantErr:   true,
			errSubstr: "blocked pattern",
		},

		// Not in whitelist
		{
			name:      "unknown command",
			command:   "randomcmd foo",
			wantErr:   true,
			errSubstr: "not in whitelist",
		},
		{
			name:      "empty command",
			command:   "",
			wantErr:   true,
			errSubstr: "empty command",
		},
		{
			name:      "whitespace only",
			command:   "   ",
			wantErr:   true,
			errSubstr: "empty command",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta, err := ValidateCommand(tt.command)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateCommand(%q) expected error containing %q, got nil", tt.command, tt.errSubstr)
					return
				}
				if tt.errSubstr != "" && !bytes.Contains([]byte(err.Error()), []byte(tt.errSubstr)) {
					t.Errorf("ValidateCommand(%q) error = %q, want error containing %q", tt.command, err.Error(), tt.errSubstr)
				}
				return
			}
			if err != nil {
				t.Errorf("ValidateCommand(%q) unexpected error: %v", tt.command, err)
				return
			}
			if meta.Safe != tt.wantSafe {
				t.Errorf("ValidateCommand(%q) Safe = %v, want %v", tt.command, meta.Safe, tt.wantSafe)
			}
		})
	}
}

func TestSanitizeArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "clean args unchanged",
			args: []string{"status", "--json", "--fast"},
			want: []string{"status", "--json", "--fast"},
		},
		{
			name: "removes semicolon",
			args: []string{"status; rm -rf /"},
			want: []string{"status rm -rf /"},
		},
		{
			name: "removes pipe",
			args: []string{"status | cat"},
			want: []string{"status  cat"},
		},
		{
			name: "removes shell metacharacters",
			args: []string{"$(whoami)", "`id`", "${HOME}"},
			want: []string{"whoami", "id", "HOME"},
		},
		{
			name: "removes newlines",
			args: []string{"foo\nbar", "baz\rbat"},
			want: []string{"foobar", "bazbat"},
		},
		{
			name: "empty after sanitize removed",
			args: []string{"$()"},
			want: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeArgs(tt.args)
			if len(got) != len(tt.want) {
				t.Errorf("SanitizeArgs(%v) = %v, want %v", tt.args, got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("SanitizeArgs(%v)[%d] = %q, want %q", tt.args, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestParseCommandArgs(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    []string
	}{
		{
			name:    "simple command",
			command: "status",
			want:    []string{"status"},
		},
		{
			name:    "command with args",
			command: "mail send foo bar",
			want:    []string{"mail", "send", "foo", "bar"},
		},
		{
			name:    "command with flags",
			command: "status --json --fast",
			want:    []string{"status", "--json", "--fast"},
		},
		{
			name:    "quoted string",
			command: `mail send "hello world"`,
			want:    []string{"mail", "send", "hello world"},
		},
		{
			name:    "single quoted string",
			command: `mail send 'hello world'`,
			want:    []string{"mail", "send", "hello world"},
		},
		{
			name:    "mixed quotes",
			command: `mail send "hello 'nested'" world`,
			want:    []string{"mail", "send", "hello 'nested'", "world"},
		},
		{
			name:    "extra whitespace",
			command: "  status   --json  ",
			want:    []string{"status", "--json"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseCommandArgs(tt.command)
			if len(got) != len(tt.want) {
				t.Errorf("parseCommandArgs(%q) = %v, want %v", tt.command, got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("parseCommandArgs(%q)[%d] = %q, want %q", tt.command, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestAPIHandler_Commands(t *testing.T) {
	handler := NewAPIHandler(30*time.Second, 60*time.Second, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/commands", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /api/commands status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp CommandListResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if len(resp.Commands) == 0 {
		t.Error("Expected non-empty command list")
	}

	// Verify some expected commands are present
	foundStatus := false
	foundMailSend := false
	for _, cmd := range resp.Commands {
		if cmd.Name == "status" {
			foundStatus = true
			if !cmd.Safe {
				t.Error("status command should be safe")
			}
		}
		if cmd.Name == "mail send" {
			foundMailSend = true
			if !cmd.Confirm {
				t.Error("mail send should require confirmation")
			}
		}
	}
	if !foundStatus {
		t.Error("Expected 'status' in command list")
	}
	if !foundMailSend {
		t.Error("Expected 'mail send' in command list")
	}
}

func TestAPIHandler_Run_BlockedCommand(t *testing.T) {
	handler := NewAPIHandler(30*time.Second, 60*time.Second, "test-token")

	body := `{"command": "delete everything"}`
	req := httptest.NewRequest(http.MethodPost, "/api/run", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Dashboard-Token", "test-token")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("POST /api/run blocked command status = %d, want %d", w.Code, http.StatusForbidden)
	}

	var resp CommandResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if resp.Success {
		t.Error("Expected success=false for blocked command")
	}
	if resp.Error == "" {
		t.Error("Expected error message for blocked command")
	}
}

func TestAPIHandler_Run_InvalidJSON(t *testing.T) {
	handler := NewAPIHandler(30*time.Second, 60*time.Second, "test-token")

	body := `{invalid json}`
	req := httptest.NewRequest(http.MethodPost, "/api/run", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Dashboard-Token", "test-token")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("POST /api/run invalid JSON status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestAPIHandler_Run_EmptyCommand(t *testing.T) {
	handler := NewAPIHandler(30*time.Second, 60*time.Second, "test-token")

	body := `{"command": ""}`
	req := httptest.NewRequest(http.MethodPost, "/api/run", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Dashboard-Token", "test-token")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("POST /api/run empty command status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestAPIHandler_Run_MissingCSRFToken(t *testing.T) {
	handler := NewAPIHandler(30*time.Second, 60*time.Second, "test-token")

	body := `{"command": "status"}`
	req := httptest.NewRequest(http.MethodPost, "/api/run", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	// Deliberately omit X-Dashboard-Token header
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("POST /api/run without CSRF token status = %d, want %d", w.Code, http.StatusForbidden)
	}

	var resp CommandResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}
	if resp.Success {
		t.Error("Expected success=false without CSRF token")
	}
	if !strings.Contains(resp.Error, "dashboard token") {
		t.Errorf("Expected error about dashboard token, got: %q", resp.Error)
	}
}

func TestAPIHandler_Run_WrongCSRFToken(t *testing.T) {
	handler := NewAPIHandler(30*time.Second, 60*time.Second, "test-token")

	body := `{"command": "status"}`
	req := httptest.NewRequest(http.MethodPost, "/api/run", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Dashboard-Token", "wrong-token")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("POST /api/run with wrong CSRF token status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestAPIHandler_Run_ConfirmRequired(t *testing.T) {
	handler := NewAPIHandler(30*time.Second, 60*time.Second, "test-token")

	// "mail send" requires Confirm: true in AllowedCommands
	body := `{"command": "mail send alice -s test -m hello"}`
	req := httptest.NewRequest(http.MethodPost, "/api/run", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Dashboard-Token", "test-token")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("POST /api/run confirm command without confirmed=true status = %d, want %d", w.Code, http.StatusForbidden)
	}

	var resp CommandResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}
	if !strings.Contains(resp.Error, "confirmation") {
		t.Errorf("Expected error about confirmation, got: %q", resp.Error)
	}
}

func TestAPIHandler_NotFound(t *testing.T) {
	handler := NewAPIHandler(30*time.Second, 60*time.Second, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/unknown", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("GET /api/unknown status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestGetCommandList(t *testing.T) {
	commands := GetCommandList()

	if len(commands) == 0 {
		t.Error("GetCommandList returned empty list")
	}

	// Check that all commands have required fields
	for _, cmd := range commands {
		if cmd.Name == "" {
			t.Error("Command has empty name")
		}
		if cmd.Desc == "" {
			t.Errorf("Command %q has empty description", cmd.Name)
		}
		if cmd.Category == "" {
			t.Errorf("Command %q has empty category", cmd.Name)
		}
	}
}

func TestAPIHandler_Crew(t *testing.T) {
	handler := &APIHandler{
		gtPath:            "false", // fast-failing stub — crew handler gracefully returns empty on error
		workDir:           t.TempDir(),
		defaultRunTimeout: 5 * time.Second,
		maxRunTimeout:     10 * time.Second,
		cmdSem:            make(chan struct{}, maxConcurrentCommands),
		csrfToken:         "test-token",
	}

	req := httptest.NewRequest(http.MethodGet, "/api/crew", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /api/crew status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp CrewResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Response should have the expected structure (even if empty)
	if resp.Crew == nil {
		t.Error("Expected Crew field to be initialized")
	}
	if resp.ByRig == nil {
		t.Error("Expected ByRig field to be initialized")
	}
}

func TestAPIHandler_Ready(t *testing.T) {
	handler := &APIHandler{
		gtPath:            "false", // fast-failing stub — ready handler gracefully returns empty on error
		workDir:           t.TempDir(),
		defaultRunTimeout: 5 * time.Second,
		maxRunTimeout:     10 * time.Second,
		cmdSem:            make(chan struct{}, maxConcurrentCommands),
		csrfToken:         "test-token",
	}

	req := httptest.NewRequest(http.MethodGet, "/api/ready", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /api/ready status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp ReadyResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Response should have the expected structure (even if empty)
	if resp.Items == nil {
		t.Error("Expected Items field to be initialized")
	}
	if resp.BySource == nil {
		t.Error("Expected BySource field to be initialized")
	}
}

func TestAPIHandler_IssueCreate_MissingTitle(t *testing.T) {
	handler := NewAPIHandler(30*time.Second, 60*time.Second, "test-token")

	body := `{"title": ""}`
	req := httptest.NewRequest(http.MethodPost, "/api/issues/create", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Dashboard-Token", "test-token")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("POST /api/issues/create empty title status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestAPIHandler_IssueCreate_InvalidTitle(t *testing.T) {
	handler := NewAPIHandler(30*time.Second, 60*time.Second, "test-token")

	tests := []struct {
		name  string
		title string
	}{
		{"newline in title", "foo\nbar"},
		{"carriage return in title", "foo\rbar"},
		{"null byte in title", "foo\x00bar"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := map[string]interface{}{
				"title": tt.title,
			}
			body, _ := json.Marshal(payload)
			req := httptest.NewRequest(http.MethodPost, "/api/issues/create", bytes.NewBuffer(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Dashboard-Token", "test-token")
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("POST /api/issues/create with %s: status = %d, want %d", tt.name, w.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestAPIHandler_IssueCreate_InvalidDescription(t *testing.T) {
	handler := NewAPIHandler(30*time.Second, 60*time.Second, "test-token")

	payload := map[string]interface{}{
		"title":       "Valid title",
		"description": "desc with null\x00byte",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/issues/create", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Dashboard-Token", "test-token")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("POST /api/issues/create with null in description: status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestAPIHandler_IssueCreate_InvalidJSON(t *testing.T) {
	handler := NewAPIHandler(30*time.Second, 60*time.Second, "test-token")

	body := `{not valid json}`
	req := httptest.NewRequest(http.MethodPost, "/api/issues/create", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Dashboard-Token", "test-token")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("POST /api/issues/create invalid JSON status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// --- parseIssueShowOutput edge-case tests (issue #1228: panic-safe string indexing) ---

func TestParseIssueShowOutput_EmptyOutput(t *testing.T) {
	resp := parseIssueShowOutput("", "gt-123")
	if resp.ID != "gt-123" {
		t.Errorf("ID = %q, want %q", resp.ID, "gt-123")
	}
	if resp.Title != "" {
		t.Errorf("Title = %q, want empty", resp.Title)
	}
}

func TestParseIssueShowOutput_NoBracket(t *testing.T) {
	// First line without bracket section — title extraction is skipped
	input := "○ gt-abc · My title without status\nType: issue\nCreated: 2025-01-01"
	resp := parseIssueShowOutput(input, "gt-abc")
	if resp.Title != "" {
		t.Errorf("Title = %q, want empty (no bracket means no title extraction)", resp.Title)
	}
	if resp.Type != "issue" {
		t.Errorf("Type = %q, want %q", resp.Type, "issue")
	}
	if resp.Created != "2025-01-01" {
		t.Errorf("Created = %q, want %q", resp.Created, "2025-01-01")
	}
}

func TestParseIssueShowOutput_NoDotSeparator(t *testing.T) {
	// Created line without "·" separator — should not panic on parts[0]
	input := "○ gt-abc · My title   [● P2 · OPEN]\nCreated: 2025-01-01"
	resp := parseIssueShowOutput(input, "gt-abc")
	if resp.Created != "2025-01-01" {
		t.Errorf("Created = %q, want %q", resp.Created, "2025-01-01")
	}
	if resp.Updated != "" {
		t.Errorf("Updated = %q, want empty", resp.Updated)
	}
}

func TestParseIssueShowOutput_CreatedAndUpdated(t *testing.T) {
	// No space around "·" here so TrimPrefix strips "Updated:" cleanly.
	// Real bd output may have spaces around "·", but this test validates
	// the bounds-check safety of the split, not the TrimPrefix edge case.
	input := "○ gt-abc · My title   [● P2 · OPEN]\nCreated: 2025-01-01·Updated: 2025-06-15"
	resp := parseIssueShowOutput(input, "gt-abc")
	if resp.Created != "2025-01-01" {
		t.Errorf("Created = %q, want %q", resp.Created, "2025-01-01")
	}
	if resp.Updated != "2025-06-15" {
		t.Errorf("Updated = %q, want %q", resp.Updated, "2025-06-15")
	}
}

func TestParseIssueShowOutput_TitleAndStatus(t *testing.T) {
	input := "○ gt-abc · Deploy widget   [● P1 · IN PROGRESS]\nType: convoy"
	resp := parseIssueShowOutput(input, "gt-abc")
	if resp.Title != "Deploy widget" {
		t.Errorf("Title = %q, want %q", resp.Title, "Deploy widget")
	}
	if resp.Priority != "P1" {
		t.Errorf("Priority = %q, want %q", resp.Priority, "P1")
	}
	if resp.Status != "IN PROGRESS" {
		t.Errorf("Status = %q, want %q", resp.Status, "IN PROGRESS")
	}
}

func TestParseMailInboxText_EmptyOutput(t *testing.T) {
	msgs := parseMailInboxText("")
	if len(msgs) != 0 {
		t.Errorf("got %d messages from empty output, want 0", len(msgs))
	}
}

func TestParseMailInboxText_UnreadMarker(t *testing.T) {
	// Verifies the TrimPrefix fix for "●" marker — should not panic
	input := "📬 Inbox:\n1. ● Test subject\n      msg-1 from alice\n      2025-01-01"
	msgs := parseMailInboxText(input)
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if msgs[0].Subject != "Test subject" {
		t.Errorf("Subject = %q, want %q", msgs[0].Subject, "Test subject")
	}
	if msgs[0].Read {
		t.Error("expected unread message")
	}
}

// --- groupIntoThreads tests ---

func TestGroupIntoThreads_SingleMessages(t *testing.T) {
	msgs := []MailMessage{
		{ID: "msg-1", From: "alice", Subject: "Hello", Timestamp: "2026-01-01T10:00:00Z"},
		{ID: "msg-2", From: "bob", Subject: "World", Timestamp: "2026-01-01T11:00:00Z"},
	}
	threads := groupIntoThreads(msgs)
	if len(threads) != 2 {
		t.Fatalf("got %d threads, want 2", len(threads))
	}
	if threads[0].Count != 1 {
		t.Errorf("thread 0 count = %d, want 1", threads[0].Count)
	}
	if threads[1].Subject != "World" {
		t.Errorf("thread 1 subject = %q, want %q", threads[1].Subject, "World")
	}
}

func TestGroupIntoThreads_ByThreadID(t *testing.T) {
	msgs := []MailMessage{
		{ID: "msg-1", From: "alice", Subject: "Status update", ThreadID: "t-001", Timestamp: "2026-01-01T10:00:00Z"},
		{ID: "msg-2", From: "bob", Subject: "Re: Status update", ThreadID: "t-001", Timestamp: "2026-01-01T11:00:00Z"},
		{ID: "msg-3", From: "carol", Subject: "Other topic", Timestamp: "2026-01-01T12:00:00Z"},
	}
	threads := groupIntoThreads(msgs)
	if len(threads) != 2 {
		t.Fatalf("got %d threads, want 2", len(threads))
	}
	// First thread should have 2 messages grouped by ThreadID
	if threads[0].Count != 2 {
		t.Errorf("thread 0 count = %d, want 2", threads[0].Count)
	}
	if threads[0].Subject != "Status update" {
		t.Errorf("thread 0 subject = %q, want %q", threads[0].Subject, "Status update")
	}
	if threads[0].LastMessage.ID != "msg-2" {
		t.Errorf("thread 0 last message ID = %q, want %q", threads[0].LastMessage.ID, "msg-2")
	}
	// Second thread is standalone
	if threads[1].Count != 1 {
		t.Errorf("thread 1 count = %d, want 1", threads[1].Count)
	}
}

func TestGroupIntoThreads_ByReplyTo(t *testing.T) {
	msgs := []MailMessage{
		{ID: "msg-1", From: "alice", Subject: "Question", Timestamp: "2026-01-01T10:00:00Z"},
		{ID: "msg-2", From: "bob", Subject: "Re: Question", ReplyTo: "msg-1", Timestamp: "2026-01-01T11:00:00Z"},
	}
	threads := groupIntoThreads(msgs)
	if len(threads) != 1 {
		t.Fatalf("got %d threads, want 1", len(threads))
	}
	if threads[0].Count != 2 {
		t.Errorf("thread count = %d, want 2", threads[0].Count)
	}
	if threads[0].Subject != "Question" {
		t.Errorf("thread subject = %q, want %q", threads[0].Subject, "Question")
	}
}

func TestGroupIntoThreads_UnreadCount(t *testing.T) {
	msgs := []MailMessage{
		{ID: "msg-1", From: "alice", Subject: "Update", ThreadID: "t-001", Read: true},
		{ID: "msg-2", From: "bob", Subject: "Re: Update", ThreadID: "t-001", Read: false},
		{ID: "msg-3", From: "carol", Subject: "Re: Update", ThreadID: "t-001", Read: false},
	}
	threads := groupIntoThreads(msgs)
	if len(threads) != 1 {
		t.Fatalf("got %d threads, want 1", len(threads))
	}
	if threads[0].UnreadCount != 2 {
		t.Errorf("unread count = %d, want 2", threads[0].UnreadCount)
	}
}

func TestGroupIntoThreads_ReSubjectStrip(t *testing.T) {
	msgs := []MailMessage{
		{ID: "msg-1", From: "alice", Subject: "Re: Original topic", ThreadID: "t-001"},
	}
	threads := groupIntoThreads(msgs)
	if threads[0].Subject != "Original topic" {
		t.Errorf("subject = %q, want %q", threads[0].Subject, "Original topic")
	}
}

// --- parseIssueShowJSON tests (issue #1228: prefer structured JSON over text parsing) ---

func TestParseIssueShowJSON_ValidOutput(t *testing.T) {
	input := `[{
		"id": "gt-abc",
		"title": "Deploy widget",
		"description": "Detailed plan here",
		"status": "open",
		"priority": 1,
		"issue_type": "convoy",
		"created_at": "2025-01-01T00:00:00Z",
		"updated_at": "2025-06-15T00:00:00Z",
		"depends_on": ["gt-dep1"],
		"blocks": ["gt-blk1", "gt-blk2"]
	}]`
	resp, ok := parseIssueShowJSON(input)
	if !ok {
		t.Fatal("parseIssueShowJSON returned ok=false for valid input")
	}
	if resp.ID != "gt-abc" {
		t.Errorf("ID = %q, want %q", resp.ID, "gt-abc")
	}
	if resp.Title != "Deploy widget" {
		t.Errorf("Title = %q, want %q", resp.Title, "Deploy widget")
	}
	if resp.Priority != "P1" {
		t.Errorf("Priority = %q, want %q", resp.Priority, "P1")
	}
	if resp.Status != "open" {
		t.Errorf("Status = %q, want %q", resp.Status, "open")
	}
	if resp.Type != "convoy" {
		t.Errorf("Type = %q, want %q", resp.Type, "convoy")
	}
	if resp.Description != "Detailed plan here" {
		t.Errorf("Description = %q, want %q", resp.Description, "Detailed plan here")
	}
	if len(resp.DependsOn) != 1 || resp.DependsOn[0] != "gt-dep1" {
		t.Errorf("DependsOn = %v, want [gt-dep1]", resp.DependsOn)
	}
	if len(resp.Blocks) != 2 {
		t.Errorf("Blocks = %v, want 2 elements", resp.Blocks)
	}
}

func TestParseIssueShowJSON_ZeroPriority(t *testing.T) {
	input := `[{"id": "gt-abc", "title": "No priority", "priority": 0}]`
	resp, ok := parseIssueShowJSON(input)
	if !ok {
		t.Fatal("parseIssueShowJSON returned ok=false")
	}
	if resp.Priority != "" {
		t.Errorf("Priority = %q, want empty for priority=0", resp.Priority)
	}
}

func TestParseIssueShowJSON_InvalidInputs(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty string", ""},
		{"malformed JSON", "{not valid"},
		{"empty array", "[]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := parseIssueShowJSON(tt.input)
			if ok {
				t.Errorf("parseIssueShowJSON(%q) returned ok=true, want false", tt.input)
			}
		})
	}
}

func TestAPIHandler_SSE_ContentType(t *testing.T) {
	handler := NewAPIHandler(30*time.Second, 60*time.Second, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	// Cancel context quickly so the SSE handler returns instead of blocking
	ctx, cancel := context.WithTimeout(req.Context(), 100*time.Millisecond)
	defer cancel()
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	contentType := w.Header().Get("Content-Type")
	if contentType != "text/event-stream" {
		t.Errorf("GET /api/events Content-Type = %q, want %q", contentType, "text/event-stream")
	}

	body := w.Body.String()
	if !strings.Contains(body, "event: connected") {
		t.Error("SSE response should contain initial 'connected' event")
	}
}

// TestOptionsCacheConcurrentAccess verifies that concurrent cache reads and
// writes don't race. The read lock is held through serialization so a
// concurrent writer can't replace the cached pointer mid-encode.
//
// Regression test for steveyegge/gastown#1230 item 4.
func TestOptionsCacheConcurrentAccess(t *testing.T) {
	h := &APIHandler{
		gtPath:            "echo", // won't actually be called for cache hits
		workDir:           t.TempDir(),
		defaultRunTimeout: 5 * time.Second,
		maxRunTimeout:     10 * time.Second,
		cmdSem:            make(chan struct{}, maxConcurrentCommands),
	}

	// Pre-populate cache so reads hit.
	h.optionsCacheMu.Lock()
	h.optionsCache = &OptionsResponse{
		Rigs: []string{"rig-a", "rig-b"},
		Crew: []string{"alice"},
	}
	h.optionsCacheTime = time.Now()
	h.optionsCacheMu.Unlock()

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)

	// Half readers, half writers — run with -race to detect data races.
	for i := 0; i < goroutines; i++ {
		if i%2 == 0 {
			go func() {
				defer wg.Done()
				req := httptest.NewRequest(http.MethodGet, "/api/options", nil)
				w := httptest.NewRecorder()
				h.handleOptions(w, req)
				if w.Code != http.StatusOK {
					t.Errorf("handleOptions returned %d", w.Code)
				}
			}()
		} else {
			go func(n int) {
				defer wg.Done()
				h.optionsCacheMu.Lock()
				h.optionsCache = &OptionsResponse{
					Rigs: []string{strings.Repeat("x", n)},
				}
				h.optionsCacheTime = time.Now()
				h.optionsCacheMu.Unlock()
			}(i)
		}
	}

	wg.Wait()
}

func TestParseRigListJSON(t *testing.T) {
	tests := []struct {
		name string
		json string
		want []string
	}{
		{
			name: "valid JSON with rigs",
			json: `[{"name":"alpha","status":"operational"},{"name":"greenplace","status":"operational"}]`,
			want: []string{"alpha", "greenplace"},
		},
		{
			name: "empty array",
			json: `[]`,
			want: []string{},
		},
		{
			name: "invalid JSON",
			json: `{not valid`,
			want: nil,
		},
		{
			name: "skips empty names",
			json: `[{"name":"alpha"},{"name":""},{"name":"greenplace"}]`,
			want: []string{"alpha", "greenplace"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseRigListJSON(tt.json)
			if tt.want == nil {
				if got != nil {
					t.Errorf("parseRigListJSON(%q) = %v, want nil", tt.json, got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Errorf("parseRigListJSON(%q) = %v, want %v", tt.json, got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("parseRigListJSON(%q)[%d] = %q, want %q", tt.json, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestHandleOptionsUsesRigListJSON(t *testing.T) {
	binDir := t.TempDir()
	gtPath := filepath.Join(binDir, "gt")
	bdPath := filepath.Join(binDir, "bd")

	gtScript := `#!/usr/bin/env sh
set -eu
case "$*" in
  "rig list --json")
    printf '[{"name":"alpha","status":"operational"},{"name":"greenplace","status":"operational"}]\n'
    ;;
  "polecat list --all --json")
    printf '[]\n'
    ;;
  "hooks list")
    printf '\n'
    ;;
  "mail inbox")
    printf '\n'
    ;;
  "crew list --all")
    printf '\n'
    ;;
  "status --json")
    printf '{"agents":[]}\n'
    ;;
  *)
    printf 'unexpected gt args: %s\n' "$*" >&2
    exit 2
    ;;
esac
`
	bdScript := `#!/usr/bin/env sh
set -eu
case "$*" in
  "list --type=convoy --json")
    printf '[]\n'
    ;;
  *)
    printf 'unexpected bd args: %s\n' "$*" >&2
    exit 2
    ;;
esac
`

	if err := os.WriteFile(gtPath, []byte(gtScript), 0o755); err != nil {
		t.Fatalf("write fake gt: %v", err)
	}
	if err := os.WriteFile(bdPath, []byte(bdScript), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	h := &APIHandler{
		gtPath:            gtPath,
		workDir:           t.TempDir(),
		defaultRunTimeout: 5 * time.Second,
		maxRunTimeout:     10 * time.Second,
		cmdSem:            make(chan struct{}, maxConcurrentCommands),
	}

	req := httptest.NewRequest(http.MethodGet, "/api/options?type=rigs", nil)
	w := httptest.NewRecorder()
	h.handleOptions(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("handleOptions returned status %d: %s", w.Code, w.Body.String())
	}
	var resp OptionsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v\nbody=%s", err, w.Body.String())
	}
	want := []string{"alpha", "greenplace"}
	if len(resp.Rigs) != len(want) {
		t.Fatalf("Rigs = %v, want %v", resp.Rigs, want)
	}
	for i := range want {
		if resp.Rigs[i] != want[i] {
			t.Fatalf("Rigs[%d] = %q, want %q; full=%v", i, resp.Rigs[i], want[i], resp.Rigs)
		}
	}
}

func TestParseConvoyListJSON(t *testing.T) {
	tests := []struct {
		name string
		json string
		want []string
	}{
		{
			name: "valid JSON with convoys",
			json: `[{"id":"hq-cv-abc","title":"Deploy widgets"},{"id":"hq-cv-def","title":"Fix bugs"}]`,
			want: []string{"hq-cv-abc", "hq-cv-def"},
		},
		{
			name: "empty array",
			json: `[]`,
			want: []string{},
		},
		{
			name: "invalid JSON",
			json: `{not valid`,
			want: nil,
		},
		{
			name: "empty string",
			json: ``,
			want: nil,
		},
		{
			name: "skips empty IDs",
			json: `[{"id":"hq-cv-abc"},{"id":""},{"id":"hq-cv-def"}]`,
			want: []string{"hq-cv-abc", "hq-cv-def"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseConvoyListJSON(tt.json)
			if tt.want == nil {
				if got != nil {
					t.Errorf("parseConvoyListJSON(%q) = %v, want nil", tt.json, got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Errorf("parseConvoyListJSON(%q) = %v, want %v", tt.json, got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("parseConvoyListJSON(%q)[%d] = %q, want %q", tt.json, i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestRunGtCommandSemaphore verifies that runGtCommand limits concurrent
// command execution via a semaphore. With a 1-slot semaphore and 3 commands
// each sleeping 0.1s, total time must be >= 0.25s (serialized), proving the
// semaphore prevents all 3 from running simultaneously (~0.1s).
//
// Regression test for steveyegge/gastown#1230 item 5.
func TestRunGtCommandSemaphore(t *testing.T) {
	// Create handler with a 1-slot semaphore — fully serialized execution.
	h := &APIHandler{
		gtPath:            "sleep",
		workDir:           t.TempDir(),
		defaultRunTimeout: 5 * time.Second,
		maxRunTimeout:     10 * time.Second,
		cmdSem:            make(chan struct{}, 1),
	}

	const numCmds = 3
	var wg sync.WaitGroup
	wg.Add(numCmds)

	start := time.Now()
	for i := 0; i < numCmds; i++ {
		go func() {
			defer wg.Done()
			_, _ = h.runGtCommand(context.Background(), 2*time.Second, []string{"0.1"})
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	// With 1-slot semaphore, 3 x 0.1s sleeps must run serially: >= 0.25s.
	// Without semaphore they'd all run in ~0.1s.
	if elapsed < 250*time.Millisecond {
		t.Errorf("elapsed = %v, want >= 250ms (commands should be serialized by semaphore)", elapsed)
	}
}

// TestRunGtCommandSemaphoreContextCancel verifies that a cancelled context
// returns immediately instead of blocking on a full semaphore.
//
// Regression test for steveyegge/gastown#1230 item 5.
func TestRunGtCommandSemaphoreContextCancel(t *testing.T) {
	h := &APIHandler{
		gtPath:            "sleep",
		workDir:           t.TempDir(),
		defaultRunTimeout: 5 * time.Second,
		maxRunTimeout:     10 * time.Second,
		cmdSem:            make(chan struct{}, 1), // 1 slot
	}

	// Fill the semaphore.
	h.cmdSem <- struct{}{}

	// Try to run a command with a context that expires quickly.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := h.runGtCommand(ctx, 5*time.Second, []string{"10"})
	if err == nil {
		t.Fatal("expected error when semaphore full and context cancelled")
	}
	if !strings.Contains(err.Error(), "context") {
		t.Errorf("error = %q, want context-related error", err)
	}

	// Drain the slot we manually added.
	<-h.cmdSem
}

// TestRunGtCommandSemaphoreTimeoutBudget verifies that the timeout parameter
// bounds total latency including semaphore wait time. A call with a short
// timeout should fail within that budget even if the semaphore is full.
//
// Regression test: timeout context must be created before semaphore acquisition.
func TestRunGtCommandSemaphoreTimeoutBudget(t *testing.T) {
	h := &APIHandler{
		gtPath:            "sleep",
		workDir:           t.TempDir(),
		defaultRunTimeout: 5 * time.Second,
		maxRunTimeout:     10 * time.Second,
		cmdSem:            make(chan struct{}, 1), // 1 slot
	}

	// Fill the semaphore so the call must wait.
	h.cmdSem <- struct{}{}

	start := time.Now()
	// Use a background context (no external deadline) but a short timeout.
	// The timeout should bound the semaphore wait.
	_, err := h.runGtCommand(context.Background(), 200*time.Millisecond, []string{"10"})
	elapsed := time.Since(start)

	// Drain the slot we manually added.
	<-h.cmdSem

	if err == nil {
		t.Fatal("expected error when semaphore full and timeout expires")
	}
	if !strings.Contains(err.Error(), "command slot unavailable") {
		t.Errorf("error = %q, want 'command slot unavailable'", err)
	}
	// The call should have returned within the timeout budget (200ms + margin).
	if elapsed > 500*time.Millisecond {
		t.Errorf("elapsed = %v, want < 500ms (timeout should bound semaphore wait)", elapsed)
	}
}

// TestHandleSessionPreviewPrefixValidation verifies that handleSessionPreview
// accepts session names with known rig prefixes and rejects invalid prefixes.
func TestHandleSessionPreviewPrefixValidation(t *testing.T) {
	originalRegistry := session.DefaultRegistry()
	t.Cleanup(func() { session.SetDefaultRegistry(originalRegistry) })

	testRegistry := session.NewPrefixRegistry()
	testRegistry.Register("nx", "nexus")
	testRegistry.Register("myrig", "myrig-project")
	session.SetDefaultRegistry(testRegistry)

	h := &APIHandler{
		gtPath:            "true",
		workDir:           t.TempDir(),
		defaultRunTimeout: 1 * time.Second,
		maxRunTimeout:     2 * time.Second,
		cmdSem:            make(chan struct{}, 5),
	}

	tests := []struct {
		name          string
		sessionName   string
		wantRejected  bool
		wantErrSubstr string
	}{
		{"registered prefix nx", "nx-polecat-alpha", false, ""},
		{"registered prefix myrig", "myrig-crew-bob", false, ""},
		{"legacy gt- prefix", "gt-polecat-test", false, ""},
		{"legacy bd- prefix", "bd-some-bead", false, ""},
		{"hq- prefix", "hq-nonexistent-session", false, ""},
		{"gthq- prefix", "gthq-deacon", false, ""},
		{"unknown prefix rejected", "unknown-session-name", true, "must start with a known rig prefix"},
		{"no prefix rejected", "justsomename", true, "must start with a known rig prefix"},
		{"invalid characters rejected", "gt-bad_chars!", true, "invalid characters"},
		{"missing session parameter", "", true, "Missing session parameter"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			url := "/api/session-preview"
			if tc.sessionName != "" {
				url += "?session=" + tc.sessionName
			}
			req := httptest.NewRequest(http.MethodGet, url, nil)
			rec := httptest.NewRecorder()

			h.handleSessionPreview(rec, req)

			if tc.wantRejected {
				if rec.Code != http.StatusBadRequest {
					t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
				}
				if !strings.Contains(rec.Body.String(), tc.wantErrSubstr) {
					t.Errorf("body = %q, want substring %q", rec.Body.String(), tc.wantErrSubstr)
				}
			} else {
				if rec.Code == http.StatusBadRequest && strings.Contains(rec.Body.String(), "prefix") {
					t.Errorf("valid prefix %q was rejected: %s", tc.sessionName, rec.Body.String())
				}
			}
		})
	}
}

func TestPaneCurrentCommandIsAgent(t *testing.T) {
	tests := []struct {
		cmd  string
		want bool
	}{
		{"claude", true},
		{"node", true},
		{"codex", true},
		{"opencode", true},
		{"cursor-agent", true},
		{"copilot", true},
		{"agent", true},
		{"bash", false},
		{"", false},
		{"   ", false},
	}
	for _, tt := range tests {
		if got := paneCurrentCommandIsAgent(tt.cmd); got != tt.want {
			t.Errorf("paneCurrentCommandIsAgent(%q) = %v, want %v", tt.cmd, got, tt.want)
		}
	}
}
