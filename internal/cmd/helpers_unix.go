//go:build !windows

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/tmux"
)

// attachToTmuxSession attaches to a tmux session.
// If already inside tmux, uses switch-client instead of attach-session.
// Uses syscall.Exec to replace the Go process with tmux for direct terminal
// control, and passes -u for UTF-8 support regardless of locale settings.
// See: https://github.com/steveyegge/gastown/issues/1219
func attachToTmuxSession(sessionID string) error {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("tmux not found: %w", err)
	}

	// Base args with UTF-8 and socket support
	baseArgs := []string{"tmux", "-u"}
	if socket := tmux.GetDefaultSocket(); socket != "" {
		baseArgs = append(baseArgs, "-L", socket)
	}

	var args []string
	if isInSameTmuxSocket() {
		// Same tmux socket: switch to the target session
		args = append(baseArgs, "switch-client", "-t", sessionID)
	} else {
		// Outside tmux or different socket: attach to the session
		args = append(baseArgs, "attach-session", "-t", sessionID)
	}

	// Replace the Go process with tmux for direct terminal control
	return syscall.Exec(tmuxPath, args, os.Environ())
}

// execAgent execs the configured agent, replacing the current process.
// Used when we're already in the target session and just need to start the agent.
// If prompt is provided, it's passed as the initial prompt.
func execAgent(cfg *config.RuntimeConfig, prompt string) error {
	if cfg == nil {
		cfg = config.DefaultRuntimeConfig()
	}

	agentPath, err := exec.LookPath(cfg.Command)
	if err != nil {
		return fmt.Errorf("%s not found: %w", cfg.Command, err)
	}

	// exec replaces current process with agent
	// args[0] must be the command name (convention for exec)
	args := append([]string{cfg.Command}, cfg.Args...)
	if prompt != "" {
		args = append(args, prompt)
	}
	return syscall.Exec(agentPath, args, os.Environ())
}

// execRuntime execs the runtime CLI, replacing the current process.
// Used when we're already in the target session and just need to start the runtime.
// If prompt is provided, it's passed according to the runtime's prompt mode.
func execRuntime(prompt, rigPath, configDir string) error {
	townRoot := filepath.Dir(rigPath)
	runtimeConfig := config.ResolveRoleAgentConfig("crew", townRoot, rigPath)
	args := runtimeConfig.BuildArgsWithPrompt(prompt)
	if len(args) == 0 {
		return fmt.Errorf("runtime command not configured")
	}

	binPath, err := exec.LookPath(args[0])
	if err != nil {
		return fmt.Errorf("runtime command not found: %w", err)
	}

	env := os.Environ()
	if runtimeConfig.Session != nil && runtimeConfig.Session.ConfigDirEnv != "" && configDir != "" {
		env = append(env, fmt.Sprintf("%s=%s", runtimeConfig.Session.ConfigDirEnv, configDir))
	}

	return syscall.Exec(binPath, args, env)
}
