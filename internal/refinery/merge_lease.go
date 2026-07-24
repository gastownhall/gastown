package refinery

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

type mergeLeasePushResult struct {
	holder      string
	phase       beads.MergeLeasePhase
	intendedTip string
}

type mergeEligibilityError struct {
	mr     *MRInfo
	result ProcessResult
}

func (e *mergeEligibilityError) Error() string {
	return e.result.Error
}

func singleMergeLease(mr *MRInfo, target, intendedTip string) beads.MergeLease {
	return beads.MergeLease{
		Phase:         beads.MergeLeasePhaseAcquired,
		Target:        strings.TrimSpace(target),
		IntendedTip:   strings.TrimSpace(intendedTip),
		MRIDs:         []string{strings.TrimSpace(mr.ID)},
		SubmittedSHAs: []string{strings.TrimSpace(mr.CommitSHA)},
	}
}

func batchMergeLease(stacked []*MRInfo, target, intendedTip string) beads.MergeLease {
	ids := make([]string, 0, len(stacked))
	submitted := make([]string, 0, len(stacked))
	parts := make([]string, 0, len(stacked))
	for _, mr := range stacked {
		ids = append(ids, strings.TrimSpace(mr.ID))
		submitted = append(submitted, strings.TrimSpace(mr.CommitSHA))
		parts = append(parts, strings.TrimSpace(mr.ID)+"@"+strings.TrimSpace(mr.CommitSHA))
	}
	return beads.MergeLease{
		Phase:         beads.MergeLeasePhaseAcquired,
		Target:        strings.TrimSpace(target),
		IntendedTip:   strings.TrimSpace(intendedTip),
		MRIDs:         ids,
		SubmittedSHAs: submitted,
		BatchID:       strings.TrimSpace(target) + ":" + strings.Join(parts, ","),
	}
}

func sameMergeLeaseIdentity(want, got beads.MergeLease) bool {
	return strings.TrimSpace(want.Target) == strings.TrimSpace(got.Target) &&
		strings.TrimSpace(want.BatchID) == strings.TrimSpace(got.BatchID) &&
		slices.Equal(want.MRIDs, got.MRIDs) &&
		slices.Equal(want.SubmittedSHAs, got.SubmittedSHAs)
}

func (e *Engineer) tryResumeSingleMergeLease(ctx context.Context, mr *MRInfo) (ProcessResult, bool) {
	if !e.config.AutoPush || mr.Target != e.rig.DefaultBranch() || e.mergeSlotCheck == nil {
		return ProcessResult{}, false
	}
	status, err := e.mergeSlotCheck()
	if err != nil {
		return ProcessResult{Error: fmt.Sprintf("check retained merge lease: %v", err)}, true
	}
	identity := singleMergeLease(mr, mr.Target, "")
	if status == nil || status.Lease == nil || !sameMergeLeaseIdentity(identity, *status.Lease) {
		return ProcessResult{}, false
	}
	resumed, err := e.resumeProofBoundPush(ctx, status, func() error {
		eligibility := e.recheckMRStillMergeable(mr, mr.Target)
		if !eligibility.Success {
			return &mergeEligibilityError{mr: mr, result: eligibility}
		}
		return nil
	})
	if err != nil {
		return ProcessResult{Error: err.Error()}, true
	}
	return ProcessResult{
		Success:         true,
		MergeCommit:     resumed.intendedTip,
		MergeSlotHolder: resumed.holder,
		MergeLeasePhase: resumed.phase,
	}, true
}

func (e *Engineer) retainedBatchLease(stacked []*MRInfo, target string) (*beads.MergeSlotStatus, error) {
	if target != e.rig.DefaultBranch() || e.mergeSlotCheck == nil {
		return nil, nil
	}
	status, err := e.mergeSlotCheck()
	if err != nil {
		return nil, fmt.Errorf("check retained batch merge lease: %w", err)
	}
	identity := batchMergeLease(stacked, target, "")
	if status == nil || status.Lease == nil || !sameMergeLeaseIdentity(identity, *status.Lease) {
		return nil, nil
	}
	return status, nil
}

func (e *Engineer) executeProofBoundPush(
	ctx context.Context,
	lease beads.MergeLease,
	prePushCheck func() error,
) (mergeLeasePushResult, error) {
	if e.mergeSlotCheck == nil || e.mergeSlotAcquireLease == nil || e.mergeSlotTransition == nil ||
		e.mergeSlotRelease == nil || e.pushRemoteBranchTip == nil || e.pushTarget == nil ||
		e.verifyPushedCommit == nil || e.verifyPushedReachable == nil {
		return mergeLeasePushResult{}, fmt.Errorf("merge lease dependencies are not configured")
	}
	status, err := e.mergeSlotCheck()
	if err != nil {
		return mergeLeasePushResult{}, fmt.Errorf("check retained merge lease: %w", err)
	}
	if status != nil && status.Lease != nil && sameMergeLeaseIdentity(lease, *status.Lease) {
		return e.resumeProofBoundPush(ctx, status, prePushCheck)
	}

	prePushSHA, err := e.pushRemoteBranchTip("origin", lease.Target)
	if err != nil {
		return mergeLeasePushResult{}, fmt.Errorf("capture pre-push target %s: %w", lease.Target, err)
	}
	if strings.TrimSpace(prePushSHA) == "" {
		return mergeLeasePushResult{}, fmt.Errorf("capture pre-push target %s: empty remote tip", lease.Target)
	}
	lease.PrePushSHA = prePushSHA
	holder, err := e.acquireMainPushLease(ctx, lease)
	if err != nil {
		return mergeLeasePushResult{}, err
	}
	lease.OwnerToken = holder

	current, err := e.pushRemoteBranchTip("origin", lease.Target)
	if err != nil {
		return mergeLeasePushResult{}, e.releasePreMutationLease(holder,
			fmt.Errorf("confirm captured pre-push target %s: %w", lease.Target, err))
	}
	if current != lease.PrePushSHA {
		return mergeLeasePushResult{}, e.releasePreMutationLease(holder,
			fmt.Errorf("target %s changed before lease acquisition: captured %s, authoritative %s",
				lease.Target, lease.PrePushSHA, current))
	}
	if err := prePushCheck(); err != nil {
		return mergeLeasePushResult{}, e.releasePreMutationLease(holder, err)
	}
	return e.pushUnderLease(holder, lease)
}

func (e *Engineer) resumeProofBoundPush(
	_ context.Context,
	status *beads.MergeSlotStatus,
	prePushCheck func() error,
) (mergeLeasePushResult, error) {
	lease := status.Lease
	if lease == nil || status.Holder == "" || lease.OwnerToken != status.Holder {
		return mergeLeasePushResult{}, fmt.Errorf("retained merge lease owner token mismatch")
	}
	holder := lease.OwnerToken
	switch lease.Phase {
	case beads.MergeLeasePhaseProofEstablished, beads.MergeLeasePhaseLifecycleComplete:
		if err := e.verifyPushedReachable("origin", lease.Target, lease.IntendedTip); err != nil {
			return mergeLeasePushResult{}, fmt.Errorf("re-prove retained merge lease: %w", err)
		}
		return mergeLeasePushResult{holder: holder, phase: lease.Phase, intendedTip: lease.IntendedTip}, nil
	case beads.MergeLeasePhaseAcquired, beads.MergeLeasePhaseMutationPossible:
		remoteTip, err := e.pushRemoteBranchTip("origin", lease.Target)
		if err != nil {
			return mergeLeasePushResult{}, fmt.Errorf("re-prove retained merge lease target: %w", err)
		}
		if remoteTip == lease.IntendedTip {
			if err := e.mergeSlotTransition(holder, beads.MergeLeasePhaseProofEstablished); err != nil {
				return mergeLeasePushResult{}, fmt.Errorf("persist proof-established merge lease: %w", err)
			}
			return mergeLeasePushResult{holder: holder, phase: beads.MergeLeasePhaseProofEstablished, intendedTip: lease.IntendedTip}, nil
		}
		if remoteTip != lease.PrePushSHA {
			if err := e.verifyPushedReachable("origin", lease.Target, lease.IntendedTip); err == nil {
				if transitionErr := e.mergeSlotTransition(holder, beads.MergeLeasePhaseProofEstablished); transitionErr != nil {
					return mergeLeasePushResult{}, fmt.Errorf("persist proof-established merge lease: %w", transitionErr)
				}
				return mergeLeasePushResult{holder: holder, phase: beads.MergeLeasePhaseProofEstablished, intendedTip: lease.IntendedTip}, nil
			}
			return mergeLeasePushResult{}, fmt.Errorf(
				"retained merge lease target is contradictory: pre-push=%s intended=%s authoritative=%s",
				lease.PrePushSHA, lease.IntendedTip, remoteTip)
		}
		if err := e.git.ResetHard(lease.IntendedTip); err != nil {
			return mergeLeasePushResult{}, fmt.Errorf("restore intended merge tip %s: %w", lease.IntendedTip, err)
		}
		if err := prePushCheck(); err != nil {
			if lease.Phase == beads.MergeLeasePhaseAcquired {
				return mergeLeasePushResult{}, e.releasePreMutationLease(holder, err)
			}
			return mergeLeasePushResult{}, err
		}
		return e.pushUnderLease(holder, *lease)
	default:
		return mergeLeasePushResult{}, fmt.Errorf("retained merge lease has invalid phase %q", lease.Phase)
	}
}

func (e *Engineer) pushUnderLease(holder string, lease beads.MergeLease) (mergeLeasePushResult, error) {
	if err := e.mergeSlotTransition(holder, beads.MergeLeasePhaseMutationPossible); err != nil {
		if lease.Phase == beads.MergeLeasePhaseAcquired {
			return mergeLeasePushResult{}, e.releasePreMutationLease(holder,
				fmt.Errorf("persist mutation-possible merge lease: %w", err))
		}
		return mergeLeasePushResult{}, fmt.Errorf("confirm mutation-possible merge lease: %w", err)
	}
	if err := e.pushTarget("origin", lease.Target, false); err != nil {
		pushErr := fmt.Errorf("push to origin/%s: %w", lease.Target, err)
		remoteTip, proofErr := e.pushRemoteBranchTip("origin", lease.Target)
		if proofErr != nil {
			return mergeLeasePushResult{}, errors.Join(pushErr,
				fmt.Errorf("authoritative post-error proof unavailable: %w", proofErr))
		}
		if remoteTip != lease.PrePushSHA {
			return mergeLeasePushResult{}, errors.Join(pushErr,
				fmt.Errorf("authoritative post-error target %s is %s, expected unchanged %s",
					lease.Target, remoteTip, lease.PrePushSHA))
		}
		if releaseErr := e.mergeSlotRelease(holder); releaseErr != nil {
			return mergeLeasePushResult{}, errors.Join(pushErr,
				fmt.Errorf("release unchanged-target merge lease: %w", releaseErr))
		}
		return mergeLeasePushResult{}, pushErr
	}
	if err := e.verifyPushedCommit("origin", lease.Target, lease.IntendedTip); err != nil {
		return mergeLeasePushResult{}, fmt.Errorf("verify pushed commit: %w", err)
	}
	if err := e.mergeSlotTransition(holder, beads.MergeLeasePhaseProofEstablished); err != nil {
		return mergeLeasePushResult{}, fmt.Errorf("persist proof-established merge lease: %w", err)
	}
	return mergeLeasePushResult{holder: holder, phase: beads.MergeLeasePhaseProofEstablished, intendedTip: lease.IntendedTip}, nil
}

func (e *Engineer) acquireMainPushLease(ctx context.Context, lease beads.MergeLease) (string, error) {
	slotID, err := e.mergeSlotEnsureExists()
	if err != nil {
		return "", fmt.Errorf("ensure merge slot exists: %w", err)
	}
	seq := atomic.AddUint64(&mergeSlotSeq, 1)
	holder := fmt.Sprintf("%s/refinery/push/%d-%d", e.rig.Name, time.Now().UnixNano(), seq)
	lease.OwnerToken = holder
	lease.Phase = beads.MergeLeasePhaseAcquired
	backoff := e.mergeSlotRetryBackoff
	if backoff <= 0 {
		backoff = 500 * time.Millisecond
	}
	for attempt := 0; attempt <= e.mergeSlotMaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", fmt.Errorf("context canceled while waiting for merge slot: %w", ctx.Err())
			case <-time.After(backoff):
			}
			if backoff < 5*time.Second {
				backoff *= 2
			}
		}
		status, acquireErr := e.mergeSlotAcquireLease(holder, false, lease)
		if acquireErr != nil {
			return "", fmt.Errorf("acquire merge slot %s: %w", slotID, acquireErr)
		}
		if status == nil {
			return "", fmt.Errorf("acquire merge slot %s: empty status", slotID)
		}
		if status.Holder == holder && status.Lease != nil && status.Lease.OwnerToken == holder {
			return holder, nil
		}
	}
	return "", fmt.Errorf("merge slot %s: %w after %d retries", slotID, errMergeSlotTimeout, e.mergeSlotMaxRetries)
}

func (e *Engineer) releasePreMutationLease(holder string, cause error) error {
	if releaseErr := e.mergeSlotRelease(holder); releaseErr != nil {
		return errors.Join(cause, fmt.Errorf("release pre-mutation merge lease: %w", releaseErr))
	}
	return cause
}

func (e *Engineer) completeMergeLease(holder string) error {
	if strings.TrimSpace(holder) == "" {
		return nil
	}
	status, err := e.mergeSlotCheck()
	if err != nil {
		return fmt.Errorf("check merge lease before lifecycle completion: %w", err)
	}
	if status == nil || status.Lease == nil || status.Holder != holder ||
		status.Lease.OwnerToken != holder {
		return fmt.Errorf("complete merge lease: exact owner token %q is not retained", holder)
	}
	switch status.Lease.Phase {
	case beads.MergeLeasePhaseProofEstablished:
		if err := e.mergeSlotTransition(holder, beads.MergeLeasePhaseLifecycleComplete); err != nil {
			return fmt.Errorf("persist lifecycle-complete merge lease: %w", err)
		}
	case beads.MergeLeasePhaseLifecycleComplete:
	default:
		return fmt.Errorf("complete merge lease: phase %q is not proof-established", status.Lease.Phase)
	}
	if err := e.mergeSlotRelease(holder); err != nil {
		return fmt.Errorf("release lifecycle-complete merge lease: %w", err)
	}
	return nil
}
