package plugin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/steveyegge/gastown/internal/util"
)

// Script execution contract (op-omcx).
//
// When a plugin directory contains a run.sh alongside plugin.md, the daemon
// executes the script DIRECTLY instead of asking an AI dog to run it. Code
// guards must run in code: six RESTART_POLECAT false fires happened because
// a correct guard lived in run.sh while the dog dispatch path only ever fed
// plugin.md to the dog as a prompt, so the guard never executed.
//
// The script's exit code gates whether the AI agent step dispatches at all:
//
//	0                     — script completed the plugin run; no agent step.
//	ScriptExitNeedsAgent  — script pre-checks passed and the plugin wants the
//	                        AI agent step; the daemon dispatches a dog with
//	                        the plugin.md instructions (plus the script's
//	                        output tail as context).
//	anything else/timeout — failure; recorded and logged; the agent step is
//	                        NOT dispatched (fail-safe).
const (
	// ScriptExitNeedsAgent is the documented run.sh exit code that requests
	// the AI agent step after the script's own checks complete.
	ScriptExitNeedsAgent = 10

	// DefaultScriptTimeout bounds run.sh execution when the plugin does not
	// declare execution.timeout in its frontmatter.
	DefaultScriptTimeout = 5 * time.Minute

	// scriptOutputTailLimit caps how much combined stdout/stderr is retained
	// from a script run (the tail is what carries the failure context).
	scriptOutputTailLimit = 8 * 1024
)

// ScriptResult is the outcome of executing a plugin's run.sh.
type ScriptResult struct {
	// ExitCode is the script's exit status (-1 when it timed out).
	ExitCode int

	// TimedOut is true when the script was killed at the execution timeout.
	TimedOut bool

	// Output is the tail of the script's combined stdout+stderr.
	Output string
}

// Success reports whether the script completed the plugin run on its own.
func (r *ScriptResult) Success() bool {
	return r != nil && !r.TimedOut && r.ExitCode == 0
}

// NeedsAgent reports whether the script requested the AI agent step.
func (r *ScriptResult) NeedsAgent() bool {
	return r != nil && !r.TimedOut && r.ExitCode == ScriptExitNeedsAgent
}

// ScriptTimeout returns the execution budget for this plugin's run.sh,
// from execution.timeout in the frontmatter or DefaultScriptTimeout.
func (p *Plugin) ScriptTimeout() time.Duration {
	if p.Execution != nil && p.Execution.Timeout != "" {
		if d, err := time.ParseDuration(p.Execution.Timeout); err == nil && d > 0 {
			return d
		}
	}
	return DefaultScriptTimeout
}

// RunScript executes the plugin's run.sh with the plugin directory as the
// working directory and GT_TOWN_ROOT set. Non-zero exit codes are returned
// in the ScriptResult, not as an error; the error return is reserved for
// failures to execute the script at all (missing file, spawn failure).
func RunScript(ctx context.Context, p *Plugin, townRoot string) (*ScriptResult, error) {
	scriptPath := filepath.Join(p.Path, "run.sh")
	if _, err := os.Stat(scriptPath); err != nil {
		return nil, fmt.Errorf("plugin %s: stat run.sh: %w", p.Name, err)
	}

	runCtx, cancel := context.WithTimeout(ctx, p.ScriptTimeout())
	defer cancel()

	cmd := exec.CommandContext(runCtx, "bash", scriptPath)
	cmd.Dir = p.Path
	cmd.Env = append(os.Environ(), "GT_TOWN_ROOT="+townRoot)
	// Kill the whole process group on timeout — otherwise a child holding
	// the output pipe keeps Run() blocked long past the deadline.
	util.SetProcessGroup(cmd)
	cmd.WaitDelay = 5 * time.Second

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	runErr := cmd.Run()
	result := &ScriptResult{Output: outputTail(buf.String(), scriptOutputTailLimit)}

	if runCtx.Err() != nil {
		result.TimedOut = true
		result.ExitCode = -1
		return result, nil
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		return nil, fmt.Errorf("plugin %s: running run.sh: %w", p.Name, runErr)
	}
	return result, nil
}

// FormatAgentStepMailBody formats dog instructions for the AI agent step of
// a plugin whose run.sh pre-check already ran and requested the agent step
// (exit ScriptExitNeedsAgent). Unlike FormatMailBody, it must NOT tell the
// dog to execute run.sh again — the daemon already did.
func (p *Plugin) FormatAgentStepMailBody(scriptOutput string) string {
	var sb bytes.Buffer

	sb.WriteString("Execute the following plugin (AI agent step):\n\n")
	sb.WriteString(fmt.Sprintf("**Plugin**: %s\n", p.Name))
	sb.WriteString(fmt.Sprintf("**Description**: %s\n", p.Description))
	if p.RigName != "" {
		sb.WriteString(fmt.Sprintf("**Rig**: %s\n", p.RigName))
	}
	sb.WriteString(fmt.Sprintf(
		"\nThe daemon already executed this plugin's run.sh pre-check; it exited with code %d, requesting the AI agent step. Do NOT re-run run.sh.\n",
		ScriptExitNeedsAgent))
	if scriptOutput != "" {
		sb.WriteString("\n**run.sh output tail**:\n\n```\n")
		sb.WriteString(scriptOutput)
		sb.WriteString("\n```\n")
	}
	sb.WriteString("\n---\n\n## Instructions\n\n")
	sb.WriteString(p.Instructions)
	sb.WriteString("\n\n---\n\n")
	sb.WriteString("After completion:\n")
	sb.WriteString("1. Follow the plugin's recording instructions above. If none are provided, run `gt plugin record-run --plugin " + p.Name + " --result <outcome> --title \"Plugin run: " + p.Name + "\"`.\n")
	sb.WriteString("2. Run `gt dog done` — this clears your work and auto-terminates the session. Run this even if recording fails.\n")

	return sb.String()
}

// outputTail returns the last max bytes of s, trimmed to start after the
// first newline inside the cut window so partial lines aren't shown.
func outputTail(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[len(s)-max:]
	if idx := bytes.IndexByte([]byte(cut), '\n'); idx >= 0 && idx+1 < len(cut) {
		cut = cut[idx+1:]
	}
	return cut
}
