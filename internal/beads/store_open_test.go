package beads

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// isolateStoreOpenEnv points every Dolt selector at a dead endpoint so a guard
// regression fails on connect instead of touching a real server.
func isolateStoreOpenEnv(t *testing.T) {
	t.Helper()
	t.Setenv("BEADS_DOLT_SERVER_HOST", "127.0.0.1")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "1")
	t.Setenv("BEADS_DOLT_PORT", "1")
	t.Setenv("BEADS_DOLT_AUTO_START", "0")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
}

func TestOpenStoreFromConfig_RefusesMissingMetadata(t *testing.T) {
	isolateStoreOpenEnv(t)
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	store, err := OpenStoreFromConfig(context.Background(), beadsDir)
	if store != nil {
		_ = store.Close()
		t.Fatal("OpenStoreFromConfig returned a store for a .beads dir without metadata.json")
	}
	if !errors.Is(err, ErrStoreDatabaseNotConfigured) {
		t.Fatalf("err = %v, want ErrStoreDatabaseNotConfigured", err)
	}
	for _, want := range []string{"metadata.json", "gt doctor --fix", "bd init"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestOpenStoreFromConfig_RefusesMetadataWithoutDatabase(t *testing.T) {
	isolateStoreOpenEnv(t)
	// The SDK lets BEADS_DOLT_SERVER_DATABASE override metadata.json, but the
	// guard must still judge the directory itself.
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "somedb")
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	metadata := `{"backend":"dolt","dolt_mode":"server","dolt_database":"  "}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := OpenStoreFromConfig(context.Background(), beadsDir)
	if !errors.Is(err, ErrStoreDatabaseNotConfigured) {
		t.Fatalf("err = %v, want ErrStoreDatabaseNotConfigured", err)
	}
	if !strings.Contains(err.Error(), "dolt_database") {
		t.Errorf("error %q does not mention dolt_database", err)
	}
}

func TestOpenStoreFromConfig_DoesNotFollowRedirect(t *testing.T) {
	isolateStoreOpenEnv(t)
	root := t.TempDir()
	target := filepath.Join(root, "target", ".beads")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "metadata.json"), []byte(`{"dolt_database":"target"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	redirectDir := filepath.Join(root, "worktree", ".beads")
	if err := os.MkdirAll(redirectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(redirectDir, "redirect"), []byte(target+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The SDK reads <beadsDir>/metadata.json verbatim, so the guard must not
	// approve a redirect-only directory on the strength of its target.
	_, err := OpenStoreFromConfig(context.Background(), redirectDir)
	if !errors.Is(err, ErrStoreDatabaseNotConfigured) {
		t.Fatalf("err = %v, want ErrStoreDatabaseNotConfigured", err)
	}
}

func TestBeadsOpenStore_RefusesStrayBeadsDir(t *testing.T) {
	isolateStoreOpenEnv(t)
	workDir := t.TempDir()
	beadsDir := filepath.Join(workDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	_, cleanup, err := NewWithBeadsDir(workDir, beadsDir).OpenStore(context.Background())
	if cleanup != nil {
		cleanup()
	}
	if !errors.Is(err, ErrStoreDatabaseNotConfigured) {
		t.Fatalf("err = %v, want ErrStoreDatabaseNotConfigured", err)
	}
}

// TestNoDirectSDKOpenFromConfig keeps every SDK OpenFromConfig call behind the
// guarded wrappers in this file's package, so no production path can create a
// default "beads" database for an uninitialized .beads directory.
func TestNoDirectSDKOpenFromConfig(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	allowed := filepath.Join("internal", "beads", "store.go")

	var violations []string
	for _, top := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(repoRoot, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(repoRoot, path)
			if rel == allowed {
				return nil
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", rel, err)
			}
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "OpenFromConfig" {
					violations = append(violations, fset.Position(call.Pos()).String())
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", top, err)
		}
	}

	if len(violations) > 0 {
		t.Fatalf("do not call beadsdk.OpenFromConfig directly; it creates the database when metadata.json names none. Use beads.OpenStoreFromConfig (or beads.CreateOrOpenStoreFromConfig on init paths):\n%s",
			strings.Join(violations, "\n"))
	}
}
