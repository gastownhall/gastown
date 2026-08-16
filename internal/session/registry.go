// Package session provides polecat session lifecycle management.
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"regexp"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/tmux"
)

// PrefixRegistry maps beads prefixes to rig names and vice versa.
// Used to resolve session names that use rig-specific prefixes.
type PrefixRegistry struct {
	mu          sync.RWMutex
	prefixToRig map[string]string // "gt" → "gastown"
	rigToPrefix map[string]string // "gastown" → "gt"
}

// NewPrefixRegistry creates an empty prefix registry.
func NewPrefixRegistry() *PrefixRegistry {
	return &PrefixRegistry{
		prefixToRig: make(map[string]string),
		rigToPrefix: make(map[string]string),
	}
}

// Register adds a prefix↔rig mapping.
func (r *PrefixRegistry) Register(prefix, rigName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prefixToRig[prefix] = rigName
	r.rigToPrefix[rigName] = prefix
}

// RigForPrefix returns the rig name for a given prefix.
// Returns the prefix itself if no mapping is found.
func (r *PrefixRegistry) RigForPrefix(prefix string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if rig, ok := r.prefixToRig[prefix]; ok {
		return rig
	}
	return prefix
}

// PrefixForRig returns the beads prefix for a given rig name.
// Returns DefaultPrefix if no mapping is found.
func (r *PrefixRegistry) PrefixForRig(rigName string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if prefix, ok := r.rigToPrefix[rigName]; ok {
		return prefix
	}
	return DefaultPrefix
}

// AllRigs returns a copy of the rig-name → prefix mapping for all registered rigs.
// Callers can iterate it to find known rig names embedded in session strings.
func (r *PrefixRegistry) AllRigs() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]string, len(r.rigToPrefix))
	for rig, prefix := range r.rigToPrefix {
		out[rig] = prefix
	}
	return out
}

// Prefixes returns all registered prefixes, sorted longest-first for matching.
func (r *PrefixRegistry) Prefixes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	prefixes := make([]string, 0, len(r.prefixToRig))
	for p := range r.prefixToRig {
		prefixes = append(prefixes, p)
	}
	sort.Slice(prefixes, func(i, j int) bool {
		return len(prefixes[i]) > len(prefixes[j])
	})
	return prefixes
}

// defaultRegistry is the package-level registry used by convenience functions.
// Access is protected by defaultRegistryMu for concurrent test safety.
var (
	defaultRegistry   = NewPrefixRegistry()
	defaultRegistryMu sync.RWMutex
)

// DefaultRegistry returns the package-level prefix registry.
func DefaultRegistry() *PrefixRegistry {
	defaultRegistryMu.RLock()
	defer defaultRegistryMu.RUnlock()
	return defaultRegistry
}

// SetDefaultRegistry replaces the package-level prefix registry.
func SetDefaultRegistry(r *PrefixRegistry) {
	defaultRegistryMu.Lock()
	defaultRegistry = r
	defaultRegistryMu.Unlock()
}

// InitRegistry populates the default registry from the town's rigs.json and
// loads the agent registry from settings/agents.json.
// Both registries are loaded independently — a failure in one does not
// prevent the other from loading.
// Should be called early in the process lifecycle.
// Safe to call multiple times; later calls replace earlier data.
func InitRegistry(townRoot string) error {
	var errs []error

	// Determine the tmux socket name from GT_TMUX_SOCKET env var:
	//   unset / "default" / "auto" → per-town socket derived from town directory path
	//   any other value            → use that name as-is
	socket := os.Getenv("GT_TMUX_SOCKET")
	switch socket {
	case "", "default", "auto":
		socket = townSocketName(townRoot)
	}
	tmux.SetDefaultSocket(socket)

	r, err := BuildPrefixRegistryFromTown(townRoot)
	if err != nil {
		errs = append(errs, fmt.Errorf("prefix registry: %w", err))
	} else {
		SetDefaultRegistry(r)
	}

	// Load agent registry so all entry points (CLI, daemon, witness) respect
	// user-configured overrides like custom process_names.
	if err := config.LoadAgentRegistry(config.DefaultAgentRegistryPath(townRoot)); err != nil {
		errs = append(errs, fmt.Errorf("agent registry: %w", err))
	}

	return errors.Join(errs...)
}

// sanitizeRe matches non-alphanumeric, non-hyphen characters.
var sanitizeRe = regexp.MustCompile(`[^a-z0-9-]+`)

// sanitizeTownName cleans a town name to be a valid tmux socket name.
// Lowercases, replaces non-alphanumeric characters with hyphens, trims hyphens.
// townSocketName derives a unique tmux socket name from the full town path.
// Uses the directory basename plus a short hash of the canonical path to ensure
// uniqueness even when two towns share the same basename (e.g., ~/gt and ~/work/gt).
// Format: "basename-hash6" (e.g., "gt-a1b2c3").
func townSocketName(townRoot string) string {
	base := sanitizeTownName(filepath.Base(townRoot))

	// Resolve symlinks and get absolute path for a canonical representation.
	canonical, err := filepath.EvalSymlinks(townRoot)
	if err != nil {
		canonical, err = filepath.Abs(townRoot)
		if err != nil {
			canonical = townRoot
		}
	}

	h := sha256.Sum256([]byte(canonical))
	suffix := hex.EncodeToString(h[:3]) // 6 hex chars = 3 bytes
	return base + "-" + suffix
}

// LegacySocketName returns the old-format socket name (basename only, no hash)
// used before path-based socket derivation was added. Used by gt down to clean
// up sessions orphaned on the old socket during migration.
func LegacySocketName(townRoot string) string {
	return sanitizeTownName(filepath.Base(townRoot))
}

func sanitizeTownName(name string) string {
	name = strings.ToLower(name)
	name = sanitizeRe.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	if name == "" {
		return "default"
	}
	return name
}

// PrefixFor returns the beads prefix for a rig, using the default registry.
// Returns DefaultPrefix if the rig is unknown.
func PrefixFor(rigName string) string {
	return DefaultRegistry().PrefixForRig(rigName)
}

// BuildPrefixRegistryFromTown reads rigs.json and returns a populated PrefixRegistry.
// Checks mayor/rigs.json first (canonical), then falls back to town-root rigs.json.
// Warns to stderr if rigs.json is missing entirely — an empty registry causes
// silent failures in session name parsing (crew cycling, nudge routing, etc.).
func BuildPrefixRegistryFromTown(townRoot string) (*PrefixRegistry, error) {
	// Canonical location: inside mayor worktree.
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	fallbackPath := filepath.Join(townRoot, "rigs.json")
	if _, err := os.Stat(rigsPath); err == nil {
		r, err := BuildPrefixRegistryFromFile(rigsPath)
		if err == nil {
			// Maintain fallback copy at town root (resilient to git ops in mayor/).
			copyFileIfNewer(rigsPath, fallbackPath)
		}
		return r, err
	}

	// Fallback: town root (safe from git operations in mayor worktree).
	if _, err := os.Stat(fallbackPath); err == nil {
		style.PrintWarning("mayor/rigs.json missing, using fallback %s", fallbackPath)
		return BuildPrefixRegistryFromFile(fallbackPath)
	}

	// No rigs.json found anywhere — warn loudly.
	style.PrintWarning("rigs.json not found (checked mayor/rigs.json and town root). " +
		"PrefixRegistry is empty — session parsing will fail. " +
		"Run 'gt doctor' or restore rigs.json.")
	return NewPrefixRegistry(), nil
}

// rigsJSON is the minimal structure for reading rigs.json prefix data.
type rigsJSON struct {
	Rigs map[string]rigEntry `json:"rigs"`
}

type rigEntry struct {
	Beads *beadsEntry `json:"beads,omitempty"`
}

type beadsEntry struct {
	Prefix string `json:"prefix"`
}

// BuildPrefixRegistryFromFile reads a rigs.json file and returns a PrefixRegistry.
func BuildPrefixRegistryFromFile(path string) (*PrefixRegistry, error) {
	r := NewPrefixRegistry()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return r, nil
		}
		return nil, err
	}

	var rigs rigsJSON
	if err := json.Unmarshal(data, &rigs); err != nil {
		return nil, err
	}

	for rigName, entry := range rigs.Rigs {
		if entry.Beads != nil && entry.Beads.Prefix != "" {
			r.Register(entry.Beads.Prefix, rigName)
		}
	}

	return r, nil
}

// LegacyPrefixes are prefixes accepted as valid even when the registry is empty.
// gt = default rig, bd = beads, hq = town-level HQ services, gthq = gastown HQ.
var LegacyPrefixes = []string{"gt", "bd", "hq", "gthq"}

// HasKnownPrefix returns true if s starts with a registered or legacy prefix
// followed by "-". Use this instead of hand-rolling prefix checks so that
// all call-sites agree on what constitutes a valid prefix.
func HasKnownPrefix(s string) bool {
	if DefaultRegistry().HasPrefix(s) {
		return true
	}
	for _, p := range LegacyPrefixes {
		if strings.HasPrefix(s, p+"-") {
			return true
		}
	}
	return false
}

// HasPrefix returns true if the session name starts with a registered prefix followed by a dash.
func (r *PrefixRegistry) HasPrefix(sess string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for p := range r.prefixToRig {
		if strings.HasPrefix(sess, p+"-") {
			return true
		}
	}
	return false
}

// IsKnownSession returns true if the session name belongs to Gas Town.
// Checks for HQ prefix and registered rig prefixes from the default registry.
func IsKnownSession(sess string) bool {
	if strings.HasPrefix(sess, HQPrefix) {
		return true
	}
	return DefaultRegistry().HasPrefix(sess)
}

// matchPrefix finds the prefix in a session name suffix using the registry.
// Returns the prefix and the remaining string after the prefix dash.
// Tries longest prefix match first.
// Only matches sessions with registered prefixes - does NOT fall back to
// splitting on dashes, as that would incorrectly match non-gastown sessions
// (e.g., "gs-1923" or "dotfiles-main" would be parsed as gastown sessions).
func (r *PrefixRegistry) matchPrefix(session string) (prefix, rest string, matched bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Try known prefixes, longest first
	for _, p := range r.sortedPrefixes() {
		candidate := p + "-"
		if strings.HasPrefix(session, candidate) {
			return p, session[len(candidate):], true
		}
	}

	return "", "", false
}

// sortedPrefixes returns prefixes sorted longest-first (must hold read lock).
func (r *PrefixRegistry) sortedPrefixes() []string {
	prefixes := make([]string, 0, len(r.prefixToRig))
	for p := range r.prefixToRig {
		prefixes = append(prefixes, p)
	}
	sort.Slice(prefixes, func(i, j int) bool {
		return len(prefixes[i]) > len(prefixes[j])
	})
	return prefixes
}

// copyFileIfNewer copies src to dst if src is newer or dst doesn't exist.
// Errors are silently ignored — this is a best-effort resilience mechanism.
func copyFileIfNewer(src, dst string) {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return
	}
	if dstInfo, err := os.Stat(dst); err == nil {
		if !srcInfo.ModTime().After(dstInfo.ModTime()) {
			return // dst is up to date
		}
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return
	}
	_ = os.Rename(tmp, dst)
}
