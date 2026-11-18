package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/deacon"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/util"
)

const (
	defaultMainBranchTestInterval = 30 * time.Minute
	defaultMainBranchTestTimeout  = 10 * time.Minute
)

// MainBranchTestConfig holds configuration for the main_branch_test patrol.
// This patrol periodically runs quality gates on each rig's main branch to
// catch regressions from direct-to-main pushes, bad merges, or sequential
// merge conflicts that individually pass but break together.
type MainBranchTestConfig struct {
	Enabled     bool     `json:"enabled"`
	IntervalStr string   `json:"interval,omitempty"`
	TimeoutStr  string   `json:"timeout,omitempty"`
	Rigs        []string `json:"rigs,omitempty"`
}

func mainBranchTestInterval(cfg *DaemonPatrolConfig) time.Duration {
	if cfg != nil && cfg.Patrols != nil && cfg.Patrols.MainBranchTest != nil {
		if value := cfg.Patrols.MainBranchTest.IntervalStr; value != "" {
			if duration, err := time.ParseDuration(value); err == nil && duration > 0 {
				return duration
			}
		}
	}
	return defaultMainBranchTestInterval
}

func mainBranchTestTimeout(cfg *DaemonPatrolConfig) time.Duration {
	if cfg != nil && cfg.Patrols != nil && cfg.Patrols.MainBranchTest != nil {
		if value := cfg.Patrols.MainBranchTest.TimeoutStr; value != "" {
			if duration, err := time.ParseDuration(value); err == nil && duration > 0 {
				return duration
			}
		}
	}
	return defaultMainBranchTestTimeout
}

func mainBranchTestRigs(cfg *DaemonPatrolConfig) []string {
	if cfg != nil && cfg.Patrols != nil && cfg.Patrols.MainBranchTest != nil {
		return cfg.Patrols.MainBranchTest.Rigs
	}
	return nil
}

type validationCommand struct {
	Kind    string
	Label   string
	Command string
}

// rigGateConfig is the effective merge-queue validation configuration merged
// from committed .gastown/settings.json and local rig settings/config.json.
type rigGateConfig struct {
	SetupCommand    string
	Commands        []validationCommand
	RetryFlakyTests int
}

func loadRigGateConfig(rigPath string) (*rigGateConfig, error) {
	var localMQ *config.MergeQueueConfig
	settingsPath := config.RigSettingsPath(rigPath)
	if _, err := os.Stat(settingsPath); err == nil {
		settings, loadErr := config.LoadRigSettings(settingsPath)
		if loadErr != nil {
			return nil, fmt.Errorf("loading rig settings: %w", loadErr)
		}
		localMQ = settings.MergeQueue
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("checking rig settings: %w", err)
	}

	projectDir := filepath.Join(rigPath, "refinery", "rig")
	var repoMQ *config.MergeQueueConfig
	repoSettings, err := config.LoadRepoSettings(projectDir)
	if err != nil {
		return nil, err
	}
	if repoSettings != nil {
		repoMQ = repoSettings.MergeQueue
	}

	effective := config.MergeSettingsCommand(repoMQ, localMQ)
	if effective == nil {
		return nil, nil
	}
	result := &rigGateConfig{
		SetupCommand:    strings.TrimSpace(effective.SetupCommand),
		RetryFlakyTests: effective.RetryFlakyTests,
	}
	for _, command := range []validationCommand{
		{Kind: "build", Label: "build", Command: effective.BuildCommand},
		{Kind: "lint", Label: "lint", Command: effective.LintCommand},
		{Kind: "typecheck", Label: "typecheck", Command: effective.TypecheckCommand},
		{Kind: "test", Label: "test", Command: effective.TestCommand},
	} {
		command.Command = strings.TrimSpace(command.Command)
		if command.Command == "" {
			continue
		}
		if command.Kind == "test" && !effective.IsRunTestsEnabled() {
			continue
		}
		result.Commands = append(result.Commands, command)
	}
	if result.SetupCommand == "" && len(result.Commands) == 0 {
		return nil, nil
	}
	return result, nil
}

type mainCheckFailure struct {
	Kind           string
	Label          string
	Command        string
	ExitCode       int
	Evidence       string
	Infrastructure bool
}

func (f *mainCheckFailure) Error() string {
	if f == nil {
		return ""
	}
	return fmt.Sprintf("%s failed (exit %d): %s", f.Label, f.ExitCode, f.Evidence)
}

func (d *Daemon) runMainBranchTests() {
	if !d.isPatrolActive("main_branch_test") {
		return
	}

	d.logger.Printf("main_branch_test: starting patrol cycle")
	rigNames := d.getKnownRigs()
	if len(rigNames) == 0 {
		d.logger.Printf("main_branch_test: no rigs found")
		return
	}

	allowedRigs := mainBranchTestRigs(d.patrolConfig)
	timeout := mainBranchTestTimeout(d.patrolConfig)
	var tested, failed int
	var failures []string

	for _, rigName := range rigNames {
		if len(allowedRigs) > 0 && !sliceContains(allowedRigs, rigName) {
			continue
		}
		rigPath := filepath.Join(d.config.TownRoot, rigName)
		if err := d.testRigMainBranch(rigName, rigPath, timeout); err != nil {
			d.logger.Printf("main_branch_test: %s: FAILED: %v", rigName, err)
			failures = append(failures, fmt.Sprintf("%s: %v", rigName, err))
			failed++
		} else {
			d.logger.Printf("main_branch_test: %s: passed", rigName)
		}
		tested++
	}

	if len(failures) > 0 {
		msg := fmt.Sprintf("main branch test failures:\n%s", strings.Join(failures, "\n"))
		d.logger.Printf("main_branch_test: escalating %d failure(s)", len(failures))
		d.escalate("main_branch_test", msg)
	}
	d.logger.Printf("main_branch_test: patrol cycle complete (%d tested, %d failed)", tested, failed)
}

// testRigMainBranch validates current main, compares a failure against the last
// green commit, attributes the first bad commit, and submits a recovery event.
func (d *Daemon) testRigMainBranch(rigName, rigPath string, timeout time.Duration) error {
	gateCfg, err := loadRigGateConfig(rigPath)
	if err != nil {
		return fmt.Errorf("loading effective gate config: %w", err)
	}
	if gateCfg == nil {
		d.logger.Printf("main_branch_test: %s: no validation commands configured, skipping", rigName)
		return nil
	}

	defaultBranch := "main"
	rigCfg, rigCfgErr := rig.LoadRigConfig(rigPath)
	if rigCfgErr == nil && rigCfg.DefaultBranch != "" {
		defaultBranch = rigCfg.DefaultBranch
	}
	bareRepoPath := filepath.Join(rigPath, ".repo.git")
	if _, err := os.Stat(bareRepoPath); err != nil {
		return fmt.Errorf("infrastructure: bare repo unavailable at %s: %w", bareRepoPath, err)
	}

	ctx, cancel := context.WithTimeout(d.ctx, timeout)
	defer cancel()
	if output, err := runGit(ctx, bareRepoPath, "fetch", "origin", defaultBranch); err != nil {
		return fmt.Errorf("infrastructure: git fetch failed: %w (%s)", err, output)
	}
	headSHA, err := gitOutput(ctx, bareRepoPath, "rev-parse", "origin/"+defaultBranch)
	if err != nil {
		return fmt.Errorf("infrastructure: resolving main head: %w", err)
	}

	currentFailure, err := d.validateRigRef(ctx, rigName, rigPath, bareRepoPath, headSHA, gateCfg)
	if err != nil {
		return err
	}
	if currentFailure == nil {
		if err := deacon.RecordMainBranchValidation(d.config.TownRoot, rigName, headSHA, true); err != nil {
			return fmt.Errorf("recording green main: %w", err)
		}
		_, _ = deacon.ResolveValidationIncidents(d.config.TownRoot, "", rigName, "", "post-merge")
		return nil
	}
	if err := deacon.RecordMainBranchValidation(d.config.TownRoot, rigName, headSHA, false); err != nil {
		d.logger.Printf("main_branch_test: %s: warning: recording failed test state: %v", rigName, err)
	}
	if currentFailure.Infrastructure {
		return fmt.Errorf("infrastructure: %w", currentFailure)
	}

	mainState, err := deacon.MainBranchValidation(d.config.TownRoot, rigName)
	if err != nil {
		return fmt.Errorf("loading last-green state: %w", err)
	}
	if mainState.LastGreenSHA == "" {
		return fmt.Errorf("%w; no known-green baseline, so hosted repair was not dispatched", currentFailure)
	}

	baselineFailure, err := d.validateRigRef(ctx, rigName, rigPath, bareRepoPath, mainState.LastGreenSHA, gateCfg)
	if err != nil {
		return fmt.Errorf("infrastructure while checking last-green baseline: %w", err)
	}
	if baselineFailure != nil {
		return fmt.Errorf("%w; last-green baseline %s now also fails (%v), so this is infrastructure or pre-existing",
			currentFailure, shortMainSHA(mainState.LastGreenSHA), baselineFailure)
	}

	firstBadSHA, firstFailure, err := d.findFirstFailingCommit(
		ctx, rigName, rigPath, bareRepoPath, mainState.LastGreenSHA, headSHA, gateCfg, currentFailure,
	)
	if err != nil {
		return err
	}
	sourceIssue := sourceIssueFromCommit(ctx, bareRepoPath, firstBadSHA, rigCfg)
	result := deacon.ProcessValidationFailure(d.config.TownRoot, deacon.ValidationFailure{
		Rig:         rigName,
		SourceIssue: sourceIssue,
		Commit:      firstBadSHA,
		Phase:       "post-merge",
		Kind:        firstFailure.Kind,
		Command:     firstFailure.Command,
		ExitCode:    firstFailure.ExitCode,
		Summary:     fmt.Sprintf("%s regression on %s", firstFailure.Label, shortMainSHA(firstBadSHA)),
		Evidence: fmt.Sprintf("Last green: %s\nCurrent main: %s\nFirst bad: %s\n%s",
			mainState.LastGreenSHA, headSHA, firstBadSHA, firstFailure.Evidence),
	})
	if result.Error != nil {
		return fmt.Errorf("%w; validation recovery failed: %v", currentFailure, result.Error)
	}
	return fmt.Errorf("%w; recovery=%s incident=%s repair=%s",
		currentFailure, result.Action, result.IncidentID, result.RepairBead)
}

func (d *Daemon) findFirstFailingCommit(
	ctx context.Context,
	rigName, rigPath, bareRepoPath, lastGreen, headSHA string,
	gateCfg *rigGateConfig,
	headFailure *mainCheckFailure,
) (string, *mainCheckFailure, error) {
	output, err := gitOutput(ctx, bareRepoPath, "rev-list", "--reverse", lastGreen+".."+headSHA)
	if err != nil {
		return "", nil, fmt.Errorf("infrastructure: listing commits since last green: %w", err)
	}
	commits := strings.Fields(output)
	if len(commits) == 0 {
		return headSHA, headFailure, nil
	}
	for _, commit := range commits {
		if commit == headSHA {
			return headSHA, headFailure, nil
		}
		failure, validateErr := d.validateRigRef(ctx, rigName, rigPath, bareRepoPath, commit, gateCfg)
		if validateErr != nil {
			return "", nil, fmt.Errorf("infrastructure while attributing regression at %s: %w", shortMainSHA(commit), validateErr)
		}
		if failure != nil {
			if failure.Infrastructure {
				return "", nil, fmt.Errorf("infrastructure while attributing regression at %s: %w", shortMainSHA(commit), failure)
			}
			return commit, failure, nil
		}
	}
	return headSHA, headFailure, nil
}

func (d *Daemon) validateRigRef(
	ctx context.Context,
	rigName, rigPath, bareRepoPath, ref string,
	gateCfg *rigGateConfig,
) (*mainCheckFailure, error) {
	worktreePath := filepath.Join(rigPath, ".main-test-worktree")
	if _, err := os.Stat(worktreePath); err == nil {
		_, _ = runGit(context.Background(), bareRepoPath, "worktree", "remove", "--force", worktreePath)
	}
	if output, err := runGit(ctx, bareRepoPath, "worktree", "add", "--detach", worktreePath, ref); err != nil {
		return nil, fmt.Errorf("git worktree add at %s: %w (%s)", shortMainSHA(ref), err, output)
	}
	defer func() {
		if output, err := runGit(context.Background(), bareRepoPath, "worktree", "remove", "--force", worktreePath); err != nil {
			d.logger.Printf("main_branch_test: %s: warning: worktree cleanup failed: %v (%s)", rigName, err, output)
		}
	}()

	if gateCfg.SetupCommand != "" {
		if failure := d.runCommandOnWorktree(ctx, rigName, worktreePath, validationCommand{
			Kind: "build", Label: "setup", Command: gateCfg.SetupCommand,
		}, 0); failure != nil {
			return failure, nil
		}
	}
	for _, command := range gateCfg.Commands {
		if failure := d.runCommandOnWorktree(ctx, rigName, worktreePath, command, gateCfg.RetryFlakyTests); failure != nil {
			return failure, nil
		}
	}
	return nil, nil
}

func (d *Daemon) runCommandOnWorktree(
	ctx context.Context,
	rigName, workDir string,
	command validationCommand,
	retries int,
) *mainCheckFailure {
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			d.logger.Printf("main_branch_test: %s: retrying %s (%d/%d)", rigName, command.Label, attempt, retries)
		}
		d.logger.Printf("main_branch_test: %s: running %s: %s", rigName, command.Label, command.Command)
		cmd := exec.CommandContext(ctx, "sh", "-c", command.Command) //nolint:gosec // trusted rig config
		cmd.Dir = workDir
		cmd.Env = append(os.Environ(), "CI=true")
		util.SetDetachedProcessGroup(cmd)
		output, err := cmd.CombinedOutput()
		if err == nil {
			return nil
		}
		exitCode := -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		if len(lines) > 50 {
			lines = lines[len(lines)-50:]
		}
		failure := &mainCheckFailure{
			Kind:           command.Kind,
			Label:          command.Label,
			Command:        command.Command,
			ExitCode:       exitCode,
			Evidence:       strings.Join(lines, "\n"),
			Infrastructure: ctx.Err() != nil || exitCode == 126 || exitCode == 127,
		}
		if failure.Infrastructure || attempt == retries {
			return failure
		}
	}
	return nil
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	util.SetDetachedProcessGroup(cmd)
	output, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	output, err := runGit(ctx, dir, args...)
	if err != nil {
		return "", fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, output)
	}
	return output, nil
}

func sourceIssueFromCommit(ctx context.Context, bareRepoPath, sha string, rigCfg *rig.RigConfig) string {
	if rigCfg == nil || rigCfg.Beads == nil || rigCfg.Beads.Prefix == "" {
		return ""
	}
	message, err := gitOutput(ctx, bareRepoPath, "show", "-s", "--format=%B", sha)
	if err != nil {
		return ""
	}
	pattern := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(rigCfg.Beads.Prefix) + `-[a-z0-9]+\b`)
	return pattern.FindString(message)
}

func shortMainSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func sliceContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
