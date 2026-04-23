package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(sessionDiaryCmd)
}

var sessionDiaryCmd = &cobra.Command{
	Use:    "session-diary",
	Short:  "Write a MemPalace diary entry for this session (called by Stop hook)",
	Hidden: true,
	RunE:   runSessionDiary,
}

func runSessionDiary(cmd *cobra.Command, args []string) error {
	townRoot := os.Getenv("GT_TOWN_ROOT")
	if townRoot == "" {
		townRoot = filepath.Join(os.Getenv("HOME"), "gt")
	}
	pythonBin := filepath.Join(townRoot, ".mempalace-env", "bin", "python")

	// Skip if mempalace not installed
	if _, err := os.Stat(pythonBin); err != nil {
		return nil
	}

	role := os.Getenv("GT_ROLE")
	if role == "" {
		role = "unknown"
	}
	rig := os.Getenv("GT_RIG")
	if rig == "" {
		rig = "hq"
	}
	sessionID := os.Getenv("GT_SESSION_ID")
	if sessionID == "" {
		sessionID = "no-session"
	}

	// Build a compact AAAK diary entry
	now := time.Now().Format("2006-01-02T15:04")
	entry := fmt.Sprintf("SESSION:%s|role:%s|rig:%s|sid:%s|status:completed",
		now, role, rig, sessionID[:8])

	// Write diary via Python mempalace API
	script := fmt.Sprintf(`
import sys
from mempalace.mcp_server import palace_instance
p = palace_instance()
p.diary_write(agent_name=%q, entry=%q, topic="session")
print("ok")
`, role, entry)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c := exec.CommandContext(ctx, pythonBin, "-c", script)
	var stderr bytes.Buffer
	c.Stderr = &stderr

	if err := c.Run(); err != nil {
		// Fail silently — diary is best-effort
		return nil
	}

	return nil
}
