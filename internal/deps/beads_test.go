package deps

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestParseBeadsVersion(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"bd version 0.55.4 (dev: main@3e1378e122c6)", "0.55.4"},
		{"bd version 0.55.4", "0.55.4"},
		{"bd version 1.2.3", "1.2.3"},
		{"bd version 10.20.30 (release)", "10.20.30"},
		{"some other output", ""},
		{"", ""},
	}

	for _, tt := range tests {
		result := parseBeadsVersion(tt.input)
		if result != tt.expected {
			t.Errorf("parseBeadsVersion(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestParseMiniBeadsVersion(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"mb version 0.21.3", "0.21.3"},
		{"bd version 0.21.3 (minibeads shim)", ""},
		{"bd version 1.0.5", ""},
		{"some other output", ""},
	}

	for _, tt := range tests {
		result := parseMiniBeadsVersion(tt.input)
		if result != tt.expected {
			t.Errorf("parseMiniBeadsVersion(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestRequestedIssueTrackerBackend(t *testing.T) {
	tests := []struct {
		env      string
		expected IssueTrackerBackend
	}{
		{"", IssueTrackerBackendDefault},
		{"beads", IssueTrackerBackendDefault},
		{"bd", IssueTrackerBackendDefault},
		{"upstream", IssueTrackerBackendDefault},
		{"mb", IssueTrackerBackendMinibeads},
		{"minibeads", IssueTrackerBackendMinibeads},
		{"MINIBEADS", IssueTrackerBackendMinibeads},
	}

	for _, tt := range tests {
		t.Run(tt.env, func(t *testing.T) {
			t.Setenv("GT_BEADS_BACKEND", tt.env)
			t.Setenv("GT_ISSUE_TRACKER_BACKEND", "")
			if got := RequestedIssueTrackerBackend(); got != tt.expected {
				t.Fatalf("RequestedIssueTrackerBackend() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestRequestedIssueTrackerBackend_GenericEnvTakesPrecedence(t *testing.T) {
	t.Setenv("GT_BEADS_BACKEND", "minibeads")
	t.Setenv("GT_ISSUE_TRACKER_BACKEND", "beads")

	if got := RequestedIssueTrackerBackend(); got != IssueTrackerBackendDefault {
		t.Fatalf("RequestedIssueTrackerBackend() = %q, want default beads backend", got)
	}
}

func TestEffectiveIssueTrackerBackend_ReadsTownConfig(t *testing.T) {
	t.Setenv("GT_BEADS_BACKEND", "")
	t.Setenv("GT_ISSUE_TRACKER_BACKEND", "")

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{
  "type": "town",
  "version": 1,
  "name": "test-town",
  "issue_tracker_backend": "minibeads",
  "created_at": "2026-07-01T00:00:00Z"
}`), 0600); err != nil {
		t.Fatal(err)
	}

	if got := EffectiveIssueTrackerBackend(townRoot); got != IssueTrackerBackendMinibeads {
		t.Fatalf("EffectiveIssueTrackerBackend() = %q, want minibeads", got)
	}
}

func TestEffectiveIssueTrackerBackend_EnvOverridesTownConfig(t *testing.T) {
	t.Setenv("GT_BEADS_BACKEND", "")
	t.Setenv("GT_ISSUE_TRACKER_BACKEND", "beads")

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{
  "type": "town",
  "version": 1,
  "name": "test-town",
  "issue_tracker_backend": "minibeads",
  "created_at": "2026-07-01T00:00:00Z"
}`), 0600); err != nil {
		t.Fatal(err)
	}

	if got := EffectiveIssueTrackerBackend(townRoot); got != IssueTrackerBackendDefault {
		t.Fatalf("EffectiveIssueTrackerBackend() = %q, want default beads backend", got)
	}
}

func TestEffectiveIssueTrackerBackend_DoesNotInferMiniBeadsFromPathOnly(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeIssueTrackerVersion(t, fakeDir, "mb", "mb version 0.21.4")
	t.Setenv("PATH", fakeDir)
	t.Setenv("GT_BEADS_BACKEND", "")
	t.Setenv("GT_ISSUE_TRACKER_BACKEND", "")

	if got := EffectiveIssueTrackerBackend(t.TempDir()); got != IssueTrackerBackendDefault {
		t.Fatalf("EffectiveIssueTrackerBackend() = %q, want default beads backend", got)
	}
}

func TestEffectiveIssueTrackerBackend_DetectsUpstreamBeadsFromPath(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeIssueTrackerVersion(t, fakeDir, "bd", "bd version "+MinBeadsVersion)
	t.Setenv("PATH", fakeDir)
	t.Setenv("GT_BEADS_BACKEND", "")
	t.Setenv("GT_ISSUE_TRACKER_BACKEND", "")

	if got := EffectiveIssueTrackerBackend(t.TempDir()); got != IssueTrackerBackendDefault {
		t.Fatalf("EffectiveIssueTrackerBackend() = %q, want default beads backend", got)
	}
}

func TestCheckBeadsForBackend_MiniBeadsUsesMB(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeIssueTrackerVersion(t, fakeDir, "mb", "mb version 0.21.4")
	t.Setenv("PATH", fakeDir)

	status, version := CheckBeadsForBackend(IssueTrackerBackendMinibeads)
	if status != BeadsOK {
		t.Fatalf("CheckBeadsForBackend(minibeads) status = %v, want BeadsOK", status)
	}
	if version != "minibeads 0.21.4" {
		t.Fatalf("CheckBeadsForBackend(minibeads) version = %q", version)
	}
}

func TestCheckBeadsForBackend_MiniBeadsDoesNotUseBD(t *testing.T) {
	fakeDir := t.TempDir()
	writeFakeIssueTrackerVersion(t, fakeDir, "bd", "bd version "+MinBeadsVersion)
	t.Setenv("PATH", fakeDir)

	status, _ := CheckBeadsForBackend(IssueTrackerBackendMinibeads)
	if status != BeadsNotFound {
		t.Fatalf("CheckBeadsForBackend(minibeads) status = %v, want BeadsNotFound without mb", status)
	}
}

func writeFakeIssueTrackerVersion(t *testing.T, dir, name, output string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		path := filepath.Join(dir, name+".bat")
		if err := os.WriteFile(path, []byte("@echo off\r\necho "+output+"\r\n"), 0755); err != nil {
			t.Fatal(err)
		}
		return
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho '"+output+"'\n"), 0755); err != nil {
		t.Fatal(err)
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b     string
		expected int
	}{
		{"0.55.4", "0.55.4", 0},
		{"0.55.4", "0.54.0", 1},
		{"0.54.0", "0.55.4", -1},
		{"1.0.0", "0.99.99", 1},
		{"0.55.5", "0.55.4", 1},
		{"0.55.4", "0.55.5", -1},
	}

	for _, tt := range tests {
		result := CompareVersions(tt.a, tt.b)
		if result != tt.expected {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", tt.a, tt.b, result, tt.expected)
		}
	}
}

func TestCheckBeads(t *testing.T) {
	// This test depends on whether bd is installed in the test environment
	status, version := CheckBeads()

	// We expect bd to be installed in dev environment
	if status == BeadsNotFound {
		t.Skip("bd not installed, skipping integration test")
	}

	if status == BeadsOK && version == "" {
		t.Error("CheckBeads returned BeadsOK but empty version")
	}

	t.Logf("CheckBeads: status=%d, version=%s", status, version)
}
