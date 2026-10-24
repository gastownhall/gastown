//go:build integration

package opencodeserver

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestRealOpenCodeServerLifecycle(t *testing.T) {
	command := os.Getenv("GT_OPENCODE_TEST_COMMAND")
	if command == "" {
		command = "opencode"
		if runtime.GOOS == "windows" {
			command = "opencode.cmd"
		}
	}
	resolved, err := exec.LookPath(command)
	if err != nil {
		t.Skipf("OpenCode executable not found: %v", err)
	}

	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "README.md"), []byte("integration smoke\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	server, err := StartServer(ctx, ServerOptions{Command: resolved, Directory: directory})
	if err != nil {
		t.Fatalf("StartServer: %v", err)
	}
	defer server.Stop()
	client, err := server.NewClient(directory)
	if err != nil {
		t.Fatal(err)
	}
	events, err := client.Events(ctx)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	events.Close()
	session, err := client.CreateSession(ctx, CreateSessionOptions{Title: "Gas Town integration smoke", Agent: "build"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	got, err := client.GetSession(ctx, session.ID)
	if err != nil || got.ID != session.ID {
		t.Fatalf("GetSession = %#v, %v", got, err)
	}
	if err := client.DeleteSession(ctx, session.ID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
}
