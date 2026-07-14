package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// MergeRequest is the subset of GitLab's merge-request payload the refinery uses.
type MergeRequest struct {
	IID             int    `json:"iid"`
	State           string `json:"state"`
	SourceBranch    string `json:"source_branch"`
	TargetBranch    string `json:"target_branch"`
	MergeStatus     string `json:"merge_status"`
	MergeCommitSHA  string `json:"merge_commit_sha"`
	SquashCommitSHA string `json:"squash_commit_sha"`
	WebURL          string `json:"web_url"`
}

// MergedSHA returns the commit the MR landed on the target branch, preferring
// the squash commit when the merge was squashed.
func (m *MergeRequest) MergedSHA() string {
	if m.SquashCommitSHA != "" {
		return m.SquashCommitSHA
	}
	return m.MergeCommitSHA
}

// MergeOptions controls how MergeMR lands a merge request.
type MergeOptions struct {
	Squash              bool
	SquashCommitMessage string
	RemoveSourceBranch  bool
}

// FindOpenMR returns the single open merge request from sourceBranch into
// targetBranch on the project. It is an error for zero or more than one to
// match, so the caller never merges an ambiguous request.
func (c *Client) FindOpenMR(ctx context.Context, projectPath, sourceBranch, targetBranch string) (*MergeRequest, error) {
	path := fmt.Sprintf("/projects/%s/merge_requests?state=opened&source_branch=%s&target_branch=%s",
		encodeProjectPath(projectPath), url.QueryEscape(sourceBranch), url.QueryEscape(targetBranch))
	var mrs []MergeRequest
	if err := c.restRequest(ctx, http.MethodGet, path, nil, &mrs); err != nil {
		return nil, err
	}
	switch len(mrs) {
	case 0:
		return nil, fmt.Errorf("gitlab: no open MR for %s -> %s in %s", sourceBranch, targetBranch, projectPath)
	case 1:
		return &mrs[0], nil
	default:
		return nil, fmt.Errorf("gitlab: %d open MRs for %s -> %s in %s (ambiguous)", len(mrs), sourceBranch, targetBranch, projectPath)
	}
}

// MergeMR merges the given merge request server-side via the GitLab API. The
// token must carry the Maintainer role on the project. GitLab enforces the
// project's own gates (approvals, unresolved discussions, pipeline status);
// those surface here as an *APIError (e.g. 405/422) rather than a silent no-op.
func (c *Client) MergeMR(ctx context.Context, projectPath string, iid int, opts MergeOptions) (*MergeRequest, error) {
	body := map[string]any{
		"squash":                      opts.Squash,
		"should_remove_source_branch": opts.RemoveSourceBranch,
	}
	if msg := strings.TrimSpace(opts.SquashCommitMessage); msg != "" {
		body["squash_commit_message"] = msg
	}
	path := fmt.Sprintf("/projects/%s/merge_requests/%d/merge", encodeProjectPath(projectPath), iid)
	var mr MergeRequest
	if err := c.restRequest(ctx, http.MethodPut, path, body, &mr); err != nil {
		return nil, err
	}
	return &mr, nil
}

// encodeProjectPath URL-encodes a namespaced project path
// ("group/sub/project") into the form GitLab expects in a path segment
// ("group%2Fsub%2Fproject").
func encodeProjectPath(p string) string {
	segments := strings.Split(p, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	return strings.Join(segments, "%2F")
}
