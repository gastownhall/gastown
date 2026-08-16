package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jonbaldie/gastown/internal/nudge"
)

func TestNewPropeller(t *testing.T) {
	proxy := NewProxy()
	prop := NewPropeller(proxy, "/town", "hq-mayor")

	if prop.proxy != proxy {
		t.Error("proxy not set correctly")
	}
	if prop.townRoot != "/town" {
		t.Error("townRoot not set correctly")
	}
	if prop.session != "hq-mayor" {
		t.Error("session not set correctly")
	}
}

func TestPropeller_StartStop(t *testing.T) {
	prop := NewPropeller(nil, "", "hq-mayor")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	prop.Start(ctx)

	time.Sleep(100 * time.Millisecond)

	prop.Stop()
}

func TestPropeller_DeliverNudges_NoProxy(t *testing.T) {
	// Test that deliverNudges handles nil proxy gracefully
	prop := NewPropeller(nil, "/town", "hq-mayor")
	prop.deliverNudges() // Should not panic
}

func TestPropeller_EventLoop_Cancellation(t *testing.T) {
	// Test that eventLoop exits on context cancellation
	prop := NewPropeller(nil, "/town", "hq-mayor")

	ctx, cancel := context.WithCancel(context.Background())
	prop.ctx = ctx
	prop.cancel = cancel

	// Start eventLoop in a goroutine
	done := make(chan struct{})
	go func() {
		prop.eventLoop()
		close(done)
	}()

	// Cancel context
	cancel()

	// Wait for eventLoop to exit
	select {
	case <-done:
		// Success
	case <-time.After(1 * time.Second):
		t.Error("eventLoop did not exit after context cancellation")
	}
}

func TestPropeller_DeliverNudges_RequeuesWhenSessionUnavailable(t *testing.T) {
	townRoot := t.TempDir()
	proxy := NewProxy()
	prop := NewPropeller(proxy, townRoot, "hq-mayor")

	if err := nudge.Enqueue(townRoot, "hq-mayor", nudge.QueuedNudge{
		Sender:   "witness",
		Message:  "Escalation pending",
		Priority: nudge.PriorityUrgent,
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	prop.deliverNudges()

	pending, err := nudge.Pending(townRoot, "hq-mayor")
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if pending != 1 {
		t.Fatalf("expected requeued nudge to remain pending, got %d", pending)
	}

	drained, err := nudge.Drain(townRoot, "hq-mayor")
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(drained) != 1 {
		t.Fatalf("expected 1 requeued nudge, got %d", len(drained))
	}
	if drained[0].Priority != nudge.PriorityUrgent {
		t.Fatalf("priority = %q, want %q", drained[0].Priority, nudge.PriorityUrgent)
	}
}

func TestPropeller_NotifyReturnsErrorWithoutSessionID(t *testing.T) {
	proxy := NewProxy()
	prop := NewPropeller(proxy, t.TempDir(), "hq-mayor")

	err := prop.notify("test message", map[string]string{"gt/eventType": "nudge"}, true)
	if err == nil {
		t.Fatal("expected notify to fail when sessionID is unavailable")
	}
}

func TestEscalationMetaFromNudges_MetadataDriven(t *testing.T) {
	nudges := []nudge.QueuedNudge{
		{Sender: "witness", Message: "Helpful document about urgent migrations", Priority: nudge.PriorityNormal, Kind: "mail"},
		{Sender: "witness", Message: "Neutral text", Priority: nudge.PriorityUrgent, Kind: "escalation", ThreadID: "hq-esc789", Severity: "critical"},
	}

	meta := escalationMetaFromNudges(nudges)
	if meta == nil {
		t.Fatal("expected escalation metadata")
	}
	if meta.Kind != "escalation" || meta.ThreadID != "hq-esc789" || meta.Severity != "critical" {
		t.Fatalf("unexpected escalation meta: %#v", meta)
	}
}

func TestEscalationMetaFromNudges_IgnoresHeuristicText(t *testing.T) {
	nudges := []nudge.QueuedNudge{
		{Sender: "witness", Message: "Urgent migration doc that is helpful", Priority: nudge.PriorityUrgent, Kind: "mail"},
	}

	if meta := escalationMetaFromNudges(nudges); meta != nil {
		t.Fatalf("expected no escalation metadata for generic mail, got %#v", meta)
	}
}

func TestBuildSessionUpdateMetaAddsEscalationFields(t *testing.T) {
	nudges := []nudge.QueuedNudge{{Sender: "witness", Message: "neutral", Priority: nudge.PriorityUrgent, Kind: "escalation", ThreadID: "hq-esc999", Severity: "critical"}}
	meta := buildSessionUpdateMeta(nudges, "hq-mayor")
	if meta["gt/escalation"] != "true" || meta["gt/threadID"] != "hq-esc999" || meta["gt/severity"] != "critical" || meta["gt/kind"] != "escalation" {
		t.Fatalf("unexpected meta: %#v", meta)
	}
}

func TestNotifyWithMetaInjectsEscalationMetadataToUI(t *testing.T) {
	p := NewProxy()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	defer r.Close()
	p.setStreams(nil, w)
	p.sessionMux.Lock()
	p.sessionID = "test-session"
	p.sessionMux.Unlock()

	prop := NewPropeller(p, t.TempDir(), "hq-mayor")
	meta := map[string]string{"gt/eventType": "nudge", "gt/escalation": "true", "gt/threadID": "hq-esc777", "gt/severity": "high"}

	go func() {
		prop.notifyWithMeta("Escalation text", meta)
		w.Close()
	}()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("failed to read pipe: %v", err)
	}

	var msg JSONRPCMessage
	if err := json.Unmarshal(buf.Bytes(), &msg); err != nil {
		t.Fatalf("failed to parse message: %v", err)
	}
	var params map[string]any
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		t.Fatalf("failed to parse params: %v", err)
	}
	update := params["update"].(map[string]any)
	metaAny := update["_meta"].(map[string]any)
	if metaAny["gt/escalation"] != "true" || metaAny["gt/threadID"] != "hq-esc777" || metaAny["gt/severity"] != "high" {
		t.Fatalf("unexpected injected _meta: %#v", metaAny)
	}
}

func TestACPAttachedMayorEscalationPath_MetadataAndUrgencyEndToEnd(t *testing.T) {
	townRoot := t.TempDir()
	if err := nudge.Enqueue(townRoot, "hq-mayor", nudge.QueuedNudge{
		Sender:   "gastown/witness",
		Message:  "Escalation mail from gastown/witness. ID: hq-esc-end2end. Severity: critical. Run 'gt mail read hq-esc-end2end' or 'gt escalate ack hq-esc-end2end'.",
		Priority: nudge.PriorityUrgent,
		Kind:     "escalation",
		ThreadID: "hq-esc-end2end",
		Severity: "critical",
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	p := NewProxy()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	defer r.Close()
	p.setStreams(nil, w)
	p.sessionMux.Lock()
	p.sessionID = "attached-session"
	p.sessionMux.Unlock()

	prop := NewPropeller(p, townRoot, "hq-mayor")
	go func() {
		prop.deliverNudges()
		w.Close()
	}()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	var msg JSONRPCMessage
	if err := json.Unmarshal(buf.Bytes(), &msg); err != nil {
		t.Fatalf("json.Unmarshal message: %v", err)
	}
	var params map[string]any
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		t.Fatalf("json.Unmarshal params: %v", err)
	}
	update := params["update"].(map[string]any)
	content, ok := update["content"].(map[string]any)
	if !ok {
		t.Fatalf("expected structured content payload, got %#v", update["content"])
	}
	if text, ok := content["text"].(string); !ok || !bytes.Contains([]byte(text), []byte("hq-esc-end2end")) {
		t.Fatalf("session/update content missing escalation id: %#v", content)
	}
	meta := update["_meta"].(map[string]any)
	for key, want := range map[string]string{"gt/escalation": "true", "gt/threadID": "hq-esc-end2end", "gt/severity": "critical", "gt/kind": "escalation", "gt/urgent": "1"} {
		if meta[key] != want {
			t.Fatalf("_meta[%s] = %#v, want %q", key, meta[key], want)
		}
	}
	remaining, err := nudge.Pending(townRoot, "hq-mayor")
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("expected only deferred prompt reminder to remain after successful attached delivery, got %d", remaining)
	}
}
