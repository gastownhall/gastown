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

	// Interactive `codex` (not `codex exec`) still refuses untrusted /
	// non-git directories unless this flag is present. Fresh sandboxes
	// happen to be valid trusted worktrees; pre-existing ones often are not.
	if !strings.Contains(cmd, "--skip-git-repo-check") {
		t.Fatalf("codex startup command missing --skip-git-repo-check (GH#4670): %q", cmd)
	}
}
