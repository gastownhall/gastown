package feed

import (
	"os/exec"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/constants"
	"github.com/jonbaldie/gastown/internal/tmux"
	"github.com/jonbaldie/gastown/internal/util"
)

// Panel represents which panel has focus
type Panel int

const (
	PanelTree Panel = iota
	PanelConvoy
	PanelFeed
	PanelProblems // Problems panel in problems view
)

// ViewMode represents which view is active
type ViewMode int

const (
	ViewActivity ViewMode = iota // Default activity stream view
	ViewProblems                 // Problem-first view
)

// Layout constants for panel height distribution and event history.
const (
	treePanelPercent   = 30
	convoyPanelPercent = 25
	maxEventHistory    = 1000
)

// Event represents an activity event
type Event struct {
	Time    time.Time
	Type    string // create, update, complete, fail, delete
	Actor   string // who did it (e.g., "gastown/crew/joe")
	Target  string // what was affected (e.g., "gt-xyz")
	Message string // human-readable description
	Rig     string // which rig
	Role    string // actor's role
	Raw     string // raw line for fallback display
}

// Agent represents an agent in the tree
type Agent struct {
	ID         string
	Name       string
	Role       string // mayor, witness, refinery, crew, polecat
	Rig        string
	Status     string // running, idle, working, dead
	LastEvent  *Event
	LastUpdate time.Time
	Expanded   bool
}

// Rig represents a rig with its agents
type Rig struct {
	Name     string
	Agents   map[string]*Agent // keyed by role/name
	Expanded bool
}

// Model is the main bubbletea model for the feed TUI
type Model struct {
	// Dimensions
	width  int
	height int

	// Panels
	focusedPanel   Panel
	treeViewport   viewport.Model
	convoyViewport viewport.Model
	feedViewport   viewport.Model

	// Data
	rigs        map[string]*Rig
	events      []Event
	convoyState *ConvoyState
	townRoot    string

	// UI state
	keys     KeyMap
	help     help.Model
	showHelp bool
	filter   string

	// View mode
	viewMode ViewMode

	// Problems view state
	problemAgents     []*ProblemAgent
	selectedProblem   int
	selectedBeadID    string // stable selection tracking by bead ID
	problemsViewport  viewport.Model
	stuckDetector     *StuckDetector
	lastProblemsCheck time.Time
	problemsError     error // last error from problems fetch

	// Event source
	eventChan <-chan Event
	done      chan struct{}
	closeOnce sync.Once

	// mu protects all fields read by View() from concurrent access:
	// events, rigs, convoyState, eventChan, townRoot, width, height,
	// focusedPanel, showHelp, help, filter, viewMode, problemAgents,
	// selectedProblem, selectedBeadID, problemsError, lastProblemsCheck,
	// and all viewports. Write lock is held during Update/handleKey
	// mutations; read lock is held during View/render.
	mu sync.RWMutex
}

// NewModel creates a new feed TUI model.
// The bd parameter provides access to agent beads for health detection.
func NewModel(bd *beads.Beads) *Model {
	h := help.New()
	h.ShowAll = false

	return &Model{
		focusedPanel:     PanelTree,
		treeViewport:     viewport.New(0, 0),
		convoyViewport:   viewport.New(0, 0),
		feedViewport:     viewport.New(0, 0),
		problemsViewport: viewport.New(0, 0),
		rigs:             make(map[string]*Rig),
		events:           make([]Event, 0, maxEventHistory),
		problemAgents:    make([]*ProblemAgent, 0),
		keys:             DefaultKeyMap(),
		help:             h,
		done:             make(chan struct{}),
		viewMode:         ViewActivity,
		stuckDetector:    NewStuckDetector(bd),
	}
}

// NewModelWithProblemsView creates a new feed TUI model starting in problems view.
// The bd parameter provides access to agent beads for health detection.
func NewModelWithProblemsView(bd *beads.Beads) *Model {
	m := NewModel(bd)
	m.viewMode = ViewProblems
	m.focusedPanel = PanelProblems
	return m
}

// SetTownRoot sets the town root for convoy fetching.
// Safe to call concurrently with the Bubble Tea event loop.
func (m *Model) SetTownRoot(townRoot string) {
	m.mu.Lock()
	m.townRoot = townRoot
	m.mu.Unlock()
}

// Init initializes the model
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		m.listenForEvents(),
		m.fetchConvoys(),
		tea.SetWindowTitle("GT Feed"),
	}
	// If starting in problems view, fetch problems immediately
	if m.viewMode == ViewProblems {
		cmds = append(cmds, m.fetchProblems())
	}
	return tea.Batch(cmds...)
}

// eventMsg is sent when a new event arrives
type eventMsg Event

// convoyUpdateMsg is sent when convoy data is refreshed
type convoyUpdateMsg struct {
	state *ConvoyState
}

// problemsUpdateMsg is sent when problems data is refreshed
type problemsUpdateMsg struct {
	agents  []*ProblemAgent
	fetched bool // true when data was fetched (even if agents is empty/nil)
	err     error
}

// problemsTickMsg is sent to trigger the next problems refresh
type problemsTickMsg struct{}

// tickMsg is sent periodically to refresh the view
type tickMsg time.Time

// listenForEvents returns a command that listens for events.
// Captures channels under the read lock to avoid racing with SetEventChannel.
func (m *Model) listenForEvents() tea.Cmd {
	m.mu.RLock()
	eventChan := m.eventChan
	done := m.done
	m.mu.RUnlock()

	if eventChan == nil {
		return nil
	}
	return func() tea.Msg {
		select {
		case event, ok := <-eventChan:
			if !ok {
				return nil
			}
			return eventMsg(event)
		case <-done:
			return nil
		}
	}
}

// tick returns a command for periodic refresh
func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// fetchConvoys returns a command that fetches convoy data.
// Captures townRoot under the read lock to avoid racing with SetTownRoot.
func (m *Model) fetchConvoys() tea.Cmd {
	m.mu.RLock()
	townRoot := m.townRoot
	m.mu.RUnlock()

	if townRoot == "" {
		return nil
	}
	return func() tea.Msg {
		state, _ := FetchConvoys(townRoot)
		return convoyUpdateMsg{state: state}
	}
}

// convoyRefreshTick returns a command that schedules the next convoy refresh
func (m *Model) convoyRefreshTick() tea.Cmd {
	return tea.Tick(10*time.Second, func(t time.Time) tea.Msg {
		return convoyUpdateMsg{} // Empty state triggers a refresh
	})
}

// fetchProblems returns a command that fetches problem agent data
func (m *Model) fetchProblems() tea.Cmd {
	detector := m.stuckDetector
	return func() tea.Msg {
		agents, err := detector.CheckAll()
		if err != nil {
			return problemsUpdateMsg{fetched: true, err: err}
		}
		return problemsUpdateMsg{agents: agents, fetched: true}
	}
}

// problemsRefreshTick returns a command that schedules the next problems refresh
func (m *Model) problemsRefreshTick() tea.Cmd {
	return tea.Tick(5*time.Second, func(t time.Time) tea.Msg {
		return problemsTickMsg{}
	})
}

// Update handles messages
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(msg)

	case tea.WindowSizeMsg:
		m.mu.Lock()
		m.width = msg.Width
		m.height = msg.Height
		m.mu.Unlock()
		m.updateViewportSizes()

	case eventMsg:
		m.addEvent(Event(msg))
		cmds = append(cmds, m.listenForEvents())

	case convoyUpdateMsg:
		if msg.state != nil {
			// Fresh data arrived - update state and schedule next tick
			m.mu.Lock()
			m.convoyState = msg.state
			m.updateViewContentLocked()
			m.mu.Unlock()
			cmds = append(cmds, m.convoyRefreshTick())
		} else {
			// Tick fired - fetch new data
			cmds = append(cmds, m.fetchConvoys())
		}

	case problemsUpdateMsg:
		if msg.err != nil {
			// Error fetching problems - record error, schedule delayed retry
			m.mu.Lock()
			m.problemsError = msg.err
			m.updateViewContentLocked()
			scheduleNext := m.viewMode == ViewProblems
			m.mu.Unlock()
			if scheduleNext {
				cmds = append(cmds, m.problemsRefreshTick())
			}
		} else if msg.fetched {
			// Fresh data arrived - update state and schedule next tick
			m.mu.Lock()
			m.problemAgents = msg.agents
			m.problemsError = nil
			m.lastProblemsCheck = time.Now()
			// Restore selection by bead ID for stability across refreshes
			m.restoreSelectionByBeadID()
			m.updateViewContentLocked()
			scheduleNext := m.viewMode == ViewProblems
			m.mu.Unlock()
			if scheduleNext {
				cmds = append(cmds, m.problemsRefreshTick())
			}
		}

	case problemsTickMsg:
		// Timer tick - fetch new data if in problems view
		m.mu.RLock()
		inProblems := m.viewMode == ViewProblems
		m.mu.RUnlock()
		if inProblems {
			cmds = append(cmds, m.fetchProblems())
		}

	case tickMsg:
		cmds = append(cmds, tick())
	}

	// Update viewports (under lock to protect from concurrent View)
	m.mu.Lock()
	var cmd tea.Cmd
	switch m.focusedPanel {
	case PanelTree:
		m.treeViewport, cmd = m.treeViewport.Update(msg)
	case PanelConvoy:
		m.convoyViewport, cmd = m.convoyViewport.Update(msg)
	case PanelFeed:
		m.feedViewport, cmd = m.feedViewport.Update(msg)
	case PanelProblems:
		m.problemsViewport, cmd = m.problemsViewport.Update(msg)
	}
	m.mu.Unlock()
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

// handleKey processes key presses
func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Quit):
		m.closeOnce.Do(func() { close(m.done) })
		return m, tea.Quit

	case key.Matches(msg, m.keys.Help):
		m.mu.Lock()
		m.showHelp = !m.showHelp
		m.help.ShowAll = m.showHelp
		m.mu.Unlock()
		return m, nil

	case key.Matches(msg, m.keys.ToggleProblems):
		return m.toggleProblemsView()

	case key.Matches(msg, m.keys.Tab):
		return m.handleTabKey()

	case key.Matches(msg, m.keys.FocusTree):
		if m.viewMode == ViewActivity {
			m.mu.Lock()
			m.focusedPanel = PanelTree
			m.mu.Unlock()
		}
		return m, nil

	case key.Matches(msg, m.keys.FocusFeed):
		if m.viewMode == ViewActivity {
			m.mu.Lock()
			m.focusedPanel = PanelFeed
			m.mu.Unlock()
		}
		return m, nil

	case key.Matches(msg, m.keys.FocusConvoy):
		if m.viewMode == ViewActivity {
			m.mu.Lock()
			m.focusedPanel = PanelConvoy
			m.mu.Unlock()
		}
		return m, nil

	case key.Matches(msg, m.keys.Refresh):
		m.updateViewContent()
		if m.viewMode == ViewProblems {
			return m, m.fetchProblems()
		}
		return m, nil

	case key.Matches(msg, m.keys.Enter):
		if m.viewMode == ViewProblems {
			return m.attachToSelected()
		}

	case key.Matches(msg, m.keys.Nudge):
		if m.viewMode == ViewProblems {
			return m.nudgeSelected()
		}

	case key.Matches(msg, m.keys.Handoff):
		if m.viewMode == ViewProblems {
			return m.handoffSelected()
		}

	case key.Matches(msg, m.keys.Up):
		if m.viewMode == ViewProblems {
			return m.selectPrevProblem()
		}

	case key.Matches(msg, m.keys.Down):
		if m.viewMode == ViewProblems {
			return m.selectNextProblem()
		}
	}

	// Pass to focused viewport (under lock to protect from concurrent View)
	m.mu.Lock()
	var cmd tea.Cmd
	switch m.focusedPanel {
	case PanelTree:
		m.treeViewport, cmd = m.treeViewport.Update(msg)
	case PanelConvoy:
		m.convoyViewport, cmd = m.convoyViewport.Update(msg)
	case PanelFeed:
		m.feedViewport, cmd = m.feedViewport.Update(msg)
	case PanelProblems:
		m.problemsViewport, cmd = m.problemsViewport.Update(msg)
	}
	m.mu.Unlock()
	return m, cmd
}

// toggleProblemsView switches between activity and problems view
func (m *Model) toggleProblemsView() (tea.Model, tea.Cmd) {
	m.mu.Lock()
	if m.viewMode == ViewProblems {
		m.viewMode = ViewActivity
		m.focusedPanel = PanelTree
		m.mu.Unlock()
		m.updateViewportSizes()
		return m, nil
	}
	m.viewMode = ViewProblems
	m.focusedPanel = PanelProblems
	lastCheck := m.lastProblemsCheck
	m.mu.Unlock()
	m.updateViewportSizes()
	// Fetch problems if we haven't recently
	if time.Since(lastCheck) > 5*time.Second {
		return m, m.fetchProblems()
	}
	return m, nil
}

// handleTabKey handles Tab key for panel/problem cycling
func (m *Model) handleTabKey() (tea.Model, tea.Cmd) {
	if m.viewMode == ViewProblems {
		// In problems view, Tab cycles through problem agents
		return m.selectNextProblem()
	}
	// In activity view, Tab cycles panels
	m.mu.Lock()
	switch m.focusedPanel {
	case PanelTree:
		m.focusedPanel = PanelConvoy
	case PanelConvoy:
		m.focusedPanel = PanelFeed
	case PanelFeed:
		m.focusedPanel = PanelTree
	}
	m.mu.Unlock()
	return m, nil
}

// restoreSelectionByBeadID finds the previously-selected agent by bead ID
// after a data refresh and updates the index. Falls back to clamping if not found.
func (m *Model) restoreSelectionByBeadID() {
	if m.selectedBeadID != "" {
		idx := 0
		for _, agent := range m.problemAgents {
			if agent.State.NeedsAttention() {
				if agent.CurrentBeadID == m.selectedBeadID {
					m.selectedProblem = idx
					return
				}
				idx++
			}
		}
	}
	// Not found or no previous selection - clamp to bounds
	problemCount := 0
	for _, agent := range m.problemAgents {
		if agent.State.NeedsAttention() {
			problemCount++
		}
	}
	if m.selectedProblem >= problemCount {
		m.selectedProblem = problemCount - 1
	}
	if m.selectedProblem < 0 {
		m.selectedProblem = 0
	}
	// Update tracked bead ID
	if selected := m.getSelectedProblemAgent(); selected != nil {
		m.selectedBeadID = selected.CurrentBeadID
	}
}

// selectNextProblem moves selection to next problem agent
func (m *Model) selectNextProblem() (tea.Model, tea.Cmd) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.problemAgents) == 0 {
		return m, nil
	}
	problemCount := 0
	for _, agent := range m.problemAgents {
		if agent.State.NeedsAttention() {
			problemCount++
		}
	}
	if problemCount == 0 {
		return m, nil
	}
	m.selectedProblem++
	if m.selectedProblem >= problemCount {
		m.selectedProblem = 0
	}
	if selected := m.getSelectedProblemAgent(); selected != nil {
		m.selectedBeadID = selected.CurrentBeadID
	}
	m.updateViewContentLocked()
	return m, nil
}

// selectPrevProblem moves selection to previous problem agent
func (m *Model) selectPrevProblem() (tea.Model, tea.Cmd) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.problemAgents) == 0 {
		return m, nil
	}
	problemCount := 0
	for _, agent := range m.problemAgents {
		if agent.State.NeedsAttention() {
			problemCount++
		}
	}
	if problemCount == 0 {
		return m, nil
	}
	m.selectedProblem--
	if m.selectedProblem < 0 {
		m.selectedProblem = problemCount - 1
	}
	if selected := m.getSelectedProblemAgent(); selected != nil {
		m.selectedBeadID = selected.CurrentBeadID
	}
	m.updateViewContentLocked()
	return m, nil
}

// getSelectedProblemAgent returns the currently selected problem agent
func (m *Model) getSelectedProblemAgent() *ProblemAgent {
	if m.selectedProblem < 0 || len(m.problemAgents) == 0 {
		return nil
	}
	// Find the nth problem agent
	idx := 0
	for _, agent := range m.problemAgents {
		if agent.State.NeedsAttention() {
			if idx == m.selectedProblem {
				return agent
			}
			idx++
		}
	}
	return nil
}

// attachToSelected attaches to the selected agent's tmux session
func (m *Model) attachToSelected() (tea.Model, tea.Cmd) {
	agent := m.getSelectedProblemAgent()
	if agent == nil {
		return m, nil
	}
	// Exit TUI and switch to/attach tmux session
	m.closeOnce.Do(func() { close(m.done) })
	var c *exec.Cmd
	if tmux.IsInSameSocket() {
		// Same tmux socket: switch the current client to the target session
		c = tmux.BuildCommand("switch-client", "-t", agent.SessionID)
	} else {
		// Outside tmux or different socket: attach to the session
		c = tmux.BuildCommand("attach-session", "-t", agent.SessionID)
	}
	return m, tea.Sequence(
		tea.ExitAltScreen,
		tea.ExecProcess(c, func(err error) tea.Msg {
			return tea.Quit()
		}),
	)
}

// nudgeTarget returns the proper gt nudge target for an agent.
// Uses rig/name format for polecats, rig/crew/name for crew,
// and role shortcuts for singletons (mayor, deacon, witness, refinery).
func nudgeTarget(agent *ProblemAgent) string {
	switch agent.Role {
	case constants.RoleMayor, constants.RoleDeacon:
		return agent.Role
	case constants.RoleWitness, constants.RoleRefinery:
		return agent.Rig + "/" + agent.Role
	case constants.RoleCrew:
		return agent.Rig + "/crew/" + agent.Name
	case constants.RolePolecat:
		return agent.Rig + "/" + agent.Name
	default:
		// Fallback to session ID
		return agent.SessionID
	}
}

// nudgeSelected sends a nudge to the selected agent
func (m *Model) nudgeSelected() (tea.Model, tea.Cmd) {
	agent := m.getSelectedProblemAgent()
	if agent == nil {
		return m, nil
	}
	// Run gt nudge with proper target format
	target := nudgeTarget(agent)
	c := exec.Command("gt", "nudge", target, "continue")
	util.SetDetachedProcessGroup(c)
	return m, tea.ExecProcess(c, func(err error) tea.Msg {
		// Refresh problems after nudge
		return problemsTickMsg{}
	})
}

// handoffSelected sends a handoff request to the selected agent
func (m *Model) handoffSelected() (tea.Model, tea.Cmd) {
	agent := m.getSelectedProblemAgent()
	if agent == nil {
		return m, nil
	}
	// Run gt nudge with proper target format
	target := nudgeTarget(agent)
	c := exec.Command("gt", "nudge", target, "handoff")
	util.SetDetachedProcessGroup(c)
	return m, tea.ExecProcess(c, func(err error) tea.Msg {
		return problemsTickMsg{}
	})
}

// updateViewportSizes recalculates viewport dimensions.
// Acquires the write lock for the entire operation so that reads of
// width/height/showHelp and writes to viewports are atomic with View().
func (m *Model) updateViewportSizes() {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Reserve space: header (1) + borders (6 for 3 panels) + status bar (1) + help (1-2)
	headerHeight := 1
	statusHeight := 1
	helpHeight := 1
	if m.showHelp {
		helpHeight = 3
	}
	borderHeight := 6 // top and bottom borders for 3 panels
	if m.viewMode == ViewProblems {
		borderHeight = 2 // single panel
	}

	availableHeight := m.height - headerHeight - statusHeight - helpHeight - borderHeight
	if availableHeight < 6 {
		availableHeight = 6
	}

	contentWidth := m.width - 4 // borders and padding
	if contentWidth < 20 {
		contentWidth = 20
	}

	if m.viewMode == ViewProblems {
		// Problems view: single large panel
		m.problemsViewport.Width = contentWidth
		m.problemsViewport.Height = availableHeight
	} else {
		// Activity view: split by configured percentages
		treeHeight := availableHeight * treePanelPercent / 100
		convoyHeight := availableHeight * convoyPanelPercent / 100
		feedHeight := availableHeight - treeHeight - convoyHeight

		// Ensure minimum heights
		if treeHeight < 3 {
			treeHeight = 3
		}
		if convoyHeight < 3 {
			convoyHeight = 3
		}
		if feedHeight < 3 {
			feedHeight = 3
		}

		m.treeViewport.Width = contentWidth
		m.treeViewport.Height = treeHeight
		m.convoyViewport.Width = contentWidth
		m.convoyViewport.Height = convoyHeight
		m.feedViewport.Width = contentWidth
		m.feedViewport.Height = feedHeight
	}

	m.updateViewContentLocked()
}

// updateViewContent refreshes the content of all viewports.
// Acquires the write lock to protect viewport and data access.
func (m *Model) updateViewContent() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updateViewContentLocked()
}

// updateViewContentLocked refreshes viewport content.
// Caller must hold m.mu.
func (m *Model) updateViewContentLocked() {
	if m.viewMode == ViewProblems {
		m.problemsViewport.SetContent(m.renderProblemsContent())
	} else {
		m.treeViewport.SetContent(m.renderTree())
		m.convoyViewport.SetContent(m.renderConvoys())
		m.feedViewport.SetContent(m.renderFeed())
	}
}

// addEvent adds an event and updates the agent tree.
// Acquires mu for the entire operation including view updates.
func (m *Model) addEvent(e Event) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.addEventLocked(e) {
		m.updateViewContentLocked()
	}
}

// addEventLocked performs the actual event mutation under the write lock.
// Returns true if the caller should call updateViewContent afterward.
// Caller must hold m.mu write lock.
func (m *Model) addEventLocked(e Event) bool {
	// Update agent tree first (always do this for status tracking)
	if e.Rig != "" {
		rig, ok := m.rigs[e.Rig]
		if !ok {
			rig = &Rig{
				Name:     e.Rig,
				Agents:   make(map[string]*Agent),
				Expanded: true,
			}
			m.rigs[e.Rig] = rig
		}

		if e.Actor != "" {
			agent, ok := rig.Agents[e.Actor]
			if !ok {
				agent = &Agent{
					ID:   e.Actor,
					Name: e.Actor,
					Role: e.Role,
					Rig:  e.Rig,
				}
				rig.Agents[e.Actor] = agent
			}
			agent.LastEvent = &e
			agent.LastUpdate = e.Time
		}
	}

	// Filter out events with empty bead IDs (malformed mutations)
	if e.Type == "update" && e.Target == "" {
		return false
	}

	// Filter out noisy agent session updates from the event feed.
	// Agent session molecules (like gt-gastown-crew-joe) update frequently
	// for status tracking. These updates are visible in the agent tree,
	// so we don't need to clutter the event feed with them.
	// We still show create/complete/fail/delete events for agent sessions.
	if e.Type == "update" && beads.IsAgentSessionBead(e.Target) {
		// Skip adding to event feed, but still refresh the view
		// (agent tree was updated above)
		return true
	}

	// Deduplicate rapid updates to the same bead within 2 seconds.
	// This prevents spam when multiple deps/labels are added to one issue.
	if e.Type == "update" && e.Target != "" && len(m.events) > 0 {
		lastEvent := m.events[len(m.events)-1]
		if lastEvent.Type == "update" && lastEvent.Target == e.Target {
			// Same bead updated within 2 seconds - skip duplicate
			if e.Time.Sub(lastEvent.Time) < 2*time.Second {
				return false
			}
		}
	}

	// Add to event feed
	m.events = append(m.events, e)

	// Keep max events within history limit
	if len(m.events) > maxEventHistory {
		m.events = m.events[len(m.events)-maxEventHistory:]
	}

	return true
}

// SetEventChannel sets the channel to receive events from.
// Safe to call concurrently with the Bubble Tea event loop.
func (m *Model) SetEventChannel(ch <-chan Event) {
	m.mu.Lock()
	m.eventChan = ch
	m.mu.Unlock()
}

// View renders the TUI.
// Acquires the read lock to safely access model state from the render path.
func (m *Model) View() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.render()
}
