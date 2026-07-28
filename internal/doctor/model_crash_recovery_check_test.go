package doctor

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestModelCrashRecoveryCheckPassesWithoutState(t *testing.T) {
	townRoot := t.TempDir()
	writeHealthyModelCrashWatchdog(t, townRoot)
	check := NewModelCrashRecoveryCheck()
	result := check.Run(&CheckContext{TownRoot: townRoot})
	if result.Status != StatusOK {
		t.Fatalf("status = %s, want OK: %#v", result.Status, result)
	}
}

func TestModelCrashRecoveryCheckSkipsUnprovisionedTown(t *testing.T) {
	result := NewModelCrashRecoveryCheck().Run(&CheckContext{TownRoot: t.TempDir()})
	if result.Status != StatusOK {
		t.Fatalf("unprovisioned town reported watchdog failure: %#v", result)
	}
}

func TestModelCrashRecoveryCheckContractMarkerRequiresWatchdog(t *testing.T) {
	townRoot := t.TempDir()
	marker := filepath.Join(townRoot, "bin", "gt-lmstudio-watchdog")
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	result := NewModelCrashRecoveryCheck().Run(&CheckContext{TownRoot: townRoot})
	if result.Status != StatusError {
		t.Fatalf("installed watchdog contract without state was not strict: %#v", result)
	}
}

func TestModelCrashRecoveryCheckSurfacesUnavailableWatchdog(t *testing.T) {
	tests := []struct {
		name  string
		write func(*testing.T, string)
	}{
		{name: "missing"},
		{name: "malformed", write: func(t *testing.T, townRoot string) {
			writeModelCrashWatchdogFile(t, townRoot, []byte(`{"status":`))
		}},
		{name: "stale", write: func(t *testing.T, townRoot string) {
			checkedAt := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339Nano)
			writeModelCrashWatchdogFile(t, townRoot, []byte(`{"status":"healthy","checked_at":"`+checkedAt+`"}`))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			townRoot := t.TempDir()
			writeModelCrashSupervisorMarker(t, townRoot)
			if tt.write != nil {
				tt.write(t, townRoot)
			}
			result := NewModelCrashRecoveryCheck().Run(&CheckContext{TownRoot: townRoot})
			if result.Status != StatusError || len(result.Details) == 0 {
				t.Fatalf("unavailable watchdog was not visible: %#v", result)
			}
		})
	}
}

func TestModelCrashRecoveryCheckDistinguishesActiveAndExhaustedIncident(t *testing.T) {
	tests := []struct {
		name      string
		action    string
		exhausted bool
		want      CheckStatus
	}{
		{name: "confirmed recovery active", action: "local-restart", want: StatusWarning},
		{name: "recovery exhausted", action: "awaiting-local-probe", exhausted: true, want: StatusError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			townRoot := t.TempDir()
			writeHealthyModelCrashWatchdog(t, townRoot)
			stateDir := filepath.Join(townRoot, "deacon")
			if err := os.MkdirAll(stateDir, 0o700); err != nil {
				t.Fatal(err)
			}
			state := `{
				"version": 1,
				"sessions": {
					"rig/polecats/toast": {
						"session_name": "gt-toast",
						"incident_id": "model-crash-1-toast",
						"local_restarts": 1,
						"recovery_action": "` + tt.action + `",
						"recovery_exhausted": ` + boolJSON(tt.exhausted) + `
					}
				},
				"alerts": {}
			}`
			if err := os.WriteFile(filepath.Join(stateDir, "model-crash-supervisor.json"), []byte(state), 0o600); err != nil {
				t.Fatal(err)
			}

			check := NewModelCrashRecoveryCheck()
			result := check.Run(&CheckContext{TownRoot: townRoot})
			if result.Status != tt.want {
				t.Fatalf("status = %s, want %s: %#v", result.Status, tt.want, result)
			}
			if result.Message == "" || len(result.Details) == 0 {
				t.Fatalf("doctor result lacks incident visibility: %#v", result)
			}
		})
	}
}

func writeModelCrashSupervisorMarker(t *testing.T, townRoot string) {
	t.Helper()
	stateDir := filepath.Join(townRoot, "deacon")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "model-crash-supervisor.json"), []byte(`{"version":1,"sessions":{},"alerts":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeHealthyModelCrashWatchdog(t *testing.T, townRoot string) {
	t.Helper()
	checkedAt := time.Now().Add(-30 * time.Second).UTC().Format(time.RFC3339Nano)
	writeModelCrashWatchdogFile(t, townRoot, []byte(`{"status":"healthy","checked_at":"`+checkedAt+`"}`))
}

func writeModelCrashWatchdogFile(t *testing.T, townRoot string, data []byte) {
	t.Helper()
	stateDir := filepath.Join(townRoot, "deacon")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "lmstudio-watchdog.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func boolJSON(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
