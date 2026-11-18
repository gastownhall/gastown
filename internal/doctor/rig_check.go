package doctor

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/git"
)

// RigIsGitRepoCheck verifies the rig has a valid mayor/rig git clone.
// Note: The rig directory itself is not a git repo - it contains clones.
type RigIsGitRepoCheck struct {
	BaseCheck
}

// NewRigIsGitRepoCheck creates a new rig git repo check.
func NewRigIsGitRepoCheck() *RigIsGitRepoCheck {
	return &RigIsGitRepoCheck{
		BaseCheck: BaseCheck{
			CheckName:        "rig-is-git-repo",
			CheckDescription: "Verify rig has a valid mayor/rig git clone",
			CheckCategory:    CategoryRig,
		},
	}
}

// Run checks if the rig has a valid mayor/rig git clone.
func (c *RigIsGitRepoCheck) Run(ctx *CheckContext) *CheckResult {
	rigPath := ctx.RigPath()
	if rigPath == "" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "No rig specified",
		}
	}

	// Check mayor/rig/ which is the authoritative clone for the rig
	mayorRigPath := filepath.Join(rigPath, "mayor", "rig")
	gitPath := filepath.Join(mayorRigPath, ".git")
	info, err := os.Stat(gitPath)
	if os.IsNotExist(err) {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "No mayor/rig clone found",
			Details: []string{fmt.Sprintf("Missing: %s", gitPath)},
			FixHint: "Clone the repository to mayor/rig/",
		}
	}
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("Cannot access mayor/rig/.git: %v", err),
		}
	}

	// Verify git status works
	cmd := exec.Command("git", "-C", mayorRigPath, "status", "--porcelain")
	if err := cmd.Run(); err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "git status failed on mayor/rig",
			Details: []string{fmt.Sprintf("Error: %v", err)},
			FixHint: "Check git configuration and repository integrity",
		}
	}

	gitType := "clone"
	if info.Mode().IsRegular() {
		gitType = "worktree"
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: fmt.Sprintf("Valid mayor/rig %s", gitType),
	}
}

// GitExcludeConfiguredCheck verifies .git/info/exclude has Gas Town directories.
type GitExcludeConfiguredCheck struct {
	FixableCheck
	missingEntries []string
	excludePath    string
}

// NewGitExcludeConfiguredCheck creates a new git exclude check.
func NewGitExcludeConfiguredCheck() *GitExcludeConfiguredCheck {
	return &GitExcludeConfiguredCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "git-exclude-configured",
				CheckDescription: "Check .git/info/exclude has Gas Town directories",
				CheckCategory:    CategoryRig,
			},
		},
	}
}

// requiredExcludes returns the directories that should be excluded.
func (c *GitExcludeConfiguredCheck) requiredExcludes() []string {
	return []string{"/polecats/", "/witness/", "/refinery/", "/mayor/"}
}

// Run checks if .git/info/exclude contains required entries.
func (c *GitExcludeConfiguredCheck) Run(ctx *CheckContext) *CheckResult {
	rigPath := ctx.RigPath()
	if rigPath == "" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "No rig specified",
		}
	}

	// Check mayor/rig/ which is the authoritative clone
	mayorRigPath := filepath.Join(rigPath, "mayor", "rig")
	gitDir := filepath.Join(mayorRigPath, ".git")
	info, err := os.Stat(gitDir)
	if os.IsNotExist(err) {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "No mayor/rig clone found",
			FixHint: "Run rig-is-git-repo check first",
		}
	}

	// If .git is a file (worktree), read the actual git dir
	if info.Mode().IsRegular() {
		content, err := os.ReadFile(gitDir)
		if err != nil {
			return &CheckResult{
				Name:    c.Name(),
				Status:  StatusError,
				Message: fmt.Sprintf("Cannot read .git file: %v", err),
			}
		}
		// Format: "gitdir: /path/to/actual/git/dir"
		line := strings.TrimSpace(string(content))
		if strings.HasPrefix(line, "gitdir: ") {
			gitDir = strings.TrimPrefix(line, "gitdir: ")
			// Resolve relative paths
			if !filepath.IsAbs(gitDir) {
				gitDir = filepath.Join(rigPath, gitDir)
			}
		}
	}

	c.excludePath = filepath.Join(gitDir, "info", "exclude")

	// Read existing excludes
	existing := make(map[string]bool)
	if file, err := os.Open(c.excludePath); err == nil {
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" && !strings.HasPrefix(line, "#") {
				existing[line] = true
			}
		}
		_ = file.Close() //nolint:gosec // G104: best-effort close
	}

	// Check for missing entries. Accept either anchored (/refinery/) or
	// legacy un-anchored (refinery/) forms — the un-anchored form is overly
	// broad but still covers the required directory.
	c.missingEntries = nil
	for _, required := range c.requiredExcludes() {
		unanchored := strings.TrimPrefix(required, "/")
		if !existing[required] && !existing[unanchored] {
			c.missingEntries = append(c.missingEntries, required)
		}
	}

	if len(c.missingEntries) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "Git exclude properly configured",
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: fmt.Sprintf("%d Gas Town directories not excluded", len(c.missingEntries)),
		Details: []string{fmt.Sprintf("Missing: %s", strings.Join(c.missingEntries, ", "))},
		FixHint: "Run 'gt doctor --fix' to add missing entries",
	}
}

// Fix appends missing entries to .git/info/exclude.
func (c *GitExcludeConfiguredCheck) Fix(ctx *CheckContext) error {
	if len(c.missingEntries) == 0 {
		return nil
	}

	// Ensure info directory exists
	infoDir := filepath.Dir(c.excludePath)
	if err := os.MkdirAll(infoDir, 0755); err != nil {
		return fmt.Errorf("failed to create info directory: %w", err)
	}

	// Append missing entries
	f, err := os.OpenFile(c.excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("failed to open exclude file: %w", err)
	}
	defer f.Close()

	// Add a header comment if file is empty or new
	info, _ := f.Stat()
	if info.Size() == 0 {
		if _, err := f.WriteString("# Gas Town directories\n"); err != nil {
			return err
		}
	} else {
		// Add newline before new entries
		if _, err := f.WriteString("\n# Gas Town directories\n"); err != nil {
			return err
		}
	}

	for _, entry := range c.missingEntries {
		if _, err := f.WriteString(entry + "\n"); err != nil {
			return err
		}
	}

	return nil
}

// HooksPathConfiguredCheck verifies all clones have core.hooksPath set to .githooks.
// This ensures the pre-push hook blocks pushes to invalid branches (no internal PRs).
type HooksPathConfiguredCheck struct {
	FixableCheck
	unconfiguredClones []string
}

// NewHooksPathConfiguredCheck creates a new hooks path check.
func NewHooksPathConfiguredCheck() *HooksPathConfiguredCheck {
	return &HooksPathConfiguredCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "hooks-path-configured",
				CheckDescription: "Check core.hooksPath is set for all clones",
				CheckCategory:    CategoryRig,
			},
		},
	}
}

// Run checks if all clones have core.hooksPath configured.
func (c *HooksPathConfiguredCheck) Run(ctx *CheckContext) *CheckResult {
	rigPath := ctx.RigPath()
	if rigPath == "" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "No rig specified",
		}
	}

	c.unconfiguredClones = nil

	// Check all clone locations
	clonePaths := []string{
		filepath.Join(rigPath, "mayor", "rig"),
		filepath.Join(rigPath, "refinery", "rig"),
	}

	// Add crew clones
	crewDir := filepath.Join(rigPath, "crew")
	if entries, err := os.ReadDir(crewDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				clonePaths = append(clonePaths, filepath.Join(crewDir, entry.Name()))
			}
		}
	}

	// Add polecat clones
	polecatDir := filepath.Join(rigPath, "polecats")
	if entries, err := os.ReadDir(polecatDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				clonePaths = append(clonePaths, filepath.Join(polecatDir, entry.Name()))
			}
		}
	}

	for _, clonePath := range clonePaths {
		// Skip if not a git repo
		if _, err := os.Stat(filepath.Join(clonePath, ".git")); os.IsNotExist(err) {
			continue
		}

		// Skip if no .githooks directory exists
		if _, err := os.Stat(filepath.Join(clonePath, ".githooks")); os.IsNotExist(err) {
			continue
		}

		// Check core.hooksPath
		cmd := exec.Command("git", "-C", clonePath, "config", "--get", "core.hooksPath")
		output, err := cmd.Output()
		if err != nil || strings.TrimSpace(string(output)) != ".githooks" {
			// Get relative path for cleaner output
			relPath, _ := filepath.Rel(rigPath, clonePath)
			if relPath == "" {
				relPath = clonePath
			}
			c.unconfiguredClones = append(c.unconfiguredClones, clonePath)
		}
	}

	if len(c.unconfiguredClones) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "All clones have hooks configured",
		}
	}

	// Build details with relative paths
	var details []string
	for _, clonePath := range c.unconfiguredClones {
		relPath, _ := filepath.Rel(rigPath, clonePath)
		if relPath == "" {
			relPath = clonePath
		}
		details = append(details, relPath)
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: fmt.Sprintf("%d clone(s) missing hooks configuration", len(c.unconfiguredClones)),
		Details: details,
		FixHint: "Run 'gt doctor --fix' to configure hooks",
	}
}

// Fix configures core.hooksPath for all unconfigured clones.
func (c *HooksPathConfiguredCheck) Fix(ctx *CheckContext) error {
	for _, clonePath := range c.unconfiguredClones {
		cmd := exec.Command("git", "-C", clonePath, "config", "core.hooksPath", ".githooks")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to configure hooks for %s: %w", clonePath, err)
		}
	}
	return nil
}

// WitnessExistsCheck verifies the witness directory structure exists.
type WitnessExistsCheck struct {
	FixableCheck
	rigPath     string
	needsCreate bool
}

// CanFix is state-dependent: creating a missing directory is safe, but
// replacing an unexpected file at witness/ requires manual inspection.
func (c *WitnessExistsCheck) CanFix() bool {
	return c.needsCreate
}

// NewWitnessExistsCheck creates a new witness exists check.
func NewWitnessExistsCheck() *WitnessExistsCheck {
	return &WitnessExistsCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "witness-exists",
				CheckDescription: "Verify witness/ directory structure exists",
				CheckCategory:    CategoryRig,
			},
		},
	}
}

// Run checks if the witness directory structure exists.
func (c *WitnessExistsCheck) Run(ctx *CheckContext) *CheckResult {
	c.rigPath = ctx.RigPath()
	if c.rigPath == "" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "No rig specified",
		}
	}

	witnessDir := filepath.Join(c.rigPath, "witness")
	var issues []string
	misplacedRootGit := false
	c.needsCreate = false

	// witness/ is the canonical clone-free working directory. Older towns may
	// still have a witness/rig git clone; the witness manager supports that
	// layout, but a clone is no longer a structural requirement. Runtime
	// settings and hooks are intentionally owned by their dedicated checks.
	info, err := os.Stat(witnessDir)
	if os.IsNotExist(err) {
		issues = append(issues, "Missing: witness/")
		c.needsCreate = true
	} else if err != nil {
		issues = append(issues, fmt.Sprintf("Cannot inspect witness/: %v", err))
	} else if !info.IsDir() {
		issues = append(issues, "Invalid: witness/ is not a directory")
	} else {
		// Canonical Witness homes are clone-free at witness/ itself. A Git
		// marker here means a product worktree was misplaced one level too
		// high; only the supported legacy witness/rig/.git layout may contain
		// product Git metadata.
		rootGit := filepath.Join(witnessDir, ".git")
		if _, err := os.Lstat(rootGit); err == nil {
			misplacedRootGit = true
			issues = append(issues,
				"Invalid: witness/.git is a misplaced product worktree; canonical witness/ must be clone-free (legacy Git belongs at witness/rig/.git)")
		} else if !os.IsNotExist(err) {
			issues = append(issues, fmt.Sprintf("Cannot inspect witness/.git: %v", err))
		}
	}

	if len(issues) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "Witness structure exists",
		}
	}

	fixHint := "Replace the invalid witness/ path with a directory after preserving any file contents"
	if c.needsCreate {
		fixHint = "Run 'gt doctor --fix' to create missing structure"
	} else if misplacedRootGit {
		fixHint = "Preserve any product changes, then remove the misplaced witness/.git worktree; use clone-free witness/ or supported legacy witness/rig/.git"
	}
	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: "Witness structure incomplete",
		Details: issues,
		FixHint: fixHint,
	}
}

// Fix creates missing witness structure.
func (c *WitnessExistsCheck) Fix(ctx *CheckContext) error {
	if !c.needsCreate {
		return ErrCannotFix
	}
	witnessDir := filepath.Join(c.rigPath, "witness")

	if c.needsCreate {
		if err := os.MkdirAll(witnessDir, 0755); err != nil {
			return fmt.Errorf("failed to create witness/: %w", err)
		}
	}

	return nil
}

// RefineryExistsCheck verifies the refinery directory structure exists.
type RefineryExistsCheck struct {
	FixableCheck
	rigPath     string
	needsCreate bool
	needsClone  bool
	needsMail   bool
}

// NewRefineryExistsCheck creates a new refinery exists check.
func NewRefineryExistsCheck() *RefineryExistsCheck {
	return &RefineryExistsCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "refinery-exists",
				CheckDescription: "Verify refinery/ directory structure exists",
				CheckCategory:    CategoryRig,
			},
		},
	}
}

// Run checks if the refinery directory structure exists.
func (c *RefineryExistsCheck) Run(ctx *CheckContext) *CheckResult {
	c.rigPath = ctx.RigPath()
	if c.rigPath == "" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "No rig specified",
		}
	}

	refineryDir := filepath.Join(c.rigPath, "refinery")
	rigClone := filepath.Join(refineryDir, "rig")
	mailInbox := filepath.Join(refineryDir, "mail", "inbox.jsonl")

	var issues []string
	c.needsCreate = false
	c.needsClone = false
	c.needsMail = false

	// Check refinery/ directory
	if _, err := os.Stat(refineryDir); os.IsNotExist(err) {
		issues = append(issues, "Missing: refinery/")
		c.needsCreate = true
	} else {
		// Check refinery/rig/ clone
		rigGit := filepath.Join(rigClone, ".git")
		if _, err := os.Stat(rigGit); os.IsNotExist(err) {
			issues = append(issues, "Missing: refinery/rig/ (git clone)")
			c.needsClone = true
		}

		// Check refinery/mail/inbox.jsonl
		if _, err := os.Stat(mailInbox); os.IsNotExist(err) {
			issues = append(issues, "Missing: refinery/mail/inbox.jsonl")
			c.needsMail = true
		}
	}

	if len(issues) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "Refinery structure exists",
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: "Refinery structure incomplete",
		Details: issues,
		FixHint: "Run 'gt doctor --fix' to create missing structure",
	}
}

// Fix creates missing refinery structure.
func (c *RefineryExistsCheck) Fix(ctx *CheckContext) error {
	refineryDir := filepath.Join(c.rigPath, "refinery")

	if c.needsCreate {
		if err := os.MkdirAll(refineryDir, 0755); err != nil {
			return fmt.Errorf("failed to create refinery/: %w", err)
		}
	}

	if c.needsMail {
		mailDir := filepath.Join(refineryDir, "mail")
		if err := os.MkdirAll(mailDir, 0755); err != nil {
			return fmt.Errorf("failed to create refinery/mail/: %w", err)
		}
		inboxPath := filepath.Join(mailDir, "inbox.jsonl")
		if err := os.WriteFile(inboxPath, []byte{}, 0644); err != nil {
			return fmt.Errorf("failed to create inbox.jsonl: %w", err)
		}
	}

	// Auto-repair refinery worktree from shared bare repo (.repo.git).
	// The refinery/rig is a worktree (not a full clone), so we don't need
	// the repo URL -- we just create a worktree from the local bare repo.
	if c.needsClone {
		bareRepoPath := filepath.Join(c.rigPath, ".repo.git")
		if _, err := os.Stat(bareRepoPath); os.IsNotExist(err) {
			return fmt.Errorf("cannot auto-create refinery/rig/ worktree: bare repo not found at %s", bareRepoPath)
		}

		bareGit := git.NewGitWithDir(bareRepoPath, "")
		_ = bareGit.WorktreePrune()

		rigClone := filepath.Join(refineryDir, "rig")
		// Detect default branch from rig config
		rigCfgPath := filepath.Join(c.rigPath, "settings", "rig.json")
		defaultBranch := "main"
		if data, err := os.ReadFile(rigCfgPath); err == nil {
			var cfg struct {
				DefaultBranch string `json:"default_branch"`
			}
			if json.Unmarshal(data, &cfg) == nil && cfg.DefaultBranch != "" {
				defaultBranch = cfg.DefaultBranch
			}
		}

		if err := bareGit.WorktreeAddExisting(rigClone, defaultBranch); err != nil {
			return fmt.Errorf("creating refinery worktree from bare repo: %w", err)
		}

		// Configure hooks path
		refineryGit := git.NewGit(rigClone)
		_ = refineryGit.ConfigureHooksPath()
	}

	return nil
}

// MayorCloneExistsCheck verifies the mayor/rig clone exists.
type MayorCloneExistsCheck struct {
	FixableCheck
	rigPath     string
	needsCreate bool
	needsClone  bool
}

// NewMayorCloneExistsCheck creates a new mayor clone check.
func NewMayorCloneExistsCheck() *MayorCloneExistsCheck {
	return &MayorCloneExistsCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "mayor-clone-exists",
				CheckDescription: "Verify mayor/rig/ git clone exists",
				CheckCategory:    CategoryRig,
			},
		},
	}
}

// Run checks if the mayor/rig clone exists.
func (c *MayorCloneExistsCheck) Run(ctx *CheckContext) *CheckResult {
	c.rigPath = ctx.RigPath()
	if c.rigPath == "" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "No rig specified",
		}
	}

	mayorDir := filepath.Join(c.rigPath, "mayor")
	rigClone := filepath.Join(mayorDir, "rig")

	var issues []string
	c.needsCreate = false
	c.needsClone = false

	// Check mayor/ directory
	if _, err := os.Stat(mayorDir); os.IsNotExist(err) {
		issues = append(issues, "Missing: mayor/")
		c.needsCreate = true
	} else {
		// Check mayor/rig/ clone
		rigGit := filepath.Join(rigClone, ".git")
		if _, err := os.Stat(rigGit); os.IsNotExist(err) {
			issues = append(issues, "Missing: mayor/rig/ (git clone)")
			c.needsClone = true
		}
	}

	if len(issues) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "Mayor clone exists",
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: "Mayor structure incomplete",
		Details: issues,
		FixHint: "Run 'gt doctor --fix' to create structure (clone requires repo URL)",
	}
}

// Fix creates missing mayor structure.
func (c *MayorCloneExistsCheck) Fix(ctx *CheckContext) error {
	mayorDir := filepath.Join(c.rigPath, "mayor")

	if c.needsCreate {
		if err := os.MkdirAll(mayorDir, 0755); err != nil {
			return fmt.Errorf("failed to create mayor/: %w", err)
		}
	}

	// Note: Cannot auto-fix clone without knowing the repo URL
	if c.needsClone {
		return fmt.Errorf("cannot auto-create mayor/rig/ clone (requires repo URL)")
	}

	return nil
}

// PolecatClonesValidCheck verifies each polecat directory is a valid clone.
type PolecatClonesValidCheck struct {
	BaseCheck
}

// NewPolecatClonesValidCheck creates a new polecat clones check.
func NewPolecatClonesValidCheck() *PolecatClonesValidCheck {
	return &PolecatClonesValidCheck{
		BaseCheck: BaseCheck{
			CheckName:        "polecat-clones-valid",
			CheckDescription: "Verify polecat directories are valid git clones",
			CheckCategory:    CategoryRig,
		},
	}
}

// Run checks if each polecat directory is a valid git clone.
func (c *PolecatClonesValidCheck) Run(ctx *CheckContext) *CheckResult {
	rigPath := ctx.RigPath()
	if rigPath == "" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "No rig specified",
		}
	}

	polecatsDir := filepath.Join(rigPath, "polecats")
	entries, err := os.ReadDir(polecatsDir)
	if os.IsNotExist(err) {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No polecats/ directory (none deployed)",
		}
	}
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("Cannot read polecats/: %v", err),
		}
	}

	var issues []string
	var warnings []string
	validCount := 0

	// Get rig name for new structure path detection
	rigName := ctx.RigName

	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		polecatName := entry.Name()

		// Determine worktree path (handle both new and old structures)
		// New structure: polecats/<name>/<rigname>/
		// Old structure: polecats/<name>/
		polecatPath := filepath.Join(polecatsDir, polecatName, rigName)
		if _, err := os.Stat(polecatPath); os.IsNotExist(err) {
			polecatPath = filepath.Join(polecatsDir, polecatName)
		}

		// Check if it's a git clone
		gitPath := filepath.Join(polecatPath, ".git")
		if _, err := os.Stat(gitPath); os.IsNotExist(err) {
			issues = append(issues, fmt.Sprintf("%s: not a git clone", polecatName))
			continue
		}

		// Verify git status works and check for uncommitted changes
		cmd := exec.Command("git", "-C", polecatPath, "status", "--porcelain")
		output, err := cmd.Output()
		if err != nil {
			issues = append(issues, fmt.Sprintf("%s: git status failed", polecatName))
			continue
		}

		if len(output) > 0 {
			warnings = append(warnings, fmt.Sprintf("%s: has uncommitted changes", polecatName))
		}

		// Check if on a polecat branch
		cmd = exec.Command("git", "-C", polecatPath, "branch", "--show-current")
		branchOutput, err := cmd.Output()
		if err == nil {
			branch := strings.TrimSpace(string(branchOutput))
			if !strings.HasPrefix(branch, constants.BranchPolecatPrefix) {
				warnings = append(warnings, fmt.Sprintf("%s: on branch '%s' (expected %s*)", polecatName, branch, constants.BranchPolecatPrefix))
			}
		}

		validCount++
	}

	if len(issues) > 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("%d polecat(s) invalid", len(issues)),
			Details: append(issues, warnings...),
			FixHint: "Cannot auto-fix (data loss risk)",
		}
	}

	if len(warnings) > 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("%d polecat(s) valid, %d warning(s)", validCount, len(warnings)),
			Details: warnings,
		}
	}

	if validCount == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No polecats deployed",
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: fmt.Sprintf("%d polecat(s) valid", validCount),
	}
}

// BeadsConfigValidCheck verifies beads configuration if .beads/ exists.
type BeadsConfigValidCheck struct {
	FixableCheck
	rigPath   string
	needsSync bool
}

// NewBeadsConfigValidCheck creates a new beads config check.
func NewBeadsConfigValidCheck() *BeadsConfigValidCheck {
	return &BeadsConfigValidCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "beads-config-valid",
				CheckDescription: "Verify beads configuration if .beads/ exists",
				CheckCategory:    CategoryRig,
			},
		},
	}
}

// Run checks if beads is properly configured.
func (c *BeadsConfigValidCheck) Run(ctx *CheckContext) *CheckResult {
	c.rigPath = ctx.RigPath()
	if c.rigPath == "" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "No rig specified",
		}
	}

	beadsDir := filepath.Join(c.rigPath, ".beads")
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No .beads/ directory (beads not configured)",
		}
	}

	// Check if bd command works
	cmd := exec.Command("bd", "stats", "--json")
	cmd.Dir = c.rigPath
	if err := cmd.Run(); err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "bd command failed",
			Details: []string{fmt.Sprintf("Error: %v", err)},
			FixHint: "Check beads installation and .beads/ configuration",
		}
	}

	// Note: With Dolt backend, there's no sync status to check.
	// Beads changes are persisted immediately.

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: "Beads configured and accessible",
	}
}

// Fix is a no-op with Dolt backend (no sync needed).
func (c *BeadsConfigValidCheck) Fix(ctx *CheckContext) error {
	// With Dolt backend, beads changes are persisted immediately - no sync needed
	return nil
}

// BeadsRedirectCheck verifies that rig-level beads redirect exists for tracked beads.
// When a repo has .beads/ tracked in git (at mayor/rig/.beads), the rig root needs
// a redirect file pointing to that location.
type BeadsRedirectCheck struct {
	FixableCheck
}

// NewBeadsRedirectCheck creates a new beads redirect check.
func NewBeadsRedirectCheck() *BeadsRedirectCheck {
	return &BeadsRedirectCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "beads-redirect",
				CheckDescription: "Verify rig-level beads redirect for tracked beads",
				CheckCategory:    CategoryRig,
			},
		},
	}
}

// Run checks if the rig-level beads redirect exists when needed.
func (c *BeadsRedirectCheck) Run(ctx *CheckContext) *CheckResult {
	// Only applies when checking a specific rig
	if ctx.RigName == "" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No rig specified (skipping redirect check)",
		}
	}

	rigPath := ctx.RigPath()
	mayorRigBeads := filepath.Join(rigPath, "mayor", "rig", ".beads")
	rigBeadsDir := filepath.Join(rigPath, ".beads")
	redirectPath := filepath.Join(rigBeadsDir, "redirect")

	// Check if this rig has tracked beads (mayor/rig/.beads exists)
	if _, err := os.Stat(mayorRigBeads); os.IsNotExist(err) {
		// No tracked beads - check if rig/.beads exists (local beads)
		if _, err := os.Stat(rigBeadsDir); os.IsNotExist(err) {
			return &CheckResult{
				Name:    c.Name(),
				Status:  StatusError,
				Message: "No .beads directory found at rig root",
				Details: []string{
					"Beads database not initialized for this rig",
					"This prevents issue tracking for this rig",
				},
				FixHint: "Run 'gt doctor --fix --rig " + ctx.RigName + "' to initialize beads",
			}
		}
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "Rig uses local beads (no redirect needed)",
		}
	}

	// Tracked beads exist - check for conflicting local beads
	hasLocalData := hasBeadsData(rigBeadsDir)
	redirectExists := false
	if _, err := os.Stat(redirectPath); err == nil {
		redirectExists = true
	}

	// Case: Local beads directory has actual data (not just redirect)
	if hasLocalData && !redirectExists {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "Conflicting local beads found with tracked beads",
			Details: []string{
				"Tracked beads exist at: mayor/rig/.beads",
				"Local beads with data exist at: .beads/",
				"Fix will remove local beads and create redirect to tracked beads",
			},
			FixHint: "Run 'gt doctor --fix --rig " + ctx.RigName + "' to fix",
		}
	}

	// Case: No redirect file (but no conflicting data)
	if !redirectExists {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "Missing rig-level beads redirect for tracked beads",
			Details: []string{
				"Tracked beads exist at: mayor/rig/.beads",
				"Missing redirect at: .beads/redirect",
				"Without this redirect, bd commands from rig root won't find beads",
			},
			FixHint: "Run 'gt doctor --fix' to create the redirect",
		}
	}

	// Verify redirect points to correct location
	content, err := os.ReadFile(redirectPath)
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("Could not read redirect file: %v", err),
		}
	}

	target := strings.TrimSpace(string(content))
	if target != "mayor/rig/.beads" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("Redirect points to %q, expected mayor/rig/.beads", target),
			FixHint: "Run 'gt doctor --fix --rig " + ctx.RigName + "' to correct the redirect",
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: "Rig-level beads redirect is correctly configured",
	}
}

// Fix creates or corrects the rig-level beads redirect, or initializes beads if missing.
func (c *BeadsRedirectCheck) Fix(ctx *CheckContext) error {
	if ctx.RigName == "" {
		return nil
	}

	rigPath := ctx.RigPath()
	mayorRigBeads := filepath.Join(rigPath, "mayor", "rig", ".beads")
	rigBeadsDir := filepath.Join(rigPath, ".beads")
	redirectPath := filepath.Join(rigBeadsDir, "redirect")

	// Check if tracked beads exist
	hasTrackedBeads := true
	if _, err := os.Stat(mayorRigBeads); os.IsNotExist(err) {
		hasTrackedBeads = false
	}

	// Check if local beads exist
	hasLocalBeads := true
	if _, err := os.Stat(rigBeadsDir); os.IsNotExist(err) {
		hasLocalBeads = false
	}

	// Case 1: No beads at all - initialize with bd init
	if !hasTrackedBeads && !hasLocalBeads {
		// Get the rig's beads prefix from rigs.json (falls back to "gt" if not found)
		prefix := config.GetRigPrefix(ctx.TownRoot, ctx.RigName)

		// Create .beads directory
		if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
			return fmt.Errorf("creating .beads directory: %w", err)
		}

		// Run bd init with the configured prefix (Dolt is the only backend since bd v0.51.0).
		// Gas Town rigs use Dolt server mode via the shared town Dolt sql-server.
		initArgs := []string{"init"}
		if prefix != "" {
			initArgs = append(initArgs, "--prefix", prefix)
		}
		initArgs = append(initArgs, "--database", ctx.RigName)
		initArgs = append(initArgs, "--server")
		doltCfg := doltserver.DefaultConfig(ctx.TownRoot)
		initArgs = append(initArgs, "--server-port", strconv.Itoa(doltCfg.Port))
		bdEnv := append(stripEnvPrefixes(os.Environ(), "BEADS_DIR=", "BEADS_DB=", "BEADS_DOLT_SERVER_DATABASE="),
			"BEADS_DIR="+rigBeadsDir,
			"BEADS_DOLT_SERVER_DATABASE="+ctx.RigName,
		)
		cmd := exec.Command("bd", initArgs...)
		cmd.Dir = rigPath
		cmd.Env = bdEnv
		if output, err := cmd.CombinedOutput(); err != nil {
			// bd might not be installed — create config.yaml via shared helper.
			if writeErr := beads.EnsureConfigYAML(rigBeadsDir, prefix); writeErr != nil {
				return fmt.Errorf("bd init failed (%v) and fallback config creation failed: %w", err, writeErr)
			}
			// Continue - minimal config created
		} else {
			_ = output // bd init succeeded
			// Configure Gas Town bead types (beads v0.46.0+). Rig remains durable,
			// not infra/wisp-backed.
			for _, cfg := range []struct{ key, value string }{
				{"types.custom", constants.BeadsCustomTypes},
				{"types.infra", constants.BeadsInfraTypes},
			} {
				configCmd := exec.Command("bd", "config", "set", cfg.key, cfg.value)
				configCmd.Dir = rigPath
				configCmd.Env = bdEnv
				_, _ = configCmd.CombinedOutput() // Ignore errors - older beads don't need this
			}
		}
		if err := doltserver.EnsureMetadataForBeadsDir(ctx.TownRoot, rigBeadsDir, ctx.RigName, ctx.RigName); err != nil {
			return fmt.Errorf("ensuring metadata.json: %w", err)
		}
		return nil
	}

	// Case 2: Tracked beads exist - create redirect (may need to remove conflicting local beads)
	if hasTrackedBeads {
		// Check if local beads have conflicting data
		if hasLocalBeads && hasBeadsData(rigBeadsDir) {
			// Remove conflicting local beads directory
			if err := os.RemoveAll(rigBeadsDir); err != nil {
				return fmt.Errorf("removing conflicting local beads: %w", err)
			}
		}

		// Create .beads directory if needed
		if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
			return fmt.Errorf("creating .beads directory: %w", err)
		}

		// Write redirect file
		if err := os.WriteFile(redirectPath, []byte("mayor/rig/.beads\n"), 0644); err != nil {
			return fmt.Errorf("writing redirect file: %w", err)
		}
	}

	return nil
}

// hasBeadsData checks if a beads directory has actual data (issues.db, config.yaml)
// as opposed to just being a redirect-only directory.
func hasBeadsData(beadsDir string) bool {
	// Check for actual beads data files (Dolt-only — issues.jsonl is no longer supported)
	dataFiles := []string{"issues.db", "config.yaml"}
	for _, f := range dataFiles {
		if _, err := os.Stat(filepath.Join(beadsDir, f)); err == nil {
			return true
		}
	}
	return false
}

// bareRepoHealth returns nil if .repo.git is structurally usable as a bare repo,
// or an error describing why it is not. Catches the recurring corruption mode
// where .repo.git is reduced to objects/ + worktrees/ (no HEAD, refs, config),
// and also rejects a non-bare repo masquerading as .repo.git (which would let
// refspec repair write into a working tree's git dir).
func bareRepoHealth(bareRepoPath string) error {
	if _, err := os.Stat(filepath.Join(bareRepoPath, "HEAD")); err != nil {
		return fmt.Errorf("HEAD missing: %w", err)
	}
	cmd := exec.Command("git", "-C", bareRepoPath, "rev-parse", "--git-dir")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("git rev-parse --git-dir failed: %s", msg)
	}
	stderr.Reset()
	bareCmd := exec.Command("git", "-C", bareRepoPath, "rev-parse", "--is-bare-repository")
	bareCmd.Stderr = &stderr
	out, err := bareCmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("git rev-parse --is-bare-repository failed: %s", msg)
	}
	if strings.TrimSpace(string(out)) != "true" {
		return fmt.Errorf(".repo.git is not a bare repository")
	}
	return nil
}

// BareRepoRefspecCheck verifies that the shared bare repo has the correct refspec configured.
// Without this, worktrees created from the bare repo cannot fetch and see origin/* refs.
// See: https://github.com/anthropics/gastown/issues/286
type BareRepoRefspecCheck struct {
	FixableCheck
}

// NewBareRepoRefspecCheck creates a new bare repo refspec check.
func NewBareRepoRefspecCheck() *BareRepoRefspecCheck {
	return &BareRepoRefspecCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "bare-repo-refspec",
				CheckDescription: "Verify bare repo has correct refspec for worktrees",
				CheckCategory:    CategoryRig,
			},
		},
	}
}

// Run checks if the bare repo has the correct remote.origin.fetch refspec.
func (c *BareRepoRefspecCheck) Run(ctx *CheckContext) *CheckResult {
	if ctx.RigName == "" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No rig specified, skipping bare repo check",
		}
	}

	bareRepoPath := filepath.Join(ctx.RigPath(), ".repo.git")
	if _, err := os.Stat(bareRepoPath); os.IsNotExist(err) {
		// No bare repo - might be using a different architecture
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No shared bare repo found (using individual clones)",
		}
	}

	// Before checking refspec, verify the bare repo is fundamentally usable.
	// Without this guard, a partial-shell .repo.git (objects/ + worktrees/ only)
	// will pass after Fix auto-creates a config file, masking the real corruption.
	if healthErr := bareRepoHealth(bareRepoPath); healthErr != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "Bare repo is structurally broken — refspec check cannot verify",
			Details: []string{
				healthErr.Error(),
				"Configuring a refspec on a corrupt repo would silently mask the corruption.",
			},
			FixHint: "Run 'gt doctor --fix --rig " + ctx.RigName + "' (bare-repo-exists check will re-clone)",
		}
	}

	// Check the refspec
	cmd := exec.Command("git", "-C", bareRepoPath, "config", "--get", "remote.origin.fetch")
	out, err := cmd.Output()
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "Bare repo missing remote.origin.fetch refspec",
			Details: []string{
				"Worktrees cannot fetch or see origin/* refs without this config",
				"This breaks refinery merge operations and causes stale origin/main",
			},
			FixHint: "Run 'gt doctor --fix' to configure the refspec",
		}
	}

	refspec := strings.TrimSpace(string(out))
	expectedRefspec := "+refs/heads/*:refs/remotes/origin/*"
	if refspec != expectedRefspec {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "Bare repo has non-standard refspec",
			Details: []string{
				fmt.Sprintf("Current: %s", refspec),
				fmt.Sprintf("Expected: %s", expectedRefspec),
			},
			FixHint: "Run 'gt doctor --fix' to update the refspec",
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: "Bare repo refspec configured correctly",
	}
}

// Fix sets the correct refspec on the bare repo.
func (c *BareRepoRefspecCheck) Fix(ctx *CheckContext) error {
	if ctx.RigName == "" {
		return nil
	}

	bareRepoPath := filepath.Join(ctx.RigPath(), ".repo.git")
	if _, err := os.Stat(bareRepoPath); os.IsNotExist(err) {
		return nil // No bare repo to fix
	}

	// Refuse to write config into a structurally broken bare repo. `git config`
	// auto-creates .repo.git/config, which would make subsequent BareRepoRefspecCheck
	// runs return OK and hide the real corruption from operators.
	if healthErr := bareRepoHealth(bareRepoPath); healthErr != nil {
		return fmt.Errorf("refusing to set refspec: bare repo is structurally broken (%s); run bare-repo-exists fix to re-clone", healthErr)
	}

	cmd := exec.Command("git", "-C", bareRepoPath, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("setting refspec: %s", strings.TrimSpace(stderr.String()))
	}
	return nil
}

// DefaultBranchExistsCheck verifies that the configured default_branch exists
// as a remote tracking ref in the bare repo.
type DefaultBranchExistsCheck struct {
	BaseCheck
}

// NewDefaultBranchExistsCheck creates a new default branch exists check.
func NewDefaultBranchExistsCheck() *DefaultBranchExistsCheck {
	return &DefaultBranchExistsCheck{
		BaseCheck: BaseCheck{
			CheckName:        "default-branch-exists",
			CheckDescription: "Verify configured default_branch exists on remote",
			CheckCategory:    CategoryRig,
		},
	}
}

// Run checks if the configured default_branch exists as origin/<branch> in the bare repo.
func (c *DefaultBranchExistsCheck) Run(ctx *CheckContext) *CheckResult {
	rigPath := ctx.RigPath()
	if rigPath == "" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "No rig specified",
		}
	}

	// Load rig config to get default_branch
	configPath := filepath.Join(rigPath, "config.json")
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "No config.json found",
		}
	}
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("Cannot read config.json: %v", err),
		}
	}

	// Parse just the default_branch field
	type rigConfig struct {
		DefaultBranch string `json:"default_branch"`
	}
	var cfg rigConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("Cannot parse config.json: %v", err),
		}
	}

	if cfg.DefaultBranch == "" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No default_branch configured (will use 'main')",
		}
	}

	// Check bare repo for the ref
	bareRepoPath := filepath.Join(rigPath, ".repo.git")
	if _, err := os.Stat(bareRepoPath); os.IsNotExist(err) {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No shared bare repo (skipping ref check)",
		}
	}

	ref := fmt.Sprintf("refs/remotes/origin/%s", cfg.DefaultBranch)
	cmd := exec.Command("git", "-C", bareRepoPath, "rev-parse", "--verify", ref)
	if err := cmd.Run(); err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("default_branch %q not found on remote", cfg.DefaultBranch),
			Details: []string{
				fmt.Sprintf("Ref %s does not exist in bare repo", ref),
				"Polecat spawn will fail with a cryptic git error",
			},
			FixHint: fmt.Sprintf("Fix the branch name in %s/config.json, or create the branch on the remote", rigPath),
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: fmt.Sprintf("default_branch %q exists on remote", cfg.DefaultBranch),
	}
}

// DefaultBranchAllRigsCheck validates default_branch for all rigs in the workspace.
// Unlike DefaultBranchExistsCheck (which requires --rig), this runs globally.
type DefaultBranchAllRigsCheck struct {
	BaseCheck
}

// NewDefaultBranchAllRigsCheck creates a new global default branch check.
func NewDefaultBranchAllRigsCheck() *DefaultBranchAllRigsCheck {
	return &DefaultBranchAllRigsCheck{
		BaseCheck: BaseCheck{
			CheckName:        "default-branch-all-rigs",
			CheckDescription: "Verify default_branch exists on remote for all rigs",
			CheckCategory:    CategoryRig,
		},
	}
}

// Run checks default_branch for every discovered rig.
func (c *DefaultBranchAllRigsCheck) Run(ctx *CheckContext) *CheckResult {
	entries, err := os.ReadDir(ctx.TownRoot)
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("Cannot read town root: %v", err),
		}
	}

	var errors []string
	rigsChecked := 0

	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || entry.Name() == "mayor" || entry.Name() == "docs" || entry.Name() == "scripts" {
			continue
		}

		rigPath := filepath.Join(ctx.TownRoot, entry.Name())
		configPath := filepath.Join(rigPath, "config.json")

		data, err := os.ReadFile(configPath)
		if err != nil {
			continue // No config.json = not a rig or no default_branch configured
		}

		type rigConfig struct {
			DefaultBranch string `json:"default_branch"`
		}
		var cfg rigConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			continue
		}

		if cfg.DefaultBranch == "" {
			continue // Using default "main", skip
		}

		rigsChecked++

		// Check bare repo for the ref
		bareRepoPath := filepath.Join(rigPath, ".repo.git")
		if _, err := os.Stat(bareRepoPath); os.IsNotExist(err) {
			continue // No bare repo, skip
		}

		ref := fmt.Sprintf("refs/remotes/origin/%s", cfg.DefaultBranch)
		cmd := exec.Command("git", "-C", bareRepoPath, "rev-parse", "--verify", ref)
		if err := cmd.Run(); err != nil {
			errors = append(errors, fmt.Sprintf("%s: default_branch %q not found on remote", entry.Name(), cfg.DefaultBranch))
		}
	}

	if len(errors) > 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("%d rig(s) with invalid default_branch", len(errors)),
			Details: errors,
			FixHint: "Fix the branch name in <rig>/config.json, or create the branch on the remote",
		}
	}

	if rigsChecked == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No rigs with custom default_branch configured",
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: fmt.Sprintf("All %d rig(s) with custom default_branch validated", rigsChecked),
	}
}

// BareRepoExistsCheck verifies that .repo.git exists when worktrees depend on it.
// Worktrees (refinery/rig, polecats) created from the shared bare repo have .git files
// pointing to .repo.git/worktrees/<name>. If .repo.git is missing (deleted, moved, or
// never created), all those worktrees break with "fatal: not a git repository".
type BareRepoExistsCheck struct {
	FixableCheck
	brokenWorktrees []string          // worktree paths with broken .repo.git references
	pushURLMismatch bool              // config.json push_url differs from .repo.git push URL
	bareRepoCorrupt bool              // .repo.git exists but is not a usable git directory
	recoveredHeads  map[string]string // rig-relative worktree path -> HEAD ref captured before re-clone
}

// NewBareRepoExistsCheck creates a new bare repo exists check.
func NewBareRepoExistsCheck() *BareRepoExistsCheck {
	return &BareRepoExistsCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "bare-repo-exists",
				CheckDescription: "Verify .repo.git exists when worktrees depend on it",
				CheckCategory:    CategoryRig,
			},
		},
	}
}

// Run checks if .repo.git exists when worktrees reference it.
func (c *BareRepoExistsCheck) Run(ctx *CheckContext) *CheckResult {
	if ctx.RigName == "" {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusOK,
			Message:  "No rig specified (skipping bare repo check)",
			Category: c.Category(),
		}
	}

	rigPath := ctx.RigPath()
	bareRepoPath := filepath.Join(rigPath, ".repo.git")

	// Reset mutable state to avoid stale values if check is reused across rigs.
	c.brokenWorktrees = nil
	c.pushURLMismatch = false
	c.bareRepoCorrupt = false
	c.recoveredHeads = nil
	worktreeDirs := c.findWorktreeDirs(rigPath, ctx.RigName)

	for _, wtDir := range worktreeDirs {
		gitFile := filepath.Join(wtDir, ".git")
		info, err := os.Stat(gitFile)
		if err != nil {
			continue // no .git entry, skip
		}

		// Only check .git files (worktrees), not .git directories (regular clones)
		if info.IsDir() {
			continue
		}

		content, err := os.ReadFile(gitFile)
		if err != nil {
			continue
		}

		line := strings.TrimSpace(string(content))
		if !strings.HasPrefix(line, "gitdir: ") {
			continue
		}

		gitdir := strings.TrimPrefix(line, "gitdir: ")
		// Check if this worktree references .repo.git
		if strings.Contains(gitdir, ".repo.git") {
			// Resolve the target path
			targetPath := gitdir
			if !filepath.IsAbs(targetPath) {
				targetPath = filepath.Join(wtDir, targetPath)
			}

			// Check if the target exists
			if _, err := os.Stat(targetPath); os.IsNotExist(err) {
				relPath, _ := filepath.Rel(rigPath, wtDir)
				if relPath == "" {
					relPath = wtDir
				}
				c.brokenWorktrees = append(c.brokenWorktrees, relPath)
			}
		}
	}

	// If .repo.git exists, first verify it is structurally usable. A bare repo
	// reduced to objects/ + worktrees/ (no HEAD/refs/config) blocks `gt sling --create`
	// with "fatal: not a git repository" and is the recurring corruption mode this
	// check exists to catch. Detect it here as a hard error so --fix triggers re-clone.
	if _, err := os.Stat(bareRepoPath); err == nil {
		if healthErr := bareRepoHealth(bareRepoPath); healthErr != nil {
			c.bareRepoCorrupt = true
			details := []string{
				fmt.Sprintf("Bare repo at %s is structurally broken", bareRepoPath),
				healthErr.Error(),
				"Worktrees and `gt sling --create` cannot operate on this repo until it is re-cloned.",
			}
			if len(c.brokenWorktrees) > 0 {
				details = append(details, fmt.Sprintf("Also: %d worktree(s) have broken references", len(c.brokenWorktrees)))
				details = append(details, c.brokenWorktrees...)
			}
			return &CheckResult{
				Name:     c.Name(),
				Status:   StatusError,
				Message:  "Shared bare repo exists but is unusable (corrupt)",
				Details:  details,
				FixHint:  "Run 'gt doctor --fix --rig " + ctx.RigName + "' to re-clone .repo.git",
				Category: c.Category(),
			}
		}
	}

	// If .repo.git exists, also verify push URL matches config.json
	if _, err := os.Stat(bareRepoPath); err == nil {
		c.pushURLMismatch = false
		var configWarning string // track config read issues for combined reporting

		// Read push_url from config.json directly (not via config.RigEntry/loadRig).
		// The doctor check reads on-disk config independently of the loaded town.json state.
		// If push_url field semantics change in config.RigEntry, update this struct to match.
		configPath := filepath.Join(rigPath, "config.json")
		cfgData, readErr := os.ReadFile(configPath)
		if readErr != nil {
			if !os.IsNotExist(readErr) {
				// config.json exists but unreadable — warn about permissions
				if len(c.brokenWorktrees) == 0 {
					return &CheckResult{
						Name:     c.Name(),
						Status:   StatusWarning,
						Message:  "Shared bare repo exists but config.json is unreadable",
						Details:  []string{readErr.Error()},
						FixHint:  "Check file permissions on " + configPath,
						Category: c.Category(),
					}
				}
				configWarning = "config.json unreadable: " + readErr.Error()
			}
			// config.json missing — skip push URL validation (bare repo can exist without config)
		} else {
			var cfg struct {
				PushURL string `json:"push_url,omitempty"`
			}
			if jsonErr := json.Unmarshal(cfgData, &cfg); jsonErr != nil {
				// config.json is malformed — cannot validate push URL
				if len(c.brokenWorktrees) == 0 {
					return &CheckResult{
						Name:     c.Name(),
						Status:   StatusWarning,
						Message:  "Shared bare repo exists but config.json is malformed",
						Details:  []string{jsonErr.Error()},
						FixHint:  "Check config.json syntax in " + configPath,
						Category: c.Category(),
					}
				}
				configWarning = "config.json malformed: " + jsonErr.Error()
			} else {
				cfgPushURL := strings.TrimSpace(cfg.PushURL)
				// Get actual push and fetch URLs from .repo.git using git wrapper
				bareGit := git.NewGitWithDir(bareRepoPath, "")
				actualPush, pushErr := bareGit.GetPushURL("origin")
				actualFetch, fetchErr := bareGit.RemoteURL("origin")
				if pushErr != nil || fetchErr != nil {
					// Cannot query remote config — report warning
					if len(c.brokenWorktrees) == 0 {
						details := []string{}
						if pushErr != nil {
							details = append(details, "push URL query failed: "+pushErr.Error())
						}
						if fetchErr != nil {
							details = append(details, "fetch URL query failed: "+fetchErr.Error())
						}
						return &CheckResult{
							Name:     c.Name(),
							Status:   StatusWarning,
							Message:  "Cannot validate push URL — git remote query failed",
							Details:  details,
							FixHint:  "Check .repo.git remote configuration for " + ctx.RigName,
							Category: c.Category(),
						}
					}
					configWarning = fmt.Sprintf("git remote query failed (push: %v, fetch: %v)", pushErr, fetchErr)
				} else {
					actualFetch = strings.TrimSpace(actualFetch)
					if cfgPushURL != "" {
						// Config specifies a push URL — it should match actual
						if actualPush != cfgPushURL {
							c.pushURLMismatch = true
						}
					} else {
						// Config has no push URL — this may be a legacy config that
						// predates the push_url feature. Don't flag a mismatch; the
						// existing git push URL (if any) may be intentionally set.
						// RegisterRig will auto-detect and sync to config.json on next run.
					}
				}
			}
		}

		// Return based on the combination of conditions.
		// All paths return from here — no fall-through to the "missing .repo.git" block.
		if len(c.brokenWorktrees) == 0 && !c.pushURLMismatch {
			return &CheckResult{
				Name:     c.Name(),
				Status:   StatusOK,
				Message:  "Shared bare repo exists and worktrees are valid",
				Category: c.Category(),
			}
		}
		if c.pushURLMismatch && len(c.brokenWorktrees) == 0 {
			return &CheckResult{
				Name:     c.Name(),
				Status:   StatusWarning,
				Message:  "Shared bare repo push URL does not match config.json",
				Details:  []string{"Note: manual config.json edits require 'gt rig add <name> --adopt' to propagate to town.json"},
				FixHint:  "Run 'gt doctor --fix --rig " + ctx.RigName + "' to update push URL",
				Category: c.Category(),
			}
		}
		if c.pushURLMismatch {
			// Both push URL mismatch and broken worktrees
			details := []string{fmt.Sprintf("Push URL mismatch and %d broken worktree(s)", len(c.brokenWorktrees))}
			if configWarning != "" {
				details = append(details, configWarning)
			}
			details = append(details, c.brokenWorktrees...)
			return &CheckResult{
				Name:     c.Name(),
				Status:   StatusError,
				Message:  fmt.Sprintf("Push URL mismatch and %d broken worktree(s)", len(c.brokenWorktrees)),
				Details:  details,
				FixHint:  "Run 'gt doctor --fix --rig " + ctx.RigName + "' to repair",
				Category: c.Category(),
			}
		}
		// Broken worktrees only (.repo.git exists but worktree refs are stale)
		details := []string{fmt.Sprintf("Bare repo exists at %s but %d worktree(s) have broken references", bareRepoPath, len(c.brokenWorktrees))}
		if configWarning != "" {
			details = append(details, configWarning)
		}
		details = append(details, c.brokenWorktrees...)
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusError,
			Message:  fmt.Sprintf("%d worktree(s) have broken references in .repo.git", len(c.brokenWorktrees)),
			Details:  details,
			FixHint:  "Run 'gt doctor --fix --rig " + ctx.RigName + "' to recreate worktree entries",
			Category: c.Category(),
		}
	}

	// .repo.git missing but no worktrees depend on it
	if len(c.brokenWorktrees) == 0 {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusOK,
			Message:  "No worktrees depend on .repo.git",
			Category: c.Category(),
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusError,
		Message: fmt.Sprintf("%d worktree(s) reference missing .repo.git", len(c.brokenWorktrees)),
		Details: append(
			[]string{"Missing: " + bareRepoPath},
			c.brokenWorktrees...,
		),
		FixHint:  "Run 'gt doctor --fix --rig " + ctx.RigName + "' to recreate .repo.git from remote",
		Category: c.Category(),
	}
}

// Fix recreates the .repo.git bare repo from the rig's git_url and re-registers
// existing worktrees. Also fixes push URL mismatches on existing repos.
func (c *BareRepoExistsCheck) Fix(ctx *CheckContext) error {
	if ctx.RigName == "" {
		return nil
	}

	rigPath := ctx.RigPath()
	bareRepoPath := filepath.Join(rigPath, ".repo.git")

	// If .repo.git is corrupt (exists but unusable), nuke it so the missing-repo
	// branch below re-clones from config.json. We cannot repair a partial-shell
	// bare repo in place — git refuses to operate on it.
	//
	// Before nuking:
	//   1. Re-verify health to guard against stale state — Run may have flagged
	//      corruption minutes ago and the operator could have repaired it manually
	//      between Run and Fix. Refusing to delete a now-healthy repo prevents
	//      data loss from a TOCTOU window.
	//   2. Capture every worktree that references .repo.git AND its HEAD ref.
	//      Run only flags worktrees whose .repo.git/worktrees/<name> gitdir was
	//      already missing — but after we delete .repo.git, ALL referencing
	//      worktrees are broken and must be re-registered. HEAD must be captured
	//      now because os.RemoveAll will erase .repo.git/worktrees/<name>/HEAD.
	if c.bareRepoCorrupt {
		if _, err := os.Stat(bareRepoPath); err == nil {
			if healthErr := bareRepoHealth(bareRepoPath); healthErr == nil {
				// Repaired between Run and Fix — drop the corrupt flag and skip deletion.
				c.bareRepoCorrupt = false
			} else {
				refs := c.collectBareRepoReferences(rigPath, ctx.RigName)
				c.recoveredHeads = make(map[string]string, len(refs))
				c.brokenWorktrees = c.brokenWorktrees[:0]
				for _, r := range refs {
					c.brokenWorktrees = append(c.brokenWorktrees, r.relPath)
					if r.headRef != "" {
						c.recoveredHeads[r.relPath] = r.headRef
					}
				}
				if rmErr := os.RemoveAll(bareRepoPath); rmErr != nil {
					return fmt.Errorf("removing corrupt .repo.git: %w", rmErr)
				}
			}
		}
	}

	// Fix push URL mismatch on existing .repo.git.
	// Only apply if .repo.git exists — if missing, recreation below sets the correct push URL.
	// Note: config.json is parsed inline here (not via config.RigEntry) because the doctor
	// check needs to read the on-disk config independently of the loaded town.json state.
	if c.pushURLMismatch {
		if _, statErr := os.Stat(bareRepoPath); statErr == nil {
			configPath := filepath.Join(rigPath, "config.json")
			cfgData, readErr := os.ReadFile(configPath)
			if readErr != nil {
				return fmt.Errorf("cannot read config.json to fix push URL: %w", readErr)
			}
			var cfg struct {
				PushURL string `json:"push_url,omitempty"`
			}
			if jsonErr := json.Unmarshal(cfgData, &cfg); jsonErr != nil {
				return fmt.Errorf("cannot parse config.json to fix push URL: %w", jsonErr)
			}
			cfgPushURL := strings.TrimSpace(cfg.PushURL)
			bareGit := git.NewGitWithDir(bareRepoPath, "")
			if cfgPushURL != "" {
				if err := bareGit.ConfigurePushURL("origin", cfgPushURL); err != nil {
					return fmt.Errorf("updating push URL on .repo.git: %w", err)
				}
			} else {
				// Config has no push URL — this may be a legacy config that
				// predates the push_url feature. Don't clear; the existing
				// git push URL (if any) may be intentionally set.
			}
		}
	}

	if len(c.brokenWorktrees) == 0 && !c.bareRepoCorrupt {
		// No worktrees to fix and bare repo is healthy — push URL mismatch
		// (if any) already handled above.
		return nil
	}

	// Recreate .repo.git if it's missing (originally absent or removed above as corrupt).
	if _, err := os.Stat(bareRepoPath); err != nil {
		// Read git_url from config.json
		configPath := filepath.Join(rigPath, "config.json")
		data, err := os.ReadFile(configPath)
		if err != nil {
			return fmt.Errorf("cannot read config.json to get git_url: %w", err)
		}

		var cfg struct {
			GitURL  string `json:"git_url"`
			PushURL string `json:"push_url,omitempty"`
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("cannot parse config.json: %w", err)
		}
		if cfg.GitURL == "" {
			return fmt.Errorf("config.json has no git_url, cannot recreate .repo.git")
		}

		// Clone bare repo (shallow, single-branch for efficiency on repos with many branches)
		cmd := exec.Command("git", "clone", "--bare", "--single-branch", "--depth", "1", cfg.GitURL, bareRepoPath)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("cloning bare repo: %s", strings.TrimSpace(stderr.String()))
		}

		// Configure refspec so worktrees can fetch origin/* refs.
		// Skip full fetch — the shallow single-branch clone already has the default branch.
		stderr.Reset()
		configCmd := exec.Command("git", "-C", bareRepoPath, "config",
			"remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
		configCmd.Stderr = &stderr
		if err := configCmd.Run(); err != nil {
			return fmt.Errorf("configuring refspec: %s", strings.TrimSpace(stderr.String()))
		}

		// Restore push URL if configured (for read-only upstream repos)
		if cfg.PushURL != "" {
			stderr.Reset()
			pushURLCmd := exec.Command("git", "-C", bareRepoPath, "remote", "set-url", "--push", "origin", cfg.PushURL)
			pushURLCmd.Stderr = &stderr
			if err := pushURLCmd.Run(); err != nil {
				return fmt.Errorf("configuring push URL: %s", strings.TrimSpace(stderr.String()))
			}
		}
	}

	// Re-register broken worktrees so the bare repo knows about them.
	// Git worktrees are tracked in .repo.git/worktrees/<name>/ with a gitdir file
	// pointing back to the worktree. We need to create these metadata entries.
	for _, relPath := range c.brokenWorktrees {
		wtPath := filepath.Join(rigPath, relPath)
		gitFile := filepath.Join(wtPath, ".git")

		content, err := os.ReadFile(gitFile)
		if err != nil {
			continue
		}

		line := strings.TrimSpace(string(content))
		if !strings.HasPrefix(line, "gitdir: ") {
			continue
		}

		gitdir := strings.TrimPrefix(line, "gitdir: ")
		if !filepath.IsAbs(gitdir) {
			gitdir = filepath.Join(wtPath, gitdir)
		}
		gitdir = filepath.Clean(gitdir)

		// Extract worktree name from path (e.g., .repo.git/worktrees/rig -> "rig")
		worktreeName := filepath.Base(gitdir)

		// Create worktree metadata directory
		wtMetaDir := filepath.Join(bareRepoPath, "worktrees", worktreeName)
		if err := os.MkdirAll(wtMetaDir, 0755); err != nil {
			continue
		}

		// Write gitdir file (points back to the worktree's .git file)
		gitdirFile := filepath.Join(wtMetaDir, "gitdir")
		if err := os.WriteFile(gitdirFile, []byte(wtPath+"/.git\n"), 0644); err != nil {
			continue
		}

		// Detect which branch the worktree was on. Prefer a HEAD captured before
		// .repo.git was nuked (corrupt-recovery path), fall back to reading from
		// the live worktree gitdir (legacy missing-bare-repo path), then to main.
		headContent := "ref: refs/heads/main\n"
		if saved, ok := c.recoveredHeads[relPath]; ok && saved != "" {
			headContent = saved
		} else if oldHead, err := os.ReadFile(filepath.Join(gitdir, "HEAD")); err == nil {
			headContent = string(oldHead)
		}

		headFile := filepath.Join(wtMetaDir, "HEAD")
		if err := os.WriteFile(headFile, []byte(headContent), 0644); err != nil {
			continue
		}
	}

	return nil
}

// bareRepoWorktreeRef captures everything we need to re-register a worktree
// after .repo.git is re-cloned. HEAD is captured BEFORE deletion because
// os.RemoveAll erases .repo.git/worktrees/<name>/HEAD; without this the
// re-registration would silently default to main.
type bareRepoWorktreeRef struct {
	relPath string // rig-relative worktree directory path
	headRef string // contents of the worktree's HEAD (e.g. "ref: refs/heads/foo\n"), empty if unreadable
}

// collectBareRepoReferences returns refs for every worktree dir whose .git
// file points into <rigPath>/.repo.git/worktrees/<name>. Used during corrupt
// .repo.git recovery: all referencing worktrees must be re-registered after
// re-clone, regardless of whether the gitdir target was already missing.
//
// Path matching is exact (resolved + cleaned + prefix check) so a worktree
// from another rig that happens to contain ".repo.git" in its path is not
// mis-collected.
func (c *BareRepoExistsCheck) collectBareRepoReferences(rigPath, rigName string) []bareRepoWorktreeRef {
	bareRepoPrefix := filepath.Clean(filepath.Join(rigPath, ".repo.git", "worktrees")) + string(filepath.Separator)
	var refs []bareRepoWorktreeRef
	for _, wtDir := range c.findWorktreeDirs(rigPath, rigName) {
		gitFile := filepath.Join(wtDir, ".git")
		info, err := os.Stat(gitFile)
		if err != nil || info.IsDir() {
			continue
		}
		content, err := os.ReadFile(gitFile)
		if err != nil {
			continue
		}
		line := strings.TrimSpace(string(content))
		if !strings.HasPrefix(line, "gitdir: ") {
			continue
		}
		gitdir := strings.TrimPrefix(line, "gitdir: ")
		if !filepath.IsAbs(gitdir) {
			gitdir = filepath.Join(wtDir, gitdir)
		}
		gitdir = filepath.Clean(gitdir)
		if !strings.HasPrefix(gitdir, bareRepoPrefix) {
			continue
		}
		relPath, _ := filepath.Rel(rigPath, wtDir)
		if relPath == "" {
			relPath = wtDir
		}
		ref := bareRepoWorktreeRef{relPath: relPath}
		// Capture HEAD now, before the caller removes .repo.git.
		if headBytes, err := os.ReadFile(filepath.Join(gitdir, "HEAD")); err == nil {
			ref.headRef = string(headBytes)
		}
		refs = append(refs, ref)
	}
	return refs
}

// findWorktreeDirs returns paths to directories that may be git worktrees within a rig.
// Checks refinery/rig and all polecat worktree directories.
func (c *BareRepoExistsCheck) findWorktreeDirs(rigPath, rigName string) []string {
	var dirs []string

	// refinery/rig
	refineryRig := filepath.Join(rigPath, "refinery", "rig")
	if _, err := os.Stat(refineryRig); err == nil {
		dirs = append(dirs, refineryRig)
	}

	// witness/rig
	witnessRig := filepath.Join(rigPath, "witness", "rig")
	if _, err := os.Stat(witnessRig); err == nil {
		dirs = append(dirs, witnessRig)
	}

	// polecats/<name>/<rigname>/
	polecatsDir := filepath.Join(rigPath, "polecats")
	if entries, err := os.ReadDir(polecatsDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			// New structure: polecats/<name>/<rigname>/
			newPath := filepath.Join(polecatsDir, entry.Name(), rigName)
			if _, err := os.Stat(newPath); err == nil {
				dirs = append(dirs, newPath)
			}
			// Old structure: polecats/<name>/
			oldPath := filepath.Join(polecatsDir, entry.Name())
			if oldPath != newPath {
				if _, err := os.Stat(filepath.Join(oldPath, ".git")); err == nil {
					dirs = append(dirs, oldPath)
				}
			}
		}
	}

	return dirs
}

// RigChecks returns all rig-level health checks.
func RigChecks() []Check {
	return []Check{
		NewRigIsGitRepoCheck(),
		NewGitExcludeConfiguredCheck(),
		NewHooksPathConfiguredCheck(),
		NewBareRepoExistsCheck(),
		NewBareRepoRefspecCheck(),
		NewDefaultBranchExistsCheck(),
		NewWitnessExistsCheck(),
		NewRefineryExistsCheck(),
		NewMayorCloneExistsCheck(),
		NewPolecatClonesValidCheck(),
		NewBeadsConfigValidCheck(),
		NewBeadsRedirectCheck(),
		NewTestutilSymlinkCheck(),
	}
}
