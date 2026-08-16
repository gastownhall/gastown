// Package beads provides delegation tracking for work units.
package beads

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jonbaldie/gastown/internal/style"
)

// Delegation represents a work delegation relationship between work units.
// Delegation links a parent work unit to a child work unit, tracking who
// delegated the work and to whom, along with any terms of the delegation.
// This enables work distribution with credit cascade - work flows down,
// validation and credit flow up.
type Delegation struct {
	// Parent is the work unit ID that delegated the work
	Parent string `json:"parent"`

	// Child is the work unit ID that received the delegated work
	Child string `json:"child"`

	// DelegatedBy is the entity (hop:// URI or actor string) that delegated
	DelegatedBy string `json:"delegated_by"`

	// DelegatedTo is the entity (hop:// URI or actor string) receiving delegation
	DelegatedTo string `json:"delegated_to"`

	// Terms contains optional conditions of the delegation
	Terms *DelegationTerms `json:"terms,omitempty"`

	// CreatedAt is when the delegation was created
	CreatedAt string `json:"created_at,omitempty"`
}

// DelegationTerms holds optional terms/conditions for a delegation.
type DelegationTerms struct {
	// Portion describes what part of the parent work is delegated
	Portion string `json:"portion,omitempty"`

	// Deadline is the expected completion date
	Deadline string `json:"deadline,omitempty"`

	// AcceptanceCriteria describes what constitutes completion
	AcceptanceCriteria string `json:"acceptance_criteria,omitempty"`

	// CreditShare is the percentage of credit that flows to the delegate (0-100)
	CreditShare int `json:"credit_share,omitempty"`
}

// AddDelegation creates a delegation relationship from parent to child work unit.
// The delegation is stored in the child issue's metadata under the "delegated_from"
// key. The bd slot command was removed in v0.62; metadata is the replacement storage.
func (b *Beads) AddDelegation(d *Delegation) error {
	if d.Parent == "" || d.Child == "" {
		return fmt.Errorf("delegation requires both parent and child work unit IDs")
	}
	if d.DelegatedBy == "" || d.DelegatedTo == "" {
		return fmt.Errorf("delegation requires both delegated_by and delegated_to entities")
	}

	var err error
	if b.store != nil {
		err = b.storeDelegationSet(d.Child, d)
	} else {
		// CLI path: use --set-metadata flag (bd update in v0.62+).
		delegationJSON, marshalErr := json.Marshal(d)
		if marshalErr != nil {
			return fmt.Errorf("marshaling delegation: %w", marshalErr)
		}
		_, err = b.run("update", d.Child, "--set-metadata=delegated_from="+string(delegationJSON))
	}
	if err != nil {
		return fmt.Errorf("setting delegation metadata: %w", err)
	}

	// Also add a dependency so child blocks parent (work must complete before parent can close)
	if err := b.AddDependency(d.Parent, d.Child); err != nil {
		// Log but don't fail - the delegation is still recorded
		style.PrintWarning("could not add blocking dependency for delegation: %v", err)
	}

	return nil
}

// RemoveDelegation removes a delegation relationship.
func (b *Beads) RemoveDelegation(parent, child string) error {
	var err error
	if b.store != nil {
		err = b.storeDelegationClear(child)
	} else {
		// CLI path: use --unset-metadata flag (bd update in v0.62+).
		_, err = b.run("update", child, "--unset-metadata=delegated_from")
	}
	if err != nil {
		return fmt.Errorf("clearing delegation metadata: %w", err)
	}

	// Also remove the blocking dependency
	if err := b.RemoveDependency(parent, child); err != nil {
		// Log but don't fail
		style.PrintWarning("could not remove blocking dependency: %v", err)
	}

	return nil
}

// GetDelegation retrieves the delegation information for a child work unit.
// Returns nil if the issue has no delegation.
func (b *Beads) GetDelegation(child string) (*Delegation, error) {
	// Verify the issue exists and get its metadata.
	issue, err := b.Show(child)
	if err != nil {
		return nil, fmt.Errorf("getting issue: %w", err)
	}

	return parseDelegationFromMetadata(issue.Metadata)
}

// parseDelegationFromMetadata extracts a Delegation from an issue's metadata JSON.
// Returns nil if the metadata is empty or has no "delegated_from" key.
func parseDelegationFromMetadata(metadata json.RawMessage) (*Delegation, error) {
	if len(metadata) == 0 {
		return nil, nil
	}

	var meta map[string]json.RawMessage
	if err := json.Unmarshal(metadata, &meta); err != nil {
		return nil, nil // Malformed metadata; treat as no delegation
	}

	raw, ok := meta["delegated_from"]
	if !ok || len(raw) == 0 {
		return nil, nil
	}

	// Handle explicit null
	if strings.TrimSpace(string(raw)) == "null" {
		return nil, nil
	}

	var delegation Delegation
	if err := json.Unmarshal(raw, &delegation); err != nil {
		return nil, fmt.Errorf("parsing delegation: %w", err)
	}

	return &delegation, nil
}

// ListDelegationsFrom returns all delegations from a parent work unit.
// This searches for issues that have delegated_from pointing to the parent.
func (b *Beads) ListDelegationsFrom(parent string) ([]*Delegation, error) {
	// List all issues that depend on this parent (delegated work blocks parent)
	issues, err := b.List(ListOptions{Status: "all"})
	if err != nil {
		return nil, fmt.Errorf("listing issues: %w", err)
	}

	// For each issue, show to get its metadata and check for delegation.
	var delegations []*Delegation
	for _, issue := range issues {
		full, showErr := b.Show(issue.ID)
		if showErr != nil {
			continue // Skip issues that can't be shown
		}
		d, parseErr := parseDelegationFromMetadata(full.Metadata)
		if parseErr != nil || d == nil {
			continue
		}
		if d.Parent == parent {
			delegations = append(delegations, d)
		}
	}

	return delegations, nil
}
