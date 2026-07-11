package cigate

import (
	"os"
	"strconv"
	"testing"
)

// TestCheckBranchLive runs the gate against real GitHub. Opt-in:
//
//	GT_CIGATE_LIVE_TEST_DIR=<repo clone> \
//	GT_CIGATE_LIVE_TEST_BRANCH=<branch> \
//	GT_CIGATE_LIVE_TEST_WANT=<verdict> go test ./internal/cigate -run Live
func TestCheckBranchLive(t *testing.T) {
	dir := os.Getenv("GT_CIGATE_LIVE_TEST_DIR")
	branch := os.Getenv("GT_CIGATE_LIVE_TEST_BRANCH")
	want := os.Getenv("GT_CIGATE_LIVE_TEST_WANT")
	if dir == "" || branch == "" {
		t.Skip("set GT_CIGATE_LIVE_TEST_DIR and GT_CIGATE_LIVE_TEST_BRANCH to run")
	}
	res := New().CheckBranch(dir, branch)
	t.Logf("live verdict=%s pr=%d url=%s failing=%v pending=%v err=%v",
		res.Verdict, res.PRNumber, res.PRURL, res.FailingChecks, res.PendingChecks, res.Err)
	if want != "" && string(res.Verdict) != want {
		t.Fatalf("verdict = %s, want %s", res.Verdict, want)
	}
}

// TestMacroscopeLive runs the settle check and the comment fetch against a
// real PR (op-sr9u). Opt-in:
//
//	GT_CIGATE_LIVE_TEST_DIR=<repo clone> \
//	GT_CIGATE_LIVE_TEST_BRANCH=<branch> \
//	GT_CIGATE_LIVE_TEST_PR=<pr number> go test ./internal/cigate -run Live -v
func TestMacroscopeLive(t *testing.T) {
	dir := os.Getenv("GT_CIGATE_LIVE_TEST_DIR")
	branch := os.Getenv("GT_CIGATE_LIVE_TEST_BRANCH")
	prStr := os.Getenv("GT_CIGATE_LIVE_TEST_PR")
	if dir == "" || (branch == "" && prStr == "") {
		t.Skip("set GT_CIGATE_LIVE_TEST_DIR and GT_CIGATE_LIVE_TEST_BRANCH or _PR to run")
	}
	opts := MacroscopeOptions{CheckPatterns: []string{"macroscope"}, BotLogins: []string{"macroscopeapp"}}
	g := New()
	if branch != "" {
		res := g.CheckMacroscopeSettle(dir, branch, opts)
		t.Logf("live settle: settled=%v pending=%v pr=%d err=%v", res.Settled, res.PendingChecks, res.PRNumber, res.Err)
		if res.Err != nil {
			t.Fatalf("settle check failed: %v", res.Err)
		}
		if prStr == "" && res.PRNumber != 0 {
			prStr = strconv.Itoa(res.PRNumber)
		}
	}
	if prStr == "" {
		return
	}
	pr, err := strconv.Atoi(prStr)
	if err != nil {
		t.Fatalf("bad GT_CIGATE_LIVE_TEST_PR: %v", err)
	}
	comments, err := g.FetchUnaddressedComments(dir, pr, opts)
	if err != nil {
		t.Fatalf("comment fetch failed: %v", err)
	}
	t.Logf("live fetch: %d unaddressed Macroscope comment(s)", len(comments))
	for _, c := range comments {
		t.Logf("  - %s (%s): %s", c.URL, c.Path, c.Excerpt)
	}
}
