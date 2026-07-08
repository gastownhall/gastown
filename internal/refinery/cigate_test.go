package refinery

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/cigate"
)

func TestCIGateMergeDecision(t *testing.T) {
	t.Run("red blocks with NeedsCIGreen", func(t *testing.T) {
		res := cigate.CheckResult{Verdict: cigate.VerdictRed, PRNumber: 7, FailingChecks: []string{"jenkins/build"}}
		blocked := ciGateMergeDecision(res)
		if blocked == nil || !blocked.NeedsCIGreen || blocked.Success {
			t.Fatalf("want NeedsCIGreen block, got %+v", blocked)
		}
		if !strings.Contains(blocked.Error, "jenkins/build") {
			t.Errorf("block reason should name the failing check: %q", blocked.Error)
		}
	})

	t.Run("pending blocks with NeedsCIGreen", func(t *testing.T) {
		res := cigate.CheckResult{Verdict: cigate.VerdictPending, PRNumber: 7, PendingChecks: []string{"macroscope"}}
		blocked := ciGateMergeDecision(res)
		if blocked == nil || !blocked.NeedsCIGreen {
			t.Fatalf("want NeedsCIGreen block, got %+v", blocked)
		}
	})

	for _, tt := range []struct {
		name string
		res  cigate.CheckResult
	}{
		{"green proceeds", cigate.CheckResult{Verdict: cigate.VerdictGreen, PRNumber: 7}},
		{"merged proceeds", cigate.CheckResult{Verdict: cigate.VerdictMerged, PRNumber: 7}},
		{"no PR proceeds", cigate.CheckResult{Verdict: cigate.VerdictNoPR}},
		{"error fails open", cigate.CheckResult{Verdict: cigate.VerdictError, Err: errors.New("gh down")}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if blocked := ciGateMergeDecision(tt.res); blocked != nil {
				t.Errorf("want proceed (nil), got %+v", blocked)
			}
		})
	}
}
