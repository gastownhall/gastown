package workflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// semverRe matches MAJOR.MINOR.PATCH with an optional -prerelease suffix.
var semverRe = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z.-]+)?$`)

// ErrNoVersionFiles is returned by ApplyBump when the rig lists no version files.
var ErrNoVersionFiles = errors.New("no version_files configured for gated rig")

// NextVersion returns v bumped by bump (patch, minor or major). The
// prerelease suffix, if any, is dropped.
func NextVersion(v, bump string) (string, error) {
	m := semverRe.FindStringSubmatch(v)
	if m == nil {
		return "", fmt.Errorf("version %q is not MAJOR.MINOR.PATCH", v)
	}
	var n [3]int
	for i := range n {
		x, err := strconv.Atoi(m[i+1])
		if err != nil {
			return "", fmt.Errorf("version %q: %w", v, err)
		}
		n[i] = x
	}
	switch bump {
	case "patch":
		n[2]++
	case "minor":
		n[1], n[2] = n[1]+1, 0
	case "major":
		n[0], n[1], n[2] = n[0]+1, 0, 0
	default:
		return "", fmt.Errorf("bump must be patch, minor or major, got %q", bump)
	}
	return fmt.Sprintf("%d.%d.%d", n[0], n[1], n[2]), nil
}

// VersionOf returns the semver version held in content, a version file named
// name: a plain VERSION file (the trimmed content) or a JSON file with a
// top-level "version" string.
func VersionOf(name string, content []byte) (string, error) {
	var v string
	switch {
	case filepath.Base(name) == "VERSION":
		v = strings.TrimSpace(string(content))
	case strings.EqualFold(filepath.Ext(name), ".json"):
		start, end, err := jsonVersionSpan(content)
		if err != nil {
			return "", fmt.Errorf("%s: %w", name, err)
		}
		v = string(content[start:end])
	default:
		return "", fmt.Errorf("%s: unsupported version file (want VERSION or .json)", name)
	}
	if !semverRe.MatchString(v) {
		return "", fmt.Errorf("%s: version %q is not MAJOR.MINOR.PATCH", name, v)
	}
	return v, nil
}

// withVersion returns content with its version replaced by v. JSON files are
// edited in place (only the bytes of the top-level "version" value change),
// so formatting and key order are preserved.
func withVersion(name string, content []byte, v string) ([]byte, error) {
	if filepath.Base(name) == "VERSION" {
		return []byte(v + "\n"), nil
	}
	start, end, err := jsonVersionSpan(content)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	out := make([]byte, 0, len(content)+len(v))
	out = append(out, content[:start]...)
	out = append(out, v...)
	return append(out, content[end:]...), nil
}

// jsonVersionSpan returns the byte range of the raw string contents (inside
// the quotes) of the top-level "version" key of a JSON object.
func jsonVersionSpan(content []byte) (start, end int, err error) {
	dec := json.NewDecoder(bytes.NewReader(content))
	tok, err := dec.Token()
	if err != nil {
		return 0, 0, fmt.Errorf("parsing JSON: %w", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return 0, 0, errors.New("JSON version file must be an object")
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return 0, 0, fmt.Errorf("parsing JSON: %w", err)
		}
		key, _ := keyTok.(string)
		before := int(dec.InputOffset())
		valTok, err := dec.Token()
		if err != nil {
			return 0, 0, fmt.Errorf("parsing JSON: %w", err)
		}
		if _, ok := valTok.(json.Delim); ok {
			if err := skipJSON(dec); err != nil {
				return 0, 0, err
			}
			if key == "version" {
				return 0, 0, errors.New(`top-level "version" is not a string`)
			}
			continue
		}
		if key != "version" {
			continue
		}
		s, ok := valTok.(string)
		if !ok {
			return 0, 0, errors.New(`top-level "version" is not a string`)
		}
		after := int(dec.InputOffset())
		raw := content[before:after]
		open, closeQ := bytes.IndexByte(raw, '"'), bytes.LastIndexByte(raw, '"')
		if open < 0 || closeQ <= open || string(raw[open+1:closeQ]) != s {
			return 0, 0, errors.New(`top-level "version" uses escapes; edit it by hand`)
		}
		return before + open + 1, before + closeQ, nil
	}
	return 0, 0, errors.New(`no top-level "version" key`)
}

// skipJSON consumes the rest of the array or object whose opening delimiter
// was just read.
func skipJSON(dec *json.Decoder) error {
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err == io.EOF {
			return errors.New("parsing JSON: unexpected end")
		}
		if err != nil {
			return fmt.Errorf("parsing JSON: %w", err)
		}
		if dd, ok := tok.(json.Delim); ok {
			switch dd {
			case '{', '[':
				depth++
			default:
				depth--
			}
		}
	}
	return nil
}

// versionFilePath resolves rel inside repoDir, refusing paths outside it.
func versionFilePath(repoDir, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) {
		return "", fmt.Errorf("version file %q is outside the repository", rel)
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("version file %q is outside the repository", rel)
	}
	return filepath.Join(repoDir, clean), nil
}

// ReadVersion returns the version shared by every file in files (relative to
// repoDir). It errors with "version files disagree: ..." when they differ.
func ReadVersion(repoDir string, files []string) (string, error) {
	_, v, err := readVersions(repoDir, files)
	return v, err
}

func readVersions(repoDir string, files []string) (map[string][]byte, string, error) {
	if len(files) == 0 {
		return nil, "", ErrNoVersionFiles
	}
	contents := make(map[string][]byte, len(files))
	versions := make([]string, len(files))
	for i, f := range files {
		p, err := versionFilePath(repoDir, f)
		if err != nil {
			return nil, "", err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, "", fmt.Errorf("reading version file %s: %w", f, err)
		}
		if versions[i], err = VersionOf(f, b); err != nil {
			return nil, "", err
		}
		contents[f] = b
	}
	for _, v := range versions[1:] {
		if v != versions[0] {
			parts := make([]string, len(files))
			for i, f := range files {
				parts[i] = f + "=" + versions[i]
			}
			return nil, "", fmt.Errorf("version files disagree: %s", strings.Join(parts, ", "))
		}
	}
	return contents, versions[0], nil
}

// ApplyBump bumps the version held in every file of files (relative to
// repoDir) by bump (patch, minor or major) and returns the new version. All
// files must hold the same semver version; nothing is written otherwise.
func ApplyBump(repoDir string, files []string, bump string) (newVersion string, err error) {
	contents, cur, err := readVersions(repoDir, files)
	if err != nil {
		return "", err
	}
	next, err := NextVersion(cur, bump)
	if err != nil {
		return "", err
	}
	updated := make(map[string][]byte, len(files))
	for _, f := range files {
		if updated[f], err = withVersion(f, contents[f], next); err != nil {
			return "", err
		}
	}
	for _, f := range files {
		p, _ := versionFilePath(repoDir, f)
		info, err := os.Stat(p)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(p, updated[f], info.Mode().Perm()); err != nil {
			return "", fmt.Errorf("writing version file %s: %w", f, err)
		}
	}
	return next, nil
}

// ReviewBump returns the bump kind (patch, minor or major) a review
// artifact's frontmatter requests, quotes trimmed.
func ReviewBump(fm map[string]string) (string, error) {
	switch b := unquote(fm["bump"]); b {
	case "patch", "minor", "major":
		return b, nil
	default:
		return "", errors.New("review.md must set bump: patch|minor|major")
	}
}
