package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/jonbaldie/gastown/internal/beads"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

// runMailAnnounces lists announce channels or reads messages from a channel.
func runMailAnnounces(cmd *cobra.Command, args []string) error {
	// Find workspace
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Load messaging config
	configPath := config.MessagingConfigPath(townRoot)
	cfg, err := config.LoadMessagingConfig(configPath)
	if err != nil {
		return fmt.Errorf("loading messaging config: %w", err)
	}

	// If no channel specified, list all channels
	if len(args) == 0 {
		return listAnnounceChannels(cfg)
	}

	// Read messages from specified channel
	channelName := args[0]
	return readAnnounceChannel(townRoot, cfg, channelName)
}

// listAnnounceChannels lists all announce channels and their configuration.
func listAnnounceChannels(cfg *config.MessagingConfig) error {
	if cfg.Announces == nil || len(cfg.Announces) == 0 {
		if mailAnnouncesJSON {
			fmt.Println("[]")
			return nil
		}
		fmt.Printf("%s No announce channels configured\n", style.Dim.Render("○"))
		return nil
	}

	// JSON output
	if mailAnnouncesJSON {
		type channelInfo struct {
			Name        string   `json:"name"`
			Readers     []string `json:"readers"`
			RetainCount int      `json:"retain_count"`
		}
		var channels []channelInfo
		for name, annCfg := range cfg.Announces {
			channels = append(channels, channelInfo{
				Name:        name,
				Readers:     annCfg.Readers,
				RetainCount: annCfg.RetainCount,
			})
		}
		// Sort by name for consistent output
		sort.Slice(channels, func(i, j int) bool {
			return channels[i].Name < channels[j].Name
		})
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(channels)
	}

	// Human-readable output
	fmt.Printf("%s Announce Channels (%d)\n\n", style.Bold.Render("📢"), len(cfg.Announces))

	// Sort channel names for consistent output
	var names []string
	for name := range cfg.Announces {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		annCfg := cfg.Announces[name]
		retainStr := "unlimited"
		if annCfg.RetainCount > 0 {
			retainStr = fmt.Sprintf("%d messages", annCfg.RetainCount)
		}
		fmt.Printf("  %s %s\n", style.Bold.Render("●"), name)
		fmt.Printf("    Readers: %s\n", strings.Join(annCfg.Readers, ", "))
		fmt.Printf("    Retain: %s\n", style.Dim.Render(retainStr))
	}

	return nil
}

// readAnnounceChannel reads messages from an announce channel.
func readAnnounceChannel(townRoot string, cfg *config.MessagingConfig, channelName string) error {
	// Validate channel exists
	if cfg.Announces == nil {
		return fmt.Errorf("no announce channels configured")
	}
	_, ok := cfg.Announces[channelName]
	if !ok {
		return fmt.Errorf("unknown announce channel: %s", channelName)
	}

	// Query beads for messages with announce_channel=<channel>
	messages, err := listAnnounceMessages(townRoot, channelName)
	if err != nil {
		return fmt.Errorf("listing announce messages: %w", err)
	}

	// JSON output
	if mailAnnouncesJSON {
		// Ensure empty array instead of null for JSON
		if messages == nil {
			messages = []announceMessage{}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(messages)
	}

	// Human-readable output
	fmt.Printf("%s Channel: %s (%d messages)\n\n",
		style.Bold.Render("📢"), channelName, len(messages))

	if len(messages) == 0 {
		fmt.Printf("  %s\n", style.Dim.Render("(no messages)"))
		return nil
	}

	for _, msg := range messages {
		priorityMarker := ""
		if msg.Priority <= 1 {
			priorityMarker = " " + style.Bold.Render("!")
		}

		fmt.Printf("  %s %s%s\n", style.Bold.Render("●"), msg.Title, priorityMarker)
		fmt.Printf("    %s from %s\n",
			style.Dim.Render(msg.ID),
			msg.From)
		fmt.Printf("    %s\n",
			style.Dim.Render(msg.Created.Local().Format("2006-01-02 15:04")))
		if msg.Description != "" {
			// Show first line of description as preview
			lines := strings.SplitN(msg.Description, "\n", 2)
			preview := lines[0]
			if len(preview) > 80 {
				preview = preview[:77] + "..."
			}
			fmt.Printf("    %s\n", style.Dim.Render(preview))
		}
	}

	return nil
}

// announceMessage represents a message in an announce channel.
type announceMessage struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	From        string    `json:"from"`
	Created     time.Time `json:"created"`
	Priority    int       `json:"priority"`
}

// listAnnounceMessages lists messages from an announce channel.
func listAnnounceMessages(townRoot, channelName string) ([]announceMessage, error) {
	beadsDir := filepath.Join(townRoot, ".beads")

	// Query for messages with label announce_channel:<channel>
	// Messages are stored with this label when sent via sendToAnnounce()
	args := []string{"list",
		"--label", "gt:message",
		"--label", "announce_channel:" + channelName,
		"--sort", "-created", // Newest first
		"--limit", "0", // No limit
		"--json",
	}

	cmd := beads.Spawn(args...)
	cmd.Env = append(os.Environ(), "BEADS_DIR="+beadsDir)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return nil, fmt.Errorf("%s", errMsg)
		}
		return nil, err
	}

	// Parse JSON output
	var issues []struct {
		ID          string    `json:"id"`
		Title       string    `json:"title"`
		Description string    `json:"description"`
		Labels      []string  `json:"labels"`
		CreatedAt   time.Time `json:"created_at"`
		Priority    int       `json:"priority"`
	}

	output := strings.TrimSpace(stdout.String())
	if output == "" || output == "[]" {
		return nil, nil
	}

	if err := json.Unmarshal(stdout.Bytes(), &issues); err != nil {
		return nil, fmt.Errorf("parsing bd output: %w", err)
	}

	// Convert to announceMessage, extracting 'from' from labels
	var messages []announceMessage
	for _, issue := range issues {
		msg := announceMessage{
			ID:          issue.ID,
			Title:       issue.Title,
			Description: issue.Description,
			Created:     issue.CreatedAt,
			Priority:    issue.Priority,
		}

		// Extract 'from' from labels (format: "from:address")
		for _, label := range issue.Labels {
			if strings.HasPrefix(label, "from:") {
				msg.From = strings.TrimPrefix(label, "from:")
				break
			}
		}

		messages = append(messages, msg)
	}

	return messages, nil
}
