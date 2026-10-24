package tmux

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestSetGetDefaultSocket(t *testing.T) {
	// Save and restore
	orig := defaultSocket
	defer func() { defaultSocket = orig }()

	// Initially empty
	SetDefaultSocket("")
	if got := GetDefaultSocket(); got != "" {
		t.Errorf("expected empty, got %q", got)
	}

	SetDefaultSocket("mytown")
	if got := GetDefaultSocket(); got != "mytown" {
		t.Errorf("expected %q, got %q", "mytown", got)
	}
}

func TestNewTmuxInheritsSocket(t *testing.T) {
	orig := defaultSocket
	defer func() { defaultSocket = orig }()

	SetDefaultSocket("testtown")
	tmx := NewTmux()
	if tmx.socketName != "testtown" {
		t.Errorf("NewTmux() socketName = %q, want %q", tmx.socketName, "testtown")
	}
}

func TestNewTmuxWithSocket(t *testing.T) {
	tmx := NewTmuxWithSocket("custom")
	if tmx.socketName != "custom" {
		t.Errorf("NewTmuxWithSocket() socketName = %q, want %q", tmx.socketName, "custom")
	}
}

func TestBuildCommandNoSocket(t *testing.T) {
	orig := defaultSocket
	defer func() { defaultSocket = orig }()

	SetDefaultSocket("")
	cmd := BuildCommand("list-sessions")
	args := cmd.Args
	// Should be: tmux -u list-sessions
	expected := []string{"tmux", "-u", "list-sessions"}
	if len(args) != len(expected) {
		t.Fatalf("args = %v, want %v", args, expected)
	}
	for i, a := range args {
		if a != expected[i] {
			t.Errorf("args[%d] = %q, want %q", i, a, expected[i])
		}
	}
}

func TestBuildCommandWithSocket(t *testing.T) {
	orig := defaultSocket
	defer func() { defaultSocket = orig }()

	SetDefaultSocket("mytown")
	cmd := BuildCommand("has-session", "-t", "hq-mayor")
	args := cmd.Args
	// Should be: tmux -u -L mytown has-session -t hq-mayor
	expected := []string{"tmux", "-u", "-L", "mytown", "has-session", "-t", "hq-mayor"}
	if len(args) != len(expected) {
		t.Fatalf("args = %v, want %v", args, expected)
	}
	for i, a := range args {
		if a != expected[i] {
			t.Errorf("args[%d] = %q, want %q", i, a, expected[i])
		}
	}
}

func TestSocketDir_HonorsTMUXTMPDIR(t *testing.T) {
	uid := fmt.Sprintf("tmux-%d", os.Getuid())

	t.Setenv("TMUX_TMPDIR", "")
	if got, want := SocketDir(), filepath.Join("/tmp", uid); got != want {
		t.Errorf("SocketDir() = %q, want %q", got, want)
	}

	dir := t.TempDir()
	t.Setenv("TMUX_TMPDIR", dir)
	if got, want := SocketDir(), filepath.Join(dir, uid); got != want {
		t.Errorf("SocketDir() with TMUX_TMPDIR = %q, want %q", got, want)
	}
}
