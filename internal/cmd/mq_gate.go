package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/refinery"
	"github.com/steveyegge/gastown/internal/workflow"
)

// Exit codes of gt mq gate.
const (
	mqGateExitReady    = 0 // ready to merge (or not gated)
	mqGateExitError    = 1 // any other error / refusal
	mqGateExitPending  = 3 // CI pending: requeue
	mqGateExitRejected = 5 // CI failed: item back to execute, MR rejected
)

var mqGateJSON bool

var mqGateCmd = &cobra.Command{
	Use:   "gate <rig> <mr-id>",
	Short: "Run the gated-workflow merge gate for a merge request (no merge)",
	Long: `Run the refinery merge gate for a merge request without merging it.

For an MR whose source issue is a gated workflow item, the gate requires the
item at step merge, a human-approved review.md, pushes the approved version
bump commit onto the MR branch, finds the PR at that head and reads its CI.

Exit codes:
  0  ready to merge: bump pushed, CI green on the head (also: MR not gated)
  3  CI pending: requeue and skip this MR for now
  5  CI failed: the item went back to execute and the MR was rejected
  1  any other error or refusal

The refinery merges a gated MR only after this exits 0, and gt mq post-merge
refuses a gated MR that did not pass it.

Examples:
  gt mq gate gastown gt-mr-abc123
  gt mq gate gastown gt-mr-abc123 --json`,
	Args: cobra.ExactArgs(2),
	RunE: runMQGate,
}

func init() {
	mqGateCmd.Flags().BoolVar(&mqGateJSON, "json", false, "Output JSON: {gated, result, reason, head_sha, version}")
	mqCmd.AddCommand(mqGateCmd)
}

// mqGateRun runs the merge gate for mrID in rigName. Tests replace it.
var mqGateRun = func(rigName, mrID string) (refinery.GateDecision, error) {
	_, r, err := getRig(rigName)
	if err != nil {
		return refinery.GateDecision{Result: refinery.GateError}, err
	}
	eng := refinery.NewEngineer(r)
	// Engineer progress goes to stderr so --json stays parseable.
	eng.SetOutput(os.Stderr)
	if err := eng.LoadConfig(); err != nil {
		return refinery.GateDecision{Result: refinery.GateError}, err
	}
	return eng.GateMR(context.Background(), mrID)
}

func runMQGate(cmd *cobra.Command, args []string) error {
	d, err := mqGateRun(args[0], args[1])
	if err != nil {
		d.Result = refinery.GateError
		if d.Reason == "" {
			d.Reason = err.Error()
		}
	}
	out := cmd.OutOrStdout()
	if mqGateJSON {
		data, mErr := json.Marshal(d)
		if mErr != nil {
			return mErr
		}
		_, _ = fmt.Fprintln(out, string(data))
	} else {
		line := d.Result
		if d.HeadSHA != "" {
			line += " " + d.HeadSHA
		}
		if d.Version != "" {
			line += " " + d.Version
		}
		if d.Reason != "" {
			line += ": " + d.Reason
		}
		_, _ = fmt.Fprintln(out, line)
	}
	switch d.Result {
	case refinery.GateReady, refinery.GatePassthrough:
		return nil
	case refinery.GatePending:
		return NewSilentExit(mqGateExitPending)
	case refinery.GateRejected:
		return NewSilentExit(mqGateExitRejected)
	default:
		return NewSilentExit(mqGateExitError)
	}
}

// mqPostMergeGatedCheck guards gt mq post-merge for gated workflow items.
// For a non-gated MR it returns (nil, nil). For a gated one it requires the
// item at step merge (or already done by a previous post-merge run) and the
// latest CI CheckRun to be a gtb-verified pass naming the MR's head, then
// returns a function that records merge->done, to call before closing.
// Otherwise it refuses.
func mqPostMergeGatedCheck(rigName, rigPath string, mr *refinery.MergeRequest) (recordDone func() error, err error) {
	if mr == nil || strings.TrimSpace(mr.IssueID) == "" {
		return nil, nil
	}
	cfg, err := workflow.LoadConfig(rigPath)
	if err != nil {
		return nil, fmt.Errorf("rig settings %s do not parse: merges paused until settings parse: %w", filepath.Join(rigPath, "settings", "config.json"), err)
	}
	if !cfg.Enabled {
		return nil, nil
	}
	id := strings.TrimSpace(mr.IssueID)
	env, envErr := newWorkflowEnv(nil, rigName)
	if envErr != nil {
		if _, gErr := workflow.NewBeadsItems(beads.New(rigPath)).Get(id); errors.Is(gErr, workflow.ErrItemNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("gated item %s: cannot open the workflow environment: %w", id, envErr)
	}
	it, err := env.Items.Get(id)
	if errors.Is(err, workflow.ErrItemNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("gated item %s: %w", id, err)
	}

	refuse := func(why string) error {
		return fmt.Errorf("gated item %s did not pass the merge gate (run gtb mq gate): %s", id, why)
	}
	step, history, err := workflow.CurrentStep(env, it)
	if err != nil {
		return nil, refuse(fmt.Sprintf("reading transitions: %v", err))
	}
	if !history || (step != "merge" && step != workflow.StepDone) {
		return nil, refuse(fmt.Sprintf("item is at step %s, not merge", step))
	}
	checks, err := env.Store.Checks(id)
	if err != nil {
		return nil, refuse(fmt.Sprintf("reading checks: %v", err))
	}
	var ci []workflow.CheckRun
	for _, c := range checks {
		if c.CmdSetSHA == refinery.CICheckCmdSet && c.Source == "gtb" && c.Provenance == "verified" {
			ci = append(ci, c)
		}
	}
	sort.SliceStable(ci, func(i, j int) bool { return ci[i].At.Before(ci[j].At) })
	head := strings.TrimSpace(mr.CommitSHA)
	if len(ci) == 0 {
		return nil, refuse("no CI evidence")
	}
	if last := ci[len(ci)-1]; !last.Passed || head == "" || last.Tail != refinery.CIHeadTail(head) {
		return nil, refuse(fmt.Sprintf("latest CI evidence is not a pass for head %s", head))
	}
	// The pass must be recorded at or after the item's latest transition into
	// merge: a pass from an earlier merge cycle does not count.
	transitions, err := env.Store.Transitions(id)
	if err != nil {
		return nil, refuse(fmt.Sprintf("reading transitions: %v", err))
	}
	var mergedAt time.Time
	for _, tr := range transitions {
		if tr.To == "merge" {
			mergedAt = tr.At
		}
	}
	if mergedAt.IsZero() {
		return nil, refuse("no transition into merge")
	}
	var freshPass bool
	for _, c := range ci {
		if c.Passed && c.Tail == refinery.CIHeadTail(head) && !c.At.IsZero() && !c.At.Before(mergedAt) {
			freshPass = true
		}
	}
	if !freshPass {
		return nil, refuse(fmt.Sprintf("no CI pass for head %s recorded since the item entered merge", head))
	}
	if step == workflow.StepDone {
		// A previous post-merge run recorded done; finish the cleanup.
		return func() error { return nil }, nil
	}
	return func() error {
		if err := env.Store.Transition(id, "merge", workflow.StepDone, "refinery"); err != nil {
			return fmt.Errorf("gated item %s: recording merge->done: %w", id, err)
		}
		now := time.Now()
		if env.Now != nil {
			now = env.Now()
		}
		if err := env.Items.SetStep(id, workflow.StepDone, now); err != nil {
			return fmt.Errorf("gated item %s: done recorded but set step failed: %w", id, err)
		}
		return nil
	}, nil
}
