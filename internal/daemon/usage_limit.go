package daemon

import "regexp"

// claudeUsageLimitPatterns are signatures indicating that Claude has hit a
// usage / rate limit, as opposed to a true crash. Matched (case-insensitive)
// against either an agent's stderr or the tail of its tmux pane.
//
// Patterns are intentionally specific to actual Claude rate-limit messages.
// Detection is conservative — false positives (treating a real crash as a
// rate limit) are worse than false negatives, since a crash misclassified as
// a rate limit will still recover via the fixed paused-retry delay; a rate
// limit misclassified as a crash falls through to existing crash-loop
// handling, which is the pre-fix status quo.
var claudeUsageLimitPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)Claude (AI )?usage limit reached`),
	regexp.MustCompile(`(?i)You've hit your .*limit`),
	regexp.MustCompile(`(?i)limit\s*·\s*resets \d`),
	regexp.MustCompile(`(?i)Stop and wait for limit to reset`),
	regexp.MustCompile(`(?i)API Error:\s*Rate limit reached`),
	regexp.MustCompile(`(?i)\b429\b.*Too Many Requests`),
	regexp.MustCompile(`(?i)"type"\s*:\s*"rate_limit_error"`),
}

// IsClaudeUsageLimit reports whether the given output contains a Claude
// usage / rate-limit signature. Output may be stderr from a recently exited
// Claude process or the tail of its tmux pane.
//
// This is the central detector — keep all classification logic here so it
// can be unit-tested with synthetic fixtures.
func IsClaudeUsageLimit(output string) bool {
	if output == "" {
		return false
	}
	for _, re := range claudeUsageLimitPatterns {
		if re.MatchString(output) {
			return true
		}
	}
	return false
}
