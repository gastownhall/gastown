package refinery

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gitpkg "github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
)

// makeEngineerWithCR creates an Engineer with the CR gate enabled and injectable stubs.
func makeEngineerWithCR(
	t *testing.T,
	findPR func(branch string) (int, error),
	getStatus func(prNumber int) (*gitpkg.CRReview, time.Time, error),
) *Engineer {
	t.Helper()
	e := &Engineer{
		rig:     &rig.Rig{Name: "test-rig"},
		output:  &bytes.Buffer{},
		config:  DefaultMergeQueueConfig(),
		crFindPR:    findPR,
		crGetStatus: getStatus,
	}
	e.config.CodeRabbit = &CodeRabbitConfig{
		Enabled:       true,
		MinAgeForSkip: 10 * time.Minute,
		ParkLabel:     "cr-findings-open",
	}
	return e
}

func TestCheckCodeRabbitGate_Disabled(t *testing.T) {
	// Gate should be a no-op when CodeRabbit config is nil or disabled.
	e := &Engineer{
		output: io.Discard,
		config: DefaultMergeQueueConfig(),
	}
	// nil config
	if got := e.checkCodeRabbitGate(context.Background(), "feat/x"); got != nil {
		t.Errorf("expected nil for nil CodeRabbit config, got %+v", got)
	}
	// disabled
	e.config.CodeRabbit = &CodeRabbitConfig{Enabled: false}
	if got := e.checkCodeRabbitGate(context.Background(), "feat/x"); got != nil {
		t.Errorf("expected nil for disabled CodeRabbit config, got %+v", got)
	}
}

func TestCheckCodeRabbitGate_NoPR(t *testing.T) {
	// No PR for branch → gate is a no-op.
	e := makeEngineerWithCR(t,
		func(branch string) (int, error) { return 0, nil },
		nil,
	)
	if got := e.checkCodeRabbitGate(context.Background(), "feat/no-pr"); got != nil {
		t.Errorf("expected nil when no PR exists, got %+v", got)
	}
}

func TestCheckCodeRabbitGate_FindPRError(t *testing.T) {
	// FindPR error → fail-open (nil result).
	e := makeEngineerWithCR(t,
		func(branch string) (int, error) { return 0, io.ErrUnexpectedEOF },
		nil,
	)
	if got := e.checkCodeRabbitGate(context.Background(), "feat/api-down"); got != nil {
		t.Errorf("expected nil on FindPR error (fail-open), got %+v", got)
	}
}

func TestCheckCodeRabbitGate_GetStatusError(t *testing.T) {
	// GetStatus error → fail-open (nil result).
	e := makeEngineerWithCR(t,
		func(branch string) (int, error) { return 42, nil },
		func(prNumber int) (*gitpkg.CRReview, time.Time, error) {
			return nil, time.Time{}, io.ErrUnexpectedEOF
		},
	)
	if got := e.checkCodeRabbitGate(context.Background(), "feat/status-down"); got != nil {
		t.Errorf("expected nil on GetStatus error (fail-open), got %+v", got)
	}
}

func TestCheckCodeRabbitGate_NoCRReview_NewPR_Wait(t *testing.T) {
	// CR hasn't reviewed, PR is < 10 min old → ShouldWait.
	prCreated := time.Now().Add(-2 * time.Minute) // 2 min old
	e := makeEngineerWithCR(t,
		func(branch string) (int, error) { return 10, nil },
		func(prNumber int) (*gitpkg.CRReview, time.Time, error) {
			return nil, prCreated, nil
		},
	)
	got := e.checkCodeRabbitGate(context.Background(), "feat/new-pr")
	if got == nil {
		t.Fatal("expected CRCheckResult, got nil")
	}
	if !got.ShouldWait {
		t.Errorf("expected ShouldWait=true, got %+v", got)
	}
	if got.ShouldPark {
		t.Errorf("expected ShouldPark=false, got %+v", got)
	}
}

func TestCheckCodeRabbitGate_NoCRReview_OldPR_Proceed(t *testing.T) {
	// CR hasn't reviewed, PR is > 10 min old → proceed (nil).
	prCreated := time.Now().Add(-15 * time.Minute) // 15 min old
	e := makeEngineerWithCR(t,
		func(branch string) (int, error) { return 10, nil },
		func(prNumber int) (*gitpkg.CRReview, time.Time, error) {
			return nil, prCreated, nil
		},
	)
	if got := e.checkCodeRabbitGate(context.Background(), "feat/old-pr"); got != nil {
		t.Errorf("expected nil for old PR with no CR review, got %+v", got)
	}
}

func TestCheckCodeRabbitGate_CRApproved_Proceed(t *testing.T) {
	// CR reviewed and approved → proceed (nil).
	e := makeEngineerWithCR(t,
		func(branch string) (int, error) { return 20, nil },
		func(prNumber int) (*gitpkg.CRReview, time.Time, error) {
			return &gitpkg.CRReview{
				State: "APPROVED",
				Body:  "Looks good!",
			}, time.Now().Add(-5 * time.Minute), nil
		},
	)
	if got := e.checkCodeRabbitGate(context.Background(), "feat/approved"); got != nil {
		t.Errorf("expected nil for APPROVED CR review, got %+v", got)
	}
}

func TestCheckCodeRabbitGate_CRChangesRequested_Park(t *testing.T) {
	// CR requested changes → ShouldPark.
	e := makeEngineerWithCR(t,
		func(branch string) (int, error) { return 30, nil },
		func(prNumber int) (*gitpkg.CRReview, time.Time, error) {
			return &gitpkg.CRReview{
				State: "CHANGES_REQUESTED",
				Body:  "Please fix the indentation.",
			}, time.Now().Add(-5 * time.Minute), nil
		},
	)
	got := e.checkCodeRabbitGate(context.Background(), "feat/changes-req")
	if got == nil {
		t.Fatal("expected CRCheckResult, got nil")
	}
	if !got.ShouldPark {
		t.Errorf("expected ShouldPark=true, got %+v", got)
	}
}

func TestCheckCodeRabbitGate_CRCommented_Actionable_Park(t *testing.T) {
	// CR reviewed with COMMENTED state and actionable markers → ShouldPark.
	actionableBodies := []string{
		"Here is a 🛠️ suggestion for improvement.",
		"⚠️ This could cause a data race.",
		"🧹 Minor nit: rename variable.",
		"Nitpick: the function name is a bit long.",
		"This is an actionable comment about your implementation.",
	}
	for _, body := range actionableBodies {
		body := body
		t.Run(body[:20], func(t *testing.T) {
			e := makeEngineerWithCR(t,
				func(branch string) (int, error) { return 40, nil },
				func(prNumber int) (*gitpkg.CRReview, time.Time, error) {
					return &gitpkg.CRReview{State: "COMMENTED", Body: body},
						time.Now().Add(-5 * time.Minute), nil
				},
			)
			got := e.checkCodeRabbitGate(context.Background(), "feat/actionable")
			if got == nil || !got.ShouldPark {
				t.Errorf("body %q: expected ShouldPark=true, got %v", body, got)
			}
		})
	}
}

func TestCheckCodeRabbitGate_CRCommented_NoActionable_Proceed(t *testing.T) {
	// CR reviewed with COMMENTED state but no actionable markers → proceed.
	e := makeEngineerWithCR(t,
		func(branch string) (int, error) { return 50, nil },
		func(prNumber int) (*gitpkg.CRReview, time.Time, error) {
			return &gitpkg.CRReview{
				State: "COMMENTED",
				Body:  "Great work, just a small observation about naming.",
			}, time.Now().Add(-5 * time.Minute), nil
		},
	)
	if got := e.checkCodeRabbitGate(context.Background(), "feat/no-action"); got != nil {
		t.Errorf("expected nil for COMMENTED with no actionable markers, got %+v", got)
	}
}

func TestIsActionableCRBody(t *testing.T) {
	cases := []struct {
		body     string
		expected bool
	}{
		{"Clean review, looks good!", false},
		{"I noticed 🛠️ you could simplify this.", true},
		{"⚠️ potential memory leak", true},
		{"🧹 Remove unused import", true},
		{"Nitpick: rename this variable", true},
		{"nitpick: this could be clearer", true},
		{"This is an actionable comment about X", true},
		{"No issues found.", false},
		{"", false},
	}
	for _, tc := range cases {
		got := isActionableCRBody(tc.body)
		if got != tc.expected {
			t.Errorf("isActionableCRBody(%q) = %v, want %v", tc.body, got, tc.expected)
		}
	}
}

func TestEngineer_LoadConfig_CodeRabbit(t *testing.T) {
	tmpDir := t.TempDir()

	config := map[string]interface{}{
		"type":    "rig",
		"version": 1,
		"name":    "test-rig",
		"merge_queue": map[string]interface{}{
			"code_rabbit": map[string]interface{}{
				"enabled":         true,
				"min_age_for_skip": "5m",
				"park_label":      "cr-hold",
			},
		},
	}

	data, _ := json.MarshalIndent(config, "", "  ")
	if err := os.WriteFile(filepath.Join(tmpDir, "config.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	r := &rig.Rig{Name: "test-rig", Path: tmpDir}
	e := NewEngineer(r)
	if err := e.LoadConfig(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if e.config.CodeRabbit == nil {
		t.Fatal("expected CodeRabbit config to be set")
	}
	if !e.config.CodeRabbit.Enabled {
		t.Error("expected CodeRabbit.Enabled = true")
	}
	if e.config.CodeRabbit.MinAgeForSkip != 5*time.Minute {
		t.Errorf("expected MinAgeForSkip = 5m, got %v", e.config.CodeRabbit.MinAgeForSkip)
	}
	if e.config.CodeRabbit.ParkLabel != "cr-hold" {
		t.Errorf("expected ParkLabel = cr-hold, got %q", e.config.CodeRabbit.ParkLabel)
	}
}

func TestEngineer_LoadConfig_CodeRabbit_InvalidDuration(t *testing.T) {
	tmpDir := t.TempDir()

	config := map[string]interface{}{
		"type":    "rig",
		"version": 1,
		"name":    "test-rig",
		"merge_queue": map[string]interface{}{
			"code_rabbit": map[string]interface{}{
				"enabled":         true,
				"min_age_for_skip": "not-a-duration",
			},
		},
	}

	data, _ := json.MarshalIndent(config, "", "  ")
	if err := os.WriteFile(filepath.Join(tmpDir, "config.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	r := &rig.Rig{Name: "test-rig", Path: tmpDir}
	e := NewEngineer(r)
	if err := e.LoadConfig(); err == nil {
		t.Error("expected error for invalid min_age_for_skip duration")
	} else if !strings.Contains(err.Error(), "min_age_for_skip") {
		t.Errorf("expected error to mention min_age_for_skip, got: %v", err)
	}
}

func TestDoMerge_CRParked(t *testing.T) {
	// When CR gate returns ShouldPark, doMerge should return CRParked=true.
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.CodeRabbit = &CodeRabbitConfig{Enabled: true}

	// Stub: PR #99 exists, CR has CHANGES_REQUESTED
	e.crFindPR = func(branch string) (int, error) { return 99, nil }
	e.crGetStatus = func(prNumber int) (*gitpkg.CRReview, time.Time, error) {
		return &gitpkg.CRReview{State: "CHANGES_REQUESTED", Body: "Please fix."}, time.Now(), nil
	}

	createFeatureBranch(t, workDir, "feat/cr-parked", "test.txt", "hello")
	result := e.doMerge(context.Background(), "feat/cr-parked", "main", "gt-test")

	if result.Success {
		t.Error("expected failure (CR parked), got success")
	}
	if !result.CRParked {
		t.Errorf("expected CRParked=true, got: %+v", result)
	}
}

func TestDoMerge_CRWait(t *testing.T) {
	// When CR gate returns ShouldWait, doMerge should return CRWait=true.
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.CodeRabbit = &CodeRabbitConfig{Enabled: true, MinAgeForSkip: 10 * time.Minute}

	// Stub: PR #88 exists, CR hasn't reviewed yet, PR is only 1 min old
	e.crFindPR = func(branch string) (int, error) { return 88, nil }
	e.crGetStatus = func(prNumber int) (*gitpkg.CRReview, time.Time, error) {
		return nil, time.Now().Add(-1 * time.Minute), nil
	}

	createFeatureBranch(t, workDir, "feat/cr-wait", "test.txt", "hello")
	result := e.doMerge(context.Background(), "feat/cr-wait", "main", "gt-test")

	if result.Success {
		t.Error("expected failure (CR wait), got success")
	}
	if !result.CRWait {
		t.Errorf("expected CRWait=true, got: %+v", result)
	}
}

func TestDoMerge_CRApproved_Proceeds(t *testing.T) {
	// When CR gate passes (CR approved), doMerge proceeds normally.
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.CodeRabbit = &CodeRabbitConfig{Enabled: true}
	// Disable auto-push for test
	e.config.AutoPush = false

	// Stub: PR #77 exists, CR approved
	e.crFindPR = func(branch string) (int, error) { return 77, nil }
	e.crGetStatus = func(prNumber int) (*gitpkg.CRReview, time.Time, error) {
		return &gitpkg.CRReview{State: "APPROVED", Body: "LGTM!"}, time.Now(), nil
	}

	createFeatureBranch(t, workDir, "feat/cr-approved", "test.txt", "hello")
	result := e.doMerge(context.Background(), "feat/cr-approved", "main", "gt-test")

	if result.CRParked || result.CRWait {
		t.Errorf("CR gate should not have blocked: CRParked=%v CRWait=%v", result.CRParked, result.CRWait)
	}
}

func TestCRMinAge_DefaultFallback(t *testing.T) {
	e := &Engineer{config: DefaultMergeQueueConfig()}
	// nil CodeRabbitConfig
	if got := e.crMinAge(); got != crDefaultMinAge {
		t.Errorf("crMinAge with nil config: want %v, got %v", crDefaultMinAge, got)
	}
	// Zero MinAgeForSkip → default
	e.config.CodeRabbit = &CodeRabbitConfig{MinAgeForSkip: 0}
	if got := e.crMinAge(); got != crDefaultMinAge {
		t.Errorf("crMinAge with zero: want %v, got %v", crDefaultMinAge, got)
	}
	// Explicit value
	e.config.CodeRabbit.MinAgeForSkip = 5 * time.Minute
	if got := e.crMinAge(); got != 5*time.Minute {
		t.Errorf("crMinAge with explicit 5m: want %v, got %v", 5*time.Minute, got)
	}
}

func TestCRParkLabel_DefaultFallback(t *testing.T) {
	e := &Engineer{config: DefaultMergeQueueConfig()}
	// nil CodeRabbitConfig
	if got := e.crParkLabel(); got != crDefaultParkLabel {
		t.Errorf("crParkLabel with nil config: want %q, got %q", crDefaultParkLabel, got)
	}
	// Empty ParkLabel → default
	e.config.CodeRabbit = &CodeRabbitConfig{ParkLabel: ""}
	if got := e.crParkLabel(); got != crDefaultParkLabel {
		t.Errorf("crParkLabel with empty: want %q, got %q", crDefaultParkLabel, got)
	}
	// Custom label
	e.config.CodeRabbit.ParkLabel = "my-custom-label"
	if got := e.crParkLabel(); got != "my-custom-label" {
		t.Errorf("crParkLabel with custom: want %q, got %q", "my-custom-label", got)
	}
}
