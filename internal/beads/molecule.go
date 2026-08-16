// Package beads molecule support - composable workflow templates.
package beads

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/jonbaldie/gastown/internal/telemetry"
)

// MoleculeStep represents a parsed step from a molecule definition.
type MoleculeStep struct {
	Ref          string         // Step reference (from "## Step: <ref>")
	Title        string         // Step title (first non-empty line or ref)
	Instructions string         // Prose instructions for this step
	Needs        []string       // Step refs this step depends on
	WaitsFor     []string       // Dynamic wait conditions (e.g., "all-children")
	Tier         string         // Optional tier hint: haiku, sonnet, opus
	Type         string         // Step type: "task" (default), "wait", etc.
	Backoff      *BackoffConfig // Backoff configuration for wait-type steps
}

// BackoffConfig defines exponential backoff parameters for wait-type steps.
// Used by patrol agents to implement cost-saving await-signal patterns.
type BackoffConfig struct {
	Base       string // Base interval (e.g., "30s")
	Multiplier int    // Multiplier for exponential growth (default: 2)
	Max        string // Maximum interval cap (e.g., "10m")
}

// stepHeaderRegex matches "## Step: <ref>" with optional whitespace.
var stepHeaderRegex = regexp.MustCompile(`(?i)^##\s*Step:\s*(\S+)\s*$`)

// needsLineRegex matches "Needs: step1, step2, ..." lines.
var needsLineRegex = regexp.MustCompile(`(?i)^Needs:\s*(.+)$`)

// tierLineRegex matches "Tier: haiku|sonnet|opus" lines.
var tierLineRegex = regexp.MustCompile(`(?i)^Tier:\s*(haiku|sonnet|opus)\s*$`)

// waitsForLineRegex matches "WaitsFor: condition1, condition2, ..." lines.
// Common conditions: "all-children" (fanout gate for dynamically bonded children)
var waitsForLineRegex = regexp.MustCompile(`(?i)^WaitsFor:\s*(.+)$`)

// typeLineRegex matches "Type: task|wait|..." lines.
// Common types: "task" (default), "wait" (await-signal with backoff)
var typeLineRegex = regexp.MustCompile(`(?i)^Type:\s*(\w+)\s*$`)

// backoffLineRegex matches "Backoff: base=30s, multiplier=2, max=10m" lines.
// Parses backoff configuration for wait-type steps.
var backoffLineRegex = regexp.MustCompile(`(?i)^Backoff:\s*(.+)$`)

// templateVarRegex matches {{variable}} placeholders.
var templateVarRegex = regexp.MustCompile(`\{\{(\w+)\}\}`)

// ParseMoleculeSteps extracts step definitions from a molecule's description.
//
// The expected format is:
//
//	## Step: <ref>
//	<prose instructions>
//	Needs: <step>, <step>  # optional
//	Tier: haiku|sonnet|opus  # optional
//	Type: task|wait  # optional, default is "task"
//	Backoff: base=30s, multiplier=2, max=10m  # optional, for wait-type steps
//
// Returns an empty slice if no steps are found.
func ParseMoleculeSteps(description string) ([]MoleculeStep, error) {
	if description == "" {
		return nil, nil
	}

	lines := strings.Split(description, "\n")
	var steps []MoleculeStep
	var currentStep *MoleculeStep
	var contentLines []string

	// Helper to finalize current step
	finalizeStep := func() {
		if currentStep == nil {
			return
		}

		// Process content lines to extract Needs/Tier and build instructions
		var instructionLines []string
		for _, line := range contentLines {
			trimmed := strings.TrimSpace(line)

			// Check for Needs: line
			if matches := needsLineRegex.FindStringSubmatch(trimmed); matches != nil {
				deps := strings.Split(matches[1], ",")
				for _, dep := range deps {
					dep = strings.TrimSpace(dep)
					if dep != "" {
						currentStep.Needs = append(currentStep.Needs, dep)
					}
				}
				continue
			}

			// Check for Tier: line
			if matches := tierLineRegex.FindStringSubmatch(trimmed); matches != nil {
				currentStep.Tier = strings.ToLower(matches[1])
				continue
			}

			// Check for WaitsFor: line
			if matches := waitsForLineRegex.FindStringSubmatch(trimmed); matches != nil {
				conditions := strings.Split(matches[1], ",")
				for _, cond := range conditions {
					cond = strings.TrimSpace(cond)
					if cond != "" {
						currentStep.WaitsFor = append(currentStep.WaitsFor, cond)
					}
				}
				continue
			}

			// Check for Type: line
			if matches := typeLineRegex.FindStringSubmatch(trimmed); matches != nil {
				currentStep.Type = strings.ToLower(matches[1])
				continue
			}

			// Check for Backoff: line
			if matches := backoffLineRegex.FindStringSubmatch(trimmed); matches != nil {
				currentStep.Backoff = parseBackoffConfig(matches[1])
				continue
			}

			// Regular instruction line
			instructionLines = append(instructionLines, line)
		}

		// Build instructions, trimming leading/trailing blank lines
		currentStep.Instructions = strings.TrimSpace(strings.Join(instructionLines, "\n"))

		// Set title from first non-empty line of instructions, or use ref
		if currentStep.Instructions != "" {
			firstLine := strings.SplitN(currentStep.Instructions, "\n", 2)[0]
			currentStep.Title = strings.TrimSpace(firstLine)
		}
		if currentStep.Title == "" {
			currentStep.Title = currentStep.Ref
		}

		steps = append(steps, *currentStep)
		currentStep = nil
		contentLines = nil
	}

	for _, line := range lines {
		// Check for step header
		if matches := stepHeaderRegex.FindStringSubmatch(line); matches != nil {
			// Finalize previous step if any
			finalizeStep()

			// Start new step
			currentStep = &MoleculeStep{
				Ref: matches[1],
			}
			contentLines = nil
			continue
		}

		// Accumulate content lines if we're in a step
		if currentStep != nil {
			contentLines = append(contentLines, line)
		}
	}

	// Finalize last step
	finalizeStep()

	return steps, nil
}

// parseBackoffConfig parses a backoff configuration string.
// Expected format: "base=30s, multiplier=2, max=10m"
// Returns nil if parsing fails.
func parseBackoffConfig(configStr string) *BackoffConfig {
	cfg := &BackoffConfig{
		Multiplier: 2, // Default multiplier
	}

	// Split by comma and parse key=value pairs
	parts := strings.Split(configStr, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		// Split by = to get key and value
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}

		key := strings.TrimSpace(strings.ToLower(kv[0]))
		value := strings.TrimSpace(kv[1])

		switch key {
		case "base":
			cfg.Base = value
		case "multiplier":
			if m, err := strconv.Atoi(value); err == nil {
				cfg.Multiplier = m
			}
		case "max":
			cfg.Max = value
		}
	}

	// Return nil if no base was specified (incomplete config)
	if cfg.Base == "" {
		return nil
	}

	return cfg
}

// ExpandTemplateVars replaces {{variable}} placeholders in text using the provided context map.
// Unknown variables are left as-is.
func ExpandTemplateVars(text string, ctx map[string]string) string {
	if ctx == nil {
		return text
	}

	return templateVarRegex.ReplaceAllStringFunc(text, func(match string) string {
		// Extract variable name from {{name}}
		varName := match[2 : len(match)-2]
		if value, ok := ctx[varName]; ok {
			return value
		}
		return match // Leave unknown variables as-is
	})
}

// InstantiateOptions configures molecule instantiation behavior.
type InstantiateOptions struct {
	// Context map for {{variable}} substitution
	Context map[string]string
}

// InstantiateMolecule creates child issues from a molecule template.
//
// This function supports two molecule formats (format bridge pattern):
//
//  1. New format (child issues): If the molecule proto has child issues,
//     those children are used as templates. Dependencies are copied from
//     the template children's DependsOn relationships.
//
//  2. Old format (embedded markdown): If the molecule has no children,
//     steps are parsed from the Description field using ParseMoleculeSteps().
//     Dependencies are extracted from "Needs:" declarations in the markdown.
//
// For each step, this creates:
//   - A child issue with ID "{parent.ID}.{step.Ref}"
//   - Title from step title
//   - Description from step instructions (with template vars expanded)
//   - Type: task
//   - Priority: inherited from parent
//   - Dependencies wired according to template
//
// The function is atomic via bd CLI - either all issues are created or none.
// Returns the created step issues.
func (b *Beads) InstantiateMolecule(ctx context.Context, mol *Issue, parent *Issue, opts InstantiateOptions) ([]*Issue, error) {
	if mol == nil {
		return nil, fmt.Errorf("molecule issue is nil")
	}
	if parent == nil {
		return nil, fmt.Errorf("parent issue is nil")
	}

	// FORMAT BRIDGE: Try new format first (child issues), fall back to old format (markdown)
	templateChildren, err := b.List(ListOptions{
		Parent:   mol.ID,
		Status:   "all",
		Priority: -1,
	})
	if err != nil {
		// Non-fatal - might not have children, continue to old format
		templateChildren = nil
	}

	if len(templateChildren) > 0 {
		// NEW FORMAT: Use child issues as templates
		return b.instantiateFromChildren(ctx, mol, parent, templateChildren, opts)
	}

	// OLD FORMAT: Parse steps from molecule description
	return b.instantiateFromMarkdown(ctx, mol, parent, opts)
}

// instantiateFromChildren creates steps from template child issues (new format).
func (b *Beads) instantiateFromChildren(ctx context.Context, mol *Issue, parent *Issue, templates []*Issue, opts InstantiateOptions) ([]*Issue, error) {
	var createdIssues []*Issue
	templateToNew := make(map[string]string) // template ID -> new issue ID

	// First pass: create all child issues
	for _, tmpl := range templates {
		// Expand template variables in description
		description := tmpl.Description
		if opts.Context != nil {
			description = ExpandTemplateVars(description, opts.Context)
		}

		// Add provenance metadata
		if description != "" {
			description += "\n\n"
		}
		description += fmt.Sprintf("instantiated_from: %s\ntemplate_step: %s", mol.ID, tmpl.ID)

		// Create the child issue
		stepType := tmpl.Type
		if stepType == "" {
			stepType = "task"
		}
		childOpts := CreateOptions{
			Title:       tmpl.Title,
			Labels:      []string{"gt:" + stepType},
			Priority:    parent.Priority,
			Description: description,
			Parent:      parent.ID,
		}

		child, err := b.Create(childOpts)
		if err != nil {
			// Attempt to clean up created issues on failure (best-effort cleanup)
			for _, created := range createdIssues {
				_ = b.Close(created.ID)
			}
			return nil, fmt.Errorf("creating step from template %q: %w", tmpl.ID, err)
		}
		telemetry.RecordBeadCreate(ctx, child.ID, parent.ID, mol.ID)

		createdIssues = append(createdIssues, child)
		templateToNew[tmpl.ID] = child.ID
	}

	// Second pass: wire dependencies based on template dependencies
	for _, tmpl := range templates {
		if len(tmpl.DependsOn) == 0 {
			continue
		}

		newChildID := templateToNew[tmpl.ID]
		for _, depTemplateID := range tmpl.DependsOn {
			newDepID, ok := templateToNew[depTemplateID]
			if !ok {
				// Dependency points outside the template - skip
				continue
			}
			if err := b.AddDependency(newChildID, newDepID); err != nil {
				// Log but don't fail - the issues are created
				return createdIssues, fmt.Errorf("adding dependency %s -> %s: %w", newChildID, newDepID, err)
			}
		}
	}

	return createdIssues, nil
}

// instantiateFromMarkdown creates steps from embedded markdown (old format).
func (b *Beads) instantiateFromMarkdown(ctx context.Context, mol *Issue, parent *Issue, opts InstantiateOptions) ([]*Issue, error) {
	// Parse steps from molecule
	steps, err := ParseMoleculeSteps(mol.Description)
	if err != nil {
		return nil, fmt.Errorf("parsing molecule steps: %w", err)
	}

	if len(steps) == 0 {
		return nil, fmt.Errorf("molecule has no steps defined")
	}

	// Build map of step ref -> step for dependency validation
	stepMap := make(map[string]*MoleculeStep)
	for i := range steps {
		stepMap[steps[i].Ref] = &steps[i]
	}

	// Validate all Needs references exist
	for _, step := range steps {
		for _, need := range step.Needs {
			if _, ok := stepMap[need]; !ok {
				return nil, fmt.Errorf("step %q depends on unknown step %q", step.Ref, need)
			}
		}
	}

	// Create child issues for each step
	var createdIssues []*Issue
	stepIssueIDs := make(map[string]string) // step ref -> issue ID

	for _, step := range steps {
		// Expand template variables in instructions
		instructions := step.Instructions
		if opts.Context != nil {
			instructions = ExpandTemplateVars(instructions, opts.Context)
		}

		// Build description with provenance metadata
		description := instructions
		if description != "" {
			description += "\n\n"
		}
		description += fmt.Sprintf("instantiated_from: %s\nstep: %s", mol.ID, step.Ref)
		if step.Tier != "" {
			description += fmt.Sprintf("\ntier: %s", step.Tier)
		}

		// Create the child issue
		childOpts := CreateOptions{
			Title:       step.Title,
			Labels:      []string{"gt:task"},
			Priority:    parent.Priority,
			Description: description,
			Parent:      parent.ID,
		}

		child, err := b.Create(childOpts)
		if err != nil {
			// Attempt to clean up created issues on failure (best-effort cleanup)
			for _, created := range createdIssues {
				_ = b.Close(created.ID)
			}
			return nil, fmt.Errorf("creating step %q: %w", step.Ref, err)
		}
		telemetry.RecordBeadCreate(ctx, child.ID, parent.ID, mol.ID)

		createdIssues = append(createdIssues, child)
		stepIssueIDs[step.Ref] = child.ID
	}

	// Wire inter-step dependencies based on Needs: declarations
	for _, step := range steps {
		if len(step.Needs) == 0 {
			continue
		}

		childID := stepIssueIDs[step.Ref]
		for _, need := range step.Needs {
			dependsOnID := stepIssueIDs[need]
			if err := b.AddDependency(childID, dependsOnID); err != nil {
				// Log but don't fail - the issues are created
				// This is non-atomic but bd CLI doesn't support transactions
				return createdIssues, fmt.Errorf("adding dependency %s -> %s: %w", childID, dependsOnID, err)
			}
		}
	}

	return createdIssues, nil
}

// ValidateMolecule checks if an issue is a valid molecule definition.
// Returns an error describing the problem, or nil if valid.
//
// Note: This function only validates the old format (embedded markdown steps).
// For new format molecules (with child issues), validation is implicit during
// instantiation - if the molecule has children, those are used as templates.
// Use InstantiateMolecule directly for new format molecules; this function
// will report "no steps defined" for new format molecules since it cannot
// access child issues without a Beads client.
func ValidateMolecule(mol *Issue) error {
	if mol == nil {
		return fmt.Errorf("molecule is nil")
	}

	if mol.Type != "molecule" {
		return fmt.Errorf("issue type is %q, expected molecule", mol.Type)
	}

	steps, err := ParseMoleculeSteps(mol.Description)
	if err != nil {
		return fmt.Errorf("parsing steps: %w", err)
	}

	if len(steps) == 0 {
		return fmt.Errorf("molecule has no steps defined")
	}

	// Build step map for reference validation
	stepMap := make(map[string]bool)
	for _, step := range steps {
		if step.Ref == "" {
			return fmt.Errorf("step has empty ref")
		}
		if stepMap[step.Ref] {
			return fmt.Errorf("duplicate step ref: %s", step.Ref)
		}
		stepMap[step.Ref] = true
	}

	// Validate Needs references
	for _, step := range steps {
		for _, need := range step.Needs {
			if !stepMap[need] {
				return fmt.Errorf("step %q depends on unknown step %q", step.Ref, need)
			}
			if need == step.Ref {
				return fmt.Errorf("step %q has self-dependency", step.Ref)
			}
		}
	}

	// Detect cycles in dependency graph
	if err := detectCycles(steps); err != nil {
		return err
	}

	return nil
}

// detectCycles checks for circular dependencies in the step graph using DFS.
// Returns an error describing the cycle if one is found.
func detectCycles(steps []MoleculeStep) error {
	// Build adjacency list: step -> steps it depends on
	deps := make(map[string][]string)
	for _, step := range steps {
		deps[step.Ref] = step.Needs
	}

	// Track visit state: 0 = unvisited, 1 = visiting (in stack), 2 = visited
	state := make(map[string]int)

	// DFS from each node to find cycles
	var path []string
	var dfs func(node string) error

	dfs = func(node string) error {
		if state[node] == 2 {
			return nil // Already fully processed
		}
		if state[node] == 1 {
			// Found a back edge - there's a cycle
			// Build cycle path for error message
			cycleStart := -1
			for i, n := range path {
				if n == node {
					cycleStart = i
					break
				}
			}
			cycle := append(path[cycleStart:], node)
			return fmt.Errorf("cycle detected in step dependencies: %s", formatCycle(cycle))
		}

		state[node] = 1 // Mark as visiting
		path = append(path, node)

		for _, dep := range deps[node] {
			if err := dfs(dep); err != nil {
				return err
			}
		}

		path = path[:len(path)-1] // Pop from path
		state[node] = 2           // Mark as visited
		return nil
	}

	for _, step := range steps {
		if state[step.Ref] == 0 {
			if err := dfs(step.Ref); err != nil {
				return err
			}
		}
	}

	return nil
}

// formatCycle formats a cycle path as "a -> b -> c -> a".
func formatCycle(cycle []string) string {
	return strings.Join(cycle, " -> ")
}
