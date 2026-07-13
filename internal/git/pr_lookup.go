package git

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

var (
	// ErrPullRequestNotFound means an authoritative lookup completed but found no PR.
	ErrPullRequestNotFound = errors.New("pull request not found")
	// ErrPullRequestAmbiguous means a branch lookup matched multiple PRs and must be disambiguated.
	ErrPullRequestAmbiguous = errors.New("pull request lookup ambiguous")
)

// PullRequestRef identifies a GitHub pull request. URL or Number is authoritative;
// Branch is only used as a target-repo-scoped fallback with ambiguity checks.
type PullRequestRef struct {
	URL        string
	Number     int
	Branch     string
	HeadOwner  string
	HeadSHA    string
	TargetRepo string // owner/repo; defaults to upstream if configured, else origin
}

// PullRequestInfo is the normalized PR state used by merge queue guards.
type PullRequestInfo struct {
	Number       int    `json:"number"`
	URL          string `json:"url"`
	State        string `json:"state"`
	MergedAt     string `json:"merged_at,omitempty"`
	HeadRefName  string `json:"head_ref_name,omitempty"`
	HeadOwner    string `json:"head_owner,omitempty"`
	HeadRepo     string `json:"head_repo,omitempty"`
	HeadSHA      string `json:"head_sha,omitempty"`
	BaseRepo     string `json:"base_repo,omitempty"`
	LookupSource string `json:"lookup_source,omitempty"`
}

// Open reports whether the PR is currently open.
func (p *PullRequestInfo) Open() bool {
	return p != nil && strings.EqualFold(p.State, "OPEN")
}

// Merged reports whether the PR has been merged.
func (p *PullRequestInfo) Merged() bool {
	return p != nil && strings.EqualFold(p.State, "MERGED")
}

// LookupPullRequest resolves a PR using one authoritative path: recorded URL or
// number first, otherwise an unambiguous target-repo head lookup.
func (g *Git) LookupPullRequest(ref PullRequestRef) (*PullRequestInfo, error) {
	targetRepo, err := g.pullRequestTargetRepo(ref.TargetRepo)
	if err != nil {
		return nil, err
	}

	if strings.TrimSpace(ref.URL) != "" {
		pr, err := g.viewPullRequest(strings.TrimSpace(ref.URL), targetRepo)
		if err != nil {
			return nil, err
		}
		pr.LookupSource = "recorded-url"
		return pr, nil
	}
	if ref.Number > 0 {
		pr, err := g.viewPullRequest(strconv.Itoa(ref.Number), targetRepo)
		if err != nil {
			return nil, err
		}
		pr.LookupSource = "recorded-number"
		return pr, nil
	}

	branch := strings.TrimSpace(ref.Branch)
	if branch == "" {
		return nil, fmt.Errorf("%w: no recorded PR URL/number or branch", ErrPullRequestNotFound)
	}
	if strings.TrimSpace(ref.HeadOwner) != "" {
		return g.lookupPullRequestByQualifiedHead(targetRepo, strings.TrimSpace(ref.HeadOwner), branch, strings.TrimSpace(ref.HeadSHA))
	}
	return g.lookupPullRequestByHead(targetRepo, branch, strings.TrimSpace(ref.HeadSHA))
}

// HasOpenPullRequest checks whether the ref resolves to an open PR. Errors and
// ambiguity are treated as protected so callers do not delete a branch blindly.
func (g *Git) HasOpenPullRequest(ref PullRequestRef) bool {
	pr, err := g.LookupPullRequest(ref)
	if err != nil {
		return !errors.Is(err, ErrPullRequestNotFound)
	}
	return pr.Open()
}

func (g *Git) pullRequestTargetRepo(explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit), nil
	}
	if upstreamURL, err := g.GetUpstreamURL(); err == nil && upstreamURL != "" {
		if repo, parseErr := githubRepoFromRemoteURL(upstreamURL); parseErr == nil {
			return repo, nil
		}
	}
	originURL, err := g.RemoteURL("origin")
	if err != nil {
		return "", fmt.Errorf("resolve target repo from origin remote: %w", err)
	}
	repo, err := githubRepoFromRemoteURL(originURL)
	if err != nil {
		return "", err
	}
	return repo, nil
}

func githubRepoFromRemoteURL(raw string) (string, error) {
	normalized := normalizeGitRemoteURL(raw)
	path, ok := strings.CutPrefix(normalized, "github.com/")
	if !ok {
		return "", fmt.Errorf("remote is not a GitHub repo: %q", strings.TrimSpace(raw))
	}
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("remote is not a GitHub owner/repo URL: %q", strings.TrimSpace(raw))
	}
	return parts[0] + "/" + parts[1], nil
}

func (g *Git) viewPullRequest(selector, targetRepo string) (*PullRequestInfo, error) {
	args := []string{"pr", "view", selector, "--json", "number,url,state,mergedAt,headRefName,headRefOid,headRepository,headRepositoryOwner"}
	if targetRepo != "" && !strings.HasPrefix(selector, "http://") && !strings.HasPrefix(selector, "https://") {
		args = append(args, "--repo", targetRepo)
	}
	out, err := g.runGH(args...)
	if err != nil {
		return nil, fmt.Errorf("gh pr view %s failed: %w", selector, err)
	}
	var raw ghPullRequest
	if err := json.Unmarshal(bytes.TrimSpace(out), &raw); err != nil {
		return nil, fmt.Errorf("parse gh pr view %s: %w", selector, err)
	}
	pr := raw.toInfo()
	if pr.BaseRepo == "" {
		if repo, ok := githubRepoFromPullURL(selector); ok {
			pr.BaseRepo = repo
		} else {
			pr.BaseRepo = targetRepo
		}
	}
	if targetRepo != "" && pr.BaseRepo != "" && !strings.EqualFold(pr.BaseRepo, targetRepo) {
		return nil, fmt.Errorf("recorded PR %s targets %s, want %s", selector, pr.BaseRepo, targetRepo)
	}
	return pr, nil
}

func (g *Git) lookupPullRequestByHead(targetRepo, branch, headSHA string) (*PullRequestInfo, error) {
	out, err := g.runGH("pr", "list", "--repo", targetRepo, "--head", branch, "--state", "all", "--json", "number,url,state,mergedAt,headRefName,headRefOid,headRepository,headRepositoryOwner", "--limit", "100")
	if err != nil {
		return nil, fmt.Errorf("gh pr list head %s in %s failed: %w", branch, targetRepo, err)
	}
	var raw []ghPullRequest
	if err := json.Unmarshal(bytes.TrimSpace(out), &raw); err != nil {
		return nil, fmt.Errorf("parse gh pr list head %s in %s: %w", branch, targetRepo, err)
	}
	return selectPullRequest(raw, targetRepo, branch, headSHA, "head")
}

func (g *Git) lookupPullRequestByQualifiedHead(targetRepo, headOwner, branch, headSHA string) (*PullRequestInfo, error) {
	out, err := g.runGH("api", "-X", "GET", "repos/"+targetRepo+"/pulls", "-f", "state=all", "-f", "head="+headOwner+":"+branch)
	if err != nil {
		return nil, fmt.Errorf("gh api pull lookup head %s:%s in %s failed: %w", headOwner, branch, targetRepo, err)
	}
	var raw []ghRESTPullRequest
	if err := json.Unmarshal(bytes.TrimSpace(out), &raw); err != nil {
		return nil, fmt.Errorf("parse gh api pull lookup head %s:%s in %s: %w", headOwner, branch, targetRepo, err)
	}
	prs := make([]ghPullRequest, 0, len(raw))
	for _, pr := range raw {
		prs = append(prs, pr.toGH())
	}
	return selectPullRequest(prs, targetRepo, branch, headSHA, "qualified-head")
}

func (g *Git) runGH(args ...string) ([]byte, error) {
	cmd := exec.Command("gh", args...)
	cmd.Dir = g.workDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
	}
	return out, nil
}

func selectPullRequest(raw []ghPullRequest, targetRepo, branch, headSHA, source string) (*PullRequestInfo, error) {
	matches := make([]*PullRequestInfo, 0, len(raw))
	for _, candidate := range raw {
		pr := candidate.toInfo()
		if pr.BaseRepo == "" {
			pr.BaseRepo = targetRepo
		}
		if pr.HeadRefName != "" && pr.HeadRefName != branch {
			continue
		}
		if pr.BaseRepo != "" && !strings.EqualFold(pr.BaseRepo, targetRepo) {
			continue
		}
		if headSHA != "" && pr.HeadSHA != "" && pr.HeadSHA != headSHA {
			continue
		}
		pr.LookupSource = source
		matches = append(matches, pr)
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("%w: no PR for head %s in %s", ErrPullRequestNotFound, branch, targetRepo)
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("%w: head %s in %s matched %d PRs: %s", ErrPullRequestAmbiguous, branch, targetRepo, len(matches), describePullRequestMatches(matches))
	}
	return matches[0], nil
}

func githubRepoFromPullURL(raw string) (string, bool) {
	path, ok := strings.CutPrefix(strings.TrimSpace(raw), "https://github.com/")
	if !ok {
		return "", false
	}
	parts := strings.Split(path, "/")
	if len(parts) < 4 || parts[0] == "" || parts[1] == "" || parts[2] != "pull" {
		return "", false
	}
	return strings.ToLower(parts[0] + "/" + parts[1]), true
}

func describePullRequestMatches(prs []*PullRequestInfo) string {
	parts := make([]string, 0, len(prs))
	for _, pr := range prs {
		parts = append(parts, fmt.Sprintf("#%d %s %s:%s", pr.Number, pr.URL, pr.HeadOwner, pr.HeadRefName))
	}
	return strings.Join(parts, ", ")
}

type ghPullRequest struct {
	Number              int    `json:"number"`
	URL                 string `json:"url"`
	State               string `json:"state"`
	MergedAt            string `json:"mergedAt"`
	HeadRefName         string `json:"headRefName"`
	HeadRefOID          string `json:"headRefOid"`
	HeadRepository      ghRepo `json:"headRepository"`
	HeadRepositoryOwner ghUser `json:"headRepositoryOwner"`
	BaseRepository      ghRepo `json:"baseRepository"`
}

type ghRepo struct {
	NameWithOwner string `json:"nameWithOwner"`
}

type ghUser struct {
	Login string `json:"login"`
}

func (p ghPullRequest) toInfo() *PullRequestInfo {
	state := strings.ToUpper(p.State)
	if state == "CLOSED" && p.MergedAt != "" {
		state = "MERGED"
	}
	return &PullRequestInfo{
		Number:      p.Number,
		URL:         p.URL,
		State:       state,
		MergedAt:    p.MergedAt,
		HeadRefName: p.HeadRefName,
		HeadOwner:   p.HeadRepositoryOwner.Login,
		HeadRepo:    p.HeadRepository.NameWithOwner,
		HeadSHA:     p.HeadRefOID,
		BaseRepo:    p.BaseRepository.NameWithOwner,
	}
}

type ghRESTPullRequest struct {
	Number   int     `json:"number"`
	HTMLURL  string  `json:"html_url"`
	State    string  `json:"state"`
	MergedAt *string `json:"merged_at"`
	Head     struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo *struct {
			FullName string `json:"full_name"`
			Owner    struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"repo"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"head"`
	Base struct {
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"base"`
}

func (p ghRESTPullRequest) toGH() ghPullRequest {
	mergedAt := ""
	if p.MergedAt != nil {
		mergedAt = *p.MergedAt
	}
	headRepo := ""
	headOwner := p.Head.User.Login
	if p.Head.Repo != nil {
		headRepo = p.Head.Repo.FullName
		if p.Head.Repo.Owner.Login != "" {
			headOwner = p.Head.Repo.Owner.Login
		}
	}
	return ghPullRequest{
		Number:              p.Number,
		URL:                 p.HTMLURL,
		State:               p.State,
		MergedAt:            mergedAt,
		HeadRefName:         p.Head.Ref,
		HeadRefOID:          p.Head.SHA,
		HeadRepository:      ghRepo{NameWithOwner: headRepo},
		HeadRepositoryOwner: ghUser{Login: headOwner},
		BaseRepository:      ghRepo{NameWithOwner: p.Base.Repo.FullName},
	}
}
