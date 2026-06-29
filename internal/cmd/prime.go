package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/cli"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/lock"
	"github.com/steveyegge/gastown/internal/refinery"
	"github.com/steveyegge/gastown/internal/state"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/telemetry"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/util"
	"github.com/steveyegge/gastown/internal/workspace"
	worktreeintegrity "github.com/steveyegge/gastown/internal/worktree"
)

var primeHookMode bool
var primeDryRun bool
var primeState bool
var primeStateJSON bool
var primeExplain bool
var primeStructuredSessionStartOutput bool

// Prime's external injections are best-effort; role context should still
// return when bd/mail is slow or wedged.
var primeExternalToolTimeout = 5 * time.Second
var primeExternalToolWaitDelay = time.Second

// primeHookSource stores the SessionStart source ("startup", "resume", "clear", "compact")
// when running in hook mode. Used to provide lighter output on compaction/resume.
var primeHookSource string

// primeHandoffReason stores the reason from the handoff marker (e.g., "compaction").
// Set by checkHandoffMarker when a marker with a reason field is found.
var primeHandoffReason string

// Role represents a detected agent role.
type Role string

const (
	RoleMayor    Role = "mayor"
	RoleDeacon   Role = "deacon"
	RoleBoot     Role = "boot"
	RoleWitness  Role = "witness"
	RoleRefinery Role = "refinery"
	RolePolecat  Role = "polecat"
	RoleCrew     Role = "crew"
	RoleDog      Role = "dog"
	RoleUnknown  Role = "unknown"
)

var primeCmd = &cobra.Command{
	Use:         "prime",
	GroupID:     GroupDiag,
	Annotations: map[string]string{AnnotationPolecatSafe: "true"},
	Short:       "Output role context for current directory",
	Long: `Detect the agent role from the current directory and output context.

Role detection:
  - Town root → Neutral (no role inferred; use GT_ROLE)
  - mayor/ or <rig>/mayor/ → Mayor context
  - <rig>/witness/rig/ → Witness context
  - <rig>/refinery/rig/ → Refinery context
  - <rig>/polecats/<name>/ → Polecat context

This command is typically used in shell prompts or agent initialization.

HOOK MODE (--hook):
  When called as an LLM runtime hook, use --hook to enable session ID handling,
  agent-ready signaling, and session persistence.

  Session ID resolution (first match wins):
    1. GT_SESSION_ID env var
    2. CLAUDE_SESSION_ID env var
    3. Persisted .runtime/session_id (from prior SessionStart)
    4. Stdin JSON (Claude Code format)
    5. Auto-generated UUID

  Source resolution: GT_HOOK_SOURCE env var, then stdin JSON "source" field.

  Claude Code integration (in .claude/settings.json):
    "SessionStart": [{"hooks": [{"type": "command", "command": "gt prime --hook"}]}]
    Claude sends JSON on stdin: {"session_id":"uuid","source":"startup|resume|compact"}

  Gemini CLI / other runtimes (in .gemini/settings.json):
    "SessionStart": "export GT_SESSION_ID=$(uuidgen) GT_HOOK_SOURCE=startup && gt prime --hook"
    "PreCompress":  "export GT_HOOK_SOURCE=compact && gt prime --hook"
    Set GT_SESSION_ID + GT_HOOK_SOURCE as env vars to skip the stdin read entirely.`,
	RunE: runPrime,
}

func init() {
	primeCmd.Flags().BoolVar(&primeHookMode, "hook", false,
		"Hook mode: read session ID from stdin JSON (for LLM runtime hooks)")
	primeCmd.Flags().BoolVar(&primeDryRun, "dry-run", false,
		"Show what would be injected without side effects (no marker removal, no mail)")
	primeCmd.Flags().BoolVar(&primeState, "state", false,
		"Show detected session state only (normal/post-handoff/crash/autonomous)")
	primeCmd.Flags().BoolVar(&primeStateJSON, "json", false,
		"Output state as JSON (requires --state)")
	primeCmd.Flags().BoolVar(&primeExplain, "explain", false,
		"Show why each section was included")
	rootCmd.AddCommand(primeCmd)
}

// RoleContext is an alias for RoleInfo for backward compatibility.
// New code should use RoleInfo directly.
type RoleContext = RoleInfo

func runPrime(cmd *cobra.Command, args []string) (retErr error) {
	defer func() { telemetry.RecordPrime(context.Background(), os.Getenv("GT_ROLE"), primeHookMode, retErr) }()
	if err := validatePrimeFlags(); err != nil {
		return err
	}

	cwd, townRoot, err := resolvePrimeWorkspace()
	if err != nil {
		return err
	}
	if townRoot == "" {
		return nil // Silent exit - not in workspace and not enabled
	}

	roleInfo, err := GetRoleWithContext(cwd, townRoot)
	if err != nil {
		return fmt.Errorf("detecting role: %w", err)
	}
	if err := ensureRoleWorktreeIntegrity(cwd, townRoot, roleInfo.Role); err != nil {
		return err
	}

	if primeHookMode {
		handlePrimeHookMode(townRoot, cwd)
	}

	// Check for handoff marker (prevents handoff loop bug)
	if primeDryRun {
		checkHandoffMarkerDryRun(cwd)
	} else {
		checkHandoffMarker(cwd)
	}

	warnRoleMismatch(roleInfo, cwd)

	ctx := RoleContext{
		Role:     roleInfo.Role,
		Rig:      roleInfo.Rig,
		Polecat:  roleInfo.Polecat,
		TownRoot: townRoot,
		WorkDir:  cwd,
	}

	// --state mode: output state only and exit
	if primeState {
		outputState(ctx, primeStateJSON)
		return nil
	}

	// Compact/resume: fast path that skips setupPrimeSession and the
	// retry-heavy findAgentWork. The agent already has role context and
	// work state in compressed memory — just confirm identity and inject
	// any new mail. This keeps PreCompress hooks under 1s for non-Claude
	// runtimes that have short hook timeouts (Gemini CLI).
	if isCompactResume() {
		runPrimeCompactResume(ctx)
		return nil
	}

	if err := setupPrimeSession(ctx, roleInfo); err != nil {
		return err
	}

	// P0: Fetch work context once — used for both OTel attribution and output.
	// injectWorkContext sets GT_WORK_RIG/BEAD/MOL in the current process env and
	// in the tmux session env so all subsequent subprocesses (bd, mail, …) carry
	// the correct work attribution until the next gt prime overwrites it.
	hookedBead, hookErr := findAgentWork(ctx)
	if hookErr != nil {
		// Cross-rig / unresolvable hook bead (gt-el4): the agent bead names a
		// hook bead that bd show cannot find. Don't sit idle "pontificating" —
		// emit a clear message, fire a HIGH escalation so the witness sees the
		// dead-with-active-work state, and exit non-zero so the dog can clear
		// the hook on its next sweep.
		if errors.Is(hookErr, ErrHookUnresolvable) {
			agentID := getAgentIdentity(ctx)
			fmt.Fprintf(os.Stderr,
				"polecat prime: hooked bead not resolvable from %s; check rig DB / dispatch routing. err=%v\n",
				ctx.WorkDir, hookErr)
			firePolecatHookUnresolvableEscalation(agentID, hookErr.Error())
			return fmt.Errorf("polecat prime: hook unresolvable: %w", hookErr)
		}
		// Database error during hook query — NOT the same as "no work assigned".
		// Emit a loud warning so the agent does NOT run gt done / close the bead.
		// This prevents the destructive cycle: DB error → "no work" → gt done → bead lost. (GH#2638)
		fmt.Fprintf(os.Stderr, "\n%s\n", style.Bold.Render("## ⚠️  DATABASE ERROR — DO NOT RUN gt done ⚠️"))
		fmt.Fprintf(os.Stderr, "Hook query failed: %v\n", hookErr)
		fmt.Fprintf(os.Stderr, "This is a database connectivity error, NOT an empty hook.\n")
		fmt.Fprintf(os.Stderr, "Your work may still be assigned. Do NOT close any beads.\n")
		fmt.Fprintf(os.Stderr, "Escalate to witness/mayor and wait for resolution.\n\n")
	}
	injectWorkContext(ctx, hookedBead)

	formula, err := outputRoleContext(ctx)
	if err != nil {
		return err
	}
	// Log the rendered formula to OTEL so it's visible in VictoriaLogs alongside
	// Claude's API calls, letting operators see exactly what context each agent
	// started with. Only emitted when GT telemetry is active (GT_OTEL_LOGS_URL set).
	telemetry.RecordPrimeContext(context.Background(), formula, os.Getenv("GT_ROLE"), primeHookMode)

	hasSlungWork, err := checkSlungWork(ctx, hookedBead)
	if err != nil {
		return err
	}
	explain(hasSlungWork, "Autonomous mode: hooked/in-progress work detected")

	outputMoleculeContext(ctx)
	outputCheckpointContext(ctx)
	runPrimeExternalTools(ctx, cwd)

	if ctx.Role == RoleMayor {
		checkPendingEscalations(ctx)
	}

	if !hasSlungWork {
		explain(true, "Startup directive: normal mode (no hooked work)")
		outputStartupDirective(ctx)
	}

	return nil
}

func ensureRoleWorktreeIntegrity(cwd, townRoot string, role Role) error {
	if err := worktreeintegrity.Validate(cwd, worktreeintegrity.IntegrityOptions{
		TownRoot: townRoot,
		Require:  roleRequiresWorktreeIntegrity(role),
	}); err != nil {
		return fmt.Errorf("%w\nRemediation: stop using this worktree and run `gt doctor --fix`", err)
	}
	return nil
}

func roleRequiresWorktreeIntegrity(role Role) bool {
	switch role {
	case RolePolecat, RoleCrew, RoleWitness, RoleRefinery, RoleDog, RoleBoot:
		return true
	default:
		return false
	}
}

// runPrimeCompactResume runs a lighter prime after compaction or resume.
// The agent already has full role context in compressed memory. This just
// restores identity and injects any new mail. It deliberately skips
// setupPrimeSession and findAgentWork (which hit Dolt) to stay fast
// enough for non-Claude runtimes with short hook timeouts.
//
// Unlike the full prime path, this outputs a brief recovery line instead of
// the full AUTONOMOUS WORK MODE block. This prevents agents from re-announcing
// and re-initializing after compaction. (GH#1965)
func runPrimeCompactResume(ctx RoleContext) {
	// Brief identity confirmation
	actor := getAgentIdentity(ctx)
	source := primeHookSource
	if source == "" && primeHandoffReason != "" {
		source = "handoff-" + primeHandoffReason
	}
	fmt.Printf("\n> **Recovery**: Context %s complete. You are **%s** (%s).\n",
		source, actor, ctx.Role)

	// Session metadata for seance
	outputSessionMetadata(ctx)

	fmt.Println("\n---")
	fmt.Println()
	fmt.Println("**Continue your current task.** If you've lost context, run `gt prime` for full reload.")

	// Remind polecats about gt done — after compaction the agent may have lost
	// the formula checklist and forgotten that gt done is required to submit work.
	// Without this, polecats finish implementation and sit at the prompt forever.
	if ctx.Role == RolePolecat {
		fmt.Printf("\n**IMPORTANT**: When all work is complete (code committed, tests pass), run `%s done` to submit to the merge queue.\n", cli.Name())
	}
}

// validatePrimeFlags checks that CLI flag combinations are valid.
func validatePrimeFlags() error {
	if primeState && (primeHookMode || primeDryRun || primeExplain) {
		return fmt.Errorf("--state cannot be combined with other flags (except --json)")
	}
	if primeStateJSON && !primeState {
		return fmt.Errorf("--json requires --state")
	}
	return nil
}

// resolvePrimeWorkspace finds the cwd and town root for prime.
// Returns empty townRoot (not an error) when not in a workspace and not enabled.
func resolvePrimeWorkspace() (cwd, townRoot string, err error) {
	cwd, err = os.Getwd()
	if err != nil {
		return "", "", fmt.Errorf("getting current directory: %w", err)
	}

	townRoot, err = workspace.FindFromCwd()
	if err != nil {
		return "", "", fmt.Errorf("finding workspace: %w", err)
	}

	// "Discover, Don't Track" principle:
	// If in a workspace, proceed. If not, check global enabled state.
	if townRoot == "" {
		if !state.IsEnabled() {
			return cwd, "", nil // Signal caller to exit silently
		}
		return "", "", fmt.Errorf("not in a Gas Town workspace")
	}

	return cwd, townRoot, nil
}

// handlePrimeHookMode reads session ID from stdin and persists it.
// Called when --hook flag is set for LLM runtime hook integration.
func handlePrimeHookMode(townRoot, cwd string) {
	sessionID, source := readHookSessionID()
	if !primeDryRun {
		persistSessionID(townRoot, sessionID)
		if cwd != townRoot {
			persistSessionID(cwd, sessionID)
		}
	}
	_ = os.Setenv("GT_SESSION_ID", sessionID)
	_ = os.Setenv("CLAUDE_SESSION_ID", sessionID) // Legacy compatibility

	// ZFC: Signal agent readiness via tmux env var (gt-sk5u).
	// WaitForCommand polls for this instead of probing the process tree.
	// This handles agents wrapped in shell scripts where pane_current_command
	// remains "bash" even though the agent is running as a descendant.
	signalAgentReady()

	// Store source for compact/resume detection in runPrime
	primeHookSource = source

	explain(true, "Session beacon: hook mode enabled, session ID from stdin")
	for _, line := range hookSessionBeaconLines(sessionID, source) {
		fmt.Println(line)
	}
}

// hookSessionBeaconLines returns the bracketed session/source markers used by
// the normal hook path. Structured SessionStart output skips them because Codex
// tries to auto-detect JSON, sees the leading '[', and misclassifies the startup
// stream as JSON instead of plain text metadata.
func hookSessionBeaconLines(sessionID, source string) []string {
	if primeStructuredSessionStartOutput {
		return nil
	}
	lines := []string{fmt.Sprintf("[session:%s]", sessionID)}
	if source != "" {
		lines = append(lines, fmt.Sprintf("[source:%s]", source))
	}
	return lines
}

// signalAgentReady sets GT_AGENT_READY=1 in the current tmux session environment.
// Called from the agent's SessionStart hook to signal that the agent has started.
// WaitForCommand polls for this variable as a ZFC-compliant alternative to
// probing the process tree via IsAgentAlive.
// Uses ResolveCurrentSession to find our session on the town socket — raw
// exec.Command("tmux", ...) would use the default socket and miss the gastown server.
func signalAgentReady() {
	t := tmux.NewTmux()
	name, err := t.ResolveCurrentSession()
	if err != nil || name == "" {
		return
	}
	_ = t.SetEnvironment(name, tmux.EnvAgentReady, "1")
}

// isCompactResume returns true if the current prime is running after compaction or resume.
// In these cases, the agent already has role context in compressed memory and only needs
// a brief identity confirmation plus hook/work status.
//
// This also returns true for compaction-triggered handoff cycles (crew workers).
// When PreCompact runs "gt handoff --cycle --reason compaction", the new session
// gets source="startup" but the handoff marker carries reason="compaction".
// Without this, the new session runs full prime with AUTONOMOUS WORK MODE,
// causing the agent to re-initialize instead of continuing. (GH#1965)
func isCompactResume() bool {
	return primeHookSource == "compact" || primeHookSource == "resume" || primeHandoffReason == "compaction"
}

// warnRoleMismatch outputs a prominent warning if GT_ROLE disagrees with cwd detection.
func warnRoleMismatch(roleInfo RoleInfo, cwd string) {
	if !roleInfo.Mismatch {
		return
	}
	fmt.Printf("\n%s\n", style.Bold.Render("⚠️  ROLE/LOCATION MISMATCH"))
	fmt.Printf("You are %s (from $GT_ROLE) but your cwd suggests %s.\n",
		style.Bold.Render(string(roleInfo.Role)),
		style.Bold.Render(string(roleInfo.CwdRole)))
	fmt.Printf("Expected home: %s\n", roleInfo.Home)
	fmt.Printf("Actual cwd:    %s\n", cwd)
	fmt.Println()
	fmt.Println("This can cause commands to misbehave. Either:")
	fmt.Println("  1. cd to your home directory, OR")
	fmt.Println("  2. Use absolute paths for gt/bd commands")
	fmt.Println()
}

// setupPrimeSession handles identity locking, beads redirect, and session events.
// Skipped entirely in dry-run mode.
func setupPrimeSession(ctx RoleContext, roleInfo RoleInfo) error {
	if primeDryRun {
		return nil
	}
	if err := acquireIdentityLock(ctx); err != nil {
		return err
	}
	if !roleInfo.Mismatch {
		ensureBeadsRedirect(ctx)
	}
	repairSessionEnv(ctx, roleInfo)
	// Only emit session_start when gt prime is running as a SessionStart or
	// PreCompact hook. Bare gt prime calls (e.g. an agent reading another
	// agent's context) must not emit session_start — doing so logs a spurious
	// event with the target agent's persisted session_id, which pollutes the
	// event stream and can confuse gt seance discovery.
	if primeHookMode {
		emitSessionEvent(ctx)
	}
	return nil
}

// repairSessionEnv checks if the tmux session is missing identity env vars
// and re-injects them from the current role context. This self-heals sessions
// that were created through non-standard paths or older gt versions. GH#3006.
func repairSessionEnv(ctx RoleContext, roleInfo RoleInfo) {
	if os.Getenv("TMUX") == "" {
		return
	}

	t := tmux.NewTmux()
	session, err := t.ResolveCurrentSession()
	if err != nil || session == "" {
		return
	}

	// Quick check: if GT_ROLE is already set in the session env, assume healthy.
	if _, err := t.GetEnvironment(session, "GT_ROLE"); err == nil {
		return
	}

	// Map prime Role type to config.AgentEnv role constant.
	var agentName string
	switch ctx.Role {
	case RoleCrew:
		agentName = roleInfo.Polecat // RoleInfo.Polecat holds crew member name too
	case RolePolecat:
		agentName = roleInfo.Polecat
	case RoleDog:
		agentName = roleInfo.Polecat
	}

	envVars := config.AgentEnv(config.AgentEnvConfig{
		Role:        string(ctx.Role),
		Rig:         ctx.Rig,
		AgentName:   agentName,
		TownRoot:    ctx.TownRoot,
		SessionName: session,
	})

	// Only inject identity-related vars that are missing, not the full AgentEnv
	// output (which includes Dolt ports, OTEL config, etc. that may have been
	// intentionally overridden per-session).
	identitySet := make(map[string]bool, len(config.IdentityEnvVars))
	for _, k := range config.IdentityEnvVars {
		identitySet[k] = true
	}
	// Also include GT_ROOT and GT_SESSION — core session identity.
	identitySet["GT_ROOT"] = true
	identitySet["GT_SESSION"] = true

	var repaired int
	for k, v := range envVars {
		if !identitySet[k] {
			continue
		}
		if _, err := t.GetEnvironment(session, k); err == nil {
			continue // already set at session level
		}
		if err := t.SetEnvironment(session, k, v); err == nil {
			repaired++
		}
	}

	if repaired > 0 {
		fmt.Printf("\n%s Injected %d missing identity vars into session %s\n",
			style.Bold.Render("⚠️  SESSION ENV REPAIR:"), repaired, session)
		// Also set in the current process so this prime run uses the correct identity.
		for k, v := range envVars {
			if identitySet[k] {
				os.Setenv(k, v)
			}
		}
	}
}

// outputRoleContext emits session metadata and all role/context output sections.
// Returns the rendered formula content for OTEL telemetry (empty if using fallback path).
func outputRoleContext(ctx RoleContext) (string, error) {
	explain(true, "Session metadata: always included for seance discovery")
	outputSessionMetadata(ctx)

	explain(true, fmt.Sprintf("Role context: detected role is %s", ctx.Role))
	formula, err := outputPrimeContext(ctx)
	if err != nil {
		return "", err
	}

	outputRoleDirectives(ctx, os.Stdout, primeExplain)
	outputContextFile(ctx)
	outputHandoffContent(ctx)
	outputAttachmentStatus(ctx)
	return formula, nil
}

// runPrimeExternalTools runs lightweight memory and mail injection.
// Skipped in dry-run mode with explain output.
func runPrimeExternalTools(ctx RoleContext, cwd string) {
	if primeDryRun {
		explain(true, "memory injection: skipped in dry-run mode")
		explain(true, "gt mail check --inject: skipped in dry-run mode")
		return
	}
	runMemoryInject(cwd)
	if shouldSkipStartupMailInject(string(ctx.Role)) {
		explain(true, fmt.Sprintf("gt mail check --inject: skipped for patrol role %s", ctx.Role))
		return
	}
	runMailCheckInject(cwd)
}

func shouldSkipStartupMailInject(role string) bool {
	switch strings.ToLower(role) {
	case string(RoleWitness), string(RoleRefinery), string(RoleDeacon), string(RoleBoot):
		return true
	default:
		return false
	}
}

func runPrimeExternalCommand(workDir, name string, args ...string) (bytes.Buffer, bytes.Buffer, error) {
	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), primeExternalToolTimeout)
	defer cancel()

	if name == "bd" {
		args = beads.InjectFlatForListJSON(args)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	if name == "bd" {
		beads.ConfigureCommand(cmd, workDir, beads.ResolveBeadsDir(workDir), beads.SubprocessModeForArgs(args))
	} else {
		cmd.Dir = workDir
		cmd.Env = os.Environ()
		util.SetProcessGroup(cmd)
	}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = primeExternalToolWaitDelay

	return stdout, stderr, cmd.Run()
}

// memoryTypeLabels maps type keys to human-readable section headers for prime injection.
var memoryTypeLabels = map[string]string{
	"feedback":  "Behavioral Rules (from user feedback)",
	"user":      "User Context",
	"project":   "Project Context",
	"reference": "Reference Links",
	"general":   "General",
}

// runMemoryInject loads memories from beads kv and outputs them during prime.
// Memories are grouped by type and ordered by priority (feedback first).
func runMemoryInject(workDir string) {
	kvs, err := bdKvListJSONForPrime(workDir)
	if err != nil {
		return // Silently skip if kv list fails
	}

	// Group memories by type
	type mem struct {
		shortKey string
		value    string
	}
	grouped := make(map[string][]mem)

	for k, v := range kvs {
		if !strings.HasPrefix(k, memoryKeyPrefix) {
			continue
		}
		memType, shortKey := parseMemoryKey(k)
		grouped[memType] = append(grouped[memType], mem{shortKey: shortKey, value: v})
	}

	if len(grouped) == 0 {
		return
	}

	// Sort each group by key
	for t := range grouped {
		sort.Slice(grouped[t], func(i, j int) bool {
			return grouped[t][i].shortKey < grouped[t][j].shortKey
		})
	}

	fmt.Println()
	fmt.Println("# Agent Memories")

	for _, t := range memoryTypeOrder {
		mems, ok := grouped[t]
		if !ok || len(mems) == 0 {
			continue
		}
		label := memoryTypeLabels[t]
		if label == "" {
			label = t
		}
		fmt.Printf("\n## %s\n\n", label)
		for _, m := range mems {
			fmt.Printf("- **%s**: %s\n", m.shortKey, m.value)
		}
	}
}

func bdKvListJSONForPrime(workDir string) (map[string]string, error) {
	stdout, _, err := runPrimeExternalCommand(workDir, "bd", "kv", "list", "--json")
	if err != nil {
		return nil, err
	}

	return parseBdKvListJSON(stdout.Bytes())
}

// runMailCheckInject runs `gt mail check --inject` and outputs the result.
// This injects any pending mail into the agent's context.
func runMailCheckInject(workDir string) {
	stdout, stderr, err := runPrimeExternalCommand(workDir, "gt", "mail", "check", "--inject")
	if err != nil {
		// Skip if mail check fails, but log stderr for debugging
		if errMsg := strings.TrimSpace(stderr.String()); errMsg != "" {
			fmt.Fprintf(os.Stderr, "gt mail check: %s\n", errMsg)
		}
		return
	}

	output := strings.TrimSpace(stdout.String())
	if output != "" {
		fmt.Println()
		fmt.Println(output)
	}
}

// checkSlungWork checks for hooked work on the agent's hook.
// If found, displays AUTONOMOUS WORK MODE and tells the agent to execute immediately.
// Returns true if hooked work was found (caller should skip normal startup directive).
//
// hookedBead is pre-fetched by the caller (runPrime) via findAgentWork to avoid a
// redundant lookup and ensure work context is already injected before output runs.
func checkSlungWork(ctx RoleContext, hookedBead *beads.Issue) (bool, error) {
	if hookedBead == nil {
		return false, nil
	}
	if ctx.Role == RoleRefinery {
		if stop, err := refinery.ActiveSafetyStop(ctx.TownRoot, ctx.Rig); err != nil {
			return true, fmt.Errorf("checking refinery safety stop: %w", err)
		} else if stop != nil {
			outputRefinerySafetyStopDirective(ctx, stop)
			return true, nil
		}
	}

	attachment := beads.ParseAttachmentFields(hookedBead)
	hasWorkflow := hasWorkflowAttachment(attachment)

	outputAutonomousDirective(ctx, hookedBead, hasWorkflow)
	outputHookedBeadDetails(hookedBead)

	if hasWorkflow {
		if err := outputMoleculeWorkflow(ctx, attachment); err != nil {
			return true, err
		}
	} else {
		outputBeadPreview(hookedBead)
	}

	return true, nil
}

func outputRefinerySafetyStopDirective(ctx RoleContext, stop *refinery.SafetyStop) {
	fmt.Println()
	fmt.Printf("%s\n", style.Bold.Render("## REFINERY SAFETY STOP ACTIVE"))
	fmt.Printf("Refinery %s is %s.\n", ctx.Rig, stop.Reason())
	fmt.Println("Hooked refinery work remains parked; do not run patrol, MR, or merge workflow until Mayor clears the safety_stop label.")
	fmt.Println()
}

func hasWorkflowAttachment(attachment *beads.AttachmentFields) bool {
	return attachment != nil && (attachment.AttachedMolecule != "" || attachment.AttachedFormula != "")
}

// findAgentWork looks up hooked or in-progress beads assigned to this agent.
// Primary: reads hook_bead from the agent bead (same strategy as detectSessionState/gt hook).
// Fallback: queries by assignee for agents without an agent bead.
// For polecats and crew, retries up to 3 times with 2-second delays to handle
// the timing race where hook state hasn't propagated by the time gt prime runs.
// See: https://github.com/steveyegge/gastown/issues/1438
//
// Returns (nil, nil) if no work is found.
// Returns (nil, err) if all attempts failed due to database errors — the caller
// MUST distinguish this from "no work" to avoid silently closing beads. (GH#2638)
func findAgentWork(ctx RoleContext) (*beads.Issue, error) {
	agentID := getAgentIdentity(ctx)
	if agentID == "" {
		return nil, nil
	}

	// Polecats, crew, and dogs use a retry loop to handle the timing race
	// where the hook write (status=hooked + assignee) hasn't propagated to
	// new Dolt connections by the time gt prime runs on session startup.
	// Dogs are especially affected since dispatch is fire-and-forget. (GH#2748)
	// Uses exponential backoff: 500ms, 1s, 2s, 4s, 8s (total ~15.5s max).
	// See: https://github.com/steveyegge/gastown/issues/2389
	//
	// On compact/resume, the agent already has work context in memory.
	// A single attempt suffices — retries would add ~15s of latency to
	// compaction hooks, causing non-Claude runtimes to report hook failure.
	maxAttempts := 1
	if (ctx.Role == RolePolecat || ctx.Role == RoleCrew || ctx.Role == RoleDog) && !isCompactResume() {
		maxAttempts = 5
	}

	var lastErr error
	backoff := 500 * time.Millisecond
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			time.Sleep(backoff)
			backoff *= 2
		}

		result, err := findAgentWorkOnce(ctx, agentID)
		if result != nil {
			return result, nil
		}
		if err != nil {
			lastErr = err
		} else {
			// Successful query returned no work — not a DB error
			lastErr = nil
		}
	}

	return nil, lastErr
}

// ErrHookUnresolvable signals that the agent bead points at a hook bead that
// cannot be resolved from the agent's CWD (e.g., cross-rig dispatch where an
// `hq-` bead was handed to a `gt-` rig polecat). See gt-el4.
var ErrHookUnresolvable = errors.New("hooked bead not resolvable from this rig")

// isBeadNotFound reports whether an error from beads.Show represents a missing
// bead (as opposed to a connectivity / auth / parsing error). Heuristic match
// on the canonical "no issue found" / "not found" markers bd surfaces.
func isBeadNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no issue found") ||
		strings.Contains(msg, "not found") ||
		strings.Contains(msg, "issue not found")
}

// firePolecatHookUnresolvableEscalation fires a HIGH escalation so the witness
// sees the dead-with-active-work state immediately. Best effort — logged on
// failure but does not gate the prime exit.
var firePolecatHookUnresolvableEscalation = func(agentID, detail string) {
	msg := fmt.Sprintf("polecat hook unresolvable: agent=%s detail=%s — see gt-el4", agentID, detail)
	cmd := exec.Command("gt", "escalate", "--severity", "high", "--reason", "polecat-hook-unresolvable", msg)
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "polecat prime: escalation failed: %v\n", err)
	}
}

// findAgentWorkOnce performs a single attempt to find hooked work for an agent.
// Returns (nil, nil) when no work is found.
// Returns (nil, err) when the database query itself failed — the caller must
// not treat this as "no work assigned". (GH#2638)
// Returns (nil, ErrHookUnresolvable) when the agent bead points at a hook bead
// that cannot be resolved — the polecat must fail fast rather than pontificate.
func findAgentWorkOnce(ctx RoleContext, agentID string) (*beads.Issue, error) {
	// Use rig root for beads queries instead of ctx.WorkDir. Polecat worktrees
	// rely on .beads/redirect which can fail to resolve in edge cases, causing
	// polecats to miss hooked work and exit immediately. The rig root directory
	// always has the authoritative .beads/ database. (GH#2503)
	b := beads.New(rigBeadsRoot(ctx))

	// Agent bead's hook_bead field. NOTE: updateAgentHookBead was made a no-op
	// (see sling_helpers.go), so HookBead is typically empty. Kept for backward
	// compatibility with agent beads that still have hook_bead set.
	agentBeadID := buildAgentBeadID(agentID, ctx.Role, ctx.TownRoot)
	var staleHookErr error
	if agentBeadID != "" {
		agentBeadDir := beads.ResolveHookDir(ctx.TownRoot, agentBeadID, ctx.WorkDir)
		ab := beads.New(agentBeadDir)
		if agentBead, err := ab.Show(agentBeadID); err == nil && agentBead != nil && agentBead.HookBead != "" {
			hookBeadDir := beads.ResolveHookDir(ctx.TownRoot, agentBead.HookBead, ctx.WorkDir)
			hb := beads.New(hookBeadDir)
			hookBead, showErr := hb.Show(agentBead.HookBead)
			if showErr == nil && hookBead != nil &&
				(hookBead.Status == beads.StatusHooked || hookBead.Status == "in_progress") {
				return hookBead, nil
			}
			// The agent bead names a hook bead but `bd show` cannot find it.
			// This is the cross-rig dispatch failure mode (gt-el4): an `hq-`
			// bead was handed to a polecat whose DB only resolves `gt-`. Fail
			// fast — never pontificate, the witness will clear the hook on
			// its next sweep and the dispatcher will (or won't) re-issue.
			if hookBead == nil || isBeadNotFound(showErr) {
				staleHookErr = fmt.Errorf("%w: agent=%s hook_bead=%s cwd=%s: %v",
					ErrHookUnresolvable, agentID, agentBead.HookBead, ctx.WorkDir, showErr)
			}
		}
	}

	// Fallback: query by assignee
	hookedBeads, err := b.List(beads.ListOptions{
		Status:   beads.StatusHooked,
		Assignee: agentID,
		Priority: -1,
	})
	if err != nil {
		return nil, fmt.Errorf("querying hooked beads: %w", err)
	}

	// Fall back to in_progress beads (session interrupted before completion)
	if len(hookedBeads) == 0 {
		inProgressBeads, err := b.List(beads.ListOptions{
			Status:   "in_progress",
			Assignee: agentID,
			Priority: -1,
		})
		if err != nil {
			return nil, fmt.Errorf("querying in-progress beads: %w", err)
		}
		if len(inProgressBeads) > 0 {
			hookedBeads = inProgressBeads
		}
	}

	// Town-level fallback: rig-level agents (polecats, crew) may have hooked
	// HQ beads (hq-* prefix) stored in townRoot/.beads, not the rig's database.
	// Matches the fallback in molecule_status.go and unsling.go. (gt-dtq7)
	if len(hookedBeads) == 0 && !isTownLevelRole(agentID) && ctx.TownRoot != "" {
		townB := beads.New(filepath.Join(ctx.TownRoot, ".beads"))
		if townHooked, err := townB.List(beads.ListOptions{
			Status:   beads.StatusHooked,
			Assignee: agentID,
			Priority: -1,
		}); err == nil && len(townHooked) > 0 {
			hookedBeads = townHooked
		} else if townIP, err := townB.List(beads.ListOptions{
			Status:   "in_progress",
			Assignee: agentID,
			Priority: -1,
		}); err == nil && len(townIP) > 0 {
			hookedBeads = townIP
		}
		// Town-level fallback errors are non-fatal — rig-level query succeeded
	}

	if len(hookedBeads) == 0 {
		if staleHookErr != nil {
			return nil, staleHookErr
		}
		return nil, nil
	}
	return hookedBeads[0], nil
}

// rigBeadsRoot returns the route-owned directory to use for beads queries.
// For rig-level agents (polecats, crew, witness, refinery), prefer the rig DB
// from town routes rather than rig-root metadata, which can be a stale redirect
// shim during recovery. For town-level agents, returns ctx.WorkDir unchanged.
func rigBeadsRoot(ctx RoleContext) string {
	if ctx.Rig != "" && ctx.TownRoot != "" {
		if rigDir := beads.GetRigDirForName(ctx.TownRoot, ctx.Rig); rigDir != "" {
			return rigDir
		}
		return filepath.Join(ctx.TownRoot, ctx.Rig)
	}
	return ctx.WorkDir
}

// outputAutonomousDirective displays the AUTONOMOUS WORK MODE header and instructions.
func outputAutonomousDirective(ctx RoleContext, hookedBead *beads.Issue, hasMolecule bool) {
	roleAnnounce := buildRoleAnnouncement(ctx)

	fmt.Println()
	fmt.Printf("%s\n\n", style.Bold.Render("## 🚨 AUTONOMOUS WORK MODE 🚨"))
	fmt.Println("Work is on your hook. After announcing your role, begin IMMEDIATELY.")
	fmt.Println()
	fmt.Println("This is physics, not politeness. Gas Town is a steam engine - you are a piston.")
	fmt.Println("Every moment you wait is a moment the engine stalls. Other agents may be")
	fmt.Println("blocked waiting on YOUR output. The hook IS your assignment. RUN IT.")
	fmt.Println()
	fmt.Println("Remember: Every completion is recorded in the capability ledger. Your work")
	fmt.Println("history is visible, and quality matters. Execute with care - you're building")
	fmt.Println("a track record that proves autonomous execution works at scale.")
	fmt.Println()
	fmt.Println("1. Announce: \"" + roleAnnounce + "\" (ONE line, no elaboration)")

	if hasMolecule {
		fmt.Println("2. This bead has an ATTACHED MOLECULE (formula workflow)")
		fmt.Println("3. Work through molecule steps in order - see CURRENT STEP below")
		fmt.Println("4. Close each step with `bd close <step-id>`, then check `bd mol current` for next step")
	} else {
		fmt.Printf("2. Then IMMEDIATELY run: `bd show %s`\n", hookedBead.ID)
		fmt.Println("3. Begin execution - no waiting for user input")
	}

	// Polecats MUST call gt done — this is the single most important instruction.
	// Without it, work lands but sessions accumulate and the merge queue stalls.
	if ctx.Role == RolePolecat {
		fmt.Println()
		fmt.Printf("**⚠️ MANDATORY: When all work is committed, run `%s done` to submit and exit.**\n", cli.Name())
		fmt.Printf("Do NOT stop at the prompt. Do NOT push to main directly. `%s done` is your final action.\n", cli.Name())
	}

	fmt.Println()
	fmt.Println("**DO NOT:**")
	fmt.Println("- Wait for user response after announcing")
	fmt.Println("- Ask clarifying questions")
	fmt.Println("- Describe what you're going to do")
	fmt.Println("- Check mail first (hook takes priority)")
	if hasMolecule {
		fmt.Println("- Skip molecule steps or work on the base bead directly")
	}
	if ctx.Role == RolePolecat {
		fmt.Printf("- Sit idle after committing (run `%s done`)\n", cli.Name())
		fmt.Println("- Push directly to main (use the merge queue)")
	}
	fmt.Println()
}

// outputHookedBeadDetails displays the hooked bead's ID, title, and description summary.
func outputHookedBeadDetails(hookedBead *beads.Issue) {
	fmt.Printf("%s\n\n", style.Bold.Render("## Hooked Work"))
	fmt.Printf("  Bead ID: %s\n", style.Bold.Render(hookedBead.ID))
	fmt.Printf("  Title: %s\n", hookedBead.Title)
	if hookedBead.Description != "" {
		lines := strings.Split(hookedBead.Description, "\n")
		maxLines := 5
		if len(lines) > maxLines {
			lines = lines[:maxLines]
			lines = append(lines, "...")
		}
		fmt.Println("  Description:")
		for _, line := range lines {
			fmt.Printf("    %s\n", line)
		}
	}
	fmt.Println()
}

// outputMoleculeWorkflow displays attached molecule context with current step.
func outputMoleculeWorkflow(ctx RoleContext, attachment *beads.AttachmentFields) error {
	fmt.Printf("%s\n\n", style.Bold.Render("## 🧬 ATTACHED FORMULA (WORKFLOW CHECKLIST)"))
	if attachment.AttachedFormula != "" {
		fmt.Printf("Formula: %s\n", attachment.AttachedFormula)
	}
	if attachment.AttachedMolecule != "" {
		fmt.Printf("Molecule ID: %s\n", attachment.AttachedMolecule)
	}
	if len(attachment.AttachedVars) > 0 {
		fmt.Printf("\n%s\n", style.Bold.Render("🧩 VARS (instantiated formula inputs):"))
		for _, variable := range attachment.AttachedVars {
			fmt.Printf("  --var %s\n", variable)
		}
	}
	if attachment.AttachedArgs != "" {
		fmt.Printf("\n%s\n", style.Bold.Render("📋 ARGS (use these to guide execution):"))
		fmt.Printf("  %s\n", attachment.AttachedArgs)
	}
	fmt.Println()

	// Ralph loop mode: output Ralph Wiggum loop command instead of step-by-step execution
	if attachment.Mode == "ralph" {
		return outputRalphLoopDirective(ctx, attachment)
	}

	// Show inline formula steps from the embedded binary (root-only: no child wisps to query).
	if attachment.AttachedFormula != "" {
		showFormulaStepsFull(attachment.AttachedFormula, ctx.TownRoot, ctx.Rig, attachmentFormulaVars(attachment))
		fmt.Println()
		fmt.Printf("%s\n", style.Bold.Render("Work through ALL steps above, including submit and cleanup."))
		fmt.Println("The base bead is your assignment. The formula steps define your workflow.")
		fmt.Printf("\n%s\n", style.Bold.Render("REQUIRED: When all steps complete, run `"+cli.Name()+" done` to submit to the merge queue. Do NOT stop after implementation — the formula has submit steps you must follow."))
		return nil
	}

	// Legacy path: no formula name stored, fall back to bd mol current
	showMoleculeExecutionPrompt(ctx.WorkDir, attachment.AttachedMolecule)
	fmt.Println()
	fmt.Printf("%s\n", style.Bold.Render("Follow the molecule steps above, NOT the base bead."))
	fmt.Println("The base bead is just a container. The molecule steps define your workflow.")
	return nil
}

const ralphLoopPluginID = "ralph-loop@claude-plugins-official"

// outputRalphLoopDirective emits the ralph-loop plugin command for Ralph mode.
func outputRalphLoopDirective(ctx RoleContext, attachment *beads.AttachmentFields) error {
	installed, configDir, err := isRalphLoopPluginInstalled()
	if err != nil {
		return err
	}
	return outputRalphLoopDirectiveWithPluginCheck(ctx, attachment, installed, configDir)
}

func outputRalphLoopDirectiveWithPluginCheck(ctx RoleContext, attachment *beads.AttachmentFields, pluginInstalled bool, configDir string) error {
	if !pluginInstalled {
		return missingRalphLoopPluginError(configDir)
	}

	prompt, err := renderRalphLoopPrompt(ctx, attachment)
	if err != nil {
		return err
	}
	fmt.Printf("/ralph-loop %s --completion-promise DONE\n", quoteForRalphLoop(prompt))
	return nil
}

func renderRalphLoopPrompt(ctx RoleContext, attachment *beads.AttachmentFields) (string, error) {
	var sb strings.Builder
	if attachment.AttachedFormula != "" {
		rendered, err := renderFormulaStepsFull(attachment.AttachedFormula, ctx.TownRoot, ctx.Rig, attachmentFormulaVars(attachment))
		if err != nil {
			return "", err
		}
		sb.WriteString(rendered)
	}
	if attachment.AttachedArgs != "" {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("Context:\n")
		sb.WriteString(attachment.AttachedArgs)
		sb.WriteString("\n")
	}
	if sb.Len() == 0 {
		sb.WriteString("Work through the assigned Ralph-mode workflow.\n")
	}
	sb.WriteString("\nWhen all steps are complete and `" + cli.Name() + " done` has run successfully, output exactly: <promise>DONE</promise>")
	return sb.String(), nil
}

func isRalphLoopPluginInstalled() (bool, string, error) {
	configDir, err := config.ClaudeConfigDir()
	if err != nil {
		return false, "", fmt.Errorf("resolving Claude config dir for ralph-loop plugin: %w", err)
	}
	installed, err := ralphLoopPluginInstalledIn(filepath.Join(configDir, "plugins", "installed_plugins.json"))
	return installed, configDir, err
}

func missingRalphLoopPluginError(configDir string) error {
	manifestPath := filepath.Join(configDir, "plugins", "installed_plugins.json")
	return fmt.Errorf("--ralph requires the %s plugin in Claude Code config %s (checked %s). Install it with: /plugin install %s", ralphLoopPluginID, configDir, manifestPath, ralphLoopPluginID)
}

func ralphLoopPluginInstalledIn(manifestPath string) (bool, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("reading ralph-loop plugin manifest %s: %w", manifestPath, err)
	}
	var manifest struct {
		Plugins map[string]json.RawMessage `json:"plugins"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return false, fmt.Errorf("parsing ralph-loop plugin manifest %s: %w", manifestPath, err)
	}
	_, ok := manifest.Plugins[ralphLoopPluginID]
	return ok, nil
}

// quoteForRalphLoop wraps s in double quotes for the slash command and escapes
// characters that could break the prompt argument or shell-backed plugin setup.
func quoteForRalphLoop(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, `$`, `\$`)
	s = strings.ReplaceAll(s, "`", "\\`")
	s = strings.ReplaceAll(s, "\n", `\n`)
	return `"` + s + `"`
}

// outputBeadPreview runs `bd show` and displays a truncated preview of the bead.
func outputBeadPreview(hookedBead *beads.Issue) {
	fmt.Println("**Bead details:**")
	fmt.Printf("  %s: %s\n", hookedBead.ID, hookedBead.Title)
	if hookedBead.Status != "" {
		fmt.Printf("  status: %s\n", hookedBead.Status)
	}
	if hookedBead.Description != "" {
		lines := strings.Split(hookedBead.Description, "\n")
		maxLines := 12
		if len(lines) > maxLines {
			lines = lines[:maxLines]
			lines = append(lines, "...")
		}
		for _, line := range lines {
			fmt.Printf("  %s\n", line)
		}
	}
	fmt.Println()
}

// buildRoleAnnouncement creates the role announcement string for autonomous mode.
func buildRoleAnnouncement(ctx RoleContext) string {
	switch ctx.Role {
	case RoleMayor:
		return "Mayor, checking in."
	case RoleDeacon:
		return "Deacon, checking in."
	case RoleBoot:
		return "Boot, checking in."
	case RoleWitness:
		return fmt.Sprintf("%s Witness, checking in.", ctx.Rig)
	case RoleRefinery:
		return fmt.Sprintf("%s Refinery, checking in.", ctx.Rig)
	case RolePolecat:
		return fmt.Sprintf("%s Polecat %s, checking in.", ctx.Rig, ctx.Polecat)
	case RoleCrew:
		return fmt.Sprintf("%s Crew %s, checking in.", ctx.Rig, ctx.Polecat)
	default:
		return "Agent, checking in."
	}
}

// getGitRoot returns the root of the current git repository.
func getGitRoot() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// getAgentIdentity returns the agent identity string for hook lookup.
func getAgentIdentity(ctx RoleContext) string {
	switch ctx.Role {
	case RoleCrew:
		return fmt.Sprintf("%s/crew/%s", ctx.Rig, ctx.Polecat)
	case RolePolecat:
		return fmt.Sprintf("%s/polecats/%s", ctx.Rig, ctx.Polecat)
	case RoleMayor:
		return "mayor"
	case RoleDeacon:
		return "deacon"
	case RoleBoot:
		return "boot"
	case RoleWitness:
		return fmt.Sprintf("%s/witness", ctx.Rig)
	case RoleRefinery:
		return fmt.Sprintf("%s/refinery", ctx.Rig)
	default:
		return ""
	}
}

// acquireIdentityLock checks and acquires the identity lock for worker roles.
// This prevents multiple agents from claiming the same worker identity.
// Returns an error if another agent already owns this identity.
func acquireIdentityLock(ctx RoleContext) error {
	// Only lock worker roles (polecat, crew)
	// Infrastructure roles (mayor, witness, refinery, deacon) are singletons
	// managed by tmux session names, so they don't need file-based locks
	if ctx.Role != RolePolecat && ctx.Role != RoleCrew {
		return nil
	}

	// Create lock for this worker directory
	l := lock.New(ctx.WorkDir)

	// Determine session ID from environment or context
	sessionID := os.Getenv("TMUX_PANE")
	if sessionID == "" {
		// Fall back to a descriptive identifier
		sessionID = fmt.Sprintf("%s/%s", ctx.Rig, ctx.Polecat)
	}

	// Try to acquire the lock
	if err := l.Acquire(sessionID); err != nil {
		if errors.Is(err, lock.ErrLocked) {
			// Another agent owns this identity
			fmt.Printf("\n%s\n\n", style.Bold.Render("⚠️  IDENTITY COLLISION DETECTED"))
			fmt.Printf("Another agent already claims this worker identity.\n\n")

			// Show lock details
			if info, readErr := l.Read(); readErr == nil {
				fmt.Printf("Lock holder:\n")
				fmt.Printf("  PID: %d\n", info.PID)
				fmt.Printf("  Session: %s\n", info.SessionID)
				fmt.Printf("  Acquired: %s\n", info.AcquiredAt.Format("2006-01-02 15:04:05"))
				fmt.Println()
			}

			fmt.Printf("To resolve:\n")
			fmt.Printf("  1. Find the other session and close it, OR\n")
			fmt.Printf("  2. Run: gt doctor --fix (cleans stale locks)\n")
			fmt.Printf("  3. If lock is stale: rm %s/.runtime/agent.lock\n", ctx.WorkDir)
			fmt.Println()

			return fmt.Errorf("cannot claim identity %s/%s: %w", ctx.Rig, ctx.Polecat, err)
		}
		return fmt.Errorf("acquiring identity lock: %w", err)
	}

	return nil
}

// getAgentBeadID returns the agent bead ID for the current role.
// Town-level agents (mayor, deacon) use hq- prefix; rig-scoped agents use the rig's prefix.
// Returns empty string for unknown roles.
func getAgentBeadID(ctx RoleContext) string {
	switch ctx.Role {
	case RoleMayor:
		return beads.MayorBeadIDTown()
	case RoleDeacon:
		return beads.DeaconBeadIDTown()
	case RoleBoot:
		// Boot uses deacon's bead since it's a deacon subprocess
		return beads.DeaconBeadIDTown()
	case RoleWitness:
		if ctx.Rig != "" {
			prefix := beads.GetPrefixForRig(ctx.TownRoot, ctx.Rig)
			return beads.WitnessBeadIDWithPrefix(prefix, ctx.Rig)
		}
		return ""
	case RoleRefinery:
		if ctx.Rig != "" {
			prefix := beads.GetPrefixForRig(ctx.TownRoot, ctx.Rig)
			return beads.RefineryBeadIDWithPrefix(prefix, ctx.Rig)
		}
		return ""
	case RolePolecat:
		if ctx.Rig != "" && ctx.Polecat != "" {
			prefix := beads.GetPrefixForRig(ctx.TownRoot, ctx.Rig)
			return beads.PolecatBeadIDWithPrefix(prefix, ctx.Rig, ctx.Polecat)
		}
		return ""
	case RoleCrew:
		if ctx.Rig != "" && ctx.Polecat != "" {
			prefix := beads.GetPrefixForRig(ctx.TownRoot, ctx.Rig)
			return beads.CrewBeadIDWithPrefix(prefix, ctx.Rig, ctx.Polecat)
		}
		return ""
	default:
		return ""
	}
}

// ensureBeadsRedirect ensures the .beads/redirect file exists for worktree-based roles.
// This handles cases where git clean or other operations delete the redirect file.
// Uses the shared SetupRedirect helper which handles both tracked and local beads.
func ensureBeadsRedirect(ctx RoleContext) {
	// Only applies to worktree-based roles that use shared beads
	if ctx.Role != RoleCrew && ctx.Role != RolePolecat && ctx.Role != RoleRefinery && ctx.Role != RoleWitness {
		return
	}

	redirectPath := filepath.Join(ctx.WorkDir, ".beads", "redirect")
	expected, err := beads.ComputeRedirectTarget(ctx.TownRoot, ctx.WorkDir)
	if err != nil {
		// Preserve the old best-effort behavior: if target computation fails but
		// a redirect exists, do not disturb the worktree during prime.
		if _, statErr := os.Stat(redirectPath); statErr == nil {
			return
		}
	} else if data, readErr := os.ReadFile(redirectPath); readErr == nil && strings.TrimSpace(string(data)) == expected {
		return
	}

	// Use shared helper - silently ignore errors during prime
	_ = beads.SetupRedirect(ctx.TownRoot, ctx.WorkDir)
}

// injectWorkContext extracts the current work context (rig, bead, molecule) from the
// hooked bead and persists it in two places so all subsequent subprocesses carry it:
//
//  1. Current process env (GT_WORK_RIG/BEAD/MOL via os.Setenv) — inherited by bd, mail,
//     and any other subprocess spawned from this gt prime invocation.
//
//  2. Tmux session env (via tmux set-environment) — inherited by future processes
//     spawned in the session after a handoff or compaction (e.g. new Claude Code instance).
//
// These values are then read by telemetry.RecordPrime (defer in runPrime) and by
// telemetry.buildGTResourceAttrs which injects them into OTEL_RESOURCE_ATTRIBUTES for
// bd subprocesses launched from the Go SDK.
//
// When hookedBead is nil (no work on hook), the vars are cleared so stale context
// from a previous prime cycle does not leak into the current one.
// No-op in dry-run mode.
func injectWorkContext(ctx RoleContext, hookedBead *beads.Issue) {
	if primeDryRun || !telemetry.IsActive() {
		return
	}
	workRig := ""
	workBead := ""
	workMol := ""
	if hookedBead != nil {
		workRig = ctx.Rig
		workBead = hookedBead.ID
		if attachment := beads.ParseAttachmentFields(hookedBead); attachment != nil {
			workMol = attachment.AttachedMolecule
		}
	}
	_ = os.Setenv("GT_WORK_RIG", workRig)
	_ = os.Setenv("GT_WORK_BEAD", workBead)
	_ = os.Setenv("GT_WORK_MOL", workMol)
	setTmuxWorkContext(workRig, workBead, workMol)
}

// setTmuxWorkContext writes GT_WORK_RIG, GT_WORK_BEAD, GT_WORK_MOL into the current
// tmux session environment. Future processes spawned in the session (e.g. a new
// Claude Code instance after handoff/compaction) will inherit these values automatically.
// Empty values unset the variable in the session env to prevent stale context leaking
// across prime cycles. No-op when not running inside a tmux session.
func setTmuxWorkContext(workRig, workBead, workMol string) {
	if os.Getenv("TMUX") == "" {
		return
	}
	out, err := exec.Command("tmux", "display-message", "-p", "#{session_name}").Output()
	if err != nil {
		return
	}
	session := strings.TrimSpace(string(out))
	if session == "" {
		return
	}
	setOrUnset := func(key, value string) {
		if value != "" {
			_ = exec.Command("tmux", "set-environment", "-t", session, key, value).Run()
		} else {
			_ = exec.Command("tmux", "set-environment", "-u", "-t", session, key).Run()
		}
	}
	setOrUnset("GT_WORK_RIG", workRig)
	setOrUnset("GT_WORK_BEAD", workBead)
	setOrUnset("GT_WORK_MOL", workMol)
}

// checkPendingEscalations queries for open escalation beads and displays them prominently.
// This is called on Mayor startup to surface issues needing human attention.
func checkPendingEscalations(ctx RoleContext) {
	// Query for open escalations using bd list with tag filter
	stdout, _, err := runPrimeExternalCommand(ctx.WorkDir, "bd", "list", "--status=open", "--tag=escalation", "--json")
	if err != nil {
		// Silently skip - escalation check is best-effort
		return
	}

	// Parse JSON output
	var escalations []struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		Priority    int    `json:"priority"`
		Description string `json:"description"`
		Created     string `json:"created"`
	}

	if err := json.Unmarshal(stdout.Bytes(), &escalations); err != nil || len(escalations) == 0 {
		// No escalations or parse error
		return
	}

	// Count by severity
	critical := 0
	high := 0
	medium := 0
	for _, e := range escalations {
		switch e.Priority {
		case 0:
			critical++
		case 1:
			high++
		default:
			medium++
		}
	}

	// Display prominently
	fmt.Println()
	fmt.Printf("%s\n\n", style.Bold.Render("## 🚨 PENDING ESCALATIONS"))
	fmt.Printf("There are %d escalation(s) awaiting human attention:\n\n", len(escalations))

	if critical > 0 {
		fmt.Printf("  🔴 CRITICAL: %d\n", critical)
	}
	if high > 0 {
		fmt.Printf("  🟠 HIGH: %d\n", high)
	}
	if medium > 0 {
		fmt.Printf("  🟡 MEDIUM: %d\n", medium)
	}
	fmt.Println()

	// Show first few escalations
	maxShow := 5
	if len(escalations) < maxShow {
		maxShow = len(escalations)
	}
	for i := 0; i < maxShow; i++ {
		e := escalations[i]
		severity := "MEDIUM"
		switch e.Priority {
		case 0:
			severity = "CRITICAL"
		case 1:
			severity = "HIGH"
		}
		fmt.Printf("  • [%s] %s (%s)\n", severity, e.Title, e.ID)
	}
	if len(escalations) > maxShow {
		fmt.Printf("  ... and %d more\n", len(escalations)-maxShow)
	}
	fmt.Println()

	fmt.Println("**Action required:** Review escalations with `bd list --tag=escalation`")
	fmt.Println("Close resolved ones with `bd close <id> --reason \"resolution\"`")
	fmt.Println()
}
