// Package beads provides field parsing utilities for structured issue descriptions.
package beads

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Note: AgentFields, ParseAgentFields, FormatAgentDescription, and CreateAgentBead are in beads.go

// AttachmentFields holds the attachment info for pinned beads.
// These fields track which molecule is attached to a handoff/pinned bead.
type AttachmentFields struct {
	AttachedMolecule string   // Root issue ID of the attached molecule
	AttachedFormula  string   // Formula name (e.g., "mol-polecat-work") for inline step display
	AttachedAt       string   // ISO 8601 timestamp when attached
	AttachedArgs     string   // Natural language args passed via gt sling --args (no-tmux mode)
	AttachedVars     []string // Formula variables passed via gt sling --var
	DispatchedBy     string   // Agent ID that dispatched this work (for completion notification)
	NoMerge          bool     // If true, gt done skips merge queue (for upstream PRs/human review)
	ReviewOnly       bool     // If true, assignee must evaluate and report back — no merge/commit/push
	Mode             string   // Execution mode: "" (normal) or "ralph" (Ralph Wiggum loop)
	ConvoyID         string   // Convoy bead ID tracking this issue (e.g., "hq-cv-abc")
	MergeStrategy    string   // Convoy merge strategy: "direct", "mr", "local", or "" (default = mr)
	ConvoyOwned      bool     // If true, convoy has gt:owned label (caller-managed lifecycle)
	FormulaVars      string   // Newline-separated key=value pairs for formula template substitution
}

// ParseAttachmentFields extracts attachment fields from an issue's description.
// Fields are expected as "key: value" lines. Returns nil if no attachment fields found.
func ParseAttachmentFields(issue *Issue) *AttachmentFields {
	if issue == nil || issue.Description == "" {
		return nil
	}

	fields := &AttachmentFields{}
	hasFields := false
	var formulaVars []string

	for _, line := range strings.Split(issue.Description, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Look for "key: value" pattern
		colonIdx := strings.Index(line, ":")
		if colonIdx == -1 {
			continue
		}

		key := strings.TrimSpace(line[:colonIdx])
		value := strings.TrimSpace(line[colonIdx+1:])
		if value == "" {
			continue
		}

		// Map keys to fields (case-insensitive)
		switch strings.ToLower(key) {
		case "attached_molecule", "attached-molecule", "attachedmolecule":
			fields.AttachedMolecule = value
			hasFields = true
		case "attached_formula", "attached-formula", "attachedformula":
			fields.AttachedFormula = value
			hasFields = true
		case "attached_at", "attached-at", "attachedat":
			fields.AttachedAt = value
			hasFields = true
		case "attached_args", "attached-args", "attachedargs":
			fields.AttachedArgs = value
			hasFields = true
		case "attached_vars", "attached-vars", "attachedvars":
			fields.AttachedVars = parseAttachedVars(value)
			hasFields = true
		case "dispatched_by", "dispatched-by", "dispatchedby":
			fields.DispatchedBy = value
			hasFields = true
		case "no_merge", "no-merge", "nomerge":
			fields.NoMerge = strings.ToLower(value) == "true"
			hasFields = true
		case "review_only", "review-only", "reviewonly":
			fields.ReviewOnly = strings.ToLower(value) == "true"
			hasFields = true
		case "mode":
			fields.Mode = value
			hasFields = true
		case "convoy_id", "convoy-id", "convoyid", "convoy":
			fields.ConvoyID = value
			hasFields = true
		case "merge_strategy", "merge-strategy", "mergestrategy":
			fields.MergeStrategy = value
			hasFields = true
		case "convoy_owned", "convoy-owned", "convoyowned":
			fields.ConvoyOwned = strings.ToLower(value) == "true"
			hasFields = true
		case "formula_vars", "formula-vars", "formulavars":
			formulaVars = append(formulaVars, splitFormulaVars(parseFormulaVars(value))...)
			hasFields = true
		}
	}
	if len(formulaVars) > 0 {
		fields.FormulaVars = strings.Join(formulaVars, "\n")
	}

	if !hasFields {
		return nil
	}
	return fields
}

// FormatAttachmentFields formats AttachmentFields as a string suitable for an issue description.
// Only non-empty fields are included.
func FormatAttachmentFields(fields *AttachmentFields) string {
	if fields == nil {
		return ""
	}

	var lines []string

	if fields.AttachedMolecule != "" {
		lines = append(lines, "attached_molecule: "+fields.AttachedMolecule)
	}
	if fields.AttachedFormula != "" {
		lines = append(lines, "attached_formula: "+fields.AttachedFormula)
	}
	if fields.AttachedAt != "" {
		lines = append(lines, "attached_at: "+fields.AttachedAt)
	}
	if fields.AttachedArgs != "" {
		lines = append(lines, "attached_args: "+fields.AttachedArgs)
	}
	if len(fields.AttachedVars) > 0 {
		lines = append(lines, "attached_vars: "+formatAttachedVars(fields.AttachedVars))
	}
	if fields.DispatchedBy != "" {
		lines = append(lines, "dispatched_by: "+fields.DispatchedBy)
	}
	if fields.NoMerge {
		lines = append(lines, "no_merge: true")
	}
	if fields.ReviewOnly {
		lines = append(lines, "review_only: true")
	}
	if fields.Mode != "" {
		lines = append(lines, "mode: "+fields.Mode)
	}
	if fields.ConvoyID != "" {
		lines = append(lines, "convoy_id: "+fields.ConvoyID)
	}
	if fields.MergeStrategy != "" {
		lines = append(lines, "merge_strategy: "+fields.MergeStrategy)
	}
	if fields.ConvoyOwned {
		lines = append(lines, "convoy_owned: true")
	}
	if fields.FormulaVars != "" {
		if formatted := formatFormulaVars(fields.FormulaVars); formatted != "" {
			lines = append(lines, "formula_vars: "+formatted)
		}
	}

	return strings.Join(lines, "\n")
}

// SetAttachmentFields updates an issue's description with the given attachment fields.
// Existing attachment field lines are replaced; other content is preserved.
// Returns the new description string.
func SetAttachmentFields(issue *Issue, fields *AttachmentFields) string {
	// Known attachment field keys (lowercase)
	attachmentKeys := map[string]bool{
		"attached_molecule": true,
		"attached-molecule": true,
		"attachedmolecule":  true,
		"attached_formula":  true,
		"attached-formula":  true,
		"attachedformula":   true,
		"attached_at":       true,
		"attached-at":       true,
		"attachedat":        true,
		"attached_args":     true,
		"attached-args":     true,
		"attachedargs":      true,
		"attached_vars":     true,
		"attached-vars":     true,
		"attachedvars":      true,
		"dispatched_by":     true,
		"dispatched-by":     true,
		"dispatchedby":      true,
		"no_merge":          true,
		"no-merge":          true,
		"nomerge":           true,
		"review_only":       true,
		"review-only":       true,
		"reviewonly":        true,
		"mode":              true,
		"convoy_id":         true,
		"convoy-id":         true,
		"convoyid":          true,
		"convoy":            true,
		"merge_strategy":    true,
		"merge-strategy":    true,
		"mergestrategy":     true,
		"convoy_owned":      true,
		"convoy-owned":      true,
		"convoyowned":       true,
		"formula_vars":      true,
		"formula-vars":      true,
		"formulavars":       true,
	}

	// Collect non-attachment lines from existing description
	var otherLines []string
	if issue != nil && issue.Description != "" {
		for _, line := range strings.Split(issue.Description, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				// Preserve blank lines in content
				otherLines = append(otherLines, line)
				continue
			}

			// Check if this is an attachment field line
			colonIdx := strings.Index(trimmed, ":")
			if colonIdx == -1 {
				otherLines = append(otherLines, line)
				continue
			}

			key := strings.ToLower(strings.TrimSpace(trimmed[:colonIdx]))
			if !attachmentKeys[key] {
				otherLines = append(otherLines, line)
			}
			// Skip attachment field lines - they'll be replaced
		}
	}

	// Build new description: attachment fields first, then other content
	formatted := FormatAttachmentFields(fields)

	// Trim trailing blank lines from other content
	for len(otherLines) > 0 && strings.TrimSpace(otherLines[len(otherLines)-1]) == "" {
		otherLines = otherLines[:len(otherLines)-1]
	}
	// Trim leading blank lines from other content
	for len(otherLines) > 0 && strings.TrimSpace(otherLines[0]) == "" {
		otherLines = otherLines[1:]
	}

	if formatted == "" {
		return strings.Join(otherLines, "\n")
	}
	if len(otherLines) == 0 {
		return formatted
	}

	return formatted + "\n\n" + strings.Join(otherLines, "\n")
}

// ConvoyFields holds the structured fields for a convoy bead.
// These fields are stored as key: value lines in the issue description.
type ConvoyFields struct {
	Owner                string // Convoy owner address (e.g., "mayor/")
	Notify               string // Additional notification address
	Molecule             string // Associated molecule/swarm ID
	Merge                string // Merge strategy
	BaseBranch           string // Target branch for polecats (e.g., "feat/extraction-review")
	Watchers             string // Comma-separated mail notification addresses (added via gt convoy watch)
	NudgeWatchers        string // Comma-separated nudge notification addresses (added via gt convoy watch --nudge)
	CompletionNotifiedAt string // RFC3339 timestamp when completion notifications were claimed/sent
}

// ParseConvoyFields extracts convoy fields from an issue's description.
// Returns nil if no convoy fields found.
func ParseConvoyFields(issue *Issue) *ConvoyFields {
	if issue == nil || issue.Description == "" {
		return nil
	}

	fields := &ConvoyFields{}
	hasFields := false

	for _, line := range strings.Split(issue.Description, "\n") {
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
		if value == "" {
			continue
		}

		switch strings.ToLower(key) {
		case "owner":
			fields.Owner = value
			hasFields = true
		case "notify":
			fields.Notify = value
			hasFields = true
		case "molecule":
			fields.Molecule = value
			hasFields = true
		case "merge":
			fields.Merge = value
			hasFields = true
		case "base_branch", "base-branch", "basebranch":
			fields.BaseBranch = value
			hasFields = true
		case "watchers":
			fields.Watchers = value
			hasFields = true
		case "nudge_watchers", "nudge-watchers", "nudgewatchers":
			fields.NudgeWatchers = value
			hasFields = true
		case "completion_notified_at", "completion-notified-at", "completionnotifiedat":
			fields.CompletionNotifiedAt = value
			hasFields = true
		}
	}

	if !hasFields {
		return nil
	}
	return fields
}

// NotificationAddresses returns deduplicated mail notification addresses from convoy fields.
// Includes Owner, Notify, and all Watchers addresses.
func (f *ConvoyFields) NotificationAddresses() []string {
	if f == nil {
		return nil
	}
	seen := make(map[string]bool)
	var addrs []string
	for _, addr := range []string{f.Owner, f.Notify} {
		if addr != "" && !seen[addr] {
			addrs = append(addrs, addr)
			seen[addr] = true
		}
	}
	for _, addr := range splitWatchers(f.Watchers) {
		if addr != "" && !seen[addr] {
			addrs = append(addrs, addr)
			seen[addr] = true
		}
	}
	return addrs
}

// NudgeNotificationAddresses returns deduplicated nudge addresses from convoy fields.
func (f *ConvoyFields) NudgeNotificationAddresses() []string {
	if f == nil {
		return nil
	}
	seen := make(map[string]bool)
	var addrs []string
	for _, addr := range splitWatchers(f.NudgeWatchers) {
		if addr != "" && !seen[addr] {
			addrs = append(addrs, addr)
			seen[addr] = true
		}
	}
	return addrs
}

// AddWatcher adds a mail watcher address to the comma-separated Watchers field.
// Returns true if the address was added (false if already present).
func (f *ConvoyFields) AddWatcher(addr string) bool {
	existing := splitWatchers(f.Watchers)
	for _, w := range existing {
		if w == addr {
			return false
		}
	}
	existing = append(existing, addr)
	f.Watchers = strings.Join(existing, ",")
	return true
}

// AddNudgeWatcher adds a nudge watcher address to the comma-separated NudgeWatchers field.
// Returns true if the address was added (false if already present).
func (f *ConvoyFields) AddNudgeWatcher(addr string) bool {
	existing := splitWatchers(f.NudgeWatchers)
	for _, w := range existing {
		if w == addr {
			return false
		}
	}
	existing = append(existing, addr)
	f.NudgeWatchers = strings.Join(existing, ",")
	return true
}

// RemoveWatcher removes a mail watcher address. Returns true if it was present.
func (f *ConvoyFields) RemoveWatcher(addr string) bool {
	existing := splitWatchers(f.Watchers)
	var remaining []string
	found := false
	for _, w := range existing {
		if w == addr {
			found = true
		} else {
			remaining = append(remaining, w)
		}
	}
	if found {
		f.Watchers = strings.Join(remaining, ",")
	}
	return found
}

// RemoveNudgeWatcher removes a nudge watcher address. Returns true if it was present.
func (f *ConvoyFields) RemoveNudgeWatcher(addr string) bool {
	existing := splitWatchers(f.NudgeWatchers)
	var remaining []string
	found := false
	for _, w := range existing {
		if w == addr {
			found = true
		} else {
			remaining = append(remaining, w)
		}
	}
	if found {
		f.NudgeWatchers = strings.Join(remaining, ",")
	}
	return found
}

// splitWatchers splits a comma-separated watcher string into trimmed, non-empty addresses.
func splitWatchers(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// FormatConvoyFields formats ConvoyFields as a string suitable for an issue description.
// Only non-empty fields are included.
func FormatConvoyFields(fields *ConvoyFields) string {
	if fields == nil {
		return ""
	}

	var lines []string
	if fields.Owner != "" {
		lines = append(lines, "Owner: "+fields.Owner)
	}
	if fields.Notify != "" {
		lines = append(lines, "Notify: "+fields.Notify)
	}
	if fields.Merge != "" {
		lines = append(lines, "Merge: "+fields.Merge)
	}
	if fields.Molecule != "" {
		lines = append(lines, "Molecule: "+fields.Molecule)
	}
	if fields.BaseBranch != "" {
		lines = append(lines, "base_branch: "+fields.BaseBranch)
	}
	if fields.Watchers != "" {
		lines = append(lines, "Watchers: "+fields.Watchers)
	}
	if fields.NudgeWatchers != "" {
		lines = append(lines, "nudge_watchers: "+fields.NudgeWatchers)
	}
	if fields.CompletionNotifiedAt != "" {
		lines = append(lines, "completion_notified_at: "+fields.CompletionNotifiedAt)
	}

	return strings.Join(lines, "\n")
}

func formatAttachedVars(vars []string) string {
	if len(vars) == 0 {
		return ""
	}
	encoded, err := json.Marshal(vars)
	if err != nil {
		return strings.Join(vars, ", ")
	}
	return string(encoded)
}

func parseAttachedVars(raw string) []string {
	if raw == "" {
		return nil
	}
	var vars []string
	if strings.HasPrefix(raw, "[") {
		if err := json.Unmarshal([]byte(raw), &vars); err == nil {
			return vars
		}
	}
	return []string{raw}
}

func formatFormulaVars(raw string) string {
	return formatAttachedVars(splitFormulaVars(raw))
}

func parseFormulaVars(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "[") {
		if vars := parseAttachedVars(raw); len(vars) > 0 {
			return strings.Join(vars, "\n")
		}
		return ""
	}
	return strings.Join(splitFormulaVars(raw), "\n")
}

func splitFormulaVars(raw string) []string {
	if raw == "" {
		return nil
	}
	vars := strings.Split(raw, "\n")
	out := vars[:0]
	for _, variable := range vars {
		variable = strings.TrimSpace(variable)
		if variable != "" {
			out = append(out, variable)
		}
	}
	return out
}

// SetConvoyFields updates an issue's description with the given convoy fields.
// Existing convoy field lines are replaced; other content is preserved.
// Returns the new description string.
func SetConvoyFields(issue *Issue, fields *ConvoyFields) string {
	if issue == nil {
		return FormatConvoyFields(fields)
	}

	// Known convoy field keys (lowercase)
	convoyKeys := map[string]bool{
		"owner":                  true,
		"notify":                 true,
		"merge":                  true,
		"molecule":               true,
		"base_branch":            true,
		"base-branch":            true,
		"basebranch":             true,
		"watchers":               true,
		"nudge_watchers":         true,
		"nudge-watchers":         true,
		"nudgewatchers":          true,
		"completion_notified_at": true,
		"completion-notified-at": true,
		"completionnotifiedat":   true,
	}

	// Collect non-convoy lines from existing description
	var otherLines []string
	if issue.Description != "" {
		for _, line := range strings.Split(issue.Description, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				otherLines = append(otherLines, line)
				continue
			}

			colonIdx := strings.Index(trimmed, ":")
			if colonIdx == -1 {
				otherLines = append(otherLines, line)
				continue
			}

			key := strings.ToLower(strings.TrimSpace(trimmed[:colonIdx]))
			if !convoyKeys[key] {
				otherLines = append(otherLines, line)
			}
		}
	}

	// Build new description: other content first, then convoy fields
	formatted := FormatConvoyFields(fields)

	// Trim trailing blank lines from other content
	for len(otherLines) > 0 && strings.TrimSpace(otherLines[len(otherLines)-1]) == "" {
		otherLines = otherLines[:len(otherLines)-1]
	}
	// Trim leading blank lines from other content
	for len(otherLines) > 0 && strings.TrimSpace(otherLines[0]) == "" {
		otherLines = otherLines[1:]
	}

	if len(otherLines) == 0 {
		return formatted
	}
	if formatted == "" {
		return strings.Join(otherLines, "\n")
	}

	return strings.Join(otherLines, "\n") + "\n" + formatted
}

// MRFields holds the structured fields for a merge-request issue.
// These fields are stored as key: value lines in the issue description.
type MRFields struct {
	Branch      string // Source branch name (e.g., "polecat/Nux/gt-xyz")
	Target      string // Target branch (e.g., "main" or "integration/gt-epic")
	SourceIssue string // The work item being merged (e.g., "gt-xyz")
	Worker      string // Who did the work
	Rig         string // Which rig
	CommitSHA   string // HEAD commit SHA at submission time (GH#3032: dedup key)
	PRURL       string // Recorded pull request URL, if one exists for this MR
	PRNumber    int    // Recorded pull request number, scoped to the target repo
	MergeCommit string // SHA of merge commit (set on close)
	CloseReason string // Reason for closing: merged, rejected, conflict, superseded
	AgentBead   string // Agent bead ID that created this MR (for traceability)

	// Conflict resolution fields (for priority scoring)
	RetryCount      int    // Number of conflict-resolution cycles
	LastConflictSHA string // SHA of main when conflict occurred
	ConflictTaskID  string // Link to conflict-resolution task (if any)

	// Convoy tracking (for priority scoring - convoy starvation prevention)
	ConvoyID        string // Parent convoy ID if part of a convoy
	ConvoyCreatedAt string // Convoy creation time (ISO 8601) for starvation prevention

	// Pre-verification fields (Phase 3: polecat-owned rebasing)
	// When a polecat rebases onto the target and runs gates before submission,
	// these fields allow the refinery to fast-path merge without re-running gates.
	PreVerified     bool   // Polecat ran full gates after rebasing onto target
	PreVerifiedAt   string // ISO 8601 timestamp when verification completed
	PreVerifiedBase string // Target branch SHA at verification time
}

// ParseMRFields extracts structured merge-request fields from an issue's description.
// Fields are expected as "key: value" lines, with optional prose text mixed in.
// Returns nil if no MR fields are found.
func ParseMRFields(issue *Issue) *MRFields {
	if issue == nil || issue.Description == "" {
		return nil
	}

	fields := &MRFields{}
	hasFields := false

	for _, line := range strings.Split(issue.Description, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Look for "key: value" pattern
		colonIdx := strings.Index(line, ":")
		if colonIdx == -1 {
			continue
		}

		key := strings.TrimSpace(line[:colonIdx])
		value := strings.TrimSpace(line[colonIdx+1:])
		if value == "" || strings.EqualFold(value, "null") {
			continue
		}

		// Map keys to fields (case-insensitive)
		switch strings.ToLower(key) {
		case "branch":
			fields.Branch = value
			hasFields = true
		case "target":
			fields.Target = value
			hasFields = true
		case "source_issue", "source-issue", "sourceissue":
			fields.SourceIssue = value
			hasFields = true
		case "worker":
			fields.Worker = value
			hasFields = true
		case "rig":
			fields.Rig = value
			hasFields = true
		case "commit_sha", "commit-sha", "commitsha":
			fields.CommitSHA = value
			hasFields = true
		case "pr_url", "pr-url", "prurl":
			fields.PRURL = value
			hasFields = true
		case "pr_number", "pr-number", "prnumber":
			if n, err := parseIntField(value); err == nil {
				fields.PRNumber = n
				hasFields = true
			}
		case "merge_commit", "merge-commit", "mergecommit":
			fields.MergeCommit = value
			hasFields = true
		case "close_reason", "close-reason", "closereason":
			fields.CloseReason = value
			hasFields = true
		case "agent_bead", "agent-bead", "agentbead":
			fields.AgentBead = value
			hasFields = true
		case "retry_count", "retry-count", "retrycount":
			if n, err := parseIntField(value); err == nil {
				fields.RetryCount = n
				hasFields = true
			}
		case "last_conflict_sha", "last-conflict-sha", "lastconflictsha":
			fields.LastConflictSHA = value
			hasFields = true
		case "conflict_task_id", "conflict-task-id", "conflicttaskid":
			fields.ConflictTaskID = value
			hasFields = true
		case "convoy_id", "convoy-id", "convoyid", "convoy":
			fields.ConvoyID = value
			hasFields = true
		case "convoy_created_at", "convoy-created-at", "convoycreatedat":
			fields.ConvoyCreatedAt = value
			hasFields = true
		case "pre_verified", "pre-verified", "preverified":
			fields.PreVerified = strings.ToLower(value) == "true"
			hasFields = true
		case "pre_verified_at", "pre-verified-at", "preverifiedat":
			fields.PreVerifiedAt = value
			hasFields = true
		case "pre_verified_base", "pre-verified-base", "preverifiedbase":
			fields.PreVerifiedBase = value
			hasFields = true
		}
	}

	if !hasFields {
		return nil
	}
	return fields
}

// parseIntField parses an integer from a string, returning 0 on error.
func parseIntField(s string) (int, error) {
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}

// FormatMRFields formats MRFields as a string suitable for an issue description.
// Only non-empty fields are included.
func FormatMRFields(fields *MRFields) string {
	if fields == nil {
		return ""
	}

	var lines []string

	if fields.Branch != "" {
		lines = append(lines, "branch: "+fields.Branch)
	}
	if fields.Target != "" {
		lines = append(lines, "target: "+fields.Target)
	}
	if fields.SourceIssue != "" {
		lines = append(lines, "source_issue: "+fields.SourceIssue)
	}
	if fields.Worker != "" {
		lines = append(lines, "worker: "+fields.Worker)
	}
	if fields.Rig != "" {
		lines = append(lines, "rig: "+fields.Rig)
	}
	if fields.CommitSHA != "" {
		lines = append(lines, "commit_sha: "+fields.CommitSHA)
	}
	if fields.PRURL != "" {
		lines = append(lines, "pr_url: "+fields.PRURL)
	}
	if fields.PRNumber > 0 {
		lines = append(lines, fmt.Sprintf("pr_number: %d", fields.PRNumber))
	}
	if fields.MergeCommit != "" {
		lines = append(lines, "merge_commit: "+fields.MergeCommit)
	}
	if fields.CloseReason != "" {
		lines = append(lines, "close_reason: "+fields.CloseReason)
	}
	if fields.AgentBead != "" {
		lines = append(lines, "agent_bead: "+fields.AgentBead)
	}
	if fields.RetryCount > 0 {
		lines = append(lines, fmt.Sprintf("retry_count: %d", fields.RetryCount))
	}
	if fields.LastConflictSHA != "" {
		lines = append(lines, "last_conflict_sha: "+fields.LastConflictSHA)
	}
	if fields.ConflictTaskID != "" {
		lines = append(lines, "conflict_task_id: "+fields.ConflictTaskID)
	}
	if fields.ConvoyID != "" {
		lines = append(lines, "convoy_id: "+fields.ConvoyID)
	}
	if fields.ConvoyCreatedAt != "" {
		lines = append(lines, "convoy_created_at: "+fields.ConvoyCreatedAt)
	}
	if fields.PreVerified {
		lines = append(lines, "pre_verified: true")
	}
	if fields.PreVerifiedAt != "" {
		lines = append(lines, "pre_verified_at: "+fields.PreVerifiedAt)
	}
	if fields.PreVerifiedBase != "" {
		lines = append(lines, "pre_verified_base: "+fields.PreVerifiedBase)
	}

	return strings.Join(lines, "\n")
}

// SetMRFields updates an issue's description with the given MR fields.
// Existing MR field lines are replaced; other content is preserved.
// Returns the new description string.
func SetMRFields(issue *Issue, fields *MRFields) string {
	if issue == nil {
		return FormatMRFields(fields)
	}

	// Known MR field keys (lowercase)
	mrKeys := map[string]bool{
		"branch":            true,
		"target":            true,
		"source_issue":      true,
		"source-issue":      true,
		"sourceissue":       true,
		"worker":            true,
		"rig":               true,
		"commit_sha":        true,
		"commit-sha":        true,
		"commitsha":         true,
		"pr_url":            true,
		"pr-url":            true,
		"prurl":             true,
		"pr_number":         true,
		"pr-number":         true,
		"prnumber":          true,
		"merge_commit":      true,
		"merge-commit":      true,
		"mergecommit":       true,
		"close_reason":      true,
		"close-reason":      true,
		"closereason":       true,
		"agent_bead":        true,
		"agent-bead":        true,
		"agentbead":         true,
		"retry_count":       true,
		"retry-count":       true,
		"retrycount":        true,
		"last_conflict_sha": true,
		"last-conflict-sha": true,
		"lastconflictsha":   true,
		"conflict_task_id":  true,
		"conflict-task-id":  true,
		"conflicttaskid":    true,
		"convoy_id":         true,
		"convoy-id":         true,
		"convoyid":          true,
		"convoy":            true,
		"convoy_created_at": true,
		"convoy-created-at": true,
		"convoycreatedat":   true,
		"pre_verified":      true,
		"pre-verified":      true,
		"preverified":       true,
		"pre_verified_at":   true,
		"pre-verified-at":   true,
		"preverifiedat":     true,
		"pre_verified_base": true,
		"pre-verified-base": true,
		"preverifiedbase":   true,
	}

	// Collect non-MR lines from existing description
	var otherLines []string
	if issue.Description != "" {
		for _, line := range strings.Split(issue.Description, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				// Preserve blank lines in content
				otherLines = append(otherLines, line)
				continue
			}

			// Check if this is an MR field line
			colonIdx := strings.Index(trimmed, ":")
			if colonIdx == -1 {
				otherLines = append(otherLines, line)
				continue
			}

			key := strings.ToLower(strings.TrimSpace(trimmed[:colonIdx]))
			if !mrKeys[key] {
				otherLines = append(otherLines, line)
			}
			// Skip MR field lines - they'll be replaced
		}
	}

	// Build new description: MR fields first, then other content
	formatted := FormatMRFields(fields)

	// Trim trailing blank lines from other content
	for len(otherLines) > 0 && strings.TrimSpace(otherLines[len(otherLines)-1]) == "" {
		otherLines = otherLines[:len(otherLines)-1]
	}
	// Trim leading blank lines from other content
	for len(otherLines) > 0 && strings.TrimSpace(otherLines[0]) == "" {
		otherLines = otherLines[1:]
	}

	if formatted == "" {
		return strings.Join(otherLines, "\n")
	}
	if len(otherLines) == 0 {
		return formatted
	}

	return formatted + "\n\n" + strings.Join(otherLines, "\n")
}

// RoleConfig holds structured lifecycle configuration for role beads.
// These fields are stored as "key: value" lines in the role bead description.
// This enables agents to self-register their lifecycle configuration,
// replacing hardcoded identity string parsing in the daemon.
type RoleConfig struct {
	// SessionPattern defines how to derive tmux session name.
	// Supports placeholders: {rig}, {name}, {role}
	// Examples: "hq-mayor", "hq-deacon", "gt-{rig}-{role}", "gt-{rig}-{name}"
	SessionPattern string

	// WorkDirPattern defines the working directory relative to town root.
	// Supports placeholders: {town}, {rig}, {name}, {role}
	// Examples: "{town}", "{town}/{rig}", "{town}/{rig}/polecats/{name}"
	WorkDirPattern string

	// NeedsPreSync indicates whether workspace needs git sync before starting.
	// True for agents with persistent clones (refinery, crew, polecat).
	NeedsPreSync bool

	// StartCommand is the command to run after creating the session.
	// Default: "exec claude --dangerously-skip-permissions"
	StartCommand string

	// EnvVars are additional environment variables to set in the session.
	// Stored as "key=value" pairs.
	EnvVars map[string]string

	// Health check thresholds - per ZFC, agents control their own stuck detection.
	// These allow the Deacon's patrol config to be agent-defined rather than hardcoded.

	// PingTimeout is how long to wait for a health check response.
	// Format: duration string (e.g., "30s", "1m"). Default: 30s.
	PingTimeout string

	// ConsecutiveFailures is how many failed health checks before force-kill.
	// Default: 3.
	ConsecutiveFailures int

	// KillCooldown is the minimum time between force-kills of the same agent.
	// Format: duration string (e.g., "5m", "10m"). Default: 5m.
	KillCooldown string

	// StuckThreshold is how long a wisp can be in_progress before considered stuck.
	// Format: duration string (e.g., "1h", "30m"). Default: 1h.
	StuckThreshold string

	// WispTTLs maps wisp types to their TTL duration strings.
	// Stored as "wisp_ttl_<type>: <duration>" in the role bead description.
	// Examples: wisp_ttl_patrol: 48h, wisp_ttl_error: 336h, wisp_ttl_gc_report: 24h
	// These override rig config and hardcoded defaults for compaction policy.
	WispTTLs map[string]string
}

// ParseRoleConfig extracts RoleConfig from a role bead's description.
// Fields are expected as "key: value" lines. Returns nil if no config found.
func ParseRoleConfig(description string) *RoleConfig {
	config := &RoleConfig{
		EnvVars:  make(map[string]string),
		WispTTLs: make(map[string]string),
	}
	hasFields := false

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
		if value == "" || value == "null" {
			continue
		}

		switch strings.ToLower(key) {
		case "session_pattern", "session-pattern", "sessionpattern":
			config.SessionPattern = value
			hasFields = true
		case "work_dir_pattern", "work-dir-pattern", "workdirpattern", "workdir_pattern":
			config.WorkDirPattern = value
			hasFields = true
		case "needs_pre_sync", "needs-pre-sync", "needspresync":
			config.NeedsPreSync = strings.ToLower(value) == "true"
			hasFields = true
		case "start_command", "start-command", "startcommand":
			config.StartCommand = value
			hasFields = true
		case "env_var", "env-var", "envvar":
			// Format: "env_var: KEY=VALUE"
			if eqIdx := strings.Index(value, "="); eqIdx != -1 {
				envKey := strings.TrimSpace(value[:eqIdx])
				envVal := strings.TrimSpace(value[eqIdx+1:])
				config.EnvVars[envKey] = envVal
				hasFields = true
			}
		// Health check threshold fields (ZFC: agent-controlled)
		case "ping_timeout", "ping-timeout", "pingtimeout":
			config.PingTimeout = value
			hasFields = true
		case "consecutive_failures", "consecutive-failures", "consecutivefailures":
			if n, err := parseIntField(value); err == nil {
				config.ConsecutiveFailures = n
				hasFields = true
			}
		case "kill_cooldown", "kill-cooldown", "killcooldown":
			config.KillCooldown = value
			hasFields = true
		case "stuck_threshold", "stuck-threshold", "stuckthreshold":
			config.StuckThreshold = value
			hasFields = true
		default:
			// Check for wisp_ttl_* pattern (e.g., wisp_ttl_patrol, wisp-ttl-error)
			lowerKey := strings.ToLower(key)
			if wispType, ok := ParseWispTTLKey(lowerKey); ok {
				config.WispTTLs[wispType] = value
				hasFields = true
			}
		}
	}

	if !hasFields {
		return nil
	}
	return config
}

// ParseWispTTLKey checks if a lowercase key matches the wisp_ttl_* pattern
// and returns the wisp type suffix. Supports underscore, hyphen, and camelCase variants.
// Examples: "wisp_ttl_patrol" → "patrol", "wisp-ttl-gc_report" → "gc_report"
func ParseWispTTLKey(key string) (string, bool) {
	for _, prefix := range []string{"wisp_ttl_", "wisp-ttl-", "wispttl"} {
		if strings.HasPrefix(key, prefix) {
			wispType := key[len(prefix):]
			if wispType != "" {
				return wispType, true
			}
		}
	}
	return "", false
}

// FormatRoleConfig formats RoleConfig as a string suitable for a role bead description.
// Only non-empty/non-default fields are included.
func FormatRoleConfig(config *RoleConfig) string {
	if config == nil {
		return ""
	}

	var lines []string

	if config.SessionPattern != "" {
		lines = append(lines, "session_pattern: "+config.SessionPattern)
	}
	if config.WorkDirPattern != "" {
		lines = append(lines, "work_dir_pattern: "+config.WorkDirPattern)
	}
	if config.NeedsPreSync {
		lines = append(lines, "needs_pre_sync: true")
	}
	if config.StartCommand != "" {
		lines = append(lines, "start_command: "+config.StartCommand)
	}
	for k, v := range config.EnvVars {
		lines = append(lines, "env_var: "+k+"="+v)
	}
	// Sort wisp TTL keys for deterministic output
	wispTypes := make([]string, 0, len(config.WispTTLs))
	for k := range config.WispTTLs {
		wispTypes = append(wispTypes, k)
	}
	sort.Strings(wispTypes)
	for _, wt := range wispTypes {
		lines = append(lines, "wisp_ttl_"+wt+": "+config.WispTTLs[wt])
	}

	return strings.Join(lines, "\n")
}

// ExpandRolePattern expands placeholders in a pattern string.
// Supported placeholders: {town}, {rig}, {name}, {role}, {prefix}
func ExpandRolePattern(pattern, townRoot, rig, name, role, prefix string) string {
	result := pattern
	result = strings.ReplaceAll(result, "{town}", townRoot)
	result = strings.ReplaceAll(result, "{rig}", rig)
	result = strings.ReplaceAll(result, "{name}", name)
	result = strings.ReplaceAll(result, "{role}", role)
	result = strings.ReplaceAll(result, "{prefix}", prefix)
	return result
}
