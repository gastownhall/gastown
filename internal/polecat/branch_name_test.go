package polecat

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

func TestFormatGeneratedBranchName_ActionCompatible(t *testing.T) {
	branch := FormatGeneratedBranchName("alpha", "gt-pin-bd-metadata", "mk123456")
	if strings.Contains(branch, "@") {
		t.Fatalf("FormatGeneratedBranchName() = %q, must not contain @", branch)
	}

	claudeCodeActionBranch := regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9/_.#+,-]*$`)
	if !claudeCodeActionBranch.MatchString(branch) {
		t.Fatalf("FormatGeneratedBranchName() = %q, rejected by claude-code-action branch pattern", branch)
	}
	if err := exec.Command("git", "check-ref-format", "--branch", branch).Run(); err != nil {
		t.Fatalf("FormatGeneratedBranchName() = %q, rejected by git check-ref-format: %v", branch, err)
	}
}

func TestParseBranchName(t *testing.T) {
	tests := []struct {
		name          string
		branch        string
		wantOk        bool
		wantGenerated bool
		wantPolecat   string
		wantIssue     string
	}{
		{
			name:          "generated issue with plus suffix",
			branch:        "polecat/alpha/gt-pin-bd-metadata+mk123456",
			wantOk:        true,
			wantGenerated: true,
			wantPolecat:   "alpha",
			wantIssue:     "gt-pin-bd-metadata",
		},
		{
			name:          "generated dotted subtask with plus suffix",
			branch:        "polecat/alpha/gt-4kp9.5.5.1+mk123456",
			wantOk:        true,
			wantGenerated: true,
			wantPolecat:   "alpha",
			wantIssue:     "gt-4kp9.5.5.1",
		},
		{
			name:          "legacy generated issue with at suffix",
			branch:        "polecat/alpha/gt-jns7.1@mk123456",
			wantOk:        true,
			wantGenerated: true,
			wantPolecat:   "alpha",
			wantIssue:     "gt-jns7.1",
		},
		{
			name:        "raw dashed issue slug is not truncated",
			branch:      "polecat/alpha/gt-pin-bd-metadata",
			wantOk:      true,
			wantPolecat: "alpha",
			wantIssue:   "gt-pin-bd-metadata",
		},
		{
			name:          "no issue generated branch",
			branch:        "polecat/alpha-mk123456",
			wantOk:        true,
			wantGenerated: true,
			wantPolecat:   "alpha",
		},
		{
			name:          "generated issue with underscore suffix",
			branch:        "polecat/alpha/cap-5gw_mrb05j24",
			wantOk:        true,
			wantGenerated: true,
			wantPolecat:   "alpha",
			wantIssue:     "cap-5gw",
		},
		{
			name:          "generated dashed issue with underscore suffix",
			branch:        "polecat/alpha/gt-pin-bd-metadata_mk123456",
			wantOk:        true,
			wantGenerated: true,
			wantPolecat:   "alpha",
			wantIssue:     "gt-pin-bd-metadata",
		},
		{
			name:          "generated dotted subtask with underscore suffix",
			branch:        "polecat/alpha/gt-4kp9.5.5.1_mk123456",
			wantOk:        true,
			wantGenerated: true,
			wantPolecat:   "alpha",
			wantIssue:     "gt-4kp9.5.5.1",
		},
		{
			name:   "empty generated suffix is invalid",
			branch: "polecat/alpha/gt-abc+",
		},
		{
			name:   "empty underscore suffix is invalid",
			branch: "polecat/alpha/gt-abc_",
		},
		{
			name:   "empty generated issue is invalid",
			branch: "polecat/alpha/+mk123456",
		},
		{
			name:   "empty underscore issue is invalid",
			branch: "polecat/alpha/_mk123456",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseBranchName(tt.branch)
			if ok != tt.wantOk {
				t.Fatalf("ParseBranchName(%q) ok = %v, want %v", tt.branch, ok, tt.wantOk)
			}
			if !ok {
				return
			}
			if got.Generated != tt.wantGenerated {
				t.Errorf("Generated = %v, want %v", got.Generated, tt.wantGenerated)
			}
			if got.Polecat != tt.wantPolecat {
				t.Errorf("Polecat = %q, want %q", got.Polecat, tt.wantPolecat)
			}
			if got.Issue != tt.wantIssue {
				t.Errorf("Issue = %q, want %q", got.Issue, tt.wantIssue)
			}
		})
	}
}

func TestFormatGeneratedBranchNameWithDelimiter(t *testing.T) {
	tests := []struct {
		name      string
		issue     string
		delimiter string
		want      string
	}{
		{name: "underscore delimiter", issue: "cap-5gw", delimiter: "_", want: "polecat/alpha/cap-5gw_mk123456"},
		{name: "plus delimiter", issue: "cap-5gw", delimiter: "+", want: "polecat/alpha/cap-5gw+mk123456"},
		{name: "invalid delimiter falls back to plus", issue: "cap-5gw", delimiter: "!", want: "polecat/alpha/cap-5gw+mk123456"},
		{name: "empty delimiter falls back to plus", issue: "cap-5gw", delimiter: "", want: "polecat/alpha/cap-5gw+mk123456"},
		{name: "multi-char delimiter falls back to plus", issue: "cap-5gw", delimiter: "__", want: "polecat/alpha/cap-5gw+mk123456"},
		{name: "no issue ignores delimiter", issue: "", delimiter: "_", want: "polecat/alpha-mk123456"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatGeneratedBranchNameWithDelimiter("alpha", tt.issue, "mk123456", tt.delimiter)
			if got != tt.want {
				t.Errorf("FormatGeneratedBranchNameWithDelimiter() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUnderscoreBranchRoundTrip(t *testing.T) {
	// The docker-compose-safe delimiter must round-trip: format with "_" and
	// parse back to the same issue (AA-849).
	branch := FormatGeneratedBranchNameWithDelimiter("nux", "cap-5gw", "mrb05j24", "_")
	if branch != "polecat/nux/cap-5gw_mrb05j24" {
		t.Fatalf("format = %q, want polecat/nux/cap-5gw_mrb05j24", branch)
	}
	meta, ok := ParseGeneratedBranchName(branch)
	if !ok {
		t.Fatalf("ParseGeneratedBranchName(%q) not ok", branch)
	}
	if meta.Polecat != "nux" || meta.Issue != "cap-5gw" {
		t.Fatalf("round-trip = %+v, want polecat nux issue cap-5gw", meta)
	}
	if err := exec.Command("git", "check-ref-format", "--branch", branch).Run(); err != nil {
		t.Fatalf("branch %q rejected by git check-ref-format: %v", branch, err)
	}
}

func TestValidBranchDelimiter(t *testing.T) {
	for _, valid := range []string{"+", "@", "_"} {
		if !ValidBranchDelimiter(valid) {
			t.Errorf("ValidBranchDelimiter(%q) = false, want true", valid)
		}
	}
	for _, invalid := range []string{"", "-", ".", "/", "!", "__", "+@", "a"} {
		if ValidBranchDelimiter(invalid) {
			t.Errorf("ValidBranchDelimiter(%q) = true, want false", invalid)
		}
	}
}

func TestParseGeneratedBranchNameRejectsRawDashedIssues(t *testing.T) {
	rejects := []string{
		"polecat/alpha/gt-pin-bd-metadata",
		"polecat/alpha/gt-jns7.1-mk123456",
	}
	for _, branch := range rejects {
		if meta, ok := ParseGeneratedBranchName(branch); ok {
			t.Errorf("ParseGeneratedBranchName(%q) = %+v, want ok=false", branch, meta)
		}
	}
}
