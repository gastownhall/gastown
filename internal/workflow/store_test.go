//go:build integration && !windows

package workflow

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/gastown/internal/testutil"
)

func openTestDolt(t *testing.T) *sql.DB {
	t.Helper()
	testutil.RequireDoltContainer(t)
	root, err := sql.Open("mysql", "root:@tcp("+testutil.DoltContainerAddr()+")/")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	name := fmt.Sprintf("gtb_wf_test_%d", time.Now().UnixNano())
	if _, err := root.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() { _, _ = root.Exec("DROP DATABASE " + name) })
	db, err := sql.Open("mysql", "root:@tcp("+testutil.DoltContainerAddr()+")/"+name+"?parseTime=true")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestAdvance_ConcurrentSingleTransition(t *testing.T) {
	db := openTestDolt(t)
	s, err := NewDoltStore(db)
	if err != nil {
		t.Fatalf("NewDoltStore: %v", err)
	}
	const n = 10
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = s.Transition("it-1", "spec", "plan", fmt.Sprintf("actor-%d", i))
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrStepConflict):
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("successes = %d, want 1 (errs=%v)", ok, errs)
	}
	trs, err := s.Transitions("it-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(trs) != 1 {
		t.Fatalf("transitions = %d, want 1: %+v", len(trs), trs)
	}
}

func TestDoltStore_RoundTrip(t *testing.T) {
	db := openTestDolt(t)
	s, err := NewDoltStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewDoltStore(db); err != nil {
		t.Fatalf("second NewDoltStore (idempotent tables): %v", err)
	}
	at := time.Date(2026, 10, 22, 1, 2, 3, 456789000, time.UTC)
	a := Approval{ItemID: "it-1", Artifact: "spec", SHA: "abc", Verdict: "approve", Role: "human", Source: "cli", Channel: "terminal", Actor: "me", At: at}
	if err := s.AddApproval(a); err != nil {
		t.Fatal(err)
	}
	r := TDDRun{ItemID: "it-1", PlanStep: "1", Phase: "red", Cmd: "go test", Exit: 1, TestFilesSHA: "t", TestFiles: []string{"a_test.go", "b/c_test.go"}, Tail: "FAIL", Source: "cli", Provenance: "p", At: at}
	if err := s.AddTDD(r); err != nil {
		t.Fatal(err)
	}
	c := CheckRun{ItemID: "it-1", CmdSetSHA: "c", Passed: true, Tail: "ok", Source: "cli", Provenance: "p", At: at}
	if err := s.AddCheck(c); err != nil {
		t.Fatal(err)
	}
	as, err := s.Approvals("it-1")
	if err != nil || len(as) != 1 || as[0] != a {
		t.Fatalf("approvals = %+v, %v", as, err)
	}
	rs, err := s.TDD("it-1")
	if err != nil || len(rs) != 1 || !reflect.DeepEqual(rs[0], r) {
		t.Fatalf("tdd = %+v, %v", rs, err)
	}
	cs, err := s.Checks("it-1")
	if err != nil || len(cs) != 1 || cs[0] != c {
		t.Fatalf("checks = %+v, %v", cs, err)
	}
	if got, _ := s.Approvals("other"); len(got) != 0 {
		t.Fatalf("other item approvals = %+v", got)
	}
	if err := s.Transition("it-1", "spec", "plan", "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.Transition("it-1", "spec", "plan", "me"); !errors.Is(err, ErrStepConflict) {
		t.Fatalf("stale transition err = %v", err)
	}
	if err := s.Transition("it-1", "plan", "execute", "me"); err != nil {
		t.Fatal(err)
	}
	trs, err := s.Transitions("it-1")
	if err != nil || len(trs) != 2 || trs[1].To != "execute" {
		t.Fatalf("transitions = %+v, %v", trs, err)
	}
}
