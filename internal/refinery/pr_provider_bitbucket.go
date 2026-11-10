package refinery

import (
	"fmt"

	"github.com/steveyegge/gastown/internal/bitbucket"
	"github.com/steveyegge/gastown/internal/git"
)

// bitbucketPRProvider implements PRProvider using the Bitbucket Cloud REST API.
type bitbucketPRProvider struct {
	git       *git.Git
	workspace string
	repoSlug  string
}

func newBitbucketPRProvider(g *git.Git) (PRProvider, error) {
	remoteURL, err := g.RemoteURL("origin")
	if err != nil {
		return nil, fmt.Errorf("bitbucket provider: failed to get origin remote URL: %w", err)
	}
	workspace, repoSlug, err := bitbucket.ParseBitbucketRemote(remoteURL)
	if err != nil {
		return nil, fmt.Errorf("bitbucket provider: %w", err)
	}
	return &bitbucketPRProvider{
		git:       g,
		workspace: workspace,
		repoSlug:  repoSlug,
	}, nil
}

func (p *bitbucketPRProvider) FindPullRequest(branch, _ string, _ int, headSHA string) (*git.PullRequestInfo, error) {
	return p.git.FindBitbucketPullRequest(p.workspace, p.repoSlug, branch, headSHA)
}

func (p *bitbucketPRProvider) IsPRApproved(pr *git.PullRequestInfo) (bool, error) {
	return p.git.IsBitbucketPRApproved(p.workspace, p.repoSlug, pr.Number)
}

func (p *bitbucketPRProvider) MergePR(pr *git.PullRequestInfo, method string) (string, error) {
	prNumber := 0
	if pr != nil {
		prNumber = pr.Number
	}
	return "", fmt.Errorf("bitbucket PR merge is disabled until the provider supports submitted-head conditional merge for PR #%d", prNumber)
}

// ChecksPassed is not supported for Bitbucket yet: the provider has no
// build-status client. Returning an error makes gated merges fail closed.
func (p *bitbucketPRProvider) ChecksPassed(pr *git.PullRequestInfo) (bool, bool, error) {
	prNumber := 0
	if pr != nil {
		prNumber = pr.Number
	}
	return false, false, fmt.Errorf("bitbucket CI status checks are not supported (PR #%d); gated merges need vcs_provider=github", prNumber)
}
