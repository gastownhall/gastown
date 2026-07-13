package git

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLookupPullRequestRecordedURLSurvivesDeletedHead(t *testing.T) {
	installFakeGH(t, `#!/bin/sh
case "$*" in
  *baseRepository*) printf 'unsupported field requested: %s\n' "$*" >&2; exit 2 ;;
esac
if [ "$1" = "pr" ] && [ "$2" = "view" ] && [ "$3" = "https://github.com/upstream/repo/pull/42" ]; then
  printf '%s\n' '{"number":42,"url":"https://github.com/upstream/repo/pull/42","state":"MERGED","mergedAt":"2026-07-13T12:00:00Z","headRefName":"fix/deleted-head","headRefOid":"abc123","headRepository":null,"headRepositoryOwner":{"login":"fork-owner"},"baseRepository":{"nameWithOwner":"upstream/repo"}}'
  exit 0
fi
printf 'unexpected gh args: %s\n' "$*" >&2
exit 1
`)
	dir := initTestRepo(t)
	g := NewGit(dir)
	addGitHubRemotes(t, g)

	pr, err := g.LookupPullRequest(PullRequestRef{URL: "https://github.com/upstream/repo/pull/42", Branch: "fix/deleted-head"})
	if err != nil {
		t.Fatalf("LookupPullRequest: %v", err)
	}
	if !pr.Merged() || pr.Number != 42 || pr.HeadOwner != "fork-owner" {
		t.Fatalf("unexpected PR: %+v", pr)
	}
	if pr.LookupSource != "recorded-url" {
		t.Fatalf("LookupSource = %q, want recorded-url", pr.LookupSource)
	}
}

func TestLookupPullRequestQualifiedForkHead(t *testing.T) {
	installFakeGH(t, `#!/bin/sh
if [ "$1" = "api" ] && [ "$2" = "-X" ] && [ "$3" = "GET" ] && [ "$4" = "repos/upstream/repo/pulls" ]; then
  printf '%s\n' '[{"number":4474,"html_url":"https://github.com/upstream/repo/pull/4474","state":"open","merged_at":null,"head":{"ref":"fix/fork-head","sha":"sha4474","repo":{"full_name":"blairsilverberg/repo","owner":{"login":"blairsilverberg"}},"user":{"login":"blairsilverberg"}},"base":{"repo":{"full_name":"upstream/repo"}}}]'
  exit 0
fi
printf 'unexpected gh args: %s\n' "$*" >&2
exit 1
`)
	dir := initTestRepo(t)
	g := NewGit(dir)
	addGitHubRemotes(t, g)

	pr, err := g.LookupPullRequest(PullRequestRef{Branch: "fix/fork-head", HeadOwner: "blairsilverberg"})
	if err != nil {
		t.Fatalf("LookupPullRequest: %v", err)
	}
	if !pr.Open() || pr.Number != 4474 || pr.HeadOwner != "blairsilverberg" {
		t.Fatalf("unexpected PR: %+v", pr)
	}
	if pr.LookupSource != "qualified-head" {
		t.Fatalf("LookupSource = %q, want qualified-head", pr.LookupSource)
	}
}

func TestLookupPullRequestBranchAmbiguityFailsClosed(t *testing.T) {
	installFakeGH(t, `#!/bin/sh
if [ "$1" = "pr" ] && [ "$2" = "list" ]; then
  printf '%s\n' '[{"number":1,"url":"https://github.com/upstream/repo/pull/1","state":"OPEN","mergedAt":"","headRefName":"shared","headRefOid":"a1","headRepository":{"nameWithOwner":"one/repo"},"headRepositoryOwner":{"login":"one"},"baseRepository":{"nameWithOwner":"upstream/repo"}},{"number":2,"url":"https://github.com/upstream/repo/pull/2","state":"OPEN","mergedAt":"","headRefName":"shared","headRefOid":"b2","headRepository":{"nameWithOwner":"two/repo"},"headRepositoryOwner":{"login":"two"},"baseRepository":{"nameWithOwner":"upstream/repo"}}]'
  exit 0
fi
printf 'unexpected gh args: %s\n' "$*" >&2
exit 1
`)
	dir := initTestRepo(t)
	g := NewGit(dir)
	addGitHubRemotes(t, g)

	_, err := g.LookupPullRequest(PullRequestRef{Branch: "shared"})
	if !errors.Is(err, ErrPullRequestAmbiguous) {
		t.Fatalf("LookupPullRequest err = %v, want ErrPullRequestAmbiguous", err)
	}
	if !g.HasOpenPR("shared") {
		t.Fatal("HasOpenPR should protect branch deletion on ambiguous lookup")
	}
}

func TestLookupPullRequestBranchHeadSHADisambiguates(t *testing.T) {
	installFakeGH(t, `#!/bin/sh
if [ "$1" = "pr" ] && [ "$2" = "list" ]; then
  printf '%s\n' '[{"number":1,"url":"https://github.com/upstream/repo/pull/1","state":"CLOSED","mergedAt":"","headRefName":"shared","headRefOid":"old","headRepository":{"nameWithOwner":"one/repo"},"headRepositoryOwner":{"login":"one"},"baseRepository":{"nameWithOwner":"upstream/repo"}},{"number":2,"url":"https://github.com/upstream/repo/pull/2","state":"CLOSED","mergedAt":"2026-07-13T12:00:00Z","headRefName":"shared","headRefOid":"wanted","headRepository":{"nameWithOwner":"two/repo"},"headRepositoryOwner":{"login":"two"},"baseRepository":{"nameWithOwner":"upstream/repo"}}]'
  exit 0
fi
printf 'unexpected gh args: %s\n' "$*" >&2
exit 1
`)
	dir := initTestRepo(t)
	g := NewGit(dir)
	addGitHubRemotes(t, g)

	pr, err := g.LookupPullRequest(PullRequestRef{Branch: "shared", HeadSHA: "wanted"})
	if err != nil {
		t.Fatalf("LookupPullRequest: %v", err)
	}
	if pr.Number != 2 || !pr.Merged() {
		t.Fatalf("unexpected PR: %+v", pr)
	}
}

func TestFindPRNumberRequiresOpenPR(t *testing.T) {
	installFakeGH(t, `#!/bin/sh
if [ "$1" = "pr" ] && [ "$2" = "view" ] && [ "$3" = "99" ]; then
  printf '%s\n' '{"number":99,"url":"https://github.com/upstream/repo/pull/99","state":"CLOSED","mergedAt":"","headRefName":"closed","headRefOid":"abc","headRepository":{"nameWithOwner":"fork/repo"},"headRepositoryOwner":{"login":"fork"},"baseRepository":{"nameWithOwner":"upstream/repo"}}'
  exit 0
fi
printf 'unexpected gh args: %s\n' "$*" >&2
exit 1
`)
	dir := initTestRepo(t)
	g := NewGit(dir)
	addGitHubRemotes(t, g)

	number, err := g.FindPRNumberForRef(PullRequestRef{Number: 99})
	if err != nil {
		t.Fatalf("FindPRNumberForRef: %v", err)
	}
	if number != 0 {
		t.Fatalf("FindPRNumberForRef = %d, want 0 for closed-unmerged PR", number)
	}
}

func TestPullRequestApprovalAndMergeUseResolvedURLAndRepo(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "gh.log")
	t.Setenv("GH_LOG", logPath)
	installFakeGH(t, `#!/bin/sh
printf '%s\n' "$*" >> "$GH_LOG"
if [ "$1" = "pr" ] && [ "$2" = "view" ]; then
  printf '%s\n' '{"reviewDecision":"APPROVED"}'
  exit 0
fi
if [ "$1" = "pr" ] && [ "$2" = "merge" ]; then
  exit 0
fi
printf 'unexpected gh args: %s\n' "$*" >&2
exit 1
`)
	dir := initTestRepo(t)
	g := NewGit(dir)
	pr := &PullRequestInfo{Number: 42, URL: "https://github.com/upstream/repo/pull/42", BaseRepo: "upstream/repo"}

	approved, err := g.IsPullRequestApproved(pr)
	if err != nil {
		t.Fatalf("IsPullRequestApproved: %v", err)
	}
	if !approved {
		t.Fatal("IsPullRequestApproved = false, want true")
	}
	if _, err := g.GhPrMergePullRequest(pr, "squash"); err != nil {
		t.Fatalf("GhPrMergePullRequest: %v", err)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read gh log: %v", err)
	}
	log := string(logBytes)
	for _, want := range []string{
		"pr view https://github.com/upstream/repo/pull/42 --json reviewDecision --repo upstream/repo",
		"pr merge https://github.com/upstream/repo/pull/42 --squash --delete-branch --repo upstream/repo",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("gh log missing %q\nlog:\n%s", want, log)
		}
	}
}

func installFakeGH(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gh")
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func addGitHubRemotes(t *testing.T, g *Git) {
	t.Helper()
	if _, err := g.AddRemote("origin", "https://github.com/fork/repo.git"); err != nil && !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("AddRemote origin: %v", err)
	}
	if err := g.AddUpstreamRemote("https://github.com/upstream/repo.git"); err != nil {
		t.Fatalf("AddUpstreamRemote: %v", err)
	}
}
