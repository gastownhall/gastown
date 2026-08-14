// Package dog manages Dogs - Deacon's helper workers for infrastructure tasks.
// Dogs are reusable workers with multi-rig worktrees, managed by the Deacon.
// Unlike polecats (single-rig, ephemeral sessions), dogs handle cross-rig infrastructure work.
package dog

import (
	"strings"
	"time"
)

// State represents a dog's operational state.
type State string
type WorkKind string

const (
	WorkKindBead    WorkKind = "bead"
	WorkKindFormula WorkKind = "formula"
	WorkKindPlugin  WorkKind = "plugin"

	// StateIdle means the dog is available for work.
	StateIdle State = "idle"
	// StateWorking means the dog is executing a task.
	StateWorking State = "working"
)

// CanClearStateOnly reports whether recovery may clear work without updating
// an authoritative source bead. New source-backed dispatches are explicitly
// typed; empty kind retains the pre-kind legacy recovery behavior.
func CanClearStateOnly(work string, kind WorkKind) bool {
	return kind == WorkKindPlugin || (kind == "" && strings.HasPrefix(work, "plugin:"))
}

// Dog represents a Deacon helper worker.
type Dog struct {
	Name          string            // Dog name (e.g., "alpha")
	State         State             // Current state
	Path          string            // Path to kennel dir (~/gt/deacon/dogs/<name>)
	Worktrees     map[string]string // Rig name -> worktree path
	LastActive    time.Time         // Last activity timestamp
	Work          string            // Current work assignment (bead ID or molecule)
	WorkKind      WorkKind          // Whether Work is a source bead or formula name
	WorkSourceID  string            // Exact source bead ID for bead/formula work
	WorkStartedAt time.Time         // When current work was assigned
	CreatedAt     time.Time         // When dog was added to kennel
}

// DogState is the persistent state stored in .dog.json.
type DogState struct {
	Name          string            `json:"name"`
	State         State             `json:"state"`
	LastActive    time.Time         `json:"last_active"`
	Work          string            `json:"work,omitempty"`            // Current work assignment
	WorkKind      WorkKind          `json:"work_kind,omitempty"`       // bead or formula
	WorkSourceID  string            `json:"work_source_id,omitempty"`  // Exact source bead ID
	WorkStartedAt time.Time         `json:"work_started_at,omitempty"` // When work was assigned
	Worktrees     map[string]string `json:"worktrees,omitempty"`       // Rig -> path (for verification)
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}
