package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/crew"
	"github.com/steveyegge/gastown/internal/daemon"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/mayor"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
	"golang.org/x/term"
)

var statusJSON bool
var statusFast bool
var statusWatch bool
var statusInterval int
var statusVerbose bool

var statusCmd = &cobra.Command{
	Use:         "status",
	Aliases:     []string{"stat"},
	GroupID:     GroupDiag,
	Annotations: map[string]string{AnnotationPolecatSafe: "true"},
	Short:       "Show overall town status",
	Long: `Display the current status of the Gas Town workspace.

Shows town name, registered rigs, polecats, and witness status.

Use --fast to skip mail lookups for faster execution.
Use --watch to continuously refresh status at regular intervals.`,
	RunE: runStatus,
}

func init() {
	statusCmd.Flags().BoolVar(&statusJSON, "json", false, "Output as JSON")
	statusCmd.Flags().BoolVar(&statusFast, "fast", false, "Skip mail lookups for faster execution")
	statusCmd.Flags().BoolVarP(&statusWatch, "watch", "w", false, "Watch mode: refresh status continuously")
	statusCmd.Flags().IntVarP(&statusInterval, "interval", "n", 2, "Refresh interval in seconds")
	statusCmd.Flags().BoolVarP(&statusVerbose, "verbose", "v", false, "Show detailed multi-line output per agent")
	rootCmd.AddCommand(statusCmd)
}

// TownStatus represents the overall status of the workspace.
type TownStatus struct {
	Name     string         `json:"name"`
	Location string         `json:"location"`
	Overseer *OverseerInfo  `json:"overseer,omitempty"` // Human operator
	DND      *DNDInfo       `json:"dnd,omitempty"`      // Current agent DND status
	Daemon   *ServiceInfo   `json:"daemon,omitempty"`   // Daemon status
	Dolt     *DoltInfo      `json:"dolt,omitempty"`     // Dolt server status
	Tmux     *TmuxInfo      `json:"tmux,omitempty"`     // Tmux server status
	ACP      *ServiceInfo   `json:"acp,omitempty"`      // ACP mayor status
	Agents   []AgentRuntime `json:"agents"`             // Global agents (Mayor, Deacon)
	Rigs     []RigStatus    `json:"rigs"`
	Summary  StatusSum      `json:"summary"`
}

// ServiceInfo represents a background service status.
type ServiceInfo struct {
	Running bool `json:"running"`
	PID     int  `json:"pid,omitempty"`
}

// DoltInfo represents the Dolt server status.
type DoltInfo struct {
	Running       bool   `json:"running"`
	PID           int    `json:"pid,omitempty"`
	Port          int    `json:"port"`
	Remote        bool   `json:"remote,omitempty"`
	DataDir       string `json:"data_dir,omitempty"`
	PortConflict  bool   `json:"port_conflict,omitempty"`  // Port taken by another town's Dolt
	ConflictOwner string `json:"conflict_owner,omitempty"` // --data-dir of the process holding the port
}

// TmuxInfo represents the tmux server status.
type TmuxInfo struct {
	Socket       string `json:"socket"`                // Socket name derived from town name (e.g., "gt-test")
	SocketPath   string `json:"socket_path,omitempty"` // Full socket path (e.g., /tmp/tmux-501/gt-test)
	Running      bool   `json:"running"`               // Is the tmux server running?
	PID          int    `json:"pid,omitempty"`         // PID of the tmux server process
	SessionCount int    `json:"session_count"`         // Number of sessions
}

// OverseerInfo represents the human operator's identity and status.
type OverseerInfo struct {
	Name       string `json:"name"`
	Email      string `json:"email,omitempty"`
	Username   string `json:"username,omitempty"`
	Source     string `json:"source"`
	UnreadMail int    `json:"unread_mail"`
}

// DNDInfo represents Do Not Disturb status for the current agent context.
type DNDInfo struct {
	Enabled bool   `json:"enabled"`
	Level   string `json:"level"`
	Agent   string `json:"agent,omitempty"`
}

// AgentRuntime represents the runtime state of an agent.
type AgentRuntime struct {
	Name              string `json:"name"`                         // Display name (e.g., "mayor", "witness")
	Address           string `json:"address"`                      // Full address (e.g., "greenplace/witness")
	Session           string `json:"session"`                      // tmux session name
	Role              string `json:"role"`                         // Role type
	Running           bool   `json:"running"`                      // Is tmux session running?
	ACP               bool   `json:"acp"`                          // Is ACP session active?
	HasWork           bool   `json:"has_work"`                     // Has pinned work?
	WorkTitle         string `json:"work_title,omitempty"`         // Title of pinned work
	HookBead          string `json:"hook_bead,omitempty"`          // Pinned bead ID from agent bead
	State             string `json:"state,omitempty"`              // Agent state from agent bead
	NotificationLevel string `json:"notification_level,omitempty"` // Notification level (verbose, normal, muted)
	UnreadMail        int    `json:"unread_mail"`                  // Number of unread messages
	FirstSubject      string `json:"first_subject,omitempty"`      // Subject of first unread message
	AgentAlias        string `json:"agent_alias,omitempty"`        // Configured agent name (e.g., "opus-46", "pi")
	AgentInfo         string `json:"agent_info,omitempty"`         // Runtime summary (e.g., "claude/opus", "pi/kimi-k2p5")
}

// RigStatus represents status of a single rig.
type RigStatus struct {
	Name         string          `json:"name"`
	Polecats     []string        `json:"polecats"`
	PolecatCount int             `json:"polecat_count"`
	Crews        []string        `json:"crews"`
	CrewCount    int             `json:"crew_count"`
	HasWitness   bool            `json:"has_witness"`
	HasRefinery  bool            `json:"has_refinery"`
	Hooks        []AgentHookInfo `json:"hooks,omitempty"`
	Agents       []AgentRuntime  `json:"agents,omitempty"` // Runtime state of all agents in rig
	MQ           *MQSummary      `json:"mq,omitempty"`     // Merge queue summary
}

// MQSummary represents the merge queue status for a rig.
type MQSummary struct {
	Pending  int    `json:"pending"`   // Open MRs ready to merge (no blockers)
	InFlight int    `json:"in_flight"` // MRs currently being processed
	Blocked  int    `json:"blocked"`   // MRs waiting on dependencies
	State    string `json:"state"`     // idle, processing, or blocked
	Health   string `json:"health"`    // healthy, stale, or empty
}

// AgentHookInfo represents an agent's hook (pinned work) status.
type AgentHookInfo struct {
	Agent    string `json:"agent"`              // Agent address (e.g., "greenplace/toast", "greenplace/witness")
	Role     string `json:"role"`               // Role type (polecat, crew, witness, refinery)
	HasWork  bool   `json:"has_work"`           // Whether agent has pinned work
	Molecule string `json:"molecule,omitempty"` // Attached molecule ID
	Title    string `json:"title,omitempty"`    // Pinned bead title
}

// StatusSum provides summary counts.
type StatusSum struct {
	RigCount      int `json:"rig_count"`
	PolecatCount  int `json:"polecat_count"`
	CrewCount     int `json:"crew_count"`
	WitnessCount  int `json:"witness_count"`
	RefineryCount int `json:"refinery_count"`
	ActiveHooks   int `json:"active_hooks"`
}

// resolveAgentDisplay inspects the actual running process in the tmux session
// to determine what runtime and model are being used. Falls back to config
// when the session isn't running.
func resolveAgentDisplay(townRoot string, townSettings *config.TownSettings, role string, sessionName string, running bool) (alias, info string) {
	// Map legacy role names to config role names
	configRole := role
	switch role {
	case "coordinator":
		configRole = constants.RoleMayor
	case "health-check":
		configRole = constants.RoleDeacon
	}

	// Get alias from config
	if townSettings != nil {
		alias = townSettings.RoleAgents[configRole]
		if alias == "" {
			alias = townSettings.DefaultAgent
		}
	}

	// If mayor is in ACP mode, use the ACP agent name instead
	if configRole == constants.RoleMayor && mayor.IsACPActive(townRoot) {
		if acpAgent, err := mayor.GetACPAgent(townRoot); err == nil && acpAgent != "" {
			alias = acpAgent
		}
	}

	// If session is running, inspect the actual process
	if running && sessionName != "" {
		if detected := detectRuntimeFromSession(sessionName); detected != "" {
			info = detected
			return alias, info
		}
	}

	// Fall back to config-based display
	if townSettings != nil && alias != "" {
		rc := townSettings.Agents[alias]
		if rc != nil {
			info = buildInfoFromConfig(rc)
		} else {
			info = alias
		}
	}
	return alias, info
}

// detectRuntimeFromSession inspects the actual process tree in a tmux session
// to determine what agent runtime and model are in use.
func detectRuntimeFromSession(sessionName string) string {
	// Get the PID of the shell process in the tmux pane
	t := tmux.NewTmux()
	pid, err := t.GetPanePID(sessionName)
	if err != nil || pid == "" {
		return ""
	}

	// Walk child processes to find the actual agent (not the shell)
	cmdline := findAgentCmdline(pid)
	if cmdline == "" {
		return ""
	}

	return parseRuntimeInfo(cmdline)
}

// findAgentCmdline checks the pane process itself and its descendants for a known agent.
// The pane PID may BE the agent (e.g., claude), or the agent may be a child (e.g., shell → pi).
// Also handles wrapper processes (node /path/to/pi, bun /path/to/opencode).
func findAgentCmdline(panePid string) string {
	// Check the pane process itself first
	cmdline := readCmdline(panePid)
	if isAgentCmdline(cmdline) {
		return cmdline
	}

	// Walk children (shell → agent)
	childrenPath := "/proc/" + panePid + "/task/" + panePid + "/children"
	childrenBytes, err := os.ReadFile(childrenPath)
	if err != nil {
		return cmdline // return whatever the pane process is
	}

	children := strings.Fields(string(childrenBytes))
	for _, childPid := range children {
		childCmd := readCmdline(childPid)
		if isAgentCmdline(childCmd) {
			return childCmd
		}
		// Check grandchildren (cgroup-wrap → agent)
		gcPath := "/proc/" + childPid + "/task/" + childPid + "/children"
		gcBytes, err := os.ReadFile(gcPath)
		if err != nil {
			continue
		}
		for _, gcPid := range strings.Fields(string(gcBytes)) {
			gcCmd := readCmdline(gcPid)
			if isAgentCmdline(gcCmd) {
				return gcCmd
			}
		}
	}

	return cmdline // return pane process cmdline as fallback
}

// isAgentCmdline returns true if the cmdline contains a known agent,
// either as the main command or as the first arg of a wrapper (node/bun).
func isAgentCmdline(cmdline string) bool {
	if cmdline == "" {
		return false
	}
	parts := strings.Split(cmdline, "\x00")
	if len(parts) == 0 {
		return false
	}
	base := filepath.Base(parts[0])
	if isKnownAgent(base) {
		return true
	}
	// Check if wrapper (node/bun) is running an agent
	if isAgentWrapper(base) && len(parts) > 1 {
		argBase := filepath.Base(parts[1])
		return isKnownAgent(argBase)
	}
	return false
}

// readCmdline reads /proc/<pid>/cmdline and returns it as a space-joined string.
func readCmdline(pid string) string {
	data, err := os.ReadFile("/proc/" + pid + "/cmdline")
	if err != nil || len(data) == 0 {
		return ""
	}
	// cmdline uses null bytes as separators
	return string(data)
}

// extractBaseName gets the base command name from a null-separated cmdline.
func extractBaseName(cmdline string) string {
	if cmdline == "" {
		return ""
	}
	parts := strings.Split(cmdline, "\x00")
	if len(parts) == 0 {
		return ""
	}
	return filepath.Base(parts[0])
}

// isKnownAgent returns true if the command is a recognized agent runtime.
func isKnownAgent(base string) bool {
	return config.IsKnownPreset(base)
}

// isAgentWrapper returns true if the command is a runtime wrapper (node, bun, etc.)
// that may host an agent as its first argument.
func isAgentWrapper(base string) bool {
	switch base {
	case "node", "bun", "npx", "bunx":
		return true
	}
	return false
}

// parseRuntimeInfo extracts "runtime/model" from a null-separated cmdline.
// Handles direct invocation (claude --model opus) and wrapper patterns (node /path/to/pi).
func parseRuntimeInfo(cmdline string) string {
	if cmdline == "" {
		return ""
	}
	parts := strings.Split(cmdline, "\x00")
	if len(parts) == 0 {
		return ""
	}

	// Find the actual agent command — skip wrappers (node, bun, cgroup-wrap)
	cmd := ""
	startIdx := 0
	for i, part := range parts {
		base := filepath.Base(part)
		if isKnownAgent(base) {
			cmd = base
			startIdx = i
			break
		}
	}
	if cmd == "" {
		cmd = filepath.Base(parts[0])
	}

	// Extract model and provider from flags
	model := ""
	provider := ""
	for i := startIdx; i < len(parts); i++ {
		arg := parts[i]
		if (arg == "--model" || arg == "-m") && i+1 < len(parts) && parts[i+1] != "" {
			model = parts[i+1]
		}
		if arg == "--provider" && i+1 < len(parts) && parts[i+1] != "" {
			provider = parts[i+1]
		}
	}

	if model != "" {
		return cmd + "/" + model
	}
	if provider != "" {
		return cmd + "/" + provider
	}

	// For pi, check its settings file for actual default provider/model
	if cmd == "pi" {
		if piInfo := readPiDefaults(); piInfo != "" {
			return "pi/" + piInfo
		}
	}

	return cmd
}

// readPiDefaults reads ~/.pi/agent/settings.json to get the actual default provider/model.
func readPiDefaults() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(home, ".pi", "agent", "settings.json"))
	if err != nil {
		return ""
	}
	var settings struct {
		DefaultProvider string `json:"defaultProvider"`
		DefaultModel    string `json:"defaultModel"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return ""
	}
	if settings.DefaultModel != "" {
		return settings.DefaultModel
	}
	if settings.DefaultProvider != "" {
		return settings.DefaultProvider
	}
	return ""
}

// buildInfoFromConfig builds display info from a RuntimeConfig (fallback when not running).
func buildInfoFromConfig(rc *config.RuntimeConfig) string {
	if rc.Command == "" {
		return "claude"
	}
	cmd := filepath.Base(rc.Command)
	if cmd == "" {
		cmd = "claude"
	}
	if cmd == "cgroup-wrap" && len(rc.Args) > 0 {
		cmd = rc.Args[0]
	}

	model := ""
	for i, arg := range rc.Args {
		if (arg == "--model" || arg == "-m") && i+1 < len(rc.Args) {
			model = rc.Args[i+1]
			break
		}
	}

	if model != "" {
		return cmd + "/" + model
	}
	return cmd
}

func runStatus(cmd *cobra.Command, args []string) error {
	if statusWatch {
		return runStatusWatch(cmd, args)
	}
	return runStatusOnce(cmd, args)
}

func runStatusWatch(_ *cobra.Command, _ []string) error {
	if statusJSON {
		return fmt.Errorf("--json and --watch cannot be used together")
	}
	if statusInterval <= 0 {
		return fmt.Errorf("interval must be positive, got %d", statusInterval)
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	ticker := time.NewTicker(time.Duration(statusInterval) * time.Second)
	defer ticker.Stop()

	isTTY := term.IsTerminal(int(os.Stdout.Fd()))

	// Cache the last successful status to handle transient tmux/beads
	// failures. Watch mode spawns many tmux subprocesses per iteration;
	// under load the tmux server can intermittently fail, causing all
	// agents to appear as not running (empty bubbles).
	var cachedStatus *TownStatus
	var cachedAt time.Time
	maxStale := time.Duration(statusInterval) * time.Second * 5

	for {
		var buf bytes.Buffer

		if isTTY {
			buf.WriteString("\033[H\033[2J") // ANSI: cursor home + clear screen
		}

		timestamp := time.Now().Format("15:04:05")
		header := fmt.Sprintf("[%s] gt status --watch (every %ds, Ctrl+C to stop)", timestamp, statusInterval)
		if isTTY {
			fmt.Fprintf(&buf, "%s\n\n", style.Dim.Render(header))
		} else {
			fmt.Fprintf(&buf, "%s\n\n", header)
		}

		status, err := gatherStatus()
		usedCache := false

		// On error, retry once before giving up.
		if err != nil {
			status, err = gatherStatus()
		}

		if err == nil {
			// Detect degraded results: zero running agents when we
			// previously had some. This indicates a transient tmux
			// failure rather than all agents legitimately stopping.
			running := countRunningAgents(status)
			if running == 0 && cachedStatus != nil &&
				countRunningAgents(*cachedStatus) > 0 {
				// Retry once to confirm.
				retry, retryErr := gatherStatus()
				if retryErr == nil &&
					countRunningAgents(retry) > 0 {
					status = retry
				} else if time.Since(cachedAt) < maxStale {
					status = *cachedStatus
					usedCache = true
				}
			}
		} else if cachedStatus != nil &&
			time.Since(cachedAt) < maxStale {
			// Complete failure even after retry — use cache.
			status = *cachedStatus
			usedCache = true
			err = nil
		}

		if err != nil {
			fmt.Fprintf(&buf, "Error: %v\n", err)
		} else {
			if !usedCache {
				statusCopy := status
				cachedStatus = &statusCopy
				cachedAt = time.Now()
			}
			if usedCache {
				staleNote := fmt.Sprintf(
					"(using cached data from %s)",
					cachedAt.Format("15:04:05"),
				)
				if isTTY {
					fmt.Fprintf(&buf, "%s\n",
						style.Dim.Render(staleNote))
				} else {
					fmt.Fprintf(&buf, "%s\n", staleNote)
				}
			}
			if err := outputStatusText(&buf, status); err != nil {
				fmt.Fprintf(&buf, "Error: %v\n", err)
			}
		}

		// Write the entire frame atomically to prevent the terminal from
		// rendering a blank screen between the clear and the content.
		_, _ = os.Stdout.Write(buf.Bytes())

		select {
		case <-sigChan:
			if isTTY {
				fmt.Println("\nStopped.")
			}
			return nil
		case <-ticker.C:
		}
	}
}

// countRunningAgents returns the number of agents with Running=true
// across all global agents and rig agents in the status.
func countRunningAgents(s TownStatus) int {
	count := 0
	for _, a := range s.Agents {
		if a.Running {
			count++
		}
	}
	for _, r := range s.Rigs {
		for _, a := range r.Agents {
			if a.Running {
				count++
			}
		}
	}
	return count
}

func runStatusOnce(_ *cobra.Command, _ []string) error {
	status, err := gatherStatus()
	if err != nil {
		return err
	}
	if statusJSON {
		return outputStatusJSON(status)
	}
	return outputStatusText(os.Stdout, status)
}

func gatherStatus() (TownStatus, error) {
	// Find town root
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return TownStatus{}, fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	fast := statusFast
	skipBeadsPrefetch := false
	if !fast {
		if release, ok := tryStatusDetailLock(townRoot); ok {
			defer release()
		} else {
			fast = true
			skipBeadsPrefetch = true
		}
	}

	// Load town config
	townConfigPath := constants.MayorTownPath(townRoot)
	townConfig, err := config.LoadTownConfig(townConfigPath)
	if err != nil {
		// Try to continue without config
		townConfig = &config.TownConfig{Name: filepath.Base(townRoot)}
	}

	// Load rigs config
	rigsConfigPath := constants.MayorRigsPath(townRoot)
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		// Empty config if file doesn't exist
		rigsConfig = &config.RigsConfig{Rigs: make(map[string]config.RigEntry)}
	}

	// Load town settings for agent display info
	townSettings, _ := config.LoadOrCreateTownSettings(config.TownSettingsPath(townRoot))

	// Create rig manager
	g := git.NewGit(townRoot)
	mgr := rig.NewManager(townRoot, rigsConfig, g)

	// Create tmux instance for runtime checks
	t := tmux.NewTmux()

	// Pre-fetch all tmux sessions and verify agent liveness for O(1) lookup.
	// A Gas Town session is only considered "running" if the agent process is
	// alive inside it, not merely if the tmux session exists. This prevents
	// zombie sessions (tmux alive, agent dead) from showing as running.
	// See: gt-bd6i3
	allSessions := make(map[string]bool)
	if sessions, err := t.ListSessions(); err == nil {
		var sessionMu sync.Mutex
		var sessionWg sync.WaitGroup
		for _, s := range sessions {
			if session.IsKnownSession(s) {
				sessionWg.Add(1)
				go func(name string) {
					defer sessionWg.Done()
					alive := t.IsAgentAlive(name)
					sessionMu.Lock()
					allSessions[name] = alive
					sessionMu.Unlock()
				}(s)
			} else {
				allSessions[s] = true
			}
		}
		sessionWg.Wait()
	}

	// Discover rigs
	rigs, err := mgr.DiscoverRigs()
	if err != nil {
		return TownStatus{}, fmt.Errorf("discovering rigs: %w", err)
	}

	// Pre-fetch agent beads across all rig-specific beads DBs. If another status
	// process already holds the detail lock, skip this Dolt-heavy section and
	// render runtime-only status instead of amplifying the query storm.
	allAgentBeads := make(map[string]*beads.Issue)
	allHookBeads := make(map[string]*beads.Issue)
	var beadsMu sync.Mutex // Protects allAgentBeads and allHookBeads

	// Helper to safely merge beads into the shared maps
	mergeAgentBeads := func(beadsMap map[string]*beads.Issue) {
		beadsMu.Lock()
		for id, issue := range beadsMap {
			allAgentBeads[id] = issue
		}
		beadsMu.Unlock()
	}
	mergeHookBeads := func(beadsMap map[string]*beads.Issue) {
		beadsMu.Lock()
		for id, issue := range beadsMap {
			allHookBeads[id] = issue
		}
		beadsMu.Unlock()
	}

	if !skipBeadsPrefetch {
		var beadsWg sync.WaitGroup

		// Fetch town-level agent beads (Mayor, Deacon) from town beads
		townBeadsPath := beads.GetTownBeadsPath(townRoot)
		beadsWg.Add(1)
		go func() {
			defer beadsWg.Done()
			townBeadsClient := beads.New(townBeadsPath)
			townAgentBeads, _ := townBeadsClient.ListAgentBeads()
			mergeAgentBeads(townAgentBeads)

			// Fetch hook beads from town beads
			var townHookIDs []string
			for _, issue := range townAgentBeads {
				hookID := issue.HookBead
				if hookID == "" {
					fields := beads.ParseAgentFields(issue.Description)
					if fields != nil {
						hookID = fields.HookBead
					}
				}
				if hookID != "" {
					townHookIDs = append(townHookIDs, hookID)
				}
			}
			if len(townHookIDs) > 0 {
				townHookBeads, _ := townBeadsClient.ShowMultiple(townHookIDs)
				mergeHookBeads(townHookBeads)
			}
		}()

		// Fetch rig-level agent beads in parallel
		for _, r := range rigs {
			beadsWg.Add(1)
			go func(r *rig.Rig) {
				defer beadsWg.Done()
				rigBeadsPath := filepath.Join(r.Path, "mayor", "rig")
				rigBeads := beads.New(rigBeadsPath)
				rigAgentBeads, _ := rigBeads.ListAgentBeads()
				if rigAgentBeads == nil {
					return
				}
				mergeAgentBeads(rigAgentBeads)

				var hookIDs []string
				for _, issue := range rigAgentBeads {
					// Use the HookBead field from the database column; fall back for legacy beads.
					hookID := issue.HookBead
					if hookID == "" {
						fields := beads.ParseAgentFields(issue.Description)
						if fields != nil {
							hookID = fields.HookBead
						}
					}
					if hookID != "" {
						hookIDs = append(hookIDs, hookID)
					}
				}

				if len(hookIDs) == 0 {
					return
				}
				hookBeads, _ := rigBeads.ShowMultiple(hookIDs)
				mergeHookBeads(hookBeads)
			}(r)
		}

		beadsWg.Wait()
	}

	// Create mail router for inbox lookups
	mailRouter := mail.NewRouter(townRoot)

	// Load overseer config
	var overseerInfo *OverseerInfo
	if overseerConfig, err := config.LoadOrDetectOverseer(townRoot); err == nil && overseerConfig != nil {
		overseerInfo = &OverseerInfo{
			Name:     overseerConfig.Name,
			Email:    overseerConfig.Email,
			Username: overseerConfig.Username,
			Source:   overseerConfig.Source,
		}
		// Get overseer mail count (skip in --fast mode)
		if !fast {
			if mailbox, err := mailRouter.GetMailbox("overseer"); err == nil {
				_, unread, _ := mailbox.Count()
				overseerInfo.UnreadMail = unread
			}
		}
	}

	// Build status - parallel fetch global agents and rigs
	status := TownStatus{
		Name:     townConfig.Name,
		Location: townRoot,
		Overseer: overseerInfo,
		DND:      detectCurrentDNDStatus(townRoot),
		Rigs:     make([]RigStatus, len(rigs)),
	}

	// Daemon status
	if daemonRunning, daemonPid, err := daemon.IsRunning(townRoot); err == nil {
		status.Daemon = &ServiceInfo{Running: daemonRunning, PID: daemonPid}
	}

	// Dolt status
	doltCfg := doltserver.DefaultConfig(townRoot)
	if doltCfg.IsRemote() {
		status.Dolt = &DoltInfo{Remote: true, Port: doltCfg.Port}
	} else {
		doltRunning, doltPid, _ := doltserver.IsRunning(townRoot)
		port := doltCfg.Port
		if doltRunning {
			// Read the actual port from state — doltCfg.Port comes from
			// DefaultConfig which reads GT_DOLT_PORT from the shell env,
			// but gt status is typically run without that env var set.
			if state, err := doltserver.LoadState(townRoot); err == nil && state.Port > 0 {
				port = state.Port
			}
		}
		doltInfo := &DoltInfo{
			Running: doltRunning,
			PID:     doltPid,
			Port:    port,
			DataDir: doltCfg.DataDir,
		}
		// Check if port is held by another town's Dolt
		if !doltRunning {
			if conflictPid, conflictDir := doltserver.CheckPortConflict(townRoot); conflictPid > 0 {
				doltInfo.PortConflict = true
				doltInfo.ConflictOwner = conflictDir
			}
		}
		status.Dolt = doltInfo
	}

	// Tmux status
	socket := tmux.GetDefaultSocket()
	socketLabel := "default"
	if socket != "" {
		socketLabel = socket
	}
	tmuxInfo := &TmuxInfo{
		Socket:       socketLabel,
		SessionCount: len(allSessions),
		Running:      len(allSessions) > 0,
	}
	// Resolve socket path: /tmp/tmux-<UID>/<socket>
	tmuxInfo.SocketPath = filepath.Join(tmux.SocketDir(), socketLabel)
	if _, err := os.Stat(tmuxInfo.SocketPath); err == nil {
		tmuxInfo.Running = true
		tmuxInfo.PID = tmux.NewTmux().ServerPID()
	}
	status.Tmux = tmuxInfo

	// ACP status
	if mayor.IsACPActive(townRoot) {
		acpPid, _ := mayor.GetACPPid(townRoot)
		status.ACP = &ServiceInfo{Running: true, PID: acpPid}
	}

	var wg sync.WaitGroup

	// Fetch global agents in parallel with rig discovery
	wg.Add(1)
	go func() {
		defer wg.Done()
		status.Agents = discoverGlobalAgents(townRoot, allSessions, allAgentBeads, allHookBeads, mailRouter, fast)
	}()

	// Process all rigs in parallel
	rigActiveHooks := make([]int, len(rigs)) // Track hooks per rig for thread safety
	for i, r := range rigs {
		wg.Add(1)
		go func(idx int, r *rig.Rig) {
			defer wg.Done()

			rs := RigStatus{
				Name:         r.Name,
				Polecats:     r.Polecats,
				PolecatCount: len(r.Polecats),
				HasWitness:   r.HasWitness,
				HasRefinery:  r.HasRefinery,
			}

			// Count crew workers
			crewGit := git.NewGit(r.Path)
			crewMgr := crew.NewManager(r, crewGit)
			if workers, err := crewMgr.List(); err == nil {
				for _, w := range workers {
					rs.Crews = append(rs.Crews, w.Name)
				}
				rs.CrewCount = len(workers)
			}

			// Run hooks, agents, and MQ discovery concurrently within this rig.
			// Each was previously sequential; now they overlap since they use
			// independent bd/beads calls.
			var rigWg sync.WaitGroup

			// Discover hooks for all agents in this rig
			// In --fast mode, skip expensive handoff bead lookups. Hook info comes from
			// preloaded agent beads via discoverRigAgents instead.
			if !fast {
				rigWg.Add(1)
				go func() {
					defer rigWg.Done()
					rs.Hooks = discoverRigHooks(r, rs.Crews)
				}()
			}

			// Get MQ summary if rig has a refinery
			// Skip in --fast mode to avoid expensive bd queries
			if !fast {
				rigWg.Add(1)
				go func() {
					defer rigWg.Done()
					rs.MQ = getMQSummary(r)
				}()
			}

			// Discover runtime state for all agents in this rig
			// (uses preloaded maps, so it's fast — but run concurrently with hooks/MQ)
			rigWg.Add(1)
			go func() {
				defer rigWg.Done()
				rs.Agents = discoverRigAgents(allSessions, r, rs.Crews, allAgentBeads, allHookBeads, mailRouter, fast)
			}()

			rigWg.Wait()

			activeHooks := 0
			for _, hook := range rs.Hooks {
				if hook.HasWork {
					activeHooks++
				}
			}
			rigActiveHooks[idx] = activeHooks

			status.Rigs[idx] = rs
		}(i, r)
	}

	wg.Wait()

	// Enrich agents with runtime info — inspect actual running processes
	for i := range status.Agents {
		a := &status.Agents[i]
		alias, info := resolveAgentDisplay(townRoot, townSettings, a.Role, a.Session, a.Running)
		a.AgentAlias = alias
		a.AgentInfo = info
	}
	for i := range status.Rigs {
		for j := range status.Rigs[i].Agents {
			a := &status.Rigs[i].Agents[j]
			alias, info := resolveAgentDisplay(townRoot, townSettings, a.Role, a.Session, a.Running)
			a.AgentAlias = alias
			a.AgentInfo = info
		}
	}

	// Aggregate summary (after parallel work completes)
	for i, rs := range status.Rigs {
		status.Summary.PolecatCount += rs.PolecatCount
		status.Summary.CrewCount += rs.CrewCount
		status.Summary.ActiveHooks += rigActiveHooks[i]
		if rs.HasWitness {
			status.Summary.WitnessCount++
		}
		if rs.HasRefinery {
			status.Summary.RefineryCount++
		}
	}
	status.Summary.RigCount = len(rigs)

	return status, nil
}

func outputStatusJSON(status TownStatus) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(status)
}

func outputStatusText(w io.Writer, status TownStatus) error {
	// Header
	fmt.Fprintf(w, "%s %s\n", style.Bold.Render("Town:"), status.Name)
	fmt.Fprintf(w, "%s\n\n", style.Dim.Render(status.Location))

	// E-stop banner (if active)
	addEstopToStatus(status.Location)

	// Overseer info
	if status.Overseer != nil {
		overseerDisplay := status.Overseer.Name
		if status.Overseer.Email != "" {
			overseerDisplay = fmt.Sprintf("%s <%s>", status.Overseer.Name, status.Overseer.Email)
		} else if status.Overseer.Username != "" && status.Overseer.Username != status.Overseer.Name {
			overseerDisplay = fmt.Sprintf("%s (@%s)", status.Overseer.Name, status.Overseer.Username)
		}
		fmt.Fprintf(w, "👤 %s %s\n", style.Bold.Render("Overseer:"), overseerDisplay)
		if status.Overseer.UnreadMail > 0 {
			fmt.Fprintf(w, "   📬 %d unread\n", status.Overseer.UnreadMail)
		}
		fmt.Fprintln(w)
	}

	// Current agent notification mode (DND)
	if status.DND != nil {
		icon := "🔔"
		state := "off"
		desc := "notifications normal"
		if status.DND.Enabled {
			icon = "🔕"
			state = "on"
			desc = "notifications muted"
		}
		fmt.Fprintf(w, "%s %s %s", icon, style.Bold.Render("DND:"), style.Bold.Render(state))
		if status.DND.Agent != "" {
			fmt.Fprintf(w, " %s", style.Dim.Render("("+status.DND.Agent+")"))
		}
		fmt.Fprintf(w, "\n   %s\n\n", style.Dim.Render(desc))
	}

	// Infrastructure services
	if status.Daemon != nil || status.Dolt != nil || status.Tmux != nil {
		fmt.Fprintf(w, "%s ", style.Bold.Render("Services:"))
		var parts []string
		if status.Daemon != nil {
			if status.Daemon.Running {
				parts = append(parts, fmt.Sprintf("daemon %s", style.Dim.Render(fmt.Sprintf("(PID %d)", status.Daemon.PID))))
			} else {
				parts = append(parts, fmt.Sprintf("daemon %s", style.Dim.Render("(stopped)")))
			}
		}
		if status.Dolt != nil {
			if status.Dolt.Remote {
				parts = append(parts, fmt.Sprintf("dolt %s", style.Dim.Render(fmt.Sprintf("(remote :%d)", status.Dolt.Port))))
			} else if status.Dolt.Running {
				dataDir := status.Dolt.DataDir
				if home, err := os.UserHomeDir(); err == nil {
					dataDir = strings.Replace(dataDir, home, "~", 1)
				}
				parts = append(parts, fmt.Sprintf("dolt %s", style.Dim.Render(fmt.Sprintf("(PID %d, :%d, %s)", status.Dolt.PID, status.Dolt.Port, dataDir))))
			} else if status.Dolt.PortConflict {
				parts = append(parts, fmt.Sprintf("dolt %s", style.Bold.Render(fmt.Sprintf("(stopped, :%d ⚠ port used by %s)", status.Dolt.Port, status.Dolt.ConflictOwner))))
			} else {
				parts = append(parts, fmt.Sprintf("dolt %s", style.Dim.Render(fmt.Sprintf("(stopped, :%d)", status.Dolt.Port))))
			}
		}
		if status.Tmux != nil {
			if status.Tmux.Running {
				parts = append(parts, fmt.Sprintf("tmux %s", style.Dim.Render(fmt.Sprintf("(-L %s, PID %d, %d sessions, %s)", status.Tmux.Socket, status.Tmux.PID, status.Tmux.SessionCount, status.Tmux.SocketPath))))
			} else {
				parts = append(parts, fmt.Sprintf("tmux %s", style.Dim.Render(fmt.Sprintf("(-L %s, no server)", status.Tmux.Socket))))
			}
		}
		if status.ACP != nil {
			if status.ACP.Running {
				parts = append(parts, fmt.Sprintf("acp %s", style.Dim.Render(fmt.Sprintf("(PID %d)", status.ACP.PID))))
			} else {
				parts = append(parts, fmt.Sprintf("acp %s", style.Dim.Render("(stopped)")))
			}
		}
		fmt.Fprintf(w, "%s\n", strings.Join(parts, "  "))
		fmt.Fprintln(w)
	}

	// Role icons - uses centralized emojis from constants package
	roleIcons := map[string]string{
		constants.RoleMayor:    constants.EmojiMayor,
		constants.RoleDeacon:   constants.EmojiDeacon,
		constants.RoleWitness:  constants.EmojiWitness,
		constants.RoleRefinery: constants.EmojiRefinery,
		constants.RoleCrew:     constants.EmojiCrew,
		constants.RolePolecat:  constants.EmojiPolecat,
		// Legacy names for backwards compatibility
		"coordinator":  constants.EmojiMayor,
		"health-check": constants.EmojiDeacon,
	}

	// Global Agents (Mayor, Deacon)
	for _, agent := range status.Agents {
		icon := roleIcons[agent.Role]
		if icon == "" {
			icon = roleIcons[agent.Name]
		}
		if statusVerbose {
			fmt.Fprintf(w, "%s %s\n", icon, style.Bold.Render(capitalizeFirst(agent.Name)))
			renderAgentDetails(w, agent, "   ", nil, status.Location)
			fmt.Fprintln(w)
		} else {
			// Compact: icon + name on one line
			renderAgentCompact(w, agent, icon+" ", nil, status.Location)
		}
	}
	if !statusVerbose && len(status.Agents) > 0 {
		fmt.Fprintln(w)
	}

	if len(status.Rigs) == 0 {
		fmt.Fprintf(w, "%s\n", style.Dim.Render("No rigs registered. Use 'gt rig add' to add one."))
		return nil
	}

	// Rigs
	for _, r := range status.Rigs {
		// Rig header with separator
		fmt.Fprintf(w, "─── %s ───────────────────────────────────────────\n\n", style.Bold.Render(r.Name+"/"))

		// Group agents by role
		var witnesses, refineries, crews, polecats []AgentRuntime
		for _, agent := range r.Agents {
			switch agent.Role {
			case constants.RoleWitness:
				witnesses = append(witnesses, agent)
			case constants.RoleRefinery:
				refineries = append(refineries, agent)
			case constants.RoleCrew:
				crews = append(crews, agent)
			case constants.RolePolecat:
				polecats = append(polecats, agent)
			}
		}

		// Witness
		if len(witnesses) > 0 {
			if statusVerbose {
				fmt.Fprintf(w, "%s %s\n", roleIcons[constants.RoleWitness], style.Bold.Render("Witness"))
				for _, agent := range witnesses {
					renderAgentDetails(w, agent, "   ", r.Hooks, status.Location)
				}
				fmt.Fprintln(w)
			} else {
				for _, agent := range witnesses {
					renderAgentCompact(w, agent, roleIcons[constants.RoleWitness]+" ", r.Hooks, status.Location)
				}
			}
		}

		// Refinery
		if len(refineries) > 0 {
			if statusVerbose {
				fmt.Fprintf(w, "%s %s\n", roleIcons[constants.RoleRefinery], style.Bold.Render("Refinery"))
				for _, agent := range refineries {
					renderAgentDetails(w, agent, "   ", r.Hooks, status.Location)
				}
				// MQ summary (shown under refinery)
				if r.MQ != nil {
					mqStr := formatMQSummary(r.MQ)
					if mqStr != "" {
						fmt.Fprintf(w, "   MQ: %s\n", mqStr)
					}
				}
				fmt.Fprintln(w)
			} else {
				for _, agent := range refineries {
					// Compact: include MQ on same line if present
					mqSuffix := ""
					if r.MQ != nil {
						mqStr := formatMQSummaryCompact(r.MQ)
						if mqStr != "" {
							mqSuffix = "  " + mqStr
						}
					}
					renderAgentCompactWithSuffix(w, agent, roleIcons[constants.RoleRefinery]+" ", r.Hooks, status.Location, mqSuffix)
				}
			}
		}

		// Crew
		if len(crews) > 0 {
			if statusVerbose {
				fmt.Fprintf(w, "%s %s (%d)\n", roleIcons[constants.RoleCrew], style.Bold.Render("Crew"), len(crews))
				for _, agent := range crews {
					renderAgentDetails(w, agent, "   ", r.Hooks, status.Location)
				}
				fmt.Fprintln(w)
			} else {
				fmt.Fprintf(w, "%s %s (%d)\n", roleIcons[constants.RoleCrew], style.Bold.Render("Crew"), len(crews))
				for _, agent := range crews {
					renderAgentCompact(w, agent, "   ", r.Hooks, status.Location)
				}
			}
		}

		// Polecats
		if len(polecats) > 0 {
			if statusVerbose {
				fmt.Fprintf(w, "%s %s (%d)\n", roleIcons[constants.RolePolecat], style.Bold.Render("Polecats"), len(polecats))
				for _, agent := range polecats {
					renderAgentDetails(w, agent, "   ", r.Hooks, status.Location)
				}
				fmt.Fprintln(w)
			} else {
				fmt.Fprintf(w, "%s %s (%d)\n", roleIcons[constants.RolePolecat], style.Bold.Render("Polecats"), len(polecats))
				for _, agent := range polecats {
					renderAgentCompact(w, agent, "   ", r.Hooks, status.Location)
				}
			}
		}

		// No agents
		if len(witnesses) == 0 && len(refineries) == 0 && len(crews) == 0 && len(polecats) == 0 {
			fmt.Fprintf(w, "   %s\n", style.Dim.Render("(no agents)"))
		}
		fmt.Fprintln(w)
	}

	return nil
}

// renderAgentDetails renders full agent bead details
func renderAgentDetails(w io.Writer, agent AgentRuntime, indent string, hooks []AgentHookInfo, townRoot string) { //nolint:unparam // indent kept for future customization
	// Line 1: Agent bead ID + status
	// Per gt-zecmc: derive status from tmux (observable reality), not bead state.
	// "Discover, don't track" - agent liveness is observable from tmux session.
	sessionExists := agent.Running

	var statusStr string
	var stateInfo string

	if sessionExists {
		statusStr = style.Success.Render("running")
	} else {
		statusStr = style.Error.Render("stopped")
	}

	// Show non-observable states that represent intentional agent decisions.
	// These can't be discovered from tmux and are legitimately recorded in beads.
	beadState := agent.State
	switch beadState {
	case "stuck":
		// Agent escalated - needs help
		stateInfo = style.Warning.Render(" [stuck]")
	case "awaiting-gate":
		// Agent waiting for external trigger (phase gate)
		stateInfo = style.Dim.Render(" [awaiting-gate]")
	case "muted", "paused", "degraded":
		// Other intentional non-observable states
		stateInfo = style.Dim.Render(fmt.Sprintf(" [%s]", beadState))
		// Ignore observable states: "running", "idle", "dead", "done", "stopped", ""
		// These should be derived from tmux, not bead.
	}

	// Build agent bead ID using canonical naming: prefix-rig-role-name
	agentBeadID := "gt-" + agent.Name
	if agent.Address != "" && agent.Address != agent.Name {
		// Use address for full path agents like gastown/crew/joe → gt-gastown-crew-joe
		addr := strings.TrimSuffix(agent.Address, "/") // Remove trailing slash for global agents
		parts := strings.Split(addr, "/")
		if len(parts) == 1 {
			// Global agent: mayor/, deacon/ → hq-mayor, hq-deacon
			agentBeadID = beads.AgentBeadIDWithPrefix(beads.TownBeadsPrefix, "", parts[0], "")
		} else if len(parts) >= 2 {
			rig := parts[0]
			prefix := beads.GetPrefixForRig(townRoot, rig)
			if parts[1] == constants.RoleCrew && len(parts) >= 3 {
				agentBeadID = beads.CrewBeadIDWithPrefix(prefix, rig, parts[2])
			} else if parts[1] == constants.RoleWitness {
				agentBeadID = beads.WitnessBeadIDWithPrefix(prefix, rig)
			} else if parts[1] == constants.RoleRefinery {
				agentBeadID = beads.RefineryBeadIDWithPrefix(prefix, rig)
			} else if len(parts) == 2 {
				// polecat: rig/name
				agentBeadID = beads.PolecatBeadIDWithPrefix(prefix, rig, parts[1])
			}
		}
	}

	fmt.Fprintf(w, "%s%s %s%s\n", indent, style.Dim.Render(agentBeadID), statusStr, stateInfo)

	// Line 2: Agent runtime info
	if agent.AgentInfo != "" {
		fmt.Printf("%s  agent: %s\n", indent, agent.AgentInfo)
	}

	// Line 3: Hook bead (pinned work)
	hookStr := style.Dim.Render("(none)")
	hookBead := agent.HookBead
	hookTitle := agent.WorkTitle

	// Fall back to hooks array if agent bead doesn't have hook info
	if hookBead == "" && hooks != nil {
		for _, h := range hooks {
			if h.Agent == agent.Address && h.HasWork {
				hookBead = h.Molecule
				hookTitle = h.Title
				break
			}
		}
	}

	if hookBead != "" {
		if hookTitle != "" {
			hookStr = fmt.Sprintf("%s → %s", hookBead, truncateWithEllipsis(hookTitle, 40))
		} else {
			hookStr = hookBead
		}
	} else if hookTitle != "" {
		// Has title but no molecule ID
		hookStr = truncateWithEllipsis(hookTitle, 50)
	}

	fmt.Fprintf(w, "%s  hook: %s\n", indent, hookStr)

	// Line 4: Notification mode (DND)
	if agent.NotificationLevel == beads.NotifyMuted {
		fmt.Fprintf(w, "%s  notify: 🔕 muted (DND)\n", indent)
	}

	// Line 5: Mail (if any unread)
	if agent.UnreadMail > 0 {
		mailStr := fmt.Sprintf("📬 %d unread", agent.UnreadMail)
		if agent.FirstSubject != "" {
			mailStr = fmt.Sprintf("📬 %d unread → %s", agent.UnreadMail, truncateWithEllipsis(agent.FirstSubject, 35))
		}
		fmt.Fprintf(w, "%s  mail: %s\n", indent, mailStr)
	}
}

// formatMQSummary formats the MQ status for verbose display
func formatMQSummary(mq *MQSummary) string {
	if mq == nil {
		return ""
	}
	mqParts := []string{}
	if mq.Pending > 0 {
		mqParts = append(mqParts, fmt.Sprintf("%d pending", mq.Pending))
	}
	if mq.InFlight > 0 {
		mqParts = append(mqParts, style.Warning.Render(fmt.Sprintf("%d in-flight", mq.InFlight)))
	}
	if mq.Blocked > 0 {
		mqParts = append(mqParts, style.Dim.Render(fmt.Sprintf("%d blocked", mq.Blocked)))
	}
	if len(mqParts) == 0 {
		return ""
	}
	// Add state indicator
	stateIcon := "○" // idle
	switch mq.State {
	case "processing":
		stateIcon = style.Success.Render("●")
	case "blocked":
		stateIcon = style.Error.Render("○")
	}
	// Add health warning if stale
	healthSuffix := ""
	if mq.Health == "stale" {
		healthSuffix = style.Error.Render(" [stale]")
	}
	return fmt.Sprintf("%s %s%s", stateIcon, strings.Join(mqParts, ", "), healthSuffix)
}

// formatMQSummaryCompact formats MQ status for compact single-line display
func formatMQSummaryCompact(mq *MQSummary) string {
	if mq == nil {
		return ""
	}
	// Very compact: "MQ:12" or "MQ:12 [stale]"
	total := mq.Pending + mq.InFlight + mq.Blocked
	if total == 0 {
		return ""
	}
	healthSuffix := ""
	if mq.Health == "stale" {
		healthSuffix = style.Error.Render("[stale]")
	}
	return fmt.Sprintf("MQ:%d%s", total, healthSuffix)
}

// renderAgentCompactWithSuffix renders a single-line agent status with an extra suffix
func renderAgentCompactWithSuffix(w io.Writer, agent AgentRuntime, indent string, hooks []AgentHookInfo, _ string, suffix string) {
	// Build status indicator (gt-zecmc: use tmux state, not bead state)
	statusIndicator := buildStatusIndicator(agent)

	// Get hook info
	hookBead := agent.HookBead
	hookTitle := agent.WorkTitle
	if hookBead == "" && hooks != nil {
		for _, h := range hooks {
			if h.Agent == agent.Address && h.HasWork {
				hookBead = h.Molecule
				hookTitle = h.Title
				break
			}
		}
	}

	// Build hook suffix
	hookSuffix := ""
	if hookBead != "" {
		if hookTitle != "" {
			hookSuffix = style.Dim.Render(" → ") + truncateWithEllipsis(hookTitle, 30)
		} else {
			hookSuffix = style.Dim.Render(" → ") + hookBead
		}
	} else if hookTitle != "" {
		hookSuffix = style.Dim.Render(" → ") + truncateWithEllipsis(hookTitle, 30)
	}

	// Mail indicator
	mailSuffix := ""
	if agent.UnreadMail > 0 {
		mailSuffix = fmt.Sprintf(" 📬%d", agent.UnreadMail)
	}

	// Agent runtime info
	agentSuffix := ""
	if agent.AgentInfo != "" {
		agentSuffix = " " + style.Dim.Render("["+agent.AgentInfo+"]")
	}

	// Print single line: name + status + agent-info + hook + mail + suffix
	fmt.Fprintf(w, "%s%-12s %s%s%s%s%s\n", indent, agent.Name, statusIndicator, agentSuffix, hookSuffix, mailSuffix, suffix)
}

// renderAgentCompact renders a single-line agent status
func renderAgentCompact(w io.Writer, agent AgentRuntime, indent string, hooks []AgentHookInfo, _ string) {
	// Build status indicator (gt-zecmc: use tmux state, not bead state)
	statusIndicator := buildStatusIndicator(agent)

	// Get hook info
	hookBead := agent.HookBead
	hookTitle := agent.WorkTitle
	if hookBead == "" && hooks != nil {
		for _, h := range hooks {
			if h.Agent == agent.Address && h.HasWork {
				hookBead = h.Molecule
				hookTitle = h.Title
				break
			}
		}
	}

	// Build hook suffix
	hookSuffix := ""
	if hookBead != "" {
		if hookTitle != "" {
			hookSuffix = style.Dim.Render(" → ") + truncateWithEllipsis(hookTitle, 30)
		} else {
			hookSuffix = style.Dim.Render(" → ") + hookBead
		}
	} else if hookTitle != "" {
		hookSuffix = style.Dim.Render(" → ") + truncateWithEllipsis(hookTitle, 30)
	}

	// Mail indicator
	mailSuffix := ""
	if agent.UnreadMail > 0 {
		mailSuffix = fmt.Sprintf(" 📬%d", agent.UnreadMail)
	}

	// Agent runtime info
	agentSuffix := ""
	if agent.AgentInfo != "" {
		agentSuffix = " " + style.Dim.Render("["+agent.AgentInfo+"]")
	}

	// Print single line: name + status + agent-info + hook + mail
	fmt.Fprintf(w, "%s%-12s %s%s%s%s\n", indent, agent.Name, statusIndicator, agentSuffix, hookSuffix, mailSuffix)
}

// buildStatusIndicator creates the visual status indicator for an agent.
// Per gt-zecmc: uses tmux state (observable reality), not bead state.
// Non-observable states (stuck, awaiting-gate, muted, etc.) are shown as suffixes.
func buildStatusIndicator(agent AgentRuntime) string {
	sessionExists := agent.Running

	// Base indicator from tmux state or ACP state
	var indicator string
	if sessionExists {
		indicator = style.Success.Render("●")
	} else {
		indicator = style.Error.Render("○")
	}

	// Add mode info if ACP
	if agent.ACP {
		indicator += style.Dim.Render(" acp")
	}

	// Add non-observable state suffix if present
	beadState := agent.State
	switch beadState {
	case "stuck":
		indicator += style.Warning.Render(" stuck")
	case "awaiting-gate":
		indicator += style.Dim.Render(" gate")
	case "muted", "paused", "degraded":
		indicator += style.Dim.Render(" " + beadState)
		// Ignore observable states: running, idle, dead, done, stopped, ""
	}

	if agent.NotificationLevel == beads.NotifyMuted {
		indicator += style.Dim.Render(" 🔕")
	}

	return indicator
}

// formatHookInfo formats the hook bead and title for display
func formatHookInfo(hookBead, title string, maxLen int) string {
	if hookBead == "" {
		return ""
	}
	if title == "" {
		return fmt.Sprintf(" → %s", hookBead)
	}
	title = truncateWithEllipsis(title, maxLen)
	return fmt.Sprintf(" → %s", title)
}

// truncateWithEllipsis shortens a string to maxLen, adding "..." if truncated
func truncateWithEllipsis(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen < 4 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// capitalizeFirst capitalizes the first letter of a string
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	return string(s[0]-32) + s[1:]
}

// discoverRigHooks finds all hook attachments for agents in a rig.
// It fetches all pinned handoff beads in a single bd call, then resolves
// each agent's hook in-memory. This replaces the previous N+1 pattern where
// each agent triggered a separate bd subprocess.
func discoverRigHooks(r *rig.Rig, crews []string) []AgentHookInfo {
	var hooks []AgentHookInfo

	// Create beads instance for the rig
	b := beads.New(r.Path)

	// Batch-fetch all handoff beads in one bd call
	allHandoffs, err := b.FindAllHandoffBeads()
	if err != nil {
		// On error, return empty hooks for all agents rather than failing
		allHandoffs = make(map[string]*beads.Issue)
	}

	// Check polecats
	for _, name := range r.Polecats {
		hooks = append(hooks, resolveHookFromMap(allHandoffs, name, r.Name+"/"+name, constants.RolePolecat))
	}

	// Check crew workers
	for _, name := range crews {
		hooks = append(hooks, resolveHookFromMap(allHandoffs, name, r.Name+"/crew/"+name, constants.RoleCrew))
	}

	// Check witness
	if r.HasWitness {
		hooks = append(hooks, resolveHookFromMap(allHandoffs, constants.RoleWitness, r.Name+"/witness", constants.RoleWitness))
	}

	// Check refinery
	if r.HasRefinery {
		hooks = append(hooks, resolveHookFromMap(allHandoffs, constants.RoleRefinery, r.Name+"/refinery", constants.RoleRefinery))
	}

	return hooks
}

// resolveHookFromMap builds an AgentHookInfo from a pre-fetched map of handoff beads.
// This is the in-memory equivalent of getAgentHook, avoiding per-agent bd subprocess calls.
func resolveHookFromMap(allHandoffs map[string]*beads.Issue, role, agentAddress, roleType string) AgentHookInfo {
	hook := AgentHookInfo{
		Agent: agentAddress,
		Role:  roleType,
	}

	handoff, ok := allHandoffs[role]
	if !ok || handoff == nil {
		return hook
	}

	attachment := beads.ParseAttachmentFields(handoff)
	if attachment != nil && attachment.AttachedMolecule != "" {
		hook.HasWork = true
		hook.Molecule = attachment.AttachedMolecule
		hook.Title = handoff.Title
	} else if handoff.Description != "" {
		hook.HasWork = true
		hook.Title = handoff.Title
	}

	return hook
}

// discoverGlobalAgents checks runtime state for town-level agents (Mayor, Deacon).
// Uses parallel fetching for performance. If skipMail is true, mail lookups are skipped.
// allSessions is a preloaded map of tmux sessions for O(1) lookup.
// allAgentBeads is a preloaded map of agent beads for O(1) lookup.
// allHookBeads is a preloaded map of hook beads for O(1) lookup.
func discoverGlobalAgents(townRoot string, allSessions map[string]bool, allAgentBeads map[string]*beads.Issue, allHookBeads map[string]*beads.Issue, mailRouter *mail.Router, skipMail bool) []AgentRuntime {
	// Get session names dynamically
	mayorSession := getMayorSessionName()
	deaconSession := getDeaconSessionName()

	// Define agents to discover
	// Note: Mayor and Deacon are town-level agents with hq- prefix bead IDs
	agentDefs := []struct {
		name    string
		address string
		session string
		role    string
		beadID  string
	}{
		{constants.RoleMayor, constants.RoleMayor + "/", mayorSession, "coordinator", beads.MayorBeadIDTown()},
		{constants.RoleDeacon, constants.RoleDeacon + "/", deaconSession, "health-check", beads.DeaconBeadIDTown()},
	}

	agents := make([]AgentRuntime, len(agentDefs))
	var wg sync.WaitGroup

	for i, def := range agentDefs {
		wg.Add(1)
		go func(idx int, d struct {
			name    string
			address string
			session string
			role    string
			beadID  string
		}) {
			defer wg.Done()

			agent := AgentRuntime{
				Name:    d.name,
				Address: d.address,
				Session: d.session,
				Role:    d.role,
			}

			// Check tmux session from preloaded map (O(1))
			agent.Running = allSessions[d.session]

			// Check for ACP session (for Mayor)
			if d.name == "mayor" {
				if mayor.IsACPActive(townRoot) {
					agent.ACP = true
					agent.Running = true
				}
			}

			// Look up agent bead from preloaded map (O(1))
			if issue, ok := allAgentBeads[d.beadID]; ok {
				// Prefer database columns over description parsing
				// HookBead column is authoritative (cleared by unsling)
				agent.HookBead = issue.HookBead
				agent.State = beads.ResolveAgentState(issue.Description, issue.AgentState)
				if agent.HookBead != "" {
					agent.HasWork = true
					// Get hook title from preloaded map
					if pinnedIssue, ok := allHookBeads[agent.HookBead]; ok {
						agent.WorkTitle = pinnedIssue.Title
					}
				}
				// Parse description fields for notification level
				if fields := beads.ParseAgentFields(issue.Description); fields != nil {
					agent.NotificationLevel = fields.NotificationLevel
				}
			}

			// Get mail info (skip if --fast)
			if !skipMail {
				populateMailInfo(&agent, mailRouter)
			}

			agents[idx] = agent
		}(i, def)
	}

	wg.Wait()
	return agents
}

// populateMailInfo fetches unread mail count and first subject for an agent
func populateMailInfo(agent *AgentRuntime, router *mail.Router) {
	if router == nil {
		return
	}
	mailbox, err := router.GetMailbox(agent.Address)
	if err != nil {
		return
	}
	_, unread, _ := mailbox.Count()
	agent.UnreadMail = unread
	if unread > 0 {
		if messages, err := mailbox.ListUnread(); err == nil && len(messages) > 0 {
			agent.FirstSubject = messages[0].Subject
		}
	}
}

// detectCurrentDNDStatus returns DND status for the currently resolved role context.
// Returns nil when role context cannot be determined (e.g. outside agent context).
func detectCurrentDNDStatus(townRoot string) *DNDInfo {
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}

	roleInfo, err := GetRoleWithContext(cwd, townRoot)
	if err != nil {
		return nil
	}

	ctx := RoleContext{
		Role:     roleInfo.Role,
		Rig:      roleInfo.Rig,
		Polecat:  roleInfo.Polecat,
		TownRoot: townRoot,
		WorkDir:  cwd,
	}
	agentBeadID := getAgentBeadID(ctx)
	if agentBeadID == "" {
		return nil
	}

	bd := beads.New(townRoot)
	level, err := bd.GetAgentNotificationLevel(agentBeadID)
	if err != nil || level == "" {
		level = beads.NotifyNormal
	}

	return &DNDInfo{
		Enabled: level == beads.NotifyMuted,
		Level:   level,
		Agent:   agentBeadID,
	}
}

// agentDef defines an agent to discover
type agentDef struct {
	name    string
	address string
	session string
	role    string
	beadID  string
}

// discoverRigAgents checks runtime state for all agents in a rig.
// Uses parallel fetching for performance. If skipMail is true, mail lookups are skipped.
// allSessions is a preloaded map of tmux sessions for O(1) lookup.
// allAgentBeads is a preloaded map of agent beads for O(1) lookup.
// allHookBeads is a preloaded map of hook beads for O(1) lookup.
func discoverRigAgents(allSessions map[string]bool, r *rig.Rig, crews []string, allAgentBeads map[string]*beads.Issue, allHookBeads map[string]*beads.Issue, mailRouter *mail.Router, skipMail bool) []AgentRuntime {
	// Build list of all agents to discover
	var defs []agentDef
	townRoot := filepath.Dir(r.Path)
	prefix := beads.GetPrefixForRig(townRoot, r.Name)

	// Witness
	if r.HasWitness {
		defs = append(defs, agentDef{
			name:    constants.RoleWitness,
			address: r.Name + "/witness",
			session: witnessSessionName(r.Name),
			role:    constants.RoleWitness,
			beadID:  beads.WitnessBeadIDWithPrefix(prefix, r.Name),
		})
	}

	// Refinery
	if r.HasRefinery {
		defs = append(defs, agentDef{
			name:    constants.RoleRefinery,
			address: r.Name + "/refinery",
			session: session.RefinerySessionName(session.PrefixFor(r.Name)),
			role:    constants.RoleRefinery,
			beadID:  beads.RefineryBeadIDWithPrefix(prefix, r.Name),
		})
	}

	// Polecats
	for _, name := range r.Polecats {
		defs = append(defs, agentDef{
			name:    name,
			address: r.Name + "/" + name,
			session: session.PolecatSessionName(session.PrefixFor(r.Name), name),
			role:    constants.RolePolecat,
			beadID:  beads.PolecatBeadIDWithPrefix(prefix, r.Name, name),
		})
	}

	// Crew
	for _, name := range crews {
		defs = append(defs, agentDef{
			name:    name,
			address: r.Name + "/crew/" + name,
			session: crewSessionName(r.Name, name),
			role:    constants.RoleCrew,
			beadID:  beads.CrewBeadIDWithPrefix(prefix, r.Name, name),
		})
	}

	if len(defs) == 0 {
		return nil
	}

	// Fetch all agents in parallel
	agents := make([]AgentRuntime, len(defs))
	var wg sync.WaitGroup

	for i, def := range defs {
		wg.Add(1)
		go func(idx int, d agentDef) {
			defer wg.Done()

			agent := AgentRuntime{
				Name:    d.name,
				Address: d.address,
				Session: d.session,
				Role:    d.role,
			}

			// Check tmux session from preloaded map (O(1))
			agent.Running = allSessions[d.session]

			// Look up agent bead from preloaded map (O(1))
			if issue, ok := allAgentBeads[d.beadID]; ok {
				// Prefer database columns over description parsing
				// HookBead column is authoritative (cleared by unsling)
				agent.HookBead = issue.HookBead
				agent.State = beads.ResolveAgentState(issue.Description, issue.AgentState)
				if agent.HookBead != "" {
					agent.HasWork = true
					// Get hook title from preloaded map
					if pinnedIssue, ok := allHookBeads[agent.HookBead]; ok {
						agent.WorkTitle = pinnedIssue.Title
					}
				}
				// Parse description fields for notification level
				if fields := beads.ParseAgentFields(issue.Description); fields != nil {
					agent.NotificationLevel = fields.NotificationLevel
				}
			}

			// Get mail info (skip if --fast)
			if !skipMail {
				populateMailInfo(&agent, mailRouter)
			}

			agents[idx] = agent
		}(i, def)
	}

	wg.Wait()
	return agents
}

// getMQSummary queries beads for merge-request issues and returns a summary.
// Uses a single bd call to fetch all non-closed merge-requests, then splits
// open vs in_progress in memory. Previously used two separate bd calls.
// Returns nil if the rig has no refinery or no MQ issues.
func getMQSummary(r *rig.Rig) *MQSummary {
	if !r.HasRefinery {
		return nil
	}

	// Create beads instance for the rig
	b := beads.New(r.BeadsPath())

	// Single query for all non-closed merge-request issues.
	// Status "all" fetches everything; we filter open/in_progress in memory.
	opts := beads.ListOptions{
		Label:    "gt:merge-request",
		Status:   "all",
		Priority: -1, // No priority filter
	}
	allMRs, err := b.List(opts)
	if err != nil {
		return nil
	}

	// Split by status in memory
	pending := 0
	blocked := 0
	inProgress := 0
	for _, mr := range allMRs {
		switch mr.Status {
		case "open":
			if len(mr.BlockedBy) > 0 || mr.BlockedByCount > 0 {
				blocked++
			} else {
				pending++
			}
		case "in_progress":
			inProgress++
		}
		// closed/other statuses are ignored
	}

	// Determine queue state
	state := "idle"
	if inProgress > 0 {
		state = "processing"
	} else if pending > 0 {
		state = "idle" // Has work but not processing yet
	} else if blocked > 0 {
		state = "blocked" // Only blocked items, nothing processable
	}

	// Determine queue health
	health := "empty"
	total := pending + inProgress + blocked
	if total > 0 {
		health = "healthy"
		// Check for potential issues
		if pending > 10 && inProgress == 0 {
			// Large queue but nothing processing - may be stuck
			health = "stale"
		}
	}

	// Only return summary if there's something to show
	if pending == 0 && inProgress == 0 && blocked == 0 {
		return nil
	}

	return &MQSummary{
		Pending:  pending,
		InFlight: inProgress,
		Blocked:  blocked,
		State:    state,
		Health:   health,
	}
}

// getAgentHook retrieves hook status for a specific agent.
func getAgentHook(b *beads.Beads, role, agentAddress, roleType string) AgentHookInfo {
	hook := AgentHookInfo{
		Agent: agentAddress,
		Role:  roleType,
	}

	// Find handoff bead for this role
	handoff, err := b.FindHandoffBead(role)
	if err != nil || handoff == nil {
		return hook
	}

	// Check for attachment
	attachment := beads.ParseAttachmentFields(handoff)
	if attachment != nil && attachment.AttachedMolecule != "" {
		hook.HasWork = true
		hook.Molecule = attachment.AttachedMolecule
		hook.Title = handoff.Title
	} else if handoff.Description != "" {
		// Has content but no molecule - still has work
		hook.HasWork = true
		hook.Title = handoff.Title
	}

	return hook
}
