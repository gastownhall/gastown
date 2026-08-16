package connection

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/jonbaldie/gastown/internal/tmux"
)

// LocalConnection implements Connection for local file and command operations.
type LocalConnection struct {
	tmux *tmux.Tmux
}

// NewLocalConnection creates a new local connection.
func NewLocalConnection() *LocalConnection {
	return &LocalConnection{
		tmux: tmux.NewTmux(),
	}
}

// Name returns "local" for local connections.
func (c *LocalConnection) Name() string {
	return "local"
}

// IsLocal returns true for local connections.
func (c *LocalConnection) IsLocal() bool {
	return true
}

// ReadFile reads the named file.
func (c *LocalConnection) ReadFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is from Connection interface, validated by caller
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &NotFoundError{Path: path}
		}
		if os.IsPermission(err) {
			return nil, &PermissionError{Path: path, Op: "read"}
		}
		return nil, err
	}
	return data, nil
}

// WriteFile writes data to the named file.
func (c *LocalConnection) WriteFile(path string, data []byte, perm fs.FileMode) error {
	err := os.WriteFile(path, data, perm)
	if err != nil {
		if os.IsPermission(err) {
			return &PermissionError{Path: path, Op: "write"}
		}
		return err
	}
	return nil
}

// MkdirAll creates a directory and all parent directories.
func (c *LocalConnection) MkdirAll(path string, perm fs.FileMode) error {
	err := os.MkdirAll(path, perm)
	if err != nil {
		if os.IsPermission(err) {
			return &PermissionError{Path: path, Op: "mkdir"}
		}
		return err
	}
	return nil
}

// Remove removes the named file or empty directory.
func (c *LocalConnection) Remove(path string) error {
	err := os.Remove(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // Already gone
		}
		if os.IsPermission(err) {
			return &PermissionError{Path: path, Op: "remove"}
		}
		return err
	}
	return nil
}

// RemoveAll removes the named file or directory and any children.
func (c *LocalConnection) RemoveAll(path string) error {
	err := os.RemoveAll(path)
	if err != nil {
		if os.IsPermission(err) {
			return &PermissionError{Path: path, Op: "remove"}
		}
		return err
	}
	return nil
}

// Stat returns file info for the named file.
func (c *LocalConnection) Stat(path string) (FileInfo, error) {
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &NotFoundError{Path: path}
		}
		if os.IsPermission(err) {
			return nil, &PermissionError{Path: path, Op: "stat"}
		}
		return nil, err
	}
	return FromOSFileInfo(fi), nil
}

// Glob returns the names of all files matching the pattern.
func (c *LocalConnection) Glob(pattern string) ([]string, error) {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	return matches, nil
}

// Exists returns true if the path exists.
func (c *LocalConnection) Exists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Exec runs a command and returns its combined output.
func (c *LocalConnection) Exec(cmd string, args ...string) ([]byte, error) {
	return exec.Command(cmd, args...).CombinedOutput()
}

// ExecDir runs a command in the specified directory.
func (c *LocalConnection) ExecDir(dir, cmd string, args ...string) ([]byte, error) {
	command := exec.Command(cmd, args...)
	command.Dir = dir
	return command.CombinedOutput()
}

// ExecEnv runs a command with additional environment variables.
func (c *LocalConnection) ExecEnv(env map[string]string, cmd string, args ...string) ([]byte, error) {
	command := exec.Command(cmd, args...)
	command.Env = os.Environ()
	for k, v := range env {
		command.Env = append(command.Env, k+"="+v)
	}
	return command.CombinedOutput()
}

// TmuxNewSession creates a new tmux session.
func (c *LocalConnection) TmuxNewSession(name, dir string) error {
	return c.tmux.NewSession(name, dir)
}

// TmuxKillSession terminates a tmux session.
// Uses KillSessionWithProcesses to ensure all descendant processes are killed.
func (c *LocalConnection) TmuxKillSession(name string) error {
	return c.tmux.KillSessionWithProcesses(name)
}

// TmuxSendKeys sends keys to a tmux session.
func (c *LocalConnection) TmuxSendKeys(session, keys string) error {
	return c.tmux.SendKeys(session, keys)
}

// TmuxCapturePane captures the last N lines from a tmux pane.
func (c *LocalConnection) TmuxCapturePane(session string, lines int) (string, error) {
	return c.tmux.CapturePane(session, lines)
}

// TmuxHasSession returns true if the session exists.
func (c *LocalConnection) TmuxHasSession(name string) (bool, error) {
	return c.tmux.HasSession(name)
}

// TmuxListSessions returns all tmux session names.
func (c *LocalConnection) TmuxListSessions() ([]string, error) {
	return c.tmux.ListSessions()
}

// Verify LocalConnection implements Connection.
var _ Connection = (*LocalConnection)(nil)
