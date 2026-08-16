package doctor

import (
	"fmt"
	"strings"

	"github.com/jonbaldie/gastown/internal/session"
	"github.com/jonbaldie/gastown/internal/tmux"
)

// tmuxRenamer is the minimal tmux interface needed by Fix().
// Allows injecting a mock in tests without depending on a live tmux.
type tmuxRenamer interface {
	HasSession(name string) (bool, error)
	RenameSession(from, to string) error
}

// MalformedSessionNameCheck detects Gas Town tmux sessions whose names use the
// legacy naming scheme (e.g., "gt-whatsapp_automation-witness") rather than the
// current short-prefix format (e.g., "wa-witness").
//
// Detection uses explicit legacy-name matching rather than a parse round-trip.
// Round-trip detection cannot catch legacy names: "gt-whatsapp_automation-witness"
// parses as a polecat named "whatsapp_automation-witness" and round-trips to the
// same string — no mismatch is ever reported.
//
// Instead, we scan sessions for the pattern:
//
//	{any_prefix}-{registered_rig_name}-{role_suffix}
//
// where {registered_rig_name} is a known rig (e.g., "whatsapp_automation") and
// {role_suffix} is a valid Gas Town role ("witness", "refinery", "crew-{name}").
// The canonical name is then: {rig_short_prefix}-{role_suffix}.
type MalformedSessionNameCheck struct {
	FixableCheck
	sessionListerForTest SessionLister // Injectable for testing; nil uses real tmux
	registryForTest      *session.PrefixRegistry
	tmuxForTest          tmuxRenamer     // Injectable for Fix() testing; nil uses real tmux
	malformed            []sessionRename // Cached during Run for use in Fix
}

type sessionRename struct {
	oldName string
	newName string
	isCrew  bool // crew sessions require manual rename
}

// NewMalformedSessionNameCheck creates a new malformed session name check.
func NewMalformedSessionNameCheck() *MalformedSessionNameCheck {
	return &MalformedSessionNameCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "session-name-format",
				CheckDescription: "Detect sessions with outdated Gas Town naming format",
				CheckCategory:    CategoryCleanup,
			},
		},
	}
}

// Run detects sessions whose names use the legacy {prefix}-{rig_name}-{role} format.
func (c *MalformedSessionNameCheck) Run(ctx *CheckContext) *CheckResult {
	lister := c.sessionListerForTest
	if lister == nil {
		lister = &realSessionLister{t: tmux.NewTmux()}
	}

	reg := c.registryForTest
	if reg == nil {
		reg = session.DefaultRegistry()
	}

	sessions, err := lister.ListSessions()
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "Could not list tmux sessions",
			Details: []string{err.Error()},
		}
	}

	malformed := detectLegacySessionNames(sessions, reg)
	c.malformed = malformed

	if len(malformed) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "All Gas Town sessions use current naming format",
		}
	}

	// Separate auto-fixable from crew (manual-only).
	var autoFixable, needsManual []sessionRename
	for _, r := range malformed {
		if r.isCrew {
			needsManual = append(needsManual, r)
		} else {
			autoFixable = append(autoFixable, r)
		}
	}

	var details []string
	for _, r := range autoFixable {
		details = append(details, fmt.Sprintf("Outdated: %s → should be %s", r.oldName, r.newName))
	}
	for _, r := range needsManual {
		details = append(details, fmt.Sprintf("Outdated: %s → should be %s (crew session — manual rename required)", r.oldName, r.newName))
	}

	fixHint := "Run 'gt doctor --fix' to rename sessions to current format"
	if len(autoFixable) == 0 && len(needsManual) > 0 {
		fixHint = "Crew sessions must be renamed manually: tmux rename-session -t OLD NEW"
	} else if len(needsManual) > 0 {
		fixHint = "Run 'gt doctor --fix' for patrol sessions; crew sessions must be renamed manually"
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: fmt.Sprintf("Found %d session(s) with outdated naming format", len(malformed)),
		Details: details,
		FixHint: fixHint,
	}
}

// Fix renames auto-fixable legacy sessions to their canonical names.
// Crew sessions are silently skipped — Run already told the user they need
// manual intervention, so Fix does not mislead them into thinking --fix works.
func (c *MalformedSessionNameCheck) Fix(ctx *CheckContext) error {
	if len(c.malformed) == 0 {
		return nil
	}

	var t tmuxRenamer
	if c.tmuxForTest != nil {
		t = c.tmuxForTest
	} else {
		t = tmux.NewTmux()
	}
	var lastErr error

	for _, r := range c.malformed {
		if r.isCrew {
			// Crew sessions require manual rename; skip without error.
			continue
		}

		// TOCTOU guard: a prior check (e.g., zombie-sessions) may have already
		// killed the source session between Run() and Fix().
		sourceExists, err := t.HasSession(r.oldName)
		if err != nil {
			lastErr = fmt.Errorf("check source session %s: %w", r.oldName, err)
			continue
		}
		if !sourceExists {
			continue
		}

		// Skip if target name is already in use (collision).
		targetExists, err := t.HasSession(r.newName)
		if err != nil {
			lastErr = fmt.Errorf("check target session %s: %w", r.newName, err)
			continue
		}
		if targetExists {
			continue
		}

		if err := t.RenameSession(r.oldName, r.newName); err != nil {
			lastErr = fmt.Errorf("rename %s → %s: %w", r.oldName, r.newName, err)
		}
	}

	return lastErr
}

// knownRoleSuffixes are the simple role keywords that appear at the end of a
// Gas Town session name (after the rig prefix).
var knownRoleSuffixes = []string{"witness", "refinery"}

// detectLegacySessionNames scans sessions for the legacy
//
//	{any_prefix}-{registered_rig_name}-{role_suffix}
//
// pattern. For each match it computes the canonical name
//
//	{rig_short_prefix}-{role_suffix}
//
// and returns the rename list.
func detectLegacySessionNames(sessions []string, reg *session.PrefixRegistry) []sessionRename {
	rigs := reg.AllRigs() // rigName → shortPrefix
	if len(rigs) == 0 {
		return nil
	}

	// Build set of known Gastown prefixes for ownership gating.
	knownPrefixes := make(map[string]bool)
	for _, prefix := range rigs {
		knownPrefixes[prefix] = true
	}

	var result []sessionRename
	seen := make(map[string]bool)

	for _, sess := range sessions {
		if sess == "" || seen[sess] {
			continue
		}
		r, ok := matchLegacyName(sess, rigs, knownPrefixes)
		if ok {
			seen[sess] = true
			result = append(result, r)
		}
	}
	return result
}

// matchLegacyName checks whether sess matches the old
//
//	{known_prefix}-{rig_name}-{role_suffix}  or  {known_prefix}-{rig_name}-crew-{name}
//
// pattern for any known rig, and returns the canonical rename if so.
// The prefix before the rig name must be a known Gastown prefix to avoid
// false-positives on non-Gastown sessions (e.g., "my-niflheim-witness")
// and polecat sessions whose names embed rig names (e.g., "gt-fix-gastown-witness").
func matchLegacyName(sess string, rigs map[string]string, knownPrefixes map[string]bool) (sessionRename, bool) {
	for rigName, shortPrefix := range rigs {
		// Look for "-{rigName}-" anywhere in the session name.
		needle := "-" + rigName + "-"
		idx := strings.Index(sess, needle)
		if idx < 0 {
			continue
		}

		// Ownership guard: the part before the rig name must be a known
		// Gastown prefix. This prevents matching non-Gastown sessions
		// and polecat sessions whose names happen to contain a rig name.
		sessionPrefix := sess[:idx]
		if !knownPrefixes[sessionPrefix] {
			continue
		}

		// Skip if the session already uses the correct prefix for this rig.
		if sessionPrefix == shortPrefix {
			continue
		}

		// The part after the rig name is the role suffix.
		roleSuffix := sess[idx+len(needle):]
		if roleSuffix == "" {
			continue
		}

		// Validate: must be a known Gas Town role suffix.
		if !isValidRoleSuffix(roleSuffix) {
			continue
		}

		canonical := shortPrefix + "-" + roleSuffix
		isCrew := strings.HasPrefix(roleSuffix, "crew-")

		return sessionRename{
			oldName: sess,
			newName: canonical,
			isCrew:  isCrew,
		}, true
	}
	return sessionRename{}, false
}

// isValidRoleSuffix returns true if suffix is a known Gas Town role identifier.
func isValidRoleSuffix(suffix string) bool {
	for _, role := range knownRoleSuffixes {
		if suffix == role {
			return true
		}
	}
	return strings.HasPrefix(suffix, "crew-") && len(suffix) > len("crew-")
}
