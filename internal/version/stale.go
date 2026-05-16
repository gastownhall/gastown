// Package version provides version information and staleness checking for gt.
package version

import (
	"fmt"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"

	"github.com/steveyegge/gastown/internal/util"
)

// These variables are set at build time via ldflags in cmd package.
// We provide fallback methods to read from build info.
var (
	// Commit can be set from cmd package or read from build info
	Commit = ""
)

// StaleBinaryInfo contains information about binary staleness.
type StaleBinaryInfo struct {
	IsStale       bool   // True if binary commit is behind the build-branch ref
	IsForward     bool   // True if the compare commit is a descendant of binary commit (safe to rebuild)
	OnMainBranch  bool   // True if the resolved source worktree is on a build branch
	BinaryCommit  string // Commit hash the binary was built from
	RepoCommit    string // Commit of the ref the binary was compared against (CompareRef)
	CompareRef    string // The ref staleness was computed against (e.g. "main", "origin/main")
	CommitsBehind int    // Number of commits binary is behind (0 if unknown)
	Skipped       bool   // True if no build-branch ref could be resolved to compare against
	SkipReason    string // Human-readable reason the check was skipped
	Error         error  // Any error encountered during check
}

// resolveCommitHash gets the commit hash from build info or the Commit variable.
func resolveCommitHash() string {
	if Commit != "" {
		return Commit
	}

	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" && setting.Value != "" {
				return setting.Value
			}
		}
	}

	return ""
}

// Describe returns a one-line, human-readable staleness summary for a stale
// binary, using subject as the leading noun so callers can vary it
// ("Binary" for gt doctor, "gt binary" for the startup warning):
//
//	"Binary is 3 commits behind main (built from abc123…, main at def456…)"
//	"gt binary is stale (built from abc123…, origin/main at def456…)"
//
// It is only meaningful when i.IsStale; callers gate on that. A zero
// CommitsBehind (count unknown) falls back to the "is stale" wording.
func (i *StaleBinaryInfo) Describe(subject string) string {
	if i.CommitsBehind > 0 {
		return fmt.Sprintf("%s is %d commits behind %s (built from %s, %s at %s)",
			subject, i.CommitsBehind, i.CompareRef,
			ShortCommit(i.BinaryCommit), i.CompareRef, ShortCommit(i.RepoCommit))
	}
	return fmt.Sprintf("%s is stale (built from %s, %s at %s)",
		subject, ShortCommit(i.BinaryCommit), i.CompareRef, ShortCommit(i.RepoCommit))
}

// ShortCommit returns first 12 characters of a hash.
func ShortCommit(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}

// commitsMatch compares two commit hashes, handling different lengths.
// Returns true if one is a prefix of the other (minimum 7 chars to avoid false positives).
func commitsMatch(a, b string) bool {
	minLen := len(a)
	if len(b) < minLen {
		minLen = len(b)
	}
	// Need at least 7 chars for a reasonable comparison
	if minLen < 7 {
		return false
	}
	return strings.HasPrefix(a, b[:minLen]) || strings.HasPrefix(b, a[:minLen])
}

// CheckStaleBinary compares the binary's embedded commit with the repo HEAD.
// It returns staleness info including whether the binary needs rebuilding.
// This check is designed to be fast and non-blocking - errors are captured
// but don't interrupt normal operation.
func CheckStaleBinary(repoDir string) *StaleBinaryInfo {
	info := &StaleBinaryInfo{}

	// Get binary commit
	info.BinaryCommit = resolveCommitHash()
	if info.BinaryCommit == "" {
		info.Error = fmt.Errorf("cannot determine binary commit (dev build?)")
		return info
	}

	// Check which branch the resolved source worktree is on.
	// Accept main/master (upstream) and carry/* (fork operational branches).
	var branch string
	branchCmd := exec.Command("git", "symbolic-ref", "--short", "HEAD")
	branchCmd.Dir = repoDir
	util.SetDetachedProcessGroup(branchCmd)
	if branchOutput, err := branchCmd.Output(); err == nil {
		branch = strings.TrimSpace(string(branchOutput))
	}
	info.OnMainBranch = isBuildBranch(branch)

	// Decide which ref to compare the binary against.
	//
	// GetRepoRoot resolves to $GT_ROOT/gastown/mayor/rig, a worktree that
	// normally sits on a feature branch (that's where the Mayor does git work).
	// Diffing the binary against that worktree's HEAD compares it to unmerged
	// feature work and produces a false "N commits behind" warning advising a
	// rebuild from the feature branch (GH#4034). Staleness is only meaningful
	// relative to a *build branch*.
	var compareRef string
	if info.OnMainBranch {
		// Already on a build branch — its HEAD is the build branch.
		compareRef = "HEAD"
		info.CompareRef = branch
	} else {
		// Resolve a real build-branch ref instead of the feature HEAD.
		ref, ok := resolveBuildBranchRef(repoDir, info.BinaryCommit)
		if !ok {
			info.Skipped = true
			info.SkipReason = "source worktree not on a build branch and no build-branch ref found to compare against"
			return info
		}
		compareRef = ref
		info.CompareRef = ref
	}

	// Resolve the compare ref to a commit hash.
	revCmd := exec.Command("git", "rev-parse", compareRef)
	revCmd.Dir = repoDir
	util.SetDetachedProcessGroup(revCmd)
	output, err := revCmd.Output()
	if err != nil {
		info.Error = fmt.Errorf("cannot resolve compare ref %q: %w", compareRef, err)
		return info
	}
	info.RepoCommit = strings.TrimSpace(string(output))

	// Compare commits using prefix matching (handles short vs full hash)
	// Use the shorter of the two commit lengths for comparison
	if !commitsMatch(info.BinaryCommit, info.RepoCommit) {
		// Verify the binary commit exists in the found repo. GetRepoRoot may
		// find a different clone (e.g., mayor/rig) than the one the binary was
		// built from (e.g., crew/woodhouse). If the binary commit isn't in the
		// repo's object store, we can't determine staleness — skip.
		verifyCmd := exec.Command("git", "cat-file", "-t", info.BinaryCommit)
		verifyCmd.Dir = repoDir
		if err := verifyCmd.Run(); err != nil {
			// Binary commit not in this repo — different clones, can't compare
			return info
		}

		// Check if all commits between binary and the build ref only touch
		// .beads/ files (e.g., bd backup commits). These don't affect the
		// binary and should not trigger a stale warning. (GH#2596)
		if onlyBeadsChanges(repoDir, info.BinaryCommit, compareRef) {
			// Build ref advanced but only via beads-only commits — not stale
			return info
		}

		info.IsStale = true

		// Check if this is a forward-only update (binary commit is ancestor of
		// the build ref). This prevents rebuilding to an older or diverged
		// commit, which caused a crash loop when a worktree's HEAD was behind
		// the binary's commit.
		ancestorCmd := exec.Command("git", "merge-base", "--is-ancestor", info.BinaryCommit, compareRef)
		ancestorCmd.Dir = repoDir
		util.SetDetachedProcessGroup(ancestorCmd)
		info.IsForward = ancestorCmd.Run() == nil

		// Try to count commits between binary and the build ref
		countCmd := exec.Command("git", "rev-list", "--count", info.BinaryCommit+".."+compareRef)
		countCmd.Dir = repoDir
		util.SetDetachedProcessGroup(countCmd)
		if countOutput, err := countCmd.Output(); err == nil {
			if count, parseErr := fmt.Sscanf(strings.TrimSpace(string(countOutput)), "%d", &info.CommitsBehind); parseErr != nil || count != 1 {
				info.CommitsBehind = 0
			}
		}
	}

	return info
}

// resolveBuildBranchRef finds a build-branch ref to compare the binary against
// when the resolved source worktree is parked on a non-build branch (the normal
// state for $GT_ROOT/gastown/mayor/rig). Without this, staleness would be
// computed against unmerged feature work (GH#4034).
//
// Candidates are tried in build-branch precedence order; the first that both
// (a) resolves to a commit and (b) has binaryCommit as an ancestor is returned.
// Requiring binaryCommit to be an ancestor ensures we compare against the branch
// the binary was actually built from — e.g. it routes a fork build to its
// carry/* operational branch rather than a stale local main.
func resolveBuildBranchRef(repoDir, binaryCommit string) (string, bool) {
	candidates := []string{"main", "master"}

	// Fork operational branch: include a local carry/* branch, but only if
	// exactly one exists (ambiguous otherwise — don't guess).
	if c, ok := singleCarryBranch(repoDir); ok {
		candidates = append(candidates, c)
	}

	candidates = append(candidates,
		"origin/main", "origin/master",
		"upstream/main", "upstream/master",
	)

	for _, ref := range candidates {
		if refExists(repoDir, ref) && isAncestor(repoDir, binaryCommit, ref) {
			return ref, true
		}
	}
	return "", false
}

// refExists reports whether ref resolves to a commit in repoDir.
func refExists(repoDir, ref string) bool {
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	cmd.Dir = repoDir
	util.SetDetachedProcessGroup(cmd)
	return cmd.Run() == nil
}

// isAncestor reports whether ancestor is an ancestor of ref (a commit is its
// own ancestor) in repoDir.
func isAncestor(repoDir, ancestor, ref string) bool {
	cmd := exec.Command("git", "merge-base", "--is-ancestor", ancestor, ref)
	cmd.Dir = repoDir
	util.SetDetachedProcessGroup(cmd)
	return cmd.Run() == nil
}

// singleCarryBranch returns the sole local carry/* branch, if exactly one
// exists. Multiple carry/* branches are ambiguous and yield ("", false).
func singleCarryBranch(repoDir string) (string, bool) {
	cmd := exec.Command("git", "for-each-ref", "--format=%(refname:short)", "refs/heads/carry/")
	cmd.Dir = repoDir
	util.SetDetachedProcessGroup(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	branches := strings.Fields(strings.TrimSpace(string(out)))
	if len(branches) == 1 {
		return branches[0], true
	}
	return "", false
}

// GetRepoRoot returns the git repository root for the gt source code.
// The canonical source is the gastown repo itself ($GT_ROOT/gastown).
// Crew rigs also contain cmd/gt/main.go but have different HEADs,
// so we prefer the gastown repo over CWD-based git toplevel detection.
func GetRepoRoot() (string, error) {
	// Check if GT_ROOT environment variable is set (agents always have this)
	if gtRoot := os.Getenv("GT_ROOT"); gtRoot != "" {
		candidates := []string{
			gtRoot + "/gastown",
			gtRoot + "/gastown/mayor/rig",
		}
		for _, candidate := range candidates {
			if hasGtSource(candidate) {
				return candidate, nil
			}
		}
	}

	// Try common development paths relative to home
	home := os.Getenv("HOME")
	if home != "" {
		candidates := []string{
			home + "/gt/gastown",
			home + "/gt/gastown/mayor/rig",
			home + "/gastown",
			home + "/gastown/mayor/rig",
			home + "/src/gastown",
			home + "/src/gastown/mayor/rig",
		}
		for _, candidate := range candidates {
			if hasGtSource(candidate) {
				return candidate, nil
			}
		}
	}

	// Fall back to current directory's git repo (may be a crew rig)
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	util.SetDetachedProcessGroup(cmd)
	if output, err := cmd.Output(); err == nil {
		root := strings.TrimSpace(string(output))
		if hasGtSource(root) {
			return root, nil
		}
	}

	return "", fmt.Errorf("cannot locate gt source repository")
}

// isGitRepo checks if a directory is a git repository.
func isGitRepo(dir string) bool {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = dir
	util.SetDetachedProcessGroup(cmd)
	return cmd.Run() == nil
}

// hasGtSource checks if a directory contains the gt source code.
// We look for cmd/gt/main.go as the definitive marker.
func hasGtSource(dir string) bool {
	_, err := os.Stat(dir + "/cmd/gt/main.go")
	return err == nil
}

// onlyBeadsChanges checks whether all commits between binaryCommit and
// compareRef exclusively modify files under .beads/. Returns true if the diff
// contains no changes outside .beads/, meaning the binary is functionally
// up-to-date. Used to suppress false-positive stale warnings from bd backup
// commits. (GH#2596)
func onlyBeadsChanges(repoDir, binaryCommit, compareRef string) bool {
	// Get files changed between binary commit and the build ref, excluding
	// .beads/. If this produces no output, all changes are within .beads/
	cmd := exec.Command("git", "diff", "--name-only", binaryCommit+".."+compareRef, "--", ".", ":!.beads")
	cmd.Dir = repoDir
	util.SetDetachedProcessGroup(cmd)
	output, err := cmd.Output()
	if err != nil {
		// Can't determine — be conservative, assume stale
		return false
	}
	return strings.TrimSpace(string(output)) == ""
}

// isBuildBranch returns true if the given branch is safe for automated rebuilds.
// Accepted branches:
//   - main, master: upstream default branches
//   - carry/*: fork operational branches (e.g., carry/operational)
//
// This prevents automated rebuilds from random feature, fix, or polecat branches
// which could cause downgrades or crash loops.
func isBuildBranch(branch string) bool {
	switch branch {
	case "main", "master":
		return true
	}
	return strings.HasPrefix(branch, "carry/")
}

// SetCommit allows the cmd package to pass in the build-time commit.
func SetCommit(commit string) {
	Commit = commit
}
