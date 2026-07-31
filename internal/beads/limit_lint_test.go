package beads_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// TestNoUnlimitedBdList is a lint, not a unit test.
//
// bd's "-n, --limit int" defaults to 50. A top-level `bd list` with no explicit
// limit silently returns the first 50 rows -- no error, no warning, no truncation
// marker -- and the caller treats that page as the complete set. The town holds
// >100 agent beads, so this is not theoretical.
//
// This class was fixed twice one call site at a time (14396bbc / hq-p1jeb, then
// d85e233a / ds-gxbr) and survived both, because nobody swept. This lint exists so
// it cannot silently regrow: a new direct `bd list` without a limit fails the build.
// See hq-is5vd.
//
// SCOPE. The lint flags only DIRECT exec of a top-level `bd list` with literal args.
// It deliberately does NOT flag:
//   - `bd mol wisp list`, `bd dep list`, `bd kv list` and friends -- these are
//     subcommands that do not accept --limit at all. Passing --limit to
//     `bd mol wisp list` exits 1 with "unknown flag" and returns NO data. Wisp
//     listing is separately capped at 5000 by bd's runWispList, not 50.
//   - calls that pass a variadic slice (`args...`), because those flow through a
//     chokepoint (beads.run, mail.runBdCommand, web fetcher/api, witness BdCli,
//     cmd.runBdJSON) that applies beads.InjectDefaultLimit for them.
func TestNoUnlimitedBdList(t *testing.T) {
	root := repoRoot(t)

	var violations []string
	err := filepath.Walk(filepath.Join(root, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			// A file we cannot parse is not a lint failure; the compiler owns that.
			return nil //nolint:nilerr // parse errors are the build's problem, not this lint's
		}
		rel, _ := filepath.Rel(root, path)

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isExecCommand(call) {
				return true
			}
			// A variadic spread means the args were built elsewhere and go
			// through a chokepoint that injects the limit.
			if call.Ellipsis.IsValid() {
				return true
			}
			args := call.Args
			if isCommandContext(call) {
				if len(args) == 0 {
					return true
				}
				args = args[1:] // drop ctx
			}
			if len(args) < 2 || !isBdBinary(args[0]) {
				return true
			}
			// Top-level `bd list` only: the first literal arg must be "list".
			// "mol"/"dep"/"kv" subcommands take no --limit.
			first, ok := stringLit(args[1])
			if !ok || first != "list" {
				return true
			}
			if beads.HasExplicitLimit(literalArgs(args[2:])) {
				return true
			}
			violations = append(violations,
				rel+":"+strconv.Itoa(fset.Position(call.Pos()).Line))
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking internal/: %v", err)
	}

	if len(violations) > 0 {
		t.Errorf("found %d direct `bd list` call(s) with no explicit --limit.\n"+
			"bd truncates at 50 rows silently, so these return a partial set that the\n"+
			"caller treats as complete. Add --limit=0 for a full scan, or an explicit\n"+
			"--limit=N if the query is intentionally bounded (a TUI page, a 'first N'\n"+
			"probe). See hq-is5vd.\n\n  %s",
			len(violations), strings.Join(violations, "\n  "))
	}
}

// isExecCommand reports whether call is exec.Command or exec.CommandContext.
func isExecCommand(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "exec" {
		return false
	}
	return sel.Sel.Name == "Command" || sel.Sel.Name == "CommandContext"
}

func isCommandContext(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "CommandContext"
}

// isBdBinary reports whether expr names the bd binary: the literal "bd", or an
// identifier/field such as bdPath, bdBin, d.bdPath.
func isBdBinary(expr ast.Expr) bool {
	if s, ok := stringLit(expr); ok {
		return s == "bd" || strings.HasSuffix(s, "/bd")
	}
	var name string
	switch e := expr.(type) {
	case *ast.Ident:
		name = e.Name
	case *ast.SelectorExpr:
		name = e.Sel.Name
	default:
		return false
	}
	return strings.Contains(strings.ToLower(name), "bd")
}

// literalArgs returns the string-literal subset of exprs. Non-literal args
// (e.g. "--rig="+rigName) cannot carry a limit we could recognize anyway.
func literalArgs(exprs []ast.Expr) []string {
	var out []string
	for _, e := range exprs {
		if s, ok := stringLit(e); ok {
			out = append(out, s)
		}
	}
	return out
}

func stringLit(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate repo root (no go.mod found)")
		}
		dir = parent
	}
}
