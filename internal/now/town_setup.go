package now

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/constants"
	"github.com/jonbaldie/gastown/internal/deps"
	"github.com/jonbaldie/gastown/internal/doltserver"
	"github.com/jonbaldie/gastown/internal/git"
	"github.com/jonbaldie/gastown/internal/instructions"
	"github.com/jonbaldie/gastown/internal/rig"
	"github.com/jonbaldie/gastown/internal/templates"
	"github.com/jonbaldie/gastown/internal/workspace"
)

func ensureTown(ctx context.Context, townRoot string, hooks Hooks) error {
	ok, err := workspace.IsWorkspace(townRoot)
	if err != nil {
		return fmt.Errorf("checking Town HQ: %w", err)
	}
	if ok {
		return nil
	}

	if err := deps.EnsureBeads(true); err != nil {
		return fmt.Errorf("beads dependency check failed: %w", err)
	}
	if hooks.EnsureDoltReady != nil {
		if err := hooks.EnsureDoltReady(); err != nil {
			return err
		}
	}
	if err := doltserver.EnsureDoltIdentity(); err != nil {
		return fmt.Errorf("dolt identity setup failed (required for beads): %w", err)
	}

	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		return fmt.Errorf("creating town: %w", err)
	}

	townName := filepath.Base(townRoot)
	townPath := filepath.Join(townRoot, "mayor", "town.json")
	if _, err := os.Stat(townPath); os.IsNotExist(err) {
		owner := gitOwnerEmail(ctx)
		townConfig := &config.TownConfig{
			Type:       "town",
			Version:    config.CurrentTownVersion,
			Name:       townName,
			Owner:      owner,
			PublicName: townName,
			CreatedAt:  time.Now(),
		}
		if err := config.SaveTownConfig(townPath, townConfig); err != nil {
			return fmt.Errorf("writing town.json: %w", err)
		}
	}

	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	if _, err := os.Stat(rigsPath); os.IsNotExist(err) {
		rigsConfig := &config.RigsConfig{
			Version: config.CurrentRigsVersion,
			Rigs:    make(map[string]config.RigEntry),
		}
		if err := config.SaveRigsConfig(rigsPath, rigsConfig); err != nil {
			return fmt.Errorf("writing rigs.json: %w", err)
		}
	}

	if _, err := instructions.Provision(townRoot, templates.TownRootAgentsMD(), "# Gas Town"); err != nil {
		return fmt.Errorf("writing town identity files: %w", err)
	}

	deaconDir := filepath.Join(townRoot, "deacon")
	if err := os.MkdirAll(deaconDir, 0755); err != nil {
		return fmt.Errorf("creating deacon directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(deaconDir, "dogs", "boot"), 0755); err != nil {
		return fmt.Errorf("creating boot dog directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "plugins"), 0755); err != nil {
		return fmt.Errorf("creating plugins directory: %w", err)
	}
	if err := config.EnsureDaemonPatrolConfig(townRoot); err != nil {
		return fmt.Errorf("writing daemon config: %w", err)
	}

	return nil
}

func gitOwnerEmail(ctx context.Context) string {
	cmd := exec.CommandContext(ctx, "git", "config", "user.email")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func startDolt(ctx context.Context, townRoot string, hooks Hooks) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := doltserver.StartContext(ctx, townRoot); err != nil {
		if !strings.Contains(err.Error(), "already running") {
			return fmt.Errorf("starting Dolt server: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, _, err := doltserver.InitRig(townRoot, "hq"); err != nil {
		return fmt.Errorf("initializing HQ Dolt database: %w", err)
	}
	if hooks.InitBeads != nil {
		if err := hooks.InitBeads(townRoot); err != nil {
			return fmt.Errorf("initializing town beads: %w", err)
		}
	}
	return nil
}

func chooseDoltPort(townRoot string) (int, error) {
	cfg := doltserver.DefaultConfig(townRoot)
	port := doltserver.DefaultPort
	if configured := config.ResolveConfiguredDoltPort(townRoot); configured > 0 {
		port = configured
	}

	if err := doltserver.CheckPortAvailable(port); err == nil {
		pid, dataDir := doltserver.PortHolder(port)
		if pid > 0 && (dataDir == "" || !SamePath(dataDir, cfg.DataDir)) {
			return nextFreeDoltPort(port, dataDir)
		}
		return port, nil
	}

	_, dataDir := doltserver.PortHolder(port)
	if dataDir != "" && SamePath(dataDir, cfg.DataDir) {
		return port, nil
	}
	return nextFreeDoltPort(port, dataDir)
}

func nextFreeDoltPort(busyPort int, otherDataDir string) (int, error) {
	free := doltserver.FindFreePort(busyPort + 1)
	if free <= 0 {
		if otherDataDir != "" {
			return 0, fmt.Errorf("Dolt port %d belongs to another Town (%s); no free port found", busyPort, otherDataDir)
		}
		return 0, fmt.Errorf("Dolt port %d is in use and no free port was found", busyPort)
	}
	return free, nil
}

func persistDoltPort(townRoot string, port int) error {
	if err := config.EnsureDaemonPatrolConfig(townRoot); err != nil {
		return err
	}
	path := config.DaemonPatrolConfigPath(townRoot)
	data, err := os.ReadFile(path) //nolint:gosec // G304: town path
	if err != nil {
		return fmt.Errorf("reading daemon.json: %w", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parsing daemon.json: %w", err)
	}
	env := map[string]string{}
	if envRaw, ok := raw["env"]; ok {
		if err := json.Unmarshal(envRaw, &env); err != nil {
			return fmt.Errorf("parsing daemon.json env: %w", err)
		}
	}
	env["GT_DOLT_PORT"] = strconv.Itoa(port)
	envBytes, err := json.Marshal(env)
	if err != nil {
		return err
	}
	raw["env"] = envBytes
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0644)
}

func repoIsRegisteredRig(townRoot, repoPath string) (bool, error) {
	rigsPath := constants.MayorRigsPath(townRoot)
	rigsConfig, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		return false, err
	}
	mgr := rig.NewManager(townRoot, rigsConfig, git.NewGit(townRoot))
	_, ok := mgr.FindByLocalRepo(repoPath)
	return ok, nil
}

func ensureRig(ctx context.Context, townRoot, repoPath, nameFlag string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	rigsPath := constants.MayorRigsPath(townRoot)
	rigsConfig, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		return "", fmt.Errorf("loading rigs.json: %w", err)
	}
	mgr := rig.NewManager(townRoot, rigsConfig, git.NewGit(townRoot))

	if name, ok := mgr.FindByLocalRepo(repoPath); ok {
		if nameFlag != "" && nameFlag != name {
			return "", fmt.Errorf("repository already registered as rig %q (got --name %q)", name, nameFlag)
		}
		return name, nil
	}

	name := strings.TrimSpace(nameFlag)
	if name == "" {
		name = SanitizeRigName(filepath.Base(repoPath))
	}
	if name == "" {
		return "", fmt.Errorf("could not derive a rig name; pass --name")
	}
	if mgr.RigExists(name) {
		return "", fmt.Errorf("rig %q already exists; pass --name for this repository", name)
	}

	if _, err := mgr.AddLocalRig(ctx, name, repoPath); err != nil {
		return "", err
	}
	if err := config.AddRigToDaemonPatrols(townRoot, name); err != nil {
		return "", fmt.Errorf("adding rig to daemon patrols: %w", err)
	}
	return name, nil
}
