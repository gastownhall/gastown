// Package mail provides address resolution for beads-native messaging.
// This module implements the resolution order:
// 1. Explicit prefix (group:, queue:, channel:, list:, announce:)
// 2. Starts with '@' → special pattern (@town, @crew, @rig/X, @role/X)
// 3. Contains '/' → agent address or pattern (validated against known agents)
// 4. Otherwise → lookup by name: group → queue → channel
// 5. If conflict, require prefix (group:X, queue:X, channel:X)
package mail

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/constants"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
)

// ErrUnknownRecipient indicates the address does not match any known agent.
// Callers should NOT fall back to legacy routing on this error — the address
// is definitively invalid, not just unresolvable by the new resolver.
var ErrUnknownRecipient = errors.New("unknown recipient")

// RecipientType indicates the type of resolved recipient.
type RecipientType string

const (
	RecipientAgent   RecipientType = "agent"   // Direct to agent(s)
	RecipientQueue   RecipientType = "queue"   // Single message, workers claim
	RecipientChannel RecipientType = "channel" // Broadcast, retained
)

// Recipient represents a resolved message recipient.
type Recipient struct {
	Address      string        // The resolved address (e.g., "gastown/crew/max")
	Type         RecipientType // Type of recipient (agent, queue, channel)
	OriginalName string        // Original name before resolution (for queues/channels)
}

// Resolver handles address resolution for beads-native messaging.
type Resolver struct {
	beads    *beads.Beads
	townRoot string
}

// NewResolver creates a new address resolver.
func NewResolver(b *beads.Beads, townRoot string) *Resolver {
	return &Resolver{
		beads:    b,
		townRoot: townRoot,
	}
}

// Resolve resolves an address to a list of recipients.
// Resolution order:
// 1. Contains '/' → agent address or pattern (direct delivery)
// 2. Starts with '@' → special pattern (@town, @crew, etc.)
// 3. Starts with explicit prefix → use that type (group:, queue:, channel:)
// 4. Otherwise → lookup by name: group → queue → channel
func (r *Resolver) Resolve(address string) ([]Recipient, error) {
	return r.resolveWithVisited(address, make(map[string]bool))
}

// resolveWithVisited resolves an address while threading cycle detection state.
func (r *Resolver) resolveWithVisited(address string, visited map[string]bool) ([]Recipient, error) {
	// 1. Explicit prefix takes precedence
	if strings.HasPrefix(address, "group:") {
		name := strings.TrimPrefix(address, "group:")
		return r.resolveBeadsGroupWithVisited(name, visited)
	}
	if strings.HasPrefix(address, "queue:") {
		name := strings.TrimPrefix(address, "queue:")
		return r.resolveQueue(name)
	}
	if strings.HasPrefix(address, "channel:") {
		name := strings.TrimPrefix(address, "channel:")
		return r.resolveChannel(name)
	}

	// Legacy prefixes (list:, announce:) - pass through
	if strings.HasPrefix(address, "list:") || strings.HasPrefix(address, "announce:") {
		// These are handled by existing router logic
		return []Recipient{{Address: address, Type: RecipientAgent}}, nil
	}

	// 2. Starts with '@' → special pattern (check before '/' since @rig/X contains '/')
	if strings.HasPrefix(address, "@") {
		return r.resolveAtPatternWithVisited(address, visited)
	}

	// 3. Contains '/' → agent address or pattern
	if strings.Contains(address, "/") {
		return r.resolveAgentAddress(address)
	}

	// 4. Name lookup: group → queue → channel
	return r.resolveByNameWithVisited(address, visited)
}

// resolveAgentAddress handles addresses containing '/'.
// These are either direct addresses or patterns.
func (r *Resolver) resolveAgentAddress(address string) ([]Recipient, error) {
	// Check for wildcard patterns
	if strings.Contains(address, "*") {
		return r.resolvePattern(address)
	}

	// Validate that the address refers to a known agent before accepting.
	// Without this check, typos like "laser/mayor" (instead of "mayor/")
	// silently deliver to a dead inbox with no error.
	// See: https://github.com/steveyegge/gastown/issues/2038
	if err := r.validateAgentAddress(address); err != nil {
		return nil, err
	}

	// Direct address - single recipient
	return []Recipient{{
		Address: address,
		Type:    RecipientAgent,
	}}, nil
}

// validateAgentAddress checks that a slash-containing address corresponds to
// a known agent. It checks well-known singletons, agent beads, and workspace
// directories. Returns nil if the agent exists, or an error with suggestions.
// If neither beads nor townRoot is available, validation is skipped (graceful
// degradation) and downstream validation in sendToSingle handles it.
func (r *Resolver) validateAgentAddress(address string) error {
	// Skip validation when we have no data sources to check against.
	// This preserves backward compatibility when the Resolver is used
	// without a fully configured environment.
	if r.beads == nil && r.townRoot == "" {
		return nil
	}

	normalized := normalizeAddress(strings.TrimSuffix(address, "/"))

	// Well-known town-level singletons always valid
	switch normalized {
	case constants.RoleMayor + "/", constants.RoleMayor, constants.RoleDeacon + "/", constants.RoleDeacon, "overseer":
		return nil
	}

	parts := strings.SplitN(normalized, "/", 3)
	if len(parts) < 2 || parts[1] == "" {
		return fmt.Errorf("%w: %s", ErrUnknownRecipient, address)
	}

	// Well-known rig-level singletons (rig/witness, rig/refinery)
	if len(parts) == 2 {
		switch parts[1] {
		case constants.RoleWitness, constants.RoleRefinery:
			return nil
		}
	}

	// Check agent beads if available
	if r.beads != nil {
		agents, err := r.beads.ListAgentBeads()
		if err == nil {
			for id := range agents {
				addr := AgentBeadIDToAddress(id)
				if addr != "" && normalizeAddress(addr) == normalized {
					return nil
				}
			}
		}
	}

	// Check workspace directories as fallback
	if r.townRoot != "" {
		switch len(parts) {
		case 2:
			rig, name := parts[0], parts[1]
			// Singleton role: rig/name (e.g., gastown/witness)
			if dirExistsAt(filepath.Join(r.townRoot, rig, name)) {
				return nil
			}
			// Named agent (normalized, could be crew or polecat)
			for _, role := range []string{"crew", "polecats"} {
				if dirExistsAt(filepath.Join(r.townRoot, rig, role, name)) {
					return nil
				}
			}
		case 3:
			// Explicit: rig/crew/name or rig/polecats/name
			if dirExistsAt(filepath.Join(r.townRoot, parts[0], parts[1], parts[2])) {
				return nil
			}
		}
	}

	return fmt.Errorf("%w: %s (no matching agent or workspace found)", ErrUnknownRecipient, address)
}

// dirExistsAt returns true if path exists and is a directory.
func dirExistsAt(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// resolvePattern expands a wildcard pattern to matching agents.
// Patterns like "*/witness" or "gastown/*" are expanded.
func (r *Resolver) resolvePattern(pattern string) ([]Recipient, error) {
	if r.beads == nil {
		return nil, fmt.Errorf("beads not available for pattern resolution")
	}

	// Get all agent beads
	agents, err := r.beads.ListAgentBeads()
	if err != nil {
		return nil, fmt.Errorf("listing agents: %w", err)
	}

	var recipients []Recipient
	for id := range agents {
		// Convert bead ID to address and check match
		addr := AgentBeadIDToAddress(id)
		if addr != "" && matchPattern(pattern, addr) {
			recipients = append(recipients, Recipient{
				Address: addr,
				Type:    RecipientAgent,
			})
		}
	}

	if len(recipients) == 0 {
		return nil, fmt.Errorf("no agents match pattern: %s", pattern)
	}

	return recipients, nil
}

// resolveAtPattern handles @-prefixed patterns.
// These include @town, @crew, @rig/X, @role/X, @overseer.
func (r *Resolver) resolveAtPattern(address string) ([]Recipient, error) {
	return r.resolveAtPatternWithVisited(address, make(map[string]bool))
}

// resolveAtPatternWithVisited handles @-prefixed patterns with cycle detection.
func (r *Resolver) resolveAtPatternWithVisited(address string, visited map[string]bool) ([]Recipient, error) {
	// First check if this is a beads-native group (if beads available)
	if r.beads != nil {
		groupName := strings.TrimPrefix(address, "@")
		_, fields, err := r.beads.LookupGroupByName(groupName)
		if err != nil && !errors.Is(err, beads.ErrNotFound) {
			return nil, err
		}
		if err == nil && fields != nil {
			// Found a beads-native group - expand its members
			return r.expandGroupMembersWithVisited(fields, visited)
		}
	}

	// Fall back to built-in patterns (handled by existing router)
	// Return as-is for router to handle
	return []Recipient{{Address: address, Type: RecipientAgent}}, nil
}

// resolveByName looks up a name as group → queue → channel.
// Returns error if name conflicts exist without explicit prefix.
func (r *Resolver) resolveByName(name string) ([]Recipient, error) {
	return r.resolveByNameWithVisited(name, make(map[string]bool))
}

// resolveByNameWithVisited looks up a name with cycle detection.
func (r *Resolver) resolveByNameWithVisited(name string, visited map[string]bool) ([]Recipient, error) {
	var foundGroup, foundQueue, foundChannel bool
	var groupFields *beads.GroupFields

	// Check for beads-native group
	if r.beads != nil {
		_, fields, err := r.beads.LookupGroupByName(name)
		if err != nil && !errors.Is(err, beads.ErrNotFound) {
			return nil, err
		}
		if err == nil && fields != nil {
			foundGroup = true
			groupFields = fields
		}
	}

	// Check for beads-native queue
	if r.beads != nil {
		_, queueFields, err := r.beads.LookupQueueByName(name)
		if err != nil {
			return nil, err
		}
		if queueFields != nil {
			foundQueue = true
		}
	}

	// Check for beads-native channel
	if r.beads != nil {
		_, channelFields, err := r.beads.LookupChannelByName(name)
		if err != nil {
			return nil, err
		}
		if channelFields != nil {
			foundChannel = true
		}
	}

	// Check for queue/channel in config (legacy)
	if r.townRoot != "" {
		cfg, err := config.LoadMessagingConfig(config.MessagingConfigPath(r.townRoot))
		if err == nil && cfg != nil {
			if _, ok := cfg.Queues[name]; ok {
				foundQueue = true
			}
			if _, ok := cfg.Announces[name]; ok {
				foundChannel = true
			}
		}
	}

	// Count conflicts
	conflictCount := 0
	if foundGroup {
		conflictCount++
	}
	if foundQueue {
		conflictCount++
	}
	if foundChannel {
		conflictCount++
	}

	if conflictCount == 0 {
		return nil, fmt.Errorf("unknown address: %s (not a group, queue, or channel)", name)
	}

	if conflictCount > 1 {
		var types []string
		if foundGroup {
			types = append(types, "group:"+name)
		}
		if foundQueue {
			types = append(types, "queue:"+name)
		}
		if foundChannel {
			types = append(types, "channel:"+name)
		}
		return nil, fmt.Errorf("ambiguous address %q: matches multiple types. Use explicit prefix: %s",
			name, strings.Join(types, ", "))
	}

	// Single match - resolve it
	if foundGroup {
		return r.expandGroupMembersWithVisited(groupFields, visited)
	}
	if foundQueue {
		return r.resolveQueue(name)
	}
	return r.resolveChannel(name)
}

// resolveBeadsGroup resolves a beads-native group by name.
func (r *Resolver) resolveBeadsGroup(name string) ([]Recipient, error) {
	return r.resolveBeadsGroupWithVisited(name, make(map[string]bool))
}

// resolveBeadsGroupWithVisited resolves a beads-native group with cycle detection.
func (r *Resolver) resolveBeadsGroupWithVisited(name string, visited map[string]bool) ([]Recipient, error) {
	if r.beads == nil {
		return nil, fmt.Errorf("beads not available")
	}

	_, fields, err := r.beads.LookupGroupByName(name)
	if err != nil {
		if errors.Is(err, beads.ErrNotFound) {
			return nil, fmt.Errorf("group not found: %s", name)
		}
		return nil, err
	}

	return r.expandGroupMembersWithVisited(fields, visited)
}

// expandGroupMembers expands a group's members to recipients.
// Handles nested groups and patterns recursively.
func (r *Resolver) expandGroupMembers(fields *beads.GroupFields) ([]Recipient, error) {
	return r.expandGroupMembersWithVisited(fields, make(map[string]bool))
}

// expandGroupMembersWithVisited expands group members with cycle detection.
func (r *Resolver) expandGroupMembersWithVisited(fields *beads.GroupFields, visited map[string]bool) ([]Recipient, error) {
	if fields == nil {
		return nil, nil
	}

	// Mark this group as visited for cycle detection
	if fields.Name != "" {
		if visited[fields.Name] {
			// Cycle detected - skip silently (as per design: "silent skip with warning")
			return nil, nil
		}
		visited[fields.Name] = true
	}

	seen := make(map[string]bool)
	var recipients []Recipient

	for _, member := range fields.Members {
		// Recursively resolve each member
		resolved, err := r.resolveMemberWithVisited(member, visited)
		if err != nil {
			// Log warning but continue with other members
			continue
		}

		for _, rec := range resolved {
			// Deduplicate
			if !seen[rec.Address] {
				seen[rec.Address] = true
				recipients = append(recipients, rec)
			}
		}
	}

	return recipients, nil
}

// resolveMemberWithVisited resolves a single group member with cycle detection.
func (r *Resolver) resolveMemberWithVisited(member string, visited map[string]bool) ([]Recipient, error) {
	// Check if this is a nested group reference
	if r.beads != nil && !strings.Contains(member, "/") && !strings.HasPrefix(member, "@") {
		_, fields, err := r.beads.LookupGroupByName(member)
		if err == nil && fields != nil {
			return r.expandGroupMembersWithVisited(fields, visited)
		}
	}

	// Otherwise resolve with the same visited map to maintain cycle detection
	return r.resolveWithVisited(member, visited)
}

// resolveQueue returns a queue recipient.
func (r *Resolver) resolveQueue(name string) ([]Recipient, error) {
	return []Recipient{{
		Address:      "queue:" + name,
		Type:         RecipientQueue,
		OriginalName: name,
	}}, nil
}

// resolveChannel returns a channel recipient.
func (r *Resolver) resolveChannel(name string) ([]Recipient, error) {
	return []Recipient{{
		Address:      "channel:" + name,
		Type:         RecipientChannel,
		OriginalName: name,
	}}, nil
}

// AgentBeadIDToAddress converts an agent bead ID to a mail address.
// Handles both gt- (rig agents) and hq- (town agents) prefixes:
//   - hq-mayor → mayor/
//   - hq-deacon → deacon/
//   - gt-gastown-crew-max → gastown/crew/max
func AgentBeadIDToAddress(id string) string {
	var rest string

	// Handle both gt- (rig agents) and hq- (town agents) prefixes
	if strings.HasPrefix(id, "gt-") {
		rest = strings.TrimPrefix(id, "gt-")
	} else if strings.HasPrefix(id, "hq-") {
		rest = strings.TrimPrefix(id, "hq-")
	} else {
		return ""
	}

	// Agent bead IDs include the role explicitly: <prefix>-<rig>-<role>[-<name>]
	// Scan from right for known role markers to handle hyphenated rig names.
	parts := strings.Split(rest, "-")

	if len(parts) == 1 {
		// Town-level: gt-mayor → mayor/
		return parts[0] + "/"
	}

	// Scan from right for known role markers
	for i := len(parts) - 1; i >= 1; i-- {
		switch parts[i] {
		case constants.RoleWitness, constants.RoleRefinery:
			// Singleton role: rig is everything before the role
			rig := strings.Join(parts[:i], "-")
			return rig + "/" + parts[i]
		case constants.RoleCrew, constants.RolePolecat:
			// Named role: rig/role/name
			rig := strings.Join(parts[:i], "-")
			if i+1 < len(parts) {
				name := strings.Join(parts[i+1:], "-")
				role := parts[i]
				if role == constants.RolePolecat {
					role = "polecats"
				}
				return rig + "/" + role + "/" + name
			}
			role := parts[i]
			if role == constants.RolePolecat {
				role = "polecats"
			}
			return rig + "/" + role
		case "dog":
			// Town-level named: gt-dog-alpha
			if i+1 < len(parts) {
				name := strings.Join(parts[i+1:], "-")
				return "dog/" + name
			}
			return "dog/"
		}
	}

	// Fallback: assume rig/role format
	if len(parts) == 2 {
		return parts[0] + "/" + parts[1]
	}
	return ""
}

// matchPattern checks if an address matches a wildcard pattern.
// '*' matches any single path segment (no slashes).
func matchPattern(pattern, address string) bool {
	patternParts := strings.Split(pattern, "/")
	addressParts := strings.Split(address, "/")

	if len(patternParts) != len(addressParts) {
		return false
	}

	for i, p := range patternParts {
		if p == "*" {
			continue // Wildcard matches anything
		}
		if p != addressParts[i] {
			return false
		}
	}

	return true
}
