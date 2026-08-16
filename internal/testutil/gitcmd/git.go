package gitcmd

import (
	"os/exec"
	"testing"
)

// Run runs git -C dir with the given args and fails the test on error.
func Run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// InitRepo initializes a git repository in dir with a local user identity.
func InitRepo(t *testing.T, dir string) {
	t.Helper()
	Run(t, dir, "init")
	Run(t, dir, "config", "user.name", "test")
	Run(t, dir, "config", "user.email", "test@example.com")
}

// CommitFile stages name in dir and commits it with message.
func CommitFile(t *testing.T, dir, name, message string) {
	t.Helper()
	Run(t, dir, "add", name)
	Run(t, dir, "commit", "-m", message)
}
