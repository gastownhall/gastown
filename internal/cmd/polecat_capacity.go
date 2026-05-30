package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/scheduler/capacity"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
)

const polecatAdmissionReservationTTL = 30 * time.Minute

var acquirePolecatAdmissionFn = acquirePolecatAdmission

type polecatCapacitySnapshot struct {
	Max             int `json:"max"`
	Working         int `json:"working"`
	RecoveryBlocked int `json:"recovery_blocked"`
	ReusableIdle    int `json:"reusable_idle"`
	PendingMR       int `json:"pending_mr"`
	Reservations    int `json:"reservations"`
	Free            int `json:"free"`
	ActiveSessions  int `json:"active_sessions"`
}

func (s polecatCapacitySnapshot) occupied() int {
	return s.Working + s.RecoveryBlocked + s.Reservations
}

type polecatAdmissionReservation struct {
	ID        string    `json:"id"`
	PID       int       `json:"pid"`
	Rig       string    `json:"rig,omitempty"`
	Bead      string    `json:"bead,omitempty"`
	Operation string    `json:"operation"`
	CreatedAt time.Time `json:"created_at"`
}

type polecatAdmissionHandle struct {
	townRoot string
	id       string
	path     string
	disabled bool
}

func (h *polecatAdmissionHandle) Release() {
	if h == nil || h.disabled || h.path == "" {
		return
	}
	_ = os.Remove(h.path)
}

type polecatCapacityAdmissionError struct {
	Snapshot polecatCapacitySnapshot
	Rig      string
	Bead     string
	Reason   string
}

func (e *polecatCapacityAdmissionError) Error() string {
	if e == nil {
		return "polecat admission denied"
	}
	if e.Snapshot.Max <= 0 {
		return fmt.Sprintf("polecat admission denied: %s", e.Reason)
	}
	return fmt.Sprintf(
		"polecat admission denied: %s (max=%d occupied=%d working=%d recovery_blocked=%d reservations=%d reusable_idle=%d pending_mr=%d free=%d). Resolve recovery-needed polecats or raise scheduler.max_polecats; inspect with `gt scheduler status --json` or `gt polecat list --all --json`",
		e.Reason,
		e.Snapshot.Max,
		e.Snapshot.occupied(),
		e.Snapshot.Working,
		e.Snapshot.RecoveryBlocked,
		e.Snapshot.Reservations,
		e.Snapshot.ReusableIdle,
		e.Snapshot.PendingMR,
		e.Snapshot.Free,
	)
}

func acquirePolecatAdmission(townRoot, rigName, beadID, operation string) (*polecatAdmissionHandle, polecatCapacitySnapshot, error) {
	max, err := configuredSchedulerMaxPolecats(townRoot)
	if err != nil {
		return nil, polecatCapacitySnapshot{}, err
	}
	if max <= 0 {
		return &polecatAdmissionHandle{disabled: true}, polecatCapacitySnapshot{Max: max, ActiveSessions: countActivePolecats()}, nil
	}

	lock, err := acquirePolecatAdmissionLock(townRoot)
	if err != nil {
		return nil, polecatCapacitySnapshot{}, err
	}
	defer func() { _ = lock.Unlock() }()

	if err := cleanupStalePolecatAdmissionReservations(townRoot, time.Now()); err != nil {
		return nil, polecatCapacitySnapshot{}, err
	}

	snapshot, err := polecatCapacitySnapshotForTownNoCleanup(townRoot)
	if err != nil {
		return nil, polecatCapacitySnapshot{}, err
	}
	if snapshot.Free <= 0 {
		return nil, snapshot, &polecatCapacityAdmissionError{
			Snapshot: snapshot,
			Rig:      rigName,
			Bead:     beadID,
			Reason:   "configured scheduler.max_polecats capacity is full",
		}
	}

	reservation, path, err := writePolecatAdmissionReservation(townRoot, rigName, beadID, operation)
	if err != nil {
		return nil, snapshot, err
	}
	snapshot.Reservations++
	snapshot.Free--
	return &polecatAdmissionHandle{townRoot: townRoot, id: reservation.ID, path: path}, snapshot, nil
}

func configuredSchedulerMaxPolecats(townRoot string) (int, error) {
	settings, err := config.LoadOrCreateTownSettings(config.TownSettingsPath(townRoot))
	if err != nil {
		return 0, fmt.Errorf("loading town settings for polecat admission: %w", err)
	}
	schedulerCfg := settings.Scheduler
	if schedulerCfg == nil {
		schedulerCfg = capacity.DefaultSchedulerConfig()
	}
	return schedulerCfg.GetMaxPolecats(), nil
}

func polecatCapacitySnapshotForTown(townRoot string) (polecatCapacitySnapshot, error) {
	max, err := configuredSchedulerMaxPolecats(townRoot)
	if err != nil {
		return polecatCapacitySnapshot{}, err
	}
	if max > 0 {
		if err := cleanupStalePolecatAdmissionReservationsWithLock(townRoot, time.Now()); err != nil {
			return polecatCapacitySnapshot{}, err
		}
	}
	return polecatCapacitySnapshotForTownNoCleanup(townRoot)
}

func polecatCapacitySnapshotForTownNoCleanup(townRoot string) (polecatCapacitySnapshot, error) {
	max, err := configuredSchedulerMaxPolecats(townRoot)
	if err != nil {
		return polecatCapacitySnapshot{}, err
	}
	snapshot := polecatCapacitySnapshot{Max: max, ActiveSessions: countActivePolecats()}
	if max <= 0 {
		return snapshot, nil
	}

	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		return snapshot, fmt.Errorf("loading rigs config for polecat capacity: %w", err)
	}

	tmuxClient := tmux.NewTmux()
	for rigName := range rigsConfig.Rigs {
		rigPath := filepath.Join(townRoot, rigName)
		if _, err := os.Stat(rigPath); err != nil {
			continue
		}
		polecatNames, err := listPolecatDirectoryNames(rigPath)
		if err != nil {
			return snapshot, fmt.Errorf("listing polecat dirs for %s capacity: %w", rigName, err)
		}
		if len(polecatNames) == 0 {
			continue
		}

		agents, err := beads.New(rigPath).ListAgentBeads()
		if err != nil {
			return snapshot, fmt.Errorf("listing agent beads for %s capacity: %w", rigName, err)
		}
		prefix := beads.GetPrefixForRig(townRoot, rigName)
		for _, name := range polecatNames {
			agentID := beads.PolecatBeadIDWithPrefix(prefix, rigName, name)
			issue := agents[agentID]
			fields := (*beads.AgentFields)(nil)
			if issue != nil {
				fields = beads.ParseAgentFields(issue.Description)
				fields.AgentState = beads.ResolveAgentState(issue.Description, issue.AgentState)
			}
			applyAgentFieldsToCapacitySnapshot(&snapshot, rigName, name, fields, tmuxClient)
		}
	}

	reservations, err := readPolecatAdmissionReservations(townRoot)
	if err != nil {
		return snapshot, err
	}
	snapshot.Reservations = len(reservations)
	if max > 0 {
		snapshot.Free = max - snapshot.occupied()
		if snapshot.Free < 0 {
			snapshot.Free = 0
		}
	}
	return snapshot, nil
}

func listPolecatDirectoryNames(rigPath string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(rigPath, "polecats"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

func applyAgentFieldsToCapacitySnapshot(snapshot *polecatCapacitySnapshot, rigName, polecatName string, fields *beads.AgentFields, tmuxClient *tmux.Tmux) {
	running := false
	if tmuxClient != nil {
		running, _ = tmuxClient.HasSession(session.PolecatSessionName(session.PrefixFor(rigName), polecatName))
	}
	if fields == nil {
		if running {
			snapshot.Working++
		} else {
			snapshot.RecoveryBlocked++
		}
		return
	}

	state := strings.TrimSpace(fields.AgentState)
	if fields.HookBead != "" || state == "working" || state == "spawning" {
		if running {
			snapshot.Working++
		} else {
			snapshot.RecoveryBlocked++
		}
		return
	}
	if fields.PushFailed || fields.MRFailed {
		snapshot.RecoveryBlocked++
		return
	}
	if fields.ActiveMR != "" {
		snapshot.PendingMR++
		return
	}
	if fields.CleanupStatus == "clean" || state == "nuked" {
		snapshot.ReusableIdle++
		return
	}
	snapshot.RecoveryBlocked++
}

func applyWorkstateDispositionToCapacitySnapshot(snapshot *polecatCapacitySnapshot, state polecat.State, disposition polecat.WorkstateDisposition) {
	if disposition.ReuseStatus == "idle-pr-open" {
		snapshot.PendingMR++
		return
	}
	if disposition.Reusable {
		snapshot.ReusableIdle++
		return
	}
	if !disposition.CountsTowardCapacity {
		return
	}
	if state == polecat.StateWorking || disposition.Verdict == polecat.WorkstateVerdictWorking {
		snapshot.Working++
		return
	}
	snapshot.RecoveryBlocked++
}

func acquirePolecatAdmissionLock(townRoot string) (*flock.Flock, error) {
	lockDir := filepath.Join(townRoot, ".runtime", "locks")
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		return nil, fmt.Errorf("creating polecat admission lock dir: %w", err)
	}
	lock := flock.New(filepath.Join(lockDir, "polecat-admission.lock"))
	locked, err := lock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("acquiring polecat admission lock: %w", err)
	}
	if !locked {
		return nil, fmt.Errorf("polecat admission is busy; retry shortly")
	}
	return lock, nil
}

func polecatAdmissionDir(townRoot string) string {
	return filepath.Join(townRoot, ".runtime", "polecat-admission")
}

func writePolecatAdmissionReservation(townRoot, rigName, beadID, operation string) (polecatAdmissionReservation, string, error) {
	dir := polecatAdmissionDir(townRoot)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return polecatAdmissionReservation{}, "", fmt.Errorf("creating polecat admission dir: %w", err)
	}
	now := time.Now().UTC()
	id := fmt.Sprintf("%d-%d", os.Getpid(), now.UnixNano())
	reservation := polecatAdmissionReservation{
		ID:        id,
		PID:       os.Getpid(),
		Rig:       rigName,
		Bead:      beadID,
		Operation: operation,
		CreatedAt: now,
	}
	path := filepath.Join(dir, id+".json")
	tmpPath := path + ".tmp"
	data, err := json.MarshalIndent(reservation, "", "  ")
	if err != nil {
		return polecatAdmissionReservation{}, "", err
	}
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return polecatAdmissionReservation{}, "", fmt.Errorf("writing polecat admission reservation: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return polecatAdmissionReservation{}, "", fmt.Errorf("publishing polecat admission reservation: %w", err)
	}
	return reservation, path, nil
}

func readPolecatAdmissionReservations(townRoot string) ([]polecatAdmissionReservation, error) {
	dir := polecatAdmissionDir(townRoot)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading polecat admission reservations: %w", err)
	}
	reservations := make([]polecatAdmissionReservation, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			_ = os.Remove(path)
			continue
		}
		var reservation polecatAdmissionReservation
		if err := json.Unmarshal(data, &reservation); err != nil {
			_ = os.Remove(path)
			continue
		}
		if reservation.ID == "" || reservation.PID <= 0 || reservation.CreatedAt.IsZero() || reservation.ID+".json" != entry.Name() {
			_ = os.Remove(path)
			continue
		}
		reservations = append(reservations, reservation)
	}
	return reservations, nil
}

func cleanupStalePolecatAdmissionReservations(townRoot string, now time.Time) error {
	dir := polecatAdmissionDir(townRoot)
	reservations, err := readPolecatAdmissionReservations(townRoot)
	if err != nil {
		return err
	}
	for _, reservation := range reservations {
		if reservation.PID <= 0 {
			continue
		}
		age := now.Sub(reservation.CreatedAt)
		if processAlive(reservation.PID) {
			continue
		}
		if age < polecatAdmissionReservationTTL {
			continue
		}
		_ = os.Remove(filepath.Join(dir, reservation.ID+".json"))
	}
	return nil
}

func cleanupStalePolecatAdmissionReservationsWithLock(townRoot string, now time.Time) error {
	lock, err := acquirePolecatAdmissionLock(townRoot)
	if err != nil {
		if strings.Contains(err.Error(), "admission is busy") {
			return nil
		}
		return err
	}
	defer func() { _ = lock.Unlock() }()
	return cleanupStalePolecatAdmissionReservations(townRoot, now)
}
