package polecat

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/steveyegge/gastown/internal/atomicfile"
	"github.com/steveyegge/gastown/internal/lock"
)

const (
	// DefaultPoolSize is the number of name slots in the pool.
	// Names are allocated when a polecat is first created. Polecat identity persists,
	// while clean completion retires the live session and cleanup owns remaining state.
	DefaultPoolSize = 50

	// DefaultTheme is the default theme for new rigs.
	DefaultTheme = "mad-max"

	// MaxThemeNames is the maximum number of names allowed in a custom theme file.
	// Prevents accidental theme bloat from large --from-file inputs.
	MaxThemeNames = 2000
)

// ReservedInfraAgentNames contains names reserved for infrastructure agents.
// These names must never be allocated to polecats.
var ReservedInfraAgentNames = map[string]bool{
	"witness":  true,
	"mayor":    true,
	"deacon":   true,
	"refinery": true,
	"crew":     true,
	"polecats": true,
}

// Built-in themes with themed polecat names.
var BuiltinThemes = map[string][]string{
	"mad-max": {
		"furiosa", "nux", "slit", "rictus", "dementus",
		"capable", "toast", "dag", "cheedo", "valkyrie",
		"keeper", "morsov", "ace", "warboy", "imperator",
		"organic", "coma", "splendid", "angharad", "max",
		"immortan", "bullet", "toecutter", "goose", "nightrider",
		"glory", "scrotus", "chumbucket", "corpus", "dinki",
		"prime", "vuvalini", "rockryder", "wretched", "buzzard",
		"gastown", "bullet-farmer", "citadel", "wasteland", "fury",
		"road-warrior", "interceptor", "blackfinger", "wraith", "witness",
		"chrome", "shiny", "mediocre", "guzzoline", "aqua-cola",
	},
	"minerals": {
		"obsidian", "quartz", "jasper", "onyx", "opal",
		"topaz", "garnet", "ruby", "amber", "jade",
		"pearl", "flint", "granite", "basalt", "marble",
		"shale", "slate", "pyrite", "mica", "agate",
		"malachite", "turquoise", "lapis", "emerald", "sapphire",
		"diamond", "amethyst", "citrine", "zircon", "peridot",
		"coral", "jet", "moonstone", "sunstone", "bloodstone",
		"rhodonite", "sodalite", "hematite", "magnetite", "calcite",
		"fluorite", "selenite", "kyanite", "labradorite", "amazonite",
		"chalcedony", "carnelian", "aventurine", "chrysoprase", "heliodor",
	},
	"wasteland": {
		"rust", "chrome", "nitro", "guzzle", "witness",
		"shiny", "fury", "thunder", "dust", "scavenger",
		"radrat", "ghoul", "mutant", "raider", "vault",
		"pipboy", "nuka", "brahmin", "deathclaw", "mirelurk",
		"synth", "institute", "enclave", "brotherhood", "minuteman",
		"railroad", "atom", "crater", "foundation", "refuge",
		"settler", "wanderer", "courier", "lone", "chosen",
		"tribal", "khan", "legion", "ncr", "ranger",
		"overseer", "sentinel", "paladin", "scribe", "initiate",
		"elder", "lancer", "knight", "squire", "proctor",
	},
}

// NamePool manages a bounded pool of reusable polecat names.
// Names are allocated once per polecat and persist across assignments in the
// persistent polecat model (gt-4ac). A name slot is only freed when a polecat
// is explicitly nuked.
//
// Names are drawn from a themed pool (mad-max by default).
// When the pool is exhausted, overflow names use N format (just numbers).
// The rig prefix is added by SessionName to create session names like "gt-<rig>-N".
type NamePool struct {
	mu sync.RWMutex

	// RigName is the rig this pool belongs to.
	RigName string `json:"rig_name"`

	// Theme is the current theme name (e.g., "mad-max", "minerals", or a custom theme).
	Theme string `json:"theme"`

	// CustomNames allows overriding the built-in theme names.
	CustomNames []string `json:"custom_names,omitempty"`

	// InUse tracks which pool names are currently in use.
	// Key is the name itself, value is true if in use.
	// ZFC: This is transient state derived from filesystem via Reconcile().
	// Never persist - always discover from existing polecat directories.
	InUse map[string]bool `json:"-"`

	// OverflowNext is the next overflow sequence number.
	// Starts at MaxSize+1 and increments.
	OverflowNext int `json:"overflow_next"`

	// MaxSize is the maximum number of themed names before overflow.
	MaxSize int `json:"max_size"`

	// stateFile is the path to persist pool state.
	stateFile string

	// townRoot is the town root directory, used to resolve custom theme files.
	townRoot string
}

// NewNamePool creates a new name pool for a rig.
func NewNamePool(rigPath, rigName string) *NamePool {
	return &NamePool{
		RigName:      rigName,
		Theme:        ThemeForRig(rigName),
		InUse:        make(map[string]bool),
		OverflowNext: DefaultPoolSize + 1,
		MaxSize:      DefaultPoolSize,
		stateFile:    filepath.Join(rigPath, ".runtime", "namepool-state.json"),
	}
}

// NewNamePoolWithConfig creates a name pool with specific configuration.
func NewNamePoolWithConfig(rigPath, rigName, theme string, customNames []string, maxSize int) *NamePool {
	if theme == "" {
		theme = DefaultTheme
	}
	if maxSize <= 0 {
		maxSize = DefaultPoolSize
	}

	return &NamePool{
		RigName:      rigName,
		Theme:        theme,
		CustomNames:  customNames,
		InUse:        make(map[string]bool),
		OverflowNext: maxSize + 1,
		MaxSize:      maxSize,
		stateFile:    filepath.Join(rigPath, ".runtime", "namepool-state.json"),
	}
}

// SetTownRoot sets the town root for custom theme resolution.
func (p *NamePool) SetTownRoot(townRoot string) {
	p.townRoot = townRoot
}

// getNames returns the list of names to use for the pool.
// Reserved infrastructure agent names are filtered out.
func (p *NamePool) getNames() []string {
	var names []string

	// Custom names take precedence
	if len(p.CustomNames) > 0 {
		names = p.CustomNames
	} else if themeNames, ok := BuiltinThemes[p.Theme]; ok {
		// Look up built-in theme
		names = themeNames
	} else if p.townRoot != "" {
		// Try resolving as a custom theme file
		if resolved, err := ResolveThemeNames(p.townRoot, p.Theme); err == nil {
			names = resolved
		} else {
			fmt.Fprintf(os.Stderr, "Warning: namepool theme %q not found (not built-in, no custom theme file); using default\n", p.Theme)
			names = BuiltinThemes[DefaultTheme]
		}
	} else {
		// Fall back to default theme
		names = BuiltinThemes[DefaultTheme]
	}

	// Filter out reserved infrastructure agent names
	return filterReservedNames(names)
}

// filterReservedNames removes reserved infrastructure agent names from a name list.
func filterReservedNames(names []string) []string {
	filtered := make([]string, 0, len(names))
	for _, name := range names {
		if !ReservedInfraAgentNames[name] {
			filtered = append(filtered, name)
		}
	}
	return filtered
}

// Load loads the pool state from disk.
func (p *NamePool) Load() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	data, err := os.ReadFile(p.stateFile)
	if err != nil {
		if os.IsNotExist(err) {
			// Initialize with empty state
			p.InUse = make(map[string]bool)
			p.OverflowNext = p.MaxSize + 1
			return nil
		}
		return err
	}

	// Load only runtime state - Theme and CustomNames come from settings/config.json.
	// ZFC: InUse is NEVER loaded from disk - it's transient state derived
	// from filesystem via Reconcile(). Always start with empty map.
	var loaded namePoolState
	if err := json.Unmarshal(data, &loaded); err != nil {
		return err
	}

	p.InUse = make(map[string]bool)

	p.OverflowNext = loaded.OverflowNext
	if p.OverflowNext < p.MaxSize+1 {
		p.OverflowNext = p.MaxSize + 1
	}
	if loaded.MaxSize > 0 {
		p.MaxSize = loaded.MaxSize
	}

	return nil
}

// namePoolState is the subset of NamePool that is persisted to the state file.
// Only runtime state is saved, not configuration (Theme, CustomNames come from settings).
type namePoolState struct {
	RigName      string `json:"rig_name"`
	OverflowNext int    `json:"overflow_next"`
	MaxSize      int    `json:"max_size"`
}

// Save persists the pool state to disk using atomic write.
// Only runtime state (OverflowNext, MaxSize) is saved - configuration like
// Theme and CustomNames come from settings/config.json and are not persisted here.
func (p *NamePool) Save() error {
	p.mu.RLock()
	defer p.mu.RUnlock()

	dir := filepath.Dir(p.stateFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// Only save runtime state, not configuration
	state := namePoolState{
		RigName:      p.RigName,
		OverflowNext: p.OverflowNext,
		MaxSize:      p.MaxSize,
	}

	return atomicfile.WriteJSON(p.stateFile, state)
}

// Allocate returns a name from the pool.
// It prefers names in order from the theme list, and falls back to overflow names
// when the pool is exhausted.
func (p *NamePool) Allocate() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	names := p.getNames()

	// Try to find first available name from the theme
	for i := 0; i < len(names) && i < p.MaxSize; i++ {
		name := names[i]
		if !p.InUse[name] {
			p.InUse[name] = true
			return name, nil
		}
	}

	// Pool exhausted, use overflow naming
	name := p.formatOverflowName(p.OverflowNext)
	p.OverflowNext++
	return name, nil
}

// Release returns a name slot to the available pool.
// Called when a polecat is nuked - the name becomes available for new polecats.
// NOTE: This releases the NAME, not the polecat. The polecat is gone (nuked).
// For overflow names, this is a no-op (they are not reusable).
func (p *NamePool) Release(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Check if it's a themed name
	if p.isThemedName(name) {
		delete(p.InUse, name)
	}
	// Overflow names are not reusable, so we don't track them
}

// isThemedName checks if a name is in the theme pool.
func (p *NamePool) isThemedName(name string) bool {
	names := p.getNames()
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// IsPoolName returns true if the name is a pool name (themed or numbered).
func (p *NamePool) IsPoolName(name string) bool {
	return p.isThemedName(name)
}

// ActiveCount returns the number of names currently in use from the pool.
func (p *NamePool) ActiveCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.InUse)
}

// ActiveNames returns a sorted list of names currently in use from the pool.
func (p *NamePool) ActiveNames() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var names []string
	for name := range p.InUse {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// MarkInUse marks a name as in use (for reconciling with existing polecats).
func (p *NamePool) MarkInUse(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.isThemedName(name) {
		p.InUse[name] = true
	}
}

// Reconcile updates the pool state based on existing polecat directories.
// This should be called on startup to sync pool state with reality.
func (p *NamePool) Reconcile(existingPolecats []string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Clear current state
	p.InUse = make(map[string]bool)

	// Mark all existing polecats as in use
	for _, name := range existingPolecats {
		if p.isThemedName(name) {
			p.InUse[name] = true
			continue
		}
		if seq, err := strconv.Atoi(name); err == nil && seq >= p.OverflowNext {
			p.OverflowNext = seq + 1
		}
	}
}

// formatOverflowName formats an overflow sequence number as a name.
// Returns just the number (e.g., "51") since SessionName will add the rig prefix.
// This prevents double-prefix bugs like "gt-gastown_manager-gastown_manager-51".
func (p *NamePool) formatOverflowName(seq int) string {
	return fmt.Sprintf("%d", seq)
}

// GetTheme returns the current theme name.
func (p *NamePool) GetTheme() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.Theme
}

// SetTheme sets the theme and resets the pool.
// Existing in-use names are preserved if they exist in the new theme.
// Supports both built-in themes and custom theme files in settings/themes/.
func (p *NamePool) SetTheme(theme string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var newNames []string
	if names, ok := BuiltinThemes[theme]; ok {
		newNames = names
	} else if p.townRoot != "" {
		resolved, err := ResolveThemeNames(p.townRoot, theme)
		if err != nil {
			return err
		}
		newNames = resolved
	} else {
		return fmt.Errorf("unknown theme: %s (use 'gt namepool themes' to list available themes)", theme)
	}

	// Preserve names that exist in both themes
	newInUse := make(map[string]bool)
	for name := range p.InUse {
		for _, n := range newNames {
			if n == name {
				newInUse[name] = true
				break
			}
		}
	}

	p.Theme = theme
	p.InUse = newInUse
	p.CustomNames = nil
	return nil
}

// ListThemes returns the list of available built-in themes.
func ListThemes() []string {
	themes := make([]string, 0, len(BuiltinThemes))
	for theme := range BuiltinThemes {
		themes = append(themes, theme)
	}
	sort.Strings(themes)
	return themes
}

// ThemeForRig returns a deterministic theme for a rig based on its name.
// This provides variety across rigs without requiring manual configuration.
func ThemeForRig(rigName string) string {
	themes := ListThemes()
	if len(themes) == 0 {
		return DefaultTheme
	}
	// Hash using prime multiplier for better distribution
	var hash uint32
	for _, b := range []byte(rigName) {
		hash = hash*31 + uint32(b)
	}
	return themes[hash%uint32(len(themes))] //nolint:gosec // len(themes) is small constant
}

// ThemeForRigAvoiding picks a theme for rigName that is not already in usedThemes.
// This ensures polecat names are unique across rigs by giving each rig a different theme.
// If all built-in themes are taken, falls back to the hash-based ThemeForRig result.
func ThemeForRigAvoiding(rigName string, usedThemes []string) string {
	themes := ListThemes()
	if len(themes) == 0 {
		return DefaultTheme
	}

	used := make(map[string]bool, len(usedThemes))
	for _, t := range usedThemes {
		used[t] = true
	}

	// Try to find an unused theme
	var available []string
	for _, t := range themes {
		if !used[t] {
			available = append(available, t)
		}
	}

	if len(available) == 0 {
		// All built-in themes taken — fall back to hash-based selection
		return ThemeForRig(rigName)
	}

	if len(available) == 1 {
		return available[0]
	}

	// Deterministic pick from available themes using rig name hash
	var hash uint32
	for _, b := range []byte(rigName) {
		hash = hash*31 + uint32(b)
	}
	return available[hash%uint32(len(available))] //nolint:gosec // len(available) is small
}

// GetThemeNames returns the names in a specific built-in theme.
// For custom themes, use ResolveThemeNames instead.
func GetThemeNames(theme string) ([]string, error) {
	if names, ok := BuiltinThemes[theme]; ok {
		return names, nil
	}
	return nil, fmt.Errorf("unknown theme: %s", theme)
}

// ThemeInfo describes a theme (built-in or custom).
type ThemeInfo struct {
	Name     string
	IsCustom bool
	Count    int
}

// ListAllThemes returns a sorted list of all built-in and custom themes.
func ListAllThemes(townRoot string) []ThemeInfo {
	var themes []ThemeInfo

	// Add built-in themes
	for name, names := range BuiltinThemes {
		themes = append(themes, ThemeInfo{
			Name:  name,
			Count: len(filterReservedNames(names)),
		})
	}

	// Add custom themes from settings/themes/
	if townRoot != "" {
		themesDir := filepath.Join(townRoot, "settings", "themes")
		entries, err := os.ReadDir(themesDir)
		if err == nil {
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
					continue
				}
				name := strings.TrimSuffix(e.Name(), ".txt")
				// Skip if it collides with a built-in
				if IsBuiltinTheme(name) {
					continue
				}
				names, err := ParseThemeFile(filepath.Join(themesDir, e.Name()))
				if err != nil {
					continue
				}
				themes = append(themes, ThemeInfo{
					Name:     name,
					IsCustom: true,
					Count:    len(names),
				})
			}
		}
	}

	sort.Slice(themes, func(i, j int) bool {
		return themes[i].Name < themes[j].Name
	})
	return themes
}

// IsBuiltinTheme returns true if the theme name matches a built-in theme.
func IsBuiltinTheme(theme string) bool {
	_, ok := BuiltinThemes[theme]
	return ok
}

// validPoolNameRe matches lowercase alphanumeric names with hyphens.
var validPoolNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// ValidatePoolName validates a polecat name for use in a theme.
// Names must be >3 chars, lowercase alphanumeric with hyphens, and not reserved.
func ValidatePoolName(name string) error {
	if len(name) <= 3 {
		return fmt.Errorf("name %q too short (must be >3 characters)", name)
	}
	if !validPoolNameRe.MatchString(name) {
		return fmt.Errorf("name %q invalid (must be lowercase alphanumeric with hyphens, starting with a letter)", name)
	}
	if ReservedInfraAgentNames[name] {
		return fmt.Errorf("name %q is reserved for infrastructure agents", name)
	}
	return nil
}

// ParseThemeFile reads a custom theme file (one name per line).
// Lines starting with # are comments. Blank lines are skipped.
// Names are lowercased and deduplicated. Names <=3 chars are rejected.
// Reserved names are filtered with a stderr warning.
func ParseThemeFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	seen := make(map[string]bool)
	var names []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name := strings.ToLower(line)
		if len(name) <= 3 {
			fmt.Fprintf(os.Stderr, "warning: skipping name %q (must be >3 characters)\n", name)
			continue
		}
		if !validPoolNameRe.MatchString(name) {
			fmt.Fprintf(os.Stderr, "warning: skipping invalid name %q (must be lowercase alphanumeric with hyphens)\n", name)
			continue
		}
		if ReservedInfraAgentNames[name] {
			fmt.Fprintf(os.Stderr, "warning: skipping reserved name %q\n", name)
			continue
		}
		if seen[name] {
			continue
		}
		if len(names) >= MaxThemeNames {
			return nil, fmt.Errorf("theme file %s exceeds maximum of %d names", filepath.Base(path), MaxThemeNames)
		}
		seen[name] = true
		names = append(names, name)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no valid names in theme file %s", filepath.Base(path))
	}
	return names, nil
}

// ResolveThemeNames resolves a theme name to its list of names.
// Checks built-in themes first, then looks for a custom theme file
// at <townRoot>/settings/themes/<theme>.txt.
func ResolveThemeNames(townRoot, theme string) ([]string, error) {
	if names, ok := BuiltinThemes[theme]; ok {
		return names, nil
	}
	path := filepath.Join(townRoot, "settings", "themes", theme+".txt")
	return ParseThemeFile(path)
}

// SaveCustomTheme writes a custom theme file to settings/themes/<name>.txt.
// Uses file locking (flock) to prevent concurrent writes from losing data.
func SaveCustomTheme(townRoot, name string, names []string) error {
	if IsBuiltinTheme(name) {
		return fmt.Errorf("cannot create custom theme %q: conflicts with built-in theme", name)
	}
	themesDir := filepath.Join(townRoot, "settings", "themes")
	if err := os.MkdirAll(themesDir, 0755); err != nil {
		return fmt.Errorf("creating themes directory: %w", err)
	}

	themePath := filepath.Join(themesDir, name+".txt")
	lockPath := themePath + ".lock"

	// Acquire advisory file lock to prevent concurrent writes
	unlock, err := lock.FlockAcquire(lockPath)
	if err != nil {
		return fmt.Errorf("acquiring theme file lock: %w", err)
	}
	defer unlock()

	var sb strings.Builder
	sb.WriteString("# Custom theme: " + name + "\n")
	for _, n := range names {
		sb.WriteString(n + "\n")
	}
	return os.WriteFile(themePath, []byte(sb.String()), 0644)
}

// AppendToCustomTheme atomically adds a name to a custom theme file.
// Holds a file lock across the read-check-write cycle to prevent TOCTOU races.
// Returns (alreadyExists, error).
func AppendToCustomTheme(townRoot, theme, name string) (bool, error) {
	themesDir := filepath.Join(townRoot, "settings", "themes")
	themePath := filepath.Join(themesDir, theme+".txt")
	lockPath := themePath + ".lock"

	// Acquire lock for the entire read-modify-write
	unlock, err := lock.FlockAcquire(lockPath)
	if err != nil {
		return false, fmt.Errorf("acquiring theme file lock: %w", err)
	}
	defer unlock()

	// Read current names
	existing, err := ParseThemeFile(themePath)
	if err != nil {
		return false, fmt.Errorf("reading theme %q: %w", theme, err)
	}

	// Check for duplicate
	for _, n := range existing {
		if n == name {
			return true, nil
		}
	}

	// Append and write
	existing = append(existing, name)
	var sb strings.Builder
	sb.WriteString("# Custom theme: " + theme + "\n")
	for _, n := range existing {
		sb.WriteString(n + "\n")
	}
	if err := os.WriteFile(themePath, []byte(sb.String()), 0644); err != nil {
		return false, err
	}
	return false, nil
}

// DeleteCustomTheme removes a custom theme file.
func DeleteCustomTheme(townRoot, name string) error {
	if IsBuiltinTheme(name) {
		return fmt.Errorf("cannot delete built-in theme %q", name)
	}
	path := filepath.Join(townRoot, "settings", "themes", name+".txt")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("custom theme %q not found", name)
	}
	return os.Remove(path)
}

// FindRigsUsingTheme checks all rigs in a town and returns the names of any
// rigs whose namepool style matches the given theme name.
func FindRigsUsingTheme(townRoot, theme string) []string {
	rigsFile := filepath.Join(townRoot, "mayor", "rigs.json")
	data, err := os.ReadFile(rigsFile)
	if err != nil {
		return nil
	}

	// Minimal parse — just need rig names from the "rigs" map keys.
	var registry struct {
		Rigs map[string]json.RawMessage `json:"rigs"`
	}
	if err := json.Unmarshal(data, &registry); err != nil {
		return nil
	}

	var using []string
	for rigName := range registry.Rigs {
		settingsPath := filepath.Join(townRoot, rigName, "settings", "config.json")
		sdata, err := os.ReadFile(settingsPath)
		if err != nil {
			continue
		}
		var settings struct {
			Namepool *struct {
				Style string `json:"style"`
			} `json:"namepool"`
		}
		if err := json.Unmarshal(sdata, &settings); err != nil {
			continue
		}
		if settings.Namepool != nil && settings.Namepool.Style == theme {
			using = append(using, rigName)
		}
	}
	sort.Strings(using)
	return using
}

// AddCustomName adds a custom name to the pool.
func (p *NamePool) AddCustomName(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Check if already in custom names
	for _, n := range p.CustomNames {
		if n == name {
			return
		}
	}
	p.CustomNames = append(p.CustomNames, name)
}

// Reset clears the pool state, releasing all names.
func (p *NamePool) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.InUse = make(map[string]bool)
	p.OverflowNext = p.MaxSize + 1
}
