package cmd

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/constants"
)

func TestOutputMoleculeStatus_StandaloneFormulaShowsVars(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	tempDir := t.TempDir()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir tempDir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	status := MoleculeStatusInfo{
		HasWork:         true,
		PinnedBead:      &beads.Issue{ID: "gt-wisp-xyz", Title: "Standalone formula work"},
		AttachedFormula: "mol-release",
		AttachedVars:    []string{"version=1.2.3", "channel=stable"},
	}

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	outputMoleculeStatus(status)

	w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	os.Stdout = oldStdout
	output := buf.String()

	if !strings.Contains(output, "📐 Formula: mol-release") {
		t.Fatalf("expected formula in output, got:\n%s", output)
	}
	if !strings.Contains(output, "--var version=1.2.3") || !strings.Contains(output, "--var channel=stable") {
		t.Fatalf("expected formula vars in output, got:\n%s", output)
	}
}

func TestOutputMoleculeStatus_FormulaWispShowsWorkflowContext(t *testing.T) {
	status := MoleculeStatusInfo{
		HasWork:         true,
		PinnedBead:      &beads.Issue{ID: "tool-wisp-demo", Title: "demo-hello"},
		AttachedFormula: "demo-hello",
		Progress: &MoleculeProgressInfo{
			RootID:     "tool-wisp-demo",
			RootTitle:  "demo-hello",
			TotalSteps: 3,
			DoneSteps:  0,
			ReadySteps: []string{"tool-wisp-step-1"},
		},
		NextAction: "Show the workflow steps: gt prime or bd mol current tool-wisp-demo",
	}

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	outputMoleculeStatus(status)

	w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	os.Stdout = oldStdout
	output := buf.String()

	if !strings.Contains(output, "📐 Formula: demo-hello") {
		t.Fatalf("expected formula line in output, got:\n%s", output)
	}
	if strings.Contains(output, "No molecule attached") {
		t.Fatalf("formula wisp should not be rendered as naked work, got:\n%s", output)
	}
	if strings.Contains(output, "Attach a molecule to start work") {
		t.Fatalf("formula wisp should not suggest gt mol attach, got:\n%s", output)
	}
	if !strings.Contains(output, "Show the workflow steps: gt prime or bd mol current tool-wisp-demo") {
		t.Fatalf("expected workflow next action, got:\n%s", output)
	}
}

func TestOutputMoleculeStatus_PatrolWispUsesInferredWorkflowContext(t *testing.T) {
	bead := &beads.Issue{
		ID:    "hq-wisp-refinery",
		Title: constants.MolRefineryPatrol + " (wisp)",
		Type:  "molecule",
	}
	attachment := workflowAttachmentForHookedBead(bead)
	if attachment == nil || attachment.AttachedFormula != constants.MolRefineryPatrol {
		t.Fatalf("workflowAttachmentForHookedBead() = %#v, want %q", attachment, constants.MolRefineryPatrol)
	}

	status := MoleculeStatusInfo{
		HasWork:         true,
		PinnedBead:      bead,
		AttachedFormula: attachment.AttachedFormula,
		NextAction:      "Show the workflow steps: gt prime or bd mol current hq-wisp-refinery",
	}

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	outputMoleculeStatus(status)

	w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	os.Stdout = oldStdout
	output := buf.String()

	if !strings.Contains(output, "📐 Formula: "+constants.MolRefineryPatrol) {
		t.Fatalf("expected inferred patrol formula in output, got:\n%s", output)
	}
	if strings.Contains(output, "No molecule attached") || strings.Contains(output, "Attach a molecule to start work") {
		t.Fatalf("patrol wisp should not render as naked hooked work, got:\n%s", output)
	}
}
