// Package gitlab provides a minimal GitLab API client for the Gas Town merge
// queue. It covers the operations the refinery needs to land work on a GitLab
// project whose default branch is protected (push disallowed, merge restricted
// to Maintainer): looking up the open merge request for a branch and merging it
// server-side via the API.
//
// Authentication uses a GITLAB_TOKEN environment variable holding a token with
// the Maintainer role on the target project (a Project/Group Access Token with
// the `api` scope, or a bot user added as Maintainer).
package gitlab

import (
	"fmt"
	"net/url"
	"strings"
)

// IsGitLabRemote reports whether a git remote URL points at a GitLab host. It
// recognizes gitlab.com and self-managed instances whose host contains
// "gitlab" (e.g. gitlab.example.com).
func IsGitLabRemote(remoteURL string) bool {
	host, _, err := ParseRemote(remoteURL)
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(host), "gitlab")
}

// ParseRemote extracts the host and namespaced project path
// ("group/subgroup/project") from an https, ssh, or scp-style git remote URL,
// trimming any trailing ".git".
func ParseRemote(remoteURL string) (host, projectPath string, err error) {
	remoteURL = strings.TrimSpace(remoteURL)
	switch {
	case strings.HasPrefix(remoteURL, "http://"),
		strings.HasPrefix(remoteURL, "https://"),
		strings.HasPrefix(remoteURL, "ssh://"):
		u, perr := url.Parse(remoteURL)
		if perr != nil {
			return "", "", fmt.Errorf("gitlab: parse remote %q: %w", remoteURL, perr)
		}
		host = u.Hostname()
		projectPath = strings.Trim(u.Path, "/")
	default:
		// scp-like: [user@]host:group/project(.git)
		rest := remoteURL
		if at := strings.LastIndex(rest, "@"); at >= 0 {
			rest = rest[at+1:]
		}
		colon := strings.Index(rest, ":")
		if colon < 0 {
			return "", "", fmt.Errorf("gitlab: unrecognized remote URL %q", remoteURL)
		}
		host = rest[:colon]
		projectPath = strings.Trim(rest[colon+1:], "/")
	}

	projectPath = strings.TrimSuffix(projectPath, ".git")
	if host == "" || projectPath == "" {
		return "", "", fmt.Errorf("gitlab: could not extract host/project from %q", remoteURL)
	}
	return host, projectPath, nil
}

// APIBaseFromHost returns the GitLab REST API v4 base URL for a host.
func APIBaseFromHost(host string) string {
	return "https://" + host + "/api/v4"
}
