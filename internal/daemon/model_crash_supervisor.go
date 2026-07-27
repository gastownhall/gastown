package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/atomicfile"
	"github.com/steveyegge/gastown/internal/session"
)

const (
	modelCrashScanInterval          = 60 * time.Second
	modelCrashObservationMaxGap     = 2 * modelCrashScanInterval
	modelCrashWatchdogMaxAge        = 2 * time.Minute
	modelCrashControlProbeInterval  = 30 * time.Minute
	modelCrashProgressResetInterval = 30 * time.Minute
)

type modelCrashClock interface {
	Now() time.Time
}

type realModelCrashClock struct{}

func (realModelCrashClock) Now() time.Time { return time.Now() }

type modelCrashSession struct {
	Name       string
	InstanceID string
	Identity   string
	Role       string
	Agent      string
	WorkUnit   string
	WorkDir    string
	Output     string
}

type modelCrashWatchdog struct {
	Status    string    `json:"status"`
	CheckedAt time.Time `json:"checked_at"`
}

type modelCrashExecutor interface {
	Sessions() ([]modelCrashSession, error)
	LMWatchdog() (modelCrashWatchdog, error)
	Restart(modelCrashSession, string) error
	ActivePolecat(modelCrashSession) bool
	AllowsContinuation(modelCrashSession, string, string) bool
	Alert(string, string) error
}

type modelCrashSessionState struct {
	SessionName           string    `json:"session_name"`
	IncidentID            string    `json:"incident_id"`
	WorkUnit              string    `json:"work_unit,omitempty"`
	ObservationKey        string    `json:"observation_key,omitempty"`
	HandledObservationKey string    `json:"handled_observation_key,omitempty"`
	Consecutive           int       `json:"consecutive_observations"`
	LastObservedAt        time.Time `json:"last_observed_at,omitempty"`
	ProgressSince         time.Time `json:"progress_since,omitempty"`
	LocalRestarts         int       `json:"local_restarts"`
	GoContinuations       int       `json:"go_continuations"`
	ControlProbes         int       `json:"control_probes"`
	NextProbeAt           time.Time `json:"next_probe_at,omitempty"`
	RecoveryAction        string    `json:"recovery_action,omitempty"`
	RecoveryExhausted     bool      `json:"recovery_exhausted"`
}

type modelCrashState struct {
	Version       int                                `json:"version"`
	Sessions      map[string]*modelCrashSessionState `json:"sessions"`
	Alerts        map[string]time.Time               `json:"alerts"`
	PendingAlerts map[string]string                  `json:"pending_alerts,omitempty"`
}

type modelCrashSupervisor struct {
	statePath string
	clock     modelCrashClock
	executor  modelCrashExecutor
	state     modelCrashState
	loaded    bool
}

func newModelCrashSupervisor(statePath string, clock modelCrashClock, executor modelCrashExecutor) *modelCrashSupervisor {
	return &modelCrashSupervisor{
		statePath: statePath,
		clock:     clock,
		executor:  executor,
		state: modelCrashState{
			Version:       1,
			Sessions:      make(map[string]*modelCrashSessionState),
			Alerts:        make(map[string]time.Time),
			PendingAlerts: make(map[string]string),
		},
	}
}

func modelCrashStateFile(townRoot string) string {
	return filepath.Join(townRoot, "deacon", "model-crash-supervisor.json")
}

func (s *modelCrashSupervisor) load() error {
	data, err := os.ReadFile(s.statePath) //nolint:gosec // state path is derived from the configured town root
	if err != nil {
		if os.IsNotExist(err) {
			s.loaded = true
			return nil
		}
		return err
	}
	if err := json.Unmarshal(data, &s.state); err != nil {
		return fmt.Errorf("parsing model crash supervisor state: %w", err)
	}
	if s.state.Sessions == nil {
		s.state.Sessions = make(map[string]*modelCrashSessionState)
	}
	if s.state.Alerts == nil {
		s.state.Alerts = make(map[string]time.Time)
	}
	if s.state.PendingAlerts == nil {
		s.state.PendingAlerts = make(map[string]string)
	}
	if s.reconcileInterruptedActions() {
		// Pending recovery actions are write-ahead at-most-once boundaries.
		// Persist exhaustion and the alert intent before attempting delivery,
		// so daemon termination cannot replay the action or lose visibility.
		if err := s.save(); err != nil {
			return err
		}
	}
	s.loaded = true
	return nil
}

func (s *modelCrashSupervisor) reconcileInterruptedActions() bool {
	changed := false
	for identity, candidate := range s.state.Sessions {
		if candidate == nil || !strings.HasSuffix(candidate.RecoveryAction, "-pending") {
			continue
		}
		pendingAction := candidate.RecoveryAction
		candidate.RecoveryAction = strings.TrimSuffix(pendingAction, "-pending") + "-interrupted"
		candidate.RecoveryExhausted = true
		candidate.NextProbeAt = time.Time{}
		alertID := candidate.IncidentID
		if alertID == "" {
			alertID = identity
		}
		key := alertID + ":interrupted-action"
		if _, delivered := s.state.Alerts[key]; !delivered {
			s.state.PendingAlerts[key] = fmt.Sprintf(
				"Model-crash recovery action %s for %s was interrupted after its durable at-most-once boundary; it will not be replayed",
				pendingAction, identity)
		}
		changed = true
	}
	return changed
}

func (s *modelCrashSupervisor) save() error {
	if err := os.MkdirAll(filepath.Dir(s.statePath), 0o700); err != nil {
		return err
	}
	return atomicfile.WriteJSONWithPerm(s.statePath, &s.state, 0o600)
}

func (s *modelCrashSupervisor) Scan() error {
	if !s.loaded {
		if err := s.load(); err != nil {
			return err
		}
	}

	alertsChanged := s.retryPendingAlerts()
	now := s.clock.Now()
	watchdog, err := s.executor.LMWatchdog()
	if err != nil || validateModelCrashWatchdog(watchdog, now) != nil {
		if alertsChanged {
			return s.save()
		}
		return nil
	}

	sessions, err := s.executor.Sessions()
	if err != nil {
		return err
	}
	changed := alertsChanged
	for _, candidate := range sessions {
		if candidate.Agent != "opencode-local" || candidate.Identity == "" ||
			candidate.Role == "boot" || strings.Contains(candidate.Identity, "/boot") {
			continue
		}
		current := s.state.Sessions[candidate.Identity]
		if candidate.Role == "polecat" || candidate.Role == "dog" {
			workUnit := strings.TrimSpace(candidate.WorkUnit)
			if current != nil && current.WorkUnit != workUnit {
				// A reusable worker's recovery budget belongs to its assigned
				// work, not its stable role identity. Retire the old incident
				// before observing or acting on the new assignment.
				delete(s.state.Sessions, candidate.Identity)
				current = nil
				changed = true
			}
			if workUnit == "" {
				// Running but idle workers are inventory, not recovery
				// candidates. Controls and Crew intentionally have no work
				// unit requirement.
				continue
			}
		}
		fingerprint, fatal := session.DetectModelCrash(candidate.Output)
		if !fatal {
			if current == nil || strings.TrimSpace(candidate.Output) == "" {
				continue
			}
			if current.ProgressSince.IsZero() {
				current.ProgressSince = now
				changed = true
			} else if now.Sub(current.ProgressSince) >= modelCrashProgressResetInterval {
				delete(s.state.Sessions, candidate.Identity)
				changed = true
			}
			continue
		}

		if current == nil {
			current = &modelCrashSessionState{
				SessionName: candidate.Name,
				IncidentID:  newModelCrashIncidentID(candidate.Identity, now),
				WorkUnit:    strings.TrimSpace(candidate.WorkUnit),
			}
			s.state.Sessions[candidate.Identity] = current
		}
		current.SessionName = candidate.Name
		current.ProgressSince = time.Time{}
		changed = true

		observationKey := candidate.InstanceID + "|" + fingerprint
		if current.HandledObservationKey == observationKey {
			if !current.NextProbeAt.IsZero() && !now.Before(current.NextProbeAt) &&
				current.GoContinuations == 0 {
				if err := s.runLocalProbe(candidate, current, now); err != nil {
					return err
				}
			}
			continue
		}
		if current.ObservationKey != observationKey {
			current.ObservationKey = observationKey
			current.Consecutive = 1
			current.LastObservedAt = now
			continue
		}
		gap := now.Sub(current.LastObservedAt)
		if gap <= 0 {
			continue
		}
		if gap > modelCrashObservationMaxGap {
			current.Consecutive = 1
			current.LastObservedAt = now
			continue
		}
		current.LastObservedAt = now
		current.Consecutive++
		if current.Consecutive < 2 {
			continue
		}
		current.HandledObservationKey = observationKey
		current.Consecutive = 0

		if current.LocalRestarts == 0 {
			// Write intent before the external restart so daemon termination
			// cannot replay this one-shot action.
			current.LocalRestarts = 1
			current.RecoveryAction = "local-restart-pending"
			if err := s.save(); err != nil {
				return err
			}
			if err := s.executor.Restart(candidate, "opencode-local"); err != nil {
				current.RecoveryAction = "local-restart-failed"
				current.RecoveryExhausted = true
				current.NextProbeAt = now.Add(modelCrashControlProbeInterval)
				s.alertOnce(current.IncidentID+":local-restart-failed",
					fmt.Sprintf("Local model-crash restart failed for %s: %v", candidate.Identity, err))
			} else {
				current.RecoveryAction = "local-restart"
				current.RecoveryExhausted = false
			}
			continue
		}

		if candidate.Role == "polecat" && current.GoContinuations == 0 &&
			s.executor.ActivePolecat(candidate) &&
			s.executor.AllowsContinuation(candidate, "opencode-local", "opencode-go") {
			// Persist the one hosted attempt before dispatch. This is the
			// at-most-once boundary across daemon crashes and restarts.
			current.GoContinuations = 1
			current.RecoveryAction = "go-continuation-pending"
			if err := s.save(); err != nil {
				return err
			}
			if err := s.executor.Restart(candidate, "opencode-go"); err != nil {
				current.RecoveryAction = "go-continuation-failed"
				current.RecoveryExhausted = true
				s.alertOnce(current.IncidentID+":go-continuation-failed",
					fmt.Sprintf("OpenCode Go continuation failed for %s: %v", candidate.Identity, err))
			} else {
				current.RecoveryAction = "go-continuation"
				current.RecoveryExhausted = false
			}
			continue
		}

		current.RecoveryAction = "awaiting-local-probe"
		current.RecoveryExhausted = true
		current.NextProbeAt = now.Add(modelCrashControlProbeInterval)
		s.alertOnce(current.IncidentID+":recovery-exhausted",
			fmt.Sprintf("Model-crash recovery exhausted for %s; hosted continuation is not authorized", candidate.Identity))
	}

	if changed {
		return s.save()
	}
	return nil
}

func (s *modelCrashSupervisor) runLocalProbe(candidate modelCrashSession, state *modelCrashSessionState, now time.Time) error {
	state.ControlProbes++
	state.NextProbeAt = now.Add(modelCrashControlProbeInterval)
	state.RecoveryAction = "local-probe-pending"
	if err := s.save(); err != nil {
		state.RecoveryAction = "local-probe-state-failed"
		state.RecoveryExhausted = true
		return err
	}
	if err := s.executor.Restart(candidate, "opencode-local"); err != nil {
		state.RecoveryAction = "local-probe-failed"
		state.RecoveryExhausted = true
		s.alertOnce(state.IncidentID+":local-probe-failed",
			fmt.Sprintf("Local 30-minute model-crash probe failed for %s: %v", candidate.Identity, err))
		return nil
	}
	state.RecoveryAction = "local-probe"
	state.RecoveryExhausted = false
	return nil
}

func (s *modelCrashSupervisor) alertOnce(key, message string) bool {
	if _, delivered := s.state.Alerts[key]; delivered {
		return false
	}
	if err := s.executor.Alert(key, message); err != nil {
		s.state.PendingAlerts[key] = message
		return false
	}
	delete(s.state.PendingAlerts, key)
	s.state.Alerts[key] = s.clock.Now()
	return true
}

func (s *modelCrashSupervisor) retryPendingAlerts() bool {
	changed := false
	for key, message := range s.state.PendingAlerts {
		if _, delivered := s.state.Alerts[key]; delivered {
			delete(s.state.PendingAlerts, key)
			changed = true
			continue
		}
		if err := s.executor.Alert(key, message); err != nil {
			continue
		}
		delete(s.state.PendingAlerts, key)
		s.state.Alerts[key] = s.clock.Now()
		changed = true
	}
	return changed
}

func newModelCrashIncidentID(identity string, now time.Time) string {
	replacer := strings.NewReplacer("/", "-", " ", "-", "_", "-")
	return fmt.Sprintf("model-crash-%d-%s", now.Unix(), replacer.Replace(identity))
}

// ValidateModelCrashWatchdog verifies the external LM watchdog dependency used
// by the model-crash supervisor. Recovery remains fail-closed unless the
// watchdog is readable, healthy, and fresh.
func ValidateModelCrashWatchdog(townRoot string, now time.Time) error {
	path := filepath.Join(townRoot, "deacon", "lmstudio-watchdog.json")
	data, err := os.ReadFile(path) //nolint:gosec // path is derived from discovered town root
	if err != nil {
		return fmt.Errorf("reading LM watchdog state: %w", err)
	}
	var watchdog modelCrashWatchdog
	if err := json.Unmarshal(data, &watchdog); err != nil {
		return fmt.Errorf("parsing LM watchdog state: %w", err)
	}
	return validateModelCrashWatchdog(watchdog, now)
}

// IsModelCrashRecoveryProvisioned reports whether this town has opted into the
// external local-model recovery contract. A watchdog file or durable
// supervisor state is provisioning evidence; unrelated installations with
// neither are not required to run the watchdog.
func IsModelCrashRecoveryProvisioned(townRoot string) bool {
	for _, path := range []string{
		filepath.Join(townRoot, "bin", "gt-lmstudio-watchdog"),
		filepath.Join(townRoot, "deacon", "lmstudio-watchdog.json"),
		modelCrashStateFile(townRoot),
	} {
		if _, err := os.Lstat(path); err == nil || !os.IsNotExist(err) {
			return true
		}
	}
	return false
}

func validateModelCrashWatchdog(watchdog modelCrashWatchdog, now time.Time) error {
	if watchdog.Status != "healthy" {
		return fmt.Errorf("LM watchdog status is %q, want healthy", watchdog.Status)
	}
	if watchdog.CheckedAt.IsZero() {
		return fmt.Errorf("LM watchdog has no check timestamp")
	}
	age := now.Sub(watchdog.CheckedAt)
	if age < 0 {
		return fmt.Errorf("LM watchdog check timestamp is in the future")
	}
	if age > modelCrashWatchdogMaxAge {
		return fmt.Errorf("LM watchdog state is stale (%s old; maximum %s)",
			age.Round(time.Second), modelCrashWatchdogMaxAge)
	}
	return nil
}

// ModelCrashSessionHealth is additive recovery visibility for `gt session
// health`. Missing state leaves all fields at their zero values.
type ModelCrashSessionHealth struct {
	IncidentID        string
	RecoveryAction    string
	RecoveryExhausted bool
}

// ModelCrashRecoveryIncident is read-only durable recovery visibility for
// status surfaces such as Doctor.
type ModelCrashRecoveryIncident struct {
	Identity          string
	SessionName       string
	IncidentID        string
	RecoveryAction    string
	RecoveryExhausted bool
	Confirmed         bool
}

// LoadModelCrashRecoveryIncidents returns active durable incident records.
func LoadModelCrashRecoveryIncidents(townRoot string) ([]ModelCrashRecoveryIncident, error) {
	data, err := os.ReadFile(modelCrashStateFile(townRoot)) //nolint:gosec // path is derived from discovered town root
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var state modelCrashState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	incidents := make([]ModelCrashRecoveryIncident, 0, len(state.Sessions))
	for identity, candidate := range state.Sessions {
		if candidate == nil {
			continue
		}
		incidents = append(incidents, ModelCrashRecoveryIncident{
			Identity:          identity,
			SessionName:       candidate.SessionName,
			IncidentID:        candidate.IncidentID,
			RecoveryAction:    candidate.RecoveryAction,
			RecoveryExhausted: candidate.RecoveryExhausted,
			Confirmed: candidate.LocalRestarts > 0 || candidate.GoContinuations > 0 ||
				candidate.ControlProbes > 0 || candidate.RecoveryAction != "",
		})
	}
	return incidents, nil
}

// LoadModelCrashSessionHealth returns durable supervisor state for a tmux
// session without changing that state.
func LoadModelCrashSessionHealth(townRoot, sessionName string) (ModelCrashSessionHealth, error) {
	incidents, err := LoadModelCrashRecoveryIncidents(townRoot)
	if err != nil {
		return ModelCrashSessionHealth{}, err
	}
	for _, candidate := range incidents {
		if candidate.SessionName == sessionName {
			return ModelCrashSessionHealth{
				IncidentID:        candidate.IncidentID,
				RecoveryAction:    candidate.RecoveryAction,
				RecoveryExhausted: candidate.RecoveryExhausted,
			}, nil
		}
	}
	return ModelCrashSessionHealth{}, nil
}
