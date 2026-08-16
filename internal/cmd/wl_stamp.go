package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jonbaldie/gastown/internal/doltserver"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/wasteland"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var (
	wlStampSubject      string
	wlStampCompletionID string
	wlStampQuality      float64
	wlStampReliability  float64
	wlStampCreativity   float64
	wlStampConfidence   float64
	wlStampSeverity     string
	wlStampSkills       []string
	wlStampType         string
	wlStampContextType  string
	wlStampEvidenceURL  string
	wlStampMessage      string
	wlStampPilotCohort  string
)

var wlStampCmd = &cobra.Command{
	Use:   "stamp",
	Short: "Create a reputation stamp for a rig",
	Args:  cobra.NoArgs,
	RunE:  runWlStamp,
	Long: `Create a reputation stamp — the core HOP reputation primitive.

A stamp records a validator's assessment of a worker's contribution.
The validator (you) stamps the subject (worker) with dimensional scores.

Valence scores are 0-5 integers for quality, reliability, and creativity.
Confidence is auto-computed from your validator tier if not specified.

Phase 1: writes directly to the local wl-commons database.

EXAMPLES:
  gt wl stamp --subject alice --completion c-abc123 \
    --quality 4 --reliability 5 --creativity 3 \
    --skills go,federation --stamp-type work \
    --context-type completion --evidence 'https://github.com/org/repo/pull/42'

  gt wl stamp --subject bob --quality 3 --reliability 3 --creativity 2 \
    --stamp-type peer_review --context-type endorsement \
    --message "Great code review feedback"`,
}

func init() {
	wlStampCmd.Flags().StringVar(&wlStampSubject, "subject", "", "Rig handle of worker being stamped (required)")
	wlStampCmd.Flags().StringVar(&wlStampCompletionID, "completion", "", "Completion ID this stamp references")
	wlStampCmd.Flags().Float64Var(&wlStampQuality, "quality", -1, "Quality score 0-5 (required)")
	wlStampCmd.Flags().Float64Var(&wlStampReliability, "reliability", -1, "Reliability score 0-5")
	wlStampCmd.Flags().Float64Var(&wlStampCreativity, "creativity", -1, "Creativity score 0-5")
	wlStampCmd.Flags().Float64Var(&wlStampConfidence, "confidence", -1, "Confidence 0.0-1.0 (auto-computed from tier if omitted)")
	wlStampCmd.Flags().StringVar(&wlStampSeverity, "severity", "leaf", "Severity: leaf, branch, root")
	wlStampCmd.Flags().StringSliceVar(&wlStampSkills, "skills", nil, "Skill tags (comma-separated, e.g., go,federation)")
	wlStampCmd.Flags().StringVar(&wlStampType, "stamp-type", "work", "Stamp type: work, mentoring, peer_review, boot_block")
	wlStampCmd.Flags().StringVar(&wlStampContextType, "context-type", "completion", "Context type: completion, endorsement, boot_block, validation_received, sandboxed_completion")
	wlStampCmd.Flags().StringVar(&wlStampEvidenceURL, "evidence", "", "Evidence URL (PR link, SkillBench summary)")
	wlStampCmd.Flags().StringVar(&wlStampMessage, "message", "", "Optional human-readable note")
	wlStampCmd.Flags().StringVar(&wlStampPilotCohort, "pilot-cohort", "", "Pilot cohort tag (andela-pilot, commbank-pilot, indie)")

	_ = wlStampCmd.MarkFlagRequired("subject")
	_ = wlStampCmd.MarkFlagRequired("quality")

	wlCmd.AddCommand(wlStampCmd)
}

func runWlStamp(cmd *cobra.Command, args []string) error {
	// Validate inputs
	if err := validateStampInputs(); err != nil {
		return err
	}

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	wlCfg, err := wasteland.LoadConfig(townRoot)
	if err != nil {
		return fmt.Errorf("loading wasteland config: %w", err)
	}
	author := wlCfg.RigHandle

	if author == wlStampSubject {
		return fmt.Errorf("cannot stamp yourself (author=%q, subject=%q)", author, wlStampSubject)
	}

	// Build valence JSON
	valence := buildValenceJSON(wlStampQuality, wlStampReliability, wlStampCreativity)

	// Build skill tags JSON
	skillTagsJSON := ""
	if len(wlStampSkills) > 0 {
		skillTagsJSON = buildSkillTagsJSON(wlStampSkills)
	}

	// Compute confidence (default to 0.7 if not specified — "trusted" tier)
	confidence := wlStampConfidence
	if confidence < 0 {
		confidence = 0.7
	}

	// Generate stamp ID from content hash
	stampID := generateStampID(author, wlStampSubject, valence, wlStampCompletionID)

	stamp := &doltserver.StampRecord{
		ID:          stampID,
		Author:      author,
		Subject:     wlStampSubject,
		Valence:     valence,
		Confidence:  confidence,
		Severity:    wlStampSeverity,
		ContextID:   wlStampCompletionID,
		ContextType: wlStampContextType,
		StampType:   wlStampType,
		PilotCohort: wlStampPilotCohort,
		SkillTags:   skillTagsJSON,
		Message:     wlStampMessage,
		StampIndex:  -1, // will be computed below
	}

	dbName := wasteland.ResolveDBName(townRoot)
	if !doltserver.DatabaseExists(townRoot, dbName) {
		if wlCfg.LocalDir == "" {
			return fmt.Errorf("database %q not found\nJoin a wasteland first with: gt wl join <org/db>", dbName)
		}
		return insertStampInLocalClone(wlCfg.LocalDir, stamp)
	}

	store := doltserver.NewWLCommons(townRoot)
	if err := insertStamp(store, stamp); err != nil {
		return err
	}

	fmt.Printf("%s Stamp created\n", style.Bold.Render("✓"))
	fmt.Printf("  Stamp ID: %s\n", stampID)
	fmt.Printf("  Author: %s\n", author)
	fmt.Printf("  Subject: %s\n", wlStampSubject)
	fmt.Printf("  Valence: %s\n", valence)
	fmt.Printf("  Confidence: %.2f\n", confidence)
	fmt.Printf("  Severity: %s\n", wlStampSeverity)
	fmt.Printf("  Type: %s\n", wlStampType)
	if wlStampPilotCohort != "" {
		fmt.Printf("  Cohort: %s\n", wlStampPilotCohort)
	}
	if wlStampCompletionID != "" {
		fmt.Printf("  Completion: %s\n", wlStampCompletionID)
	}
	if stamp.StampIndex >= 0 {
		fmt.Printf("  Stamp index: %d\n", stamp.StampIndex)
	}

	return nil
}

func validateStampInputs() error {
	if wlStampQuality < 0 || wlStampQuality > 5 {
		return fmt.Errorf("quality must be 0-5 (got %.1f)", wlStampQuality)
	}
	if wlStampReliability >= 0 && wlStampReliability > 5 {
		return fmt.Errorf("reliability must be 0-5 (got %.1f)", wlStampReliability)
	}
	if wlStampCreativity >= 0 && wlStampCreativity > 5 {
		return fmt.Errorf("creativity must be 0-5 (got %.1f)", wlStampCreativity)
	}
	if wlStampConfidence >= 0 && (wlStampConfidence < 0 || wlStampConfidence > 1) {
		return fmt.Errorf("confidence must be 0.0-1.0 (got %.2f)", wlStampConfidence)
	}

	validSeverities := map[string]bool{"leaf": true, "branch": true, "root": true}
	if !validSeverities[wlStampSeverity] {
		return fmt.Errorf("severity must be leaf, branch, or root (got %q)", wlStampSeverity)
	}

	validStampTypes := map[string]bool{"work": true, "mentoring": true, "peer_review": true, "endorsement": true, "boot_block": true}
	if !validStampTypes[wlStampType] {
		return fmt.Errorf("stamp-type must be work, mentoring, peer_review, endorsement, or boot_block (got %q)", wlStampType)
	}

	if wlStampPilotCohort != "" {
		validCohorts := map[string]bool{"andela-pilot": true, "commbank-pilot": true, "indie": true}
		if !validCohorts[wlStampPilotCohort] {
			return fmt.Errorf("pilot-cohort must be andela-pilot, commbank-pilot, or indie (got %q)", wlStampPilotCohort)
		}
	}

	validContextTypes := map[string]bool{
		"completion": true, "endorsement": true, "boot_block": true,
		"validation_received": true, "sandboxed_completion": true,
	}
	if !validContextTypes[wlStampContextType] {
		return fmt.Errorf("context-type must be completion, endorsement, boot_block, validation_received, or sandboxed_completion (got %q)", wlStampContextType)
	}

	return nil
}

func buildValenceJSON(quality, reliability, creativity float64) string {
	parts := []string{fmt.Sprintf(`"quality":%.0f`, quality)}
	if reliability >= 0 {
		parts = append(parts, fmt.Sprintf(`"reliability":%.0f`, reliability))
	}
	if creativity >= 0 {
		parts = append(parts, fmt.Sprintf(`"creativity":%.0f`, creativity))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func buildSkillTagsJSON(skills []string) string {
	quoted := make([]string, len(skills))
	for i, s := range skills {
		quoted[i] = fmt.Sprintf(`"%s"`, strings.TrimSpace(s))
	}
	return "[" + strings.Join(quoted, ",") + "]"
}

// stampCounter provides a monotonically-incrementing component for generateStampID.
// On Windows, time.Now() has ~100ns–15ms resolution, making back-to-back calls
// return identical timestamps and therefore identical IDs (GH#3104).
var stampCounter atomic.Uint64

func generateStampID(author, subject, valence, contextID string) string {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	seq := stampCounter.Add(1)
	input := fmt.Sprintf("%s|%s|%s|%s|%s|%d", author, subject, valence, contextID, now, seq)
	hash := sha256.Sum256([]byte(input))
	return fmt.Sprintf("s-%s", hex.EncodeToString(hash[:])[:12])
}

// insertStamp computes passbook chain linkage and inserts the stamp via the store.
func insertStamp(store doltserver.WLCommonsStore, stamp *doltserver.StampRecord) error {
	// Query the last stamp for this subject to compute chain linkage
	last, err := store.QueryLastStampForSubject(stamp.Subject)
	if err != nil {
		// Non-fatal: proceed without chain linkage
		stamp.StampIndex = 0
	} else if last == nil {
		// Genesis stamp for this subject
		stamp.StampIndex = 0
	} else {
		stamp.PrevStampHash = computeStampHash(last.ID)
		if last.StampIndex >= 0 {
			stamp.StampIndex = last.StampIndex + 1
		} else {
			stamp.StampIndex = 0
		}
	}

	return store.InsertStamp(stamp)
}

// computeStampHash generates a hash of a stamp ID for passbook chain linkage.
func computeStampHash(stampID string) string {
	hash := sha256.Sum256([]byte(stampID))
	return hex.EncodeToString(hash[:])
}

func insertStampInLocalClone(localDir string, stamp *doltserver.StampRecord) error {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	contextID := "NULL"
	if stamp.ContextID != "" {
		contextID = fmt.Sprintf("'%s'", doltserver.EscapeSQL(stamp.ContextID))
	}
	contextType := "NULL"
	if stamp.ContextType != "" {
		contextType = fmt.Sprintf("'%s'", doltserver.EscapeSQL(stamp.ContextType))
	}
	stampType := "NULL"
	if stamp.StampType != "" {
		stampType = fmt.Sprintf("'%s'", doltserver.EscapeSQL(stamp.StampType))
	}
	pilotCohort := "NULL"
	if stamp.PilotCohort != "" {
		pilotCohort = fmt.Sprintf("'%s'", doltserver.EscapeSQL(stamp.PilotCohort))
	}
	skillTags := "NULL"
	if stamp.SkillTags != "" {
		skillTags = fmt.Sprintf("'%s'", doltserver.EscapeSQL(stamp.SkillTags))
	}
	message := "NULL"
	if stamp.Message != "" {
		message = fmt.Sprintf("'%s'", doltserver.EscapeSQL(stamp.Message))
	}

	script := fmt.Sprintf(`INSERT INTO stamps (id, author, subject, valence, confidence, severity, context_id, context_type, stamp_type, pilot_cohort, skill_tags, message, created_at)
VALUES ('%s', '%s', '%s', '%s', %f, '%s', %s, %s, %s, %s, %s, %s, '%s');
CALL DOLT_ADD('-A');
CALL DOLT_COMMIT('-m', 'wl stamp: %s stamps %s');`,
		doltserver.EscapeSQL(stamp.ID), doltserver.EscapeSQL(stamp.Author), doltserver.EscapeSQL(stamp.Subject),
		doltserver.EscapeSQL(stamp.Valence), stamp.Confidence, doltserver.EscapeSQL(stamp.Severity),
		contextID, contextType, stampType, pilotCohort, skillTags, message, now,
		doltserver.EscapeSQL(stamp.Author), doltserver.EscapeSQL(stamp.Subject))

	cmd := exec.Command("dolt", "sql", "-q", script)
	cmd.Dir = localDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("inserting stamp: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}
