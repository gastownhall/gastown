package convoy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"sync"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/util"
)

// convoyIDPattern validates convoy IDs.
var convoyIDPattern = regexp.MustCompile(`^hq-[a-zA-Z0-9-]+$`)

// IssueItem represents a tracked issue within a convoy.
type IssueItem struct {
	ID     string
	Title  string
	Status string
}

// ConvoyItem represents a convoy with its tracked issues.
type ConvoyItem struct {
	ID       string
	Title    string
	Status   string
	Issues   []IssueItem
	Progress string // e.g., "2/5"
	Expanded bool
}

// Model is the bubbletea model for the convoy TUI.
type Model struct {
	convoys   []ConvoyItem
	cursor    int    // Current selection index in flattened view
	townBeads string // Path to town beads directory
	err       error

	// UI state
	keys     KeyMap
	help     help.Model
	showHelp bool
	width    int
	height   int

	// mu protects all fields read by View() from concurrent access:
	// convoys, cursor, err, showHelp, help, width, height.
	// Write lock is held during Update mutations; read lock during View/render.
	mu sync.RWMutex
}

// New creates a new convoy TUI model.
func New(townBeads string) *Model {
	return &Model{
		townBeads: townBeads,
		keys:      DefaultKeyMap(),
		help:      help.New(),
		convoys:   make([]ConvoyItem, 0),
	}
}

// Init initializes the model.
func (m *Model) Init() tea.Cmd {
	return m.fetchConvoys
}

// fetchConvoysMsg is the result of fetching convoys.
type fetchConvoysMsg struct {
	convoys []ConvoyItem
	err     error
}

// fetchConvoys fetches convoy data from beads.
func (m *Model) fetchConvoys() tea.Msg {
	convoys, err := loadConvoys(m.townBeads)
	return fetchConvoysMsg{convoys: convoys, err: err}
}

// loadConvoys loads convoy data from the beads directory.
func loadConvoys(townBeads string) ([]ConvoyItem, error) {
	ctx, cancel := context.WithTimeout(context.Background(), constants.BdSubprocessTimeout)
	defer cancel()

	// Get list of open convoys using label-based filtering; local filter below also
	// catches legacy type=convoy beads that predate the gt:convoy label.
	listArgs := []string{"list", "--label=gt:convoy", "--json", "--limit=0"}
	listCmd := exec.CommandContext(ctx, "bd", listArgs...)
	util.SetDetachedProcessGroup(listCmd)
	listCmd.Dir = townBeads
	var stdout bytes.Buffer
	listCmd.Stdout = &stdout

	if err := listCmd.Run(); err != nil {
		return nil, fmt.Errorf("listing convoys: %w", err)
	}

	var rawConvoys []struct {
		ID        string   `json:"id"`
		Title     string   `json:"title"`
		Status    string   `json:"status"`
		IssueType string   `json:"issue_type"`
		Labels    []string `json:"labels"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &rawConvoys); err != nil {
		return nil, fmt.Errorf("parsing convoy list: %w", err)
	}

	convoys := make([]ConvoyItem, 0, len(rawConvoys))
	for _, rc := range rawConvoys {
		if rc.IssueType != "convoy" && !tuiConvoyHasLabel(rc.Labels, "gt:convoy") {
			continue
		}
		issues, completed, total := loadTrackedIssues(townBeads, rc.ID)
		convoys = append(convoys, ConvoyItem{
			ID:       rc.ID,
			Title:    rc.Title,
			Status:   rc.Status,
			Issues:   issues,
			Progress: fmt.Sprintf("%d/%d", completed, total),
			Expanded: false,
		})
	}

	return convoys, nil
}

func tuiConvoyHasLabel(labels []string, target string) bool {
	for _, label := range labels {
		if label == target {
			return true
		}
	}
	return false
}

// loadTrackedIssues loads issues tracked by a convoy.
func loadTrackedIssues(townBeads, convoyID string) ([]IssueItem, int, int) {
	// Validate convoy ID for safety
	if !convoyIDPattern.MatchString(convoyID) {
		return nil, 0, 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), constants.BdSubprocessTimeout)
	defer cancel()

	// Query tracked issues using bd dep list (returns full issue details)
	cmd := exec.CommandContext(ctx, "bd", "dep", "list", convoyID, "-t", "tracks", "--json")
	util.SetDetachedProcessGroup(cmd)
	cmd.Dir = townBeads
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return nil, 0, 0
	}

	var tracked []struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &tracked); err != nil {
		return nil, 0, 0
	}

	// Extract raw issue IDs and refresh status via cross-rig lookup.
	// bd dep list returns status from the dependency record in HQ beads
	// which is never updated when cross-rig issues are closed in their rig.
	for i := range tracked {
		tracked[i].ID = beads.ExtractIssueID(tracked[i].ID)
	}
	freshStatus := refreshIssueStatus(ctx, tracked)

	issues := make([]IssueItem, 0, len(tracked))
	completed := 0
	for _, t := range tracked {
		status := t.Status
		if fresh, ok := freshStatus[t.ID]; ok {
			status = fresh
		}
		issues = append(issues, IssueItem{
			ID:     t.ID,
			Title:  t.Title,
			Status: status,
		})
		if status == "closed" {
			completed++
		}
	}

	// Sort by status (open first, then closed)
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Status == issues[j].Status {
			return issues[i].ID < issues[j].ID
		}
		return issues[i].Status != "closed" // open comes first
	})

	return issues, completed, len(issues)
}

// refreshIssueStatus does a batch bd show to get current status for tracked issues.
// Returns a map from issue ID to current status.
func refreshIssueStatus(ctx context.Context, tracked []struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}) map[string]string {
	if len(tracked) == 0 {
		return nil
	}

	args := []string{"show"}
	for _, t := range tracked {
		args = append(args, t.ID)
	}
	args = append(args, "--json")

	cmd := exec.CommandContext(ctx, "bd", args...)
	util.SetDetachedProcessGroup(cmd)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return nil
	}

	var issues []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &issues); err != nil {
		return nil
	}

	result := make(map[string]string, len(issues))
	for _, issue := range issues {
		result[issue.ID] = issue.Status
	}
	return result
}

// Update handles messages.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.mu.Lock()
		m.width = msg.Width
		m.height = msg.Height
		m.help.Width = msg.Width
		m.mu.Unlock()
		return m, nil

	case fetchConvoysMsg:
		m.mu.Lock()
		m.err = msg.err
		m.convoys = msg.convoys
		m.mu.Unlock()
		return m, nil

	case tea.KeyMsg:
		switch {
		case key.Matches(msg, m.keys.Quit):
			return m, tea.Quit

		case key.Matches(msg, m.keys.Help):
			m.mu.Lock()
			m.showHelp = !m.showHelp
			m.mu.Unlock()
			return m, nil

		case key.Matches(msg, m.keys.Up):
			m.mu.Lock()
			if m.cursor > 0 {
				m.cursor--
			}
			m.mu.Unlock()
			return m, nil

		case key.Matches(msg, m.keys.Down):
			m.mu.Lock()
			max := m.maxCursorLocked()
			if m.cursor < max {
				m.cursor++
			}
			m.mu.Unlock()
			return m, nil

		case key.Matches(msg, m.keys.Top):
			m.mu.Lock()
			m.cursor = 0
			m.mu.Unlock()
			return m, nil

		case key.Matches(msg, m.keys.Bottom):
			m.mu.Lock()
			m.cursor = m.maxCursorLocked()
			m.mu.Unlock()
			return m, nil

		case key.Matches(msg, m.keys.Toggle):
			m.mu.Lock()
			m.toggleExpandLocked()
			m.mu.Unlock()
			return m, nil

		// Number keys for direct convoy access
		case msg.String() >= "1" && msg.String() <= "9":
			n := int(msg.String()[0] - '0')
			m.mu.Lock()
			if n <= len(m.convoys) {
				m.jumpToConvoyLocked(n - 1)
			}
			m.mu.Unlock()
			return m, nil
		}
	}

	return m, nil
}

// maxCursorLocked returns the maximum valid cursor position.
// Caller must hold m.mu (read or write).
func (m *Model) maxCursorLocked() int {
	count := 0
	for _, c := range m.convoys {
		count++ // convoy itself
		if c.Expanded {
			count += len(c.Issues)
		}
	}
	if count == 0 {
		return 0
	}
	return count - 1
}

// cursorToConvoyIndexLocked returns the convoy index and issue index for the current cursor.
// Returns (convoyIdx, issueIdx) where issueIdx is -1 if on a convoy row.
// Caller must hold m.mu (read or write).
func (m *Model) cursorToConvoyIndexLocked() (int, int) {
	pos := 0
	for ci, c := range m.convoys {
		if pos == m.cursor {
			return ci, -1
		}
		pos++
		if c.Expanded {
			for ii := range c.Issues {
				if pos == m.cursor {
					return ci, ii
				}
				pos++
			}
		}
	}
	return -1, -1
}

// toggleExpandLocked toggles expansion of the convoy at the current cursor.
// Caller must hold m.mu write lock.
func (m *Model) toggleExpandLocked() {
	ci, ii := m.cursorToConvoyIndexLocked()
	if ci >= 0 && ii == -1 {
		// On a convoy row, toggle it
		m.convoys[ci].Expanded = !m.convoys[ci].Expanded
	}
}

// jumpToConvoyLocked moves the cursor to a specific convoy by index.
// Caller must hold m.mu write lock.
func (m *Model) jumpToConvoyLocked(convoyIdx int) {
	if convoyIdx < 0 || convoyIdx >= len(m.convoys) {
		return
	}
	pos := 0
	for ci, c := range m.convoys {
		if ci == convoyIdx {
			m.cursor = pos
			return
		}
		pos++
		if c.Expanded {
			pos += len(c.Issues)
		}
	}
}

// View renders the model.
// Acquires read lock to safely access all View-visible fields.
func (m *Model) View() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.renderView()
}
