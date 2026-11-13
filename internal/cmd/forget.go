package cmd

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/style"
)

// legacyMemPrefix is the prefix gt used when storing memories in the
// beads KV store (memory.<type>.<key>). bd's first-class memory keys
// are untyped; the legacy prefix is stripped from user input so old
// references still resolve.
const legacyMemPrefix = "memory."

var forgetCmd = &cobra.Command{
	Use:   "forget <key>",
	Short: "Delete a stored memory",
	Long: `Delete a persistent memory from the beads memory store (bd forget).

The key is the same one shown by 'gt memories'. For backward
compatibility, legacy 'memory.<type>.<key>' KV keys and '<type>/<key>'
paths are accepted and reduced to their plain key.

Examples:
  gt forget refinery-worktree
  gt forget memory.feedback.dont-mock-db
  gt forget feedback/dont-mock-db`,
	Args: cobra.ExactArgs(1),
	RunE: runForget,
}

func init() {
	forgetCmd.GroupID = GroupWork
	rootCmd.AddCommand(forgetCmd)
}

func runForget(cmd *cobra.Command, args []string) error {
	key := normalizeMemoryKey(args[0])
	if key == "" {
		return fmt.Errorf("invalid memory key: %s", args[0])
	}

	var out, errOut bytes.Buffer
	bdCmd := exec.Command("bd", "forget", key)
	bdCmd.Stdout = &out
	bdCmd.Stderr = &errOut
	if err := bdCmd.Run(); err != nil {
		return forgetError(key, err, &out, &errOut)
	}

	fmt.Printf("%s Forgot memory: %s\n", style.Success.Render("✓"), style.Bold.Render(key))
	return nil
}

// normalizeMemoryKey strips the legacy 'memory.' prefix and any
// 'type/key' path form, then applies the same sanitization gt remember
// uses, so old KV-era references resolve to the plain bd key.
func normalizeMemoryKey(key string) string {
	key = strings.TrimPrefix(key, legacyMemPrefix)
	if idx := strings.LastIndex(key, "/"); idx >= 0 {
		key = key[idx+1:]
	}
	return sanitizeKey(key)
}

// forgetError renders a bd forget failure, preferring bd's own message
// over the generic exit status.
func forgetError(key string, err error, out, errOut *bytes.Buffer) error {
	if msg := strings.TrimSpace(errOut.String()); msg != "" {
		return fmt.Errorf("forgetting memory %s: %s", key, msg)
	}
	if msg := strings.TrimSpace(out.String()); msg != "" {
		return fmt.Errorf("forgetting memory %s: %s", key, msg)
	}
	return fmt.Errorf("forgetting memory %s: %w", key, err)
}
