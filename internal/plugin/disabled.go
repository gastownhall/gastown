package plugin

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// DisabledPluginsDir is the town-level directory where operators park plugins
// that must not be reinstalled. Each entry disables one plugin town-wide. An
// entry is usually the archived plugin directory itself, renamed to carry
// provenance and a date, e.g.
//
//	rebuild-gt.town.redisabled-20260806
//	dolt-archive.DEN-SOURCE.removed-20260806
//	tool-updater.rig.redisabled-20260714
//
// so the plugin name is the prefix up to the first dot, never the whole entry
// name. A bare "rebuild-gt" entry, file or directory, disables it just the same.
const DisabledPluginsDir = ".disabled-plugins"

// DisabledPlugin records a plugin name that is blocked from sync, and the
// marker that blocks it.
type DisabledPlugin struct {
	Name   string // plugin name, e.g. "rebuild-gt"
	Marker string // entry under .disabled-plugins/ that blocked it

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
// directory.
//
// A town without the directory has disabled nothing, and that is not an error.
// A directory that exists but cannot be read is one: the caller cannot tell
// which plugins it names, and syncing anyway would reinstall exactly the
// plugins the directory exists to keep out.
func LoadDisabledPlugins(townRoot string) (map[string]DisabledPlugin, error) {
	disabled := make(map[string]DisabledPlugin)
	if townRoot == "" {
		return disabled, nil
	}

	entries, err := os.ReadDir(filepath.Join(townRoot, DisabledPluginsDir))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return disabled, nil
		}
		return nil, fmt.Errorf("reading disabled plugins: %w", err)
	}

	for _, entry := range entries {
		// Files and symlinks count as well as directories: an operator who
		// writes ".disabled-plugins/rebuild-gt" has said what they mean, and
		// ignoring it would reinstall the plugin without a word.
		if strings.HasPrefix(entry.Name(), ".") {
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
	return disabled, nil
}

// disabledPluginName extracts the plugin name from a marker's name. Markers are
// "<plugin>.<provenance>.<when>"; the name is everything before the first dot.
// A marker with no dot is already a bare plugin name.
func disabledPluginName(dirName string) string {
	if i := strings.IndexByte(dirName, '.'); i >= 0 {
		return dirName[:i]
	}
	return dirName
}
