package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// si-mefr: detection for the armed state si-d6kw left behind.
//
// When a branch is rebased under a second worktree, that worktree's index ends
// up staged to remove the entire rebase delta. The Mayor's fleet sweep found
// keeper armed with 7767 staged line-deletions and coma with 6824 — and the
// auto-save commits that with no agent acting (proven 2026-07-28 at 08:01,
// during an explicit stop-write, with both agents complying).
//
// THE LOAD-BEARING DETAIL: the delta is staged as MODIFICATIONS to files that
// still exist, not as file deletions. Every file-status check — --diff-filter=D,
// --name-status — reports NOTHING while thousands of lines are staged to
// disappear. Only a LINE-based count sees it. Each test below therefore carries
// its own negative control asserting the file-status view is blind to the very
// state being measured; without that control these tests would pass just as
// happily against a fixture that was never armed in the first place.

// armFixture stages a mass line-deletion the way a rebase does: the file stays,
// its contents mostly go.
func armFixture(t *testing.T, repo string, lines int) {
	t.Helper()
	body := strings.Repeat("a line of merged work\n", lines)
	path := filepath.Join(repo, "delta.txt")
	writeAndCommit(t, repo, path, body, "seed the delta")

	// Rewrite to a single line and stage it — file still present, contents gone.
	writeAndStage(t, repo, path, "a line of merged work\n")
}

func writeAndCommit(t *testing.T, repo, path, body, msg string) {
	t.Helper()
	writeAndStage(t, repo, path, body)
	run(t, repo, "commit", "-m", msg)
}

func writeAndStage(t *testing.T, repo, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	run(t, repo, "add", filepath.Base(path))
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// assertFileStatusIsBlind is the negative control. If this ever fails, the
// fixture is staging real file deletions and the line-based test below is
// passing for a reason that has nothing to do with the defect.
func assertFileStatusIsBlind(t *testing.T, g *Git, repo string) {
	t.Helper()
	deleted, err := g.StagedDeletions()
	if err != nil {
		t.Fatalf("StagedDeletions: %v", err)
	}
	if len(deleted) != 0 {
		t.Fatalf("negative control failed: StagedDeletions saw %v. The fixture is staging FILE "+
			"deletions, so it is not the rebase-armed state and the line-based assertion below "+
			"proves nothing about it", deleted)
	}
	nameStatus := strings.TrimSpace(run(t, repo, "diff", "--cached", "--name-status", "--diff-filter=D"))
	if nameStatus != "" {
		t.Fatalf("negative control failed: --diff-filter=D reported %q; the armed state must be "+
			"invisible to file-status checks", nameStatus)
	}
}

func TestStagedLineDeletionsSeesWhatFileStatusChecksCannot(t *testing.T) {
	repo := initTestRepo(t)
	g := NewGit(repo)
	armFixture(t, repo, 200)

	// The control first: prove the state really is invisible to file-status.
	assertFileStatusIsBlind(t, g, repo)

	got, err := g.StagedLineDeletions()
	if err != nil {
		t.Fatalf("StagedLineDeletions: %v", err)
	}
	if got != 199 {
		t.Fatalf("StagedLineDeletions = %d, want 199 — this is the count that made keeper's 7767 "+
			"visible when every file-status check read clean", got)
	}
}

// Denominator: a clean index must read zero, or "armed" would fire on every
// worktree and the check would be deleted for crying wolf.
func TestStagedLineDeletionsIsZeroOnACleanIndex(t *testing.T) {
	repo := initTestRepo(t)
	g := NewGit(repo)

	got, err := g.StagedLineDeletions()
	if err != nil {
		t.Fatalf("StagedLineDeletions: %v", err)
	}
	if got != 0 {
		t.Fatalf("StagedLineDeletions = %d on a clean index, want 0", got)
	}
}

// A pure addition is not an armed state. shortstat reports insertions in the
// same line, so a parser that grabs the wrong number reads 200 deletions here.
func TestStagedLineDeletionsIgnoresInsertions(t *testing.T) {
	repo := initTestRepo(t)
	g := NewGit(repo)

	writeAndStage(t, repo, filepath.Join(repo, "new.txt"), strings.Repeat("added\n", 200))

	got, err := g.StagedLineDeletions()
	if err != nil {
		t.Fatalf("StagedLineDeletions: %v", err)
	}
	if got != 0 {
		t.Fatalf("StagedLineDeletions = %d for a 200-line pure insertion, want 0 — the parser is "+
			"reading the insertions field", got)
	}
}

// shortstat omits the insertions clause entirely when there are none, so the
// deletions field moves position. A positional parser passes the test above and
// fails here.
func TestStagedLineDeletionsHandlesDeletionsOnlyShortstat(t *testing.T) {
	repo := initTestRepo(t)
	g := NewGit(repo)

	path := filepath.Join(repo, "delta.txt")
	writeAndCommit(t, repo, path, strings.Repeat("x\n", 50), "seed")
	writeAndStage(t, repo, path, "")

	raw := run(t, repo, "diff", "--cached", "--shortstat")
	if strings.Contains(raw, "insertion") {
		t.Skipf("git emitted an insertions clause (%q); this git does not exercise the "+
			"deletions-only shortstat form", strings.TrimSpace(raw))
	}

	got, err := g.StagedLineDeletions()
	if err != nil {
		t.Fatalf("StagedLineDeletions: %v", err)
	}
	if got != 50 {
		t.Fatalf("StagedLineDeletions = %d for shortstat %q, want 50", got, strings.TrimSpace(raw))
	}
}

// Guard the parser against a shortstat whose numbers it must not transpose.
func TestParseShortstatDeletions(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{" 5 files changed, 26 insertions(+), 434 deletions(-)", 434},
		{" 1 file changed, 1 insertion(+), 1 deletion(-)", 1},
		{" 3 files changed, 7767 deletions(-)", 7767},
		{" 2 files changed, 12 insertions(+)", 0},
		{"", 0},
		{"garbage", 0},
	}
	for _, tc := range cases {
		if got := parseShortstatDeletions(tc.in); got != tc.want {
			t.Errorf("parseShortstatDeletions(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
	// The transposition this guards against: 26 insertions must never be read
	// as the deletion count for the line that recorded 434.
	if got := parseShortstatDeletions(" 5 files changed, 26 insertions(+), 434 deletions(-)"); got == 26 {
		t.Fatal("parser returned the insertions count as deletions")
	}
}
