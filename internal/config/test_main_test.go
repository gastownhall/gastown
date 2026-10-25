package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
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
	os.Exit(code)
}

func startupCommandBody(t *testing.T, command string) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		return command
	}

	if !strings.HasPrefix(command, "& ") {
		t.Fatalf("startup command is not a PowerShell script invocation: %q", command)
	}
	quotedPath := strings.TrimSpace(strings.TrimPrefix(command, "& "))
	if len(quotedPath) < 2 || quotedPath[0] != '\'' || quotedPath[len(quotedPath)-1] != '\'' {
		t.Fatalf("startup script path is not single-quoted: %q", command)
	}
	scriptPath := strings.ReplaceAll(quotedPath[1:len(quotedPath)-1], "''", "'")
	if !strings.EqualFold(filepath.Ext(scriptPath), ".ps1") {
		t.Fatalf("startup command does not invoke a PowerShell script: %q", command)
	}

	data, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read startup script %q: %v", scriptPath, err)
	}
	return string(data)
}

func startupEnvAssignment(key, value string) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("$env:%s=%s", key, psQuote(value))
	}
	return fmt.Sprintf("%s=%s", key, ShellQuote(value))
}
