//go:build !windows

package testutil

import (
	"errors"
	"testing"
)

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
