package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/style"
)

var rememberKey string
var rememberType string

func init() {
	rememberCmd.Flags().StringVar(&rememberKey, "key", "", "Explicit key slug (default: auto-generated from content)")
	rememberCmd.Flags().StringVar(&rememberType, "type", "", "Deprecated no-op: bd memories are untyped")
	_ = rememberCmd.Flags().MarkDeprecated("type", "bd memories are untyped; the type is ignored")
	rememberCmd.GroupID = GroupWork
	rootCmd.AddCommand(rememberCmd)
}

var rememberCmd = &cobra.Command{
	Use:   `remember "insight"`,
	Short: "Store a persistent memory",
	Long: `Store a persistent memory in the beads memory store (bd remember).

Memories persist across sessions and are injected during gt prime.
They are managed by bd's first-class memory system, so they can also
be inspected and edited directly with:
  bd remember, bd recall, bd memories, bd forget

The key is auto-generated from the content if not specified.
Use --key to provide an explicit slug for easy retrieval.

--type is deprecated and ignored: bd memories are untyped.

Examples:
  gt remember "Refinery uses worktree, cannot checkout main"
  gt remember --key senior-go-dev "User has 10 years Go experience"
  gt remember --key refinery-worktree "Refinery uses worktree, cannot checkout main"`,
	Args: cobra.ExactArgs(1),
	RunE: runRemember,
}

func runRemember(cmd *cobra.Command, args []string) error {
	content := args[0]
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("memory content cannot be empty")
	}

	key := rememberKey
	if key == "" {
		key = autoKey(content)
	}

	// Sanitize key: lowercase, hyphens instead of spaces, strip dots
	key = sanitizeKey(key)
	if key == "" {
		key = autoKey(content)
	}

	var out, errOut bytes.Buffer
	bdCmd := exec.Command("bd", "remember", content, "--key", key)
	bdCmd.Stdout = &out
	bdCmd.Stderr = &errOut
	if err := bdCmd.Run(); err != nil {
		if msg := strings.TrimSpace(errOut.String()); msg != "" {
			return fmt.Errorf("storing memory: %s", msg)
		}
		if msg := strings.TrimSpace(out.String()); msg != "" {
			return fmt.Errorf("storing memory: %s", msg)
		}
		return fmt.Errorf("storing memory: %w", err)
	}

	verb := "Stored"
	if strings.HasPrefix(strings.TrimSpace(out.String()), "Updated") {
		verb = "Updated"
	}
	fmt.Printf("%s %s memory: %s\n", style.Success.Render("✓"), verb, style.Bold.Render(key))
	return nil
}

// autoKey generates a short key from content using first few meaningful words.
func autoKey(content string) string {
	// Take first ~5 words, lowercase, hyphenate
	words := strings.Fields(strings.ToLower(content))
	if len(words) > 5 {
		words = words[:5]
	}

	// Strip non-alphanumeric chars from each word
	var clean []string
	for _, w := range words {
		w = strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				return r
			}
			return -1
		}, w)
		if w != "" {
			clean = append(clean, w)
		}
	}

	if len(clean) == 0 {
		// Fallback to hash
		h := sha256.Sum256([]byte(content))
		return hex.EncodeToString(h[:4])
	}

	slug := strings.Join(clean, "-")
	// Cap length
	if len(slug) > 40 {
		slug = slug[:40]
	}
	return slug
}

// sanitizeKey normalizes a key slug.
func sanitizeKey(key string) string {
	key = strings.ToLower(key)
	key = strings.ReplaceAll(key, " ", "-")
	key = strings.ReplaceAll(key, ".", "-")

	// Strip anything that isn't alphanumeric or hyphen
	key = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return -1
	}, key)

	// Collapse multiple hyphens
	for strings.Contains(key, "--") {
		key = strings.ReplaceAll(key, "--", "-")
	}
	key = strings.Trim(key, "-")

	return key
}

// bdMemoriesJSON calls bd memories --json and returns key -> content.
func bdMemoriesJSON() (map[string]string, error) {
	cmd := exec.Command("bd", "memories", "--json")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	return parseBdMemoriesJSON(out)
}

// parseBdMemoriesJSON parses bd memories --json output: a flat map of
// key -> content plus a schema_version entry.
func parseBdMemoriesJSON(data []byte) (map[string]string, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing memories: %w", err)
	}

	mems := make(map[string]string, len(raw))
	for k, v := range raw {
		if k == "schema_version" || bytes.Equal(v, []byte("null")) {
			continue
		}

		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			mems[k] = s
			continue
		}

		// Keep non-string values visible without dropping them.
		var compact bytes.Buffer
		if err := json.Compact(&compact, v); err == nil {
			mems[k] = compact.String()
		}
	}
	return mems, nil
}
