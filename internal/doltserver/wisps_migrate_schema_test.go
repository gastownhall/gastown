package doltserver

import (
	"os"
	"strings"
	"testing"
)

func TestWispDependenciesCreateDDLUsesTypedTargets(t *testing.T) {
	if strings.Contains(wispDependenciesCreateDDL, "depends_on_id") {
		t.Fatalf("wisp_dependencies DDL should not create legacy depends_on_id:\n%s", wispDependenciesCreateDDL)
	}
	for _, want := range []string{
		"id char(36)",
		"depends_on_issue_id varchar(255)",
		"depends_on_wisp_id varchar(255)",
		"depends_on_external varchar(255)",
		"UNIQUE KEY uk_wisp_dep_issue_target",
		"UNIQUE KEY uk_wisp_dep_wisp_target",
		"UNIQUE KEY uk_wisp_dep_external_target",
		"CONSTRAINT ck_wisp_dep_one_target",
	} {
		if !strings.Contains(wispDependenciesCreateDDL, want) {
			t.Fatalf("wisp_dependencies DDL missing %q:\n%s", want, wispDependenciesCreateDDL)
		}
	}
}

func TestCopyAuxiliaryDataUsesTypedDependencyTargets(t *testing.T) {
	data, err := os.ReadFile("wisps_migrate.go")
	if err != nil {
		t.Fatalf("read wisps_migrate.go: %v", err)
	}
	body := doltserverSourceBetween(t, string(data), "func copyAuxiliaryData(", "// deriveDBName")
	if strings.Contains(body, "depends_on_id") {
		t.Fatalf("copyAuxiliaryData should not copy legacy depends_on_id:\n%s", body)
	}
	for _, want := range []string{
		"depends_on_issue_id, depends_on_wisp_id, depends_on_external",
		"CASE WHEN target_wisp.id IS NULL THEN d.depends_on_issue_id ELSE NULL END",
		"CASE WHEN target_wisp.id IS NOT NULL THEN d.depends_on_issue_id ELSE d.depends_on_wisp_id END",
		"LEFT JOIN wisps target_wisp ON target_wisp.id = d.depends_on_issue_id",
		"UPDATE wisp_dependencies wd INNER JOIN wisps target_wisp ON target_wisp.id = wd.depends_on_issue_id",
		"UPDATE dependencies d INNER JOIN wisps target_wisp ON target_wisp.id = d.depends_on_issue_id",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("copyAuxiliaryData missing %q:\n%s", want, body)
		}
	}
}

func doltserverSourceBetween(t *testing.T, source, startMarker, endMarker string) string {
	t.Helper()
	start := strings.Index(source, startMarker)
	if start == -1 {
		t.Fatalf("could not find %q", startMarker)
	}
	end := strings.Index(source[start:], endMarker)
	if end == -1 {
		t.Fatalf("could not find %q after %q", endMarker, startMarker)
	}
	return source[start : start+end]
}
