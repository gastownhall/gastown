package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
)

var (
	orphanBurnPreview         bool
	orphanBurnExecute         bool
	orphanBurnValidatedDigest string
	orphanBurnAuditBead       string
)

type orphanBurnExclusion struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

type orphanBurnPlan struct {
	Requested []string              `json:"requested"`
	Safe      []string              `json:"safe"`
	Excluded  []orphanBurnExclusion `json:"excluded,omitempty"`
	SetDigest string                `json:"set_digest"`
}

type orphanBurnAudit struct {
	Operation       string                `json:"operation"`
	Result          string                `json:"result"`
	Requested       []string              `json:"requested"`
	Safe            []string              `json:"safe"`
	Excluded        []orphanBurnExclusion `json:"excluded,omitempty"`
	SetDigest       string                `json:"set_digest"`
	ValidatedDigest string                `json:"validated_digest,omitempty"`
	Error           string                `json:"error,omitempty"`
}

var moleculeBurnOrphansCmd = &cobra.Command{
	Use:   "burn-orphans [wisp-id...]",
	Short: "Preview or execute an audited, fail-closed orphan-wisp burn batch",
	Long: `Safely burn a reviewed batch of orphan wisps.

Preview canonicalizes the IDs, excludes preserved records, computes a digest,
and writes the plan to the audit bead. Execution independently repeats those
checks and refuses to mutate anything unless the requested IDs exactly equal
the safe set and its digest equals --validated-digest.

Examples:
  gt mol burn-orphans --preview --audit-bead hq-audit hq-wisp-a hq-wisp-b
  gt mol burn-orphans --execute --audit-bead hq-audit \
    --validated-digest <preview-digest> hq-wisp-a hq-wisp-b`,
	Args: cobra.MinimumNArgs(1),
	RunE: runMoleculeBurnOrphans,
}

func registerOrphanBurnFlags() {
	moleculeBurnOrphansCmd.Flags().BoolVar(&orphanBurnPreview, "preview", false, "Validate and persist the reviewed safe set without mutation")
	moleculeBurnOrphansCmd.Flags().BoolVar(&orphanBurnExecute, "execute", false, "Execute a previously reviewed set")
	moleculeBurnOrphansCmd.Flags().StringVar(&orphanBurnValidatedDigest, "validated-digest", "", "Digest printed by the matching preview")
	moleculeBurnOrphansCmd.Flags().StringVar(&orphanBurnAuditBead, "audit-bead", "", "Durable bead that receives preview and result comments")
}

func runMoleculeBurnOrphans(_ *cobra.Command, args []string) error {
	if orphanBurnPreview == orphanBurnExecute {
		return fmt.Errorf("exactly one of --preview or --execute is required")
	}
	if strings.TrimSpace(orphanBurnAuditBead) == "" {
		return fmt.Errorf("--audit-bead is required")
	}
	if orphanBurnPreview && orphanBurnValidatedDigest != "" {
		return fmt.Errorf("--validated-digest is only valid with --execute")
	}
	if orphanBurnExecute && strings.TrimSpace(orphanBurnValidatedDigest) == "" {
		return fmt.Errorf("--validated-digest is required with --execute")
	}

	workDir, err := findLocalBeadsDir()
	if err != nil {
		return fmt.Errorf("not in a beads workspace: %w", err)
	}
	bd := beads.New(workDir)
	plan, err := buildOrphanBurnPlan(bd, args)
	if err != nil {
		return persistOrphanBurnFailure(bd, "validation", args, orphanBurnValidatedDigest, err)
	}

	if orphanBurnPreview {
		audit := orphanBurnAudit{
			Operation: "orphan-wisp-burn-preview", Result: "review-required",
			Requested: plan.Requested, Safe: plan.Safe, Excluded: plan.Excluded, SetDigest: plan.SetDigest,
		}
		if err := addOrphanBurnAudit(bd, audit); err != nil {
			return err
		}
		return writeOrphanBurnJSON(plan)
	}

	if err := validateOrphanBurnExecution(plan, orphanBurnValidatedDigest); err != nil {
		return persistOrphanBurnPlanFailure(bd, plan, err)
	}

	preflight := orphanBurnAudit{
		Operation: "orphan-wisp-burn-execute", Result: "validated-before-mutation",
		Requested: plan.Requested, Safe: plan.Safe, SetDigest: plan.SetDigest,
		ValidatedDigest: strings.TrimSpace(orphanBurnValidatedDigest),
	}
	if err := addOrphanBurnAudit(bd, preflight); err != nil {
		return err
	}
	if err := bd.ForceCloseWithReason("burned: audited orphan recovery", plan.Safe...); err != nil {
		return persistOrphanBurnPlanFailure(bd, plan, fmt.Errorf("closing orphan wisps: %w", err))
	}

	result := orphanBurnAudit{
		Operation: "orphan-wisp-burn-execute", Result: "burned",
		Requested: plan.Requested, Safe: plan.Safe, SetDigest: plan.SetDigest,
		ValidatedDigest: strings.TrimSpace(orphanBurnValidatedDigest),
	}
	if err := addOrphanBurnAudit(bd, result); err != nil {
		return fmt.Errorf("burn completed but result audit failed: %w", err)
	}
	return writeOrphanBurnJSON(result)
}

type orphanBurnIssueReader interface {
	ShowMultiple(ids []string) (map[string]*beads.Issue, error)
}

func buildOrphanBurnPlan(reader orphanBurnIssueReader, requested []string) (orphanBurnPlan, error) {
	canonical, err := canonicalBurnIDs(requested)
	if err != nil {
		return orphanBurnPlan{}, err
	}
	issues, err := reader.ShowMultiple(canonical)
	if err != nil {
		return orphanBurnPlan{}, fmt.Errorf("loading requested wisps: %w", err)
	}

	plan := orphanBurnPlan{Requested: canonical}
	for _, id := range canonical {
		issue, ok := issues[id]
		if !ok || issue == nil {
			return orphanBurnPlan{}, fmt.Errorf("requested wisp %s was not found", id)
		}
		if reason := orphanBurnPreserveReason(issue); reason != "" {
			plan.Excluded = append(plan.Excluded, orphanBurnExclusion{ID: id, Reason: reason})
			continue
		}
		plan.Safe = append(plan.Safe, id)
	}
	plan.SetDigest = burnSetDigest(plan.Safe)
	return plan, nil
}

func canonicalBurnIDs(ids []string) ([]string, error) {
	seen := make(map[string]struct{}, len(ids))
	canonical := make([]string, 0, len(ids))
	for _, raw := range ids {
		id := strings.ToLower(strings.TrimSpace(raw))
		if id == "" {
			return nil, fmt.Errorf("burn set contains an empty ID")
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		canonical = append(canonical, id)
	}
	if len(canonical) == 0 {
		return nil, fmt.Errorf("burn set is empty")
	}
	sort.Strings(canonical)
	return canonical, nil
}

func burnSetDigest(ids []string) string {
	sum := sha256.Sum256([]byte(strings.Join(ids, "\n")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func orphanBurnPreserveReason(issue *beads.Issue) string {
	if issue == nil {
		return "missing"
	}
	id := strings.ToLower(strings.TrimSpace(issue.ID))
	if !issue.Ephemeral && !strings.Contains(id, "-wisp-") {
		return "not-an-ephemeral-wisp"
	}
	preservedTypes := map[string]bool{
		"message": true, "escalation": true, "audit": true, "event": true,
		"handoff": true, "agent": true, "queue": true, "convoy": true,
		"formula": true, "merge-request": true, "role": true, "rig": true,
	}
	if typ := strings.ToLower(strings.TrimSpace(issue.Type)); preservedTypes[typ] {
		return "preserved-type:" + typ
	}
	for _, raw := range issue.Labels {
		label := strings.ToLower(strings.TrimSpace(raw))
		if beads.ProtectedIssueLabel(label) {
			return "preserved-label:" + label
		}
		switch label {
		case "gt:message", "gt:escalation", "gt:audit", "gt:event", "gt:handoff",
			"gt:agent", "gt:queue", "gt:convoy", "gt:formula", "gt:merge-request":
			return "preserved-label:" + label
		}
	}
	return ""
}

func equalStringSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func validateOrphanBurnExecution(plan orphanBurnPlan, validatedDigest string) error {
	// Both comparisons are intentional. The set comparison catches explicitly
	// supplied preserved IDs, while the digest comparison catches any extra or
	// missing otherwise-safe ID relative to the reviewed preview.
	if !equalStringSets(plan.Requested, plan.Safe) {
		return fmt.Errorf("requested burn set differs from validated safe set (requested=%d safe=%d)", len(plan.Requested), len(plan.Safe))
	}
	if !strings.EqualFold(plan.SetDigest, strings.TrimSpace(validatedDigest)) {
		return fmt.Errorf("requested burn digest %s differs from validated digest %s", plan.SetDigest, strings.TrimSpace(validatedDigest))
	}
	return nil
}

func addOrphanBurnAudit(bd *beads.Beads, audit orphanBurnAudit) error {
	payload, err := json.Marshal(audit)
	if err != nil {
		return fmt.Errorf("encoding orphan burn audit: %w", err)
	}
	if err := bd.AddComment(orphanBurnAuditBead, "ORPHAN-WISP-BURN-AUDIT "+string(payload)); err != nil {
		return fmt.Errorf("persisting orphan burn audit on %s: %w", orphanBurnAuditBead, err)
	}
	return nil
}

func persistOrphanBurnPlanFailure(bd *beads.Beads, plan orphanBurnPlan, cause error) error {
	audit := orphanBurnAudit{
		Operation: "orphan-wisp-burn-execute", Result: "rejected",
		Requested: plan.Requested, Safe: plan.Safe, Excluded: plan.Excluded,
		SetDigest: plan.SetDigest, ValidatedDigest: strings.TrimSpace(orphanBurnValidatedDigest), Error: cause.Error(),
	}
	if err := addOrphanBurnAudit(bd, audit); err != nil {
		return fmt.Errorf("%v; additionally failed to persist rejection audit: %w", cause, err)
	}
	return cause
}

func persistOrphanBurnFailure(bd *beads.Beads, result string, requested []string, digest string, cause error) error {
	audit := orphanBurnAudit{
		Operation: "orphan-wisp-burn-execute", Result: result,
		Requested: requested, ValidatedDigest: strings.TrimSpace(digest), Error: cause.Error(),
	}
	if err := addOrphanBurnAudit(bd, audit); err != nil {
		return fmt.Errorf("%v; additionally failed to persist rejection audit: %w", cause, err)
	}
	return cause
}

func writeOrphanBurnJSON(value any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}
