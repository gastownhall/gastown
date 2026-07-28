package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/deacon"
	"github.com/steveyegge/gastown/internal/dog"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/util"
)

type daemonModelCrashExecutor struct {
	daemon    *Daemon
	sendHuman func(string, string) error
	notify    func(string, string) error
}

func (e *daemonModelCrashExecutor) Sessions() ([]modelCrashSession, error) {
	names, err := e.daemon.tmux.ListSessions()
	if err != nil {
		return nil, err
	}
	result := make([]modelCrashSession, 0, len(names))
	dogManager := dog.NewManager(e.daemon.config.TownRoot, &config.RigsConfig{})
	for _, name := range names {
		agent, err := e.daemon.tmux.GetEnvironment(name, "GT_AGENT")
		if err != nil || agent != "opencode-local" {
			continue
		}
		identity, err := e.daemon.tmux.GetEnvironment(name, "GT_ROLE")
		if err != nil || identity == "" {
			continue
		}
		role := config.ExtractSimpleRole(identity)
		if role == "boot" || identity == "deacon/boot" {
			continue
		}
		dogName := ""
		if role == "dog" {
			dogName, err = e.daemon.tmux.GetEnvironment(name, "GT_DOG_NAME")
			if err != nil || dogName == "" {
				continue
			}
			identity = "deacon/dogs/" + dogName
		}
		candidate := modelCrashSession{
			Name:     name,
			Identity: identity,
			Role:     role,
			Agent:    agent,
		}
		switch role {
		case "polecat":
			workUnit, active, workErr := e.polecatWorkUnit(candidate)
			if workErr != nil {
				continue
			}
			if !active {
				result = append(result, candidate)
				continue
			}
			candidate.WorkUnit = workUnit
		case "dog":
			current, workErr := dogManager.Get(dogName)
			if workErr != nil {
				continue
			}
			workUnit, active := modelCrashDogWorkUnit(current)
			if !active {
				result = append(result, candidate)
				continue
			}
			candidate.WorkUnit = workUnit
		}
		instanceID, _ := e.daemon.tmux.GetEnvironment(name, "GT_RUN")
		if instanceID == "" {
			if info, infoErr := e.daemon.tmux.GetSessionInfo(name); infoErr == nil {
				instanceID = info.Created
			}
		}
		if instanceID == "" {
			// Session name alone is a conservative legacy fallback. Current
			// sessions have GT_RUN; older sessions at least need two scans.
			instanceID = name
		}
		workDir, _ := e.daemon.tmux.GetPaneWorkDir(name)
		output, captureErr := e.daemon.tmux.CapturePane(name, 80)
		if captureErr != nil {
			continue
		}
		candidate.InstanceID = instanceID
		candidate.WorkDir = workDir
		candidate.Output = output
		if hb := polecat.ReadSessionHeartbeat(e.daemon.config.TownRoot, name); hb != nil {
			candidate.HeartbeatAt = hb.Timestamp
		}
		result = append(result, candidate)
	}
	return result, nil
}

func (e *daemonModelCrashExecutor) Nudge(candidate modelCrashSession, message string) error {
	if candidate.Identity == "" {
		return fmt.Errorf("cannot nudge session with empty identity")
	}
	cmd := exec.Command(e.daemon.gtPath, "nudge", candidate.Identity, message)
	cmd.Dir = e.daemon.config.TownRoot
	util.SetDetachedProcessGroup(cmd)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("gt nudge %s: %w: %s",
			candidate.Identity, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (e *daemonModelCrashExecutor) RecoveryPolicy(candidate modelCrashSession) modelCrashRecoveryPolicy {
	result := defaultModelCrashRecoveryPolicy()
	if candidate.WorkDir == "" {
		return result
	}
	cfg, err := deacon.LoadModelEscalationConfig(candidate.WorkDir)
	if err != nil || cfg == nil || cfg.Recovery == nil {
		return result
	}
	if parsed, parseErr := time.ParseDuration(cfg.Recovery.NudgeAfter); parseErr == nil && parsed > 0 {
		result.NudgeAfter = parsed
	}
	if parsed, parseErr := time.ParseDuration(cfg.Recovery.LocalRestartAfter); parseErr == nil && parsed > 0 {
		result.LocalRestartAfter = parsed
	}
	if parsed, parseErr := time.ParseDuration(cfg.Recovery.GoEscalateAfter); parseErr == nil && parsed > 0 {
		result.GoEscalateAfter = parsed
	}
	if result.LocalRestartAfter < result.NudgeAfter {
		result.LocalRestartAfter = result.NudgeAfter
	}
	if result.GoEscalateAfter < result.LocalRestartAfter {
		result.GoEscalateAfter = result.LocalRestartAfter
	}
	return result
}

func (e *daemonModelCrashExecutor) LMWatchdog() (modelCrashWatchdog, error) {
	path := filepath.Join(e.daemon.config.TownRoot, "deacon", "lmstudio-watchdog.json")
	data, err := os.ReadFile(path) //nolint:gosec // path is derived from configured town root
	if err != nil {
		return modelCrashWatchdog{}, err
	}
	var watchdog modelCrashWatchdog
	if err := json.Unmarshal(data, &watchdog); err != nil {
		return modelCrashWatchdog{}, err
	}
	return watchdog, nil
}

func (e *daemonModelCrashExecutor) Restart(candidate modelCrashSession, agent string) error {
	if candidate.Role == "dog" {
		return e.restartDog(candidate, agent)
	}
	args, err := modelCrashRestartArgs(candidate, agent)
	if err != nil {
		return err
	}
	cmd := exec.Command(e.daemon.gtPath, args...)
	cmd.Dir = e.daemon.config.TownRoot
	util.SetDetachedProcessGroup(cmd)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("gt %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (e *daemonModelCrashExecutor) restartDog(candidate modelCrashSession, agent string) error {
	const prefix = "deacon/dogs/"
	dogName := strings.TrimPrefix(candidate.Identity, prefix)
	if dogName == "" || dogName == candidate.Identity || strings.Contains(dogName, "/") {
		return fmt.Errorf("invalid dog identity %q", candidate.Identity)
	}
	manager := dog.NewManager(e.daemon.config.TownRoot, &config.RigsConfig{})
	current, err := manager.Get(dogName)
	if err != nil {
		return fmt.Errorf("loading dog %s: %w", dogName, err)
	}
	sessions := dog.NewSessionManager(e.daemon.tmux, e.daemon.config.TownRoot, manager)
	if err := sessions.Stop(dogName, true); err != nil {
		return fmt.Errorf("stopping dog %s: %w", dogName, err)
	}
	if err := sessions.Start(dogName, dog.SessionStartOptions{
		WorkDesc:      current.Work,
		AgentOverride: agent,
	}); err != nil {
		return fmt.Errorf("starting dog %s: %w", dogName, err)
	}
	return nil
}

func modelCrashRestartArgs(candidate modelCrashSession, agent string) ([]string, error) {
	switch candidate.Role {
	case "mayor", "deacon":
		return []string{candidate.Role, "restart", "--agent", agent}, nil
	case "witness", "refinery":
		parts := strings.Split(candidate.Identity, "/")
		if len(parts) != 2 || parts[0] == "" {
			return nil, fmt.Errorf("invalid %s identity %q", candidate.Role, candidate.Identity)
		}
		return []string{candidate.Role, "restart", parts[0], "--agent", agent}, nil
	case "crew":
		parts := strings.Split(candidate.Identity, "/")
		if len(parts) != 3 || parts[0] == "" || parts[2] == "" {
			return nil, fmt.Errorf("invalid crew identity %q", candidate.Identity)
		}
		return []string{"crew", "restart", parts[0] + "/" + parts[2], "--agent", agent}, nil
	case "polecat":
		parts := strings.Split(candidate.Identity, "/")
		if len(parts) != 3 || parts[0] == "" || parts[2] == "" {
			return nil, fmt.Errorf("invalid polecat identity %q", candidate.Identity)
		}
		return []string{"session", "restart", parts[0] + "/" + parts[2], "--force", "--agent", agent}, nil
	default:
		return nil, fmt.Errorf("no safe restart primitive for role %q (%s)", candidate.Role, candidate.Identity)
	}
}

func (e *daemonModelCrashExecutor) ActivePolecat(candidate modelCrashSession) bool {
	workUnit, active, err := e.polecatWorkUnit(candidate)
	return err == nil && active && workUnit == strings.TrimSpace(candidate.WorkUnit)
}

func (e *daemonModelCrashExecutor) polecatWorkUnit(candidate modelCrashSession) (string, bool, error) {
	if candidate.Role != "polecat" {
		return "", false, fmt.Errorf("role %q is not a polecat", candidate.Role)
	}
	parts := strings.Split(candidate.Identity, "/")
	if len(parts) != 3 {
		return "", false, fmt.Errorf("invalid polecat identity %q", candidate.Identity)
	}
	rigName, polecatName := parts[0], parts[2]
	prefix := beads.GetPrefixForRig(e.daemon.config.TownRoot, rigName)
	agentBeadID := beads.PolecatBeadIDWithPrefix(prefix, rigName, polecatName)
	info, err := e.daemon.getAgentBeadInfo(agentBeadID)
	if err != nil {
		return "", false, err
	}
	workUnit, active := modelCrashPolecatWorkUnit(info, false, candidate.Identity, candidate.Identity)
	if !active {
		return "", false, nil
	}
	assignee, open, err := e.modelCrashHookAssignment(workUnit)
	if err != nil {
		return "", false, err
	}
	workUnit, active = modelCrashPolecatWorkUnit(info, !open, assignee, candidate.Identity)
	return workUnit, active, nil
}

func (e *daemonModelCrashExecutor) modelCrashHookAssignment(beadID string) (string, bool, error) {
	cmd := exec.Command(e.daemon.bdPath, "show", beadID, "--json") //nolint:gosec // daemon-owned binary and bead ID
	setSysProcAttr(cmd)
	cmd.Dir = e.daemon.config.TownRoot
	cmd.Env = bdReadOnlyRoutingEnv(e.daemon.config.TownRoot)
	output, err := cmd.Output()
	if err != nil {
		return "", false, fmt.Errorf("bd show %s: %w", beadID, err)
	}
	var issues []struct {
		Status   string `json:"status"`
		Assignee string `json:"assignee"`
	}
	if err := json.Unmarshal(output, &issues); err != nil {
		return "", false, fmt.Errorf("parsing work bead %s: %w", beadID, err)
	}
	if len(issues) == 0 {
		return "", false, fmt.Errorf("work bead not found: %s", beadID)
	}
	return strings.TrimSpace(issues[0].Assignee),
		!strings.EqualFold(strings.TrimSpace(issues[0].Status), "closed"), nil
}

func modelCrashPolecatWorkUnit(
	info *AgentBeadInfo,
	hookClosed bool,
	hookAssignee, candidateIdentity string,
) (string, bool) {
	if info == nil {
		return "", false
	}
	workUnit := strings.TrimSpace(info.HookBead)
	if workUnit == "" || hookClosed ||
		strings.TrimSpace(hookAssignee) != strings.TrimSpace(candidateIdentity) {
		return "", false
	}
	switch beads.AgentState(info.State) {
	case beads.AgentStateDone, beads.AgentStateNuked:
		return "", false
	}
	return workUnit, true
}

func modelCrashDogWorkUnit(candidate *dog.Dog) (string, bool) {
	if candidate == nil || candidate.State != dog.StateWorking {
		return "", false
	}
	workUnit := strings.TrimSpace(candidate.Work)
	return workUnit, workUnit != ""
}

func (e *daemonModelCrashExecutor) AllowsContinuation(candidate modelCrashSession, fromAgent, toAgent string) bool {
	if candidate.Role != "polecat" || candidate.WorkDir == "" {
		return false
	}
	policy, err := deacon.LoadModelEscalationConfig(candidate.WorkDir)
	if err != nil || policy == nil || !policy.Enabled {
		return false
	}
	for _, rule := range policy.Rules {
		if rule.FromAgent == fromAgent && rule.ToAgent == toAgent && rule.PromoteAfterFailures == 1 {
			return true
		}
	}
	return false
}

func (e *daemonModelCrashExecutor) Alert(key, message string) error {
	subject := "Model crash recovery requires attention"
	sendHuman := e.sendHuman
	if sendHuman == nil {
		sendHuman = e.sendHumanAlert
	}
	notify := e.notify
	if notify == nil {
		notify = sendModelCrashNotification
	}
	mailErr := sendHuman(subject, message)
	_ = notify(subject, message)
	if mailErr != nil {
		return fmt.Errorf("sending model crash alert %s: %w", key, mailErr)
	}
	return nil
}

func (e *daemonModelCrashExecutor) sendHumanAlert(subject, message string) error {
	cmd := exec.Command(e.daemon.gtPath, modelCrashHumanAlertArgs(subject, message)...)
	cmd.Dir = e.daemon.config.TownRoot
	util.SetDetachedProcessGroup(cmd)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func modelCrashHumanAlertArgs(subject, message string) []string {
	return []string{"mail", "send", "--human", "-s", subject, "-m", message}
}

func sendModelCrashNotification(title, message string) error {
	if goruntime.GOOS != "darwin" {
		return nil
	}
	script := "display notification " + strconv.Quote(message) + " with title " + strconv.Quote(title)
	cmd := exec.Command("osascript", "-e", script)
	util.SetDetachedProcessGroup(cmd)
	return cmd.Run()
}
