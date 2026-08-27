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

	// dogSummaryMaxLen caps the step summary recorded as the root wisp's close
	// reason, so a chatty failure message cannot bloat the wisp row.
	dogSummaryMaxLen = 500
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

// dogStepResult records the outcome of one formula step within a dog cycle.
type dogStepResult struct {
	slug   string
	failed bool
	reason string
}

// dogMol tracks a molecule (wisp) lifecycle for a daemon dog patrol.
// Graceful degradation: if bd fails, the dog still does its work — molecule
// tracking is observability, not control flow.
//
// Wisps are root-only (gt-th5n, b8f79dc8): `bd mol wisp` materializes child step
// rows only for formulas that opt in with `pour = true`, and no dog formula does
// — materializing them would recreate the wisp flood the root-only default was
// introduced to stop (mol-dog-doctor alone runs every 5 minutes with 13 steps).
// So step outcomes are accumulated in memory here and summarized onto the root
// wisp when the cycle closes, rather than closing per-step child wisps.
type dogMol struct {
	rootID   string          // Root wisp ID (e.g., "gt-wisp-abc123"), empty if pour failed.
	steps    []dogStepResult // Ordered step outcomes for this cycle.
	bdPath   string
	townRoot string
	logger   interface{ Printf(string, ...interface{}) }
}

// pourDogMolecule creates an ephemeral wisp molecule from a formula.
// Returns a dogMol handle for closing steps. If bd fails, returns a no-op
// handle so the caller can proceed without error checking.
func (d *Daemon) pourDogMolecule(formulaName string, vars map[string]string) *dogMol {
	dm := &dogMol{
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

	d.logger.Printf("dog_molecule: poured %s → %s (root-only)", formulaName, dm.rootID)
	return dm
}

// closeStep marks a molecule step as completed. Root-only wisps have no child
// row to close, so the outcome is recorded and reported when the cycle closes.
func (dm *dogMol) closeStep(stepSlug string) {
	dm.recordStep(stepSlug, false, "")
}

// failStep marks a molecule step as failed with a reason.
func (dm *dogMol) failStep(stepSlug, reason string) {
	dm.recordStep(stepSlug, true, reason)
}

// recordStep stores a step outcome, replacing any earlier outcome for the same
// slug so a step that is retried within a cycle reports its final state.
func (dm *dogMol) recordStep(stepSlug string, failed bool, reason string) {
	if dm.rootID == "" {
		return // No molecule — graceful degradation.
	}
	for i := range dm.steps {
		if dm.steps[i].slug == stepSlug {
			dm.steps[i].failed = failed
			dm.steps[i].reason = reason
			return
		}
	}
	dm.steps = append(dm.steps, dogStepResult{slug: stepSlug, failed: failed, reason: reason})
}

// stepSummary renders the cycle's step outcomes as a single line, e.g.
// "scan ok, reap ok, purge failed (2 databases had errors)". Empty if no steps
// were recorded. The result is truncated so it stays usable as a close reason.
func (dm *dogMol) stepSummary() string {
	if len(dm.steps) == 0 {
		return ""
	}
	parts := make([]string, 0, len(dm.steps))
	for _, step := range dm.steps {
		if !step.failed {
			parts = append(parts, step.slug+" ok")
			continue
		}
		if step.reason == "" {
			parts = append(parts, step.slug+" failed")
			continue
		}
		parts = append(parts, fmt.Sprintf("%s failed (%s)", step.slug, step.reason))
	}
	return truncateSummary(strings.Join(parts, ", "), dogSummaryMaxLen)
}

// truncateSummary caps s at maxRunes, appending an ellipsis when it cuts.
func truncateSummary(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "…"
}

// close records the cycle's step outcomes on the root molecule wisp and closes
// it. It first closes any materialized child step wisps that were left open,
// which prevents orphan step wisps from accumulating for `pour = true` formulas
// whose callers forget to close individual steps (the root cause of gt-3o59).
func (dm *dogMol) close() {
	if dm.rootID == "" {
		return
	}

	// Backstop for formulas that opt into `pour = true`: close any materialized
	// step wisps that were never explicitly closed. A no-op for root-only wisps.
	dm.closeRemainingSteps()

	// Record the cycle's step outcomes on the root wisp. This is the observability
	// the per-step child wisps used to carry.
	var closeArgs []string
	if summary := dm.stepSummary(); summary != "" {
		dm.logger.Printf("dog_molecule: %s steps: %s", dm.rootID, summary)
		closeArgs = []string{"--reason", "steps: " + summary}
	}

	if err := dm.closeWisp(dm.rootID, closeArgs...); err != nil {
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

// childInfo holds fields from child wisp JSON used by closeRemainingSteps.
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
