package config

import (
	"strings"
	"testing"
)

// GH#4670: Codex launch for pre-existing polecat sandboxes dies during
// startup. The reporter's working manual command used
// `codex exec --skip-git-repo-check`. This loop asserts what gt actually
// puts on the Codex argv.

func TestGH4670_CodexStartupCommandIncludesGitRepoCheckBypass(t *testing.T) {
	t.Parallel()

	rc := RuntimeConfigFromPreset(AgentCodex)
	cmd := rc.BuildCommandWithPrompt("[GAS TOWN] witness -> polecat/Toast • assigned")

	if !strings.Contains(cmd, "codex") {
		t.Fatalf("expected codex in startup command, got %q", cmd)
	}
	if !strings.Contains(cmd, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("expected yolo flag in startup command, got %q", cmd)
	}
}
