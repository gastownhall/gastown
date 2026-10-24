package refinery

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

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

// ChecksPassed reads the PR's CI state with `gh pr checks <pr> --json state,name`.
func (p *githubPRProvider) ChecksPassed(pr *git.PullRequestInfo) (bool, bool, error) {
	if pr == nil || (pr.Number == 0 && pr.URL == "") {
		return false, false, fmt.Errorf("pull request identity is missing")
	}
	sel := pr.URL
	if sel == "" {
		sel = strconv.Itoa(pr.Number)
	}
	args := []string{"pr", "checks", sel, "--json", "state,name"}
	if pr.BaseRepo != "" {
		args = append(args, "--repo", pr.BaseRepo)
	}
	cmd := exec.Command("gh", args...)
	cmd.Dir = p.git.WorkDir()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	return classifyGHChecks(stdout.Bytes(), stderr.Bytes(), runErr)
}

// classifyGHChecks interprets `gh pr checks --json state,name` output. gh
// exits non-zero when checks fail (1) or are pending (8) while still printing
// the JSON, so the JSON wins whenever it parses. Passed means every check is
// SUCCESS, NEUTRAL or SKIPPED; pending means any is PENDING, QUEUED or
// IN_PROGRESS (and no check definitely failed). Zero checks is pending.
func classifyGHChecks(stdout, stderr []byte, runErr error) (passed, pending bool, err error) {
	out := bytes.TrimSpace(stdout)
	if len(out) == 0 {
		if runErr != nil && strings.Contains(strings.ToLower(string(stderr)), "no checks reported") {
			return false, true, nil
		}
		if runErr != nil {
			return false, false, fmt.Errorf("gh pr checks: %s: %w", strings.TrimSpace(string(stderr)), runErr)
		}
		return false, true, nil
	}
	var checks []struct {
		Name  string `json:"name"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(out, &checks); err != nil {
		return false, false, fmt.Errorf("parsing gh pr checks output: %w", err)
	}
	if len(checks) == 0 {
		return false, true, nil
	}
	var failed []string
	for _, c := range checks {
		switch strings.ToUpper(strings.TrimSpace(c.State)) {
		case "SUCCESS", "NEUTRAL", "SKIPPED":
		case "PENDING", "QUEUED", "IN_PROGRESS":
			pending = true
		default:
			name := c.Name
			if name == "" {
				name = "(unnamed)"
			}
			failed = append(failed, name)
		}
	}
	if len(failed) > 0 {
		return false, false, &ChecksFailedError{Names: failed}
	}
	return !pending, pending, nil
}
