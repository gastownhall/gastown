// Package cigate implements the hard CI gate (AA-851): a polecat cannot
// complete (gt done) and cannot be reaped while its branch's pull request
// has any CI check red or pending, and the refinery will not merge such a
// PR. The gate applies only when a PR exists — branches with no PR and
// already-merged PRs pass through.
//
// CI state is read via the gh CLI (`gh pr list ... --json statusCheckRollup`),
// matching the existing PR-state precedents in internal/git (HasOpenPR,
// FindPRNumber). The rollup evaluation is a port of the display-layer
// determineCIStatus (internal/web/fetcher.go): it handles both GitHub check
// runs (Status/Conclusion) and commit statuses (State — how Jenkins contexts
// report), so GitHub Actions, Jenkins, and other status providers are all
// covered by one rollup.
//
// Failure semantics: when CI state cannot be determined (gh missing, rate
// limit, network), the verdict is ERROR and callers FAIL OPEN — a GitHub
// outage must not brick every completion town-wide — but must escalate
// loudly (see RunEscalationCmd) so a silently-disabled gate is visible.
package cigate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Verdict classifies the CI state of a branch's pull request.
type Verdict string

const (
	// VerdictGreen means all reported checks passed.
	VerdictGreen Verdict = "GREEN"
	// VerdictRed means at least one check failed.
	VerdictRed Verdict = "RED"
	// VerdictPending means no failures but at least one check is still
	// running (or the PR has no checks reported yet).
	VerdictPending Verdict = "PENDING"
	// VerdictNoPR means the branch has no pull request — the gate does not apply.
	VerdictNoPR Verdict = "NO_PR"
	// VerdictMerged means the branch's PR is already merged.
	VerdictMerged Verdict = "MERGED"
	// VerdictClosedUnmerged means the PR was closed without merging
	// (a deliberate human action; callers pass through with a warning).
	VerdictClosedUnmerged Verdict = "CLOSED_UNMERGED"
	// VerdictError means CI state could not be determined; callers fail open.
	VerdictError Verdict = "ERROR"
)

// Blocks reports whether the verdict blocks completion/reaping.
// Only RED and PENDING block; ERROR fails open (callers escalate it loudly).
func (v Verdict) Blocks() bool {
	return v == VerdictRed || v == VerdictPending
}

// CheckResult is the outcome of one CI gate evaluation for a branch.
type CheckResult struct {
	Verdict       Verdict
	PRNumber      int
	PRURL         string
	FailingChecks []string
	PendingChecks []string
	Err           error // set when Verdict == VerdictError
}

// Summary renders a one-line human-readable description of the result.
func (r CheckResult) Summary() string {
	switch r.Verdict {
	case VerdictRed:
		return fmt.Sprintf("PR #%d has failing checks [%s]", r.PRNumber, strings.Join(r.FailingChecks, ", "))
	case VerdictPending:
		return fmt.Sprintf("PR #%d has pending checks [%s]", r.PRNumber, strings.Join(r.PendingChecks, ", "))
	case VerdictGreen:
		return fmt.Sprintf("PR #%d all checks green", r.PRNumber)
	case VerdictMerged:
		return fmt.Sprintf("PR #%d already merged", r.PRNumber)
	case VerdictClosedUnmerged:
		return fmt.Sprintf("PR #%d closed without merging", r.PRNumber)
	case VerdictNoPR:
		return "no PR for branch"
	case VerdictError:
		return fmt.Sprintf("CI status unknown: %v", r.Err)
	}
	return string(r.Verdict)
}

// EnvDisabled reports whether the gate is force-disabled process-wide via
// GT_CI_GATE=off (emergency kill switch that needs no config edit or redeploy).
func EnvDisabled() bool {
	switch strings.ToLower(os.Getenv("GT_CI_GATE")) {
	case "off", "0", "false", "disabled":
		return true
	}
	return false
}

// Runner executes a command in dir and returns its stdout. Swappable in tests.
type Runner func(dir, name string, args ...string) ([]byte, error)

func defaultRunner(dir, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("%s: %w (%s)", name, err, msg)
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}

// Gate evaluates PR CI status for branches.
type Gate struct {
	// Run executes external commands (gh). Defaults to a real exec runner.
	Run Runner
	// IgnoreChecks lists status-check context names that are human approval
	// gates rather than CI (e.g. pullapprove) — matched case-insensitively
	// against the check's display name and excluded from the green/pending
	// evaluation. A human merge gate pends on every PR until a person acts,
	// so counting it would burn the full pending timeout on every completion
	// (AA-859). Approval enforcement stays with the merge layer (branch
	// protection / refinery require_review), not this gate.
	IgnoreChecks []string
}

// New returns a Gate backed by the real gh CLI. ignoreChecks lists
// human-gate check names to exclude from evaluation (see Gate.IgnoreChecks);
// callers with a rig config should pass cfg.HumanGateChecksOrDefault().
func New(ignoreChecks ...string) *Gate {
	return &Gate{Run: defaultRunner, IgnoreChecks: ignoreChecks}
}

// prInfo mirrors the fields requested from `gh pr list --json`.
type prInfo struct {
	Number            int           `json:"number"`
	State             string        `json:"state"` // OPEN | MERGED | CLOSED
	URL               string        `json:"url"`
	StatusCheckRollup []rollupEntry `json:"statusCheckRollup"`
}

// rollupEntry covers both rollup shapes GitHub returns: check runs
// (name/status/conclusion) and commit statuses (context/state).
type rollupEntry struct {
	Name       string `json:"name"`    // check runs
	Context    string `json:"context"` // commit statuses (Jenkins-style)
	State      string `json:"state"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

func (e rollupEntry) displayName() string {
	if e.Name != "" {
		return e.Name
	}
	if e.Context != "" {
		return e.Context
	}
	return "unnamed-check"
}

// CheckBranch resolves the branch's PR via gh and evaluates its check rollup.
// PR resolution: newest OPEN PR wins; with no open PR, a merged PR yields
// MERGED, a closed-unmerged PR yields CLOSED_UNMERGED, and none yields NO_PR.
// dir must be inside a clone whose origin is the repo to query (gh resolves
// the repo from git remotes).
func (g *Gate) CheckBranch(dir, branch string) CheckResult {
	run := g.Run
	if run == nil {
		run = defaultRunner
	}
	out, err := run(dir, "gh", "pr", "list", "--head", branch, "--state", "all",
		"--json", "number,state,url,statusCheckRollup", "--limit", "20")
	if err != nil {
		return CheckResult{Verdict: VerdictError, Err: err}
	}
	var prs []prInfo
	if err := json.Unmarshal(bytes.TrimSpace(out), &prs); err != nil {
		return CheckResult{Verdict: VerdictError, Err: fmt.Errorf("parsing gh pr list output: %w", err)}
	}
	// gh returns newest-first; keep the first PR seen in each state.
	var open, merged, closed *prInfo
	for i := range prs {
		switch prs[i].State {
		case "OPEN":
			if open == nil {
				open = &prs[i]
			}
		case "MERGED":
			if merged == nil {
				merged = &prs[i]
			}
		case "CLOSED":
			if closed == nil {
				closed = &prs[i]
			}
		}
	}
	switch {
	case open != nil:
		res := evaluateRollup(open.StatusCheckRollup, g.IgnoreChecks)
		res.PRNumber = open.Number
		res.PRURL = open.URL
		return res
	case merged != nil:
		return CheckResult{Verdict: VerdictMerged, PRNumber: merged.Number, PRURL: merged.URL}
	case closed != nil:
		return CheckResult{Verdict: VerdictClosedUnmerged, PRNumber: closed.Number, PRURL: closed.URL}
	}
	return CheckResult{Verdict: VerdictNoPR}
}

// evaluateRollup ports determineCIStatus (internal/web/fetcher.go) semantics
// to a gate verdict, additionally collecting failing/pending check names.
// Checks whose display name matches ignore (case-insensitive) are human
// approval gates, not CI — they are excluded from the evaluation entirely,
// whatever state they report (AA-859).
//
// Case note: gh renders the GraphQL rollup enums in UPPERCASE for check runs
// ("conclusion":"SUCCESS", "status":"COMPLETED" — verified live against
// gh 2.45), while older code paths compared lowercase. Normalize both ways
// so the gate works across gh versions.
func evaluateRollup(checks []rollupEntry, ignore []string) CheckResult {
	if len(checks) == 0 {
		// Open PR with no checks reported yet — treat as pending so a
		// just-pushed PR isn't waved through before CI registers. The
		// pending wait window absorbs webhook lag.
		return CheckResult{Verdict: VerdictPending, PendingChecks: []string{"checks not yet reported"}}
	}
	var failing, pending []string
	kept := 0
	for _, check := range checks {
		if isIgnoredCheck(check.displayName(), ignore) {
			continue
		}
		kept++
		// Conclusion first (completed check runs).
		switch strings.ToLower(check.Conclusion) {
		case "failure", "cancelled", "timed_out", "action_required", "startup_failure": //nolint:misspell // GitHub API returns "cancelled" (British spelling)
			failing = append(failing, check.displayName())
		case "success", "skipped", "neutral":
			// Pass.
		case "stale":
			// Marked stale by a newer commit — results no longer trustworthy.
			pending = append(pending, check.displayName())
		default:
			// In-progress check runs report via Status.
			switch strings.ToLower(check.Status) {
			case "queued", "in_progress", "waiting", "pending", "requested":
				pending = append(pending, check.displayName())
			}
			// Commit statuses (Jenkins contexts) report via State.
			switch strings.ToUpper(check.State) {
			case "FAILURE", "ERROR":
				failing = append(failing, check.displayName())
			case "PENDING", "EXPECTED":
				pending = append(pending, check.displayName())
			}
		}
	}
	switch {
	case len(failing) > 0:
		return CheckResult{Verdict: VerdictRed, FailingChecks: failing, PendingChecks: pending}
	case len(pending) > 0:
		return CheckResult{Verdict: VerdictPending, PendingChecks: pending}
	case kept == 0:
		// Every reported check was an ignored human gate. Human gates
		// (e.g. pullapprove) register near-instantly on push, often before
		// the real CI does — treat this like the empty rollup so a
		// just-pushed PR isn't waved through before CI registers.
		return CheckResult{Verdict: VerdictPending, PendingChecks: []string{"CI checks not yet reported (only ignored human gates present)"}}
	}
	return CheckResult{Verdict: VerdictGreen}
}

// isIgnoredCheck reports whether a check's display name matches the ignore
// list (case-insensitive exact match).
func isIgnoredCheck(name string, ignore []string) bool {
	for _, ig := range ignore {
		if ig != "" && strings.EqualFold(name, ig) {
			return true
		}
	}
	return false
}

// WaitOptions configures WaitForGreen.
type WaitOptions struct {
	// Timeout is the total time to wait for pending checks to settle.
	Timeout time.Duration
	// PollInterval is the initial poll interval; it doubles per poll up to
	// 4x (30s → 60s → 120s with the 30s default).
	PollInterval time.Duration
	// Progress, when set, receives one line per poll.
	Progress io.Writer
	// Sleep is a test hook; nil means time.Sleep.
	Sleep func(time.Duration)
	// Now is a test hook; nil means time.Now.
	Now func() time.Time
}

// WaitForGreen polls CheckBranch while the verdict is PENDING, with backoff,
// until the checks settle or the timeout elapses. It returns the last result
// and whether the wait timed out with checks still pending.
func (g *Gate) WaitForGreen(dir, branch string, opts WaitOptions) (CheckResult, bool) {
	sleep := opts.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	interval := opts.PollInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	maxInterval := interval * 4
	deadline := now().Add(opts.Timeout)

	res := g.CheckBranch(dir, branch)
	for res.Verdict == VerdictPending {
		remaining := deadline.Sub(now())
		if remaining <= 0 {
			return res, true
		}
		if interval > remaining {
			interval = remaining
		}
		if opts.Progress != nil {
			fmt.Fprintf(opts.Progress, "  CI pending (%s) — next poll in %s, %s remaining\n",
				strings.Join(res.PendingChecks, ", "), interval.Round(time.Second), remaining.Round(time.Second))
		}
		sleep(interval)
		if interval < maxInterval {
			interval *= 2
			if interval > maxInterval {
				interval = maxInterval
			}
		}
		res = g.CheckBranch(dir, branch)
	}
	return res, false
}
