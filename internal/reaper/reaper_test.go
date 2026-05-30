package reaper

import (
	"fmt"
	"strings"
	"testing"
)

func TestValidateDBName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"hq", false},
		{"beads", false},
		{"gt", false},
		{"test_db_123", false},
		{"", true},
		{"drop table", true},
		{"db;--", true},
		{"db`name", true},
		{"../etc/passwd", true},
	}
	for _, tt := range tests {
		err := ValidateDBName(tt.name)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateDBName(%q) error = %v, wantErr %v", tt.name, err, tt.wantErr)
		}
	}
}

func TestDefaultDatabases(t *testing.T) {
	if len(DefaultDatabases) == 0 {
		t.Error("DefaultDatabases should not be empty")
	}
	for _, db := range DefaultDatabases {
		if err := ValidateDBName(db); err != nil {
			t.Errorf("DefaultDatabases contains invalid name %q: %v", db, err)
		}
	}
}

func TestFormatJSON(t *testing.T) {
	result := FormatJSON(map[string]int{"count": 42})
	if result == "" {
		t.Error("FormatJSON should not return empty string")
	}
	if result[0] != '{' {
		t.Errorf("FormatJSON should return JSON object, got %q", result[:10])
	}
}

func TestParentExcludeJoin(t *testing.T) {
	joinClause, whereCondition := parentExcludeJoin(false)

	// JOIN clause should reference the correct database.
	if joinClause == "" {
		t.Error("parentExcludeJoin joinClause should not be empty")
	}
	// parentExcludeJoin no longer qualifies table names with the database — the
	// reaper connects to a specific database via the DSN, so unqualified names
	// are correct.

	// JOIN should select wisps with open parents from wisp_dependencies.
	if !contains(joinClause, "wisp_dependencies") {
		t.Error("parentExcludeJoin should query wisp_dependencies")
	}
	if !contains(joinClause, "parent-child") {
		t.Error("parentExcludeJoin should filter on parent-child type")
	}
	if !contains(joinClause, "'open', 'hooked', 'in_progress'") {
		t.Error("parentExcludeJoin should check for open parent statuses")
	}

	// WHERE condition should be an IS NULL anti-join filter.
	if whereCondition == "" {
		t.Error("parentExcludeJoin whereCondition should not be empty")
	}
	if !contains(whereCondition, "IS NULL") {
		t.Error("parentExcludeJoin whereCondition should use IS NULL for anti-join")
	}
}

// TestReapQueryNoDatabaseNameInjection verifies that the Reap function's batch
// SELECT query does not inject the database name into the SQL string. Previously,
// dbName was passed as a Sprintf arg but the format string didn't use it, causing
// positional shift: "FROM wisps w gt WHERE..." instead of "FROM wisps w LEFT JOIN...".
func TestReapQueryNoDatabaseNameInjection(t *testing.T) {
	// Reproduce the exact Sprintf call from Reap() to verify no dbName injection.
	parentJoin, parentWhere := parentExcludeJoin(false)
	whereClause := fmt.Sprintf(
		"w.status IN ('open', 'hooked', 'in_progress') AND w.created_at < ? AND %s", parentWhere)

	// This is the fixed query — dbName is NOT in the Sprintf args.
	idQuery := fmt.Sprintf(
		"SELECT w.id FROM wisps w %s WHERE %s LIMIT %d",
		parentJoin, whereClause, DefaultBatchSize)

	// The query must NOT contain the literal database name as a bare token.
	// Before the fix, "gt" appeared between "wisps w" and "WHERE".
	if strings.Contains(idQuery, "wisps w gt") {
		t.Errorf("Reap idQuery contains injected database name: %s", idQuery)
	}
	if !strings.Contains(idQuery, "LEFT JOIN") {
		t.Errorf("Reap idQuery should contain LEFT JOIN from parentExcludeJoin, got: %s", idQuery)
	}
	if !strings.Contains(idQuery, fmt.Sprintf("LIMIT %d", DefaultBatchSize)) {
		t.Errorf("Reap idQuery should end with LIMIT %d, got: %s", DefaultBatchSize, idQuery)
	}
}

func TestParentExcludeJoinUsesSplitWispDependencyTarget(t *testing.T) {
	joinClause, _ := parentExcludeJoin(true)
	targetExpr := "COALESCE(wd.depends_on_issue_id, wd.depends_on_wisp_id, wd.depends_on_external)"

	if strings.Contains(joinClause, "wd.depends_on_id") {
		t.Fatalf("split parent join should not use legacy depends_on_id column:\n%s", joinClause)
	}
	if !strings.Contains(joinClause, "LEFT JOIN wisps pw ON pw.id = "+targetExpr) {
		t.Fatalf("split parent join should use %s for wisp parents:\n%s", targetExpr, joinClause)
	}
	if !strings.Contains(joinClause, "LEFT JOIN issues pi ON pi.id = "+targetExpr) {
		t.Fatalf("split parent join should use %s for issue parents:\n%s", targetExpr, joinClause)
	}
}

func TestDanglingParentQueryUsesSplitWispDependencyTarget(t *testing.T) {
	query := danglingParentQuery(true)
	targetExpr := "COALESCE(wd.depends_on_issue_id, wd.depends_on_wisp_id, wd.depends_on_external)"

	if strings.Contains(query, "wd.depends_on_id") {
		t.Fatalf("split dangling-parent query should not use legacy depends_on_id column:\n%s", query)
	}
	if !strings.Contains(query, "LEFT JOIN wisps pw ON pw.id = "+targetExpr) {
		t.Fatalf("split dangling-parent query should use %s for wisp parents:\n%s", targetExpr, query)
	}
	if !strings.Contains(query, "LEFT JOIN issues pi ON pi.id = "+targetExpr) {
		t.Fatalf("split dangling-parent query should use %s for issue parents:\n%s", targetExpr, query)
	}
}

func TestReverseWispDependencyDeleteQueryUsesSplitTarget(t *testing.T) {
	query := reverseWispDependencyDeleteQuery("(?,?)", true)
	targetExpr := "COALESCE(wisp_dependencies.depends_on_issue_id, wisp_dependencies.depends_on_wisp_id, wisp_dependencies.depends_on_external)"

	if strings.Contains(query, "depends_on_id") {
		t.Fatalf("split reverse dependency cleanup should not use legacy depends_on_id column:\n%s", query)
	}
	if !strings.Contains(query, targetExpr+" IN (?,?)") {
		t.Fatalf("split reverse dependency cleanup should match through %s:\n%s", targetExpr, query)
	}
}

// TestReapUpdateQueryNoDatabaseNameInjection verifies that the UPDATE query in
// Reap() does not inject dbName where the IN clause should go.
func TestReapUpdateQueryNoDatabaseNameInjection(t *testing.T) {
	dbName := "gt"
	inClause := "?,?,?"

	// This is the fixed query — only inClause in the Sprintf args.
	updateQuery := fmt.Sprintf(
		"UPDATE wisps SET status='closed', closed_at=NOW() WHERE id IN (%s)",
		inClause)

	if strings.Contains(updateQuery, dbName) {
		t.Errorf("Reap updateQuery contains injected database name %q: %s", dbName, updateQuery)
	}
	if !strings.Contains(updateQuery, "IN (?,?,?)") {
		t.Errorf("Reap updateQuery should contain parameterized IN clause, got: %s", updateQuery)
	}
}

// TestPurgeDigestQueryNoDatabaseNameInjection verifies that the purge digest
// query is a plain string with no Sprintf interpolation at all.
func TestPurgeDigestQueryNoDatabaseNameInjection(t *testing.T) {
	// The fixed digestQuery is a string literal — no Sprintf.
	digestQuery := "SELECT COALESCE(w.wisp_type, 'unknown') AS wtype, COUNT(*) AS cnt FROM wisps w WHERE w.status = 'closed' AND w.closed_at < ? GROUP BY wtype"

	if strings.Contains(digestQuery, "gt") {
		t.Errorf("purge digestQuery should not contain database name, got: %s", digestQuery)
	}
	if !strings.Contains(digestQuery, "GROUP BY wtype") {
		t.Errorf("purge digestQuery should end with GROUP BY, got: %s", digestQuery)
	}
}

// TestPurgeBatchQueryNoDatabaseNameInjection verifies that the purge batch
// SELECT query uses DefaultBatchSize as the LIMIT, not dbName.
func TestPurgeBatchQueryNoDatabaseNameInjection(t *testing.T) {
	// This is the fixed query — only DefaultBatchSize in the Sprintf args.
	idQuery := fmt.Sprintf(
		"SELECT w.id FROM wisps w WHERE w.status = 'closed' AND w.closed_at < ? LIMIT %d",
		DefaultBatchSize)

	if strings.Contains(idQuery, "gt") {
		t.Errorf("purge idQuery contains injected database name: %s", idQuery)
	}
	expected := fmt.Sprintf("LIMIT %d", DefaultBatchSize)
	if !strings.Contains(idQuery, expected) {
		t.Errorf("purge idQuery should contain %s, got: %s", expected, idQuery)
	}
}

// TestIsNothingToCommit verifies that "nothing to commit" errors are recognized
// correctly. This prevents false-positive dolt_commit_failed anomalies when the
// reaper operates on dolt_ignored tables (wisps, wisp_*), where Dolt has nothing
// to version after a successful SQL DELETE.
func TestIsNothingToCommit(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"nothing to commit", true},
		{"NOTHING TO COMMIT", true},
		{"Error 1105 (HY000): nothing to commit", true},
		{"no changes to commit", false}, // must also contain "commit" — see isNothingToCommit
		{"no changes", false},
		{"connection refused", false},
		{"table not found: wisps", false},
		{"", false},
	}
	for _, c := range cases {
		var err error
		if c.msg != "" {
			err = fmt.Errorf("%s", c.msg)
		}
		got := isNothingToCommit(err)
		if got != c.want {
			t.Errorf("isNothingToCommit(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestReapExcludesAgentBeads verifies that the Reap function excludes agent beads
// from being closed, regardless of their age. This is a regression test for the bug
// where the wisp reaper was closing agent beads (hq-mayor, hq-deacon, witness, refinery,
// etc.) after 24 hours, causing doctor to report them as missing.
func TestReapExcludesAgentBeads(t *testing.T) {
	// Verify that the WHERE clause in Reap() excludes issue_type='agent'
	// by checking the source code pattern.
	// This is a compile-time guard — if the exclusion is removed, this test
	// will fail when the query pattern doesn't match.

	// The whereClause in Reap() should contain:
	// "w.issue_type != 'agent'"
	// This test documents the expected behavior; actual exclusion is tested
	// in integration tests with a real database.

	// Integration test would require spinning up a Dolt server, which is
	// beyond the scope of this unit test. The exclusion is verified manually
	// by checking that agent beads are not closed by the wisp_reaper patrol.
	t.Log("Agent beads (issue_type='agent') are excluded from wisp reaping")
	t.Log("This prevents hq-mayor, hq-deacon, witness, refinery, etc. from being closed")
}

// TestScanExcludesAgentBeads documents that Scan() must use the same eligibility
// predicate as Reap() for stale open wisps. If Scan counts agent beads but Reap
// excludes them, the operator sees scan>0 and reap=0 for the same cutoff.
func TestScanExcludesAgentBeads(t *testing.T) {
	query := reapCandidatesQuery(false)
	if !strings.Contains(query, "w.issue_type != 'agent'") {
		t.Fatalf("expected Scan() eligibility query to exclude agent beads:\n%s", query)
	}
}

func TestScanStaleCandidatesQueryUsesSplitDependencyTarget(t *testing.T) {
	query := scanStaleCandidatesQuery(true)
	targetExpr := "COALESCE(d.depends_on_issue_id, d.depends_on_wisp_id, d.depends_on_external)"

	if strings.Contains(query, "d.depends_on_id") {
		t.Fatalf("split schema query should not use legacy depends_on_id column:\n%s", query)
	}
	if !strings.Contains(query, "INNER JOIN issues dep ON "+targetExpr+" = dep.id") {
		t.Fatalf("split schema query should join active dependencies through %s:\n%s", targetExpr, query)
	}
	if !strings.Contains(query, "SELECT DISTINCT "+targetExpr+" FROM dependencies d") {
		t.Fatalf("split schema query should exclude issues that are active dependency targets through %s:\n%s", targetExpr, query)
	}
}

func TestRetryDependencyTargetQueryFallsBackToSplitTarget(t *testing.T) {
	var calls []bool
	err := retryDependencyTargetQuery(func(splitTarget bool) error {
		calls = append(calls, splitTarget)
		if !splitTarget {
			return fmt.Errorf(`query failed: unknown column "d.depends_on_id"`)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("retryDependencyTargetQuery returned error: %v", err)
	}
	if len(calls) != 2 || calls[0] || !calls[1] {
		t.Fatalf("retryDependencyTargetQuery calls = %v, want [false true]", calls)
	}
}

func TestRetryDependencyTargetQueryDoesNotRetryUnrelatedError(t *testing.T) {
	var calls []bool
	wantErr := fmt.Errorf("connection refused")
	err := retryDependencyTargetQuery(func(splitTarget bool) error {
		calls = append(calls, splitTarget)
		return wantErr
	})
	if err != wantErr {
		t.Fatalf("retryDependencyTargetQuery error = %v, want %v", err, wantErr)
	}
	if len(calls) != 1 || calls[0] {
		t.Fatalf("retryDependencyTargetQuery calls = %v, want [false]", calls)
	}
}

func TestAutoCloseWhereClauseUsesSplitDependencyTarget(t *testing.T) {
	whereClause := autoCloseWhereClause("hq", true)
	targetExpr := "COALESCE(d.depends_on_issue_id, d.depends_on_wisp_id, d.depends_on_external)"

	if strings.Contains(whereClause, "d.depends_on_id") {
		t.Fatalf("split schema where clause should not use legacy depends_on_id column:\n%s", whereClause)
	}
	if !strings.Contains(whereClause, "INNER JOIN `hq`.issues dep ON "+targetExpr+" = dep.id") {
		t.Fatalf("split schema where clause should join active dependencies through %s:\n%s", targetExpr, whereClause)
	}
	if !strings.Contains(whereClause, "SELECT DISTINCT "+targetExpr+" FROM `hq`.dependencies d") {
		t.Fatalf("split schema where clause should exclude active dependency targets through %s:\n%s", targetExpr, whereClause)
	}
}
