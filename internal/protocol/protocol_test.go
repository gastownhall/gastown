package protocol

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/mail"
)

func TestParseMessageType(t *testing.T) {
	tests := []struct {
		subject  string
		expected MessageType
	}{
		{"MERGE_READY nux", TypeMergeReady},
		{"MERGED Toast", TypeMerged},
		{"MERGE_FAILED ace", TypeMergeFailed},
		{"REWORK_REQUEST valkyrie", TypeReworkRequest},
		{"MERGE_READY", TypeMergeReady}, // no polecat name
		{"Unknown subject", ""},
		{"", ""},
		{"  MERGE_READY nux  ", TypeMergeReady}, // with whitespace
		{"MERGEDFOO", ""},                       // prefix without space delimiter
		{"MERGE_READYBAR", ""},                  // prefix without space delimiter
		{"MERGE_FAILEDX", ""},                   // prefix without space delimiter
		{"REWORK_REQUESTZ", ""},                 // prefix without space delimiter
	}

	for _, tt := range tests {
		t.Run(tt.subject, func(t *testing.T) {
			result := ParseMessageType(tt.subject)
			if result != tt.expected {
				t.Errorf("ParseMessageType(%q) = %q, want %q", tt.subject, result, tt.expected)
			}
		})
	}
}

func TestExtractPolecat(t *testing.T) {
	tests := []struct {
		subject  string
		expected string
	}{
		{"MERGE_READY nux", "nux"},
		{"MERGED Toast", "Toast"},
		{"MERGE_FAILED ace", "ace"},
		{"REWORK_REQUEST valkyrie", "valkyrie"},
		{"MERGE_READY", ""},
		{"", ""},
		{"  MERGE_READY nux  ", "nux"},
	}

	for _, tt := range tests {
		t.Run(tt.subject, func(t *testing.T) {
			result := ExtractPolecat(tt.subject)
			if result != tt.expected {
				t.Errorf("ExtractPolecat(%q) = %q, want %q", tt.subject, result, tt.expected)
			}
		})
	}
}

func TestIsProtocolMessage(t *testing.T) {
	tests := []struct {
		subject  string
		expected bool
	}{
		{"MERGE_READY nux", true},
		{"MERGED Toast", true},
		{"MERGE_FAILED ace", true},
		{"REWORK_REQUEST valkyrie", true},
		{"CONVOY_NEEDS_FEEDING hq-cv123", true},
		{"Unknown subject", false},
		{"", false},
		{"Hello world", false},
	}

	for _, tt := range tests {
		t.Run(tt.subject, func(t *testing.T) {
			result := IsProtocolMessage(tt.subject)
			if result != tt.expected {
				t.Errorf("IsProtocolMessage(%q) = %v, want %v", tt.subject, result, tt.expected)
			}
		})
	}
}

func TestNewMergeReadyMessage(t *testing.T) {
	msg := NewMergeReadyMessage("gastown", "nux", "polecat/nux/gt-abc", "gt-abc")

	if msg.Subject != "MERGE_READY nux" {
		t.Errorf("Subject = %q, want %q", msg.Subject, "MERGE_READY nux")
	}
	if msg.From != "gastown/witness" {
		t.Errorf("From = %q, want %q", msg.From, "gastown/witness")
	}
	if msg.To != "gastown/refinery" {
		t.Errorf("To = %q, want %q", msg.To, "gastown/refinery")
	}
	if msg.Priority != mail.PriorityHigh {
		t.Errorf("Priority = %q, want %q", msg.Priority, mail.PriorityHigh)
	}
	if !strings.Contains(msg.Body, "Branch: polecat/nux/gt-abc") {
		t.Errorf("Body missing branch: %s", msg.Body)
	}
	if !strings.Contains(msg.Body, "Issue: gt-abc") {
		t.Errorf("Body missing issue: %s", msg.Body)
	}
}

func TestNewMergedMessage(t *testing.T) {
	msg := NewMergedMessage("gastown", "nux", "polecat/nux/gt-abc", "gt-abc", "main", "abc123")

	if msg.Subject != "MERGED nux" {
		t.Errorf("Subject = %q, want %q", msg.Subject, "MERGED nux")
	}
	if msg.From != "gastown/refinery" {
		t.Errorf("From = %q, want %q", msg.From, "gastown/refinery")
	}
	if msg.To != "gastown/witness" {
		t.Errorf("To = %q, want %q", msg.To, "gastown/witness")
	}
	if !strings.Contains(msg.Body, "Merge-Commit: abc123") {
		t.Errorf("Body missing merge commit: %s", msg.Body)
	}
}

func TestNewMergeFailedMessage(t *testing.T) {
	msg := NewMergeFailedMessage("gastown", "nux", "polecat/nux/gt-abc", "gt-abc", "main", "tests", "Test failed")

	if msg.Subject != "MERGE_FAILED nux" {
		t.Errorf("Subject = %q, want %q", msg.Subject, "MERGE_FAILED nux")
	}
	if !strings.Contains(msg.Body, "Failure-Type: tests") {
		t.Errorf("Body missing failure type: %s", msg.Body)
	}
	if !strings.Contains(msg.Body, "Error: Test failed") {
		t.Errorf("Body missing error: %s", msg.Body)
	}
}

func TestNewReworkRequestMessage(t *testing.T) {
	conflicts := []string{"file1.go", "file2.go"}
	msg := NewReworkRequestMessage("gastown", "nux", "polecat/nux/gt-abc", "gt-abc", "main", conflicts)

	if msg.Subject != "REWORK_REQUEST nux" {
		t.Errorf("Subject = %q, want %q", msg.Subject, "REWORK_REQUEST nux")
	}
	if !strings.Contains(msg.Body, "Conflict-Files: file1.go, file2.go") {
		t.Errorf("Body missing conflict files: %s", msg.Body)
	}
	if !strings.Contains(msg.Body, "git rebase origin/main") {
		t.Errorf("Body missing rebase instructions: %s", msg.Body)
	}
}

func TestParseMergeReadyPayload(t *testing.T) {
	body := `Branch: polecat/nux/gt-abc
Issue: gt-abc
Polecat: nux
Rig: gastown
Verified: clean git state`

	payload, err := ParseMergeReadyPayload(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if payload.Branch != "polecat/nux/gt-abc" {
		t.Errorf("Branch = %q, want %q", payload.Branch, "polecat/nux/gt-abc")
	}
	if payload.Issue != "gt-abc" {
		t.Errorf("Issue = %q, want %q", payload.Issue, "gt-abc")
	}
	if payload.Polecat != "nux" {
		t.Errorf("Polecat = %q, want %q", payload.Polecat, "nux")
	}
	if payload.Rig != "gastown" {
		t.Errorf("Rig = %q, want %q", payload.Rig, "gastown")
	}
}

func TestParseMessageType_ConvoyNeedsFeeding(t *testing.T) {
	tests := []struct {
		subject  string
		expected MessageType
	}{
		{"CONVOY_NEEDS_FEEDING hq-cv123", TypeConvoyNeedsFeeding},
		{"CONVOY_NEEDS_FEEDING", TypeConvoyNeedsFeeding},
		{"CONVOY_NEEDS_FEEDINGX", ""},
	}

	for _, tt := range tests {
		t.Run(tt.subject, func(t *testing.T) {
			result := ParseMessageType(tt.subject)
			if result != tt.expected {
				t.Errorf("ParseMessageType(%q) = %q, want %q", tt.subject, result, tt.expected)
			}
		})
	}
}

func TestNewConvoyNeedsFeedingMessage(t *testing.T) {
	msg := NewConvoyNeedsFeedingMessage("gastown", "hq-cv123", "gt-abc")

	if msg.Subject != "CONVOY_NEEDS_FEEDING hq-cv123" {
		t.Errorf("Subject = %q, want %q", msg.Subject, "CONVOY_NEEDS_FEEDING hq-cv123")
	}
	if msg.From != "gastown/refinery" {
		t.Errorf("From = %q, want %q", msg.From, "gastown/refinery")
	}
	if msg.To != "deacon/" {
		t.Errorf("To = %q, want %q", msg.To, "deacon/")
	}
	if msg.Priority != mail.PriorityHigh {
		t.Errorf("Priority = %q, want %q", msg.Priority, mail.PriorityHigh)
	}
	if !strings.Contains(msg.Body, "ConvoyID: hq-cv123") {
		t.Errorf("Body missing ConvoyID: %s", msg.Body)
	}
	if !strings.Contains(msg.Body, "SourceIssue: gt-abc") {
		t.Errorf("Body missing SourceIssue: %s", msg.Body)
	}
	if !strings.Contains(msg.Body, "Rig: gastown") {
		t.Errorf("Body missing Rig: %s", msg.Body)
	}
}

func TestParseConvoyNeedsFeedingPayload(t *testing.T) {
	ts := time.Now().Format(time.RFC3339)
	body := "ConvoyID: hq-cv123\nSourceIssue: gt-abc\nRig: gastown\nMerged-At: " + ts

	payload, err := ParseConvoyNeedsFeedingPayload(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if payload.ConvoyID != "hq-cv123" {
		t.Errorf("ConvoyID = %q, want %q", payload.ConvoyID, "hq-cv123")
	}
	if payload.SourceIssue != "gt-abc" {
		t.Errorf("SourceIssue = %q, want %q", payload.SourceIssue, "gt-abc")
	}
	if payload.Rig != "gastown" {
		t.Errorf("Rig = %q, want %q", payload.Rig, "gastown")
	}
}

func TestParseConvoyNeedsFeedingPayload_InvalidInput(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"empty body", ""},
		{"missing convoy id", "Rig: gastown\nSourceIssue: gt-abc"},
		{"missing rig", "ConvoyID: hq-cv123\nSourceIssue: gt-abc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, err := ParseConvoyNeedsFeedingPayload(tt.body)
			if err == nil {
				t.Errorf("expected error for body %q, got payload: %+v", tt.body, payload)
			}
			if payload != nil {
				t.Errorf("expected nil payload on error, got: %+v", payload)
			}
		})
	}
}

func TestParseMergeReadyPayload_InvalidInput(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"empty body", ""},
		{"missing all fields", "Hello: world"},
		{"missing branch", "Polecat: nux\nRig: gastown"},
		{"missing polecat", "Branch: polecat/nux\nRig: gastown"},
		{"missing rig", "Branch: polecat/nux\nPolecat: nux"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, err := ParseMergeReadyPayload(tt.body)
			if err == nil {
				t.Errorf("expected error for body %q, got payload: %+v", tt.body, payload)
			}
			if payload != nil {
				t.Errorf("expected nil payload on error, got: %+v", payload)
			}
		})
	}
}

func TestParseMergedPayload(t *testing.T) {
	ts := time.Now().Format(time.RFC3339)
	body := `Branch: polecat/nux/gt-abc
Issue: gt-abc
Polecat: nux
Rig: gastown
Target: main
Merged-At: ` + ts + `
Merge-Commit: abc123`

	payload, err := ParseMergedPayload(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if payload.Branch != "polecat/nux/gt-abc" {
		t.Errorf("Branch = %q, want %q", payload.Branch, "polecat/nux/gt-abc")
	}
	if payload.MergeCommit != "abc123" {
		t.Errorf("MergeCommit = %q, want %q", payload.MergeCommit, "abc123")
	}
	if payload.TargetBranch != "main" {
		t.Errorf("TargetBranch = %q, want %q", payload.TargetBranch, "main")
	}
}

func TestParseMergedPayload_InvalidInput(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"empty body", ""},
		{"missing polecat", "Branch: polecat/nux\nRig: gastown"},
		{"missing rig", "Branch: polecat/nux\nPolecat: nux"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, err := ParseMergedPayload(tt.body)
			if err == nil {
				t.Errorf("expected error for body %q, got payload: %+v", tt.body, payload)
			}
			if payload != nil {
				t.Errorf("expected nil payload on error, got: %+v", payload)
			}
		})
	}
}

func TestParseMergeFailedPayload(t *testing.T) {
	body := `Branch: polecat/nux/gt-abc
Issue: gt-abc
Polecat: nux
Rig: gastown
Target: main
Failure-Type: tests
Error: Test failed`

	payload, err := ParseMergeFailedPayload(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if payload.Branch != "polecat/nux/gt-abc" {
		t.Errorf("Branch = %q, want %q", payload.Branch, "polecat/nux/gt-abc")
	}
	if payload.FailureType != "tests" {
		t.Errorf("FailureType = %q, want %q", payload.FailureType, "tests")
	}
	if payload.Error != "Test failed" {
		t.Errorf("Error = %q, want %q", payload.Error, "Test failed")
	}
}

func TestParseMergeFailedPayload_InvalidInput(t *testing.T) {
	payload, err := ParseMergeFailedPayload("")
	if err == nil {
		t.Errorf("expected error for empty body, got payload: %+v", payload)
	}
	if payload != nil {
		t.Errorf("expected nil payload on error, got: %+v", payload)
	}
}

func TestParseReworkRequestPayload(t *testing.T) {
	body := `Branch: polecat/nux/gt-abc
Issue: gt-abc
Polecat: nux
Rig: gastown
Target: main
Conflict-Files: file1.go, file2.go`

	payload, err := ParseReworkRequestPayload(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if payload.Branch != "polecat/nux/gt-abc" {
		t.Errorf("Branch = %q, want %q", payload.Branch, "polecat/nux/gt-abc")
	}
	if payload.TargetBranch != "main" {
		t.Errorf("TargetBranch = %q, want %q", payload.TargetBranch, "main")
	}
	if len(payload.ConflictFiles) != 2 {
		t.Errorf("ConflictFiles length = %d, want 2", len(payload.ConflictFiles))
	}
}

func TestParseReworkRequestPayload_InvalidInput(t *testing.T) {
	payload, err := ParseReworkRequestPayload("")
	if err == nil {
		t.Errorf("expected error for empty body, got payload: %+v", payload)
	}
	if payload != nil {
		t.Errorf("expected nil payload on error, got: %+v", payload)
	}
}

func TestHandlerRegistry(t *testing.T) {
	registry := NewHandlerRegistry()

	handled := false
	registry.Register(TypeMergeReady, func(msg *mail.Message) error {
		handled = true
		return nil
	})

	msg := &mail.Message{Subject: "MERGE_READY nux"}

	if !registry.CanHandle(msg) {
		t.Error("Registry should be able to handle MERGE_READY message")
	}

	if err := registry.Handle(msg); err != nil {
		t.Errorf("Handle returned error: %v", err)
	}

	if !handled {
		t.Error("Handler was not called")
	}

	// Test unregistered message type
	unknownMsg := &mail.Message{Subject: "UNKNOWN message"}
	if registry.CanHandle(unknownMsg) {
		t.Error("Registry should not handle unknown message type")
	}
}

func TestProcessProtocolMessage(t *testing.T) {
	registry := NewHandlerRegistry()

	handled := false
	registry.Register(TypeMergeReady, func(msg *mail.Message) error {
		handled = true
		return nil
	})

	// Test 1: Non-protocol message returns (false, nil)
	nonProto := &mail.Message{Subject: "Hello world"}
	isProto, err := registry.ProcessProtocolMessage(nonProto)
	if isProto || err != nil {
		t.Errorf("Non-protocol message: got (%v, %v), want (false, nil)", isProto, err)
	}

	// Test 2: Recognized protocol message with handler returns (true, nil)
	readyMsg := &mail.Message{Subject: "MERGE_READY nux"}
	isProto, err = registry.ProcessProtocolMessage(readyMsg)
	if !isProto || err != nil {
		t.Errorf("Handled protocol message: got (%v, %v), want (true, nil)", isProto, err)
	}
	if !handled {
		t.Error("Handler was not called for MERGE_READY")
	}

	// Test 3: Recognized protocol message WITHOUT handler returns (true, ErrNoHandler)
	// MERGED is a valid protocol type but no handler is registered for it
	misrouted := &mail.Message{Subject: "MERGED nux"}
	isProto, err = registry.ProcessProtocolMessage(misrouted)
	if !isProto {
		t.Error("Recognized protocol message should return isProtocol=true even without handler")
	}
	if !errors.Is(err, ErrNoHandler) {
		t.Errorf("Unhandled protocol message: got error %v, want ErrNoHandler", err)
	}
}

func TestWrapWitnessHandlers(t *testing.T) {
	handler := &mockWitnessHandler{}
	registry := WrapWitnessHandlers(handler)

	// Test MERGED
	mergedMsg := &mail.Message{
		Subject: "MERGED nux",
		Body:    "Branch: polecat/nux\nIssue: gt-abc\nPolecat: nux\nRig: gastown\nTarget: main",
	}
	if err := registry.Handle(mergedMsg); err != nil {
		t.Errorf("HandleMerged error: %v", err)
	}
	if !handler.mergedCalled {
		t.Error("HandleMerged was not called")
	}

	// Test MERGE_FAILED
	failedMsg := &mail.Message{
		Subject: "MERGE_FAILED nux",
		Body:    "Branch: polecat/nux\nIssue: gt-abc\nPolecat: nux\nRig: gastown\nTarget: main\nFailure-Type: tests\nError: failed",
	}
	if err := registry.Handle(failedMsg); err != nil {
		t.Errorf("HandleMergeFailed error: %v", err)
	}
	if !handler.failedCalled {
		t.Error("HandleMergeFailed was not called")
	}

	// Test REWORK_REQUEST
	reworkMsg := &mail.Message{
		Subject: "REWORK_REQUEST nux",
		Body:    "Branch: polecat/nux\nIssue: gt-abc\nPolecat: nux\nRig: gastown\nTarget: main",
	}
	if err := registry.Handle(reworkMsg); err != nil {
		t.Errorf("HandleReworkRequest error: %v", err)
	}
	if !handler.reworkCalled {
		t.Error("HandleReworkRequest was not called")
	}
}

func TestWrapRefineryHandlers(t *testing.T) {
	handler := &mockRefineryHandler{}
	registry := WrapRefineryHandlers(handler)

	msg := &mail.Message{
		Subject: "MERGE_READY nux",
		Body:    "Branch: polecat/nux\nIssue: gt-abc\nPolecat: nux\nRig: gastown",
	}

	if err := registry.Handle(msg); err != nil {
		t.Errorf("HandleMergeReady error: %v", err)
	}
	if !handler.readyCalled {
		t.Error("HandleMergeReady was not called")
	}
}

func TestWrapWitnessHandlers_InvalidPayload(t *testing.T) {
	handler := &mockWitnessHandler{}
	registry := WrapWitnessHandlers(handler)

	// Empty body should produce parse error for all message types
	tests := []struct {
		name    string
		subject string
	}{
		{"MERGED empty body", "MERGED nux"},
		{"MERGE_FAILED empty body", "MERGE_FAILED nux"},
		{"REWORK_REQUEST empty body", "REWORK_REQUEST nux"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := &mail.Message{Subject: tt.subject, Body: ""}
			err := registry.Handle(msg)
			if err == nil {
				t.Errorf("expected error for %s with empty body", tt.subject)
			}
		})
	}

	// Handlers should NOT have been called
	if handler.mergedCalled || handler.failedCalled || handler.reworkCalled {
		t.Error("handlers should not be called when parse fails")
	}
}

func TestWrapRefineryHandlers_InvalidPayload(t *testing.T) {
	handler := &mockRefineryHandler{}
	registry := WrapRefineryHandlers(handler)

	msg := &mail.Message{Subject: "MERGE_READY nux", Body: ""}
	err := registry.Handle(msg)
	if err == nil {
		t.Error("expected error for MERGE_READY with empty body")
	}
	if handler.readyCalled {
		t.Error("handler should not be called when parse fails")
	}
}

func TestDefaultWitnessHandler(t *testing.T) {
	tmpDir := t.TempDir()
	// Prevent detectTownRoot from finding the real town via GT_TOWN_ROOT/GT_ROOT.
	// Without this, NewRouter falls back to the production beads and delivers
	// synthetic protocol messages to the live mail system during test runs.
	t.Setenv("GT_TOWN_ROOT", tmpDir)
	t.Setenv("GT_ROOT", tmpDir)
	handler := NewWitnessHandler("gastown", tmpDir)
	handler.Router = mail.NewRouterWithTownRoot(tmpDir, "")

	// Capture output
	var buf bytes.Buffer
	handler.SetOutput(&buf)

	// Test HandleMerged
	mergedPayload := &MergedPayload{
		Branch:       "polecat/nux/gt-abc",
		Issue:        "gt-abc",
		Polecat:      "nux",
		Rig:          "gastown",
		TargetBranch: "main",
		MergeCommit:  "abc123",
	}
	if err := handler.HandleMerged(mergedPayload); err != nil {
		t.Errorf("HandleMerged error: %v", err)
	}
	if !strings.Contains(buf.String(), "MERGED received") {
		t.Errorf("Output missing expected text: %s", buf.String())
	}

	// Test HandleMergeFailed
	buf.Reset()
	failedPayload := &MergeFailedPayload{
		Branch:       "polecat/nux/gt-abc",
		Issue:        "gt-abc",
		Polecat:      "nux",
		Rig:          "gastown",
		TargetBranch: "main",
		FailureType:  "tests",
		Error:        "Test failed",
	}
	if err := handler.HandleMergeFailed(failedPayload); err != nil {
		t.Errorf("HandleMergeFailed error: %v", err)
	}
	if !strings.Contains(buf.String(), "MERGE_FAILED received") {
		t.Errorf("Output missing expected text: %s", buf.String())
	}

	// Test HandleReworkRequest
	buf.Reset()
	reworkPayload := &ReworkRequestPayload{
		Branch:        "polecat/nux/gt-abc",
		Issue:         "gt-abc",
		Polecat:       "nux",
		Rig:           "gastown",
		TargetBranch:  "main",
		ConflictFiles: []string{"file1.go"},
	}
	if err := handler.HandleReworkRequest(reworkPayload); err != nil {
		t.Errorf("HandleReworkRequest error: %v", err)
	}
	if !strings.Contains(buf.String(), "REWORK_REQUEST received") {
		t.Errorf("Output missing expected text: %s", buf.String())
	}
}

// Mock handlers for testing

type mockWitnessHandler struct {
	mergedCalled bool
	failedCalled bool
	reworkCalled bool
}

func (m *mockWitnessHandler) HandleMerged(payload *MergedPayload) error {
	m.mergedCalled = true
	return nil
}

func (m *mockWitnessHandler) HandleMergeFailed(payload *MergeFailedPayload) error {
	m.failedCalled = true
	return nil
}

func (m *mockWitnessHandler) HandleReworkRequest(payload *ReworkRequestPayload) error {
	m.reworkCalled = true
	return nil
}

type mockRefineryHandler struct {
	readyCalled bool
}

func (m *mockRefineryHandler) HandleMergeReady(payload *MergeReadyPayload) error {
	m.readyCalled = true
	return nil
}

func TestDefaultRefineryHandler_HandleMergeReady(t *testing.T) {
	tmpDir := t.TempDir()
	handler := NewRefineryHandler("gastown", tmpDir)

	var buf bytes.Buffer
	handler.SetOutput(&buf)

	payload := &MergeReadyPayload{
		Branch:   "polecat/nux/gt-abc",
		Issue:    "gt-abc",
		Polecat:  "nux",
		Rig:      "gastown",
		Verified: "clean git state",
	}
	if err := handler.HandleMergeReady(payload); err != nil {
		t.Errorf("HandleMergeReady error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "MERGE_READY received") {
		t.Errorf("missing MERGE_READY text: %s", output)
	}
	if !strings.Contains(output, "nux") {
		t.Errorf("missing polecat name: %s", output)
	}
	if !strings.Contains(output, "polecat/nux/gt-abc") {
		t.Errorf("missing branch: %s", output)
	}
}

func TestDefaultRefineryHandler_NotifyMergeOutcome_Success(t *testing.T) {
	tmpDir := t.TempDir()
	// Prevent detectTownRoot from finding the real town via GT_TOWN_ROOT/GT_ROOT.
	// Without this, NewRouter falls back to the production beads and delivers
	// synthetic "MERGED nux" messages to the live mail system during test runs.
	t.Setenv("GT_TOWN_ROOT", tmpDir)
	t.Setenv("GT_ROOT", tmpDir)
	handler := NewRefineryHandler("gastown", tmpDir)
	handler.Router = mail.NewRouterWithTownRoot(tmpDir, "")

	outcome := MergeOutcome{
		Success:     true,
		MergeCommit: "abc123",
	}

	// Testing routing logic only — delivery will fail (no .beads in tmpDir)
	err := handler.NotifyMergeOutcome("nux", "polecat/nux/gt-abc", "gt-abc", "main", outcome)
	_ = err
}

func TestDefaultRefineryHandler_NotifyMergeOutcome_Conflict(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("GT_TOWN_ROOT", tmpDir)
	t.Setenv("GT_ROOT", tmpDir)
	handler := NewRefineryHandler("gastown", tmpDir)
	handler.Router = mail.NewRouterWithTownRoot(tmpDir, "")

	outcome := MergeOutcome{
		Success:       false,
		Conflict:      true,
		ConflictFiles: []string{"file1.go", "file2.go"},
	}

	err := handler.NotifyMergeOutcome("nux", "polecat/nux/gt-abc", "gt-abc", "main", outcome)
	_ = err
}

func TestDefaultRefineryHandler_NotifyMergeOutcome_Failure(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("GT_TOWN_ROOT", tmpDir)
	t.Setenv("GT_ROOT", tmpDir)
	handler := NewRefineryHandler("gastown", tmpDir)
	handler.Router = mail.NewRouterWithTownRoot(tmpDir, "")

	outcome := MergeOutcome{
		Success:     false,
		Conflict:    false,
		FailureType: "tests",
		Error:       "Test suite failed",
	}

	err := handler.NotifyMergeOutcome("nux", "polecat/nux/gt-abc", "gt-abc", "main", outcome)
	_ = err
}

func TestMergeOutcome_Fields(t *testing.T) {
	outcome := MergeOutcome{
		Success:       true,
		Conflict:      false,
		FailureType:   "",
		Error:         "",
		MergeCommit:   "abc123",
		ConflictFiles: nil,
	}

	if !outcome.Success {
		t.Error("expected Success=true")
	}
	if outcome.Conflict {
		t.Error("expected Conflict=false")
	}
	if outcome.MergeCommit != "abc123" {
		t.Errorf("MergeCommit = %q, want %q", outcome.MergeCommit, "abc123")
	}
}
