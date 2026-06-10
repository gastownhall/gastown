package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/constants"
	deaconmgr "github.com/steveyegge/gastown/internal/deacon"
	"github.com/steveyegge/gastown/internal/refinery"
	rigpkg "github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/util"
	"github.com/steveyegge/gastown/internal/witness"
	"github.com/steveyegge/gastown/internal/workspace"
)

const (
	defaultPTNRig                 = "ptn_from_scratch"
	defaultPTNTarget              = 9
	defaultPTNMaxAttempts         = 3
	defaultPTNInterval            = 3 * time.Minute
	defaultPTNNoPushThreshold     = 45 * time.Minute
	defaultPTNEscalationCooldown  = 30 * time.Minute
	ptnControllerCommandTimeout   = 5 * time.Minute
	ptnControllerPatrolTimeout    = 2 * time.Minute
	ptnControllerEscalateTimeout  = 90 * time.Second
	ptnControllerSchedulerTimeout = 5 * time.Minute
)

var (
	ptnControllerRig                string
	ptnControllerTarget             int
	ptnControllerLoop               bool
	ptnControllerInterval           time.Duration
	ptnControllerMaxAttempts        int
	ptnControllerNoPushThreshold    time.Duration
	ptnControllerEscalationCooldown time.Duration
	ptnControllerDryRun             bool
	ptnControllerJSON               bool
)

var deaconPTNControllerCmd = &cobra.Command{
	Use:     "ptn-controller",
	Aliases: []string{"ptn-control"},
	Short:   "Enforce the PTN production desired state",
	Long: `Enforce the PTN production desired state.

The controller verifies that the control plane is alive, classifies PTN polecat
lanes by issue and agent state, nudges merge-queue processing, refills missing
productive lanes, and escalates when drift survives bounded retries.

One-shot mode is intended for daemon heartbeat use. Use --loop for a foreground
controller while debugging.`,
	RunE: runDeaconPTNController,
}

func init() {
	deaconPTNControllerCmd.Flags().StringVar(&ptnControllerRig, "rig", defaultPTNRig, "Rig to control")
	deaconPTNControllerCmd.Flags().IntVar(&ptnControllerTarget, "target", defaultPTNTarget, "Desired productive polecat lanes")
	deaconPTNControllerCmd.Flags().BoolVar(&ptnControllerLoop, "loop", false, "Run continuously instead of once")
	deaconPTNControllerCmd.Flags().DurationVar(&ptnControllerInterval, "interval", defaultPTNInterval, "Loop interval")
	deaconPTNControllerCmd.Flags().IntVar(&ptnControllerMaxAttempts, "max-attempts", defaultPTNMaxAttempts, "Consecutive drift cycles before escalation")
	deaconPTNControllerCmd.Flags().DurationVar(&ptnControllerNoPushThreshold, "no-push-threshold", defaultPTNNoPushThreshold, "Maximum branch staleness while ready work or open MRs exist; 0 disables")
	deaconPTNControllerCmd.Flags().DurationVar(&ptnControllerEscalationCooldown, "escalate-cooldown", defaultPTNEscalationCooldown, "Minimum time between repeated escalations")
	deaconPTNControllerCmd.Flags().BoolVar(&ptnControllerDryRun, "dry-run", false, "Report intended actions without mutating")
	deaconPTNControllerCmd.Flags().BoolVar(&ptnControllerJSON, "json", false, "Output JSON")
	deaconCmd.AddCommand(deaconPTNControllerCmd)
}

type ptnControllerConfig struct {
	RigName          string
	TargetProductive int
	MaxAttempts      int
	NoPushThreshold  time.Duration
	EscalateCooldown time.Duration
	DryRun           bool
	JSON             bool
	Loop             bool
	Interval         time.Duration
}

type ptnControllerReport struct {
	Rig              string                      `json:"rig"`
	TargetProductive int                         `json:"target_productive"`
	ProductiveLanes  int                         `json:"productive_lanes"`
	ReadyWork        int                         `json:"ready_work"`
	OpenMRs          int                         `json:"open_mrs"`
	Services         map[string]ptnServiceStatus `json:"services"`
	Lanes            []ptnLaneStatus             `json:"lanes"`
	Actions          []string                    `json:"actions,omitempty"`
	Errors           []string                    `json:"errors,omitempty"`
	Drift            bool                        `json:"drift"`
	Escalated        bool                        `json:"escalated,omitempty"`
	NoPush           ptnNoPushStatus             `json:"no_push"`
	State            ptnControllerState          `json:"state"`
}

type ptnServiceStatus struct {
	Running bool   `json:"running"`
	Started bool   `json:"started,omitempty"`
	Error   string `json:"error,omitempty"`
}

type ptnLaneInput struct {
	Name          string
	AgentID       string
	Session       string
	HookBead      string
	AgentState    string
	IssueStatus   string
	SessionStatus string
}

type ptnLaneStatus struct {
	Name          string   `json:"name"`
	AgentID       string   `json:"agent_id,omitempty"`
	Session       string   `json:"session,omitempty"`
	HookBead      string   `json:"hook_bead,omitempty"`
	AgentState    string   `json:"agent_state,omitempty"`
	IssueStatus   string   `json:"issue_status,omitempty"`
	SessionStatus string   `json:"session_status,omitempty"`
	Productive    bool     `json:"productive"`
	Reasons       []string `json:"reasons,omitempty"`
}

type ptnLaneSummary struct {
	Productive int
	Lanes      []ptnLaneStatus
}

type ptnNoPushStatus struct {
	Enabled      bool   `json:"enabled"`
	Backlog      bool   `json:"backlog"`
	Branch       string `json:"branch,omitempty"`
	LastCommitAt string `json:"last_commit_at,omitempty"`
	Age          string `json:"age,omitempty"`
	Threshold    string `json:"threshold,omitempty"`
	Stale        bool   `json:"stale"`
	Error        string `json:"error,omitempty"`
}

type ptnControllerState struct {
	Rig                    string    `json:"rig"`
	ConsecutiveDrift       int       `json:"consecutive_drift"`
	LastDriftAt            time.Time `json:"last_drift_at,omitempty"`
	LastHealthyAt          time.Time `json:"last_healthy_at,omitempty"`
	LastEscalationAt       time.Time `json:"last_escalation_at,omitempty"`
	LastNoPushEscalationAt time.Time `json:"last_no_push_escalation_at,omitempty"`
}

func runDeaconPTNController(cmd *cobra.Command, args []string) error {
	cfg := ptnControllerConfig{
		RigName:          ptnControllerRig,
		TargetProductive: ptnControllerTarget,
		MaxAttempts:      ptnControllerMaxAttempts,
		NoPushThreshold:  ptnControllerNoPushThreshold,
		EscalateCooldown: ptnControllerEscalationCooldown,
		DryRun:           ptnControllerDryRun,
		JSON:             ptnControllerJSON,
		Loop:             ptnControllerLoop,
		Interval:         ptnControllerInterval,
	}
	if cfg.RigName == "" {
		return fmt.Errorf("--rig is required")
	}
	if cfg.TargetProductive < 1 {
		return fmt.Errorf("--target must be positive")
	}
	if cfg.MaxAttempts < 1 {
		return fmt.Errorf("--max-attempts must be positive")
	}
	if cfg.Interval <= 0 {
		return fmt.Errorf("--interval must be positive")
	}
	if cfg.EscalateCooldown < 0 {
		return fmt.Errorf("--escalate-cooldown cannot be negative")
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	for {
		report, err := runPTNControllerCycle(ctx, cfg)
		if printErr := printPTNControllerReport(report, cfg.JSON); printErr != nil && err == nil {
			err = printErr
		}
		if !cfg.Loop {
			return err
		}
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "ptn-controller cycle failed: %v\n", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(cfg.Interval):
		}
	}
}

func runPTNControllerCycle(ctx context.Context, cfg ptnControllerConfig) (*ptnControllerReport, error) {
	now := time.Now()
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return nil, err
	}
	if err := session.InitRegistry(townRoot); err != nil {
		return nil, fmt.Errorf("initializing session registry: %w", err)
	}
	_, r, err := getRig(cfg.RigName)
	if err != nil {
		return nil, err
	}

	report := &ptnControllerReport{
		Rig:              cfg.RigName,
		TargetProductive: cfg.TargetProductive,
		Services:         make(map[string]ptnServiceStatus),
	}

	state, err := loadPTNControllerState(townRoot, cfg.RigName)
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("load state: %v", err))
	}

	services := ensurePTNServices(townRoot, r, cfg.DryRun)
	report.Services = services.status
	report.Actions = append(report.Actions, services.actions...)
	report.Errors = append(report.Errors, services.errors...)

	if !cfg.DryRun {
		if out, err := runPTNCommand(ctx, townRoot, ptnControllerPatrolTimeout, "patrol", "scan", "--rig", cfg.RigName, "--json"); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("patrol scan: %v: %s", err, trimCommandOutput(out)))
		} else if strings.TrimSpace(out) != "" {
			report.Actions = append(report.Actions, "patrol scan completed")
		}
	} else {
		report.Actions = append(report.Actions, "would run patrol scan")
	}

	ready, err := ptnReadyWork(townRoot, r)
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("ready work: %v", err))
	}
	report.ReadyWork = len(ready)

	openMRs, err := ptnOpenMergeRequests(r)
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("open MRs: %v", err))
	}
	report.OpenMRs = openMRs
	if openMRs > 0 {
		if cfg.DryRun {
			report.Actions = append(report.Actions, fmt.Sprintf("would nudge refinery for %d open MR(s)", openMRs))
		} else {
			nudgeRefinery(cfg.RigName, fmt.Sprintf("PTN_CONTROLLER: %d open MR(s), drain merge queue", openMRs))
			report.Actions = append(report.Actions, fmt.Sprintf("nudged refinery for %d open MR(s)", openMRs))
		}
	}

	laneInputs, err := scanPTNLanes(r)
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("scan lanes: %v", err))
	}
	laneSummary := classifyPTNLanes(laneInputs)
	report.ProductiveLanes = laneSummary.Productive
	report.Lanes = laneSummary.Lanes

	recoveryIssues := ptnRecoveryIssueIDs(laneSummary.Lanes)
	if len(recoveryIssues) > 0 {
		report.Actions = append(report.Actions, recoverPTNLanes(ctx, townRoot, cfg, recoveryIssues)...)
	}

	deficit := cfg.TargetProductive - report.ProductiveLanes
	if deficit > 0 && len(ready) > 0 {
		report.Actions = append(report.Actions, refillPTNLanes(ctx, townRoot, r, cfg, ready, deficit)...)
	}

	report.NoPush = checkPTNNoPush(ctx, r, cfg.NoPushThreshold, report.ReadyWork, report.OpenMRs, now)
	if report.NoPush.Error != "" {
		report.Errors = append(report.Errors, "no-push watchdog: "+report.NoPush.Error)
	}
	if report.NoPush.Stale {
		if cfg.DryRun {
			report.Actions = append(report.Actions, "would trigger no-push recovery")
		} else {
			nudgeRefinery(cfg.RigName, "PTN_CONTROLLER: no-push watchdog fired, drain queue and push integrated work")
			if out, err := runPTNCommand(ctx, townRoot, ptnControllerSchedulerTimeout, "scheduler", "run", "--batch", fmt.Sprintf("%d", maxInt(1, deficit))); err != nil {
				report.Errors = append(report.Errors, fmt.Sprintf("no-push scheduler recovery: %v: %s", err, trimCommandOutput(out)))
			} else {
				report.Actions = append(report.Actions, "triggered no-push scheduler recovery")
			}
		}
	}

	report.Drift = ptnDesiredStateDrift(report)
	nextState := updatePTNControllerState(state, cfg.RigName, now, report.Drift)
	report.State = nextState

	if report.NoPush.Stale && shouldEscalatePTNNoPush(nextState, now, cfg.EscalateCooldown) {
		msg := ptnNoPushEscalationMessage(report)
		if cfg.DryRun {
			report.Actions = append(report.Actions, "would escalate no-push watchdog")
		} else if err := escalatePTN(ctx, townRoot, msg); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("escalate no-push: %v", err))
		} else {
			nextState.LastNoPushEscalationAt = now
			report.Escalated = true
			report.Actions = append(report.Actions, "escalated no-push watchdog")
		}
	}

	if report.Drift && shouldEscalatePTNDrift(nextState, now, cfg.MaxAttempts, cfg.EscalateCooldown) {
		msg := ptnDriftEscalationMessage(report, cfg)
		if cfg.DryRun {
			report.Actions = append(report.Actions, "would escalate persistent desired-state drift")
		} else if err := escalatePTN(ctx, townRoot, msg); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("escalate drift: %v", err))
		} else {
			nextState.LastEscalationAt = now
			report.Escalated = true
			report.Actions = append(report.Actions, "escalated persistent desired-state drift")
		}
	}

	report.State = nextState
	if err := savePTNControllerState(townRoot, report.State); err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("save state: %v", err))
	}

	return report, nil
}

type ptnServiceResult struct {
	status  map[string]ptnServiceStatus
	actions []string
	errors  []string
}

func ensurePTNServices(townRoot string, r *rigpkg.Rig, dryRun bool) ptnServiceResult {
	result := ptnServiceResult{status: make(map[string]ptnServiceStatus)}

	deaconStatus := ptnServiceStatus{}
	deaconManager := deaconmgr.NewManager(townRoot)
	deaconRunning, deaconErr := deaconManager.IsRunning()
	if deaconErr != nil {
		deaconStatus.Error = deaconErr.Error()
		result.errors = append(result.errors, "deacon status: "+deaconErr.Error())
	}
	deaconStatus.Running = deaconRunning
	if !deaconRunning {
		if dryRun {
			result.actions = append(result.actions, "would start deacon")
		} else if err := deaconManager.Start(""); errors.Is(err, deaconmgr.ErrAlreadyRunning) {
			deaconStatus.Running = true
		} else if err != nil {
			deaconStatus.Error = err.Error()
			result.errors = append(result.errors, "start deacon: "+err.Error())
		} else {
			deaconStatus.Running = true
			deaconStatus.Started = true
			result.actions = append(result.actions, "started deacon")
		}
	}
	result.status["deacon"] = deaconStatus

	witnessStatus := ptnServiceStatus{}
	witnessManager := witness.NewManager(r)
	witnessRunning, witnessErr := witnessManager.IsRunning()
	if witnessErr != nil {
		witnessStatus.Error = witnessErr.Error()
		result.errors = append(result.errors, "witness status: "+witnessErr.Error())
	}
	witnessStatus.Running = witnessRunning
	if !witnessRunning {
		if dryRun {
			result.actions = append(result.actions, "would start witness")
		} else if err := witnessManager.Start(false, "", nil); errors.Is(err, witness.ErrAlreadyRunning) {
			witnessStatus.Running = true
		} else if err != nil {
			witnessStatus.Error = err.Error()
			result.errors = append(result.errors, "start witness: "+err.Error())
		} else {
			witnessStatus.Running = true
			witnessStatus.Started = true
			result.actions = append(result.actions, "started witness")
		}
	}
	result.status["witness"] = witnessStatus

	refineryStatus := ptnServiceStatus{}
	refineryManager := refinery.NewManager(r)
	refineryManager.SetOutput(io.Discard)
	refineryRunning, refineryErr := refineryManager.IsRunning()
	if refineryErr != nil {
		refineryStatus.Error = refineryErr.Error()
		result.errors = append(result.errors, "refinery status: "+refineryErr.Error())
	}
	refineryStatus.Running = refineryRunning
	if !refineryRunning {
		if dryRun {
			result.actions = append(result.actions, "would start refinery")
		} else if err := refineryManager.Start(false, ""); errors.Is(err, refinery.ErrAlreadyRunning) {
			refineryStatus.Running = true
		} else if err != nil {
			refineryStatus.Error = err.Error()
			result.errors = append(result.errors, "start refinery: "+err.Error())
		} else {
			refineryStatus.Running = true
			refineryStatus.Started = true
			result.actions = append(result.actions, "started refinery")
		}
	}
	result.status["refinery"] = refineryStatus

	return result
}

func ptnReadyWork(townRoot string, r *rigpkg.Rig) ([]*beads.Issue, error) {
	bd := beads.New(r.Path)
	ready, err := bd.Ready()
	if err != nil {
		return nil, err
	}
	ready = filterReadyIssuesByRoute(townRoot, r.Name, ready)
	filtered := ready[:0]
	for _, issue := range ready {
		if issue == nil || issue.ID == "" || issue.Status == "closed" {
			continue
		}
		if beads.IsAgentBead(issue) || strings.Contains(issue.ID, "-wisp-") {
			continue
		}
		filtered = append(filtered, issue)
	}
	return filtered, nil
}

func ptnOpenMergeRequests(r *rigpkg.Rig) (int, error) {
	bd := beads.New(r.Path)
	mrs, err := bd.ListMergeRequests(beads.ListOptions{
		Status:   "open",
		Label:    "gt:merge-request",
		Priority: -1,
	})
	if err != nil {
		return 0, err
	}
	return len(mrs), nil
}

func scanPTNLanes(r *rigpkg.Rig) ([]ptnLaneInput, error) {
	bd := beads.New(r.Path)
	agentBeads, err := bd.ListAgentBeads()
	if err != nil {
		return nil, err
	}

	t := tmux.NewTmux()
	issueStatuses := make(map[string]string)
	inputsByName := make(map[string]ptnLaneInput)
	var hooks []string

	for id, issue := range agentBeads {
		beadRig, role, name, ok := beads.ParseAgentBeadID(id)
		if !ok || role != constants.RolePolecat || beadRig != r.Name || name == "" {
			continue
		}
		if issue != nil && issue.Status == "closed" {
			continue
		}
		fields := beads.ParseAgentFields("")
		if issue != nil {
			fields = beads.ParseAgentFields(issue.Description)
		}
		hook := ""
		state := ""
		if issue != nil {
			hook = issue.HookBead
			state = issue.AgentState
		}
		if hook == "" && fields != nil {
			hook = fields.HookBead
		}
		if state == "" && fields != nil {
			state = fields.AgentState
		}
		if hook != "" {
			hooks = append(hooks, hook)
		}
		sessionName := session.PolecatSessionName(session.PrefixFor(r.Name), name)
		inputsByName[name] = ptnLaneInput{
			Name:          name,
			AgentID:       id,
			Session:       sessionName,
			HookBead:      hook,
			AgentState:    state,
			SessionStatus: t.CheckSessionHealth(sessionName, 0).String(),
		}
	}

	for _, hook := range ptnUniqueStrings(hooks) {
		issue, err := bd.Show(hook)
		if err != nil || issue == nil {
			issueStatuses[hook] = "missing"
			continue
		}
		issueStatuses[hook] = strings.ToLower(strings.TrimSpace(issue.Status))
	}
	for name, input := range inputsByName {
		if input.HookBead != "" {
			input.IssueStatus = issueStatuses[input.HookBead]
		}
		inputsByName[name] = input
	}

	polecatsDir := filepath.Join(r.Path, "polecats")
	if entries, err := os.ReadDir(polecatsDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			name := entry.Name()
			if _, exists := inputsByName[name]; exists {
				continue
			}
			sessionName := session.PolecatSessionName(session.PrefixFor(r.Name), name)
			inputsByName[name] = ptnLaneInput{
				Name:          name,
				Session:       sessionName,
				AgentState:    "unknown",
				SessionStatus: t.CheckSessionHealth(sessionName, 0).String(),
			}
		}
	}

	names := make([]string, 0, len(inputsByName))
	for name := range inputsByName {
		names = append(names, name)
	}
	sort.Strings(names)
	inputs := make([]ptnLaneInput, 0, len(names))
	for _, name := range names {
		inputs = append(inputs, inputsByName[name])
	}
	return inputs, nil
}

func classifyPTNLanes(inputs []ptnLaneInput) ptnLaneSummary {
	hookCounts := make(map[string]int)
	for _, input := range inputs {
		if input.HookBead != "" {
			hookCounts[input.HookBead]++
		}
	}

	summary := ptnLaneSummary{Lanes: make([]ptnLaneStatus, 0, len(inputs))}
	for _, input := range inputs {
		status := ptnLaneStatus{
			Name:          input.Name,
			AgentID:       input.AgentID,
			Session:       input.Session,
			HookBead:      input.HookBead,
			AgentState:    strings.ToLower(strings.TrimSpace(input.AgentState)),
			IssueStatus:   strings.ToLower(strings.TrimSpace(input.IssueStatus)),
			SessionStatus: strings.ToLower(strings.TrimSpace(input.SessionStatus)),
		}
		if status.SessionStatus == "" {
			status.SessionStatus = tmux.SessionDead.String()
		}
		status.Reasons = ptnLaneUnhealthyReasons(status, hookCounts)
		status.Productive = len(status.Reasons) == 0
		if status.Productive {
			summary.Productive++
		}
		summary.Lanes = append(summary.Lanes, status)
	}
	return summary
}

func ptnLaneUnhealthyReasons(status ptnLaneStatus, hookCounts map[string]int) []string {
	var reasons []string
	if status.HookBead == "" {
		reasons = append(reasons, "no-issue")
	} else if hookCounts[status.HookBead] > 1 {
		reasons = append(reasons, "duplicate-hook")
	}

	switch status.SessionStatus {
	case "", tmux.SessionHealthy.String():
	default:
		reasons = append(reasons, status.SessionStatus)
	}

	if !ptnAgentStateProductive(status.AgentState) {
		reasons = append(reasons, ptnAgentStateReason(status.AgentState))
	}
	if status.HookBead != "" && !ptnIssueStatusLive(status.IssueStatus) {
		reasons = append(reasons, "non-live-issue")
	}
	return ptnUniqueStrings(reasons)
}

func ptnAgentStateProductive(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "working", "running":
		return true
	default:
		return false
	}
}

func ptnAgentStateReason(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "":
		return "unknown-state"
	case "review_needed":
		return "review-needed"
	default:
		return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(state)), "_", "-")
	}
}

func ptnIssueStatusLive(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "open", "hooked", "in_progress", "in-progress":
		return true
	default:
		return false
	}
}

func ptnRecoveryIssueIDs(lanes []ptnLaneStatus) []string {
	seen := make(map[string]bool)
	var ids []string
	for _, lane := range lanes {
		if lane.HookBead == "" || lane.IssueStatus == "closed" || lane.IssueStatus == "tombstone" {
			continue
		}
		if !ptnLaneRecoverable(lane) {
			continue
		}
		if !seen[lane.HookBead] {
			seen[lane.HookBead] = true
			ids = append(ids, lane.HookBead)
		}
	}
	sort.Strings(ids)
	return ids
}

func ptnLaneRecoverable(lane ptnLaneStatus) bool {
	for _, reason := range lane.Reasons {
		switch reason {
		case "duplicate-hook", tmux.SessionDead.String(), tmux.AgentDead.String(), tmux.AgentHung.String(), "stalled", "stuck":
			return true
		}
	}
	return false
}

func recoverPTNLanes(ctx context.Context, townRoot string, cfg ptnControllerConfig, issueIDs []string) []string {
	var actions []string
	for _, issueID := range issueIDs {
		if cfg.DryRun {
			actions = append(actions, fmt.Sprintf("would force re-sling %s", issueID))
			continue
		}
		out, err := runPTNCommand(ctx, townRoot, ptnControllerCommandTimeout, "sling", issueID, cfg.RigName, "--force")
		if err != nil {
			actions = append(actions, fmt.Sprintf("force re-sling %s failed: %v: %s", issueID, err, trimCommandOutput(out)))
			continue
		}
		actions = append(actions, fmt.Sprintf("force re-slung %s", issueID))
	}
	return actions
}

func refillPTNLanes(ctx context.Context, townRoot string, r *rigpkg.Rig, cfg ptnControllerConfig, ready []*beads.Issue, deficit int) []string {
	var actions []string
	if deficit <= 0 || len(ready) == 0 {
		return actions
	}
	batch := minInt(deficit, len(ready))
	if cfg.DryRun {
		actions = append(actions, fmt.Sprintf("would run scheduler for %d lane(s)", batch))
		for i := 0; i < batch; i++ {
			actions = append(actions, fmt.Sprintf("would sling %s", ready[i].ID))
		}
		return actions
	}

	out, err := runPTNCommand(ctx, townRoot, ptnControllerSchedulerTimeout, "scheduler", "run", "--batch", fmt.Sprintf("%d", batch))
	if err != nil {
		actions = append(actions, fmt.Sprintf("scheduler refill failed: %v: %s", err, trimCommandOutput(out)))
	} else {
		actions = append(actions, fmt.Sprintf("ran scheduler refill batch=%d", batch))
	}

	freshReady, err := ptnReadyWork(townRoot, r)
	if err != nil {
		actions = append(actions, fmt.Sprintf("ready reload after scheduler failed: %v", err))
		freshReady = ready
	}
	for i := 0; i < minInt(batch, len(freshReady)); i++ {
		issue := freshReady[i]
		if issue == nil || issue.ID == "" {
			continue
		}
		out, err := runPTNCommand(ctx, townRoot, ptnControllerCommandTimeout, "sling", issue.ID, cfg.RigName)
		if err != nil {
			actions = append(actions, fmt.Sprintf("direct sling %s failed: %v: %s", issue.ID, err, trimCommandOutput(out)))
			continue
		}
		actions = append(actions, fmt.Sprintf("direct slung %s", issue.ID))
	}
	return actions
}

func checkPTNNoPush(ctx context.Context, r *rigpkg.Rig, threshold time.Duration, readyWork, openMRs int, now time.Time) ptnNoPushStatus {
	status := ptnNoPushStatus{
		Enabled:   threshold > 0,
		Backlog:   readyWork+openMRs > 0,
		Threshold: threshold.String(),
	}
	if threshold <= 0 || !status.Backlog {
		return status
	}

	worktree, err := ptnGitWorktree(r.Path)
	if err != nil {
		status.Error = err.Error()
		return status
	}
	branch, lastCommit, err := ptnLastIntegrationCommit(ctx, worktree, r.DefaultBranch())
	if err != nil {
		status.Error = err.Error()
		return status
	}
	status.Branch = branch
	status.LastCommitAt = lastCommit.Format(time.RFC3339)
	age := now.Sub(lastCommit)
	if age < 0 {
		age = 0
	}
	status.Age = age.Round(time.Second).String()
	status.Stale = ptnNoPushDrift(now, lastCommit, threshold, readyWork, openMRs)
	return status
}

func ptnGitWorktree(rigPath string) (string, error) {
	candidates := []string{
		filepath.Join(rigPath, "mayor", "rig"),
		filepath.Join(rigPath, "refinery", "rig"),
		rigPath,
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		cmd := exec.Command("git", "-C", candidate, "rev-parse", "--is-inside-work-tree")
		util.SetDetachedProcessGroup(cmd)
		if out, err := cmd.Output(); err == nil && strings.TrimSpace(string(out)) == "true" {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no git worktree found for rig %s", rigPath)
}

func ptnLastIntegrationCommit(ctx context.Context, worktree, defaultBranch string) (string, time.Time, error) {
	candidates := []string{}
	for _, branch := range []string{defaultBranch, "master", "main"} {
		branch = strings.TrimSpace(branch)
		if branch == "" {
			continue
		}
		candidates = append(candidates, "refs/heads/"+branch, "refs/remotes/origin/"+branch)
	}
	candidates = ptnUniqueStrings(candidates)
	for _, ref := range candidates {
		cmd := exec.CommandContext(ctx, "git", "-C", worktree, "log", "-1", "--format=%ct", ref)
		util.SetDetachedProcessGroup(cmd)
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		epoch, err := parseUnixSeconds(strings.TrimSpace(string(out)))
		if err != nil {
			continue
		}
		return strings.TrimPrefix(strings.TrimPrefix(ref, "refs/heads/"), "refs/remotes/"), time.Unix(epoch, 0), nil
	}
	return "", time.Time{}, fmt.Errorf("no integration branch ref found in %s", worktree)
}

func parseUnixSeconds(s string) (int64, error) {
	var n int64
	if s == "" {
		return 0, fmt.Errorf("empty unix timestamp")
	}
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0, fmt.Errorf("invalid unix timestamp %q", s)
		}
		n = n*10 + int64(ch-'0')
	}
	return n, nil
}

func ptnNoPushDrift(now, lastCommit time.Time, threshold time.Duration, readyWork, openMRs int) bool {
	if threshold <= 0 || readyWork+openMRs == 0 || lastCommit.IsZero() {
		return false
	}
	return now.Sub(lastCommit) > threshold
}

func ptnDesiredStateDrift(report *ptnControllerReport) bool {
	if report == nil {
		return false
	}
	for _, svc := range report.Services {
		if svc.Error != "" || svc.Started || !svc.Running {
			return true
		}
	}
	if report.NoPush.Stale {
		return true
	}
	for _, lane := range report.Lanes {
		if lane.HookBead != "" && !lane.Productive {
			return true
		}
	}
	return report.ReadyWork > 0 && report.ProductiveLanes < report.TargetProductive
}

func updatePTNControllerState(prev ptnControllerState, rig string, now time.Time, drift bool) ptnControllerState {
	next := prev
	if next.Rig != rig {
		next = ptnControllerState{Rig: rig}
	}
	if drift {
		next.ConsecutiveDrift++
		next.LastDriftAt = now
		return next
	}
	next.ConsecutiveDrift = 0
	next.LastHealthyAt = now
	return next
}

func shouldEscalatePTNDrift(state ptnControllerState, now time.Time, maxAttempts int, cooldown time.Duration) bool {
	if maxAttempts < 1 || state.ConsecutiveDrift < maxAttempts {
		return false
	}
	return cooldownElapsed(state.LastEscalationAt, now, cooldown)
}

func shouldEscalatePTNNoPush(state ptnControllerState, now time.Time, cooldown time.Duration) bool {
	return cooldownElapsed(state.LastNoPushEscalationAt, now, cooldown)
}

func cooldownElapsed(last, now time.Time, cooldown time.Duration) bool {
	if last.IsZero() || cooldown <= 0 {
		return true
	}
	return now.Sub(last) >= cooldown
}

func loadPTNControllerState(townRoot, rig string) (ptnControllerState, error) {
	path := ptnControllerStatePath(townRoot)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ptnControllerState{Rig: rig}, nil
	}
	if err != nil {
		return ptnControllerState{Rig: rig}, err
	}
	var state ptnControllerState
	if err := json.Unmarshal(data, &state); err != nil {
		return ptnControllerState{Rig: rig}, err
	}
	if state.Rig != rig {
		return ptnControllerState{Rig: rig}, nil
	}
	return state, nil
}

func savePTNControllerState(townRoot string, state ptnControllerState) error {
	path := ptnControllerStatePath(townRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func ptnControllerStatePath(townRoot string) string {
	return filepath.Join(townRoot, "deacon", "ptn-controller-state.json")
}

func runPTNCommand(ctx context.Context, townRoot string, timeout time.Duration, args ...string) (string, error) {
	if timeout <= 0 {
		timeout = ptnControllerCommandTimeout
	}
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, "gt", args...)
	util.SetDetachedProcessGroup(cmd)
	cmd.Dir = townRoot
	cmd.Env = ptnCommandEnv(townRoot)
	out, err := cmd.CombinedOutput()
	if cmdCtx.Err() == context.DeadlineExceeded {
		return string(out), fmt.Errorf("timed out after %s", timeout)
	}
	return string(out), err
}

func ptnCommandEnv(townRoot string) []string {
	env := beads.BuildMutationRoutingBDEnv(os.Environ(), filepath.Join(townRoot, ".beads"))
	env = withoutEnvKeys(env, "BD_ACTOR", "GT_DAEMON")
	env = append(env, "BD_ACTOR=deacon/ptn-controller", "GT_DAEMON=1")
	return env
}

func withoutEnvKeys(env []string, keys ...string) []string {
	skip := make(map[string]bool, len(keys))
	for _, key := range keys {
		skip[key+"="] = true
	}
	filtered := env[:0]
	for _, entry := range env {
		omit := false
		for prefix := range skip {
			if strings.HasPrefix(entry, prefix) {
				omit = true
				break
			}
		}
		if !omit {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func escalatePTN(ctx context.Context, townRoot, message string) error {
	cmdCtx, cancel := context.WithTimeout(ctx, ptnControllerEscalateTimeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, "gt", "escalate", "-s", "HIGH", message)
	util.SetDetachedProcessGroup(cmd)
	cmd.Dir = townRoot
	cmd.Env = ptnCommandEnv(townRoot)
	out, err := cmd.CombinedOutput()
	if cmdCtx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("timed out after %s", ptnControllerEscalateTimeout)
	}
	if err != nil {
		return fmt.Errorf("%w: %s", err, trimCommandOutput(string(out)))
	}
	return nil
}

func ptnDriftEscalationMessage(report *ptnControllerReport, cfg ptnControllerConfig) string {
	return fmt.Sprintf("PTN desired-state drift: rig=%s productive=%d target=%d ready=%d open_mrs=%d consecutive_drift=%d errors=%s unhealthy=%s",
		report.Rig,
		report.ProductiveLanes,
		cfg.TargetProductive,
		report.ReadyWork,
		report.OpenMRs,
		report.State.ConsecutiveDrift,
		strings.Join(report.Errors, "; "),
		strings.Join(ptnUnhealthyLaneSummaries(report.Lanes), "; "))
}

func ptnNoPushEscalationMessage(report *ptnControllerReport) string {
	return fmt.Sprintf("PTN no-push watchdog: rig=%s branch=%s age=%s threshold=%s ready=%d open_mrs=%d last_commit=%s",
		report.Rig,
		report.NoPush.Branch,
		report.NoPush.Age,
		report.NoPush.Threshold,
		report.ReadyWork,
		report.OpenMRs,
		report.NoPush.LastCommitAt)
}

func ptnUnhealthyLaneSummaries(lanes []ptnLaneStatus) []string {
	var summaries []string
	for _, lane := range lanes {
		if lane.Productive {
			continue
		}
		summaries = append(summaries, fmt.Sprintf("%s hook=%s state=%s session=%s reasons=%s",
			lane.Name, lane.HookBead, lane.AgentState, lane.SessionStatus, strings.Join(lane.Reasons, ",")))
	}
	sort.Strings(summaries)
	return summaries
}

func printPTNControllerReport(report *ptnControllerReport, asJSON bool) error {
	if report == nil {
		return nil
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	fmt.Printf("PTN controller: rig=%s productive=%d/%d ready=%d open_mrs=%d drift=%t\n",
		report.Rig, report.ProductiveLanes, report.TargetProductive, report.ReadyWork, report.OpenMRs, report.Drift)
	if report.NoPush.Stale {
		fmt.Printf("  no-push stale: branch=%s age=%s threshold=%s\n", report.NoPush.Branch, report.NoPush.Age, report.NoPush.Threshold)
	}
	for name, svc := range report.Services {
		state := "running"
		if !svc.Running {
			state = "down"
		}
		if svc.Started {
			state = "started"
		}
		if svc.Error != "" {
			state = "error: " + svc.Error
		}
		fmt.Printf("  service %s: %s\n", name, state)
	}
	for _, lane := range report.Lanes {
		if lane.Productive {
			continue
		}
		fmt.Printf("  unhealthy lane %s hook=%s state=%s session=%s reasons=%s\n",
			lane.Name, lane.HookBead, lane.AgentState, lane.SessionStatus, strings.Join(lane.Reasons, ","))
	}
	for _, action := range report.Actions {
		fmt.Printf("  action: %s\n", action)
	}
	for _, errText := range report.Errors {
		fmt.Printf("  error: %s\n", errText)
	}
	return nil
}

func ptnUniqueStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func trimCommandOutput(out string) string {
	out = strings.TrimSpace(out)
	if len(out) <= 500 {
		return out
	}
	return out[:500] + "..."
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
