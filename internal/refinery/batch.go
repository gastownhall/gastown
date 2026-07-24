package refinery

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// BatchConfig holds configuration for the batch-then-bisect merge queue.
type BatchConfig struct {
	// MaxBatchSize is the maximum number of MRs to include in a single batch.
	// Larger batches increase throughput but increase bisection cost O(log N).
	// Default: 5.
	MaxBatchSize int `json:"max_batch_size"`

	// BatchWaitTime is how long to wait for the batch to fill before processing.
	// 0 means process immediately with whatever is available.
	// Default: 30s.
	BatchWaitTime time.Duration `json:"batch_wait_time"`

	// RetryBatchOnFlaky controls whether to retry the full batch once before
	// bisecting when tests fail. This avoids blaming an innocent MR for a
	// flaky test. Default: true.
	RetryBatchOnFlaky bool `json:"retry_batch_on_flaky"`
}

// DefaultBatchConfig returns sensible defaults for batch processing.
func DefaultBatchConfig() *BatchConfig {
	return &BatchConfig{
		MaxBatchSize:      5,
		BatchWaitTime:     30 * time.Second,
		RetryBatchOnFlaky: true,
	}
}

// BatchResult holds the outcome of processing a batch of MRs.
type BatchResult struct {
	// Merged is the set of MRs that were successfully merged.
	Merged []*MRInfo

	// Culprits is the set of MRs that caused test failures (identified via bisection).
	Culprits []*MRInfo

	// Conflicts is the set of MRs that had merge conflicts during stack construction.
	Conflicts []*MRInfo

	// MergeCommit is the final SHA pushed to the target branch (empty if nothing merged).
	MergeCommit string

	// Error is set if the batch processing encountered an infrastructure error.
	Error error
}

// AssembleBatch selects up to MaxBatchSize MRs from the ready queue.
// MRs are assumed to be pre-sorted by score (highest first).
// MRs that are blocked by other MRs not in the batch are excluded.
func (e *Engineer) AssembleBatch(readyMRs []*MRInfo, config *BatchConfig) []*MRInfo {
	if config == nil {
		config = DefaultBatchConfig()
	}
	maxSize := config.MaxBatchSize
	if maxSize <= 0 {
		maxSize = 5
	}

	batch := make([]*MRInfo, 0, maxSize)
	for _, mr := range readyMRs {
		if len(batch) >= maxSize {
			break
		}
		// Skip MRs blocked by something not already in this batch
		if mr.BlockedBy != "" {
			inBatch := false
			for _, b := range batch {
				if b.ID == mr.BlockedBy {
					inBatch = true
					break
				}
			}
			if !inBatch {
				continue
			}
		}
		batch = append(batch, mr)
	}
	return batch
}

// BuildRebaseStack constructs an ancestry-preserving merge stack on the target branch.
// Each MR is merged sequentially: target ← MR1 ← MR2 ← MR3.
// Returns the list of MRs that were successfully stacked, and any that
// conflicted (which are removed from the stack and the stack is rebuilt).
//
// On return, the git working directory is on the target branch with all
// successful MR merges applied (but not pushed).
func (e *Engineer) BuildRebaseStack(ctx context.Context, batch []*MRInfo, target string) (stacked []*MRInfo, conflicts []*MRInfo, err error) {
	if len(batch) == 0 {
		return nil, nil, nil
	}

	// Checkout target and ensure it's up to date
	if checkoutErr := e.git.Checkout(target); checkoutErr != nil {
		return nil, nil, fmt.Errorf("checkout target %s: %w", target, checkoutErr)
	}
	if pullErr := e.git.Pull("origin", target); pullErr != nil {
		_, _ = fmt.Fprintf(e.output, "[Batch] Warning: pull origin/%s: %v (continuing)\n", target, pullErr)
	}

	// Remember the base SHA to reset on retry
	baseSHA, err := e.git.Rev("HEAD")
	if err != nil {
		return nil, nil, fmt.Errorf("get base SHA: %w", err)
	}

	// Try to stack each MR via merge commit.
	for _, mr := range batch {
		_, _ = fmt.Fprintf(e.output, "[Batch] Stacking MR %s (branch %s)...\n", mr.ID, mr.Branch)

		// Check branch exists
		exists, brErr := e.git.BranchExists(mr.Branch)
		if brErr != nil || !exists {
			// Branch not found — escalate to mayor (gas-556)
			_, _ = fmt.Fprintf(e.output, "[Batch] MR %s: branch %s not found, escalating to mayor\n", mr.ID, mr.Branch)
			e.HandleMRInfoFailure(mr, ProcessResult{BranchNotFound: true})
			conflicts = append(conflicts, mr)
			continue
		}
		mergeRef, shaErr := e.submittedBranchHead(mr)
		if shaErr != nil {
			return nil, nil, shaErr
		}

		// Check for conflicts before merging
		conflictFiles, conflictErr := e.git.CheckConflicts(mergeRef, target)
		if conflictErr != nil || len(conflictFiles) > 0 {
			_, _ = fmt.Fprintf(e.output, "[Batch] MR %s: conflicts detected, removing from batch\n", mr.ID)
			conflicts = append(conflicts, mr)

			// Reset to base and rebuild stack without this MR
			if resetErr := e.git.ResetHard(baseSHA); resetErr != nil {
				return nil, nil, fmt.Errorf("reset after conflict: %w", resetErr)
			}
			// Rebuild the stack with MRs stacked so far (minus the conflicting one)
			for _, prev := range stacked {
				prevRef, refErr := e.submittedBranchHead(prev)
				if refErr != nil {
					return nil, nil, refErr
				}
				msg := e.getMergeMessage(prev)
				if mergeErr := e.git.MergeNoFF(prevRef, msg); mergeErr != nil {
					return nil, nil, fmt.Errorf("rebuild stack for %s: %w", prev.ID, mergeErr)
				}
			}
			continue
		}

		// Merge this MR onto the stack, preserving the submitted head in ancestry.
		msg := e.getMergeMessage(mr)
		if mergeErr := e.git.MergeNoFF(mergeRef, msg); mergeErr != nil {
			_, _ = fmt.Fprintf(e.output, "[Batch] MR %s: merge failed: %v, removing from batch\n", mr.ID, mergeErr)
			conflicts = append(conflicts, mr)

			// Reset and rebuild without this MR
			if resetErr := e.git.ResetHard(baseSHA); resetErr != nil {
				return nil, nil, fmt.Errorf("reset after merge failure: %w", resetErr)
			}
			for _, prev := range stacked {
				prevRef, refErr := e.submittedBranchHead(prev)
				if refErr != nil {
					return nil, nil, refErr
				}
				prevMsg := e.getMergeMessage(prev)
				if rebuildErr := e.git.MergeNoFF(prevRef, prevMsg); rebuildErr != nil {
					return nil, nil, fmt.Errorf("rebuild stack for %s: %w", prev.ID, rebuildErr)
				}
			}
			continue
		}

		stacked = append(stacked, mr)
	}

	_, _ = fmt.Fprintf(e.output, "[Batch] Stack built: %d MRs stacked, %d conflicts\n", len(stacked), len(conflicts))
	return stacked, conflicts, nil
}

// getMergeMessage returns the commit message for a merged MR.
func (e *Engineer) getMergeMessage(mr *MRInfo) string {
	// Try to get the original commit message from the branch
	msg, err := e.git.GetBranchCommitMessage(mr.Branch)
	if err != nil || strings.TrimSpace(msg) == "" {
		// Fallback to a descriptive message
		msg = fmt.Sprintf("Merge %s into %s", mr.Branch, mr.Target)
		if mr.SourceIssue != "" {
			msg = fmt.Sprintf("Merge %s into %s (%s)", mr.Branch, mr.Target, mr.SourceIssue)
		}
	}
	return msg
}

// ProcessBatch processes a batch of MRs using the batch-then-bisect algorithm.
//
// Algorithm:
//  1. Build the rebase stack (target ← MR1 ← MR2 ← ... ← MRn)
//  2. Run gates once on the stack tip
//  3. If green: push (fast-forward all MRs to target)
//  4. If red and RetryBatchOnFlaky: retry the full batch once
//  5. If still red: bisect to isolate the culprit
//  6. Re-batch good MRs for the next cycle
func (e *Engineer) ProcessBatch(ctx context.Context, batch []*MRInfo, target string, batchCfg *BatchConfig) *BatchResult {
	if batchCfg == nil {
		batchCfg = DefaultBatchConfig()
	}

	result := &BatchResult{}

	if len(batch) == 0 {
		return result
	}

	// Single MR: use existing doMerge path (no batch overhead)
	if len(batch) == 1 {
		return e.processSingleMR(ctx, batch[0], target)
	}

	_, _ = fmt.Fprintf(e.output, "[Batch] Processing batch of %d MRs targeting %s\n", len(batch), target)
	if !e.recheckBatchEligibility(batch, target, result) {
		return result
	}

	// Step 1: Build the stack
	stacked, conflicts, err := e.BuildRebaseStack(ctx, batch, target)
	if err != nil {
		result.Error = fmt.Errorf("build rebase stack: %w", err)
		return result
	}
	result.Conflicts = conflicts

	if len(stacked) == 0 {
		_, _ = fmt.Fprintln(e.output, "[Batch] No MRs could be stacked (all conflicted)")
		return result
	}

	// If only one MR survived after conflict removal, just process it directly
	if len(stacked) == 1 {
		_, _ = fmt.Fprintln(e.output, "[Batch] Only 1 MR survived stack construction, processing directly")
		// We already have the merged stack on the target branch, run gates and push.
		return e.verifyAndPush(ctx, stacked, target)
	}

	// Step 2: Run gates on the stack tip
	_, _ = fmt.Fprintf(e.output, "[Batch] Running gates on stack tip (%d MRs)...\n", len(stacked))
	gateResult := e.runBatchGates(ctx)

	// Step 3: Happy path — all green
	if gateResult.Success {
		return e.fastForwardBatch(ctx, stacked, target, result)
	}

	// Step 4: Retry if flaky test handling is enabled
	if batchCfg.RetryBatchOnFlaky {
		_, _ = fmt.Fprintln(e.output, "[Batch] Gates failed, retrying full batch (flaky test check)...")

		// Rebuild the stack from scratch for a clean retry
		if resetErr := e.resetAndRebuildStack(stacked, target); resetErr != nil {
			result.Error = fmt.Errorf("rebuild for retry: %w", resetErr)
			return result
		}

		retryResult := e.runBatchGates(ctx)
		if retryResult.Success {
			_, _ = fmt.Fprintln(e.output, "[Batch] Retry succeeded (was flaky)")
			return e.fastForwardBatch(ctx, stacked, target, result)
		}
		_, _ = fmt.Fprintln(e.output, "[Batch] Retry also failed, proceeding to bisection")
	}

	// Step 5: Bisect to find the culprit
	_, _ = fmt.Fprintf(e.output, "[Batch] Bisecting %d MRs to isolate failure...\n", len(stacked))
	good, culprits := e.bisectBatch(ctx, stacked, target)

	result.Culprits = culprits

	// Step 6: If we found good MRs, merge them
	if len(good) > 0 {
		_, _ = fmt.Fprintf(e.output, "[Batch] Merging %d good MRs after bisection\n", len(good))
		if resetErr := e.resetAndRebuildStack(good, target); resetErr != nil {
			result.Error = fmt.Errorf("rebuild good MRs: %w", resetErr)
			return result
		}
		// Verify the good subset actually passes
		verifyResult := e.runBatchGates(ctx)
		if verifyResult.Success {
			return e.fastForwardBatch(ctx, good, target, result)
		}
		// If the good subset also fails, something is wrong — don't merge anything
		_, _ = fmt.Fprintln(e.output, "[Batch] Warning: good subset also failed gates, aborting batch")
		result.Error = fmt.Errorf("good subset failed verification after bisection")
	}

	return result
}

func (e *Engineer) recheckBatchEligibility(batch []*MRInfo, target string, result *BatchResult) bool {
	for _, mr := range batch {
		if eligibility := e.recheckMRStillMergeable(mr, target); !eligibility.Success {
			if eligibility.NoMerge {
				_, _ = fmt.Fprintf(e.output, "[Batch] MR %s is not merge-eligible: %s\n", mr.ID, eligibility.Error)
				e.HandleMRInfoFailure(mr, eligibility)
			} else {
				result.Error = fmt.Errorf("pre-batch eligibility recheck failed for %s: %s", mr.ID, eligibility.Error)
			}
			return false
		}
	}
	return true
}

// processSingleMR handles the degenerate case of a batch with one MR.
func (e *Engineer) processSingleMR(ctx context.Context, mr *MRInfo, target string) *BatchResult {
	result := &BatchResult{}
	mergeMR := *mr
	mergeMR.Target = target
	processResult := e.doMerge(ctx, &mergeMR)
	if strings.TrimSpace(mr.CommitSHA) == "" {
		mr.CommitSHA = mergeMR.CommitSHA
	}
	if processResult.Success {
		result.MergeCommit = processResult.MergeCommit
		// GH#2321: Run post-merge cleanup (close beads, delete branch, nudge mayor)
		if e.HandleMRInfoSuccess(mr, processResult) {
			result.Merged = []*MRInfo{mr}
		} else {
			result.Error = fmt.Errorf("post-merge cleanup proof failed for %s", mr.ID)
		}
	} else if processResult.Conflict {
		result.Conflicts = []*MRInfo{mr}
	} else if processResult.TestsFailed {
		result.Culprits = []*MRInfo{mr}
	} else if processResult.BranchNotFound {
		// Branch not found on remote — escalate to mayor via HandleMRInfoFailure (gas-556).
		e.HandleMRInfoFailure(mr, processResult)
		result.Conflicts = []*MRInfo{mr}
	} else if processResult.NoMerge {
		// Policy-ineligible work is intentionally blocked. Dequeue silently.
		_, _ = fmt.Fprintf(e.output, "[Batch] MR %s: not merge-eligible, dequeuing\n", mr.ID)
		e.HandleMRInfoFailure(mr, processResult)
	} else if processResult.NeedsApproval {
		// PR awaiting human approval — leave in queue for retry on next poll.
		_, _ = fmt.Fprintf(e.output, "[Batch] MR %s: PR awaiting approval, will retry\n", mr.ID)
		e.HandleMRInfoFailure(mr, processResult)
	} else {
		result.Error = fmt.Errorf("merge failed: %s", processResult.Error)
	}
	return result
}

// runBatchGates runs quality gates (or legacy tests) on the current working tree.
func (e *Engineer) runBatchGates(ctx context.Context) ProcessResult {
	if len(e.config.Gates) > 0 {
		return e.runGates(ctx)
	}
	if e.config.RunTests && e.config.TestCommand != "" {
		result := e.runTests(ctx)
		if !result.Success {
			return ProcessResult{
				Success:     false,
				TestsFailed: true,
				Error:       result.Error,
			}
		}
		return ProcessResult{Success: true}
	}
	// No gates configured — pass by default
	return ProcessResult{Success: true}
}

// verifyAndPush runs gates and pushes the current state for a set of stacked MRs.
func (e *Engineer) verifyAndPush(ctx context.Context, stacked []*MRInfo, target string) *BatchResult {
	result := &BatchResult{}

	gateResult := e.runBatchGates(ctx)
	if !gateResult.Success {
		if gateResult.TestsFailed {
			result.Culprits = stacked
		} else {
			result.Error = fmt.Errorf("gates failed: %s", gateResult.Error)
		}
		return result
	}

	return e.fastForwardBatch(ctx, stacked, target, result)
}

// fastForwardBatch pushes the current state to the target branch.
// The working tree must already be on the target branch with all MR merges applied.
func (e *Engineer) fastForwardBatch(ctx context.Context, stacked []*MRInfo, target string, result *BatchResult) *BatchResult {
	// Get the tip SHA
	tipSHA, err := e.git.Rev("HEAD")
	if err != nil {
		result.Error = fmt.Errorf("get tip SHA: %w", err)
		return result
	}

	// Acquire merge slot for default branch pushes
	var pushHolder string
	if target == e.rig.DefaultBranch() {
		var slotErr error
		pushHolder, slotErr = e.acquireMainPushSlot(ctx)
		if slotErr != nil {
			if resetErr := e.git.ResetHard("origin/" + target); resetErr != nil {
				_, _ = fmt.Fprintf(e.output, "[Batch] Warning: failed to reset %s after slot failure: %v\n", target, resetErr)
			}
			result.Error = fmt.Errorf("acquire merge slot: %w", slotErr)
			return result
		}
		defer func() {
			if pushHolder != "" {
				if releaseErr := e.mergeSlotRelease(pushHolder); releaseErr != nil {
					_, _ = fmt.Fprintf(e.output, "[Batch] Warning: failed to release merge slot: %v\n", releaseErr)
				}
			}
		}()
	}

	for _, mr := range stacked {
		if eligibility := e.recheckMRStillMergeable(mr, target); !eligibility.Success {
			if resetErr := e.git.ResetHard("origin/" + target); resetErr != nil {
				_, _ = fmt.Fprintf(e.output, "[Batch] Warning: failed to reset %s after pre-push eligibility failure: %v\n", target, resetErr)
			}
			if eligibility.NoMerge {
				_, _ = fmt.Fprintf(e.output, "[Batch] MR %s became ineligible before push: %s\n", mr.ID, eligibility.Error)
				e.HandleMRInfoFailure(mr, eligibility)
			} else {
				result.Error = fmt.Errorf("pre-push eligibility recheck failed for %s: %s", mr.ID, eligibility.Error)
			}
			return result
		}
	}

	// Push to origin
	_, _ = fmt.Fprintf(e.output, "[Batch] Pushing %d merged MRs to origin/%s...\n", len(stacked), target)
	if pushErr := e.git.Push("origin", target, false); pushErr != nil {
		if resetErr := e.git.ResetHard("origin/" + target); resetErr != nil {
			_, _ = fmt.Fprintf(e.output, "[Batch] Warning: failed to reset %s after push failure: %v\n", target, resetErr)
		}
		result.Error = fmt.Errorf("push to origin: %w", pushErr)
		return result
	}
	if verifyErr := e.git.VerifyPushedCommit("origin", target, tipSHA); verifyErr != nil {
		if resetErr := e.git.ResetHard("origin/" + target); resetErr != nil {
			_, _ = fmt.Fprintf(e.output, "[Batch] Warning: failed to reset %s after verified-push failure: %v\n", target, resetErr)
		}
		result.Error = verifyErr
		return result
	}

	ids := make([]string, len(stacked))
	for i, mr := range stacked {
		ids[i] = mr.ID
	}
	_, _ = fmt.Fprintf(e.output, "[Batch] Successfully merged batch: %s (commit %s)\n", strings.Join(ids, ", "), shortSHA(tipSHA))

	result.MergeCommit = tipSHA

	// GH#2321: Run post-merge cleanup for each merged MR — close source beads,
	// delete branches, nudge mayor, and check convoy completion.
	// HandleMRInfoSuccess was previously dead code (never called), causing task
	// beads to remain open after successful merges.
	cleaned := make([]*MRInfo, 0, len(stacked))
	for _, mr := range stacked {
		mergeResult := ProcessResult{Success: true, MergeCommit: tipSHA}
		if e.HandleMRInfoSuccess(mr, mergeResult) {
			cleaned = append(cleaned, mr)
		} else if result.Error == nil {
			result.Error = fmt.Errorf("post-merge cleanup proof failed for %s", mr.ID)
		}
	}
	result.Merged = cleaned

	return result
}

// bisectBatch performs binary search to find which MR(s) caused a test failure.
// Returns the good MRs and the culprit MRs.
func (e *Engineer) bisectBatch(ctx context.Context, batch []*MRInfo, target string) (good []*MRInfo, culprits []*MRInfo) {
	if len(batch) <= 1 {
		// Base case: single MR is the culprit
		return nil, append([]*MRInfo{}, batch...)
	}

	mid := len(batch) / 2
	// Copy slices to avoid append-on-subslice aliasing
	left := append([]*MRInfo{}, batch[:mid]...)
	right := append([]*MRInfo{}, batch[mid:]...)

	_, _ = fmt.Fprintf(e.output, "[Bisect] Testing left half (%d MRs)...\n", len(left))

	// Test the left half
	if resetErr := e.resetAndRebuildStack(left, target); resetErr != nil {
		_, _ = fmt.Fprintf(e.output, "[Bisect] Error rebuilding left half: %v, treating all as culprits\n", resetErr)
		return nil, batch
	}

	leftResult := e.runBatchGates(ctx)

	if leftResult.Success {
		// Left half is green — culprit is in right half
		_, _ = fmt.Fprintf(e.output, "[Bisect] Left half passed, bisecting right half (%d MRs)...\n", len(right))

		// bisectRight handles its own stack construction (knownGood + sub-batches),
		// so no need to rebuild the full batch stack here first.
		rightGood, rightCulprits := e.bisectRight(ctx, left, right, target)
		return append(left, rightGood...), rightCulprits
	}

	// Left half failed — culprit is in left half
	_, _ = fmt.Fprintf(e.output, "[Bisect] Left half failed, bisecting left half...\n")
	leftGood, leftCulprits := e.bisectBatch(ctx, left, target)

	// Right half hasn't been tested in isolation — it might be fine
	// Test right half in context of leftGood
	if len(leftGood) > 0 {
		combined := append(leftGood, right...)
		if resetErr := e.resetAndRebuildStack(combined, target); resetErr != nil {
			_, _ = fmt.Fprintf(e.output, "[Bisect] Error testing right with good left: %v\n", resetErr)
			return leftGood, append(leftCulprits, right...)
		}
		combinedResult := e.runBatchGates(ctx)
		if combinedResult.Success {
			return append(leftGood, right...), leftCulprits
		}
		// Right half also has issues — recursively bisect it too
		rightGood, rightCulprits := e.bisectRight(ctx, leftGood, right, target)
		return append(leftGood, rightGood...), append(leftCulprits, rightCulprits...)
	}

	// No good MRs in left half, test right half alone
	if resetErr := e.resetAndRebuildStack(right, target); resetErr != nil {
		return nil, batch
	}
	rightResult := e.runBatchGates(ctx)
	if rightResult.Success {
		return right, leftCulprits
	}
	rightGood, rightCulprits := e.bisectBatch(ctx, right, target)
	return rightGood, append(leftCulprits, rightCulprits...)
}

// bisectRight bisects the right half of a batch, testing each sub-batch
// in the context of the known-good left half (cumulative merge).
func (e *Engineer) bisectRight(ctx context.Context, knownGood []*MRInfo, right []*MRInfo, target string) (good []*MRInfo, culprits []*MRInfo) {
	if len(right) <= 1 {
		return nil, append([]*MRInfo{}, right...)
	}

	mid := len(right) / 2
	// Copy slices to avoid append-on-subslice aliasing
	rLeft := append([]*MRInfo{}, right[:mid]...)
	rRight := append([]*MRInfo{}, right[mid:]...)

	// Test knownGood + rLeft
	testBatch := append(append([]*MRInfo{}, knownGood...), rLeft...)
	if resetErr := e.resetAndRebuildStack(testBatch, target); resetErr != nil {
		_, _ = fmt.Fprintf(e.output, "[Bisect] Error rebuilding for right bisection: %v\n", resetErr)
		return nil, right
	}

	result := e.runBatchGates(ctx)
	if result.Success {
		// rLeft is fine in context of knownGood — culprit is in rRight
		_, _ = fmt.Fprintf(e.output, "[Bisect-R] knownGood+rLeft passed → culprit in rRight=%v\n", mrIDs(rRight))
		newGood := append(append([]*MRInfo{}, knownGood...), rLeft...)
		rRightGood, rRightCulprits := e.bisectRight(ctx, newGood, rRight, target)
		_, _ = fmt.Fprintf(e.output, "[Bisect-R] Returning good=%v, culprits=%v\n", mrIDs(append(rLeft, rRightGood...)), mrIDs(rRightCulprits))
		return append(rLeft, rRightGood...), rRightCulprits
	}

	// rLeft has the culprit
	_, _ = fmt.Fprintf(e.output, "[Bisect-R] knownGood+rLeft failed → culprit in rLeft=%v\n", mrIDs(rLeft))
	rLeftGood, rLeftCulprits := e.bisectRight(ctx, knownGood, rLeft, target)

	// Test rRight with knownGood + rLeftGood
	_, _ = fmt.Fprintf(e.output, "[Bisect-R] Testing rRight=%v with knownGood+rLeftGood=%v\n", mrIDs(rRight), mrIDs(append(append([]*MRInfo{}, knownGood...), rLeftGood...)))
	testBatch2 := append(append(append([]*MRInfo{}, knownGood...), rLeftGood...), rRight...)
	if resetErr := e.resetAndRebuildStack(testBatch2, target); resetErr != nil {
		return rLeftGood, append(rLeftCulprits, rRight...)
	}
	result2 := e.runBatchGates(ctx)
	if result2.Success {
		_, _ = fmt.Fprintf(e.output, "[Bisect-R] rRight passed → good=%v, culprits=%v\n", mrIDs(append(rLeftGood, rRight...)), mrIDs(rLeftCulprits))
		return append(rLeftGood, rRight...), rLeftCulprits
	}
	rRightGood, rRightCulprits := e.bisectRight(ctx, append(append([]*MRInfo{}, knownGood...), rLeftGood...), rRight, target)
	return append(rLeftGood, rRightGood...), append(rLeftCulprits, rRightCulprits...)
}

// mrIDs returns the IDs of a slice of MRInfo for logging.
func mrIDs(mrs []*MRInfo) []string {
	ids := make([]string, len(mrs))
	for i, mr := range mrs {
		ids[i] = mr.ID
	}
	return ids
}

// resetAndRebuildStack resets the target branch and rebuilds the merge stack.
func (e *Engineer) resetAndRebuildStack(mrs []*MRInfo, target string) error {
	// Reset target to origin
	if err := e.git.Checkout(target); err != nil {
		return fmt.Errorf("checkout %s: %w", target, err)
	}
	if err := e.git.ResetHard("origin/" + target); err != nil {
		return fmt.Errorf("reset %s: %w", target, err)
	}

	// Rebuild the stack
	for _, mr := range mrs {
		mergeRef, refErr := e.submittedBranchHead(mr)
		if refErr != nil {
			return refErr
		}
		msg := e.getMergeMessage(mr)
		if err := e.git.MergeNoFF(mergeRef, msg); err != nil {
			return fmt.Errorf("merge %s: %w", mr.ID, err)
		}
	}
	return nil
}
