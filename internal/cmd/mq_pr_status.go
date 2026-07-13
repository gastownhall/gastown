package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/git"
)

var (
	mqPRStatusJSON      bool
	mqPRStatusPRURL     string
	mqPRStatusPRNumber  int
	mqPRStatusHeadOwner string
	mqPRStatusRepo      string
)

var mqPRStatusCmd = &cobra.Command{
	Use:   "pr-status <rig> <mr-id-or-branch>",
	Short: "Resolve the GitHub PR state for a merge-request bead",
	Long: `Resolve the GitHub PR state for a merge-request bead.

The lookup prefers recorded PR URL/number metadata, then falls back to an
unambiguous target-repo head lookup. Ambiguous branch matches fail closed so
patrols do not misclassify fork-head or deleted-head PRs.`,
	Args: cobra.ExactArgs(2),
	RunE: runMQPRStatus,
}

func init() {
	mqPRStatusCmd.Flags().BoolVar(&mqPRStatusJSON, "json", false, "Output JSON")
	mqPRStatusCmd.Flags().StringVar(&mqPRStatusPRURL, "pr-url", "", "Recorded GitHub PR URL")
	mqPRStatusCmd.Flags().IntVar(&mqPRStatusPRNumber, "pr-number", 0, "Recorded GitHub PR number")
	mqPRStatusCmd.Flags().StringVar(&mqPRStatusHeadOwner, "head-owner", "", "GitHub owner for qualified head fallback")
	mqPRStatusCmd.Flags().StringVar(&mqPRStatusRepo, "repo", "", "Target GitHub repo owner/name (defaults to upstream or origin)")
	mqCmd.AddCommand(mqPRStatusCmd)
}

type mqPRStatusResult struct {
	Found bool                 `json:"found"`
	PR    *git.PullRequestInfo `json:"pr,omitempty"`
}

func runMQPRStatus(_ *cobra.Command, args []string) error {
	rigName := args[0]
	mrID := args[1]

	mgr, r, _, err := getRefineryManager(rigName)
	if err != nil {
		return err
	}
	mr, err := mgr.FindMR(mrID)
	if err != nil {
		return err
	}
	rigGit, err := getRigGit(r.Path)
	if err != nil {
		return fmt.Errorf("resolve PR status: %w", err)
	}

	prURL := firstNonEmpty(mqPRStatusPRURL, mr.PRURL)
	prNumber := mqPRStatusPRNumber
	if prNumber == 0 {
		prNumber = mr.PRNumber
	}
	if mr.IssueID != "" && (prURL == "" || prNumber == 0) {
		if source, showErr := beads.New(r.BeadsPath()).Show(mr.IssueID); showErr == nil && source != nil {
			if prURL == "" && looksLikeGitHubPRURL(source.ExternalRef) {
				prURL = source.ExternalRef
			}
			if prNumber == 0 {
				prNumber = prNumberFromLabels(source.Labels)
			}
		}
	}

	pr, err := rigGit.LookupPullRequest(git.PullRequestRef{
		URL:        prURL,
		Number:     prNumber,
		Branch:     mr.Branch,
		HeadOwner:  mqPRStatusHeadOwner,
		HeadSHA:    mr.CommitSHA,
		TargetRepo: mqPRStatusRepo,
	})
	if err != nil {
		if errors.Is(err, git.ErrPullRequestNotFound) {
			return printMQPRStatus(mqPRStatusResult{Found: false})
		}
		return err
	}
	return printMQPRStatus(mqPRStatusResult{Found: true, PR: pr})
}

func printMQPRStatus(result mqPRStatusResult) error {
	if mqPRStatusJSON {
		data, err := json.Marshal(result)
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}
	if !result.Found || result.PR == nil {
		fmt.Println("NOT_FOUND")
		return nil
	}
	fmt.Printf("#%d %s %s\n", result.PR.Number, result.PR.State, result.PR.URL)
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func looksLikeGitHubPRURL(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, "https://github.com/") && strings.Contains(value, "/pull/")
}

func prNumberFromLabels(labels []string) int {
	for _, label := range labels {
		n, ok := strings.CutPrefix(strings.TrimSpace(label), "pr:")
		if !ok {
			continue
		}
		prNumber, err := strconv.Atoi(n)
		if err == nil && prNumber > 0 {
			return prNumber
		}
	}
	return 0
}
