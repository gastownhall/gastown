// Package beads provides rig identity bead management.
package beads

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// NewRigBeadStore returns a Beads wrapper for rig identity bead operations in
// the given town.
//
// Rig identity beads (<prefix>-rig-<name>) carry the rig's own ID prefix but,
// like agent beads, they live in the TOWN (hq) database. Prefix routing maps
// "sv-" to the svalscripts rig database, so a routed lookup for
// "sv-rig-svalscripts" is sent to a database that has never held the bead and
// comes back "issue not found" (gt-gf6). Re-rooting at the town .beads dir and
// disabling routing is what makes these lookups resolve at all.
//
// Callers that already know the town root should prefer this over ForRigBead,
// which has to walk the filesystem to find it.
func NewRigBeadStore(townRoot string) *Beads {
	if townRoot == "" {
		return &Beads{}
	}
	return &Beads{
		workDir:  townRoot,
		beadsDir: filepath.Join(townRoot, ".beads"),
		townRoot: townRoot,
		noRoute:  true,
	}
}

// ForRigBead returns a Beads wrapper suitable for operating on rig identity
// beads, preserving this wrapper's isolation and server-port settings.
// See NewRigBeadStore for why rig identity beads need a town-rooted wrapper.
//
// If the town root cannot be determined, returns the original wrapper so
// isolated/test setups keep working.
func (b *Beads) ForRigBead() *Beads {
	return b.ForAgentBead()
}

// rigBeadTarget returns the wrapper rig identity bead operations should run
// against. Mirrors agentBeadTarget: already-unrouted wrappers (including those
// from NewRigBeadStore) are used as-is.
func (b *Beads) rigBeadTarget() *Beads {
	if b.noRoute {
		return b
	}
	return b.ForRigBead()
}

// legacyRigBeadDirs returns the rig-scoped beads directories that may hold a
// rig identity bead created before the town database became canonical,
// deduplicated and in lookup order.
func legacyRigBeadDirs(rigPath string) []string {
	var dirs []string
	seen := make(map[string]bool)
	add := func(dir string) {
		if dir == "" || seen[dir] {
			return
		}
		seen[dir] = true
		dirs = append(dirs, dir)
	}

	mayorRig := filepath.Join(rigPath, "mayor", "rig")
	if _, err := os.Stat(filepath.Join(mayorRig, ".beads")); err == nil {
		add(ResolveBeadsDir(mayorRig))
	}
	add(ResolveBeadsDir(rigPath))
	return dirs
}

// ShowRigBead resolves a rig identity bead for the given rig.
//
// The town database is canonical (see NewRigBeadStore), but towns predating
// that rule have rig identity beads sitting in the rig's own database —
// anything docked by an older 'gt rig dock' wrote its status:docked label
// there. Reading only the town database would make those states invisible and
// silently un-dock the rig, so the rig-scoped database is consulted as a
// fallback.
//
// Returns ErrNotFound only when the bead is absent from both. If either lookup
// failed for a reason other than not-found — an unreachable backend — that
// error is returned so callers can tell "no docked state recorded" apart from
// "could not read the docked state".
func ShowRigBead(townRoot, rigName, prefix string) (*Issue, error) {
	id := RigBeadIDWithPrefix(prefix, rigName)

	var hardErr error
	note := func(err error) {
		if hardErr == nil && !errors.Is(err, ErrNotFound) {
			hardErr = err
		}
	}

	issue, err := NewRigBeadStore(townRoot).Show(id)
	if err == nil {
		return issue, nil
	}
	note(err)

	// Legacy locations, in the order the pre-fix readers used: the mayor/rig
	// checkout when it has its own .beads, then the rig root (following any
	// redirect). Routing is disabled because the ID's prefix would send the
	// lookup right back to the database we are already pointing at.
	rigPath := filepath.Join(townRoot, rigName)
	for _, dir := range legacyRigBeadDirs(rigPath) {
		legacy := NewWithBeadsDir(rigPath, dir)
		legacy.noRoute = true
		issue, err = legacy.Show(id)
		if err == nil {
			return issue, nil
		}
		note(err)
	}

	if hardErr != nil {
		return nil, hardErr
	}
	return nil, ErrNotFound
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
	target := b.rigBeadTarget()

	// Try to find existing bead first
	if existing, err := target.Show(id); err == nil {
		return existing, nil
	}

	// Not found — try to create
	created, createErr := target.CreateRigBead(name, fields)
	if createErr == nil {
		return created, nil
	}

	// Create failed (likely duplicate key from race or Dolt hiccup) — retry Show
	if existing, err := target.Show(id); err == nil {
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
	target := b.rigBeadTarget()

	// Ensure target database keeps rig as a durable custom type, not an
	// infra/wisp type. Failing closed avoids silently creating ephemeral rig
	// identity beads when type config cannot be persisted.
	if err := EnsureCustomTypes(target.getResolvedBeadsDir()); err != nil {
		return nil, fmt.Errorf("ensuring rig bead types: %w", err)
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
	if actor := target.getActor(); actor != "" {
		args = append(args, "--actor="+actor)
	}

	out, err := target.run(args...)
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
	issue, err := b.rigBeadTarget().Show(id)
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
	issue, err := b.rigBeadTarget().Show(id)
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
	target := b.rigBeadTarget()

	if err := target.Update(issue.ID, UpdateOptions{Description: &description}); err != nil {
		return nil, err
	}

	updated, err := target.Show(issue.ID)
	if err != nil {
		return nil, fmt.Errorf("fetching updated rig: %w", err)
	}
	return updated, nil
}

// DeleteRigBead permanently deletes a rig bead.
func (b *Beads) DeleteRigBead(name string) error {
	id := RigBeadID(name)
	return b.rigBeadTarget().deleteBead(id)
}

// ListRigBeads returns all rig beads.
func (b *Beads) ListRigBeads() (map[string]*RigFields, error) {
	out, err := b.rigBeadTarget().run("list", "--label=gt:rig", "--json")
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
