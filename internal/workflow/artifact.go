package workflow

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strings"
)

var statusLineRe = regexp.MustCompile(`^status:\s*.*$`)

// normalize converts CRLF line endings to LF.
func normalize(content []byte) []byte {
	return bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n"))
}

// frontmatterEnd returns the index (into lines) of the closing "---" of a
// leading frontmatter block, or -1 if there is none. lines[0] must be "---".
func frontmatterEnd(lines []string) int {
	if len(lines) == 0 || lines[0] != "---" {
		return -1
	}
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			return i
		}
	}
	return -1
}

// ArtifactHash returns the lowercase hex sha256 of content after normalising
// CRLF to LF and removing the first "status:" line inside the leading
// frontmatter block, so approving an artifact does not change its hash.
func ArtifactHash(content []byte) string {
	text := string(normalize(content))
	lines := strings.Split(text, "\n")
	if end := frontmatterEnd(lines); end > 0 {
		for i := 1; i < end; i++ {
			if statusLineRe.MatchString(lines[i]) {
				lines = append(lines[:i:i], lines[i+1:]...)
				break
			}
		}
		text = strings.Join(lines, "\n")
	}
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// ReadArtifact reads the artifact at path and returns its raw content, its
// ArtifactHash, and the "key: value" pairs of its leading frontmatter block.
// fm is empty (never nil) when the file has no frontmatter.
func ReadArtifact(path string) (content []byte, sha string, fm map[string]string, err error) {
	content, err = os.ReadFile(path)
	if err != nil {
		return nil, "", nil, fmt.Errorf("reading artifact %s: %w", path, err)
	}
	fm = make(map[string]string)
	lines := strings.Split(string(normalize(content)), "\n")
	if end := frontmatterEnd(lines); end > 0 {
		for _, line := range lines[1:end] {
			key, value, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			fm[key] = strings.TrimSpace(value)
		}
	}
	return content, ArtifactHash(content), fm, nil
}
