package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestNewDoltServerManagerNormalizesManagedEndpointFromTownConfig(t *testing.T) {
	townRoot := t.TempDir()
	writeManagedDoltConfig(t, townRoot, "listener:\n  host: 127.0.0.2\n  port: 5507\n")
	t.Setenv("GT_DOLT_IGNORE_CONFIG", "")
	t.Setenv("GT_DOLT_HOST", "stale-env-host")
	t.Setenv("GT_DOLT_PORT", "9999")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "stale-beads-host")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "9999")
	t.Setenv("BEADS_DOLT_PORT", "9999")

	cfg := &DoltServerConfig{
		Enabled:              true,
		External:             true,
		Host:                 "stale-daemon-host",
		Port:                 9999,
		User:                 "root",
		Password:             "secret",
		DataDir:              filepath.Join(townRoot, "custom-data"),
		LogFile:              filepath.Join(townRoot, "custom.log"),
		AutoRestart:          true,
		RestartDelay:         2 * time.Second,
		MaxRestartDelay:      3 * time.Second,
		MaxRestartsInWindow:  4,
		RestartWindow:        5 * time.Second,
		HealthyResetInterval: 6 * time.Second,
		HealthCheckInterval:  7 * time.Second,
	}

	m := NewDoltServerManager(townRoot, cfg, func(string, ...interface{}) {})
	if got := m.config.Host; got != "127.0.0.2" {
		t.Fatalf("manager host = %q, want managed host", got)
	}
	if got := m.config.Port; got != 5507 {
		t.Fatalf("manager port = %d, want managed port", got)
	}
	if cfg.Host != "stale-daemon-host" || cfg.Port != 9999 {
		t.Fatalf("input config was mutated: host=%q port=%d", cfg.Host, cfg.Port)
	}
	if !m.config.Enabled || !m.config.External || m.config.User != "root" || m.config.Password != "secret" {
		t.Fatalf("non-endpoint fields were not preserved: %#v", m.config)
	}
	if m.config.DataDir != cfg.DataDir || m.config.LogFile != cfg.LogFile {
		t.Fatalf("paths were not preserved: %#v", m.config)
	}
	if m.config.RestartDelay != cfg.RestartDelay || m.config.MaxRestartDelay != cfg.MaxRestartDelay || m.config.MaxRestartsInWindow != cfg.MaxRestartsInWindow || m.config.RestartWindow != cfg.RestartWindow || m.config.HealthyResetInterval != cfg.HealthyResetInterval || m.config.HealthCheckInterval != cfg.HealthCheckInterval {
		t.Fatalf("restart/health settings were not preserved: %#v", m.config)
	}
}

func TestNewDoltServerManagerPortOnlyManagedConfigClearsStaleHost(t *testing.T) {
	townRoot := t.TempDir()
	writeManagedDoltConfig(t, townRoot, "listener:\n  port: 5507\n")
	t.Setenv("GT_DOLT_IGNORE_CONFIG", "")

	m := NewDoltServerManager(townRoot, &DoltServerConfig{Enabled: true, Host: "stale-daemon-host", Port: 9999}, func(string, ...interface{}) {})
	if got := m.config.Host; got != "" {
		t.Fatalf("manager host = %q, want cleared", got)
	}
	if got := m.config.Port; got != 5507 {
		t.Fatalf("manager port = %d, want managed port", got)
	}
}

func TestNewDoltServerManagerHonorsIgnoreConfig(t *testing.T) {
	townRoot := t.TempDir()
	writeManagedDoltConfig(t, townRoot, "listener:\n  host: 127.0.0.2\n  port: 5507\n")
	t.Setenv("GT_DOLT_IGNORE_CONFIG", "1")

	m := NewDoltServerManager(townRoot, &DoltServerConfig{Enabled: true, Host: "daemon-host", Port: 9999}, func(string, ...interface{}) {})
	if got := m.config.Host; got != "daemon-host" {
		t.Fatalf("manager host = %q, want daemon config host", got)
	}
	if got := m.config.Port; got != 9999 {
		t.Fatalf("manager port = %d, want daemon config port", got)
	}
}

func TestNewDoltServerManager_ExternalLoopbackUsesDaemonEndpoint(t *testing.T) {
	// Given
	townRoot := t.TempDir()
	t.Setenv("GT_DOLT_EXTERNAL", "")
	t.Setenv("GT_DOLT_HOST", "")
	t.Setenv("GT_DOLT_PORT", "")
	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "daemon.json"), []byte(`{"env":{"GT_DOLT_EXTERNAL":"true","GT_DOLT_HOST":"127.0.0.1","GT_DOLT_PORT":"3308"}}`), 0644); err != nil {
		t.Fatal(err)
	}

	// When
	manager := NewDoltServerManager(townRoot, &DoltServerConfig{Enabled: true}, func(string, ...interface{}) {})

	// Then
	if !manager.IsExternal() {
		t.Fatal("external loopback endpoint must not be daemon managed")
	}
	if manager.config.Host != "127.0.0.1" || manager.config.Port != 3308 {
		t.Fatalf("endpoint = %s:%d, want 127.0.0.1:3308", manager.config.Host, manager.config.Port)
	}
}

func TestDoltServerManager_StartRejectsExternalEndpoint(t *testing.T) {
	// Given
	manager := &DoltServerManager{
		config: &DoltServerConfig{Enabled: true, External: true},
		logger: func(string, ...interface{}) {},
	}

	// When
	err := manager.Start()

	// Then
	if err == nil {
		t.Fatal("external server start must be rejected")
	}
}

func TestApplyDoltServerConfigEnvUsesNormalizedManagerConfig(t *testing.T) {
	townRoot := t.TempDir()
	writeManagedDoltConfig(t, townRoot, "listener:\n  host: 127.0.0.2\n  port: 5507\n")
	t.Setenv("GT_DOLT_IGNORE_CONFIG", "")
	t.Setenv("GT_DOLT_HOST", "stale-env-host")
	t.Setenv("GT_DOLT_PORT", "9999")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "stale-beads-host")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "9999")
	t.Setenv("BEADS_DOLT_PORT", "9999")

	m := NewDoltServerManager(townRoot, &DoltServerConfig{Enabled: true, Host: "stale-daemon-host", Port: 9999}, func(string, ...interface{}) {})
	applyDoltServerConfigEnv(m.config)

	assertProcessEnv(t, "GT_DOLT_HOST", "127.0.0.2")
	assertProcessEnv(t, "BEADS_DOLT_SERVER_HOST", "127.0.0.2")
	assertProcessEnv(t, "GT_DOLT_PORT", "5507")
	assertProcessEnv(t, "BEADS_DOLT_SERVER_PORT", "5507")
	assertProcessEnv(t, "BEADS_DOLT_PORT", "5507")
}

func TestApplyConfiguredDoltHostEnvClearsManagedConfigWithoutHost(t *testing.T) {
	townRoot := t.TempDir()
	writeManagedDoltConfig(t, townRoot, "listener:\n  port: 5507\n")
	t.Setenv("GT_DOLT_IGNORE_CONFIG", "")
	t.Setenv("GT_DOLT_HOST", "stale-env-host")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "stale-beads-host")

	applyConfiguredDoltHostEnv(townRoot, nil)

	if got := os.Getenv("GT_DOLT_HOST"); got != "" {
		t.Fatalf("GT_DOLT_HOST = %q, want cleared", got)
	}
	if got := os.Getenv("BEADS_DOLT_SERVER_HOST"); got != "" {
		t.Fatalf("BEADS_DOLT_SERVER_HOST = %q, want cleared", got)
	}
}

func TestWriteDaemonDoltConfigAutoGC(t *testing.T) {
	t.Run("default enabled", func(t *testing.T) {
		unsetEnv(t, "GT_DOLT_AUTO_GC")

		got := writeAndReadDaemonAutoGC(t)
		if !got.Enable {
			t.Fatalf("auto_gc_behavior.enable = false, want true")
		}
		if got.ArchiveLevel != 1 {
			t.Fatalf("auto_gc_behavior.archive_level = %d, want 1", got.ArchiveLevel)
		}
	})

	t.Run("kill switch disabled", func(t *testing.T) {
		t.Setenv("GT_DOLT_AUTO_GC", "disabled")

		got := writeAndReadDaemonAutoGC(t)
		if got.Enable {
			t.Fatalf("GT_DOLT_AUTO_GC=disabled: auto_gc_behavior.enable = true, want false")
		}
		if got.ArchiveLevel != 0 {
			t.Fatalf("GT_DOLT_AUTO_GC=disabled: auto_gc_behavior.archive_level = %d, want 0", got.ArchiveLevel)
		}
	})
}

func writeManagedDoltConfig(t *testing.T, townRoot, content string) {
	t.Helper()
	doltDataDir := filepath.Join(townRoot, ".dolt-data")
	if err := os.MkdirAll(doltDataDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(doltDataDir, "config.yaml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func writeAndReadDaemonAutoGC(t *testing.T) struct {
	Enable       bool `yaml:"enable"`
	ArchiveLevel int  `yaml:"archive_level"`
} {
	t.Helper()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	cfg := &DoltServerConfig{Port: 3307, DataDir: dir}
	if err := writeDaemonDoltConfig(cfg, configPath); err != nil {
		t.Fatalf("writeDaemonDoltConfig: %v", err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading daemon Dolt config: %v", err)
	}

	var parsed struct {
		Behavior struct {
			AutoGCBehavior struct {
				Enable       bool `yaml:"enable"`
				ArchiveLevel int  `yaml:"archive_level"`
			} `yaml:"auto_gc_behavior"`
		} `yaml:"behavior"`
	}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("generated daemon config is invalid YAML: %v\n%s", err, data)
	}
	return parsed.Behavior.AutoGCBehavior
}

func unsetEnv(t *testing.T, key string) {
	t.Helper()

	old, hadOld := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if hadOld {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func assertProcessEnv(t *testing.T, key, want string) {
	t.Helper()
	if got := os.Getenv(key); got != want {
		t.Fatalf("%s = %q, want %q", key, got, want)
	}
}
