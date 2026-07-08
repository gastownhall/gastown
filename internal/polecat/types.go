// Package polecat provides polecat lifecycle management.
package polecat

import "time"

// State represents the current lifecycle state of a polecat.
//
// Polecat identity is persistent, but clean completion retires the live session.
// The primary operating states are:
//
//   - Working: Session active, doing assigned work (normal operation)
//   - Idle: Available before assignment, with no pending completion cleanup
//   - Done: Work completed, session retired, cleanup/refinery still owns state
//   - ReviewNeeded: Session is live but no active work bead is attached
//   - Stalled: Session stopped unexpectedly, was never nudged back to life
//   - Zombie: Session called 'gt done' but cleanup failed - tried to die but couldn't
//
// The distinction matters: idle polecats are available capacity. Done polecats
// completed work and are waiting for cleanup/refinery state to clear. Stalled
// polecats failed mid-work. Zombies tried to exit but couldn't complete cleanup.
//
// Note: These are LIFECYCLE states. The polecat IDENTITY (CV chain, mailbox,
// work history) persists across sessions. Worktrees persist only while active
// or awaiting cleanup; they are not reused after clean completion with pending state.
//
// "Stalled", "zombie", and related conditions are detected at query time by
// cross-checking tmux session liveness against beads state. The Witness also
// detects them through monitoring (tmux state, age in StateDone, etc.).
type State string

const (
	// StateWorking means the polecat session is actively working on an issue.
	// This is the initial and primary state after sling.
	StateWorking State = "working"

	// StateIdle means the polecat is available before assignment. It has no
	// hook_bead, no active session, and no pending completion/MR cleanup state.
	StateIdle State = "idle"

	// StateDone means the polecat has completed its assigned work and called
	// 'gt done'. This is normally a transient state - the session should exit
	// immediately after. If a polecat remains in StateDone, it's a "zombie":
	// the cleanup failed and the session is stuck.
	StateDone State = "done"

	// StateReviewNeeded means a tmux session is still live but no current hooked
	// or assigned work bead exists, and cleanup status is not clean enough to
	// reuse safely. This prevents reporting "working" with Issue:none without
	// making the slot reusable before recovery decides what to do with the branch.
	StateReviewNeeded State = "review-needed"

	// StateStuck means the polecat has explicitly signaled it needs assistance.
	// This is an intentional request for help from the polecat itself.
	// Different from "stalled" (detected externally when session stops working).
	StateStuck State = "stuck"

	// StateStalled means the polecat's tmux session has died while work was still
	// assigned. This is a detected condition: beads report the polecat as working
	// (hooked bead, assigned issue) but the tmux session is gone or the agent
	// process is dead. This typically happens after disk space exhaustion, OOM,
	// or other system failures that kill sessions without cleanup.
	// Unlike "stuck" (polecat self-reports), stalled is detected externally.
	StateStalled State = "stalled"

	// StateZombie means a tmux session exists but has no corresponding worktree directory.
	// This is a detected condition: the polecat was incompletely nuked or has a
	// session naming mismatch, leaving an orphaned tmux session.
	StateZombie State = "zombie"
)

// IsWorking returns true if the polecat is currently working.
func (s State) IsWorking() bool {
	return s == StateWorking
}

// IsStalled returns true if the polecat's session has died while work was assigned.
func (s State) IsStalled() bool {
	return s == StateStalled
}

// IsIdle returns true if the polecat has completed work and is available for reuse.
func (s State) IsIdle() bool {
	return s == StateIdle
}

// Polecat represents a worker agent in a rig.
type Polecat struct {
	// Name is the polecat identifier.
	Name string `json:"name"`

	// Rig is the rig this polecat belongs to.
	Rig string `json:"rig"`

	// State is the current lifecycle state.
	State State `json:"state"`

	// ClonePath is the path to the polecat's clone of the rig.
	ClonePath string `json:"clone_path"`

	// Branch is the current git branch.
	Branch string `json:"branch"`

	// Issue is the currently assigned issue ID (if any).
	Issue string `json:"issue,omitempty"`

	// CreatedAt is when the polecat was created.
	CreatedAt time.Time `json:"created_at"`

	// UpdatedAt is when the polecat was last updated.
	UpdatedAt time.Time `json:"updated_at"`
}

// Summary provides a concise view of polecat status.
type Summary struct {
	Name  string `json:"name"`
	State State  `json:"state"`
	Issue string `json:"issue,omitempty"`
}

// Summary returns a Summary for this polecat.
func (p *Polecat) Summary() Summary {
	return Summary{
		Name:  p.Name,
		State: p.State,
		Issue: p.Issue,
	}
}

// CleanupStatus represents the git state of a polecat for cleanup decisions.
// The Witness uses this to determine whether it's safe to nuke a polecat worktree.
type CleanupStatus string

const (
	// CleanupClean means the worktree has no uncommitted work and is safe to remove.
	CleanupClean CleanupStatus = "clean"

	// CleanupUncommitted means there are uncommitted changes in the worktree.
	CleanupUncommitted CleanupStatus = "has_uncommitted"

	// CleanupStash means there are stashed changes that would be lost.
	CleanupStash CleanupStatus = "has_stash"

	// CleanupUnpushed means there are commits not pushed to the remote.
	CleanupUnpushed CleanupStatus = "has_unpushed"

	// CleanupUnknown means the status could not be determined.
	CleanupUnknown CleanupStatus = "unknown"
)

// IsSafe returns true if the status indicates it's safe to remove the worktree
// without losing any work.
func (s CleanupStatus) IsSafe() bool {
	return s == CleanupClean
}

// RequiresRecovery returns true if the status indicates there is work that
// needs to be recovered before removal. This includes uncommitted changes,
// stashes, and unpushed commits.
func (s CleanupStatus) RequiresRecovery() bool {
	switch s {
	case CleanupUncommitted, CleanupStash, CleanupUnpushed:
		return true
	default:
		return false
	}
}

// CanForceRemove returns true if the status allows forced removal.
// Force removal bypasses all git safety checks including unpushed commits.
// Stashes are excluded since they represent intentional work-in-progress.
func (s CleanupStatus) CanForceRemove() bool {
	return s == CleanupClean || s == CleanupUncommitted || s == CleanupUnpushed
}
