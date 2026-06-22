package github

import "testing"

func TestRepoFromRemoteURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
		want string
	}{
		{name: "https", url: "https://github.com/octo/repo", want: "octo/repo"},
		{name: "https git suffix", url: "https://github.com/Bella-Giraffety/gastown.git", want: "Bella-Giraffety/gastown"},
		{name: "https credentials", url: "https://token@github.com/octo/repo.git", want: "octo/repo"},
		{name: "ssh scp", url: "git@github.com:octo/repo.git", want: "octo/repo"},
		{name: "ssh url", url: "ssh://git@github.com/octo/repo.git", want: "octo/repo"},
		{name: "ssh url port", url: "ssh://git@github.com:22/octo/repo.git", want: "octo/repo"},
		{name: "git protocol", url: "git://github.com/octo/repo.git", want: "octo/repo"},
		{name: "trailing slash", url: "https://github.com/octo/repo.git/", want: "octo/repo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RepoFromRemoteURL(tt.url)
			if err != nil {
				t.Fatalf("RepoFromRemoteURL() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("RepoFromRemoteURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRepoFromRemoteURLRejectsInvalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
	}{
		{name: "empty", url: ""},
		{name: "disabled", url: "DISABLED"},
		{name: "non github host", url: "https://example.com/github.com/octo/repo"},
		{name: "host suffix", url: "https://github.com.evil/octo/repo"},
		{name: "missing repo", url: "https://github.com/octo"},
		{name: "extra path", url: "https://github.com/octo/repo/pull/1"},
		{name: "query", url: "https://github.com/octo/repo.git?x=1"},
		{name: "fragment", url: "https://github.com/octo/repo.git#readme"},
		{name: "scp extra path", url: "git@github.com:octo/repo/pull"},
		{name: "leading dash owner", url: "https://github.com/-octo/repo"},
		{name: "leading dash repo", url: "https://github.com/octo/-repo"},
		{name: "newline", url: "https://github.com/octo/re\npo"},
		{name: "file URL", url: "file:///tmp/repo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RepoFromRemoteURL(tt.url)
			if err == nil {
				t.Fatalf("RepoFromRemoteURL() = %q, want error", got)
			}
			if got != "" {
				t.Fatalf("RepoFromRemoteURL() repo = %q, want empty", got)
			}
		})
	}
}
