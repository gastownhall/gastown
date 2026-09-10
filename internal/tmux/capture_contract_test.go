package tmux

import (
	"os"
	"strings"
	"testing"
)

// CapturePane(s, N) returns the whole visible pane plus N scrollback rows, NOT
// the last N. That mismatch made sendEnterVerified's before/after diff
// unfalsifiable: the window contained Claude Code's spinner and elapsed timer,
// so consecutive snapshots always differed and "changed" always read as
// "Enter was processed". The three-retry loop under it was unreachable.
func TestCapturePaneTailIsActuallyATail(t *testing.T) {
	sock, sess := os.Getenv("GT_TMUX_SOCKET"), os.Getenv("GT_TMUX_SESSION")
	if sock == "" || sess == "" {
		t.Skip("GT_TMUX_SOCKET/GT_TMUX_SESSION not set")
	}
	tm := NewTmuxWithSocket(sock)

	whole, err := tm.CapturePane(sess, 5)
	if err != nil {
		t.Skipf("capture failed: %v", err)
	}
	wholeN := len(strings.Split(strings.TrimRight(whole, "\n"), "\n"))
	if wholeN <= 5 {
		t.Skipf("pane too short to demonstrate (%d lines)", wholeN)
	}

	tail, err := tm.CapturePaneTail(sess, 5)
	if err != nil {
		t.Fatal(err)
	}
	tailN := len(strings.Split(strings.TrimRight(tail, "\n"), "\n"))
	if tailN > 5 {
		t.Errorf("CapturePaneTail(5) returned %d lines; must be at most 5", tailN)
	}
	t.Logf("CapturePane(5)=%d lines, CapturePaneTail(5)=%d lines", wholeN, tailN)
}
