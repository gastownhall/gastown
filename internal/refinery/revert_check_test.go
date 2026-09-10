package refinery

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newRepo builds the ap-8iyu shape:
//
//	main:   old  ->  dust's replacement (landed)
//	branch: old  (a stale copy, byte-identical to the superseded version)
//
// Guzzle's real commit had dust's merge as its DIRECT PARENT, so git saw an
// ordinary edit and nothing conflicted. The branch here is built the same way.
func newRepo(t *testing.T) (dir, mainRef, branchRef string) {
	t.Helper()
	dir = t.TempDir()
	mustGit(t, dir, "init", "-q", "-b", "main")

	write(t, dir, "ledger_test.py", "the old test\n")
	write(t, dir, "untouched.py", "stable\n")
	mustGit(t, dir, "add", ".")
	mustGit(t, dir, "commit", "-q", "-m", "Teach the ledger verifier to see a row that claims nothing")

	write(t, dir, "ledger_test.py", "dust's replacement\n")
	mustGit(t, dir, "commit", "-qam", "Move the every-status assertion off the slice (ap-w0qt, DUST)")

	return dir, "main", ""
}

func TestCheckForReverts_FlagsAStaleCopyOfASupersededVersion(t *testing.T) {
	dir, mainRef, _ := newRepo(t)

	// The branch restores the pre-dust content, exactly as guzzle's did.
	mustGit(t, dir, "checkout", "-q", "-b", "guzzle", "main")
	write(t, dir, "ledger_test.py", "the old test\n")
	mustGit(t, dir, "commit", "-qam", "Decide the u1 pl_function vocabulary (ap-s1wu, GUZZLE)")

	findings, err := CheckForReverts(dir, mainRef, "guzzle", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected exactly 1 revert finding, got %d: %v", len(findings), findings)
	}
	got := findings[0]
	if got.Path != "ledger_test.py" {
		t.Errorf("flagged the wrong file: %q", got.Path)
	}
	if got.SupersededSubject == "" {
		t.Error("finding does not name the superseded commit; the halt message would not say what would be undone")
	}
	if got.BranchBlob == got.MainBlob {
		t.Error("branch and target blobs are equal; this should not have been flagged at all")
	}
}

// The check must be quiet on ordinary forward work, or a gate wired to it halts
// on everything and gets turned off.
func TestCheckForReverts_QuietOnForwardWork(t *testing.T) {
	dir, mainRef, _ := newRepo(t)

	mustGit(t, dir, "checkout", "-q", "-b", "forward", "main")
	write(t, dir, "ledger_test.py", "a genuinely new third version\n")
	write(t, dir, "brand_new.py", "new file\n")
	mustGit(t, dir, "add", ".")
	mustGit(t, dir, "commit", "-qm", "ordinary forward work")

	findings, err := CheckForReverts(dir, mainRef, "forward", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("forward work was flagged as a revert: %v", findings)
	}
}

// A branch that leaves a file alone must never be flagged for it, even when the
// target moved that file on after the branch point.
func TestCheckForReverts_IgnoresFilesTheBranchDoesNotTouch(t *testing.T) {
	dir, mainRef, _ := newRepo(t)

	mustGit(t, dir, "checkout", "-q", "-b", "elsewhere", "main")
	write(t, dir, "brand_new.py", "new file\n")
	mustGit(t, dir, "add", ".")
	mustGit(t, dir, "commit", "-qm", "touches nothing contested")

	findings, err := CheckForReverts(dir, mainRef, "elsewhere", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("branch that touched no contested file was flagged: %v", findings)
	}
}

// The documented limit, asserted so nobody reads the control as stronger than it
// is: a stale copy that also picked up unrelated edits matches no historical
// blob and is NOT caught. hq-lxqb stays live.
func TestCheckForReverts_DoesNotCatchAStaleCopyThatAlsoDrifted(t *testing.T) {
	dir, mainRef, _ := newRepo(t)

	mustGit(t, dir, "checkout", "-q", "-b", "drifted", "main")
	write(t, dir, "ledger_test.py", "the old test\nplus an unrelated edit\n")
	mustGit(t, dir, "commit", "-qam", "stale copy with drift")

	findings, err := CheckForReverts(dir, mainRef, "drifted", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("this case is OUTSIDE the stated limit and must not be claimed as caught; got %v", findings)
	}
}
