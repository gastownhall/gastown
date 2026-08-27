package doctor

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"
)

// WispGCCheck detects and cleans orphaned wisps that are older than a threshold.
// Wisps are ephemeral issues (Wisp: true flag) used for patrol cycles and
// operational workflows that shouldn't accumulate.
type WispGCCheck struct {
	FixableCheck
	threshold     time.Duration
	abandonedRigs map[string]int // rig -> count of abandoned wisps
}

// NewWispGCCheck creates a new wisp GC check with 1 hour threshold.
func NewWispGCCheck() *WispGCCheck {
	return &WispGCCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "wisp-gc",
				CheckDescription: "Detect and clean orphaned wisps (>1h old)",
				CheckCategory:    CategoryCleanup,
			},
		},
		threshold:     1 * time.Hour,
		abandonedRigs: make(map[string]int),
	}
}

// Run checks for abandoned wisps in each rig.
func (c *WispGCCheck) Run(ctx *CheckContext) *CheckResult {
	c.abandonedRigs = make(map[string]int)

	rigs, err := discoverRigs(ctx.TownRoot)
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "Failed to discover rigs",
			Details: []string{err.Error()},
		}
	}

	if len(rigs) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No rigs configured",
		}
	}

	var details []string
	totalAbandoned := 0

	for _, rigName := range rigs {
		rigPath := filepath.Join(ctx.TownRoot, rigName)
		count := c.countAbandonedWisps(rigPath)
		if count > 0 {
			c.abandonedRigs[rigName] = count
			totalAbandoned += count
			details = append(details, fmt.Sprintf("%s: %d abandoned wisp(s)", rigName, count))
		}
	}

	if totalAbandoned > 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("%d abandoned wisp(s) found (>1h old)", totalAbandoned),
			Details: details,
			FixHint: "Run 'gt doctor --fix' to garbage collect orphaned wisps",
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: "No abandoned wisps found",
	}
}

// wispListEntry is one wisp as reported by `bd mol wisp list --json`.
type wispListEntry struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	UpdatedAt string `json:"updated_at"`
}

// wispList is the envelope `bd mol wisp list --json` prints. The wisps live
// under a "wisps" key — decoding the payload as a bare array silently yields
// zero wisps (hq-675).
type wispList struct {
	Wisps []wispListEntry `json:"wisps"`
}

// countAbandonedWisps counts wisps older than the threshold in a rig.
// Queries the wisps table via bd mol wisp list (Dolt server is required).
func (c *WispGCCheck) countAbandonedWisps(rigPath string) int {
	// Query wisps table via bd CLI
	cmd := exec.Command("bd", "mol", "wisp", "list", "--json")
	cmd.Dir = rigPath

	output, err := cmd.Output()
	if err != nil {
		// Dolt is the only supported backend — no wisps table means 0 abandoned wisps.
		return 0
	}

	// Use UTC for cutoff: Dolt stores timestamps in UTC (gt-ty4).
	return countAbandonedWisps(output, time.Now().UTC().Add(-c.threshold))
}

// countAbandonedWisps counts the wisps in a `bd mol wisp list --json` payload
// that have not been touched since cutoff and are not live work.
//
// A HOOKED wisp is an agent's active molecule, never abandoned — a patrol whose
// steps take longer than the threshold is still running (hq-675). Counting it
// here would report live work as garbage and invite someone to collect it.
func countAbandonedWisps(payload []byte, cutoff time.Time) int {
	var list wispList
	if err := json.Unmarshal(payload, &list); err != nil {
		return 0
	}

	count := 0
	for _, w := range list.Wisps {
		if w.Status == "closed" || w.Status == "hooked" {
			continue
		}
		updatedAt, err := time.Parse(time.RFC3339, w.UpdatedAt)
		if err != nil {
			continue
		}
		if !updatedAt.IsZero() && updatedAt.Before(cutoff) {
			count++
		}
	}

	return count
}

// Fix runs bd mol wisp gc in each rig with abandoned wisps.
func (c *WispGCCheck) Fix(ctx *CheckContext) error {
	var lastErr error

	for rigName := range c.abandonedRigs {
		rigPath := filepath.Join(ctx.TownRoot, rigName)

		// Preview only — deliberately no --force (hq-675).
		//
		// `bd mol wisp gc` defaults to age-based collection, which has no
		// ownership scope and does not exclude HOOKED wisps. Forcing it here
		// would delete every agent's live molecule across every rig in town.
		// Report the candidates and leave deletion to the reaper.
		cmd := exec.Command("bd", "mol", "wisp", "gc")
		cmd.Dir = rigPath
		if output, err := cmd.CombinedOutput(); err != nil {
			lastErr = fmt.Errorf("%s: %v (%s)", rigName, err, string(output))
		}
	}

	return lastErr
}
