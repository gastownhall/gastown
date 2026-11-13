package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

// AgentBeadReachableCheck verifies that every agent can reach its own agent bead
// from its own working directory.
//
// agent-beads-exist only asserts that each agent bead exists *somewhere* — it
// queries the town and rig databases directly, so it stays green even when no
// agent can actually read its own bead from its own cwd. That blind spot is how
// hq-5z6 stayed hidden: agent beads carry a rig prefix but are created in the
// town database, while a rig agent's cwd resolves to the rig database, so idle
// counters, backoff windows, and heartbeats silently failed to persist.
//
// gt now falls back to the town database for agent-bead operations (see
// cmd.resolveAgentBeadDir), which makes the reachable set for an agent "the
// database its cwd resolves to, plus town". This check asserts that invariant
// per agent so a future routing regression is caught here instead of in a
// witness log nobody reads.
//
// It reports only beads that exist in some database the town knows about but
// not in one the owning agent reaches — a bead that exists nowhere is missing,
// which agent-beads-exist already reports.
type AgentBeadReachableCheck struct {
	BaseCheck

	// beadIDs lists the bead IDs readable from a beads directory. Overridden
	// in tests so reachability can be exercised without a live bd/Dolt.
	beadIDs func(beadsDir string) map[string]bool
}

// NewAgentBeadReachableCheck creates a new agent bead reachability check.
func NewAgentBeadReachableCheck() *AgentBeadReachableCheck {
	return &AgentBeadReachableCheck{
		BaseCheck: BaseCheck{
			CheckName:        "agent-bead-reachable",
			CheckDescription: "Verify each agent can read its own agent bead from its own directory",
			CheckCategory:    CategoryRig,
		},
	}
}

// agentBeadLocation pairs an agent bead ID with the directory the owning agent
// runs from.
type agentBeadLocation struct {
	id       string
	agentDir string // absolute path to the agent's working directory
}

// Run reports agents whose agent bead is unreachable from their own directory.
func (c *AgentBeadReachableCheck) Run(ctx *CheckContext) *CheckResult {
	townBeadsDir := beads.ResolveBeadsDir(beads.GetTownBeadsPath(ctx.TownRoot))

	routes, err := beads.LoadRoutes(filepath.Join(ctx.TownRoot, ".beads"))
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "Could not load routes.jsonl",
		}
	}

	locations := agentBeadLocations(ctx, routes)
	if len(locations) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No agent directories to check",
		}
	}

	// Cache bead IDs per database — several agents share one resolved database.
	list := c.beadIDs
	if list == nil {
		list = listBeadIDs
	}
	idsByDir := make(map[string]map[string]bool)
	beadIDsIn := func(beadsDir string) map[string]bool {
		if beadsDir == "" {
			return nil
		}
		if ids, ok := idsByDir[beadsDir]; ok {
			return ids
		}
		ids := list(beadsDir)
		idsByDir[beadsDir] = ids
		return ids
	}

	// Resolve every agent's databases up front so the "does this bead exist at
	// all" test can consult the full set, not just the current agent's.
	type resolvedAgent struct {
		loc     agentBeadLocation
		cwdDir  string   // where the agent's own cwd lands
		reaches []string // databases an agent-bead read from that cwd consults
	}
	var agents []resolvedAgent
	for _, loc := range locations {
		cwdDir := resolveBeadsDirFrom(loc.agentDir)
		if cwdDir == "" {
			continue // Agent dir is outside any beads workspace — nothing to assert.
		}
		agents = append(agents, resolvedAgent{
			loc:     loc,
			cwdDir:  cwdDir,
			reaches: agentBeadDirsFrom(loc.agentDir, cwdDir),
		})
	}

	// Every database the town knows about, so "does this bead exist at all" is
	// answered town-wide. This deliberately ignores ctx.RigName: a --rig run
	// narrows which agents are *reported*, not where their beads might be
	// hiding — an alphaprime2 bead misrouted into the gastown database is
	// exactly the finding this check is here to make.
	allDirs := map[string]bool{townBeadsDir: true}
	for _, dir := range knownRigBeadsDirs(ctx.TownRoot, routes) {
		allDirs[dir] = true
	}
	for _, a := range agents {
		for _, dir := range a.reaches {
			allDirs[dir] = true
		}
	}

	// existsSomewhere reports whether any known database holds the bead. A bead
	// that exists nowhere is missing, not misrouted: agent-beads-exist owns that
	// diagnosis, and reporting it here too would double-count every missing bead.
	existsSomewhere := func(id string) bool {
		for dir := range allDirs {
			if beadIDsIn(dir)[id] {
				return true
			}
		}
		return false
	}

	var unreachable []string
	checked := 0
	for _, a := range agents {
		if !existsSomewhere(a.loc.id) {
			continue
		}
		checked++
		reachable := false
		for _, dir := range a.reaches {
			if beadIDsIn(dir)[a.loc.id] {
				reachable = true
				break
			}
		}
		if reachable {
			continue
		}
		rel, relErr := filepath.Rel(ctx.TownRoot, a.loc.agentDir)
		if relErr != nil {
			rel = a.loc.agentDir
		}
		unreachable = append(unreachable, fmt.Sprintf("%s: %s resolves to %s", a.loc.id, rel, a.cwdDir))
	}

	if len(unreachable) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: fmt.Sprintf("All %d agent bead(s) reachable from their agent's directory", checked),
		}
	}

	sort.Strings(unreachable)
	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusError,
		Message: fmt.Sprintf("%d agent bead(s) unreachable from the owning agent's directory", len(unreachable)),
		Details: unreachable,
		FixHint: "Agent beads live in the town database; check the rig's .beads redirect and routes.jsonl for a database that shadows it",
	}
}

// agentBeadLocations enumerates the agent beads expected for on-disk agents,
// paired with the directory each agent runs from. Agents whose directory does
// not exist are skipped: they are not running, so they cannot be broken.
func agentBeadLocations(ctx *CheckContext, routes []beads.Route) []agentBeadLocation {
	var locations []agentBeadLocation

	add := func(id, agentDir string) {
		if id == "" {
			return
		}
		if _, err := os.Stat(agentDir); err != nil {
			return
		}
		locations = append(locations, agentBeadLocation{id: id, agentDir: agentDir})
	}

	// Town-level agents run from the town root.
	if ctx.RigName == "" {
		add(beads.MayorBeadIDTown(), filepath.Join(ctx.TownRoot, "mayor"))
		add(beads.DeaconBeadIDTown(), filepath.Join(ctx.TownRoot, "deacon"))
	}

	for _, r := range routes {
		parts := strings.Split(r.Path, "/")
		if len(parts) == 0 || parts[0] == "." {
			continue
		}
		rigName := parts[0]
		if ctx.RigName != "" && rigName != ctx.RigName {
			continue
		}
		prefix := strings.TrimSuffix(r.Prefix, "-")
		rigDir := filepath.Join(ctx.TownRoot, rigName)

		add(beads.WitnessBeadIDWithPrefix(prefix, rigName), filepath.Join(rigDir, "witness"))
		add(beads.RefineryBeadIDWithPrefix(prefix, rigName), filepath.Join(rigDir, "refinery"))

		for _, worker := range listCrewWorkers(ctx.TownRoot, rigName) {
			add(beads.CrewBeadIDWithPrefix(prefix, rigName, worker), filepath.Join(rigDir, "crew", worker))
		}
		for _, polecat := range listPolecats(ctx.TownRoot, rigName) {
			add(beads.PolecatBeadIDWithPrefix(prefix, rigName, polecat), filepath.Join(rigDir, "polecats", polecat))
		}
	}

	return locations
}

// resolveBeadsDirFrom mirrors how gt resolves a beads database from an agent's
// working directory: walk up to the nearest .beads, then follow redirects.
// Returns "" when no .beads directory is found.
func resolveBeadsDirFrom(startDir string) string {
	dir := startDir
	for {
		if _, err := os.Stat(filepath.Join(dir, ".beads")); err == nil {
			return beads.ResolveBeadsDir(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// knownRigBeadsDirs returns the beads directory of every rig the town routes
// to, whether or not that rig has agent directories on disk.
func knownRigBeadsDirs(townRoot string, routes []beads.Route) []string {
	seen := make(map[string]bool)
	var dirs []string
	for _, r := range routes {
		parts := strings.Split(r.Path, "/")
		if len(parts) == 0 || parts[0] == "." || parts[0] == "" {
			continue
		}
		rigDir := filepath.Join(townRoot, parts[0])
		if _, err := os.Stat(rigDir); err != nil {
			continue
		}
		dir := beads.ResolveBeadsDir(rigDir)
		if dir == "" || seen[dir] {
			continue
		}
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		seen[dir] = true
		dirs = append(dirs, dir)
	}
	return dirs
}

// agentBeadDirsFrom returns the databases an agent-bead read issued from
// agentDir actually consults, in order: the database the agent's cwd resolves
// to, then the database beads.ForAgentBead re-roots to (the town database that
// holds agent beads). Deriving the second from ForAgentBead rather than
// recomputing "the town .beads" keeps this check honest — if the runtime's
// town-root discovery regresses, the check regresses with it and reports the
// agent beads as unreachable instead of silently agreeing with itself.
func agentBeadDirsFrom(agentDir, cwdDir string) []string {
	dirs := []string{cwdDir}
	agentBeadDir := beads.New(agentDir).ForAgentBead().ResolvedBeadsDir()
	if agentBeadDir == "" || filepath.Clean(agentBeadDir) == filepath.Clean(cwdDir) {
		return dirs
	}
	if _, err := os.Stat(agentBeadDir); err != nil {
		return dirs // Not a real database — querying it would just fail slowly.
	}
	return append(dirs, agentBeadDir)
}

// listBeadIDs returns the set of agent bead IDs readable from beadsDir,
// covering both the issues and wisps tables.
func listBeadIDs(beadsDir string) map[string]bool {
	bd := beads.NewWithBeadsDir(filepath.Dir(beadsDir), beadsDir)
	ids := make(map[string]bool)
	if agents, err := bd.ListAgentBeads(); err == nil {
		for id := range agents {
			ids[id] = true
		}
	}
	if wisps, _ := bd.ListWispIDs(); wisps != nil {
		for id := range wisps {
			ids[id] = true
		}
	}
	return ids
}
