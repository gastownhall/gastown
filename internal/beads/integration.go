package beads

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/jonbaldie/gastown/internal/util"
)

// Integration branch template constants
const DefaultIntegrationBranchTemplate = "integration/{title}"

// LegacyIntegrationBranchTemplate is the old default template used before {title}
// was introduced. Detection falls back to this when the {title} template doesn't
// match a real branch, ensuring backward compatibility with branches created by
// the old code (which hardcoded "integration/" + epicID).
const LegacyIntegrationBranchTemplate = "integration/{epic}"

// sanitizeBranchRegex matches any character that isn't alphanumeric or a hyphen.
var sanitizeBranchRegex = regexp.MustCompile(`[^a-z0-9-]+`)

// collapseHyphensRegex matches consecutive hyphens.
var collapseHyphensRegex = regexp.MustCompile(`-{2,}`)

// SanitizeBranchSegment converts a human-readable string into a git-safe branch
// name segment. Rules: lowercase, non-alphanumeric → hyphen, collapse consecutive
// hyphens, trim leading/trailing hyphens, truncate to 60 chars.
func SanitizeBranchSegment(s string) string {
	s = strings.ToLower(s)
	s = sanitizeBranchRegex.ReplaceAllString(s, "-")
	s = collapseHyphensRegex.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 60 {
		s = s[:60]
		s = strings.TrimRight(s, "-")
	}
	return s
}

// IssueShower provides issue lookup without requiring a full Beads instance.
// *Beads satisfies this interface, so existing callers need no changes.
type IssueShower interface {
	Show(id string) (*Issue, error)
}

// BranchChecker provides branch existence checks without importing the git package.
// This avoids circular imports between beads and git.
type BranchChecker interface {
	BranchExists(name string) (bool, error)
	RemoteBranchExists(remote, name string) (bool, error)
}

// GetIntegrationBranchField extracts the integration_branch field from an epic's description.
// Returns empty string if the field is not found.
func GetIntegrationBranchField(description string) string {
	return getMetadataField(description, "integration_branch")
}

// GetBaseBranchField extracts the base_branch field from an epic's description.
// Returns empty string if the field is not found.
func GetBaseBranchField(description string) string {
	return getMetadataField(description, "base_branch")
}

// getMetadataField extracts a key: value field from a description string.
// The key match is case-insensitive.
func getMetadataField(description, key string) string {
	if description == "" {
		return ""
	}

	lowerKey := strings.ToLower(key) + ":"

	lines := strings.Split(description, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(trimmed), lowerKey) {
			parts := strings.SplitN(trimmed, ":", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return ""
}

// AddIntegrationBranchField adds or updates the integration_branch field in a description.
func AddIntegrationBranchField(description, branchName string) string {
	return addMetadataField(description, "integration_branch", branchName)
}

// AddBaseBranchField adds or updates the base_branch field in a description.
func AddBaseBranchField(description, baseBranch string) string {
	return addMetadataField(description, "base_branch", baseBranch)
}

// addMetadataField adds or updates a key: value field in a description.
func addMetadataField(description, key, value string) string {
	fieldLine := key + ": " + value

	if description == "" {
		return fieldLine
	}

	lowerKey := strings.ToLower(key) + ":"

	lines := strings.Split(description, "\n")
	var newLines []string
	found := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(trimmed), lowerKey) {
			newLines = append(newLines, fieldLine)
			found = true
		} else {
			newLines = append(newLines, line)
		}
	}

	if !found {
		newLines = append([]string{fieldLine}, newLines...)
	}

	return strings.Join(newLines, "\n")
}

// BuildIntegrationBranchName expands an integration branch template with variables.
// Variables supported:
//   - {epic}: Full epic ID (e.g., "RA-123")
//   - {title}: Sanitized epic title (e.g., "add-user-authentication")
//   - {prefix}: Epic prefix before first hyphen (e.g., "RA")
//   - {user}: Git user.name (e.g., "klauern")
//
// If template is empty, uses DefaultIntegrationBranchTemplate.
func BuildIntegrationBranchName(template, epicID, epicTitle string) string {
	if template == "" {
		template = DefaultIntegrationBranchTemplate
	}

	// If the sanitized title is empty (no title, or title was all special chars),
	// fall back to the epic ID to avoid producing invalid branch names like "integration/".
	sanitizedTitle := SanitizeBranchSegment(epicTitle)
	if sanitizedTitle == "" {
		sanitizedTitle = epicID
	}

	result := template
	result = strings.ReplaceAll(result, "{epic}", epicID)
	result = strings.ReplaceAll(result, "{title}", sanitizedTitle)
	result = strings.ReplaceAll(result, "{prefix}", ExtractEpicPrefix(epicID))

	if user := getGitUserName(); user != "" {
		result = strings.ReplaceAll(result, "{user}", user)
	}

	return result
}

// ExtractEpicPrefix extracts the prefix from an epic ID (before the first hyphen).
// Examples: "RA-123" -> "RA", "PROJ-456" -> "PROJ", "abc" -> "abc"
func ExtractEpicPrefix(epicID string) string {
	if idx := strings.Index(epicID, "-"); idx > 0 {
		return epicID[:idx]
	}
	return epicID
}

// getGitUserName returns the git user.name config value, or empty if not set.
func getGitUserName() string {
	cmd := exec.Command("git", "config", "user.name")
	util.SetDetachedProcessGroup(cmd)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// DetectIntegrationBranch checks if an issue is a descendant of an epic that has an integration branch.
// Traverses up the parent chain until it finds an epic with an integration branch or runs out of parents.
// At each epic: reads integration_branch: metadata first, falls back to BuildIntegrationBranchName.
// Checks branch existence via BranchChecker.
// Returns the integration branch name or "" if not found.
func DetectIntegrationBranch(bd IssueShower, checker BranchChecker, issueID string) (string, error) {
	const maxDepth = 10
	currentID := issueID

	for depth := 0; depth < maxDepth; depth++ {
		issue, err := bd.Show(currentID)
		if err != nil {
			return "", fmt.Errorf("looking up issue %s: %w", currentID, err)
		}

		if issue.Type == "epic" {
			// First try explicit metadata
			integrationBranch := GetIntegrationBranchField(issue.Description)
			if integrationBranch == "" {
				// Fall back to default naming convention
				integrationBranch = BuildIntegrationBranchName("", issue.ID, issue.Title)
			}

			// Check remote first (authoritative -- local refs can be stale
			// if the remote branch was deleted without pruning)
			exists, err := checker.RemoteBranchExists("origin", integrationBranch)
			if err != nil {
				// Remote check failed (network issue) -- fall back to local.
				// Swallow local errors: detection is best-effort.
				localExists, _ := checker.BranchExists(integrationBranch)
				if localExists {
					return integrationBranch, nil
				}
			} else if exists {
				return integrationBranch, nil
			}

			// If the default {title} template didn't match, try the legacy
			// {epic} template. Branches created by the old code used hardcoded
			// "integration/" + epicID, which won't match the new {title} default.
			if GetIntegrationBranchField(issue.Description) == "" {
				legacyBranch := BuildIntegrationBranchName(LegacyIntegrationBranchTemplate, issue.ID, issue.Title)
				if legacyBranch != integrationBranch {
					exists, err := checker.RemoteBranchExists("origin", legacyBranch)
					if err != nil {
						localExists, _ := checker.BranchExists(legacyBranch)
						if localExists {
							return legacyBranch, nil
						}
					} else if exists {
						return legacyBranch, nil
					}
				}
			}
			// Epic found but no integration branch - continue checking parents
		}

		if issue.Parent == "" {
			return "", nil
		}
		currentID = issue.Parent
	}

	return "", nil
}
