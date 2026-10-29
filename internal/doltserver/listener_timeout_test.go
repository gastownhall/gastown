package doltserver

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDefaultConfig_ListenerTimeoutEnvOverridesDaemonEnv(t *testing.T) {
	townRoot := t.TempDir()
	writeDaemonEnv(t, townRoot, "GT_DOLT_READ_TIMEOUT_MS=111\nGT_DOLT_WRITE_TIMEOUT_MS=222\n")
	t.Setenv("GT_DOLT_READ_TIMEOUT_MS", "123456")
	t.Setenv("GT_DOLT_WRITE_TIMEOUT_MS", "654321")

	config := DefaultConfig(townRoot)
	if config.ReadTimeoutMs != 123456 {
		t.Fatalf("ReadTimeoutMs = %d, want 123456", config.ReadTimeoutMs)
	}
	if config.WriteTimeoutMs != 654321 {
		t.Fatalf("WriteTimeoutMs = %d, want 654321", config.WriteTimeoutMs)
	}
}

func TestDefaultConfig_ListenerTimeoutFallsBackToDaemonEnv(t *testing.T) {
	townRoot := t.TempDir()
	writeDaemonEnv(t, townRoot, "# durable Dolt settings\nGT_DOLT_READ_TIMEOUT_MS=777777\nGT_DOLT_WRITE_TIMEOUT_MS=888888\n")
	unsetEnv(t, "GT_DOLT_READ_TIMEOUT_MS")
	unsetEnv(t, "GT_DOLT_WRITE_TIMEOUT_MS")

	config := DefaultConfig(townRoot)
	if config.ReadTimeoutMs != 777777 {
		t.Fatalf("ReadTimeoutMs = %d, want 777777", config.ReadTimeoutMs)
	}
	if config.WriteTimeoutMs != 888888 {
		t.Fatalf("WriteTimeoutMs = %d, want 888888", config.WriteTimeoutMs)
	}
}

func TestDefaultConfig_ListenerTimeoutDaemonEnvGeneratesDurableYAML(t *testing.T) {
	townRoot := t.TempDir()
	writeDaemonEnv(t, townRoot, "GT_DOLT_READ_TIMEOUT_MS=900000\nGT_DOLT_WRITE_TIMEOUT_MS=1200000\n")
	unsetEnv(t, "GT_DOLT_READ_TIMEOUT_MS")
	unsetEnv(t, "GT_DOLT_WRITE_TIMEOUT_MS")

	configPath := filepath.Join(townRoot, "config.yaml")
	if err := writeServerConfig(DefaultConfig(townRoot), configPath); err != nil {
		t.Fatalf("writeServerConfig: %v", err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}
	var parsed struct {
		Listener struct {
			ReadTimeoutMillis  int `yaml:"read_timeout_millis"`
			WriteTimeoutMillis int `yaml:"write_timeout_millis"`
		} `yaml:"listener"`
	}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("generated config is invalid YAML: %v", err)
	}
	if parsed.Listener.ReadTimeoutMillis != 900000 {
		t.Errorf("listener.read_timeout_millis = %d, want 900000", parsed.Listener.ReadTimeoutMillis)
	}
	if parsed.Listener.WriteTimeoutMillis != 1200000 {
		t.Errorf("listener.write_timeout_millis = %d, want 1200000", parsed.Listener.WriteTimeoutMillis)
	}
}

func TestDefaultConfig_ListenerTimeoutEmptyEnvUsesDaemonEnv(t *testing.T) {
	townRoot := t.TempDir()
	writeDaemonEnv(t, townRoot, "GT_DOLT_READ_TIMEOUT_MS=333333\nGT_DOLT_WRITE_TIMEOUT_MS=444444\n")
	t.Setenv("GT_DOLT_READ_TIMEOUT_MS", "")
	t.Setenv("GT_DOLT_WRITE_TIMEOUT_MS", "")

	config := DefaultConfig(townRoot)
	if config.ReadTimeoutMs != 333333 || config.WriteTimeoutMs != 444444 {
		t.Fatalf("empty process env should use daemon env, got read=%d write=%d", config.ReadTimeoutMs, config.WriteTimeoutMs)
	}
}

func TestDefaultConfig_ListenerTimeoutInvalidValuesUseDefaults(t *testing.T) {
	tests := []struct {
		name  string
		env   string
		value string
	}{
		{name: "not a number", env: "GT_DOLT_READ_TIMEOUT_MS", value: "five minutes"},
		{name: "negative", env: "GT_DOLT_WRITE_TIMEOUT_MS", value: "-1"},
		{name: "overflow", env: "GT_DOLT_READ_TIMEOUT_MS", value: "999999999999999999999999999999"},
		{name: "duration overflow", env: "GT_DOLT_WRITE_TIMEOUT_MS", value: "9223372036855"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			townRoot := t.TempDir()
			unsetEnv(t, "GT_DOLT_READ_TIMEOUT_MS")
			unsetEnv(t, "GT_DOLT_WRITE_TIMEOUT_MS")
			t.Setenv(tt.env, tt.value)

			var config *Config
			diagnostic := captureStderr(t, func() {
				config = DefaultConfig(townRoot)
			})
			if config.ReadTimeoutMs != DefaultReadTimeoutMs {
				t.Errorf("ReadTimeoutMs = %d, want default %d", config.ReadTimeoutMs, DefaultReadTimeoutMs)
			}
			if config.WriteTimeoutMs != DefaultWriteTimeoutMs {
				t.Errorf("WriteTimeoutMs = %d, want default %d", config.WriteTimeoutMs, DefaultWriteTimeoutMs)
			}
			if !strings.Contains(diagnostic, tt.env) {
				t.Errorf("diagnostic %q does not mention %s", diagnostic, tt.env)
			}
		})
	}
}

func TestDefaultConfig_ListenerTimeoutBlankEnvWithoutTownRootIsQuiet(t *testing.T) {
	t.Setenv("GT_DOLT_READ_TIMEOUT_MS", "")
	t.Setenv("GT_DOLT_WRITE_TIMEOUT_MS", "")

	var config *Config
	diagnostic := captureStderr(t, func() {
		config = DefaultConfig("")
	})
	if config.ReadTimeoutMs != DefaultReadTimeoutMs || config.WriteTimeoutMs != DefaultWriteTimeoutMs {
		t.Fatalf("blank env without durable fallback should use defaults, got read=%d write=%d", config.ReadTimeoutMs, config.WriteTimeoutMs)
	}
	if diagnostic != "" {
		t.Fatalf("blank env without durable fallback emitted diagnostic %q", diagnostic)
	}
}

func TestDefaultConfig_ListenerTimeoutInvalidDaemonEnvUsesDefaults(t *testing.T) {
	townRoot := t.TempDir()
	writeDaemonEnv(t, townRoot, "GT_DOLT_READ_TIMEOUT_MS=negative\nGT_DOLT_WRITE_TIMEOUT_MS=-1\n")
	unsetEnv(t, "GT_DOLT_READ_TIMEOUT_MS")
	unsetEnv(t, "GT_DOLT_WRITE_TIMEOUT_MS")

	var config *Config
	diagnostic := captureStderr(t, func() {
		config = DefaultConfig(townRoot)
	})
	if config.ReadTimeoutMs != DefaultReadTimeoutMs || config.WriteTimeoutMs != DefaultWriteTimeoutMs {
		t.Fatalf("invalid daemon env should use defaults, got read=%d write=%d", config.ReadTimeoutMs, config.WriteTimeoutMs)
	}
	if !strings.Contains(diagnostic, "GT_DOLT_READ_TIMEOUT_MS") || !strings.Contains(diagnostic, "GT_DOLT_WRITE_TIMEOUT_MS") {
		t.Fatalf("diagnostic %q does not mention both invalid daemon env values", diagnostic)
	}
}

func TestDefaultConfig_ListenerTimeoutZeroOmittedFromGeneratedYAML(t *testing.T) {
	townRoot := t.TempDir()
	t.Setenv("GT_DOLT_READ_TIMEOUT_MS", "0")
	t.Setenv("GT_DOLT_WRITE_TIMEOUT_MS", "900000")

	config := DefaultConfig(townRoot)
	configPath := filepath.Join(townRoot, "config.yaml")
	if err := writeServerConfig(config, configPath); err != nil {
		t.Fatalf("writeServerConfig: %v", err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}
	var parsed struct {
		Listener struct {
			ReadTimeoutMillis  *int `yaml:"read_timeout_millis"`
			WriteTimeoutMillis *int `yaml:"write_timeout_millis"`
		} `yaml:"listener"`
	}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("generated config is invalid YAML: %v", err)
	}
	if parsed.Listener.ReadTimeoutMillis != nil {
		t.Fatalf("zero read timeout should be omitted, got %d", *parsed.Listener.ReadTimeoutMillis)
	}
	if parsed.Listener.WriteTimeoutMillis == nil || *parsed.Listener.WriteTimeoutMillis != 900000 {
		t.Fatalf("configured write timeout = %v, want 900000", parsed.Listener.WriteTimeoutMillis)
	}
}

func writeDaemonEnv(t *testing.T, townRoot, content string) {
	t.Helper()
	daemonDir := filepath.Join(townRoot, "daemon")
	if err := os.MkdirAll(daemonDir, 0755); err != nil {
		t.Fatalf("mkdir daemon: %v", err)
	}
	if err := os.WriteFile(filepath.Join(daemonDir, "daemon.env"), []byte(content), 0600); err != nil {
		t.Fatalf("write daemon.env: %v", err)
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = old }()
	fn()
	if err := w.Close(); err != nil {
		t.Fatalf("close stderr pipe: %v", err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stderr: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close captured stderr: %v", err)
	}
	return string(data)
}

func TestDefaultConfig_ListenerTimeoutGeneratedYAML(t *testing.T) {
	for _, tt := range []struct {
		name, read, write   string
		wantRead, wantWrite int
	}{
		{name: "defaults", wantRead: 300000, wantWrite: 300000},
		{name: "process precedence", read: "28800000", write: "900000", wantRead: 28800000, wantWrite: 900000},
		{name: "write zero", read: "900000", write: "0", wantRead: 900000},
		{name: "duration boundary", read: "9223372036854", write: "9223372036854", wantRead: 9223372036854, wantWrite: 9223372036854},
	} {
		t.Run(tt.name, func(t *testing.T) {
			townRoot := t.TempDir()
			t.Setenv("GT_DOLT_READ_TIMEOUT_MS", tt.read)
			t.Setenv("GT_DOLT_WRITE_TIMEOUT_MS", tt.write)
			if tt.name == "process precedence" {
				writeDaemonEnv(t, townRoot, "GT_DOLT_READ_TIMEOUT_MS=111\nGT_DOLT_WRITE_TIMEOUT_MS=222\n")
			}
			configPath := filepath.Join(townRoot, "config.yaml")
			if err := writeServerConfig(DefaultConfig(townRoot), configPath); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			var parsed struct {
				Listener struct {
					Read  *int `yaml:"read_timeout_millis"`
					Write *int `yaml:"write_timeout_millis"`
				} `yaml:"listener"`
			}
			if err := yaml.Unmarshal(data, &parsed); err != nil {
				t.Fatal(err)
			}
			if parsed.Listener.Read == nil || *parsed.Listener.Read != tt.wantRead {
				t.Errorf("read timeout = %v, want %d", parsed.Listener.Read, tt.wantRead)
			}
			if tt.wantWrite == 0 {
				if parsed.Listener.Write != nil {
					t.Errorf("write timeout should be omitted, got %d", *parsed.Listener.Write)
				}
			} else if parsed.Listener.Write == nil || *parsed.Listener.Write != tt.wantWrite {
				t.Errorf("write timeout = %v, want %d", parsed.Listener.Write, tt.wantWrite)
			}
		})
	}
}
