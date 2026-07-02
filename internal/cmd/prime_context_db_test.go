package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

func TestContextDBURL_GateDefaultOff(t *testing.T) {
	t.Setenv("CONTEXT_DB_URL", "")
	if got := contextDBURL(); got != "" {
		t.Fatalf("contextDBURL() = %q, want empty (feature disabled by default)", got)
	}
	t.Setenv("CONTEXT_DB_URL", "  http://localhost:8080  ")
	if got := contextDBURL(); got != "http://localhost:8080" {
		t.Fatalf("contextDBURL() = %q, want trimmed URL", got)
	}
}

func TestContextDBTopK(t *testing.T) {
	cases := []struct {
		env  string
		want int
	}{
		{"", contextDBDefaultTopK},
		{"3", 3},
		{"not-a-number", contextDBDefaultTopK},
		{"0", 1},                  // clamped up
		{"-5", 1},                 // clamped up
		{"999", contextDBMaxTopK}, // clamped down
	}
	for _, tc := range cases {
		t.Setenv("CONTEXT_DB_TOP_K", tc.env)
		if got := contextDBTopK(); got != tc.want {
			t.Errorf("contextDBTopK() with env %q = %d, want %d", tc.env, got, tc.want)
		}
	}
}

func TestRenderContextSeed_Empty(t *testing.T) {
	if got := renderContextSeed(nil); got != "" {
		t.Errorf("renderContextSeed(nil) = %q, want empty", got)
	}
	if got := renderContextSeed([]contextDBHit{}); got != "" {
		t.Errorf("renderContextSeed([]) = %q, want empty", got)
	}
}

func TestRenderContextSeed_BannerAndFields(t *testing.T) {
	hits := []contextDBHit{
		{
			ConceptID:  "gastown.dispatch.sling",
			Layer:      "rig",
			Rigs:       []string{"gastown-prime"},
			Summary:    "gt sling resolves the rig and spawns a polecat",
			UpdatedAt:  "2026-06-18",
			Provenance: map[string]any{"source": "archivist"},
		},
	}
	out := renderContextSeed(hits)

	wantSubstrings := []string{
		"Retrieved Domain Context (orientation only)",
		"VERIFY AT THE SOURCE", // standing banner (invariant I1)
		"gastown.dispatch.sling",
		"rigs: gastown-prime",
		"updated_at: 2026-06-18",
		"source: archivist",
		"gt sling resolves the rig",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(out, want) {
			t.Errorf("rendered seed missing %q\n--- got ---\n%s", want, out)
		}
	}
}

func TestRenderContextSeed_UniversalConceptAndBodyFallback(t *testing.T) {
	// No rigs => "universal"; no summary => falls back to body; whitespace collapsed.
	hits := []contextDBHit{
		{
			ConceptID: "core.invariant",
			Layer:     "universal",
			Body:      "line one\n\n   line two   with   spaces",
		},
	}
	out := renderContextSeed(hits)
	if !strings.Contains(out, "rigs: universal") {
		t.Errorf("expected universal label, got:\n%s", out)
	}
	if !strings.Contains(out, "line one line two with spaces") {
		t.Errorf("expected collapsed body fallback, got:\n%s", out)
	}
}

func TestRenderContextSeed_TokenCap(t *testing.T) {
	// Many large hits must not blow past the budget unboundedly: once exceeded,
	// rendering stops and emits the omission marker.
	var hits []contextDBHit
	big := strings.Repeat("x", contextDBMaxSnippet)
	for i := 0; i < 50; i++ {
		hits = append(hits, contextDBHit{ConceptID: "c", Layer: "rig", Summary: big})
	}
	out := renderContextSeed(hits)
	if !strings.Contains(out, "token budget") {
		t.Errorf("expected token-budget omission marker for oversized seed")
	}
	// Sanity: output is bounded near the cap (allow header + one overflow hit).
	if len(out) > contextDBMaxTotal+contextDBMaxSnippet+512 {
		t.Errorf("rendered seed length %d exceeds expected bound", len(out))
	}
}

func TestRenderContextSeed_SnippetTruncated(t *testing.T) {
	long := strings.Repeat("a", contextDBMaxSnippet+100)
	out := renderContextSeed([]contextDBHit{{ConceptID: "c", Layer: "rig", Summary: long}})
	if !strings.Contains(out, "…") {
		t.Errorf("expected truncation ellipsis for long snippet")
	}
	if strings.Contains(out, long) {
		t.Errorf("full long snippet should not appear untruncated")
	}
}

func TestFetchContextDBSeed_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %q", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"concept_id":"a","score":0.9,"layer":"rig","rigs":["gastown-prime"],"summary":"hi","body":"b","updated_at":"2026-06-18","provenance":{"source":"archivist"}}]`))
	}))
	defer srv.Close()

	hits, err := fetchContextDBSeed(context.Background(), srv.URL, "test query", "gastown-prime", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hits) != 1 || hits[0].ConceptID != "a" {
		t.Fatalf("unexpected hits: %+v", hits)
	}
	if hits[0].Provenance["source"] != "archivist" {
		t.Errorf("provenance not decoded: %+v", hits[0].Provenance)
	}
}

func TestFetchContextDBSeed_GracefulDegradation(t *testing.T) {
	t.Run("non-200", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		defer srv.Close()
		if _, err := fetchContextDBSeed(context.Background(), srv.URL, "q", "", 5); err == nil {
			t.Error("expected error on non-200, got nil")
		}
	})

	t.Run("connection refused", func(t *testing.T) {
		// Unreachable address — must error, never hang the caller.
		_, err := fetchContextDBSeed(context.Background(), "http://127.0.0.1:0", "q", "", 5)
		if err == nil {
			t.Error("expected error on unreachable host, got nil")
		}
	})

	t.Run("timeout honored", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(200 * time.Millisecond)
			w.Write([]byte(`[]`))
		}))
		defer srv.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		start := time.Now()
		if _, err := fetchContextDBSeed(ctx, srv.URL, "q", "", 5); err == nil {
			t.Error("expected timeout error, got nil")
		}
		if elapsed := time.Since(start); elapsed > 150*time.Millisecond {
			t.Errorf("fetch did not respect timeout, took %v", elapsed)
		}
	})

	t.Run("bad json", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`not json`))
		}))
		defer srv.Close()
		if _, err := fetchContextDBSeed(context.Background(), srv.URL, "q", "", 5); err == nil {
			t.Error("expected decode error, got nil")
		}
	})
}

func TestFetchContextDBSeed_RigFilter(t *testing.T) {
	// Empty rig => no "rig" key in filters; non-empty => present.
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		r.Body.Read(buf)
		gotBody = string(buf)
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	fetchContextDBSeed(context.Background(), srv.URL, "q", "gastown-prime", 5)
	if !strings.Contains(gotBody, `"rig":"gastown-prime"`) {
		t.Errorf("expected rig filter in body, got: %s", gotBody)
	}

	fetchContextDBSeed(context.Background(), srv.URL, "q", "", 5)
	if strings.Contains(gotBody, `"rig"`) {
		t.Errorf("expected no rig filter for empty rig, got: %s", gotBody)
	}
}

func TestOutputContextDBSeed_DisabledIsNoop(t *testing.T) {
	// With the gate off, even a non-nil bead produces no panic / no side effects.
	t.Setenv("CONTEXT_DB_URL", "")
	outputContextDBSeed(RoleContext{Rig: "gastown-prime"}, &beads.Issue{ID: "x", Title: "t"})
	// Reaching here without contacting any server is the assertion.
}

func TestBuildContextDBQuery(t *testing.T) {
	// Work pull with a rig: rig-anchored, title leads, description follows.
	b := &beads.Issue{Title: "Fix sling", Description: "do the thing"}
	if got := buildContextDBQuery(b, "gastown-prime", queryKindWork); got != "rig: gastown-prime\nFix sling\ndo the thing" {
		t.Errorf("buildContextDBQuery(work) = %q", got)
	}

	// No rig => no anchor line, title still leads.
	if got := buildContextDBQuery(b, "", queryKindWork); got != "Fix sling\ndo the thing" {
		t.Errorf("buildContextDBQuery(no rig) = %q", got)
	}

	// Query is capped to contextDBMaxQuery regardless of role.
	long := &beads.Issue{Title: "T", Description: strings.Repeat("d", contextDBMaxQuery*2)}
	if got := buildContextDBQuery(long, "r", queryKindWork); len(got) > contextDBMaxQuery {
		t.Errorf("query not capped: len=%d", len(got))
	}

	// Empty bead (no title, no description) => no pull, even with a rig.
	empty := &beads.Issue{}
	if got := buildContextDBQuery(empty, "gastown-prime", queryKindWork); got != "" {
		t.Errorf("buildContextDBQuery(empty) = %q, want empty", got)
	}
}

func TestBuildContextDBQuery_ReviewTrimsDescription(t *testing.T) {
	longDesc := strings.Repeat("d", contextDBReviewMaxDesc*2)
	b := &beads.Issue{Title: "Review PR #12", Description: longDesc}

	work := buildContextDBQuery(b, "gastown-prime", queryKindWork)
	review := buildContextDBQuery(b, "gastown-prime", queryKindReview)

	// Review keeps the scope (rig + title) but trims the verbose description harder.
	if !strings.Contains(review, "rig: gastown-prime") || !strings.Contains(review, "Review PR #12") {
		t.Errorf("review query dropped scope: %q", review)
	}
	if len(review) >= len(work) {
		t.Errorf("review query (%d) should be shorter than work query (%d)", len(review), len(work))
	}
	// The description portion of the review query is bounded by contextDBReviewMaxDesc.
	if got := strings.Count(review, "d"); got > contextDBReviewMaxDesc {
		t.Errorf("review description not trimmed: %d d's, want <= %d", got, contextDBReviewMaxDesc)
	}
}

func TestContextDBQueryKindFromFormula(t *testing.T) {
	cases := []struct {
		formula string
		want    contextDBQueryKind
	}{
		{"", queryKindWork},
		{"mol-polecat-work", queryKindWork},
		{"mol-polecat-work-tdd", queryKindWork},
		{"mol-foreman-pr-response", queryKindReview},
		{"mol-code-review", queryKindReview},
		{"mol-pr-feedback", queryKindReview},
		{"mol-plan-prd", queryKindPlan},
		{"mol-planning", queryKindPlan},
		// review wins over plan when both substrings appear
		{"mol-plan-review", queryKindReview},
	}
	for _, tc := range cases {
		if got := contextDBQueryKindFromFormula(tc.formula); got != tc.want {
			t.Errorf("contextDBQueryKindFromFormula(%q) = %d, want %d", tc.formula, got, tc.want)
		}
	}
}

func TestContextDBTopKFor(t *testing.T) {
	// Role defaults (no env override).
	t.Setenv("CONTEXT_DB_TOP_K", "")
	roleCases := []struct {
		kind contextDBQueryKind
		want int
	}{
		{queryKindWork, contextDBDefaultTopK},
		{queryKindReview, contextDBReviewTopK},
		{queryKindPlan, contextDBPlanTopK},
	}
	for _, tc := range roleCases {
		if got := contextDBTopKFor(tc.kind); got != tc.want {
			t.Errorf("contextDBTopKFor(%d) = %d, want %d", tc.kind, got, tc.want)
		}
	}

	// Explicit env override wins over the role default, still clamped.
	t.Setenv("CONTEXT_DB_TOP_K", "7")
	for _, kind := range []contextDBQueryKind{queryKindWork, queryKindReview, queryKindPlan} {
		if got := contextDBTopKFor(kind); got != 7 {
			t.Errorf("contextDBTopKFor(%d) with override = %d, want 7", kind, got)
		}
	}
	t.Setenv("CONTEXT_DB_TOP_K", "999")
	if got := contextDBTopKFor(queryKindPlan); got != contextDBMaxTopK {
		t.Errorf("override not clamped: got %d, want %d", got, contextDBMaxTopK)
	}
}

func TestDedupeContextDBHits(t *testing.T) {
	hits := []contextDBHit{
		{ConceptID: "a", Summary: "first a"},
		{ConceptID: "b", Summary: "b"},
		{ConceptID: "a", Summary: "second a (dropped)"},
		{ConceptID: "", Summary: "empty id 1"},
		{ConceptID: "", Summary: "empty id 2"},
	}
	got := dedupeContextDBHits(hits)
	// a, b, and both empty-id hits survive (empty ids are not collapsed).
	if len(got) != 4 {
		t.Fatalf("dedupeContextDBHits() len = %d, want 4", len(got))
	}
	if got[0].ConceptID != "a" || got[0].Summary != "first a" {
		t.Errorf("dedupe should keep first occurrence, got %+v", got[0])
	}
	if got[1].ConceptID != "b" {
		t.Errorf("dedupe reordered hits: %+v", got)
	}

	// Nil / single-element inputs pass through untouched.
	if got := dedupeContextDBHits(nil); got != nil {
		t.Errorf("dedupeContextDBHits(nil) = %v, want nil", got)
	}
	single := []contextDBHit{{ConceptID: "a"}}
	if got := dedupeContextDBHits(single); len(got) != 1 {
		t.Errorf("dedupeContextDBHits(single) len = %d, want 1", len(got))
	}
}
