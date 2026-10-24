package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/steveyegge/gastown/internal/testutil/hermetic"
)

func TestMain(m *testing.M) {
	// Keep tests away from the caller's session, town and Dolt server.
	restoreEnv := hermetic.Isolate()
	if err := hermetic.Check(); err != nil {
		fmt.Fprintf(os.Stderr, "config TestMain: %v\n", err)
		os.Exit(1)
	}

	stubDir, err := os.MkdirTemp("", "gt-agent-bin-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create stub dir: %v\n", err)
		os.Exit(1)
	}

	binaries := []string{
		"claude",
		"gemini",
		"codex",
		"cursor-agent",
		"auggie",
		"amp",
		"opencode",
	}
	for _, name := range binaries {
		path := filepath.Join(stubDir, name)
		stub := []byte("#!/bin/sh\nexit 0\n")
		mode := os.FileMode(0755)
		if runtime.GOOS == "windows" {
			path += ".cmd"
			stub = []byte("@echo off\r\nexit /b 0\r\n")
			mode = 0644
		}
		if err := os.WriteFile(path, stub, mode); err != nil {
			fmt.Fprintf(os.Stderr, "write stub %s: %v\n", name, err)
			os.Exit(1)
		}
	}

	originalPath := os.Getenv("PATH")
	_ = os.Setenv("PATH", stubDir+string(os.PathListSeparator)+originalPath)
	// cursor_agent_cli_test.go skips this directory when resolving a real cursor-agent
	// (must stay in sync — do not rename without updating that resolver).
	_ = os.Setenv("GT_AGENT_STUB_BIN_DIR", stubDir)

	code := m.Run()

	_ = os.Setenv("PATH", originalPath)
	_ = os.Unsetenv("GT_AGENT_STUB_BIN_DIR")
	_ = os.RemoveAll(stubDir)
	restoreEnv()
	os.Exit(code)
}
