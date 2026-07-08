package polecat

import (
	"fmt"
	"strings"
)

const (
	polecatBranchPrefix           = "polecat/"
	generatedIssueBranchSeparator = "+"
	legacyIssueBranchSeparator    = "@"

	// BranchDelimiterConfigKey is the rig config key that selects which
	// delimiter FormatGeneratedBranchName places between the issue ID and the
	// generated suffix. Some CI systems constrain the branch-name charset
	// (e.g. docker-compose project names allow only [a-z0-9_-]), so rigs whose
	// CI rejects "+" can pick "_" instead.
	BranchDelimiterConfigKey = "polecat_branch_delimiter"
)

// issueBranchSeparators lists every delimiter ParseBranchName recognizes
// between the issue ID and the generated suffix. Parsing accepts all of them
// regardless of the rig's configured delimiter, so branches created under a
// previous delimiter configuration keep resolving to the right issue. None of
// these characters can appear in issue IDs ([a-z0-9-] plus "." for subtasks)
// or polecat names, so the split is unambiguous.
var issueBranchSeparators = []string{
	generatedIssueBranchSeparator,
	legacyIssueBranchSeparator,
	"_",
}

// ValidBranchDelimiter reports whether s may be used as the configured
// branch delimiter. Only delimiters the parser recognizes are valid;
// anything else would produce branches that no longer round-trip to an
// issue ID.
func ValidBranchDelimiter(s string) bool {
	for _, sep := range issueBranchSeparators {
		if s == sep {
			return true
		}
	}
	return false
}

// BranchNameMeta is the structured identity encoded in a polecat branch name.
type BranchNameMeta struct {
	Polecat   string
	Issue     string
	Generated bool
}

// FormatGeneratedBranchName returns the canonical generated polecat branch
// using the default "+" delimiter.
func FormatGeneratedBranchName(polecatName, issue, suffix string) string {
	return FormatGeneratedBranchNameWithDelimiter(polecatName, issue, suffix, generatedIssueBranchSeparator)
}

// FormatGeneratedBranchNameWithDelimiter returns the generated polecat branch
// with the given issue/suffix delimiter. Invalid delimiters fall back to the
// default "+" so a bad config value can never produce an unparseable branch.
func FormatGeneratedBranchNameWithDelimiter(polecatName, issue, suffix, delimiter string) string {
	if !ValidBranchDelimiter(delimiter) {
		delimiter = generatedIssueBranchSeparator
	}
	if issue != "" {
		return fmt.Sprintf("%s%s/%s%s%s", polecatBranchPrefix, polecatName, issue, delimiter, suffix)
	}
	return fmt.Sprintf("%s%s-%s", polecatBranchPrefix, polecatName, suffix)
}

// ParseBranchName decodes polecat branch names without guessing at dashed issue IDs.
func ParseBranchName(branch string) (BranchNameMeta, bool) {
	if !strings.HasPrefix(branch, polecatBranchPrefix) {
		return BranchNameMeta{}, false
	}

	rest := branch[len(polecatBranchPrefix):]
	if rest == "" {
		return BranchNameMeta{}, false
	}

	if slash := strings.Index(rest, "/"); slash >= 0 {
		if slash == 0 {
			return BranchNameMeta{}, false
		}
		polecatName := rest[:slash]
		issueTail := rest[slash+1:]
		if issueTail == "" || strings.Contains(issueTail, "/") {
			return BranchNameMeta{}, false
		}
		issue, generated, ok := parseIssueTail(issueTail)
		if !ok {
			return BranchNameMeta{}, false
		}
		return BranchNameMeta{Polecat: polecatName, Issue: issue, Generated: generated}, true
	}

	dash := strings.LastIndex(rest, "-")
	if dash <= 0 || dash == len(rest)-1 {
		return BranchNameMeta{}, false
	}
	return BranchNameMeta{Polecat: rest[:dash], Generated: true}, true
}

// ParseGeneratedBranchName decodes only branch names emitted by
// FormatGeneratedBranchName / FormatGeneratedBranchNameWithDelimiter and the
// legacy @ issue-suffix form kept for in-flight branches.
func ParseGeneratedBranchName(branch string) (BranchNameMeta, bool) {
	meta, ok := ParseBranchName(branch)
	if !ok || !meta.Generated {
		return BranchNameMeta{}, false
	}
	return meta, true
}

func parseIssueTail(issueTail string) (issue string, generated bool, ok bool) {
	delim := -1
	for _, sep := range issueBranchSeparators {
		if idx := strings.Index(issueTail, sep); idx >= 0 && (delim == -1 || idx < delim) {
			delim = idx
		}
	}
	if delim >= 0 {
		if delim == 0 || delim == len(issueTail)-1 {
			return "", false, false
		}
		return issueTail[:delim], true, true
	}
	return issueTail, false, true
}
