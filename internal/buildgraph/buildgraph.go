// Package buildgraph classifies Go import paths that must stay out of the
// default gt compile graph.
//
// Gas Town talks to Dolt as an external SQL server. Compiling beads' CGO
// embedded Dolt engine, testcontainers, or a browser driver into ./cmd/gt
// is a compile-time regression, not a feature.
package buildgraph

import "strings"

// ProductionForbiddenPrefixes must not appear in the CGO-disabled ./cmd/gt
// dependency graph. Each prefix is a known compile-time cliff:
//
//   - github.com/dolthub/ is the embedded Dolt engine (and its cloud SDKs)
//   - github.com/testcontainers/ is the Docker test harness
//   - github.com/go-rod/ is browser automation used only by e2e tests
var ProductionForbiddenPrefixes = []string{
	"github.com/dolthub/",
	"github.com/testcontainers/",
	"github.com/go-rod/",
}

// MatchingPrefixes returns the import paths that start with any prefix.
// Order of deps is preserved. Each dep appears at most once.
func MatchingPrefixes(deps, prefixes []string) []string {
	var hits []string
	for _, dep := range deps {
		for _, prefix := range prefixes {
			if strings.HasPrefix(dep, prefix) {
				hits = append(hits, dep)
				break
			}
		}
	}
	return hits
}
