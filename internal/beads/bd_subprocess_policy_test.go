package beads

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestNoAdHocBdSubprocesses(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	internalDir := filepath.Join(repoRoot, "internal")

	var violations []string
	err := filepath.WalkDir(internalDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			rel = path
		}
		if rel == "internal/beads/exec.go" {
			return nil
		}
		violations = append(violations, adHocBDSubprocesses(t, repoRoot, path)...)
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal: %v", err)
	}

	if len(violations) > 0 {
		t.Fatalf("do not spawn bd directly; use beads.Command, Session.Command, or beads.Spawn so host/port/database targeting stays in Authority:\n%s", strings.Join(violations, "\n"))
	}
}

func adHocBDSubprocesses(t *testing.T, repoRoot, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isExecCommandCall(call) {
			return true
		}
		argIndex := 0
		if selectorName(call) == "CommandContext" {
			argIndex = 1
		}
		if len(call.Args) <= argIndex || !isBDCommandArg(call.Args[argIndex]) {
			return true
		}
		pos := fset.Position(call.Pos())
		rel, err := filepath.Rel(repoRoot, pos.Filename)
		if err != nil {
			rel = pos.Filename
		}
		out = append(out, rel+":"+strconv.Itoa(pos.Line))
		return true
	})
	return out
}

func isExecCommandCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || (sel.Sel.Name != "Command" && sel.Sel.Name != "CommandContext") {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == "exec"
}

func selectorName(call *ast.CallExpr) string {
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		return sel.Sel.Name
	}
	return ""
}

func isBDCommandArg(expr ast.Expr) bool {
	switch v := expr.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return false
		}
		value, err := strconv.Unquote(v.Value)
		return err == nil && value == "bd"
	default:
		return false
	}
}
