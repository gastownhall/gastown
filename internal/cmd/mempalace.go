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
	"github.com/steveyegge/gastown/internal/style"
)

var mempalaceCmd = &cobra.Command{
	Use:     "mempalace",
	Aliases: []string{"mp"},
	Short:   "MemPalace structured memory management",
	Long: `MemPalace integration for Gas Town agents.

MemPalace provides structured, semantic memory across sessions and rigs.
It complements the flat KV store (bd remember/bd memories) with:
  - Semantic search (find memories by meaning, not just keywords)
  - Knowledge graph (entity relationships with temporal validity)
  - Session diaries (AAAK-format session recordings)
  - Wing/room organization (per-rig memory scoping)

The MCP server is configured in all agents' settings.json via gt hooks sync.
Agents access MemPalace tools directly: mempalace_search, mempalace_kg_query, etc.`,
}

var mempalaceStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show MemPalace installation status",
	RunE:  runMemPalaceStatus,
}

var mempalaceRemoteCmd = &cobra.Command{
	Use:   "remote-config",
	Short: "Print MCP config for remote GT instances",
	RunE:  runMemPalaceRemoteConfig,
}

func init() {
	rootCmd.AddCommand(mempalaceCmd)
	mempalaceCmd.AddCommand(mempalaceStatusCmd)
	mempalaceCmd.AddCommand(mempalaceRemoteCmd)
}

func runMemPalaceStatus(cmd *cobra.Command, args []string) error {
	townRoot := os.Getenv("GT_TOWN_ROOT")
	if townRoot == "" {
		townRoot = filepath.Join(os.Getenv("HOME"), "gt")
	}

	pythonBin := filepath.Join(townRoot, ".mempalace-env", "bin", "python")
	palacePath := filepath.Join(os.Getenv("HOME"), ".mempalace", "palace")

	// Check venv
	if _, err := os.Stat(pythonBin); err != nil {
		fmt.Printf("%s MemPalace venv not found at %s\n", style.Error.Render("✖"), pythonBin)
		return nil
	}
	fmt.Printf("%s Venv: %s\n", style.Success.Render("✓"), pythonBin)

	// Check palace
	if _, err := os.Stat(palacePath); err != nil {
		fmt.Printf("%s Palace not found at %s\n", style.Error.Render("✖"), palacePath)
		return nil
	}
	fmt.Printf("%s Palace: %s\n", style.Success.Render("✓"), palacePath)

	// Get version and stats via Python
	script := `
import mempalace
print(f"version:{mempalace.__version__}")
from mempalace.mcp_server import _get_collection
col = _get_collection()
print(f"drawers:{col.count()}")
from mempalace.mcp_server import _kg
if _kg:
    stats = _kg.stats()
    print(f"kg_entities:{stats.get('entities', 0)}")
    print(f"kg_triples:{stats.get('triples', 0)}")
`
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c := exec.CommandContext(ctx, pythonBin, "-c", script)
	var stdout bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = os.Stderr

	if err := c.Run(); err != nil {
		fmt.Printf("%s Could not query MemPalace: %v\n", style.Error.Render("✖"), err)
		return nil
	}

	for _, line := range bytes.Split(stdout.Bytes(), []byte("\n")) {
		s := string(line)
		if len(s) == 0 {
			continue
		}
		parts := bytes.SplitN(line, []byte(":"), 2)
		if len(parts) != 2 {
			continue
		}
		key, val := string(parts[0]), string(parts[1])
		switch key {
		case "version":
			fmt.Printf("%s Version: %s\n", style.Success.Render("✓"), val)
		case "drawers":
			fmt.Printf("%s Drawers: %s\n", style.Success.Render("✓"), val)
		case "kg_entities":
			fmt.Printf("%s KG Entities: %s\n", style.Success.Render("✓"), val)
		case "kg_triples":
			fmt.Printf("%s KG Triples: %s\n", style.Success.Render("✓"), val)
		}
	}

	// Check MCP global config
	claudeJSON := filepath.Join(os.Getenv("HOME"), ".claude.json")
	if data, err := os.ReadFile(claudeJSON); err == nil {
		if bytes.Contains(data, []byte("mempalace")) {
			fmt.Printf("%s Global MCP: configured in ~/.claude.json\n", style.Success.Render("✓"))
		} else {
			fmt.Printf("%s Global MCP: not in ~/.claude.json\n", style.Warning.Render("~"))
		}
	}

	return nil
}

func runMemPalaceRemoteConfig(cmd *cobra.Command, args []string) error {
	fmt.Println(`# Add to remote instance's ~/.claude.json:
{
  "mcpServers": {
    "mempalace": {
      "type": "stdio",
      "command": "ssh",
      "args": ["gt2", "--", "/home/kanaba/gt/.mempalace-env/bin/python", "-m", "mempalace.mcp_server"]
    }
  }
}

# Or use the mempalace-remote proxy script:
# scp gt2:/home/kanaba/gt/.mempalace-env/bin/mempalace-remote ~/.local/bin/
# Then in ~/.claude.json:
{
  "mcpServers": {
    "mempalace": {
      "type": "stdio",
      "command": "mempalace-remote"
    }
  }
}`)
	return nil
}
