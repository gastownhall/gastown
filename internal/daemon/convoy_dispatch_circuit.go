package daemon

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
)

type convoyDispatchFailureKind string

const (
	convoyDispatchRespawnExhausted    convoyDispatchFailureKind = "respawn-exhausted"
	convoyDispatchRigPrefixUnresolved convoyDispatchFailureKind = "rig-prefix-unresolved"
	convoyDispatchAssignmentChurn     convoyDispatchFailureKind = "assignment-churn"
)

const (
	convoyDispatchLeaseDuration = 60 * time.Minute
	convoyDispatchChurnLimit    = 2
)

type convoyDispatchLease struct {
	ConvoyID   string    `json:"convoy_id"`
	IssueID    string    `json:"issue_id"`
	Owner      string    `json:"owner"`
	Attempts   int       `json:"attempts"`
	AcquiredAt time.Time `json:"acquired_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

type convoyDispatchCircuitEntry struct {
	ConvoyID     string                    `json:"convoy_id"`
	IssueID      string                    `json:"issue_id"`
	Kind         convoyDispatchFailureKind `json:"kind"`
	Fingerprint  string                    `json:"fingerprint"`
	Error        string                    `json:"error"`
	LatchedAt    time.Time                 `json:"latched_at"`
	AlertPending bool                      `json:"alert_pending,omitempty"`
}

type convoyDispatchCircuitState struct {
	Circuits    map[string]*convoyDispatchCircuitEntry `json:"circuits"`
	Leases      map[string]*convoyDispatchLease        `json:"leases,omitempty"`
	LastUpdated time.Time                              `json:"last_updated"`
}

type convoyDispatchCircuitBreaker struct {
	mu                 sync.Mutex
	townRoot           string
	now                func() time.Time
	state              convoyDispatchCircuitState
	loadErr            error
	loadFingerprint    string
	loadAlertDelivered bool
}

func newConvoyDispatchCircuitBreaker(townRoot string, now func() time.Time) *convoyDispatchCircuitBreaker {
	if now == nil {
		now = time.Now
	}
	b := &convoyDispatchCircuitBreaker{
		townRoot: townRoot,
		now:      now,
		state: convoyDispatchCircuitState{
			Circuits: make(map[string]*convoyDispatchCircuitEntry),
			Leases:   make(map[string]*convoyDispatchLease),
		},
	}
	b.loadErr = b.load()
	if b.loadErr != nil {
		b.loadAlertDelivered = b.corruptionAlertWasDelivered()
	} else {
		// A successfully loaded (or absent) main state ends the previous
		// corruption incident. The same bytes becoming corrupt later should
		// produce a new alert.
		_ = os.Remove(convoyDispatchCorruptionAlertStateFile(townRoot))
	}
	return b
}

func convoyDispatchCircuitStateFile(townRoot string) string {
	return filepath.Join(townRoot, "daemon", "convoy-dispatch-circuits.json")
}

func convoyDispatchCorruptionAlertStateFile(townRoot string) string {
	return filepath.Join(townRoot, "daemon", "convoy-dispatch-circuit-corruption-alert.json")
}

func convoyDispatchCircuitKey(convoyID, issueID string) string {
	return convoyID + "::" + issueID
}

func (b *convoyDispatchCircuitBreaker) load() error {
	data, err := os.ReadFile(convoyDispatchCircuitStateFile(b.townRoot)) //nolint:gosec // trusted town root
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		b.loadFingerprint = fmt.Sprintf("read-error:%x", sha256.Sum256([]byte(err.Error())))
		return err
	}
	b.loadFingerprint = fmt.Sprintf("sha256:%x", sha256.Sum256(data))
	if err := json.Unmarshal(data, &b.state); err != nil {
		return err
	}
	b.loadFingerprint = ""
	if b.state.Circuits == nil {
		b.state.Circuits = make(map[string]*convoyDispatchCircuitEntry)
	}
	if b.state.Leases == nil {
		b.state.Leases = make(map[string]*convoyDispatchLease)
	}
	return nil
}

// TryAcquireLease writes the assignment intent before gt sling runs. A live
// lease suppresses convoy rescans across daemon restarts. If the same issue
// becomes dispatchable again after two completed lease windows, the circuit
// opens instead of alternating polecats indefinitely.
func (b *convoyDispatchCircuitBreaker) TryAcquireLease(
	convoyID, issueID, owner, churnFingerprint string,
) (acquired bool, churned bool, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	key := convoyDispatchCircuitKey(convoyID, issueID)
	now := b.now().UTC()
	attempts := 1
	if existing := b.state.Leases[key]; existing != nil {
		if now.Before(existing.ExpiresAt) {
			return false, false, nil
		}
		attempts = existing.Attempts + 1
	}
	if attempts > convoyDispatchChurnLimit {
		delete(b.state.Leases, key)
		b.state.Circuits[key] = &convoyDispatchCircuitEntry{
			ConvoyID:     convoyID,
			IssueID:      issueID,
			Kind:         convoyDispatchAssignmentChurn,
			Fingerprint:  churnFingerprint,
			Error:        fmt.Sprintf("work returned to the ready queue after %d assignment leases", attempts-1),
			LatchedAt:    now,
			AlertPending: true,
		}
		return false, true, b.saveLocked()
	}
	b.state.Leases[key] = &convoyDispatchLease{
		ConvoyID:   convoyID,
		IssueID:    issueID,
		Owner:      owner,
		Attempts:   attempts,
		AcquiredAt: now,
		ExpiresAt:  now.Add(convoyDispatchLeaseDuration),
	}
	return true, false, b.saveLocked()
}

// ReleaseLease allows a failed sling to be retried; successful dispatch keeps
// its lease so stale convoy reads cannot resling the same work.
func (b *convoyDispatchCircuitBreaker) ReleaseLease(convoyID, issueID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := convoyDispatchCircuitKey(convoyID, issueID)
	if b.state.Leases[key] == nil {
		return nil
	}
	delete(b.state.Leases, key)
	return b.saveLocked()
}

func (b *convoyDispatchCircuitBreaker) corruptionAlertWasDelivered() bool {
	var marker struct {
		Fingerprint string `json:"fingerprint"`
	}
	data, err := os.ReadFile(convoyDispatchCorruptionAlertStateFile(b.townRoot)) //nolint:gosec // trusted town root
	if err != nil || json.Unmarshal(data, &marker) != nil {
		return false
	}
	return marker.Fingerprint != "" && marker.Fingerprint == b.loadFingerprint
}

func (b *convoyDispatchCircuitBreaker) saveLocked() error {
	path := convoyDispatchCircuitStateFile(b.townRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b.state.LastUpdated = b.now().UTC()
	data, err := json.MarshalIndent(&b.state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".convoy-dispatch-circuits-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// Record latches a permanent failure. duplicate is true when the exact
// convoy+issue+fingerprint is already persisted.
func (b *convoyDispatchCircuitBreaker) Record(
	convoyID, issueID string,
	kind convoyDispatchFailureKind,
	fingerprint, errText string,
) (duplicate bool, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	key := convoyDispatchCircuitKey(convoyID, issueID)
	if existing := b.state.Circuits[key]; existing != nil &&
		existing.Kind == kind &&
		existing.Fingerprint == fingerprint {
		return true, nil
	}
	b.state.Circuits[key] = &convoyDispatchCircuitEntry{
		ConvoyID:     convoyID,
		IssueID:      issueID,
		Kind:         kind,
		Fingerprint:  fingerprint,
		Error:        errText,
		LatchedAt:    b.now().UTC(),
		AlertPending: true,
	}
	return false, b.saveLocked()
}

func (b *convoyDispatchCircuitBreaker) LoadError() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.loadErr
}

func (b *convoyDispatchCircuitBreaker) CorruptionAlertPending() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.loadErr != nil && !b.loadAlertDelivered
}

func (b *convoyDispatchCircuitBreaker) MarkCorruptionAlertDelivered() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	path := convoyDispatchCorruptionAlertStateFile(b.townRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(struct {
		Fingerprint string    `json:"fingerprint"`
		AlertedAt   time.Time `json:"alerted_at"`
	}{
		Fingerprint: b.loadFingerprint,
		AlertedAt:   b.now().UTC(),
	}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".convoy-dispatch-corruption-alert-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	b.loadAlertDelivered = true
	return nil
}

// FailureKind returns the currently persisted failure class for a convoy issue.
func (b *convoyDispatchCircuitBreaker) FailureKind(convoyID, issueID string) convoyDispatchFailureKind {
	b.mu.Lock()
	defer b.mu.Unlock()
	if entry := b.state.Circuits[convoyDispatchCircuitKey(convoyID, issueID)]; entry != nil {
		return entry.Kind
	}
	return ""
}

func (b *convoyDispatchCircuitBreaker) AlertPending(convoyID, issueID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.state.Circuits[convoyDispatchCircuitKey(convoyID, issueID)]
	return entry != nil && entry.AlertPending
}

func (b *convoyDispatchCircuitBreaker) AlertDetail(convoyID, issueID string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.state.Circuits[convoyDispatchCircuitKey(convoyID, issueID)]
	if entry == nil {
		return ""
	}
	return entry.Error
}

func (b *convoyDispatchCircuitBreaker) MarkAlertDelivered(convoyID, issueID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := convoyDispatchCircuitKey(convoyID, issueID)
	entry := b.state.Circuits[key]
	if entry == nil || !entry.AlertPending {
		return nil
	}
	entry.AlertPending = false
	if err := b.saveLocked(); err != nil {
		entry.AlertPending = true
		return err
	}
	return nil
}

// ShouldSuppress reports whether the relevant state fingerprint is unchanged.
// A changed fingerprint clears the old latch so exactly one new dispatch can
// be attempted against the new state.
func (b *convoyDispatchCircuitBreaker) ShouldSuppress(
	convoyID, issueID, currentFingerprint string,
) (bool, convoyDispatchFailureKind) {
	b.mu.Lock()
	defer b.mu.Unlock()

	key := convoyDispatchCircuitKey(convoyID, issueID)
	entry := b.state.Circuits[key]
	if entry == nil {
		return false, ""
	}
	if entry.Fingerprint == currentFingerprint {
		return true, entry.Kind
	}
	delete(b.state.Circuits, key)
	if err := b.saveLocked(); err != nil {
		// Fail closed: if the reset cannot be persisted, retain the circuit
		// rather than retrying on every daemon scan/restart.
		b.state.Circuits[key] = entry
		return true, entry.Kind
	}
	return false, entry.Kind
}

func classifyPermanentConvoyDispatchError(errText string) convoyDispatchFailureKind {
	if strings.Contains(strings.ToLower(errText), "respawn limit reached for") {
		return convoyDispatchRespawnExhausted
	}
	return ""
}

func classifyUnresolvedConvoyRig(prefix, rig string) convoyDispatchFailureKind {
	if prefix != "" && rig == "" {
		return convoyDispatchRigPrefixUnresolved
	}
	return ""
}

// convoyRouteFingerprint hashes only the route relevant to prefix. Changes to
// unrelated routes must not reopen this circuit.
func convoyRouteFingerprint(townRoot, prefix string) string {
	routes, err := beads.LoadRoutes(filepath.Join(townRoot, ".beads"))
	if err != nil {
		return fmt.Sprintf("route:%s:error:%s", prefix, err)
	}
	for _, route := range routes {
		if route.Prefix == prefix {
			return fmt.Sprintf("route:%s:%s", prefix, route.Path)
		}
	}
	return fmt.Sprintf("route:%s:<missing>", prefix)
}

// convoyRespawnFingerprint includes only this issue's count and configured
// threshold. Resetting the issue or changing the threshold reopens dispatch;
// another issue's respawn activity does not.
func convoyRespawnFingerprint(townRoot, issueID string) string {
	var state struct {
		Beads map[string]struct {
			Count int `json:"count"`
		} `json:"beads"`
	}
	prefix := beads.ExtractPrefix(issueID)
	rig := beads.GetRigNameForPrefix(townRoot, prefix)
	stateRoot := townRoot
	if rig != "" {
		stateRoot = filepath.Join(townRoot, rig)
	}
	data, err := os.ReadFile(filepath.Join(stateRoot, "witness", "bead-respawn-counts.json")) //nolint:gosec // trusted town root
	if err == nil {
		_ = json.Unmarshal(data, &state)
	}
	count := 0
	if rec, ok := state.Beads[issueID]; ok {
		count = rec.Count
	}
	maxRespawns := config.LoadOperationalConfig(townRoot).GetWitnessConfig().MaxBeadRespawnsV()
	return fmt.Sprintf("respawn:%d/%d", count, maxRespawns)
}
