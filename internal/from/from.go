// Package from plans a Gas Town from a parent folder of local Git repositories.
package from

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/git"
	"github.com/jonbaldie/gastown/internal/rig"
	"github.com/jonbaldie/gastown/internal/workspace"
)

var leftoverFileNames = []string{
	"compose.yaml",
	"compose.yml",
}

// RigAction is what apply should do for one discovered repository.
type RigAction int

const (
	// ActionAdd registers a new Rig.
	ActionAdd RigAction = iota
	// ActionSkip leaves an already-matching Rig in place.
	ActionSkip
)

// Rig is one planned Rig derived from a local Git repository.
type Rig struct {
	SourcePath string
	Name       string
	GitURL     string
	Prefix     string
	Action     RigAction
}

// Plan is the dry-run and apply input for gt from.
type Plan struct {
	ParentAbs     string
	TownAbs       string
	TownExists    bool
	Rigs          []Rig
	LeftoverFiles []string
	Skipped       []string
}

// Prepare resolves paths, discovers candidate repositories, and fails before
// any Town writes when the layout is invalid.
func Prepare(parent, town string) (*Plan, error) {
	parentAbs, err := resolvePath(parent)
	if err != nil {
		return nil, fmt.Errorf("resolving parent path: %w", err)
	}
	info, err := os.Stat(parentAbs)
	if err != nil {
		return nil, fmt.Errorf("parent folder: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("parent %s is not a directory", parentAbs)
	}

	townAbs, err := resolveTownPath(parentAbs, town)
	if err != nil {
		return nil, err
	}
	inside, err := pathInside(townAbs, parentAbs)
	if err != nil {
		return nil, fmt.Errorf("comparing Town path %s to parent %s: %w", townAbs, parentAbs, err)
	}
	if inside {
		return nil, fmt.Errorf("Town path %s is inside parent folder %s; HQ files must not mix with project repositories", townAbs, parentAbs)
	}

	if townRoot, err := workspace.Find(parentAbs); err == nil && townRoot != "" {
		if townRoot == parentAbs {
			return nil, fmt.Errorf("parent %s is a Gas Town HQ; gt from scans a folder of project repositories, not a Town", parentAbs)
		}
		return nil, fmt.Errorf("parent %s is inside Gas Town HQ %s; gt from scans a folder of project repositories, not a Town", parentAbs, townRoot)
	}

	plan := &Plan{
		ParentAbs: parentAbs,
		TownAbs:   townAbs,
	}

	townInfo, townStatErr := os.Stat(townAbs)
	switch {
	case townStatErr == nil:
		if !townInfo.IsDir() {
			return nil, fmt.Errorf("Town path %s exists and is not a directory", townAbs)
		}
		isTown, err := workspace.IsWorkspace(townAbs)
		if err != nil {
			return nil, fmt.Errorf("checking Town path: %w", err)
		}
		if !isTown {
			return nil, fmt.Errorf("path %s exists but is not a Gas Town HQ", townAbs)
		}
		plan.TownExists = true
	case !os.IsNotExist(townStatErr):
		return nil, fmt.Errorf("checking Town path: %w", townStatErr)
	}

	if err := discover(plan); err != nil {
		return nil, err
	}
	if err := preflight(plan); err != nil {
		return nil, err
	}
	return plan, nil
}

func resolveTownPath(parentAbs, town string) (string, error) {
	if strings.TrimSpace(town) == "" {
		return filepath.Join(filepath.Dir(parentAbs), filepath.Base(parentAbs)+".gt"), nil
	}
	abs, err := resolvePath(town)
	if err != nil {
		return "", fmt.Errorf("resolving Town path: %w", err)
	}
	return abs, nil
}

func resolvePath(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("path is required")
	}
	if strings.HasPrefix(p, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("getting home directory: %w", err)
		}
		switch {
		case p == "~":
			p = home
		case strings.HasPrefix(p, "~/"):
			p = filepath.Join(home, p[2:])
		default:
			p = filepath.Join(home, p[1:])
		}
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("resolving absolute path: %w", err)
	}
	return filepath.Clean(abs), nil
}

func pathInside(inner, outer string) (bool, error) {
	rel, err := filepath.Rel(outer, inner)
	if err != nil {
		return false, err
	}
	if rel == "." {
		return true, nil
	}
	return !strings.HasPrefix(rel, ".."), nil
}

func discover(plan *Plan) error {
	entries, err := os.ReadDir(plan.ParentAbs)
	if err != nil {
		return fmt.Errorf("reading parent folder: %w", err)
	}

	var children []string
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() {
			if isLeftoverFileName(name) {
				plan.LeftoverFiles = append(plan.LeftoverFiles, name)
			}
			continue
		}
		if strings.HasPrefix(name, ".") {
			continue
		}
		child := filepath.Join(plan.ParentAbs, name)
		if looksLikeAssembledRig(child) {
			plan.Skipped = append(plan.Skipped, name+" (assembled Rig)")
			continue
		}
		if isGitDirRepo(child) {
			children = append(children, child)
		}
	}

	sources := children
	if len(sources) == 0 && isGitDirRepo(plan.ParentAbs) {
		sources = []string{plan.ParentAbs}
	}
	if len(sources) == 0 {
		return fmt.Errorf("no Git repositories found in %s", plan.ParentAbs)
	}

	existing, err := loadExistingRigs(plan)
	if err != nil {
		return err
	}

	for _, source := range sources {
		planned, err := planRig(source)
		if err != nil {
			return err
		}
		classifyAgainstExisting(&planned, existing)
		plan.Rigs = append(plan.Rigs, planned)
	}
	return nil
}

func planRig(source string) (Rig, error) {
	name := rig.SanitizeName(filepath.Base(source))
	g := git.NewGit(source)
	gitURL := fileURL(source)
	origin, err := g.ConfiguredRemoteURL("origin")
	switch {
	case err == nil && strings.TrimSpace(origin) != "":
		gitURL = strings.TrimSpace(origin)
	case err == nil, errors.Is(err, git.ErrRemoteNotConfigured):
		// Keep the file:// URL when origin is absent.
	default:
		return Rig{}, fmt.Errorf("reading origin for %s: %w", source, err)
	}
	return Rig{
		SourcePath: source,
		Name:       name,
		GitURL:     gitURL,
		Prefix:     rig.DeriveBeadsPrefix(name),
		Action:     ActionAdd,
	}, nil
}

func classifyAgainstExisting(planned *Rig, existing map[string]config.RigEntry) {
	entry, ok := existing[planned.Name]
	if !ok {
		return
	}
	if samePath(entry.LocalRepo, planned.SourcePath) {
		planned.Action = ActionSkip
	}
}

func loadExistingRigs(plan *Plan) (map[string]config.RigEntry, error) {
	if !plan.TownExists {
		return nil, nil
	}
	rigsPath := filepath.Join(plan.TownAbs, "mayor", "rigs.json")
	cfg, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		return nil, fmt.Errorf("loading rigs.json: %w", err)
	}
	if cfg.Rigs == nil {
		return map[string]config.RigEntry{}, nil
	}
	return cfg.Rigs, nil
}

func preflight(plan *Plan) error {
	byName := make(map[string][]string)
	byPrefix := make(map[string][]string)
	for _, r := range plan.Rigs {
		byName[r.Name] = append(byName[r.Name], r.SourcePath)
		byPrefix[r.Prefix] = append(byPrefix[r.Prefix], r.Name)
		if rig.IsReservedName(r.Name) {
			return fmt.Errorf("folder %s sanitizes to reserved Rig name %q", r.SourcePath, r.Name)
		}
	}
	for name, paths := range byName {
		if len(paths) > 1 {
			return fmt.Errorf("folders %s sanitize to the same Rig name %q", strings.Join(paths, " and "), name)
		}
	}
	for prefix, names := range byPrefix {
		if len(unique(names)) > 1 {
			return fmt.Errorf("Rigs %s would share Beads prefix %q", strings.Join(unique(names), " and "), prefix)
		}
	}

	existing, err := loadExistingRigs(plan)
	if err != nil {
		return err
	}
	for _, r := range plan.Rigs {
		entry, ok := existing[r.Name]
		if !ok {
			continue
		}
		if r.Action == ActionSkip {
			continue
		}
		return fmt.Errorf("Rig %q already exists for a different source (%s)", r.Name, entry.LocalRepo)
	}
	if plan.TownExists {
		for _, r := range plan.Rigs {
			if r.Action == ActionSkip {
				continue
			}
			if err := beads.CheckPrefixAvailable(plan.TownAbs, r.Prefix+"-", r.Name); err != nil {
				if errors.Is(err, beads.ErrPrefixInUse) {
					return fmt.Errorf("Beads prefix %q for Rig %q collides with an existing Town prefix: %w", r.Prefix, r.Name, err)
				}
				return fmt.Errorf("checking Beads prefix %q: %w", r.Prefix, err)
			}
		}
	}
	return nil
}

func isGitDirRepo(path string) bool {
	info, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil && info.IsDir()
}

func looksLikeAssembledRig(path string) bool {
	if info, err := os.Stat(filepath.Join(path, ".repo.git")); err == nil && info.IsDir() {
		return true
	}
	data, err := os.ReadFile(filepath.Join(path, "config.json"))
	if err != nil {
		return false
	}
	var cfg struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(data, &cfg) != nil {
		return false
	}
	return cfg.Type == "rig"
}

func isLeftoverFileName(name string) bool {
	for _, leftover := range leftoverFileNames {
		if name == leftover {
			return true
		}
	}
	return false
}

func fileURL(abs string) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	return u.String()
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if resolved, err := filepath.EvalSymlinks(a); err == nil {
		a = resolved
	}
	if resolved, err := filepath.EvalSymlinks(b); err == nil {
		b = resolved
	}
	return a == b
}

func unique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
