package tmux

import (
	"strings"
	"testing"
	"time"
)

// GH#4670: after switching the default agent to Codex, `gt session start`
// reports "session died during startup (agent command may have failed)" for
// pre-existing polecat sandboxes. Freshly slung sandboxes start. These tests
// are the diagnosis loop for that symptom.

// Realistic Codex workspace-trust TUI (openai/codex#14547).
const gh4670CodexTrustTUI = `> You are in /tmp/old-polecat

Do you trust the contents of this directory? Working with untrusted
contents comes with higher risk of prompt injection.

› 1. Yes, continue
  2. No, quit`

func TestGH4670_CodexTrustSelectorLooksLikeReadyPrompt(t *testing.T) {
	t.Parallel()

	prefix := "› "
	if !matchesPromptPrefix("› 1. Yes, continue", prefix) {
		t.Fatal("Codex trust selector should match ReadyPromptPrefix › ; if this fails, WaitForRuntimeReady cannot false-trigger on the modal")
	}
	if !matchesPromptPrefix("›", prefix) {
		t.Fatal("lone › line should match ReadyPromptPrefix")
	}
	if matchesPromptPrefix("  2. No, quit", prefix) {
		t.Fatal("option 2 must not match the ready prefix")
	}
}

func TestGH4670_CheckStartupBlocked_CodexTrustTUI(t *testing.T) {
	t.Parallel()

	name, blocked := containsBlockingStartupDialog(gh4670CodexTrustTUI)
	if !blocked {
		t.Fatalf("live Codex trust TUI must be treated as blocking, got name=%q blocked=false (promptAppearsAfterStartupBlocker false-negative)", name)
	}
	if name != "workspace trust prompt" {
		t.Fatalf("name = %q, want workspace trust prompt", name)
	}
}

func TestGH4670_CheckStartupBlocked_StaleTrustDialogBeforeReadyPrompt(t *testing.T) {
	content := gh4670CodexTrustTUI + "\n\n› "
	if name, blocked := containsBlockingStartupDialog(content); blocked {
		t.Fatalf("stale trust dialog blocked healthy ready prompt: name=%q", name)
	}
}

func TestGH4670_CheckStartupBlocked_StaleGitErrorBeforeReadyPrompt(t *testing.T) {
	content := "Not inside a trusted directory; use --skip-git-repo-check\n\n› "
	if name, blocked := containsBlockingStartupDialog(content); blocked {
		t.Fatalf("stale git check blocked healthy ready prompt: name=%q", name)
	}
}

func TestGH4670_DelayedAgentExitKeepsSessionForCapture(t *testing.T) {
	// Codex git-repo-check / trust-quit often takes >250ms. The pane must
	// remain after that delayed exit so callers can capture the failure
	// instead of reporting the opaque GH#4670 "session died during startup".
	tm := newTestTmux(t)
	session := "gt-test-gh4670-delayexit-" + t.Name()
	_ = tm.KillSession(session)
	defer func() { _ = tm.KillSession(session) }()

	err := tm.NewSessionWithCommand(session, "", `sh -c 'sleep 0.4; echo "Not inside a trusted directory and --skip-git-repo-check was not specified."; exit 1'`)
	if err != nil {
		t.Fatalf("NewSessionWithCommand returned error for delayed exit (health check window too wide?): %v", err)
	}

	time.Sleep(800 * time.Millisecond)

	has, hasErr := tm.HasSession(session)
	if hasErr != nil {
		t.Fatalf("HasSession: %v", hasErr)
	}
	if !has {
		t.Fatal("session vanished after delayed agent exit; remain-on-exit must stay on so pane output is recoverable")
	}

	output, _ := tm.CapturePane(session, 50)
	if !strings.Contains(output, "Not inside a trusted directory") {
		t.Fatalf("expected git-repo-check error in pane, got %q", strings.TrimSpace(output))
	}
}

func TestGH4670_CheckStartupBlocked_LiveCodexTrustPane(t *testing.T) {
	tm := newTestTmux(t)
	session := "gt-test-gh4670-trustpane-" + t.Name()
	_ = tm.KillSession(session)
	defer func() { _ = tm.KillSession(session) }()

	// Keep the pane alive with cat so remain-on-exit is not the variable.
	if err := tm.NewSessionWithCommand(session, "", "cat"); err != nil {
		t.Fatalf("NewSessionWithCommand: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := tm.SendKeys(session, gh4670CodexTrustTUI); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	err := tm.CheckStartupBlocked(session)
	if err == nil {
		pane, _ := tm.CapturePane(session, 80)
		t.Fatalf("CheckStartupBlocked returned nil on live Codex trust TUI; startup would proceed to nudge/quit. pane=%q", pane)
	}
	if !strings.Contains(err.Error(), "workspace trust prompt") {
		t.Fatalf("CheckStartupBlocked error = %v, want workspace trust prompt", err)
	}
}
