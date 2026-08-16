//go:build !windows

package util

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jonbaldie/gastown/internal/lock"
	"github.com/jonbaldie/gastown/internal/process"
	"github.com/jonbaldie/gastown/internal/tmux"
)

// minOrphanAge is the minimum age (in seconds) a process must be before
// we consider it orphaned. This prevents race conditions with newly spawned
// processes and avoids killing legitimate short-lived subagents.
const minOrphanAge = 60

// buildChildMap builds a parent→children map from a single ps call.
// This replaces per-PID pgrep calls, reducing O(N) process spawns to O(1).
func buildChildMap() map[int][]int {
	table, err := process.Capture()
	if err != nil {
		return map[int][]int{}
	}
	children := make(map[int][]int)
	for _, p := range table.All() {
		children[p.PPID] = append(children[p.PPID], p.PID)
	}
	return children
}

// addDescendants adds all descendant PIDs of a process to the set using
// a pre-built child map (no additional process spawns).
func addDescendants(parentPID int, childMap map[int][]int, pids map[int]bool) {
	for _, pid := range childMap[parentPID] {
		if !pids[pid] {
			pids[pid] = true
			addDescendants(pid, childMap, pids)
		}
	}
}

// getTmuxSessionPIDs returns a set of PIDs belonging to ANY tmux session
// across ALL tmux sockets on this machine.
//
// This prevents killing Claude processes that are running in tmux sessions,
// even if they temporarily show TTY "?" during startup or session transitions.
//
// CRITICAL: We protect ALL tmux sessions on ALL sockets. When multiple Gas Town
// instances run on the same machine, each uses its own tmux socket. A single-socket
// query would miss processes in other towns' sessions, causing cross-town kills.
func getTmuxSessionPIDs() map[int]bool {
	pids := make(map[int]bool)

	// Build process tree once, shared across all socket scans
	childMap := buildChildMap()

	// Scan all tmux sockets in the socket directory.
	// Each Gas Town instance (and any personal tmux servers) gets its own socket.
	socketDir := tmux.SocketDir()
	entries, err := os.ReadDir(socketDir)
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			socketPath := filepath.Join(socketDir, entry.Name())
			collectPanePIDs(socketPath, childMap, pids)
		}
	}

	// Also query the current town's socket via BuildCommand as a fallback.
	// This handles non-standard socket locations (e.g. GT_TMUX_SOCKET override).
	out, err := tmux.BuildCommand("list-panes", "-a", "-F", "#{pane_pid}").Output()
	if err == nil {
		for _, pidStr := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if pid, err := strconv.Atoi(pidStr); err == nil && pid > 0 {
				pids[pid] = true
				addDescendants(pid, childMap, pids)
			}
		}
	}

	return pids
}

// collectPanePIDs queries a single tmux socket for all pane PIDs and adds them
// (plus their descendant processes) to the protection set.
func collectPanePIDs(socketPath string, childMap map[int][]int, pids map[int]bool) {
	out, err := exec.Command("tmux", "-S", socketPath, "list-panes", "-a", "-F", "#{pane_pid}").Output()
	if err != nil {
		return
	}
	for _, pidStr := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if pid, err := strconv.Atoi(pidStr); err == nil && pid > 0 {
			pids[pid] = true
			addDescendants(pid, childMap, pids)
		}
	}
}

// getACPSessionPIDs returns a set of PIDs belonging to active ACP (Agent Client Protocol) sessions.
// ACP sessions run outside of tmux and would otherwise be killed by the zombie-scan.
// We protect the ACP proxy process and all its children (including the opencode agent).
func getACPSessionPIDs() map[int]bool {
	pids := make(map[int]bool)

	// Find all town roots by looking for mayor-acp.pid files
	// Common locations: ~/gt, ~/town-*, etc.
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return pids
	}

	// Build process tree once
	childMap := buildChildMap()

	// Check the primary town root (~/gt)
	pidPath := filepath.Join(homeDir, "gt", "mayor", "mayor-acp.pid")
	if data, err := os.ReadFile(pidPath); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
			// Check if process is still alive
			if processExists(pid) {
				pids[pid] = true
				// Add all child processes (including the opencode agent)
				addDescendants(pid, childMap, pids)
			}
		}
	}

	return pids
}

// sigkillGracePeriod is how long (in seconds) we wait after sending SIGTERM
// before escalating to SIGKILL. If a process was sent SIGTERM and is still
// around after this period, we use SIGKILL on the next cleanup cycle.
const sigkillGracePeriod = 60

// signalState tracks what signal was last sent to a PID and when.
type signalState struct {
	Signal    string    // "SIGTERM" or "SIGKILL"
	Timestamp time.Time // When the signal was sent
}

// stateFileDir returns the directory for state files.
func stateFileDir() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = "/tmp"
	}
	return dir
}

// loadSignalState reads a state file and returns the current signal state
// for each tracked PID. Automatically cleans up entries for dead processes.
// Uses file locking to prevent concurrent access.
func loadSignalState(filename string) map[int]signalState {
	state := make(map[int]signalState)

	path := filepath.Join(stateFileDir(), filename)

	// Acquire coordination lock (serializes with saveSignalState)
	unlock, err := lock.FlockAcquire(path + ".flock")
	if err != nil {
		return state
	}
	defer unlock()

	f, err := os.Open(path)
	if err != nil {
		return state // File doesn't exist yet, that's fine
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		parts := strings.Fields(scanner.Text())
		if len(parts) != 3 {
			continue
		}
		pid, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}
		sig := parts[1]
		ts, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			continue
		}

		// Only keep if process still exists
		if err := syscall.Kill(pid, 0); err == nil || err == syscall.EPERM {
			state[pid] = signalState{Signal: sig, Timestamp: time.Unix(ts, 0)}
		}
	}

	return state
}

// saveSignalState writes the current signal state to a state file.
// Uses file locking to prevent concurrent access.
func saveSignalState(filename string, state map[int]signalState) error {
	path := filepath.Join(stateFileDir(), filename)

	// Acquire coordination lock (serializes with loadSignalState)
	unlock, err := lock.FlockAcquire(path + ".flock")
	if err != nil {
		return fmt.Errorf("acquiring lock: %w", err)
	}
	defer unlock()

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	for pid, s := range state {
		fmt.Fprintf(f, "%d %s %d\n", pid, s.Signal, s.Timestamp.Unix())
	}
	return nil
}

// orphanStateFile is the filename for orphan process tracking state.
const orphanStateFile = "gastown-orphan-state"

// loadOrphanState reads the orphan state file.
func loadOrphanState() map[int]signalState {
	return loadSignalState(orphanStateFile)
}

// saveOrphanState writes the orphan state file.
func saveOrphanState(state map[int]signalState) error {
	return saveSignalState(orphanStateFile, state)
}

// processExists checks if a process is still running.
func processExists(pid int) bool {
	return process.Alive(pid)
}

// getProcessCwd returns the current working directory of a process.
// On Linux, reads /proc/<pid>/cwd. On macOS and other Unix, uses lsof.
// Returns empty string if the cwd cannot be determined.
//
// On hardened Linux kernels (Ubuntu default: kernel.yama.ptrace_scope=1),
// readlink(/proc/<pid>/cwd) fails with EACCES for non-descendant same-user
// processes. The lsof fallback handles this when lsof is installed (it may
// be setuid or hold CAP_SYS_PTRACE). If neither method works, "" is returned
// and the caller fails safe by not killing the process.
func getProcessCwd(pid int) string {
	pidStr := strconv.Itoa(pid)

	// Try /proc/<pid>/cwd first (Linux).
	// Fails on hardened kernels (ptrace_scope>=1) for non-descendant processes.
	if target, err := os.Readlink(filepath.Join("/proc", pidStr, "cwd")); err == nil {
		// Linux appends " (deleted)" when the directory has been removed.
		// Strip it so the walk-up in isInGasTownWorkspace can still match
		// the workspace root (the process is definitely orphaned if its
		// workspace was nuked).
		return strings.TrimSuffix(target, " (deleted)")
	}

	// Fallback: lsof (macOS, and Linux when /proc is restricted by ptrace_scope).
	// -a is required to AND the -p and -d conditions; without it lsof ORs them.
	// lsof may be setuid or have CAP_SYS_PTRACE, letting it succeed where
	// readlink failed. Not installed by default on Alpine or minimal Ubuntu images.
	out, err := exec.Command("lsof", "-a", "-p", pidStr, "-d", "cwd", "-Fn").Output()
	if err != nil {
		return ""
	}
	// lsof -Fn output: lines starting with 'p' (pid) and 'n' (name/path)
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n") {
			return line[1:]
		}
	}
	return ""
}

// resolveTownRoot returns the Gas Town workspace root for a process, identified
// by walking up from its CWD looking for the mayor/town.json marker.
// Returns the workspace root path, or "" if the process is not in any workspace
// or its CWD cannot be determined.
func resolveTownRoot(pid int) string {
	cwd := getProcessCwd(pid)
	if cwd == "" {
		return ""
	}
	return resolveTownRootFromDir(cwd)
}

// resolveTownRootFromDir walks up from dir looking for mayor/town.json.
// Returns the workspace root path, or "" if not found.
func resolveTownRootFromDir(dir string) string {
	current := dir
	for {
		if _, err := os.Stat(filepath.Join(current, "mayor", "town.json")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}

// isInGasTownWorkspace checks whether a process's working directory is inside
// a Gas Town workspace (identified by the mayor/town.json marker).
func isInGasTownWorkspace(pid int) bool {
	return resolveTownRoot(pid) != ""
}

// isIDEClaudeProcess checks if a Claude process was spawned by an IDE extension
// (VS Code, Cursor, etc.). IDE-launched Claude processes run with TTY "?" but
// are legitimate — they're controlled by the IDE, not orphaned from dead sessions.
func isIDEClaudeProcess(pid int) bool {
	args := process.CommandLine(pid)
	if args == "" {
		return false
	}
	// Check for IDE-specific paths in the executable
	if strings.Contains(args, "vscode-server") ||
		strings.Contains(args, "vscode/extensions") ||
		strings.Contains(args, ".cursor-server") ||
		strings.Contains(args, ".cursor/extensions") {
		return true
	}
	// Generic IDE detection: stream-json I/O is specific to IDE extensions
	if strings.Contains(args, "--output-format stream-json") &&
		strings.Contains(args, "--input-format stream-json") {
		return true
	}
	return false
}

// parseEtime parses ps etime format into seconds.
// Format: [[DD-]HH:]MM:SS
// Examples: "01:23" (83s), "01:02:03" (3723s), "2-01:02:03" (176523s)
func parseEtime(etime string) (int, error) {
	var days, hours, minutes, seconds int

	// Check for days component (DD-HH:MM:SS)
	if idx := strings.Index(etime, "-"); idx != -1 {
		d, err := strconv.Atoi(etime[:idx])
		if err != nil {
			return 0, fmt.Errorf("parsing days: %w", err)
		}
		days = d
		etime = etime[idx+1:]
	}

	// Split remaining by colons
	parts := strings.Split(etime, ":")
	switch len(parts) {
	case 2: // MM:SS
		m, err := strconv.Atoi(parts[0])
		if err != nil {
			return 0, fmt.Errorf("parsing minutes: %w", err)
		}
		s, err := strconv.Atoi(parts[1])
		if err != nil {
			return 0, fmt.Errorf("parsing seconds: %w", err)
		}
		minutes, seconds = m, s
	case 3: // HH:MM:SS
		h, err := strconv.Atoi(parts[0])
		if err != nil {
			return 0, fmt.Errorf("parsing hours: %w", err)
		}
		m, err := strconv.Atoi(parts[1])
		if err != nil {
			return 0, fmt.Errorf("parsing minutes: %w", err)
		}
		s, err := strconv.Atoi(parts[2])
		if err != nil {
			return 0, fmt.Errorf("parsing seconds: %w", err)
		}
		hours, minutes, seconds = h, m, s
	default:
		return 0, fmt.Errorf("unexpected etime format: %s", etime)
	}

	return days*86400 + hours*3600 + minutes*60 + seconds, nil
}

// isAgentOrphanCommName returns true if the ps "comm" field names a runtime we track for
// TTY-less orphan / zombie cleanup (matches internal/config agent presets).
func isAgentOrphanCommName(cmdLower string) bool {
	return process.IsKnownAgent(cmdLower)
}

// OrphanedProcess represents a claude process running without a controlling terminal.
type OrphanedProcess struct {
	PID      int
	Cmd      string
	Age      int    // Age in seconds
	TownRoot string // Gas Town workspace root, or "" if not in any workspace
}

// FindOrphanedClaudeProcesses finds Gas Town agent processes (claude/codex/opencode/cursor-agent/copilot, etc.)
// without a controlling terminal.
// These are typically subagent processes spawned by Claude Code's Task tool that didn't
// clean up properly after completion.
//
// Detection is based on TTY column: processes with TTY "?" have no controlling terminal.
// This is safer than process tree walking because:
// - Legitimate terminal sessions always have a TTY (pts/*)
// - Orphaned subagents have no TTY (?)
// - Won't accidentally kill user's personal claude instances in terminals
//
// Additionally, processes must be older than minOrphanAge seconds to be considered
// orphaned. This prevents race conditions with newly spawned processes.
func FindOrphanedClaudeProcesses() ([]OrphanedProcess, error) {
	// Get PIDs belonging to valid Gas Town tmux sessions.
	// These should not be killed even if they show TTY "?" during startup.
	protectedPIDs := getTmuxSessionPIDs()

	// Also protect ACP sessions (opencode agents running outside tmux)
	// ACP sessions have their own lifecycle management and should not be killed
	acpPIDs := getACPSessionPIDs()
	for pid := range acpPIDs {
		protectedPIDs[pid] = true
	}

	table, err := process.Capture()
	if err != nil {
		return nil, fmt.Errorf("listing processes: %w", err)
	}

	var orphans []OrphanedProcess
	for _, p := range table.All() {
		tty := p.TTY
		if tty != "?" && tty != "??" {
			continue
		}
		if !isAgentOrphanCommName(strings.ToLower(p.Name)) {
			continue
		}
		if protectedPIDs[p.PID] {
			continue
		}
		if isIDEClaudeProcess(p.PID) {
			continue
		}
		age := int(p.Elapsed / time.Second)
		if age < minOrphanAge {
			continue
		}
		townRoot := resolveTownRoot(p.PID)
		if townRoot == "" {
			continue
		}

		orphans = append(orphans, OrphanedProcess{
			PID:      p.PID,
			Cmd:      p.Name,
			Age:      age,
			TownRoot: townRoot,
		})
	}

	return orphans, nil
}

// CleanupResult describes what happened to an orphaned process.
type CleanupResult struct {
	Process OrphanedProcess
	Signal  string // "SIGTERM", "SIGKILL", or "UNKILLABLE"
	Error   error
}

// ZombieProcess represents a claude process not in any active tmux session.
type ZombieProcess struct {
	PID      int
	Cmd      string
	Age      int    // Age in seconds
	TTY      string // TTY column from ps (may be "?" or a session like "s024")
	TownRoot string // Gas Town workspace root, or "" if not in any workspace
}

// FindZombieClaudeProcesses finds Claude processes with no TTY that are NOT in
// any active tmux session. This catches "zombie" processes whose tmux session
// has died. Processes with a real TTY (e.g. pts/*) are skipped because those
// are interactive terminal sessions, not zombies.
func FindZombieClaudeProcesses() ([]ZombieProcess, error) {
	// Get ALL valid PIDs (panes + their children) from active tmux sessions
	validPIDs := getTmuxSessionPIDs()

	// Also protect ACP sessions (opencode agents running outside tmux)
	// ACP sessions have their own lifecycle management and should not be killed
	acpPIDs := getACPSessionPIDs()
	for pid := range acpPIDs {
		validPIDs[pid] = true
	}

	// SAFETY CHECK: If no valid PIDs found, tmux might be down or no sessions exist.
	// Returning empty is safer than marking all Claude processes as zombies.
	if len(validPIDs) == 0 {
		// Check if tmux is even running
		if err := tmux.BuildCommand("list-sessions").Run(); err != nil {
			return nil, fmt.Errorf("tmux not available: %w", err)
		}
		// tmux is running but no gt-*/hq-* sessions - that's a valid state,
		// but we can't safely determine zombies without reference sessions.
		// Return empty rather than marking everything as zombie.
		return nil, nil
	}

	table, err := process.Capture()
	if err != nil {
		return nil, fmt.Errorf("listing processes: %w", err)
	}

	var zombies []ZombieProcess
	for _, p := range table.All() {
		if !isAgentOrphanCommName(strings.ToLower(p.Name)) {
			continue
		}
		if validPIDs[p.PID] {
			continue
		}
		if p.TTY != "?" && p.TTY != "??" {
			continue
		}
		if isIDEClaudeProcess(p.PID) {
			continue
		}
		age := int(p.Elapsed / time.Second)
		if age < minOrphanAge {
			continue
		}
		townRoot := resolveTownRoot(p.PID)
		if townRoot == "" {
			continue
		}

		zombies = append(zombies, ZombieProcess{
			PID:      p.PID,
			Cmd:      p.Name,
			Age:      age,
			TTY:      p.TTY,
			TownRoot: townRoot,
		})
	}

	return zombies, nil
}

// zombieStateFile is the filename for zombie process tracking state.
const zombieStateFile = "gastown-zombie-state"

// loadZombieState reads the zombie state file.
func loadZombieState() map[int]signalState {
	return loadSignalState(zombieStateFile)
}

// saveZombieState writes the zombie state file.
func saveZombieState(state map[int]signalState) error {
	return saveSignalState(zombieStateFile, state)
}

// ZombieCleanupResult describes what happened to a zombie process.
type ZombieCleanupResult struct {
	Process ZombieProcess
	Signal  string // "SIGTERM", "SIGKILL", or "UNKILLABLE"
	Error   error
}

// CleanupZombieClaudeProcesses finds and kills zombie Claude processes.
// Uses tmux verification to ensure we never kill processes in active sessions.
//
// Uses the same graceful escalation as orphan cleanup:
//  1. First encounter → SIGTERM, record in state file
//  2. Next cycle, still alive after grace period → SIGKILL
//  3. Next cycle, still alive after SIGKILL → log as unkillable
func CleanupZombieClaudeProcesses() ([]ZombieCleanupResult, error) {
	zombies, err := FindZombieClaudeProcesses()
	if err != nil {
		return nil, err
	}

	state := loadZombieState()
	now := time.Now()

	var results []ZombieCleanupResult
	var lastErr error

	activeZombies := make(map[int]bool)
	for _, z := range zombies {
		activeZombies[z.PID] = true
	}

	// Check state for PIDs that died or need escalation
	for pid, s := range state {
		if !activeZombies[pid] {
			delete(state, pid)
			continue
		}

		elapsed := now.Sub(s.Timestamp).Seconds()

		if s.Signal == "SIGKILL" {
			results = append(results, ZombieCleanupResult{
				Process: ZombieProcess{PID: pid, Cmd: "claude"},
				Signal:  "UNKILLABLE",
				Error:   fmt.Errorf("process %d survived SIGKILL", pid),
			})
			delete(state, pid)
			delete(activeZombies, pid)
			continue
		}

		if s.Signal == "SIGTERM" && elapsed >= float64(sigkillGracePeriod) {
			if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
				if err != syscall.ESRCH {
					lastErr = fmt.Errorf("SIGKILL PID %d: %w", pid, err)
				}
				delete(state, pid)
				delete(activeZombies, pid)
				continue
			}
			state[pid] = signalState{Signal: "SIGKILL", Timestamp: now}
			results = append(results, ZombieCleanupResult{
				Process: ZombieProcess{PID: pid, Cmd: "claude"},
				Signal:  "SIGKILL",
			})
			delete(activeZombies, pid)
		}
	}

	// Send SIGTERM to new zombies
	for _, zombie := range zombies {
		if !activeZombies[zombie.PID] {
			continue
		}
		if _, exists := state[zombie.PID]; exists {
			continue
		}

		// TOCTOU guard: re-verify this process is still a zombie before signaling.
		// Between FindZombieClaudeProcesses() and now, the process may have
		// joined a tmux session or been adopted by an active session.
		if !isProcessStillOrphaned(zombie.PID) {
			continue
		}

		if err := syscall.Kill(zombie.PID, syscall.SIGTERM); err != nil {
			if err != syscall.ESRCH {
				lastErr = fmt.Errorf("SIGTERM PID %d: %w", zombie.PID, err)
			}
			continue
		}
		state[zombie.PID] = signalState{Signal: "SIGTERM", Timestamp: now}
		results = append(results, ZombieCleanupResult{
			Process: zombie,
			Signal:  "SIGTERM",
		})
	}

	if err := saveZombieState(state); err != nil {
		if lastErr == nil {
			lastErr = fmt.Errorf("saving zombie state: %w", err)
		}
	}

	return results, lastErr
}

// CleanupOrphanedClaudeProcesses finds and kills orphaned claude/codex processes.
//
// Uses a state machine to escalate signals:
//  1. First encounter → SIGTERM, record in state file
//  2. Next cycle, still alive after grace period → SIGKILL, update state
//  3. Next cycle, still alive after SIGKILL → log as unkillable, remove from state
//
// Returns the list of cleanup results and any error encountered.
func CleanupOrphanedClaudeProcesses() ([]CleanupResult, error) {
	orphans, err := FindOrphanedClaudeProcesses()
	if err != nil {
		return nil, err
	}

	// Load previous state
	state := loadOrphanState()
	now := time.Now()

	var results []CleanupResult
	var lastErr error

	// Track which PIDs we're still working on
	activeOrphans := make(map[int]bool)
	for _, o := range orphans {
		activeOrphans[o.PID] = true
	}

	// First pass: check state for PIDs that died (cleanup) or need escalation
	for pid, s := range state {
		if !activeOrphans[pid] {
			// Process died, remove from state
			delete(state, pid)
			continue
		}

		// Process still alive - check if we need to escalate
		elapsed := now.Sub(s.Timestamp).Seconds()

		if s.Signal == "SIGKILL" {
			// Already sent SIGKILL and it's still alive - unkillable
			results = append(results, CleanupResult{
				Process: OrphanedProcess{PID: pid, Cmd: "claude"},
				Signal:  "UNKILLABLE",
				Error:   fmt.Errorf("process %d survived SIGKILL", pid),
			})
			delete(state, pid) // Remove from tracking, nothing more we can do
			delete(activeOrphans, pid)
			continue
		}

		if s.Signal == "SIGTERM" && elapsed >= float64(sigkillGracePeriod) {
			// Sent SIGTERM but still alive after grace period - escalate to SIGKILL
			if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
				if err != syscall.ESRCH {
					lastErr = fmt.Errorf("SIGKILL PID %d: %w", pid, err)
				}
				delete(state, pid)
				delete(activeOrphans, pid)
				continue
			}
			state[pid] = signalState{Signal: "SIGKILL", Timestamp: now}
			results = append(results, CleanupResult{
				Process: OrphanedProcess{PID: pid, Cmd: "claude"},
				Signal:  "SIGKILL",
			})
			delete(activeOrphans, pid)
		}
		// If SIGTERM was recent, leave it alone - check again next cycle
	}

	// Second pass: send SIGTERM to new orphans not yet in state
	for _, orphan := range orphans {
		if !activeOrphans[orphan.PID] {
			continue // Already handled above
		}
		if _, exists := state[orphan.PID]; exists {
			continue // Already in state, waiting for grace period
		}

		// TOCTOU guard: re-verify this process is still orphaned before signaling.
		// Between FindOrphanedClaudeProcesses() and now, the process may have
		// joined a tmux session or acquired a TTY.
		if !isProcessStillOrphaned(orphan.PID) {
			continue
		}

		// New orphan - send SIGTERM
		if err := syscall.Kill(orphan.PID, syscall.SIGTERM); err != nil {
			if err != syscall.ESRCH {
				lastErr = fmt.Errorf("SIGTERM PID %d: %w", orphan.PID, err)
			}
			continue
		}
		state[orphan.PID] = signalState{Signal: "SIGTERM", Timestamp: now}
		results = append(results, CleanupResult{
			Process: orphan,
			Signal:  "SIGTERM",
		})
	}

	// Save updated state
	if err := saveOrphanState(state); err != nil {
		if lastErr == nil {
			lastErr = fmt.Errorf("saving orphan state: %w", err)
		}
	}

	return results, lastErr
}

// isProcessStillOrphaned re-checks whether a process is still orphaned/zombie.
// Used for TOCTOU re-verification immediately before sending signals.
// Returns true if the process still has no controlling terminal and is not
// in any active tmux session (i.e., still safe to signal).
func isProcessStillOrphaned(pid int) bool {
	// Re-check the process TTY via ps
	out, err := exec.Command("ps", "-o", "tty=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false // Process may have exited - not orphaned anymore
	}

	tty := strings.TrimSpace(string(out))
	if tty == "" {
		return false // Process gone
	}

	// If it now has a real TTY, it's been adopted
	if tty != "?" && tty != "??" {
		return false
	}

	// Re-check against current tmux session PIDs
	protectedPIDs := getTmuxSessionPIDs()
	return !protectedPIDs[pid]
}
