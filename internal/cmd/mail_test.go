package cmd

import (
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
)

func TestMailHelpUsesTownRootMessagingConfig(t *testing.T) {
	const want = "<town-root>/config/messaging.json"

	for _, command := range []struct {
		name string
		long string
	}{
		{name: "send", long: mailSendCmd.Long},
		{name: "announces", long: mailAnnouncesCmd.Long},
	} {
		t.Run(command.name, func(t *testing.T) {
			if !strings.Contains(command.long, want) {
				t.Fatalf("help should document %q:\n%s", want, command.long)
			}
			if strings.Contains(command.long, "~/gt/config/messaging.json") {
				t.Fatalf("help should not document the obsolete home-relative path:\n%s", command.long)
			}
		})
	}
}

func TestMailSendHumanFlagIsPermanentOverseerRoute(t *testing.T) {
	flag := mailSendCmd.Flags().Lookup("human")
	if flag == nil {
		t.Fatal("mail send command missing --human flag")
	}
	if !strings.Contains(flag.Usage, "urgent") || !strings.Contains(flag.Usage, "permanent") {
		t.Fatalf("--human help does not describe urgent permanent delivery: %q", flag.Usage)
	}

	oldHuman, oldSelf, oldTo, oldPermanent, oldUrgent := mailSendHuman, mailSendSelf, mailTo, mailPermanent, mailUrgent
	t.Cleanup(func() {
		mailSendHuman, mailSendSelf, mailTo, mailPermanent = oldHuman, oldSelf, oldTo, oldPermanent
		mailUrgent = oldUrgent
	})

	mailSendHuman = true
	mailSendSelf = false
	mailTo = ""
	mailPermanent = false
	mailUrgent = false

	to, handled, err := resolveHumanMailTarget(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !handled || to != "overseer" || !mailPermanent || !mailUrgent {
		t.Fatalf("--human handled=%v target=%q permanent=%v urgent=%v, want true/overseer/true/true",
			handled, to, mailPermanent, mailUrgent)
	}

	if _, _, err := resolveHumanMailTarget([]string{"mayor/"}); err == nil {
		t.Fatal("--human accepted a conflicting positional address")
	}
}

// TestClaimPatternMatching tests claim pattern matching via the beads package.
// This verifies that the pattern matching used for queue eligibility works correctly.
func TestClaimPatternMatching(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		caller  string
		want    bool
	}{
		// Exact matches
		{
			name:    "exact match",
			pattern: "gastown/polecats/capable",
			caller:  "gastown/polecats/capable",
			want:    true,
		},
		{
			name:    "exact match with different name",
			pattern: "gastown/polecats/toast",
			caller:  "gastown/polecats/capable",
			want:    false,
		},

		// Wildcard at end
		{
			name:    "wildcard matches polecat",
			pattern: "gastown/polecats/*",
			caller:  "gastown/polecats/capable",
			want:    true,
		},
		{
			name:    "wildcard matches different polecat",
			pattern: "gastown/polecats/*",
			caller:  "gastown/polecats/toast",
			want:    true,
		},
		{
			name:    "wildcard doesn't match wrong rig",
			pattern: "gastown/polecats/*",
			caller:  "beads/polecats/capable",
			want:    false,
		},
		{
			name:    "wildcard doesn't match nested path",
			pattern: "gastown/polecats/*",
			caller:  "gastown/polecats/sub/capable",
			want:    false,
		},

		// Universal wildcard
		{
			name:    "universal wildcard matches anything",
			pattern: "*",
			caller:  "anything",
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := beads.MatchClaimPattern(tt.pattern, tt.caller)
			if got != tt.want {
				t.Errorf("MatchClaimPattern(%q, %q) = %v, want %v",
					tt.pattern, tt.caller, got, tt.want)
			}
		})
	}
}

// TestQueueMessageReleaseValidation tests the validation logic for the release command.
// This tests that release correctly identifies:
// - Messages not claimed (no claimed-by label)
// - Messages claimed by a different worker
// - Messages without queue labels (non-queue messages)
func TestQueueMessageReleaseValidation(t *testing.T) {
	tests := []struct {
		name        string
		msgInfo     *queueMessageInfo
		caller      string
		wantErr     bool
		errContains string
	}{
		{
			name: "caller matches claimed-by - valid release",
			msgInfo: &queueMessageInfo{
				ID:        "hq-test1",
				Title:     "Test Message",
				ClaimedBy: "gastown/polecats/nux",
				QueueName: "work-requests",
				Status:    "open",
			},
			caller:  "gastown/polecats/nux",
			wantErr: false,
		},
		{
			name: "message not claimed",
			msgInfo: &queueMessageInfo{
				ID:        "hq-test2",
				Title:     "Test Message",
				ClaimedBy: "", // Not claimed
				QueueName: "work-requests",
				Status:    "open",
			},
			caller:      "gastown/polecats/nux",
			wantErr:     true,
			errContains: "not claimed",
		},
		{
			name: "claimed by different worker",
			msgInfo: &queueMessageInfo{
				ID:        "hq-test3",
				Title:     "Test Message",
				ClaimedBy: "gastown/polecats/other",
				QueueName: "work-requests",
				Status:    "open",
			},
			caller:      "gastown/polecats/nux",
			wantErr:     true,
			errContains: "was claimed by",
		},
		{
			name: "not a queue message",
			msgInfo: &queueMessageInfo{
				ID:        "hq-test4",
				Title:     "Test Message",
				ClaimedBy: "gastown/polecats/nux",
				QueueName: "", // No queue label
				Status:    "open",
			},
			caller:      "gastown/polecats/nux",
			wantErr:     true,
			errContains: "not a queue message",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateQueueRelease(tt.msgInfo, tt.caller)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
					return
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error %q should contain %q", err.Error(), tt.errContains)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

// validateQueueRelease checks if a queue message can be released by the caller.
// This mirrors the validation logic in runMailRelease.
func validateQueueRelease(msgInfo *queueMessageInfo, caller string) error {
	// Verify message is a queue message
	if msgInfo.QueueName == "" {
		return fmt.Errorf("message %s is not a queue message (no queue label)", msgInfo.ID)
	}

	// Verify message is claimed
	if msgInfo.ClaimedBy == "" {
		return fmt.Errorf("message %s is not claimed", msgInfo.ID)
	}

	// Verify caller is the one who claimed it
	if msgInfo.ClaimedBy != caller {
		return fmt.Errorf("message %s was claimed by %s, not %s", msgInfo.ID, msgInfo.ClaimedBy, caller)
	}

	return nil
}

// TestMailAnnounces tests the announces command functionality.
func TestMailAnnounces(t *testing.T) {
	t.Run("listAnnounceChannels with nil config", func(t *testing.T) {
		// Test with nil announces map
		cfg := &config.MessagingConfig{
			Announces: nil,
		}

		// Reset flag to default
		mailAnnouncesJSON = false

		// This should not panic and should handle nil gracefully
		// We can't easily capture stdout in unit tests, but we can verify no panic
		err := listAnnounceChannels(cfg)
		if err != nil {
			t.Errorf("listAnnounceChannels with nil announces should not error: %v", err)
		}
	})

	t.Run("listAnnounceChannels with empty config", func(t *testing.T) {
		cfg := &config.MessagingConfig{
			Announces: make(map[string]config.AnnounceConfig),
		}

		mailAnnouncesJSON = false
		err := listAnnounceChannels(cfg)
		if err != nil {
			t.Errorf("listAnnounceChannels with empty announces should not error: %v", err)
		}
	})

	t.Run("readAnnounceChannel validates channel exists", func(t *testing.T) {
		cfg := &config.MessagingConfig{
			Announces: map[string]config.AnnounceConfig{
				"alerts": {
					Readers:     []string{"@town"},
					RetainCount: 100,
				},
			},
		}

		// Test with unknown channel
		err := readAnnounceChannel("/tmp", cfg, "nonexistent")
		if err == nil {
			t.Error("readAnnounceChannel should error for unknown channel")
		}
		if !strings.Contains(err.Error(), "unknown announce channel") {
			t.Errorf("error should mention 'unknown announce channel', got: %v", err)
		}
	})

	t.Run("readAnnounceChannel errors on nil announces", func(t *testing.T) {
		cfg := &config.MessagingConfig{
			Announces: nil,
		}

		err := readAnnounceChannel("/tmp", cfg, "alerts")
		if err == nil {
			t.Error("readAnnounceChannel should error for nil announces")
		}
		if !strings.Contains(err.Error(), "no announce channels configured") {
			t.Errorf("error should mention 'no announce channels configured', got: %v", err)
		}
	})
}

// TestAnnounceMessageParsing tests parsing of announce messages from beads output.
func TestAnnounceMessageParsing(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   string
	}{
		{
			name:   "extracts from label",
			labels: []string{"from:mayor/", "announce_channel:alerts"},
			want:   "mayor/",
		},
		{
			name:   "extracts from with rig path",
			labels: []string{"announce_channel:alerts", "from:gastown/witness"},
			want:   "gastown/witness",
		},
		{
			name:   "no from label",
			labels: []string{"announce_channel:alerts"},
			want:   "",
		},
		{
			name:   "empty labels",
			labels: []string{},
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the label extraction logic from listAnnounceMessages
			var from string
			for _, label := range tt.labels {
				if strings.HasPrefix(label, "from:") {
					from = strings.TrimPrefix(label, "from:")
					break
				}
			}
			if from != tt.want {
				t.Errorf("extracting from label: got %q, want %q", from, tt.want)
			}
		})
	}
}
