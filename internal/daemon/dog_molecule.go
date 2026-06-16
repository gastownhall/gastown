package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
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

// pourResult is the JSON shape of `bd mol wisp --json`. id_mapping maps each
// formula node — "<formula>" for the root, "<formula>.<step>" for each step — to
// its freshly-created wisp ID.
type pourResult struct {
	IDMapping map[string]string `json:"id_mapping"`
	NewEpicID string            `json:"new_epic_id"`
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

	// Build args: bd mol wisp <formula> --json --var k=v ...
	// --json gives the full id_mapping so we capture every step wisp ID from the
	// pour's own response. Earlier code re-read the children via
	// `bd show <root> --children`, but under concurrent Dolt write load that
	// cross-connection read misses the just-committed molecule (bd returns its
	// no-issue envelope), so steps were never discovered or closed and leaked
	// permanently (gt-k61r). The pour's own connection always observes its writes.
	args := []string{"mol", "wisp", formulaName, "--json"}
	for k, v := range vars {
		args = append(args, "--var", fmt.Sprintf("%s=%s", k, v))
	}

	out, err := dm.runBd(args...)
	if err != nil {
		d.logger.Printf("dog_molecule: pour %s failed (non-fatal): %v", formulaName, err)
		return dm
	}

	dm.parsePour(out, formulaName)
	if dm.rootID == "" {
		d.logger.Printf("dog_molecule: pour %s: could not parse root ID from output: %s", formulaName, out)
		return dm
	}

	d.logger.Printf("dog_molecule: poured %s → %s (%d steps)", formulaName, dm.rootID, len(dm.stepIDs))
	return dm
}

// parsePour populates rootID and stepIDs from `bd mol wisp --json` output. If the
// JSON cannot be parsed it falls back to scraping the root ID from text so the
// molecule is still closeable.
func (dm *dogMol) parsePour(raw, formulaName string) {
	var pr pourResult
	if err := json.Unmarshal([]byte(raw), &pr); err != nil {
		dm.rootID = parseWispID(raw)
		dm.logger.Printf("dog_molecule: pour JSON parse failed (fell back to text root=%q): %v", dm.rootID, err)
		return
	}

	dm.rootID = pr.NewEpicID
	prefix := formulaName + "."
	for key, id := range pr.IDMapping {
		switch {
		case key == formulaName:
			if dm.rootID == "" {
				dm.rootID = id
			}
		case strings.HasPrefix(key, prefix):
			dm.stepIDs[strings.TrimPrefix(key, prefix)] = id
		}
	}
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

// close closes every step wisp of the molecule, then the root. Step IDs are
// captured at pour time (parsePour), so this needs no cross-connection read —
// the source of the step-wisp leak (gt-k61r). bd close is idempotent, so
// re-closing a step already closed via closeStep is a harmless no-op.
func (dm *dogMol) close() {
	if dm.rootID == "" {
		return
	}

	// --force: molecule steps carry a needs-based DAG (e.g. probe→inspect→report),
	// and bd refuses to close a wisp that is "blocked by" an open dependency. We
	// close in map order (unordered), and this is a teardown — the dependency
	// ordering models work sequencing, not a cleanup constraint — so force past
	// the blocked-by check. Without --force, dependency-blocked steps never close
	// and the step wisps leak even though discovery succeeded (gt-k61r follow-up).
	for slug, id := range dm.stepIDs {
		if err := dm.closeWisp(id, "--force"); err != nil {
			dm.logger.Printf("dog_molecule: close step %s (%s) failed after %d attempts (non-fatal): %v", slug, id, dogCloseMaxAttempts, err)
		}
	}

	if err := dm.closeWisp(dm.rootID, "--force"); err != nil {
		dm.logger.Printf("dog_molecule: close root %s failed after %d attempts (non-fatal): %v", dm.rootID, dogCloseMaxAttempts, err)
	}
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
	// Use mutation routing (BD_DOLT_AUTO_COMMIT=on) for all dog-molecule bd calls.
	// These are internal town-db molecule ops that must observe their own writes;
	// read-only routing's snapshot (BD_DOLT_AUTO_COMMIT=off) is stale under
	// concurrent Dolt load.
	beads.ConfigureCommand(cmd, dm.townRoot, filepath.Join(dm.townRoot, ".beads"), beads.MutationRouting)

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
