package refinery

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

type fakePostMergeLifecycle struct {
	mr         *MergeRequest
	agent      *beads.Issue
	source     *beads.Issue
	fail       map[string]error
	events     []string
	sourceGone bool
	sourceNoop bool
}

func successfulPostMergeLifecycle() *fakePostMergeLifecycle {
	mr := &MergeRequest{
		ID:           "gt-mr",
		Branch:       "polecat/test/gt-work",
		AgentBead:    "hq-agent",
		IssueID:      "gt-work",
		TargetBranch: "main",
		CommitSHA:    "submitted",
		Status:       MROpen,
	}
	return &fakePostMergeLifecycle{
		mr:     mr,
		source: &beads.Issue{ID: "gt-work", Status: string(beads.StatusOpen), Labels: []string{"gt:task"}},
		agent: &beads.Issue{
			ID:     "hq-agent",
			Title:  "agent",
			Labels: []string{"gt:agent"},
			Description: beads.FormatAgentDescription("agent", &beads.AgentFields{
				AgentState:    string(beads.AgentStateDone),
				CleanupStatus: "clean",
				ActiveMR:      "gt-mr",
			}),
		},
		fail: make(map[string]error),
	}
}

func (f *fakePostMergeLifecycle) event(name string) error {
	f.events = append(f.events, name)
	return f.fail[name]
}

func (f *fakePostMergeLifecycle) loadMR(string) (*MergeRequest, error) {
	if err := f.event("load-mr"); err != nil {
		return nil, err
	}
	copy := *f.mr
	return &copy, nil
}

func (f *fakePostMergeLifecycle) verify(*MergeRequest) error { return f.event("proof") }

func (f *fakePostMergeLifecycle) closeMR(mr *MergeRequest, _ string) error {
	if err := f.event("close-mr"); err != nil {
		return err
	}
	if f.mr.Status != MRClosed {
		f.mr.Status = MRClosed
		f.mr.CloseReason = CloseReasonMerged
	}
	return nil
}

func (f *fakePostMergeLifecycle) loadSource(string) (*beads.Issue, error) {
	if err := f.event("load-source"); err != nil {
		return nil, err
	}
	if f.sourceGone {
		return nil, beads.ErrNotFound
	}
	return f.source, nil
}

func (f *fakePostMergeLifecycle) closeSource(string, string) error {
	if err := f.event("close-source"); err != nil {
		return err
	}
	if !f.sourceNoop {
		f.source.Status = string(beads.StatusClosed)
	}
	return nil
}

func (f *fakePostMergeLifecycle) loadAgent(string) (*beads.Issue, error) {
	if err := f.event("load-agent"); err != nil {
		return nil, err
	}
	return f.agent, nil
}

func (f *fakePostMergeLifecycle) finalizeAgent(_, _ string) error {
	if err := f.event("finalize-agent"); err != nil {
		return err
	}
	fields := beads.ParseAgentFields(f.agent.Description)
	fields.AgentState = string(beads.AgentStateIdle)
	fields.ActiveMR = ""
	f.agent.Description = beads.FormatAgentDescription(f.agent.Title, fields)
	return nil
}

func (f *fakePostMergeLifecycle) releaseSlot() error { return f.event("release-slot") }

func (f *fakePostMergeLifecycle) coordinator() *postMergeCoordinator {
	return &postMergeCoordinator{
		loadMR:        f.loadMR,
		verifyProof:   f.verify,
		closeMR:       f.closeMR,
		loadSource:    f.loadSource,
		closeSource:   f.closeSource,
		loadAgent:     f.loadAgent,
		finalizeAgent: f.finalizeAgent,
		releaseSlot:   f.releaseSlot,
	}
}

func TestPostMergeCoordinatorProofFailureHasNoMutations(t *testing.T) {
	f := successfulPostMergeLifecycle()
	f.fail["proof"] = errors.New("not reachable")

	_, err := f.coordinator().run(f.mr, "merge")
	if err == nil || !strings.Contains(err.Error(), "not reachable") {
		t.Fatalf("run error = %v, want proof failure", err)
	}
	if want := []string{"load-mr", "proof"}; !reflect.DeepEqual(f.events, want) {
		t.Fatalf("events = %v, want %v", f.events, want)
	}
	if f.mr.Status != MROpen || f.source.Status != string(beads.StatusOpen) {
		t.Fatalf("proof failure mutated MR/source: mr=%s source=%s", f.mr.Status, f.source.Status)
	}
	fields := beads.ParseAgentFields(f.agent.Description)
	if fields.AgentState != string(beads.AgentStateDone) || fields.ActiveMR != f.mr.ID {
		t.Fatalf("proof failure mutated agent: %+v", fields)
	}
}

func TestPostMergeCoordinatorRunsOrderedLifecycle(t *testing.T) {
	f := successfulPostMergeLifecycle()

	result, err := f.coordinator().run(f.mr, "merge")
	if err != nil {
		t.Fatalf("run error: %v", err)
	}
	want := []string{
		"load-mr", "proof", "close-mr", "load-mr",
		"load-source", "close-source", "load-source", "load-agent", "finalize-agent", "release-slot",
	}

	if !reflect.DeepEqual(f.events, want) {
		t.Fatalf("events = %v, want %v", f.events, want)
	}
	if !result.MRClosed || !result.SourceIssueClosed || result.SourceIssueID != "gt-work" {
		t.Fatalf("result = %+v", result)
	}
	fields := beads.ParseAgentFields(f.agent.Description)
	if fields.AgentState != string(beads.AgentStateIdle) || fields.ActiveMR != "" {
		t.Fatalf("agent not finalized: %+v", fields)
	}
}

func TestPostMergeCoordinatorRequiresAuthoritativeSourceClosure(t *testing.T) {
	f := successfulPostMergeLifecycle()
	f.sourceNoop = true

	if _, err := f.coordinator().run(f.mr, "merge"); err == nil {
		t.Fatal("run accepted a successful close call without terminal source state")
	}
	for _, event := range f.events {
		if event == "load-agent" || event == "finalize-agent" || event == "release-slot" {
			t.Fatalf("source closure failure reached later stage: %v", f.events)
		}
	}
}

func TestPostMergeCoordinatorRejectsSnapshotDriftIncludingEmptyExpectedFields(t *testing.T) {
	f := successfulPostMergeLifecycle()
	expected := *f.mr
	expected.CommitSHA = ""

	_, err := f.coordinator().run(&expected, "merge")
	if err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("run error = %v, want snapshot drift", err)
	}
	if want := []string{"load-mr"}; !reflect.DeepEqual(f.events, want) {
		t.Fatalf("events = %v, want %v", f.events, want)
	}
}

func TestPostMergeCoordinatorRequiresAuthoritativeMergedTerminalReason(t *testing.T) {
	for _, reason := range []CloseReason{CloseReasonRejected, ""} {
		t.Run(string(reason), func(t *testing.T) {
			f := successfulPostMergeLifecycle()
			f.mr.Status = MRClosed
			f.mr.CloseReason = reason

			_, err := f.coordinator().run(f.mr, "merge")
			if err == nil || !strings.Contains(err.Error(), "close_reason") {
				t.Fatalf("run error = %v, want terminal reason failure", err)
			}
			want := []string{"load-mr", "proof", "close-mr", "load-mr"}
			if !reflect.DeepEqual(f.events, want) {
				t.Fatalf("events = %v, want %v", f.events, want)
			}
		})
	}
}

func TestPostMergeCoordinatorKnownSourceNotFoundIsAuthoritativeAbsence(t *testing.T) {
	f := successfulPostMergeLifecycle()
	f.sourceGone = true

	result, err := f.coordinator().run(f.mr, "merge")
	if err != nil {
		t.Fatalf("run error: %v", err)
	}
	if !result.SourceIssueNotFound || result.SourceIssueClosed {
		t.Fatalf("result = %+v, want authoritative source absence", result)
	}
}

func TestPostMergeCoordinatorPropagatesOrderedStageFailures(t *testing.T) {
	stages := []string{"load-mr", "proof", "close-mr", "load-source", "close-source", "load-agent", "finalize-agent", "release-slot"}
	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			f := successfulPostMergeLifecycle()
			f.fail[stage] = errors.New("injected " + stage)

			_, err := f.coordinator().run(f.mr, "merge")
			if err == nil || !strings.Contains(err.Error(), "injected "+stage) {
				t.Fatalf("run error = %v", err)
			}
			if got := f.events[len(f.events)-1]; got != stage {
				t.Fatalf("last event = %q, want %q; events=%v", got, stage, f.events)
			}
		})
	}
}

func TestPostMergeCoordinatorAgentBlockerMatrix(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*beads.AgentFields)
	}{
		{"non-done", func(f *beads.AgentFields) { f.AgentState = "working" }},
		{"mismatched-mr", func(f *beads.AgentFields) { f.ActiveMR = "gt-other" }},
		{"dirty-cleanup", func(f *beads.AgentFields) { f.CleanupStatus = "has_uncommitted" }},
		{"hook", func(f *beads.AgentFields) { f.HookBead = "gt-hook" }},
		{"mr-failed", func(f *beads.AgentFields) { f.MRFailed = true }},
		{"push-failed", func(f *beads.AgentFields) { f.PushFailed = true }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := successfulPostMergeLifecycle()
			fields := beads.ParseAgentFields(f.agent.Description)
			tt.mutate(fields)
			f.agent.Description = beads.FormatAgentDescription(f.agent.Title, fields)

			_, err := f.coordinator().run(f.mr, "merge")
			if err == nil {
				t.Fatal("run succeeded for blocked agent")
			}
			got := beads.ParseAgentFields(f.agent.Description)
			if got.AgentState == string(beads.AgentStateIdle) {
				t.Fatalf("blocked agent became idle: %+v", got)
			}
			for _, event := range f.events {
				if event == "finalize-agent" || event == "release-slot" {
					t.Fatalf("blocked agent reached later stage: %v", f.events)
				}
			}
		})
	}
}

func TestPostMergeCoordinatorMissingAgentAndUnresolvedSourceAreErrors(t *testing.T) {
	t.Run("missing-agent", func(t *testing.T) {
		f := successfulPostMergeLifecycle()
		f.agent = nil
		if _, err := f.coordinator().run(f.mr, "merge"); err == nil {
			t.Fatal("run succeeded with missing agent")
		}
	})
	t.Run("unresolved-source", func(t *testing.T) {
		f := successfulPostMergeLifecycle()
		f.mr.IssueID = ""
		if _, err := f.coordinator().run(f.mr, "merge"); err == nil {
			t.Fatal("run succeeded with unresolved source")
		}
	})
}

func TestPostMergeCoordinatorRetryAfterReacquisitionDoesNotReleaseNewLease(t *testing.T) {
	f := successfulPostMergeLifecycle()
	c := f.coordinator()
	activeLease := "lifecycle-attempt-1"
	c.releaseSlot = func() error {
		f.events = append(f.events, "release-slot")
		activeLease = ""
		return nil
	}
	if _, err := c.run(f.mr, "merge"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if activeLease != "" {
		t.Fatalf("first lifecycle left lease %q held", activeLease)
	}

	activeLease = "lifecycle-attempt-2"
	f.events = nil
	if _, err := c.run(f.mr, "merge"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	fields := beads.ParseAgentFields(f.agent.Description)
	if fields.AgentState != string(beads.AgentStateIdle) || fields.ActiveMR != "" {
		t.Fatalf("retry changed finalized agent incorrectly: %+v", fields)
	}
	for _, event := range f.events {
		if event == "release-slot" {
			t.Fatalf("retry released a lease acquired after the completed lifecycle: %v", f.events)
		}
	}
	if activeLease != "lifecycle-attempt-2" {
		t.Fatalf("retry changed reacquired lease to %q", activeLease)
	}
}

func TestPostMergeCoordinatorDoesNotAcceptBlockedIdleAgentAsFinalized(t *testing.T) {
	f := successfulPostMergeLifecycle()
	fields := beads.ParseAgentFields(f.agent.Description)
	fields.AgentState = string(beads.AgentStateIdle)
	fields.ActiveMR = ""
	fields.CleanupStatus = "has_uncommitted"
	f.agent.Description = beads.FormatAgentDescription(f.agent.Title, fields)

	if _, err := f.coordinator().run(f.mr, "merge"); err == nil {
		t.Fatal("run accepted dirty idle agent as finalized")
	}

	for _, event := range f.events {
		if event == "release-slot" {
			t.Fatalf("blocked idle agent released slot: %v", f.events)
		}
	}
}

func TestEngineerMergedCloseHelperRequiresCoordinator(t *testing.T) {
	e := &Engineer{}
	if err := e.closeMRWithReason(&MRInfo{ID: "gt-mr"}, string(CloseReasonMerged)); err == nil {
		t.Fatal("direct merged close helper bypassed coordinator")
	}
}
