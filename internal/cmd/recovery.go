package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/daemon"
	"github.com/steveyegge/gastown/internal/deacon"
	"github.com/steveyegge/gastown/internal/workspace"
)

var (
	recoveryJSON bool

	recoveryCmd = &cobra.Command{
		Use:     "recovery",
		GroupID: GroupDiag,
		Short:   "Inspect the local-first recovery ladder",
		RunE:    requireSubcommand,
	}

	recoveryStatusCmd = &cobra.Command{
		Use:   "status",
		Short: "Show session, validation, and infrastructure recovery incidents",
		RunE:  runRecoveryStatus,
	}
)

type recoveryStatusIncident struct {
	ID             string    `json:"id"`
	Class          string    `json:"class"`
	Identity       string    `json:"identity,omitempty"`
	Rig            string    `json:"rig,omitempty"`
	WorkUnit       string    `json:"work_unit,omitempty"`
	Tier           string    `json:"tier,omitempty"`
	Status         string    `json:"status"`
	LeaseOwner     string    `json:"lease_owner,omitempty"`
	LocalAttempts  int       `json:"local_attempts,omitempty"`
	HostedAttempts int       `json:"hosted_attempts,omitempty"`
	Nudges         int       `json:"nudges,omitempty"`
	NextActionAt   time.Time `json:"next_action_at,omitempty"`
	Evidence       string    `json:"evidence,omitempty"`
	Terminal       bool      `json:"terminal,omitempty"`
}

type recoveryStatusOutput struct {
	GeneratedAt    time.Time                `json:"generated_at"`
	Infrastructure map[string]any           `json:"infrastructure,omitempty"`
	Incidents      []recoveryStatusIncident `json:"incidents"`
}

func init() {
	recoveryCmd.AddCommand(recoveryStatusCmd)
	recoveryStatusCmd.Flags().BoolVar(&recoveryJSON, "json", false, "Output as JSON")
	rootCmd.AddCommand(recoveryCmd)
	beadsExemptCommands["recovery"] = true
	branchCheckExemptCommands["recovery"] = true
}

func runRecoveryStatus(_ *cobra.Command, _ []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}
	out := recoveryStatusOutput{GeneratedAt: time.Now().UTC()}

	sessionIncidents, err := daemon.LoadModelCrashRecoveryIncidents(townRoot)
	if err != nil {
		return fmt.Errorf("loading session recovery state: %w", err)
	}
	for _, incident := range sessionIncidents {
		out.Incidents = append(out.Incidents, recoveryStatusIncident{
			ID:             incident.IncidentID,
			Class:          incident.Kind,
			Identity:       incident.Identity,
			WorkUnit:       incident.WorkUnit,
			Tier:           incident.Tier,
			Status:         incident.RecoveryAction,
			LeaseOwner:     incident.LeaseOwner,
			LocalAttempts:  incident.LocalRestarts + incident.ControlProbes,
			HostedAttempts: incident.GoContinuations,
			Nudges:         incident.StallNudges,
			NextActionAt:   incident.NextActionAt,
			Terminal:       incident.RecoveryExhausted,
		})
	}

	validation, err := deacon.LoadValidationState(townRoot)
	if err != nil {
		return fmt.Errorf("loading validation recovery state: %w", err)
	}
	for _, incident := range validation.Incidents {
		if incident == nil {
			continue
		}
		tier := "local"
		if incident.HostedAttempts > 0 {
			tier = "go"
		}
		out.Incidents = append(out.Incidents, recoveryStatusIncident{
			ID:             incident.ID,
			Class:          "validation",
			Rig:            incident.Rig,
			WorkUnit:       incident.SourceIssue,
			Tier:           tier,
			Status:         incident.Status,
			LeaseOwner:     "deacon/validation-recovery",
			LocalAttempts:  incident.LocalAttempts,
			HostedAttempts: incident.HostedAttempts,
			Evidence:       incident.LastEvent.Summary,
			Terminal:       incident.Status == "resolved" || incident.Status == "escalated",
		})
	}

	watchdogPath := filepath.Join(townRoot, "deacon", "lmstudio-watchdog.json")
	if data, readErr := os.ReadFile(watchdogPath); readErr == nil {
		var state map[string]any
		if json.Unmarshal(data, &state) == nil {
			out.Infrastructure = state
		}
	}

	sort.Slice(out.Incidents, func(i, j int) bool {
		if out.Incidents[i].Class != out.Incidents[j].Class {
			return out.Incidents[i].Class < out.Incidents[j].Class
		}
		return out.Incidents[i].ID < out.Incidents[j].ID
	})

	if recoveryJSON {
		data, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}
	if len(out.Incidents) == 0 {
		fmt.Println("No recovery incidents recorded")
	} else {
		for _, incident := range out.Incidents {
			fmt.Printf("%s  class=%s tier=%s status=%s", incident.ID, incident.Class, incident.Tier, incident.Status)
			if incident.WorkUnit != "" {
				fmt.Printf(" work=%s", incident.WorkUnit)
			}
			if incident.NextActionAt.IsZero() {
				fmt.Println()
			} else {
				fmt.Printf(" next=%s\n", incident.NextActionAt.Format(time.RFC3339))
			}
		}
	}
	if status, ok := out.Infrastructure["status"].(string); ok {
		fmt.Printf("infrastructure  status=%s\n", status)
	}
	return nil
}
