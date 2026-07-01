package cmd

import (
	"context"
	"os/exec"

	"github.com/steveyegge/gastown/internal/deps"
)

func issueTrackerCommand(args ...string) *exec.Cmd {
	return issueTrackerCommandForTown(detectTownRootFromCwd(), args...)
}

func issueTrackerCommandName() string {
	return deps.IssueTrackerCommandName(deps.IssueTrackerBackendForCommand(detectTownRootFromCwd()))
}

func issueTrackerCommandContext(ctx context.Context, args ...string) *exec.Cmd {
	return issueTrackerCommandContextForTown(ctx, detectTownRootFromCwd(), args...)
}

func issueTrackerCommandForTown(townRoot string, args ...string) *exec.Cmd {
	name := deps.IssueTrackerCommandName(deps.IssueTrackerBackendForCommand(townRoot))
	return exec.Command(name, args...) //nolint:gosec // G204: args are constructed internally
}

func issueTrackerCommandContextForTown(ctx context.Context, townRoot string, args ...string) *exec.Cmd {
	name := deps.IssueTrackerCommandName(deps.IssueTrackerBackendForCommand(townRoot))
	return exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: args are constructed internally
}
