package cmd

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/dog"
)

func runSlingFormulaSourceForTest(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("sling_formula.go")
	if err != nil {
		t.Fatalf("read sling_formula.go: %v", err)
	}
	source := string(data)
	funcStart := strings.Index(source, "func runSlingFormula(")
	if funcStart == -1 {
		t.Fatal("runSlingFormula not found")
	}
	body := source[funcStart:]
	nextFunc := strings.Index(body[1:], "\nfunc ")
	if nextFunc != -1 {
		body = body[:nextFunc+1]
	}
	return body
}

func TestRunSlingFormulaCleansDelayedDogFailure(t *testing.T) {
	body := runSlingFormulaSourceForTest(t)

	for _, want := range []string{
		") (err error)",
		"defer func()",
		"cleanupDelayedDogFormulaFailure(err, delayedDogInfo, cleanupID, formulaWorkDir)",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("runSlingFormula missing %q", want)
		}
	}

	unlockDeferIdx := strings.Index(body, "defer assigneeUnlock()")
	cleanupDeferIdx := strings.Index(body, "defer func()")
	if unlockDeferIdx == -1 || cleanupDeferIdx == -1 || unlockDeferIdx > cleanupDeferIdx {
		t.Fatal("dog formula cleanup must be deferred after assignee unlock so it runs before unlocking")
	}
}

func TestCleanupDelayedDogFormulaFailureClearsWorkAfterWispCleanupError(t *testing.T) {
	prevCleanup := cleanupFailedDogFormulaWispFn
	cleanupFailedDogFormulaWispFn = func(string, string) error {
		return errors.New("close failed")
	}
	t.Cleanup(func() { cleanupFailedDogFormulaWispFn = prevCleanup })

	townRoot := t.TempDir()
	rigsConfig := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	startedAt := time.Now().Truncate(time.Second)
	writeDogStateForDispatchTest(t, townRoot, "alpha", &dog.DogState{
		Name:          "alpha",
		State:         dog.StateWorking,
		Work:          "mol-dog-reaper",
		WorkStartedAt: startedAt,
		LastActive:    startedAt,
		CreatedAt:     startedAt,
		UpdatedAt:     startedAt,
	})
	dispatch := &DogDispatchInfo{
		DogName:       "alpha",
		townRoot:      townRoot,
		workDesc:      "mol-dog-reaper",
		workStartedAt: startedAt,
		ownsWork:      true,
		rigsConfig:    rigsConfig,
	}

	err := cleanupDelayedDogFormulaFailure(errors.New("start failed"), dispatch, "gt-wisp", townRoot)
	if err == nil || !strings.Contains(err.Error(), "start failed") || !strings.Contains(err.Error(), "close failed") {
		t.Fatalf("cleanup error = %v, want joined start and close errors", err)
	}

	got, err := dog.NewManager(townRoot, rigsConfig).Get("alpha")
	if err != nil {
		t.Fatalf("Get() after cleanup: %v", err)
	}
	if got.State != dog.StateIdle || got.Work != "" || !got.WorkStartedAt.IsZero() {
		t.Fatalf("cleanup did not clear dog assignment: state=%q work=%q started=%v", got.State, got.Work, got.WorkStartedAt)
	}
}

func TestRunSlingFormulaSerializesWholeDogPool(t *testing.T) {
	body := runSlingFormulaSourceForTest(t)
	if !strings.Contains(body, `tryAcquireSlingAssigneeLock(townRoot, "deacon/dogs")`) {
		t.Fatal("dog-pool formula dispatch must use one pool-wide lock, not a per-formula lock")
	}
	if strings.Contains(body, `tryAcquireSlingAssigneeLock(townRoot, "deacon/dogs/"+formulaName)`) {
		t.Fatal("dog-pool formula dispatch still uses per-formula locking")
	}
}

func TestRunSlingFormulaExistingHookedDogStartsDelayedSession(t *testing.T) {
	body := runSlingFormulaSourceForTest(t)

	existingIdx := strings.Index(body, "shouldReuseExistingFormula(existing, delayedDogInfo, slingForce)")
	if existingIdx == -1 {
		t.Fatal("existing hooked formula no-op block not found")
	}
	existingBlock := body[existingIdx:]
	stepIdx := strings.Index(existingBlock, "\n\t// Step 1:")
	if stepIdx == -1 {
		t.Fatal("could not isolate existing hooked formula block")
	}
	existingBlock = existingBlock[:stepIdx]
	startIdx := strings.Index(existingBlock, "delayedDogInfo.StartDelayedSession()")
	completeIdx := strings.Index(existingBlock, "delayedDogComplete = true")
	nudgeIdx := strings.Index(existingBlock, "nudgeFormulaDog(delayedDogInfo, formulaSlingPrompt(formulaName))")
	returnIdx := strings.LastIndex(existingBlock, "return nil")
	if startIdx == -1 {
		t.Fatal("existing hooked formula path must start the delayed dog session")
	}
	if completeIdx == -1 || completeIdx < startIdx {
		t.Fatal("existing hooked formula path must mark delayed dog startup complete")
	}
	if nudgeIdx == -1 || nudgeIdx < completeIdx {
		t.Fatal("existing hooked formula path must nudge the dog before returning")
	}
	if returnIdx != -1 && returnIdx < nudgeIdx {
		t.Fatal("existing hooked formula path returns before starting/nudging dog")
	}
}

func TestRunSlingFormulaNonOwnedDogReuseCannotCreateFreshWisp(t *testing.T) {
	body := runSlingFormulaSourceForTest(t)
	reuseIdx := strings.Index(body, "shouldReuseExistingFormula(existing, delayedDogInfo, slingForce)")
	guardIdx := strings.Index(body, "delayedDogInfo != nil && !delayedDogInfo.ownsWork")
	stepIdx := strings.Index(body, "// Step 1: Cook the formula")
	if reuseIdx == -1 || guardIdx == -1 || stepIdx == -1 {
		t.Fatal("could not find dog reuse guard or cook step")
	}
	if guardIdx < reuseIdx || guardIdx > stepIdx {
		t.Fatal("non-owned dog reuse must abort before creating a fresh formula wisp")
	}
}

func TestRunSlingFormulaDogNudgeBeforeEmptyPaneReturn(t *testing.T) {
	body := runSlingFormulaSourceForTest(t)

	dogNudgeIdx := strings.LastIndex(body, "nudgeFormulaDog(delayedDogInfo, prompt)")
	emptyPaneIdx := strings.Index(body, "if targetPane == \"\" {")
	if dogNudgeIdx == -1 {
		t.Fatal("dog-specific nudge call not found")
	}
	if emptyPaneIdx == -1 {
		t.Fatal("empty-pane return block not found")
	}
	if dogNudgeIdx > emptyPaneIdx {
		t.Fatal("dog-specific nudge must run before generic empty-pane return")
	}
}
