// Package polecat — target_clean.go
//
// Per-bead `target/` clean hook for reused polecats (hq-x0v7v).
//
// Rust polecats accumulate large `target/` directories (30-50 GB each) when a
// worktree is reused across many beads. The accumulated cruft eventually fills
// the disk and crashes the daemon. Cargo's own caches don't get reaped because
// the worktree is technically "always in use".
//
// This file implements a configurable hook: when the daemon reuses an idle
// polecat for a new bead, optionally delete the polecat's `target/` directory
// before the new work attaches. The cleaner is policy-driven so operators can
// pick between always, every-N-beads, or never.
//
// Policy is parsed from a single config string at `polecat.target_clean_policy`:
//   - "per_bead"           (default) clean on every reuse
//   - "every_n_beads:<N>"  clean once every N reuses (N >= 1)
//   - "never"              skip the hook entirely
//
// The counter for `every_n_beads` is persisted alongside the polecat at
// `<polecatDir>/target_clean_counter`. Cleaner state is intentionally
// independent of the agent-bead schema so adding/removing the hook never
// requires a bead migration.
package polecat

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jonbaldie/gastown/internal/config"
)

// TargetCleanPolicy describes when to clean a polecat's target/ on reuse.
type TargetCleanPolicy struct {
	// Mode is one of "per_bead", "every_n_beads", "never".
	Mode string
	// EveryN is the N for every_n_beads. Zero/unset for the other modes.
	EveryN int
}

// Policy mode constants.
const (
	TargetCleanModePerBead     = "per_bead"
	TargetCleanModeEveryNBeads = "every_n_beads"
	TargetCleanModeNever       = "never"
)

// DefaultTargetCleanPolicy returns the default policy (per_bead).
func DefaultTargetCleanPolicy() TargetCleanPolicy {
	return TargetCleanPolicy{Mode: TargetCleanModePerBead}
}

// ParseTargetCleanPolicy parses a config string into a TargetCleanPolicy.
// Empty input returns the default (per_bead). Whitespace is trimmed.
func ParseTargetCleanPolicy(raw string) (TargetCleanPolicy, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return DefaultTargetCleanPolicy(), nil
	}
	switch {
	case s == TargetCleanModePerBead:
		return TargetCleanPolicy{Mode: TargetCleanModePerBead}, nil
	case s == TargetCleanModeNever:
		return TargetCleanPolicy{Mode: TargetCleanModeNever}, nil
	case strings.HasPrefix(s, TargetCleanModeEveryNBeads+":"):
		nStr := strings.TrimPrefix(s, TargetCleanModeEveryNBeads+":")
		n, err := strconv.Atoi(strings.TrimSpace(nStr))
		if err != nil {
			return TargetCleanPolicy{}, fmt.Errorf("invalid target_clean_policy %q: %w", raw, err)
		}
		if n < 1 {
			return TargetCleanPolicy{}, fmt.Errorf("invalid target_clean_policy %q: N must be >= 1", raw)
		}
		return TargetCleanPolicy{Mode: TargetCleanModeEveryNBeads, EveryN: n}, nil
	default:
		return TargetCleanPolicy{}, fmt.Errorf("invalid target_clean_policy %q (expected per_bead, every_n_beads:N, or never)", raw)
	}
}

// String formats the policy back to its config representation.
func (p TargetCleanPolicy) String() string {
	switch p.Mode {
	case TargetCleanModeEveryNBeads:
		return fmt.Sprintf("%s:%d", TargetCleanModeEveryNBeads, p.EveryN)
	case TargetCleanModeNever:
		return TargetCleanModeNever
	default:
		return TargetCleanModePerBead
	}
}

// targetCleanPolicy resolves the per-town policy from settings.
// Falls back to the compiled-in default (per_bead) on any load/parse error so
// the daemon never gets stuck on a malformed config.
func (m *Manager) targetCleanPolicy() TargetCleanPolicy {
	townRoot := filepath.Dir(m.rig.Path)
	settingsPath := config.TownSettingsPath(townRoot)
	settings, err := config.LoadOrCreateTownSettings(settingsPath)
	if err != nil || settings == nil || settings.Polecat == nil {
		return DefaultTargetCleanPolicy()
	}
	policy, err := ParseTargetCleanPolicy(settings.Polecat.TargetCleanPolicy)
	if err != nil {
		return DefaultTargetCleanPolicy()
	}
	return policy
}

// targetCleanCounterFile is the per-polecat counter path. Keeps every_n_beads
// state out of the agent-bead schema and reset-safe across daemon restarts.
func targetCleanCounterFile(polecatDir string) string {
	return filepath.Join(polecatDir, "target_clean_counter")
}

// readTargetCleanCounter returns the persisted counter for this polecat.
// Missing/garbage files return 0 (not an error — first reuse, fresh polecat).
func readTargetCleanCounter(polecatDir string) int {
	data, err := os.ReadFile(targetCleanCounterFile(polecatDir))
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// writeTargetCleanCounter persists the counter. Errors are non-fatal — the
// worst case is one extra/missed clean.
func writeTargetCleanCounter(polecatDir string, n int) error {
	return os.WriteFile(targetCleanCounterFile(polecatDir), []byte(strconv.Itoa(n)), 0o644)
}

// ShouldCleanTarget decides — given a policy and the post-increment counter
// value — whether to clean target/ on this reuse.
//
// Behavior:
//   - per_bead: always true
//   - every_n_beads:N: true when counterAfterIncrement % N == 0
//   - never: always false
func ShouldCleanTarget(policy TargetCleanPolicy, counterAfterIncrement int) bool {
	switch policy.Mode {
	case TargetCleanModeNever:
		return false
	case TargetCleanModeEveryNBeads:
		if policy.EveryN <= 0 {
			return false
		}
		return counterAfterIncrement%policy.EveryN == 0
	default:
		// per_bead and any unknown mode → clean (fail-safe: more cleaning, not less)
		return true
	}
}

// cleanTargetDir deletes <clonePath>/target if it exists.
//
// Returns (bytesFreed, didClean, err). bytesFreed is best-effort (du-style sum
// of regular files inside target/) and may be 0 if walking failed.
//
// Safety rails:
//   - clonePath must be non-empty and absolute (caller should pre-validate it
//     lives under the polecat root, which this function cannot do alone).
//   - missing target/ is a no-op, not an error.
func cleanTargetDir(clonePath string) (int64, bool, error) {
	if clonePath == "" {
		return 0, false, fmt.Errorf("clean target: empty clonePath")
	}
	if !filepath.IsAbs(clonePath) {
		return 0, false, fmt.Errorf("clean target: clonePath must be absolute, got %q", clonePath)
	}
	targetDir := filepath.Join(clonePath, "target")
	info, err := os.Stat(targetDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("stat %s: %w", targetDir, err)
	}
	if !info.IsDir() {
		// Refuse to delete a file named "target" — that's not a build artifact dir.
		return 0, false, fmt.Errorf("%s is not a directory (refusing to clean)", targetDir)
	}

	bytes := dirSizeBytes(targetDir)
	if err := os.RemoveAll(targetDir); err != nil {
		return bytes, false, fmt.Errorf("removing %s: %w", targetDir, err)
	}
	return bytes, true, nil
}

// dirSizeBytes returns the sum of regular file sizes under root. Best-effort:
// permission errors or symlink loops are silently skipped — this is only used
// for logging the freed-disk figure.
func dirSizeBytes(root string) int64 {
	var total int64
	_ = filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total
}

// formatBytes pretty-prints a byte count for human-readable logs.
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n2 := n / unit; n2 >= unit; n2 /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// RunTargetCleanHook is the polecat-reuse cleaner entry point.
//
// Given the polecat's directory (for counter state) and worktree clone path
// (where target/ lives), the policy decides whether to clean. Counter is
// always incremented and persisted; the clean only fires when policy permits.
//
// Returns a human-readable log line ("" if no action taken) plus any error.
// Errors from the clean step are returned but should be treated as warnings
// by the caller — failure to clean must NOT block the reuse path.
func RunTargetCleanHook(polecatDir, clonePath string, policy TargetCleanPolicy) (string, error) {
	if policy.Mode == TargetCleanModeNever {
		return "", nil
	}

	counter := readTargetCleanCounter(polecatDir) + 1
	// Persist the bumped counter unconditionally so every_n_beads stays accurate.
	if err := writeTargetCleanCounter(polecatDir, counter); err != nil {
		// Counter persistence is best-effort; continue with the clean decision.
		_ = err
	}

	if !ShouldCleanTarget(policy, counter) {
		return "", nil
	}

	bytes, didClean, err := cleanTargetDir(clonePath)
	if err != nil {
		return "", err
	}
	if !didClean {
		// target/ didn't exist — common on first-ever reuse / non-Rust rigs.
		return "", nil
	}
	return fmt.Sprintf("target-clean: removed %s (%s freed, policy=%s, counter=%d)",
		filepath.Join(clonePath, "target"), formatBytes(bytes), policy.String(), counter), nil
}
