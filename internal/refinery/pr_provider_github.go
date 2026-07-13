package refinery

import (
	"errors"

	"github.com/steveyegge/gastown/internal/git"
)

// githubPRProvider implements PRProvider using the gh CLI via git.Git.
type githubPRProvider struct {
	git *git.Git
}

func newGitHubPRProvider(g *git.Git) PRProvider {
	return &githubPRProvider{git: g}
}

func (p *githubPRProvider) FindPullRequest(branch, prURL string, prNumber int, headSHA string) (*git.PullRequestInfo, error) {
	pr, err := p.git.LookupPullRequest(git.PullRequestRef{URL: prURL, Number: prNumber, Branch: branch, HeadSHA: headSHA})
	if err != nil {
		if errors.Is(err, git.ErrPullRequestNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if !pr.Open() {
		return nil, nil
	}
	return pr, nil
}

func (p *githubPRProvider) IsPRApproved(pr *git.PullRequestInfo) (bool, error) {
	return p.git.IsPullRequestApproved(pr)
}

func (p *githubPRProvider) MergePR(pr *git.PullRequestInfo, method string) (string, error) {
	return p.git.GhPrMergePullRequest(pr, method)
}
