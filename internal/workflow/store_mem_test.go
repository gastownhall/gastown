package workflow

import (
	"errors"
	"testing"

	"github.com/go-sql-driver/mysql"
)

func TestMemStore_TransitionConflict(t *testing.T) {
	s := NewMemStore()
	if err := s.Transition("it-1", "spec", "plan", "tester"); err != nil {
		t.Fatalf("first transition: %v", err)
	}
	if err := s.Transition("it-1", "spec", "plan", "tester"); !errors.Is(err, ErrStepConflict) {
		t.Fatalf("second transition err = %v, want ErrStepConflict", err)
	}
	trs, err := s.Transitions("it-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(trs) != 1 || trs[0].From != "spec" || trs[0].To != "plan" || trs[0].Actor != "tester" {
		t.Fatalf("transitions = %+v", trs)
	}
	if err := s.Transition("it-1", "plan", "execute", "tester"); err != nil {
		t.Fatalf("chained transition: %v", err)
	}
}

func TestRaceErr_MapsLostRaces(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&mysql.MySQLError{Number: 1062, Message: "Duplicate entry"}, true},
		{&mysql.MySQLError{Number: 1105, Message: "commit: constraint violation"}, true},
		{&mysql.MySQLError{Number: 1213, Message: "serialization failure: this transaction conflicts with a committed transaction from another client, try restarting transaction"}, true},
		{&mysql.MySQLError{Number: 1146, Message: "table not found"}, false},
		{errors.New("plain"), false},
	} {
		if got := errors.Is(raceErr("it-1", "commit transition", tc.err), ErrStepConflict); got != tc.want {
			t.Errorf("raceErr(%v) conflict = %v, want %v", tc.err, got, tc.want)
		}
	}
}
