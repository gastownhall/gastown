package github

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// RepoFromRemoteURL returns the owner/repo ref for a GitHub remote URL.
func RepoFromRemoteURL(remoteURL string) (string, error) {
	owner, repo, err := ParseRemoteURL(remoteURL)
	if err != nil {
		return "", err
	}
	return owner + "/" + repo, nil
}

// ParseRemoteURL extracts owner and repo from a strict GitHub remote URL.
func ParseRemoteURL(remoteURL string) (owner, repo string, err error) {
	raw := strings.TrimSpace(remoteURL)
	if raw == "" {
		return "", "", errors.New("github: empty remote URL")
	}
	if strings.EqualFold(raw, "DISABLED") {
		return "", "", errors.New("github: disabled remote URL")
	}
	if strings.ContainsAny(raw, "\x00\n\r\t") {
		return "", "", errors.New("github: invalid remote URL")
	}

	if strings.HasPrefix(raw, "git@github.com:") {
		return parseRepoPath(strings.TrimPrefix(raw, "git@github.com:"))
	}

	u, parseErr := url.Parse(raw)
	if parseErr != nil || u.Scheme == "" {
		return "", "", errors.New("github: unsupported remote URL")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", "", errors.New("github: remote URL must not include query or fragment")
	}
	if !isGitHubRemoteScheme(u.Scheme) || !strings.EqualFold(u.Hostname(), "github.com") {
		return "", "", errors.New("github: unsupported remote URL")
	}

	return parseRepoPath(strings.TrimPrefix(u.Path, "/"))
}

func isGitHubRemoteScheme(scheme string) bool {
	switch strings.ToLower(scheme) {
	case "http", "https", "ssh", "git":
		return true
	default:
		return false
	}
}

func parseRepoPath(path string) (owner, repo string, err error) {
	path = strings.TrimSuffix(path, "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		return "", "", errors.New("github: remote URL must contain exactly owner/repo")
	}

	owner = parts[0]
	repo = strings.TrimSuffix(parts[1], ".git")
	if !validGitHubRepoSegment(owner) || !validGitHubRepoSegment(repo) {
		return "", "", fmt.Errorf("github: invalid owner/repo")
	}
	return owner, repo, nil
}

func validGitHubRepoSegment(segment string) bool {
	if segment == "" || strings.HasPrefix(segment, "-") {
		return false
	}
	for _, r := range segment {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}
