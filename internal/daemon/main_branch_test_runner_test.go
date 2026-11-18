package daemon

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"

	gtconfig "github.com/steveyegge/gastown/internal/config"
)

func TestMainBranchTestInterval(t *testing.T) {
	// Nil config returns default
	if got := mainBranchTestInterval(nil); got != defaultMainBranchTestInterval {
		t.Errorf("expected default %v, got %v", defaultMainBranchTestInterval, got)
	}

	// Configured interval
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			MainBranchTest: &MainBranchTestConfig{
				Enabled:     true,
				IntervalStr: "15m",
			},
		},
	}
	if got := mainBranchTestInterval(config); got.Minutes() != 15 {
		t.Errorf("expected 15m, got %v", got)
	}

	// Invalid interval returns default
	config.Patrols.MainBranchTest.IntervalStr = "bad"
	if got := mainBranchTestInterval(config); got != defaultMainBranchTestInterval {
		t.Errorf("expected default for invalid interval, got %v", got)
	}
}

func TestMainBranchTestTimeout(t *testing.T) {
	// Nil config returns default
	if got := mainBranchTestTimeout(nil); got != defaultMainBranchTestTimeout {
		t.Errorf("expected default %v, got %v", defaultMainBranchTestTimeout, got)
	}

	// Configured timeout
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			MainBranchTest: &MainBranchTestConfig{
				Enabled:    true,
				TimeoutStr: "5m",
			},
		},
	}
	if got := mainBranchTestTimeout(config); got.Minutes() != 5 {
		t.Errorf("expected 5m, got %v", got)
	}
}

func TestMainBranchTestRigs(t *testing.T) {
	// Nil config returns nil
	if got := mainBranchTestRigs(nil); got != nil {
		t.Errorf("expected nil, got %v", got)
	}

	// Configured rigs
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			MainBranchTest: &MainBranchTestConfig{
				Enabled: true,
				Rigs:    []string{"gastown", "beads"},
			},
		},
	}
	got := mainBranchTestRigs(config)
	if len(got) != 2 || got[0] != "gastown" || got[1] != "beads" {
		t.Errorf("expected [gastown beads], got %v", got)
	}
}

func TestIsPatrolEnabledMainBranchTest(t *testing.T) {
	// Nil config — disabled (opt-in)
	if IsPatrolEnabled(nil, "main_branch_test") {
		t.Error("expected main_branch_test disabled with nil config")
	}

	// Explicitly disabled
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			MainBranchTest: &MainBranchTestConfig{
				Enabled: false,
			},
		},
	}
	if IsPatrolEnabled(config, "main_branch_test") {
		t.Error("expected main_branch_test disabled when Enabled=false")
	}

	// Enabled
	config.Patrols.MainBranchTest.Enabled = true
	if !IsPatrolEnabled(config, "main_branch_test") {
		t.Error("expected main_branch_test enabled when Enabled=true")
	}
}

func TestLoadRigGateConfig(t *testing.T) {
	t.Run("no config file", func(t *testing.T) {
		cfg, err := loadRigGateConfig("/nonexistent/path")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg != nil {
			t.Errorf("expected nil config for nonexistent path, got %+v", cfg)
		}
	})

	t.Run("no merge_queue section", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "settings"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := gtconfig.SaveRigSettings(gtconfig.RigSettingsPath(dir), gtconfig.NewRigSettings()); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadRigGateConfig(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg != nil {
			t.Errorf("expected nil config for no merge_queue, got %+v", cfg)
		}
	})

	t.Run("test_command only", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "settings"), 0755); err != nil {
			t.Fatal(err)
		}
		settings := gtconfig.NewRigSettings()
		settings.MergeQueue = &gtconfig.MergeQueueConfig{TestCommand: "go test ./..."}
		if err := gtconfig.SaveRigSettings(gtconfig.RigSettingsPath(dir), settings); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadRigGateConfig(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg == nil {
			t.Fatal("expected non-nil config")
		}
		if len(cfg.Commands) != 1 || cfg.Commands[0].Command != "go test ./..." {
			t.Errorf("expected one go test command, got %+v", cfg.Commands)
		}
	})

	t.Run("effective repo and local settings are merged", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "settings"), 0755); err != nil {
			t.Fatal(err)
		}
		local := gtconfig.NewRigSettings()
		local.MergeQueue = &gtconfig.MergeQueueConfig{
			SetupCommand: "make setup",
			TestCommand:  "go test ./...",
		}
		if err := gtconfig.SaveRigSettings(gtconfig.RigSettingsPath(dir), local); err != nil {
			t.Fatal(err)
		}
		repoSettingsPath := filepath.Join(dir, "refinery", "rig", ".gastown", "settings.json")
		if err := os.MkdirAll(filepath.Dir(repoSettingsPath), 0755); err != nil {
			t.Fatal(err)
		}
		repo := gtconfig.NewRigSettings()
		repo.MergeQueue = &gtconfig.MergeQueueConfig{
			BuildCommand: "go build ./...",
			LintCommand:  "golangci-lint run",
		}
		if err := gtconfig.SaveRigSettings(repoSettingsPath, repo); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadRigGateConfig(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg == nil {
			t.Fatal("expected non-nil config")
		}
		if cfg.SetupCommand != "make setup" {
			t.Errorf("expected setup command, got %q", cfg.SetupCommand)
		}
		if len(cfg.Commands) != 3 {
			t.Fatalf("expected build, lint, and test commands, got %+v", cfg.Commands)
		}
		if cfg.Commands[0].Command != "go build ./..." || cfg.Commands[2].Command != "go test ./..." {
			t.Errorf("unexpected effective commands: %+v", cfg.Commands)
		}
	})

	t.Run("no test commands", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "settings"), 0755); err != nil {
			t.Fatal(err)
		}
		settings := gtconfig.NewRigSettings()
		settings.MergeQueue = &gtconfig.MergeQueueConfig{Enabled: true}
		if err := gtconfig.SaveRigSettings(gtconfig.RigSettingsPath(dir), settings); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadRigGateConfig(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg != nil {
			t.Errorf("expected nil for no test commands, got %+v", cfg)
		}
	})
}

func TestContains(t *testing.T) {
	if !sliceContains([]string{"a", "b", "c"}, "b") {
		t.Error("expected true for 'b' in [a b c]")
	}
	if sliceContains([]string{"a", "b", "c"}, "d") {
		t.Error("expected false for 'd' in [a b c]")
	}
	if sliceContains(nil, "a") {
		t.Error("expected false for nil slice")
	}
}

func TestRunCommandOnWorktreeRetriesFlakyFailure(t *testing.T) {
	workDir := t.TempDir()
	d := &Daemon{logger: log.New(io.Discard, "", 0)}
	failure := d.runCommandOnWorktree(context.Background(), "canary", workDir, validationCommand{
		Kind:    "test",
		Label:   "test",
		Command: "if [ ! -f retry-marker ]; then touch retry-marker; exit 1; fi",
	}, 1)
	if failure != nil {
		t.Fatalf("expected retry to pass, got %v", failure)
	}
}

func TestRunCommandOnWorktreeClassifiesMissingCommandAsInfrastructure(t *testing.T) {
	d := &Daemon{logger: log.New(io.Discard, "", 0)}
	failure := d.runCommandOnWorktree(context.Background(), "canary", t.TempDir(), validationCommand{
		Kind:    "test",
		Label:   "test",
		Command: "exec definitely-not-a-command",
	}, 0)
	if failure == nil || !failure.Infrastructure || failure.ExitCode != 127 {
		t.Fatalf("expected exit 127 infrastructure failure, got %+v", failure)
	}
}

func TestDefaultLifecycleConfigIncludesMainBranchTest(t *testing.T) {
	config := DefaultLifecycleConfig()
	if config.Patrols.MainBranchTest == nil {
		t.Fatal("expected MainBranchTest in default lifecycle config")
	}
	if !config.Patrols.MainBranchTest.Enabled {
		t.Error("expected MainBranchTest.Enabled=true")
	}
	if config.Patrols.MainBranchTest.IntervalStr != "30m" {
		t.Errorf("expected interval '30m', got %q", config.Patrols.MainBranchTest.IntervalStr)
	}
	if config.Patrols.MainBranchTest.TimeoutStr != "10m" {
		t.Errorf("expected timeout '10m', got %q", config.Patrols.MainBranchTest.TimeoutStr)
	}
}

func TestEnsureLifecycleDefaultsFillsMainBranchTest(t *testing.T) {
	config := &DaemonPatrolConfig{
		Type:    "daemon-patrol-config",
		Version: 1,
		Patrols: &PatrolsConfig{}, // All nil
	}
	changed := EnsureLifecycleDefaults(config)
	if !changed {
		t.Error("expected changed=true when MainBranchTest was nil")
	}
	if config.Patrols.MainBranchTest == nil {
		t.Fatal("expected MainBranchTest to be populated")
	}
	if !config.Patrols.MainBranchTest.Enabled {
		t.Error("expected MainBranchTest.Enabled=true after defaults")
	}
}
