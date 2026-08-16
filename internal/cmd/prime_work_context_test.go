package cmd

import (
	"os"
	"testing"

	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/telemetry"
)

// injectWorkContextTest is a helper that calls injectWorkContext with OTel
// activated and TMUX unset (to skip tmux subprocess calls).
func setupWorkContextTest(t *testing.T) {
	t.Helper()
	t.Setenv(telemetry.EnvLogsURL, "http://localhost:9428/insert/opentelemetry/v1/logs")
	t.Setenv("TMUX", "") // prevent tmux set-environment subprocess calls
	t.Setenv("GT_WORK_RIG", "")
	t.Setenv("GT_WORK_BEAD", "")
	t.Setenv("GT_WORK_MOL", "")
	primeDryRun = false
}

func TestInjectWorkContext_NoBeadClearsVars(t *testing.T) {
	setupWorkContextTest(t)
	// Pre-populate with stale values from a previous cycle.
	t.Setenv("GT_WORK_RIG", "oldrig")
	t.Setenv("GT_WORK_BEAD", "old-bead")
	t.Setenv("GT_WORK_MOL", "old-mol")

	ctx := RoleContext{Rig: "gastown"}
	injectWorkContext(ctx, nil)

	if got := os.Getenv("GT_WORK_RIG"); got != "" {
		t.Errorf("GT_WORK_RIG = %q, want empty (no bead on hook)", got)
	}
	if got := os.Getenv("GT_WORK_BEAD"); got != "" {
		t.Errorf("GT_WORK_BEAD = %q, want empty (no bead on hook)", got)
	}
	if got := os.Getenv("GT_WORK_MOL"); got != "" {
		t.Errorf("GT_WORK_MOL = %q, want empty (no bead on hook)", got)
	}
}

func TestInjectWorkContext_BeadOnly(t *testing.T) {
	setupWorkContextTest(t)

	ctx := RoleContext{Rig: "gastown"}
	bead := &beads.Issue{ID: "sg-05iq", Description: ""}
	injectWorkContext(ctx, bead)

	if got := os.Getenv("GT_WORK_RIG"); got != "gastown" {
		t.Errorf("GT_WORK_RIG = %q, want %q", got, "gastown")
	}
	if got := os.Getenv("GT_WORK_BEAD"); got != "sg-05iq" {
		t.Errorf("GT_WORK_BEAD = %q, want %q", got, "sg-05iq")
	}
	if got := os.Getenv("GT_WORK_MOL"); got != "" {
		t.Errorf("GT_WORK_MOL = %q, want empty (no molecule attachment)", got)
	}
}

func TestInjectWorkContext_BeadWithMolecule(t *testing.T) {
	setupWorkContextTest(t)

	ctx := RoleContext{Rig: "gastown"}
	// Build a bead description that encodes an AttachedMolecule.
	desc := beads.FormatAttachmentFields(&beads.AttachmentFields{
		AttachedMolecule: "mol-abc123",
	})
	bead := &beads.Issue{ID: "sg-05iq", Description: desc}
	injectWorkContext(ctx, bead)

	if got := os.Getenv("GT_WORK_BEAD"); got != "sg-05iq" {
		t.Errorf("GT_WORK_BEAD = %q, want %q", got, "sg-05iq")
	}
	if got := os.Getenv("GT_WORK_MOL"); got != "mol-abc123" {
		t.Errorf("GT_WORK_MOL = %q, want %q", got, "mol-abc123")
	}
}

func TestInjectWorkContext_GenericPolecat_EmptyRig(t *testing.T) {
	setupWorkContextTest(t)

	// Generic polecats have no fixed rig (ctx.Rig is empty).
	ctx := RoleContext{Rig: ""}
	bead := &beads.Issue{ID: "sg-xyzw"}
	injectWorkContext(ctx, bead)

	if got := os.Getenv("GT_WORK_RIG"); got != "" {
		t.Errorf("GT_WORK_RIG = %q, want empty for generic polecat", got)
	}
	if got := os.Getenv("GT_WORK_BEAD"); got != "sg-xyzw" {
		t.Errorf("GT_WORK_BEAD = %q, want %q", got, "sg-xyzw")
	}
}

func TestInjectWorkContext_NoopWhenOTelDisabled(t *testing.T) {
	t.Setenv(telemetry.EnvMetricsURL, "")
	t.Setenv(telemetry.EnvLogsURL, "")
	t.Setenv("GT_WORK_RIG", "")
	t.Setenv("GT_WORK_BEAD", "")
	t.Setenv("TMUX", "")
	primeDryRun = false

	ctx := RoleContext{Rig: "gastown"}
	bead := &beads.Issue{ID: "sg-05iq"}
	injectWorkContext(ctx, bead)

	// Env vars should NOT be set since OTel is disabled.
	if got := os.Getenv("GT_WORK_BEAD"); got != "" {
		t.Errorf("GT_WORK_BEAD = %q, want empty when OTel disabled", got)
	}
}

func TestInjectWorkContext_NoopInDryRun(t *testing.T) {
	setupWorkContextTest(t)
	primeDryRun = true
	t.Cleanup(func() { primeDryRun = false })

	ctx := RoleContext{Rig: "gastown"}
	bead := &beads.Issue{ID: "sg-05iq"}
	injectWorkContext(ctx, bead)

	if got := os.Getenv("GT_WORK_BEAD"); got != "" {
		t.Errorf("GT_WORK_BEAD = %q, want empty in dry-run mode", got)
	}
}
