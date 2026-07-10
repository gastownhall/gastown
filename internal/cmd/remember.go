package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/style"
)

const memoryKeyPrefix = "memory."

// validMemoryTypes are the recognized memory type categories.
// Typed memories are stored as memory.<type>.<key> in the kv store.
// Legacy untyped memories (memory.<key>) are treated as "general".
var validMemoryTypes = map[string]string{
	"feedback":  "Guidance or corrections from users — behavioral rules for future work",
	"project":   "Ongoing work context, goals, deadlines, decisions",
	"user":      "Info about the user's role, preferences, expertise",
	"reference": "Pointers to external resources (URLs, tools, dashboards)",
	"general":   "Uncategorized memories (default)",
}

// memoryTypeOrder defines the injection priority during gt prime.
// Feedback first (behavioral corrections), then user context, then the rest.
var memoryTypeOrder = []string{"feedback", "user", "project", "reference", "general"}

var rememberKey string
var rememberType string

func init() {
	rememberCmd.Flags().StringVar(&rememberKey, "key", "", "Explicit key slug (default: auto-generated from content)")
	rememberCmd.Flags().StringVar(&rememberType, "type", "", "Memory type: feedback, project, user, reference (default: general)")
	rememberCmd.GroupID = GroupWork
	rootCmd.AddCommand(rememberCmd)
}

var rememberCmd = &cobra.Command{
	Use:   `remember "insight"`,
	Short: "Store a persistent memory",
	Long: `Store a persistent memory in the beads key-value store.

Memories persist across sessions and are injected during gt prime.
This replaces filesystem-based MEMORY.md with bead-backed storage.

The key is auto-generated from the content if not specified.
Use --key to provide an explicit slug for easy retrieval.

Memory types help organize memories and prioritize injection:
  feedback   Guidance or corrections from users
  project    Ongoing work context, goals, deadlines
  user       Info about the user's role and preferences
  reference  Pointers to external resources

Examples:
  gt remember "Refinery uses worktree, cannot checkout main"
  gt remember --type feedback "Don't mock the database in integration tests"
  gt remember --type user --key senior-go-dev "User has 10 years Go experience"
  gt remember --key refinery-worktree "Refinery uses worktree, cannot checkout main"`,
	Args: cobra.ExactArgs(1),
	RunE: runRemember,
}

func runRemember(cmd *cobra.Command, args []string) error {
	content := args[0]
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("memory content cannot be empty")
	}

	// Validate --type if provided
	memType := strings.ToLower(strings.TrimSpace(rememberType))
	if memType != "" {
		if _, ok := validMemoryTypes[memType]; !ok {
			return fmt.Errorf("invalid memory type %q — valid types: feedback, project, user, reference", memType)
		}
	}
	if memType == "" {
		memType = "general"
	}

	key := rememberKey
	if key == "" {
		key = autoKey(content)
	}

	// Sanitize key: lowercase, hyphens instead of spaces, strip dots
	key = sanitizeKey(key)

	fullKey := memoryKeyPrefix + memType + "." + key

	// Check if key already exists
	existing, _ := bdKvGet(fullKey)
	verb := "Stored"
	if existing != "" {
		verb = "Updated"
	}

	if err := bdKvSet(fullKey, content); err != nil {
		return fmt.Errorf("storing memory: %w", err)
	}

	displayKey := key
	if memType != "general" {
		displayKey = memType + "/" + key
	}
	fmt.Printf("%s %s memory: %s\n", style.Success.Render("✓"), verb, style.Bold.Render(displayKey))
	return nil
}

// parseMemoryKey extracts the type and short key from a full kv key.
// Handles both typed keys (memory.<type>.<key>) and legacy keys (memory.<key>).
func parseMemoryKey(kvKey string) (memType, shortKey string) {
	rest := strings.TrimPrefix(kvKey, memoryKeyPrefix)
	if rest == "" {
		return "general", ""
	}

	// Check if first segment is a known type
	if dotIdx := strings.Index(rest, "."); dotIdx > 0 {
		candidate := rest[:dotIdx]
		if _, ok := validMemoryTypes[candidate]; ok {
			return candidate, rest[dotIdx+1:]
		}
	}

	// Legacy untyped memory
	return "general", rest
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

// bdKvSet calls bd kv set <key> <value>.
func bdKvSet(key, value string) error {
	cmd := exec.Command("bd", "kv", "set", key, value)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// bdKvGet calls bd kv get <key> and returns the value.
func bdKvGet(key string) (string, error) {
	cmd := exec.Command("bd", "kv", "get", key)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// bdKvClear calls bd kv clear <key>.
func bdKvClear(key string) error {
	cmd := exec.Command("bd", "kv", "clear", key)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// parseBdKvListJSON parses bd kv list --json output into displayable string values.
func parseBdKvListJSON(data []byte) (map[string]string, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing kv list: %w", err)
	}

	kvs := make(map[string]string, len(raw))
	for k, v := range raw {
		var s *string
		if err := json.Unmarshal(v, &s); err == nil {
			if s != nil {
				kvs[k] = *s
			}
			continue
		}

		if !strings.HasPrefix(k, memoryKeyPrefix) {
			continue
		}

		// Keep non-string memory values visible without promoting bd metadata.
		var compact bytes.Buffer
		if err := json.Compact(&compact, v); err != nil {
			continue
		}
		kvs[k] = compact.String()
	}
	return kvs, nil
}

// bdKvListJSON calls bd kv list --json and returns the parsed string values.
func bdKvListJSON() (map[string]string, error) {
	cmd := exec.Command("bd", "kv", "list", "--json")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	return parseBdKvListJSON(out)
}
