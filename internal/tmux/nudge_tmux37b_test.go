package tmux

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestNudgeSubmit_NamedEnterNoop_LiteralCRRequired replays GH#4666 defect 3:
// on tmux 3.7b, named-key Enter/C-m/KPEnter do not submit while a literal
// carriage return (send-keys -l $'\r') does. The stub swallows named Enter
// the way that tmux does not deliver it; the message must still reach cat.
func TestNudgeSubmit_NamedEnterNoop_LiteralCRRequired(t *testing.T) {
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed")
	}

	t.Setenv("GT_REAL_TMUX", realTmux)
	t.Setenv("GT_TMUX_NOOP_NAMED_ENTER", "1")

	socket := "gt4666-enter-" + strings.ReplaceAll(t.Name(), "/", "-")
	session := "nudge-enter"
	outFile := filepath.Join(t.TempDir(), "submitted.txt")
	startIsolatedTmuxSession(t, realTmux, socket, session,
		"printf 'esc to interrupt\\n'; exec cat >"+shellQuote(outFile))

	stub := installTmuxStub(t)
	tm := NewTmuxWithSocketAndBinary(socket, stub)

	msg := "hello-from-4666"
	if err := tm.NudgeSessionWithOpts(session, msg, NudgeOpts{}); err != nil {
		t.Logf("NudgeSessionWithOpts error (may be expected while Enter is a no-op): %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	var got []byte
	for time.Now().Before(deadline) {
		got, err = os.ReadFile(outFile)
		if err == nil && strings.Contains(string(got), msg) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("named-key Enter did not submit; literal CR is required. outfile=%q err=%v", string(got), err)
}

// TestNudgeSendKeys_DoesNotPassUnknown37bFlags records send-keys argv during a
// real NudgeSession. tmux 3.7b rejects -V and -o on send-keys (GH#4666 defect 1).
func TestNudgeSendKeys_DoesNotPassUnknown37bFlags(t *testing.T) {
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed")
	}

	logPath := filepath.Join(t.TempDir(), "tmux.argv")
	t.Setenv("GT_REAL_TMUX", realTmux)
	t.Setenv("GT_TMUX_ARGV_LOG", logPath)
	t.Setenv("GT_TMUX_REJECT_37B_FLAGS", "1")

	socket := "gt4666-flags-" + strings.ReplaceAll(t.Name(), "/", "-")
	session := "nudge-flags"
	startIsolatedTmuxSession(t, realTmux, socket, session, "printf 'esc to interrupt\\n'; exec cat")

	stub := installTmuxStub(t)
	tm := NewTmuxWithSocketAndBinary(socket, stub)
	_ = tm.NudgeSessionWithOpts(session, "flag-probe", NudgeOpts{})

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read argv log: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, "send-keys") {
			continue
		}
		fields := strings.Fields(line)
		for _, f := range fields {
			if f == "-V" || f == "-o" {
				t.Fatalf("send-keys used tmux 3.7b-unknown flag %s: %s", f, line)
			}
		}
	}
}

func startIsolatedTmuxSession(t *testing.T, realTmux, socket, sessionName, shellCmd string) {
	t.Helper()
	cmd := exec.Command(realTmux, "-u", "-L", socket, "new-session", "-d", "-s", sessionName, "sh", "-c", shellCmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command(realTmux, "-L", socket, "kill-server").Run()
	})
}

func installTmuxStub(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	src := filepath.Join(filepath.Dir(file), "testdata", "tmuxstub.sh")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read tmux stub: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "tmux")
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		t.Fatalf("write tmux stub: %v", err)
	}
	return dst
}

func shellQuote(path string) string {
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}
