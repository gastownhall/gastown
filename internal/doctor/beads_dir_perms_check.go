package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/jonbaldie/gastown/internal/beads"
)

// BeadsDirPermsCheck verifies .beads directories use mode 0700.
// Beads warns on 0755; install/init and doctor --fix should keep these private.
type BeadsDirPermsCheck struct {
	FixableCheck
	loose []string
}

// NewBeadsDirPermsCheck creates a .beads directory permission check.
func NewBeadsDirPermsCheck() *BeadsDirPermsCheck {
	return &BeadsDirPermsCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "beads-dir-perms",
				CheckDescription: "Verify .beads directories are mode 0700",
				CheckCategory:    CategoryConfig,
			},
		},
	}
}

// Run reports .beads directories whose permission bits are not 0700.
func (c *BeadsDirPermsCheck) Run(ctx *CheckContext) *CheckResult {
	c.loose = nil
	if runtime.GOOS == "windows" {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusOK,
			Message:  "Directory permission bits are not enforced on Windows",
			Category: c.CheckCategory,
		}
	}

	for _, dir := range collectBeadsDirs(ctx.TownRoot) {
		info, err := os.Stat(dir)
		if err != nil {
			return &CheckResult{
				Name:     c.Name(),
				Status:   StatusWarning,
				Message:  fmt.Sprintf("Could not stat %s: %v", dir, err),
				Category: c.CheckCategory,
			}
		}
		if !info.IsDir() {
			continue
		}
		if info.Mode().Perm() != beads.DirPerm {
			c.loose = append(c.loose, dir)
		}
	}

	if len(c.loose) == 0 {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusOK,
			Message:  "All .beads directories are mode 0700",
			Category: c.CheckCategory,
		}
	}

	details := make([]string, 0, len(c.loose))
	for _, dir := range c.loose {
		rel := dir
		if r, err := filepath.Rel(ctx.TownRoot, dir); err == nil {
			rel = r
		}
		info, err := os.Stat(dir)
		mode := os.FileMode(0)
		if err == nil {
			mode = info.Mode().Perm()
		}
		details = append(details, fmt.Sprintf("%s is %04o (want 0700)", rel, mode))
	}

	return &CheckResult{
		Name:     c.Name(),
		Status:   StatusWarning,
		Message:  fmt.Sprintf("%d .beads director(ies) are not mode 0700", len(c.loose)),
		Details:  details,
		FixHint:  "Run 'gt doctor --fix' to chmod .beads directories to 0700",
		Category: c.CheckCategory,
	}
}

// Fix sets loose .beads directories to mode 0700.
func (c *BeadsDirPermsCheck) Fix(ctx *CheckContext) error {
	if len(c.loose) == 0 {
		return nil
	}
	for _, dir := range c.loose {
		if err := beads.EnsureDir(dir); err != nil {
			return err
		}
	}
	return nil
}

func collectBeadsDirs(townRoot string) []string {
	seen := make(map[string]struct{})
	var dirs []string
	add := func(path string) {
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		dirs = append(dirs, path)
	}

	add(filepath.Join(townRoot, ".beads"))
	add(filepath.Join(townRoot, "deacon", ".beads"))

	rigs, err := findRigDirs(townRoot)
	if err == nil {
		for _, rig := range rigs {
			add(filepath.Join(rig, ".beads"))
			add(filepath.Join(rig, "mayor", "rig", ".beads"))
			for _, dir := range getBeadsDirsToCheck(rig) {
				add(dir)
			}
		}
	}

	sort.Strings(dirs)
	return dirs
}
