package cmd

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jonbaldie/gastown/internal/doltserver"
)

// withMockSocket replaces localDoltSocketPath for the duration of the
// test with a function that returns sockPath unconditionally. Useful for
// asserting the unix-socket DSN branch without requiring a real Dolt.
func withMockSocket(t *testing.T, sockPath string) {
	t.Helper()
	orig := localDoltSocketPath
	localDoltSocketPath = func(int) string { return sockPath }
	t.Cleanup(func() { localDoltSocketPath = orig })
}

// withNoSocket forces localDoltSocketPath to return "" so tests can
// assert the TCP fallback branch on machines that happen to have Dolt
// running locally.
func withNoSocket(t *testing.T) {
	t.Helper()
	orig := localDoltSocketPath
	localDoltSocketPath = func(int) string { return "" }
	t.Cleanup(func() { localDoltSocketPath = orig })
}

func TestBuildDoltDSN_Socket(t *testing.T) {
	withMockSocket(t, "/tmp/mysql.sock")
	got := buildDoltDSN("root", 3307, "hq", dsnOpts{
		ParseTime:   true,
		Timeout:     "5s",
		ReadTimeout: "10s",
	})
	want := "root@unix(/tmp/mysql.sock)/hq?parseTime=true&timeout=5s&readTimeout=10s"
	if got != want {
		t.Errorf("got\n  %s\nwant\n  %s", got, want)
	}
}

func TestBuildDoltDSN_TCPFallback(t *testing.T) {
	withNoSocket(t)
	got := buildDoltDSN("root", 3307, "hq", dsnOpts{
		ParseTime:   true,
		Timeout:     "5s",
		ReadTimeout: "10s",
	})
	want := "root@tcp(127.0.0.1:3307)/hq?parseTime=true&timeout=5s&readTimeout=10s"
	if got != want {
		t.Errorf("got\n  %s\nwant\n  %s", got, want)
	}
}

func TestBuildDoltDSN_EmptyDBName(t *testing.T) {
	// install.go:522 uses no dbName; the trailing slash with empty dbName
	// is valid in the go-mysql-driver DSN spec.
	withNoSocket(t)
	got := buildDoltDSN("root", 3307, "", dsnOpts{
		Timeout:      "1s",
		ReadTimeout:  "1s",
		WriteTimeout: "1s",
	})
	want := "root@tcp(127.0.0.1:3307)/?timeout=1s&readTimeout=1s&writeTimeout=1s"
	if got != want {
		t.Errorf("got\n  %s\nwant\n  %s", got, want)
	}
}

func TestBuildDoltDSN_NoQueryParams(t *testing.T) {
	// install.go:662 has no query parameters; helper should omit the
	// trailing "?".
	withNoSocket(t)
	got := buildDoltDSN("root", 3307, "", dsnOpts{})
	want := "root@tcp(127.0.0.1:3307)/"
	if got != want {
		t.Errorf("got\n  %s\nwant\n  %s", got, want)
	}
}

func TestBuildDoltDSN_DefaultUser(t *testing.T) {
	// Empty user defaults to "root" (matches the inline DSNs that
	// previously hardcoded "root").
	withNoSocket(t)
	got := buildDoltDSN("", 3307, "hq", dsnOpts{})
	if !strings.HasPrefix(got, "root@") {
		t.Errorf("expected DSN to start with root@, got %q", got)
	}
}

func TestBuildDoltDSN_AllOpts(t *testing.T) {
	// Every option populated → all four query params present in declared order.
	withNoSocket(t)
	got := buildDoltDSN("root", 3307, "hq", dsnOpts{
		ParseTime:    true,
		Timeout:      "5s",
		ReadTimeout:  "30s",
		WriteTimeout: "30s",
	})
	want := "root@tcp(127.0.0.1:3307)/hq?parseTime=true&timeout=5s&readTimeout=30s&writeTimeout=30s"
	if got != want {
		t.Errorf("got\n  %s\nwant\n  %s", got, want)
	}
}

func TestBuildDoltDSNFromConfig(t *testing.T) {
	withNoSocket(t)
	cfg := &doltserver.Config{User: "root", Port: 3307}
	got := buildDoltDSNFromConfig(cfg, "hq", dsnOpts{
		ParseTime:    true,
		Timeout:      "5s",
		ReadTimeout:  "30s",
		WriteTimeout: "30s",
	})
	want := "root@tcp(127.0.0.1:3307)/hq?parseTime=true&timeout=5s&readTimeout=30s&writeTimeout=30s"
	if got != want {
		t.Errorf("got\n  %s\nwant\n  %s", got, want)
	}
}

func TestBuildDoltDSNFromConfig_RemoteHostPreserved(t *testing.T) {
	withMockSocket(t, "/tmp/mysql.13306.sock")
	cfg := &doltserver.Config{User: "alice", Host: "10.0.0.5", Port: 13306}
	got := buildDoltDSNFromConfig(cfg, "hq", dsnOpts{ParseTime: true})
	want := "alice@tcp(10.0.0.5:13306)/hq?parseTime=true"
	if got != want {
		t.Errorf("got\n  %s\nwant\n  %s", got, want)
	}
}

func TestBuildDoltDSNFromConfig_LocalHostFallbackPreserved(t *testing.T) {
	withNoSocket(t)
	cfg := &doltserver.Config{User: "root", Host: "localhost", Port: 13306}
	got := buildDoltDSNFromConfig(cfg, "hq", dsnOpts{})
	want := "root@tcp(localhost:13306)/hq"
	if got != want {
		t.Errorf("got\n  %s\nwant\n  %s", got, want)
	}
}

func TestBuildDoltDSNFromConfig_TownScopedSocketPreferred(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix domain sockets not supported on Windows")
	}

	townRoot := t.TempDir()
	dataDir := filepath.Join(townRoot, ".dolt-data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatal(err)
	}
	sockPath := doltserver.SocketPath(townRoot)
	_ = os.Remove(sockPath)
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
		_ = os.Remove(sockPath)
	})

	cfg := &doltserver.Config{User: "root", TownRoot: townRoot, Port: 3327}
	got := buildDoltDSNFromConfig(cfg, "hq", dsnOpts{})
	want := "root@unix(" + sockPath + ")/hq"
	if got != want {
		t.Errorf("got\n  %s\nwant\n  %s", got, want)
	}
}

// TestLocalDoltSocketPath_RealSocket verifies the actual probe (not the
// test mock) recognizes a live unix socket.
func TestLocalDoltSocketPath_RealSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix domain sockets not supported on Windows")
	}

	port := 20000 + os.Getpid()%10000
	sockPath := fmt.Sprintf("/tmp/mysql.%d.sock", port)
	_ = os.Remove(sockPath)
	t.Cleanup(func() { _ = os.Remove(sockPath) })

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	got := buildDoltDSN("root", port, "hq", dsnOpts{Timeout: "1s"})
	wantSubstr := "@unix(" + sockPath + ")/hq"
	if !strings.Contains(got, wantSubstr) {
		t.Errorf("got %q; expected to contain %q", got, wantSubstr)
	}
}

func TestLocalDoltSocketPath_StaleSocketReturnsEmpty(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix domain sockets not supported on Windows")
	}

	port := 30000 + os.Getpid()%10000
	sockPath := fmt.Sprintf("/tmp/mysql.%d.sock", port)
	_ = os.Remove(sockPath)
	t.Cleanup(func() { _ = os.Remove(sockPath) })

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	got := buildDoltDSN("root", port, "hq", dsnOpts{Timeout: "1s"})
	wantSubstr := fmt.Sprintf("@tcp(127.0.0.1:%d)/hq", port)
	if !strings.Contains(got, wantSubstr) {
		t.Errorf("expected TCP fallback for stale socket, got %q", got)
	}
}

func TestLocalDoltSocketPath_AbsentReturnsEmpty(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/tmp", "wad6f")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	nonExistent := filepath.Join(tmpDir, "not-a-real-socket.sock")
	orig := localDoltSocketPath
	localDoltSocketPath = func(int) string {
		info, err := os.Stat(nonExistent)
		if err != nil {
			return ""
		}
		if info.Mode()&os.ModeSocket == 0 {
			return ""
		}
		return nonExistent
	}
	t.Cleanup(func() { localDoltSocketPath = orig })

	got := buildDoltDSN("root", 3307, "hq", dsnOpts{Timeout: "1s"})
	if !strings.Contains(got, "@tcp(127.0.0.1:3307)/hq") {
		t.Errorf("expected TCP fallback when socket absent, got %q", got)
	}
}
