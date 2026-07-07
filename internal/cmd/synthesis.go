package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/formula"
	"github.com/steveyegge/gastown/internal/runtime"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

// Synthesis command flags
var (
	synthesisRig      string
	synthesisDryRun   bool
	synthesisForce    bool
	synthesisReviewID string
)

var synthesisCmd = &cobra.Command{
	Use:     "synthesis",
	Aliases: []string{"synth"},
	GroupID: GroupWork,
	Short:   "Manage convoy synthesis steps",
	RunE:    requireSubcommand,
	Long: `Manage synthesis steps for convoy formulas.

Synthesis is the final step in a convoy workflow that combines outputs
from all parallel legs into a unified deliverable.

Commands:
  start     Start synthesis for a convoy (checks all legs complete)
  status    Show synthesis readiness and leg outputs
  close     Close convoy after synthesis complete

Examples:
  gt synthesis status hq-cv-abc     # Check if ready for synthesis
  gt synthesis start hq-cv-abc      # Start synthesis step
  gt synthesis close hq-cv-abc      # Close convoy after synthesis`,
}

var synthesisStartCmd = &cobra.Command{
	Use:   "start <convoy-id>",
	Short: "Start synthesis for a convoy",
	Long: `Start the synthesis step for a convoy.

This command:
  1. Verifies all legs are complete
  2. Collects outputs from all legs
  3. Creates a synthesis bead with combined context
  4. Slings the synthesis to a polecat

Options:
  --rig=NAME      Target rig for synthesis polecat (default: current)
  --review-id=ID  Override review ID for output paths
  --force         Start synthesis even if some legs incomplete
  --dry-run       Show what would happen without executing`,
	Args: cobra.ExactArgs(1),
	RunE: runSynthesisStart,
}

var synthesisStatusCmd = &cobra.Command{
	Use:   "status <convoy-id>",
	Short: "Show synthesis readiness",
	Long: `Show whether a convoy is ready for synthesis.

Displays:
  - Convoy metadata
  - Leg completion status
  - Available leg outputs
  - Formula synthesis configuration`,
	Args: cobra.ExactArgs(1),
	RunE: runSynthesisStatus,
}

var synthesisCloseCmd = &cobra.Command{
	Use:   "close <convoy-id>",
	Short: "Close convoy after synthesis",
	Long: `Close a convoy after synthesis is complete.

This marks the convoy as complete and triggers any configured notifications.`,
	Args: cobra.ExactArgs(1),
	RunE: runSynthesisClose,
}

func init() {
	// Start flags
	synthesisStartCmd.Flags().StringVar(&synthesisRig, "rig", "", "Target rig for synthesis polecat")
	synthesisStartCmd.Flags().BoolVar(&synthesisDryRun, "dry-run", false, "Preview execution")
	synthesisStartCmd.Flags().BoolVar(&synthesisForce, "force", false, "Start even if legs incomplete")
	synthesisStartCmd.Flags().StringVar(&synthesisReviewID, "review-id", "", "Override review ID")

	// Add subcommands
	synthesisCmd.AddCommand(synthesisStartCmd)
	synthesisCmd.AddCommand(synthesisStatusCmd)
	synthesisCmd.AddCommand(synthesisCloseCmd)

	rootCmd.AddCommand(synthesisCmd)
}

// LegOutput represents collected output from a convoy leg.
type LegOutput struct {
	LegID    string `json:"leg_id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	FilePath string `json:"file_path,omitempty"`
	Content  string `json:"content,omitempty"`
	HasFile  bool   `json:"has_file"`
}

// ConvoyMeta holds metadata about a convoy including its formula.
type ConvoyMeta struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Status      string   `json:"status"`
	Formula     string   `json:"formula,omitempty"`      // Formula name
	FormulaPath string   `json:"formula_path,omitempty"` // Path to formula file
	Rig         string   `json:"rig,omitempty"`          // Rig used for formula resolution
	ReviewID    string   `json:"review_id,omitempty"`    // Review ID for output paths
	LegIssues   []string `json:"leg_issues,omitempty"`   // Tracked leg issue IDs
}

// runSynthesisStart implements gt synthesis start.
func runSynthesisStart(cmd *cobra.Command, args []string) error {
	convoyID := args[0]

	// Get convoy metadata
	meta, err := getConvoyMeta(convoyID)
	if err != nil {
		return fmt.Errorf("getting convoy metadata: %w", err)
	}

	fmt.Printf("%s Checking synthesis readiness for %s...\n", style.Bold.Render("🔬"), convoyID)
	targetRig := resolveSynthesisTargetRig()

	// Load formula if specified
	f, err := loadSynthesisFormula(meta, targetRig)
	if err != nil {
		return err
	}

	// Check leg completion status
	legOutputs, allComplete, err := collectLegOutputs(meta, f)
	if err != nil {
		return fmt.Errorf("collecting leg outputs: %w", err)
	}

	// Report status
	completedCount := 0
	for _, leg := range legOutputs {
		if leg.Status == "closed" {
			completedCount++
		}
	}
	fmt.Printf("  Legs: %d/%d complete\n", completedCount, len(legOutputs))

	if !allComplete && !synthesisForce {
		fmt.Printf("\n%s Not all legs complete. Use --force to proceed anyway.\n",
			style.Warning.Render("⚠"))
		fmt.Printf("\nIncomplete legs:\n")
		for _, leg := range legOutputs {
			if leg.Status != "closed" {
				fmt.Printf("  ○ %s: %s [%s]\n", leg.LegID, leg.Title, leg.Status)
			}
		}
		return nil
	}

	// Determine review ID
	reviewID := synthesisReviewID
	if reviewID == "" {
		reviewID = meta.ReviewID
	}
	if reviewID == "" {
		// Extract from convoy ID
		reviewID = strings.TrimPrefix(convoyID, "hq-cv-")
	}

	if synthesisDryRun {
		fmt.Printf("\n%s Would start synthesis:\n", style.Dim.Render("[dry-run]"))
		fmt.Printf("  Convoy:    %s\n", convoyID)
		fmt.Printf("  Review ID: %s\n", reviewID)
		fmt.Printf("  Target:    %s\n", targetRig)
		fmt.Printf("  Legs:      %d outputs collected\n", len(legOutputs))
		if f != nil && f.Synthesis != nil {
			fmt.Printf("  Synthesis: %s\n", f.Synthesis.Title)
		}
		return nil
	}

	// Create synthesis bead
	synthesisID, err := createSynthesisBead(convoyID, meta, f, legOutputs, reviewID)
	if err != nil {
		return fmt.Errorf("creating synthesis bead: %w", err)
	}
	fmt.Printf("%s Created synthesis bead: %s\n", style.Bold.Render("✓"), synthesisID)

	// Sling to target rig
	fmt.Printf("  Slinging to %s...\n", targetRig)
	if err := slingSynthesis(synthesisID, targetRig); err != nil {
		return fmt.Errorf("slinging synthesis: %w", err)
	}

	fmt.Printf("%s Synthesis started\n", style.Bold.Render("✓"))
	fmt.Printf("  Monitor: gt convoy status %s\n", convoyID)

	return nil
}

// runSynthesisStatus implements gt synthesis status.
func runSynthesisStatus(cmd *cobra.Command, args []string) error {
	convoyID := args[0]

	meta, err := getConvoyMeta(convoyID)
	if err != nil {
		return fmt.Errorf("getting convoy metadata: %w", err)
	}

	// Load formula if available
	f, _ := loadSynthesisFormula(meta, "")

	// Collect leg outputs
	legOutputs, allComplete, err := collectLegOutputs(meta, f)
	if err != nil {
		return fmt.Errorf("collecting leg outputs: %w", err)
	}

	// Display status
	fmt.Printf("🚚 %s %s\n\n", style.Bold.Render(convoyID+":"), meta.Title)
	fmt.Printf("  Status: %s\n", formatConvoyStatus(meta.Status))

	if meta.Formula != "" {
		fmt.Printf("  Formula: %s\n", meta.Formula)
	}

	fmt.Printf("\n  %s\n", style.Bold.Render("Legs:"))
	for _, leg := range legOutputs {
		status := "○"
		if leg.Status == "closed" {
			status = "✓"
		}
		fileStatus := ""
		if leg.HasFile {
			fileStatus = style.Dim.Render(" (output: ✓)")
		}
		fmt.Printf("    %s %s: %s [%s]%s\n", status, leg.LegID, leg.Title, leg.Status, fileStatus)
	}

	// Synthesis readiness
	fmt.Printf("\n  %s\n", style.Bold.Render("Synthesis:"))
	if allComplete {
		fmt.Printf("    %s Ready - all legs complete\n", style.Success.Render("✓"))
		fmt.Printf("    Run: gt synthesis start %s\n", convoyID)
	} else {
		completedCount := 0
		for _, leg := range legOutputs {
			if leg.Status == "closed" {
				completedCount++
			}
		}
		fmt.Printf("    %s Waiting - %d/%d legs complete\n",
			style.Warning.Render("○"), completedCount, len(legOutputs))
	}

	if f != nil && f.Synthesis != nil {
		fmt.Printf("\n  %s\n", style.Bold.Render("Synthesis Config:"))
		fmt.Printf("    Title: %s\n", f.Synthesis.Title)
		if f.Output != nil && f.Output.Synthesis != "" {
			fmt.Printf("    Output: %s\n", f.Output.Synthesis)
		}
	}

	return nil
}

// runSynthesisClose implements gt synthesis close.
func runSynthesisClose(cmd *cobra.Command, args []string) error {
	convoyID := args[0]

	townBeads, err := getTownBeadsDir()
	if err != nil {
		return err
	}

	// Read convoy to validate lifecycle state before closing
	showArgs := []string{"show", convoyID, "--json"}
	showCmd := exec.Command("bd", showArgs...)
	showCmd.Dir = townBeads
	var showOut bytes.Buffer
	showCmd.Stdout = &showOut
	if err := showCmd.Run(); err != nil {
		return fmt.Errorf("reading convoy '%s': %w", convoyID, err)
	}
	var convoys []struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(showOut.Bytes(), &convoys); err != nil || len(convoys) == 0 {
		return fmt.Errorf("parsing convoy '%s': invalid response", convoyID)
	}
	status := convoys[0].Status

	if err := ensureKnownConvoyStatus(status); err != nil {
		return fmt.Errorf("convoy '%s' has invalid lifecycle state: %w", convoyID, err)
	}

	// Idempotent: if already closed, just report it
	if normalizeConvoyStatus(status) == convoyStatusClosed {
		fmt.Printf("%s Convoy %s is already closed\n", style.Dim.Render("○"), convoyID)
		return nil
	}

	// Close the convoy
	closeArgs := []string{"close", convoyID, "--reason=synthesis complete"}
	if sessionID := runtime.SessionIDFromEnv(); sessionID != "" {
		closeArgs = append(closeArgs, "--session="+sessionID)
	}
	closeCmd := exec.Command("bd", closeArgs...)
	closeCmd.Dir = townBeads
	closeCmd.Stderr = os.Stderr

	if err := closeCmd.Run(); err != nil {
		return fmt.Errorf("closing convoy: %w", err)
	}

	fmt.Printf("%s Convoy closed: %s\n", style.Bold.Render("✓"), convoyID)

	// TODO: Trigger notification if configured
	// Parse description for "Notify: <address>" and send mail

	return nil
}

// getConvoyMeta retrieves convoy metadata from beads.
func getConvoyMeta(convoyID string) (*ConvoyMeta, error) {
	townBeads, err := getTownBeadsDir()
	if err != nil {
		return nil, err
	}

	showCmd := exec.Command("bd", "show", convoyID, "--json")
	showCmd.Dir = townBeads
	var stdout bytes.Buffer
	showCmd.Stdout = &stdout

	if err := showCmd.Run(); err != nil {
		return nil, fmt.Errorf("convoy '%s' not found", convoyID)
	}

	var convoys []struct {
		ID          string   `json:"id"`
		Title       string   `json:"title"`
		Status      string   `json:"status"`
		Description string   `json:"description"`
		Type        string   `json:"issue_type"`
		Labels      []string `json:"labels"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &convoys); err != nil {
		return nil, fmt.Errorf("parsing convoy data: %w", err)
	}

	if len(convoys) == 0 || !isConvoyIssue(convoys[0].Type, convoys[0].Labels) {
		return nil, fmt.Errorf("'%s' is not a convoy", convoyID)
	}

	convoy := convoys[0]

	// Parse formula and review ID from description
	meta := &ConvoyMeta{
		ID:     convoy.ID,
		Title:  convoy.Title,
		Status: convoy.Status,
	}

	parseConvoyDescriptionMeta(meta, convoy.Description)

	// Get tracked leg issues
	tracked, err := getTrackedIssues(townBeads, convoyID)
	if err != nil {
		return nil, fmt.Errorf("getting tracked issues for convoy %s: %w", convoyID, err)
	}
	for _, t := range tracked {
		meta.LegIssues = append(meta.LegIssues, t.ID)
	}

	return meta, nil
}

// collectLegOutputs gathers outputs from all convoy legs.
func collectLegOutputs(meta *ConvoyMeta, f *formula.Formula) ([]LegOutput, bool, error) { //nolint:unparam // error return kept for future use
	var outputs []LegOutput
	allComplete := true

	// If we have tracked issues, use those as legs
	if len(meta.LegIssues) > 0 {
		for _, issueID := range meta.LegIssues {
			details := getIssueDetails(issueID)
			output := LegOutput{
				LegID: issueID,
				Title: "(unknown)",
			}
			if details != nil {
				output.Title = details.Title
				output.Status = details.Status
			}
			if output.Status != "closed" {
				allComplete = false
			}
			outputs = append(outputs, output)
		}
	}

	// If we have a formula, also try to find output files
	if f != nil && f.Output != nil && meta.ReviewID != "" {
		for _, leg := range f.Legs {
			// Expand output path template
			outputPath := expandOutputPath(f.Output.Directory, f.Output.LegPattern,
				meta.ReviewID, leg.ID)

			// Check if file exists and read content
			if content, err := os.ReadFile(outputPath); err == nil {
				// Find or create leg output entry
				found := false
				for i := range outputs {
					if outputs[i].LegID == leg.ID {
						outputs[i].FilePath = outputPath
						outputs[i].Content = string(content)
						outputs[i].HasFile = true
						found = true
						break
					}
				}
				if !found {
					outputs = append(outputs, LegOutput{
						LegID:    leg.ID,
						Title:    leg.Title,
						Status:   "closed", // If file exists, assume complete
						FilePath: outputPath,
						Content:  string(content),
						HasFile:  true,
					})
				}
			}
		}
	}

	return outputs, allComplete, nil
}

// expandOutputPath expands template variables in output paths.
// Supports Go template output syntax plus legacy bare placeholders.
func expandOutputPath(directory, pattern, reviewID, legID string) string {
	dir := expandOutputTemplate(directory, reviewID, legID)
	file := expandOutputTemplate(pattern, reviewID, legID)
	return filepath.Join(dir, file)
}

func expandOutputTemplate(tmplText, reviewID, legID string) string {
	ctx := map[string]interface{}{
		"review_id": reviewID,
		"leg": map[string]interface{}{
			"id": legID,
		},
	}
	if rendered, err := renderTemplate(tmplText, ctx); err == nil {
		return rendered
	}

	text := strings.ReplaceAll(tmplText, "{{review_id}}", reviewID)
	return strings.ReplaceAll(text, "{{leg.id}}", legID)
}

// createSynthesisBead creates a bead for the synthesis step.
func createSynthesisBead(convoyID string, meta *ConvoyMeta, f *formula.Formula,
	legOutputs []LegOutput, reviewID string) (string, error) {

	// Build synthesis title
	title := "Synthesis: " + meta.Title
	if f != nil && f.Synthesis != nil && f.Synthesis.Title != "" {
		title = f.Synthesis.Title + ": " + meta.Title
	}

	// Build synthesis description with leg outputs
	var desc strings.Builder
	desc.WriteString(fmt.Sprintf("convoy: %s\n", convoyID))
	desc.WriteString(fmt.Sprintf("review_id: %s\n", reviewID))
	desc.WriteString("\n")

	var outputDir, outputSynthesis string
	if f != nil && f.Output != nil {
		outputDir = expandOutputTemplate(f.Output.Directory, reviewID, "")
		outputSynthesis = f.Output.Synthesis
	}

	// Add synthesis instructions from formula
	if f != nil && f.Synthesis != nil && f.Synthesis.Description != "" {
		formulaName := meta.Formula
		if formulaName == "" {
			formulaName = f.Name
		}
		synCtx := formulaTemplateContext(formulaName, meta.Title, reviewID, 0, "", nil, nil, nil)
		synCtx["problem"] = meta.Title
		addOutputTemplateContext(synCtx, outputDir, outputSynthesis)
		synDesc := renderTemplateOrDefault(f.Synthesis.Description, synCtx, f.Synthesis.Description)

		desc.WriteString("## Instructions\n\n")
		desc.WriteString(synDesc)
		desc.WriteString("\n\n")
	}

	// Add collected leg outputs
	desc.WriteString("## Leg Outputs\n\n")
	for _, leg := range legOutputs {
		desc.WriteString(fmt.Sprintf("### %s: %s\n\n", leg.LegID, leg.Title))
		if leg.Content != "" {
			desc.WriteString(leg.Content)
			desc.WriteString("\n\n")
		} else if leg.FilePath != "" {
			desc.WriteString(fmt.Sprintf("Output file: %s\n\n", leg.FilePath))
		} else {
			desc.WriteString("(no output available)\n\n")
		}
	}

	// Add output path if configured
	if f != nil && f.Output != nil && f.Output.Synthesis != "" {
		outputPath := filepath.Join(outputDir, f.Output.Synthesis)
		desc.WriteString(fmt.Sprintf("\n## Output\n\nWrite synthesis to: %s\n", outputPath))
	}

	// Guard against flag-like synthesis titles (gt-e0kx5)
	if beads.IsFlagLikeTitle(title) {
		return "", fmt.Errorf("refusing to create synthesis bead: title %q looks like a CLI flag", title)
	}

	// Create the bead
	createArgs := []string{
		"create",
		"--type=task",
		"--title=" + title,
		"--description=" + desc.String(),
		"--json",
	}

	townBeads, err := getTownBeadsDir()
	if err != nil {
		return "", err
	}

	createCmd := exec.Command("bd", createArgs...)
	createCmd.Dir = townBeads
	var stdout bytes.Buffer
	createCmd.Stdout = &stdout
	createCmd.Stderr = os.Stderr

	if err := createCmd.Run(); err != nil {
		return "", fmt.Errorf("creating synthesis bead: %w", err)
	}

	// Parse created bead ID
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		// Try to extract ID from non-JSON output (bead IDs have format: prefix-id)
		out := strings.TrimSpace(stdout.String())
		if looksLikeIssueID(out) {
			return out, nil
		}
		return "", fmt.Errorf("parsing created bead: %w", err)
	}

	// Add tracking relation: convoy tracks synthesis.
	_ = addTrackingRelationFn(townBeads, convoyID, result.ID) // Non-fatal if this fails

	return result.ID, nil
}

// slingSynthesis slings the synthesis bead to a rig.
func slingSynthesis(beadID, targetRig string) error {
	slingArgs := []string{"sling", beadID, targetRig}
	slingCmd := exec.Command("gt", slingArgs...)
	slingCmd.Stdout = os.Stdout
	slingCmd.Stderr = os.Stderr

	return slingCmd.Run()
}

func resolveSynthesisTargetRig() string {
	if synthesisRig != "" {
		return synthesisRig
	}
	townRoot, err := workspace.FindFromCwdOrError()
	if err == nil {
		rigName, _, rigErr := findCurrentRig(townRoot)
		if rigErr == nil && rigName != "" {
			return rigName
		}
	}
	return "gastown"
}

func loadSynthesisFormula(meta *ConvoyMeta, targetRig string) (*formula.Formula, error) {
	if meta.FormulaPath != "" {
		f, err := formula.ParseFile(meta.FormulaPath)
		if err != nil {
			return nil, fmt.Errorf("loading formula: %w", err)
		}
		return f, nil
	}
	if meta.Formula == "" {
		return nil, nil
	}
	rigName := meta.Rig
	if rigName == "" {
		rigName = targetRig
	}
	f, err := loadNamedFormulaFromCwd(meta.Formula, rigName)
	if errors.Is(err, formula.ErrFormulaNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("loading formula: %w", err)
	}
	return f, nil
}

func parseConvoyDescriptionMeta(meta *ConvoyMeta, description string) {
	for _, line := range strings.Split(description, "\n") {
		line = strings.TrimSpace(line)
		if colonIdx := strings.Index(line, ":"); colonIdx != -1 {
			key := strings.ToLower(strings.TrimSpace(line[:colonIdx]))
			value := strings.TrimSpace(line[colonIdx+1:])
			switch key {
			case "formula", "formula convoy":
				meta.Formula = value
			case "formula_path", "formula-path":
				meta.FormulaPath = value
			case "review_id", "review-id":
				meta.ReviewID = value
			case "rig":
				meta.Rig = value
			}
		}
	}
}

// CheckSynthesisReady checks if a convoy is ready for synthesis.
// Returns true if all tracked legs are complete.
func CheckSynthesisReady(convoyID string) (bool, error) {
	meta, err := getConvoyMeta(convoyID)
	if err != nil {
		return false, err
	}

	_, allComplete, err := collectLegOutputs(meta, nil)
	return allComplete, err
}

// TriggerSynthesisIfReady checks convoy status and starts synthesis if ready.
// This can be called by the witness when a leg completes.
func TriggerSynthesisIfReady(convoyID, targetRig string) error {
	ready, err := CheckSynthesisReady(convoyID)
	if err != nil {
		return err
	}

	if !ready {
		return nil // Not ready yet
	}

	// Synthesis is ready - start it
	fmt.Printf("%s All legs complete, starting synthesis...\n", style.Bold.Render("🔬"))

	meta, err := getConvoyMeta(convoyID)
	if err != nil {
		return err
	}

	// Load formula if available
	f, _ := loadSynthesisFormula(meta, targetRig)

	legOutputs, _, _ := collectLegOutputs(meta, f)
	reviewID := meta.ReviewID
	if reviewID == "" {
		reviewID = strings.TrimPrefix(convoyID, "hq-cv-")
	}

	synthesisID, err := createSynthesisBead(convoyID, meta, f, legOutputs, reviewID)
	if err != nil {
		return fmt.Errorf("creating synthesis bead: %w", err)
	}

	if err := slingSynthesis(synthesisID, targetRig); err != nil {
		return fmt.Errorf("slinging synthesis: %w", err)
	}

	return nil
}
