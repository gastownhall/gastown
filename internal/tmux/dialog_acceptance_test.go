package tmux

import (
	"testing"
	"time"
)

// TestAcceptWorkspaceTrustDialog_NoDialog verifies that when no trust dialog
// is present (agent prompt visible), the function returns quickly without error.
func TestAcceptWorkspaceTrustDialog_NoDialog(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-trust-nodlg-" + t.Name()

	newReadyTestSession(t, tm, sessionName)
	defer func() { _ = tm.KillSession(sessionName) }()

	// Session starts with a shell prompt containing ">", "$", or "%"
	// The polling loop should exit early when it sees the prompt.
	start := time.Now()
	err := tm.AcceptWorkspaceTrustDialog(sessionName)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("AcceptWorkspaceTrustDialog: %v", err)
	}

	// Should complete well before the 8s timeout since prompt is visible
	if elapsed > 6*time.Second {
		t.Errorf("took %v, expected early exit (< 6s)", elapsed)
	}
}

// TestAcceptWorkspaceTrustDialog_DetectsDialog verifies that when trust dialog
// text appears in the pane, it is detected and accepted (Enter key sent).
func TestAcceptWorkspaceTrustDialog_DetectsDialog(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-trust-dlg-" + t.Name()

	newReadyTestSession(t, tm, sessionName)
	defer func() { _ = tm.KillSession(sessionName) }()

	// Simulate the trust dialog by echoing its text into the pane
	if err := tm.SendKeys(sessionName, "echo 'Quick safety check - do you trust this folder?'"); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}
	waitForPaneText(t, tm, sessionName, "Quick safety check")

	err := tm.AcceptWorkspaceTrustDialog(sessionName)
	if err != nil {
		t.Fatalf("AcceptWorkspaceTrustDialog: %v", err)
	}

	// Verify that Enter was sent (we can't easily verify the exact keypress,
	// but the function should return without error after detecting the dialog)
}

// TestAcceptWorkspaceTrustDialog_DetectsCodexDialog verifies that Codex's
// workspace trust prompt is treated as a trust dialog instead of an agent prompt.
func TestAcceptWorkspaceTrustDialog_DetectsCodexDialog(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-trust-codex-" + t.Name()

	newReadyTestSession(t, tm, sessionName)
	defer func() { _ = tm.KillSession(sessionName) }()

	if err := tm.SendKeys(sessionName, "echo '> You are in /tmp/demo'; echo 'Do you trust the contents of this directory?'"); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}
	waitForPaneText(t, tm, sessionName, "Do you trust the contents of this directory?")

	if err := tm.AcceptWorkspaceTrustDialog(sessionName); err != nil {
		t.Fatalf("AcceptWorkspaceTrustDialog: %v", err)
	}
}

// TestAcceptBypassPermissionsWarning_NoDialog verifies that when no bypass
// permissions dialog is present, the function returns quickly without error.
func TestAcceptBypassPermissionsWarning_NoDialog(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-bypass-nodlg-" + t.Name()

	newReadyTestSession(t, tm, sessionName)
	defer func() { _ = tm.KillSession(sessionName) }()

	start := time.Now()
	err := tm.AcceptBypassPermissionsWarning(sessionName)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("AcceptBypassPermissionsWarning: %v", err)
	}

	if elapsed > 6*time.Second {
		t.Errorf("took %v, expected early exit (< 6s)", elapsed)
	}
}

// TestAcceptBypassPermissionsWarning_DetectsDialog verifies that when bypass
// permissions dialog text appears in the pane, it is detected and accepted.
func TestAcceptBypassPermissionsWarning_DetectsDialog(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-bypass-dlg-" + t.Name()

	newReadyTestSession(t, tm, sessionName)
	defer func() { _ = tm.KillSession(sessionName) }()

	// Simulate the bypass permissions dialog
	if err := tm.SendKeys(sessionName, "echo 'Bypass Permissions mode is enabled'"); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}
	waitForPaneText(t, tm, sessionName, "Bypass Permissions mode")

	err := tm.AcceptBypassPermissionsWarning(sessionName)
	if err != nil {
		t.Fatalf("AcceptBypassPermissionsWarning: %v", err)
	}
}

// TestAcceptStartupDialogs_NoDialogs verifies the combined function returns
// quickly when no dialogs are present.
func TestAcceptStartupDialogs_NoDialogs(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-startup-nodlg-" + t.Name()

	newReadyTestSession(t, tm, sessionName)
	defer func() { _ = tm.KillSession(sessionName) }()

	start := time.Now()
	err := tm.AcceptStartupDialogs(sessionName)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("AcceptStartupDialogs: %v", err)
	}

	// Both dialog checks should early-exit when prompt is visible
	if elapsed > 12*time.Second {
		t.Errorf("took %v, expected faster completion", elapsed)
	}
}

// TestAcceptWorkspaceTrustDialog_InvalidSession verifies error handling
// when the session doesn't exist.
func TestAcceptWorkspaceTrustDialog_InvalidSession(t *testing.T) {
	tm := newTestTmux(t)

	// Should not panic or hang — should return nil after timeout
	err := tm.AcceptWorkspaceTrustDialog("gt-nonexistent-session-xyz")
	// CapturePane errors are retried until timeout, then returns nil
	if err != nil {
		t.Fatalf("expected nil error for nonexistent session, got: %v", err)
	}
}

// TestContainsPromptIndicator verifies the prompt detection helper
// recognizes various shell and agent prompt patterns.
func TestContainsPromptIndicator(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"claude prompt", "Hello! How can I help?\n>", true},
		{"codex prompt", "Ready\n› ", true},
		{"bash prompt", "user@host:~$", true},
		{"zsh prompt", "╰─❯", true},
		{"root prompt", "root@host:~#", true},
		{"csh prompt", "host%", true},
		{"dialog text only", "Quick safety check\nDo you trust this folder?", false},
		{"empty", "", false},
		{"whitespace only", "   \n  \n  ", false},
		{"bypass dialog", "Bypass Permissions mode\n1. No\n2. Yes, I accept", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containsPromptIndicator(tt.content)
			if got != tt.want {
				t.Errorf("containsPromptIndicator(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}

func TestContainsWorkspaceTrustDialog(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"claude trust prompt", "Quick safety check\nDo you trust this folder?", true},
		{"codex trust prompt", "> You are in /tmp/demo\nDo you trust the contents of this directory?", true},
		{"bypass dialog", "Bypass Permissions mode\n1. No\n2. Yes, I accept", false},
		{"shell prompt", "user@host:~$", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containsWorkspaceTrustDialog(tt.content)
			if got != tt.want {
				t.Errorf("containsWorkspaceTrustDialog(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}

func TestContainsBlockingStartupDialog(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		content     string
		wantBlocked bool
		wantName    string
	}{
		{
			name: "codex update modal",
			content: `Update available! 0.137.0 -> 0.138.0
Update now
Skip
Skip until next version`,
			wantBlocked: true,
			wantName:    "codex update prompt",
		},
		{
			name:        "codex git repo check error",
			content:     "Not inside a trusted directory and --skip-git-repo-check was not specified.",
			wantBlocked: true,
			wantName:    "codex git repo check",
		},
		{
			// Real Codex TUI from openai/codex#14547. The selector line uses
			// the same › glyph as Codex's ready prompt. A false-negative here
			// lets gt session start treat a live trust modal as "ready" and
			// nudge it, which quits Codex ("No, quit") — GH#4670.
			name: "codex trust modal with selector",
			content: `> You are in /tmp/old-polecat

Do you trust the contents of this directory? Working with untrusted
contents comes with higher risk of prompt injection.

› 1. Yes, continue
  2. No, quit`,
			wantBlocked: true,
			wantName:    "workspace trust prompt",
		},
		{
			// CapturePane sometimes lands the cursor on its own › line after
			// the question. promptAppearsAfterStartupBlocker currently treats
			// that as "stale dialog, agent is ready".
			name: "codex trust modal with lone selector after question",
			content: `> You are in /tmp/old-polecat
Do you trust the contents of this directory?
›
1. Yes, continue
2. No, quit`,
			wantBlocked: true,
			wantName:    "workspace trust prompt",
		},
		{
			name:        "bypass modal",
			content:     "Bypass Permissions mode\n1. No\n2. Yes, I accept",
			wantBlocked: true,
			wantName:    "bypass permissions prompt",
		},
		{
			name:        "ready prompt",
			content:     "› ",
			wantBlocked: false,
		},
		{
			name: "stale bypass dialog before codex prompt",
			content: `Bypass Permissions mode
1. No
2. Yes, I accept
› `,
			wantBlocked: false,
		},
		{
			name: "stale bypass dialog before prompt and status",
			content: `Bypass Permissions mode
1. No
2. Yes, I accept
›
session ready`,
			wantBlocked: false,
		},
		{
			name: "stale trust dialog before shell prompt",
			content: `Quick safety check
Do you trust this folder?
user@host:~$`,
			wantBlocked: false,
		},
		{
			name: "old shell prompt before current bypass dialog",
			content: `user@host:~$
Bypass Permissions mode
1. No
2. Yes, I accept`,
			wantBlocked: true,
			wantName:    "bypass permissions prompt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotName, gotBlocked := containsBlockingStartupDialog(tt.content)
			if gotBlocked != tt.wantBlocked {
				t.Fatalf("blocked = %v, want %v", gotBlocked, tt.wantBlocked)
			}
			if gotName != tt.wantName {
				t.Fatalf("name = %q, want %q", gotName, tt.wantName)
			}
		})
	}
}

// TestDismissStartupDialogsBlind_SendsKeys verifies that the blind dismiss
// sends keys without error on a valid session (no screen-scraping).
func TestDismissStartupDialogsBlind_SendsKeys(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-blind-dismiss-" + t.Name()

	newReadyTestSession(t, tm, sessionName)
	defer func() { _ = tm.KillSession(sessionName) }()

	// Should complete quickly — no polling, no CapturePane
	start := time.Now()
	err := tm.DismissStartupDialogsBlind(sessionName)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("DismissStartupDialogsBlind: %v", err)
	}

	// Should take ~700ms (500ms + 200ms sleeps) — not the 8s+ dialog poll timeout
	if elapsed > 3*time.Second {
		t.Errorf("took %v, expected ~700ms (no polling)", elapsed)
	}
}

// TestDismissStartupDialogsBlind_InvalidSession verifies error handling
// when the session doesn't exist.
func TestDismissStartupDialogsBlind_InvalidSession(t *testing.T) {
	tm := newTestTmux(t)

	err := tm.DismissStartupDialogsBlind("gt-nonexistent-session-blind-xyz")
	// Should return an error since the session doesn't exist
	if err == nil {
		t.Error("expected error for nonexistent session, got nil")
	}
}
