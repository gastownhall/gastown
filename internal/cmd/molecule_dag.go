package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/style"
)

// DAGNode represents a node in the dependency graph.
type DAGNode struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	Status       string     `json:"status"`
	Parallel     bool       `json:"parallel,omitempty"`
	Dependencies []string   `json:"dependencies,omitempty"`
	Dependents   []string   `json:"dependents,omitempty"`
	Tier         int        `json:"tier"` // Execution tier (0 = root, higher = later)
	Children     []*DAGNode `json:"children,omitempty"`
}

// DAGInfo contains the full DAG information for a molecule.
type DAGInfo struct {
	RootID       string              `json:"root_id"`
	RootTitle    string              `json:"root_title"`
	RootOnly     bool                `json:"root_only,omitempty"` // Steps come from the attached formula; no per-step state exists.
	Formula      string              `json:"formula,omitempty"`   // Attached formula name, set when RootOnly.
	TotalNodes   int                 `json:"total_nodes"`
	Tiers        int                 `json:"tiers"`
	CriticalPath []string            `json:"critical_path,omitempty"`
	Nodes        map[string]*DAGNode `json:"nodes"`
	TierGroups   [][]string          `json:"tier_groups"` // Nodes grouped by tier
}

var moleculeDagCmd = &cobra.Command{
	Use:   "dag <molecule-id>",
	Short: "Visualize molecule dependency DAG",
	Long: `Display the dependency DAG (Directed Acyclic Graph) for a molecule.

Shows the dependency structure with execution tiers and status:
  ✓ done        - Step completed
  ⧖ in_progress - Step being worked on
  ○ ready       - Step ready to execute (all deps met)
  ◌ blocked     - Step waiting on dependencies

Examples:
  gt mol dag gs-wisp-abc     # Show DAG for molecule
  gt mol dag gs-wisp-abc --json  # JSON output
  gt mol dag gs-wisp-abc --tree  # Tree view (default)
  gt mol dag gs-wisp-abc --tiers # Group by execution tier`,
	Args: cobra.ExactArgs(1),
	RunE: runMoleculeDag,
}

var (
	dagShowTiers bool
	dagTreeView  bool
)

func init() {
	moleculeDagCmd.Flags().BoolVar(&dagShowTiers, "tiers", false, "Group output by execution tier")
	moleculeDagCmd.Flags().BoolVar(&dagTreeView, "tree", true, "Show tree view (default)")
	moleculeDagCmd.Flags().BoolVar(&moleculeJSON, "json", false, "Output as JSON")
}

func runMoleculeDag(cmd *cobra.Command, args []string) error {
	rootID := args[0]

	workDir, err := findLocalBeadsDir()
	if err != nil {
		return fmt.Errorf("not in a beads workspace: %w", err)
	}

	b := beads.New(workDir)

	// Get the root issue
	root, err := b.Show(rootID)
	if err != nil {
		return fmt.Errorf("getting root issue: %w", err)
	}

	// Find all children of the root issue
	children, err := b.List(beads.ListOptions{
		Parent:   rootID,
		Status:   "all",
		Priority: -1,
	})
	if err != nil {
		return fmt.Errorf("listing children: %w", err)
	}

	var dag *DAGInfo
	if len(children) == 0 {
		// Root-only wisp: derive the DAG from the attached formula's steps.
		steps, formulaName, err := rootOnlySteps(root)
		if err != nil {
			return fmt.Errorf("%s: %s", rootID, formulaLoadWarning(formulaName, err))
		}
		if formulaName == "" {
			return noStepsError(rootID)
		}
		dag = buildFormulaDAG(root, steps, formulaName)
	} else {
		dag, err = buildDAG(b, root, children)
		if err != nil {
			return fmt.Errorf("building DAG: %w", err)
		}
	}

	// JSON output
	if moleculeJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(dag)
	}

	// Human-readable output
	if dagShowTiers {
		return outputDAGTiers(dag)
	}
	return outputDAGTree(dag)
}

// buildDAG constructs the DAG from molecule children.
func buildDAG(b *beads.Beads, root *beads.Issue, children []*beads.Issue) (*DAGInfo, error) {
	dag := &DAGInfo{
		RootID:    root.ID,
		RootTitle: root.Title,
		Nodes:     make(map[string]*DAGNode),
	}

	// Get IDs for batch fetch
	var stepIDs []string
	for _, child := range children {
		stepIDs = append(stepIDs, child.ID)
	}

	// Fetch full details for all steps
	stepsMap, err := b.ShowMultiple(stepIDs)
	if err != nil {
		return nil, fmt.Errorf("fetching step details: %w", err)
	}

	// Build closed set for status checking
	closedIDs := make(map[string]bool)
	for _, child := range children {
		if child.Status == "closed" {
			closedIDs[child.ID] = true
		}
	}

	// Create nodes
	for _, child := range children {
		step := stepsMap[child.ID]
		if step == nil {
			step = child
		}

		node := &DAGNode{
			ID:     child.ID,
			Title:  child.Title,
			Status: child.Status,
		}

		// Extract dependencies (all blocking types)
		for _, dep := range step.Dependencies {
			if isBlockingDepType(dep.DependencyType) {
				node.Dependencies = append(node.Dependencies, dep.ID)
			}
		}

		// Check if parallel flag is set (from description)
		if strings.Contains(step.Description, "parallel: true") ||
			strings.Contains(step.Description, "parallel=true") {
			node.Parallel = true
		}

		// Compute ready status for open steps
		if child.Status == "open" {
			allDepsClosed := true
			for _, depID := range node.Dependencies {
				if !closedIDs[depID] {
					allDepsClosed = false
					break
				}
			}
			if allDepsClosed {
				node.Status = "ready"
			} else {
				node.Status = "blocked"
			}
		}

		dag.Nodes[child.ID] = node
		dag.TotalNodes++
	}

	// Build dependents (reverse edges)
	for id, node := range dag.Nodes {
		for _, depID := range node.Dependencies {
			if depNode, ok := dag.Nodes[depID]; ok {
				depNode.Dependents = append(depNode.Dependents, id)
			}
		}
	}

	// Compute tiers using topological sort
	computeTiers(dag)

	// Find critical path
	dag.CriticalPath = findCriticalPath(dag)

	return dag, nil
}

// computeTiers assigns execution tiers to each node.
// Tier 0 = nodes with no dependencies, higher tiers depend on lower ones.
func computeTiers(dag *DAGInfo) {
	// Calculate in-degrees
	inDegree := make(map[string]int)
	for id, node := range dag.Nodes {
		inDegree[id] = len(node.Dependencies)
	}

	// Kahn's algorithm for tier assignment
	currentTier := 0
	processed := 0
	tierGroups := [][]string{}

	for processed < dag.TotalNodes {
		// Find all nodes with in-degree 0 (current tier)
		var tierNodes []string
		for id, degree := range inDegree {
			if degree == 0 {
				tierNodes = append(tierNodes, id)
			}
		}

		if len(tierNodes) == 0 {
			// Cycle detected (shouldn't happen with validated molecules)
			break
		}

		// Sort for deterministic output
		sort.Strings(tierNodes)
		tierGroups = append(tierGroups, tierNodes)

		// Assign tier and remove from graph
		for _, id := range tierNodes {
			dag.Nodes[id].Tier = currentTier
			delete(inDegree, id)
			processed++

			// Decrement in-degree of dependents
			for _, depID := range dag.Nodes[id].Dependents {
				if _, ok := inDegree[depID]; ok {
					inDegree[depID]--
				}
			}
		}

		currentTier++
	}

	dag.Tiers = currentTier
	dag.TierGroups = tierGroups
}

// findCriticalPath finds the longest path through the DAG.
func findCriticalPath(dag *DAGInfo) []string {
	// DFS to find longest path
	memo := make(map[string][]string)

	var dfs func(id string) []string
	dfs = func(id string) []string {
		if path, ok := memo[id]; ok {
			return path
		}

		node := dag.Nodes[id]
		if node == nil {
			return nil
		}

		longestSuffix := []string{}
		for _, depID := range node.Dependents {
			suffix := dfs(depID)
			if len(suffix) > len(longestSuffix) {
				longestSuffix = suffix
			}
		}

		path := append([]string{id}, longestSuffix...)
		memo[id] = path
		return path
	}

	// Find longest path starting from tier 0 nodes
	var criticalPath []string
	for _, tierNodes := range dag.TierGroups {
		for _, id := range tierNodes {
			if dag.Nodes[id].Tier == 0 {
				path := dfs(id)
				if len(path) > len(criticalPath) {
					criticalPath = path
				}
			}
		}
		break // Only check tier 0
	}

	return criticalPath
}

// outputDAGTree outputs the DAG as a tree.
func outputDAGTree(dag *DAGInfo) error {
	fmt.Printf("\n%s %s\n", style.Bold.Render("🌳 DAG:"), dag.RootTitle)
	fmt.Printf("   Root: %s\n", dag.RootID)
	if dag.RootOnly {
		fmt.Printf("   Formula: %s (steps read inline)\n", dag.Formula)
	}
	fmt.Printf("   Nodes: %d | Tiers: %d\n", dag.TotalNodes, dag.Tiers)

	if len(dag.CriticalPath) > 0 {
		fmt.Printf("   Critical path: %s\n", strings.Join(dag.CriticalPath, " → "))
	}
	fmt.Println()

	// Build tree structure for display
	// Start with tier 0 nodes (roots)
	if len(dag.TierGroups) > 0 {
		for i, id := range dag.TierGroups[0] {
			isLast := i == len(dag.TierGroups[0])-1
			printNode(dag, id, "", isLast, make(map[string]bool))
		}
	}

	// Legend
	fmt.Println()
	dagLegend(dag)

	return nil
}

// dagStatusIcon renders a DAG node's status as a single glyph. "inline" nodes
// come from a root-only wisp's formula and carry no tracked state, so they get
// a neutral mark rather than the "blocked" one.
func dagStatusIcon(status string) string {
	switch status {
	case "closed":
		return style.Bold.Render("✓")
	case "in_progress":
		return style.Bold.Render("⧖")
	case "ready":
		return style.Bold.Render("○")
	case dagStatusInline:
		return style.Dim.Render("·")
	default:
		return style.Dim.Render("◌")
	}
}

// dagLegend prints the status glyph key for a DAG rendering.
func dagLegend(dag *DAGInfo) {
	if dag.RootOnly {
		fmt.Printf("   %s step declared by the formula (read inline: per-step state is not tracked)\n",
			style.Dim.Render("·"))
		return
	}
	fmt.Printf("   %s done  %s in_progress  %s ready  %s blocked\n",
		style.Bold.Render("✓"), style.Bold.Render("⧖"), style.Bold.Render("○"), style.Dim.Render("◌"))
}

// printNode recursively prints a node and its dependents.
func printNode(dag *DAGInfo, id, prefix string, isLast bool, visited map[string]bool) {
	if visited[id] {
		return // Prevent cycles in display
	}
	visited[id] = true

	node := dag.Nodes[id]
	if node == nil {
		return
	}

	// Connector
	connector := "├─"
	if isLast {
		connector = "└─"
	}

	// Parallel marker
	parallelMark := ""
	if node.Parallel {
		parallelMark = " ∥"
	}

	// Print node
	fmt.Printf("%s%s %s %s%s\n", prefix, connector, dagStatusIcon(node.Status), node.ID, parallelMark)

	// Child prefix
	childPrefix := prefix
	if isLast {
		childPrefix += "   "
	} else {
		childPrefix += "│  "
	}

	// Print dependents (children in the DAG)
	for i, depID := range node.Dependents {
		isLastChild := i == len(node.Dependents)-1
		printNode(dag, depID, childPrefix, isLastChild, visited)
	}
}

// outputDAGTiers outputs the DAG grouped by execution tier.
func outputDAGTiers(dag *DAGInfo) error {
	fmt.Printf("\n%s %s\n", style.Bold.Render("📊 DAG Tiers:"), dag.RootTitle)
	fmt.Printf("   Root: %s\n", dag.RootID)
	if dag.RootOnly {
		fmt.Printf("   Formula: %s (steps read inline)\n", dag.Formula)
	}
	fmt.Printf("   Nodes: %d | Tiers: %d\n", dag.TotalNodes, dag.Tiers)
	fmt.Println()

	for tier, nodes := range dag.TierGroups {
		fmt.Printf("   %s Tier %d", style.Bold.Render("─"), tier)
		if tier == 0 {
			fmt.Printf(" (entry)")
		} else if tier == dag.Tiers-1 {
			fmt.Printf(" (exit)")
		}
		fmt.Println()

		for _, id := range nodes {
			node := dag.Nodes[id]
			if node == nil {
				continue
			}

			icon := dagStatusIcon(node.Status)

			// Parallel marker
			parallelMark := ""
			if node.Parallel {
				parallelMark = " [parallel]"
			}

			// Dependencies
			depStr := ""
			if len(node.Dependencies) > 0 {
				depStr = fmt.Sprintf(" ← %s", strings.Join(node.Dependencies, ", "))
			}

			fmt.Printf("       %s %s%s%s\n", icon, id, parallelMark, depStr)
		}
		fmt.Println()
	}

	// Critical path
	if len(dag.CriticalPath) > 0 {
		fmt.Printf("   %s %s\n", style.Bold.Render("Critical path:"), strings.Join(dag.CriticalPath, " → "))
	}

	// Legend
	fmt.Println()
	dagLegend(dag)

	return nil
}
