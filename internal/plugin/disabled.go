package plugin

import (
	"os"
	"path/filepath"
	"strings"
)

// DisabledPluginsDir is the town-level directory where operators park plugins
// that must not be reinstalled. Entries are archived plugin directories whose
// names carry provenance and a date, e.g.
//
//	rebuild-gt.town.redisabled-20260806
//	dolt-archive.DEN-SOURCE.removed-20260806
//	tool-updater.rig.redisabled-20260714
//
// so the plugin name is the prefix up to the first dot, never the whole
// directory name.
const DisabledPluginsDir = ".disabled-plugins"

// DisabledPlugin records a plugin name that is blocked from sync, and the
// marker directory that blocks it.
type DisabledPlugin struct {
	Name   string // plugin name, e.g. "rebuild-gt"
	Marker string // directory under .disabled-plugins/ that blocked it

	// Installed reports that the runtime already has this plugin, which makes
	// the marker suspect: something disabled it, yet it is running. Set by
	// SyncPlugins and DetectDrift, which know the target; always false from
	// LoadDisabledPlugins, which does not.
	Installed bool
}

// LoadDisabledPlugins reads <townRoot>/.disabled-plugins and returns the plugin
// names an operator has deliberately disabled town-wide, keyed by plugin name.
//
// This is a positive record of operator intent, so it holds regardless of which
// source directory sync resolves to, who invokes sync, or from which working
// directory. A missing or unreadable directory yields an empty set: the guard
// only ever removes names from a sync, it never blocks one from running.
func LoadDisabledPlugins(townRoot string) map[string]DisabledPlugin {
	disabled := make(map[string]DisabledPlugin)
	if townRoot == "" {
		return disabled
	}

	entries, err := os.ReadDir(filepath.Join(townRoot, DisabledPluginsDir))
	if err != nil {
		return disabled
	}

	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		name := disabledPluginName(entry.Name())
		if name == "" {
			continue
		}
		// ReadDir is sorted, so the first marker for a name wins
		// deterministically. Any single marker is sufficient evidence.
		if _, ok := disabled[name]; !ok {
			disabled[name] = DisabledPlugin{Name: name, Marker: entry.Name()}
		}
	}
	return disabled
}

// disabledPluginName extracts the plugin name from a marker directory name.
// Markers are "<plugin>.<provenance>.<when>"; the name is everything before the
// first dot. A marker with no dot is already a bare plugin name.
func disabledPluginName(dirName string) string {
	if i := strings.IndexByte(dirName, '.'); i >= 0 {
		return dirName[:i]
	}
	return dirName
}
