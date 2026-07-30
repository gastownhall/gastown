package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/rig"
)

// si-n7vl: `gt sling <bead> <rig>/<polecat>` could not resume an IDLE polecat.
// It either errored ("getting pane for si-keeper: exit status 1") or, when the
// dead-polecat fallback did fire, spawned a FRESH polecat on a FRESH branch —
// so the worktree and branch you were trying to resume were never reattached.
// That is why operators reached for --branch, which is what collided two
// worktrees onto one ref (si-d6kw).
//
// The tests below cover the four acceptance items on si-n7vl:
//
//	A. sling to an idle polecat by name -> resumes ITS worktree (ClonePath)
//	B. the 2-part rig/polecat form works without --create for an EXISTING
//	   polecat, and still refuses a nonexistent one
//	C. denominator: a polecat with no usable worktree still takes fresh-spawn
//	D. the resumed polecat reaches its branch with no --branch from the caller

func mkPolecatDir(t *testing.T, townRoot, rigName, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(townRoot, rigName, "polecats", name), 0755); err != nil {
		t.Fatalf("mkdir polecat dir: %v", err)
	}
}

func mkCrewDir(t *testing.T, townRoot, rigName, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(townRoot, rigName, "crew", name), 0755); err != nil {
		t.Fatalf("mkdir crew dir: %v", err)
	}
}

// B: the shorthand form must resolve an EXISTING polecat even though --create
// was not passed. --create means "make one that does not exist"; this one does.
func TestPolecatTargetRigAndName_ShorthandResolvesExistingPolecatWithoutCreate(t *testing.T) {
	townRoot := t.TempDir()
	mkPolecatDir(t, townRoot, "silicon", "keeper")

	rigName, polecatName, ok := polecatTargetRigAndName("silicon/keeper", false /* allowShorthand */, townRoot)
	if !ok {
		t.Fatalf("silicon/keeper not recognised as a polecat target without --create; " +
			"this is the exact case that surfaced 'getting pane for si-keeper: exit status 1'")
	}
	if rigName != "silicon" || polecatName != "keeper" {
		t.Fatalf("got rig=%q polecat=%q, want rig=silicon polecat=keeper", rigName, polecatName)
	}
}

// B, denominator: a name with no polecat directory must still be refused when
// --create was not passed. Without this the test above passes for the wrong
// reason (everything 2-part accepted).
func TestPolecatTargetRigAndName_ShorthandRefusesNonexistentPolecatWithoutCreate(t *testing.T) {
	townRoot := t.TempDir()
	mkPolecatDir(t, townRoot, "silicon", "keeper")

	if rigName, polecatName, ok := polecatTargetRigAndName("silicon/ghost", false, townRoot); ok {
		t.Fatalf("silicon/ghost accepted without --create (rig=%q polecat=%q); a polecat that does not exist "+
			"must still require --create", rigName, polecatName)
	}
}

// --create keeps its old meaning: mint a polecat that does not exist yet.
func TestPolecatTargetRigAndName_ShorthandWithCreateAcceptsNonexistentPolecat(t *testing.T) {
	townRoot := t.TempDir()

	rigName, polecatName, ok := polecatTargetRigAndName("silicon/ghost", true, townRoot)
	if !ok {
		t.Fatal("silicon/ghost refused with --create; --create must still allow minting a new polecat")
	}
	if rigName != "silicon" || polecatName != "ghost" {
		t.Fatalf("got rig=%q polecat=%q, want rig=silicon polecat=ghost", rigName, polecatName)
	}
}

// Regression guard: a crew member is not a polecat, with or without --create.
func TestPolecatTargetRigAndName_CrewMemberIsNotAPolecatTarget(t *testing.T) {
	townRoot := t.TempDir()
	mkCrewDir(t, townRoot, "silicon", "alice")

	for _, allowShorthand := range []bool{false, true} {
		if _, _, ok := polecatTargetRigAndName("silicon/alice", allowShorthand, townRoot); ok {
			t.Fatalf("silicon/alice treated as a polecat target (allowShorthand=%v); it is a crew member", allowShorthand)
		}
	}
}

// Regression guard: a role second segment is never a polecat name.
func TestPolecatTargetRigAndName_KnownRoleIsNotAPolecatTarget(t *testing.T) {
	townRoot := t.TempDir()
	mkPolecatDir(t, townRoot, "silicon", "crew")

	if _, _, ok := polecatTargetRigAndName("silicon/crew", true, townRoot); ok {
		t.Fatal("silicon/crew treated as a polecat target; 'crew' is a role, not a polecat name")
	}
}

// The explicit 3-part form must yield the polecat NAME, not just the rig — the
// old signature returned only the rig, which is why the fallback could not tell
// which polecat to resume.
func TestPolecatTargetRigAndName_ThreePartFormYieldsPolecatName(t *testing.T) {
	rigName, polecatName, ok := polecatTargetRigAndName("silicon/polecats/keeper", false, "")
	if !ok {
		t.Fatal("silicon/polecats/keeper not recognised as a polecat target")
	}
	if rigName != "silicon" || polecatName != "keeper" {
		t.Fatalf("got rig=%q polecat=%q, want rig=silicon polecat=keeper", rigName, polecatName)
	}
}

// D: with no --branch and no --base-branch, a named resume lands on the
// polecat's OWN branch. That is the entire point of the bead: recovery must
// never require the caller to name a branch, because naming one is what
// collided two worktrees onto a single ref.
func TestResumeOptionsForPolecat_DefaultsToThePolecatsOwnBranch(t *testing.T) {
	got := resumeOptionsForPolecat("polecat/dag/si-aka.9+ms43eli5", "main", SlingSpawnOptions{HookBead: "si-aka.9"})
	if got.ResumeBranch != "polecat/dag/si-aka.9+ms43eli5" {
		t.Fatalf("ResumeBranch = %q, want the polecat's existing branch; a fresh branch off main "+
			"loses the work the operator is trying to resume", got.ResumeBranch)
	}
	if got.HookBead != "si-aka.9" {
		t.Fatalf("HookBead = %q, want si-aka.9 carried through untouched", got.HookBead)
	}
}

// D, denominator: a polecat parked on the rig default branch has nothing to
// resume, so reuse must mint a fresh polecat/<name>/<bead> branch as usual.
func TestResumeOptionsForPolecat_DefaultBranchIsNotResumed(t *testing.T) {
	if got := resumeOptionsForPolecat("main", "main", SlingSpawnOptions{}); got.ResumeBranch != "" {
		t.Fatalf("ResumeBranch = %q, want empty: a worktree sitting on the default branch has no work to resume", got.ResumeBranch)
	}
}

// D, denominator: a detached or unreadable HEAD is not a branch to resume.
func TestResumeOptionsForPolecat_EmptyCurrentBranchIsNotResumed(t *testing.T) {
	if got := resumeOptionsForPolecat("", "main", SlingSpawnOptions{}); got.ResumeBranch != "" {
		t.Fatalf("ResumeBranch = %q, want empty for a detached HEAD", got.ResumeBranch)
	}
}

// An explicit caller flag still wins over the polecat's current branch.
func TestResumeOptionsForPolecat_ExplicitFlagsWin(t *testing.T) {
	got := resumeOptionsForPolecat("polecat/dag/old", "main", SlingSpawnOptions{ResumeBranch: "fix/pr-head"})
	if got.ResumeBranch != "fix/pr-head" {
		t.Fatalf("ResumeBranch = %q, want the explicitly requested fix/pr-head", got.ResumeBranch)
	}

	got = resumeOptionsForPolecat("polecat/dag/old", "main", SlingSpawnOptions{BaseBranch: "develop"})
	if got.ResumeBranch != "" {
		t.Fatalf("ResumeBranch = %q, want empty: --base-branch asks for fresh work off develop", got.ResumeBranch)
	}
}

// A + D, at the call site: ResumePolecatForSling must actually HAND the
// polecat's own branch to reuse. resumeOptionsForPolecat deciding correctly is
// worth nothing if the decision is computed and dropped.
func TestResumePolecatForSlingPassesThePolecatsOwnBranchToReuse(t *testing.T) {
	rigDir := t.TempDir()
	env := &slingPolecatEnv{
		townRoot: filepath.Dir(rigDir),
		rigName:  "silicon",
		rig:      &rig.Rig{Name: "silicon", Path: rigDir},
	}
	if got := env.rig.DefaultBranch(); got != "main" {
		t.Fatalf("test fixture rig default branch = %q, want main", got)
	}

	prevEnv := resolveSlingPolecatEnvFn
	prevBranch := polecatResumableBranchFn
	prevReuse := reuseIdlePolecatForSlingFn
	t.Cleanup(func() {
		resolveSlingPolecatEnvFn = prevEnv
		polecatResumableBranchFn = prevBranch
		reuseIdlePolecatForSlingFn = prevReuse
	})

	resolveSlingPolecatEnvFn = func(rigName string, opts SlingSpawnOptions) (*slingPolecatEnv, error) {
		return env, nil
	}
	polecatResumableBranchFn = func(_ *slingPolecatEnv, polecatName string) (string, error) {
		return "polecat/keeper/si-aka.37+ms43gm2z", nil
	}
	var gotOpts SlingSpawnOptions
	var gotName string
	reuseIdlePolecatForSlingFn = func(_ *slingPolecatEnv, polecatName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		gotName = polecatName
		gotOpts = opts
		return &SpawnedPolecatInfo{RigName: "silicon", PolecatName: polecatName}, nil
	}

	// No --branch and no --base-branch: exactly the invocation that used to fail.
	if _, err := ResumePolecatForSling("silicon", "keeper", SlingSpawnOptions{HookBead: "si-aka.37"}); err != nil {
		t.Fatalf("ResumePolecatForSling: %v", err)
	}
	if gotName != "keeper" {
		t.Fatalf("reuse called for %q, want keeper", gotName)
	}
	if gotOpts.ResumeBranch != "polecat/keeper/si-aka.37+ms43gm2z" {
		t.Fatalf("reuse got ResumeBranch=%q, want the polecat's own branch — without it reuse resets the "+
			"worktree to origin/main and mints a fresh branch, which is the work being recovered", gotOpts.ResumeBranch)
	}
	if gotOpts.HookBead != "si-aka.37" {
		t.Fatalf("reuse got HookBead=%q, want si-aka.37", gotOpts.HookBead)
	}
}

// Denominator for the above: nothing resumable means nothing is invented.
func TestResumePolecatForSlingFailsWhenThereIsNoWorktreeToResume(t *testing.T) {
	prevEnv := resolveSlingPolecatEnvFn
	prevBranch := polecatResumableBranchFn
	prevReuse := reuseIdlePolecatForSlingFn
	t.Cleanup(func() {
		resolveSlingPolecatEnvFn = prevEnv
		polecatResumableBranchFn = prevBranch
		reuseIdlePolecatForSlingFn = prevReuse
	})

	resolveSlingPolecatEnvFn = func(rigName string, opts SlingSpawnOptions) (*slingPolecatEnv, error) {
		return &slingPolecatEnv{rigName: rigName, rig: &rig.Rig{Name: rigName, Path: t.TempDir()}}, nil
	}
	polecatResumableBranchFn = func(_ *slingPolecatEnv, polecatName string) (string, error) {
		return "", errors.New("has no usable worktree")
	}
	reuseIdlePolecatForSlingFn = func(_ *slingPolecatEnv, polecatName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		t.Fatal("reuse attempted for a polecat with no worktree")
		return nil, nil
	}

	if _, err := ResumePolecatForSling("silicon", "ghost", SlingSpawnOptions{}); err == nil {
		t.Fatal("ResumePolecatForSling succeeded with no worktree to resume")
	}
}

// A: slinging to a named idle polecat resumes ITS worktree. The pre-existing
// ClonePath must come back, and no fresh polecat may be spawned.
func TestResolveTargetResumesNamedIdlePolecat(t *testing.T) {
	townRoot := t.TempDir()
	mkPolecatDir(t, townRoot, "silicon", "keeper")
	existingClone := filepath.Join(townRoot, "silicon", "polecats", "keeper", "silicon")

	prevResolve := resolveTargetAgentFn
	prevResume := resumePolecatForSlingFn
	prevSpawn := spawnPolecatForSling
	t.Cleanup(func() {
		resolveTargetAgentFn = prevResolve
		resumePolecatForSlingFn = prevResume
		spawnPolecatForSling = prevSpawn
	})

	resolveTargetAgentFn = func(target string) (string, string, string, error) {
		return "", "", "", errors.New("getting pane for si-keeper: exit status 1")
	}
	resumeCalls := 0
	var gotRig, gotPolecat string
	resumePolecatForSlingFn = func(rigName, polecatName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		resumeCalls++
		gotRig, gotPolecat = rigName, polecatName
		return &SpawnedPolecatInfo{
			RigName:     rigName,
			PolecatName: polecatName,
			ClonePath:   existingClone,
			Branch:      "polecat/keeper/si-aka.37+ms43gm2z",
		}, nil
	}
	spawnPolecatForSling = func(rigName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		t.Fatalf("spawnPolecatForSling called for a resumable named polecat; " +
			"spawning fresh is the defect si-n7vl fixes")
		return nil, nil
	}

	res, err := resolveTarget("silicon/keeper", ResolveTargetOptions{
		TownRoot: townRoot,
		NoBoot:   true,
		HookBead: "si-aka.37",
	})
	if err != nil {
		t.Fatalf("resolveTarget: %v", err)
	}
	if resumeCalls != 1 {
		t.Fatalf("resume called %d times, want 1", resumeCalls)
	}
	if gotRig != "silicon" || gotPolecat != "keeper" {
		t.Fatalf("resumed rig=%q polecat=%q, want silicon/keeper", gotRig, gotPolecat)
	}
	if res.WorkDir != existingClone {
		t.Fatalf("WorkDir = %q, want the polecat's pre-existing worktree %q", res.WorkDir, existingClone)
	}
	if res.Agent != "silicon/polecats/keeper" {
		t.Fatalf("Agent = %q, want silicon/polecats/keeper", res.Agent)
	}
	if res.NewPolecatInfo == nil || res.NewPolecatInfo.Branch != "polecat/keeper/si-aka.37+ms43gm2z" {
		t.Fatalf("resumed polecat info did not carry the existing branch: %+v", res.NewPolecatInfo)
	}
}

// C, denominator: when the named polecat cannot be resumed — no worktree on
// disk, needs recovery — the fresh-spawn path must still run. Without this the
// test above could pass while every sling silently hard-failed.
func TestResolveTargetSpawnsFreshWhenNamedPolecatCannotResume(t *testing.T) {
	townRoot := t.TempDir()
	mkPolecatDir(t, townRoot, "silicon", "keeper")

	prevResolve := resolveTargetAgentFn
	prevResume := resumePolecatForSlingFn
	prevSpawn := spawnPolecatForSling
	t.Cleanup(func() {
		resolveTargetAgentFn = prevResolve
		resumePolecatForSlingFn = prevResume
		spawnPolecatForSling = prevSpawn
	})

	resolveTargetAgentFn = func(target string) (string, string, string, error) {
		return "", "", "", errors.New("simulated dead target")
	}
	resumePolecatForSlingFn = func(rigName, polecatName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		return nil, errors.New("idle polecat worktree not found")
	}
	spawnCalls := 0
	spawnPolecatForSling = func(rigName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		spawnCalls++
		return &SpawnedPolecatInfo{
			RigName:     rigName,
			PolecatName: "toast",
			ClonePath:   filepath.Join(townRoot, "silicon", "polecats", "toast", "silicon"),
		}, nil
	}

	res, err := resolveTarget("silicon/polecats/keeper", ResolveTargetOptions{
		TownRoot: townRoot,
		NoBoot:   true,
	})
	if err != nil {
		t.Fatalf("resolveTarget: %v", err)
	}
	if spawnCalls != 1 {
		t.Fatalf("spawnPolecatForSling called %d times, want 1 — an unresumable polecat must still get a fresh one", spawnCalls)
	}
	if res.Agent != "silicon/polecats/toast" {
		t.Fatalf("Agent = %q, want the freshly spawned silicon/polecats/toast", res.Agent)
	}
}
