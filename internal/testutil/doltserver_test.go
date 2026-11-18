//go:build !windows

package testutil

import (
	"errors"
	"os"
	"testing"
)

func TestConfigureDoltTestProcessEnvOverridesProductionRouting(t *testing.T) {
	keys := append([]string{}, doltTargetSelectorEnvVars...)
	keys = append(keys,
		"GT_DOLT_HOST", "GT_DOLT_PORT", "BEADS_DOLT_SERVER_HOST",
		"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT", "BEADS_DOLT_AUTO_START",
		"GT_TEST_EXTERNAL_DOLT",
	)
	t.Cleanup(restoreEnvironment(t, keys))

	for _, key := range doltTargetSelectorEnvVars {
		requireSetenv(t, key, "production-selector")
	}
	requireSetenv(t, "GT_DOLT_HOST", "production.example")
	requireSetenv(t, "GT_DOLT_PORT", "3307")
	requireSetenv(t, "BEADS_DOLT_SERVER_HOST", "127.0.0.1")
	requireSetenv(t, "BEADS_DOLT_SERVER_PORT", "3307")
	requireSetenv(t, "BEADS_DOLT_PORT", "3307")

	configureDoltTestProcessEnv("49152")

	for _, key := range doltTargetSelectorEnvVars {
		if value, ok := os.LookupEnv(key); ok {
			t.Errorf("%s leaked into test environment as %q", key, value)
		}
	}
	for key, want := range map[string]string{
		"GT_DOLT_HOST":           "127.0.0.1",
		"GT_DOLT_PORT":           "49152",
		"BEADS_DOLT_SERVER_HOST": "127.0.0.1",
		"BEADS_DOLT_SERVER_PORT": "49152",
		"BEADS_DOLT_PORT":        "49152",
		"BEADS_DOLT_AUTO_START":  "0",
		"GT_TEST_EXTERNAL_DOLT":  "1",
	} {
		if got := os.Getenv(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func restoreEnvironment(t *testing.T, keys []string) func() {
	t.Helper()
	type value struct {
		value string
		set   bool
	}
	original := make(map[string]value, len(keys))
	for _, key := range keys {
		v, ok := os.LookupEnv(key)
		original[key] = value{value: v, set: ok}
	}
	return func() {
		for key, originalValue := range original {
			if originalValue.set {
				_ = os.Setenv(key, originalValue.value)
			} else {
				_ = os.Unsetenv(key)
			}
		}
	}
}

func requireSetenv(t *testing.T, key, value string) {
	t.Helper()
	if err := os.Setenv(key, value); err != nil {
		t.Fatalf("set %s: %v", key, err)
	}
}

func TestIsDockerUnavailableErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "rootless", err: errors.New("testcontainers docker unavailable: rootless Docker not found"), want: true},
		{name: "daemon", err: errors.New("Cannot connect to the Docker daemon at unix:///var/run/docker.sock"), want: true},
		{name: "ordinary", err: errors.New("pulling image failed"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDockerUnavailableErr(tt.err); got != tt.want {
				t.Fatalf("isDockerUnavailableErr(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
