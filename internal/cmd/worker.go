package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/worker"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var (
	workerTownFlag     string
	workerJSON         bool
	workerStartSession string
	workerStartBead    string
	workerStartRole    string
	workerStartRig     string
	workerStartAgent   string
	workerStartType    string
	workerDeliverMsg   string
	workerDeliverPri   string
	workerDeliverSrc   string
)

func init() {
	rootCmd.AddCommand(workerCmd)
	workerCmd.AddCommand(workerServeCmd)
	workerCmd.AddCommand(workerStatusCmd)
	workerCmd.AddCommand(workerStartRunCmd)
	workerCmd.AddCommand(workerDeliverCmd)
	workerServeCmd.Flags().StringVar(&workerTownFlag, "town", "", "Town root (default: detect from cwd)")
	workerStatusCmd.Flags().StringVar(&workerTownFlag, "town", "", "Town root (default: detect from cwd)")
	workerStatusCmd.Flags().BoolVar(&workerJSON, "json", false, "Output as JSON")
	workerStartRunCmd.Flags().StringVar(&workerTownFlag, "town", "", "Town root (default: detect from cwd)")
	workerStartRunCmd.Flags().StringVar(&workerStartSession, "session", "", "Session ID")
	workerStartRunCmd.Flags().StringVar(&workerStartBead, "bead", "", "Hook bead ID")
	workerStartRunCmd.Flags().StringVar(&workerStartRole, "role", "", "Role")
	workerStartRunCmd.Flags().StringVar(&workerStartRig, "rig", "", "Rig")
	workerStartRunCmd.Flags().StringVar(&workerStartAgent, "agent", "", "Agent name")
	workerStartRunCmd.Flags().StringVar(&workerStartType, "agent-type", "", "Agent type")
	workerDeliverCmd.Flags().StringVar(&workerTownFlag, "town", "", "Town root (default: detect from cwd)")
	workerDeliverCmd.Flags().StringVarP(&workerDeliverMsg, "message", "m", "", "Prompt text")
	workerDeliverCmd.Flags().StringVar(&workerDeliverPri, "priority", worker.PriorityNormal, "Priority: system, normal, urgent")
	workerDeliverCmd.Flags().StringVar(&workerDeliverSrc, "source", worker.SourceNudge, "Source: nudge, mail, sling, prime")
	workerDeliverCmd.Flags().BoolVar(&workerJSON, "json", false, "Output as JSON")
}

var workerCmd = &cobra.Command{
	Use:   "worker",
	Short: "Town Worker: session protocol and lifecycle",
	Long: `The Worker is the town seam for talking to a session.

Agents connect. The town is the server. Prompts, health, cost, and
authorize go through this module. A session uses the protocol adapter
when the agent is connected. A session uses the tmux adapter when the
agent is not connected.`,
}

var workerServeCmd = &cobra.Command{
	Use:    "serve",
	Short:  "Run the Worker server on the town host",
	Hidden: true,
	RunE:   runWorkerServe,
}

var workerStatusCmd = &cobra.Command{
	Use:   "status [session-or-run]",
	Short: "Show Worker lifecycle state for a session or run",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runWorkerStatus,
}

func resolveWorkerTown() (string, error) {
	if workerTownFlag != "" {
		return workerTownFlag, nil
	}
	root, err := workspace.FindFromCwdOrError()
	if err != nil {
		return "", err
	}
	return root, nil
}

func runWorkerServe(cmd *cobra.Command, args []string) error {
	townRoot, err := resolveWorkerTown()
	if err != nil {
		return err
	}
	w, err := worker.Listen(townRoot, worker.NewTmuxAdapter())
	if err != nil {
		return err
	}
	defer func() { _ = w.Close() }()

	fmt.Fprintf(os.Stderr, "worker listening on %s\n", worker.SocketPath(townRoot))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	return nil
}

func runWorkerStatus(cmd *cobra.Command, args []string) error {
	townRoot, err := resolveWorkerTown()
	if err != nil {
		return err
	}
	w, err := worker.Open(townRoot)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	id := ""
	if len(args) == 1 {
		id = args[0]
	}
	if id == "" {
		ev, err := w.Events(ctx)
		if err != nil {
			return err
		}
		if workerJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(ev)
		}
		if len(ev) == 0 {
			fmt.Println(style.Dim.Render("No worker events."))
			return nil
		}
		for _, e := range ev {
			fmt.Printf("%s  %s  run=%s  bead=%s\n", e.Timestamp.Format(time.RFC3339), e.Type, e.RunID, e.BeadID)
		}
		return nil
	}

	st, err := w.State(ctx, id, id)
	if err != nil {
		return err
	}
	h, herr := w.Health(ctx, id)
	if workerJSON {
		out := map[string]any{"state": st}
		if herr == nil {
			out["health"] = h
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	fmt.Printf("state: %s\n", st)
	if herr == nil && h != nil {
		fmt.Printf("health: %s\n", h.Status)
		if h.ContextUse > 0 {
			fmt.Printf("context_usage: %.2f\n", h.ContextUse)
		}
	}
	return nil
}

var workerStartRunCmd = &cobra.Command{
	Use:    "start-run",
	Short:  "Register a Worker run (internal)",
	Hidden: true,
	RunE:   runWorkerStartRun,
}

func runWorkerStartRun(cmd *cobra.Command, args []string) error {
	if workerStartSession == "" {
		return fmt.Errorf("--session is required")
	}
	townRoot, err := resolveWorkerTown()
	if err != nil {
		return err
	}
	w, err := worker.Open(townRoot)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	run, err := w.StartRun(ctx, worker.StartSpec{
		SessionID: workerStartSession,
		BeadID:    workerStartBead,
		Role:      workerStartRole,
		Rig:       workerStartRig,
		AgentName: workerStartAgent,
		AgentType: workerStartType,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s\n", run.RunID)
	return nil
}

var workerDeliverCmd = &cobra.Command{
	Use:    "deliver <session-or-run>",
	Short:  "Deliver a prompt through Worker (internal)",
	Hidden: true,
	Args:   cobra.ExactArgs(1),
	RunE:   runWorkerDeliver,
}

func runWorkerDeliver(cmd *cobra.Command, args []string) error {
	if workerDeliverMsg == "" {
		return fmt.Errorf("--message is required")
	}
	townRoot, err := resolveWorkerTown()
	if err != nil {
		return err
	}
	w, err := worker.Open(townRoot)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d, err := w.Deliver(ctx, worker.Prompt{
		RunID:    args[0],
		Content:  workerDeliverMsg,
		Priority: workerDeliverPri,
		Source:   workerDeliverSrc,
	})
	if err != nil {
		return err
	}
	if workerJSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(d)
	}
	if d.Queued {
		fmt.Fprintf(cmd.OutOrStdout(), "queued position=%d adapter=%s run=%s\n", d.Position, d.Adapter, d.RunID)
		return nil
	}
	if d.Accepted {
		fmt.Fprintf(cmd.OutOrStdout(), "accepted adapter=%s run=%s\n", d.Adapter, d.RunID)
		return nil
	}
	return fmt.Errorf("deliver neither accepted nor queued")
}
