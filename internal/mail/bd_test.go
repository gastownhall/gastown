package mail

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBdError_Error(t *testing.T) {
	tests := []struct {
		name string
		err  *bdError
		want string
	}{
		{
			name: "stderr present",
			err: &bdError{
				Err:    errors.New("some error"),
				Stderr: "stderr output",
			},
			want: "stderr output",
		},
		{
			name: "no stderr, has error",
			err: &bdError{
				Err:    errors.New("some error"),
				Stderr: "",
			},
			want: "some error",
		},
		{
			name: "no stderr, no error",
			err: &bdError{
				Err:    nil,
				Stderr: "",
			},
			want: "unknown bd error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.err.Error()
			if got != tt.want {
				t.Errorf("bdError.Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBdError_Unwrap(t *testing.T) {
	originalErr := errors.New("original error")
	bdErr := &bdError{
		Err:    originalErr,
		Stderr: "stderr output",
	}

	unwrapped := bdErr.Unwrap()
	if unwrapped != originalErr {
		t.Errorf("bdError.Unwrap() = %v, want %v", unwrapped, originalErr)
	}
}

func TestBdError_UnwrapNil(t *testing.T) {
	bdErr := &bdError{
		Err:    nil,
		Stderr: "",
	}

	unwrapped := bdErr.Unwrap()
	if unwrapped != nil {
		t.Errorf("bdError.Unwrap() with nil Err should return nil, got %v", unwrapped)
	}
}

func TestBdError_ContainsError(t *testing.T) {
	tests := []struct {
		name     string
		err      *bdError
		substr   string
		contains bool
	}{
		{
			name: "substring present",
			err: &bdError{
				Stderr: "error: bead not found",
			},
			substr:   "bead not found",
			contains: true,
		},
		{
			name: "substring not present",
			err: &bdError{
				Stderr: "error: bead not found",
			},
			substr:   "permission denied",
			contains: false,
		},
		{
			name: "empty stderr",
			err: &bdError{
				Stderr: "",
			},
			substr:   "anything",
			contains: false,
		},
		{
			name: "case sensitive",
			err: &bdError{
				Stderr: "Error: Bead Not Found",
			},
			substr:   "bead not found",
			contains: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.err.ContainsError(tt.substr)
			if got != tt.contains {
				t.Errorf("bdError.ContainsError(%q) = %v, want %v", tt.substr, got, tt.contains)
			}
		})
	}
}

func TestBdError_ContainsErrorPartialMatch(t *testing.T) {
	err := &bdError{
		Stderr: "fatal: invalid bead ID format: expected prefix-#id",
	}

	// Test partial matches
	if !err.ContainsError("invalid bead ID") {
		t.Error("Should contain partial substring")
	}
	if !err.ContainsError("fatal:") {
		t.Error("Should contain prefix")
	}
	if !err.ContainsError("expected prefix") {
		t.Error("Should contain suffix")
	}
}

func TestBdError_ContainsErrorSpecialChars(t *testing.T) {
	err := &bdError{
		Stderr: "error: bead 'gt-123' not found (exit 1)",
	}

	if !err.ContainsError("'gt-123'") {
		t.Error("Should handle quotes in substring")
	}
	if !err.ContainsError("(exit 1)") {
		t.Error("Should handle parentheses in substring")
	}
}

func TestBdError_ImplementsErrorInterface(t *testing.T) {
	// Verify bdError implements error interface
	var err error = &bdError{
		Err:    errors.New("test"),
		Stderr: "test stderr",
	}

	_ = err.Error() // Should compile and not panic
}

func TestBdError_WithAllFields(t *testing.T) {
	originalErr := errors.New("original error")
	bdErr := &bdError{
		Err:    originalErr,
		Stderr: "command failed: bead not found",
	}

	// Test Error() returns stderr
	got := bdErr.Error()
	want := "command failed: bead not found"
	if got != want {
		t.Errorf("bdError.Error() = %q, want %q", got, want)
	}

	// Test Unwrap() returns original error
	unwrapped := bdErr.Unwrap()
	if unwrapped != originalErr {
		t.Errorf("bdError.Unwrap() = %v, want %v", unwrapped, originalErr)
	}

	// Test ContainsError works
	if !bdErr.ContainsError("bead not found") {
		t.Error("ContainsError should find substring in stderr")
	}
	if bdErr.ContainsError("not present") {
		t.Error("ContainsError should return false for non-existent substring")
	}
}

func TestBdSubprocessEnv_SuppressesAutoImport(t *testing.T) {
	got := bdSubprocessEnv([]string{"PATH=/usr/bin"}, "/tmp/.beads", true, nil)

	if !envContains(got, "BEADS_NO_AUTO_IMPORT=1") {
		t.Fatalf("expected BEADS_NO_AUTO_IMPORT=1 in env, got %v", got)
	}
	if !envContains(got, "BEADS_DIR=/tmp/.beads") {
		t.Fatalf("expected BEADS_DIR to be passed through, got %v", got)
	}
	if !envContains(got, "PATH=/usr/bin") {
		t.Fatalf("expected base env to be preserved, got %v", got)
	}
}

func TestBdSubprocessEnv_ExtraEnvCannotOverrideCanonicalPolicy(t *testing.T) {
	got := bdSubprocessEnv(nil, "/tmp/.beads", true, []string{"BEADS_NO_AUTO_IMPORT=0"})

	value, ok := envLastValue(got, "BEADS_NO_AUTO_IMPORT")
	if !ok {
		t.Fatalf("expected BEADS_NO_AUTO_IMPORT in env, got %v", got)
	}
	if value != "1" {
		t.Fatalf("expected canonical env to win, got BEADS_NO_AUTO_IMPORT=%s in %v", value, got)
	}
}

func TestBdSubprocessEnv_DoesNotMutateBaseEnv(t *testing.T) {
	base := make([]string, 1, 4)
	base[0] = "PATH=/usr/bin"
	backing := base[:cap(base)]
	backing[1] = "SENTINEL=keep"

	_ = bdSubprocessEnv(base, "/tmp/.beads", true, nil)

	if len(base) != 1 {
		t.Fatalf("baseEnv length changed to %d", len(base))
	}
	if backing[1] != "SENTINEL=keep" {
		t.Fatalf("baseEnv backing array was mutated: got %q", backing[1])
	}
}

func TestBdSubprocessEnv_FiltersStaleBdTargetEnv(t *testing.T) {
	beadsDir := t.TempDir()
	metadata := []byte(`{"dolt_database":"rigdb"}`)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), metadata, 0644); err != nil {
		t.Fatal(err)
	}

	got := bdSubprocessEnv([]string{
		"PATH=/usr/bin",
		"BEADS_DIR=/wrong",
		"BEADS_DB=/wrong.db",
		"BEADS_DOLT_SERVER_DATABASE=wrong",
		"BEADS_DOLT_SERVER_HOST=wrong-host",
		"BEADS_DOLT_SERVER_PORT=9999",
		"BEADS_DOLT_PORT=9999",
	}, beadsDir, true, nil)

	if envContains(got, "BEADS_DIR=/wrong") || envContains(got, "BEADS_DB=/wrong.db") || envContains(got, "BEADS_DOLT_SERVER_DATABASE=wrong") || envContains(got, "BEADS_DOLT_SERVER_HOST=wrong-host") || envContains(got, "BEADS_DOLT_SERVER_PORT=9999") || envContains(got, "BEADS_DOLT_PORT=9999") {
		t.Fatalf("stale bd target env was not filtered: %v", got)
	}
	if !envContains(got, "BEADS_DIR="+beadsDir) {
		t.Fatalf("expected current BEADS_DIR in env, got %v", got)
	}
	if value, ok := envLastValue(got, "BEADS_DOLT_SERVER_DATABASE"); !ok || value != "rigdb" {
		t.Fatalf("database selector = %q present=%v, want rigdb in %v", value, ok, got)
	}
	for _, want := range []string{"BD_READONLY=true", "BD_DOLT_AUTO_COMMIT=off", "BD_EXPORT_AUTO=false", "BD_BACKUP_ENABLED=false", "BD_DOLT_AUTO_PUSH=false", "BD_NO_PUSH=true", "BD_EXPORT_GIT_ADD=false", "BD_NO_GIT_OPS=true"} {
		if !envContains(got, want) {
			t.Fatalf("expected %s in env, got %v", want, got)
		}
	}
}

func TestBdSubprocessEnv_WriteCommandsAreNotReadonly(t *testing.T) {
	got := bdSubprocessEnv([]string{"PATH=/usr/bin", "BD_READONLY=true"}, "/tmp/.beads", false, []string{"BD_READONLY=true"})
	if value, ok := envLastValue(got, "BD_READONLY"); ok {
		t.Fatalf("write command env should not inherit or set BD_READONLY, got %q in %v", value, got)
	}
	for _, want := range []string{"BD_DOLT_AUTO_COMMIT=on", "BD_EXPORT_AUTO=false", "BD_BACKUP_ENABLED=false", "BD_DOLT_AUTO_PUSH=false", "BD_NO_PUSH=true", "BD_EXPORT_GIT_ADD=false", "BD_NO_GIT_OPS=true"} {
		if !envContains(got, want) {
			t.Fatalf("expected %s in env, got %v", want, got)
		}
	}
}

func TestBdSubprocessEnv_ReadonlyCannotBeOverridden(t *testing.T) {
	got := bdSubprocessEnv([]string{"PATH=/usr/bin", "BD_READONLY=false"}, "/tmp/.beads", true, []string{"BD_READONLY=false"})
	if value, ok := envLastValue(got, "BD_READONLY"); !ok || value != "true" {
		t.Fatalf("read command env should force BD_READONLY=true, got %q present=%v in %v", value, ok, got)
	}
}

func TestIsMailBdReadCommand(t *testing.T) {
	tests := []struct {
		args []string
		want bool
	}{
		{[]string{"list", "--json"}, true},
		{[]string{"show", "hq-abc"}, true},
		{[]string{"sql", "--json", "SELECT * FROM wisps"}, true},
		{[]string{"sql", "--json", "WITH x AS (SELECT 1) SELECT * FROM x"}, true},
		{[]string{"mol", "wisp", "list", "--json"}, true},
		{[]string{"message", "thread", "hq-abc", "--json"}, true},
		{[]string{"sql", "UPDATE issues SET status='closed'"}, false},
		{[]string{"mol", "wisp", "create", "mol-test"}, false},
		{[]string{"message", "send", "mayor", "--body", "hi"}, false},
		{[]string{"create", "title"}, false},
		{[]string{"close", "hq-abc"}, false},
		{[]string{"label", "add", "hq-abc", "read"}, false},
	}
	for _, tt := range tests {
		if got := isMailBdReadCommand(tt.args); got != tt.want {
			t.Fatalf("isMailBdReadCommand(%v) = %v, want %v", tt.args, got, tt.want)
		}
	}
}

func TestBdSubprocessEnv_AllowsRoutingWhenBeadsDirEmpty(t *testing.T) {
	got := bdSubprocessEnv([]string{
		"PATH=/usr/bin",
		"GT_DOLT_HOST=127.0.0.2",
		"GT_DOLT_PORT=5507",
		"BEADS_DIR=/wrong",
		"BEADS_DB=/wrong.db",
		"BEADS_DOLT_SERVER_DATABASE=wrong",
		"BEADS_DOLT_SERVER_HOST=wrong-host",
		"BEADS_DOLT_SERVER_PORT=9999",
		"BEADS_DOLT_PORT=9999",
	}, "", true, nil)

	for _, key := range []string{"BEADS_DIR", "BEADS_DB", "BEADS_DOLT_SERVER_DATABASE"} {
		if value, ok := envLastValue(got, key); ok {
			t.Fatalf("expected %s to be absent for routed command, got %q in %v", key, value, got)
		}
	}
	if !envContains(got, "BEADS_NO_AUTO_IMPORT=1") {
		t.Fatalf("expected BEADS_NO_AUTO_IMPORT=1 in env, got %v", got)
	}
	if !envContains(got, "BEADS_DOLT_SERVER_HOST=127.0.0.2") || !envContains(got, "BEADS_DOLT_SERVER_PORT=5507") || !envContains(got, "BEADS_DOLT_PORT=5507") {
		t.Fatalf("expected GT_DOLT host/port fallback for routed command, got %v", got)
	}
}

func envContains(env []string, kv string) bool {
	for _, entry := range env {
		if entry == kv {
			return true
		}
	}
	return false
}

func envLastValue(env []string, key string) (string, bool) {
	prefix := key + "="
	var value string
	found := false
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			value = strings.TrimPrefix(entry, prefix)
			found = true
		}
	}
	return value, found
}
