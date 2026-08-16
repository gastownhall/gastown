package tmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonbaldie/gastown/internal/tmux/tmuxtest"
)

// TestNudgeSubmit_NamedEnterNoop_LiteralCRRequired replays GH#4666 defect 3:
// on tmux 3.7b, named-key Enter/C-m/KPEnter do not submit while a literal
// carriage return (send-keys -l $'\r') does. The stub swallows named Enter
// the way that tmux does not deliver it; the message must still reach cat.
func TestNudgeSubmit_NamedEnterNoop_LiteralCRRequired(t *testing.T) {
	realTmux := tmuxtest.RealTmuxOrSkip(t)

	t.Setenv("GT_REAL_TMUX", realTmux)
	t.Setenv("GT_TMUX_NOOP_NAMED_ENTER", "1")

	socket := tmuxtest.SocketName(t, "gt4666-enter-")
	session := "nudge-enter"
	outFile := filepath.Join(t.TempDir(), "submitted.txt")
	tmuxtest.StartSession(t, realTmux, socket, session,
		"printf 'esc to interrupt\\n'; exec cat >"+tmuxtest.ShellQuote(outFile))

	stub := tmuxtest.InstallStub(t)
	tm := NewTmuxWithSocketAndBinary(socket, stub)
	tm.SetCapabilities(Capabilities{LiteralCR: true})

	msg := "hello-from-4666"
	nudgeErr := tm.NudgeSessionWithOpts(session, msg, NudgeOpts{})
	if nudgeErr != nil {
		t.Logf("NudgeSessionWithOpts error after literal CR submit: %v", nudgeErr)
	}

	deadline := time.Now().Add(2 * time.Second)
	var got []byte
	var err error
	for time.Now().Before(deadline) {
		got, err = os.ReadFile(outFile)
		if err == nil && strings.Contains(string(got), msg) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("named-key Enter did not submit; literal CR is required. outfile=%q err=%v", string(got), err)
}

// TestStartupDialogSubmit_NamedEnterNoop_LiteralCRRequired verifies startup
// acceptance uses the same tmux 3.7b-safe submission path as nudges.
func TestStartupDialogSubmit_NamedEnterNoop_LiteralCRRequired(t *testing.T) {
	realTmux := tmuxtest.RealTmuxOrSkip(t)

	t.Setenv("GT_REAL_TMUX", realTmux)
	t.Setenv("GT_TMUX_NOOP_NAMED_ENTER", "1")

	socket := tmuxtest.SocketName(t, "gt-dialog-enter-")
	session := "dialog-enter"
	outFile := filepath.Join(t.TempDir(), "accepted.txt")
	tmuxtest.StartSession(t, realTmux, socket, session,
		"printf 'Do you trust the contents of this directory?\\n'; exec cat >"+tmuxtest.ShellQuote(outFile))

	stub := tmuxtest.InstallStub(t)
	tm := NewTmuxWithSocketAndBinary(socket, stub)
	tm.SetCapabilities(Capabilities{LiteralCR: true})
	if err := tm.AcceptWorkspaceTrustDialog(session); err != nil {
		t.Fatalf("AcceptWorkspaceTrustDialog: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(outFile); err == nil && len(data) > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	data, err := os.ReadFile(outFile)
	t.Fatalf("startup dialog was not accepted with a literal CR; outfile=%q err=%v", data, err)
}

// TestNudgeSendKeys_DoesNotPassUnknown37bFlags records send-keys argv during a
// real NudgeSession. tmux 3.7b rejects -V and -o on send-keys (GH#4666 defect 1).
func TestNudgeSendKeys_DoesNotPassUnknown37bFlags(t *testing.T) {
	realTmux := tmuxtest.RealTmuxOrSkip(t)

	logPath := filepath.Join(t.TempDir(), "tmux.argv")
	t.Setenv("GT_REAL_TMUX", realTmux)
	t.Setenv("GT_TMUX_ARGV_LOG", logPath)
	t.Setenv("GT_TMUX_REJECT_37B_FLAGS", "1")

	socket := tmuxtest.SocketName(t, "gt4666-flags-")
	session := "nudge-flags"
	tmuxtest.StartSession(t, realTmux, socket, session, "printf 'esc to interrupt\\n'; exec cat")

	stub := tmuxtest.InstallStub(t)
	tm := NewTmuxWithSocketAndBinary(socket, stub)
	tm.SetCapabilities(Capabilities{LiteralCR: true})
	nudgeErr := tm.NudgeSessionWithOpts(session, "flag-probe", NudgeOpts{})
	if nudgeErr != nil && sendKeysUsedUnknown37bFlag(nudgeErr.Error()) {
		t.Fatalf("production send-keys used a tmux 3.7b-unknown flag: %v", nudgeErr)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read argv log: %v (nudgeErr=%v)", err, nudgeErr)
	}
	sawSendKeys := false
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, "send-keys") {
			continue
		}
		sawSendKeys = true
		fields := strings.Fields(line)
		for _, f := range fields {
			if f == "-V" || f == "-o" {
				t.Fatalf("send-keys used tmux 3.7b-unknown flag %s: %s", f, line)
			}
		}
	}
	if !sawSendKeys {
		t.Fatalf("no send-keys invocation recorded; cannot verify flags. log=%q nudgeErr=%v", data, nudgeErr)
	}
}

func sendKeysUsedUnknown37bFlag(errText string) bool {
	return strings.Contains(errText, "unknown flag -V") || strings.Contains(errText, "unknown flag -o")
}
