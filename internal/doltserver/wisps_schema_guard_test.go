package doltserver

import (
	"os"
	"strings"
	"testing"
)

func TestWispDependenciesDDLUsesTypedTargets(t *testing.T) {
	ddl := ""
	for _, table := range wispAuxTableDDLs {
		if table.name == "wisp_dependencies" {
			ddl = table.ddl
			break
		}
	}
	if ddl == "" {
		t.Fatal("wisp_dependencies DDL not found")
	}
	for _, want := range []string{"depends_on_issue_id", "depends_on_wisp_id", "depends_on_external", "uk_wisp_dep_wisp_target"} {
		if !strings.Contains(ddl, want) {
			t.Fatalf("wisp_dependencies DDL missing %q:\n%s", want, ddl)
		}
	}
	for _, legacy := range []string{"depends_on_id varchar", "PRIMARY KEY (issue_id, depends_on_id)", "idx_wisp_deps_depends_on"} {
		if strings.Contains(ddl, legacy) {
			t.Fatalf("wisp_dependencies DDL still contains legacy %q:\n%s", legacy, ddl)
		}
	}
}

func TestWispDependencyMigrationSQLUsesTypedTargets(t *testing.T) {
	data, err := os.ReadFile("wisps_migrate.go")
	if err != nil {
		t.Fatalf("read wisps_migrate.go: %v", err)
	}
	source := string(data)
	for _, want := range []string{
		"depends_on_issue_id, depends_on_wisp_id, depends_on_external",
		"target_wisp.id = d.depends_on_issue_id",
		"wisp_dependencies_legacy",
		"target_wisp.id = wd.depends_on_id",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("wisps migration source missing %q", want)
		}
	}
	if strings.Contains(source, "wisp_dependencies (issue_id, depends_on_id") {
		t.Fatal("wisps migration source still inserts legacy depends_on_id")
	}
}
