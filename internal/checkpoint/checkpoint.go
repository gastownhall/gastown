// Package checkpoint provides session checkpointing for crash recovery.
// When a polecat session dies (context limit, crash, timeout), checkpoints
// allow the next session to recover state and resume work.
package checkpoint

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jonbaldie/gastown/internal/runtime"
	"github.com/jonbaldie/gastown/internal/util"
)

// Filename is the checkpoint file name within the polecat directory.
const Filename = ".polecat-checkpoint.json"

// Checkpoint represents a session recovery checkpoint.
type Checkpoint struct {
	// MoleculeID is the current molecule being worked.
	MoleculeID string `json:"molecule_id,omitempty"`

	// CurrentStep is the step ID currently in progress.
	CurrentStep string `json:"current_step,omitempty"`

	// StepTitle is the human-readable title of the current step.
	StepTitle string `json:"step_title,omitempty"`

	// ModifiedFiles lists files modified since the last commit.
	ModifiedFiles []string `json:"modified_files,omitempty"`

	// LastCommit is the SHA of the last commit.
	LastCommit string `json:"last_commit,omitempty"`

	// Branch is the current git branch.
	Branch string `json:"branch,omitempty"`

	// HookedBead is the bead ID on the agent's hook.
	HookedBead string `json:"hooked_bead,omitempty"`

	// Timestamp is when the checkpoint was written.
	Timestamp time.Time `json:"timestamp"`

	// SessionID identifies the session that wrote the checkpoint.
	SessionID string `json:"session_id,omitempty"`

	// Notes contains optional context from the session.
	Notes string `json:"notes,omitempty"`
}

// Path returns the checkpoint file path for a given polecat directory.
func Path(polecatDir string) string {
	return filepath.Join(polecatDir, Filename)
}

// Read loads a checkpoint from the polecat directory.
// Returns nil, nil if no checkpoint exists.
func Read(polecatDir string) (*Checkpoint, error) {
	path := Path(polecatDir)

	data, err := os.ReadFile(path) //nolint:gosec // G304: path is constructed from trusted polecatDir
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading checkpoint: %w", err)
	}

	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fmt.Errorf("parsing checkpoint: %w", err)
	}

	return &cp, nil
}

// Write saves a checkpoint to the polecat directory.
func Write(polecatDir string, cp *Checkpoint) error {
	// Set timestamp if not already set
	if cp.Timestamp.IsZero() {
		cp.Timestamp = time.Now()
	}

	// Set session ID from environment if available
	if cp.SessionID == "" {
		cp.SessionID = runtime.SessionIDFromEnv()
		if cp.SessionID == "" {
			cp.SessionID = fmt.Sprintf("pid-%d", os.Getpid())
		}
	}

	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling checkpoint: %w", err)
	}

	path := Path(polecatDir)
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing checkpoint: %w", err)
	}

	return nil
}

// Remove deletes the checkpoint file.
func Remove(polecatDir string) error {
	path := Path(polecatDir)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing checkpoint: %w", err)
	}
	return nil
}

// Capture creates a checkpoint by capturing current git and work state.
func Capture(polecatDir string) (*Checkpoint, error) {
	cp := &Checkpoint{
		Timestamp: time.Now(),
	}

	// Get modified files from git status
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = polecatDir
	util.SetDetachedProcessGroup(cmd)
	output, err := cmd.Output()
	if err == nil {
		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		for _, line := range lines {
			if len(line) > 3 {
				// Format: XY filename
				file := strings.TrimSpace(line[3:])
				if file != "" {
					cp.ModifiedFiles = append(cp.ModifiedFiles, file)
				}
			}
		}
	}

	// Get last commit SHA
	cmd = exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = polecatDir
	util.SetDetachedProcessGroup(cmd)
	output, err = cmd.Output()
	if err == nil {
		cp.LastCommit = strings.TrimSpace(string(output))
	}

	// Get current branch
	cmd = exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = polecatDir
	util.SetDetachedProcessGroup(cmd)
	output, err = cmd.Output()
	if err == nil {
		cp.Branch = strings.TrimSpace(string(output))
	}

	return cp, nil
}

// WithMolecule adds molecule context to a checkpoint.
func (cp *Checkpoint) WithMolecule(moleculeID, stepID, stepTitle string) *Checkpoint {
	cp.MoleculeID = moleculeID
	cp.CurrentStep = stepID
	cp.StepTitle = stepTitle
	return cp
}

// WithHookedBead adds hooked bead context to a checkpoint.
func (cp *Checkpoint) WithHookedBead(beadID string) *Checkpoint {
	cp.HookedBead = beadID
	return cp
}

// WithNotes adds context notes to a checkpoint.
func (cp *Checkpoint) WithNotes(notes string) *Checkpoint {
	cp.Notes = notes
	return cp
}

// Age returns how long ago the checkpoint was written.
func (cp *Checkpoint) Age() time.Duration {
	return time.Since(cp.Timestamp)
}

// IsStale returns true if the checkpoint is at or older than the threshold.
func (cp *Checkpoint) IsStale(threshold time.Duration) bool {
	return cp.Age() >= threshold
}

// Summary returns a concise summary of the checkpoint.
func (cp *Checkpoint) Summary() string {
	var parts []string

	if cp.MoleculeID != "" {
		if cp.CurrentStep != "" {
			parts = append(parts, fmt.Sprintf("molecule %s, step %s", cp.MoleculeID, cp.CurrentStep))
		} else {
			parts = append(parts, fmt.Sprintf("molecule %s", cp.MoleculeID))
		}
	}

	if cp.HookedBead != "" {
		parts = append(parts, fmt.Sprintf("hooked: %s", cp.HookedBead))
	}

	if len(cp.ModifiedFiles) > 0 {
		parts = append(parts, fmt.Sprintf("%d modified files", len(cp.ModifiedFiles)))
	}

	if cp.Branch != "" {
		parts = append(parts, fmt.Sprintf("branch: %s", cp.Branch))
	}

	if len(parts) == 0 {
		return "no significant state"
	}

	return strings.Join(parts, ", ")
}
