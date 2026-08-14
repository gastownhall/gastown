package beads

import "strings"

// ResolveMergeStrategy returns the merge strategy dispatch must honor.
// An explicit CLI --merge value wins. Otherwise a stored merge_strategy on the
// issue is reused. Otherwise local-commit-only issue text becomes "local".
// Empty means the default merge-request path.
func ResolveMergeStrategy(cliMerge, storedMerge, issueText string) string {
	if s := strings.TrimSpace(cliMerge); s != "" {
		return s
	}
	if s := strings.TrimSpace(storedMerge); s != "" {
		return s
	}
	if IssueTextImpliesLocalMerge(issueText) {
		return "local"
	}
	return ""
}

// IssueTextImpliesLocalMerge reports whether issue title or description states
// that the branch must stay local. This is a protocol safety rule for the
// production phrases that must not reach a shared remote.
func IssueTextImpliesLocalMerge(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "local commit only") ||
		strings.Contains(lower, "do not push") ||
		strings.Contains(lower, "don't push")
}

// IsLocalMergeStrategy reports whether a stored merge strategy value is local.
func IsLocalMergeStrategy(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), "local")
}

// HasLocalMergeStrategy reports whether attachment fields carry merge_strategy=local.
func HasLocalMergeStrategy(fields *AttachmentFields) bool {
	return fields != nil && IsLocalMergeStrategy(fields.MergeStrategy)
}
