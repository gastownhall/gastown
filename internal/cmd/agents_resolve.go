package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
)

var (
	agentsResolveRole  string
	agentsResolveRig   string
	agentsResolveJSON  bool
	agentsResolveQuiet bool
)

var agentsResolveCmd = &cobra.Command{
	Use:   "resolve",
	Short: "Resolve the active agent bead for a role",
	Long: `Resolve the active agent bead for a role.

The resolver searches the current rig database and the town database across
both durable issues and ephemeral wisps. It prefers the current rig's wisp
record, then rig issue, town wisp, and town issue. Closed beads are ignored.`,
	RunE: runAgentsResolve,
}

func init() {
	agentsResolveCmd.Flags().StringVar(&agentsResolveRole, "role", "", "Agent role to resolve (witness, refinery, crew, polecat, mayor, deacon)")
	agentsResolveCmd.Flags().StringVar(&agentsResolveRig, "rig", "", "Rig name for rig-scoped roles")
	agentsResolveCmd.Flags().BoolVar(&agentsResolveJSON, "json", false, "Output match provenance as JSON")
	agentsResolveCmd.Flags().BoolVar(&agentsResolveQuiet, "quiet", false, "Suppress no-match diagnostics")
	agentsCmd.AddCommand(agentsResolveCmd)
}

type agentBeadSource string

const (
	agentSourceRigWisps   agentBeadSource = "rig-wisps"
	agentSourceRigIssues  agentBeadSource = "rig-issues"
	agentSourceTownWisps  agentBeadSource = "town-wisps"
	agentSourceTownIssues agentBeadSource = "town-issues"
)

type agentBeadCandidate struct {
	ID           string
	Source       agentBeadSource
	BeadsDir     string
	Status       string
	Issue        *beads.Issue
	IdentityRank int
}

type agentsResolveResult struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	BeadsDir string `json:"beads_dir"`
	Status   string `json:"status"`
}

func runAgentsResolve(cmd *cobra.Command, _ []string) error {
	role := strings.TrimSpace(agentsResolveRole)
	rig := strings.TrimSpace(agentsResolveRig)
	if role == "" {
		return fmt.Errorf("--role is required")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}
	currentBeadsDir, err := resolveAgentTrackingBeadsDir()
	if err != nil {
		return err
	}

	candidates, err := findAgentBeadCandidates(cwd, currentBeadsDir, rig)
	if err != nil {
		return err
	}

	var matches []agentBeadCandidate
	for _, candidate := range candidates {
		if identityRank, ok := agentBeadMatchRank(candidate.Issue, role, rig); ok {
			candidate.IdentityRank = identityRank
			matches = append(matches, candidate)
		}
	}

	match, err := pickBestAgentBead(matches)
	if err != nil {
		return err
	}
	if match == nil {
		message := fmt.Sprintf("no agent bead found for role %q", role)
		if rig != "" {
			message += fmt.Sprintf(" in rig %q", rig)
		}
		if agentsResolveJSON {
			_ = json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"error": message})
			return NewSilentExit(1)
		}
		if agentsResolveQuiet {
			return NewSilentExit(1)
		}
		return fmt.Errorf("%s", message)
	}
	if agentsResolveJSON {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(agentsResolveResult{
			ID:       match.ID,
			Source:   string(match.Source),
			BeadsDir: match.BeadsDir,
			Status:   match.Status,
		})
	}

	fmt.Fprintln(cmd.OutOrStdout(), match.ID)
	return nil
}

func findAgentBeadCandidates(cwd, currentBeadsDir, requestedRig string) ([]agentBeadCandidate, error) {
	var candidates []agentBeadCandidate
	townRoot := beads.FindTownRoot(cwd)

	// --rig selects the registered rig's ledger, not merely a filter over the
	// caller's current ledger. This lets town-level patrol launchers resolve a
	// Witness or Refinery identity stored in any registered rig.
	rigBeadsDir := currentBeadsDir
	if requestedRig != "" && townRoot != "" {
		if resolved, ok := beads.ResolveRepoAliasBeadsDir(townRoot, requestedRig); ok {
			rigBeadsDir = resolved
		}
	}

	rigCandidates, err := loadAgentBeadsFromDir(rigBeadsDir, agentSourceRigIssues, agentSourceRigWisps)
	if err != nil {
		return nil, err
	}
	candidates = append(candidates, rigCandidates...)

	if townRoot == "" {
		return candidates, nil
	}
	townBeadsDir := beads.ResolveBeadsDir(beads.GetTownBeadsPath(townRoot))
	if townBeadsDir == "" || filepath.Clean(townBeadsDir) == filepath.Clean(rigBeadsDir) {
		return candidates, nil
	}

	townCandidates, err := loadAgentBeadsFromDir(townBeadsDir, agentSourceTownIssues, agentSourceTownWisps)
	if err != nil {
		return nil, err
	}
	candidates = append(candidates, townCandidates...)
	return candidates, nil
}

func loadAgentBeadsFromDir(beadsDir string, issueSource, wispSource agentBeadSource) ([]agentBeadCandidate, error) {
	db := beads.NewWithBeadsDir(filepath.Dir(beadsDir), beadsDir)
	var candidates []agentBeadCandidate

	issues, err := db.ListAgentBeadsFromIssues()
	if err != nil {
		return nil, fmt.Errorf("listing agent issues in %s: %w", beadsDir, err)
	}
	for _, issue := range issues {
		candidates = append(candidates, agentBeadCandidate{
			ID:       issue.ID,
			Source:   issueSource,
			BeadsDir: beadsDir,
			Status:   issue.Status,
			Issue:    issue,
		})
	}

	wisps, err := db.ListAgentBeadsFromWisps()
	if err != nil {
		return nil, fmt.Errorf("listing agent wisps in %s: %w", beadsDir, err)
	}
	for _, wisp := range wisps {
		candidates = append(candidates, agentBeadCandidate{
			ID:       wisp.ID,
			Source:   wispSource,
			BeadsDir: beadsDir,
			Status:   wisp.Status,
			Issue:    wisp,
		})
	}

	return candidates, nil
}

func agentBeadMatches(issue *beads.Issue, role, rig string) bool {
	_, ok := agentBeadMatchRank(issue, role, rig)
	return ok
}

func agentBeadMatchRank(issue *beads.Issue, role, rig string) (int, bool) {
	if issue == nil {
		return 0, false
	}

	fields := beads.ParseAgentFields(issue.Description)
	if fields.RoleType == role {
		if rig == "" || fields.Rig == rig {
			return 0, true
		}
	}

	idRig, idRole, _, ok := beads.ParseAgentBeadID(issue.ID)
	if !ok || idRole != role {
		return 0, false
	}
	if rig == "" {
		return 1, idRig == ""
	}
	return 1, idRig == rig
}

func pickBestAgentBead(candidates []agentBeadCandidate) (*agentBeadCandidate, error) {
	open := candidates[:0]
	for _, candidate := range candidates {
		if strings.EqualFold(candidate.Status, "closed") {
			continue
		}
		open = append(open, candidate)
	}
	if len(open) == 0 {
		return nil, nil
	}

	sort.Slice(open, func(i, j int) bool {
		leftRank := agentBeadSourceRank(open[i].Source)
		rightRank := agentBeadSourceRank(open[j].Source)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		if open[i].IdentityRank != open[j].IdentityRank {
			return open[i].IdentityRank < open[j].IdentityRank
		}
		return open[i].ID < open[j].ID
	})

	bestSourceRank := agentBeadSourceRank(open[0].Source)
	bestIdentityRank := open[0].IdentityRank
	var sameRank []string
	for _, candidate := range open {
		if agentBeadSourceRank(candidate.Source) != bestSourceRank || candidate.IdentityRank != bestIdentityRank {
			break
		}
		sameRank = append(sameRank, candidate.ID)
	}
	if len(sameRank) > 1 {
		return nil, fmt.Errorf("multiple matching agent beads in %s: %s", open[0].Source, strings.Join(sameRank, ", "))
	}

	return &open[0], nil
}

func agentBeadSourceRank(source agentBeadSource) int {
	switch source {
	case agentSourceRigWisps:
		return 0
	case agentSourceRigIssues:
		return 1
	case agentSourceTownWisps:
		return 2
	case agentSourceTownIssues:
		return 3
	default:
		return 99
	}
}
