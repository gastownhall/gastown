package cigate

import (
	"os"
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
