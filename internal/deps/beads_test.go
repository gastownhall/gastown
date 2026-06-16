package deps

import (
	"os"
	"os/exec"
	"path/filepath"
	rdebug "runtime/debug"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
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

func TestBeadsInstallPathPinned(t *testing.T) {
	if strings.Contains(BeadsInstallPath, "@latest") {
		t.Fatalf("BeadsInstallPath must not float on @latest: %s", BeadsInstallPath)
	}
	want := beadsModulePath + "/cmd/bd@" + RequiredBeadsModuleVersion
	if BeadsInstallPath != want {
		t.Fatalf("BeadsInstallPath = %q, want %q", BeadsInstallPath, want)
	}
}

func TestRequiredBeadsModuleVersionMatchesGoMod(t *testing.T) {
	goModPath := findRepoGoMod(t)
	data, err := os.ReadFile(goModPath)
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	parsed, err := modfile.Parse(goModPath, data, nil)
	if err != nil {
		t.Fatalf("parse go.mod: %v", err)
	}
	for _, req := range parsed.Require {
		if req.Mod.Path == beadsModulePath {
			if req.Mod.Version != RequiredBeadsModuleVersion {
				t.Fatalf("RequiredBeadsModuleVersion = %q, go.mod requires %q", RequiredBeadsModuleVersion, req.Mod.Version)
			}
			return
		}
	}
	t.Fatalf("go.mod does not require %s", beadsModulePath)
}

func TestResolveBeadsPathSkipsUnverifiableShadow(t *testing.T) {
	realBD, err := exec.LookPath("bd")
	if err != nil {
		t.Skip("bd not installed, skipping resolver integration test")
	}
	status, version := checkBeadsAtPath(realBD)
	if status != BeadsOK {
		t.Skipf("installed bd is not compatible (%v %q), skipping resolver integration test", status, version)
	}

	shadowDir := t.TempDir()
	shadow := filepath.Join(shadowDir, "bd")
	if err := os.WriteFile(shadow, []byte("#!/bin/sh\necho 'bd version 9.9.9'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shadowDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	resolved, err := ResolveBeadsPath()
	if err != nil {
		t.Fatalf("ResolveBeadsPath: %v", err)
	}
	if resolved == shadow {
		t.Fatalf("ResolveBeadsPath returned unverifiable shadow bd %q", resolved)
	}
}

func TestBeadsModuleVersionFromBuildInfo(t *testing.T) {
	tests := []struct {
		name   string
		info   *rdebug.BuildInfo
		want   string
		wantOK bool
	}{
		{
			name: "main module",
			info: &rdebug.BuildInfo{
				Main: rdebug.Module{Path: beadsModulePath, Version: RequiredBeadsModuleVersion},
			},
			want:   RequiredBeadsModuleVersion,
			wantOK: true,
		},
		{
			name: "dependency module",
			info: &rdebug.BuildInfo{
				Main: rdebug.Module{Path: "github.com/steveyegge/gastown"},
				Deps: []*rdebug.Module{{Path: beadsModulePath, Version: "v1.0.6"}},
			},
			want:   "v1.0.6",
			wantOK: true,
		},
		{
			name: "replace version wins",
			info: &rdebug.BuildInfo{
				Main: rdebug.Module{
					Path:    beadsModulePath,
					Version: "v1.0.5",
					Replace: &rdebug.Module{Path: "example.com/beads-fork", Version: RequiredBeadsModuleVersion},
				},
			},
			want:   RequiredBeadsModuleVersion,
			wantOK: true,
		},
		{
			name: "local replace non fatal",
			info: &rdebug.BuildInfo{
				Main: rdebug.Module{
					Path:    beadsModulePath,
					Version: "v1.0.5",
					Replace: &rdebug.Module{Path: "../beads"},
				},
			},
		},
		{
			name: "devel non fatal",
			info: &rdebug.BuildInfo{
				Main: rdebug.Module{Path: beadsModulePath, Version: "(devel)"},
			},
		},
		{
			name: "nil non fatal",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := beadsModuleVersionFromBuildInfo(tt.info)
			if ok != tt.wantOK || got != tt.want {
				t.Fatalf("beadsModuleVersionFromBuildInfo() = %q, %v; want %q, %v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestBeadsModuleVersionMismatch(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{"v1.0.5", true},
		{RequiredBeadsModuleVersion, false},
		{"v1.0.6", true},
		{"v1.0.7", true},
		{"(devel)", false},
		{"", false},
	}

	for _, tt := range tests {
		if got := beadsModuleVersionMismatch(tt.version); got != tt.want {
			t.Fatalf("beadsModuleVersionMismatch(%q) = %v, want %v", tt.version, got, tt.want)
		}
	}
}

func findRepoGoMod(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		candidate := filepath.Join(dir, "go.mod")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod")
		}
		dir = parent
	}
}
