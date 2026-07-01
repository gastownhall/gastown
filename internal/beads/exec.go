package beads

import (
	"context"
	"os"
	"os/exec"
	"strings"

	"github.com/steveyegge/gastown/internal/deps"
	"github.com/steveyegge/gastown/internal/util"
)

// SubprocessEnvMode describes how a bd subprocess should target Dolt and
// whether it may mutate state. New raw bd call sites should use this helper so
// target selection and side-effect suppression stay centralized.
type SubprocessEnvMode int

const (
	ReadOnlyRouting SubprocessEnvMode = iota
	MutationRouting
	ReadOnlyPinned
	MutationPinned
)

// Command builds a bd command with the shared Gas Town bd environment policy.
func Command(dir, fallbackBeadsDir string, mode SubprocessEnvMode, args ...string) *exec.Cmd {
	cmd := exec.Command(commandNameForDir(dir), args...) //nolint:gosec // G204: args are constructed internally
	ConfigureCommand(cmd, dir, fallbackBeadsDir, mode)
	return cmd
}

// CommandContext builds a context-bound bd command with the shared Gas Town bd
// environment policy.
func CommandContext(ctx context.Context, dir, fallbackBeadsDir string, mode SubprocessEnvMode, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, commandNameForDir(dir), args...) //nolint:gosec // G204: args are constructed internally
	ConfigureCommand(cmd, dir, fallbackBeadsDir, mode)
	return cmd
}

func commandNameForDir(dir string) string {
	return deps.IssueTrackerCommandName(deps.IssueTrackerBackendForCommand(FindTownRoot(dir)))
}

// ConfigureCommand applies the shared bd subprocess policy to an existing
// command. This is for callers that need a custom bd path.
func ConfigureCommand(cmd *exec.Cmd, dir, fallbackBeadsDir string, mode SubprocessEnvMode) {
	cmd.Dir = dir
	cmd.Env = EnvForSubprocessMode(os.Environ(), fallbackBeadsDir, mode)
	util.SetDetachedProcessGroup(cmd)
}

func EnvForSubprocessMode(base []string, fallbackBeadsDir string, mode SubprocessEnvMode) []string {
	switch mode {
	case ReadOnlyRouting:
		return BuildReadOnlyRoutingBDEnv(base, fallbackBeadsDir)
	case MutationRouting:
		return BuildMutationRoutingBDEnv(base, fallbackBeadsDir)
	case ReadOnlyPinned:
		return BuildReadOnlyPinnedBDEnv(base, fallbackBeadsDir)
	case MutationPinned:
		return BuildMutationPinnedBDEnv(base, fallbackBeadsDir)
	default:
		return BuildMutationRoutingBDEnv(base, fallbackBeadsDir)
	}
}

func SubprocessModeForArgs(args []string) SubprocessEnvMode {
	if ArgsAreReadOnly(args) {
		return ReadOnlyRouting
	}
	return MutationRouting
}

var bdBoolGlobalFlags = map[string]bool{
	"--allow-stale": true,
	"--help":        true,
	"--json":        true,
	"--profile":     true,
	"--quiet":       true,
	"--verbose":     true,
	"--version":     true,
	"-V":            true,
	"-h":            true,
	"-q":            true,
	"-v":            true,
}

var bdTargetSelectorFlags = map[string]bool{
	"--db":        true,
	"--directory": true,
	"--global":    true,
	"--sandbox":   true,
	"-C":          true,
}

// BDSubcommandIndex returns the argv index of bd's subcommand after recognized
// bd global flags. Unknown leading flags fail closed so proxy allowlists cannot
// be bypassed by treating command flags as globals.
func BDSubcommandIndex(argv []string) (int, bool) {
	if len(argv) < 2 || argv[0] != "bd" {
		return 0, false
	}
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			return 0, false
		}
		if !strings.HasPrefix(arg, "-") {
			return i, true
		}
		if _, _, ok := strings.Cut(arg, "="); ok {
			return 0, false
		}
		if bdBoolGlobalFlags[arg] {
			continue
		}
		return 0, false
	}
	return 0, false
}

// HasBDTargetSelectorFlag reports whether argv contains bd globals that can
// override the database or working directory selected by Gas Town. The proxy
// rejects these even after the subcommand because bd accepts globals anywhere.
func HasBDTargetSelectorFlag(argv []string) bool {
	if len(argv) == 0 || argv[0] != "bd" {
		return false
	}
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			return false
		}
		name := arg
		if cut, _, ok := strings.Cut(arg, "="); ok {
			name = cut
		}
		if bdTargetSelectorFlags[name] {
			return true
		}
	}
	return false
}
