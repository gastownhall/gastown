package mail

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// [si-zgo] HALF 2 — NO CONSUMER CARRIES ITS OWN COPY OF THE GROUP VOCABULARY.
//
// The parity round trip in groups_test.go passes even if a consumer re-hardcodes the set locally,
// because it only compares the router's behaviour to the derived directory. That is exactly how
// si-75d hid: a gate that runs, passes, and was never testing the thing.
//
// This is the more important half, for the enumerated-vs-derived reason. A hand-placed guard only
// covers what someone anticipated; this walks every consumer and finds the copy nobody declared.
//
// WHAT IT LOOKS FOR: a composite literal or slice of string literals, outside this file's owning
// package, containing two or more group addresses ("@town", "@crew", ...). One mention is a doc
// example or a single-address call; two or more collected together is a second spelling of the set.

// groupTokens is DERIVED from the vocabulary, not typed here — a hand-kept list of what to look
// for would itself be the third copy, and would silently stop covering a group added later.
func groupTokens() []string {
	out := make([]string, 0, len(GroupVocabulary))
	for _, g := range GroupVocabulary {
		out = append(out, "@"+g.Name)
	}
	return out
}

func TestNoConsumerHardcodesTheGroupVocabulary(t *testing.T) {
	tokens := groupTokens()
	if len(tokens) < 2 {
		t.Fatal("fewer than 2 group tokens derived — this scan would pass vacuously")
	}

	root := repoRoot(t)
	var scanned int
	var offenders []string

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			base := info.Name()
			if base == ".git" || base == "vendor" || base == "node_modules" || base == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// The owning package is where the vocabulary is ALLOWED to be spelled.
		if filepath.Dir(path) == filepath.Join(root, "internal", "mail") {
			return nil
		}
		// PRODUCTION CONSUMERS ONLY, and this exclusion is a judgement worth stating rather than
		// hiding. Run against _test.go files this scan flags four sites, and only two are the
		// defect: the other two are validator TEST TABLES (TestIsValidMemberPattern,
		// TestIsValidMailAddress) that must enumerate concrete addresses — including invalid ones
		// — because enumerating examples IS their job. Deriving those from the vocabulary would
		// destroy the test.
		//
		// A guard that reds correct code is one somebody deletes, after which there is no guard.
		// The rule this encodes: production code must COMPOSE the vocabulary; a test may spell an
		// address it is specifically about. The two genuine test-side copies were assertions on
		// the shipped directory list, and those are caught by the parity round trip and by their
		// own failure — they do not need this scan as well.
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		scanned++

		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}

		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			found := map[string]bool{}
			collectGroupStrings(lit, tokens, found)
			if len(found) >= 2 {
				names := make([]string, 0, len(found))
				for k := range found {
					names = append(names, k)
				}
				rel, _ := filepath.Rel(root, path)
				offenders = append(offenders, rel+":"+
					strconv.Itoa(fset.Position(lit.Pos()).Line)+" carries "+strings.Join(names, " "))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk failed: %v", err)
	}

	// DENOMINATOR ASSERT — a walk that examined no files reports "no offenders" and reads exactly
	// like a clean scan. The count is asserted so an empty scan fails loudly instead of passing.
	if scanned == 0 {
		t.Fatal("scanned ZERO .go files — this check examined nothing and its pass is meaningless")
	}
	t.Logf("scanned %d .go file(s) outside internal/mail", scanned)

	if len(offenders) > 0 {
		t.Errorf("consumer(s) carry their own copy of the group vocabulary; compose "+
			"mail.GroupAddresses() / mail.GroupVocabulary instead:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}

// collectGroupStrings records which group tokens appear as string literals inside a composite
// literal — including nested ones, so a []DirectoryEntry{{Address: "@town"}, ...} is caught.
func collectGroupStrings(n ast.Node, tokens []string, found map[string]bool) {
	ast.Inspect(n, func(x ast.Node) bool {
		bl, ok := x.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return true
		}
		v, err := strconv.Unquote(bl.Value)
		if err != nil {
			return true
		}
		for _, tok := range tokens {
			if v == tok || strings.HasPrefix(v, tok+"/") {
				found[tok] = true
			}
		}
		return true
	})
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not locate repo root (no go.mod found walking up)")
	return ""
}
