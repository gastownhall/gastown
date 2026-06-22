package witness

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

// StalledCrewResult represents a single stalled crew session detection.
type StalledCrewResult struct {
	CrewName        string `json:"crew_name"`
	StuckCycles     int    `json:"stuck_cycles"`     // How many consecutive cycles the pane hash hasn't changed
	LastChanged     string `json:"last_changed"`     // RFC3339 timestamp of last hash change
	Action          string `json:"action"`           // "detected" (Layer 2 detection-only; auto-action is a separate bead)
	PaneSnapshot    string `json:"pane_snapshot"`    // Last ~10 lines for diagnostic context
	Error           string `json:"error,omitempty"`
}

// DetectStalledCrewResult holds aggregate results.
type DetectStalledCrewResult struct {
	Checked int                 `json:"checked"`
	Stalled []StalledCrewResult `json:"stalled,omitempty"`
	Errors  []string            `json:"errors,omitempty"`
}

// crewHashState is the persisted per-crew hash tracking record.
type crewHashState struct {
	Hash             string `json:"hash"`
	LastChangedAt    string `json:"last_changed_at"`
	UnchangedCycles  int    `json:"unchanged_cycles"`
	LastSnapshot     string `json:"last_snapshot,omitempty"`
}

const (
	// DefaultCrewStuckThreshold is the number of consecutive unchanged-pane
	// patrol cycles before emitting a CREW_STUCK signal. At the default
	// 5m patrol interval this means ~15 min of no pane activity.
	DefaultCrewStuckThreshold = 3

	// crewPaneCaptureLines is how many recent lines to hash. The volatile
	// bottom-bar of the Claude Code TUI (token counts, model labels) is
	// excluded by taking lines from the top of the capture, not the bottom.
	crewPaneCaptureLines = 40
)

// DetectStalledCrew checks live crew sessions for the stuck-at-prompt pattern
// (pane hash unchanged across N patrol cycles AND no nudge consumed). Closes
// the witness gap captured in glaicier gt-e4b3 / gt-wwoh — dave's session sat
// at the /clear prompt 30+ min without consuming nudges and required manual
// mayor intervention.
//
// Detection-only — no auto-action. The auto-unblock-via-paste-buffer is a
// SEPARATE follow-up (gt-e4b3.2 or similar) once we have detection signal
// data to tune false-positive thresholds against.
//
// Hash-state persisted at <townRoot>/.gt/crew-hashes/<crew>.json so unchanged-
// cycle counts survive daemon restarts.
func DetectStalledCrew(workDir, rigName string) *DetectStalledCrewResult {
	result := &DetectStalledCrewResult{}

	townRoot, err := workspace.Find(workDir)
	if err != nil || townRoot == "" {
		townRoot = workDir
	}

	// List crew workers — they live at <townRoot>/<rigName>/crew/
	crewDir := filepath.Join(townRoot, rigName, "crew")
	entries, err := os.ReadDir(crewDir)
	if err != nil {
		return result // No crew dir or unreadable — no-op
	}

	hashDir := filepath.Join(townRoot, ".gt", "crew-hashes")
	if err := os.MkdirAll(hashDir, 0o755); err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("create hash dir: %v", err))
		return result
	}

	t := tmux.NewTmux()
	now := time.Now().UTC().Format(time.RFC3339)

	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		crewName := entry.Name()
		sessionName := session.CrewSessionName(session.PrefixFor(rigName), crewName)
		result.Checked++

		// Only check live sessions — dead sessions are out of scope for stuck-at-prompt
		// (they need different recovery: respawn or handoff, both mayor-level decisions).
		alive, err := t.HasSession(sessionName)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("HasSession(%s): %v", sessionName, err))
			continue
		}
		if !alive {
			continue
		}

		// Capture pane content. Take the TOP N lines (skip volatile bottom bar).
		content, err := t.CapturePane(sessionName, crewPaneCaptureLines)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("CapturePane(%s): %v", sessionName, err))
			continue
		}

		// Hash the captured content. Skip the bottom 5 lines (TUI status bar,
		// token-count updates, "Update available!" notifications etc — all volatile).
		lines := strings.Split(content, "\n")
		stableLineCount := len(lines) - 5
		if stableLineCount < 5 {
			continue // Pane too short to hash meaningfully
		}
		stableContent := strings.Join(lines[:stableLineCount], "\n")
		sum := sha256.Sum256([]byte(stableContent))
		currentHash := hex.EncodeToString(sum[:])

		// Read prior state
		stateFile := filepath.Join(hashDir, crewName+".json")
		prior := readCrewHashState(stateFile)

		updated := crewHashState{
			Hash:            currentHash,
			LastChangedAt:   prior.LastChangedAt,
			UnchangedCycles: prior.UnchangedCycles,
			LastSnapshot:    content,
		}

		if prior.Hash == "" || prior.Hash != currentHash {
			// First check OR hash changed — reset counter
			updated.LastChangedAt = now
			updated.UnchangedCycles = 0
		} else {
			// Hash matches — increment unchanged counter
			updated.UnchangedCycles = prior.UnchangedCycles + 1
		}

		_ = writeCrewHashState(stateFile, updated)

		if updated.UnchangedCycles >= DefaultCrewStuckThreshold {
			snippet := content
			if len(snippet) > 600 {
				snippet = "...[truncated]\n" + snippet[len(snippet)-600:]
			}
			result.Stalled = append(result.Stalled, StalledCrewResult{
				CrewName:     crewName,
				StuckCycles:  updated.UnchangedCycles,
				LastChanged:  updated.LastChangedAt,
				Action:       "detected",
				PaneSnapshot: snippet,
			})
		}
	}

	return result
}

func readCrewHashState(path string) crewHashState {
	var s crewHashState
	b, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	_ = json.Unmarshal(b, &s)
	return s
}

func writeCrewHashState(path string, s crewHashState) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
