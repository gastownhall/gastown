package cmd

import (
	"os"
	"reflect"
	"testing"
)

func TestIsProcessRunning_CurrentProcess(t *testing.T) {
	if !isProcessRunning(os.Getpid()) {
		t.Error("current process should be detected as running")
	}
}

func TestIsProcessRunning_InvalidPID(t *testing.T) {
	if isProcessRunning(99999999) {
		t.Error("invalid PID should not be detected as running")
	}
}

func TestIsProcessRunning_MaxPID(t *testing.T) {
	if isProcessRunning(2147483647) {
		t.Error("max PID should not be running")
	}
}

func TestFindOrphanedAgentPIDsFromPSIncludesKiro(t *testing.T) {
	townRoot := "/tmp/gastown-test"
	psOutput := []byte(`  PID COMM ARGS
101 kiro-cli kiro-cli chat --trust-all-tools /tmp/gastown-test
102 kiro kiro chat --trust-all-tools /tmp/gastown-test/rigs/demo
103 KIRO-CLI KIRO-CLI chat --trust-all-tools /tmp/gastown-test
104 node node /tmp/gastown-test/tool.js
105 kiro-cli kiro-cli chat --trust-all-tools /tmp/other-town
106 zsh zsh -lc kiro-cli chat --trust-all-tools /tmp/gastown-test
bad kiro-cli kiro-cli chat --trust-all-tools /tmp/gastown-test
`)

	got := findOrphanedAgentPIDsFromPS(psOutput, townRoot)
	want := []int{101, 102, 103, 104}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("findOrphanedAgentPIDsFromPS() = %v, want %v", got, want)
	}
}

func TestIsShutdownOrphanAgentCommNameIncludesKiro(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"kiro-cli", true},
		{"kiro", true},
		{"codex", true},
		{"node", true},
		{"zsh", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isShutdownOrphanAgentCommName(tt.name); got != tt.want {
				t.Fatalf("isShutdownOrphanAgentCommName(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}
