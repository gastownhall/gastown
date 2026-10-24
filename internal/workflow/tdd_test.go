package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// fakeRunner returns scripted results in order and records every call.
type fakeRunner struct {
	results []fakeResult
	calls   []fakeCall
}

type fakeResult struct {
	exit int
	tail string
	err  error
}

type fakeCall struct {
	dir  string
	argv []string
}

func (f *fakeRunner) Run(_ context.Context, dir string, argv []string) (int, string, error) {
	f.calls = append(f.calls, fakeCall{dir, append([]string(nil), argv...)})
	if len(f.results) == 0 {
		return 0, "", nil
	}
	r := f.results[0]
	f.results = f.results[1:]
	return r.exit, r.tail, r.err
}

var fixedNow = time.Date(2026, 10, 22, 9, 0, 0, 0, time.FixedZone("x", 3600))

func tddEnv(t *testing.T) (GateEnv, string) {
	t.Helper()
	env := newTestEnv(t)
	env.Now = func() time.Time { return fixedNow }
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a_test.go"), "package a\n")
	writeFile(t, filepath.Join(dir, "sub", "b_test.go"), "package b\n")
	return env, dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

var testArgv = []string{"go", "test", "./x"}
var testFileList = []string{"sub/b_test.go", "a_test.go"}

func TestRed_PassingTestRefused(t *testing.T) {
	env, dir := tddEnv(t)
	r := &fakeRunner{results: []fakeResult{{exit: 0, tail: "ok"}}}
	run, err := Red(env, r, "it-1", "1", dir, testArgv, testFileList)
	if err == nil {
		t.Fatal("Red with passing test: want error")
	}
	if run.Phase != "refused" || run.Tail != "test passed before implementation" {
		t.Fatalf("run = %+v", run)
	}
	runs, _ := env.Store.TDD("it-1")
	if len(runs) != 1 || runs[0].Phase != "refused" {
		t.Fatalf("stored = %+v", runs)
	}
}

func TestRed_RecordsVerifiedRed(t *testing.T) {
	env, dir := tddEnv(t)
	r := &fakeRunner{results: []fakeResult{{exit: 1, tail: "FAIL"}}}
	run, err := Red(env, r, "it-1", "1", dir, testArgv, testFileList)
	if err != nil {
		t.Fatalf("Red: %v", err)
	}
	if run.Phase != "red" || run.Provenance != "verified" || run.Source != "gtb" || run.Exit != 1 || run.Tail != "FAIL" {
		t.Fatalf("run = %+v", run)
	}
	var argv []string
	if err := json.Unmarshal([]byte(run.Cmd), &argv); err != nil || !reflect.DeepEqual(argv, testArgv) {
		t.Fatalf("Cmd = %q (%v)", run.Cmd, err)
	}
	if !run.At.Equal(fixedNow) || run.At.Location() != time.UTC {
		t.Fatalf("At = %v, want %v in UTC", run.At, fixedNow)
	}
	if !reflect.DeepEqual(run.TestFiles, []string{"a_test.go", "sub/b_test.go"}) {
		t.Fatalf("TestFiles = %v", run.TestFiles)
	}
	want, err := testFilesSHA(dir, testFileList)
	if err != nil || run.TestFilesSHA != want || want == "" {
		t.Fatalf("TestFilesSHA = %q, want %q (%v)", run.TestFilesSHA, want, err)
	}
	if len(r.calls) != 1 || r.calls[0].dir != dir || !reflect.DeepEqual(r.calls[0].argv, testArgv) {
		t.Fatalf("calls = %+v", r.calls)
	}
}

func TestRed_MissingTestFileErrors(t *testing.T) {
	env, dir := tddEnv(t)
	r := &fakeRunner{results: []fakeResult{{exit: 1}}}
	if _, err := Red(env, r, "it-1", "1", dir, testArgv, []string{"nope_test.go"}); err == nil {
		t.Fatal("want error for missing test file")
	}
	if len(r.calls) != 0 {
		t.Fatal("runner must not run when a test file is missing")
	}
	if runs, _ := env.Store.TDD("it-1"); len(runs) != 0 {
		t.Fatalf("stored = %+v", runs)
	}
}

func TestRed_EmptyArgvErrors(t *testing.T) {
	env, dir := tddEnv(t)
	if _, err := Red(env, &fakeRunner{}, "it-1", "1", dir, nil, testFileList); err == nil {
		t.Fatal("want error for empty argv")
	}
}

func TestTestFilesSHA_Format(t *testing.T) {
	_, dir := tddEnv(t)
	got, err := testFilesSHA(dir, []string{"sub/b_test.go", "a_test.go"})
	if err != nil {
		t.Fatal(err)
	}
	h := func(s string) string { return sha256Hex([]byte(s)) }
	want := h("a_test.go\x00" + h("package a\n") + "\n" + "sub/b_test.go\x00" + h("package b\n") + "\n")
	if got != want {
		t.Fatalf("sha = %s, want %s", got, want)
	}
}

func TestGreen_TestFileChangedRefused(t *testing.T) {
	env, dir := tddEnv(t)
	r := &fakeRunner{results: []fakeResult{{exit: 1, tail: "FAIL"}, {exit: 0, tail: "ok"}}}
	if _, err := Red(env, r, "it-1", "1", dir, testArgv, testFileList); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "a_test.go"), "package a\n// weakened\n")
	run, err := Green(env, r, "it-1", "1", dir)
	if err == nil {
		t.Fatal("Green after test change: want error")
	}
	if run.Phase != "refused" || run.Tail != "test files changed since red" {
		t.Fatalf("run = %+v", run)
	}
	if len(r.calls) != 1 {
		t.Fatalf("green must not run the command after a test change; calls = %d", len(r.calls))
	}
}

func TestGreen_TestFileRemovedRefused(t *testing.T) {
	env, dir := tddEnv(t)
	r := &fakeRunner{results: []fakeResult{{exit: 1}}}
	if _, err := Red(env, r, "it-1", "1", dir, testArgv, testFileList); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "a_test.go")); err != nil {
		t.Fatal(err)
	}
	run, err := Green(env, r, "it-1", "1", dir)
	if err == nil || run.Phase != "refused" || run.Tail != "test files changed since red" {
		t.Fatalf("run = %+v, err = %v", run, err)
	}
}

func TestGreen_FailingRefused(t *testing.T) {
	env, dir := tddEnv(t)
	r := &fakeRunner{results: []fakeResult{{exit: 1}, {exit: 2, tail: "still FAIL"}}}
	if _, err := Red(env, r, "it-1", "1", dir, testArgv, testFileList); err != nil {
		t.Fatal(err)
	}
	run, err := Green(env, r, "it-1", "1", dir)
	if err == nil || run.Phase != "refused" || run.Exit != 2 || !strings.Contains(run.Tail, "still FAIL") {
		t.Fatalf("run = %+v, err = %v", run, err)
	}
}

func TestGreen_RerunsRedArgvAndRecordsGreen(t *testing.T) {
	env, dir := tddEnv(t)
	r := &fakeRunner{results: []fakeResult{{exit: 1}, {exit: 1}, {exit: 0, tail: "PASS"}}}
	if _, err := Red(env, r, "it-1", "1", dir, []string{"old"}, testFileList); err != nil {
		t.Fatal(err)
	}
	if _, err := Red(env, r, "it-1", "1", dir, testArgv, testFileList); err != nil {
		t.Fatal(err)
	}
	run, err := Green(env, r, "it-1", "1", dir)
	if err != nil {
		t.Fatalf("Green: %v", err)
	}
	if run.Phase != "green" || run.Provenance != "verified" || run.Source != "gtb" || run.Tail != "PASS" || run.At.Location() != time.UTC {
		t.Fatalf("run = %+v", run)
	}
	if got := r.calls[2]; got.dir != dir || !reflect.DeepEqual(got.argv, testArgv) {
		t.Fatalf("green call = %+v, want latest red argv", got)
	}
	runs, _ := env.Store.TDD("it-1")
	if !redThenGreen(runs, "1") {
		t.Fatalf("store has no red then green: %+v", runs)
	}
}

func TestGreen_NoRedErrors(t *testing.T) {
	env, dir := tddEnv(t)
	r := &fakeRunner{}
	if _, err := Green(env, r, "it-1", "1", dir); err == nil {
		t.Fatal("want error without a red")
	}
	if len(r.calls) != 0 {
		t.Fatal("runner must not run without a red")
	}
}

func TestExecRunner_RealCommand(t *testing.T) {
	out := filepath.Join(t.TempDir(), "outputs")
	r := ExecRunner{OutputDir: out}
	exit, tail, err := r.Run(context.Background(), t.TempDir(), []string{"sh", "-c", "echo hi; exit 3"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exit != 3 || tail != "hi\n" {
		t.Fatalf("exit = %d, tail = %q", exit, tail)
	}
	st, err := os.Stat(out)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("output dir: %v %v", st, err)
	}
	logs, _ := filepath.Glob(filepath.Join(out, "*.log"))
	if len(logs) != 1 || strings.Contains(filepath.Base(logs[0]), ":") {
		t.Fatalf("logs = %v", logs)
	}
	if b, _ := os.ReadFile(logs[0]); string(b) != "hi\n" {
		t.Fatalf("log = %q", b)
	}
}

func TestExecRunner_StartFailureIsError(t *testing.T) {
	r := ExecRunner{OutputDir: t.TempDir()}
	if _, _, err := r.Run(context.Background(), t.TempDir(), []string{"/nonexistent/gtb-binary"}); err == nil {
		t.Fatal("want error for a command that cannot start")
	}
}

func TestTail_TrimsAtRuneBoundary(t *testing.T) {
	// é is 2 bytes; the trailing "a" makes byte len-1000 fall mid-rune.
	b := []byte(strings.Repeat("a", 10) + strings.Repeat("é", 600) + "a")
	got := tailOf(b)
	if len(got) != 999 || !utf8.ValidString(got) || !strings.HasPrefix(got, "é") || !strings.HasSuffix(string(b), got) {
		t.Fatalf("tail len %d prefix %q", len(got), got[:4])
	}
	if tailOf([]byte("short")) != "short" {
		t.Fatal("short tail")
	}
}

func TestOutputDir(t *testing.T) {
	if got, want := OutputDir("/t", "r", "it-1"), filepath.Join("/t", "r", ".gtb", "items", "it-1", "outputs"); got != want {
		t.Fatalf("OutputDir = %s, want %s", got, want)
	}
}

var badIDs = []string{"../../x", "..", "a..b", "", "-x", ".x", "a/b", "a b", "a\\b"}

func TestValidateID(t *testing.T) {
	for _, id := range badIDs {
		if err := validateID(id); !errors.Is(err, ErrInvalidItemID) {
			t.Errorf("validateID(%q) = %v, want ErrInvalidItemID", id, err)
		}
	}
	for _, id := range []string{"it-1", "gt-abc.1", "A_b", "1", "repro"} {
		if err := validateID(id); err != nil {
			t.Errorf("validateID(%q) = %v", id, err)
		}
	}
}

func TestInvalidIDs_RejectedBeforeAccess(t *testing.T) {
	env, dir := tddEnv(t)
	approvedChecks(env, "true")
	r := &fakeRunner{results: []fakeResult{{exit: 1}}}
	for _, bad := range []string{"../../x", "a..b"} {
		if _, err := Red(env, r, bad, "1", dir, testArgv, testFileList); !errors.Is(err, ErrInvalidItemID) {
			t.Errorf("Red item %q: %v", bad, err)
		}
		if _, err := Red(env, r, "it-1", bad, dir, testArgv, testFileList); !errors.Is(err, ErrInvalidItemID) {
			t.Errorf("Red step %q: %v", bad, err)
		}
		if _, err := Green(env, r, bad, "1", dir); !errors.Is(err, ErrInvalidItemID) {
			t.Errorf("Green item %q: %v", bad, err)
		}
		if _, err := Green(env, r, "it-1", bad, dir); !errors.Is(err, ErrInvalidItemID) {
			t.Errorf("Green step %q: %v", bad, err)
		}
		if _, err := Check(env, r, bad, dir); !errors.Is(err, ErrInvalidItemID) {
			t.Errorf("Check %q: %v", bad, err)
		}
		if _, err := HumanApprove(env, &fakeTerminal{tty: true, answer: true}, noEnv, bad, "spec", "approve", "me"); !errors.Is(err, ErrInvalidItemID) {
			t.Errorf("HumanApprove %q: %v", bad, err)
		}
		if _, err := RecordVerdict(env, bad, "spec", "reviewer", "approve", "nux"); !errors.Is(err, ErrInvalidItemID) {
			t.Errorf("RecordVerdict %q: %v", bad, err)
		}
	}
	if len(r.calls) != 0 {
		t.Fatalf("runner called %d times for invalid ids", len(r.calls))
	}
	if runs, _ := env.Store.TDD("it-1"); len(runs) != 0 {
		t.Fatalf("stored = %+v", runs)
	}
}

func TestGreen_LatestRedByStoreOrder(t *testing.T) {
	env, dir := tddEnv(t)
	sha, err := testFilesSHA(dir, testFileList)
	if err != nil {
		t.Fatal(err)
	}
	files, _ := cleanTestFiles(testFileList)
	add := func(cmd string, at time.Time) {
		if err := env.Store.AddTDD(TDDRun{ItemID: "it-1", PlanStep: "1", Phase: "red", Cmd: cmd, Exit: 1, TestFilesSHA: sha, TestFiles: files, Source: "gtb", Provenance: "verified", At: at}); err != nil {
			t.Fatal(err)
		}
	}
	add(`["first"]`, fixedNow)
	add(`["second"]`, fixedNow.Add(-time.Hour)) // appended later, earlier At
	r := &fakeRunner{results: []fakeResult{{exit: 0}}}
	if _, err := Green(env, r, "it-1", "1", dir); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.calls[0].argv, []string{"second"}) {
		t.Fatalf("green ran %v, want the last appended red", r.calls[0].argv)
	}
}
