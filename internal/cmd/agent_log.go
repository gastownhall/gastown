package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jonbaldie/gastown/internal/agentlog"
	"github.com/jonbaldie/gastown/internal/telemetry"
	"github.com/spf13/cobra"
)

var (
	agentLogSession   string
	agentLogWorkDir   string
	agentLogAgentType string
	agentLogSince     string
	agentLogRunID     string
)

var agentLogCmd = &cobra.Command{
	Use:    "agent-log",
	Short:  "Stream agent conversation events to OTLP log endpoint (invoked by session lifecycle)",
	Hidden: true,
	RunE:   runAgentLog,
}

func init() {
	agentLogCmd.Flags().StringVar(&agentLogSession, "session", "", "Gas Town tmux session name (used as log tag)")
	agentLogCmd.Flags().StringVar(&agentLogWorkDir, "work-dir", "", "Agent working directory (used to locate conversation log files)")
	agentLogCmd.Flags().StringVar(&agentLogAgentType, "agent", "claudecode", "Agent type (claudecode, opencode)")
	agentLogCmd.Flags().StringVar(&agentLogSince, "since", "", "Only watch JSONL files modified at or after this RFC3339 timestamp (filters out pre-existing Claude sessions)")
	agentLogCmd.Flags().StringVar(&agentLogRunID, "run-id", "", "GASTA run identifier (GT_RUN); injected into every agent.event for waterfall correlation")
	_ = agentLogCmd.MarkFlagRequired("session")
	_ = agentLogCmd.MarkFlagRequired("work-dir")
	rootCmd.AddCommand(agentLogCmd)
}

func runAgentLog(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	// Inject run ID into context so every RecordAgentEvent call carries run.id.
	// Falls back to GT_RUN env var when --run-id is not provided.
	if agentLogRunID != "" {
		ctx = telemetry.WithRunID(ctx, agentLogRunID)
	} else if envRunID := os.Getenv("GT_RUN"); envRunID != "" {
		ctx = telemetry.WithRunID(ctx, envRunID)
	}

	provider, err := telemetry.Init(ctx, "gastown", "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: telemetry init failed: %v\n", err)
	}
	if provider != nil {
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = provider.Shutdown(shutdownCtx)
		}()
	}

	// Parse --since timestamp. When provided by activateAgentLogging, this is
	// approximately the GT session start time, ensuring we only watch Claude
	// instances spawned by this Gas Town session (not pre-existing user sessions
	// or other Gas Town rigs running in the same work dir).
	var since time.Time
	if agentLogSince != "" {
		since, err = time.Parse(time.RFC3339, agentLogSince)
		if err != nil {
			return fmt.Errorf("parsing --since %q: %w", agentLogSince, err)
		}
	}

	adapter := agentlog.NewAdapter(agentLogAgentType)
	if adapter == nil {
		return fmt.Errorf("unknown agent type %q; supported: claudecode, opencode", agentLogAgentType)
	}

	ch, err := adapter.Watch(ctx, agentLogSession, agentLogWorkDir, since)
	if err != nil {
		return fmt.Errorf("starting watcher: %w", err)
	}

	for ev := range ch {
		if ev.EventType == "usage" {
			telemetry.RecordAgentTokenUsage(ctx, ev.SessionID, ev.NativeSessionID,
				ev.InputTokens, ev.OutputTokens, ev.CacheReadTokens, ev.CacheCreationTokens)
		} else {
			telemetry.RecordAgentEvent(ctx, ev.SessionID, ev.AgentType, ev.EventType, ev.Role, ev.Content, ev.NativeSessionID, ev.Timestamp)
		}
	}
	return nil
}
