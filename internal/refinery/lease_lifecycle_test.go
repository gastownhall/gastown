package refinery

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

type fakePersistedMergeSlot struct {
	status       *beads.MergeSlotStatus
	acquisitions int
	transitions  []beads.MergeLeasePhase
	releases     []string
	releaseErrs  []error
}

func (f *fakePersistedMergeSlot) check() (*beads.MergeSlotStatus, error) {
	if f.status == nil {
		return &beads.MergeSlotStatus{Available: true}, nil
	}
	copy := *f.status
	if f.status.Lease != nil {
		leaseCopy := *f.status.Lease
		leaseCopy.MRIDs = append([]string(nil), f.status.Lease.MRIDs...)
		leaseCopy.SubmittedSHAs = append([]string(nil), f.status.Lease.SubmittedSHAs...)
		copy.Lease = &leaseCopy
	}
	return &copy, nil
}

func (f *fakePersistedMergeSlot) acquire(holder string, _ bool, lease beads.MergeLease) (*beads.MergeSlotStatus, error) {
	f.acquisitions++
	if f.status != nil && f.status.Holder != "" && f.status.Holder != holder {
		return f.check()
	}
	leaseCopy := lease
	f.status = &beads.MergeSlotStatus{Holder: holder, Lease: &leaseCopy}
	return f.check()
}

func (f *fakePersistedMergeSlot) transition(holder string, phase beads.MergeLeasePhase) error {
	if f.status == nil || f.status.Holder != holder || f.status.Lease == nil ||
		f.status.Lease.OwnerToken != holder {
		return fmt.Errorf("token mismatch")
	}
	f.transitions = append(f.transitions, phase)
	f.status.Lease.Phase = phase
	return nil
}

func (f *fakePersistedMergeSlot) release(holder string) error {
	f.releases = append(f.releases, holder)
	if len(f.releaseErrs) > 0 {
		err := f.releaseErrs[0]
		f.releaseErrs = f.releaseErrs[1:]
		if err != nil {
			return err
		}
	}
	if f.status == nil || f.status.Holder != holder || f.status.Lease == nil ||
		f.status.Lease.OwnerToken != holder {
		return fmt.Errorf("release token mismatch")
	}
	f.status = nil
	return nil
}

func installPersistedMergeSlot(e *Engineer, slot *fakePersistedMergeSlot) {
	e.mergeSlotCheck = slot.check
	e.mergeSlotAcquireLease = slot.acquire
	e.mergeSlotTransition = slot.transition
	e.mergeSlotRelease = slot.release
}

func prepareLeaseDoMerge(t *testing.T) (*Engineer, *MRInfo, string) {
	t.Helper()
	workDir, g, _ := testGitRepo(t)
	createFeatureBranch(t, workDir, "polecat/test/lease", "lease.txt", "lease\n")
	submitted := run(t, workDir, "git", "rev-parse", "polecat/test/lease")
	prePush := run(t, workDir, "git", "rev-parse", "origin/main")
	e := newTestEngineer(t, workDir, g)
	e.config.RunTests = false
	e.config.AutoPush = true
	mr := &MRInfo{ID: "mr-lease", Branch: "polecat/test/lease", Target: "main", CommitSHA: submitted}
	return e, mr, prePush
}

func TestDoMerge_VerificationErrorAfterSuccessfulPushRetainsLease(t *testing.T) {
	e, mr, prePush := prepareLeaseDoMerge(t)
	slot := &fakePersistedMergeSlot{}
	installPersistedMergeSlot(e, slot)
	e.pushRemoteBranchTip = func(_, _ string) (string, error) { return prePush, nil }
	e.pushTarget = func(_, _ string, _ bool) error { return nil }
	e.verifyPushedCommit = func(_, _, _ string) error { return errors.New("verification unavailable") }

	result := e.doMerge(context.Background(), mr)
	if result.Success || result.Error == "" {
		t.Fatalf("doMerge = %+v, want verification failure", result)
	}
	if slot.status == nil || slot.status.Lease == nil ||
		slot.status.Lease.Phase != beads.MergeLeasePhaseMutationPossible {
		t.Fatalf("lease after verification failure = %+v, want retained mutation-possible", slot.status)
	}
	if len(slot.releases) != 0 {
		t.Fatalf("verification failure released lease: %v", slot.releases)
	}
}

func TestFastForwardBatch_VerificationErrorAfterSuccessfulPushRetainsSharedLease(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	writeFile(t, workDir, "batch-lease.txt", "batch\n")
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "batch lease")
	prePush := run(t, workDir, "git", "rev-parse", "origin/main")
	e := newTestEngineer(t, workDir, g)
	slot := &fakePersistedMergeSlot{}
	installPersistedMergeSlot(e, slot)
	e.pushRemoteBranchTip = func(_, _ string) (string, error) { return prePush, nil }
	e.pushTarget = func(_, _ string, _ bool) error { return nil }
	e.verifyPushedCommit = func(_, _, _ string) error { return errors.New("verification unavailable") }
	stacked := []*MRInfo{
		{ID: "mr-1", Branch: "polecat/test/one", Target: "main", CommitSHA: "one"},
		{ID: "mr-2", Branch: "polecat/test/two", Target: "main", CommitSHA: "two"},
	}

	result := e.fastForwardBatch(context.Background(), stacked, "main", &BatchResult{})
	if result.Error == nil {
		t.Fatal("fastForwardBatch succeeded despite verification failure")
	}
	if slot.status == nil || slot.status.Lease == nil ||
		slot.status.Lease.Phase != beads.MergeLeasePhaseMutationPossible ||
		slot.status.Lease.BatchID == "" {
		t.Fatalf("shared lease after verification failure = %+v", slot.status)
	}
	if len(slot.releases) != 0 {
		t.Fatalf("verification failure released shared lease: %v", slot.releases)
	}
}

func TestPushErrorWithUnchangedRemoteReleasesLease(t *testing.T) {
	e, mr, prePush := prepareLeaseDoMerge(t)
	slot := &fakePersistedMergeSlot{}
	installPersistedMergeSlot(e, slot)
	e.pushRemoteBranchTip = func(_, _ string) (string, error) { return prePush, nil }
	e.pushTarget = func(_, _ string, _ bool) error { return errors.New("push rejected") }

	result := e.doMerge(context.Background(), mr)
	if result.Success || result.Error == "" {
		t.Fatalf("doMerge = %+v, want push failure", result)
	}
	if slot.status != nil {
		t.Fatalf("unchanged remote retained lease: %+v", slot.status)
	}
	if len(slot.releases) != 1 {
		t.Fatalf("release count = %d, want exactly 1", len(slot.releases))
	}
}

func TestPushErrorWithUnavailableProofRetainsLease(t *testing.T) {
	e, mr, prePush := prepareLeaseDoMerge(t)
	slot := &fakePersistedMergeSlot{}
	installPersistedMergeSlot(e, slot)
	proofs := 0
	e.pushRemoteBranchTip = func(_, _ string) (string, error) {
		proofs++
		if proofs <= 2 {
			return prePush, nil
		}
		return "", errors.New("remote unavailable")
	}
	e.pushTarget = func(_, _ string, _ bool) error { return errors.New("push transport failed") }

	result := e.doMerge(context.Background(), mr)
	if result.Success || result.Error == "" {
		t.Fatalf("doMerge = %+v, want push failure", result)
	}
	if slot.status == nil || slot.status.Lease == nil ||
		slot.status.Lease.Phase != beads.MergeLeasePhaseMutationPossible {
		t.Fatalf("unavailable proof did not retain lease: %+v", slot.status)
	}
	if len(slot.releases) != 0 {
		t.Fatalf("unavailable proof released lease: %v", slot.releases)
	}
}

func TestDoMerge_PrePushFailureReleasesLeaseExactlyOnce(t *testing.T) {
	e, mr, prePush := prepareLeaseDoMerge(t)
	slot := &fakePersistedMergeSlot{}
	installPersistedMergeSlot(e, slot)
	e.pushRemoteBranchTip = func(_, _ string) (string, error) { return prePush, nil }
	e.mergeSlotAcquireLease = func(holder string, addWaiter bool, lease beads.MergeLease) (*beads.MergeSlotStatus, error) {
		status, err := slot.acquire(holder, addWaiter, lease)
		e.testAllowSyntheticMRs = false
		return status, err
	}

	result := e.doMerge(context.Background(), mr)
	if result.Success || result.Error == "" {
		t.Fatalf("doMerge = %+v, want pre-push failure", result)
	}
	if len(slot.releases) != 1 {
		t.Fatalf("pre-push release count = %d, want exactly 1", len(slot.releases))
	}
}

func TestLifecycleRetryResumesOriginalLeaseAndReleasesOnce(t *testing.T) {
	e, mr, _ := prepareLeaseDoMerge(t)
	slot := &fakePersistedMergeSlot{status: &beads.MergeSlotStatus{
		Holder: "original-token",
		Lease: &beads.MergeLease{
			OwnerToken:    "original-token",
			Phase:         beads.MergeLeasePhaseProofEstablished,
			Target:        "main",
			PrePushSHA:    "before",
			IntendedTip:   "after",
			MRIDs:         []string{"mr-lease"},
			SubmittedSHAs: []string{mr.CommitSHA},
		},
	}}
	installPersistedMergeSlot(e, slot)
	e.verifyPushedReachable = func(_, _, _ string) error { return nil }

	result := e.doMerge(context.Background(), mr)
	if !result.Success || result.MergeSlotHolder != "original-token" {
		t.Fatalf("resumed doMerge = %+v", result)
	}
	if err := e.completeMergeLease(result.MergeSlotHolder); err != nil {
		t.Fatalf("complete retained lease: %v", err)
	}
	if slot.acquisitions != 0 {
		t.Fatalf("retry acquired %d leases", slot.acquisitions)
	}
	if len(slot.releases) != 1 || slot.releases[0] != "original-token" {
		t.Fatalf("releases = %v, want original token once", slot.releases)
	}
}

func TestCompletedLifecycleRetryDoesNotAcquireOrReleaseDifferentLease(t *testing.T) {
	e, mr, _ := prepareLeaseDoMerge(t)
	slot := &fakePersistedMergeSlot{status: &beads.MergeSlotStatus{
		Holder: "completed-original-token",
		Lease: &beads.MergeLease{
			OwnerToken:    "completed-original-token",
			Phase:         beads.MergeLeasePhaseLifecycleComplete,
			Target:        "main",
			PrePushSHA:    "before",
			IntendedTip:   "after",
			MRIDs:         []string{"mr-lease"},
			SubmittedSHAs: []string{mr.CommitSHA},
		},
	}}
	installPersistedMergeSlot(e, slot)
	e.verifyPushedReachable = func(_, _, _ string) error { return nil }

	result := e.doMerge(context.Background(), mr)
	if !result.Success || result.MergeLeasePhase != beads.MergeLeasePhaseLifecycleComplete {
		t.Fatalf("completed retry doMerge = %+v", result)
	}
	if err := e.completeMergeLease(result.MergeSlotHolder); err != nil {
		t.Fatalf("release completed retained lease: %v", err)
	}
	if slot.acquisitions != 0 {
		t.Fatalf("completed retry acquired %d leases", slot.acquisitions)
	}
	if len(slot.releases) != 1 || slot.releases[0] != "completed-original-token" {
		t.Fatalf("completed retry releases = %v", slot.releases)
	}
}

func TestReleaseFailureRetryReleasesOriginalToken(t *testing.T) {
	e, _, _ := prepareLeaseDoMerge(t)
	slot := &fakePersistedMergeSlot{
		status: &beads.MergeSlotStatus{
			Holder: "release-failure-original",
			Lease: &beads.MergeLease{
				OwnerToken:    "release-failure-original",
				Phase:         beads.MergeLeasePhaseLifecycleComplete,
				Target:        "main",
				PrePushSHA:    "before",
				IntendedTip:   "after",
				MRIDs:         []string{"mr-lease"},
				SubmittedSHAs: []string{"submitted"},
			},
		},
		releaseErrs: []error{errors.New("write failed"), nil},
	}
	installPersistedMergeSlot(e, slot)

	if err := e.completeMergeLease("release-failure-original"); err == nil {
		t.Fatal("first release unexpectedly succeeded")
	}
	if err := e.completeMergeLease("release-failure-original"); err != nil {
		t.Fatalf("release retry: %v", err)
	}
	if slot.acquisitions != 0 {
		t.Fatalf("release retry acquired %d leases", slot.acquisitions)
	}
	if len(slot.releases) != 2 ||
		slot.releases[0] != "release-failure-original" ||
		slot.releases[1] != "release-failure-original" {
		t.Fatalf("release attempts = %v, want original token", slot.releases)
	}
}
