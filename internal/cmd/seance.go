package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/constants"
	"github.com/jonbaldie/gastown/internal/events"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/worker"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var (
	seanceRole   string
	seanceRig    string
	seanceRecent int
	seanceTalk   string
	seancePrompt string
	seanceJSON   bool
)

var seanceCmd = &cobra.Command{
	Use:     "seance",
	GroupID: GroupDiag,
	Short:   "Talk to your predecessor sessions",
	Long: `Seance lets you literally talk to predecessor sessions.

"Where did you put the stuff you left for me?" - The #1 handoff question.

Instead of parsing logs, seance spawns a Claude subprocess that resumes
a predecessor session with full context. You can ask questions directly:
  - "Why did you make this decision?"
  - "Where were you stuck?"
  - "What did you try that didn't work?"

DISCOVERY:
  gt seance                     # List recent sessions from events
  gt seance --role crew         # Filter by role type
  gt seance --rig gastown       # Filter by rig
  gt seance --recent 10         # Last N sessions

THE SEANCE (talk to predecessor):
  gt seance --talk <session-id>              # Interactive conversation
  gt seance --talk <id> -p "Where is X?"     # One-shot question

The --talk flag spawns: claude --fork-session --resume <id>
This loads the predecessor's full context without modifying their session.

Sessions are discovered from:
  1. Events emitted by SessionStart hooks (~/gt/.events.jsonl)
  2. The [GAS TOWN] beacon makes sessions searchable in /resume`,
	RunE: runSeance,
}

func init() {
	seanceCmd.Flags().StringVar(&seanceRole, "role", "", "Filter by role (crew, polecat, witness, etc.)")
	seanceCmd.Flags().StringVar(&seanceRig, "rig", "", "Filter by rig name")
	seanceCmd.Flags().IntVarP(&seanceRecent, "recent", "n", 20, "Number of recent sessions to show")
	seanceCmd.Flags().StringVarP(&seanceTalk, "talk", "t", "", "Session ID to commune with")
	seanceCmd.Flags().StringVarP(&seancePrompt, "prompt", "p", "", "One-shot prompt (with --talk)")
	seanceCmd.Flags().BoolVar(&seanceJSON, "json", false, "Output as JSON")

	rootCmd.AddCommand(seanceCmd)
}

// sessionEvent represents a session_start event from our event stream.
type sessionEvent struct {
	Timestamp string                 `json:"ts"`
	Type      string                 `json:"type"`
	Actor     string                 `json:"actor"`
	Payload   map[string]interface{} `json:"payload"`
}

func runSeance(cmd *cobra.Command, args []string) error {
	// If --talk is provided, spawn a seance
	if seanceTalk != "" {
		return runSeanceTalk(seanceTalk, seancePrompt)
	}

	// Otherwise, list discoverable sessions
	return runSeanceList()
}

func runSeanceList() error {
	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		return fmt.Errorf("not in a Gas Town workspace")
	}

	// Read session events from our event stream
	sessions, err := discoverSessions(townRoot)
	if err != nil {
		return fmt.Errorf("discovering sessions: %w", err)
	}

	// Apply filters
	var filtered []sessionEvent
	for _, s := range sessions {
		if seanceRole != "" {
			actor := strings.ToLower(s.Actor)
			if !strings.Contains(actor, strings.ToLower(seanceRole)) {
				continue
			}
		}
		if seanceRig != "" {
			actor := strings.ToLower(s.Actor)
			if !strings.Contains(actor, strings.ToLower(seanceRig)) {
				continue
			}
		}
		filtered = append(filtered, s)
	}

	// Apply limit
	if seanceRecent > 0 && len(filtered) > seanceRecent {
		filtered = filtered[:seanceRecent]
	}

	if seanceJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(filtered)
	}

	if len(filtered) == 0 {
		fmt.Println("No session events found.")
		fmt.Println(style.Dim.Render("Sessions are discovered from ~/gt/.events.jsonl"))
		fmt.Println(style.Dim.Render("Ensure SessionStart hooks emit session_start events"))
		return nil
	}

	// Print header
	fmt.Printf("%s\n\n", style.Bold.Render("Discoverable Sessions"))

	// Column widths
	idWidth := 12
	roleWidth := 26
	timeWidth := 16
	topicWidth := 28

	fmt.Printf("%-*s  %-*s  %-*s  %-*s\n",
		idWidth, "SESSION_ID",
		roleWidth, "ROLE",
		timeWidth, "STARTED",
		topicWidth, "TOPIC")
	fmt.Printf("%s\n", strings.Repeat("─", idWidth+roleWidth+timeWidth+topicWidth+6))

	for _, s := range filtered {
		sessionID := getPayloadString(s.Payload, "session_id")
		if len(sessionID) > idWidth {
			sessionID = sessionID[:idWidth-1] + "…"
		}

		role := s.Actor
		if len(role) > roleWidth {
			role = role[:roleWidth-1] + "…"
		}

		timeStr := formatEventTime(s.Timestamp)

		topic := getPayloadString(s.Payload, "topic")
		if topic == "" {
			topic = "-"
		}
		if len(topic) > topicWidth {
			topic = topic[:topicWidth-1] + "…"
		}

		fmt.Printf("%-*s  %-*s  %-*s  %-*s\n",
			idWidth, sessionID,
			roleWidth, role,
			timeWidth, timeStr,
			topicWidth, topic)
	}

	fmt.Printf("\n%s\n", style.Bold.Render("Talk to a predecessor:"))
	fmt.Printf("  gt seance --talk <session-id>\n")
	fmt.Printf("  gt seance --talk <session-id> -p \"Where did you put X?\"\n")

	return nil
}

// resolveSeanceCommand finds the command for an agent that supports --fork-session.
// Returns the resolved command path, or error if no agent supports fork session.
func resolveSeanceCommand() (string, error) {
	for _, name := range config.ListAgentPresets() {
		preset := config.GetAgentPresetByName(name)
		if preset != nil && preset.SupportsForkSession {
			// Use RuntimeConfigFromPreset to resolve the actual command path
			rc := config.RuntimeConfigFromPreset(preset.Name)
			return rc.Command, nil
		}
	}
	return "", fmt.Errorf("no agent supports fork session (seance requires --fork-session)")
}

func runSeanceTalk(sessionID, prompt string) error {
	// Resolve the agent command that supports fork session
	agentCmd, err := resolveSeanceCommand()
	if err != nil {
		return err
	}

	// Clean up any orphaned symlinks from previous interrupted sessions
	cleanupOrphanedSessionSymlinks()

	// Find workspace root (needed for both prefix resolution and session symlinks)
	townRoot, _ := workspace.FindFromCwd()

	// Resolve prefix to full session ID if needed
	if len(sessionID) < 36 {
		sessionID = strings.TrimSuffix(sessionID, "…")
		sessionID = strings.TrimSuffix(sessionID, "...")
		if townRoot != "" {
			resolved, err := resolveSessionPrefix(townRoot, sessionID)
			if err != nil {
				return fmt.Errorf("resolving session ID: %w", err)
			}
			sessionID = resolved
		}
	}

	fmt.Printf("%s Summoning session %s...\n\n", style.Bold.Render("🔮"), sessionID)
	cleanup, err := symlinkSessionToCurrentAccount(townRoot, sessionID)
	if err != nil {
		// Not fatal - session might already be in current account
		fmt.Printf("%s\n", style.Dim.Render("Note: "+err.Error()))
	}
	if cleanup != nil {
		defer cleanup()
	}

	// Build the command
	args := []string{"--fork-session", "--resume", sessionID}

	// Clear CLAUDECODE env var so the subprocess doesn't trigger the nested
	// session guard. Seance is intentionally spawning a new Claude Code
	// instance to read a predecessor's context — not a true nested session.
	env := clearClaudeCodeEnv(os.Environ())

	if prompt != "" {
		// One-shot mode: -p flag (boolean) makes claude print and exit,
		// prompt is a positional argument at the end.
		args = append(args, "-p", prompt)

		cmd := exec.Command(agentCmd, args...)
		cmd.Env = env
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			return fmt.Errorf("seance failed: %w", err)
		}
		return nil
	}

	// Interactive mode
	cmd := exec.Command(agentCmd, args...)
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Printf("%s\n", style.Dim.Render("You are now talking to your predecessor. Ask them anything."))
	fmt.Printf("%s\n\n", style.Dim.Render("Exit with /exit or Ctrl+C"))

	if err := cmd.Run(); err != nil {
		// Exit errors are normal when user exits
		if exitErr, ok := err.(*exec.ExitError); ok {
			if exitErr.ExitCode() == 0 || exitErr.ExitCode() == 130 {
				return nil // Normal exit or Ctrl+C
			}
		}
		return fmt.Errorf("seance ended: %w", err)
	}

	return nil
}

// clearClaudeCodeEnv returns a copy of the environment with Claude Code
// nesting-detection variables removed. This prevents the nested-session
// guard from blocking seance subprocesses.
func clearClaudeCodeEnv(environ []string) []string {
	var filtered []string
	for _, e := range environ {
		if strings.HasPrefix(e, "CLAUDECODE=") ||
			strings.HasPrefix(e, "CLAUDE_CODE_ENTRYPOINT=") {
			continue
		}
		filtered = append(filtered, e)
	}
	return filtered
}

// discoverSessions reads session_start events from our event stream.
func discoverSessions(townRoot string) ([]sessionEvent, error) {
	eventsPath := filepath.Join(townRoot, events.EventsFile)

	file, err := os.Open(eventsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()

	var sessions []sessionEvent
	scanner := bufio.NewScanner(file)

	// Increase buffer for large lines
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		var event sessionEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}

		if event.Type == events.TypeSessionStart {
			sessions = append(sessions, event)
		}
	}

	if wev, err := worker.ReadEvents(townRoot); err == nil {
		for _, ev := range wev {
			if ev.Type != worker.EventStarted && ev.Type != worker.EventStopped {
				continue
			}
			payload := map[string]interface{}{
				"session_id": ev.SessionID,
				"run_id":     ev.RunID,
				"bead_id":    ev.BeadID,
				"topic":      ev.Type,
			}
			sessions = append(sessions, sessionEvent{
				Timestamp: ev.Timestamp.UTC().Format(time.RFC3339),
				Type:      events.TypeSessionStart,
				Actor:     ev.SessionID,
				Payload:   payload,
			})
		}
	}

	// Sort by timestamp descending (most recent first)
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].Timestamp > sessions[j].Timestamp
	})

	return sessions, scanner.Err()
}

// resolveSessionPrefix resolves a truncated session ID prefix to the full UUID
// by searching session_start events. Returns an error if zero or multiple matches.
func resolveSessionPrefix(townRoot, prefix string) (string, error) {
	sessions, err := discoverSessions(townRoot)
	if err != nil {
		return "", fmt.Errorf("searching sessions: %w", err)
	}

	var matches []string
	seen := make(map[string]bool)
	for _, s := range sessions {
		id := getPayloadString(s.Payload, "session_id")
		if strings.HasPrefix(id, prefix) && !seen[id] {
			matches = append(matches, id)
			seen[id] = true
		}
	}

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no session found matching prefix %q", prefix)
	case 1:
		return matches[0], nil
	default:
		display := matches
		if len(display) > 3 {
			display = display[:3]
		}
		return "", fmt.Errorf("ambiguous prefix %q matches %d sessions: %s",
			prefix, len(matches), strings.Join(display, ", "))
	}
}

func getPayloadString(payload map[string]interface{}, key string) string {
	if v, ok := payload[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func formatEventTime(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	return t.Local().Format("2006-01-02 15:04")
}

// sessionsIndex represents the structure of sessions-index.json files.
// We use json.RawMessage for entries to preserve all fields when copying.
type sessionsIndex struct {
	Version int               `json:"version"`
	Entries []json.RawMessage `json:"entries"`
}

// sessionsIndexEntry is a minimal struct to extract just the sessionId from an entry.
type sessionsIndexEntry struct {
	SessionID string `json:"sessionId"`
}

// sessionLocation contains the location info for a session.
type sessionLocation struct {
	configDir  string // The account's config directory
	projectDir string // The project directory name (e.g., "-Users-jv-gt-gastown-crew-propane")
}

// sessionsIndexLockTimeout is how long to wait for the index lock.
const sessionsIndexLockTimeout = 5 * time.Second

// lockSessionsIndex acquires an exclusive lock on the sessions index file.
// Returns the lock (caller must unlock) or error if lock cannot be acquired.
// The lock file is created adjacent to the index file with a .lock suffix.
func lockSessionsIndex(indexPath string) (*flock.Flock, error) {
	lockPath := indexPath + ".lock"

	// Ensure the directory exists
	if err := os.MkdirAll(filepath.Dir(lockPath), 0755); err != nil {
		return nil, fmt.Errorf("creating lock directory: %w", err)
	}

	lock := flock.New(lockPath)
	ctx, cancel := context.WithTimeout(context.Background(), sessionsIndexLockTimeout)
	defer cancel()

	locked, err := lock.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("acquiring lock: %w", err)
	}
	if !locked {
		return nil, fmt.Errorf("timeout waiting for sessions index lock")
	}

	return lock, nil
}

// findSessionLocation searches all account config directories for a session.
// Returns the config directory and project directory that contain the session.
func findSessionLocation(townRoot, sessionID string) *sessionLocation {
	if townRoot == "" {
		return nil
	}

	// Load accounts config
	accountsPath := constants.MayorAccountsPath(townRoot)
	cfg, err := config.LoadAccountsConfig(accountsPath)
	if err == nil {
		// Search each account's config directory
		for _, acct := range cfg.Accounts {
			if acct.ConfigDir == "" {
				continue
			}

			// Expand ~ in path
			configDir := acct.ConfigDir
			if strings.HasPrefix(configDir, "~/") {
				home, _ := os.UserHomeDir()
				configDir = filepath.Join(home, configDir[2:])
			}

			// Search all sessions-index.json files in this account
			projectsDir := filepath.Join(configDir, "projects")
			if _, err := os.Stat(projectsDir); os.IsNotExist(err) {
				continue
			}

			// Walk through project directories
			entries, err := os.ReadDir(projectsDir)
			if err != nil {
				continue
			}

			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}

				indexPath := filepath.Join(projectsDir, entry.Name(), "sessions-index.json")
				if _, err := os.Stat(indexPath); os.IsNotExist(err) {
					continue
				}

				// Read and parse the sessions index
				data, err := os.ReadFile(indexPath)
				if err != nil {
					continue
				}

				var index sessionsIndex
				if err := json.Unmarshal(data, &index); err != nil {
					continue
				}

				// Check if this index contains our session
				for _, rawEntry := range index.Entries {
					var e sessionsIndexEntry
					if json.Unmarshal(rawEntry, &e) == nil && e.SessionID == sessionID {
						return &sessionLocation{
							configDir:  configDir,
							projectDir: entry.Name(),
						}
					}
				}
			}
		}
	}

	// Fallback: direct scan of ~/.claude/projects/ for single-account setups
	// where accounts.json is missing or yields no results
	home, homeErr := os.UserHomeDir()
	if homeErr == nil {
		claudeDir := filepath.Join(home, ".claude")
		resolved, evalErr := filepath.EvalSymlinks(claudeDir)
		if evalErr != nil {
			resolved = claudeDir
		}
		fallbackProjectsDir := filepath.Join(resolved, "projects")
		if fallbackEntries, readErr := os.ReadDir(fallbackProjectsDir); readErr == nil {
			for _, entry := range fallbackEntries {
				if !entry.IsDir() {
					continue
				}
				sessionFile := filepath.Join(fallbackProjectsDir, entry.Name(), sessionID+".jsonl")
				if _, statErr := os.Stat(sessionFile); statErr == nil {
					return &sessionLocation{
						configDir:  resolved,
						projectDir: entry.Name(),
					}
				}
			}
		}
	}

	return nil
}

// symlinkSessionToCurrentAccount finds a session in any account and symlinks
// it to the current account so Claude can access it.
// Returns a cleanup function to remove the symlink after use.
func symlinkSessionToCurrentAccount(townRoot, sessionID string) (cleanup func(), err error) {
	// Get current account's config directory (resolve ~/.claude symlink)
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("getting home directory: %w", err)
	}

	claudeDir := filepath.Join(home, ".claude")
	currentConfigDir, err := filepath.EvalSymlinks(claudeDir)
	if err != nil {
		// ~/.claude might not be a symlink, use it directly
		currentConfigDir = claudeDir
	}

	return symlinkSessionToConfigDir(townRoot, sessionID, currentConfigDir)
}

// symlinkSessionToConfigDir symlinks a session file from its source account into the
// given target config directory, updating the sessions-index.json so Claude can find it.
// Returns a cleanup function (may be nil if no work was needed) and any error.
func symlinkSessionToConfigDir(townRoot, sessionID, targetConfigDir string) (cleanup func(), err error) {
	// Find where the session lives
	loc := findSessionLocation(townRoot, sessionID)
	if loc == nil {
		return nil, fmt.Errorf("session not found in any account")
	}

	// Session in same account but possibly different project dir.
	// Symlink into cwd-based project dir so Claude can find it via --resume.
	// Resolve both paths to handle macOS /var → /private/var symlink differences.
	resolvedLocDir, _ := filepath.EvalSymlinks(loc.configDir)
	resolvedTargetDir, _ := filepath.EvalSymlinks(targetConfigDir)
	if resolvedLocDir == resolvedTargetDir {
		cwd, cwdErr := os.Getwd()
		if cwdErr != nil {
			return nil, nil
		}
		cwdProjectDir := strings.ReplaceAll(cwd, "/", "-")
		if cwdProjectDir == loc.projectDir {
			return nil, nil // Already in correct project dir
		}
		sourceFile := filepath.Join(targetConfigDir, "projects", loc.projectDir, sessionID+".jsonl")
		targetDir := filepath.Join(targetConfigDir, "projects", cwdProjectDir)
		if mkErr := os.MkdirAll(targetDir, 0755); mkErr != nil {
			return nil, nil
		}
		targetFile := filepath.Join(targetDir, sessionID+".jsonl")
		if _, lstatErr := os.Lstat(targetFile); lstatErr == nil {
			return nil, nil // Already exists
		}
		if symlinkErr := os.Symlink(sourceFile, targetFile); symlinkErr != nil {
			return nil, nil
		}
		return func() { _ = os.Remove(targetFile) }, nil
	}

	// Source: the session file in the other account
	sourceSessionFile := filepath.Join(loc.configDir, "projects", loc.projectDir, sessionID+".jsonl")

	// Check source exists
	if _, err := os.Stat(sourceSessionFile); os.IsNotExist(err) {
		return nil, fmt.Errorf("session file not found: %s", sourceSessionFile)
	}

	// Target: the project directory in the target account
	currentProjectDir := filepath.Join(targetConfigDir, "projects", loc.projectDir)

	// Create project directory if it doesn't exist
	if err := os.MkdirAll(currentProjectDir, 0755); err != nil {
		return nil, fmt.Errorf("creating project directory: %w", err)
	}

	// Symlink the specific session file
	targetSessionFile := filepath.Join(currentProjectDir, sessionID+".jsonl")

	// Check if target session file already exists
	if info, err := os.Lstat(targetSessionFile); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			// Already a symlink - check if it points to the right place
			existing, _ := os.Readlink(targetSessionFile)
			if existing == sourceSessionFile {
				// Already symlinked correctly, no cleanup needed
				return nil, nil
			}
			// Different symlink, remove it
			_ = os.Remove(targetSessionFile)
		} else {
			// Real file exists - session already in current account
			return nil, nil
		}
	}

	// Create the symlink to the session file
	if err := os.Symlink(sourceSessionFile, targetSessionFile); err != nil {
		return nil, fmt.Errorf("creating symlink: %w", err)
	}

	// Also need to update/create sessions-index.json so Claude can find the session
	// Read source index to get the session entry
	sourceIndexPath := filepath.Join(loc.configDir, "projects", loc.projectDir, "sessions-index.json")
	sourceIndexData, err := os.ReadFile(sourceIndexPath)
	if err != nil {
		// Clean up the symlink we just created
		_ = os.Remove(targetSessionFile)
		return nil, fmt.Errorf("reading source sessions index: %w", err)
	}

	var sourceIndex sessionsIndex
	if err := json.Unmarshal(sourceIndexData, &sourceIndex); err != nil {
		_ = os.Remove(targetSessionFile)
		return nil, fmt.Errorf("parsing source sessions index: %w", err)
	}

	// Find the session entry (as raw JSON to preserve all fields)
	var sessionEntry json.RawMessage
	for _, rawEntry := range sourceIndex.Entries {
		var e sessionsIndexEntry
		if json.Unmarshal(rawEntry, &e) == nil && e.SessionID == sessionID {
			sessionEntry = rawEntry
			break
		}
	}

	if sessionEntry == nil {
		_ = os.Remove(targetSessionFile)
		return nil, fmt.Errorf("session not found in source index")
	}

	// Read or create target index (with file locking to prevent race conditions)
	targetIndexPath := filepath.Join(currentProjectDir, "sessions-index.json")

	// Acquire lock for read-modify-write operation
	lock, err := lockSessionsIndex(targetIndexPath)
	if err != nil {
		_ = os.Remove(targetSessionFile)
		return nil, fmt.Errorf("locking sessions index: %w", err)
	}
	defer func() { _ = lock.Unlock() }()

	var targetIndex sessionsIndex
	if targetIndexData, err := os.ReadFile(targetIndexPath); err == nil {
		_ = json.Unmarshal(targetIndexData, &targetIndex)
	} else {
		targetIndex.Version = 1
	}

	// Check if session already in target index
	sessionInIndex := false
	for _, rawEntry := range targetIndex.Entries {
		var e sessionsIndexEntry
		if json.Unmarshal(rawEntry, &e) == nil && e.SessionID == sessionID {
			sessionInIndex = true
			break
		}
	}

	// Add session to target index if not present
	indexModified := false
	if !sessionInIndex {
		targetIndex.Entries = append(targetIndex.Entries, sessionEntry)
		indexModified = true

		// Write updated index
		targetIndexData, err := json.MarshalIndent(targetIndex, "", "  ")
		if err != nil {
			_ = os.Remove(targetSessionFile)
			return nil, fmt.Errorf("encoding target sessions index: %w", err)
		}
		if err := os.WriteFile(targetIndexPath, targetIndexData, 0600); err != nil {
			_ = os.Remove(targetSessionFile)
			return nil, fmt.Errorf("writing target sessions index: %w", err)
		}
	}

	// Return cleanup function
	cleanup = func() {
		_ = os.Remove(targetSessionFile)
		// If we modified the index, remove the entry we added
		if indexModified {
			// Acquire lock for read-modify-write operation
			cleanupLock, lockErr := lockSessionsIndex(targetIndexPath)
			if lockErr == nil {
				defer func() { _ = cleanupLock.Unlock() }()
			}
			// Proceed with best-effort cleanup even without lock,
			// to avoid leaving stale entries in sessions-index.json

			// Re-read index, remove our entry, write it back
			if data, err := os.ReadFile(targetIndexPath); err == nil {
				var idx sessionsIndex
				if json.Unmarshal(data, &idx) == nil {
					newEntries := make([]json.RawMessage, 0, len(idx.Entries))
					for _, rawEntry := range idx.Entries {
						var e sessionsIndexEntry
						if json.Unmarshal(rawEntry, &e) == nil && e.SessionID != sessionID {
							newEntries = append(newEntries, rawEntry)
						}
					}
					idx.Entries = newEntries
					if newData, err := json.MarshalIndent(idx, "", "  "); err == nil {
						_ = os.WriteFile(targetIndexPath, newData, 0600)
					}
				}
			}
		}
	}

	return cleanup, nil
}

// cleanupOrphanedSessionSymlinks removes stale session symlinks from the current account.
// This handles cases where a previous seance was interrupted (e.g., SIGKILL) and
// couldn't run its cleanup function. Call this at the start of seance operations.
func cleanupOrphanedSessionSymlinks() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}

	claudeDir := filepath.Join(home, ".claude")
	currentConfigDir, err := filepath.EvalSymlinks(claudeDir)
	if err != nil {
		currentConfigDir = claudeDir
	}

	projectsDir := filepath.Join(currentConfigDir, "projects")
	if _, err := os.Stat(projectsDir); os.IsNotExist(err) {
		return
	}

	// Walk through project directories
	projectEntries, err := os.ReadDir(projectsDir)
	if err != nil {
		return
	}

	for _, projEntry := range projectEntries {
		if !projEntry.IsDir() {
			continue
		}

		projPath := filepath.Join(projectsDir, projEntry.Name())
		files, err := os.ReadDir(projPath)
		if err != nil {
			continue
		}

		var orphanedSessionIDs []string

		for _, f := range files {
			if !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}

			filePath := filepath.Join(projPath, f.Name())
			info, err := os.Lstat(filePath)
			if err != nil {
				continue
			}

			// Only check symlinks
			if info.Mode()&os.ModeSymlink == 0 {
				continue
			}

			// Check if symlink target exists
			target, err := os.Readlink(filePath)
			if err != nil {
				continue
			}

			if _, err := os.Stat(target); os.IsNotExist(err) {
				// Target doesn't exist - this is an orphaned symlink
				sessionID := strings.TrimSuffix(f.Name(), ".jsonl")
				orphanedSessionIDs = append(orphanedSessionIDs, sessionID)
				_ = os.Remove(filePath)
			}
		}

		// Clean up orphaned entries from sessions-index.json
		if len(orphanedSessionIDs) > 0 {
			indexPath := filepath.Join(projPath, "sessions-index.json")

			// Acquire lock for read-modify-write operation
			lock, lockErr := lockSessionsIndex(indexPath)
			if lockErr != nil {
				// Best effort cleanup - skip this project if lock fails
				continue
			}

			data, err := os.ReadFile(indexPath)
			if err != nil {
				_ = lock.Unlock()
				continue
			}

			var index sessionsIndex
			if err := json.Unmarshal(data, &index); err != nil {
				_ = lock.Unlock()
				continue
			}

			// Build a set of orphaned IDs for fast lookup
			orphanedSet := make(map[string]bool)
			for _, id := range orphanedSessionIDs {
				orphanedSet[id] = true
			}

			// Filter out orphaned entries
			newEntries := make([]json.RawMessage, 0, len(index.Entries))
			for _, rawEntry := range index.Entries {
				var e sessionsIndexEntry
				if json.Unmarshal(rawEntry, &e) == nil && !orphanedSet[e.SessionID] {
					newEntries = append(newEntries, rawEntry)
				}
			}

			if len(newEntries) != len(index.Entries) {
				index.Entries = newEntries
				if newData, err := json.MarshalIndent(index, "", "  "); err == nil {
					_ = os.WriteFile(indexPath, newData, 0600)
				}
			}

			_ = lock.Unlock()
		}
	}
}
