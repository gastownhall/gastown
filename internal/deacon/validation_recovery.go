package deacon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/steveyegge/gastown/internal/atomicfile"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/util"
)

const (
	validationStateVersion = 1
	maxValidationEvidence  = 32 * 1024

	// ValidationFailedEventType identifies durable validation recovery events.
	ValidationFailedEventType = "VALIDATION_FAILED"
)

// ValidationRecoveryConfig controls the hosted relief valve for confirmed
// quality failures. It lives under validation_failure in model-escalation.json.
type ValidationRecoveryConfig struct {
	Enabled           bool   `json:"enabled"`
	LocalAgent        string `json:"local_agent,omitempty"`
	MaxLocalAttempts  int    `json:"max_local_attempts,omitempty"`
	ToAgent           string `json:"to_agent"`
	MaxHostedAttempts int    `json:"max_hosted_attempts"`
	RepairPriority    int    `json:"repair_priority"`
}

// ValidationFailure is the durable event emitted after a quality failure has
// been confirmed as a regression rather than flaky, pre-existing, or infra.
type ValidationFailure struct {
	Type         string    `json:"type"`
	IncidentID   string    `json:"incident_id,omitempty"`
	Rig          string    `json:"rig"`
	SourceIssue  string    `json:"source_issue,omitempty"`
	MergeRequest string    `json:"merge_request,omitempty"`
	Branch       string    `json:"branch,omitempty"`
	Commit       string    `json:"commit,omitempty"`
	Phase        string    `json:"phase"` // pre-merge or post-merge
	Kind         string    `json:"kind"`  // functional, test, build, lint, typecheck
	Command      string    `json:"command,omitempty"`
	ExitCode     int       `json:"exit_code,omitempty"`
	Summary      string    `json:"summary"`
	Evidence     string    `json:"evidence,omitempty"`
	ReportedAt   time.Time `json:"reported_at"`
}

// ValidationObservation records a distinct failing result within an incident.
type ValidationObservation struct {
	Fingerprint string    `json:"fingerprint"`
	Commit      string    `json:"commit,omitempty"`
	Kind        string    `json:"kind"`
	ExitCode    int       `json:"exit_code,omitempty"`
	ObservedAt  time.Time `json:"observed_at"`
}

// ValidationIncident tracks one local failure and its bounded hosted repair.
type ValidationIncident struct {
	ID                string                  `json:"id"`
	Rig               string                  `json:"rig"`
	Phase             string                  `json:"phase"`
	SourceIssue       string                  `json:"source_issue,omitempty"`
	MergeRequest      string                  `json:"merge_request,omitempty"`
	Branch            string                  `json:"branch,omitempty"`
	InitialCommit     string                  `json:"initial_commit,omitempty"`
	RepairBead        string                  `json:"repair_bead,omitempty"`
	TargetAgent       string                  `json:"target_agent,omitempty"`
	LocalAttempts     int                     `json:"local_attempts"`
	HostedAttempts    int                     `json:"hosted_attempts"`
	MaxLocalAttempts  int                     `json:"max_local_attempts"`
	MaxHostedAttempts int                     `json:"max_hosted_attempts"`
	Status            string                  `json:"status"`
	LastError         string                  `json:"last_error,omitempty"`
	LastFingerprint   string                  `json:"last_fingerprint,omitempty"`
	FirstEvent        ValidationFailure       `json:"first_event"`
	LastEvent         ValidationFailure       `json:"last_event"`
	Observations      []ValidationObservation `json:"observations,omitempty"`
	CreatedAt         time.Time               `json:"created_at"`
	UpdatedAt         time.Time               `json:"updated_at"`
	EscalatedAt       time.Time               `json:"escalated_at,omitempty"`
	ResolvedAt        time.Time               `json:"resolved_at,omitempty"`
}

// MainBranchValidationState stores the last proven-green commit for a rig.
type MainBranchValidationState struct {
	LastGreenSHA  string    `json:"last_green_sha,omitempty"`
	LastTestedSHA string    `json:"last_tested_sha,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// ValidationState is persisted at deacon/validation-recovery-state.json.
type ValidationState struct {
	Version      int                                   `json:"version"`
	Incidents    map[string]*ValidationIncident        `json:"incidents"`
	MainBranches map[string]*MainBranchValidationState `json:"main_branches,omitempty"`
	LastUpdated  time.Time                             `json:"last_updated"`
}

// ValidationRecoveryResult describes a report's recovery outcome.
type ValidationRecoveryResult struct {
	IncidentID string `json:"incident_id"`
	Action     string `json:"action"`
	RepairBead string `json:"repair_bead,omitempty"`
	Message    string `json:"message,omitempty"`
	Error      error  `json:"-"`
}

// ValidationStateFile returns the durable validation recovery ledger path.
func ValidationStateFile(townRoot string) string {
	return filepath.Join(townRoot, "deacon", "validation-recovery-state.json")
}

func validationStateLockFile(townRoot string) string {
	return ValidationStateFile(townRoot) + ".lock"
}

// LoadValidationState loads the validation ledger without taking its lock.
// Callers performing read-modify-write operations must use withValidationState.
func LoadValidationState(townRoot string) (*ValidationState, error) {
	data, err := os.ReadFile(ValidationStateFile(townRoot)) //nolint:gosec // trusted town root
	if errors.Is(err, os.ErrNotExist) {
		return newValidationState(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading validation recovery state: %w", err)
	}
	var state ValidationState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parsing validation recovery state: %w", err)
	}
	if state.Incidents == nil {
		state.Incidents = make(map[string]*ValidationIncident)
	}
	if state.MainBranches == nil {
		state.MainBranches = make(map[string]*MainBranchValidationState)
	}
	return &state, nil
}

func newValidationState() *ValidationState {
	return &ValidationState{
		Version:      validationStateVersion,
		Incidents:    make(map[string]*ValidationIncident),
		MainBranches: make(map[string]*MainBranchValidationState),
	}
}

func saveValidationState(townRoot string, state *ValidationState) error {
	state.Version = validationStateVersion
	state.LastUpdated = time.Now().UTC()
	return atomicfile.EnsureDirAndWriteJSONWithPerm(ValidationStateFile(townRoot), state, 0600)
}

func withValidationState(townRoot string, fn func(*ValidationState) error) error {
	if err := os.MkdirAll(filepath.Dir(ValidationStateFile(townRoot)), 0755); err != nil {
		return fmt.Errorf("creating deacon state directory: %w", err)
	}
	lock := flock.New(validationStateLockFile(townRoot))
	if err := lock.Lock(); err != nil {
		return fmt.Errorf("locking validation recovery state: %w", err)
	}
	defer func() { _ = lock.Unlock() }()

	state, err := LoadValidationState(townRoot)
	if err != nil {
		return err
	}
	if err := fn(state); err != nil {
		return err
	}
	return saveValidationState(townRoot, state)
}

// NormalizeValidationFailure validates and canonicalizes a report.
func NormalizeValidationFailure(event ValidationFailure) (ValidationFailure, error) {
	event.Type = ValidationFailedEventType
	event.Rig = strings.TrimSpace(event.Rig)
	event.SourceIssue = strings.TrimSpace(event.SourceIssue)
	event.MergeRequest = strings.TrimSpace(event.MergeRequest)
	event.Branch = strings.TrimSpace(event.Branch)
	event.Commit = strings.TrimSpace(event.Commit)
	event.Phase = strings.ToLower(strings.TrimSpace(event.Phase))
	event.Kind = strings.ToLower(strings.TrimSpace(event.Kind))
	event.Command = strings.TrimSpace(event.Command)
	event.Summary = strings.TrimSpace(event.Summary)
	event.IncidentID = strings.TrimSpace(event.IncidentID)
	event.Evidence = truncateValidationEvidence(event.Evidence)

	if event.Rig == "" {
		return event, errors.New("rig is required")
	}
	if event.Phase != "pre-merge" && event.Phase != "post-merge" {
		return event, fmt.Errorf("invalid phase %q: expected pre-merge or post-merge", event.Phase)
	}
	switch event.Kind {
	case "functional", "test", "build", "lint", "typecheck":
	default:
		return event, fmt.Errorf("invalid kind %q", event.Kind)
	}
	if event.Summary == "" {
		return event, errors.New("summary is required")
	}
	if strings.TrimSpace(event.Evidence) == "" {
		return event, errors.New("failure evidence is required")
	}
	if event.Phase == "pre-merge" {
		if event.SourceIssue == "" {
			return event, errors.New("source issue is required for pre-merge recovery")
		}
		if event.Branch == "" {
			return event, errors.New("branch is required for pre-merge recovery")
		}
	}
	if event.Phase == "post-merge" && event.Commit == "" {
		return event, errors.New("commit is required for post-merge recovery")
	}
	if event.ReportedAt.IsZero() {
		event.ReportedAt = time.Now().UTC()
	}
	return event, nil
}

func truncateValidationEvidence(evidence string) string {
	if len(evidence) <= maxValidationEvidence {
		return evidence
	}
	return "[truncated]\n" + evidence[len(evidence)-maxValidationEvidence:]
}

func validationFingerprint(event ValidationFailure) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		event.Rig, event.Phase, event.SourceIssue, event.MergeRequest,
		event.Branch, event.Commit, event.Kind, event.Command,
		strconv.Itoa(event.ExitCode), event.Summary, event.Evidence,
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func validationIncidentID(event ValidationFailure) string {
	if event.IncidentID != "" {
		return event.IncidentID
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		event.Rig, event.Phase, event.SourceIssue, event.MergeRequest,
		event.Branch, event.Commit, event.Kind,
	}, "\x00")))
	return "vf-" + hex.EncodeToString(sum[:8])
}

func findActiveIncident(state *ValidationState, event ValidationFailure) *ValidationIncident {
	if event.IncidentID != "" {
		return state.Incidents[event.IncidentID]
	}
	for _, incident := range state.Incidents {
		if incident.Status == "resolved" || incident.Rig != event.Rig || incident.Phase != event.Phase {
			continue
		}
		if event.SourceIssue != "" && incident.SourceIssue == event.SourceIssue {
			return incident
		}
		if event.MergeRequest != "" && incident.MergeRequest == event.MergeRequest {
			return incident
		}
		if event.Phase == "post-merge" && incident.InitialCommit == event.Commit {
			return incident
		}
	}
	return nil
}

func loadValidationRecoveryConfig(townRoot, rigName string) (*ValidationRecoveryConfig, error) {
	projectDir := filepath.Join(townRoot, rigName, "refinery", "rig")
	cfg, err := LoadModelEscalationConfig(projectDir)
	if err != nil {
		return nil, err
	}
	if cfg == nil || !cfg.Enabled || cfg.ValidationFailure == nil || !cfg.ValidationFailure.Enabled {
		return nil, nil
	}
	result := *cfg.ValidationFailure
	if result.ToAgent == "" {
		result.ToAgent = "opencode-go"
	}
	if result.LocalAgent == "" {
		result.LocalAgent = "opencode-local"
	}
	if result.MaxLocalAttempts <= 0 {
		result.MaxLocalAttempts = 1
	}
	if result.MaxHostedAttempts <= 0 {
		result.MaxHostedAttempts = 1
	}
	if result.RepairPriority <= 0 || result.RepairPriority > 4 {
		result.RepairPriority = 1
	}
	return &result, nil
}

var (
	dispatchValidationRepairFn = dispatchValidationRepair
	createValidationRepairFn   = createValidationRepair
	escalateValidationFn       = escalateValidationToMayor
)

// ProcessValidationFailure records a confirmed failure before taking action,
// deduplicates repeat observations, and enforces the hosted-attempt boundary.
func ProcessValidationFailure(townRoot string, raw ValidationFailure) *ValidationRecoveryResult {
	event, err := NormalizeValidationFailure(raw)
	if err != nil {
		return &ValidationRecoveryResult{Action: "error", Error: err}
	}
	cfg, err := loadValidationRecoveryConfig(townRoot, event.Rig)
	if err != nil {
		return &ValidationRecoveryResult{Action: "error", Error: err}
	}
	if cfg == nil {
		return &ValidationRecoveryResult{Action: "disabled", Message: "validation recovery is not enabled for this rig"}
	}

	fingerprint := validationFingerprint(event)
	var incidentID string
	var repairBead string
	var action string

	err = withValidationState(townRoot, func(state *ValidationState) error {
		incident := findActiveIncident(state, event)
		if incident == nil {
			incidentID = validationIncidentID(event)
			event.IncidentID = incidentID
			if existing := state.Incidents[incidentID]; existing != nil && existing.Status != "resolved" {
				incident = existing
			} else {
				now := time.Now().UTC()
				incident = &ValidationIncident{
					ID:                incidentID,
					Rig:               event.Rig,
					Phase:             event.Phase,
					SourceIssue:       event.SourceIssue,
					MergeRequest:      event.MergeRequest,
					Branch:            event.Branch,
					InitialCommit:     event.Commit,
					TargetAgent:       cfg.ToAgent,
					MaxLocalAttempts:  cfg.MaxLocalAttempts,
					MaxHostedAttempts: cfg.MaxHostedAttempts,
					Status:            "recorded",
					FirstEvent:        event,
					LastEvent:         event,
					CreatedAt:         now,
					UpdatedAt:         now,
				}
				state.Incidents[incidentID] = incident
			}
		}
		incidentID = incident.ID
		event.IncidentID = incident.ID
		repairBead = incident.RepairBead

		if incident.Status == "resolved" {
			action = "resolved"
			return nil
		}
		if incident.Status == "dispatching" || incident.Status == "escalating" {
			action = "in-progress"
			return nil
		}
		if incident.LastFingerprint == fingerprint &&
			incident.Status != "dispatch-error" &&
			incident.Status != "escalation-error" {
			action = "duplicate"
			return nil
		}
		incident.LastFingerprint = fingerprint
		incident.LastEvent = event
		incident.Observations = append(incident.Observations, ValidationObservation{
			Fingerprint: fingerprint,
			Commit:      event.Commit,
			Kind:        event.Kind,
			ExitCode:    event.ExitCode,
			ObservedAt:  event.ReportedAt,
		})
		incident.UpdatedAt = time.Now().UTC()

		if incident.LocalAttempts >= incident.MaxLocalAttempts &&
			incident.HostedAttempts >= incident.MaxHostedAttempts {
			if incident.Status == "escalated" {
				action = "already-escalated"
			} else {
				incident.Status = "escalating"
				action = "escalate"
			}
			return nil
		}

		incident.Status = "dispatching"
		incident.LastError = ""
		action = "dispatch"
		return nil
	})
	if err != nil {
		return &ValidationRecoveryResult{IncidentID: incidentID, Action: "error", Error: err}
	}

	switch action {
	case "duplicate":
		return &ValidationRecoveryResult{IncidentID: incidentID, Action: "duplicate", RepairBead: repairBead, Message: "failure observation already processed"}
	case "resolved":
		return &ValidationRecoveryResult{IncidentID: incidentID, Action: "resolved", RepairBead: repairBead, Message: "incident is already resolved"}
	case "in-progress":
		return &ValidationRecoveryResult{IncidentID: incidentID, Action: "in-progress", RepairBead: repairBead, Message: "incident recovery is already in progress"}
	case "already-escalated":
		return &ValidationRecoveryResult{IncidentID: incidentID, Action: "already-escalated", RepairBead: repairBead, Message: "incident already escalated to Mayor"}
	case "escalate":
		err = escalateValidationFn(townRoot, incidentID, event)
		stateErr := withValidationState(townRoot, func(state *ValidationState) error {
			incident := state.Incidents[incidentID]
			if err != nil {
				incident.Status = "escalation-error"
				incident.LastError = err.Error()
			} else {
				incident.Status = "escalated"
				incident.EscalatedAt = time.Now().UTC()
			}
			incident.UpdatedAt = time.Now().UTC()
			return nil
		})
		if err == nil && stateErr != nil {
			err = fmt.Errorf("persisting validation escalation result: %w", stateErr)
		}
		if err != nil {
			return &ValidationRecoveryResult{IncidentID: incidentID, Action: "error", RepairBead: repairBead, Error: err}
		}
		return &ValidationRecoveryResult{IncidentID: incidentID, Action: "escalated", RepairBead: repairBead, Message: "hosted repair allowance exhausted; escalated to Mayor"}
	}

	if event.Phase == "post-merge" && repairBead == "" {
		repairBead, err = createValidationRepairFn(townRoot, event, cfg.RepairPriority, incidentID)
		if err == nil {
			err = withValidationState(townRoot, func(state *ValidationState) error {
				state.Incidents[incidentID].RepairBead = repairBead
				return nil
			})
		}
	}
	targetAgent := cfg.LocalAgent
	hostedAttempt := false
	if currentState, loadErr := LoadValidationState(townRoot); loadErr == nil {
		if current := currentState.Incidents[incidentID]; current != nil &&
			current.LocalAttempts >= current.MaxLocalAttempts {
			targetAgent = cfg.ToAgent
			hostedAttempt = true
		}
	}
	if err == nil {
		beadID := event.SourceIssue
		if event.Phase == "post-merge" {
			beadID = repairBead
		}
		err = dispatchValidationRepairFn(townRoot, beadID, event.Rig, event.Branch, targetAgent, incidentID)
	}

	stateErr := withValidationState(townRoot, func(state *ValidationState) error {
		incident := state.Incidents[incidentID]
		incident.UpdatedAt = time.Now().UTC()
		incident.RepairBead = repairBead
		if err != nil {
			incident.Status = "dispatch-error"
			incident.LastError = err.Error()
		} else {
			if hostedAttempt {
				incident.Status = "hosted-dispatched"
				incident.HostedAttempts++
			} else {
				incident.Status = "local-dispatched"
				incident.LocalAttempts++
			}
		}
		return nil
	})
	if err == nil && stateErr != nil {
		err = fmt.Errorf("persisting validation dispatch result: %w", stateErr)
	}
	if err != nil {
		return &ValidationRecoveryResult{IncidentID: incidentID, Action: "error", RepairBead: repairBead, Error: err}
	}
	return &ValidationRecoveryResult{
		IncidentID: incidentID,
		Action:     "dispatched",
		RepairBead: repairBead,
		Message:    fmt.Sprintf("dispatched one repair attempt with agent %q", targetAgent),
	}
}

func dispatchValidationRepair(townRoot, beadID, rigName, branch, agent, incidentID string) error {
	if beadID == "" {
		return errors.New("repair bead is empty")
	}
	args := []string{"sling", beadID, rigName, "--force", "--no-convoy", "--agent", agent}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, "--args", fmt.Sprintf(
		"Validation recovery incident %s. Reproduce the recorded failure first, repair it without weakening validation, then run the failing check and the full configured suite.",
		incidentID,
	))
	cmd := exec.Command("gt", args...)
	cmd.Dir = townRoot
	cmd.Env = deaconMutationRoutingEnv(townRoot)
	util.SetDetachedProcessGroup(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("dispatching validation repair: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func createValidationRepair(townRoot string, event ValidationFailure, priority int, incidentID string) (string, error) {
	// The refinery project worktree always carries the rig's .beads redirect.
	// Mayor worktrees are optional and may not have one on docked/minimal rigs.
	workDir := filepath.Join(townRoot, event.Rig, "refinery", "rig")
	if _, err := os.Stat(workDir); err != nil {
		return "", fmt.Errorf("repair issue workspace: %w", err)
	}
	title := fmt.Sprintf("Repair validation regression from %s", shortValidationSHA(event.Commit))
	description := fmt.Sprintf(`Validation recovery incident: %s
Rig: %s
Failing commit: %s
Source issue: %s
Failure kind: %s
Command: %s
Exit code: %d
Summary: %s

Evidence:
%s`, incidentID, event.Rig, event.Commit, event.SourceIssue, event.Kind, event.Command, event.ExitCode, event.Summary, event.Evidence)
	acceptance := "Reproduce the reported regression; make the failing check pass; run the rig's full configured validation suite; do not remove or weaken valid tests."
	args := []string{
		"create", "--silent",
		"--title=" + title,
		"--type=bug",
		fmt.Sprintf("--priority=%d", priority),
		"--description=" + description,
		"--acceptance=" + acceptance,
	}
	if event.SourceIssue != "" {
		args = append(args, "--deps=discovered-from:"+event.SourceIssue)
	}
	cmd := beads.Command(workDir, filepath.Join(workDir, ".beads"), beads.MutationRouting, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("creating validation repair bead: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	id := strings.TrimSpace(string(output))
	if fields := strings.Fields(id); len(fields) > 0 {
		id = fields[len(fields)-1]
	}
	if id == "" {
		return "", errors.New("bd create returned an empty repair bead id")
	}
	return id, nil
}

func shortValidationSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	if sha == "" {
		return "unknown"
	}
	return sha
}

func escalateValidationToMayor(townRoot, incidentID string, event ValidationFailure) error {
	subject := fmt.Sprintf("VALIDATION_REPAIR_FAILED %s", incidentID)
	body := fmt.Sprintf(`Validation incident %s still fails after its hosted repair attempt.

Rig: %s
Phase: %s
Source issue: %s
Merge request: %s
Branch: %s
Commit: %s
Kind: %s
Command: %s
Exit code: %d
Summary: %s

Evidence:
%s

Automatic dispatch is stopped. Investigate manually; do not weaken valid validation.`,
		incidentID, event.Rig, event.Phase, event.SourceIssue, event.MergeRequest,
		event.Branch, event.Commit, event.Kind, event.Command, event.ExitCode,
		event.Summary, event.Evidence)
	cmd := exec.Command("gt", "mail", "send", "mayor/", "-s", subject, "-m", body)
	cmd.Dir = townRoot
	cmd.Env = deaconMutationRoutingEnv(townRoot)
	util.SetDetachedProcessGroup(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("escalating validation incident to Mayor: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// ResolveValidationIncidents marks matching active incidents resolved.
func ResolveValidationIncidents(townRoot, incidentID, rigName, sourceIssue, phase string) (int, error) {
	resolved := 0
	err := withValidationState(townRoot, func(state *ValidationState) error {
		now := time.Now().UTC()
		for id, incident := range state.Incidents {
			if incidentID != "" && id != incidentID {
				continue
			}
			if rigName != "" && incident.Rig != rigName {
				continue
			}
			if sourceIssue != "" && incident.SourceIssue != sourceIssue && incident.RepairBead != sourceIssue {
				continue
			}
			if phase != "" && incident.Phase != phase {
				continue
			}
			if incident.Status == "resolved" {
				continue
			}
			incident.Status = "resolved"
			incident.ResolvedAt = now
			incident.UpdatedAt = now
			resolved++
		}
		return nil
	})
	return resolved, err
}

// MainBranchValidation returns a copy of a rig's main-branch state.
func MainBranchValidation(townRoot, rigName string) (*MainBranchValidationState, error) {
	state, err := LoadValidationState(townRoot)
	if err != nil {
		return nil, err
	}
	current := state.MainBranches[rigName]
	if current == nil {
		return &MainBranchValidationState{}, nil
	}
	copy := *current
	return &copy, nil
}

// RecordMainBranchValidation updates a rig's tested and optionally green SHA.
func RecordMainBranchValidation(townRoot, rigName, testedSHA string, green bool) error {
	return withValidationState(townRoot, func(state *ValidationState) error {
		current := state.MainBranches[rigName]
		if current == nil {
			current = &MainBranchValidationState{}
			state.MainBranches[rigName] = current
		}
		current.LastTestedSHA = testedSHA
		if green {
			current.LastGreenSHA = testedSHA
		}
		current.UpdatedAt = time.Now().UTC()
		return nil
	})
}
