package opencodeserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestServerProcessLifecycle(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server, err := StartServer(ctx, ServerOptions{
		Command:        os.Args[0],
		CommandArgs:    []string{"-test.run=TestOpenCodeServerHelperProcess"},
		Directory:      t.TempDir(),
		StartupTimeout: 5 * time.Second,
		Environment: map[string]string{
			"GO_WANT_OPENCODE_SERVER_HELPER": "1",
			"GO_OPENCODE_HELPER_VERSION":     "1.18.16",
		},
	})
	if err != nil {
		t.Fatalf("StartServer: %v", err)
	}
	if server.Version() != "1.18.16" {
		t.Fatalf("Version = %q, want 1.18.16", server.Version())
	}
	if _, err := server.Client(t.TempDir()).Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if err := server.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := server.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

func TestServerRejectsUnsupportedVersion(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := StartServer(ctx, ServerOptions{
		Command:        os.Args[0],
		CommandArgs:    []string{"-test.run=TestOpenCodeServerHelperProcess"},
		Directory:      t.TempDir(),
		StartupTimeout: 5 * time.Second,
		Environment: map[string]string{
			"GO_WANT_OPENCODE_SERVER_HELPER": "1",
			"GO_OPENCODE_HELPER_VERSION":     "2.0.0",
		},
	})
	if err == nil {
		t.Fatal("StartServer accepted unsupported major version")
	}
}

func TestSupportedVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		version string
		want    bool
	}{
		{"1.18.15", false},
		{"1.18.16", true},
		{"v1.18.16", true},
		{"1.18.16-beta.1", false},
		{"1.18.16+build", true},
		{"1.18.17-beta.1", true},
		{"1.19.0", true},
		{"2.0.0", false},
		{"dev", false},
		{"1.18.-", false},
		{"1.18.+build", false},
		{"1.18.16-", false},
		{"1.18.16+", false},
	}
	for _, tt := range tests {
		if got := SupportedVersion(tt.version); got != tt.want {
			t.Errorf("SupportedVersion(%q) = %v, want %v", tt.version, got, tt.want)
		}
	}
}

func TestServerParentCancellationAllowsGracefulStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	server, err := StartServer(ctx, ServerOptions{
		Command:        os.Args[0],
		CommandArgs:    []string{"-test.run=TestOpenCodeServerHelperProcess"},
		Directory:      t.TempDir(),
		StartupTimeout: 5 * time.Second,
		Environment: map[string]string{
			"GO_WANT_OPENCODE_SERVER_HELPER": "1",
			"GO_OPENCODE_HELPER_VERSION":     "1.18.16",
		},
	})
	if err != nil {
		t.Fatalf("StartServer: %v", err)
	}
	cancel()

	select {
	case <-server.Done():
		t.Fatal("parent cancellation killed the server before the worker could abort its active turn")
	case <-time.After(100 * time.Millisecond):
	}
	if err := server.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestServerStopsWhenParentExits(t *testing.T) {
	if os.Getenv("GO_WANT_OPENCODE_PARENT_HELPER") == "1" {
		descendantPIDPath := os.Getenv("GO_OPENCODE_DESCENDANT_PID_PATH")
		server, err := StartServer(context.Background(), ServerOptions{
			Command:        os.Args[0],
			CommandArgs:    []string{"-test.run=^TestOpenCodeServerHelperProcess$"},
			Directory:      os.TempDir(),
			StartupTimeout: 5 * time.Second,
			Environment: map[string]string{
				"GO_WANT_OPENCODE_SERVER_HELPER":  "1",
				"GO_OPENCODE_HELPER_VERSION":      "1.18.16",
				"GO_OPENCODE_HELPER_CHILD":        "1",
				"GO_OPENCODE_DESCENDANT_PID_PATH": descendantPIDPath,
			},
		})
		if err != nil {
			os.Exit(2)
		}
		pid := server.PID()
		if data, readErr := os.ReadFile(descendantPIDPath); readErr == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		}
		state := State{URL: server.URL(), Username: server.Username(), Password: server.Password(), PID: pid, Directory: os.TempDir()}
		data, _ := json.Marshal(state)
		if err := os.WriteFile(os.Getenv("GO_OPENCODE_PARENT_STATE"), data, 0600); err != nil {
			os.Exit(3)
		}
		os.Exit(0)
	}

	statePath := filepath.Join(t.TempDir(), "server.json")
	descendantPIDPath := filepath.Join(t.TempDir(), "descendant.pid")
	parent := exec.Command(os.Args[0], "-test.run=^TestServerStopsWhenParentExits$")
	parent.Env = append(os.Environ(),
		"GO_WANT_OPENCODE_PARENT_HELPER=1",
		"GO_OPENCODE_PARENT_STATE="+statePath,
		"GO_OPENCODE_DESCENDANT_PID_PATH="+descendantPIDPath,
	)
	if output, err := parent.CombinedOutput(); err != nil {
		t.Fatalf("parent helper: %v\n%s", err, output)
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = terminateProcessTree(state.PID) })
	client, err := NewClient(state.URL, state.Username, state.Password, state.Directory, &http.Client{Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		healthCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, healthErr := client.Health(healthCtx)
		cancel()
		if healthErr != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("OpenCode server survived its worker parent")
}

func TestServerProcessUsesRandomCredentials(t *testing.T) {
	t.Parallel()
	start := func() *ServerProcess {
		server, err := StartServer(context.Background(), ServerOptions{
			Command:        os.Args[0],
			CommandArgs:    []string{"-test.run=TestOpenCodeServerHelperProcess"},
			Directory:      t.TempDir(),
			StartupTimeout: 5 * time.Second,
			Environment: map[string]string{
				"GO_WANT_OPENCODE_SERVER_HELPER": "1",
				"GO_OPENCODE_HELPER_VERSION":     "1.18.16",
			},
		})
		if err != nil {
			t.Fatalf("StartServer: %v", err)
		}
		return server
	}
	first := start()
	defer first.Stop()
	second := start()
	defer second.Stop()
	if first.Password() == "" || first.Password() == second.Password() {
		t.Fatal("server credentials were empty or reused")
	}
}

func TestMergeEnvironmentOverridesKeys(t *testing.T) {
	base := []string{"PATH=old", "KEEP=value"}
	overlayKey := "PATH"
	if runtime.GOOS == "windows" {
		overlayKey = "Path"
	}
	merged := mergeEnvironment(base, map[string]string{overlayKey: "new"})
	var pathValues int
	for _, entry := range merged {
		key, value, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(key, "PATH") {
			pathValues++
			if value != "new" {
				t.Fatalf("PATH = %q, want new", value)
			}
		}
	}
	if pathValues != 1 {
		t.Fatalf("PATH entries = %d, want 1: %v", pathValues, merged)
	}
}

func TestOpenCodeServerHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_OPENCODE_SERVER_HELPER") != "1" {
		return
	}
	if os.Getenv("GO_OPENCODE_HELPER_CHILD") == "1" {
		child := exec.Command(os.Args[0], os.Args[1:]...)
		child.Env = append(os.Environ(), "GO_OPENCODE_HELPER_CHILD=0")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(5)
		}
		_ = child.Wait()
		os.Exit(0)
	}
	if path := os.Getenv("GO_OPENCODE_DESCENDANT_PID_PATH"); path != "" {
		if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			os.Exit(6)
		}
	}
	port := 0
	hasServe := false
	hostname := ""
	for i, arg := range os.Args {
		if arg == "serve" {
			hasServe = true
		}
		if arg == "--port" && i+1 < len(os.Args) {
			port, _ = strconv.Atoi(os.Args[i+1])
		}
		if arg == "--hostname" && i+1 < len(os.Args) {
			hostname = os.Args[i+1]
		}
	}
	if !hasServe || port == 0 || hostname != "127.0.0.1" {
		fmt.Fprintln(os.Stderr, "missing secure serve arguments")
		os.Exit(2)
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	version := os.Getenv("GO_OPENCODE_HELPER_VERSION")
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ := r.BasicAuth()
		if user != os.Getenv("OPENCODE_SERVER_USERNAME") || pass != os.Getenv("OPENCODE_SERVER_PASSWORD") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/global/health" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(Health{Healthy: true, Version: version})
	})}
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		os.Exit(4)
	}
	os.Exit(0)
}
