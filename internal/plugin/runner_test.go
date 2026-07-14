package plugin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testScriptPlugin creates a plugin directory with the given run.sh body and
// returns a Plugin pointing at it.
func testScriptPlugin(t *testing.T, script, timeout string) *Plugin {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/usr/bin/env bash\n"+script+"\n"), 0755); err != nil {
		t.Fatalf("writing run.sh: %v", err)
	}
	p := &Plugin{
		Name:         "test-script",
		Description:  "script contract test plugin",
		Path:         dir,
		HasRunScript: true,
		Instructions: "Do the thing.",
	}
	if timeout != "" {
		p.Execution = &Execution{Timeout: timeout}
	}
	return p
}

func TestRunScript_SuccessCompletesRun(t *testing.T) {
	p := testScriptPlugin(t, "echo guard-ran; exit 0", "")

	res, err := RunScript(context.Background(), p, t.TempDir())
	if err != nil {
		t.Fatalf("RunScript: %v", err)
	}
	if !res.Success() {
		t.Errorf("Success() = false, want true (exit=%d timedOut=%v)", res.ExitCode, res.TimedOut)
	}
	if res.NeedsAgent() {
		t.Error("NeedsAgent() = true, want false for exit 0")
	}
	if !strings.Contains(res.Output, "guard-ran") {
		t.Errorf("Output = %q, want it to contain script stdout", res.Output)
	}
}

func TestRunScript_NeedsAgentExitCode(t *testing.T) {
	p := testScriptPlugin(t, "echo pre-checks passed; exit 10", "")

	res, err := RunScript(context.Background(), p, t.TempDir())
	if err != nil {
		t.Fatalf("RunScript: %v", err)
	}
	if !res.NeedsAgent() {
		t.Errorf("NeedsAgent() = false, want true for exit %d (got exit %d)", ScriptExitNeedsAgent, res.ExitCode)
	}
	if res.Success() {
		t.Error("Success() = true, want false for the needs-agent exit code")
	}
}

func TestRunScript_FailureIsNotAgentDispatch(t *testing.T) {
	p := testScriptPlugin(t, "echo boom >&2; exit 7", "")

	res, err := RunScript(context.Background(), p, t.TempDir())
	if err != nil {
		t.Fatalf("RunScript: %v", err)
	}
	if res.ExitCode != 7 {
		t.Errorf("ExitCode = %d, want 7", res.ExitCode)
	}
	if res.Success() || res.NeedsAgent() {
		t.Error("failure exit must be neither Success nor NeedsAgent (fail-safe)")
	}
	if !strings.Contains(res.Output, "boom") {
		t.Errorf("Output = %q, want stderr captured", res.Output)
	}
}

func TestRunScript_TimeoutFailsSafe(t *testing.T) {
	// Exit code 10 after the deadline must NOT count as a needs-agent run.
	p := testScriptPlugin(t, "sleep 5; exit 10", "150ms")

	start := time.Now()
	res, err := RunScript(context.Background(), p, t.TempDir())
	if err != nil {
		t.Fatalf("RunScript: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("RunScript took %v, want timeout enforcement near 150ms", elapsed)
	}
	if !res.TimedOut {
		t.Error("TimedOut = false, want true")
	}
	if res.Success() || res.NeedsAgent() {
		t.Error("timed-out run must be neither Success nor NeedsAgent (fail-safe)")
	}
}

func TestRunScript_RunsInPluginDirWithTownRoot(t *testing.T) {
	p := testScriptPlugin(t, `printf 'cwd=%s town=%s' "$(pwd -P)" "$GT_TOWN_ROOT"`, "")
	townRoot := t.TempDir()

	res, err := RunScript(context.Background(), p, townRoot)
	if err != nil {
		t.Fatalf("RunScript: %v", err)
	}
	wantDir, _ := filepath.EvalSymlinks(p.Path)
	if !strings.Contains(res.Output, "cwd="+wantDir) {
		t.Errorf("Output = %q, want cwd %q (plugin dir)", res.Output, wantDir)
	}
	if !strings.Contains(res.Output, "town="+townRoot) {
		t.Errorf("Output = %q, want GT_TOWN_ROOT %q", res.Output, townRoot)
	}
}

func TestRunScript_MissingScriptErrors(t *testing.T) {
	p := &Plugin{Name: "no-script", Path: t.TempDir()}

	if _, err := RunScript(context.Background(), p, t.TempDir()); err == nil {
		t.Fatal("RunScript with no run.sh: got nil error, want error")
	}
}

func TestScriptTimeout(t *testing.T) {
	cases := []struct {
		name   string
		plugin *Plugin
		want   time.Duration
	}{
		{"default when no execution block", &Plugin{}, DefaultScriptTimeout},
		{"default when empty timeout", &Plugin{Execution: &Execution{}}, DefaultScriptTimeout},
		{"parses declared timeout", &Plugin{Execution: &Execution{Timeout: "2m"}}, 2 * time.Minute},
		{"default on unparseable timeout", &Plugin{Execution: &Execution{Timeout: "soon"}}, DefaultScriptTimeout},
		{"default on non-positive timeout", &Plugin{Execution: &Execution{Timeout: "-5s"}}, DefaultScriptTimeout},
	}
	for _, tc := range cases {
		if got := tc.plugin.ScriptTimeout(); got != tc.want {
			t.Errorf("%s: ScriptTimeout() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestScriptResult_NilSafe(t *testing.T) {
	var r *ScriptResult
	if r.Success() || r.NeedsAgent() {
		t.Error("nil ScriptResult must be neither Success nor NeedsAgent")
	}
}

func TestFormatAgentStepMailBody(t *testing.T) {
	p := &Plugin{
		Name:         "check-thing",
		Description:  "checks the thing",
		RigName:      "gastown",
		Instructions: "Inspect the pane output and decide.",
	}

	body := p.FormatAgentStepMailBody("[check-thing] 2 candidates need judgment")

	for _, want := range []string{
		"Do NOT re-run run.sh",
		"exited with code 10",
		"Inspect the pane output and decide.",
		"[check-thing] 2 candidates need judgment",
		"gt dog done",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("FormatAgentStepMailBody missing %q\nbody:\n%s", want, body)
		}
	}
	if strings.Contains(body, "bash run.sh") {
		t.Error("FormatAgentStepMailBody must not instruct the dog to run the script again")
	}
}

func TestOutputTail(t *testing.T) {
	long := strings.Repeat("x", 100) + "\nline-two\n" + strings.Repeat("y", 50)
	got := outputTail(long, 60)
	if len(got) > 60 {
		t.Errorf("outputTail returned %d bytes, want <= 60", len(got))
	}
	if !strings.HasSuffix(got, strings.Repeat("y", 50)) {
		t.Errorf("outputTail = %q, want the tail preserved", got)
	}
	if short := outputTail("short", 60); short != "short" {
		t.Errorf("outputTail(short) = %q, want unchanged", short)
	}
}
