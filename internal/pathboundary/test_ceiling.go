package pathboundary

import (
	"os"
	"path/filepath"
	"strings"
)

// TestCeiling returns the highest ancestor a hermetic test may inspect.
// It is inactive unless the repository-owned test wrapper sets the variable,
// and only applies to paths contained by that ceiling.
func TestCeiling(start string) string {
	raw := os.Getenv("GT_TEST_DISCOVERY_CEILING")
	if raw == "" {
		return ""
	}
	start, err := filepath.Abs(start)
	if err != nil {
		return ""
	}
	ceiling, err := filepath.Abs(raw)
	if err != nil {
		return ""
	}
	start = filepath.Clean(start)
	ceiling = filepath.Clean(ceiling)
	rel, err := filepath.Rel(ceiling, start)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return ceiling
}
