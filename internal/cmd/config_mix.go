package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

var configMixJSON bool

var configMixCmd = &cobra.Command{
	Use:   "mix [assignment...]",
	Short: "Mix agent types across roles and crew",
	Long: `Assign different agent types in one command.

Each assignment is target=agent. Add :effort on a role to set thinking
effort for that role. Crew workers use the crew:name form.

Examples:
  gt config mix
  gt config mix default=codex mayor=pi crew=codex
  gt config mix mayor=pi:high polecat=codex crew:alice=pi
  gt config mix --json

Changes apply to new sessions. Restart or hand off a running role to
use a changed mix.`,
	Args: cobra.ArbitraryArgs,
	RunE: runConfigMix,
}

func runConfigMix(_ *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("finding town root: %w", err)
	}

	settingsPath := config.TownSettingsPath(townRoot)
	if len(args) > 0 {
		assignments, parseErr := config.ParseMixSpecs(args)
		if parseErr != nil {
			return parseErr
		}
		if err := config.ApplyTownMix(settingsPath, assignments); err != nil {
			return err
		}
	}

	settings, err := config.LoadOrCreateTownSettings(settingsPath)
	if err != nil {
		return fmt.Errorf("loading town settings: %w", err)
	}
	mix := config.DescribeTownMix(settings)
	if configMixJSON {
		report := struct {
			config.TownMix
			Binaries []config.MixBinary `json:"binaries"`
		}{
			TownMix:  mix,
			Binaries: config.DescribeMixBinaries(settings),
		}
		data, marshalErr := json.MarshalIndent(report, "", "  ")
		if marshalErr != nil {
			return fmt.Errorf("encoding mix: %w", marshalErr)
		}
		fmt.Println(string(data))
		return nil
	}

	printTownMix(mix, settings)
	return nil
}

func printTownMix(mix config.TownMix, settings *config.TownSettings) {
	if mix.Mixed {
		fmt.Printf("%s %s\n\n", style.Bold.Render("Mixed town:"), strings.Join(mix.Providers, " + "))
	} else if len(mix.Providers) == 1 {
		fmt.Printf("Town agents: %s\n\n", style.Bold.Render(mix.Providers[0]))
	} else {
		fmt.Printf("Town agents: %s\n\n", style.Bold.Render(mix.DefaultAgent))
	}

	fmt.Printf("Default agent: %s\n\n", style.Bold.Render(mix.DefaultAgent))

	writer := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(writer, "ROLE\tAGENT\tPROVIDER\tEFFORT\tSOURCE\n")
	for _, entry := range mix.Roles {
		effort := entry.Effort
		if effort == "" {
			effort = "runtime default"
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n", entry.Name, entry.Agent, displayProvider(entry.Provider), effort, entry.Source)
	}
	_ = writer.Flush()

	if len(mix.Crew) > 0 {
		fmt.Println()
		crewWriter := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintf(crewWriter, "CREW\tAGENT\tPROVIDER\n")
		for _, entry := range mix.Crew {
			fmt.Fprintf(crewWriter, "%s\t%s\t%s\n", entry.Name, entry.Agent, displayProvider(entry.Provider))
		}
		_ = crewWriter.Flush()
	}

	printMixBinaries(settings)
	fmt.Println()
	fmt.Println("Restart or hand off a running session to apply a changed mix.")
}

func displayProvider(provider string) string {
	if provider == "" {
		return "-"
	}
	return provider
}

func printMixBinaries(settings *config.TownSettings) {
	binaries := config.DescribeMixBinaries(settings)
	if len(binaries) == 0 {
		return
	}
	fmt.Println()
	fmt.Println("BINARIES")
	for _, binary := range binaries {
		status := "ok"
		if !binary.Present {
			status = "missing"
		}
		fmt.Printf("  %-12s %s (%s)\n", binary.Agent, status, binary.Command)
	}
}
