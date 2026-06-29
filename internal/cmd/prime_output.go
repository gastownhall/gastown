package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/checkpoint"
	"github.com/steveyegge/gastown/internal/cli"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/deacon"
	"github.com/steveyegge/gastown/internal/refinery"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/templates"
	"github.com/steveyegge/gastown/internal/workspace"
)

// outputPrimeContext outputs the role-specific context using templates or fallback.
// Returns the rendered template content (empty string when using fallback path).
func outputPrimeContext(ctx RoleContext) (string, error) {
	// Try to use templates first
	tmpl, err := templates.New()
	if err != nil {
		// Fall back to hardcoded output if templates fail
		outputPrimeContextFallback(ctx)
		return "", nil
	}

	// Map role to template name
	var roleName string
	switch ctx.Role {
	case RoleMayor:
		roleName = constants.RoleMayor
	case RoleDeacon:
		roleName = constants.RoleDeacon
	case RoleWitness:
		roleName = constants.RoleWitness
	case RoleRefinery:
		roleName = constants.RoleRefinery
	case RolePolecat:
		roleName = constants.RolePolecat
	case RoleCrew:
		roleName = constants.RoleCrew
	case RoleBoot:
		roleName = "boot"
	case RoleDog:
		roleName = "dog"
	default:
		// Unknown role - use fallback
		outputPrimeContextFallback(ctx)
		return "", nil
	}

	// Build template data
	// Get town name for session names
	townName, _ := workspace.GetTownName(ctx.TownRoot)

	// Get default branch from rig config (default to "main" if not set)
	defaultBranch := "main"
	if ctx.Rig != "" && ctx.TownRoot != "" {
		rigPath := filepath.Join(ctx.TownRoot, ctx.Rig)
		if rigCfg, err := rig.LoadRigConfig(rigPath); err == nil && rigCfg.DefaultBranch != "" {
			defaultBranch = rigCfg.DefaultBranch
		}
	}

	data := templates.RoleData{
		Role:          roleName,
		RigName:       ctx.Rig,
		TownRoot:      ctx.TownRoot,
		TownName:      townName,
		WorkDir:       ctx.WorkDir,
		DefaultBranch: defaultBranch,
		Polecat:       ctx.Polecat,
		DogName:       ctx.Polecat, // ctx.Polecat holds the dog name for RoleDog
		MayorSession:  session.MayorSessionName(),
		DeaconSession: session.DeaconSessionName(),
	}

	// Render and output
	output, err := tmpl.RenderRole(roleName, data)
	if err != nil {
		return "", fmt.Errorf("rendering template: %w", err)
	}

	fmt.Print(output)
	return output, nil
}

// outputRoleDirectives loads and emits operator-provided role directives.
// These come from the directive file layout (town-level and/or rig-level)
// and override formula defaults where they conflict.
//
// w and explainEnabled are injected so tests can capture output without
// mutating os.Stdout or the primeExplain global (avoiding data races
// under t.Parallel).
func outputRoleDirectives(ctx RoleContext, w io.Writer, explainEnabled bool) {
	role := string(ctx.Role)
	townRoot := ctx.TownRoot
	rigName := ctx.Rig

	townPath := filepath.Join(townRoot, "directives", role+".md")
	rigPath := ""
	if rigName != "" {
		rigPath = filepath.Join(townRoot, rigName, "directives", role+".md")
	}

	explainf := func(format string, args ...any) {
		if explainEnabled {
			fmt.Fprintf(w, "\n[EXPLAIN] "+format+"\n", args...)
		}
	}

	content := config.LoadRoleDirective(role, townRoot, rigName)
	if content == "" {
		explainf("Role directives: no directive files found (checked %s", townPath)
		if rigPath != "" {
			explainf("Role directives: also checked %s", rigPath)
		}
		return
	}

	// Determine source label for the header
	hasTown := false
	hasRig := false
	if data, err := os.ReadFile(townPath); err == nil { //nolint:gosec // G304: path is from trusted config
		if s := strings.TrimSpace(string(data)); s != "" {
			hasTown = true
		}
	}
	if rigPath != "" {
		if data, err := os.ReadFile(rigPath); err == nil { //nolint:gosec // G304: path is from trusted config
			if s := strings.TrimSpace(string(data)); s != "" {
				hasRig = true
			}
		}
	}

	explainf("Role directives: town=%v rig=%v (town=%s, rig=%s)", hasTown, hasRig, townPath, rigPath)

	fmt.Fprintln(w)
	if hasTown && hasRig {
		fmt.Fprintln(w, "## Town & Rig Directives (operator policy — overrides formula where they conflict)")
	} else if hasRig {
		fmt.Fprintln(w, "## Rig Directives (operator policy — overrides formula where they conflict)")
	} else {
		fmt.Fprintln(w, "## Town Directives (operator policy — overrides formula where they conflict)")
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, content)
}

func outputPrimeContextFallback(ctx RoleContext) {
	switch ctx.Role {
	case RoleMayor:
		outputMayorContext(ctx)
	case RoleWitness:
		outputWitnessContext(ctx)
	case RoleRefinery:
		outputRefineryContext(ctx)
	case RolePolecat:
		outputPolecatContext(ctx)
	case RoleCrew:
		outputCrewContext(ctx)
	case RoleBoot:
		outputBootContext(ctx)
	default:
		outputUnknownContext(ctx)
	}
}

func outputMayorContext(ctx RoleContext) {
	fmt.Printf("%s\n\n", style.Bold.Render("# Mayor Context"))
	fmt.Println("You are the **Mayor** - the global coordinator of Gas Town.")
	fmt.Println()
	fmt.Println("## Responsibilities")
	fmt.Println("- Coordinate work across all rigs")
	fmt.Println("- Delegate to Refineries, not directly to polecats")
	fmt.Println("- Monitor overall system health")
	fmt.Println()
	fmt.Println("## Key Commands")
	fmt.Println("- `" + cli.Name() + " mail inbox` - Check your messages")
	fmt.Println("- `" + cli.Name() + " mail read <id>` - Read a specific message")
	fmt.Println("- `" + cli.Name() + " status` - Show overall town status")
	fmt.Println("- `" + cli.Name() + " rig list` - List all rigs")
	fmt.Println("- `bd ready` - Issues ready to work")
	fmt.Println()
	fmt.Println("## Hookable Mail")
	fmt.Println("Mail can be hooked for ad-hoc instructions: `" + cli.Name() + " hook attach <mail-id>`")
	fmt.Println("If mail is on your hook, read and execute its instructions (GUPP applies).")
	fmt.Println()
	fmt.Println("## Lifecycle Nudges (SLOT_OPEN)")
	fmt.Println("When you receive a SLOT_OPEN nudge from the Witness, a polecat has completed")
	fmt.Println("work and its slot is available. **Always verify via CLI before deciding action:**")
	fmt.Println()
	fmt.Println("1. Run `" + cli.Name() + " polecat list` to get ground truth on polecat state")
	fmt.Println("2. Do NOT trust your in-context belief about polecat state — it may be stale")
	fmt.Println("3. If slots are open and beads are queued: `" + cli.Name() + " sling <bead> <rig>`")
	fmt.Println("4. Witness lifecycle events are authoritative — never second-guess them")
	fmt.Println()
	fmt.Println("## Startup")
	fmt.Println("Check for handoff messages with 🤝 HANDOFF in subject - continue predecessor's work.")
	fmt.Println()
	outputCommandQuickReference(ctx)
	fmt.Printf("Town root: %s\n", style.Dim.Render(ctx.TownRoot))
}

func outputWitnessContext(ctx RoleContext) {
	fmt.Printf("%s\n\n", style.Bold.Render("# Witness Context"))
	fmt.Printf("You are the **Witness** for rig: %s\n\n", style.Bold.Render(ctx.Rig))
	fmt.Println("## Responsibilities")
	fmt.Println("- Monitor polecat health via heartbeat")
	fmt.Println("- Spawn replacement agents for stuck polecats")
	fmt.Println("- Report rig status to Mayor")
	fmt.Println()
	fmt.Println("## Key Commands")
	fmt.Println("- `" + cli.Name() + " witness status` - Show witness status")
	fmt.Println("- `" + cli.Name() + " polecat list` - List polecats in this rig")
	fmt.Println()
	fmt.Println("## Hookable Mail")
	fmt.Println("Mail can be hooked for ad-hoc instructions: `" + cli.Name() + " hook attach <mail-id>`")
	fmt.Println("If mail is on your hook, read and execute its instructions (GUPP applies).")
	fmt.Println()
	outputCommandQuickReference(ctx)
	fmt.Printf("Rig: %s\n", style.Dim.Render(ctx.Rig))
}

func outputRefineryContext(ctx RoleContext) {
	fmt.Printf("%s\n\n", style.Bold.Render("# Refinery Context"))
	fmt.Printf("You are the **Refinery** for rig: %s\n\n", style.Bold.Render(ctx.Rig))
	fmt.Println("## Responsibilities")
	fmt.Println("- Process the merge queue for this rig")
	fmt.Println("- Merge polecat work to integration branch")
	fmt.Println("- Resolve merge conflicts")
	fmt.Println("- Land completed swarms to main")
	fmt.Println()
	fmt.Println("## Key Commands")
	fmt.Println("- `" + cli.Name() + " merge queue` - Show pending merges")
	fmt.Println("- `" + cli.Name() + " merge next` - Process next merge")
	fmt.Println()
	fmt.Println("## Hookable Mail")
	fmt.Println("Mail can be hooked for ad-hoc instructions: `" + cli.Name() + " hook attach <mail-id>`")
	fmt.Println("If mail is on your hook, read and execute its instructions (GUPP applies).")
	fmt.Println()
	outputCommandQuickReference(ctx)
	fmt.Printf("Rig: %s\n", style.Dim.Render(ctx.Rig))
}

func outputPolecatContext(ctx RoleContext) {
	fmt.Printf("%s\n\n", style.Bold.Render("# Polecat Context"))
	fmt.Printf("You are polecat **%s** in rig: %s\n\n",
		style.Bold.Render(ctx.Polecat), style.Bold.Render(ctx.Rig))
	fmt.Println("## Startup Protocol")
	fmt.Println("1. Run `" + cli.Name() + " prime` - loads context and checks mail automatically")
	fmt.Println("2. Check inbox - if mail shown, read with `" + cli.Name() + " mail read <id>`")
	fmt.Println("3. Look for '📋 Work Assignment' messages for your task")
	fmt.Println("4. If no mail, check `bd list --status=in_progress` for existing work")
	fmt.Println()
	fmt.Println("## Key Commands")
	fmt.Println("- `" + cli.Name() + " mail inbox` - Check your inbox for work assignments")
	fmt.Println("- `bd show <issue>` - View your assigned issue")
	fmt.Println("- `bd close <issue>` - Mark issue complete")
	fmt.Println("- `" + cli.Name() + " done` - Signal work ready for merge")
	fmt.Println()
	fmt.Println("## Hookable Mail")
	fmt.Println("Mail can be hooked for ad-hoc instructions: `" + cli.Name() + " hook attach <mail-id>`")
	fmt.Println("If mail is on your hook, read and execute its instructions (GUPP applies).")
	fmt.Println()
	outputCommandQuickReference(ctx)
	fmt.Printf("Polecat: %s | Rig: %s\n",
		style.Dim.Render(ctx.Polecat), style.Dim.Render(ctx.Rig))
}

func outputCrewContext(ctx RoleContext) {
	fmt.Printf("%s\n\n", style.Bold.Render("# Crew Worker Context"))
	fmt.Printf("You are crew worker **%s** in rig: %s\n\n",
		style.Bold.Render(ctx.Polecat), style.Bold.Render(ctx.Rig))
	fmt.Println("## About Crew Workers")
	fmt.Println("- Persistent workspace (not auto-garbage-collected)")
	fmt.Println("- User-managed (not Witness-monitored)")
	fmt.Println("- Long-lived identity across sessions")
	fmt.Println()
	fmt.Println("**Identity**: You are the AI agent. The human sending you messages is the")
	fmt.Println("**Overseer** — the only non-agent role in Gas Town. Do not confuse your identity with theirs.")
	fmt.Println()
	fmt.Println("## Key Commands")
	fmt.Println("- `" + cli.Name() + " mail inbox` - Check your inbox")
	fmt.Println("- `bd ready` - Available issues")
	fmt.Println("- `bd show <issue>` - View issue details")
	fmt.Println("- `bd close <issue>` - Mark issue complete")
	fmt.Println()
	fmt.Println("## Hookable Mail")
	fmt.Println("Mail can be hooked for ad-hoc instructions: `" + cli.Name() + " hook attach <mail-id>`")
	fmt.Println("If mail is on your hook, read and execute its instructions (GUPP applies).")
	fmt.Println()
	outputCommandQuickReference(ctx)
	fmt.Printf("Crew: %s | Rig: %s\n",
		style.Dim.Render(ctx.Polecat), style.Dim.Render(ctx.Rig))
}

func outputBootContext(ctx RoleContext) {
	fmt.Printf("%s\n\n", style.Bold.Render("# Boot Watchdog Context"))
	fmt.Println("You are the **Boot Watchdog** - the daemon's entry point for Deacon triage.")
	fmt.Println()
	fmt.Println("## Responsibilities")
	fmt.Println("- Observe Deacon session health")
	fmt.Println("- Decide whether to wake, nudge, or restart the Deacon")
	fmt.Println("- Run triage and exit (ephemeral - fresh each spawn)")
	fmt.Println()
	fmt.Println("## Key Commands")
	fmt.Println("- `" + cli.Name() + " boot triage` - Run triage directly")
	fmt.Println("- `" + cli.Name() + " boot status` - Show Boot status")
	fmt.Println("- `" + cli.Name() + " deacon status` - Check Deacon health")
	fmt.Println()
	outputCommandQuickReference(ctx)
	fmt.Printf("Town root: %s\n", style.Dim.Render(ctx.TownRoot))
}

func outputUnknownContext(ctx RoleContext) {
	fmt.Printf("%s\n\n", style.Bold.Render("# Gas Town Context"))
	fmt.Println("Could not determine specific role from current directory.")
	fmt.Println()
	if ctx.Rig != "" {
		fmt.Printf("You appear to be in rig: %s\n\n", style.Bold.Render(ctx.Rig))
	}
	fmt.Println("Navigate to a specific agent directory:")
	fmt.Println("- `<rig>/polecats/<name>/` - Polecat role")
	fmt.Println("- `<rig>/witness/rig/` - Witness role")
	fmt.Println("- `<rig>/refinery/rig/` - Refinery role")
	fmt.Println("- `mayor/` or `<rig>/mayor/` - Mayor role")
	fmt.Println("- Town root is neutral (set GT_ROLE or cd into a role directory)")
	fmt.Println()
	fmt.Printf("Town root: %s\n", style.Dim.Render(ctx.TownRoot))
}

// outputCommandQuickReference outputs a compact role-aware cheatsheet of commonly
// confused commands. This helps agents avoid guessing wrong commands.
func outputCommandQuickReference(ctx RoleContext) {
	c := cli.Name()
	fmt.Println("## ⚡ Command Quick-Reference")
	fmt.Println()
	fmt.Println("**Commonly confused — use the right command:**")
	fmt.Println()

	switch ctx.Role {
	case RoleMayor:
		fmt.Println("| Want to... | Correct command | Common mistake |")
		fmt.Println("|------------|----------------|----------------|")
		fmt.Println("| Close/complete a bead | `bd close <id>` | ~~bd complete~~ (not a command), ~~bd update --status done~~ (invalid status) |")
		fmt.Printf("| Dispatch work to polecat | `%s sling <bead> <rig>` | ~~gt polecat spawn~~ (not a command) |\n", c)
		fmt.Printf("| Message another agent | `%s nudge <target> \"msg\"` | ~~tmux send-keys~~ (unreliable) |\n", c)
		fmt.Printf("| Kill stuck polecat | `%s polecat nuke <rig>/<name> --force` | ~~gt polecat kill~~ (not a command) |\n", c)
		fmt.Printf("| Pause rig (daemon won't restart) | `%s rig park <rig>` | ~~gt rig stop~~ (daemon will restart it) |\n", c)
		fmt.Printf("| Permanently disable rig | `%s rig dock <rig>` | ~~gt rig park~~ (temporary only) |\n", c)
		fmt.Println("| Create issues | `bd create \"title\"` | ~~gt issue create~~ (not a command) |")

	case RoleCrew:
		fmt.Println("| Want to... | Correct command | Common mistake |")
		fmt.Println("|------------|----------------|----------------|")
		fmt.Println("| Close/complete a bead | `bd close <id>` | ~~bd complete~~ (not a command), ~~bd update --status done~~ (invalid status) |")
		fmt.Printf("| Message another agent | `%s nudge <target> \"msg\"` | ~~tmux send-keys~~ (unreliable) |\n", c)
		fmt.Printf("| Dispatch work to polecat | `%s sling <bead> <rig>` | ~~gt polecat spawn~~ (not a command) |\n", c)
		fmt.Printf("| Stop my session | `%s crew stop %s` | ~~gt rig stop~~ (stops rig agents, not crew) |\n", c, ctx.Polecat)
		fmt.Printf("| Pause rig (daemon won't restart) | `%s rig park <rig>` | ~~gt rig stop~~ (daemon will restart it) |\n", c)
		fmt.Printf("| Permanently disable rig | `%s rig dock <rig>` | ~~gt rig park~~ (temporary only) |\n", c)

	case RolePolecat:
		fmt.Println("| Want to... | Correct command | Common mistake |")
		fmt.Println("|------------|----------------|----------------|")
		fmt.Printf("| Signal work complete | `%s done` | ~~bd close <root-issue>~~ (Refinery closes it) |\n", c)
		fmt.Println("| Close a sub-issue | `bd close <id>` | ~~bd complete~~ (not a command), ~~bd update --status done~~ (invalid status) |")
		fmt.Printf("| Message another agent | `%s nudge <target> \"msg\"` | ~~tmux send-keys~~ (unreliable) |\n", c)
		fmt.Println("| Check workflow steps | `bd mol current` | ~~bd ready~~ (excludes molecule steps) |")
		fmt.Println("| Create issues | `bd create \"title\"` | ~~gt issue create~~ (not a command) |")
		fmt.Printf("| Escalate blocker | `%s escalate \"desc\" -s HIGH` | ~~waiting for human~~ (never wait) |\n", c)

	case RoleWitness:
		fmt.Println("| Want to... | Correct command | Common mistake |")
		fmt.Println("|------------|----------------|----------------|")
		fmt.Println("| Close/complete a bead | `bd close <id>` | ~~bd complete~~ (not a command), ~~bd update --status done~~ (invalid status) |")
		fmt.Printf("| Message a polecat | `%s nudge %s/<name> \"msg\"` | ~~tmux send-keys~~ (unreliable) |\n", c, ctx.Rig)
		fmt.Printf("| Kill stuck polecat | `%s polecat nuke %s/<name> --force` | ~~gt polecat kill~~ (not a command) |\n", c, ctx.Rig)
		fmt.Printf("| View polecat output | `%s peek %s/<name> 50` | |\n", c, ctx.Rig)
		fmt.Println("| Create issues | `bd create \"title\"` | ~~gt issue create~~ (not a command) |")

	case RoleRefinery:
		fmt.Println("| Want to... | Correct command | Common mistake |")
		fmt.Println("|------------|----------------|----------------|")
		fmt.Printf("| Check merge queue | `%s mq list %s` | ~~git branch -r \\| grep polecat~~ (misses MRs) |\n", c, ctx.Rig)
		fmt.Printf("| Message a polecat | `%s nudge %s/<name> \"msg\"` | ~~tmux send-keys~~ (unreliable) |\n", c, ctx.Rig)
		fmt.Println("| Create issues | `bd create \"title\"` | ~~gt issue create~~ (not a command) |")

	case RoleDeacon:
		fmt.Println("| Want to... | Correct command | Common mistake |")
		fmt.Println("|------------|----------------|----------------|")
		fmt.Printf("| Start rig agents | `%s rig start <rig>` | ~~gt rig boot~~ (starts without patrol) |\n", c)
		fmt.Printf("| Pause rig (daemon won't restart) | `%s rig park <rig>` | ~~gt rig stop~~ (daemon will restart it) |\n", c)
		fmt.Printf("| Permanently disable rig | `%s rig dock <rig>` | ~~gt rig park~~ (temporary only) |\n", c)
		fmt.Printf("| Message another agent | `%s nudge <target> \"msg\"` | ~~tmux send-keys~~ (unreliable) |\n", c)

	case RoleBoot:
		fmt.Println("| Want to... | Correct command | Common mistake |")
		fmt.Println("|------------|----------------|----------------|")
		fmt.Printf("| Run triage | `%s boot triage` | ~~gt deacon heartbeat~~ (that's Deacon's job) |\n", c)
		fmt.Printf("| Check Deacon health | `%s deacon status` | ~~gt status~~ (town-wide, not Deacon-specific) |\n", c)
		fmt.Printf("| Nudge the Deacon | `%s nudge deacon \"msg\"` | ~~tmux send-keys~~ (blocked; can stage unsubmitted input) |\n", c)
	}

	fmt.Println()
	fmt.Println("**Rig lifecycle commands (park vs dock vs stop):**")
	fmt.Println("- `park/unpark` — Temporary pause. Daemon skips parked rigs.")
	fmt.Println("- `dock/undock` — Persistent disable. Survives daemon restarts.")
	fmt.Println("- `stop/start` — Immediate stop/start of rig patrol agents (witness + refinery).")
	fmt.Println("- `restart/reboot` — Stop then start rig agents.")
	fmt.Println()
}

// outputContextFile reads and displays the CONTEXT.md file from the town root.
// This provides a simple plugin point for operators to inject custom instructions
// that all agents (including polecats) will see during priming.
func outputContextFile(ctx RoleContext) {
	contextPath := filepath.Join(ctx.TownRoot, "CONTEXT.md")
	data, err := os.ReadFile(contextPath)
	if err != nil {
		explain(true, "CONTEXT.md: not found at "+contextPath)
		return
	}
	explain(true, "CONTEXT.md: found at "+contextPath+", injecting contents")
	fmt.Println()
	fmt.Print(string(data))
}

// outputHandoffContent reads and displays the pinned handoff bead for the role.
func outputHandoffContent(ctx RoleContext) {
	if ctx.Role == RoleUnknown {
		return
	}

	// Get role key for handoff bead lookup
	roleKey := string(ctx.Role)

	bd := beads.New(ctx.TownRoot)
	issue, err := bd.FindHandoffBead(roleKey)
	if err != nil {
		// Silently skip if beads lookup fails (might not be a beads repo)
		return
	}
	if issue == nil || issue.Description == "" {
		// No handoff content
		return
	}

	// Display handoff content
	fmt.Println()
	fmt.Printf("%s\n\n", style.Bold.Render("## 🤝 Handoff from Previous Session"))
	fmt.Println(issue.Description)
	fmt.Println()
	fmt.Println(style.Dim.Render("(Clear with: gt rig reset --handoff)"))
}

// outputStartupDirective outputs role-specific instructions for the agent.
// This tells agents like Mayor to announce themselves on startup.
func outputStartupDirective(ctx RoleContext) {
	switch ctx.Role {
	case RoleMayor:
		fmt.Println()
		fmt.Println("---")
		fmt.Println()
		fmt.Println("**STARTUP PROTOCOL**: You are the Mayor. Please:")
		fmt.Println("1. Run `" + cli.Name() + " prime` (loads full context, mail, and pending work)")
		fmt.Println("2. Announce: \"Mayor, checking in.\"")
		fmt.Println("3. Check mail: `" + cli.Name() + " mail inbox` - look for 🤝 HANDOFF messages")
		fmt.Println("4. Check for attached work: `" + cli.Name() + " hook`")
		fmt.Println("   - If mol attached → **RUN IT** (no human input needed)")
		fmt.Println("   - If no mol → await user instruction")
	case RoleWitness:
		if stopped, reason := IsRigParkedOrDocked(ctx.TownRoot, ctx.Rig); stopped {
			fmt.Println()
			fmt.Println("---")
			fmt.Println()
			fmt.Printf("Rig %s is %s. No patrol needed. Exit cleanly.\n", ctx.Rig, reason)
			return
		}
		fmt.Println()
		fmt.Println("---")
		fmt.Println()
		fmt.Println("**STARTUP PROTOCOL**: You are the Witness. Please:")
		fmt.Println("1. Run `" + cli.Name() + " prime` (loads full context, mail, and pending work)")
		fmt.Println("2. Announce: \"Witness, checking in.\"")
		fmt.Println("3. Check mail: `" + cli.Name() + " mail inbox` - look for 🤝 HANDOFF messages")
		fmt.Println("4. Check for attached patrol: `" + cli.Name() + " hook`")
		fmt.Println("   - If mol attached → **RUN IT** (resume from current step)")
		fmt.Println("   - If no mol → create patrol: `" + cli.Name() + " patrol new`")
	case RolePolecat:
		fmt.Println()
		fmt.Println("---")
		fmt.Println()
		fmt.Println("**STARTUP PROTOCOL**: You are a polecat with NO WORK on your hook.")
		fmt.Println()
		fmt.Println("1. Run `" + cli.Name() + " prime` (loads full context, mail, and pending work)")
		fmt.Println("2. Check if any mail was injected above in this output")
		fmt.Println("3. If you have mail with work instructions → execute that work")
		fmt.Println("4. If NO mail → run `" + cli.Name() + " done` IMMEDIATELY")
		fmt.Println()
		fmt.Println("Polecat sessions are ephemeral. No work on hook + no mail = terminate.")
		fmt.Println("DO NOT wait. DO NOT escalate. DO NOT send idle alerts.")
		fmt.Println("Just run `" + cli.Name() + " done` and exit.")
	case RoleRefinery:
		if stopped, reason := IsRigParkedOrDocked(ctx.TownRoot, ctx.Rig); stopped {
			fmt.Println()
			fmt.Println("---")
			fmt.Println()
			fmt.Printf("Rig %s is %s. No patrol needed. Exit cleanly.\n", ctx.Rig, reason)
			return
		}
		if stop, err := refinery.ActiveSafetyStop(ctx.TownRoot, ctx.Rig); err != nil {
			fmt.Println()
			fmt.Println("---")
			fmt.Println()
			style.PrintWarning("could not check refinery safety stop: %v", err)
			fmt.Println("No patrol needed. Exit cleanly until safety-stop state can be verified.")
			return
		} else if stop != nil {
			fmt.Println()
			fmt.Println("---")
			fmt.Println()
			fmt.Printf("Refinery %s is %s. No patrol needed. Exit cleanly.\n", ctx.Rig, stop.Reason())
			return
		}
		fmt.Println()
		fmt.Println("---")
		fmt.Println()
		fmt.Println("**STARTUP PROTOCOL**: You are the Refinery. Please:")
		fmt.Println("1. Run `" + cli.Name() + " prime` (loads full context, mail, and pending work)")
		fmt.Println("2. Announce: \"Refinery, checking in.\"")
		fmt.Println("3. Check mail: `" + cli.Name() + " mail inbox` - look for 🤝 HANDOFF messages")
		fmt.Println("4. Check for attached patrol: `" + cli.Name() + " hook`")
		fmt.Println("   - If mol attached → **RUN IT** (resume from current step)")
		fmt.Println("   - If no mol → create patrol: `" + cli.Name() + " patrol new`")
	case RoleCrew:
		fmt.Println()
		fmt.Println("---")
		fmt.Println()
		fmt.Println("**STARTUP PROTOCOL**: You are a crew worker. Please:")
		fmt.Println("1. Run `" + cli.Name() + " prime` (loads full context, mail, and pending work)")
		fmt.Printf("2. Announce: \"%s Crew %s, checking in.\"\n", ctx.Rig, ctx.Polecat)
		fmt.Println("3. Check mail: `" + cli.Name() + " mail inbox`")
		fmt.Println("4. If there's a 🤝 HANDOFF message, read it and continue the work")
		fmt.Println("5. Check for attached work: `" + cli.Name() + " hook`")
		fmt.Println("   - If attachment found → **RUN IT** (no human input needed)")
		fmt.Println("   - If no attachment → **STOP and wait for input**. Do NOT run")
		fmt.Println("     any more commands. Do NOT poll mail. Do NOT check status.")
		fmt.Println("     Sit idle at your prompt — a nudge or user message will arrive.")
	case RoleDeacon:
		// Skip startup protocol if paused - the pause message was already shown
		paused, _, _ := deacon.IsPaused(ctx.TownRoot)
		if paused {
			return
		}
		fmt.Println()
		fmt.Println("---")
		fmt.Println()
		fmt.Println("**STARTUP PROTOCOL**: You are the Deacon. Please:")
		fmt.Println("1. Run `" + cli.Name() + " prime` (loads full context, mail, and pending work)")
		fmt.Println("2. Announce: \"Deacon, checking in.\"")
		fmt.Println("3. Signal awake: `" + cli.Name() + " deacon heartbeat \"starting patrol\"`")
		fmt.Println("4. Check mail: `" + cli.Name() + " mail inbox` - look for 🤝 HANDOFF messages")
		fmt.Println("5. Check for attached patrol: `" + cli.Name() + " hook`")
		fmt.Println("   - If mol attached → **RUN IT** (resume from current step)")
		fmt.Println("   - If no mol → create patrol: `bd mol wisp mol-deacon-patrol`")
	case RoleDog:
		fmt.Println()
		fmt.Println("---")
		fmt.Println()
		fmt.Println("**STARTUP PROTOCOL**: You are a dog with NO WORK on your hook.")
		fmt.Println()
		fmt.Println("This likely means dispatch had a timing race (hook write not yet propagated).")
		fmt.Println("Before going idle, try to recover work:")
		fmt.Println()
		fmt.Println("1. Check mail: `" + cli.Name() + " mail inbox` — dispatcher may have sent instructions")
		fmt.Println("2. If mail has work → execute it")
		fmt.Println("3. If no mail → check ready queue: `bd ready`")
		fmt.Println("4. If ready queue has work → claim top bead: `bd update <id> --claim`")
		fmt.Println("5. If nothing available → run `" + cli.Name() + " done` and exit")
		fmt.Println()
		fmt.Println("DO NOT sit idle waiting. Recover or terminate. (GH#2748)")
	case RoleBoot:
		fmt.Println()
		fmt.Println("---")
		fmt.Println()
		fmt.Println("**STARTUP PROTOCOL**: You are Boot. Please:")
		fmt.Println("1. Run `" + cli.Name() + " prime` (loads full context)")
		fmt.Println("2. Run `" + cli.Name() + " boot triage` immediately")
		fmt.Println("3. When triage completes, exit cleanly")
	}
}

// outputAttachmentStatus checks for attached work molecule and outputs status.
// This is key for the autonomous overnight work pattern.
// The Propulsion Principle: "If you find something on your hook, YOU RUN IT."
func outputAttachmentStatus(ctx RoleContext) {
	// Skip only unknown roles - all valid roles can have pinned work
	if ctx.Role == RoleUnknown {
		return
	}

	// Check for pinned beads with attachments
	b := beads.New(ctx.WorkDir)

	// Build assignee string based on role (same as getAgentIdentity)
	assignee := getAgentIdentity(ctx)
	if assignee == "" {
		return
	}

	// Find pinned beads for this agent
	pinnedBeads, err := b.List(beads.ListOptions{
		Status:   beads.StatusPinned,
		Assignee: assignee,
		Priority: -1,
	})
	if err != nil || len(pinnedBeads) == 0 {
		// No pinned beads - interactive mode
		return
	}

	// Check first pinned bead for attachment
	attachment := beads.ParseAttachmentFields(pinnedBeads[0])
	if !hasWorkflowAttachment(attachment) {
		// No attachment - interactive mode
		return
	}

	// Has attached work - output prominently with current step
	fmt.Println()
	fmt.Printf("%s\n\n", style.Bold.Render("## 🎯 ATTACHED WORK DETECTED"))
	fmt.Printf("Pinned bead: %s\n", pinnedBeads[0].ID)
	if attachment.AttachedFormula != "" {
		fmt.Printf("Attached formula: %s\n", attachment.AttachedFormula)
	}
	if attachment.AttachedMolecule != "" {
		fmt.Printf("Attached molecule: %s\n", attachment.AttachedMolecule)
	}
	if attachment.AttachedAt != "" {
		fmt.Printf("Attached at: %s\n", attachment.AttachedAt)
	}
	if len(attachment.AttachedVars) > 0 {
		fmt.Println()
		fmt.Printf("%s\n", style.Bold.Render("🧩 VARS (instantiated formula inputs):"))
		for _, variable := range attachment.AttachedVars {
			fmt.Printf("  --var %s\n", variable)
		}
	}
	if attachment.AttachedArgs != "" {
		fmt.Println()
		fmt.Printf("%s\n", style.Bold.Render("📋 ARGS (use these to guide execution):"))
		fmt.Printf("  %s\n", attachment.AttachedArgs)
	}
	fmt.Println()

	// Show inline formula steps if formula name is known, else fall back to bd mol current
	if attachment.AttachedFormula != "" {
		showFormulaStepsFull(attachment.AttachedFormula, ctx.TownRoot, ctx.Rig, attachmentFormulaVars(attachment))
	} else {
		showMoleculeExecutionPrompt(ctx.WorkDir, attachment.AttachedMolecule)
	}
}

// outputContinuationDirective displays a brief continuation prompt for post-compact/resume.
// Unlike outputAutonomousDirective, this does NOT ask the agent to re-announce or
// re-run startup protocol — it just reminds the agent what's on the hook. (GH#1965)
func outputContinuationDirective(hookedBead *beads.Issue, hasMolecule bool) {
	fmt.Println()
	fmt.Printf("%s\n\n", style.Bold.Render("## ▶ CONTINUE HOOKED WORK"))
	fmt.Println("Your context was compacted/resumed. **Continue working on your hooked bead.**")
	fmt.Println("Do NOT re-announce, re-initialize, or re-read the bead from scratch.")
	fmt.Println("Pick up where you left off.")
	fmt.Println()
	fmt.Printf("  Hooked: %s — %s\n", style.Bold.Render(hookedBead.ID), hookedBead.Title)
	if hasMolecule {
		fmt.Println("  (Has attached molecule — check `bd mol current` for next step)")
	}
	fmt.Println()
}

// outputHandoffWarning outputs the post-handoff warning message.
func outputHandoffWarning(prevSession string) {
	fmt.Println()
	fmt.Println(style.Bold.Render("╔══════════════════════════════════════════════════════════════════╗"))
	fmt.Println(style.Bold.Render("║  ✅ HANDOFF COMPLETE - You are the NEW session                   ║"))
	fmt.Println(style.Bold.Render("╚══════════════════════════════════════════════════════════════════╝"))
	fmt.Println()
	if prevSession != "" {
		fmt.Printf("Your predecessor (%s) handed off to you.\n", prevSession)
	}
	fmt.Println()
	fmt.Println(style.Bold.Render("⚠️  DO NOT run /handoff - that was your predecessor's action."))
	fmt.Println("   The /handoff you see in context is NOT a request for you.")
	fmt.Println()
	fmt.Println("Instead: Check your hook (`" + cli.Name() + " mol status`) and mail (`" + cli.Name() + " mail inbox`).")
	fmt.Println()
}

// outputState outputs only the session state (for --state flag).
// If jsonOutput is true, outputs JSON format instead of key:value.
func outputState(ctx RoleContext, jsonOutput bool) {
	state := detectSessionState(ctx)

	if jsonOutput {
		data, err := json.Marshal(state)
		if err != nil {
			// Fall back to plain text on error
			fmt.Printf("state: %s\n", state.State)
			fmt.Printf("role: %s\n", state.Role)
			return
		}
		fmt.Println(string(data))
		return
	}

	fmt.Printf("state: %s\n", state.State)
	fmt.Printf("role: %s\n", state.Role)

	switch state.State {
	case "post-handoff":
		if state.PrevSession != "" {
			fmt.Printf("prev_session: %s\n", state.PrevSession)
		}
	case "crash-recovery":
		if state.CheckpointAge != "" {
			fmt.Printf("checkpoint_age: %s\n", state.CheckpointAge)
		}
	case "autonomous":
		if state.HookedBead != "" {
			fmt.Printf("hooked_bead: %s\n", state.HookedBead)
		}
	}
}

// outputCheckpointContext reads and displays any previous session checkpoint.
// This enables crash recovery by showing what the previous session was working on.
func outputCheckpointContext(ctx RoleContext) {
	// Only applies to polecats and crew workers
	if ctx.Role != RolePolecat && ctx.Role != RoleCrew {
		return
	}

	// Read checkpoint
	cp, err := checkpoint.Read(ctx.WorkDir)
	if err != nil {
		// Silently ignore read errors
		return
	}
	if cp == nil {
		// No checkpoint exists
		return
	}

	// Check if checkpoint is stale (older than 24 hours)
	if cp.IsStale(24 * time.Hour) {
		// Remove stale checkpoint
		_ = checkpoint.Remove(ctx.WorkDir)
		return
	}

	// Display checkpoint context
	fmt.Println()
	fmt.Printf("%s\n\n", style.Bold.Render("## 📌 Previous Session Checkpoint"))
	fmt.Printf("A previous session left a checkpoint %s ago.\n\n", cp.Age().Round(time.Minute))

	if cp.StepTitle != "" {
		fmt.Printf("  **Working on:** %s\n", cp.StepTitle)
	}
	if cp.MoleculeID != "" {
		fmt.Printf("  **Molecule:** %s\n", cp.MoleculeID)
	}
	if cp.CurrentStep != "" {
		fmt.Printf("  **Step:** %s\n", cp.CurrentStep)
	}
	if cp.HookedBead != "" {
		fmt.Printf("  **Hooked bead:** %s\n", cp.HookedBead)
	}
	if cp.Branch != "" {
		fmt.Printf("  **Branch:** %s\n", cp.Branch)
	}
	if len(cp.ModifiedFiles) > 0 {
		fmt.Printf("  **Modified files:** %d\n", len(cp.ModifiedFiles))
		// Show first few files
		maxShow := 5
		if len(cp.ModifiedFiles) < maxShow {
			maxShow = len(cp.ModifiedFiles)
		}
		for i := 0; i < maxShow; i++ {
			fmt.Printf("    - %s\n", cp.ModifiedFiles[i])
		}
		if len(cp.ModifiedFiles) > maxShow {
			fmt.Printf("    ... and %d more\n", len(cp.ModifiedFiles)-maxShow)
		}
	}
	if cp.Notes != "" {
		fmt.Printf("  **Notes:** %s\n", cp.Notes)
	}
	fmt.Println()

	fmt.Println("Use this context to resume work. The checkpoint will be updated as you progress.")
	fmt.Println()
}

// outputDeaconPausedMessage outputs a prominent PAUSED message for the Deacon.
// When paused, the Deacon must not perform any patrol actions.
func outputDeaconPausedMessage(state *deacon.PauseState) {
	fmt.Println()
	fmt.Printf("%s\n\n", style.Bold.Render("## ⏸️  DEACON PAUSED"))
	fmt.Println("You are paused and must NOT perform any patrol actions.")
	fmt.Println()
	if state.Reason != "" {
		fmt.Printf("Reason: %s\n", state.Reason)
	}
	fmt.Printf("Paused at: %s\n", state.PausedAt.Format(time.RFC3339))
	if state.PausedBy != "" {
		fmt.Printf("Paused by: %s\n", state.PausedBy)
	}
	fmt.Println()
	fmt.Println("Wait for human to run `" + cli.Name() + " deacon resume` before working.")
	fmt.Println()
	fmt.Println("**DO NOT:**")
	fmt.Println("- Create patrol molecules")
	fmt.Println("- Run heartbeats")
	fmt.Println("- Check agent health")
	fmt.Println("- Take any autonomous actions")
	fmt.Println()
	fmt.Println("You may respond to direct human questions.")
}

// explain outputs an explanatory message if --explain mode is enabled.
func explain(condition bool, reason string) {
	if primeExplain && condition {
		fmt.Printf("\n[EXPLAIN] %s\n", reason)
	}
}
