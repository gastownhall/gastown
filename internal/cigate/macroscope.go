package cigate

// Macroscope comment-settle extension of the AA-851 CI gate (op-sr9u /
// hq-owe9c): Macroscope posts its inline review comments asynchronously
// AFTER its check runs report a conclusion, so a completion gated only on
// check state can race the review — the PR goes green, gt done passes, and
// substantive comments arrive minutes later with the author's session
// already dead (observed 3/3 on capital #21457/#21459/#21461, 2026-07-11;
// each cost an adoption-dispatch cycle).
//
// The settle phase runs only after the CI verdict is GREEN:
//  1. wait for every Macroscope check context to reach a TERMINAL state on
//     the final head (same poll/backoff machinery as WaitForGreen);
//  2. then perform ONE review-comment fetch;
//  3. unaddressed Macroscope threads fail the gate like CI red — the author
//     stays assigned and must address each comment (fix, or refute with
//     evidence, replying on the thread) before re-running gt done;
//  4. if Macroscope never settles (outage), the caller FAILS OPEN after the
//     settle timeout with a human escalation, per the gate's existing
//     30m pending rule.
//
// A rollup with no Macroscope contexts at all is settled trivially: on rigs
// without Macroscope any wait would burn the full timeout on every
// completion, and on rigs with it the Macroscope check registers within
// seconds of push — long before the rest of CI turns green.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// MacroscopeOptions selects which checks and comment authors belong to
// Macroscope. Zero values mean "match nothing" — callers should populate
// from config (CheckPatternsOrDefault / BotLoginsOrDefault).
type MacroscopeOptions struct {
	// CheckPatterns are case-insensitive substrings matched against check
	// display names (e.g. "macroscope" matches both
	// "Macroscope - Correctness Check" and "Macroscope - Approvability Check").
	CheckPatterns []string
	// BotLogins are review-comment author logins that count as Macroscope,
	// compared case-insensitively with any "[bot]" suffix stripped
	// (GraphQL reports "macroscopeapp", REST "macroscopeapp[bot]").
	BotLogins []string
}

// MacroscopeSettleResult is the outcome of one settle evaluation.
type MacroscopeSettleResult struct {
	// Settled is true when every matched Macroscope check context reports a
	// terminal state (or the PR has no Macroscope contexts / no open PR).
	Settled bool
	// PendingChecks lists the Macroscope contexts not yet terminal.
	PendingChecks []string
	PRNumber      int
	PRURL         string
	Err           error
}

// UnaddressedComment describes one Macroscope review thread that has
// neither been resolved nor replied to by anyone but the bot.
type UnaddressedComment struct {
	Author  string
	Path    string
	URL     string
	Excerpt string
}

// MacroscopeEnvDisabled reports whether the settle phase is force-disabled
// process-wide via GT_MACROSCOPE_SETTLE=off (parallel to GT_CI_GATE=off;
// GT_CI_GATE=off also disables it since the whole gate is skipped).
func MacroscopeEnvDisabled() bool {
	switch strings.ToLower(os.Getenv("GT_MACROSCOPE_SETTLE")) {
	case "off", "0", "false", "disabled":
		return true
	}
	return false
}

// matchesMacroscope reports whether a check display name matches any
// configured pattern (case-insensitive substring).
func matchesMacroscope(name string, patterns []string) bool {
	lower := strings.ToLower(name)
	for _, p := range patterns {
		if p != "" && strings.Contains(lower, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

// terminal reports whether a rollup entry has finished reporting: a check
// run with status COMPLETED (or any conclusion), or a commit status in a
// state other than PENDING/EXPECTED. Failure states are terminal — the
// settle phase only asks "has Macroscope finished?", not "did it pass?"
// (pass/fail already belongs to the main CI verdict).
func (e rollupEntry) terminal() bool {
	if e.Status != "" || e.Conclusion != "" {
		return strings.EqualFold(e.Status, "completed") || e.Conclusion != ""
	}
	switch strings.ToUpper(e.State) {
	case "", "PENDING", "EXPECTED":
		return false
	}
	return true
}

// CheckMacroscopeSettle resolves the branch's open PR and reports whether
// its Macroscope check contexts have all reached a terminal state on the
// current (final) head. No open PR, or an open PR with no Macroscope
// contexts, is settled trivially.
func (g *Gate) CheckMacroscopeSettle(dir, branch string, opts MacroscopeOptions) MacroscopeSettleResult {
	run := g.Run
	if run == nil {
		run = defaultRunner
	}
	out, err := run(dir, "gh", "pr", "list", "--head", branch, "--state", "all",
		"--json", "number,state,url,statusCheckRollup", "--limit", "20")
	if err != nil {
		return MacroscopeSettleResult{Err: err}
	}
	var prs []prInfo
	if err := json.Unmarshal(bytes.TrimSpace(out), &prs); err != nil {
		return MacroscopeSettleResult{Err: fmt.Errorf("parsing gh pr list output: %w", err)}
	}
	var open *prInfo
	for i := range prs {
		if prs[i].State == "OPEN" {
			open = &prs[i]
			break
		}
	}
	if open == nil {
		return MacroscopeSettleResult{Settled: true}
	}
	res := MacroscopeSettleResult{Settled: true, PRNumber: open.Number, PRURL: open.URL}
	for _, check := range open.StatusCheckRollup {
		if !matchesMacroscope(check.displayName(), opts.CheckPatterns) {
			continue
		}
		if !check.terminal() {
			res.Settled = false
			res.PendingChecks = append(res.PendingChecks, check.displayName())
		}
	}
	return res
}

// WaitForMacroscopeSettle polls CheckMacroscopeSettle with the same backoff
// as WaitForGreen until the Macroscope contexts settle, an error occurs, or
// the timeout elapses. It returns the last result and whether the wait
// timed out with contexts still pending.
func (g *Gate) WaitForMacroscopeSettle(dir, branch string, opts MacroscopeOptions, w WaitOptions) (MacroscopeSettleResult, bool) {
	sleep := w.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	now := w.Now
	if now == nil {
		now = time.Now
	}
	interval := w.PollInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	maxInterval := interval * 4
	deadline := now().Add(w.Timeout)

	res := g.CheckMacroscopeSettle(dir, branch, opts)
	for !res.Settled && res.Err == nil {
		remaining := deadline.Sub(now())
		if remaining <= 0 {
			return res, true
		}
		if interval > remaining {
			interval = remaining
		}
		if w.Progress != nil {
			fmt.Fprintf(w.Progress, "  Macroscope not settled (%s) — next poll in %s, %s remaining\n",
				strings.Join(res.PendingChecks, ", "), interval.Round(time.Second), remaining.Round(time.Second))
		}
		sleep(interval)
		if interval < maxInterval {
			interval *= 2
			if interval > maxInterval {
				interval = maxInterval
			}
		}
		res = g.CheckMacroscopeSettle(dir, branch, opts)
	}
	return res, false
}

// reviewThreadsQuery fetches the PR's review threads with resolution state —
// resolution is GraphQL-only (the REST pulls/comments endpoint has no
// isResolved), and a thread resolved without a reply must count as
// addressed. first:100/first:50 comfortably covers real PRs; a busier PR
// degrades to evaluating the first 100 threads rather than failing.
const reviewThreadsQuery = `query($owner:String!,$name:String!,$pr:Int!){
  repository(owner:$owner,name:$name){
    pullRequest(number:$pr){
      reviewThreads(first:100){
        nodes{
          isResolved
          comments(first:50){
            nodes{ author{login} path url bodyText isMinimized }
          }
        }
      }
    }
  }
}`

// reviewThreadsResponse mirrors the GraphQL response shape.
type reviewThreadsResponse struct {
	Data struct {
		Repository struct {
			PullRequest struct {
				ReviewThreads struct {
					Nodes []reviewThread `json:"nodes"`
				} `json:"reviewThreads"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
}

type reviewThread struct {
	IsResolved bool `json:"isResolved"`
	Comments   struct {
		Nodes []reviewComment `json:"nodes"`
	} `json:"comments"`
}

type reviewComment struct {
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	Path        string `json:"path"`
	URL         string `json:"url"`
	BodyText    string `json:"bodyText"`
	IsMinimized bool   `json:"isMinimized"`
}

// isBotLogin reports whether a comment author login matches the configured
// Macroscope bot logins ("[bot]" suffix stripped, case-insensitive).
func isBotLogin(login string, bots []string) bool {
	login = strings.TrimSuffix(strings.ToLower(login), "[bot]")
	for _, b := range bots {
		if b != "" && login == strings.TrimSuffix(strings.ToLower(b), "[bot]") {
			return true
		}
	}
	return false
}

// FetchUnaddressedComments performs the single post-settle comment fetch:
// it lists the PR's review threads and returns those opened by a Macroscope
// bot that are unaddressed — not resolved, not minimized, and with no reply
// from anyone but the bot. Addressing a comment per the review convention
// (fix + reply with the commit, or refute with evidence on the thread) or
// resolving the thread clears it.
func (g *Gate) FetchUnaddressedComments(dir string, prNumber int, opts MacroscopeOptions) ([]UnaddressedComment, error) {
	run := g.Run
	if run == nil {
		run = defaultRunner
	}
	repoOut, err := run(dir, "gh", "repo", "view", "--json", "owner,name")
	if err != nil {
		return nil, fmt.Errorf("resolving repo for comment fetch: %w", err)
	}
	var repo struct {
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(repoOut), &repo); err != nil {
		return nil, fmt.Errorf("parsing gh repo view output: %w", err)
	}
	out, err := run(dir, "gh", "api", "graphql",
		"-f", "query="+reviewThreadsQuery,
		"-F", "owner="+repo.Owner.Login,
		"-F", "name="+repo.Name,
		"-F", "pr="+strconv.Itoa(prNumber))
	if err != nil {
		return nil, fmt.Errorf("fetching review threads: %w", err)
	}
	var resp reviewThreadsResponse
	if err := json.Unmarshal(bytes.TrimSpace(out), &resp); err != nil {
		return nil, fmt.Errorf("parsing review threads: %w", err)
	}

	var unaddressed []UnaddressedComment
	for _, thread := range resp.Data.Repository.PullRequest.ReviewThreads.Nodes {
		if thread.IsResolved || len(thread.Comments.Nodes) == 0 {
			continue
		}
		first := thread.Comments.Nodes[0]
		if first.IsMinimized || !isBotLogin(first.Author.Login, opts.BotLogins) {
			continue
		}
		replied := false
		for _, c := range thread.Comments.Nodes[1:] {
			if !isBotLogin(c.Author.Login, opts.BotLogins) {
				replied = true
				break
			}
		}
		if replied {
			continue
		}
		unaddressed = append(unaddressed, UnaddressedComment{
			Author:  first.Author.Login,
			Path:    first.Path,
			URL:     first.URL,
			Excerpt: commentExcerpt(first.BodyText),
		})
	}
	return unaddressed, nil
}

// commentExcerpt reduces a comment body to its first line, truncated.
func commentExcerpt(body string) string {
	line := strings.TrimSpace(body)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	const max = 120
	if len(line) > max {
		line = line[:max] + "…"
	}
	return line
}
