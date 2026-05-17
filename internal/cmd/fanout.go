package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

var fanoutCmd = &cobra.Command{
	Use:     "fanout --rig <rig> [--parent <bead-id>]",
	GroupID: GroupWork,
	Short:   "Throttled bulk bead creation pinned to a target rig",
	Long: `Create many beads in a target rig's database without ambient database discovery.

Reads bead titles from stdin (one per line). Blank lines and lines starting
with '#' are skipped. Each title becomes a new bead created serially in the
specified rig's database with a configurable delay between writes.

This command is designed for mayor orchestration workflows that need to convert
large GitHub issue triage sets into rig-owned tracking beads without triggering
Dolt instability from parallel writes or ambient database discovery.

Key properties:
  - All writes are pinned to --rig's database (no ambient discovery)
  - Serial writes with configurable rate limiting (--rate)
  - Idempotent: re-running with the same --state-file skips already-created beads
  - Partial failures are reported with enough detail to retry safely

Examples:
  # Create tasks for a rig epic (reads titles from stdin)
  echo -e "Fix auth bug\nAdd login rate limit\nAudit token storage" \
    | gt fanout --rig gastown --parent gt-epic123

  # Create from a file with throttling
  cat issue-titles.txt | gt fanout --rig gastown --rate 1s

  # Dry run to preview without creating
  cat titles.txt | gt fanout --rig gastown --parent gt-epic123 --dry-run

  # Resume after interruption using existing state file
  cat titles.txt | gt fanout --rig gastown --parent gt-epic123 \
    --state-file /tmp/fanout-epic123.jsonl

Input format (stdin):
  Fix the auth token refresh bug
  Add rate limiting to the login endpoint
  # This comment line is skipped
  Audit session token storage for compliance`,
	RunE: runFanout,
}

var (
	fanoutRig       string
	fanoutParent    string
	fanoutType      string
	fanoutPriority  string
	fanoutLabels    []string
	fanoutRate      time.Duration
	fanoutStateFile string
	fanoutDryRun    bool
)

func init() {
	fanoutCmd.Flags().StringVar(&fanoutRig, "rig", "", "Target rig (required); pins all writes to this rig's database")
	fanoutCmd.Flags().StringVar(&fanoutParent, "parent", "", "Parent epic bead ID; each created bead is linked as a child")
	fanoutCmd.Flags().StringVarP(&fanoutType, "type", "t", "task", "Bead type for created beads")
	fanoutCmd.Flags().StringVarP(&fanoutPriority, "priority", "p", "2", "Priority 0-4 for created beads")
	fanoutCmd.Flags().StringArrayVarP(&fanoutLabels, "label", "l", nil, "Label to add to each bead (repeatable)")
	fanoutCmd.Flags().DurationVar(&fanoutRate, "rate", 500*time.Millisecond, "Delay between writes to throttle Dolt load (0 to disable)")
	fanoutCmd.Flags().StringVar(&fanoutStateFile, "state-file", "", "JSONL file tracking progress; created automatically if not specified")
	fanoutCmd.Flags().BoolVarP(&fanoutDryRun, "dry-run", "n", false, "Preview what would be created without writing")

	_ = fanoutCmd.MarkFlagRequired("rig")
	rootCmd.AddCommand(fanoutCmd)
}

// fanoutStateEntry records one successfully created bead for idempotent re-runs.
type fanoutStateEntry struct {
	Title     string `json:"title"`
	BeadID    string `json:"bead_id"`
	CreatedAt string `json:"created_at"`
}

func runFanout(_ *cobra.Command, _ []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("finding town root: %w", err)
	}

	// Resolve the rig's beads directory, pinning all writes to it.
	// This prevents ambient database discovery when running from arbitrary cwd.
	rigDir := filepath.Join(townRoot, fanoutRig)
	// Guard against path traversal: --rig "../other" would escape townRoot.
	cleanTown := filepath.Clean(townRoot)
	if !strings.HasPrefix(filepath.Clean(rigDir), cleanTown+string(os.PathSeparator)) {
		return fmt.Errorf("rig %q escapes town root %s", fanoutRig, townRoot)
	}
	if info, err := os.Stat(rigDir); err != nil || !info.IsDir() {
		return fmt.Errorf("rig %q not found at %s", fanoutRig, rigDir)
	}
	rigBeadsDir := beads.ResolveBeadsDir(rigDir)

	// Read titles from stdin.
	titles, err := readFanoutTitles()
	if err != nil {
		return err
	}
	if len(titles) == 0 {
		return fmt.Errorf("no titles provided on stdin (one per line)")
	}

	// Load existing state for idempotency.
	stateFile := fanoutStateFile
	if stateFile == "" {
		key := fanoutRig
		if fanoutParent != "" {
			key = fanoutParent
		}
		stateFile = filepath.Join(os.TempDir(), fmt.Sprintf("gt-fanout-%s.jsonl", sanitizeFilename(key)))
	}
	done, err := loadFanoutState(stateFile)
	if err != nil {
		return fmt.Errorf("loading state file %s: %w", stateFile, err)
	}

	if fanoutDryRun {
		fmt.Printf("%s Dry run — would create %d beads in rig %q\n", style.Bold.Render("🔍"), len(titles), fanoutRig)
		if fanoutParent != "" {
			fmt.Printf("  Parent: %s\n", fanoutParent)
		}
		fmt.Printf("  Rate: %s between writes\n", fanoutRate)
		fmt.Printf("  State file: %s\n", stateFile)
		fmt.Printf("\n")
		for i, title := range titles {
			if beadID, ok := done[title]; ok {
				fmt.Printf("  [%d/%d] SKIP (already created as %s): %s\n", i+1, len(titles), beadID, title)
			} else {
				fmt.Printf("  [%d/%d] CREATE: %s\n", i+1, len(titles), title)
			}
		}
		return nil
	}

	// State file writer (append-only for resume safety).
	sf, err := os.OpenFile(stateFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("opening state file %s: %w", stateFile, err)
	}
	defer sf.Close()

	fmt.Printf("%s Creating %d beads in rig %q\n", style.Bold.Render("📋"), len(titles), fanoutRig)
	if fanoutParent != "" {
		fmt.Printf("  Parent: %s\n", fanoutParent)
	}
	fmt.Printf("  Rate: %s between writes\n", fanoutRate)
	fmt.Printf("  State: %s\n", stateFile)
	fmt.Printf("\n")

	type fanoutFailure struct {
		title string
		err   string
	}
	var created []string
	var failures []fanoutFailure   // bead creation failures (retryable via state file)
	var depWarnings []fanoutFailure // dep-link failures (bead created; fix manually)
	skipped := 0

	for i, title := range titles {
		if beadID, ok := done[title]; ok {
			fmt.Printf("  [%d/%d] %s %s → %s\n", i+1, len(titles), style.Dim.Render("SKIP"), title, beadID)
			skipped++
			continue
		}

		fmt.Printf("  [%d/%d] Creating: %s\n", i+1, len(titles), title)

		beadID, createErr := fanoutCreateBead(title, rigBeadsDir)
		if createErr != nil {
			fmt.Printf("         %s %v\n", style.Error.Render("✗"), createErr)
			failures = append(failures, fanoutFailure{title: title, err: createErr.Error()})
			continue
		}

		// Link to parent if specified. Dep failure is a warning — the bead exists
		// and is persisted, so retrying won't help. User must fix the link manually.
		if fanoutParent != "" {
			if depErr := fanoutAddParentDep(fanoutParent, beadID, rigBeadsDir); depErr != nil {
				fmt.Printf("         %s dep link failed: %v\n", style.Warning.Render("⚠"), depErr)
				depWarnings = append(depWarnings, fanoutFailure{
					title: title,
					err:   fmt.Sprintf("%s created but dep link failed: %v", beadID, depErr),
				})
			}
		}

		fmt.Printf("         %s %s\n", style.Success.Render("✓"), beadID)
		created = append(created, beadID)

		// Persist to state file before sleeping so partial runs are recoverable.
		// Also update done map to skip duplicate titles later in the same run.
		done[title] = beadID
		entry := fanoutStateEntry{
			Title:     title,
			BeadID:    beadID,
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		}
		if data, marshalErr := json.Marshal(entry); marshalErr != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to marshal state for %q: %v (re-run may re-create this bead)\n", beadID, marshalErr)
		} else if _, writeErr := fmt.Fprintf(sf, "%s\n", data); writeErr != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to write state for %q: %v (re-run may re-create this bead)\n", beadID, writeErr)
		} else {
			_ = sf.Sync()
		}

		// Throttle: delay before next write to avoid Dolt lock contention.
		if fanoutRate > 0 && i < len(titles)-1 {
			time.Sleep(fanoutRate)
		}
	}

	// Summary.
	fmt.Printf("\n%s Fanout complete: %d created, %d skipped, %d failed\n",
		style.Bold.Render("📊"), len(created), skipped, len(failures))

	if len(created) > 0 {
		fmt.Printf("  Created: %s\n", strings.Join(created, " "))
	}
	if len(depWarnings) > 0 {
		fmt.Printf("\n%s Dep-link warnings — beads created but parent links missing (fix with 'bd dep add'):\n",
			style.Warning.Render("⚠"))
		for _, w := range depWarnings {
			fmt.Printf("  ⚠ %q: %s\n", w.title, w.err)
		}
	}
	if len(failures) > 0 {
		fmt.Printf("\n%s Partial failures — retry with the same --state-file to skip successful beads:\n",
			style.Warning.Render("⚠"))
		for _, f := range failures {
			fmt.Printf("  ✗ %q: %s\n", f.title, f.err)
		}
		return fmt.Errorf("%d bead(s) failed to create", len(failures))
	}

	return nil
}

// readFanoutTitles reads bead titles from stdin, one per line.
// Blank lines and comment lines (starting with '#') are skipped.
func readFanoutTitles() ([]string, error) {
	var titles []string
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		titles = append(titles, line)
	}
	return titles, scanner.Err()
}

// loadFanoutState reads an existing state file and returns a map of title → bead ID.
// Returns an empty map if the file does not exist (fresh run).
func loadFanoutState(path string) (map[string]string, error) {
	done := make(map[string]string)
	f, err := os.Open(path) //nolint:gosec // G304: user-controlled path, intentional
	if os.IsNotExist(err) {
		return done, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var entry fanoutStateEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err == nil && entry.Title != "" {
			done[entry.Title] = entry.BeadID
		}
	}
	return done, scanner.Err()
}

// fanoutCreateBead creates a single bead in the pinned rig database.
// Returns the new bead ID on success.
func fanoutCreateBead(title, rigBeadsDir string) (string, error) {
	if beads.IsFlagLikeTitle(title) {
		return "", fmt.Errorf("title %q looks like a CLI flag; skip or quote it", title)
	}

	args := []string{
		"create",
		"--title=" + title,
		"--type=" + fanoutType,
		"--priority=" + fanoutPriority,
		"--silent",
	}
	for _, label := range fanoutLabels {
		args = append(args, "--label="+label)
	}

	out, err := BdCmd(args...).
		WithBeadsDir(rigBeadsDir).
		WithAutoCommit().
		Output()
	if err != nil {
		return "", fmt.Errorf("bd create: %w", err)
	}

	beadID := strings.TrimSpace(string(out))
	if beadID == "" {
		return "", fmt.Errorf("bd create returned empty ID for %q", title)
	}
	return beadID, nil
}

// fanoutAddParentDep links a child bead to its parent epic via a parent-child dep.
func fanoutAddParentDep(parentID, childID, rigBeadsDir string) error {
	return BdCmd("dep", "add", parentID, childID, "--type=parent-child").
		WithBeadsDir(rigBeadsDir).
		WithAutoCommit().
		Run()
}

// sanitizeFilename makes a string safe for use in a filename.
func sanitizeFilename(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}
