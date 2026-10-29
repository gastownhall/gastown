package doltserver

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestListenerTimeoutNativeDolt exercises the generated configuration with the
// real consumer. Opt in with GT_TEST_NATIVE_DOLT=1; every server uses a fresh
// data directory, isolated Dolt configuration, and an ephemeral loopback port.
func TestListenerTimeoutNativeDolt(t *testing.T) {
	if os.Getenv("GT_TEST_NATIVE_DOLT") != "1" {
		t.Skip("set GT_TEST_NATIVE_DOLT=1 to test the installed Dolt binary")
	}
	dolt, err := exec.LookPath("dolt")
	if err != nil {
		t.Fatal(err)
	}
	version, err := exec.Command(dolt, "version").CombinedOutput()
	if err != nil {
		t.Fatalf("dolt version: %v: %s", err, version)
	}
	t.Log(strings.TrimSpace(string(version)))
	for _, tc := range []struct {
		name, process string
		cancel        bool
		restarts      int
	}{
		{name: "process_overrides_durable", process: "1000", cancel: true, restarts: 1},
		{name: "durable_without_shell_env", restarts: 2},
		{name: "zero_uses_dolt_defaults", process: "0", restarts: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			townRoot := t.TempDir()
			writeDaemonEnv(t, townRoot, "GT_DOLT_READ_TIMEOUT_MS=28800000\nGT_DOLT_WRITE_TIMEOUT_MS=28800000\n")
			for _, key := range []string{"GT_DOLT_READ_TIMEOUT_MS", "GT_DOLT_WRITE_TIMEOUT_MS"} {
				unsetEnv(t, key)
				if tc.process != "" {
					t.Setenv(key, tc.process)
				}
			}
			for attempt := 1; attempt <= tc.restarts; attempt++ {
				t.Run(fmt.Sprintf("start_%d", attempt), func(t *testing.T) {
					config := DefaultConfig(townRoot)
					config.Host = "127.0.0.1"
					listener, err := net.Listen("tcp", "127.0.0.1:0")
					if err != nil {
						t.Fatal(err)
					}
					config.Port = listener.Addr().(*net.TCPAddr).Port
					if err := listener.Close(); err != nil {
						t.Fatal(err)
					}
					if err := os.MkdirAll(config.DataDir, 0700); err != nil {
						t.Fatal(err)
					}
					configPath := filepath.Join(townRoot, "config.yaml")
					if err := writeServerConfig(config, configPath); err != nil {
						t.Fatal(err)
					}
					yaml, err := os.ReadFile(configPath)
					if err != nil {
						t.Fatal(err)
					}
					t.Logf("Generated configuration consumed by dolt sql-server --config:\n%s", yaml)
					// Do not inherit live endpoint, credential, or Dolt configuration
					// overrides. DOLT_ROOT_PATH keeps global Dolt state isolated.
					var childEnv []string
					for _, entry := range os.Environ() {
						if !strings.HasPrefix(entry, "DOLT_") && !strings.HasPrefix(entry, "GT_") && !strings.HasPrefix(entry, "BEADS_") {
							childEnv = append(childEnv, entry)
						}
					}
					childEnv = append(childEnv, "DOLT_ROOT_PATH="+townRoot)
					logFile, err := os.Create(filepath.Join(t.TempDir(), "server.log"))
					if err != nil {
						t.Fatal(err)
					}
					defer logFile.Close()
					server := exec.Command(dolt, "sql-server", "--config", configPath)
					server.Dir, server.Env = townRoot, childEnv
					server.Stdout, server.Stderr = logFile, logFile
					if err := server.Start(); err != nil {
						t.Fatal(err)
					}
					done := make(chan error, 1)
					go func() { done <- server.Wait() }()
					defer func() {
						_ = server.Process.Signal(os.Interrupt)
						select {
						case <-done:
						case <-time.After(10 * time.Second):
							_ = server.Process.Kill()
							<-done
						}
						if t.Failed() {
							logs, _ := os.ReadFile(logFile.Name())
							t.Logf("isolated server log:\n%s", logs)
						}
					}()
					query := func(sql string) ([]byte, error) {
						ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
						defer cancel()
						cmd := exec.CommandContext(ctx, dolt, "--host", config.Host, "--port", fmt.Sprint(config.Port), "--user", "root", "--password", "", "--no-tls", "sql", "-q", sql)
						cmd.Dir, cmd.Env = townRoot, childEnv
						return cmd.CombinedOutput()
					}
					deadline := time.Now().Add(20 * time.Second)
					for {
						output, err := query("SELECT 1")
						if err == nil {
							break
						}
						if time.Now().After(deadline) {
							t.Fatalf("isolated server did not become ready: %v: %s", err, output)
						}
						time.Sleep(100 * time.Millisecond)
					}
					start := time.Now()
					output, err := query("SELECT SLEEP(3)")
					elapsed := time.Since(start)
					t.Logf("dolt --host %s --port %d --user root --password '' --no-tls sql -q 'SELECT SLEEP(3)'\nElapsed: %s; error: %v\n%s", config.Host, config.Port, elapsed, err, output)
					if tc.cancel {
						if err == nil || !strings.Contains(strings.ToLower(string(output)), "connection timeout") || elapsed >= 3*time.Second {
							t.Fatalf("1000ms listener should cancel the 3s query before completion")
						}
					} else if err != nil || elapsed < 3*time.Second {
						t.Fatalf("3s query should complete with durable or Dolt-default listener limits")
					}
				})
			}
		})
	}
}
