package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// resolveGrokForCLIPTest returns a real grok binary path. test_main_test.go prepends
// tiny PATH stubs that shadow the real CLI; we skip those dirs and prefer GT_GROK_BIN when set.
func resolveGrokForCLIPTest(t *testing.T) (string, []byte) {
	t.Helper()
	if p := os.Getenv("GT_GROK_BIN"); p != "" {
		out, err := exec.Command(p, "--help").CombinedOutput()
		if err != nil {
			t.Fatalf("GT_GROK_BIN --help: %v\n%s", err, out)
		}
		return p, out
	}
	stubDir := os.Getenv("GT_AGENT_STUB_BIN_DIR")
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		if stubDir != "" && dir == stubDir {
			continue
		}
		p := filepath.Join(dir, "grok")
		info, err := os.Stat(p)
		if err != nil || info.IsDir() {
			continue
		}
		out, err := exec.Command(p, "--help").CombinedOutput()
		if err != nil {
			continue
		}
		if len(out) >= 200 && strings.Contains(string(out), "Usage:") {
			return p, out
		}
	}
	return "", nil
}

// TestGrokAgentCLIPresetMatchesHelp verifies the built-in AgentGrok preset stays aligned with
// the Grok Build CLI when `grok` is installed (curl -fsSL https://x.ai/cli/install.sh | bash).
func TestGrokAgentCLIPresetMatchesHelp(t *testing.T) {
	path, out := resolveGrokForCLIPTest(t)
	if path == "" {
		t.Skip("real grok not found outside test stubs; install Grok CLI or set GT_GROK_BIN")
	}

	t.Logf("grok CLI contract using %s", path)
	help := strings.ToLower(string(out))

	info := GetAgentPreset(AgentGrok)
	if info == nil {
		t.Fatal("grok preset not found")
	}

	for _, needle := range []string{
		"--always-approve",
		"--resume",
		"--continue",
		"--output-format",
		"--single",
	} {
		if !strings.Contains(help, strings.ToLower(needle)) {
			t.Errorf("grok --help missing %q (preset may be stale vs CLI)", needle)
		}
	}
	// --trust and --no-leader are accepted by the CLI but omitted from --help.
	for _, flag := range []string{"--trust", "--no-leader"} {
		if err := exec.Command(path, flag, "--version").Run(); err != nil {
			t.Errorf("grok %s --version failed (preset arg %s may be stale): %v", flag, flag, err)
		}
	}
	if !strings.Contains(help, "output-format") || !strings.Contains(help, "json") {
		t.Errorf("grok --help should document --output-format with json")
	}

	if info.ResumeFlag != "--resume" {
		t.Errorf("preset ResumeFlag = %q, want --resume", info.ResumeFlag)
	}
	if info.ContinueFlag != "--continue" {
		t.Errorf("preset ContinueFlag = %q, want --continue", info.ContinueFlag)
	}
	if info.NonInteractive == nil {
		t.Fatal("preset NonInteractive is nil")
	}
	if info.NonInteractive.PromptFlag != "-p" {
		t.Errorf("preset PromptFlag %q should match CLI -p/--single for headless use", info.NonInteractive.PromptFlag)
	}
	of := strings.Fields(info.NonInteractive.OutputFlag)
	if len(of) < 2 || of[0] != "--output-format" {
		t.Fatalf("preset OutputFlag = %q, want '--output-format …'", info.NonInteractive.OutputFlag)
	}
	if !strings.Contains(help, strings.TrimPrefix(of[0], "-")) {
		t.Errorf("grok --help should document %s", of[0])
	}

	if info.HooksDir != ".grok/hooks" || info.HooksSettingsFile != "gastown.json" {
		t.Errorf("hooks path parts = %q + %q, want .grok/hooks + gastown.json", info.HooksDir, info.HooksSettingsFile)
	}

	agentHelp, err := exec.Command(path, "agent", "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("grok agent --help: %v\n%s", err, agentHelp)
	}
	agentHelpLower := strings.ToLower(string(agentHelp))
	if !strings.Contains(agentHelpLower, "stdio") {
		t.Error("grok agent --help should document stdio transport")
	}
	if info.ACP == nil || info.ACP.Command != "agent" {
		t.Errorf("preset ACP.Command = %v, want agent", info.ACP)
	}
}
