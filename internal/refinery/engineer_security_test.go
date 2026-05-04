package refinery

import (
	"strings"
	"testing"
)

func TestValidateTestCommand(t *testing.T) {
	tests := []struct {
		name    string
		cmd     string
		wantErr bool
	}{
		{
			name:    "valid command",
			cmd:     "go test ./...",
			wantErr: false,
		},
		{
			name:    "valid command with pipes",
			cmd:     "make test | tee results.log",
			wantErr: false,
		},
		{
			name:    "valid command with env vars",
			cmd:     "CI=true go test -v ./...",
			wantErr: false,
		},
		{
			name:    "empty string",
			cmd:     "",
			wantErr: true,
		},
		{
			name:    "whitespace only",
			cmd:     "   ",
			wantErr: true,
		},
		{
			name:    "tab and newline only",
			cmd:     "\t\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateTestCommand(tt.cmd)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateTestCommand(%q) error = %v, wantErr %v", tt.cmd, err, tt.wantErr)
			}
		})
	}
}

func TestRunTests_EmptyCommand(t *testing.T) {
	// Verify that runTests returns a failure when TestCommand is empty,
	// rather than silently succeeding or executing a blank shell command.
	e := &Engineer{
		config: &MergeQueueConfig{
			TestCommand: "",
		},
	}

	result := e.runTests(nil)
	if result.Success {
		t.Error("expected failure for empty test command, got success")
	}
	if result.Error == "" {
		t.Error("expected error message for empty test command")
	}
}

func TestRunTests_WhitespaceCommand(t *testing.T) {
	e := &Engineer{
		config: &MergeQueueConfig{
			TestCommand: "   ",
		},
	}

	result := e.runTests(nil)
	if result.Success {
		t.Error("expected failure for whitespace-only test command, got success")
	}
}

func TestDefaultMergeQueueConfig_SecretScan(t *testing.T) {
	cfg := DefaultMergeQueueConfig()
	if cfg.SecretScan == nil {
		t.Error("expected SecretScan to be non-nil")
	}
	if !cfg.SecretScan.Enabled {
		t.Error("expected SecretScan.Enabled to be true by default")
	}
	if cfg.SecretScan.Tool != "gitleaks" {
		t.Errorf("expected SecretScan.Tool to be 'gitleaks', got '%s'", cfg.SecretScan.Tool)
	}
	if cfg.SecretScan.Timeout != "30s" {
		t.Errorf("expected SecretScan.Timeout to be '30s', got '%s'", cfg.SecretScan.Timeout)
	}
}

func TestTruncateSecret(t *testing.T) {
	// Test that short secrets are returned unchanged
	short := "abc123"
	result := truncateSecret(short, 50)
	if result != short {
		t.Errorf("expected '%s', got '%s'", short, result)
	}

	// Test that long secrets are truncated
	long := "thisisaverylongsecretkeythatshouldbetruncatedtominimalelementsofdisplay"
	result = truncateSecret(long, 20)
	if len(result) > 20 {
		t.Errorf("result length %d exceeds max 20", len(result))
	}
	if !strings.Contains(result, "[REDACTED]") {
		t.Error("expected [REDACTED] in truncated secret")
	}

	// Test truncation with [REDACTED] marker
	result = truncateSecret(long, 30)
	if len(result) != 30 {
		t.Errorf("expected length 30, got %d", len(result))
	}
	if !strings.Contains(result, "[REDACTED]") {
		t.Error("expected [REDACTED] in truncated secret")
	}
}
