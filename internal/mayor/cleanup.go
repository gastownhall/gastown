package mayor

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jonbaldie/gastown/internal/workspace"
)

const (
	acpPidFileName   = "mayor-acp.pid"
	acpAgentFileName = "mayor-acp.agent"
)

var (
	ErrCleanupVetoed = fmt.Errorf("cleanup vetoed: ACP session is active")
)

func ACPPidFilePath(townRoot string) string {
	return filepath.Join(townRoot, "mayor", acpPidFileName)
}

func WriteACPPid(townRoot string) error {
	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		return fmt.Errorf("creating mayor directory: %w", err)
	}

	pidPath := ACPPidFilePath(townRoot)
	pid := os.Getpid()
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(pid)), 0644); err != nil {
		return fmt.Errorf("writing ACP PID file: %w", err)
	}
	return nil
}

func RemoveACPPid(townRoot string) error {
	pidPath := ACPPidFilePath(townRoot)
	if _, err := os.Stat(pidPath); os.IsNotExist(err) {
		return nil
	}
	return os.Remove(pidPath)
}

func GetACPPid(townRoot string) (int, error) {
	pidPath := ACPPidFilePath(townRoot)
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return 0, err
	}

	pidStr := strings.TrimSpace(string(data))
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return 0, fmt.Errorf("invalid PID in %s: %w", pidPath, err)
	}

	return pid, nil
}

// ACP agent name persistence functions

func ACPAgentFilePath(townRoot string) string {
	return filepath.Join(townRoot, "mayor", acpAgentFileName)
}

func WriteACPAgent(townRoot, agentName string) error {
	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		return fmt.Errorf("creating mayor directory: %w", err)
	}

	agentPath := ACPAgentFilePath(townRoot)
	if err := os.WriteFile(agentPath, []byte(agentName), 0644); err != nil {
		return fmt.Errorf("writing ACP agent file: %w", err)
	}
	return nil
}

func RemoveACPAgent(townRoot string) error {
	agentPath := ACPAgentFilePath(townRoot)
	if _, err := os.Stat(agentPath); os.IsNotExist(err) {
		return nil
	}
	return os.Remove(agentPath)
}

func GetACPAgent(townRoot string) (string, error) {
	agentPath := ACPAgentFilePath(townRoot)
	data, err := os.ReadFile(agentPath)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(data)), nil
}

func IsACPActive(townRoot string) bool {
	pid, err := GetACPPid(townRoot)
	if err != nil {
		return false
	}

	// Check if process is alive without any side effects.
	// NOTE: We intentionally do NOT remove stale PID files here.
	// Stale PID cleanup should be done explicitly via CleanupStaleACP(),
	// not as a side effect of checking if ACP is active.
	// Removing the PID file here triggers the proxy's PID file monitoring,
	// causing unexpected shutdowns.
	return acpProcessAlive(pid)
}

func IsACPActiveInWorkDir(workDir string) bool {
	townRoot, err := workspace.Find(workDir)
	if err != nil || townRoot == "" {
		return false
	}
	return IsACPActive(townRoot)
}

type CleanupVetoChecker struct {
	townRoot string
}

func NewCleanupVetoChecker(townRoot string) *CleanupVetoChecker {
	return &CleanupVetoChecker{townRoot: townRoot}
}

func NewCleanupVetoCheckerFromWorkDir(workDir string) (*CleanupVetoChecker, error) {
	townRoot, err := workspace.Find(workDir)
	if err != nil {
		return nil, fmt.Errorf("finding town root: %w", err)
	}
	if townRoot == "" {
		return nil, fmt.Errorf("not in a Gas Town workspace")
	}
	return NewCleanupVetoChecker(townRoot), nil
}

func (c *CleanupVetoChecker) ShouldVetoCleanup() (bool, string) {
	if IsACPActive(c.townRoot) {
		return true, "ACP session is active - Mayor may be reviewing worker diffs"
	}
	return false, ""
}

func (c *CleanupVetoChecker) VetoIfActive() error {
	if vetoed, reason := c.ShouldVetoCleanup(); vetoed {
		return fmt.Errorf("%w: %s", ErrCleanupVetoed, reason)
	}
	return nil
}

func (c *CleanupVetoChecker) GetACPExpiry() (time.Time, bool) {
	pidPath := ACPPidFilePath(c.townRoot)
	info, err := os.Stat(pidPath)
	if err != nil {
		return time.Time{}, false
	}
	return info.ModTime(), true
}

func (c *CleanupVetoChecker) CleanupStaleACP() error {
	pidPath := ACPPidFilePath(c.townRoot)
	if _, err := os.Stat(pidPath); os.IsNotExist(err) {
		return nil
	}

	if IsACPActive(c.townRoot) {
		return nil
	}

	return RemoveACPPid(c.townRoot)
}
