// Package opencodestate stores the durable mapping between a Gas Town tmux
// session and its OpenCode HTTP session. It intentionally has no dependency on
// the worker process package so low-level transports can query it safely.
package opencodestate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/steveyegge/gastown/internal/atomicfile"
	"github.com/steveyegge/gastown/internal/constants"
)

const maxStateSize = 64 << 10

type State struct {
	GasTownSession  string    `json:"gastown_session"`
	OpenCodeSession string    `json:"opencode_session"`
	WorkKey         string    `json:"work_key,omitempty"`
	Directory       string    `json:"directory"`
	Agent           string    `json:"agent,omitempty"`
	Model           string    `json:"model,omitempty"`
	Variant         string    `json:"variant,omitempty"`
	URL             string    `json:"url"`
	Username        string    `json:"username"`
	Password        string    `json:"password"`
	PID             int       `json:"pid"`
	Version         string    `json:"version"`
	CreatedAt       time.Time `json:"created_at"`
}

func Path(townRoot, gasTownSession string) string {
	sum := sha256.Sum256([]byte(gasTownSession))
	name := hex.EncodeToString(sum[:16]) + ".json"
	return filepath.Join(townRoot, constants.DirRuntime, "opencode-server", name)
}

func AcquireSessionLock(townRoot, gasTownSession string) (func(), error) {
	if townRoot == "" || gasTownSession == "" {
		return nil, fmt.Errorf("town root and Gas Town session are required")
	}
	lockPath := Path(townRoot, gasTownSession) + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		return nil, fmt.Errorf("creating OpenCode lock directory: %w", err)
	}
	if err := protectCredentialPath(filepath.Dir(lockPath), true); err != nil {
		return nil, fmt.Errorf("protecting OpenCode lock directory: %w", err)
	}
	fileLock := flock.New(lockPath, flock.SetPermissions(0600))
	locked, err := fileLock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("acquiring OpenCode worker lock: %w", err)
	}
	if !locked {
		return nil, fmt.Errorf("OpenCode server worker is already running for %s", gasTownSession)
	}
	if err := protectCredentialPath(lockPath, false); err != nil {
		_ = fileLock.Unlock()
		return nil, fmt.Errorf("protecting OpenCode worker lock: %w", err)
	}

	var once sync.Once
	return func() {
		once.Do(func() { _ = fileLock.Unlock() })
	}, nil
}

func SessionLockHeld(townRoot, gasTownSession string) (bool, error) {
	if townRoot == "" || gasTownSession == "" {
		return false, fmt.Errorf("town root and Gas Town session are required")
	}
	lockPath := Path(townRoot, gasTownSession) + ".lock"
	if _, err := os.Stat(lockPath); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("checking OpenCode worker lock: %w", err)
	}
	probe := flock.New(lockPath)
	locked, err := probe.TryLock()
	if err != nil {
		return false, fmt.Errorf("probing OpenCode worker lock: %w", err)
	}
	if locked {
		_ = probe.Unlock()
		return false, nil
	}
	return true, nil
}

func Save(townRoot string, state State) error {
	if townRoot == "" || state.GasTownSession == "" || state.OpenCodeSession == "" || state.Directory == "" || state.URL == "" || state.Username == "" || state.Password == "" {
		return fmt.Errorf("incomplete OpenCode server state")
	}
	if state.CreatedAt.IsZero() {
		state.CreatedAt = time.Now().UTC()
	}
	path := Path(townRoot, state.GasTownSession)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("creating OpenCode state directory: %w", err)
	}
	if err := protectCredentialPath(filepath.Dir(path), true); err != nil {
		return fmt.Errorf("protecting OpenCode state directory: %w", err)
	}
	if err := atomicfile.WriteJSONWithPerm(path, state, 0600); err != nil {
		return fmt.Errorf("writing OpenCode state: %w", err)
	}
	if err := protectCredentialPath(path, false); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("protecting OpenCode state: %w", err)
	}
	return nil
}

func Load(townRoot, gasTownSession string) (State, error) {
	path := Path(townRoot, gasTownSession)
	file, err := os.Open(path) //nolint:gosec // path is derived from a hash under townRoot
	if err != nil {
		return State{}, err
	}
	defer file.Close()

	var state State
	decoder := json.NewDecoder(io.LimitReader(file, maxStateSize+1))
	if err := decoder.Decode(&state); err != nil {
		return State{}, fmt.Errorf("decoding OpenCode state: %w", err)
	}
	if info, err := file.Stat(); err == nil && info.Size() > maxStateSize {
		return State{}, fmt.Errorf("OpenCode state exceeds %d bytes", maxStateSize)
	}
	if state.GasTownSession != gasTownSession {
		return State{}, fmt.Errorf("OpenCode state session mismatch: got %q, want %q", state.GasTownSession, gasTownSession)
	}
	return state, nil
}

func Remove(townRoot, gasTownSession, openCodeSession string) error {
	state, err := Load(townRoot, gasTownSession)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if state.OpenCodeSession != openCodeSession {
		return nil
	}
	if err := os.Remove(Path(townRoot, gasTownSession)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing OpenCode state: %w", err)
	}
	return nil
}

func Active(ctx context.Context, townRoot, gasTownSession string) (State, bool) {
	state, err := Load(townRoot, gasTownSession)
	if err != nil {
		return State{}, false
	}
	if held, err := SessionLockHeld(townRoot, gasTownSession); err != nil || !held {
		return State{}, false
	}
	parsed, err := url.Parse(state.URL)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" {
		return State{}, false
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return State{}, false
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(state.URL, "/")+"/global/health", nil)
	if err != nil {
		return State{}, false
	}
	req.SetBasicAuth(state.Username, state.Password)
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return State{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return State{}, false
	}
	var health struct {
		Healthy bool `json:"healthy"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&health); err != nil || !health.Healthy {
		return State{}, false
	}
	return state, true
}
