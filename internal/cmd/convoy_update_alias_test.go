package cmd

import (
	"strings"
	"testing"
)

// UAT Low #10: Mayor ran
//
//	gt convoy update hq-cv-ilwtm --add-issues ck-7vj ew-1of it-3nn
//
// and got exit 1 because `update` is not a convoy subcommand. The canonical
// command is `gt convoy add`. `update` must resolve to add, and --add-issues
// must be accepted so agents that guess that shape succeed.
func TestConvoyUpdateIsAliasOfAdd(t *testing.T) {
	if !convoyAddCmd.HasAlias("update") {
		t.Fatal(`gt convoy update should be an alias of gt convoy add`)
	}

	found, args, err := convoyCmd.Find([]string{"update", "hq-cv-ilwtm", "ck-7vj"})
	if err != nil {
		t.Fatalf("gt convoy update should resolve: %v", err)
	}
	if found.Name() != "add" {
		t.Fatalf("gt convoy update resolved to %q, want add", found.Name())
	}
	if got := strings.Join(args, " "); got != "hq-cv-ilwtm ck-7vj" {
		t.Fatalf("update leftover args = %q, want convoy id + issues", got)
	}
}

func TestConvoyAddAcceptsAddIssuesFlag(t *testing.T) {
	if convoyAddCmd.Flags().Lookup("add-issues") == nil {
		t.Fatal("gt convoy add/update should accept --add-issues")
	}
}

func TestConvoyUpdateMayorArgvParses(t *testing.T) {
	t.Cleanup(func() { convoyAddIssues = nil })

	found, args, err := convoyCmd.Find([]string{"update", "hq-cv-ilwtm", "--add-issues", "ck-7vj", "ew-1of", "it-3nn"})
	if err != nil {
		t.Fatalf("Mayor argv should resolve: %v", err)
	}
	if found.Name() != "add" {
		t.Fatalf("resolved %q, want add", found.Name())
	}
	if err := found.ParseFlags(args); err != nil {
		t.Fatalf("Mayor argv should parse: %v", err)
	}
	convoyID, issues, err := collectConvoyAddIssues(found.Flags().Args())
	if err != nil {
		t.Fatalf("collect after parse: %v", err)
	}
	if convoyID != "hq-cv-ilwtm" {
		t.Fatalf("convoy ID = %q, want hq-cv-ilwtm", convoyID)
	}
	got := strings.Join(issues, " ")
	if !strings.Contains(got, "ck-7vj") || !strings.Contains(got, "ew-1of") || !strings.Contains(got, "it-3nn") {
		t.Fatalf("parsed issues %q missing Mayor IDs", got)
	}
}

func TestCollectConvoyAddIssues_MayorInvocation(t *testing.T) {
	t.Cleanup(func() { convoyAddIssues = nil })

	// Cobra StringSlice consumes the first --add-issues token; leftover
	// space-separated IDs remain positional. Merge both sources.
	convoyAddIssues = []string{"ck-7vj"}
	convoyID, issues, err := collectConvoyAddIssues([]string{"hq-cv-ilwtm", "ew-1of", "it-3nn"})
	if err != nil {
		t.Fatalf("collectConvoyAddIssues: %v", err)
	}
	if convoyID != "hq-cv-ilwtm" {
		t.Fatalf("convoy ID = %q, want hq-cv-ilwtm", convoyID)
	}
	want := "hq-cv-ilwtm issues=ew-1of it-3nn ck-7vj"
	got := convoyID + " issues=" + strings.Join(issues, " ")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCollectConvoyAddIssues_FlagOnly(t *testing.T) {
	t.Cleanup(func() { convoyAddIssues = nil })

	convoyAddIssues = []string{"ck-7vj", "ew-1of"}
	convoyID, issues, err := collectConvoyAddIssues([]string{"hq-cv-ilwtm"})
	if err != nil {
		t.Fatalf("flag-only issues should be accepted: %v", err)
	}
	if convoyID != "hq-cv-ilwtm" {
		t.Fatalf("convoy ID = %q, want hq-cv-ilwtm", convoyID)
	}
	if strings.Join(issues, " ") != "ck-7vj ew-1of" {
		t.Fatalf("issues = %v, want ck-7vj ew-1of", issues)
	}
}

func TestCollectConvoyAddIssues_RequiresIssues(t *testing.T) {
	t.Cleanup(func() { convoyAddIssues = nil })

	if _, _, err := collectConvoyAddIssues(nil); err == nil {
		t.Fatal("expected error with no convoy ID")
	}
	if _, _, err := collectConvoyAddIssues([]string{"hq-cv-ilwtm"}); err == nil {
		t.Fatal("expected error with convoy ID but no issues")
	}
}

func TestConvoyHelpMentionsAddAndUpdate(t *testing.T) {
	if !strings.Contains(convoyCmd.Long, "add") {
		t.Fatalf("convoy help should mention add:\n%s", convoyCmd.Long)
	}
	if !strings.Contains(convoyCmd.Long, "update") {
		t.Fatalf("convoy help should mention update:\n%s", convoyCmd.Long)
	}
	if !strings.Contains(convoyAddCmd.Long, "update") {
		t.Fatalf("convoy add help should mention the update alias:\n%s", convoyAddCmd.Long)
	}
	if !strings.Contains(convoyAddCmd.Long, "--add-issues") {
		t.Fatalf("convoy add help should mention --add-issues:\n%s", convoyAddCmd.Long)
	}
}
