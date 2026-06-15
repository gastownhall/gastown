package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func init() {
	showCmd.GroupID = GroupWork
	rootCmd.AddCommand(showCmd)
}

var showCmd = &cobra.Command{
	Use:   "show <bead-id> [flags]",
	Short: "Show details of a bead",
	Long: `Displays the full details of a bead by ID.

Delegates to 'bd show' - all bd show flags are supported.
Works with any bead prefix (gt-, bd-, hq-, etc.) and routes
to the correct beads database automatically.

Examples:
  gt show gt-abc123          # Show a gastown issue
  gt show hq-xyz789          # Show a town-level bead (convoy, mail, etc.)
  gt show bd-def456          # Show a beads issue
  gt show gt-abc123 --json   # Output as JSON
  gt show gt-abc123 -v       # Verbose output`,
	DisableFlagParsing: true, // Pass all flags through to bd show
	RunE:               runShow,
}

func runShow(cmd *cobra.Command, args []string) error {
	// Handle --help since DisableFlagParsing bypasses Cobra's help handling
	if helped, err := checkHelpFlag(cmd, args); helped || err != nil {
		return err
	}

	if len(args) == 0 {
		return fmt.Errorf("bead ID required\n\nUsage: gt show <bead-id> [flags]")
	}

	return execBdShow(args)
}

// extractBeadIDFromArgs returns the first positional bead ID, falling back to
// bd show's --id form for IDs that look like flags. bd show processes positional
// IDs before --id values even when --id appears earlier in argv.
func extractBeadIDFromArgs(args []string) string {
	idFlag := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			if i+1 < len(args) {
				return args[i+1]
			}
			break
		}
		if strings.HasPrefix(arg, "--id=") {
			if idFlag == "" {
				idFlag = strings.TrimPrefix(arg, "--id=")
			}
			continue
		}
		if arg == "--id" {
			if i+1 < len(args) {
				if idFlag == "" {
					idFlag = args[i+1]
				}
				i++
			}
			continue
		}
		if showFlagConsumesNextArg(arg) {
			i++
			continue
		}
		if !strings.HasPrefix(arg, "-") {
			return arg
		}
	}
	return idFlag
}

func showFlagConsumesNextArg(arg string) bool {
	switch arg {
	case "--as-of", "--actor", "--db", "--directory", "--dolt-auto-commit", "--format", "-C":
		return true
	default:
		return false
	}
}

type bdShowInvocation struct {
	Dir         string
	Env         []string
	ExecArgs    []string
	CommandArgs []string
}

func newBdShowInvocation(args []string, environ []string) bdShowInvocation {
	dir := ""
	if beadID := extractBeadIDFromArgs(args); beadID != "" {
		if resolved := resolveBeadDir(beadID); resolved != "" && resolved != "." {
			dir = resolved
		}
	}

	bdc := &bdCmd{
		args:   append([]string{"show"}, args...),
		env:    environ,
		stderr: os.Stderr,
	}
	if dir != "" {
		bdc.Dir(dir)
	}
	cmd := bdc.Build()
	commandArgs := append([]string(nil), cmd.Args[1:]...)

	return bdShowInvocation{
		Dir:         cmd.Dir,
		Env:         cmd.Env,
		ExecArgs:    cmd.Args,
		CommandArgs: commandArgs,
	}
}

func currentBdShowInvocation(args []string) bdShowInvocation {
	return newBdShowInvocation(args, os.Environ())
}
