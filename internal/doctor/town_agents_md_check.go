package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonbaldie/gastown/internal/instructions"
	"github.com/jonbaldie/gastown/internal/templates"
)

// TownAgentsMDCheck verifies the town-root AGENTS.md is up to date with
// the version embedded in the binary. This is the highest-value migration
// check — behavioral norms for agents come from AGENTS.md.
//
// The town-root AGENTS.md (~/gt/AGENTS.md) is loaded by agents running
// from within the town git tree (Mayor, Deacon). CLAUDE.md is a symlink
// to that file. It must contain operational norms (Dolt awareness,
// communication hygiene, nudge-first) that guide agent behavior.
type TownAgentsMDCheck struct {
	FixableCheck
	missingSections []templates.TownRootRequiredSection
	fileMissing     bool
	pairWrong       bool
}

// NewTownAgentsMDCheck creates a new town-root AGENTS.md version check.
func NewTownAgentsMDCheck() *TownAgentsMDCheck {
	return &TownAgentsMDCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "town-agents-md",
				CheckDescription: "Verify town-root AGENTS.md is up to date with embedded version",
				CheckCategory:    CategoryConfig,
			},
		},
	}
}

// Run checks the town-root CLAUDE.md for completeness.
func (c *TownAgentsMDCheck) Run(ctx *CheckContext) *CheckResult {
	c.missingSections = nil
	c.fileMissing = false
	c.pairWrong = false

	content, err := readTownIdentity(ctx.TownRoot)
	if err != nil {
		if os.IsNotExist(err) {
			c.fileMissing = true
			return &CheckResult{
				Name:    c.Name(),
				Status:  StatusError,
				Message: "Town-root AGENTS.md is missing",
				FixHint: "Run 'gt doctor --fix' to create it from embedded template",
			}
		}
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("Cannot read town-root identity file: %v", err),
		}
	}

	required := templates.TownRootRequiredSections()
	var missing []templates.TownRootRequiredSection
	var details []string

	for _, section := range required {
		if !strings.Contains(content, section.Heading) {
			missing = append(missing, section)
			details = append(details, fmt.Sprintf("Missing: %s (%s)", section.Name, section.Heading))
		}
	}

	c.pairWrong = !instructions.TownPairValid(ctx.TownRoot)
	if c.pairWrong {
		details = append(details, "CLAUDE.md is not a symlink to AGENTS.md")
	}

	if len(missing) == 0 && !c.pairWrong {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "Town-root AGENTS.md has all required sections",
		}
	}

	c.missingSections = missing
	if len(missing) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "Town-root instruction pair is inverted or incomplete",
			Details: details,
			FixHint: "Run 'gt doctor --fix' to make AGENTS.md canonical and CLAUDE.md a symlink",
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: fmt.Sprintf("Town-root AGENTS.md missing %d section(s)", len(missing)),
		Details: details,
		FixHint: "Run 'gt doctor --fix' to add missing sections from embedded template",
	}
}

func readTownIdentity(townRoot string) (string, error) {
	for _, name := range []string{instructions.CanonicalFile, instructions.AliasFile} {
		data, err := os.ReadFile(filepath.Join(townRoot, name))
		if err == nil {
			return string(data), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
	}
	return "", os.ErrNotExist
}

// Fix updates the town-root AGENTS.md with missing sections from the
// embedded template while preserving user customizations.
func (c *TownAgentsMDCheck) Fix(ctx *CheckContext) error {
	canonical := templates.TownRootAgentsMD()

	if c.fileMissing {
		_, err := instructions.Provision(ctx.TownRoot, canonical, "")
		return err
	}

	current, err := readTownIdentity(ctx.TownRoot)
	if err != nil {
		if os.IsNotExist(err) {
			_, provErr := instructions.Provision(ctx.TownRoot, canonical, "")
			return provErr
		}
		return fmt.Errorf("reading identity file: %w", err)
	}

	var toAppend strings.Builder
	if len(c.missingSections) > 0 {
		canonicalSections := parseH2Sections(canonical)
		for _, missing := range c.missingSections {
			for _, cs := range canonicalSections {
				if strings.Contains(cs.content, missing.Heading) {
					toAppend.WriteString("\n")
					toAppend.WriteString(cs.content)
					break
				}
			}
		}
	}

	updated := current
	if toAppend.Len() > 0 {
		if !strings.HasSuffix(updated, "\n") {
			updated += "\n"
		}
		updated += toAppend.String()
	}

	_, err = instructions.Provision(ctx.TownRoot, updated, "")
	return err
}

// h2Section represents a section of markdown delimited by H2 headings.
type h2Section struct {
	heading string // The H2 heading line (e.g., "## Dolt Server — Operational Awareness")
	content string // Full section content including the heading and all sub-content
}

// parseH2Sections splits markdown content into sections by H2 headings.
// The preamble (content before the first H2) is returned as a section with
// an empty heading.
func parseH2Sections(content string) []h2Section {
	var sections []h2Section
	lines := strings.Split(content, "\n")

	var currentHeading string
	var currentContent strings.Builder
	inSection := false

	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			// Save previous section
			if inSection || currentContent.Len() > 0 {
				sections = append(sections, h2Section{
					heading: currentHeading,
					content: currentContent.String(),
				})
			}
			// Start new section
			currentHeading = line
			currentContent.Reset()
			currentContent.WriteString(line)
			currentContent.WriteString("\n")
			inSection = true
		} else {
			currentContent.WriteString(line)
			currentContent.WriteString("\n")
		}
	}

	// Save final section
	if currentContent.Len() > 0 {
		sections = append(sections, h2Section{
			heading: currentHeading,
			content: currentContent.String(),
		})
	}

	return sections
}
