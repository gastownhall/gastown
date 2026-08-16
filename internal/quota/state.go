// Package quota manages Claude Code account quota rotation for Gas Town.
//
// When sessions hit rate limits, the overseer can scan for blocked sessions
// and rotate them to available accounts. State is persisted to mayor/quota.json
// with crash-safe atomic writes and file-level locking.
package quota

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/jonbaldie/gastown/internal/atomicfile"
	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/constants"
	"github.com/jonbaldie/gastown/internal/util"
)

// Manager handles quota state persistence with file locking.
type Manager struct {
	townRoot string
}

// NewManager creates a new quota manager for the given town root.
func NewManager(townRoot string) *Manager {
	return &Manager{townRoot: townRoot}
}

// statePath returns the path to quota.json.
func (m *Manager) statePath() string {
	return constants.MayorQuotaPath(m.townRoot)
}

// lockPath returns the path to the flock file for quota state.
func (m *Manager) lockPath() string {
	return filepath.Join(m.townRoot, constants.DirMayor, constants.DirRuntime, "quota.lock")
}

// lock acquires an exclusive file lock for quota state operations.
// Caller must defer unlock().
func (m *Manager) lock() (func(), error) {
	lockDir := filepath.Dir(m.lockPath())
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		return nil, fmt.Errorf("creating quota lock dir: %w", err)
	}
	fl := flock.New(m.lockPath())
	if err := fl.Lock(); err != nil {
		return nil, fmt.Errorf("acquiring quota lock: %w", err)
	}
	return func() { _ = fl.Unlock() }, nil
}

// Load reads the quota state from disk. Returns an empty state if the file
// doesn't exist yet (first run).
func (m *Manager) Load() (*config.QuotaState, error) {
	data, err := os.ReadFile(m.statePath())
	if os.IsNotExist(err) {
		return &config.QuotaState{
			Version:  config.CurrentQuotaVersion,
			Accounts: make(map[string]config.AccountQuotaState),
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading quota state: %w", err)
	}

	var state config.QuotaState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parsing quota state: %w", err)
	}
	if state.Accounts == nil {
		state.Accounts = make(map[string]config.AccountQuotaState)
	}
	return &state, nil
}

// Save writes the quota state to disk atomically with file locking.
func (m *Manager) Save(state *config.QuotaState) error {
	unlock, err := m.lock()
	if err != nil {
		return err
	}
	defer unlock()

	state.Version = config.CurrentQuotaVersion
	return atomicfile.EnsureDirAndWriteJSON(m.statePath(), state)
}

// WithLock acquires the quota file lock, runs fn, then releases the lock.
// Use this to hold the lock across multiple Load/SaveUnlocked calls,
// eliminating TOCTOU races in multi-step operations like rotation.
func (m *Manager) WithLock(fn func() error) error {
	unlock, err := m.lock()
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

// SaveUnlocked writes the quota state to disk without acquiring the lock.
// The caller MUST already hold the lock via WithLock. Using this outside
// of WithLock will corrupt state under concurrent access.
func (m *Manager) SaveUnlocked(state *config.QuotaState) error {
	state.Version = config.CurrentQuotaVersion
	return atomicfile.EnsureDirAndWriteJSON(m.statePath(), state)
}

// MarkLimited marks an account as rate-limited with an optional reset time.
func (m *Manager) MarkLimited(handle string, resetsAt string) error {
	unlock, err := m.lock()
	if err != nil {
		return err
	}
	defer unlock()

	state, err := m.Load()
	if err != nil {
		return err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	state.Accounts[handle] = config.AccountQuotaState{
		Status:    config.QuotaStatusLimited,
		LimitedAt: now,
		ResetsAt:  resetsAt,
		LastUsed:  state.Accounts[handle].LastUsed,
	}

	return atomicfile.EnsureDirAndWriteJSON(m.statePath(), state)
}

// MarkAvailable marks an account as available (not rate-limited).
func (m *Manager) MarkAvailable(handle string) error {
	unlock, err := m.lock()
	if err != nil {
		return err
	}
	defer unlock()

	state, err := m.Load()
	if err != nil {
		return err
	}

	existing := state.Accounts[handle]
	state.Accounts[handle] = config.AccountQuotaState{
		Status:   config.QuotaStatusAvailable,
		LastUsed: existing.LastUsed,
	}

	return atomicfile.EnsureDirAndWriteJSON(m.statePath(), state)
}

// AvailableAccounts returns account handles that are not rate-limited,
// sorted by least-recently-used first.
func (m *Manager) AvailableAccounts(state *config.QuotaState) []string {
	var available []string
	for handle, acctState := range state.Accounts {
		if acctState.Status == config.QuotaStatusAvailable || acctState.Status == "" {
			available = append(available, handle)
		}
	}
	// Sort by LastUsed ascending (least recently used first)
	sortByLastUsed(available, state)
	return available
}

// LimitedAccounts returns account handles that are currently rate-limited.
func (m *Manager) LimitedAccounts(state *config.QuotaState) []string {
	var limited []string
	for handle, acctState := range state.Accounts {
		if acctState.Status == config.QuotaStatusLimited {
			limited = append(limited, handle)
		}
	}
	return limited
}

// sortByLastUsed sorts handles by their LastUsed timestamp ascending.
func sortByLastUsed(handles []string, state *config.QuotaState) {
	// Simple insertion sort — handles list is small (3-5 accounts)
	for i := 1; i < len(handles); i++ {
		key := handles[i]
		j := i - 1
		for j >= 0 && state.Accounts[handles[j]].LastUsed > state.Accounts[key].LastUsed {
			handles[j+1] = handles[j]
			j--
		}
		handles[j+1] = key
	}
}

// EnsureAccountsTracked adds any registered accounts that are missing from
// quota state. Called during scan to keep state in sync with accounts.json.
func (m *Manager) EnsureAccountsTracked(state *config.QuotaState, accounts map[string]config.Account) {
	for handle := range accounts {
		if _, exists := state.Accounts[handle]; !exists {
			state.Accounts[handle] = config.AccountQuotaState{
				Status: config.QuotaStatusAvailable,
			}
		}
	}
}

// RecordSwap records a keychain swap mapping in quota state.
// targetConfigDir is the config dir whose keychain entry was overwritten.
// sourceHandle is the account handle whose token was swapped in.
// The caller must hold the quota lock or call this within WithLock.
func RecordSwap(state *config.QuotaState, targetConfigDir, sourceHandle string) {
	if state.ActiveSwaps == nil {
		state.ActiveSwaps = make(map[string]string)
	}
	state.ActiveSwaps[targetConfigDir] = sourceHandle
}

// ClearSwap removes a swap mapping when the config dir is no longer swapped.
// The caller must hold the quota lock or call this within WithLock.
func ClearSwap(state *config.QuotaState, targetConfigDir string) {
	delete(state.ActiveSwaps, targetConfigDir)
}

// ResolveSwapSourceDirs resolves activeSwaps (targetConfigDir -> accountHandle)
// to targetConfigDir -> sourceConfigDir using the accounts config.
func ResolveSwapSourceDirs(activeSwaps map[string]string, accounts map[string]config.Account) map[string]string {
	resolved := make(map[string]string, len(activeSwaps))
	for targetDir, handle := range activeSwaps {
		acct, ok := accounts[handle]
		if !ok {
			continue
		}
		resolved[targetDir] = util.ExpandHome(acct.ConfigDir)
	}
	return resolved
}

// ClearExpired checks all limited accounts and marks them available if their
// ResetsAt time has passed. Returns the number of accounts cleared.
// The caller is responsible for persisting state if changes were made.
func (m *Manager) ClearExpired(state *config.QuotaState) int {
	return clearExpiredAt(m, state, time.Now())
}

// clearExpiredAt is the testable core of ClearExpired, accepting a reference time.
func clearExpiredAt(_ *Manager, state *config.QuotaState, now time.Time) int {
	cleared := 0
	for handle, acctState := range state.Accounts {
		if acctState.Status != config.QuotaStatusLimited {
			continue
		}
		if acctState.ResetsAt == "" {
			continue
		}
		resetTime, err := ParseResetTime(acctState.ResetsAt, now)
		if err != nil {
			continue // can't parse — leave as-is
		}
		if now.After(resetTime) {
			state.Accounts[handle] = config.AccountQuotaState{
				Status:   config.QuotaStatusAvailable,
				LastUsed: acctState.LastUsed,
			}
			cleared++
		}
	}
	return cleared
}

// parseResetTimePattern matches formats like "7pm", "11am", "3:30pm", "7:00pm"
var parseResetTimePattern = regexp.MustCompile(`(?i)^(\d{1,2})(?::(\d{2}))?\s*(am|pm)\b`)

// ParseResetTime parses a human-readable reset time string into a time.Time.
// Supported formats:
//
//	"7pm (America/Los_Angeles)" → today at 7pm in that timezone
//	"11am (America/Los_Angeles)" → today at 11am in that timezone
//	"3:30pm (America/Los_Angeles)" → today at 3:30pm in that timezone
//	"7pm" → today at 7pm in local timezone
//
// The reference time is used to determine "today".
func ParseResetTime(resetsAt string, reference time.Time) (time.Time, error) {
	resetsAt = strings.TrimSpace(resetsAt)

	// Extract timezone if present: "7pm (America/Los_Angeles)" or "7pm"
	loc := reference.Location()
	if idx := strings.Index(resetsAt, "("); idx != -1 {
		end := strings.Index(resetsAt, ")")
		if end > idx {
			tzName := strings.TrimSpace(resetsAt[idx+1 : end])
			parsed, err := time.LoadLocation(tzName)
			if err == nil {
				loc = parsed
			}
			resetsAt = strings.TrimSpace(resetsAt[:idx])
		}
	}

	// Parse the time portion: "7pm", "11am", "3:30pm"
	m := parseResetTimePattern.FindStringSubmatch(resetsAt)
	if len(m) < 4 {
		return time.Time{}, fmt.Errorf("cannot parse reset time: %q", resetsAt)
	}

	hour := 0
	fmt.Sscanf(m[1], "%d", &hour)
	minute := 0
	if m[2] != "" {
		fmt.Sscanf(m[2], "%d", &minute)
	}

	ampm := strings.ToLower(m[3])
	if ampm == "pm" && hour != 12 {
		hour += 12
	} else if ampm == "am" && hour == 12 {
		hour = 0
	}

	// Build the reset time using today's date in the target timezone
	refInLoc := reference.In(loc)
	resetTime := time.Date(refInLoc.Year(), refInLoc.Month(), refInLoc.Day(),
		hour, minute, 0, 0, loc)

	return resetTime, nil
}
