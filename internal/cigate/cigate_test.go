package cigate

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func stubRunner(out string, err error) Runner {
	return func(dir, name string, args ...string) ([]byte, error) {
		if err != nil {
			return nil, err
		}
		return []byte(out), nil
	}
}

func TestEvaluateRollup(t *testing.T) {
	tests := []struct {
		name    string
		checks  []rollupEntry
		verdict Verdict
		failing []string
		pending []string
	}{
		{
			name:    "empty rollup is pending (checks not registered yet)",
			checks:  nil,
			verdict: VerdictPending,
			pending: []string{"checks not yet reported"},
		},
		{
			name: "all check runs green",
			checks: []rollupEntry{
				{Name: "build", Status: "completed", Conclusion: "success"},
				{Name: "lint", Status: "completed", Conclusion: "skipped"},
				{Name: "note", Status: "completed", Conclusion: "neutral"},
			},
			verdict: VerdictGreen,
		},
		{
			name: "one failing check run is red",
			checks: []rollupEntry{
				{Name: "build", Status: "completed", Conclusion: "success"},
				{Name: "test", Status: "completed", Conclusion: "failure"},
			},
			verdict: VerdictRed,
			failing: []string{"test"},
		},
		{
			name: "cancelled and timed_out are red",
			checks: []rollupEntry{
				{Name: "a", Conclusion: "cancelled"},
				{Name: "b", Conclusion: "timed_out"},
				{Name: "c", Conclusion: "action_required"},
			},
			verdict: VerdictRed,
			failing: []string{"a", "b", "c"},
		},
		{
			name: "in-progress check run is pending",
			checks: []rollupEntry{
				{Name: "build", Status: "completed", Conclusion: "success"},
				{Name: "test", Status: "in_progress"},
			},
			verdict: VerdictPending,
			pending: []string{"test"},
		},
		{
			name: "jenkins commit status failure is red",
			checks: []rollupEntry{
				{Context: "continuous-integration/jenkins/branch", State: "FAILURE"},
			},
			verdict: VerdictRed,
			failing: []string{"continuous-integration/jenkins/branch"},
		},
		{
			name: "jenkins commit status pending is pending",
			checks: []rollupEntry{
				{Context: "continuous-integration/jenkins/pr-merge", State: "PENDING"},
				{Name: "gha", Status: "completed", Conclusion: "success"},
			},
			verdict: VerdictPending,
			pending: []string{"continuous-integration/jenkins/pr-merge"},
		},
		{
			name: "jenkins success plus gha success is green",
			checks: []rollupEntry{
				{Context: "jenkins/build", State: "SUCCESS"},
				{Name: "gha", Status: "completed", Conclusion: "success"},
			},
			verdict: VerdictGreen,
		},
		{
			name: "red wins over pending",
			checks: []rollupEntry{
				{Name: "test", Conclusion: "failure"},
				{Name: "deploy", Status: "queued"},
			},
			verdict: VerdictRed,
			failing: []string{"test"},
			pending: []string{"deploy"},
		},
		{
			name: "expected commit status is pending",
			checks: []rollupEntry{
				{Context: "macroscope", State: "EXPECTED"},
			},
			verdict: VerdictPending,
			pending: []string{"macroscope"},
		},
		{
			// Real gh 2.45 output uses UPPERCASE GraphQL enums (verified live).
			name: "uppercase enums from modern gh: green",
			checks: []rollupEntry{
				{Name: "API & Page Tests", Status: "COMPLETED", Conclusion: "SUCCESS"},
				{Name: "Mergify Merge Queue", Status: "COMPLETED", Conclusion: "NEUTRAL"},
			},
			verdict: VerdictGreen,
		},
		{
			name: "uppercase enums from modern gh: red",
			checks: []rollupEntry{
				{Name: "API & Page Tests", Status: "COMPLETED", Conclusion: "FAILURE"},
			},
			verdict: VerdictRed,
			failing: []string{"API & Page Tests"},
		},
		{
			name: "uppercase enums from modern gh: pending",
			checks: []rollupEntry{
				{Name: "build", Status: "IN_PROGRESS", Conclusion: ""},
			},
			verdict: VerdictPending,
			pending: []string{"build"},
		},
		{
			name: "startup_failure is red, stale is pending",
			checks: []rollupEntry{
				{Name: "a", Conclusion: "STARTUP_FAILURE"},
				{Name: "b", Conclusion: "STALE"},
			},
			verdict: VerdictRed,
			failing: []string{"a"},
			pending: []string{"b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := evaluateRollup(tt.checks)
			if res.Verdict != tt.verdict {
				t.Fatalf("verdict = %s, want %s", res.Verdict, tt.verdict)
			}
			if got, want := strings.Join(res.FailingChecks, ","), strings.Join(tt.failing, ","); got != want {
				t.Errorf("failing = %q, want %q", got, want)
			}
			if got, want := strings.Join(res.PendingChecks, ","), strings.Join(tt.pending, ","); got != want {
				t.Errorf("pending = %q, want %q", got, want)
			}
		})
	}
}

func TestVerdictBlocks(t *testing.T) {
	blocking := map[Verdict]bool{
		VerdictRed:            true,
		VerdictPending:        true,
		VerdictGreen:          false,
		VerdictNoPR:           false,
		VerdictMerged:         false,
		VerdictClosedUnmerged: false,
		VerdictError:          false, // fail-open
	}
	for v, want := range blocking {
		if v.Blocks() != want {
			t.Errorf("%s.Blocks() = %v, want %v", v, v.Blocks(), want)
		}
	}
}

func TestCheckBranch(t *testing.T) {
	tests := []struct {
		name     string
		ghOutput string
		ghErr    error
		verdict  Verdict
		prNumber int
	}{
		{
			name:     "no PR",
			ghOutput: `[]`,
			verdict:  VerdictNoPR,
		},
		{
			name: "open PR green",
			ghOutput: `[{"number":42,"state":"OPEN","url":"https://github.com/x/y/pull/42",
				"statusCheckRollup":[{"name":"build","status":"completed","conclusion":"success"}]}]`,
			verdict:  VerdictGreen,
			prNumber: 42,
		},
		{
			name: "open PR red",
			ghOutput: `[{"number":7,"state":"OPEN","url":"u",
				"statusCheckRollup":[{"name":"test","status":"completed","conclusion":"failure"}]}]`,
			verdict:  VerdictRed,
			prNumber: 7,
		},
		{
			name: "open PR pending",
			ghOutput: `[{"number":8,"state":"OPEN","url":"u",
				"statusCheckRollup":[{"context":"jenkins/pr","state":"PENDING"}]}]`,
			verdict:  VerdictPending,
			prNumber: 8,
		},
		{
			name:     "merged PR only",
			ghOutput: `[{"number":9,"state":"MERGED","url":"u","statusCheckRollup":[]}]`,
			verdict:  VerdictMerged,
			prNumber: 9,
		},
		{
			name:     "closed unmerged PR only",
			ghOutput: `[{"number":10,"state":"CLOSED","url":"u","statusCheckRollup":[]}]`,
			verdict:  VerdictClosedUnmerged,
			prNumber: 10,
		},
		{
			name: "open PR preferred over merged",
			ghOutput: `[{"number":12,"state":"OPEN","url":"u",
				"statusCheckRollup":[{"name":"b","conclusion":"success"}]},
				{"number":11,"state":"MERGED","url":"u","statusCheckRollup":[]}]`,
			verdict:  VerdictGreen,
			prNumber: 12,
		},
		{
			name:    "gh error fails open as ERROR",
			ghErr:   errors.New("gh: rate limited"),
			verdict: VerdictError,
		},
		{
			name:     "garbage output is ERROR",
			ghOutput: `not json`,
			verdict:  VerdictError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &Gate{Run: stubRunner(tt.ghOutput, tt.ghErr)}
			res := g.CheckBranch("/tmp", "polecat/test")
			if res.Verdict != tt.verdict {
				t.Fatalf("verdict = %s, want %s (err=%v)", res.Verdict, tt.verdict, res.Err)
			}
			if tt.prNumber != 0 && res.PRNumber != tt.prNumber {
				t.Errorf("prNumber = %d, want %d", res.PRNumber, tt.prNumber)
			}
			if tt.verdict == VerdictError && res.Err == nil {
				t.Errorf("VerdictError should carry Err")
			}
		})
	}
}

// sequenceGate returns a Gate whose CheckBranch yields canned outputs in order,
// repeating the last one.
func sequenceGate(outputs ...string) *Gate {
	i := 0
	return &Gate{Run: func(dir, name string, args ...string) ([]byte, error) {
		out := outputs[i]
		if i < len(outputs)-1 {
			i++
		}
		return []byte(out), nil
	}}
}

const (
	prPending = `[{"number":5,"state":"OPEN","url":"u","statusCheckRollup":[{"context":"jenkins","state":"PENDING"}]}]`
	prGreen   = `[{"number":5,"state":"OPEN","url":"u","statusCheckRollup":[{"context":"jenkins","state":"SUCCESS"}]}]`
	prRed     = `[{"number":5,"state":"OPEN","url":"u","statusCheckRollup":[{"context":"jenkins","state":"FAILURE"}]}]`
)

// fakeClock drives WaitForGreen without real sleeping.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time        { return c.now }
func (c *fakeClock) Sleep(d time.Duration) { c.now = c.now.Add(d) }

func TestWaitForGreen(t *testing.T) {
	t.Run("pending then green", func(t *testing.T) {
		clock := &fakeClock{now: time.Unix(0, 0)}
		g := sequenceGate(prPending, prPending, prGreen)
		res, timedOut := g.WaitForGreen("/tmp", "b", WaitOptions{
			Timeout: 30 * time.Minute, PollInterval: 30 * time.Second,
			Sleep: clock.Sleep, Now: clock.Now,
		})
		if timedOut || res.Verdict != VerdictGreen {
			t.Fatalf("got %s timedOut=%v, want GREEN", res.Verdict, timedOut)
		}
	})

	t.Run("pending then red", func(t *testing.T) {
		clock := &fakeClock{now: time.Unix(0, 0)}
		g := sequenceGate(prPending, prRed)
		res, timedOut := g.WaitForGreen("/tmp", "b", WaitOptions{
			Timeout: 30 * time.Minute, PollInterval: 30 * time.Second,
			Sleep: clock.Sleep, Now: clock.Now,
		})
		if timedOut || res.Verdict != VerdictRed {
			t.Fatalf("got %s timedOut=%v, want RED", res.Verdict, timedOut)
		}
	})

	t.Run("stuck pending times out", func(t *testing.T) {
		clock := &fakeClock{now: time.Unix(0, 0)}
		g := sequenceGate(prPending)
		start := clock.now
		res, timedOut := g.WaitForGreen("/tmp", "b", WaitOptions{
			Timeout: 10 * time.Minute, PollInterval: 30 * time.Second,
			Sleep: clock.Sleep, Now: clock.Now,
		})
		if !timedOut || res.Verdict != VerdictPending {
			t.Fatalf("got %s timedOut=%v, want PENDING + timeout", res.Verdict, timedOut)
		}
		if waited := clock.now.Sub(start); waited < 10*time.Minute || waited > 12*time.Minute {
			t.Errorf("waited %s, want ~10m", waited)
		}
	})

	t.Run("backoff caps at 4x", func(t *testing.T) {
		clock := &fakeClock{now: time.Unix(0, 0)}
		var sleeps []time.Duration
		g := sequenceGate(prPending)
		_, _ = g.WaitForGreen("/tmp", "b", WaitOptions{
			Timeout: 20 * time.Minute, PollInterval: 30 * time.Second,
			Sleep: func(d time.Duration) { sleeps = append(sleeps, d); clock.Sleep(d) },
			Now:   clock.Now,
		})
		if len(sleeps) < 4 {
			t.Fatalf("expected several polls, got %d", len(sleeps))
		}
		want := []time.Duration{30 * time.Second, 60 * time.Second, 120 * time.Second, 120 * time.Second}
		for i, w := range want {
			if sleeps[i] != w {
				t.Errorf("sleep[%d] = %s, want %s (all: %v)", i, sleeps[i], w, sleeps)
			}
		}
	})
}

func TestSummary(t *testing.T) {
	res := CheckResult{Verdict: VerdictRed, PRNumber: 3, FailingChecks: []string{"jenkins/build", "macroscope"}}
	want := "PR #3 has failing checks [jenkins/build, macroscope]"
	if got := res.Summary(); got != want {
		t.Errorf("Summary() = %q, want %q", got, want)
	}
	errRes := CheckResult{Verdict: VerdictError, Err: fmt.Errorf("boom")}
	if !strings.Contains(errRes.Summary(), "boom") {
		t.Errorf("error summary should include cause: %q", errRes.Summary())
	}
}
