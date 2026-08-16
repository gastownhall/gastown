package instructions

import "strings"

type pairNames struct {
	canonical string
	alias     string
}

type provisionPlan struct {
	canonicalName string
	aliasName     string
	canonicalBody string
	writeBody     bool
	writeAlias    bool
	remove        []string
}

func planProvision(snap dirSnap, content, skipIfContains string) provisionPlan {
	names := pairNames{canonical: CanonicalFile, alias: AliasFile}
	if hasConstitution(snap) {
		names = pairNames{canonical: LocalCanonicalFile, alias: LocalAliasFile}
	}

	body, writeBody := canonicalBody(snap, names, content, skipIfContains)
	plan := provisionPlan{
		canonicalName: names.canonical,
		aliasName:     names.alias,
		canonicalBody: body,
		writeBody:     writeBody,
	}

	alias := snap.entry(names.alias)
	if !symlinkPointsAt(alias, names.canonical) {
		plan.writeAlias = true
		if alias.exists {
			plan.remove = append(plan.remove, names.alias)
		}
	}

	canonical := snap.entry(names.canonical)
	if writeBody && canonical.symlink {
		plan.remove = appendUnique(plan.remove, names.canonical)
	}
	if writeBody && names.canonical == CanonicalFile && snap.claude.regular && !isConstitution(snap.claude) {
		plan.remove = appendUnique(plan.remove, AliasFile)
		plan.writeAlias = true
	}
	if writeBody && names.canonical == LocalCanonicalFile && snap.claudeLocal.regular {
		plan.remove = appendUnique(plan.remove, LocalAliasFile)
		plan.writeAlias = true
	}

	return plan
}

func canonicalBody(snap dirSnap, names pairNames, content, skipIfContains string) (string, bool) {
	current := snap.entry(names.canonical)
	if current.regular {
		if skipIfContains != "" && strings.Contains(current.content, skipIfContains) {
			return current.content, false
		}
		if current.content == content {
			return current.content, false
		}
		return content, true
	}

	if migrated, ok := migrateOverlay(snap, names, skipIfContains); ok {
		if skipIfContains != "" {
			return migrated, true
		}
		return content, true
	}

	return content, true
}

func migrateOverlay(snap dirSnap, names pairNames, skipIfContains string) (string, bool) {
	if names.canonical == CanonicalFile {
		if snap.claude.regular && !isConstitution(snap.claude) && IsGasTownOverlay(snap.claude.content) {
			if skipIfContains == "" || strings.Contains(snap.claude.content, skipIfContains) {
				return snap.claude.content, true
			}
		}
		if snap.agents.symlink && symlinkPointsAt(snap.agents, AliasFile) && snap.claude.regular && IsGasTownOverlay(snap.claude.content) {
			return snap.claude.content, true
		}
	}
	if names.canonical == LocalCanonicalFile {
		if snap.claudeLocal.regular && IsGasTownOverlay(snap.claudeLocal.content) {
			if skipIfContains == "" || strings.Contains(snap.claudeLocal.content, skipIfContains) {
				return snap.claudeLocal.content, true
			}
		}
	}
	return "", false
}

func appendUnique(items []string, name string) []string {
	for _, item := range items {
		if item == name {
			return items
		}
	}
	return append(items, name)
}

func planNoop(plan provisionPlan) bool {
	return !plan.writeBody && !plan.writeAlias
}

// TownPairValid reports whether dir has AGENTS.md as a regular file and
// CLAUDE.md as a symlink to it.
func TownPairValid(dir string) bool {
	snap := snapshot(dir)
	return snap.agents.regular && symlinkPointsAt(snap.claude, CanonicalFile)
}
