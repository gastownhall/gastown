package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultLifecycleConfig(t *testing.T) {
	config := DefaultLifecycleConfig()

	if config.Type != "daemon-patrol-config" {
		t.Errorf("expected type daemon-patrol-config, got %s", config.Type)
	}
	if config.Version != 1 {
		t.Errorf("expected version 1, got %d", config.Version)
	}
	if config.Patrols == nil {
		t.Fatal("expected patrols to be non-nil")
	}

	p := config.Patrols

	// Verify all patrols are enabled with expected defaults
	if p.WispReaper == nil || !p.WispReaper.Enabled {
		t.Error("expected wisp_reaper to be enabled")
	}
	if p.WispReaper.IntervalStr != "30m" {
		t.Errorf("expected wisp_reaper interval 30m, got %s", p.WispReaper.IntervalStr)
	}
	if p.WispReaper.DeleteAgeStr != "24h" {
		t.Errorf("expected wisp_reaper delete_age 24h, got %s", p.WispReaper.DeleteAgeStr)
	}

	if p.CompactorDog == nil || !p.CompactorDog.Enabled {
		t.Error("expected compactor_dog to be enabled")
	}
	if p.CompactorDog.Threshold != 2000 {
		t.Errorf("expected compactor_dog threshold 2000, got %d", p.CompactorDog.Threshold)
	}

	if p.CheckpointDog == nil || !p.CheckpointDog.Enabled {
		t.Error("expected checkpoint_dog to be enabled")
	}
	if p.CheckpointDog.IntervalStr != "10m" {
		t.Errorf("expected checkpoint_dog interval 10m, got %s", p.CheckpointDog.IntervalStr)
	}

	if p.DoctorDog == nil || !p.DoctorDog.Enabled {
		t.Error("expected doctor_dog to be enabled")
	}

	if p.JsonlGitBackup == nil || !p.JsonlGitBackup.Enabled {
		t.Error("expected jsonl_git_backup to be enabled")
	}
	if p.JsonlGitBackup.Scrub == nil || !*p.JsonlGitBackup.Scrub {
		t.Error("expected jsonl_git_backup scrub to be true")
	}

	if p.DoltBackup == nil || !p.DoltBackup.Enabled {
		t.Error("expected dolt_backup to be enabled")
	}

	if p.ScheduledMaintenance == nil || !p.ScheduledMaintenance.Enabled {
		t.Error("expected scheduled_maintenance to be enabled")
	}
	if p.ScheduledMaintenance.Window != "03:00" {
		t.Errorf("expected maintenance window 03:00, got %s", p.ScheduledMaintenance.Window)
	}
	if p.ScheduledMaintenance.Threshold == nil || *p.ScheduledMaintenance.Threshold != 1000 {
		t.Error("expected maintenance threshold 1000")
	}

	if p.MainBranchTest == nil || !p.MainBranchTest.Enabled {
		t.Error("expected main_branch_test to be enabled")
	}
	if p.MainBranchTest.IntervalStr != "30m" {
		t.Errorf("expected main_branch_test interval 30m, got %s", p.MainBranchTest.IntervalStr)
	}
	if p.MainBranchTest.TimeoutStr != "10m" {
		t.Errorf("expected main_branch_test timeout 10m, got %s", p.MainBranchTest.TimeoutStr)
	}
}

func TestEnsureLifecycleDefaults_NilConfig(t *testing.T) {
	if EnsureLifecycleDefaults(nil) {
		t.Error("expected false for nil config")
	}
}

func TestEnsureLifecycleDefaults_EmptyConfig(t *testing.T) {
	config := &DaemonPatrolConfig{Type: "daemon-patrol-config", Version: 1}
	changed := EnsureLifecycleDefaults(config)

	if !changed {
		t.Error("expected changes for empty config")
	}
	if config.Patrols == nil {
		t.Fatal("expected patrols to be set")
	}
	if config.Patrols.WispReaper == nil || !config.Patrols.WispReaper.Enabled {
		t.Error("expected wisp_reaper to be set")
	}
	if config.Patrols.CompactorDog == nil || !config.Patrols.CompactorDog.Enabled {
		t.Error("expected compactor_dog to be set")
	}
	if config.Patrols.CheckpointDog == nil || !config.Patrols.CheckpointDog.Enabled {
		t.Error("expected checkpoint_dog to be set")
	}
	if config.Patrols.Handler == nil || !config.Patrols.Handler.Enabled {
		t.Error("expected handler to be set")
	}
}

func TestEnsureLifecycleDefaults_PreservesExisting(t *testing.T) {
	// Config with user-customized wisp_reaper
	config := &DaemonPatrolConfig{
		Type:    "daemon-patrol-config",
		Version: 1,
		Patrols: &PatrolsConfig{
			WispReaper: &WispReaperConfig{
				Enabled:     true,
				IntervalStr: "1h", // User customized to 1h
				DeleteAgeStr: "336h", // User customized to 14 days
			},
		},
	}

	changed := EnsureLifecycleDefaults(config)

	if !changed {
		t.Error("expected changes (other patrols were nil)")
	}

	// User's wisp_reaper should be preserved
	if config.Patrols.WispReaper.IntervalStr != "1h" {
		t.Errorf("expected preserved interval 1h, got %s", config.Patrols.WispReaper.IntervalStr)
	}
	if config.Patrols.WispReaper.DeleteAgeStr != "336h" {
		t.Errorf("expected preserved delete_age 336h, got %s", config.Patrols.WispReaper.DeleteAgeStr)
	}

	// Other patrols should be filled in
	if config.Patrols.CompactorDog == nil || !config.Patrols.CompactorDog.Enabled {
		t.Error("expected compactor_dog to be filled in")
	}
	if config.Patrols.DoctorDog == nil {
		t.Error("expected doctor_dog to be filled in")
	}
}

func TestEnsureLifecycleDefaults_FullyConfigured(t *testing.T) {
	// Config with all patrols already set (even if disabled)
	threshold := 2000
	config := &DaemonPatrolConfig{
		Type:    "daemon-patrol-config",
		Version: 1,
		Patrols: &PatrolsConfig{
			WispReaper:           &WispReaperConfig{Enabled: false},
			CompactorDog:         &CompactorDogConfig{Enabled: false},
			CheckpointDog:        &CheckpointDogConfig{Enabled: false},
			DoctorDog:            &DoctorDogConfig{Enabled: false},
			JsonlGitBackup:       &JsonlGitBackupConfig{Enabled: false},
			DoltBackup:           &DoltBackupConfig{Enabled: false},
			ScheduledMaintenance: &ScheduledMaintenanceConfig{Enabled: false, Threshold: &threshold},
			MainBranchTest:       &MainBranchTestConfig{Enabled: false},
			Handler:              &PatrolConfig{Enabled: false},
		},
	}

	changed := EnsureLifecycleDefaults(config)

	if changed {
		t.Error("expected no changes for fully configured config")
	}

	// User's disabled settings should be preserved
	if config.Patrols.WispReaper.Enabled {
		t.Error("expected wisp_reaper to remain disabled")
	}
	if config.Patrols.ScheduledMaintenance.Threshold == nil || *config.Patrols.ScheduledMaintenance.Threshold != 2000 {
		t.Error("expected threshold to remain 2000")
	}
}

func TestEnsureLifecycleConfigFile_NewFile(t *testing.T) {
	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}

	err := EnsureLifecycleConfigFile(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify file was created
	configFile := filepath.Join(mayorDir, "daemon.json")
	data, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatalf("config file not created: %v", err)
	}

	var config DaemonPatrolConfig
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if config.Patrols == nil {
		t.Fatal("expected patrols in created config")
	}
	if config.Patrols.WispReaper == nil || !config.Patrols.WispReaper.Enabled {
		t.Error("expected wisp_reaper to be enabled in new config")
	}
}

func TestEnsureLifecycleConfigFile_ExistingPartial(t *testing.T) {
	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write partial config with just env and wisp_reaper
	existing := &DaemonPatrolConfig{
		Type:    "daemon-patrol-config",
		Version: 1,
		Env:     map[string]string{"GT_DOLT_PORT": "3307"},
		Patrols: &PatrolsConfig{
			WispReaper: &WispReaperConfig{
				Enabled:     true,
				IntervalStr: "1h",
			},
		},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	configFile := filepath.Join(mayorDir, "daemon.json")
	if err := os.WriteFile(configFile, data, 0644); err != nil {
		t.Fatal(err)
	}

	err := EnsureLifecycleConfigFile(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Reload and verify
	data, _ = os.ReadFile(configFile)
	var config DaemonPatrolConfig
	json.Unmarshal(data, &config)

	// Existing env preserved
	if config.Env["GT_DOLT_PORT"] != "3307" {
		t.Error("expected env to be preserved")
	}

	// Existing wisp_reaper preserved
	if config.Patrols.WispReaper.IntervalStr != "1h" {
		t.Errorf("expected preserved interval 1h, got %s", config.Patrols.WispReaper.IntervalStr)
	}

	// New patrols filled in
	if config.Patrols.CompactorDog == nil || !config.Patrols.CompactorDog.Enabled {
		t.Error("expected compactor_dog to be added")
	}
	if config.Patrols.DoctorDog == nil || !config.Patrols.DoctorDog.Enabled {
		t.Error("expected doctor_dog to be added")
	}
	if config.Patrols.ScheduledMaintenance == nil || !config.Patrols.ScheduledMaintenance.Enabled {
		t.Error("expected scheduled_maintenance to be added")
	}
}

func TestEnsureLifecycleConfigFile_ProductionScenario(t *testing.T) {
	// Simulates the actual production daemon.json: has core patrols (deacon,
	// refinery, witness) and explicitly disabled dolt_backup, but is missing
	// all data maintenance tickers (wisp_reaper, compactor_dog, doctor_dog,
	// jsonl_git_backup, scheduled_maintenance).
	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}

	existing := &DaemonPatrolConfig{
		Type:    "daemon-patrol-config",
		Version: 1,
		Patrols: &PatrolsConfig{
			Deacon:   &PatrolConfig{Enabled: true, Interval: "5m", Agent: "deacon"},
			Refinery: &PatrolConfig{Enabled: true, Interval: "5m", Agent: "refinery"},
			Witness:  &PatrolConfig{Enabled: true, Interval: "5m", Agent: "witness"},
			DoltBackup: &DoltBackupConfig{Enabled: false},
		},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	configFile := filepath.Join(mayorDir, "daemon.json")
	if err := os.WriteFile(configFile, data, 0644); err != nil {
		t.Fatal(err)
	}

	err := EnsureLifecycleConfigFile(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Reload and verify
	data, _ = os.ReadFile(configFile)
	var config DaemonPatrolConfig
	json.Unmarshal(data, &config)

	// Core patrols preserved
	if config.Patrols.Deacon == nil || !config.Patrols.Deacon.Enabled {
		t.Error("expected deacon to remain enabled")
	}
	if config.Patrols.Refinery == nil || !config.Patrols.Refinery.Enabled {
		t.Error("expected refinery to remain enabled")
	}
	if config.Patrols.Witness == nil || !config.Patrols.Witness.Enabled {
		t.Error("expected witness to remain enabled")
	}

	// Explicitly disabled dolt_backup preserved (user intent)
	if config.Patrols.DoltBackup == nil {
		t.Fatal("expected dolt_backup config to be preserved")
	}
	if config.Patrols.DoltBackup.Enabled {
		t.Error("expected dolt_backup to remain disabled (user explicitly set false)")
	}

	// Missing lifecycle tickers auto-populated with defaults
	if config.Patrols.WispReaper == nil || !config.Patrols.WispReaper.Enabled {
		t.Error("expected wisp_reaper to be auto-populated and enabled")
	}
	if config.Patrols.CompactorDog == nil || !config.Patrols.CompactorDog.Enabled {
		t.Error("expected compactor_dog to be auto-populated and enabled")
	}
	if config.Patrols.CheckpointDog == nil || !config.Patrols.CheckpointDog.Enabled {
		t.Error("expected checkpoint_dog to be auto-populated and enabled")
	}
	if config.Patrols.DoctorDog == nil || !config.Patrols.DoctorDog.Enabled {
		t.Error("expected doctor_dog to be auto-populated and enabled")
	}
	if config.Patrols.JsonlGitBackup == nil || !config.Patrols.JsonlGitBackup.Enabled {
		t.Error("expected jsonl_git_backup to be auto-populated and enabled")
	}
	if config.Patrols.ScheduledMaintenance == nil || !config.Patrols.ScheduledMaintenance.Enabled {
		t.Error("expected scheduled_maintenance to be auto-populated and enabled")
	}
}

func TestEnsureLifecycleConfigFile_AlreadyComplete(t *testing.T) {
	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write fully configured file
	config := DefaultLifecycleConfig()
	data, _ := json.MarshalIndent(config, "", "  ")
	configFile := filepath.Join(mayorDir, "daemon.json")
	if err := os.WriteFile(configFile, data, 0644); err != nil {
		t.Fatal(err)
	}

	// Get mod time before
	info1, _ := os.Stat(configFile)

	err := EnsureLifecycleConfigFile(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// File should not have been rewritten (same mod time)
	info2, _ := os.Stat(configFile)
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Error("expected file to not be rewritten when already complete")
	}
}
