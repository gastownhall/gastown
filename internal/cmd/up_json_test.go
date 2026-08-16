package cmd

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/jonbaldie/gastown/internal/constants"
	"github.com/jonbaldie/gastown/internal/session"
)

func TestBuildUpSummary(t *testing.T) {
	services := []ServiceStatus{
		{Name: "Daemon", Type: "daemon", OK: true, Detail: "PID 123"},
		{Name: "Deacon", Type: constants.RoleDeacon, OK: true, Detail: "gt-deacon"},
		{Name: "Mayor", Type: constants.RoleMayor, OK: false, Detail: "failed"},
	}

	summary := buildUpSummary(services)
	if summary.Total != 3 {
		t.Fatalf("Total = %d, want 3", summary.Total)
	}
	if summary.Started != 2 {
		t.Fatalf("Started = %d, want 2", summary.Started)
	}
	if summary.Failed != 1 {
		t.Fatalf("Failed = %d, want 1", summary.Failed)
	}
}

func TestEmitUpJSON_Success(t *testing.T) {
	services := []ServiceStatus{
		{Name: "Dolt", Type: "dolt", OK: true, Detail: "started (port 3306)"},
		{Name: "Daemon", Type: "daemon", OK: true, Detail: "PID 123"},
		{Name: "Deacon", Type: constants.RoleDeacon, OK: true, Detail: "gt-deacon"},
	}

	var buf bytes.Buffer
	err := emitUpJSON(&buf, services)
	if err != nil {
		t.Fatalf("emitUpJSON returned error: %v", err)
	}

	var output UpOutput
	if err := json.Unmarshal(buf.Bytes(), &output); err != nil {
		t.Fatalf("invalid JSON output: %v\noutput: %s", err, buf.String())
	}

	if !output.Success {
		t.Fatalf("Success = %v, want true", output.Success)
	}
	if len(output.Services) != 3 {
		t.Fatalf("len(Services) = %d, want 3", len(output.Services))
	}
	if output.Summary.Total != 3 || output.Summary.Started != 3 || output.Summary.Failed != 0 {
		t.Fatalf("unexpected summary: %+v", output.Summary)
	}
}

func TestEmitUpJSON_FailureReturnsSilentExitAndValidJSON(t *testing.T) {
	services := []ServiceStatus{
		{Name: "Daemon", Type: "daemon", OK: true, Detail: "PID 123"},
		{Name: "Mayor", Type: constants.RoleMayor, OK: false, Detail: "start failed"},
	}

	var buf bytes.Buffer
	err := emitUpJSON(&buf, services)
	if err == nil {
		t.Fatal("emitUpJSON should return error when a service has failed")
	}
	code, ok := IsSilentExit(err)
	if !ok {
		t.Fatalf("expected SilentExitError, got: %T (%v)", err, err)
	}
	if code != 1 {
		t.Fatalf("silent exit code = %d, want 1", code)
	}

	var output UpOutput
	if err := json.Unmarshal(buf.Bytes(), &output); err != nil {
		t.Fatalf("invalid JSON output: %v\noutput: %s", err, buf.String())
	}

	if output.Success {
		t.Fatalf("Success = %v, want false", output.Success)
	}
	if output.Summary.Total != 2 || output.Summary.Started != 1 || output.Summary.Failed != 1 {
		t.Fatalf("unexpected summary: %+v", output.Summary)
	}
}

func TestEmitUpJSON_SuccessDerivesFromServices(t *testing.T) {
	// When all services are OK, Success should be true even without explicit allOK param
	services := []ServiceStatus{
		{Name: "Dolt", Type: "dolt", OK: true, Detail: "started"},
		{Name: "Daemon", Type: "daemon", OK: true, Detail: "PID 1"},
	}
	var buf bytes.Buffer
	if err := emitUpJSON(&buf, services); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var output UpOutput
	if err := json.Unmarshal(buf.Bytes(), &output); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if !output.Success {
		t.Fatal("Success should be true when all services OK")
	}
	if output.Summary.Failed != 0 {
		t.Fatalf("Failed = %d, want 0", output.Summary.Failed)
	}

	// When Dolt fails, Success should be false and Summary.Failed should reflect it
	services[0].OK = false
	buf.Reset()
	if err := emitUpJSON(&buf, services); err == nil {
		t.Fatal("expected SilentExitError when Dolt fails")
	}
	if err := json.Unmarshal(buf.Bytes(), &output); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if output.Success {
		t.Fatal("Success should be false when Dolt fails")
	}
	if output.Summary.Failed != 1 {
		t.Fatalf("Failed = %d, want 1", output.Summary.Failed)
	}
}

func TestEmitUpJSON_SessionNames(t *testing.T) {
	rigName := "gastown"
	prefix := session.PrefixFor(rigName)

	services := []ServiceStatus{
		{
			Name:   "Crew (gastown/max)",
			Type:   constants.RoleCrew,
			Rig:    rigName,
			OK:     true,
			Detail: session.CrewSessionName(prefix, "max"),
		},
		{
			Name:   "Polecat (gastown/alpha)",
			Type:   constants.RolePolecat,
			Rig:    rigName,
			OK:     true,
			Detail: session.PolecatSessionName(prefix, "alpha"),
		},
	}

	var buf bytes.Buffer
	if err := emitUpJSON(&buf, services); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output UpOutput
	if err := json.Unmarshal(buf.Bytes(), &output); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Verify crew session name uses prefix, not rig name
	wantCrew := session.CrewSessionName(prefix, "max")
	if output.Services[0].Detail != wantCrew {
		t.Fatalf("crew Detail = %q, want %q", output.Services[0].Detail, wantCrew)
	}

	// Verify polecat session name uses prefix (format: {prefix}-{name})
	wantPolecat := session.PolecatSessionName(prefix, "alpha")
	if output.Services[1].Detail != wantPolecat {
		t.Fatalf("polecat Detail = %q, want %q", output.Services[1].Detail, wantPolecat)
	}
}
