package cmd

import (
	"strings"
	"testing"
)

func TestThirdPartyRigAddWarning(t *testing.T) {
	tests := []struct {
		name    string
		gitURL  string
		pushURL string
		want    bool
	}{
		{name: "public github without push url", gitURL: "https://github.com/kelseyhightower/envconfig", want: true},
		{name: "github with push url", gitURL: "https://github.com/kelseyhightower/envconfig", pushURL: "https://github.com/you/envconfig", want: false},
		{name: "local path", gitURL: "/tmp/repo.git", want: false},
		{name: "file url", gitURL: "file:///tmp/repo.git", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := thirdPartyRigAddWarning(tt.gitURL, tt.pushURL)
			if tt.want && msg == "" {
				t.Fatalf("expected warning for %q", tt.gitURL)
			}
			if !tt.want && msg != "" {
				t.Fatalf("unexpected warning %q", msg)
			}
			if tt.want && (!strings.Contains(msg, "--push-url") || !strings.Contains(msg, "--merge=local")) {
				t.Fatalf("warning missing actionable flags: %q", msg)
			}
		})
	}
}
