package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/util"
)

// bdCmd is a builder for constructing bd exec.Command calls.
// It provides a fluent API for configuring environment variables,
// working directory, and I/O settings common to bd CLI invocations.
type bdCmd struct {
	args       []string
	dir        string
	env        []string
	stderr     io.Writer
	autoCommit bool
	allowStale bool
	gtRoot     string
	beadsDir   string
	routing    bool
}

// BdCmd creates a new bd command builder with the given arguments.
// The command will execute "bd" with the provided arguments.
//
// Example:
//
//	err := cmd.BdCmd("show", beadID, "--json").
//	    Dir(workDir).
//	    Run()
func BdCmd(args ...string) *bdCmd {
	return &bdCmd{
		args:   args,
		env:    os.Environ(),
		stderr: os.Stderr,
	}
}

// WithAutoCommit sets BD_DOLT_AUTO_COMMIT=on in the environment.
// This is used for sequential dependent bd calls where each call
// needs to see the changes from previous calls.
func (b *bdCmd) WithAutoCommit() *bdCmd {
	b.autoCommit = true
	return b
}

// AllowStale requests bd's stale-read bypass when the installed bd supports it.
// Unsupported bd versions silently omit the flag so callers can share one
// compatibility path instead of hardcoding version-specific arguments.
func (b *bdCmd) AllowStale() *bdCmd {
	b.allowStale = true
	return b
}

// WithGTRoot adds GT_ROOT=root to the environment.
// This is required for bd to find town-level formulas and configuration.
func (b *bdCmd) WithGTRoot(root string) *bdCmd {
	b.gtRoot = root
	return b
}

// WithBeadsDir sets BEADS_DIR explicitly in the environment.
// This prevents inherited BEADS_DIR from the parent process from causing
// bd to write to the wrong database. The dir should be the resolved
// .beads directory path (e.g., from beads.ResolveBeadsDir).
func (b *bdCmd) WithBeadsDir(dir string) *bdCmd {
	b.beadsDir = dir
	return b
}

// Dir sets the working directory for the command. When a directory is provided,
// bd is also pinned to that directory's resolved .beads database unless
// WithBeadsDir supplies a more specific database.
func (b *bdCmd) Dir(dir string) *bdCmd {
	b.dir = dir
	return b
}

// StripBeadsDir removes any inherited BEADS_DIR from the environment.
// Use this when the command relies on Dir() for routing and an inherited
// BEADS_DIR would incorrectly override the resolved database. If Dir() is set,
// buildEnv will still add an explicit BEADS_DIR for that directory; this method
// only strips inherited values from the parent process.
func (b *bdCmd) StripBeadsDir() *bdCmd {
	b.env = filterEnvKey(b.env, "BEADS_DIR")
	return b
}

// WithRouting strips inherited bd target selectors and does not pin BEADS_DIR,
// allowing bd prefix routing to choose the target database. Dir still sets cwd.
func (b *bdCmd) WithRouting() *bdCmd {
	b.routing = true
	b.env = filterEnvKey(b.env, "BEADS_DIR")
	return b
}

// Stderr sets the stderr writer for the command.
// Defaults to os.Stderr if not set.
func (b *bdCmd) Stderr(w io.Writer) *bdCmd {
	b.stderr = w
	return b
}

// filterEnvKey removes all entries matching the given key from the env slice.
// This ensures appended values aren't shadowed by existing entries, since
// glibc getenv() returns the first match in the environment array.
func filterEnvKey(env []string, key string) []string {
	return beads.StripEnvKey(env, key)
}

func filterBdTargetEnv(env []string) []string {
	return beads.StripBDTargetEnv(env)
}

func pinBeadsDirEnv(env []string, beadsDir string) []string {
	if beadsDir == "" {
		return beads.StripBDTargetEnv(env)
	}
	return beads.BuildPinnedBDEnv(env, beadsDir)
}

// buildEnv constructs the final environment slice based on configured options.
func (b *bdCmd) buildEnv() []string {
	env := b.env

	// Add BD_DOLT_AUTO_COMMIT=on for sequential dependent calls.
	// Filter existing entries first — glibc getenv() returns the first match,
	// so an existing "off" entry would shadow the appended "on".
	if b.autoCommit {
		env = filterEnvKey(env, "BD_DOLT_AUTO_COMMIT")
		env = filterEnvKey(env, "BD_READONLY")
		env = append(env, "BD_DOLT_AUTO_COMMIT=on")
	}

	// Add GT_ROOT if specified.
	// Filter existing entries first for the same reason as above.
	if b.gtRoot != "" {
		env = filterEnvKey(env, "GT_ROOT")
		env = append(env, "GT_ROOT="+b.gtRoot)
	}

	// Add BEADS_DIR if specified.
	// This prevents inherited BEADS_DIR from causing bd to target the wrong
	// database (e.g., HQ instead of rig). See gt-ctir.
	//
	// Also clear inherited Dolt target variables. Dashboard and agent shells can
	// carry a town-level or remote BEADS_DOLT_* target; keeping it while changing
	// BEADS_DIR makes `bd show <displayed-id>` query a different database than
	// `gt ready` used to render the row.
	if b.routing {
		env = beads.BuildRoutingBDEnv(env, beads.ResolveBeadsDir(b.dir))
	} else if b.beadsDir != "" {
		env = pinBeadsDirEnv(env, b.beadsDir)
	} else if b.dir != "" {
		env = pinBeadsDirEnv(env, beads.ResolveBeadsDir(b.dir))
	} else {
		env = beads.SuppressBDSideEffects(beads.StripBDTargetEnv(env))
	}

	return env
}

// Build returns the configured exec.Cmd.
// This allows callers to further customize the command before execution.
func (b *bdCmd) Build() *exec.Cmd {
	args := b.resolvedArgs()
	cmd := exec.Command("bd", args...)
	cmd.Dir = b.dir
	cmd.Env = b.buildEnv()
	cmd.Stderr = b.stderr
	return cmd
}

func resolveBdCmdTimeout() time.Duration {
	if v := os.Getenv("GT_BD_TIMEOUT_SEC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return constants.BdCommandTimeout
}

func (b *bdCmd) buildContextCommand(ctx context.Context) *exec.Cmd {
	args := b.resolvedArgs()
	cmd := exec.CommandContext(ctx, "bd", args...)
	util.SetProcessGroup(cmd)
	cmd.Dir = b.dir
	cmd.Env = b.buildEnv()
	cmd.Stderr = b.stderr
	return cmd
}

func (b *bdCmd) wrapTimeout(err error, deadline time.Duration) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		return fmt.Errorf("%s timed out after %v: %w", b.argsDesc(), deadline, err)
	}
	return err
}

func (b *bdCmd) wrapCommandError(ctx context.Context, err error, deadline time.Duration) error {
	if err == nil {
		return nil
	}
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("%s timed out after %v: %w", b.argsDesc(), deadline, err)
	}
	return b.wrapTimeout(err, deadline)
}

func (b *bdCmd) argsDesc() string {
	desc := "bd"
	if len(b.args) > 0 {
		desc += " " + b.args[0]
	}
	if len(b.args) > 1 {
		desc += fmt.Sprintf(" ... (%d args)", len(b.args))
	}
	if b.beadsDir != "" {
		desc += fmt.Sprintf(" beads_dir=%s", b.beadsDir)
	}
	if b.dir != "" {
		desc += fmt.Sprintf(" cwd=%s", b.dir)
	}
	return desc
}

// resolvedArgs returns the final args, normalizing requested stale-read support
// to bd's global flag position when supported and stripping it when unsupported.
func (b *bdCmd) resolvedArgs() []string {
	filtered := make([]string, 0, len(b.args))
	requestedAllowStale := b.allowStale
	for _, a := range b.args {
		if a == "--allow-stale" {
			requestedAllowStale = true
			continue
		}
		filtered = append(filtered, a)
	}
	if !requestedAllowStale {
		return b.args
	}
	if beads.BdSupportsAllowStaleWithEnv(b.buildEnv()) {
		return append([]string{"--allow-stale"}, filtered...)
	}
	return filtered
}

// Run builds and runs the command, returning any error.
// This is a convenience method equivalent to Build().Run().
func (b *bdCmd) Run() error {
	deadline := resolveBdCmdTimeout()
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	return b.wrapCommandError(ctx, b.buildContextCommand(ctx).Run(), deadline)
}

// Output builds and runs the command, returning stdout and any error.
// This is a convenience method equivalent to Build().Output().
// Note: Output() captures stdout but Stderr must still be configured
// separately if you want to capture stderr instead of it going to os.Stderr.
func (b *bdCmd) Output() ([]byte, error) {
	deadline := resolveBdCmdTimeout()
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	out, err := b.buildContextCommand(ctx).Output()
	return out, b.wrapCommandError(ctx, err, deadline)
}

// CombinedOutput builds and runs the command, returning combined stdout+stderr.
// This overrides the configured Stderr writer to capture both streams.
// Useful for including command output in error messages.
func (b *bdCmd) CombinedOutput() ([]byte, error) {
	deadline := resolveBdCmdTimeout()
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	args := b.resolvedArgs()
	cmd := exec.CommandContext(ctx, "bd", args...)
	util.SetProcessGroup(cmd)
	cmd.Dir = b.dir
	cmd.Env = b.buildEnv()
	out, err := cmd.CombinedOutput()
	return out, b.wrapCommandError(ctx, err, deadline)
}
