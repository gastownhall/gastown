package refinery

import (
	"errors"
	"strings"
	"testing"
)

func lookupReturning(status, title string) beadStatusLookup {
	return func(string) (string, []string, string, error) { return status, nil, title, nil }
}

func lookupWithLabels(status string, labels []string, title string) beadStatusLookup {
	return func(string) (string, []string, string, error) { return status, labels, title, nil }
}

// The exact incident: ap-wvk was DEFERRED at 19:45 and merged at 19:56.
func TestCheckFrozen_HaltsTheApWvkCase(t *testing.T) {
	f, err := CheckFrozen("ap-wvk", lookupReturning("deferred", "TV card: Multi-Timeframe High Low Marking Lines [2pHM8wzh]"))
	if err != nil {
		t.Fatal(err)
	}
	if f == nil {
		t.Fatal("a deferred bead did not halt the merge; this is the defect that let ap-wvk land")
	}
	if f.BeadID != "ap-wvk" || f.Status != "deferred" {
		t.Errorf("finding does not identify the bead correctly: %+v", f)
	}
	// The halt message must say what would land, or a human cannot rule on it.
	if !strings.Contains(f.String(), "Multi-Timeframe") {
		t.Errorf("halt message does not name the work: %s", f)
	}
}

func TestCheckFrozen_BlockedAlsoHalts(t *testing.T) {
	f, err := CheckFrozen("ap-x", lookupReturning("blocked", "held by a ruling"))
	if err != nil || f == nil {
		t.Fatalf("blocked should halt; got f=%v err=%v", f, err)
	}
}

// Ordinary work must pass, or the gate halts on everything and gets disabled.
func TestCheckFrozen_QuietOnWorkThatMayProceed(t *testing.T) {
	for _, status := range []string{"open", "in_progress", "hooked", "closed", "", "IN_PROGRESS"} {
		f, err := CheckFrozen("ap-ok", lookupReturning(status, "ordinary work"))
		if err != nil {
			t.Fatalf("status %q: %v", status, err)
		}
		if f != nil {
			t.Errorf("status %q was halted but should merge freely: %s", status, f)
		}
	}
}

func TestCheckFrozen_StatusIsCaseAndSpaceInsensitive(t *testing.T) {
	f, err := CheckFrozen("ap-x", lookupReturning("  DEFERRED  ", "t"))
	if err != nil || f == nil {
		t.Fatalf("a deferred bead must halt regardless of case or padding; got f=%v err=%v", f, err)
	}
}

// A lookup failure must NOT read as "not frozen". Silently treating an unknown
// status as permission to merge is the exact failure this guard exists to close,
// so the caller is forced to decide rather than defaulting to merge.
func TestCheckFrozen_LookupFailureIsAnErrorNotPermission(t *testing.T) {
	f, err := CheckFrozen("ap-x", func(string) (string, []string, string, error) {
		return "", nil, "", errors.New("beads unavailable")
	})
	if err == nil {
		t.Fatal("a lookup failure returned no error; it would read as permission to merge")
	}
	if f != nil {
		t.Error("no finding should be returned alongside an error")
	}
}

// A branch with no bead behind it is not this guard's business.
func TestCheckFrozen_NoBeadIsNotARefusal(t *testing.T) {
	for _, id := range []string{"", "   "} {
		f, err := CheckFrozen(id, func(string) (string, []string, string, error) {
			t.Fatal("lookup must not be called for an empty bead id")
			return "", nil, "", nil
		})
		if err != nil || f != nil {
			t.Errorf("empty bead id should pass quietly; got f=%v err=%v", f, err)
		}
	}
}

// THE GAP THE WITNESS FOUND. A status-keyed freeze is defeated by ordinary work:
// rust resumed ap-mnp minutes after it was deferred, which set it in_progress,
// and in_progress is not a frozen status. Nobody intended to un-freeze anything -
// resuming work is the normal path. Membership must be the fact.
func TestCheckFrozen_LabelHaltsEvenWhenAnAgentHasResumedTheBead(t *testing.T) {
	for _, status := range []string{"in_progress", "open", "hooked"} {
		f, err := CheckFrozen("ap-mnp", lookupWithLabels(status, []string{"tv-card"}, "TV card: PDH & PDL - Previous Day Levels [2nqAQazP]"))
		if err != nil {
			t.Fatalf("status %q: %v", status, err)
		}
		if f == nil {
			t.Fatalf("status %q: a tv-card bead escaped the freeze by being resumed - this is exactly how ap-mnp got away", status)
		}
		if !strings.Contains(f.Explanation, "cannot register") {
			t.Errorf("halt message does not give the capability grounds: %s", f.Explanation)
		}
	}
}

// A bead that merely carries unrelated labels must still merge freely.
func TestCheckFrozen_UnrelatedLabelsDoNotHalt(t *testing.T) {
	f, err := CheckFrozen("ap-ok", lookupWithLabels("open", []string{"bug", "needs-semantic-review"}, "ordinary work"))
	if err != nil {
		t.Fatal(err)
	}
	if f != nil {
		t.Errorf("unrelated labels halted a mergeable bead: %s", f)
	}
}

func TestCheckFrozen_LabelMatchIsCaseInsensitive(t *testing.T) {
	f, err := CheckFrozen("ap-x", lookupWithLabels("open", []string{"  TV-Card "}, "t"))
	if err != nil || f == nil {
		t.Fatalf("label match must tolerate case and padding; got f=%v err=%v", f, err)
	}
}
