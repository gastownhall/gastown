package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRepoFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readRepoFile(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const pkgJSON = `{
  "name": "tbb",
  "version": "0.24.7",
  "nested": {"version": "9.9.9"},
  "scripts": {
    "test": "node --test"
  }
}
`

func TestApplyBump_MinorBothFiles(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "package.json", pkgJSON)
	writeRepoFile(t, dir, ".claude-plugin/plugin.json", `{"name":"tbb","version":"0.24.7","description":"x"}`)
	got, err := ApplyBump(dir, []string{"package.json", ".claude-plugin/plugin.json"}, "minor")
	if err != nil {
		t.Fatalf("ApplyBump: %v", err)
	}
	if got != "0.25.0" {
		t.Fatalf("version = %q, want 0.25.0", got)
	}
	// Only the top-level version value changes; formatting and key order stay.
	if want := strings.Replace(pkgJSON, `"version": "0.24.7"`, `"version": "0.25.0"`, 1); readRepoFile(t, dir, "package.json") != want {
		t.Fatalf("package.json =\n%s\nwant\n%s", readRepoFile(t, dir, "package.json"), want)
	}
	if got := readRepoFile(t, dir, ".claude-plugin/plugin.json"); got != `{"name":"tbb","version":"0.25.0","description":"x"}` {
		t.Fatalf("plugin.json = %s", got)
	}
}

func TestApplyBump_Disagree(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "package.json", pkgJSON)
	writeRepoFile(t, dir, "VERSION", "0.24.8\n")
	_, err := ApplyBump(dir, []string{"package.json", "VERSION"}, "patch")
	if err == nil || !strings.Contains(err.Error(), "version files disagree: ") {
		t.Fatalf("err = %v, want version files disagree", err)
	}
	if readRepoFile(t, dir, "package.json") != pkgJSON || readRepoFile(t, dir, "VERSION") != "0.24.8\n" {
		t.Fatal("files changed despite disagreement")
	}
}

func TestApplyBump_VersionFileAndKinds(t *testing.T) {
	for _, tc := range []struct{ from, bump, want string }{
		{"1.2.3", "patch", "1.2.4"},
		{"1.2.3", "minor", "1.3.0"},
		{"1.2.3", "major", "2.0.0"},
		{"1.2.3-rc.1", "patch", "1.2.4"},
	} {
		dir := t.TempDir()
		writeRepoFile(t, dir, "VERSION", tc.from+"\n")
		got, err := ApplyBump(dir, []string{"VERSION"}, tc.bump)
		if err != nil || got != tc.want {
			t.Fatalf("%s %s: got %q, %v; want %s", tc.from, tc.bump, got, err, tc.want)
		}
		if readRepoFile(t, dir, "VERSION") != tc.want+"\n" {
			t.Fatalf("VERSION = %q", readRepoFile(t, dir, "VERSION"))
		}
	}
}

func TestApplyBump_Errors(t *testing.T) {
	dir := t.TempDir()
	writeRepoFile(t, dir, "VERSION", "1.2.3\n")
	writeRepoFile(t, dir, "bad.json", `{"version": 3}`)
	writeRepoFile(t, dir, "nover.json", `{"nested": {"version": "1.2.3"}}`)
	writeRepoFile(t, dir, "notes.txt", "1.2.3")
	for name, tc := range map[string]struct {
		files []string
		bump  string
		want  string
	}{
		"empty list":   {nil, "patch", "no version_files configured for gated rig"},
		"bad bump":     {[]string{"VERSION"}, "huge", "bump"},
		"non-string":   {[]string{"bad.json"}, "patch", "version"},
		"nested only":  {[]string{"nover.json"}, "patch", "version"},
		"unsupported":  {[]string{"notes.txt"}, "patch", "unsupported"},
		"escape":       {[]string{"../VERSION"}, "patch", "outside"},
		"missing file": {[]string{"nope/VERSION"}, "patch", "nope"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ApplyBump(dir, tc.files, tc.bump); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestReadVersion_AndVersionOf(t *testing.T) {
	if v, err := VersionOf("package.json", []byte(pkgJSON)); err != nil || v != "0.24.7" {
		t.Fatalf("VersionOf = %q, %v", v, err)
	}
	if _, err := VersionOf("VERSION", []byte("not-semver\n")); err == nil {
		t.Fatal("non-semver VERSION accepted")
	}
	if v, err := NextVersion("0.24.7", "major"); err != nil || v != "1.0.0" {
		t.Fatalf("NextVersion = %q, %v", v, err)
	}
	dir := t.TempDir()
	writeRepoFile(t, dir, "package.json", pkgJSON)
	writeRepoFile(t, dir, "VERSION", "0.24.7")
	if v, err := ReadVersion(dir, []string{"package.json", "VERSION"}); err != nil || v != "0.24.7" {
		t.Fatalf("ReadVersion = %q, %v", v, err)
	}
}
