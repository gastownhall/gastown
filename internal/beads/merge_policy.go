package beads

import "strings"

// ResolveMergeStrategy returns the merge strategy dispatch must honor.
// An explicit CLI --merge value wins. Otherwise a stored merge_strategy on the
// issue is reused. Otherwise issue prose that states local-commit-only intent
// becomes "local". Empty means the default MR path.
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
// production phrases in gastownhall/gastown#4512, not agent judgment.
func IssueTextImpliesLocalMerge(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "local commit only") ||
		strings.Contains(lower, "do not push") ||
		strings.Contains(lower, "don't push")
}

// HasLocalMergeStrategy reports whether attachment fields carry merge_strategy=local.
func HasLocalMergeStrategy(fields *AttachmentFields) bool {
	return fields != nil && strings.EqualFold(strings.TrimSpace(fields.MergeStrategy), "local")
}
