package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

const (
	// bdMolTimeout is the timeout for bd molecule operations.
	bdMolTimeout = 15 * time.Second

	// dogCloseMaxAttempts / dogCloseRetryDelay bound the retry on `bd close` for
	// dog wisps. A transient Dolt slowdown (the connection-churn window) can make
	// a single close fail, and without a retry the wisp stays OPEN forever — a
	// root cause of the dog wisp flood (gt-ye21). Retrying turns a transient
	// failure back into a clean close instead of a permanent orphan.
	dogCloseMaxAttempts = 3
	dogCloseRetryDelay  = 500 * time.Millisecond
)

// closeWisp runs `bd close <id>` (plus any extra args) with bounded retries so a
// transient Dolt error does not leave the wisp open. Returns the final error if
// every attempt fails.
func (dm *dogMol) closeWisp(id string, extra ...string) error {
	args := append([]string{"close", id}, extra...)
	var err error
	for attempt := 1; attempt <= dogCloseMaxAttempts; attempt++ {
		if _, err = dm.runBd(args...); err == nil {
			return nil
		}
		if attempt < dogCloseMaxAttempts {
			time.Sleep(time.Duration(attempt) * dogCloseRetryDelay)
		}
	}
	return err
}

// dogMol tracks a molecule (wisp) lifecycle for a daemon dog patrol.
// Graceful degradation: if bd fails, the dog still does its work — molecule
// tracking is observability, not control flow.
type dogMol struct {
	rootID   string            // Root wisp ID (e.g., "gt-wisp-abc123"), empty if pour failed.
	stepIDs  map[string]string // step slug -> wisp issue ID
	bdPath   string
	townRoot string
	logger   interface{ Printf(string, ...interface{}) }
}

// pourDogMolecule creates an ephemeral wisp molecule from a formula.
// Returns a dogMol handle for closing steps. If bd fails, returns a no-op
// handle so the caller can proceed without error checking.
func (d *Daemon) pourDogMolecule(formulaName string, vars map[string]string) *dogMol {
	dm := &dogMol{
		stepIDs:  make(map[string]string),
		bdPath:   d.bdPath,
		townRoot: d.config.TownRoot,
		logger:   d.logger,
	}

	// Build args: bd mol wisp <formula> --var k=v ...
	args := []string{"mol", "wisp", formulaName}
	for k, v := range vars {
		args = append(args, "--var", fmt.Sprintf("%s=%s", k, v))
	}

	out, err := dm.runBd(args...)
	if err != nil {
		d.logger.Printf("dog_molecule: pour %s failed (non-fatal): %v", formulaName, err)
		return dm
	}

	// Parse root ID from output. bd mol wisp prints the root ID on the first line.
	// Example output: "✓ Spawned wisp: gt-wisp-abc123 — Reap stale wisps..."
	dm.rootID = parseWispID(out)
	if dm.rootID == "" {
		d.logger.Printf("dog_molecule: pour %s: could not parse root ID from output: %s", formulaName, out)
		return dm
	}

	// Discover step IDs by listing children of the root wisp.
	dm.discoverSteps()

	d.logger.Printf("dog_molecule: poured %s → %s (%d steps)", formulaName, dm.rootID, len(dm.stepIDs))
	return dm
}

// closeStep marks a molecule step as closed.
func (dm *dogMol) closeStep(stepSlug string) {
	if dm.rootID == "" {
		return // No molecule — graceful degradation.
	}

	stepID, ok := dm.stepIDs[stepSlug]
	if !ok {
		dm.logger.Printf("dog_molecule: closeStep %q: unknown step (known: %v)", stepSlug, dm.knownSteps())
		return
	}

	if err := dm.closeWisp(stepID); err != nil {
		dm.logger.Printf("dog_molecule: close step %s (%s) failed after %d attempts (non-fatal): %v", stepSlug, stepID, dogCloseMaxAttempts, err)
		return
	}
}

// failStep marks a molecule step as failed with a reason.
func (dm *dogMol) failStep(stepSlug, reason string) {
	if dm.rootID == "" {
		return
	}

	stepID, ok := dm.stepIDs[stepSlug]
	if !ok {
		dm.logger.Printf("dog_molecule: failStep %q: unknown step", stepSlug)
		return
	}

	if err := dm.closeWisp(stepID, "--reason", reason); err != nil {
		dm.logger.Printf("dog_molecule: fail step %s (%s) failed after %d attempts (non-fatal): %v", stepSlug, stepID, dogCloseMaxAttempts, err)
	}
}

// close closes all remaining open child step wisps, then closes the root molecule wisp.
// This prevents orphan step wisps from accumulating when callers forget to
// explicitly close individual steps (the root cause of gt-3o59).
func (dm *dogMol) close() {
	if dm.rootID == "" {
		return
	}

	// Close any step wisps that were never explicitly closed/failed.
	dm.closeRemainingSteps()

	if err := dm.closeWisp(dm.rootID); err != nil {
		dm.logger.Printf("dog_molecule: close root %s failed after %d attempts (non-fatal): %v", dm.rootID, dogCloseMaxAttempts, err)
	}
}

// closeRemainingSteps queries all children of the root wisp and closes any that
// are still open. This is the backstop that prevents step wisp leaks regardless
// of whether individual callers remembered to close each step.
func (dm *dogMol) closeRemainingSteps() {
	if dm.rootID == "" {
		return
	}

	out, err := dm.runBd("show", dm.rootID, "--children", "--json")
	if err != nil {
		dm.logger.Printf("dog_molecule: closeRemainingSteps: list children of %s failed: %v", dm.rootID, err)
		return
	}

	children, parseErr := parseChildrenJSON(out)
	if parseErr != nil {
		dm.logger.Printf("dog_molecule: closeRemainingSteps: parse children JSON for %s failed: %v", dm.rootID, parseErr)
		return
	}

	closed := 0
	for _, child := range children {
		if child.ID == "" || child.Status == "" {
			continue
		}
		// Close any child that is still open/hooked/in_progress.
		if child.Status == "open" || child.Status == "hooked" || child.Status == "in_progress" {
			if err := dm.closeWisp(child.ID); err != nil {
				dm.logger.Printf("dog_molecule: closeRemainingSteps: close %s failed after %d attempts: %v", child.ID, dogCloseMaxAttempts, err)
			} else {
				closed++
			}
		}
	}
	if closed > 0 {
		dm.logger.Printf("dog_molecule: closeRemainingSteps: closed %d orphan step wisp(s) under %s", closed, dm.rootID)
	}
}

// discoverSteps lists children of the root wisp and maps step slugs to IDs.
// Step titles in the formula are like "Scan databases for stale wisps" —
// we match on the step ID embedded in the wisp title or metadata.
func (dm *dogMol) discoverSteps() {
	if dm.rootID == "" {
		return
	}

	// Use bd show to get children. The mol wisp command creates child wisps
	// whose titles include the step ID from the formula.
	out, err := dm.runBd("show", dm.rootID, "--children", "--json")
	if err != nil {
		dm.logger.Printf("dog_molecule: discover steps for %s failed: %v", dm.rootID, err)
		return
	}

	children, parseErr := parseChildrenJSON(out)
	if parseErr != nil {
		dm.logger.Printf("dog_molecule: discover steps: parse children JSON for %s failed: %v", dm.rootID, parseErr)
		return
	}

	// Map known step slugs from each child's title. The wisp title typically starts
	// with the step title from the formula.
	for _, child := range children {
		if child.ID == "" || child.Title == "" {
			continue
		}

		titleLower := strings.ToLower(child.Title)
		switch {
		case strings.Contains(titleLower, "scan"):
			dm.stepIDs["scan"] = child.ID
		case strings.Contains(titleLower, "reap"):
			dm.stepIDs["reap"] = child.ID
		case strings.Contains(titleLower, "purge"):
			dm.stepIDs["purge"] = child.ID
		case strings.Contains(titleLower, "report"):
			dm.stepIDs["report"] = child.ID
		case strings.Contains(titleLower, "export"):
			dm.stepIDs["export"] = child.ID
		case strings.Contains(titleLower, "push"):
			dm.stepIDs["push"] = child.ID
		case strings.Contains(titleLower, "diagnos"):
			dm.stepIDs["diagnose"] = child.ID
		case strings.Contains(titleLower, "backup"):
			dm.stepIDs["backup"] = child.ID
		case strings.Contains(titleLower, "probe"):
			dm.stepIDs["probe"] = child.ID
		case strings.Contains(titleLower, "inspect"):
			dm.stepIDs["inspect"] = child.ID
		case strings.Contains(titleLower, "clean"):
			dm.stepIDs["clean"] = child.ID
		case strings.Contains(titleLower, "verif"):
			dm.stepIDs["verify"] = child.ID
		case strings.Contains(titleLower, "compact"):
			dm.stepIDs["compact"] = child.ID
		case strings.Contains(titleLower, "checkpoint"):
			dm.stepIDs["checkpoint"] = child.ID
		case strings.Contains(titleLower, "auto-close") || strings.Contains(titleLower, "auto close"):
			dm.stepIDs["auto-close"] = child.ID
		case strings.Contains(titleLower, "sync"):
			dm.stepIDs["sync"] = child.ID
		case strings.Contains(titleLower, "offsite"):
			dm.stepIDs["offsite"] = child.ID
		case strings.Contains(titleLower, "rotat"):
			dm.stepIDs["rotate"] = child.ID
		}
	}
}

// childInfo holds fields from child wisp JSON used by discoverSteps and
// closeRemainingSteps.
type childInfo struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// parseChildrenJSON parses the output of `bd show <id> --children --json`.
// bd returns a map keyed by parent ID plus envelope metadata:
// {"hq-wisp-abc": [{...}, ...], "schema_version": 1}.
// For legacy compatibility, a bare array is also accepted.
func parseChildrenJSON(raw string) ([]childInfo, error) {
	data := bytes.TrimSpace([]byte(raw))
	if len(data) == 0 {
		return nil, fmt.Errorf("empty children JSON")
	}

	var arr []childInfo
	if data[0] == '[' {
		if err := json.Unmarshal(data, &arr); err != nil {
			return nil, err
		}
		return arr, nil
	}

	if data[0] != '{' {
		return nil, fmt.Errorf("unrecognized JSON shape: %.200s", raw)
	}

	var wrapped map[string]json.RawMessage
	if err := json.Unmarshal(data, &wrapped); err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(wrapped))
	for key := range wrapped {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var children []childInfo
	sawChildArray := false
	for _, key := range keys {
		if key == "schema_version" {
			continue
		}

		value := bytes.TrimSpace(wrapped[key])
		if len(value) == 0 {
			return nil, fmt.Errorf("empty child payload for key %q", key)
		}
		if value[0] != '[' {
			return nil, fmt.Errorf("non-array child payload for key %q", key)
		}

		var group []childInfo
		if err := json.Unmarshal(value, &group); err != nil {
			return nil, fmt.Errorf("parse child array for key %q: %w", key, err)
		}
		children = append(children, group...)
		sawChildArray = true
	}

	if !sawChildArray {
		return nil, fmt.Errorf("children JSON object has no child arrays")
	}

	return children, nil
}

// knownSteps returns the list of known step slugs for debugging.
func (dm *dogMol) knownSteps() []string {
	var steps []string
	for k := range dm.stepIDs {
		steps = append(steps, k)
	}
	return steps
}

// runBd executes a bd command and returns stdout.
func (dm *dogMol) runBd(args ...string) (string, error) {
	bdPath := dm.bdPath
	if bdPath == "" {
		bdPath = "bd"
	}

	ctx, cancel := context.WithTimeout(context.Background(), bdMolTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bdPath, args...)
	beads.ConfigureCommand(cmd, dm.townRoot, filepath.Join(dm.townRoot, ".beads"), beads.SubprocessModeForArgs(args))

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return "", fmt.Errorf("%s: %s", err, errMsg)
		}
		return "", err
	}

	return strings.TrimSpace(stdout.String()), nil
}

// parseWispID extracts a wisp ID from bd mol wisp output.
// Looks for patterns like "gt-wisp-abc123" or any ID containing "-wisp-".
func parseWispID(output string) string {
	for _, word := range strings.Fields(output) {
		// Strip ANSI codes and punctuation.
		cleaned := stripANSI(word)
		cleaned = strings.TrimRight(cleaned, ".,;:!?")
		if strings.Contains(cleaned, "-wisp-") {
			return cleaned
		}
	}
	// Fallback: look for any bead-like ID (prefix-xxxx pattern).
	for _, word := range strings.Fields(output) {
		cleaned := stripANSI(word)
		cleaned = strings.TrimRight(cleaned, ".,;:!?")
		if len(cleaned) > 3 && strings.Contains(cleaned, "-") && !strings.HasPrefix(cleaned, "--") {
			// Could be a bead ID like "gt-abc123".
			return cleaned
		}
	}
	return ""
}

// stripANSI removes ANSI escape codes from a string.
func stripANSI(s string) string {
	var result strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\033' {
			// Skip escape sequence.
			i++
			if i < len(s) && s[i] == '[' {
				i++
				for i < len(s) && !((s[i] >= 'A' && s[i] <= 'Z') || (s[i] >= 'a' && s[i] <= 'z')) {
					i++
				}
				if i < len(s) {
					i++ // Skip the terminating letter.
				}
			}
		} else {
			result.WriteByte(s[i])
			i++
		}
	}
	return result.String()
}
