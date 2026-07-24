// Package beads provides agent bead management.
package beads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	beadsdk "github.com/steveyegge/beads"

	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/telemetry"
)

// lockAgentBead acquires an exclusive file lock for a specific agent bead ID.
// This prevents concurrent read-modify-write races in methods like
// CreateOrReopenAgentBead, ResetAgentBeadForReuse, and UpdateAgentDescriptionFields.
// Caller must defer fl.Unlock().
func (b *Beads) lockAgentBead(id string) (*flock.Flock, error) {
	lockDir := filepath.Join(b.getResolvedBeadsDir(), ".locks")
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		return nil, fmt.Errorf("creating bead lock dir: %w", err)
	}
	lockPath := filepath.Join(lockDir, fmt.Sprintf("agent-%s.lock", id))
	fl := flock.New(lockPath)
	if err := fl.Lock(); err != nil {
		return nil, fmt.Errorf("acquiring agent bead lock for %s: %w", id, err)
	}
	return fl, nil
}

// AgentFields holds structured fields for agent beads.
// These are stored as "key: value" lines in the description.
type AgentFields struct {
	RoleType          string // polecat, witness, refinery, deacon, mayor
	Rig               string // Rig name (empty for global agents like mayor/deacon)
	AgentState        string // spawning, working, done, stuck, escalated, idle, running, nuked
	HookBead          string // Currently pinned work bead ID
	CleanupStatus     string // ZFC: polecat self-reports git state (clean, has_uncommitted, has_stash, has_unpushed)
	ActiveMR          string // Currently active merge request bead ID (for traceability)
	NotificationLevel string // DND mode: verbose, normal, muted (default: normal)
	Mode              string // Execution mode: "" (normal) or "ralph" (Ralph Wiggum loop)
	// Note: RoleBead field removed - role definitions are now config-based.
	// See internal/config/roles/*.toml and config-based-roles.md.

	// Completion metadata fields (gt-x7t9).
	// Written by gt done, read by witness survey-workers to discover
	// completion state from beads instead of POLECAT_DONE mail.
	ExitType        string // COMPLETED, ESCALATED, DEFERRED, PHASE_COMPLETE (see witness.ExitType*)
	MRID            string // MR bead ID (if MR was created)
	Branch          string // Polecat working branch name
	LastSourceIssue string // Last source/work bead ID, preserved after hook_bead is cleared
	MRFailed        bool   // True when MR creation was attempted but failed
	PushFailed      bool   // True when branch push to origin failed (gas-556)
	CompletionTime  string // RFC3339 timestamp of when gt done was called
}

// Notification level constants
const (
	NotifyVerbose = "verbose" // All notifications (mail, convoy events, etc.)
	NotifyNormal  = "normal"  // Important events only (default)
	NotifyMuted   = "muted"   // Silent/DND mode - batch for later
)

// FormatAgentDescription creates a description string from agent fields.
func FormatAgentDescription(title string, fields *AgentFields) string {
	if fields == nil {
		return title
	}

	var lines []string
	lines = append(lines, title)
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("role_type: %s", fields.RoleType))

	if fields.Rig != "" {
		lines = append(lines, fmt.Sprintf("rig: %s", fields.Rig))
	} else {
		lines = append(lines, "rig: null")
	}

	lines = append(lines, fmt.Sprintf("agent_state: %s", fields.AgentState))

	if fields.HookBead != "" {
		lines = append(lines, fmt.Sprintf("hook_bead: %s", fields.HookBead))
	} else {
		lines = append(lines, "hook_bead: null")
	}

	// Note: role_bead field no longer written - role definitions are config-based

	if fields.CleanupStatus != "" {
		lines = append(lines, fmt.Sprintf("cleanup_status: %s", fields.CleanupStatus))
	} else {
		lines = append(lines, "cleanup_status: null")
	}

	if fields.ActiveMR != "" {
		lines = append(lines, fmt.Sprintf("active_mr: %s", fields.ActiveMR))
	} else {
		lines = append(lines, "active_mr: null")
	}

	if fields.NotificationLevel != "" {
		lines = append(lines, fmt.Sprintf("notification_level: %s", fields.NotificationLevel))
	} else {
		lines = append(lines, "notification_level: null")
	}

	if fields.Mode != "" {
		lines = append(lines, fmt.Sprintf("mode: %s", fields.Mode))
	}

	// Completion metadata fields (gt-x7t9)
	if fields.ExitType != "" {
		lines = append(lines, fmt.Sprintf("exit_type: %s", fields.ExitType))
	}
	if fields.MRID != "" {
		lines = append(lines, fmt.Sprintf("mr_id: %s", fields.MRID))
	}
	if fields.Branch != "" {
		lines = append(lines, fmt.Sprintf("branch: %s", fields.Branch))
	}
	if fields.LastSourceIssue != "" {
		lines = append(lines, fmt.Sprintf("last_source_issue: %s", fields.LastSourceIssue))
	}
	if fields.MRFailed {
		lines = append(lines, "mr_failed: true")
	}
	if fields.PushFailed {
		lines = append(lines, "push_failed: true")
	}
	if fields.CompletionTime != "" {
		lines = append(lines, fmt.Sprintf("completion_time: %s", fields.CompletionTime))
	}

	return strings.Join(lines, "\n")
}

// ParseAgentFields extracts agent fields from an issue's description.
func ParseAgentFields(description string) *AgentFields {
	fields := &AgentFields{}

	for _, line := range strings.Split(description, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		colonIdx := strings.Index(line, ":")
		if colonIdx == -1 {
			continue
		}

		key := strings.TrimSpace(line[:colonIdx])
		value := strings.TrimSpace(line[colonIdx+1:])
		if value == "null" || value == "" {
			value = ""
		}

		switch strings.ToLower(key) {
		case "role_type":
			fields.RoleType = value
		case "rig":
			fields.Rig = value
		case "agent_state":
			fields.AgentState = value
		case "hook_bead":
			fields.HookBead = value
		case "cleanup_status":
			fields.CleanupStatus = value
		case "active_mr":
			fields.ActiveMR = value
		case "notification_level":
			fields.NotificationLevel = value
		case "mode":
			fields.Mode = value
		// Completion metadata fields (gt-x7t9)
		case "exit_type":
			fields.ExitType = value
		case "mr_id":
			fields.MRID = value
		case "branch":
			fields.Branch = value
		case "last_source_issue":
			fields.LastSourceIssue = value
		case "mr_failed":
			fields.MRFailed = value == "true"
		case "push_failed":
			fields.PushFailed = value == "true"
		case "completion_time":
			fields.CompletionTime = value
		}
	}

	return fields
}

// CreateAgentBead creates an agent bead for tracking agent lifecycle.
// The ID format is: <prefix>-<rig>-<role>-<name> (e.g., gt-gastown-polecat-Toast)
// Use AgentBeadID() helper to generate correct IDs.
// The created_by field is populated from BD_ACTOR env var for provenance tracking.
//
// This function automatically ensures custom types are configured in the target
// database before creating the bead. This handles multi-repo routing scenarios
// where the bead may be routed to a different database than the one this wrapper
// is connected to.
func (b *Beads) CreateAgentBead(id, title string, fields *AgentFields) (*Issue, error) {
	// Guard against flag-like titles (gt-e0kx5: --help garbage beads)
	if IsFlagLikeTitle(title) {
		return nil, fmt.Errorf("refusing to create agent bead: %w (got %q)", ErrFlagTitle, title)
	}

	target := b.agentBeadTarget()
	targetDir := target.getResolvedBeadsDir()

	description := FormatAgentDescription(title, fields)
	if issue, err := target.createAgentBeadViaStore(context.Background(), id, title, description); err == nil {
		return issue, nil
	}

	// Ensure target database has custom types configured before falling back to
	// the bd CLI. The store path above avoids stale external bd schema during
	// fresh install; this remains for older stores or non-server configurations.
	_ = EnsureCustomTypes(targetDir)

	buildArgs := func() []string {
		a := []string{"create", "--json",
			"--id=" + id,
			"--title=" + title,
			"--description=" + description,
			"--type=task",
			"--labels=gt:agent",
		}
		if NeedsForceForID(id) {
			a = append(a, "--force")
		}
		// Default actor from BD_ACTOR env var for provenance tracking
		// Uses getActor() to respect isolated mode (tests)
		if actor := target.getActor(); actor != "" {
			a = append(a, "--actor="+actor)
		}
		return a
	}

	out, err := target.run(buildArgs()...)
	if err != nil {
		out, err = target.run(buildArgs()...)
		if err != nil {
			return nil, fmt.Errorf("creating %s: bd create failed: %w", id, err)
		}
	}

	var issue Issue
	if err := json.Unmarshal(out, &issue); err != nil {
		return nil, fmt.Errorf("parsing bd create output: %w", err)
	}

	// Note: role slot no longer set - role definitions are config-based
	// Note: hook_bead slot no longer set - bd slot removed in v0.62 (hq-l6mm5)

	return &issue, nil
}

func (b *Beads) createAgentBeadViaStore(ctx context.Context, id, title, description string) (*Issue, error) {
	store, cleanup, err := b.OpenStore(ctx)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	now := time.Now().UTC()
	actor := b.getActor()
	issue := &beadsdk.Issue{
		ID:          id,
		Title:       title,
		Description: description,
		Status:      beadsdk.StatusOpen,
		Priority:    2,
		IssueType:   beadsdk.TypeTask,
		CreatedAt:   now,
		UpdatedAt:   now,
		CreatedBy:   actor,
		Labels:      []string{"gt:agent"},
	}
	if err := store.CreateIssue(ctx, issue, actor); err != nil {
		return nil, err
	}
	return sdkIssueToIssue(issue), nil
}

// CreateOrReopenAgentBead creates an agent bead or reopens an existing one.
// This handles the case where a polecat is nuked and re-spawned with the same name:
// the old agent bead exists (open or closed), so we update it instead of
// failing with a UNIQUE constraint error.
//
// The function:
// 1. Tries to create the agent bead
// 2. If create fails, checks if bead exists (via bd show)
// 3. If bead exists and is closed, reopens it
// 4. Updates the bead with new fields regardless of prior state
//
// This is robust against Dolt backend issues where bd close/reopen may fail:
// - If nuke used ResetAgentBeadForReuse, bead is open → update directly
// - If bead is closed (legacy state), reopen then update
// - If bead is in unknown state, falls back to show+update
func (b *Beads) CreateOrReopenAgentBead(id, title string, fields *AgentFields) (*Issue, error) {
	// First try to create the bead (no lock needed - create is atomic)
	issue, err := b.CreateAgentBead(id, title, fields)
	if err == nil {
		return issue, nil
	}

	// Create failed - need to do Show→Reopen→Update which requires locking
	// to prevent concurrent modifications (e.g., nuke clearing fields while
	// spawn is updating them). See gt-joazs.
	fl, lockErr := b.lockAgentBead(id)
	if lockErr != nil {
		return nil, fmt.Errorf("locking agent bead %s: %w", id, lockErr)
	}
	defer func() { _ = fl.Unlock() }()

	// Create failed - check if bead already exists (handles both open and closed states)
	createErr := err

	target := b.agentBeadTarget()

	existing, showErr := target.Show(id)
	if showErr != nil {
		// Bead doesn't exist (or can't be read) - return original create error
		return nil, createErr
	}

	// If bead is closed, reopen it first
	if existing.Status == "closed" {
		if _, reopenErr := target.run("reopen", id, "--reason=re-spawning agent"); reopenErr != nil {
			// Reopen failed - try setting status to open via update as fallback
			// This handles Dolt backends where bd reopen may not work
			openStatus := "open"
			if updateErr := target.Update(id, UpdateOptions{Status: &openStatus}); updateErr != nil {
				return nil, fmt.Errorf("could not reopen agent bead %s (reopen: %v, update: %v, original: %v)",
					id, reopenErr, updateErr, createErr)
			}
		}
	}

	// Update the bead with new fields and ensure gt:agent label is set.
	// Agent beads use type=task (a valid built-in type) and are identified
	// by the gt:agent label, not by type (see IsAgentBead).
	description := FormatAgentDescription(title, fields)
	updateOpts := UpdateOptions{
		Title:       &title,
		Description: &description,
		SetLabels:   labelsForAgentBeadReuse(existing.Labels),
	}
	if err := target.Update(id, updateOpts); err != nil {
		return nil, fmt.Errorf("updating agent bead: %w", err)
	}

	// Note: role slot no longer set - role definitions are config-based
	// Note: hook_bead slot no longer set - bd slot removed in v0.62 (hq-l6mm5)

	// Return the updated bead
	return target.Show(id)
}

func labelsForAgentBeadReuse(existing []string) []string {
	labels := []string{"gt:agent"}
	seen := map[string]bool{"gt:agent": true}
	for _, label := range existing {
		if !strings.HasPrefix(label, "safety_stop:") || seen[label] {
			continue
		}
		labels = append(labels, label)
		seen[label] = true
	}
	return labels
}

// ResetAgentBeadForReuse clears all mutable fields on an agent bead without closing it.
// This is the preferred cleanup method during polecat nuke because it avoids the
// close/reopen cycle that fails on Dolt backends (tombstone operations not supported,
// bd reopen failures). By keeping the bead open with agent_state="nuked",
// CreateOrReopenAgentBead can simply update it on re-spawn without needing reopen.
//
// This is the standard nuke path (gt-14b8o).
func (b *Beads) ResetAgentBeadForReuse(id, reason string) error {
	// Lock the agent bead to prevent concurrent read-modify-write races.
	// Without this, a concurrent CreateOrReopenAgentBead could overwrite
	// the nuked state we're about to set. See gt-joazs.
	fl, lockErr := b.lockAgentBead(id)
	if lockErr != nil {
		return fmt.Errorf("locking agent bead %s: %w", id, lockErr)
	}
	defer func() { _ = fl.Unlock() }()

	target := b.agentBeadTarget()

	// Get current issue to preserve immutable fields (title, role_type, rig)
	issue, err := target.Show(id)
	if err != nil {
		return err
	}

	// Parse existing fields and clear mutable ones
	fields := ParseAgentFields(issue.Description)
	fields.HookBead = ""      // Clear hook_bead
	fields.ActiveMR = ""      // Clear active_mr
	fields.CleanupStatus = "" // Clear cleanup_status
	fields.Mode = ""          // Clear Ralph-mode threshold marker
	fields.AgentState = string(AgentStateNuked)
	// Clear completion metadata (gt-x7t9)
	fields.ExitType = ""
	fields.MRID = ""
	fields.Branch = ""
	fields.LastSourceIssue = ""
	fields.MRFailed = false
	fields.PushFailed = false
	fields.CompletionTime = ""

	// Update description with cleared fields
	description := FormatAgentDescription(issue.Title, fields)
	if err := target.Update(id, UpdateOptions{Description: &description}); err != nil {
		return fmt.Errorf("resetting agent bead fields: %w", err)
	}

	// Hook slot no longer maintained (hq-l6mm5) — no need to clear.

	return nil
}

// UpdateAgentState updates the agent_state field in an agent bead.
// bd >= 0.62.0 no longer provides a supported `bd agent state` writer, so
// Gastown writes agent_state through the description field and readers mirror
// that contract with fallback to the legacy structured column via ResolveAgentState.
//
// Resolves the concrete target DB first so the update hits the correct database
// when the agent bead routes to a different beads dir via routes.jsonl.
func (b *Beads) UpdateAgentState(id string, state string) (retErr error) {
	defer func() { telemetry.RecordAgentStateChange(context.Background(), id, state, nil, retErr) }()
	target := b.agentBeadTarget()
	return target.UpdateAgentDescriptionFields(id, AgentFieldUpdates{AgentState: &state})
}

// SetHookBead and ClearHookBead removed (hq-l6mm5).
// Hook slot on agent beads is no longer maintained. Work bead status=hooked
// and assignee=<agent> is the authoritative source for hook tracking.

// AgentFieldUpdates specifies which agent description fields to update.
// Only non-nil fields are modified; nil fields are left unchanged.
// This allows multiple fields to be updated in a single read-modify-write
// cycle, avoiding races where concurrent callers overwrite each other's changes.
type AgentFieldUpdates struct {
	AgentState        *string // Sync description agent_state with column (gt-ulom)
	CleanupStatus     *string
	ActiveMR          *string
	NotificationLevel *string
	Mode              *string
	HookBead          *string // Clear hook_bead on completion (gt-qbh)
	// Completion metadata fields (gt-x7t9)
	ExitType        *string
	MRID            *string
	Branch          *string
	LastSourceIssue *string
	MRFailed        *bool
	PushFailed      *bool // True when branch push to origin failed (gas-556)
	CompletionTime  *string
}

// UpdateAgentDescriptionFields atomically updates one or more agent description
// fields in a single Show-Parse-Modify-Update cycle. This prevents the race
// condition where concurrent callers updating different fields overwrite each
// other because the entire description is replaced.
func (b *Beads) UpdateAgentDescriptionFields(id string, updates AgentFieldUpdates) error {
	if target := b.agentBeadTarget(); target != b {
		return target.UpdateAgentDescriptionFields(id, updates)
	}

	// Validate notification level if provided
	if updates.NotificationLevel != nil {
		level := *updates.NotificationLevel
		if level != "" && level != NotifyVerbose && level != NotifyNormal && level != NotifyMuted {
			return fmt.Errorf("invalid notification level %q: must be verbose, normal, or muted", level)
		}
	}

	// Lock the agent bead to prevent concurrent read-modify-write races.
	// Without this, concurrent callers updating different fields could overwrite
	// each other's changes. See gt-joazs.
	fl, lockErr := b.lockAgentBead(id)
	if lockErr != nil {
		return fmt.Errorf("locking agent bead %s: %w", id, lockErr)
	}
	defer func() { _ = fl.Unlock() }()

	issue, err := b.Show(id)
	if err != nil {
		return err
	}

	fields := ParseAgentFields(issue.Description)

	if updates.AgentState != nil {
		fields.AgentState = *updates.AgentState
	}
	if updates.CleanupStatus != nil {
		fields.CleanupStatus = *updates.CleanupStatus
	}
	if updates.ActiveMR != nil {
		fields.ActiveMR = *updates.ActiveMR
	}
	if updates.NotificationLevel != nil {
		fields.NotificationLevel = *updates.NotificationLevel
	}
	if updates.Mode != nil {
		fields.Mode = *updates.Mode
	}
	if updates.HookBead != nil {
		fields.HookBead = *updates.HookBead
	}
	// Completion metadata fields (gt-x7t9)
	if updates.ExitType != nil {
		fields.ExitType = *updates.ExitType
	}
	if updates.MRID != nil {
		fields.MRID = *updates.MRID
	}
	if updates.Branch != nil {
		fields.Branch = *updates.Branch
	}
	if updates.LastSourceIssue != nil {
		fields.LastSourceIssue = *updates.LastSourceIssue
	}
	if updates.MRFailed != nil {
		fields.MRFailed = *updates.MRFailed
	}
	if updates.PushFailed != nil {
		fields.PushFailed = *updates.PushFailed
	}
	if updates.CompletionTime != nil {
		fields.CompletionTime = *updates.CompletionTime
	}

	description := FormatAgentDescription(issue.Title, fields)
	return b.Update(id, UpdateOptions{Description: &description})
}

// UpdateAgentCleanupStatus updates the cleanup_status field in an agent bead.
// This is called by the polecat to self-report its git state (ZFC compliance).
// Valid statuses: clean, has_uncommitted, has_stash, has_unpushed
func (b *Beads) UpdateAgentCleanupStatus(id string, cleanupStatus string) error {
	return b.UpdateAgentDescriptionFields(id, AgentFieldUpdates{CleanupStatus: &cleanupStatus})
}

// UpdateAgentActiveMR updates the active_mr field in an agent bead.
// This links the agent to their current merge request for traceability.
// Pass empty string to clear the field (e.g., after merge completes).
func (b *Beads) UpdateAgentActiveMR(id string, activeMR string) error {
	return b.UpdateAgentDescriptionFields(id, AgentFieldUpdates{ActiveMR: &activeMR})
}

// ClearAgentActiveMRIfMatches clears active_mr only when it still references
// expectedMR. It returns true when a clear was written.
func (b *Beads) ClearAgentActiveMRIfMatches(id string, expectedMR string) (bool, error) {
	if target := b.agentBeadTarget(); target != b {
		return target.ClearAgentActiveMRIfMatches(id, expectedMR)
	}

	id = strings.TrimSpace(id)
	expectedMR = strings.TrimSpace(expectedMR)
	if id == "" || expectedMR == "" {
		return false, nil
	}

	fl, lockErr := b.lockAgentBead(id)
	if lockErr != nil {
		return false, fmt.Errorf("locking agent bead %s: %w", id, lockErr)
	}
	defer func() { _ = fl.Unlock() }()

	issue, err := b.Show(id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	if !IsAgentBead(issue) {
		return false, fmt.Errorf("%s is not an agent bead", id)
	}

	fields := ParseAgentFields(issue.Description)
	if strings.TrimSpace(fields.ActiveMR) != expectedMR {
		return false, nil
	}

	fields.ActiveMR = ""
	description := FormatAgentDescription(issue.Title, fields)
	if err := b.Update(id, UpdateOptions{Description: &description}); err != nil {
		return false, err
	}
	return true, nil
}

// UpdateAgentNotificationLevel updates the notification_level field in an agent bead.
// Valid levels: verbose, normal, muted (DND mode).
// Pass empty string to reset to default (normal).
func (b *Beads) UpdateAgentNotificationLevel(id string, level string) error {
	return b.UpdateAgentDescriptionFields(id, AgentFieldUpdates{NotificationLevel: &level})
}

// CompletionMetadata holds the fields written by gt done to record
// polecat work completion on the agent bead. The witness survey-workers
// step reads these fields to discover completion state from beads
// instead of POLECAT_DONE mail (nudge-over-mail redesign, gt-x7t9).
type CompletionMetadata struct {
	ExitType       string // COMPLETED, ESCALATED, DEFERRED, PHASE_COMPLETE
	MRID           string // MR bead ID (empty if no MR)
	Branch         string // Polecat working branch
	HookBead       string // The work bead ID
	MRFailed       bool   // True when MR creation was attempted but failed
	PushFailed     bool   // True when branch push to origin failed (gas-556)
	CompletionTime string // RFC3339 timestamp
}

// UpdateAgentCompletion atomically writes all completion metadata fields
// to an agent bead. Called by gt done to record completion state.
func (b *Beads) UpdateAgentCompletion(id string, meta *CompletionMetadata) error {
	mrFailed := meta.MRFailed
	pushFailed := meta.PushFailed
	return b.UpdateAgentDescriptionFields(id, AgentFieldUpdates{
		ExitType:        &meta.ExitType,
		MRID:            &meta.MRID,
		Branch:          &meta.Branch,
		LastSourceIssue: &meta.HookBead,
		MRFailed:        &mrFailed,
		PushFailed:      &pushFailed,
		CompletionTime:  &meta.CompletionTime,
	})
}

// ClearAgentCompletion removes all completion metadata fields from an agent bead.
// Called when a polecat is re-slung with new work (resets stale completion state).
func (b *Beads) ClearAgentCompletion(id string) error {
	empty := ""
	notFailed := false
	return b.UpdateAgentDescriptionFields(id, AgentFieldUpdates{
		ExitType:        &empty,
		MRID:            &empty,
		Branch:          &empty,
		LastSourceIssue: &empty,
		MRFailed:        &notFailed,
		PushFailed:      &notFailed,
		CompletionTime:  &empty,
	})
}

// GetAgentNotificationLevel returns the notification level for an agent.
// Returns "normal" if not set (the default).
func (b *Beads) GetAgentNotificationLevel(id string) (string, error) {
	_, fields, err := b.GetAgentBead(id)
	if err != nil {
		return "", err
	}
	if fields == nil {
		return NotifyNormal, nil
	}
	if fields.NotificationLevel == "" {
		return NotifyNormal, nil
	}
	return fields.NotificationLevel, nil
}

// GetAgentBead retrieves an agent bead by ID.
// Returns nil if not found.
func (b *Beads) GetAgentBead(id string) (*Issue, *AgentFields, error) {
	if target := b.agentBeadTarget(); target != b {
		return target.GetAgentBead(id)
	}

	issue, err := b.Show(id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	if !IsAgentBead(issue) {
		return nil, nil, fmt.Errorf("issue %s is not an agent bead (type=%s)", id, issue.Type)
	}

	fields := ParseAgentFields(issue.Description)
	fields.AgentState = ResolveAgentState(issue.Description, issue.AgentState)
	return issue, fields, nil
}

// ListAgentBeads returns all agent beads in a single query.
// Returns a map of agent bead ID to Issue.
//
// Queries both the issues table (authoritative metadata source) and the
// wisps table (fallback existence source). Issues take precedence for duplicate
// IDs so labels/type are preserved for doctor validation.
func (b *Beads) ListAgentBeads() (map[string]*Issue, error) {
	// Query issues table first. Issues include labels and type metadata used by
	// doctor checks (for example, validating gt:agent labels).
	// Agent beads are type=agent (infrastructure), hidden by bd list default filter.
	// Use --include-infra so they appear in results.
	out, err := b.run("list", "--label=gt:agent", "--include-infra", "--json", "--flat", "--no-pager")
	if err != nil {
		return nil, err
	}
	issuesByID := make(map[string]*Issue)
	var issues []*Issue
	if jsonErr := json.Unmarshal(out, &issues); jsonErr != nil {
		return nil, fmt.Errorf("parsing bd list --json output: %w (raw output %d bytes)", jsonErr, len(out))
	}
	for _, issue := range issues {
		issuesByID[issue.ID] = issue
	}

	// Query wisps table as a fallback source.
	// Keep issues-table entries when both exist for the same ID so richer
	// metadata (labels/type) is preserved.
	wispBeads, _ := b.ListAgentBeadsFromWisps()

	return mergeAgentBeadSources(issuesByID, wispBeads), nil
}

// mergeAgentBeadSources merges issue-backed and wisp-backed agent bead maps.
// Issues are authoritative because they carry full metadata (labels/type),
// while wisps are treated as a fallback existence source.
func mergeAgentBeadSources(issuesByID, wispsByID map[string]*Issue) map[string]*Issue {
	merged := make(map[string]*Issue, len(issuesByID)+len(wispsByID))
	for id, issue := range issuesByID {
		merged[id] = issue
	}
	for id, issue := range wispsByID {
		if _, exists := merged[id]; !exists {
			merged[id] = issue
		}
	}
	return merged
}

// ListAgentBeadsFromWisps queries the wisps table for agent beads.
// Returns nil, nil if the wisps table doesn't exist yet or has no agent beads.
func (b *Beads) ListAgentBeadsFromWisps() (map[string]*Issue, error) {
	out, err := b.run("mol", "wisp", "list", "--json")
	if err != nil {
		return nil, nil // Wisps table may not exist yet
	}

	// bd mol wisp list --json returns {"wisps": [...], "count": N, ...}
	var wrapper struct {
		Wisps []*Issue `json:"wisps"`
	}
	if err := json.Unmarshal(out, &wrapper); err != nil {
		return nil, nil
	}

	result := make(map[string]*Issue)
	for _, w := range wrapper.Wisps {
		// Check by type/label first (works when fields are present)
		if IsAgentBead(w) {
			result[w.ID] = w
			continue
		}
		// Fallback: wisps JSON may omit issue_type/labels fields.
		// Detect agent beads by ID pattern (prefix-rig-role format).
		if isAgentBeadByID(w.ID) {
			result[w.ID] = w
		}
	}

	return result, nil
}

// isAgentBeadByID detects agent beads by their ID naming convention.
// Agent bead IDs follow two patterns:
//   - Full form (prefix != rig): prefix-rig-role[-name] (e.g., gt-gastown-witness)
//   - Collapsed form (prefix == rig): prefix-role[-name] (e.g., bcc-witness)
//
// where role is one of: witness, refinery, crew, polecat, deacon, mayor.
// The collapsed form has only 2 parts for role-only IDs, so we must check
// from parts[1:] not parts[2:].
func isAgentBeadByID(id string) bool {
	parts := strings.Split(id, "-")
	if len(parts) < 2 {
		return false
	}
	// Check parts[1:] to handle both full-form (role at parts[2]) and
	// collapsed-form (role at parts[1]) agent bead IDs.
	for _, part := range parts[1:] {
		switch part {
		case constants.RoleWitness, constants.RoleRefinery, constants.RoleCrew, constants.RolePolecat, constants.RoleDeacon, constants.RoleMayor:
			return true
		}
	}
	return false
}

// ListWispIDs returns a set of all wisp IDs in the wisps table.
// This is useful for existence checks where wisp metadata (type, labels)
// may not be available in the list output.
func (b *Beads) ListWispIDs() (map[string]bool, error) {
	out, err := b.run("mol", "wisp", "list", "--json")
	if err != nil {
		return nil, nil
	}

	var wrapper struct {
		Wisps []struct {
			ID string `json:"id"`
		} `json:"wisps"`
	}
	if err := json.Unmarshal(out, &wrapper); err != nil {
		return nil, nil
	}

	result := make(map[string]bool, len(wrapper.Wisps))
	for _, w := range wrapper.Wisps {
		result[w.ID] = true
	}
	return result, nil
}
