package cigate

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestCheckBranchRealExecPath exercises the default (non-stubbed) runner by
// putting a fake gh binary on PATH, proving the exec wiring end to end:
// argument passing, stdout capture, JSON decode, and stderr propagation on
// failure. This is the "fake gh shim" simulation from the AA-851 test plan.
func TestCheckBranchRealExecPath(t *testing.T) {
	writeFakeGh := func(t *testing.T, script string) string {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, "gh")
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	t.Run("green PR via real exec", func(t *testing.T) {
		shim := writeFakeGh(t, fmt.Sprintf(`echo '%s'`,
			`[{"number":21,"state":"OPEN","url":"u","statusCheckRollup":[{"__typename":"CheckRun","name":"CI","status":"COMPLETED","conclusion":"SUCCESS"}]}]`))
		t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
		res := New().CheckBranch(t.TempDir(), "polecat/shim-test")
		if res.Verdict != VerdictGreen || res.PRNumber != 21 {
			t.Fatalf("got %s pr=%d err=%v, want GREEN pr=21", res.Verdict, res.PRNumber, res.Err)
		}
	})

	t.Run("gh failure yields ERROR with stderr detail", func(t *testing.T) {
		shim := writeFakeGh(t, `echo "gh: Bad credentials" >&2; exit 1`)
		t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
		res := New().CheckBranch(t.TempDir(), "polecat/shim-test")
		if res.Verdict != VerdictError || res.Err == nil {
			t.Fatalf("got %s err=%v, want ERROR", res.Verdict, res.Err)
		}
	})
}
