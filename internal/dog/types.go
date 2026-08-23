// Package dog manages Dogs - Deacon's helper workers for infrastructure tasks.
// Dogs are reusable workers with multi-rig worktrees, managed by the Deacon.
// Unlike polecats (single-rig, ephemeral sessions), dogs handle cross-rig infrastructure work.
package dog

import (
	"time"
)

// State represents a dog's operational state.
type State string

const (
	// StateIdle means the dog is available for work.
	StateIdle State = "idle"
	// StateWorking means the dog is executing a task.
	StateWorking State = "working"
)

// Dog represents a Deacon helper worker.
type Dog struct {
	Name          string            // Dog name (e.g., "alpha")
	State         State             // Current state
	Path          string            // Path to kennel dir (~/gt/deacon/dogs/<name>)
	Worktrees     map[string]string // Rig name -> worktree path
	LastActive    time.Time         // Last activity timestamp
	Work          string            // Current work assignment (bead ID or molecule)
	WorkStartedAt time.Time         // When current work was assigned
	CreatedAt     time.Time         // When dog was added to kennel
}

// DogState is the persistent state stored in .dog.json.
type DogState struct {
	Name          string            `json:"name"`
	State         State             `json:"state"`
	LastActive    time.Time         `json:"last_active"`
	Work          string            `json:"work,omitempty"`            // Current work assignment
	WorkStartedAt time.Time         `json:"work_started_at,omitempty"` // When work was assigned
	Worktrees     map[string]string `json:"worktrees,omitempty"`       // Rig -> path (for verification)
	SkippedRigs   []string          `json:"skipped_rigs,omitempty"`    // Rigs that were unreachable during Add
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}
