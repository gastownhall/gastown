package deacon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeValidationPolicy(t *testing.T, townRoot, rigName string) {
	t.Helper()
	dir := filepath.Join(townRoot, rigName, "refinery", "rig", ".gastown")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	data := `{
		"type": "model-escalation",
		"version": 1,
		"enabled": true,
		"rules": [],
		"validation_failure": {
			"enabled": true,
			"local_agent": "opencode-local",
			"max_local_attempts": 1,
			"to_agent": "opencode-go",
			"max_hosted_attempts": 1,
			"repair_priority": 1
		}
	}`
	if err := os.WriteFile(filepath.Join(dir, "model-escalation.json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeValidationFailure(t *testing.T) {
	event, err := NormalizeValidationFailure(ValidationFailure{
		Rig:         " canary ",
		SourceIssue: "c-123",
		Branch:      "polecat/test",
		Phase:       "PRE-MERGE",
		Kind:        "TEST",
		Summary:     "fails",
		Evidence:    strings.Repeat("x", maxValidationEvidence+100),
	})
	if err != nil {
		t.Fatalf("NormalizeValidationFailure: %v", err)
	}
	if event.Rig != "canary" || event.Phase != "pre-merge" || event.Kind != "test" {
		t.Fatalf("event was not normalized: %+v", event)
	}
	if len(event.Evidence) > maxValidationEvidence+32 {
		t.Fatalf("evidence was not bounded: %d", len(event.Evidence))
	}
	if event.ReportedAt.IsZero() {
		t.Fatal("reported_at was not populated")
	}
}

func TestProcessValidationFailureDispatchDeduplicateEscalate(t *testing.T) {
	townRoot := t.TempDir()
	writeValidationPolicy(t, townRoot, "canary")

	originalDispatch := dispatchValidationRepairFn
	originalEscalate := escalateValidationFn
	t.Cleanup(func() {
		dispatchValidationRepairFn = originalDispatch
		escalateValidationFn = originalEscalate
	})

	var dispatchedAgents []string
	escalations := 0
	dispatchValidationRepairFn = func(townRoot, beadID, rigName, branch, agent, incidentID string) error {
		dispatchedAgents = append(dispatchedAgents, agent)
		if beadID != "c-123" || rigName != "canary" || branch != "polecat/test" {
			t.Fatalf("unexpected dispatch: bead=%s rig=%s branch=%s agent=%s", beadID, rigName, branch, agent)
		}
		return nil
	}
	escalateValidationFn = func(townRoot, incidentID string, event ValidationFailure) error {
		escalations++
		return nil
	}

	first := ValidationFailure{
		Rig:          "canary",
		SourceIssue:  "c-123",
		MergeRequest: "mr-1",
		Branch:       "polecat/test",
		Commit:       "aaaaaaaa",
		Phase:        "pre-merge",
		Kind:         "test",
		Command:      "go test ./...",
		ExitCode:     1,
		Summary:      "unit test failed",
		Evidence:     "first output",
	}
	result := ProcessValidationFailure(townRoot, first)
	if result.Action != "dispatched" || result.Error != nil {
		t.Fatalf("first result = %+v", result)
	}
	if len(dispatchedAgents) != 1 || dispatchedAgents[0] != "opencode-local" || escalations != 0 {
		t.Fatalf("dispatches=%v escalations=%d", dispatchedAgents, escalations)
	}

	duplicate := ProcessValidationFailure(townRoot, first)
	if duplicate.Action != "duplicate" {
		t.Fatalf("duplicate action = %q", duplicate.Action)
	}
	if len(dispatchedAgents) != 1 || escalations != 0 {
		t.Fatalf("duplicate caused side effect: dispatches=%v escalations=%d", dispatchedAgents, escalations)
	}

	second := first
	second.Commit = "bbbbbbbb"
	second.Evidence = "hosted repair still fails"
	secondResult := ProcessValidationFailure(townRoot, second)
	if secondResult.Action != "dispatched" || secondResult.Error != nil {
		t.Fatalf("second result = %+v", secondResult)
	}
	if len(dispatchedAgents) != 2 || dispatchedAgents[1] != "opencode-go" || escalations != 0 {
		t.Fatalf("dispatches=%v escalations=%d", dispatchedAgents, escalations)
	}

	third := second
	third.Commit = "cccccccc"
	third.Evidence = "Go repair still fails"
	thirdResult := ProcessValidationFailure(townRoot, third)
	if thirdResult.Action != "escalated" || thirdResult.Error != nil {
		t.Fatalf("third result = %+v", thirdResult)
	}
	if len(dispatchedAgents) != 2 || escalations != 1 {
		t.Fatalf("dispatches=%v escalations=%d", dispatchedAgents, escalations)
	}

	state, err := LoadValidationState(townRoot)
	if err != nil {
		t.Fatal(err)
	}
	incident := state.Incidents[result.IncidentID]
	if incident == nil || incident.LocalAttempts != 1 || incident.HostedAttempts != 1 || incident.Status != "escalated" {
		t.Fatalf("unexpected incident: %+v", incident)
	}
	if len(incident.Observations) != 3 {
		t.Fatalf("observations=%d, want 3", len(incident.Observations))
	}
}

func TestProcessPostMergeCreatesOneRepairBead(t *testing.T) {
	townRoot := t.TempDir()
	writeValidationPolicy(t, townRoot, "canary")

	originalDispatch := dispatchValidationRepairFn
	originalCreate := createValidationRepairFn
	t.Cleanup(func() {
		dispatchValidationRepairFn = originalDispatch
		createValidationRepairFn = originalCreate
	})

	creates := 0
	createValidationRepairFn = func(townRoot string, event ValidationFailure, priority int, incidentID string) (string, error) {
		creates++
		if priority != 1 || event.SourceIssue != "c-source" {
			t.Fatalf("unexpected repair request: priority=%d event=%+v", priority, event)
		}
		return "c-repair", nil
	}
	dispatchValidationRepairFn = func(townRoot, beadID, rigName, branch, agent, incidentID string) error {
		if beadID != "c-repair" || branch != "" {
			t.Fatalf("unexpected repair dispatch: bead=%s branch=%s", beadID, branch)
		}
		return nil
	}

	event := ValidationFailure{
		Rig:         "canary",
		SourceIssue: "c-source",
		Commit:      "deadbeef",
		Phase:       "post-merge",
		Kind:        "functional",
		Summary:     "endpoint returns 500",
		Evidence:    "reproduced twice",
	}
	first := ProcessValidationFailure(townRoot, event)
	if first.Action != "dispatched" || first.RepairBead != "c-repair" {
		t.Fatalf("first result = %+v", first)
	}
	second := ProcessValidationFailure(townRoot, event)
	if second.Action != "duplicate" || creates != 1 {
		t.Fatalf("second result=%+v creates=%d", second, creates)
	}
}

func TestCreateValidationRepairUsesRefineryWorktree(t *testing.T) {
	townRoot := t.TempDir()
	workDir := filepath.Join(townRoot, "canary", "refinery", "rig")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	cwdLog := filepath.Join(t.TempDir(), "cwd")
	script := "#!/bin/sh\npwd > \"$BD_CWD_LOG\"\nprintf 'cy-repair\\n'\n"
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BD_CWD_LOG", cwdLog)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	id, err := createValidationRepair(townRoot, ValidationFailure{
		Rig:         "canary",
		SourceIssue: "cy-source",
		Commit:      "deadbeef",
		Kind:        "test",
		Command:     "go test ./...",
		Summary:     "tests fail",
		Evidence:    "failure output",
	}, 1, "vf-test")
	if err != nil {
		t.Fatalf("createValidationRepair: %v", err)
	}
	if id != "cy-repair" {
		t.Fatalf("repair id = %q", id)
	}
	logged, err := os.ReadFile(cwdLog)
	if err != nil {
		t.Fatal(err)
	}
	wantDir, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		t.Fatal(err)
	}
	gotDir, err := filepath.EvalSymlinks(strings.TrimSpace(string(logged)))
	if err != nil {
		t.Fatal(err)
	}
	if gotDir != wantDir {
		t.Fatalf("bd cwd = %q, want %q", gotDir, wantDir)
	}
}

func TestValidationStateIsJSONAndResolutionIsDurable(t *testing.T) {
	townRoot := t.TempDir()
	if err := withValidationState(townRoot, func(state *ValidationState) error {
		state.Incidents["vf-test"] = &ValidationIncident{
			ID:          "vf-test",
			Rig:         "canary",
			Phase:       "pre-merge",
			SourceIssue: "c-1",
			Status:      "hosted-dispatched",
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveValidationIncidents(townRoot, "", "canary", "c-1", "pre-merge")
	if err != nil || resolved != 1 {
		t.Fatalf("resolved=%d err=%v", resolved, err)
	}
	data, err := os.ReadFile(ValidationStateFile(townRoot))
	if err != nil {
		t.Fatal(err)
	}
	var decoded ValidationState
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("state is not valid JSON: %v", err)
	}
	if decoded.Incidents["vf-test"].Status != "resolved" {
		t.Fatalf("incident not resolved: %+v", decoded.Incidents["vf-test"])
	}
}
