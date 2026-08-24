package tmux

import (
	"strings"
	"testing"
)

// TestIsGTBindingCurrent_DetectsStalePattern verifies that isGTBindingCurrent
// returns false when the baked-in pattern doesn't match the current pattern.
// This is the core of the gt rig add fix: after adding a rig, the prefix
// pattern changes and existing bindings become stale.
func TestIsGTBindingCurrent_DetectsStalePattern(t *testing.T) {
	tm := newTestTmux(t)

	session := "gt-test-stale-" + t.Name()
	_ = tm.KillSession(session)
	defer func() { _ = tm.KillSession(session) }()

	if err := tm.NewSessionWithCommand(session, "", "sleep 30"); err != nil {
		t.Fatalf("session creation: %v", err)
	}

	// Install a binding with an OLD pattern (missing a hypothetical "qu" prefix)
	oldPattern := "^(gt|hq)-"
	oldIfShell := "echo '#{session_name}' | grep -Eq '" + oldPattern + "'"
	if _, err := tm.run("bind-key", "-T", "prefix", "n",
		"if-shell", oldIfShell,
		"run-shell 'gt cycle next --session #{session_name} --client #{client_tty}'",
		"next-window"); err != nil {
		t.Fatalf("installing old binding: %v", err)
	}

	// Verify the binding has --client (so isGTBindingWithClient returns true)
	if !tm.isGTBindingWithClient("prefix", "n") {
		t.Fatal("expected isGTBindingWithClient to return true for the installed binding")
	}

	// But the pattern is stale — a new pattern with "qu" should not match
	newPattern := "^(gt|hq|qu)-"
	if tm.isGTBindingCurrent("prefix", "n", newPattern) {
		t.Error("expected isGTBindingCurrent to return false for stale pattern")
	}

	// The old pattern should still match
	if !tm.isGTBindingCurrent("prefix", "n", oldPattern) {
		t.Error("expected isGTBindingCurrent to return true for matching pattern")
	}
}

// TestSetCycleBindings_RefreshesStalePattern verifies that SetCycleBindings
// re-binds when the existing binding has a stale prefix pattern, even though
// it already has --client support.
func TestSetCycleBindings_RefreshesStalePattern(t *testing.T) {
	tm := newTestTmux(t)

	session := "gt-test-refresh-" + t.Name()
	_ = tm.KillSession(session)
	defer func() { _ = tm.KillSession(session) }()

	if err := tm.NewSessionWithCommand(session, "", "sleep 30"); err != nil {
		t.Fatalf("session creation: %v", err)
	}

	// Install a binding with a STALE pattern (only gt|hq, missing other prefixes)
	stalePattern := "^(gt|hq)-"
	staleIfShell := "echo '#{session_name}' | grep -Eq '" + stalePattern + "'"
	if _, err := tm.run("bind-key", "-T", "prefix", "n",
		"if-shell", staleIfShell,
		"run-shell 'gt cycle next --session #{session_name} --client #{client_tty}'",
		"next-window"); err != nil {
		t.Fatalf("installing stale binding: %v", err)
	}
	if _, err := tm.run("bind-key", "-T", "prefix", "p",
		"if-shell", staleIfShell,
		"run-shell 'gt cycle prev --session #{session_name} --client #{client_tty}'",
		"previous-window"); err != nil {
		t.Fatalf("installing stale binding for p: %v", err)
	}

	// Call SetCycleBindings — it should detect the stale pattern and re-bind
	if err := tm.SetCycleBindings(session); err != nil {
		t.Fatalf("SetCycleBindings: %v", err)
	}

	// Verify the binding was updated with the current pattern
	currentPattern := sessionPrefixPattern()
	output, err := tm.keyBindingOutput("prefix", "n")
	if err != nil {
		t.Fatalf("listing keys: %v", err)
	}
	if !strings.Contains(output, currentPattern) {
		t.Errorf("expected binding to contain current pattern %q, got: %s", currentPattern, output)
	}
}
