package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// DiscordWatcher manages the Python Discord watcher as a subprocess.
// It monitors Discord for @mentions and DMs, restarting on failure.
type DiscordWatcher struct {
	townRoot string
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	logger   func(format string, args ...interface{})
	token    string
}

// NewDiscordWatcher creates a new Discord watcher.
func NewDiscordWatcher(townRoot string, logger func(format string, args ...interface{})) *DiscordWatcher {
	ctx, cancel := context.WithCancel(context.Background())
	return &DiscordWatcher{
		townRoot: townRoot,
		ctx:      ctx,
		cancel:   cancel,
		logger:   logger,
	}
}

// Start begins the Discord watcher goroutine.
func (w *DiscordWatcher) Start() error {
	// Extract Discord token
	token, err := w.extractDiscordToken()
	if err != nil {
		return fmt.Errorf("failed to get Discord token: %w", err)
	}
	w.token = token

	w.wg.Add(1)
	go w.run()
	w.logger("Discord watcher started")
	return nil
}

// Stop gracefully stops the Discord watcher.
func (w *DiscordWatcher) Stop() {
	w.logger("Stopping Discord watcher...")
	w.cancel()
	w.wg.Wait()
	w.logger("Discord watcher stopped")
}

// run is the main watcher loop that manages the Python subprocess.
func (w *DiscordWatcher) run() {
	defer w.wg.Done()

	backoff := 5 * time.Second
	maxBackoff := 5 * time.Minute

	for {
		select {
		case <-w.ctx.Done():
			return
		default:
			// Run the Python watcher
			if err := w.runPythonWatcher(); err != nil {
				w.logger("Discord watcher error: %v, restarting in %s", err, backoff)

				// Wait before retry with exponential backoff
				select {
				case <-w.ctx.Done():
					return
				case <-time.After(backoff):
					// Increase backoff up to max
					backoff = backoff * 2
					if backoff > maxBackoff {
						backoff = maxBackoff
					}
				}
			} else {
				// Reset backoff on clean exit
				backoff = 5 * time.Second
			}
		}
	}
}

// findWatcherScript locates the Python watcher script using multiple strategies:
// 1. Next to the running binary (installed/deployed case)
// 2. Legacy dev path relative to townRoot (source checkout case)
func (w *DiscordWatcher) findWatcherScript() (string, error) {
	// Strategy 1: look next to the binary (works in installed/merged codebase)
	if exePath, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exePath), "discord", "watcher.py")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}

	// Strategy 2: legacy dev path (source checkout structure)
	devPath := filepath.Join(w.townRoot, "gastown", "crew", "hal", "internal", "discord", "watcher.py")
	if _, err := os.Stat(devPath); err == nil {
		return devPath, nil
	}

	return "", fmt.Errorf("watcher script not found (checked binary-relative and dev paths)")
}

// runPythonWatcher starts the Python watcher script and waits for it to complete.
func (w *DiscordWatcher) runPythonWatcher() error {
	// Locate the Python watcher script
	scriptPath, err := w.findWatcherScript()
	if err != nil {
		return err
	}

	// Create command
	cmd := exec.CommandContext(w.ctx, "python3", scriptPath)
	cmd.Dir = w.townRoot

	// Set environment variables
	env := os.Environ()
	env = append(env, fmt.Sprintf("DISCORD_TOKEN=%s", w.token))
	cmd.Env = env

	// Connect stdout/stderr to daemon log
	cmd.Stdout = &logWriter{w.logger, "discord-watcher"}
	cmd.Stderr = &logWriter{w.logger, "discord-watcher"}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting Python watcher: %w", err)
	}

	w.logger("Discord watcher subprocess started (PID %d)", cmd.Process.Pid)

	// Wait for process to complete
	err = cmd.Wait()
	if w.ctx.Err() != nil {
		// Context was cancelled, this is expected
		return nil
	}

	return err
}

// extractDiscordToken gets the Discord token from environment or .mcp.json.
func (w *DiscordWatcher) extractDiscordToken() (string, error) {
	// Try environment variable first
	if token := os.Getenv("DISCORD_TOKEN"); token != "" {
		w.logger("Using Discord token from DISCORD_TOKEN environment variable")
		return token, nil
	}

	// Try .mcp.json
	mcpConfigPath := filepath.Join(w.townRoot, ".mcp.json")
	data, err := os.ReadFile(mcpConfigPath)
	if err != nil {
		return "", fmt.Errorf("DISCORD_TOKEN not in environment and cannot read .mcp.json: %w", err)
	}

	var mcpConfig struct {
		MCPServers map[string]struct {
			Env map[string]string `json:"env"`
		} `json:"mcpServers"`
	}

	if err := json.Unmarshal(data, &mcpConfig); err != nil {
		return "", fmt.Errorf("failed to parse .mcp.json: %w", err)
	}

	// Look for discord-mcp server config
	for serverName, serverConfig := range mcpConfig.MCPServers {
		if token, ok := serverConfig.Env["DISCORD_TOKEN"]; ok {
			w.logger("Using Discord token from .mcp.json server: %s", serverName)
			return token, nil
		}
	}

	return "", fmt.Errorf("DISCORD_TOKEN not found in environment or .mcp.json")
}

// logWriter wraps the logger to implement io.Writer for subprocess output.
type logWriter struct {
	logger func(format string, args ...interface{})
	prefix string
}

func (lw *logWriter) Write(p []byte) (n int, err error) {
	if len(p) > 0 {
		lw.logger("[%s] %s", lw.prefix, string(p))
	}
	return len(p), nil
}
