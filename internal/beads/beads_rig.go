// Package beads provides rig identity bead management.
package beads

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// RigState represents the operational state of a rig.
type RigState string

const (
	// RigStateActive means the rig is operational and accepting work.
	RigStateActive RigState = "active"
	// RigStateArchived means the rig is no longer in use.
	RigStateArchived RigState = "archived"
	// RigStateMaintenance means the rig is temporarily offline for maintenance.
	RigStateMaintenance RigState = "maintenance"
)

// ValidRigState returns true if the given state is a recognized rig state.
func ValidRigState(s RigState) bool {
	switch s {
	case RigStateActive, RigStateArchived, RigStateMaintenance:
		return true
	}
	return false
}

// RigFields contains the fields specific to rig identity beads.
type RigFields struct {
	Repo   string   // Git URL for the rig's repository
	Prefix string   // Beads prefix for this rig (e.g., "gt", "bd")
	State  RigState // Operational state: active, archived, maintenance
}

// FormatRigDescription formats the description field for a rig identity bead.
func FormatRigDescription(name string, fields *RigFields) string {
	if fields == nil {
		return ""
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("Rig identity bead for %s.", name))
	lines = append(lines, "")

	if fields.Repo != "" {
		lines = append(lines, fmt.Sprintf("repo: %s", fields.Repo))
	}
	if fields.Prefix != "" {
		lines = append(lines, fmt.Sprintf("prefix: %s", fields.Prefix))
	}
	if fields.State != "" {
		lines = append(lines, fmt.Sprintf("state: %s", string(fields.State)))
	}

	return strings.Join(lines, "\n")
}

// ParseRigFields extracts rig fields from an issue's description.
func ParseRigFields(description string) *RigFields {
	fields := &RigFields{}

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
		case "repo":
			fields.Repo = value
		case "prefix":
			fields.Prefix = value
		case "state":
			fields.State = RigState(value)
		}
	}

	return fields
}

// EnsureRigBead returns the rig identity bead, creating it if it doesn't exist.
// This is idempotent: if the bead already exists, it is returned as-is.
// Handles races and Dolt query hiccups where Show may fail even when the bead
// exists (gt-d8681).
func (b *Beads) EnsureRigBead(name string, fields *RigFields) (*Issue, error) {
	prefix := "gt"
	if fields != nil && fields.Prefix != "" {
		prefix = fields.Prefix
	}
	id := RigBeadIDWithPrefix(prefix, name)

	// Try to find existing bead first
	if existing, err := b.Show(id); err == nil {
		return existing, nil
	}

	// Not found — try to create
	created, createErr := b.CreateRigBead(name, fields)
	if createErr == nil {
		return created, nil
	}

	// Create failed (likely duplicate key from race or Dolt hiccup) — retry Show
	if existing, err := b.Show(id); err == nil {
		return existing, nil
	}

	return nil, fmt.Errorf("ensuring rig bead %s: %w", id, createErr)
}

// CreateRigBead creates a rig identity bead for tracking rig metadata.
// The ID format is: <prefix>-rig-<name> (e.g., gt-rig-gastown)
// The ID is constructed internally from fields.Prefix and name.
// The created_by field is populated from BD_ACTOR env var for provenance tracking.
func (b *Beads) CreateRigBead(name string, fields *RigFields) (*Issue, error) {
	// Guard against flag-like rig names (gt-e0kx5: --help garbage beads)
	if IsFlagLikeTitle(name) {
		return nil, fmt.Errorf("refusing to create rig bead: %w (got %q)", ErrFlagTitle, name)
	}

	if fields != nil && fields.State != "" && !ValidRigState(fields.State) {
		return nil, fmt.Errorf("invalid rig state %q: must be one of active, archived, maintenance", fields.State)
	}

	prefix := "gt"
	if fields != nil && fields.Prefix != "" {
		prefix = fields.Prefix
	}
	id := RigBeadIDWithPrefix(prefix, name)
	description := FormatRigDescription(name, fields)

	// Ensure target database has custom types configured (including "rig").
	// This initializes and migrates the schema before config writes; without it,
	// bd rejects --type=rig on databases that haven't registered custom types yet.
	if err := EnsureCustomTypes(b.getResolvedBeadsDir()); err != nil {
		return nil, fmt.Errorf("configuring rig custom issue types: %w", err)
	}

	args := []string{"create", "--json",
		"--id=" + id,
		"--title=" + name,
		"--description=" + description,
		"--labels=gt:rig",
		"--type=rig",
	}
	if NeedsForceForID(id) {
		args = append(args, "--force")
	}

	// Default actor from BD_ACTOR env var for provenance tracking
	// Uses getActor() to respect isolated mode (tests)
	if actor := b.getActor(); actor != "" {
		args = append(args, "--actor="+actor)
	}

	out, err := b.run(args...)
	if err != nil {
		return nil, err
	}

	var issue Issue
	if err := json.Unmarshal(out, &issue); err != nil {
		return nil, fmt.Errorf("parsing bd create output: %w", err)
	}

	return &issue, nil
}

// GetRigBead retrieves a rig bead by name.
// Returns ErrNotFound if the rig does not exist.
func (b *Beads) GetRigBead(name string) (*Issue, *RigFields, error) {
	id := RigBeadID(name)
	issue, err := b.Show(id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}

	if !HasLabel(issue, "gt:rig") {
		return nil, nil, fmt.Errorf("bead %s is not a rig bead (missing gt:rig label)", id)
	}

	fields := ParseRigFields(issue.Description)
	return issue, fields, nil
}

// GetRigByID retrieves a rig bead by its full ID.
// Returns ErrNotFound if the rig does not exist.
func (b *Beads) GetRigByID(id string) (*Issue, *RigFields, error) {
	issue, err := b.Show(id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}

	if !HasLabel(issue, "gt:rig") {
		return nil, nil, fmt.Errorf("bead %s is not a rig bead (missing gt:rig label)", id)
	}

	fields := ParseRigFields(issue.Description)
	return issue, fields, nil
}

// UpdateRigBead updates the fields for a rig bead.
func (b *Beads) UpdateRigBead(name string, fields *RigFields) (*Issue, error) {
	issue, _, err := b.GetRigBead(name)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("rig %q not found", name)
		}
		return nil, err
	}

	description := FormatRigDescription(name, fields)

	if err := b.Update(issue.ID, UpdateOptions{Description: &description}); err != nil {
		return nil, err
	}

	updated, err := b.Show(issue.ID)
	if err != nil {
		return nil, fmt.Errorf("fetching updated rig: %w", err)
	}
	return updated, nil
}

// DeleteRigBead permanently deletes a rig bead.
func (b *Beads) DeleteRigBead(name string) error {
	id := RigBeadID(name)
	_, err := b.run("delete", id, "--hard", "--force")
	return err
}

// ListRigBeads returns all rig beads.
func (b *Beads) ListRigBeads() (map[string]*RigFields, error) {
	out, err := b.run("list", "--label=gt:rig", "--json")
	if err != nil {
		return nil, err
	}

	if !isJSONBytes(out) {
		return nil, nil
	}
	var issues []*Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing bd list output: %w", err)
	}

	result := make(map[string]*RigFields, len(issues))
	for _, issue := range issues {
		fields := ParseRigFields(issue.Description)
		if fields.Prefix != "" {
			result[fields.Prefix] = fields
		}
	}

	return result, nil
}

// RigBeadIDWithPrefix generates a rig identity bead ID using the specified prefix.
// Format: <prefix>-rig-<name> (e.g., gt-rig-gastown)
func RigBeadIDWithPrefix(prefix, name string) string {
	return fmt.Sprintf("%s-rig-%s", prefix, name)
}

// RigBeadID generates a rig identity bead ID using "gt" prefix.
// For non-gastown rigs, use RigBeadIDWithPrefix with the rig's configured prefix.
func RigBeadID(name string) string {
	return RigBeadIDWithPrefix("gt", name)
}
