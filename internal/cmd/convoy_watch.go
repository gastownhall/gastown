package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/spf13/cobra"
)

// convoy watch flags
var (
	convoyWatchNudge bool
	convoyWatchAddr  string
	convoyWatchJSON  bool
)

func init() {
	convoyWatchCmd.Flags().BoolVar(&convoyWatchNudge, "nudge", false, "Subscribe for nudge notification instead of mail")
	convoyWatchCmd.Flags().StringVar(&convoyWatchAddr, "addr", "", "Address to notify (default: caller's identity)")
	convoyWatchCmd.Flags().BoolVar(&convoyWatchJSON, "json", false, "Output as JSON")

	convoyUnwatchCmd.Flags().StringVar(&convoyWatchAddr, "addr", "", "Address to remove (default: caller's identity)")

	convoyCmd.AddCommand(convoyWatchCmd)
	convoyCmd.AddCommand(convoyUnwatchCmd)
}

var convoyWatchCmd = &cobra.Command{
	Use:   "watch <convoy-id>",
	Short: "Subscribe to convoy completion notifications",
	Long: `Subscribe to be notified when a convoy completes (all tracked issues close).

By default, sends a mail notification to the caller's identity when the
convoy lands. Use --nudge for lightweight nudge notifications instead.

The watcher list is stored in the convoy's description fields and processed
by notifyConvoyCompletion when the convoy closes.

Examples:
  gt convoy watch hq-cv-abc                    # Mail notification to caller
  gt convoy watch hq-cv-abc --nudge            # Nudge notification to caller
  gt convoy watch hq-cv-abc --addr gastown/crew/mel  # Mail notification to mel
  gt convoy watch hq-cv-abc --nudge --addr mayor/    # Nudge mayor on completion`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runConvoyWatch,
}

var convoyUnwatchCmd = &cobra.Command{
	Use:   "unwatch <convoy-id>",
	Short: "Unsubscribe from convoy completion notifications",
	Long: `Remove yourself (or a specified address) from a convoy's watcher list.

Removes from both mail and nudge watcher lists.

Examples:
  gt convoy unwatch hq-cv-abc                        # Remove caller from watchers
  gt convoy unwatch hq-cv-abc --addr gastown/crew/mel # Remove mel from watchers`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runConvoyUnwatch,
}

func runConvoyWatch(cmd *cobra.Command, args []string) error {
	convoyID := args[0]

	// Resolve numeric shortcut
	if n, err := strconv.Atoi(convoyID); err == nil && n > 0 {
		townBeads, err := getTownBeadsDir()
		if err != nil {
			return err
		}
		resolved, err := resolveConvoyNumber(townBeads, n)
		if err != nil {
			return err
		}
		convoyID = resolved
	}

	// Determine watcher address
	addr := convoyWatchAddr
	if addr == "" {
		addr = detectSender()
	}
	if addr == "" {
		return fmt.Errorf("could not determine caller identity; use --addr to specify")
	}

	townBeads, err := getTownBeadsDir()
	if err != nil {
		return err
	}

	// Get convoy details
	convoy, err := getConvoyForWatch(townBeads, convoyID)
	if err != nil {
		return err
	}

	// Parse existing convoy fields
	fields := beads.ParseConvoyFields(&beads.Issue{Description: convoy.Description})
	if fields == nil {
		fields = &beads.ConvoyFields{}
	}

	// Add watcher
	var added bool
	var watchType string
	if convoyWatchNudge {
		added = fields.AddNudgeWatcher(addr)
		watchType = "nudge"
	} else {
		added = fields.AddWatcher(addr)
		watchType = "mail"
	}

	if !added {
		if convoyWatchJSON {
			out, _ := json.Marshal(map[string]interface{}{
				"convoy_id":  convoyID,
				"address":    addr,
				"watch_type": watchType,
				"status":     "already_watching",
			})
			fmt.Println(string(out))
		} else {
			fmt.Printf("%s %s is already watching convoy %s (%s)\n", style.Dim.Render("○"), addr, convoyID, watchType)
		}
		return nil
	}

	// Update convoy description with new watcher
	newDesc := beads.SetConvoyFields(&beads.Issue{Description: convoy.Description}, fields)
	if err := updateConvoyDescription(townBeads, convoyID, newDesc); err != nil {
		return fmt.Errorf("updating convoy watchers: %w", err)
	}

	if convoyWatchJSON {
		out, _ := json.Marshal(map[string]interface{}{
			"convoy_id":  convoyID,
			"address":    addr,
			"watch_type": watchType,
			"status":     "subscribed",
		})
		fmt.Println(string(out))
	} else {
		emoji := "📬"
		if convoyWatchNudge {
			emoji = "🔔"
		}
		fmt.Printf("%s %s subscribed to convoy %s (%s notification)\n", emoji, addr, convoyID, watchType)
	}

	return nil
}

func runConvoyUnwatch(cmd *cobra.Command, args []string) error {
	convoyID := args[0]

	// Resolve numeric shortcut
	if n, err := strconv.Atoi(convoyID); err == nil && n > 0 {
		townBeads, err := getTownBeadsDir()
		if err != nil {
			return err
		}
		resolved, err := resolveConvoyNumber(townBeads, n)
		if err != nil {
			return err
		}
		convoyID = resolved
	}

	// Determine watcher address
	addr := convoyWatchAddr
	if addr == "" {
		addr = detectSender()
	}
	if addr == "" {
		return fmt.Errorf("could not determine caller identity; use --addr to specify")
	}

	townBeads, err := getTownBeadsDir()
	if err != nil {
		return err
	}

	// Get convoy details
	convoy, err := getConvoyForWatch(townBeads, convoyID)
	if err != nil {
		return err
	}

	// Parse existing convoy fields
	fields := beads.ParseConvoyFields(&beads.Issue{Description: convoy.Description})
	if fields == nil {
		fmt.Printf("%s %s is not watching convoy %s\n", style.Dim.Render("○"), addr, convoyID)
		return nil
	}

	// Remove from both watcher lists
	removedMail := fields.RemoveWatcher(addr)
	removedNudge := fields.RemoveNudgeWatcher(addr)

	if !removedMail && !removedNudge {
		fmt.Printf("%s %s is not watching convoy %s\n", style.Dim.Render("○"), addr, convoyID)
		return nil
	}

	// Update convoy description
	newDesc := beads.SetConvoyFields(&beads.Issue{Description: convoy.Description}, fields)
	if err := updateConvoyDescription(townBeads, convoyID, newDesc); err != nil {
		return fmt.Errorf("updating convoy watchers: %w", err)
	}

	var types []string
	if removedMail {
		types = append(types, "mail")
	}
	if removedNudge {
		types = append(types, "nudge")
	}
	fmt.Printf("🔕 %s unsubscribed from convoy %s (%s)\n", addr, convoyID, strings.Join(types, "+"))

	return nil
}

// convoyForWatch is a minimal convoy struct for watch operations.
type convoyForWatch struct {
	ID          string
	Title       string
	Status      string
	Type        string
	Description string
}

// getConvoyForWatch fetches and validates a convoy for watch/unwatch operations.
func getConvoyForWatch(townBeads, convoyID string) (*convoyForWatch, error) {
	showCmd := exec.Command("bd", "show", convoyID, "--json")
	showCmd.Dir = townBeads
	var stdout bytes.Buffer
	showCmd.Stdout = &stdout

	if err := showCmd.Run(); err != nil {
		return nil, fmt.Errorf("convoy '%s' not found", convoyID)
	}

	var convoys []struct {
		ID          string   `json:"id"`
		Title       string   `json:"title"`
		Status      string   `json:"status"`
		Type        string   `json:"issue_type"`
		Description string   `json:"description"`
		Labels      []string `json:"labels"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &convoys); err != nil {
		return nil, fmt.Errorf("parsing convoy data: %w", err)
	}

	if len(convoys) == 0 {
		return nil, fmt.Errorf("convoy '%s' not found", convoyID)
	}

	c := convoys[0]
	if !isConvoyIssue(c.Type, c.Labels) {
		return nil, fmt.Errorf("'%s' is not a convoy (type: %s)", convoyID, c.Type)
	}

	return &convoyForWatch{
		ID:          c.ID,
		Title:       c.Title,
		Status:      c.Status,
		Type:        c.Type,
		Description: c.Description,
	}, nil
}

// updateConvoyDescription updates a convoy's description via bd update.
func updateConvoyDescription(townBeads, convoyID, newDesc string) error {
	updateCmd := exec.Command("bd", "update", convoyID, "--description", newDesc)
	updateCmd.Dir = townBeads
	var stderr bytes.Buffer
	updateCmd.Stderr = &stderr

	if err := updateCmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return fmt.Errorf("bd update: %s", errMsg)
		}
		return fmt.Errorf("bd update: %w", err)
	}
	return nil
}
