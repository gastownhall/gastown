package gitlab

import "testing"

func TestIsGitLabRemote(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"https://gitlab.com/group/project.git", true},
		{"git@gitlab.com:group/project.git", true},
		{"ssh://git@gitlab.example.com:2222/group/sub/project.git", true},
		{"https://github.com/owner/repo.git", false},
		{"git@github.com:owner/repo.git", false},
		{"", false},
		{"not a url", false},
	}
	for _, tc := range cases {
		if got := IsGitLabRemote(tc.url); got != tc.want {
			t.Errorf("IsGitLabRemote(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}

func TestParseRemote(t *testing.T) {
	cases := []struct {
		url         string
		wantHost    string
		wantProject string
		wantErr     bool
	}{
		{"https://gitlab.com/group/project.git", "gitlab.com", "group/project", false},
		{"https://gitlab.com/group/sub/project", "gitlab.com", "group/sub/project", false},
		{"git@gitlab.com:group/project.git", "gitlab.com", "group/project", false},
		{"git@gitlab.com:group/sub/project.git", "gitlab.com", "group/sub/project", false},
		{"ssh://git@gitlab.example.com:2222/group/project.git", "gitlab.example.com", "group/project", false},
		{"", "", "", true},
		{"garbage", "", "", true},
	}
	for _, tc := range cases {
		host, project, err := ParseRemote(tc.url)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseRemote(%q) expected error, got host=%q project=%q", tc.url, host, project)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseRemote(%q) unexpected error: %v", tc.url, err)
			continue
		}
		if host != tc.wantHost || project != tc.wantProject {
			t.Errorf("ParseRemote(%q) = (%q, %q), want (%q, %q)", tc.url, host, project, tc.wantHost, tc.wantProject)
		}
	}
}

func TestAPIBaseFromHost(t *testing.T) {
	if got := APIBaseFromHost("gitlab.example.com"); got != "https://gitlab.example.com/api/v4" {
		t.Errorf("APIBaseFromHost = %q, want https://gitlab.example.com/api/v4", got)
	}
}
