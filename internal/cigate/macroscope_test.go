package cigate

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

var msOpts = MacroscopeOptions{
	CheckPatterns: []string{"macroscope"},
	BotLogins:     []string{"macroscopeapp"},
}

func TestRollupEntryTerminal(t *testing.T) {
	tests := []struct {
		name     string
		entry    rollupEntry
		terminal bool
	}{
		{"completed check run", rollupEntry{Name: "Macroscope - Correctness Check", Status: "COMPLETED", Conclusion: "SUCCESS"}, true},
		{"completed neutral check run (live Approvability shape)", rollupEntry{Name: "Macroscope - Approvability Check", Status: "COMPLETED", Conclusion: "NEUTRAL"}, true},
		{"completed failing check run is terminal (pass/fail is the CI verdict's job)", rollupEntry{Name: "m", Status: "COMPLETED", Conclusion: "FAILURE"}, true},
		{"in-progress check run", rollupEntry{Name: "m", Status: "IN_PROGRESS"}, false},
		{"queued check run", rollupEntry{Name: "m", Status: "QUEUED"}, false},
		{"commit status success", rollupEntry{Context: "macroscope/review", State: "SUCCESS"}, true},
		{"commit status failure is terminal", rollupEntry{Context: "macroscope/review", State: "FAILURE"}, true},
		{"commit status pending", rollupEntry{Context: "macroscope/review", State: "PENDING"}, false},
		{"commit status expected", rollupEntry{Context: "macroscope/review", State: "EXPECTED"}, false},
		{"empty entry is not terminal", rollupEntry{Name: "m"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.entry.terminal(); got != tt.terminal {
				t.Errorf("terminal() = %v, want %v", got, tt.terminal)
			}
		})
	}
}

func TestCheckMacroscopeSettle(t *testing.T) {
	tests := []struct {
		name     string
		ghOutput string
		ghErr    error
		settled  bool
		pending  []string
		wantErr  bool
	}{
		{
			name:     "no PR settles trivially",
			ghOutput: `[]`,
			settled:  true,
		},
		{
			name: "no macroscope contexts settles trivially (rig without Macroscope)",
			ghOutput: `[{"number":5,"state":"OPEN","url":"u",
				"statusCheckRollup":[{"context":"jenkins","state":"SUCCESS"}]}]`,
			settled: true,
		},
		{
			// Live shape from capital #21457: both Macroscope check runs
			// COMPLETED while Jenkins contexts still pend — the CI verdict
			// owns those; the settle phase only watches Macroscope.
			name: "macroscope terminal amid pending jenkins is settled",
			ghOutput: `[{"number":21457,"state":"OPEN","url":"u","statusCheckRollup":[
				{"name":"Macroscope - Approvability Check","status":"COMPLETED","conclusion":"NEUTRAL"},
				{"name":"Macroscope - Correctness Check","status":"COMPLETED","conclusion":"SUCCESS"},
				{"context":"continuous-integration/jenkins/branch","state":"PENDING"}]}]`,
			settled: true,
		},
		{
			name: "in-progress macroscope check is not settled",
			ghOutput: `[{"number":6,"state":"OPEN","url":"u","statusCheckRollup":[
				{"name":"Macroscope - Correctness Check","status":"IN_PROGRESS"},
				{"context":"jenkins","state":"SUCCESS"}]}]`,
			settled: false,
			pending: []string{"Macroscope - Correctness Check"},
		},
		{
			name: "macroscope commit status pending is not settled",
			ghOutput: `[{"number":7,"state":"OPEN","url":"u","statusCheckRollup":[
				{"context":"macroscope/review","state":"PENDING"}]}]`,
			settled: false,
			pending: []string{"macroscope/review"},
		},
		{
			name:    "gh error surfaces",
			ghErr:   errors.New("gh: rate limited"),
			wantErr: true,
		},
		{
			name:     "garbage output surfaces as error",
			ghOutput: `not json`,
			wantErr:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &Gate{Run: stubRunner(tt.ghOutput, tt.ghErr)}
			res := g.CheckMacroscopeSettle("/tmp", "polecat/test", msOpts)
			if tt.wantErr {
				if res.Err == nil {
					t.Fatal("want error, got nil")
				}
				return
			}
			if res.Err != nil {
				t.Fatalf("unexpected error: %v", res.Err)
			}
			if res.Settled != tt.settled {
				t.Errorf("Settled = %v, want %v", res.Settled, tt.settled)
			}
			if got, want := strings.Join(res.PendingChecks, ","), strings.Join(tt.pending, ","); got != want {
				t.Errorf("pending = %q, want %q", got, want)
			}
		})
	}
}

const (
	msPending = `[{"number":5,"state":"OPEN","url":"u","statusCheckRollup":[{"name":"Macroscope - Correctness Check","status":"IN_PROGRESS"}]}]`
	msSettled = `[{"number":5,"state":"OPEN","url":"u","statusCheckRollup":[{"name":"Macroscope - Correctness Check","status":"COMPLETED","conclusion":"SUCCESS"}]}]`
)

func TestWaitForMacroscopeSettle(t *testing.T) {
	t.Run("pending then settled", func(t *testing.T) {
		clock := &fakeClock{now: time.Unix(0, 0)}
		g := sequenceGate(msPending, msPending, msSettled)
		res, timedOut := g.WaitForMacroscopeSettle("/tmp", "b", msOpts, WaitOptions{
			Timeout: 30 * time.Minute, PollInterval: 30 * time.Second,
			Sleep: clock.Sleep, Now: clock.Now,
		})
		if timedOut || !res.Settled {
			t.Fatalf("got settled=%v timedOut=%v, want settled", res.Settled, timedOut)
		}
	})

	t.Run("stuck pending times out", func(t *testing.T) {
		clock := &fakeClock{now: time.Unix(0, 0)}
		g := sequenceGate(msPending)
		start := clock.now
		res, timedOut := g.WaitForMacroscopeSettle("/tmp", "b", msOpts, WaitOptions{
			Timeout: 10 * time.Minute, PollInterval: 30 * time.Second,
			Sleep: clock.Sleep, Now: clock.Now,
		})
		if !timedOut || res.Settled {
			t.Fatalf("got settled=%v timedOut=%v, want timeout", res.Settled, timedOut)
		}
		if waited := clock.now.Sub(start); waited < 10*time.Minute || waited > 12*time.Minute {
			t.Errorf("waited %s, want ~10m", waited)
		}
	})

	t.Run("error stops the wait", func(t *testing.T) {
		g := &Gate{Run: stubRunner("", errors.New("boom"))}
		res, timedOut := g.WaitForMacroscopeSettle("/tmp", "b", msOpts, WaitOptions{
			Timeout: 10 * time.Minute, PollInterval: 30 * time.Second,
			Sleep: func(time.Duration) {}, Now: time.Now,
		})
		if timedOut || res.Err == nil {
			t.Fatalf("want error without timeout, got err=%v timedOut=%v", res.Err, timedOut)
		}
	})
}

func TestIsBotLogin(t *testing.T) {
	bots := []string{"macroscopeapp"}
	for login, want := range map[string]bool{
		"macroscopeapp":      true, // GraphQL shape
		"macroscopeapp[bot]": true, // REST shape
		"MacroscopeApp[BOT]": true,
		"blairsilverberg":    false,
		"":                   false,
	} {
		if got := isBotLogin(login, bots); got != want {
			t.Errorf("isBotLogin(%q) = %v, want %v", login, got, want)
		}
	}
	if isBotLogin("macroscopeapp", nil) {
		t.Error("empty bot list must match nothing")
	}
	if !isBotLogin("macroscopeapp", []string{"macroscopeapp[bot]"}) {
		t.Error("configured [bot] suffix should be normalized too")
	}
}

// threadsJSON builds a GraphQL reviewThreads response body.
func threadsJSON(threads ...string) string {
	return fmt.Sprintf(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[%s]}}}}}`,
		strings.Join(threads, ","))
}

func msThread(resolved bool, comments ...string) string {
	return fmt.Sprintf(`{"isResolved":%v,"comments":{"nodes":[%s]}}`, resolved, strings.Join(comments, ","))
}

func msComment(login, body string) string {
	return fmt.Sprintf(`{"author":{"login":%q},"path":"a.py","url":"https://x/r1","bodyText":%q,"isMinimized":false}`, login, body)
}

// fetchRunner dispatches on the gh subcommand: repo view vs api graphql.
func fetchRunner(graphqlOut string, graphqlErr error) Runner {
	return func(dir, name string, args ...string) ([]byte, error) {
		if len(args) > 1 && args[0] == "repo" && args[1] == "view" {
			return []byte(`{"owner":{"login":"captec"},"name":"capital"}`), nil
		}
		if graphqlErr != nil {
			return nil, graphqlErr
		}
		return []byte(graphqlOut), nil
	}
}

func TestFetchUnaddressedComments(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want int
	}{
		{
			name: "unresolved bot thread with no reply is unaddressed",
			out:  threadsJSON(msThread(false, msComment("macroscopeapp", "🟡 Medium: thin-data guard"))),
			want: 1,
		},
		{
			name: "resolved thread is addressed",
			out:  threadsJSON(msThread(true, msComment("macroscopeapp", "issue"))),
			want: 0,
		},
		{
			name: "non-bot reply addresses the thread",
			out: threadsJSON(msThread(false,
				msComment("macroscopeapp", "issue"),
				msComment("blairsilverberg", "fixed in abc123"))),
			want: 0,
		},
		{
			name: "bot-only replies do not address the thread",
			out: threadsJSON(msThread(false,
				msComment("macroscopeapp", "issue"),
				msComment("macroscopeapp", "still an issue"))),
			want: 1,
		},
		{
			name: "non-macroscope thread is ignored",
			out:  threadsJSON(msThread(false, msComment("humanreviewer", "nit"))),
			want: 0,
		},
		{
			name: "minimized first comment is ignored (deliberately hidden)",
			out: threadsJSON(`{"isResolved":false,"comments":{"nodes":[
				{"author":{"login":"macroscopeapp"},"path":"a.py","url":"u","bodyText":"x","isMinimized":true}]}}`),
			want: 0,
		},
		{
			name: "empty thread list",
			out:  threadsJSON(),
			want: 0,
		},
		{
			name: "mixed threads count only the unaddressed",
			out: threadsJSON(
				msThread(false, msComment("macroscopeapp", "🟡 Medium one")),
				msThread(true, msComment("macroscopeapp", "resolved one")),
				msThread(false, msComment("macroscopeapp", "🟡 Medium two")),
			),
			want: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &Gate{Run: fetchRunner(tt.out, nil)}
			got, err := g.FetchUnaddressedComments("/tmp", 21457, msOpts)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != tt.want {
				t.Errorf("got %d unaddressed, want %d: %+v", len(got), tt.want, got)
			}
		})
	}

	t.Run("graphql error surfaces", func(t *testing.T) {
		g := &Gate{Run: fetchRunner("", errors.New("gh: 502"))}
		if _, err := g.FetchUnaddressedComments("/tmp", 1, msOpts); err == nil {
			t.Fatal("want error, got nil")
		}
	})

	t.Run("repo view error surfaces", func(t *testing.T) {
		g := &Gate{Run: stubRunner("", errors.New("no remote"))}
		if _, err := g.FetchUnaddressedComments("/tmp", 1, msOpts); err == nil {
			t.Fatal("want error, got nil")
		}
	})

	t.Run("comment fields populated", func(t *testing.T) {
		g := &Gate{Run: fetchRunner(threadsJSON(msThread(false,
			msComment("macroscopeapp", "🟡 Medium first line\nsecond line"))), nil)}
		got, err := g.FetchUnaddressedComments("/tmp", 1, msOpts)
		if err != nil || len(got) != 1 {
			t.Fatalf("got %v, %v", got, err)
		}
		c := got[0]
		if c.Author != "macroscopeapp" || c.Path != "a.py" || c.URL != "https://x/r1" {
			t.Errorf("fields = %+v", c)
		}
		if c.Excerpt != "🟡 Medium first line" {
			t.Errorf("excerpt = %q, want first line only", c.Excerpt)
		}
	})
}

func TestMacroscopeEnvKillSwitch(t *testing.T) {
	for _, v := range []string{"off", "0", "false", "disabled", "OFF"} {
		t.Setenv("GT_MACROSCOPE_SETTLE", v)
		if !MacroscopeEnvDisabled() {
			t.Errorf("GT_MACROSCOPE_SETTLE=%s should disable the settle phase", v)
		}
	}
	for _, v := range []string{"", "on", "1", "true"} {
		t.Setenv("GT_MACROSCOPE_SETTLE", v)
		if MacroscopeEnvDisabled() {
			t.Errorf("GT_MACROSCOPE_SETTLE=%s should NOT disable the settle phase", v)
		}
	}
}
