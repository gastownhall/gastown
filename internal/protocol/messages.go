package protocol

import (
	"fmt"
	"strings"
	"time"

	"github.com/jonbaldie/gastown/internal/mail"
)

// NewMergeReadyMessage creates a MERGE_READY protocol message.
// Sent by Witness to Refinery when a polecat's work is verified and ready.
func NewMergeReadyMessage(rig, polecat, branch, issue string) *mail.Message {
	payload := MergeReadyPayload{
		Branch:    branch,
		Issue:     issue,
		Polecat:   polecat,
		Rig:       rig,
		Verified:  "clean git state, issue closed",
		Timestamp: time.Now(),
	}

	body := formatMergeReadyBody(payload)

	msg := mail.NewMessage(
		fmt.Sprintf("%s/witness", rig),
		fmt.Sprintf("%s/refinery", rig),
		fmt.Sprintf("MERGE_READY %s", polecat),
		body,
	)
	msg.Priority = mail.PriorityHigh
	msg.Type = mail.TypeTask

	return msg
}

// formatMergeReadyBody formats the body of a MERGE_READY message.
func formatMergeReadyBody(p MergeReadyPayload) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Branch: %s\n", p.Branch))
	sb.WriteString(fmt.Sprintf("Issue: %s\n", p.Issue))
	sb.WriteString(fmt.Sprintf("Polecat: %s\n", p.Polecat))
	sb.WriteString(fmt.Sprintf("Rig: %s\n", p.Rig))
	if p.Verified != "" {
		sb.WriteString(fmt.Sprintf("Verified: %s\n", p.Verified))
	}
	return sb.String()
}

// NewMergedMessage creates a MERGED protocol message.
// Sent by Refinery to Witness when a branch is successfully merged.
func NewMergedMessage(rig, polecat, branch, issue, targetBranch, mergeCommit string) *mail.Message {
	payload := MergedPayload{
		Branch:       branch,
		Issue:        issue,
		Polecat:      polecat,
		Rig:          rig,
		MergedAt:     time.Now(),
		MergeCommit:  mergeCommit,
		TargetBranch: targetBranch,
	}

	body := formatMergedBody(payload)

	msg := mail.NewMessage(
		fmt.Sprintf("%s/refinery", rig),
		fmt.Sprintf("%s/witness", rig),
		fmt.Sprintf("MERGED %s", polecat),
		body,
	)
	msg.Priority = mail.PriorityHigh
	msg.Type = mail.TypeNotification

	return msg
}

// formatMergedBody formats the body of a MERGED message.
func formatMergedBody(p MergedPayload) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Branch: %s\n", p.Branch))
	sb.WriteString(fmt.Sprintf("Issue: %s\n", p.Issue))
	sb.WriteString(fmt.Sprintf("Polecat: %s\n", p.Polecat))
	sb.WriteString(fmt.Sprintf("Rig: %s\n", p.Rig))
	sb.WriteString(fmt.Sprintf("Target: %s\n", p.TargetBranch))
	sb.WriteString(fmt.Sprintf("Merged-At: %s\n", p.MergedAt.Format(time.RFC3339)))
	if p.MergeCommit != "" {
		sb.WriteString(fmt.Sprintf("Merge-Commit: %s\n", p.MergeCommit))
	}
	return sb.String()
}

// NewMergeFailedMessage creates a MERGE_FAILED protocol message.
// Sent by Refinery to Witness when merge fails (tests, build, etc.).
func NewMergeFailedMessage(rig, polecat, branch, issue, targetBranch, failureType, errorMsg string) *mail.Message {
	payload := MergeFailedPayload{
		Branch:       branch,
		Issue:        issue,
		Polecat:      polecat,
		Rig:          rig,
		FailedAt:     time.Now(),
		FailureType:  failureType,
		Error:        errorMsg,
		TargetBranch: targetBranch,
	}

	body := formatMergeFailedBody(payload)

	msg := mail.NewMessage(
		fmt.Sprintf("%s/refinery", rig),
		fmt.Sprintf("%s/witness", rig),
		fmt.Sprintf("MERGE_FAILED %s", polecat),
		body,
	)
	msg.Priority = mail.PriorityHigh
	msg.Type = mail.TypeTask

	return msg
}

// formatMergeFailedBody formats the body of a MERGE_FAILED message.
func formatMergeFailedBody(p MergeFailedPayload) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Branch: %s\n", p.Branch))
	sb.WriteString(fmt.Sprintf("Issue: %s\n", p.Issue))
	sb.WriteString(fmt.Sprintf("Polecat: %s\n", p.Polecat))
	sb.WriteString(fmt.Sprintf("Rig: %s\n", p.Rig))
	sb.WriteString(fmt.Sprintf("Target: %s\n", p.TargetBranch))
	sb.WriteString(fmt.Sprintf("Failed-At: %s\n", p.FailedAt.Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("Failure-Type: %s\n", p.FailureType))
	sb.WriteString(fmt.Sprintf("Error: %s\n", p.Error))
	return sb.String()
}

// NewFixNeededMessage creates a FIX_NEEDED protocol message.
// Sent by Refinery directly to the Polecat when merge fails (tests, build, etc.).
// Unlike MERGE_FAILED (which goes to Witness), FIX_NEEDED goes to the polecat
// so it can fix the code in-place without losing context.
func NewFixNeededMessage(rig, polecat, branch, issue, targetBranch, failureType, errorMsg, mrBeadID string, attemptNumber int) *mail.Message {
	payload := FixNeededPayload{
		Branch:        branch,
		Issue:         issue,
		Polecat:       polecat,
		Rig:           rig,
		FailedAt:      time.Now(),
		FailureType:   failureType,
		Error:         errorMsg,
		TargetBranch:  targetBranch,
		MRBeadID:      mrBeadID,
		AttemptNumber: attemptNumber,
	}

	body := formatFixNeededBody(payload)

	msg := mail.NewMessage(
		fmt.Sprintf("%s/refinery", rig),
		fmt.Sprintf("%s/polecats/%s", rig, polecat),
		fmt.Sprintf("FIX_NEEDED %s", polecat),
		body,
	)
	msg.Priority = mail.PriorityHigh
	msg.Type = mail.TypeTask

	return msg
}

// formatFixNeededBody formats the body of a FIX_NEEDED message.
func formatFixNeededBody(p FixNeededPayload) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Branch: %s\n", p.Branch))
	sb.WriteString(fmt.Sprintf("Issue: %s\n", p.Issue))
	sb.WriteString(fmt.Sprintf("Polecat: %s\n", p.Polecat))
	sb.WriteString(fmt.Sprintf("Rig: %s\n", p.Rig))
	sb.WriteString(fmt.Sprintf("Target: %s\n", p.TargetBranch))
	sb.WriteString(fmt.Sprintf("Failed-At: %s\n", p.FailedAt.Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("Failure-Type: %s\n", p.FailureType))
	sb.WriteString(fmt.Sprintf("Error: %s\n", p.Error))
	if p.MRBeadID != "" {
		sb.WriteString(fmt.Sprintf("MR-Bead-ID: %s\n", p.MRBeadID))
	}
	sb.WriteString(fmt.Sprintf("Attempt-Number: %d\n", p.AttemptNumber))
	return sb.String()
}

// ParseFixNeededPayload parses a FIX_NEEDED message body into a payload.
// Returns an error if required fields (Branch, Polecat, Rig) are missing.
func ParseFixNeededPayload(body string) (*FixNeededPayload, error) {
	payload := &FixNeededPayload{
		Branch:       parseField(body, "Branch"),
		Issue:        parseField(body, "Issue"),
		Polecat:      parseField(body, "Polecat"),
		Rig:          parseField(body, "Rig"),
		TargetBranch: parseField(body, "Target"),
		FailureType:  parseField(body, "Failure-Type"),
		Error:        parseField(body, "Error"),
		MRBeadID:     parseField(body, "MR-Bead-ID"),
	}

	// Parse timestamp
	if ts := parseField(body, "Failed-At"); ts != "" {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			payload.FailedAt = t
		}
	}

	// Parse attempt number
	if an := parseField(body, "Attempt-Number"); an != "" {
		fmt.Sscanf(an, "%d", &payload.AttemptNumber)
	}

	var errs []string
	if payload.Branch == "" {
		errs = append(errs, "Branch")
	}
	if payload.Polecat == "" {
		errs = append(errs, "Polecat")
	}
	if payload.Rig == "" {
		errs = append(errs, "Rig")
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("invalid FIX_NEEDED payload: missing required fields: %s", strings.Join(errs, ", "))
	}

	return payload, nil
}

// NewReworkRequestMessage creates a REWORK_REQUEST protocol message.
// Sent by Refinery to Witness when a branch needs rebasing due to conflicts.
func NewReworkRequestMessage(rig, polecat, branch, issue, targetBranch string, conflictFiles []string) *mail.Message {
	payload := ReworkRequestPayload{
		Branch:        branch,
		Issue:         issue,
		Polecat:       polecat,
		Rig:           rig,
		RequestedAt:   time.Now(),
		TargetBranch:  targetBranch,
		ConflictFiles: conflictFiles,
		Instructions:  formatRebaseInstructions(targetBranch),
	}

	body := formatReworkRequestBody(payload)

	msg := mail.NewMessage(
		fmt.Sprintf("%s/refinery", rig),
		fmt.Sprintf("%s/witness", rig),
		fmt.Sprintf("REWORK_REQUEST %s", polecat),
		body,
	)
	msg.Priority = mail.PriorityHigh
	msg.Type = mail.TypeTask

	return msg
}

// formatReworkRequestBody formats the body of a REWORK_REQUEST message.
func formatReworkRequestBody(p ReworkRequestPayload) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Branch: %s\n", p.Branch))
	sb.WriteString(fmt.Sprintf("Issue: %s\n", p.Issue))
	sb.WriteString(fmt.Sprintf("Polecat: %s\n", p.Polecat))
	sb.WriteString(fmt.Sprintf("Rig: %s\n", p.Rig))
	sb.WriteString(fmt.Sprintf("Target: %s\n", p.TargetBranch))
	sb.WriteString(fmt.Sprintf("Requested-At: %s\n", p.RequestedAt.Format(time.RFC3339)))

	if len(p.ConflictFiles) > 0 {
		sb.WriteString(fmt.Sprintf("Conflict-Files: %s\n", strings.Join(p.ConflictFiles, ", ")))
	}

	sb.WriteString("\n")
	sb.WriteString(p.Instructions)

	return sb.String()
}

// formatRebaseInstructions returns standard rebase instructions.
func formatRebaseInstructions(targetBranch string) string {
	return fmt.Sprintf(`Please rebase your changes onto %s:

  git fetch origin
  git rebase origin/%s
  # Resolve any conflicts
  git push -f

The Refinery will retry the merge after rebase is complete.`, targetBranch, targetBranch)
}

// NewConvoyNeedsFeedingMessage creates a CONVOY_NEEDS_FEEDING protocol message.
// Sent by Refinery to Deacon after a convoy-eligible merge completes, so the
// deacon can immediately feed the convoy instead of waiting for the next patrol.
func NewConvoyNeedsFeedingMessage(rig, convoyID, sourceIssue string) *mail.Message {
	payload := ConvoyNeedsFeedingPayload{
		ConvoyID:    convoyID,
		SourceIssue: sourceIssue,
		Rig:         rig,
		MergedAt:    time.Now(),
	}

	body := formatConvoyNeedsFeedingBody(payload)

	msg := mail.NewMessage(
		fmt.Sprintf("%s/refinery", rig),
		"deacon/",
		fmt.Sprintf("CONVOY_NEEDS_FEEDING %s", convoyID),
		body,
	)
	msg.Priority = mail.PriorityHigh
	msg.Type = mail.TypeTask

	return msg
}

// formatConvoyNeedsFeedingBody formats the body of a CONVOY_NEEDS_FEEDING message.
func formatConvoyNeedsFeedingBody(p ConvoyNeedsFeedingPayload) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("ConvoyID: %s\n", p.ConvoyID))
	sb.WriteString(fmt.Sprintf("SourceIssue: %s\n", p.SourceIssue))
	sb.WriteString(fmt.Sprintf("Rig: %s\n", p.Rig))
	sb.WriteString(fmt.Sprintf("Merged-At: %s\n", p.MergedAt.Format(time.RFC3339)))
	return sb.String()
}

// ParseConvoyNeedsFeedingPayload parses a CONVOY_NEEDS_FEEDING message body.
// Returns an error if required fields (ConvoyID, Rig) are missing.
func ParseConvoyNeedsFeedingPayload(body string) (*ConvoyNeedsFeedingPayload, error) {
	payload := &ConvoyNeedsFeedingPayload{
		ConvoyID:    parseField(body, "ConvoyID"),
		SourceIssue: parseField(body, "SourceIssue"),
		Rig:         parseField(body, "Rig"),
	}

	if ts := parseField(body, "Merged-At"); ts != "" {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			payload.MergedAt = t
		}
	}

	var errs []string
	if payload.ConvoyID == "" {
		errs = append(errs, "ConvoyID")
	}
	if payload.Rig == "" {
		errs = append(errs, "Rig")
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("invalid CONVOY_NEEDS_FEEDING payload: missing required fields: %s", strings.Join(errs, ", "))
	}

	return payload, nil
}

// ParseMergeReadyPayload parses a MERGE_READY message body into a payload.
// Returns an error if required fields (Branch, Polecat, Rig) are missing.
func ParseMergeReadyPayload(body string) (*MergeReadyPayload, error) {
	payload := &MergeReadyPayload{
		Branch:    parseField(body, "Branch"),
		Issue:     parseField(body, "Issue"),
		Polecat:   parseField(body, "Polecat"),
		Rig:       parseField(body, "Rig"),
		Verified:  parseField(body, "Verified"),
		Timestamp: time.Now(), // Use current time if not parseable
	}

	var errs []string
	if payload.Branch == "" {
		errs = append(errs, "Branch")
	}
	if payload.Polecat == "" {
		errs = append(errs, "Polecat")
	}
	if payload.Rig == "" {
		errs = append(errs, "Rig")
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("invalid MERGE_READY payload: missing required fields: %s", strings.Join(errs, ", "))
	}

	return payload, nil
}

// ParseMergedPayload parses a MERGED message body into a payload.
// Returns an error if required fields (Branch, Polecat, Rig) are missing.
func ParseMergedPayload(body string) (*MergedPayload, error) {
	payload := &MergedPayload{
		Branch:       parseField(body, "Branch"),
		Issue:        parseField(body, "Issue"),
		Polecat:      parseField(body, "Polecat"),
		Rig:          parseField(body, "Rig"),
		TargetBranch: parseField(body, "Target"),
		MergeCommit:  parseField(body, "Merge-Commit"),
	}

	// Parse timestamp
	if ts := parseField(body, "Merged-At"); ts != "" {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			payload.MergedAt = t
		}
	}

	var errs []string
	if payload.Branch == "" {
		errs = append(errs, "Branch")
	}
	if payload.Polecat == "" {
		errs = append(errs, "Polecat")
	}
	if payload.Rig == "" {
		errs = append(errs, "Rig")
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("invalid MERGED payload: missing required fields: %s", strings.Join(errs, ", "))
	}

	return payload, nil
}

// ParseMergeFailedPayload parses a MERGE_FAILED message body into a payload.
// Returns an error if required fields (Branch, Polecat, Rig) are missing.
func ParseMergeFailedPayload(body string) (*MergeFailedPayload, error) {
	payload := &MergeFailedPayload{
		Branch:       parseField(body, "Branch"),
		Issue:        parseField(body, "Issue"),
		Polecat:      parseField(body, "Polecat"),
		Rig:          parseField(body, "Rig"),
		TargetBranch: parseField(body, "Target"),
		FailureType:  parseField(body, "Failure-Type"),
		Error:        parseField(body, "Error"),
	}

	// Parse timestamp
	if ts := parseField(body, "Failed-At"); ts != "" {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			payload.FailedAt = t
		}
	}

	var errs []string
	if payload.Branch == "" {
		errs = append(errs, "Branch")
	}
	if payload.Polecat == "" {
		errs = append(errs, "Polecat")
	}
	if payload.Rig == "" {
		errs = append(errs, "Rig")
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("invalid MERGE_FAILED payload: missing required fields: %s", strings.Join(errs, ", "))
	}

	return payload, nil
}

// ParseReworkRequestPayload parses a REWORK_REQUEST message body into a payload.
// Returns an error if required fields (Branch, Polecat, Rig) are missing.
func ParseReworkRequestPayload(body string) (*ReworkRequestPayload, error) {
	payload := &ReworkRequestPayload{
		Branch:       parseField(body, "Branch"),
		Issue:        parseField(body, "Issue"),
		Polecat:      parseField(body, "Polecat"),
		Rig:          parseField(body, "Rig"),
		TargetBranch: parseField(body, "Target"),
	}

	// Parse timestamp
	if ts := parseField(body, "Requested-At"); ts != "" {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			payload.RequestedAt = t
		}
	}

	// Parse conflict files
	if files := parseField(body, "Conflict-Files"); files != "" {
		payload.ConflictFiles = strings.Split(files, ", ")
	}

	var errs []string
	if payload.Branch == "" {
		errs = append(errs, "Branch")
	}
	if payload.Polecat == "" {
		errs = append(errs, "Polecat")
	}
	if payload.Rig == "" {
		errs = append(errs, "Rig")
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("invalid REWORK_REQUEST payload: missing required fields: %s", strings.Join(errs, ", "))
	}

	return payload, nil
}

// ParsePolecatDonePayload parses a POLECAT_DONE notification body.
// Unlike formal protocol messages, POLECAT_DONE is a mail convention — no
// required fields are enforced. Returns a best-effort parse of available fields.
func ParsePolecatDonePayload(polecatName, body string) *PolecatDonePayload {
	payload := &PolecatDonePayload{
		Polecat:       polecatName,
		ExitType:      parseField(body, "Exit"),
		Issue:         parseField(body, "Issue"),
		Branch:        parseField(body, "Branch"),
		MR:            parseField(body, "MR"),
		ConvoyID:      parseField(body, "ConvoyID"),
		MergeStrategy: parseField(body, "MergeStrategy"),
		Errors:        parseField(body, "Errors"),
	}

	if parseField(body, "ConvoyOwned") == "true" {
		payload.ConvoyOwned = true
	}

	return payload
}

// parseField extracts a field value from a key-value body format.
// Format: "Key: value"
func parseField(body, key string) string {
	lines := strings.Split(body, "\n")
	prefix := key + ": "

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}

	return ""
}
