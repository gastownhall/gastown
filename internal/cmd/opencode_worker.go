package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	gtgit "github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/opencodeserver"
	"github.com/steveyegge/gastown/internal/workspace"
)

func init() {
	rootCmd.AddCommand(openCodeWorkerCmd)
}

var openCodeWorkerCmd = &cobra.Command{
	Use:    "opencode-worker [startup-prompt]",
	Short:  "Run an OpenCode HTTP worker",
	Hidden: true,
	Args:   cobra.MaximumNArgs(1),
	RunE:   runOpenCodeWorker,
}

func runOpenCodeWorker(cmd *cobra.Command, args []string) error {
	townRoot := os.Getenv("GT_ROOT")
	if townRoot == "" {
		var err error
		townRoot, err = workspace.FindFromCwdOrError()
		if err != nil {
			return fmt.Errorf("finding Gas Town root: %w", err)
		}
	}
	gasTownSession := os.Getenv("GT_SESSION")
	if gasTownSession == "" {
		return fmt.Errorf("GT_SESSION is required for an OpenCode server worker")
	}
	directory, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("finding OpenCode worker directory: %w", err)
	}

	startupPrompt := ""
	if len(args) == 1 {
		startupPrompt = args[0]
	}
	openCodeCommand := strings.TrimSpace(os.Getenv("GT_OPENCODE_COMMAND"))
	if openCodeCommand == "" {
		openCodeCommand = "opencode"
	}

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return opencodeserver.RunWorker(ctx, opencodeserver.WorkerOptions{
		TownRoot:       townRoot,
		GasTownSession: gasTownSession,
		Directory:      directory,
		Command:        openCodeCommand,
		StartupPrompt:  startupPrompt,
		Agent:          strings.TrimSpace(os.Getenv("GT_OPENCODE_AGENT")),
		Model:          strings.TrimSpace(os.Getenv("GT_OPENCODE_MODEL")),
		Variant:        strings.TrimSpace(os.Getenv("GT_OPENCODE_VARIANT")),
		WorkKey:        resolveOpenCodeWorkKey(directory, os.Getenv("GT_BRANCH")),
	})
}

func resolveOpenCodeWorkKey(directory, configured string) string {
	if configured = strings.TrimSpace(configured); configured != "" && configured != "HEAD" {
		return configured
	}
	git := gtgit.NewGit(directory)
	workKey, err := git.WorkKey()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(workKey)
}
