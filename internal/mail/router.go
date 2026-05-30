package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/nudge"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/telemetry"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

// ErrUnknownList indicates a mailing list name was not found in configuration.
var ErrUnknownList = errors.New("unknown mailing list")

// ErrUnknownQueue indicates a queue name was not found in configuration.
var ErrUnknownQueue = errors.New("unknown queue")

// ErrUnknownAnnounce indicates an announce channel name was not found in configuration.
var ErrUnknownAnnounce = errors.New("unknown announce channel")

// DefaultIdleNotifyTimeout is how long the router waits for a recipient's
// session to become idle before falling back to a queued nudge.
const DefaultIdleNotifyTimeout = 3 * time.Second

// Router handles message delivery via beads.
// It routes messages to the correct beads database based on address:
// - Town-level (mayor/, deacon/) -> {townRoot}/.beads
// - Rig-level (rig/polecat) -> {townRoot}/{rig}/.beads
type Router struct {
	workDir  string // fallback directory to run bd commands in
	townRoot string // town root directory (e.g., ~/gt)
	tmux     *tmux.Tmux

	// IdleNotifyTimeout controls how long to wait for a session to become
	// idle before falling back to a queued nudge. Zero uses the default.
	IdleNotifyTimeout time.Duration

	notifyWg sync.WaitGroup // tracks in-flight async notifications
}

// NewRouter creates a new mail router.
// workDir should be a directory containing a .beads database.
// The town root is auto-detected from workDir if possible.
func NewRouter(workDir string) *Router {
	// Try to detect town root from workDir
	townRoot := detectTownRoot(workDir)

	return &Router{
		workDir:  workDir,
		townRoot: townRoot,
		tmux:     tmux.NewTmux(),
	}
}

// NewRouterWithTownRoot creates a router with an explicit town root.
func NewRouterWithTownRoot(workDir, townRoot string) *Router {
	return &Router{
		workDir:  workDir,
		townRoot: townRoot,
		tmux:     tmux.NewTmux(),
	}
}

// WaitPendingNotifications blocks until all in-flight async notifications
// have completed. CLI commands should call this before exiting to avoid
// losing notifications that are still being delivered.
func (r *Router) WaitPendingNotifications() {
	r.notifyWg.Wait()
}

// isListAddress returns true if the address uses list:name syntax.
func isListAddress(address string) bool {
	return strings.HasPrefix(address, "list:")
}

// parseListName extracts the list name from a list:name address.
func parseListName(address string) string {
	return strings.TrimPrefix(address, "list:")
}

// isQueueAddress returns true if the address uses queue:name syntax.
func isQueueAddress(address string) bool {
	return strings.HasPrefix(address, "queue:")
}

// parseQueueName extracts the queue name from a queue:name address.
func parseQueueName(address string) string {
	return strings.TrimPrefix(address, "queue:")
}

// isAnnounceAddress returns true if the address uses announce:name syntax.
func isAnnounceAddress(address string) bool {
	return strings.HasPrefix(address, "announce:")
}

// parseAnnounceName extracts the announce channel name from an announce:name address.
func parseAnnounceName(address string) string {
	return strings.TrimPrefix(address, "announce:")
}

// isChannelAddress returns true if the address uses channel:name syntax (beads-native channels).
func isChannelAddress(address string) bool {
	return strings.HasPrefix(address, "channel:")
}

// parseChannelName extracts the channel name from a channel:name address.
func parseChannelName(address string) string {
	return strings.TrimPrefix(address, "channel:")
}

// expandFromConfig is a generic helper for config-based expansion.
// It loads the messaging config and calls the getter to extract the desired value.
// This consolidates the common pattern of: check townRoot, load config, lookup in map.
func expandFromConfig[T any](r *Router, name string, getter func(*config.MessagingConfig) (T, bool), errType error) (T, error) {
	var zero T
	if r.townRoot == "" {
		return zero, fmt.Errorf("%w: %s (no town root)", errType, name)
	}

	configPath := config.MessagingConfigPath(r.townRoot)
	cfg, err := config.LoadMessagingConfig(configPath)
	if err != nil {
		return zero, fmt.Errorf("loading messaging config: %w", err)
	}

	result, ok := getter(cfg)
	if !ok {
		return zero, fmt.Errorf("%w: %s", errType, name)
	}

	return result, nil
}

// expandList returns the recipients for a mailing list.
// Returns ErrUnknownList if the list is not found.
func (r *Router) expandList(listName string) ([]string, error) {
	recipients, err := expandFromConfig(r, listName, func(cfg *config.MessagingConfig) ([]string, bool) {
		r, ok := cfg.Lists[listName]
		return r, ok
	}, ErrUnknownList)
	if err != nil {
		return nil, err
	}

	if len(recipients) == 0 {
		return nil, fmt.Errorf("%w: %s (empty list)", ErrUnknownList, listName)
	}

	return recipients, nil
}

// expandQueue returns the QueueConfig for a queue name.
// Returns ErrUnknownQueue if the queue is not found.
func (r *Router) expandQueue(queueName string) (*config.QueueConfig, error) {
	return expandFromConfig(r, queueName, func(cfg *config.MessagingConfig) (*config.QueueConfig, bool) {
		qc, ok := cfg.Queues[queueName]
		if !ok {
			return nil, false
		}
		return &qc, true
	}, ErrUnknownQueue)
}

// expandAnnounce returns the AnnounceConfig for an announce channel name.
// Returns ErrUnknownAnnounce if the channel is not found.
func (r *Router) expandAnnounce(announceName string) (*config.AnnounceConfig, error) {
	return expandFromConfig(r, announceName, func(cfg *config.MessagingConfig) (*config.AnnounceConfig, bool) {
		ac, ok := cfg.Announces[announceName]
		if !ok {
			return nil, false
		}
		return &ac, true
	}, ErrUnknownAnnounce)
}

// detectTownRoot finds the town root directory.
//
// Uses workspace.Find which correctly handles nested workspaces by always
// searching to the filesystem root and returning the outermost workspace.
// Falls back to GT_TOWN_ROOT/GT_ROOT env vars when workspace.Find cannot
// locate a workspace (e.g., running from outside any workspace).
func detectTownRoot(startDir string) string {
	// workspace.Find handles nested workspaces correctly: it always searches
	// to the filesystem root and returns the outermost mayor/town.json match.
	townRoot, err := workspace.Find(startDir)
	if err == nil && townRoot != "" {
		return townRoot
	}

	// Fallback: try GT_TOWN_ROOT or GT_ROOT env vars when workspace detection
	// fails (e.g., running from outside any workspace directory).
	for _, envName := range []string{"GT_TOWN_ROOT", "GT_ROOT"} {
		if envRoot := os.Getenv(envName); envRoot != "" {
			if ok, _ := workspace.IsWorkspace(envRoot); ok {
				return envRoot
			}
		}
	}
	return ""
}

// resolveBeadsDir returns the correct .beads directory for mail delivery.
//
// All mail uses town beads ({townRoot}/.beads). Rig-level beads ({rig}/.beads)
// are for project issues only, not mail.
func (r *Router) resolveBeadsDir() string {
	// If no town root, fall back to workDir's .beads
	if r.townRoot == "" {
		return filepath.Join(r.workDir, ".beads")
	}

	// All mail uses town-level beads
	return filepath.Join(r.townRoot, ".beads")
}

func (r *Router) ensureCustomTypes(beadsDir string) error {
	if err := beads.EnsureCustomTypes(beadsDir); err != nil {
		return fmt.Errorf("ensuring custom types: %w", err)
	}
	return nil
}

func (r *Router) buildLabels(msg *Message) []string {
	var labels []string
	labels = append(labels, "gt:message")
	if msg.Type == TypeEscalation {
		labels = append(labels, "gt:escalation")
	}
	labels = append(labels, "from:"+msg.From)
	labels = append(labels, "msg-type:"+string(msg.Type))
	labels = append(labels, DeliverySendLabels()...)
	if msg.ThreadID != "" {
		labels = append(labels, "thread:"+msg.ThreadID)
	}
	if msg.ReplyTo != "" {
		labels = append(labels, "reply-to:"+msg.ReplyTo)
	}
	for _, cc := range msg.CC {
		ccIdentity := AddressToIdentity(cc)
		labels = append(labels, "cc:"+ccIdentity)
	}
	return labels
}

// isTownLevelAddress returns true if the address is for a town-level agent or the overseer.
func isTownLevelAddress(address string) bool {
	addr := strings.TrimSuffix(address, "/")
	return addr == constants.RoleMayor || addr == constants.RoleDeacon || addr == "overseer"
}

// isGroupAddress returns true if the address is a @group address.
// Group addresses start with @ and resolve to multiple recipients.
func isGroupAddress(address string) bool {
	return strings.HasPrefix(address, "@")
}

// GroupType represents the type of group address.
type GroupType string

const (
	GroupTypeRig      GroupType = "rig"      // @rig/<rigname> - all agents in a rig
	GroupTypeTown     GroupType = "town"     // @town - all town-level agents
	GroupTypeRole     GroupType = "role"     // @witnesses, @dogs, etc. - all agents of a role
	GroupTypeRigRole  GroupType = "rig-role" // @crew/<rigname>, @polecats/<rigname> - role in a rig
	GroupTypeOverseer GroupType = "overseer" // @overseer - human operator
)

// ParsedGroup represents a parsed @group address.
type ParsedGroup struct {
	Type     GroupType
	RoleType string // witness, crew, polecat, dog, etc.
	Rig      string // rig name for rig-scoped groups
	Original string // original @group string
}

// parseGroupAddress parses a @group address into its components.
// Returns nil if the address is not a valid group address.
//
// Supported patterns:
//   - @rig/<rigname>: All agents in a rig
//   - @town: All town-level agents (mayor, deacon)
//   - @witnesses: All witnesses across rigs
//   - @crew/<rigname>: Crew workers in a specific rig
//   - @polecats/<rigname>: Polecats in a specific rig
//   - @dogs: All Deacon dogs
//   - @overseer: Human operator (special case)
func parseGroupAddress(address string) *ParsedGroup {
	if !isGroupAddress(address) {
		return nil
	}

	// Remove @ prefix
	group := strings.TrimPrefix(address, "@")

	// Special cases that don't require parsing
	switch group {
	case "overseer":
		return &ParsedGroup{Type: GroupTypeOverseer, Original: address}
	case "town":
		return &ParsedGroup{Type: GroupTypeTown, Original: address}
	case "witnesses":
		return &ParsedGroup{Type: GroupTypeRole, RoleType: constants.RoleWitness, Original: address}
	case "dogs":
		return &ParsedGroup{Type: GroupTypeRole, RoleType: "dog", Original: address}
	case "refineries":
		return &ParsedGroup{Type: GroupTypeRole, RoleType: constants.RoleRefinery, Original: address}
	case "deacons":
		return &ParsedGroup{Type: GroupTypeRole, RoleType: constants.RoleDeacon, Original: address}
	}

	// Parse patterns with slashes: @rig/<name>, @crew/<rig>, @polecats/<rig>
	parts := strings.SplitN(group, "/", 2)
	if len(parts) != 2 || parts[1] == "" {
		return nil // Invalid format
	}

	prefix, qualifier := parts[0], parts[1]

	switch prefix {
	case "rig":
		return &ParsedGroup{Type: GroupTypeRig, Rig: qualifier, Original: address}
	case constants.RoleCrew:
		return &ParsedGroup{Type: GroupTypeRigRole, RoleType: constants.RoleCrew, Rig: qualifier, Original: address}
	case "polecats":
		return &ParsedGroup{Type: GroupTypeRigRole, RoleType: constants.RolePolecat, Rig: qualifier, Original: address}
	default:
		return nil // Unknown group type
	}
}

// agentBead represents an agent bead as returned by bd list --label=gt:agent.
type agentBead struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Status      string   `json:"status"`
	CreatedBy   string   `json:"created_by"`
	Type        string   `json:"issue_type"`
	Labels      []string `json:"labels"`
}

// agentBeadToAddress converts an agent bead to a mail address.
// Handles multiple ID formats:
//   - hq-mayor → mayor/
//   - hq-deacon → deacon/
//   - gt-gastown-crew-max → gastown/max (legacy)
//   - ppf-pyspark_pipeline_framework-polecat-Toast → pyspark_pipeline_framework/Toast (rig prefix)
func agentBeadToAddress(bead *agentBead) string {
	if bead == nil {
		return ""
	}

	id := bead.ID

	// Handle hq- prefixed IDs (town-level format)
	if strings.HasPrefix(id, "hq-") {
		// Well-known town-level agents
		if id == "hq-mayor" {
			return "mayor/"
		}
		if id == "hq-deacon" {
			return "deacon/"
		}

		// For other hq- agents, fall back to description parsing
		return parseAgentAddressFromDescription(bead.Description)
	}

	// Handle gt- prefixed IDs (legacy format)
	// Also handle rig-prefixed IDs (e.g., ppf-) by extracting rig from description
	var rest string
	if strings.HasPrefix(id, "gt-") {
		rest = strings.TrimPrefix(id, "gt-")
	} else {
		// For rig-prefixed IDs, extract rig and role from description
		return parseRigAgentAddress(bead)
	}

	// Agent bead IDs include the role explicitly: gt-<rig>-<role>[-<name>]
	// Scan from right for known role markers to handle hyphenated rig names.
	parts := strings.Split(rest, "-")

	if len(parts) == 1 {
		// Town-level: gt-mayor, gt-deacon
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
			// Named role: rig is before role, name is after (skip role in address)
			rig := strings.Join(parts[:i], "-")
			if i+1 < len(parts) {
				name := strings.Join(parts[i+1:], "-")
				return rig + "/" + name
			}
			return rig + "/"
		case "dog":
			// Town-level named: gt-dog-alpha
			if i+1 < len(parts) {
				name := strings.Join(parts[i+1:], "-")
				return "dog/" + name
			}
			return "dog/"
		}
	}

	// Fallback: assume first part is rig, rest is role/name
	if len(parts) == 2 {
		return parts[0] + "/" + parts[1]
	}
	return ""
}

// parseRigAgentAddress extracts address from a rig-prefixed agent bead.
// ID format: <prefix>-<rig>-<role>[-<name>]
// Examples:
//   - ppf-pyspark_pipeline_framework-witness → pyspark_pipeline_framework/witness
//   - ppf-pyspark_pipeline_framework-polecat-Toast → pyspark_pipeline_framework/Toast
//   - bd-beads-crew-beavis → beads/beavis
func parseRigAgentAddress(bead *agentBead) string {
	// Parse rig and role_type from description
	var roleType, rig string
	for _, line := range strings.Split(bead.Description, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "role_type:") {
			roleType = strings.TrimSpace(strings.TrimPrefix(line, "role_type:"))
		} else if strings.HasPrefix(line, "rig:") {
			rig = strings.TrimSpace(strings.TrimPrefix(line, "rig:"))
		}
	}

	if rig == "" || rig == "null" || roleType == "" || roleType == "null" {
		// Fallback: parse from bead ID by scanning for known role markers.
		// ID format: <prefix>-<rig>-<role>[-<name>]
		// Known rig-level roles: crew, polecat, witness, refinery
		return parseRigAgentAddressFromID(bead.ID)
	}

	// For singleton roles (witness, refinery), address is rig/role
	if roleType == constants.RoleWitness || roleType == constants.RoleRefinery {
		return rig + "/" + roleType
	}

	// For named roles (crew, polecat), extract name from ID
	// ID pattern: <prefix>-<rig>-<role>-<name>
	// Find the role in the ID and take everything after it as the name
	id := bead.ID
	roleMarker := "-" + roleType + "-"
	if idx := strings.Index(id, roleMarker); idx >= 0 {
		name := id[idx+len(roleMarker):]
		if name != "" {
			return rig + "/" + name
		}
	}

	// Fallback: return rig/roleType (may not be correct for all cases)
	return rig + "/" + roleType
}

// parseRigAgentAddressFromID extracts a mail address from a rig-prefixed bead ID
// when the description metadata is missing. Scans for known role markers in the ID
// to determine the rig name and agent name.
//
// ID format: <prefix>-<rig>-<role>[-<name>]
//
// Singleton roles (witness, refinery) must NOT have a name segment — IDs like
// "bd-beads-witness-extra" are malformed and return "".
//
// Keep role lists in sync with beads.RigLevelRoles and beads.NamedRoles.
func parseRigAgentAddressFromID(id string) string {
	// Singleton roles: no name segment allowed
	singletonRoles := []string{constants.RoleWitness, constants.RoleRefinery}
	// Named roles: require a name segment
	namedRoles := []string{constants.RoleCrew, constants.RolePolecat}

	for _, role := range namedRoles {
		marker := "-" + role + "-"
		if idx := strings.Index(id, marker); idx >= 0 {
			// Everything between prefix- and -role- is the rig name.
			// The prefix ends at the first hyphen: <prefix>-<rig>-...
			// But prefix could be multi-char (bd, gt, ppf), so we find
			// the rig as the substring between the first hyphen and the role marker.
			firstHyphen := strings.Index(id, "-")
			if firstHyphen < 0 || firstHyphen >= idx {
				continue
			}
			rig := id[firstHyphen+1 : idx]
			if rig == "" {
				continue
			}
			name := id[idx+len(marker):]
			if name != "" {
				// Named role (crew, polecat): address is rig/name
				return rig + "/" + name
			}
			// crew/polecat without a name — malformed, skip
			continue
		}
	}

	for _, role := range singletonRoles {
		// Singleton roles match only at end of ID: <prefix>-<rig>-<role>
		// Reject if a name segment follows (e.g. -witness-extra is malformed).
		marker := "-" + role + "-"
		if strings.Contains(id, marker) {
			// Has a name segment after the role — malformed singleton
			continue
		}

		suffix := "-" + role
		if strings.HasSuffix(id, suffix) {
			// Find rig between first hyphen and the suffix
			firstHyphen := strings.Index(id, "-")
			if firstHyphen < 0 {
				continue
			}
			suffixStart := len(id) - len(suffix)
			if firstHyphen >= suffixStart {
				continue
			}
			rig := id[firstHyphen+1 : suffixStart]
			if rig == "" {
				continue
			}
			return rig + "/" + role
		}
	}

	return ""
}

// parseAgentAddressFromDescription extracts agent address from description metadata.
// Looks for "location: X" first (explicit address), then falls back to
// "role_type: X" and "rig: Y" patterns in the description.
func parseAgentAddressFromDescription(desc string) string {
	var roleType, rig, location string

	for _, line := range strings.Split(desc, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "location:") {
			location = strings.TrimSpace(strings.TrimPrefix(line, "location:"))
		} else if strings.HasPrefix(line, "role_type:") {
			roleType = strings.TrimSpace(strings.TrimPrefix(line, "role_type:"))
		} else if strings.HasPrefix(line, "rig:") {
			rig = strings.TrimSpace(strings.TrimPrefix(line, "rig:"))
		}
	}

	// Explicit location takes priority (used by dogs and other agents
	// whose address can't be derived from role_type + rig alone)
	if location != "" && location != "null" {
		return location
	}

	// Handle null values from description
	if rig == "null" || rig == "" {
		rig = ""
	}
	if roleType == "null" || roleType == "" {
		return ""
	}

	// Town-level agents (no rig)
	if rig == "" {
		return roleType + "/"
	}

	// Rig-level agents: rig/name (role_type is the agent name for crew/polecat)
	return rig + "/" + roleType
}

// ResolveGroupAddress resolves a @group address to individual recipient addresses.
// Returns the list of resolved addresses and any error.
// This is the public entry point for group resolution.
func (r *Router) ResolveGroupAddress(address string) ([]string, error) {
	group := parseGroupAddress(address)
	if group == nil {
		return nil, fmt.Errorf("invalid group address: %s", address)
	}
	return r.resolveGroup(group)
}

// resolveGroup resolves a @group address to individual recipient addresses.
// Returns the list of resolved addresses and any error.
func (r *Router) resolveGroup(group *ParsedGroup) ([]string, error) {
	if group == nil {
		return nil, errors.New("nil group")
	}

	switch group.Type {
	case GroupTypeOverseer:
		return r.resolveOverseer()
	case GroupTypeTown:
		return r.resolveTownAgents()
	case GroupTypeRole:
		return r.resolveAgentsByRole(group.RoleType, "")
	case GroupTypeRig:
		return r.resolveAgentsByRig(group.Rig)
	case GroupTypeRigRole:
		return r.resolveAgentsByRole(group.RoleType, group.Rig)
	default:
		return nil, fmt.Errorf("unknown group type: %s", group.Type)
	}
}

// resolveOverseer resolves @overseer to the human operator's address.
// Loads the overseer config and returns "overseer" as the address.
func (r *Router) resolveOverseer() ([]string, error) {
	if r.townRoot == "" {
		return nil, errors.New("town root not set, cannot resolve @overseer")
	}

	// Load overseer config to verify it exists
	configPath := config.OverseerConfigPath(r.townRoot)
	_, err := config.LoadOverseerConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("resolving @overseer: %w", err)
	}

	// Return the overseer address
	return []string{"overseer"}, nil
}

// resolveTownAgents resolves @town to all town-level agents (mayor, deacon).
func (r *Router) resolveTownAgents() ([]string, error) {
	// Town-level agents have rig=null in their description
	agents := r.queryAgents("rig: null")

	var addresses []string
	for _, agent := range agents {
		if addr := agentBeadToAddress(agent); addr != "" {
			addresses = append(addresses, addr)
		}
	}

	return addresses, nil
}

// resolveAgentsByRole resolves agents by their role_type.
// If rig is non-empty, also filters by rig.
func (r *Router) resolveAgentsByRole(roleType, rig string) ([]string, error) {
	// Build query filter
	query := "role_type: " + roleType
	agents := r.queryAgents(query)

	var addresses []string
	for _, agent := range agents {
		// Filter by rig if specified
		if rig != "" {
			// Check if agent's description contains matching rig
			if !strings.Contains(agent.Description, "rig: "+rig) {
				continue
			}
		}
		if addr := agentBeadToAddress(agent); addr != "" {
			addresses = append(addresses, addr)
		}
	}

	return addresses, nil
}

// resolveAgentsByRig resolves @rig/<rigname> to all agents in that rig.
func (r *Router) resolveAgentsByRig(rig string) ([]string, error) {
	// Query for agents with matching rig in description
	query := "rig: " + rig
	agents := r.queryAgents(query)

	var addresses []string
	for _, agent := range agents {
		if addr := agentBeadToAddress(agent); addr != "" {
			addresses = append(addresses, addr)
		}
	}

	return addresses, nil
}

// queryAgents queries agent beads using bd list with description filtering.
// Searches both town-level and rig-level beads to find all agents.
func (r *Router) queryAgents(descContains string) []*agentBead {
	var allAgents []*agentBead

	// Query town-level beads
	townBeadsDir := r.resolveBeadsDir()
	townAgents, err := r.queryAgentsInDir(townBeadsDir, descContains)
	if err != nil {
		// Don't fail yet - rig beads might still have results
		townAgents = nil
	}
	allAgents = append(allAgents, townAgents...)

	// Also query rig-level beads via routes.jsonl
	if r.townRoot != "" {
		routesDir := filepath.Join(r.townRoot, ".beads")
		routes, routeErr := beads.LoadRoutes(routesDir)
		if routeErr == nil {
			for _, route := range routes {
				// Skip hq- routes (town-level, already queried)
				if strings.HasPrefix(route.Prefix, "hq-") {
					continue
				}
				rigBeadsDir := filepath.Join(r.townRoot, route.Path, ".beads")
				rigAgents, rigErr := r.queryAgentsInDir(rigBeadsDir, descContains)
				if rigErr != nil {
					continue // Skip rigs with errors
				}
				allAgents = append(allAgents, rigAgents...)
			}
		}
	}

	// Deduplicate by ID
	seen := make(map[string]bool)
	var unique []*agentBead
	for _, agent := range allAgents {
		if !seen[agent.ID] {
			seen[agent.ID] = true
			unique = append(unique, agent)
		}
	}

	return unique
}

// queryAgentsInDir queries agent beads in a specific beads directory with optional description filtering.
// Queries both the issues and wisps tables, merging results.
func (r *Router) queryAgentsInDir(beadsDir, descContains string) ([]*agentBead, error) {
	args := []string{"list", "--label=gt:agent", "--json", "--flat", "--limit=0"}

	if descContains != "" {
		args = append(args, "--desc-contains="+descContains)
	}

	ctx, cancel := bdReadCtx()
	defer cancel()

	// Query issues table (backward compat during migration)
	stdout, issuesErr := runBdCommand(ctx, args, filepath.Dir(beadsDir), beadsDir)

	// Also query wisps table for migrated agent beads (best-effort)
	wispCtx, wispCancel := bdReadCtx()
	defer wispCancel()
	wispOut, _ := runBdCommand(wispCtx, []string{"mol", "wisp", "list", "--json"}, filepath.Dir(beadsDir), beadsDir)

	// Merge results: collect agent beads from both sources
	seenIDs := make(map[string]bool)
	var agents []*agentBead

	// Parse wisps first (primary source after migration)
	if len(wispOut) > 0 {
		var wispAgents []*agentBead
		if json.Unmarshal(wispOut, &wispAgents) == nil {
			for _, agent := range wispAgents {
				if isAgentBeadEntry(agent) {
					seenIDs[agent.ID] = true
					agents = append(agents, agent)
				}
			}
		}
	}

	// Then issues (backward compat, skip duplicates)
	if len(stdout) > 0 {
		var issueAgents []*agentBead
		if json.Unmarshal(stdout, &issueAgents) == nil {
			for _, agent := range issueAgents {
				if !seenIDs[agent.ID] {
					agents = append(agents, agent)
				}
			}
		}
	} else if issuesErr != nil && len(agents) == 0 {
		return nil, fmt.Errorf("querying agents in %s: %w", beadsDir, issuesErr)
	}

	// Filter for active agents (closed/deleted agents are inactive)
	var active []*agentBead
	for _, agent := range agents {
		if agent.Status == "open" || agent.Status == "in_progress" || agent.Status == "hooked" || agent.Status == "pinned" {
			active = append(active, agent)
		}
	}

	return active, nil
}

// isAgentBeadEntry checks if an agentBead entry is an actual agent bead.
func isAgentBeadEntry(a *agentBead) bool {
	if a.Type == "agent" {
		return true
	}
	for _, l := range a.Labels {
		if l == "gt:agent" {
			return true
		}
	}
	return false
}

// queryAgentsFromDir queries agent beads from a specific beads directory.
func (r *Router) queryAgentsFromDir(beadsDir string) ([]*agentBead, error) {
	return r.queryAgentsInDir(beadsDir, "")
}

// shouldBeWisp determines if a message should be stored as a wisp.
// Returns true if:
// - Message.Wisp is explicitly set
// - Subject matches lifecycle message patterns (POLECAT_*, NUDGE, etc.)
func (r *Router) shouldBeWisp(msg *Message) bool {
	if msg.Wisp {
		return true
	}
	// Auto-detect protocol/lifecycle messages by subject prefix
	subjectLower := strings.ToLower(msg.Subject)
	wispPrefixes := []string{
		"polecat_started",
		"polecat_done",
		"work_done",
		"start_work",
		"nudge",
		"lifecycle:",
		"merged",
		"merge_ready",
		"merge_failed",
	}
	for _, prefix := range wispPrefixes {
		if strings.HasPrefix(subjectLower, prefix) {
			return true
		}
	}
	return false
}

// Send delivers a message via beads message.
// Routes the message to the correct beads database based on recipient address.
// Supports fan-out for:
// - Mailing lists (list:name) - fans out to all list members
// - @group addresses - resolves and fans out to matching agents
// Supports single-copy delivery for:
// - Queues (queue:name) - stores single message for worker claiming
// - Announces (announce:name) - bulletin board, no claiming, retention-limited
func (r *Router) Send(msg *Message) error {
	// Check for mailing list address
	if isListAddress(msg.To) {
		return r.sendToList(msg)
	}

	// Check for queue address - single message for claiming
	if isQueueAddress(msg.To) {
		return r.sendToQueue(msg)
	}

	// Check for announce address - bulletin board (single copy, no claiming)
	if isAnnounceAddress(msg.To) {
		return r.sendToAnnounce(msg)
	}

	// Check for beads-native channel address - broadcast with retention
	if isChannelAddress(msg.To) {
		return r.sendToChannel(msg)
	}

	// Check for @group address - resolve and fan-out
	if isGroupAddress(msg.To) {
		return r.sendToGroup(msg)
	}

	// Single recipient - send directly
	return r.sendToSingle(msg)
}

// sendToGroup resolves a @group address and sends individual messages to each member.
func (r *Router) sendToGroup(msg *Message) error {
	group := parseGroupAddress(msg.To)
	if group == nil {
		return fmt.Errorf("invalid group address: %s", msg.To)
	}

	recipients, err := r.resolveGroup(group)
	if err != nil {
		return fmt.Errorf("resolving group %s: %w", msg.To, err)
	}

	if len(recipients) == 0 {
		return fmt.Errorf("no recipients found for group: %s", msg.To)
	}

	// Fan-out: send a copy to each recipient
	var errs []string
	for _, recipient := range recipients {
		// Create a copy of the message for this recipient
		msgCopy := *msg
		msgCopy.To = recipient
		msgCopy.ID = "" // Each fan-out copy gets its own ID from bd create

		if err := r.sendToSingle(&msgCopy); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", recipient, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("some group sends failed: %s", strings.Join(errs, "; "))
	}

	return nil
}

// validateRecipient checks that the recipient identity corresponds to an existing agent.
// Returns an error if the recipient is invalid or doesn't exist.
// Queries agents from town-level beads AND all rig-level beads via routes.jsonl.
func (r *Router) validateRecipient(identity string) error {
	// Overseer is the human operator, not an agent bead
	if identity == "overseer" {
		return nil
	}

	// Well-known town-level singletons always valid
	switch identity {
	case "mayor", "mayor/", "deacon", "deacon/":
		return nil
	}

	// Well-known rig-level singletons (rig/witness, rig/refinery) always
	// valid — these agents are ephemeral and may not have an active session,
	// but mail queues for the next session that starts.
	parts := strings.SplitN(identity, "/", 3)
	if len(parts) == 2 {
		switch parts[1] {
		case "witness", "refinery":
			return nil
		}
	}

	// Query agents from town-level beads
	agents := r.queryAgents("")

	for _, agent := range agents {
		if agentBeadToAddress(agent) == identity {
			return nil // Found matching agent
		}
	}

	// Query agents from rig-level beads via routes.jsonl
	var routeQueryErr error
	if r.townRoot != "" {
		townBeadsDir := filepath.Join(r.townRoot, ".beads")
		routes, err := beads.LoadRoutes(townBeadsDir)
		if err == nil {
			var queryErrors []string
			for _, route := range routes {
				// Skip hq- routes (town-level, already queried)
				if strings.HasPrefix(route.Prefix, "hq-") {
					continue
				}
				rigBeadsDir := filepath.Join(r.townRoot, route.Path, ".beads")
				rigAgents, err := r.queryAgentsFromDir(rigBeadsDir)
				if err != nil {
					queryErrors = append(queryErrors, fmt.Sprintf("%s: %v", route.Path, err))
					continue
				}
				for _, agent := range rigAgents {
					if agentBeadToAddress(agent) == identity {
						return nil // Found matching agent
					}
				}
			}
			if len(queryErrors) > 0 {
				routeQueryErr = fmt.Errorf("no agent found (query errors: %s)", strings.Join(queryErrors, "; "))
			}
		}
	}

	// Fall back to workspace directory validation. Agent beads may be missing
	// (e.g., Dolt DB reset) even though the agent's workspace directory exists.
	if r.townRoot != "" && r.validateAgentWorkspace(identity) {
		return nil
	}

	if routeQueryErr != nil {
		return routeQueryErr
	}

	return fmt.Errorf("no agent found")
}

// validateAgentWorkspace checks if an agent's workspace directory exists on disk.
// Used as a fallback when the agent isn't found in the bead registry.
func (r *Router) validateAgentWorkspace(identity string) bool {
	parts := strings.Split(identity, "/")

	switch len(parts) {
	case 1:
		// Town-level singleton: "mayor", "deacon"
		name := strings.TrimSuffix(parts[0], "/")
		return dirExists(filepath.Join(r.townRoot, name))
	case 2:
		rig, name := parts[0], parts[1]
		// Singleton role: gastown/witness, gastown/refinery
		if dirExists(filepath.Join(r.townRoot, rig, name)) {
			return true
		}
		// Named role (identity normalized away crew/polecats): check both
		for _, role := range []string{"crew", "polecats"} {
			if dirExists(filepath.Join(r.townRoot, rig, role, name)) {
				return true
			}
		}
	case 3:
		// Explicit role paths: rig/crew/<name> or rig/polecats/<name>
		if parts[1] == "crew" || parts[1] == "polecats" {
			return dirExists(filepath.Join(r.townRoot, parts[0], parts[1], parts[2]))
		}
		// Dog addresses: deacon/dogs/<name>
		if dirExists(filepath.Join(r.townRoot, parts[0], parts[1], parts[2])) {
			return true
		}
	}

	return false
}

// dirExists returns true if the path exists and is a directory.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// resolveCrewShorthand expands "crew/name" or "polecats/name" shorthand addresses
// to fully-qualified "rig/name" form by scanning the town filesystem.
//
// When gt agents displays crew workers, it shows them as "crew/bob" (without rig).
// This function enables "gt mail send crew/bob" to work by finding the rig.
//
// Returns the normalized identity if exactly one rig contains the crew member,
// or the original identity unchanged if zero or multiple rigs match (to let
// validation fail with an informative error).
func (r *Router) resolveCrewShorthand(identity string) string {
	if r.townRoot == "" {
		return identity
	}

	parts := strings.Split(identity, "/")
	if len(parts) != 2 {
		return identity
	}

	roleDir, name := parts[0], parts[1]
	// Only handle crew and polecats shorthand (not real rig names)
	if roleDir != constants.RoleCrew && roleDir != "polecats" {
		return identity
	}

	// Check if "crew" or "polecats" is actually a real rig directory
	if fi, err := os.Stat(filepath.Join(r.townRoot, roleDir)); err == nil && fi.IsDir() {
		// It's a real rig, not a shorthand - let normal validation handle it
		return identity
	}

	// Scan rig directories for a crew/polecats member with this name
	entries, err := os.ReadDir(r.townRoot)
	if err != nil {
		return identity
	}

	var matches []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		rig := entry.Name()
		agentDir := filepath.Join(r.townRoot, rig, roleDir, name)
		if fi, err2 := os.Stat(agentDir); err2 == nil && fi.IsDir() {
			matches = append(matches, rig+"/"+name)
		}
	}

	if len(matches) == 1 {
		return matches[0] // Unambiguous: expand to rig/name
	}

	return identity // Ambiguous or not found: let validation handle it
}

// sendToSingle sends a message to a single recipient.
func (r *Router) sendToSingle(msg *Message) error {
	// Ensure message has an ID for in-memory tracking (notifications, logging).
	// We no longer pass --id to bd create; bd auto-generates the correct prefix.
	if msg.ID == "" {
		msg.ID = GenerateID()
	}

	// Validate message before sending
	if err := msg.Validate(); err != nil {
		return fmt.Errorf("invalid message: %w", err)
	}

	// Convert addresses to beads identities
	toIdentity := AddressToIdentity(msg.To)
	// Expand crew/polecats shorthand (e.g., "crew/bob" → "pata/bob")
	toIdentity = r.resolveCrewShorthand(toIdentity)

	// Validate recipient exists
	if err := r.validateRecipient(toIdentity); err != nil {
		return fmt.Errorf("invalid recipient %q: %w", msg.To, err)
	}

	// Build labels for type, from/thread/reply-to/cc
	labels := r.buildLabels(msg)

	// Build command: bd create --assignee=<recipient> -d <body> --labels=gt:message,... -- <subject>
	// Flags go first, then -- to end flag parsing, then the positional subject.
	// This prevents subjects like "--help" from being parsed as flags (see web/api.go).
	// Let bd auto-generate the ID with the correct database prefix.
	args := []string{"create",
		"--assignee", toIdentity,
		"-d", msg.Body,
	}

	// Add priority flag
	beadsPriority := PriorityToBeads(msg.Priority)
	args = append(args, "--priority", fmt.Sprintf("%d", beadsPriority))

	// Add labels
	if len(labels) > 0 {
		args = append(args, "--labels", strings.Join(labels, ","))
	}

	// Add actor for attribution (sender identity)
	args = append(args, "--actor", msg.From)

	// Do NOT pass --id to bd create. The msg.ID (msg-xxx prefix) is for
	// in-memory tracking only. bd auto-generates IDs with the correct
	// database prefix (e.g., hq-wisp-xxx). Passing --id causes prefix
	// mismatch errors when the msg- prefix does not match the database.

	// Add --ephemeral flag for ephemeral messages (wisps, not synced to git)
	if r.shouldBeWisp(msg) {
		args = append(args, "--ephemeral")
	}

	// End flag parsing with --, then add subject as positional argument.
	// This prevents subjects like "--help" or "--json" from being parsed as flags.
	args = append(args, "--", msg.Subject)

	beadsDir := r.resolveBeadsDir()
	if err := r.ensureCustomTypes(beadsDir); err != nil {
		return err
	}
	ctx, cancel := bdWriteCtx()
	defer cancel()
	_, err := runBdCommand(ctx, args, filepath.Dir(beadsDir), beadsDir)
	telemetry.RecordMailMessage(context.Background(), "send", telemetry.MailMessageInfo{
		ID:       msg.ID,
		From:     msg.From,
		To:       msg.To,
		Subject:  msg.Subject,
		Body:     msg.Body,
		ThreadID: msg.ThreadID,
		Priority: string(msg.Priority),
		MsgType:  string(msg.Type),
	}, err)
	if err != nil {
		return fmt.Errorf("sending message: %w", err)
	}

	// Notify recipient if they have an active session (best-effort notification).
	// Skip when the caller explicitly suppressed notification (--no-notify)
	// or for self-mail (handoffs to future-self don't need present-self notified).
	// Notification is async: the durable write is complete, so the caller
	// doesn't block on idle probing (up to 1s per recipient in fan-out).
	// Callers that exit soon after Send should call WaitPendingNotifications.
	if !msg.SuppressNotify && !isSelfMail(msg.From, msg.To) {
		msgCopy := *msg // copy to avoid data race if caller mutates msg
		r.notifyWg.Add(1)
		go func() {
			defer r.notifyWg.Done()
			r.notifyRecipient(&msgCopy) //nolint:errcheck
		}()
	}

	return nil
}

// sendToList expands a mailing list and sends individual copies to each recipient.
// Each recipient gets their own message copy with the same content.
// Collects all delivery errors and reports partial failures.
func (r *Router) sendToList(msg *Message) error {
	listName := parseListName(msg.To)
	recipients, err := r.expandList(listName)
	if err != nil {
		return err
	}

	// Fan-out: send a copy to each recipient, collecting all errors
	var errs []string
	for _, recipient := range recipients {
		// Create a copy of the message for this recipient
		msgCopy := *msg
		msgCopy.To = recipient
		msgCopy.ID = "" // Each fan-out copy gets its own ID from bd create

		if err := r.Send(&msgCopy); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", recipient, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("sending to list %s: some deliveries failed: %s", listName, strings.Join(errs, "; "))
	}

	return nil
}

// ExpandListAddress expands a list:name address to its recipients.
// Returns ErrUnknownList if the list is not found.
// This is exported for use by commands that want to show fan-out details.
func (r *Router) ExpandListAddress(address string) ([]string, error) {
	if !isListAddress(address) {
		return nil, fmt.Errorf("not a list address: %s", address)
	}
	return r.expandList(parseListName(address))
}

// sendToQueue delivers a message to a queue for worker claiming.
// Unlike sendToList, this creates a SINGLE message (no fan-out).
// The message is stored in town-level beads with queue metadata.
// Workers claim messages using bd update --claimed-by.
func (r *Router) sendToQueue(msg *Message) error {
	queueName := parseQueueName(msg.To)

	// Validate queue exists in messaging config
	_, err := r.expandQueue(queueName)
	if err != nil {
		return err
	}

	// Build labels for type, from/thread/reply-to/cc plus queue metadata
	var labels []string
	labels = append(labels, "gt:message")
	labels = append(labels, "from:"+msg.From)
	labels = append(labels, "queue:"+queueName)
	labels = append(labels, DeliverySendLabels()...)
	if msg.ThreadID != "" {
		labels = append(labels, "thread:"+msg.ThreadID)
	}
	if msg.ReplyTo != "" {
		labels = append(labels, "reply-to:"+msg.ReplyTo)
	}
	for _, cc := range msg.CC {
		ccIdentity := AddressToIdentity(cc)
		labels = append(labels, "cc:"+ccIdentity)
	}

	// Build command: bd create --assignee=queue:<name> -d <body> ... -- <subject>
	// Flags go first, then -- to end flag parsing, then the positional subject.
	// This prevents subjects like "--help" from being parsed as flags.
	// Use queue:<name> as assignee so inbox queries can filter by queue
	args := []string{"create",
		"--assignee", msg.To, // queue:name
		"-d", msg.Body,
	}

	// Add priority flag
	beadsPriority := PriorityToBeads(msg.Priority)
	args = append(args, "--priority", fmt.Sprintf("%d", beadsPriority))

	// Add labels (includes queue name for filtering)
	if len(labels) > 0 {
		args = append(args, "--labels", strings.Join(labels, ","))
	}

	// Add actor for attribution (sender identity)
	args = append(args, "--actor", msg.From)

	// Queue messages are never ephemeral - they need to persist until claimed
	// (deliberately not checking shouldBeWisp)

	// End flag parsing, then subject as positional argument
	args = append(args, "--", msg.Subject)

	// Queue messages go to town-level beads (shared location)
	beadsDir := r.resolveBeadsDir()
	if err := r.ensureCustomTypes(beadsDir); err != nil {
		return err
	}
	ctx, cancel := bdWriteCtx()
	defer cancel()
	_, err = runBdCommand(ctx, args, filepath.Dir(beadsDir), beadsDir)
	if err != nil {
		return fmt.Errorf("sending to queue %s: %w", queueName, err)
	}

	// No notification for queue messages - workers poll or check on their own schedule

	return nil
}

// sendToAnnounce delivers a message to an announce channel (bulletin board).
// Unlike sendToQueue, no claiming is supported - messages persist until retention limit.
// ONE copy is stored in town-level beads with announce_channel metadata.
func (r *Router) sendToAnnounce(msg *Message) error {
	announceName := parseAnnounceName(msg.To)

	// Validate announce channel exists and get config
	announceCfg, err := r.expandAnnounce(announceName)
	if err != nil {
		return err
	}

	// Apply retention pruning BEFORE creating new message
	if announceCfg.RetainCount > 0 {
		if err := r.pruneAnnounce(announceName, announceCfg.RetainCount); err != nil {
			// Log but don't fail - pruning is best-effort
			// The new message should still be created
			_ = err
		}
	}

	// Build labels for type, from/thread/reply-to/cc plus announce metadata.
	// Note: delivery:pending is intentionally omitted for announce messages —
	// broadcast messages have no single recipient to ack against. Subscriber
	// fan-out copies go through sendToSingle which adds delivery tracking.
	var labels []string
	labels = append(labels, "gt:message")
	labels = append(labels, "from:"+msg.From)
	labels = append(labels, "announce:"+announceName)
	if msg.ThreadID != "" {
		labels = append(labels, "thread:"+msg.ThreadID)
	}
	if msg.ReplyTo != "" {
		labels = append(labels, "reply-to:"+msg.ReplyTo)
	}
	for _, cc := range msg.CC {
		ccIdentity := AddressToIdentity(cc)
		labels = append(labels, "cc:"+ccIdentity)
	}

	// Build command: bd create --assignee=announce:<name> -d <body> ... -- <subject>
	// Flags go first, then -- to end flag parsing, then the positional subject.
	// This prevents subjects like "--help" from being parsed as flags.
	// Use announce:<name> as assignee so queries can filter by channel
	args := []string{"create",
		"--assignee", msg.To, // announce:name
		"-d", msg.Body,
	}

	// Add priority flag
	beadsPriority := PriorityToBeads(msg.Priority)
	args = append(args, "--priority", fmt.Sprintf("%d", beadsPriority))

	// Add labels (includes announce name for filtering)
	if len(labels) > 0 {
		args = append(args, "--labels", strings.Join(labels, ","))
	}

	// Add actor for attribution (sender identity)
	args = append(args, "--actor", msg.From)

	// Announce messages are never ephemeral - they need to persist for readers
	// (deliberately not checking shouldBeWisp)

	// End flag parsing, then subject as positional argument
	args = append(args, "--", msg.Subject)

	// Announce messages go to town-level beads (shared location)
	beadsDir := r.resolveBeadsDir()
	if err := r.ensureCustomTypes(beadsDir); err != nil {
		return err
	}
	ctx, cancel := bdWriteCtx()
	defer cancel()
	_, err = runBdCommand(ctx, args, filepath.Dir(beadsDir), beadsDir)
	if err != nil {
		return fmt.Errorf("sending to announce %s: %w", announceName, err)
	}

	// No notification for announce messages - readers poll or check on their own schedule

	return nil
}

// sendToChannel delivers a message to a beads-native channel.
// Creates a message with channel:<name> label for channel queries.
// Also fans out delivery to each subscriber's inbox.
// Retention is enforced by the channel's EnforceChannelRetention after message creation.
func (r *Router) sendToChannel(msg *Message) error {
	channelName := parseChannelName(msg.To)

	// Validate channel exists as a beads-native channel
	if r.townRoot == "" {
		return fmt.Errorf("town root not set, cannot send to channel: %s", channelName)
	}
	b := beads.New(r.townRoot)
	_, fields, err := b.GetChannelBead(channelName)
	if err != nil {
		return fmt.Errorf("getting channel %s: %w", channelName, err)
	}
	if fields == nil {
		return fmt.Errorf("channel not found: %s", channelName)
	}
	if fields.Status == beads.ChannelStatusClosed {
		return fmt.Errorf("channel %s is closed", channelName)
	}

	// Build labels for type, from/thread/reply-to/cc plus channel metadata.
	// Note: delivery:pending is intentionally omitted for the channel-origin
	// copy — it has no single recipient to ack. Subscriber fan-out copies go
	// through sendToSingle which adds delivery tracking.
	var labels []string
	labels = append(labels, "gt:message")
	labels = append(labels, "from:"+msg.From)
	labels = append(labels, "channel:"+channelName)
	if msg.ThreadID != "" {
		labels = append(labels, "thread:"+msg.ThreadID)
	}
	if msg.ReplyTo != "" {
		labels = append(labels, "reply-to:"+msg.ReplyTo)
	}
	for _, cc := range msg.CC {
		ccIdentity := AddressToIdentity(cc)
		labels = append(labels, "cc:"+ccIdentity)
	}

	// Build command: bd create --assignee=channel:<name> -d <body> ... -- <subject>
	// Flags go first, then -- to end flag parsing, then the positional subject.
	// This prevents subjects like "--help" from being parsed as flags.
	// Use channel:<name> as assignee so queries can filter by channel
	args := []string{"create",
		"--assignee", msg.To, // channel:name
		"-d", msg.Body,
	}

	// Add priority flag
	beadsPriority := PriorityToBeads(msg.Priority)
	args = append(args, "--priority", fmt.Sprintf("%d", beadsPriority))

	// Add labels (includes channel name for filtering)
	if len(labels) > 0 {
		args = append(args, "--labels", strings.Join(labels, ","))
	}

	// Add actor for attribution (sender identity)
	args = append(args, "--actor", msg.From)

	// Channel messages are never ephemeral - they persist according to retention policy
	// (deliberately not checking shouldBeWisp)

	// End flag parsing, then subject as positional argument
	args = append(args, "--", msg.Subject)

	// Channel messages go to town-level beads (shared location)
	beadsDir := r.resolveBeadsDir()
	if err := r.ensureCustomTypes(beadsDir); err != nil {
		return err
	}
	ctx, cancel := bdWriteCtx()
	defer cancel()
	_, err = runBdCommand(ctx, args, filepath.Dir(beadsDir), beadsDir)
	if err != nil {
		return fmt.Errorf("sending to channel %s: %w", channelName, err)
	}

	// Enforce channel retention policy (on-write cleanup)
	_ = b.EnforceChannelRetention(channelName)

	// Fan-out delivery: send a copy to each subscriber's inbox
	if len(fields.Subscribers) > 0 {
		var errs []string
		for _, subscriber := range fields.Subscribers {
			// Skip self-delivery (don't notify the sender)
			if isSelfMail(msg.From, subscriber) {
				continue
			}

			// Create a copy for this subscriber with channel context in subject
			msgCopy := *msg
			msgCopy.To = subscriber
			msgCopy.ID = "" // Each fan-out copy gets its own ID from bd create
			msgCopy.Subject = fmt.Sprintf("[channel:%s] %s", channelName, msg.Subject)

			if err := r.sendToSingle(&msgCopy); err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", subscriber, err))
			}
		}
		if len(errs) > 0 {
			return fmt.Errorf("channel %s: some subscriber deliveries failed: %s", channelName, strings.Join(errs, "; "))
		}
	}

	return nil
}

// pruneAnnounce deletes oldest messages from an announce channel to enforce retention.
// If the channel has >= retainCount messages, deletes the oldest until count < retainCount.
func (r *Router) pruneAnnounce(announceName string, retainCount int) error {
	if retainCount <= 0 {
		return nil // No retention limit
	}

	beadsDir := r.resolveBeadsDir()
	if err := r.ensureCustomTypes(beadsDir); err != nil {
		return err
	}

	// Query existing messages in this announce channel
	// Use bd list with labels filter to find messages with gt:message and announce:<name> labels
	args := []string{"list",
		"--labels=gt:message,announce:" + announceName,
		"--json",
		"--limit=0", // Get all
		"--sort=created",
		"--asc", // Oldest first
	}

	ctx, cancel := bdReadCtx()
	defer cancel()
	stdout, err := runBdCommand(ctx, args, filepath.Dir(beadsDir), beadsDir)
	if err != nil {
		return fmt.Errorf("querying announce messages: %w", err)
	}

	// Parse message list
	var messages []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(stdout, &messages); err != nil {
		return fmt.Errorf("parsing announce messages: %w", err)
	}

	// Calculate how many to delete (we're about to add 1 more)
	// If we have N messages and retainCount is R, we need to keep at most R-1 after pruning
	// so the new message makes it exactly R
	toDelete := len(messages) - (retainCount - 1)
	if toDelete <= 0 {
		return nil // No pruning needed
	}

	// Delete oldest messages
	for i := 0; i < toDelete && i < len(messages); i++ {
		deleteArgs := []string{"close", messages[i].ID, "--reason=retention pruning"}
		// Best-effort deletion - don't fail if one delete fails
		delCtx, delCancel := bdWriteCtx()
		_, _ = runBdCommand(delCtx, deleteArgs, filepath.Dir(beadsDir), beadsDir)
		delCancel()
	}

	return nil
}

// isSelfMail returns true if sender and recipient are the same identity.
// Uses AddressToIdentity for canonical normalization (handles crew/, polecats/ paths).
func isSelfMail(from, to string) bool {
	return AddressToIdentity(from) == AddressToIdentity(to)
}

// GetMailbox returns a Mailbox for the given address.
// Routes to the correct beads database based on the address.
func (r *Router) GetMailbox(address string) (*Mailbox, error) {
	beadsDir := r.resolveBeadsDir()
	workDir := filepath.Dir(beadsDir) // Parent of .beads
	return NewMailboxFromAddress(address, workDir), nil
}

// notifyRecipient sends a notification to a recipient's tmux session.
//
// Notification strategy (idle-aware):
//  1. If the session is idle (prompt visible), send an immediate nudge.
//  2. If the session is busy, enqueue a nudge for cooperative delivery at
//     the next turn boundary.
//  3. For the overseer (human operator), always use a visible banner.
//
// After a successful notification, a deferred reply-reminder nudge is also
// enqueued (after a configurable delay, default 30s) to prompt the recipient
// to reply via gt mail send rather than in chat.
//
// Supports mayor/, deacon/, rig/crew/name, rig/polecats/name, and rig/name addresses.
// Respects agent DND/muted state - skips notification if recipient has DND enabled.
func (r *Router) notifyRecipient(msg *Message) error {
	sessionIDs := AddressToSessionIDs(msg.To)
	if len(sessionIDs) == 0 {
		return nil // Unable to determine session ID
	}

	timeout := r.IdleNotifyTimeout
	if timeout == 0 {
		timeout = DefaultIdleNotifyTimeout
	}

	notification := formatNotificationMessage(msg)
	priority := nudgePriorityForMailPriority(msg.Priority)
	notified := 0
	var errs []string
	noTmuxServer := false

	// Try every possible session ID. Canonical aliases (rig/name) can map to both
	// crew and polecat sessions, and stopping after the first active session makes
	// mail disappear for the other active alias owner.
	for _, sessionID := range sessionIDs {
		if r.isSessionMuted(sessionID) {
			continue
		}

		hasSession, err := r.tmux.HasSession(sessionID)
		if errors.Is(err, tmux.ErrNoServer) {
			noTmuxServer = true
			break
		}
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", sessionID, err))
			continue
		}
		if !hasSession {
			continue
		}

		// Overseer is a human operator - use a visible banner instead of NudgeSession
		// (which types into Claude's input and would disrupt the human's terminal).
		if msg.To == "overseer" {
			if err := r.tmux.SendNotificationBanner(sessionID, msg.From, msg.Subject); err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", sessionID, err))
				continue
			}
			notified++
			continue
		}

		// Wait-idle-first delivery: try direct nudge if the agent is idle,
		// fall back to cooperative queue if busy. WaitForIdle requires 2
		// consecutive idle polls (prompt visible + no "esc to interrupt"
		// in the status bar) to distinguish genuine idle from brief
		// inter-tool-call gaps. See: https://github.com/steveyegge/gastown/issues/2032
		waitErr := r.tmux.WaitForIdle(sessionID, timeout)
		if waitErr == nil {
			// Agent is idle — deliver directly for immediate wakeup.
			if err := r.tmux.NudgeSession(sessionID, notification); err == nil {
				r.enqueueReplyReminder(msg, sessionID)
				notified++
				continue
			} else if errors.Is(err, tmux.ErrSessionNotFound) {
				continue
			} else if errors.Is(err, tmux.ErrNoServer) {
				noTmuxServer = true
				break
			} else {
				errs = append(errs, fmt.Sprintf("%s: %v", sessionID, err))
				continue
			}
		} else if errors.Is(waitErr, tmux.ErrSessionNotFound) {
			continue
		} else if errors.Is(waitErr, tmux.ErrNoServer) {
			noTmuxServer = true
			break
		} else if r.townRoot != "" {
			// Timeout (agent busy) — queue for cooperative delivery
			// at the next turn boundary.
			if err := nudge.Enqueue(r.townRoot, sessionID, nudge.QueuedNudge{
				Sender:   msg.From,
				Message:  notification,
				Priority: priority,
				Kind:     nudgeKindForMessage(msg),
				ThreadID: msg.ThreadID,
				Severity: prioritySeverityLabel(msg.Priority),
			}); err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", sessionID, err))
				continue
			}
			r.enqueueReplyReminder(msg, sessionID)
			notified++
			continue
		}
		// No town root available — last resort direct delivery.
		err = r.tmux.NudgeSession(sessionID, notification)
		if err == nil {
			r.enqueueReplyReminder(msg, sessionID)
			notified++
			continue
		}
		if errors.Is(err, tmux.ErrNoServer) {
			noTmuxServer = true
			break
		}
		errs = append(errs, fmt.Sprintf("%s: %v", sessionID, err))
	}

	if notified == 0 && r.townRoot != "" && (noTmuxServer || len(errs) == 0) {
		// No tmux session found - enqueue for ACP/propeller delivery. For
		// ambiguous aliases, queue every candidate rather than silently choosing
		// the first session ID.
		for _, sessionID := range sessionIDs {
			if r.isSessionMuted(sessionID) {
				continue
			}
			if err := nudge.Enqueue(r.townRoot, sessionID, nudge.QueuedNudge{
				Sender:   msg.From,
				Message:  notification,
				Priority: priority,
				Kind:     nudgeKindForMessage(msg),
				ThreadID: msg.ThreadID,
				Severity: prioritySeverityLabel(msg.Priority),
			}); err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", sessionID, err))
				continue
			}
			notified++
		}
	}

	if len(errs) > 0 {
		if notified > 0 {
			fmt.Fprintf(os.Stderr, "Warning: mail notification partially failed: %s\n", strings.Join(errs, "; "))
			return nil
		}
		return fmt.Errorf("mail notification failed: %s", strings.Join(errs, "; "))
	}

	return nil // No active session found
}

func (r *Router) isSessionMuted(sessionID string) bool {
	if r.townRoot == "" || sessionID == "" || sessionID == session.OverseerSessionName() {
		return false
	}
	bd := beads.New(r.townRoot)
	level, err := bd.GetAgentNotificationLevel(sessionID)
	if err != nil {
		return false
	}
	return level == beads.NotifyMuted
}

func nudgeKindForMessage(msg *Message) string {
	if msg.Type == TypeEscalation {
		return "escalation"
	}
	return "mail"
}

func nudgePriorityForMailPriority(priority Priority) string {
	switch priority {
	case PriorityUrgent, PriorityHigh:
		return nudge.PriorityUrgent
	default:
		return nudge.PriorityNormal
	}
}

func formatNotificationMessage(msg *Message) string {
	if msg.Type == TypeEscalation {
		return fmt.Sprintf("🚨 Escalation mail from %s. ID: %s. Severity: %s. Subject: %s. Run 'gt mail read %s' or 'gt escalate ack %s'.", msg.From, msg.ThreadID, prioritySeverityLabel(msg.Priority), msg.Subject, msg.ThreadID, msg.ThreadID)
	}
	return fmt.Sprintf("📬 You have new mail from %s. Subject: %s. Run 'gt mail inbox' to read.", msg.From, msg.Subject)
}

func prioritySeverityLabel(priority Priority) string {
	switch priority {
	case PriorityUrgent:
		return "critical"
	case PriorityHigh:
		return "high"
	case PriorityLow:
		return "low"
	default:
		return "medium"
	}
}

// enqueueReplyReminder queues a deferred nudge reminding the recipient to reply
// via gt mail send rather than in chat. Best-effort: errors are logged, not returned.
//
// Skipped when:
//   - No town root (can't use nudge queue)
//   - Message type is TypeReply (recipient is already replying)
//   - Configured delay is zero or negative (feature disabled)
func (r *Router) enqueueReplyReminder(msg *Message, sessionID string) {
	if r.townRoot == "" {
		return
	}
	if msg.Type == TypeReply {
		return // Already a reply — reminder would be redundant
	}
	delay := config.LoadOperationalConfig(r.townRoot).GetMailConfig().ReplyReminderDelayD()
	if delay <= 0 {
		return // Disabled by config
	}
	reminder := nudge.QueuedNudge{
		Sender:       "system",
		Message:      fmt.Sprintf("Remember to reply to %s (subject: %q) via `gt mail send %s` — not in chat.", msg.From, msg.Subject, msg.From),
		Priority:     nudge.PriorityNormal,
		Kind:         "reply-reminder",
		ThreadID:     msg.ThreadID,
		DeliverAfter: time.Now().Add(delay),
	}
	if err := nudge.Enqueue(r.townRoot, sessionID, reminder); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to enqueue reply reminder for %s: %v\n", sessionID, err)
	}
}

// ClearReplyReminders removes any queued reply-reminder nudges for the given
// recipient identity and thread. This is best-effort cleanup after a successful
// reply send so satisfied threads do not keep re-nudging.
func (r *Router) ClearReplyReminders(address, threadID string) error {
	if r.townRoot == "" || threadID == "" {
		return nil
	}

	var firstErr error
	for _, sessionID := range AddressToSessionIDs(address) {
		if _, err := nudge.RemoveKindByThread(r.townRoot, sessionID, "reply-reminder", threadID); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// IsRecipientMuted checks if a mail recipient has DND/muted notifications enabled.
// Returns true if the recipient is muted and should not receive tmux nudges.
// Fails open (returns false) if the agent bead cannot be found or the town root is not set.
func (r *Router) IsRecipientMuted(address string) bool {
	if r.townRoot == "" {
		return false
	}
	return r.isRecipientMuted(address)
}

// isRecipientMuted checks if a mail recipient has DND/muted notifications enabled.
// Returns true if the recipient is muted and should not receive tmux nudges.
// Fails open (returns false) if the agent bead cannot be found.
func (r *Router) isRecipientMuted(address string) bool {
	agentBeadID := addressToAgentBeadID(address)
	if agentBeadID == "" {
		return false // Can't determine agent bead, allow notification
	}

	bd := beads.New(r.townRoot)
	level, err := bd.GetAgentNotificationLevel(agentBeadID)
	if err != nil {
		return false // Agent bead might not exist, allow notification
	}

	return level == beads.NotifyMuted
}

// addressToAgentBeadID converts a mail address to an agent bead ID for DND lookup.
// Returns empty string if the address cannot be converted.
func addressToAgentBeadID(address string) string {
	switch {
	case address == "overseer":
		return "" // Overseer is a human, no agent bead
	case strings.HasPrefix(address, constants.RoleMayor):
		return session.MayorSessionName()
	case strings.HasPrefix(address, constants.RoleDeacon):
		return session.DeaconSessionName()
	}

	parts := strings.SplitN(address, "/", 2)
	if len(parts) != 2 || parts[1] == "" {
		return ""
	}

	rig := parts[0]
	target := parts[1]

	rigPrefix := session.PrefixFor(rig)

	switch {
	case target == constants.RoleWitness:
		return session.WitnessSessionName(rigPrefix)
	case target == constants.RoleRefinery:
		return session.RefinerySessionName(rigPrefix)
	case strings.HasPrefix(target, "crew/"):
		crewName := strings.TrimPrefix(target, "crew/")
		return session.CrewSessionName(rigPrefix, crewName)
	case strings.HasPrefix(target, "polecat/"):
		pcName := strings.TrimPrefix(target, "polecat/")
		return session.PolecatSessionName(rigPrefix, pcName)
	case strings.HasPrefix(target, "polecats/"):
		pcName := strings.TrimPrefix(target, "polecats/")
		return session.PolecatSessionName(rigPrefix, pcName)
	default:
		return session.PolecatSessionName(rigPrefix, target)
	}
}

// AddressToSessionIDs converts a mail address to possible tmux session IDs.
// Returns multiple candidates since the canonical address format (rig/name)
// doesn't distinguish between crew workers (gt-rig-crew-name) and polecats
// (gt-rig-name). The caller should try each and use the one that exists.
//
// This supersedes the approach in PR #896 which only handled slash-to-dash
// conversion but didn't address the crew/polecat ambiguity.
func AddressToSessionIDs(address string) []string {
	// Overseer address: "overseer" (human operator)
	if address == "overseer" {
		return []string{session.OverseerSessionName()}
	}

	// Mayor address: "mayor/" or "mayor"
	if strings.HasPrefix(address, constants.RoleMayor) {
		return []string{session.MayorSessionName()}
	}

	// Deacon address: "deacon/" or "deacon"
	if strings.HasPrefix(address, constants.RoleDeacon) {
		return []string{session.DeaconSessionName()}
	}

	// Rig-based address: "rig/target" or "rig/crew/name" or "rig/polecats/name"
	parts := strings.SplitN(address, "/", 2)
	if len(parts) != 2 || parts[1] == "" {
		return nil
	}

	rig := parts[0]
	target := parts[1]
	rigPrefix := session.PrefixFor(rig)

	// If target already has crew/, polecat/, or polecats/ prefix, use it directly
	// e.g., "gastown/crew/holden" → "gt-crew-holden"
	if strings.HasPrefix(target, "crew/") {
		crewName := strings.TrimPrefix(target, "crew/")
		return []string{session.CrewSessionName(rigPrefix, crewName)}
	}
	if strings.HasPrefix(target, "polecat/") {
		polecatName := strings.TrimPrefix(target, "polecat/")
		return []string{session.PolecatSessionName(rigPrefix, polecatName)}
	}
	if strings.HasPrefix(target, "polecats/") {
		polecatName := strings.TrimPrefix(target, "polecats/")
		return []string{session.PolecatSessionName(rigPrefix, polecatName)}
	}

	// Special cases that don't need crew variant
	if target == constants.RoleWitness {
		return []string{session.WitnessSessionName(rigPrefix)}
	}
	if target == constants.RoleRefinery {
		return []string{session.RefinerySessionName(rigPrefix)}
	}

	// For normalized addresses like "gastown/holden", try both:
	// 1. Crew format: gt-crew-holden
	// 2. Polecat format: gt-holden
	// Return crew first since crew workers are more commonly missed.
	return []string{
		session.CrewSessionName(rigPrefix, target),    // <prefix>-crew-name
		session.PolecatSessionName(rigPrefix, target), // <prefix>-name
	}
}
