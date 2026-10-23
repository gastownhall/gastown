package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/deacon"
	"github.com/steveyegge/gastown/internal/workspace"
)

var validationCmd = &cobra.Command{
	Use:     "validation",
	GroupID: GroupWork,
	Short:   "Report and inspect confirmed validation failures",
	RunE:    requireSubcommand,
}

var validationReportCmd = &cobra.Command{
	Use:   "report",
	Short: "Record a confirmed regression and invoke the configured recovery policy",
	Long: `Record a deterministic validation failure after flaky retries and baseline
comparison have ruled out transient, infrastructure, and pre-existing failures.

Pre-merge reports preserve and reassign the existing branch. Post-merge reports
create a forward-fix bug. Each incident receives at most the configured number
of hosted repair attempts.`,
	RunE: runValidationReport,
}

var validationStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show validation recovery incidents and main-branch state",
	RunE:  runValidationStatus,
}

var validationResolveCmd = &cobra.Command{
	Use:   "resolve",
	Short: "Mark matching validation recovery incidents resolved",
	RunE:  runValidationResolve,
}

var (
	validationRig          string
	validationKind         string
	validationSummary      string
	validationCommit       string
	validationSourceIssue  string
	validationMergeRequest string
	validationBranch       string
	validationPhase        string
	validationCommand      string
	validationExitCode     int
	validationEvidenceFile string
	validationEvidence     string
	validationIncidentID   string
	validationJSON         bool

	resolveValidationRig         string
	resolveValidationSourceIssue string
	resolveValidationPhase       string
	resolveValidationIncidentID  string
)

func init() {
	validationCmd.AddCommand(validationReportCmd)
	validationCmd.AddCommand(validationStatusCmd)
	validationCmd.AddCommand(validationResolveCmd)

	validationReportCmd.Flags().StringVar(&validationRig, "rig", "", "Rig containing the failed work (required)")
	validationReportCmd.Flags().StringVar(&validationKind, "kind", "", "Failure kind: functional, test, build, lint, or typecheck (required)")
	validationReportCmd.Flags().StringVar(&validationSummary, "summary", "", "Concise failure summary (required)")
	validationReportCmd.Flags().StringVar(&validationCommit, "commit", "", "Failing commit SHA (required for post-merge)")
	validationReportCmd.Flags().StringVar(&validationSourceIssue, "source-issue", "", "Original source issue (required for pre-merge)")
	validationReportCmd.Flags().StringVar(&validationMergeRequest, "merge-request", "", "Merge-request bead ID")
	validationReportCmd.Flags().StringVar(&validationBranch, "branch", "", "Existing repair branch (required for pre-merge)")
	validationReportCmd.Flags().StringVar(&validationPhase, "phase", "post-merge", "Failure phase: pre-merge or post-merge")
	validationReportCmd.Flags().StringVar(&validationCommand, "command", "", "Command or check that failed")
	validationReportCmd.Flags().IntVar(&validationExitCode, "exit-code", 0, "Failing command exit code")
	validationReportCmd.Flags().StringVar(&validationEvidenceFile, "evidence-file", "", "File containing bounded failure evidence")
	validationReportCmd.Flags().StringVar(&validationEvidence, "evidence", "", "Inline failure evidence (for automated producers)")
	validationReportCmd.Flags().StringVar(&validationIncidentID, "incident", "", "Existing incident ID for a hosted repair result")
	validationReportCmd.Flags().BoolVar(&validationJSON, "json", false, "Output result as JSON")
	_ = validationReportCmd.MarkFlagRequired("rig")
	_ = validationReportCmd.MarkFlagRequired("kind")
	_ = validationReportCmd.MarkFlagRequired("summary")

	validationStatusCmd.Flags().BoolVar(&validationJSON, "json", false, "Output state as JSON")

	validationResolveCmd.Flags().StringVar(&resolveValidationIncidentID, "incident", "", "Resolve one incident ID")
	validationResolveCmd.Flags().StringVar(&resolveValidationRig, "rig", "", "Limit resolution to a rig")
	validationResolveCmd.Flags().StringVar(&resolveValidationSourceIssue, "source-issue", "", "Resolve incidents for a source or repair issue")
	validationResolveCmd.Flags().StringVar(&resolveValidationPhase, "phase", "", "Limit resolution to pre-merge or post-merge")

	rootCmd.AddCommand(validationCmd)
}

func runValidationReport(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	evidence := validationEvidence
	if validationEvidenceFile != "" {
		data, readErr := os.ReadFile(validationEvidenceFile) //nolint:gosec // operator-supplied evidence file
		if readErr != nil {
			return fmt.Errorf("reading evidence file: %w", readErr)
		}
		if evidence != "" {
			evidence += "\n"
		}
		evidence += string(data)
	}

	result := deacon.ProcessValidationFailure(townRoot, deacon.ValidationFailure{
		IncidentID:   validationIncidentID,
		Rig:          validationRig,
		SourceIssue:  validationSourceIssue,
		MergeRequest: validationMergeRequest,
		Branch:       validationBranch,
		Commit:       validationCommit,
		Phase:        validationPhase,
		Kind:         validationKind,
		Command:      validationCommand,
		ExitCode:     validationExitCode,
		Summary:      validationSummary,
		Evidence:     evidence,
	})
	if validationJSON {
		out := struct {
			IncidentID string `json:"incident_id,omitempty"`
			Action     string `json:"action"`
			RepairBead string `json:"repair_bead,omitempty"`
			Message    string `json:"message,omitempty"`
			Error      string `json:"error,omitempty"`
		}{
			IncidentID: result.IncidentID,
			Action:     result.Action,
			RepairBead: result.RepairBead,
			Message:    result.Message,
		}
		if result.Error != nil {
			out.Error = result.Error.Error()
		}
		data, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(data))
	} else if result.IncidentID != "" || result.Error == nil {
		fmt.Printf("%s: %s", result.Action, result.IncidentID)
		if result.RepairBead != "" {
			fmt.Printf(" repair=%s", result.RepairBead)
		}
		if result.Message != "" {
			fmt.Printf(" — %s", result.Message)
		}
		fmt.Println()
	}
	if result.Error != nil {
		return result.Error
	}
	if result.Action == "disabled" {
		return errors.New(result.Message)
	}
	return nil
}

func runValidationStatus(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}
	state, err := deacon.LoadValidationState(townRoot)
	if err != nil {
		return err
	}
	if validationJSON {
		data, err := json.MarshalIndent(state, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}
	if len(state.Incidents) == 0 && len(state.MainBranches) == 0 {
		fmt.Println("No validation recovery state recorded")
		return nil
	}
	ids := make([]string, 0, len(state.Incidents))
	for id := range state.Incidents {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		incident := state.Incidents[id]
		fmt.Printf("%s  rig=%s phase=%s status=%s hosted=%d/%d",
			id, incident.Rig, incident.Phase, incident.Status,
			incident.HostedAttempts, incident.MaxHostedAttempts)
		if incident.SourceIssue != "" {
			fmt.Printf(" source=%s", incident.SourceIssue)
		}
		if incident.RepairBead != "" {
			fmt.Printf(" repair=%s", incident.RepairBead)
		}
		fmt.Println()
	}
	rigs := make([]string, 0, len(state.MainBranches))
	for rigName := range state.MainBranches {
		rigs = append(rigs, rigName)
	}
	sort.Strings(rigs)
	for _, rigName := range rigs {
		mainState := state.MainBranches[rigName]
		fmt.Printf("main/%s  green=%s tested=%s\n", rigName, mainState.LastGreenSHA, mainState.LastTestedSHA)
	}
	return nil
}

func runValidationResolve(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}
	if strings.TrimSpace(resolveValidationIncidentID) == "" &&
		strings.TrimSpace(resolveValidationSourceIssue) == "" {
		return errors.New("provide --incident or --source-issue")
	}
	if resolveValidationPhase != "" &&
		resolveValidationPhase != "pre-merge" &&
		resolveValidationPhase != "post-merge" {
		return fmt.Errorf("invalid phase %q", resolveValidationPhase)
	}
	count, err := deacon.ResolveValidationIncidents(
		townRoot,
		resolveValidationIncidentID,
		resolveValidationRig,
		resolveValidationSourceIssue,
		resolveValidationPhase,
	)
	if err != nil {
		return err
	}
	fmt.Printf("Resolved %d validation incident(s)\n", count)
	return nil
}
