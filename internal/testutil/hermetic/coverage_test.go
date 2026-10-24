package hermetic

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestEveryTestPackageIsHermetic fails when a package in this module has tests
// but no TestMain that isolates it. Any test binary that skips isolation
// inherits the caller's session and can act on the live town, so new test
// packages must add:
//
//	func TestMain(m *testing.M) { hermetic.Main(m) }
func TestEveryTestPackageIsHermetic(t *testing.T) {
	root := moduleRoot()
	if root == "" {
		t.Fatal("module root not found")
	}
	self, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	testFiles := map[string][]string{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "testdata" || name == "vendor" || name == "node_modules") {
				return filepath.SkipDir
			}
			if path != root {
				if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
					return filepath.SkipDir // nested module; cannot import this package
				}
			}
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			dir := filepath.Dir(path)
			testFiles[dir] = append(testFiles[dir], path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking module: %v", err)
	}

	var missing []string
	for dir, files := range testFiles {
		if dir == self {
			continue
		}
		if !anyFileContains(t, files, "hermetic.Main(", "hermetic.Isolate(") {
			rel, _ := filepath.Rel(root, dir)
			missing = append(missing, rel)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("test packages without a hermetic TestMain (add `func TestMain(m *testing.M) { hermetic.Main(m) }`):\n  %s",
			strings.Join(missing, "\n  "))
	}
}

func anyFileContains(t *testing.T, files []string, needles ...string) bool {
	t.Helper()
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		for _, n := range needles {
			if bytes.Contains(data, []byte(n)) {
				return true
			}
		}
	}
	return false
}
