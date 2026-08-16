// ABOUTME: Hidden command for shell hook to detect rigs and update cache.
// ABOUTME: Called by shell integration to set GT_TOWN_ROOT and GT_RIG env vars.

package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/jonbaldie/gastown/internal/constants"
	"github.com/jonbaldie/gastown/internal/state"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var rigDetectCache string

var rigDetectCmd = &cobra.Command{
	Use:    "detect [path]",
	Short:  "Detect rig from repository path (internal use)",
	Hidden: true,
	Long: `Detect rig from a repository path and optionally cache the result.

This is an internal command used by shell integration. It checks if the given
path is inside a Gas Town rig and outputs shell variable assignments.

When --cache is specified, the result is written to ~/.cache/gastown/rigs.cache
for fast lookups by the shell hook.

Output format (to stdout):
  export GT_TOWN_ROOT=/path/to/town
  export GT_ROOT=/path/to/town
  export GT_RIG=rigname

Or if not in a rig:
  unset GT_TOWN_ROOT GT_ROOT GT_RIG`,
	Args: cobra.MaximumNArgs(1),
	RunE: runRigDetect,
}

func init() {
	rigCmd.AddCommand(rigDetectCmd)
	rigDetectCmd.Flags().StringVar(&rigDetectCache, "cache", "", "Repository path to cache detection result for")
}

func runRigDetect(cmd *cobra.Command, args []string) error {
	checkPath := "."
	if len(args) > 0 {
		checkPath = args[0]
	}

	absPath, err := filepath.Abs(checkPath)
	if err != nil {
		return outputNotInRig()
	}

	townRoot, err := workspace.Find(absPath)
	if err != nil || townRoot == "" {
		return outputNotInRig()
	}

	rigName := detectRigFromPath(townRoot, absPath)

	if rigName != "" {
		printEnvSet("GT_TOWN_ROOT", townRoot)
		printEnvSet("GT_ROOT", townRoot)
		printEnvSet("GT_RIG", rigName)
	} else {
		printEnvSet("GT_TOWN_ROOT", townRoot)
		printEnvSet("GT_ROOT", townRoot)
		printEnvUnset("GT_RIG")
	}

	if rigDetectCache != "" {
		if err := updateRigCache(rigDetectCache, townRoot, rigName); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not update cache: %v\n", err)
		}
	}

	return nil
}

func detectRigFromPath(townRoot, absPath string) string {
	rel, err := filepath.Rel(townRoot, absPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}

	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) == 0 || parts[0] == "." {
		return ""
	}

	candidateRig := parts[0]

	switch candidateRig {
	case constants.RoleMayor, constants.RoleDeacon, ".beads", ".claude", ".git", "plugins":
		return ""
	}

	rigConfigPath := filepath.Join(townRoot, candidateRig, "config.json")
	if _, err := os.Stat(rigConfigPath); err == nil {
		return candidateRig
	}

	return ""
}

func outputNotInRig() error {
	if runtime.GOOS == "windows" {
		fmt.Println("Remove-Item Env:GT_TOWN_ROOT -ErrorAction SilentlyContinue; Remove-Item Env:GT_ROOT -ErrorAction SilentlyContinue; Remove-Item Env:GT_RIG -ErrorAction SilentlyContinue")
	} else {
		fmt.Println("unset GT_TOWN_ROOT GT_ROOT GT_RIG")
	}
	return nil
}

func updateRigCache(repoRoot, townRoot, rigName string) error {
	cacheDir := state.CacheDir()
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return err
	}

	cachePath := filepath.Join(cacheDir, "rigs.cache")

	existing := make(map[string]string)
	if data, err := os.ReadFile(cachePath); err == nil {
		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			if idx := strings.Index(line, ":"); idx > 0 {
				existing[line[:idx]] = line[idx+1:]
			}
		}
	}

	var value string
	if rigName != "" {
		if runtime.GOOS == "windows" {
			value = fmt.Sprintf("$env:GT_TOWN_ROOT=%q; $env:GT_ROOT=%q; $env:GT_RIG=%q", townRoot, townRoot, rigName)
		} else {
			value = fmt.Sprintf("export GT_TOWN_ROOT=%q; export GT_ROOT=%q; export GT_RIG=%q", townRoot, townRoot, rigName)
		}
	} else if townRoot != "" {
		if runtime.GOOS == "windows" {
			value = fmt.Sprintf("$env:GT_TOWN_ROOT=%q; $env:GT_ROOT=%q; Remove-Item Env:GT_RIG -ErrorAction SilentlyContinue", townRoot, townRoot)
		} else {
			value = fmt.Sprintf("export GT_TOWN_ROOT=%q; export GT_ROOT=%q; unset GT_RIG", townRoot, townRoot)
		}
	} else {
		if runtime.GOOS == "windows" {
			value = "Remove-Item Env:GT_TOWN_ROOT -ErrorAction SilentlyContinue; Remove-Item Env:GT_ROOT -ErrorAction SilentlyContinue; Remove-Item Env:GT_RIG -ErrorAction SilentlyContinue"
		} else {
			value = "unset GT_TOWN_ROOT GT_ROOT GT_RIG"
		}
	}

	existing[repoRoot] = value

	var lines []string
	for k, v := range existing {
		lines = append(lines, k+":"+v)
	}

	return os.WriteFile(cachePath, []byte(strings.Join(lines, "\n")+"\n"), 0644)
}

// printEnvSet prints an OS-appropriate env variable assignment for shell eval.
func printEnvSet(key, value string) {
	if runtime.GOOS == "windows" {
		fmt.Printf("$env:%s=%q\n", key, value)
	} else {
		fmt.Printf("export %s=%q\n", key, value)
	}
}

// printEnvUnset prints an OS-appropriate env variable unset for shell eval.
func printEnvUnset(key string) {
	if runtime.GOOS == "windows" {
		fmt.Printf("Remove-Item Env:%s -ErrorAction SilentlyContinue\n", key)
	} else {
		fmt.Printf("unset %s\n", key)
	}
}
