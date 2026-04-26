package cmd

import "testing"

func TestMRStatusOutputCarriesSystemCoreNativeFields(t *testing.T) {
	out := MRStatusOutput{
		ID:          "gt-mr-123",
		Branch:      "polecat/Nux/gt-bead-123",
		Target:      "main",
		SourceIssue: "gt-bead-123",
		Worker:      "Nux",
		Rig:         "gastown",
		CommitSHA:   "abc123",
		AgentBead:   "gt-agent-nux",
		ConvoyID:    "gt-cv-456",
		WispID:      "gt-mr-123",
	}

	if out.ID != "gt-mr-123" {
		t.Fatalf("id = %q", out.ID)
	}
	if out.SourceIssue != "gt-bead-123" {
		t.Fatalf("source issue = %q", out.SourceIssue)
	}
	if out.AgentBead != "gt-agent-nux" {
		t.Fatalf("agent bead = %q", out.AgentBead)
	}
	if out.ConvoyID != "gt-cv-456" {
		t.Fatalf("convoy id = %q", out.ConvoyID)
	}
	if out.WispID != "gt-mr-123" {
		t.Fatalf("wisp id = %q", out.WispID)
	}
}
