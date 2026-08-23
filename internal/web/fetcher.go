package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/steveyegge/gastown/internal/activity"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

// runCmd executes a command with a timeout and returns stdout.
// Returns empty buffer on timeout or error.
// Security: errors from this function are logged server-side only (via log.Printf
// in callers) and never included in HTTP responses. The handler renders templates
// with whatever data was successfully fetched; fetch failures result in empty panels.
func runCmd(timeout time.Duration, name string, args ...string) (*bytes.Buffer, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("%s timed out after %v", name, timeout)
		}
		return nil, err
	}
	return &stdout, nil
}

// runTmuxCmd runs a tmux command using the per-town socket.
// Without -L, tmux queries the default socket which has no Gas Town sessions.
func (f *LiveConvoyFetcher) runTmuxCmd(args ...string) (*bytes.Buffer, error) {
	fullArgs := []string{}
	if f.tmuxSocket != "" {
		fullArgs = append(fullArgs, "-L", f.tmuxSocket)
	}
	fullArgs = append(fullArgs, args...)
	return fetcherRunCmd(f.tmuxCmdTimeout, "tmux", fullArgs...)
}

var fetcherRunCmd = runCmd
var fetcherGetSessionEnv = func(sessionName, key string) (string, error) {
	return tmux.NewTmux().GetEnvironment(sessionName, key)
}

// runBdCmd executes a bd command with the configured cmdTimeout in the specified beads directory.
func (f *LiveConvoyFetcher) runBdCmd(beadsDir string, args ...string) (*bytes.Buffer, error) {
	// bd v0.59+ requires --flat for list --json to produce JSON output
	args = beads.InjectFlatForListJSON(args)

	ctx, cancel := context.WithTimeout(context.Background(), f.cmdTimeout)
	defer cancel()

	bin := f.bdBin
	if bin == "" {
		bin = "bd"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = beadsDir
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	err := cmd.Run()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("bd timed out after %v", f.cmdTimeout)
		}
		// If we got some output, return it anyway (bd may exit non-zero with warnings)
		if stdout.Len() > 0 {
			return &stdout, nil
		}
		return nil, err
	}
	return &stdout, nil
}

// fetchCircuitBreaker tracks consecutive failures for a fetch operation
// and applies exponential backoff to prevent process storms.
type fetchCircuitBreaker struct {
	mu          sync.Mutex
	failures    int
	lastAttempt time.Time
	backoff     time.Duration
	inFlight    bool
}

// maxBackoff is the maximum backoff duration for the circuit breaker.
const maxBackoff = 5 * time.Minute

// allow returns true if enough time has passed since the last failure to permit
// a new attempt, and reserves that attempt so concurrent callers do not all
// stampede through when backoff opens.
func (cb *fetchCircuitBreaker) allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.inFlight {
		return false
	}
	if cb.failures == 0 {
		cb.inFlight = true
		return true
	}
	if time.Since(cb.lastAttempt) < cb.backoff {
		return false
	}
	cb.inFlight = true
	return true
}

// recordFailure increments the failure count and sets exponential backoff.
// Backoff doubles from 10s up to maxBackoff.
func (cb *fetchCircuitBreaker) recordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.failures++
	cb.lastAttempt = time.Now()
	cb.inFlight = false
	// Exponential backoff: 10s, 20s, 40s, 80s, 160s, capped at maxBackoff
	cb.backoff = time.Duration(1<<min(cb.failures, 10)) * 5 * time.Second
	if cb.backoff > maxBackoff {
		cb.backoff = maxBackoff
	}
}

// recordSuccess resets the circuit breaker on a successful fetch.
func (cb *fetchCircuitBreaker) recordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.failures = 0
	cb.backoff = 0
	cb.inFlight = false
}

// LiveConvoyFetcher fetches convoy data from beads.
type LiveConvoyFetcher struct {
	townRoot  string
	townBeads string

	// bdBin is the bd binary name or path. Defaults to "bd" if empty.
	bdBin string

	// registry is a prefix registry built from the town's rigs.json.
	// Used for parsing tmux session names instead of relying on the
	// package-level DefaultRegistry, which may not be initialized in
	// the dashboard process context.
	registry *session.PrefixRegistry

	// Configurable timeouts (from TownSettings.WebTimeouts)
	cmdTimeout     time.Duration
	ghCmdTimeout   time.Duration
	tmuxCmdTimeout time.Duration

	// Configurable worker status thresholds (from TownSettings.WorkerStatus)
	staleThreshold          time.Duration
	stuckThreshold          time.Duration
	heartbeatFreshThreshold time.Duration
	mayorActiveThreshold    time.Duration

	// tmuxSocket is the per-town tmux socket name (e.g., "dipgt-651c6b").
	// All tmux commands must use -L with this socket; the default socket
	// has no Gas Town sessions.
	tmuxSocket string

	// Circuit breaker for FetchConvoys — prevents process storms when
	// bd list by convoy label fails persistently (e.g., schema mismatch).
	convoyBreaker fetchCircuitBreaker
}

// NewLiveConvoyFetcher creates a fetcher for the current workspace.
// Loads timeout and threshold config from TownSettings; falls back to defaults if missing.
func NewLiveConvoyFetcher() (*LiveConvoyFetcher, error) {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return nil, fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	webCfg := config.DefaultWebTimeoutsConfig()
	workerCfg := config.DefaultWorkerStatusConfig()
	if ts, err := config.LoadOrCreateTownSettings(config.TownSettingsPath(townRoot)); err == nil {
		// Replace entire defaults — individual fields fall back via ParseDurationOrDefault
		// (empty string → hardcoded default). Add explicit zero-value guards for non-duration fields.
		if ts.WebTimeouts != nil {
			webCfg = ts.WebTimeouts
		}
		if ts.WorkerStatus != nil {
			workerCfg = ts.WorkerStatus
		}
	}

	// Build a local prefix registry from the town's rigs.json so session
	// name parsing works regardless of whether the package-level
	// DefaultRegistry was initialized (gt-y24).
	registry, regErr := session.BuildPrefixRegistryFromTown(townRoot)
	if regErr != nil {
		log.Printf("dashboard: failed to build prefix registry: %v (falling back to default)", regErr)
		registry = session.DefaultRegistry()
	}

	return &LiveConvoyFetcher{
		townRoot:                townRoot,
		townBeads:               filepath.Join(townRoot, ".beads"),
		registry:                registry,
		tmuxSocket:              tmux.GetDefaultSocket(),
		cmdTimeout:              config.ParseDurationOrDefault(webCfg.CmdTimeout, 6*time.Second),
		ghCmdTimeout:            config.ParseDurationOrDefault(webCfg.GhCmdTimeout, 5*time.Second),
		tmuxCmdTimeout:          config.ParseDurationOrDefault(webCfg.TmuxCmdTimeout, 2*time.Second),
		staleThreshold:          config.ParseDurationOrDefault(workerCfg.StaleThreshold, 5*time.Minute),
		stuckThreshold:          config.ParseDurationOrDefault(workerCfg.StuckThreshold, constants.GUPPViolationTimeout),
		heartbeatFreshThreshold: config.ParseDurationOrDefault(workerCfg.HeartbeatFreshThreshold, 5*time.Minute),
		mayorActiveThreshold:    config.ParseDurationOrDefault(workerCfg.MayorActiveThreshold, 5*time.Minute),
	}, nil
}

// FetchConvoys fetches all open convoys with their activity data.
// Uses a circuit breaker to avoid hammering bd/dolt when listing fails
// persistently (e.g., "invalid issue type: convoy" schema mismatch).
func (f *LiveConvoyFetcher) FetchConvoys() ([]ConvoyRow, error) {
	if !f.convoyBreaker.allow() {
		return nil, nil // Backed off — return empty result silently
	}

	// List all open issues and filter locally so legacy type=convoy beads remain visible.
	stdout, err := f.runBdCmd(f.townRoot, "list", "--status=open", "--json", "--limit=0")
	if err != nil {
		f.convoyBreaker.recordFailure()
		return nil, fmt.Errorf("listing convoys: %w", err)
	}

	var convoys []struct {
		ID        string   `json:"id"`
		Title     string   `json:"title"`
		Status    string   `json:"status"`
		CreatedAt string   `json:"created_at"`
		IssueType string   `json:"issue_type"`
		Labels    []string `json:"labels"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &convoys); err != nil {
		f.convoyBreaker.recordFailure()
		return nil, fmt.Errorf("parsing convoy list: %w", err)
	}

	// Build convoy rows with activity data
	rows := make([]ConvoyRow, 0, len(convoys))
	for _, c := range convoys {
		if c.IssueType != "convoy" && !webConvoyHasLabel(c.Labels, "gt:convoy") {
			continue
		}
		row := ConvoyRow{
			ID:     c.ID,
			Title:  c.Title,
			Status: c.Status,
		}

		// Get tracked issues for progress and activity calculation
		tracked, err := f.getTrackedIssues(c.ID)
		if err != nil {
			log.Printf("warning: skipping convoy %s: %v", c.ID, err)
			continue
		}
		row.Total = len(tracked)

		var mostRecentActivity time.Time
		var mostRecentUpdated time.Time
		var hasAssignee bool
		assigneeSet := make(map[string]struct{})
		for _, t := range tracked {
			if t.Status == "closed" {
				row.Completed++
			} else if t.Assignee != "" {
				row.InProgress++
			} else {
				row.ReadyBeads++
			}
			// Track most recent activity from workers
			if t.LastActivity.After(mostRecentActivity) {
				mostRecentActivity = t.LastActivity
			}
			// Track most recent updated_at as fallback
			if t.UpdatedAt.After(mostRecentUpdated) {
				mostRecentUpdated = t.UpdatedAt
			}
			if t.Assignee != "" {
				hasAssignee = true
				assigneeSet[t.Assignee] = struct{}{}
			}
		}

		// Collect unique assignees (sorted for stable display order)
		row.Assignees = make([]string, 0, len(assigneeSet))
		for a := range assigneeSet {
			row.Assignees = append(row.Assignees, a)
		}
		sort.Strings(row.Assignees)

		row.Progress = fmt.Sprintf("%d/%d", row.Completed, row.Total)
		if row.Total > 0 {
			row.ProgressPct = (row.Completed * 100) / row.Total
		}

		// Calculate activity info from most recent worker activity
		if !mostRecentActivity.IsZero() {
			// Have active tmux session activity from assigned workers
			row.LastActivity = activity.Calculate(mostRecentActivity)
		} else if !hasAssignee {
			// No assignees found in beads - try fallback to any running polecat activity
			// This handles cases where bd update --assignee didn't persist or wasn't returned
			if polecatActivity := f.getAllPolecatActivity(); polecatActivity != nil {
				info := activity.Calculate(*polecatActivity)
				info.FormattedAge = info.FormattedAge + " (polecat active)"
				row.LastActivity = info
			} else if !mostRecentUpdated.IsZero() {
				// Fall back to issue updated_at if no polecats running
				info := activity.Calculate(mostRecentUpdated)
				info.FormattedAge = info.FormattedAge + " (unassigned)"
				row.LastActivity = info
			} else {
				row.LastActivity = activity.Info{
					FormattedAge: "unassigned",
					ColorClass:   activity.ColorUnknown,
				}
			}
		} else {
			// Has assignee but no active session
			row.LastActivity = activity.Info{
				FormattedAge: "idle",
				ColorClass:   activity.ColorUnknown,
			}
		}

		// Calculate work status based on progress and activity
		row.WorkStatus = calculateWorkStatus(row.Completed, row.Total, row.LastActivity.ColorClass)

		// Get tracked issues for expandable view
		row.TrackedIssues = make([]TrackedIssue, len(tracked))
		for i, t := range tracked {
			row.TrackedIssues[i] = TrackedIssue{
				ID:       t.ID,
				Title:    t.Title,
				Status:   t.Status,
				Assignee: t.Assignee,
			}
		}

		rows = append(rows, row)
	}

	f.convoyBreaker.recordSuccess()
	return rows, nil
}

func webConvoyHasLabel(labels []string, target string) bool {
	for _, label := range labels {
		if label == target {
			return true
		}
	}
	return false
}

// trackedIssueInfo holds info about an issue being tracked by a convoy.
type trackedIssueInfo struct {
	ID           string
	Title        string
	Status       string
	Assignee     string
	LastActivity time.Time
	UpdatedAt    time.Time // Fallback for activity when no assignee
}

// getTrackedIssues fetches tracked issues for a convoy.
func (f *LiveConvoyFetcher) getTrackedIssues(convoyID string) ([]trackedIssueInfo, error) {
	// Query tracked dependencies using bd dep list
	stdout, err := f.runBdCmd(f.townRoot, "dep", "list", convoyID, "-t", "tracks", "--json")
	if err != nil {
		return nil, fmt.Errorf("querying tracked issues for %s: %w", convoyID, err)
	}

	var deps []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &deps); err != nil {
		return nil, fmt.Errorf("parsing tracked issues for %s: %w", convoyID, err)
	}

	// Collect resolved issue IDs, unwrapping external:prefix:id format
	issueIDs := make([]string, 0, len(deps))
	for _, dep := range deps {
		issueIDs = append(issueIDs, beads.ExtractIssueID(dep.ID))
	}

	// Batch fetch issue details
	details, err := f.getIssueDetailsBatch(issueIDs)
	if err != nil {
		return nil, fmt.Errorf("fetching tracked issue details for %s: %w", convoyID, err)
	}

	// Get worker activity from tmux sessions based on assignees
	workers := f.getWorkersFromAssignees(details)

	// Build result
	result := make([]trackedIssueInfo, 0, len(issueIDs))
	for _, id := range issueIDs {
		info := trackedIssueInfo{ID: id}

		if d, ok := details[id]; ok {
			info.Title = d.Title
			info.Status = d.Status
			info.Assignee = d.Assignee
			info.UpdatedAt = d.UpdatedAt
		} else {
			info.Title = "(external)"
			info.Status = "unknown"
		}

		if w, ok := workers[id]; ok && w.LastActivity != nil {
			info.LastActivity = *w.LastActivity
		}

		result = append(result, info)
	}

	return result, nil
}

// issueDetail holds basic issue info.
type issueDetail struct {
	ID        string
	Title     string
	Status    string
	Assignee  string
	UpdatedAt time.Time
}

// getIssueDetailsBatch fetches details for multiple issues.
func (f *LiveConvoyFetcher) getIssueDetailsBatch(issueIDs []string) (map[string]*issueDetail, error) {
	result := make(map[string]*issueDetail)
	if len(issueIDs) == 0 {
		return result, nil
	}

	args := append([]string{"show"}, issueIDs...)
	args = append(args, "--json")

	stdout, err := fetcherRunCmd(f.cmdTimeout, "bd", args...)
	if err != nil {
		return nil, fmt.Errorf("bd show failed (issue_count=%d): %w", len(issueIDs), err)
	}

	var issues []struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Status    string `json:"status"`
		Assignee  string `json:"assignee"`
		UpdatedAt string `json:"updated_at"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &issues); err != nil {
		return nil, fmt.Errorf("bd show returned invalid JSON (issue_count=%d): %w", len(issueIDs), err)
	}

	for _, issue := range issues {
		detail := &issueDetail{
			ID:       issue.ID,
			Title:    issue.Title,
			Status:   issue.Status,
			Assignee: issue.Assignee,
		}
		// Parse updated_at timestamp
		if issue.UpdatedAt != "" {
			if t, err := time.Parse(time.RFC3339, issue.UpdatedAt); err == nil {
				detail.UpdatedAt = t
			}
		}
		result[issue.ID] = detail
	}

	return result, nil
}

// workerDetail holds worker info including last activity.
type workerDetail struct {
	Worker       string
	LastActivity *time.Time
}

// getWorkersFromAssignees gets worker activity from tmux sessions based on issue assignees.
// Assignees are in format "rigname/polecats/polecatname" which maps to tmux session "gt-rigname-polecatname".
func (f *LiveConvoyFetcher) getWorkersFromAssignees(details map[string]*issueDetail) map[string]*workerDetail {
	result := make(map[string]*workerDetail)

	// Collect unique assignees and map them to issue IDs
	assigneeToIssues := make(map[string][]string)
	for issueID, detail := range details {
		if detail == nil || detail.Assignee == "" {
			continue
		}
		assigneeToIssues[detail.Assignee] = append(assigneeToIssues[detail.Assignee], issueID)
	}

	if len(assigneeToIssues) == 0 {
		return result
	}

	// For each unique assignee, look up tmux session activity
	for assignee, issueIDs := range assigneeToIssues {
		activity := f.getSessionActivityForAssignee(assignee)
		if activity == nil {
			continue
		}

		// Apply this activity to all issues assigned to this worker
		for _, issueID := range issueIDs {
			result[issueID] = &workerDetail{
				Worker:       assignee,
				LastActivity: activity,
			}
		}
	}

	return result
}

// getSessionActivityForAssignee looks up tmux session activity for an assignee.
// Assignee format: "rigname/polecats/polecatname" -> session "gt-rigname-polecatname"
func (f *LiveConvoyFetcher) getSessionActivityForAssignee(assignee string) *time.Time {
	// Parse assignee: "roxas/polecats/dag" -> rig="roxas", polecat="dag"
	parts := strings.Split(assignee, "/")
	if len(parts) != 3 || parts[1] != "polecats" {
		return nil
	}
	rig := parts[0]
	polecat := parts[2]

	// Construct session name
	sessionName := session.PolecatSessionName(session.PrefixFor(rig), polecat)

	// Query tmux for session activity
	// Format: session_activity returns unix timestamp
	stdout, err := f.runTmuxCmd("list-sessions", "-F", "#{session_name}|#{session_activity}",
		"-f", fmt.Sprintf("#{==:#{session_name},%s}", sessionName))
	if err != nil {
		return nil
	}

	output := strings.TrimSpace(stdout.String())
	if output == "" {
		return nil
	}

	// Parse output: "gt-roxas-dag|1704312345"
	outputParts := strings.Split(output, "|")
	if len(outputParts) < 2 {
		return nil
	}

	var activityUnix int64
	if _, err := fmt.Sscanf(outputParts[1], "%d", &activityUnix); err != nil || activityUnix == 0 {
		return nil
	}

	activity := time.Unix(activityUnix, 0)
	return &activity
}

// getAllPolecatActivity returns the most recent activity from any running polecat session.
// This is used as a fallback when no specific assignee activity can be determined.
// Returns nil if no polecat sessions are running.
func (f *LiveConvoyFetcher) getAllPolecatActivity() *time.Time {
	// List all tmux sessions matching gt-*-* pattern (polecat sessions)
	// Format: gt-{rig}-{polecat}
	stdout, err := f.runTmuxCmd("list-sessions", "-F", "#{session_name}|#{session_activity}")
	if err != nil {
		return nil
	}

	var mostRecent time.Time
	for _, line := range strings.Split(stdout.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.Split(line, "|")
		if len(parts) < 2 {
			continue
		}

		sessionName := parts[0]
		// Check if it's a polecat or crew session (skip infrastructure roles).
		// Use the fetcher's own registry to avoid dependency on global
		// DefaultRegistry initialization (gt-y24).
		identity, err := session.ParseSessionNameWithRegistry(sessionName, f.registry)
		if err != nil {
			continue
		}
		if identity.Role != session.RolePolecat && identity.Role != session.RoleCrew {
			continue
		}

		var activityUnix int64
		if _, err := fmt.Sscanf(parts[1], "%d", &activityUnix); err != nil || activityUnix == 0 {
			continue
		}

		activityTime := time.Unix(activityUnix, 0)
		if activityTime.After(mostRecent) {
			mostRecent = activityTime
		}
	}

	if mostRecent.IsZero() {
		return nil
	}
	return &mostRecent
}

// calculateWorkStatus determines the work status based on progress and activity.
// Returns: "complete", "active", "stale", "stuck", or "waiting"
func calculateWorkStatus(completed, total int, activityColor string) string {
	// Check if all work is done
	if total > 0 && completed == total {
		return "complete"
	}

	// Determine status based on activity color
	switch activityColor {
	case activity.ColorGreen:
		return "active"
	case activity.ColorYellow:
		return "stale"
	case activity.ColorRed:
		return "stuck"
	default:
		return "waiting"
	}
}

// FetchMergeQueue fetches open PRs from registered rigs, one goroutine per
// rig via errgroup. Serial `gh pr list` calls dominated dashboard render
// time (~9.9s across 10 rigs); parallelizing brings this down to roughly
// the slowest single rig's fetch time.
func (f *LiveConvoyFetcher) FetchMergeQueue() ([]MergeQueueRow, error) {
	// Load registered rigs from config
	rigsConfigPath := filepath.Join(f.townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		return nil, fmt.Errorf("loading rigs config: %w", err)
	}

	rigResults := make([][]MergeQueueRow, len(rigsConfig.Rigs))
	rigNames := make([]string, 0, len(rigsConfig.Rigs))
	for rigName := range rigsConfig.Rigs {
		rigNames = append(rigNames, rigName)
	}
	// Deterministic ordering keeps output stable across ticks so
	// computeDashboardHash sees identical output for identical state.
	sort.Strings(rigNames)

	g := new(errgroup.Group)
	for i, rigName := range rigNames {
		i, rigName := i, rigName
		entry := rigsConfig.Rigs[rigName]
		g.Go(func() error {
			repoPath := gitURLToRepoPath(entry.GitURL)
			if repoPath == "" {
				return nil
			}
			prs, err := f.fetchPRsForRepo(repoPath, rigName)
			if err != nil {
				// Non-fatal: continue with other repos
				return nil
			}
			rigResults[i] = prs
			return nil
		})
	}
	// errgroup.Group.Go never returns a non-nil error above, so Wait cannot fail.
	_ = g.Wait()

	var result []MergeQueueRow
	for _, prs := range rigResults {
		result = append(result, prs...)
	}

	return result, nil
}

// gitURLToRepoPath converts a git URL to owner/repo format.
// Supports HTTPS (https://github.com/owner/repo.git) and
// SSH (git@github.com:owner/repo.git) formats.
func gitURLToRepoPath(gitURL string) string {
	// Handle HTTPS format: https://github.com/owner/repo.git
	if strings.HasPrefix(gitURL, "https://github.com/") {
		path := strings.TrimPrefix(gitURL, "https://github.com/")
		path = strings.TrimSuffix(path, ".git")
		return path
	}

	// Handle SSH format: git@github.com:owner/repo.git
	if strings.HasPrefix(gitURL, "git@github.com:") {
		path := strings.TrimPrefix(gitURL, "git@github.com:")
		path = strings.TrimSuffix(path, ".git")
		return path
	}

	// Unsupported format
	return ""
}

// prResponse represents the JSON response from gh pr list.
type prResponse struct {
	Number            int    `json:"number"`
	Title             string `json:"title"`
	URL               string `json:"url"`
	Mergeable         string `json:"mergeable"`
	StatusCheckRollup []struct {
		State      string `json:"state"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
	} `json:"statusCheckRollup"`
}

// fetchPRsForRepo fetches open PRs for a single repo.
func (f *LiveConvoyFetcher) fetchPRsForRepo(repoFull, repoShort string) ([]MergeQueueRow, error) {
	stdout, err := runCmd(f.ghCmdTimeout, "gh", "pr", "list",
		"--repo", repoFull,
		"--state", "open",
		"--json", "number,title,url,mergeable,statusCheckRollup")
	if err != nil {
		return nil, fmt.Errorf("fetching PRs for %s: %w", repoFull, err)
	}

	var prs []prResponse
	if err := json.Unmarshal(stdout.Bytes(), &prs); err != nil {
		return nil, fmt.Errorf("parsing PRs for %s: %w", repoFull, err)
	}

	result := make([]MergeQueueRow, 0, len(prs))
	for _, pr := range prs {
		row := MergeQueueRow{
			Number: pr.Number,
			Repo:   repoShort,
			Title:  pr.Title,
			URL:    pr.URL,
		}

		// Determine CI status from statusCheckRollup
		row.CIStatus = determineCIStatus(pr.StatusCheckRollup)

		// Determine mergeable status
		row.Mergeable = determineMergeableStatus(pr.Mergeable)

		// Determine color class based on overall status
		row.ColorClass = determineColorClass(row.CIStatus, row.Mergeable)

		result = append(result, row)
	}

	return result, nil
}

// determineCIStatus evaluates the overall CI status from status checks.
func determineCIStatus(checks []struct {
	State      string `json:"state"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}) string {
	if len(checks) == 0 {
		return "pending"
	}

	hasFailure := false
	hasPending := false

	for _, check := range checks {
		// Check conclusion first (for completed checks)
		switch check.Conclusion {
		case "failure", "cancelled", "timed_out", "action_required": //nolint:misspell // GitHub API returns "cancelled" (British spelling)
			hasFailure = true
		case "success", "skipped", "neutral":
			// Pass
		default:
			// Check status for in-progress checks
			switch check.Status {
			case "queued", "in_progress", "waiting", "pending", "requested":
				hasPending = true
			}
			// Also check state field
			switch check.State {
			case "FAILURE", "ERROR":
				hasFailure = true
			case "PENDING", "EXPECTED":
				hasPending = true
			}
		}
	}

	if hasFailure {
		return "fail"
	}
	if hasPending {
		return "pending"
	}
	return "pass"
}

// determineMergeableStatus converts GitHub's mergeable field to display value.
func determineMergeableStatus(mergeable string) string {
	switch strings.ToUpper(mergeable) {
	case "MERGEABLE":
		return "ready"
	case "CONFLICTING":
		return "conflict"
	default:
		return "pending"
	}
}

// determineColorClass determines the row color based on CI and merge status.
func determineColorClass(ciStatus, mergeable string) string {
	if ciStatus == "fail" || mergeable == "conflict" {
		return "mq-red"
	}
	if ciStatus == "pending" || mergeable == "pending" {
		return "mq-yellow"
	}
	if ciStatus == "pass" && mergeable == "ready" {
		return "mq-green"
	}
	return "mq-yellow"
}

// FetchWorkers fetches all running worker sessions (polecats and refinery) with activity data.
func (f *LiveConvoyFetcher) FetchWorkers() ([]WorkerRow, error) {
	// Load registered rigs to filter sessions
	rigsConfigPath := filepath.Join(f.townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		return nil, fmt.Errorf("loading rigs config: %w", err)
	}

	// Build set of registered rig names
	registeredRigs := make(map[string]bool)
	for rigName := range rigsConfig.Rigs {
		registeredRigs[rigName] = true
	}

	// Pre-fetch assigned issues map: assignee -> (issueID, title)
	assignedIssues := f.getAssignedIssuesMap()

	// Query all tmux sessions with window_activity for more accurate timing
	stdout, err := f.runTmuxCmd("list-sessions", "-F", "#{session_name}|#{window_activity}")
	if err != nil {
		// tmux not running or no sessions
		return nil, nil
	}

	// Pre-fetch merge queue count to determine refinery idle status
	mergeQueueCount := f.getMergeQueueCount()

	var workers []WorkerRow
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")

	for _, line := range lines {
		if line == "" {
			continue
		}

		parts := strings.Split(line, "|")
		if len(parts) < 2 {
			continue
		}

		sessionName := parts[0]

		// Parse session name using the fetcher's own registry to avoid
		// dependency on global DefaultRegistry initialization (gt-y24).
		identity, err := session.ParseSessionNameWithRegistry(sessionName, f.registry)
		if err != nil {
			log.Printf("dashboard: FetchWorkers: skipping session %q: %v", sessionName, err)
			continue
		}

		rig := identity.Rig

		// Skip rigs not registered in this workspace
		if !registeredRigs[rig] {
			continue
		}

		// Skip non-worker sessions (witness, mayor, deacon, boot)
		switch identity.Role {
		case session.RoleMayor, session.RoleDeacon, session.RoleWitness:
			continue
		}

		// Determine agent type and worker name
		workerName := identity.Name
		agentType := constants.RolePolecat // Default for ephemeral sessions (polecats, crew)
		if identity.Role == session.RoleRefinery {
			agentType = constants.RoleRefinery
		}

		// Parse activity timestamp
		var activityUnix int64
		if _, err := fmt.Sscanf(parts[1], "%d", &activityUnix); err != nil || activityUnix == 0 {
			continue
		}
		activityTime := time.Unix(activityUnix, 0)
		activityAge := time.Since(activityTime)

		// Get status hint - special handling for refinery
		var statusHint string
		if workerName == "refinery" {
			statusHint = f.getRefineryStatusHint(mergeQueueCount)
		} else {
			statusHint = f.getWorkerStatusHint(sessionName)
		}

		// Look up assigned issue for this worker
		// Assignee format: "rigname/polecats/workername"
		assignee := fmt.Sprintf("%s/polecats/%s", rig, workerName)
		var issueID, issueTitle string
		if issue, ok := assignedIssues[assignee]; ok {
			issueID = issue.ID
			issueTitle = issue.Title
			// Keep full title - CSS handles overflow
		}

		// Calculate work status based on activity age and issue assignment
		workStatus := calculateWorkerWorkStatus(activityAge, issueID, workerName, f.staleThreshold, f.stuckThreshold)

		workers = append(workers, WorkerRow{
			Name:         workerName,
			Rig:          rig,
			SessionID:    sessionName,
			LastActivity: activity.Calculate(activityTime),
			StatusHint:   statusHint,
			IssueID:      issueID,
			IssueTitle:   issueTitle,
			WorkStatus:   workStatus,
			AgentType:    agentType,
		})
	}

	return workers, nil
}

// assignedIssue holds issue info for the assigned issues map.
type assignedIssue struct {
	ID    string
	Title string
}

// getAssignedIssuesMap returns a map of assignee -> assigned issue.
// Queries beads for all in_progress issues with assignees.
func (f *LiveConvoyFetcher) getAssignedIssuesMap() map[string]assignedIssue {
	result := make(map[string]assignedIssue)

	// Query all in_progress issues (these are the ones being worked on)
	stdout, err := f.runBdCmd(f.townRoot, "list", "--status=in_progress", "--json")
	if err != nil {
		log.Printf("warning: bd list in_progress failed: %v", err)
		return result
	}

	var issues []struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		Assignee string `json:"assignee"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &issues); err != nil {
		log.Printf("warning: parsing bd list output: %v", err)
		return result
	}

	for _, issue := range issues {
		if issue.Assignee != "" {
			result[issue.Assignee] = assignedIssue{
				ID:    issue.ID,
				Title: issue.Title,
			}
		}
	}

	return result
}

// calculateWorkerWorkStatus determines the worker's work status based on activity and assignment.
// Returns: "working", "stale", "stuck", or "idle"
func calculateWorkerWorkStatus(activityAge time.Duration, issueID, workerName string, staleThreshold, stuckThreshold time.Duration) string {
	// Refinery has special handling - it's always "working" if it has PRs
	if workerName == "refinery" {
		return "working"
	}

	// No issue assigned = idle
	if issueID == "" {
		return "idle"
	}

	// Has issue - determine status based on activity
	switch {
	case activityAge < staleThreshold:
		return "working" // Active recently
	case activityAge < stuckThreshold:
		return "stale" // Might be thinking or stuck
	default:
		return "stuck" // Likely stuck - no activity for threshold+ minutes
	}
}

// getWorkerStatusHint captures the last non-empty line from a worker's pane.
func (f *LiveConvoyFetcher) getWorkerStatusHint(sessionName string) string {
	stdout, err := f.runTmuxCmd("capture-pane", "-t", sessionName, "-p", "-J")
	if err != nil {
		return ""
	}

	// Get last non-empty line
	lines := strings.Split(stdout.String(), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			// Truncate long lines
			if len(line) > 60 {
				line = line[:57] + "..."
			}
			return line
		}
	}
	return ""
}

// getMergeQueueCount returns the total number of open PRs across all repos.
func (f *LiveConvoyFetcher) getMergeQueueCount() int {
	mergeQueue, err := f.FetchMergeQueue()
	if err != nil {
		return 0
	}
	return len(mergeQueue)
}

// getRefineryStatusHint returns appropriate status for refinery based on merge queue.
func (f *LiveConvoyFetcher) getRefineryStatusHint(mergeQueueCount int) string {
	if mergeQueueCount == 0 {
		return "Idle - Waiting for PRs"
	}
	if mergeQueueCount == 1 {
		return "Processing 1 PR"
	}
	return fmt.Sprintf("Processing %d PRs", mergeQueueCount)
}

// parseActivityTimestamp parses a Unix timestamp string from tmux.
// Returns (0, false) for invalid or zero timestamps.
func parseActivityTimestamp(s string) (int64, bool) {
	var unix int64
	if _, err := fmt.Sscanf(s, "%d", &unix); err != nil || unix <= 0 {
		return 0, false
	}
	return unix, true
}

// FetchMail fetches recent mail messages from the beads database.
func (f *LiveConvoyFetcher) FetchMail() ([]MailRow, error) {
	// List all message issues (mail)
	stdout, err := f.runBdCmd(f.townRoot, "list", "--label=gt:message", "--json", "--limit=50")
	if err != nil {
		return nil, fmt.Errorf("listing mail: %w", err)
	}

	var messages []struct {
		ID        string   `json:"id"`
		Title     string   `json:"title"`
		Status    string   `json:"status"`
		CreatedAt string   `json:"created_at"`
		Priority  int      `json:"priority"`
		Assignee  string   `json:"assignee"`   // "to" address stored here
		CreatedBy string   `json:"created_by"` // "from" address
		Labels    []string `json:"labels"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &messages); err != nil {
		return nil, fmt.Errorf("parsing mail list: %w", err)
	}

	rows := make([]MailRow, 0, len(messages))
	for _, m := range messages {
		// Parse timestamp
		var timestamp time.Time
		var age string
		var sortKey int64
		if m.CreatedAt != "" {
			if t, err := time.Parse(time.RFC3339, m.CreatedAt); err == nil {
				timestamp = t
				age = formatTimestamp(t)
				sortKey = t.Unix()
			}
		}

		// Determine priority string
		priorityStr := "normal"
		switch m.Priority {
		case 0:
			priorityStr = "urgent"
		case 1:
			priorityStr = "high"
		case 2:
			priorityStr = "normal"
		case 3, 4:
			priorityStr = "low"
		}

		// Determine message type from labels
		msgType := "notification"
		for _, label := range m.Labels {
			if label == "task" || label == "reply" || label == "scavenge" {
				msgType = label
				break
			}
		}

		// Format from/to addresses for display
		from := formatAgentAddress(m.CreatedBy)
		to := formatAgentAddress(m.Assignee)

		rows = append(rows, MailRow{
			ID:        m.ID,
			From:      from,
			FromRaw:   m.CreatedBy,
			To:        to,
			Subject:   m.Title,
			Timestamp: timestamp.Format("15:04"),
			Age:       age,
			Priority:  priorityStr,
			Type:      msgType,
			Read:      m.Status == "closed",
			SortKey:   sortKey,
		})
	}

	// Sort by timestamp, newest first
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].SortKey > rows[j].SortKey
	})

	return rows, nil
}

// formatMailAge returns a human-readable age string.
func formatMailAge(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// formatTimestamp formats a time as "Jan 26, 3:45 PM" (or "Jan 26 2006, 3:45 PM" if different year).
func formatTimestamp(t time.Time) string {
	now := time.Now()
	if t.Year() != now.Year() {
		return t.Format("Jan 2 2006, 3:04 PM")
	}
	return t.Format("Jan 2, 3:04 PM")
}

// formatAgentAddress shortens agent addresses for display.
// "gastown/polecats/Toast" -> "Toast (gastown)"
// "mayor/" -> "Mayor"
func formatAgentAddress(addr string) string {
	if addr == "" {
		return "—"
	}
	if addr == "mayor/" || addr == "mayor" {
		return "Mayor"
	}

	parts := strings.Split(addr, "/")
	if len(parts) >= 3 && parts[1] == "polecats" {
		return fmt.Sprintf("%s (%s)", parts[2], parts[0])
	}
	if len(parts) >= 3 && parts[1] == "crew" {
		return fmt.Sprintf("%s (%s/crew)", parts[2], parts[0])
	}
	if len(parts) >= 2 {
		return fmt.Sprintf("%s/%s", parts[0], parts[len(parts)-1])
	}
	return addr
}

// FetchRigs returns all registered rigs with their agent counts.
func (f *LiveConvoyFetcher) FetchRigs() ([]RigRow, error) {
	// Load rigs config from mayor/rigs.json
	rigsConfigPath := filepath.Join(f.townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		return nil, fmt.Errorf("loading rigs config: %w", err)
	}

	var rows []RigRow
	for name, entry := range rigsConfig.Rigs {
		row := RigRow{
			Name:   name,
			GitURL: entry.GitURL,
		}

		rigPath := filepath.Join(f.townRoot, name)

		// Count polecats
		polecatsDir := filepath.Join(rigPath, "polecats")
		if entries, err := os.ReadDir(polecatsDir); err == nil {
			for _, e := range entries {
				if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
					row.PolecatCount++
				}
			}
		}

		// Count crew
		crewDir := filepath.Join(rigPath, "crew")
		if entries, err := os.ReadDir(crewDir); err == nil {
			for _, e := range entries {
				if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
					row.CrewCount++
				}
			}
		}

		// Check for witness
		witnessPath := filepath.Join(rigPath, "witness")
		if _, err := os.Stat(witnessPath); err == nil {
			row.HasWitness = true
		}

		// Check for refinery
		refineryPath := filepath.Join(rigPath, "refinery", "rig")
		if _, err := os.Stat(refineryPath); err == nil {
			row.HasRefinery = true
		}

		rows = append(rows, row)
	}

	// Sort by name
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].Name < rows[j].Name
	})

	return rows, nil
}

// FetchDogs returns all dogs in the kennel with their state.
func (f *LiveConvoyFetcher) FetchDogs() ([]DogRow, error) {
	kennelPath := filepath.Join(f.townRoot, "deacon", "dogs")

	entries, err := os.ReadDir(kennelPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No kennel yet
		}
		return nil, fmt.Errorf("reading kennel: %w", err)
	}

	var rows []DogRow
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}

		// Read dog state file
		stateFile := filepath.Join(kennelPath, name, ".dog.json")
		data, err := os.ReadFile(stateFile)
		if err != nil {
			continue // Not a valid dog
		}

		var state struct {
			Name       string            `json:"name"`
			State      string            `json:"state"`
			LastActive time.Time         `json:"last_active"`
			Work       string            `json:"work,omitempty"`
			Worktrees  map[string]string `json:"worktrees,omitempty"`
		}
		if err := json.Unmarshal(data, &state); err != nil {
			continue
		}

		rows = append(rows, DogRow{
			Name:       state.Name,
			State:      state.State,
			Work:       state.Work,
			LastActive: formatTimestamp(state.LastActive),
			RigCount:   len(state.Worktrees),
		})
	}

	// Sort by name
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].Name < rows[j].Name
	})

	return rows, nil
}

// FetchEscalations returns open escalations needing attention.
func (f *LiveConvoyFetcher) FetchEscalations() ([]EscalationRow, error) {
	// List open escalations
	stdout, err := f.runBdCmd(f.townRoot, "list", "--label=gt:escalation", "--status=open", "--json")
	if err != nil {
		return nil, nil // No escalations or bd not available
	}

	var issues []struct {
		ID          string   `json:"id"`
		Title       string   `json:"title"`
		CreatedAt   string   `json:"created_at"`
		CreatedBy   string   `json:"created_by"`
		Labels      []string `json:"labels"`
		Description string   `json:"description"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &issues); err != nil {
		return nil, fmt.Errorf("parsing escalations: %w", err)
	}

	var rows []EscalationRow
	for _, issue := range issues {
		row := EscalationRow{
			ID:          issue.ID,
			Title:       issue.Title,
			EscalatedBy: formatAgentAddress(issue.CreatedBy),
			Severity:    "medium", // default
		}

		// Parse severity from labels
		for _, label := range issue.Labels {
			if strings.HasPrefix(label, "severity:") {
				row.Severity = strings.TrimPrefix(label, "severity:")
			}
			if label == "acked" {
				row.Acked = true
			}
		}

		// Calculate age
		if issue.CreatedAt != "" {
			if t, err := time.Parse(time.RFC3339, issue.CreatedAt); err == nil {
				row.Age = formatTimestamp(t)
			}
		}

		rows = append(rows, row)
	}

	// Sort by severity (critical first), then by age
	severityOrder := map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3}
	sort.Slice(rows, func(i, j int) bool {
		si, sj := severityOrder[rows[i].Severity], severityOrder[rows[j].Severity]
		return si < sj
	})

	return rows, nil
}

// FetchHealth returns system health status.
func (f *LiveConvoyFetcher) FetchHealth() (*HealthRow, error) {
	row := &HealthRow{}

	// Read deacon heartbeat
	heartbeatFile := filepath.Join(f.townRoot, "deacon", "heartbeat.json")
	if data, err := os.ReadFile(heartbeatFile); err == nil {
		var hb struct {
			LastHeartbeat   time.Time `json:"timestamp"`
			Cycle           int64     `json:"cycle"`
			HealthyAgents   int       `json:"healthy_agents"`
			UnhealthyAgents int       `json:"unhealthy_agents"`
		}
		if err := json.Unmarshal(data, &hb); err == nil {
			row.DeaconCycle = hb.Cycle
			row.HealthyAgents = hb.HealthyAgents
			row.UnhealthyAgents = hb.UnhealthyAgents
			if !hb.LastHeartbeat.IsZero() {
				age := time.Since(hb.LastHeartbeat)
				row.DeaconHeartbeat = formatTimestamp(hb.LastHeartbeat)
				row.HeartbeatFresh = age < f.heartbeatFreshThreshold
			} else {
				row.DeaconHeartbeat = "no timestamp"
			}
		}
	} else {
		row.DeaconHeartbeat = "no heartbeat"
	}

	// Check pause state
	pauseFile := filepath.Join(f.townRoot, ".runtime", "deacon", "paused.json")
	if data, err := os.ReadFile(pauseFile); err == nil {
		var pause struct {
			Paused bool   `json:"paused"`
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(data, &pause); err == nil {
			row.IsPaused = pause.Paused
			row.PauseReason = pause.Reason
		}
	}

	return row, nil
}

// FetchQueues returns work queues and their status.
func (f *LiveConvoyFetcher) FetchQueues() ([]QueueRow, error) {
	// List queue beads
	stdout, err := f.runBdCmd(f.townRoot, "list", "--label=gt:queue", "--json")
	if err != nil {
		return nil, nil // No queues or bd not available
	}

	var queues []struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		Status      string `json:"status"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &queues); err != nil {
		return nil, fmt.Errorf("parsing queues: %w", err)
	}

	var rows []QueueRow
	for _, q := range queues {
		row := QueueRow{
			Name:   q.Title,
			Status: q.Status,
		}

		// Parse counts from description (key: value format)
		// Best-effort parsing - ignore Sscanf errors as missing/malformed data is acceptable
		for _, line := range strings.Split(q.Description, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "available_count:") {
				_, _ = fmt.Sscanf(line, "available_count: %d", &row.Available)
			} else if strings.HasPrefix(line, "processing_count:") {
				_, _ = fmt.Sscanf(line, "processing_count: %d", &row.Processing)
			} else if strings.HasPrefix(line, "completed_count:") {
				_, _ = fmt.Sscanf(line, "completed_count: %d", &row.Completed)
			} else if strings.HasPrefix(line, "failed_count:") {
				_, _ = fmt.Sscanf(line, "failed_count: %d", &row.Failed)
			} else if strings.HasPrefix(line, "status:") {
				// Override with parsed status if present
				var s string
				_, _ = fmt.Sscanf(line, "status: %s", &s)
				if s != "" {
					row.Status = s
				}
			}
		}

		rows = append(rows, row)
	}

	return rows, nil
}

// FetchSessions returns active tmux sessions with role detection.
func (f *LiveConvoyFetcher) FetchSessions() ([]SessionRow, error) {
	// List tmux sessions
	stdout, err := f.runTmuxCmd("list-sessions", "-F", "#{session_name}:#{session_activity}")
	if err != nil {
		return nil, nil // tmux not running or no sessions
	}

	var rows []SessionRow
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		if line == "" {
			continue
		}

		// SplitN always returns >= 1 element; parts[0] is safe unconditionally
		parts := strings.SplitN(line, ":", 2)
		name := parts[0]

		// Only include Gas Town sessions
		if !session.IsKnownSession(name) {
			continue
		}

		row := SessionRow{
			Name:    name,
			IsAlive: true, // Session exists
		}

		// Parse activity timestamp
		if len(parts) > 1 {
			if ts, ok := parseActivityTimestamp(parts[1]); ok && ts > 0 {
				row.Activity = formatTimestamp(time.Unix(ts, 0))
			}
		}

		// Detect role from session name using fetcher's own registry (gt-y24)
		if identity, err := session.ParseSessionNameWithRegistry(name, f.registry); err == nil {
			row.Rig = identity.Rig
			row.Role = string(identity.Role)
			row.Worker = identity.Name
		}

		rows = append(rows, row)
	}

	// Sort by rig, then role, then worker
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Rig != rows[j].Rig {
			return rows[i].Rig < rows[j].Rig
		}
		if rows[i].Role != rows[j].Role {
			return rows[i].Role < rows[j].Role
		}
		return rows[i].Worker < rows[j].Worker
	})

	return rows, nil
}

// FetchHooks returns all hooked beads (work pinned to agents).
func (f *LiveConvoyFetcher) FetchHooks() ([]HookRow, error) {
	// Query all beads with status=hooked
	stdout, err := f.runBdCmd(f.townRoot, "list", "--status=hooked", "--json", "--limit=0")
	if err != nil {
		return nil, nil // No hooked beads or bd not available
	}

	var beads []struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Assignee  string `json:"assignee"`
		UpdatedAt string `json:"updated_at"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &beads); err != nil {
		return nil, fmt.Errorf("parsing hooked beads: %w", err)
	}

	var rows []HookRow
	for _, bead := range beads {
		row := HookRow{
			ID:       bead.ID,
			Title:    bead.Title,
			Assignee: bead.Assignee,
			Agent:    formatAgentAddress(bead.Assignee),
		}

		// Keep full title - CSS handles overflow

		// Calculate age and stale status
		if bead.UpdatedAt != "" {
			if t, err := time.Parse(time.RFC3339, bead.UpdatedAt); err == nil {
				age := time.Since(t)
				row.Age = formatTimestamp(t)
				row.IsStale = age > time.Hour // Stale if hooked > 1 hour
			}
		}

		rows = append(rows, row)
	}

	// Sort by stale first (stuck work), then by age
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].IsStale != rows[j].IsStale {
			return rows[i].IsStale // Stale items first
		}
		return rows[i].Age > rows[j].Age
	})

	return rows, nil
}

// FetchMayor returns the Mayor's current status.
func (f *LiveConvoyFetcher) FetchMayor() (*MayorStatus, error) {
	status := &MayorStatus{
		IsAttached: false,
	}

	// Get the actual mayor session name (e.g., "hq-mayor")
	mayorSessionName := session.MayorSessionName()

	// Check if mayor tmux session exists
	stdout, err := f.runTmuxCmd("list-sessions", "-F", "#{session_name}:#{session_activity}")
	if err != nil {
		// tmux not running or no sessions
		return status, nil
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, mayorSessionName+":") {
			status.IsAttached = true
			status.SessionName = mayorSessionName

			// Parse activity timestamp
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				if activityTs, ok := parseActivityTimestamp(parts[1]); ok {
					age := time.Since(time.Unix(activityTs, 0))
					status.LastActivity = formatTimestamp(time.Unix(activityTs, 0))
					status.IsActive = age < f.mayorActiveThreshold
				}
			}
			break
		}
	}

	if status.IsAttached {
		status.Runtime = f.resolveMayorRuntime(mayorSessionName)
	}

	return status, nil
}

func (f *LiveConvoyFetcher) resolveMayorRuntime(sessionName string) string {
	if agentName, err := fetcherGetSessionEnv(sessionName, "GT_AGENT"); err == nil && strings.TrimSpace(agentName) != "" {
		agentName = strings.TrimSpace(agentName)
		rc, _, resolveErr := config.ResolveAgentConfigWithOverride(f.townRoot, "", agentName)
		if resolveErr == nil {
			return runtimeLabelForRuntimeConfig(rc, agentName)
		}
		if roleRC := config.ResolveRoleAgentConfig(constants.RoleMayor, f.townRoot, ""); roleRC != nil && strings.TrimSpace(roleRC.ResolvedAgent) == agentName {
			return runtimeLabelForRuntimeConfig(roleRC, agentName)
		}
		return agentName
	}

	return runtimeLabelForRuntimeConfig(config.ResolveRoleAgentConfig(constants.RoleMayor, f.townRoot, ""), "")
}

func runtimeLabelForRuntimeConfig(rc *config.RuntimeConfig, fallback string) string {
	if rc == nil {
		if fallback != "" {
			return fallback
		}
		return "claude"
	}
	if fallback == "" {
		fallback = rc.ResolvedAgent
	}
	return runtimeLabelFromConfig(rc.Command, rc.Args, fallback)
}

func runtimeLabelFromConfig(command string, args []string, fallback string) string {
	command = strings.TrimSpace(command)
	cmd := ""
	if command != "" {
		cmd = strings.TrimSpace(filepath.Base(command))
	}
	if cmd == "" {
		cmd = fallback
	}
	if cmd == "" {
		cmd = "claude"
	}
	if cmd == "cgroup-wrap" && len(args) > 0 {
		cmd = filepath.Base(args[0])
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if (arg == "--model" || arg == "-m") && i+1 < len(args) && strings.TrimSpace(args[i+1]) != "" {
			return cmd + "/" + stripModelSuffix(strings.TrimSpace(args[i+1]))
		}
		if strings.HasPrefix(arg, "--model=") {
			if v := strings.TrimSpace(strings.TrimPrefix(arg, "--model=")); v != "" {
				return cmd + "/" + stripModelSuffix(v)
			}
		}
		if strings.HasPrefix(arg, "-m=") {
			if v := strings.TrimSpace(strings.TrimPrefix(arg, "-m=")); v != "" {
				return cmd + "/" + stripModelSuffix(v)
			}
		}
	}

	return cmd
}

// stripModelSuffix removes bracketed context-window hints (e.g. "[1m]")
// from model names so the dashboard label stays human-readable.
// "sonnet[1m]" → "sonnet", "opus" → "opus".
func stripModelSuffix(model string) string {
	if idx := strings.Index(model, "["); idx > 0 {
		return model[:idx]
	}
	return model
}

// FetchIssues returns open issues (the backlog).
func (f *LiveConvoyFetcher) FetchIssues() ([]IssueRow, error) {
	// Query both open AND hooked issues for the Work panel
	// Open = ready to assign, Hooked = in progress
	var allBeads []struct {
		ID        string   `json:"id"`
		Title     string   `json:"title"`
		Type      string   `json:"type"`
		Priority  int      `json:"priority"`
		Labels    []string `json:"labels"`
		CreatedAt string   `json:"created_at"`
	}

	// Fetch open issues
	if stdout, err := f.runBdCmd(f.townRoot, "list", "--status=open", "--json", "--limit=50"); err == nil {
		var openBeads []struct {
			ID        string   `json:"id"`
			Title     string   `json:"title"`
			Type      string   `json:"type"`
			Priority  int      `json:"priority"`
			Labels    []string `json:"labels"`
			CreatedAt string   `json:"created_at"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &openBeads); err == nil {
			allBeads = append(allBeads, openBeads...)
		}
	}

	// Fetch hooked issues (in progress)
	if stdout, err := f.runBdCmd(f.townRoot, "list", "--status=hooked", "--json", "--limit=50"); err == nil {
		var hookedBeads []struct {
			ID        string   `json:"id"`
			Title     string   `json:"title"`
			Type      string   `json:"type"`
			Priority  int      `json:"priority"`
			Labels    []string `json:"labels"`
			CreatedAt string   `json:"created_at"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &hookedBeads); err == nil {
			allBeads = append(allBeads, hookedBeads...)
		}
	}

	beads := allBeads

	var rows []IssueRow
	for _, bead := range beads {
		// Skip internal types (messages, convoys, queues, merge-requests, wisps)
		// Check both legacy type field and gt: labels
		isInternal := false
		switch bead.Type {
		case "message", "convoy", "queue", "merge-request", "wisp", "agent":
			isInternal = true
		}
		for _, l := range bead.Labels {
			switch l {
			case "gt:message", "gt:convoy", "gt:queue", "gt:merge-request", "gt:wisp", "gt:agent":
				isInternal = true
			}
		}
		if isInternal {
			continue
		}

		row := IssueRow{
			ID:       bead.ID,
			Title:    bead.Title,
			Type:     bead.Type,
			Priority: bead.Priority,
		}

		// Keep full title - CSS handles overflow

		// Format labels (skip internal labels)
		var displayLabels []string
		for _, label := range bead.Labels {
			if !strings.HasPrefix(label, "gt:") && !strings.HasPrefix(label, "internal:") {
				displayLabels = append(displayLabels, label)
			}
		}
		if len(displayLabels) > 0 {
			row.Labels = strings.Join(displayLabels, ", ")
			if len(row.Labels) > 25 {
				row.Labels = row.Labels[:22] + "..."
			}
		}

		// Calculate age
		if bead.CreatedAt != "" {
			if t, err := time.Parse(time.RFC3339, bead.CreatedAt); err == nil {
				row.Age = formatTimestamp(t)
			}
		}

		rows = append(rows, row)
	}

	// Sort by priority (1=critical first), then by age
	sort.Slice(rows, func(i, j int) bool {
		pi, pj := rows[i].Priority, rows[j].Priority
		if pi == 0 {
			pi = 5 // Treat unset priority as low
		}
		if pj == 0 {
			pj = 5
		}
		if pi != pj {
			return pi < pj
		}
		return rows[i].Age > rows[j].Age // Older first for same priority
	})

	return rows, nil
}

// FetchActivity returns recent activity from the event log.
func (f *LiveConvoyFetcher) FetchActivity() ([]ActivityRow, error) {
	eventsPath := filepath.Join(f.townRoot, ".events.jsonl")

	// Read events file
	data, err := os.ReadFile(eventsPath)
	if err != nil {
		return nil, nil // No events file
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 {
		return nil, nil
	}

	// Take last 50 events for richer timeline
	start := 0
	if len(lines) > 50 {
		start = len(lines) - 50
	}

	var rows []ActivityRow
	for i := len(lines) - 1; i >= start; i-- {
		line := lines[i]
		if line == "" {
			continue
		}

		var event struct {
			Timestamp  string                 `json:"ts"`
			Type       string                 `json:"type"`
			Actor      string                 `json:"actor"`
			Payload    map[string]interface{} `json:"payload"`
			Visibility string                 `json:"visibility"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

		// Skip audit-only events
		if event.Visibility == "audit" {
			continue
		}

		row := ActivityRow{
			Type:         event.Type,
			Category:     eventCategory(event.Type),
			Actor:        formatAgentAddress(event.Actor),
			Rig:          extractRig(event.Actor),
			Icon:         eventIcon(event.Type),
			RawTimestamp: event.Timestamp,
		}

		// Calculate time ago
		if t, err := time.Parse(time.RFC3339, event.Timestamp); err == nil {
			row.Time = formatTimestamp(t)
		}

		// Generate human-readable summary
		row.Summary = eventSummary(event.Type, event.Actor, event.Payload)

		rows = append(rows, row)
	}

	return rows, nil
}

// eventCategory classifies an event type into a filter category.
func eventCategory(eventType string) string {
	switch eventType {
	case "spawn", "kill", "session_start", "session_end", "session_death", "mass_death", "nudge", "handoff":
		return "agent"
	case "sling", "hook", "unhook", "done", "merge_started", "merged", "merge_failed":
		return "work"
	case "mail", "escalation_sent", "escalation_acked", "escalation_closed":
		return "comms"
	case "boot", "halt", "patrol_started", "patrol_complete":
		return "system"
	default:
		return "system"
	}
}

// extractRig extracts the rig name from an actor address like "gastown/polecats/nux".
func extractRig(actor string) string {
	if actor == "" {
		return ""
	}
	parts := strings.SplitN(actor, "/", 2)
	if len(parts) > 0 {
		return parts[0]
	}
	return ""
}

// eventIcon returns an emoji for an event type.
func eventIcon(eventType string) string {
	icons := map[string]string{
		"sling":             "🎯",
		"hook":              "🪝",
		"unhook":            "🔓",
		"done":              "✅",
		"mail":              "📬",
		"spawn":             "🦨",
		"kill":              "💀",
		"nudge":             "👉",
		"handoff":           "🤝",
		"session_start":     "▶️",
		"session_end":       "⏹️",
		"session_death":     "☠️",
		"mass_death":        "💥",
		"patrol_started":    "🔍",
		"patrol_complete":   "✔️",
		"escalation_sent":   "⚠️",
		"escalation_acked":  "👍",
		"escalation_closed": "🔕",
		"merge_started":     "🔀",
		"merged":            "✨",
		"merge_failed":      "❌",
		"boot":              "🚀",
		"halt":              "🛑",
	}
	if icon, ok := icons[eventType]; ok {
		return icon
	}
	return "📋"
}

// eventSummary generates a human-readable summary for an event.
func eventSummary(eventType, actor string, payload map[string]interface{}) string {
	shortActor := formatAgentAddress(actor)

	switch eventType {
	case "sling":
		bead, _ := payload["bead"].(string)
		target, _ := payload["target"].(string)
		return fmt.Sprintf("%s slung to %s", bead, formatAgentAddress(target))
	case "done":
		bead, _ := payload["bead"].(string)
		return fmt.Sprintf("%s completed %s", shortActor, bead)
	case "mail":
		to, _ := payload["to"].(string)
		subject, _ := payload["subject"].(string)
		if len(subject) > 25 {
			subject = subject[:22] + "..."
		}
		return fmt.Sprintf("→ %s: %s", formatAgentAddress(to), subject)
	case "spawn":
		return fmt.Sprintf("%s spawned", shortActor)
	case "kill":
		return fmt.Sprintf("%s killed", shortActor)
	case "hook":
		bead, _ := payload["bead"].(string)
		return fmt.Sprintf("%s hooked %s", shortActor, bead)
	case "unhook":
		bead, _ := payload["bead"].(string)
		return fmt.Sprintf("%s unhooked %s", shortActor, bead)
	case "merged":
		branch, _ := payload["branch"].(string)
		return fmt.Sprintf("merged %s", branch)
	case "merge_failed":
		reason, _ := payload["reason"].(string)
		if len(reason) > 30 {
			reason = reason[:27] + "..."
		}
		return fmt.Sprintf("merge failed: %s", reason)
	case "escalation_sent":
		return "escalation created"
	case "session_death":
		role, _ := payload["role"].(string)
		return fmt.Sprintf("%s session died", formatAgentAddress(role))
	case "mass_death":
		count, _ := payload["count"].(float64)
		return fmt.Sprintf("%.0f sessions died", count)
	default:
		return eventType
	}
}
