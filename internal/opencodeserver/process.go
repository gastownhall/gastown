package opencodeserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/util"
)

const (
	defaultStartupTimeout = 30 * time.Second
	serverUsername        = "opencode"
)

type ServerOptions struct {
	Command        string
	CommandArgs    []string
	Directory      string
	StartupTimeout time.Duration
	Environment    map[string]string
}

type ServerProcess struct {
	cmd      *exec.Cmd
	url      string
	username string
	password string
	version  string
	cancel   context.CancelFunc
	done     chan struct{}
	waitMu   sync.RWMutex
	waitErr  error
	stopOnce sync.Once
	stopErr  error
	guard    func() error
}

func StartServer(ctx context.Context, opts ServerOptions) (*ServerProcess, error) {
	if opts.Command == "" {
		return nil, fmt.Errorf("OpenCode command is required")
	}
	if opts.Directory == "" {
		return nil, fmt.Errorf("OpenCode directory is required")
	}
	if opts.StartupTimeout <= 0 {
		opts.StartupTimeout = defaultStartupTimeout
	}

	port, err := availablePort()
	if err != nil {
		return nil, err
	}
	password, err := randomPassword()
	if err != nil {
		return nil, err
	}
	baseURL := "http://127.0.0.1:" + strconv.Itoa(port)

	// The worker must abort an active turn before stopping the server. Keep the
	// child alive when the caller context is canceled; Stop owns termination.
	childCtx, cancel := context.WithCancel(context.Background())
	args := append([]string(nil), opts.CommandArgs...)
	args = append(args, "serve", "--hostname", "127.0.0.1", "--port", strconv.Itoa(port))
	cmd := exec.CommandContext(childCtx, opts.Command, args...) //nolint:gosec // command is operator-configured
	cmd.Dir = opts.Directory
	cmd.Env = mergeEnvironment(os.Environ(), opts.Environment, map[string]string{
		"OPENCODE_SERVER_USERNAME": serverUsername,
		"OPENCODE_SERVER_PASSWORD": password,
	})
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	util.SetProcessGroup(cmd)
	configureServerCommand(cmd)

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("starting OpenCode server: %w", err)
	}
	guard, err := attachServerProcessGuard(cmd.Process.Pid)
	if err != nil {
		_ = terminateProcessTree(cmd.Process.Pid)
		cancel()
		_ = cmd.Wait()
		return nil, fmt.Errorf("protecting OpenCode server process tree: %w", err)
	}
	server := &ServerProcess{
		cmd:      cmd,
		url:      baseURL,
		username: serverUsername,
		password: password,
		cancel:   cancel,
		done:     make(chan struct{}),
		guard:    guard,
	}
	go func() {
		err := cmd.Wait()
		server.waitMu.Lock()
		server.waitErr = err
		server.waitMu.Unlock()
		close(server.done)
	}()

	client, err := NewClient(baseURL, serverUsername, password, opts.Directory, &http.Client{Timeout: time.Second})
	if err != nil {
		_ = server.Stop()
		return nil, err
	}
	deadline := time.NewTimer(opts.StartupTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		healthCtx, healthCancel := context.WithTimeout(ctx, time.Second)
		health, healthErr := client.Health(healthCtx)
		healthCancel()
		if healthErr == nil && health.Healthy {
			if !SupportedVersion(health.Version) {
				_ = server.Stop()
				return nil, fmt.Errorf("unsupported OpenCode server version %q; expected 1.18.16 or newer 1.x", health.Version)
			}
			server.version = health.Version
			return server, nil
		}

		select {
		case <-ctx.Done():
			_ = server.Stop()
			return nil, ctx.Err()
		case <-server.done:
			return nil, fmt.Errorf("OpenCode server exited before becoming ready: %v", server.Err())
		case <-deadline.C:
			_ = server.Stop()
			return nil, fmt.Errorf("timed out waiting for OpenCode server readiness")
		case <-ticker.C:
		}
	}
}

func SupportedVersion(version string) bool {
	version = strings.TrimSpace(version)
	if strings.HasPrefix(version, "v") {
		version = strings.TrimPrefix(version, "v")
	}
	coreAndPrerelease, build, hasBuild := strings.Cut(version, "+")
	if hasBuild && !validSemverIdentifiers(build, false) {
		return false
	}
	core, prerelease, hasPrerelease := strings.Cut(coreAndPrerelease, "-")
	if hasPrerelease && !validSemverIdentifiers(prerelease, true) {
		return false
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return false
		}
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil || major != 1 {
		return false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		return false
	}
	if minor < 18 || (minor == 18 && patch < 16) {
		return false
	}
	return minor > 18 || patch > 16 || !hasPrerelease
}

func validSemverIdentifiers(value string, rejectNumericLeadingZero bool) bool {
	if value == "" {
		return false
	}
	for _, identifier := range strings.Split(value, ".") {
		if identifier == "" {
			return false
		}
		numeric := true
		for _, char := range identifier {
			if (char < '0' || char > '9') && (char < 'A' || char > 'Z') && (char < 'a' || char > 'z') && char != '-' {
				return false
			}
			if char < '0' || char > '9' {
				numeric = false
			}
		}
		if rejectNumericLeadingZero && numeric && len(identifier) > 1 && identifier[0] == '0' {
			return false
		}
	}
	return true
}

func (s *ServerProcess) Client(directory string) *Client {
	client, err := s.NewClient(directory)
	if err != nil {
		panic(err)
	}
	return client
}

func (s *ServerProcess) NewClient(directory string) (*Client, error) {
	return NewClient(s.url, s.username, s.password, directory, nil)
}

func (s *ServerProcess) URL() string      { return s.url }
func (s *ServerProcess) Username() string { return s.username }
func (s *ServerProcess) Password() string { return s.password }
func (s *ServerProcess) Version() string  { return s.version }
func (s *ServerProcess) PID() int {
	if s.cmd == nil || s.cmd.Process == nil {
		return 0
	}
	return s.cmd.Process.Pid
}
func (s *ServerProcess) Done() <-chan struct{} { return s.done }

func (s *ServerProcess) Err() error {
	s.waitMu.RLock()
	defer s.waitMu.RUnlock()
	return s.waitErr
}

func (s *ServerProcess) Stop() error {
	s.stopOnce.Do(func() {
		if s.cmd != nil && s.cmd.Process != nil {
			s.stopErr = terminateProcessTree(s.cmd.Process.Pid)
		}
		if s.guard != nil {
			s.stopErr = errors.Join(s.stopErr, s.guard())
		}
		s.cancel()
		select {
		case <-s.done:
		case <-time.After(5 * time.Second):
			if s.cmd != nil && s.cmd.Process != nil {
				if err := s.cmd.Process.Kill(); s.stopErr == nil {
					s.stopErr = err
				}
			}
			select {
			case <-s.done:
			case <-time.After(time.Second):
			}
		}
	})
	return s.stopErr
}

func availablePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("selecting OpenCode server port: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return 0, fmt.Errorf("releasing OpenCode server port: %w", err)
	}
	return port, nil
}

func randomPassword() (string, error) {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", fmt.Errorf("generating OpenCode server password: %w", err)
	}
	return hex.EncodeToString(data[:]), nil
}

func mergeEnvironment(base []string, overlays ...map[string]string) []string {
	values := make(map[string]string, len(base))
	keys := make(map[string]string, len(base))
	canonicalKey := func(key string) string {
		if runtime.GOOS == "windows" {
			return strings.ToUpper(key)
		}
		return key
	}
	for _, entry := range base {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			canonical := canonicalKey(key)
			keys[canonical] = key
			values[canonical] = value
		}
	}
	for _, overlay := range overlays {
		for key, value := range overlay {
			canonical := canonicalKey(key)
			keys[canonical] = key
			values[canonical] = value
		}
	}
	result := make([]string, 0, len(values))
	for canonical, value := range values {
		result = append(result, keys[canonical]+"="+value)
	}
	return result
}
