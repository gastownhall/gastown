//go:build windows

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/tmux"
)

// attachToTmuxSession attaches to a tmux/psmux session on Windows.
// If already inside the multiplexer, uses switch-client instead of attach-session.
// Uses os/exec.Command with stdio passthrough since syscall.Exec is Unix-only.
func attachToTmuxSession(sessionID string) error {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("tmux not found: %w", err)
	}

	// Base args with UTF-8 and socket support
	var args []string
	args = append(args, "-u")
	if socket := tmux.GetDefaultSocket(); socket != "" {
		args = append(args, "-L", socket)
	}

	if isInSameTmuxSocket() {
		args = append(args, "switch-client", "-t", sessionID)
	} else {
		args = append(args, "attach-session", "-t", sessionID)
	}

	cmd := exec.Command(tmuxPath, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		return err
	}
	os.Exit(0)
	return nil // unreachable
}

// execAgent runs the configured agent, replacing the current process.
// Uses os/exec.Command with stdio passthrough since syscall.Exec is Unix-only.
func execAgent(cfg *config.RuntimeConfig, prompt string) error {
	if cfg == nil {
		cfg = config.DefaultRuntimeConfig()
	}

	agentPath, err := exec.LookPath(cfg.Command)
	if err != nil {
		return fmt.Errorf("%s not found: %w", cfg.Command, err)
	}

	args := append([]string{}, cfg.Args...)
	if prompt != "" {
		args = append(args, prompt)
	}

	cmd := exec.Command(agentPath, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		return err
	}
	os.Exit(0)
	return nil // unreachable
}

// execRuntime runs the runtime CLI, replacing the current process.
// Uses os/exec.Command with stdio passthrough since syscall.Exec is Unix-only.
func execRuntime(prompt, rigPath, configDir string) error {
	townRoot := filepath.Dir(rigPath)
	runtimeConfig := config.ResolveRoleAgentConfig("crew", townRoot, rigPath)
	cmdArgs := runtimeConfig.BuildArgsWithPrompt(prompt)
	if len(cmdArgs) == 0 {
		return fmt.Errorf("runtime command not configured")
	}

	binPath, err := exec.LookPath(cmdArgs[0])
	if err != nil {
		return fmt.Errorf("runtime command not found: %w", err)
	}

	cmd := exec.Command(binPath, cmdArgs[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	if runtimeConfig.Session != nil && runtimeConfig.Session.ConfigDirEnv != "" && configDir != "" {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", runtimeConfig.Session.ConfigDirEnv, configDir))
	}

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		return err
	}
	os.Exit(0)
	return nil // unreachable
}
