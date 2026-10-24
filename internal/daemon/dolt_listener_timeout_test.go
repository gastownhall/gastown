package daemon

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestWriteDaemonDoltConfigListenerTimeouts(t *testing.T) {
	type testCase struct {
		name        string
		process     []string
		durable     string
		read, write int
		warnings    bool
	}
	tests := []testCase{
		{name: "defaults", read: 30000, write: 30000},
		{name: "durable", durable: "28800000", read: 28800000, write: 28800000},
		{name: "process precedence", process: []string{"123456", "654321"}, durable: "28800000", read: 123456, write: 654321},
		{name: "empty process fallback", process: []string{"", "  "}, durable: "28800000", read: 28800000, write: 28800000},
		{name: "empty values quiet", process: []string{"", " "}, durable: " ", read: 30000, write: 30000},
		{name: "zero process", process: []string{"0", "0"}, durable: "28800000"},
		{name: "zero durable", durable: "0"},
		{name: "omit read only", process: []string{"0", "900000"}, write: 900000},
		{name: "omit write only", process: []string{"900000", "0"}, read: 900000},
	}
	for _, value := range []string{"-1", "invalid", "999999999999999999999999", "9223372036855"} {
		tests = append(tests,
			testCase{name: "invalid process " + value, process: []string{value, value}, durable: "28800000", read: 30000, write: 30000, warnings: true},
			testCase{name: "invalid durable " + value, durable: value, read: 30000, write: 30000, warnings: true},
		)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			townRoot := t.TempDir()
			keys := []string{"GT_DOLT_READ_TIMEOUT_MS", "GT_DOLT_WRITE_TIMEOUT_MS"}
			for i, key := range keys {
				unsetEnv(t, key)
				if tt.process != nil {
					t.Setenv(key, tt.process[i])
				}
			}
			if err := os.MkdirAll(filepath.Join(townRoot, "daemon"), 0755); err != nil {
				t.Fatal(err)
			}
			durablePath := filepath.Join(townRoot, "daemon", "daemon.env")
			if err := os.WriteFile(durablePath, []byte(keys[0]+"="+tt.durable+"\n"+keys[1]+"="+tt.durable+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg := &DoltServerConfig{Host: "127.0.0.2", Port: 5507, DataDir: filepath.Join(townRoot, "custom-data")}
			configPath := filepath.Join(townRoot, "config.yaml")
			for attempt := 0; attempt < 2; attempt++ {
				diagnostic := captureListenerTimeoutStderr(t, func() {
					if err := writeDaemonDoltConfig(townRoot, cfg, configPath); err != nil {
						t.Fatal(err)
					}
				})
				for _, key := range keys {
					if tt.warnings && !strings.Contains(diagnostic, key) {
						t.Errorf("diagnostic %q does not mention %s", diagnostic, key)
					}
				}
				if !tt.warnings && diagnostic != "" {
					t.Errorf("unexpected diagnostic: %s", diagnostic)
				}
				data, err := os.ReadFile(configPath)
				if err != nil {
					t.Fatal(err)
				}
				var parsed struct {
					Listener struct {
						Host           string `yaml:"host"`
						Port           int    `yaml:"port"`
						MaxConnections int    `yaml:"max_connections"`
						Read           *int   `yaml:"read_timeout_millis"`
						Write          *int   `yaml:"write_timeout_millis"`
					} `yaml:"listener"`
					DataDir string `yaml:"data_dir"`
				}
				if err := yaml.Unmarshal(data, &parsed); err != nil {
					t.Fatal(err)
				}
				if parsed.Listener.Host != cfg.Host || parsed.Listener.Port != cfg.Port || parsed.DataDir != cfg.DataDir || parsed.Listener.MaxConnections != 1000 {
					t.Fatalf("daemon settings changed: %+v", parsed)
				}
				for i, got := range []*int{parsed.Listener.Read, parsed.Listener.Write} {
					want := []int{tt.read, tt.write}[i]
					if want == 0 {
						if got != nil {
							t.Errorf("%s should be omitted, got %d", keys[i], *got)
						}
					} else if got == nil || *got != want {
						t.Errorf("%s = %v, want %d", keys[i], got, want)
					}
				}
			}
		})
	}
}

func captureListenerTimeoutStderr(t *testing.T, fn func()) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	old := os.Stderr
	os.Stderr = f
	defer func() { os.Stderr = old }()
	fn()
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
