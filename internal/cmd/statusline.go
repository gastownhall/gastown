package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	beadsdk "github.com/steveyegge/beads"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/estop"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

var (
	statusLineSession string
)

// statusLineStores is a process-scoped memo of in-process beadsdk stores keyed
// by resolved .beads directory, used only by the status-line command.
//
// hq-9i9s7.1: status-line was the dominant Dolt connection-churn source (~77%)
// because every segment called beads.New(dir).List(...), and a store-less
// beads.New shells a fresh `bd` subprocess (= a fresh Dolt connection set) per
// call. A single status-line render fanned out to ~15 bd spawns. This cache
// opens ONE in-process store per distinct beads dir and reuses it across every
// segment within the one invocation, collapsing the fan-out to 1-2 store opens.
//
// gt status-line is a short-lived, single-goroutine process (one render, then
// exit), so a package-global memo is process-scoped and needs no locking. It is
// reset+closed by withStatusLineStores around each runStatusLine invocation.
//
// Fail-open: if a store cannot be opened for a dir, the entry is recorded as a
// nil store so callers transparently fall back to the bd-subprocess path. A
// broken status line must never break an agent's terminal.
var statusLineStores *storeMemo

// storeMemo caches opened beadsdk stores by resolved beads dir for one
// status-line invocation.
type storeMemo struct {
	opened map[string]beadsdk.Storage // resolvedDir -> store (nil = open failed)
}

// newStoreMemo returns an empty store memo. Callers MUST call close() when done
// to release any opened in-process stores (defer m.close()). Used by short-lived
// gt commands that issue several beads ops to the same DB(s) within one
// invocation and want to amortize the Dolt connection over those ops instead of
// spawning a fresh `bd` (= fresh connection set) per op (hq-q7ls9).
func newStoreMemo() *storeMemo {
	return &storeMemo{opened: make(map[string]beadsdk.Storage)}
}

// close releases every store opened by the memo. Safe to call on a nil memo and
// safe to call more than once.
func (m *storeMemo) close() {
	if m == nil {
		return
	}
	for dir, s := range m.opened {
		if s != nil {
			_ = s.Close()
		}
		delete(m.opened, dir)
	}
}

// beadsFor returns a *beads.Beads for the given workDir. When an in-process
// store can be opened (and memoized) for the dir's resolved .beads location it
// returns a store-backed Beads (no bd subprocess); otherwise it falls back to
// the standard beads.New spawn path so the segment still renders.
func (m *storeMemo) beadsFor(workDir string) *beads.Beads {
	if m == nil || workDir == "" {
		return beads.New(workDir)
	}

	resolved := beads.ResolveBeadsDir(workDir)
	if resolved == "" {
		// Can't resolve a beads dir; let the subprocess path try (it has its
		// own resolution + error handling).
		return beads.New(workDir)
	}

	store, seen := m.opened[resolved]
	if !seen {
		store = openStatusLineStore(resolved)
		m.opened[resolved] = store // memoize success OR failure (nil)
	}

	if store == nil {
		return beads.New(workDir) // fail-open to subprocess
	}
	return beads.NewWithStore(workDir, store)
}

// openStatusLineStore opens an in-process read store for an already-resolved
// .beads directory. Returns nil on any failure (caller falls back to bd).
func openStatusLineStore(beadsDir string) beadsdk.Storage {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Read-only open (CreateIfMissing=false): takes the beads fast-path connect
	// (2 Dolt connections, no SHOW DATABASES catalog probe) instead of the
	// create-capable slow path (3 connections). These gt store-opens never need
	// to create a DB. (hq-q7ls9 / hq-9i9s7.2)
	store, err := beadsdk.OpenFromConfigReadOnly(ctx, beadsDir)
	if err != nil {
		return nil
	}
	return store
}

// withStatusLineStores installs a fresh store memo for the duration of fn and
// closes every opened store afterward. Safe to nest/repeat: it always restores
// the prior value.
func withStatusLineStores(fn func() error) error {
	prev := statusLineStores
	statusLineStores = &storeMemo{opened: make(map[string]beadsdk.Storage)}
	defer func() {
		for _, s := range statusLineStores.opened {
			if s != nil {
				_ = s.Close()
			}
		}
		statusLineStores = prev
	}()
	return fn()
}

var statusLineCmd = &cobra.Command{
	Use:   "status-line",
	Short: "Output status line content for tmux (internal use)",
	Long: `Output formatted status line content for the tmux status bar.

Called internally by the tmux status-right configuration. Displays
the current rig, role, worker name, and active issue. Pass --session
to specify which tmux session to query.`,
	Hidden: true, // Internal command called by tmux
	RunE:   runStatusLine,
}

func init() {
	rootCmd.AddCommand(statusLineCmd)
	statusLineCmd.Flags().StringVar(&statusLineSession, "session", "", "Tmux session name")
}

func runStatusLine(cmd *cobra.Command, args []string) error {
	// hq-9i9s7.1: open at most one in-process beads store per distinct beads dir
	// for this render, reused across all segments, then close them on exit. This
	// replaces the per-segment `bd` subprocess fan-out (the dominant Dolt churn
	// source). Fail-open is handled inside the memo (see storeMemo.beadsFor).
	return withStatusLineStores(func() error {
		return runStatusLineInner(cmd, args)
	})
}

func runStatusLineInner(cmd *cobra.Command, args []string) error {
	// Check E-stop first — prepend red indicator if active
	if townRoot, twErr := workspace.FindFromCwd(); twErr == nil {
		showEstop := false
		var info *estop.Info
		if estop.IsActive(townRoot) {
			showEstop = true
			info = estop.Read(townRoot)
		} else {
			// Check per-rig E-stop
			rigEnv := os.Getenv("GT_RIG")
			if rigEnv != "" && estop.IsRigActive(townRoot, rigEnv) {
				showEstop = true
				info = estop.ReadRig(townRoot, rigEnv)
			}
		}
		if showEstop {
			ts := ""
			if info != nil && !info.Timestamp.IsZero() {
				ts = info.Timestamp.Format("15:04")
			}
			fmt.Printf("#[bg=red,fg=white,bold] ESTOP %s #[default] ", ts)
		}
	}

	t := tmux.NewTmux()

	// Get session environment
	var rigName, polecat, crew, issue, role string

	if statusLineSession != "" {
		// Non-fatal: missing env vars are handled gracefully below
		rigName, _ = t.GetEnvironment(statusLineSession, "GT_RIG")
		polecat, _ = t.GetEnvironment(statusLineSession, "GT_POLECAT")
		crew, _ = t.GetEnvironment(statusLineSession, "GT_CREW")
		issue, _ = t.GetEnvironment(statusLineSession, "GT_ISSUE")
		role, _ = t.GetEnvironment(statusLineSession, "GT_ROLE")
	} else {
		// Fallback to process environment
		rigName = os.Getenv("GT_RIG")
		polecat = os.Getenv("GT_POLECAT")
		crew = os.Getenv("GT_CREW")
		issue = os.Getenv("GT_ISSUE")
		role = os.Getenv("GT_ROLE")
	}

	// Get session names for comparison
	mayorSession := getMayorSessionName()
	deaconSession := getDeaconSessionName()

	// Determine identity and output based on role
	if role == "mayor" || statusLineSession == mayorSession {
		return runMayorStatusLine(t)
	}

	// Deacon status line
	if role == "deacon" || statusLineSession == deaconSession {
		return runDeaconStatusLine(t)
	}

	// Witness status line (session naming: gt-<rig>-witness)
	if role == "witness" || strings.HasSuffix(statusLineSession, "-witness") {
		return runWitnessStatusLine(t, rigName)
	}

	// Refinery status line
	if role == "refinery" || strings.HasSuffix(statusLineSession, "-refinery") {
		return runRefineryStatusLine(t, rigName)
	}

	// Crew/Polecat status line
	return runWorkerStatusLine(t, statusLineSession, rigName, polecat, crew, issue)
}

// runWorkerStatusLine outputs status for crew or polecat sessions.
func runWorkerStatusLine(t *tmux.Tmux, session, rigName, polecat, crew, issue string) error {
	// Determine agent type and identity
	var icon, identity string
	if polecat != "" {
		icon = AgentTypeIcons[AgentPolecat]
		identity = fmt.Sprintf("%s/%s", rigName, polecat)
	} else if crew != "" {
		icon = AgentTypeIcons[AgentCrew]
		identity = fmt.Sprintf("%s/crew/%s", rigName, crew)
	}

	// Get pane's working directory to find workspace
	var townRoot string
	if session != "" {
		paneDir, err := t.GetPaneWorkDir(session)
		if err == nil && paneDir != "" {
			townRoot, _ = workspace.Find(paneDir)
		}
	}

	// Build status parts
	var parts []string

	// Priority 1: Check for hooked work (use rig beads)
	hookedWork := ""
	if identity != "" && rigName != "" && townRoot != "" {
		rigBeadsDir := filepath.Join(townRoot, rigName, "mayor", "rig")
		hookedWork = getHookedWork(identity, 40, rigBeadsDir)
	}

	// Priority 2: Fall back to GT_ISSUE env var or in_progress beads
	currentWork := issue
	if currentWork == "" && hookedWork == "" && session != "" {
		currentWork = getCurrentWork(t, session, identity, 40)
	}

	// Show hooked work (takes precedence)
	if hookedWork != "" {
		if icon != "" {
			parts = append(parts, fmt.Sprintf("%s 🪝 %s", icon, hookedWork))
		} else {
			parts = append(parts, fmt.Sprintf("🪝 %s", hookedWork))
		}
	} else if currentWork != "" {
		// Fall back to current work (in_progress)
		if icon != "" {
			parts = append(parts, fmt.Sprintf("%s %s", icon, currentWork))
		} else {
			parts = append(parts, currentWork)
		}
	} else if icon != "" {
		parts = append(parts, icon)
	}

	// Mail preview - only show if hook is empty
	if hookedWork == "" && identity != "" && townRoot != "" {
		unread, subject := getMailPreviewWithRoot(identity, 45, townRoot)
		if unread > 0 {
			if subject != "" {
				parts = append(parts, fmt.Sprintf("\U0001F4EC %s", subject))
			} else {
				parts = append(parts, fmt.Sprintf("\U0001F4EC %d", unread))
			}
		}
	}

	// Output
	if len(parts) > 0 {
		fmt.Print(strings.Join(parts, " | ") + " |")
	}

	return nil
}

func runMayorStatusLine(t *tmux.Tmux) error {
	// Count active sessions by listing tmux sessions
	sessions, err := t.ListSessions()
	if err != nil {
		return nil // Silent fail
	}

	// Get town root from mayor pane's working directory
	var townRoot string
	mayorSession := getMayorSessionName()
	paneDir, err := t.GetPaneWorkDir(mayorSession)
	if err == nil && paneDir != "" {
		townRoot, _ = workspace.Find(paneDir)
	}

	// Load registered rigs to validate against
	registeredRigs := make(map[string]bool)
	if townRoot != "" {
		rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
		if rigsConfig, err := config.LoadRigsConfig(rigsConfigPath); err == nil {
			for rigName := range rigsConfig.Rigs {
				registeredRigs[rigName] = true
			}
		}
	}

	// Track per-rig status for LED indicators and sorting
	type rigStatus struct {
		hasWitness  bool
		hasRefinery bool
		opState     string // "OPERATIONAL", "PARKED", or "DOCKED"
	}
	rigStatuses := make(map[string]*rigStatus)

	// Initialize for all registered rigs
	for rigName := range registeredRigs {
		rigStatuses[rigName] = &rigStatus{}
	}

	// Track per-agent-type health (working/zombie counts)
	type agentHealth struct {
		total   int
		working int
	}
	healthByType := map[AgentType]*agentHealth{
		AgentWitness:  {},
		AgentRefinery: {},
	}

	// Track deacon presence (just icon, no count)
	hasDeacon := false

	// Single pass: track rig status AND agent health
	for _, s := range sessions {
		agent := categorizeSession(s)
		if agent == nil {
			continue
		}

		// Track rig-level status (witness/refinery presence)
		// Polecats are not tracked in tmux - they're a GC concern, not a display concern
		if agent.Rig != "" && registeredRigs[agent.Rig] {
			if rigStatuses[agent.Rig] == nil {
				rigStatuses[agent.Rig] = &rigStatus{}
			}
			switch agent.Type {
			case AgentWitness:
				rigStatuses[agent.Rig].hasWitness = true
			case AgentRefinery:
				rigStatuses[agent.Rig].hasRefinery = true
			}
		}

		// Track agent health (skip Mayor and Crew)
		if health := healthByType[agent.Type]; health != nil {
			health.total++
			// Detect working state via ✻ symbol
			if isSessionWorking(t, s) {
				health.working++
			}
		}

		// Track deacon presence (just the icon, no count)
		if agent.Type == AgentDeacon {
			hasDeacon = true
		}
	}

	// Get operational state for each rig
	for rigName, status := range rigStatuses {
		opState, _ := getRigOperationalState(townRoot, rigName)
		if opState == "PARKED" || opState == "DOCKED" {
			status.opState = opState
		} else {
			status.opState = "OPERATIONAL"
		}
	}

	// Build status
	var parts []string

	// Add per-agent-type health in consistent order
	// Format: "1/3 👁️" = 1 working out of 3 total
	// Only show agent types that have sessions
	// Note: Polecats excluded - idle state is misleading noise
	// Deacon gets just an icon (no count) - shown separately below
	agentOrder := []AgentType{AgentWitness, AgentRefinery}
	var agentParts []string
	for _, agentType := range agentOrder {
		health := healthByType[agentType]
		if health.total == 0 {
			continue
		}
		icon := AgentTypeIcons[agentType]
		agentParts = append(agentParts, fmt.Sprintf("%d/%d %s", health.working, health.total, icon))
	}
	if len(agentParts) > 0 {
		parts = append(parts, strings.Join(agentParts, " "))
	}

	// Add deacon icon if running (just presence, no count)
	if hasDeacon {
		parts = append(parts, AgentTypeIcons[AgentDeacon])
	}

	// Build rig status display with LED indicators (see GetRigLED for definitions)

	// Create sortable rig list
	type rigInfo struct {
		name   string
		status *rigStatus
	}
	var rigs []rigInfo
	for rigName, status := range rigStatuses {
		// Skip docked rigs — they're intentionally disabled and don't need display.
		// Reserve 🛑 for error states (crashed agents, unreachable Dolt, etc.).
		if status.opState == "DOCKED" {
			continue
		}
		rigs = append(rigs, rigInfo{name: rigName, status: status})
	}

	// Sort by: 1) running state, 2) operational state, 3) alphabetical
	sort.Slice(rigs, func(i, j int) bool {
		isRunningI := rigs[i].status.hasWitness || rigs[i].status.hasRefinery
		isRunningJ := rigs[j].status.hasWitness || rigs[j].status.hasRefinery

		// Primary sort: running rigs before non-running rigs
		if isRunningI != isRunningJ {
			return isRunningI
		}

		// Secondary sort: operational state (for non-running rigs: OPERATIONAL < PARKED < DOCKED)
		stateOrder := map[string]int{"OPERATIONAL": 0, "PARKED": 1, "DOCKED": 2}
		stateI := stateOrder[rigs[i].status.opState]
		stateJ := stateOrder[rigs[j].status.opState]
		if stateI != stateJ {
			return stateI < stateJ
		}

		// Tertiary sort: alphabetical
		return rigs[i].name < rigs[j].name
	})

	// Build display with group separators
	var rigParts []string
	var lastGroup string
	for _, rig := range rigs {
		isRunning := rig.status.hasWitness || rig.status.hasRefinery
		var currentGroup string
		if isRunning {
			currentGroup = "running"
		} else {
			currentGroup = "idle-" + rig.status.opState
		}

		// Add separator when group changes (running -> non-running, or different opStates within non-running)
		if lastGroup != "" && lastGroup != currentGroup {
			rigParts = append(rigParts, "|")
		}
		lastGroup = currentGroup

		status := rig.status
		led := GetRigLED(status.hasWitness, status.hasRefinery, status.opState)

		// All icons get 1 space, Park gets 2
		space := " "
		if led == "🅿️" {
			space = "  "
		}
		// Abbreviate rig names to beads prefix when >2 rigs
		displayName := rig.name
		if len(rigs) > 2 && townRoot != "" {
			if prefix := config.GetRigPrefix(townRoot, rig.name); prefix != "" {
				displayName = prefix
			}
		}
		rigParts = append(rigParts, led+space+displayName)
	}

	if len(rigParts) > 0 {
		parts = append(parts, strings.Join(rigParts, " "))
	}

	// Priority 1: Check for hooked work (town beads for mayor)
	hookedWork := ""
	if townRoot != "" {
		hookedWork = getHookedWork("mayor", 40, townRoot)
	}
	if hookedWork != "" {
		parts = append(parts, fmt.Sprintf("🪝 %s", hookedWork))
	} else if townRoot != "" {
		// Priority 2: Fall back to mail preview
		unread, subject := getMailPreviewWithRoot("mayor/", 45, townRoot)
		if unread > 0 {
			if subject != "" {
				parts = append(parts, fmt.Sprintf("\U0001F4EC %s", subject))
			} else {
				parts = append(parts, fmt.Sprintf("\U0001F4EC %d", unread))
			}
		}
	}

	fmt.Print(strings.Join(parts, " | ") + " |")
	return nil
}

// runDeaconStatusLine outputs status for the deacon session.
// Shows: active rigs, polecat count, hook or mail preview
func runDeaconStatusLine(t *tmux.Tmux) error {
	// Count active rigs and polecats
	sessions, err := t.ListSessions()
	if err != nil {
		return nil // Silent fail
	}

	// Get town root from deacon pane's working directory
	var townRoot string
	deaconSession := getDeaconSessionName()
	paneDir, err := t.GetPaneWorkDir(deaconSession)
	if err == nil && paneDir != "" {
		townRoot, _ = workspace.Find(paneDir)
	}

	// Load registered rigs to validate against
	registeredRigs := make(map[string]bool)
	if townRoot != "" {
		rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
		if rigsConfig, err := config.LoadRigsConfig(rigsConfigPath); err == nil {
			for rigName := range rigsConfig.Rigs {
				registeredRigs[rigName] = true
			}
		}
	}

	rigs := make(map[string]bool)
	for _, s := range sessions {
		agent := categorizeSession(s)
		if agent == nil {
			continue
		}
		// Only count registered rigs
		if agent.Rig != "" && registeredRigs[agent.Rig] {
			rigs[agent.Rig] = true
		}
	}
	rigCount := len(rigs)

	// Build status
	// Note: Polecats excluded - their sessions are ephemeral and idle detection is a GC concern
	var parts []string
	parts = append(parts, fmt.Sprintf("%d rigs", rigCount))

	// Priority 1: Check for hooked work (town beads for deacon)
	hookedWork := ""
	if townRoot != "" {
		hookedWork = getHookedWork("deacon", 35, townRoot)
	}
	if hookedWork != "" {
		parts = append(parts, fmt.Sprintf("🪝 %s", hookedWork))
	} else if townRoot != "" {
		// Priority 2: Fall back to mail preview
		unread, subject := getMailPreviewWithRoot("deacon/", 40, townRoot)
		if unread > 0 {
			if subject != "" {
				parts = append(parts, fmt.Sprintf("\U0001F4EC %s", subject))
			} else {
				parts = append(parts, fmt.Sprintf("\U0001F4EC %d", unread))
			}
		}
	}

	fmt.Print(strings.Join(parts, " | ") + " |")
	return nil
}

// runWitnessStatusLine outputs status for a witness session.
// Shows: crew count, hook or mail preview
// Note: Polecats excluded - their sessions are ephemeral and idle detection is a GC concern
func runWitnessStatusLine(t *tmux.Tmux, rigName string) error {
	if rigName == "" {
		// Try to extract from session name: <prefix>-witness
		if identity, err := session.ParseSessionName(statusLineSession); err == nil && identity.Role == session.RoleWitness {
			rigName = identity.Rig
		}
	}

	// Get town root from witness pane's working directory
	var townRoot string
	sessionName := session.WitnessSessionName(session.PrefixFor(rigName))
	paneDir, err := t.GetPaneWorkDir(sessionName)
	if err == nil && paneDir != "" {
		townRoot, _ = workspace.Find(paneDir)
	}

	// Count crew in this rig (crew are persistent, worth tracking)
	sessions, err := t.ListSessions()
	if err != nil {
		return nil // Silent fail
	}

	crewCount := 0
	for _, s := range sessions {
		agent := categorizeSession(s)
		if agent == nil {
			continue
		}
		if agent.Rig == rigName && agent.Type == AgentCrew {
			crewCount++
		}
	}

	identity := fmt.Sprintf("%s/witness", rigName)

	// Build status
	var parts []string
	if crewCount > 0 {
		parts = append(parts, fmt.Sprintf("%d crew", crewCount))
	}

	// Priority 1: Check for hooked work (rig beads for witness)
	hookedWork := ""
	if townRoot != "" && rigName != "" {
		rigBeadsDir := filepath.Join(townRoot, rigName, "mayor", "rig")
		hookedWork = getHookedWork(identity, 30, rigBeadsDir)
	}
	if hookedWork != "" {
		parts = append(parts, fmt.Sprintf("🪝 %s", hookedWork))
	} else if townRoot != "" {
		// Priority 2: Fall back to mail preview
		unread, subject := getMailPreviewWithRoot(identity, 35, townRoot)
		if unread > 0 {
			if subject != "" {
				parts = append(parts, fmt.Sprintf("\U0001F4EC %s", subject))
			} else {
				parts = append(parts, fmt.Sprintf("\U0001F4EC %d", unread))
			}
		}
	}

	fmt.Print(strings.Join(parts, " | ") + " |")
	return nil
}

// runRefineryStatusLine outputs status for a refinery session.
// Shows: MQ length, current item, hook or mail preview
func runRefineryStatusLine(t *tmux.Tmux, rigName string) error {
	if rigName == "" {
		// Try to extract from session name: <prefix>-refinery
		if identity, err := session.ParseSessionName(statusLineSession); err == nil && identity.Role == session.RoleRefinery {
			rigName = identity.Rig
		}
	}

	if rigName == "" {
		fmt.Printf("%s ? |", AgentTypeIcons[AgentRefinery])
		return nil
	}

	// Get town root from refinery pane's working directory
	var townRoot string
	sessionName := session.RefinerySessionName(session.PrefixFor(rigName))
	paneDir, err := t.GetPaneWorkDir(sessionName)
	if err == nil && paneDir != "" {
		townRoot, _ = workspace.Find(paneDir)
	}

	// Get refinery manager using shared helper
	mgr, _, _, err := getRefineryManager(rigName)
	if err != nil {
		// Fallback to simple status if we can't access refinery
		fmt.Printf("%s MQ: ? |", AgentTypeIcons[AgentRefinery])
		return nil
	}

	// Get queue
	queue, err := mgr.Queue()
	if err != nil {
		// Fallback to simple status if we can't read queue
		fmt.Printf("%s MQ: ? |", AgentTypeIcons[AgentRefinery])
		return nil
	}

	// Count pending items and find current item
	pending := 0
	var currentItem string
	for _, item := range queue {
		if item.Position == 0 && item.MR != nil {
			// Currently processing - show issue ID
			currentItem = item.MR.IssueID
		} else {
			pending++
		}
	}

	identity := fmt.Sprintf("%s/refinery", rigName)

	// Build status
	var parts []string
	if currentItem != "" {
		parts = append(parts, fmt.Sprintf("merging %s", currentItem))
		if pending > 0 {
			parts = append(parts, fmt.Sprintf("+%d queued", pending))
		}
	} else if pending > 0 {
		parts = append(parts, fmt.Sprintf("%d queued", pending))
	} else {
		parts = append(parts, "idle")
	}

	// Priority 1: Check for hooked work (rig beads for refinery)
	hookedWork := ""
	if townRoot != "" && rigName != "" {
		rigBeadsDir := filepath.Join(townRoot, rigName, "mayor", "rig")
		hookedWork = getHookedWork(identity, 25, rigBeadsDir)
	}
	if hookedWork != "" {
		parts = append(parts, fmt.Sprintf("🪝 %s", hookedWork))
	} else if townRoot != "" {
		// Priority 2: Fall back to mail preview
		unread, subject := getMailPreviewWithRoot(identity, 30, townRoot)
		if unread > 0 {
			if subject != "" {
				parts = append(parts, fmt.Sprintf("\U0001F4EC %s", subject))
			} else {
				parts = append(parts, fmt.Sprintf("\U0001F4EC %d", unread))
			}
		}
	}

	fmt.Print(strings.Join(parts, " | ") + " |")
	return nil
}

// isSessionWorking detects if a Claude Code session is actively working.
// Returns true if the ✻ symbol is visible in the pane (indicates Claude is processing).
// Returns false for idle sessions (showing ❯ prompt) or if state cannot be determined.
func isSessionWorking(t *tmux.Tmux, session string) bool {
	// Capture last few lines of the pane
	lines, err := t.CapturePaneLines(session, 5)
	if err != nil || len(lines) == 0 {
		return false
	}

	// Check all captured lines for the working indicator
	// ✻ appears in Claude's status line when actively processing
	for _, line := range lines {
		if strings.Contains(line, "✻") {
			return true
		}
	}

	return false
}

// getMailPreviewWithRoot returns unread count and a truncated subject of the first unread message,
// using an explicit town root.
func getMailPreviewWithRoot(identity string, maxLen int, townRoot string) (int, string) {
	// NOTE (hq-q7ls9): intentionally NOT store-backed. The in-process
	// mail store path (mailbox.storeListFromDir) only queries the issues table
	// and misses ephemeral "wisp" mail messages that the bd path picks up via a
	// separate wisps-table SQL query (mailbox.listWispMessages). Using the store
	// here would undercount unread mail. Left on the bd path until the SDK mail
	// list covers wisps. NewMailboxFromAddress normalizes the identity (e.g.,
	// gastown/crew/gus -> gastown/gus).
	mailbox := mail.NewMailboxFromAddress(identity, townRoot)

	// Get unread messages
	messages, err := mailbox.ListUnread()
	if err != nil || len(messages) == 0 {
		return 0, ""
	}

	// Get first message subject, truncated
	subject := messages[0].Subject
	if len(subject) > maxLen {
		subject = subject[:maxLen-1] + "…"
	}

	return len(messages), subject
}

// getHookedWork returns a truncated title of the hooked bead for an agent.
// Returns empty string if nothing is hooked.
// beadsDir should be the directory containing .beads (for rig-level) or
// empty to use the town root (for town-level roles).
func getHookedWork(identity string, maxLen int, beadsDir string) string {
	// If no beadsDir specified, use town root
	if beadsDir == "" {
		var err error
		beadsDir, err = findMailWorkDir()
		if err != nil {
			return ""
		}
	}

	// Reuse a process-scoped in-process store when available (avoids spawning
	// a fresh bd subprocess per render); falls back to bd transparently.
	b := statusLineStores.beadsFor(beadsDir)

	// Query for hooked beads assigned to this agent
	hookedBeads, err := b.List(beads.ListOptions{
		Status:   beads.StatusHooked,
		Assignee: identity,
		Priority: -1,
	})
	if err != nil || len(hookedBeads) == 0 {
		return ""
	}

	// Return first hooked bead's ID and title, truncated
	bead := hookedBeads[0]
	display := fmt.Sprintf("%s: %s", bead.ID, bead.Title)
	if len(display) > maxLen {
		display = display[:maxLen-1] + "…"
	}
	return display
}

// getCurrentWork returns a truncated title of the first in_progress issue assigned to identity.
// Uses the pane's working directory to find the beads.
func getCurrentWork(t *tmux.Tmux, session string, identity string, maxLen int) string {
	// Get the pane's working directory
	workDir, err := t.GetPaneWorkDir(session)
	if err != nil || workDir == "" {
		return ""
	}

	// Check if there's a .beads directory
	beadsDir := filepath.Join(workDir, ".beads")
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return ""
	}

	// Query beads for in_progress issues assigned to this agent. Reuse a
	// process-scoped in-process store when available; falls back to bd.
	b := statusLineStores.beadsFor(workDir)
	issues, err := b.List(beads.ListOptions{
		Status:   "in_progress",
		Assignee: identity,
		Priority: -1,
	})
	if err != nil || len(issues) == 0 {
		return ""
	}

	// Return first issue's ID and title, truncated
	issue := issues[0]
	display := fmt.Sprintf("%s: %s", issue.ID, issue.Title)
	if len(display) > maxLen {
		display = display[:maxLen-1] + "…"
	}
	return display
}
