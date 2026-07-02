package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/style"
)

// Context-DB dispatch integration (READ path) — sbx-gastown-g2lww.
//
// When projecting a polecat's context, gt prime can inject a token-capped,
// SOURCED "Retrieved Domain Context" block fetched from the context-db search
// API (POST {CONTEXT_DB_URL}/search). This is orientation-only background; the
// agent must verify exact/current facts at the source (see the standing banner).
//
// SAFETY — this runs in the dispatch engine, so it is default-off and
// fail-open:
//
//   - Default-safe gate: the feature is active ONLY when CONTEXT_DB_URL is set.
//     Unset => this whole path is a no-op and dispatch behaves EXACTLY as before.
//   - Graceful degradation: any error (disabled, unreachable, timeout, non-200,
//     bad JSON, no hits) results in NO seed and NO error surfaced to the agent.
//     We never block dispatch, never add more than contextDBTimeout of latency,
//     and never fail a prime over the context-db.
const (
	// contextDBDefaultTopK is the number of concepts to retrieve for an
	// implement/work pull when CONTEXT_DB_TOP_K is not set.
	contextDBDefaultTopK = 5
	// contextDBReviewTopK is the role default for review pulls: a review keys on
	// the PR/scope subsystem, so it wants a tighter, more focused set.
	contextDBReviewTopK = 4
	// contextDBPlanTopK is the role default for plan pulls: a plan spans the
	// affected domains, so it wants broader orientation. Still bounded by the
	// render token cap (contextDBMaxTotal), so a larger count can't bloat output.
	contextDBPlanTopK = 8
	// contextDBMaxTopK clamps the configured top_k so a misconfiguration can't
	// pull an unbounded seed into the agent's context.
	contextDBMaxTopK = 20
	// contextDBTimeout is the hard ceiling on the /search round-trip. Kept well
	// under the ~2s dispatch-latency budget so a slow/hung db never stalls a
	// polecat launch.
	contextDBTimeout = 1500 * time.Millisecond
	// contextDBMaxQuery caps the query text (rig + bead title + description) sent
	// to the embedder. Long descriptions dilute the embedding and bloat the request.
	contextDBMaxQuery = 1200
	// contextDBReviewMaxDesc caps the description portion for a review pull. A
	// review keys on the PR/scope subsystem (the title), so the verbose work prose
	// is trimmed harder to keep the embedding focused on scope.
	contextDBReviewMaxDesc = 300
	// contextDBMaxSnippet caps each hit's body snippet (chars).
	contextDBMaxSnippet = 240
	// contextDBMaxTotal caps the whole rendered block (chars) as a coarse token
	// budget so the seed can never dominate the prime output.
	contextDBMaxTotal = 2400
)

// contextDBHit mirrors the SearchHit shape returned by the context-db API
// (context-db/api/app.py :: SearchHit). Only the fields the seed renders are
// decoded; unknown fields are ignored.
type contextDBHit struct {
	ConceptID  string         `json:"concept_id"`
	Score      float64        `json:"score"`
	Layer      string         `json:"layer"`
	Rigs       []string       `json:"rigs"`
	Summary    string         `json:"summary"`
	Body       string         `json:"body"`
	UpdatedAt  string         `json:"updated_at"`
	Provenance map[string]any `json:"provenance"`
}

// contextDBURL returns the configured context-db base URL, or "" when the
// feature is disabled. This is the single default-safe gate: with CONTEXT_DB_URL
// unset, every other function here short-circuits and prime behaves exactly as
// it did before this integration landed.
func contextDBURL() string {
	return strings.TrimSpace(os.Getenv("CONTEXT_DB_URL"))
}

// contextDBTopK resolves the work/default top_k. Retained for callers and tests
// that want the baseline (env override + clamp) without a role.
func contextDBTopK() int {
	return contextDBTopKFor(queryKindWork)
}

// contextDBTopKFor resolves the top_k for a given query kind. The role default
// (work=5, review=4, plan=8) is right-sized so the pull matches the role's need;
// an explicit CONTEXT_DB_TOP_K env override always wins over the role default.
// The result is clamped to [1, contextDBMaxTopK] so a misconfiguration can't pull
// an unbounded seed.
func contextDBTopKFor(kind contextDBQueryKind) int {
	k := contextDBDefaultTopK
	switch kind {
	case queryKindReview:
		k = contextDBReviewTopK
	case queryKindPlan:
		k = contextDBPlanTopK
	}
	if v := strings.TrimSpace(os.Getenv("CONTEXT_DB_TOP_K")); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			k = parsed // explicit override wins over the role default
		}
	}
	if k < 1 {
		k = 1
	}
	if k > contextDBMaxTopK {
		k = contextDBMaxTopK
	}
	return k
}

// contextDBDebugf logs a degradation reason to stderr only when CONTEXT_DB_DEBUG
// is set. The seed path is fail-open, so the default behavior is silence — a
// missing db must not produce noise that looks like a dispatch error.
func contextDBDebugf(format string, args ...any) {
	if os.Getenv("CONTEXT_DB_DEBUG") == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "context-db: "+format+"\n", args...)
}

// contextDBSearchRequest is the POST /search body
// ({query, filters:{rig}, top_k}).
type contextDBSearchRequest struct {
	Query   string         `json:"query"`
	Filters map[string]any `json:"filters"`
	TopK    int            `json:"top_k"`
}

// fetchContextDBSeed POSTs to {baseURL}/search and returns the hits. The caller
// is responsible for treating any error as "no seed" (graceful degradation);
// this function does not log or print on failure.
func fetchContextDBSeed(ctx context.Context, baseURL, query, rig string, topK int) ([]contextDBHit, error) {
	filters := map[string]any{}
	if rig != "" {
		filters["rig"] = rig
	}
	body, err := json.Marshal(contextDBSearchRequest{Query: query, Filters: filters, TopK: topK})
	if err != nil {
		return nil, fmt.Errorf("marshal search request: %w", err)
	}

	url := strings.TrimRight(baseURL, "/") + "/search"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("non-200 status: %s", resp.Status)
	}

	var hits []contextDBHit
	if err := json.NewDecoder(resp.Body).Decode(&hits); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return hits, nil
}

// contextDBSeedBanner is the standing, non-negotiable warning attached to every
// retrieved-context block (invariant I1 in context-db/TOUCHPOINTS.md).
const contextDBSeedBanner = "Orientation only — retrieved background, may be stale. For exact/current facts " +
	"(live infra, compliance status, prod data, secrets) VERIFY AT THE SOURCE; do not treat " +
	"retrieved chunks as authoritative current state."

// renderContextSeed assembles the token-capped, sourced "Retrieved Domain
// Context" block. Returns "" when there are no hits (caller prints nothing).
//
// Pure function (no I/O) so the assembly + capping logic is unit-testable.
func renderContextSeed(hits []contextDBHit) string {
	if len(hits) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", style.Bold.Render("## 📚 Retrieved Domain Context (orientation only)"))
	fmt.Fprintf(&b, "> %s\n\n", contextDBSeedBanner)

	for _, h := range hits {
		// Stop adding hits once the coarse char budget is exhausted, but always
		// emit at least the first hit so a single large concept still shows.
		if b.Len() > contextDBMaxTotal {
			fmt.Fprintf(&b, "_(additional concepts omitted — token budget)_\n")
			break
		}

		rigs := "universal"
		if len(h.Rigs) > 0 {
			rigs = strings.Join(h.Rigs, ", ")
		}
		layer := h.Layer
		if layer == "" {
			layer = "?"
		}
		fmt.Fprintf(&b, "- **%s** · %s · rigs: %s · updated_at: %s", h.ConceptID, layer, rigs, valueOrDash(h.UpdatedAt))
		if src := provenanceSource(h.Provenance); src != "" {
			fmt.Fprintf(&b, " · source: %s", src)
		}
		b.WriteByte('\n')

		snippet := h.Summary
		if snippet == "" {
			snippet = h.Body
		}
		snippet = strings.Join(strings.Fields(snippet), " ") // collapse whitespace
		if len(snippet) > contextDBMaxSnippet {
			snippet = snippet[:contextDBMaxSnippet] + "…"
		}
		if snippet != "" {
			fmt.Fprintf(&b, "  %s\n", snippet)
		}
	}
	return b.String()
}

// valueOrDash returns s, or "—" when s is empty (keeps the metadata line aligned).
func valueOrDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// provenanceSource extracts provenance.source from a hit, tolerating the
// untyped map shape the API returns.
func provenanceSource(prov map[string]any) string {
	if prov == nil {
		return ""
	}
	if s, ok := prov["source"].(string); ok {
		return s
	}
	return ""
}

// outputContextDBSeed is the orchestration entrypoint wired into prime's
// autonomous-work output. It is fail-open and default-off:
//
//   - Returns immediately (no output, no error) when the feature is disabled
//     (CONTEXT_DB_URL unset) or there is no hooked bead / query text.
//   - Fetches with a hard timeout; on ANY error renders nothing and the dispatch
//     proceeds normally.
//   - On success, prints the sourced block and logs the retrieved concept_ids
//     back onto the bead for reproducibility (skipped in dry-run).
func outputContextDBSeed(ctx RoleContext, hookedBead *beads.Issue) {
	baseURL := contextDBURL()
	if baseURL == "" {
		return // feature disabled — exact pre-integration behavior
	}
	if hookedBead == nil {
		return
	}

	// Shape the pull to the role: the attached formula tells us whether this is
	// implement (work), review, or plan, which drives both the query emphasis and
	// the top_k. Defaults to work when no formula is attached.
	kind := contextDBQueryKindFromFormula(attachedFormula(hookedBead))

	query := buildContextDBQuery(hookedBead, ctx.Rig, kind)
	if query == "" {
		return
	}

	reqCtx, cancel := context.WithTimeout(context.Background(), contextDBTimeout)
	defer cancel()

	hits, err := fetchContextDBSeed(reqCtx, baseURL, query, ctx.Rig, contextDBTopKFor(kind))
	if err != nil {
		contextDBDebugf("seed skipped (degraded): %v", err)
		return
	}
	hits = dedupeContextDBHits(hits)
	if len(hits) == 0 {
		contextDBDebugf("seed skipped: no hits for bead %s (rig=%s)", hookedBead.ID, ctx.Rig)
		return
	}

	block := renderContextSeed(hits)
	if block == "" {
		return
	}
	fmt.Println()
	fmt.Print(block)

	logContextDBConceptIDs(hookedBead, hits)
}

// contextDBQueryKind selects how the search query is shaped from the bead so the
// pull is role-appropriate rather than a generic title+description dump.
type contextDBQueryKind int

const (
	// queryKindWork — implement the bead → pull the rig core/subsystem (full detail).
	queryKindWork contextDBQueryKind = iota
	// queryKindReview — review a PR → pull the PR/scope subsystem (scope-led, trimmed).
	queryKindReview
	// queryKindPlan — plan work → pull the affected domains (full breadth).
	queryKindPlan
)

// attachedFormula returns the bead's attached formula name (or "" when none),
// tolerating a nil/parse-less bead.
func attachedFormula(b *beads.Issue) string {
	if b == nil {
		return ""
	}
	if a := beads.ParseAttachmentFields(b); a != nil {
		return a.AttachedFormula
	}
	return ""
}

// contextDBQueryKindFromFormula derives the query shape from the attached formula
// name. Review is matched before plan so a "plan-review" formula counts as review.
// Defaults to queryKindWork (the common case and the safe fallback).
func contextDBQueryKindFromFormula(formula string) contextDBQueryKind {
	f := strings.ToLower(strings.TrimSpace(formula))
	switch {
	case f == "":
		return queryKindWork
	case strings.Contains(f, "review") || strings.Contains(f, "pr-response") || strings.Contains(f, "pr-feedback"):
		return queryKindReview
	case strings.Contains(f, "plan") || strings.Contains(f, "prd"):
		return queryKindPlan
	default:
		return queryKindWork
	}
}

// buildContextDBQuery composes the search query from the bead, role-appropriately:
// it anchors on the rig and leads with the title (the scope) before the
// description, so the embedding is rig-scoped and scope-weighted rather than a
// generic body dump. Review trims the verbose description (it keys on the PR/scope
// subsystem). Capped to contextDBMaxQuery chars. Returns "" when the bead has no
// title or description (preserves the no-content → no-pull guard).
func buildContextDBQuery(b *beads.Issue, rig string, kind contextDBQueryKind) string {
	title := strings.TrimSpace(b.Title)
	desc := strings.TrimSpace(b.Description)
	if title == "" && desc == "" {
		return ""
	}

	if kind == queryKindReview && len(desc) > contextDBReviewMaxDesc {
		desc = strings.TrimSpace(desc[:contextDBReviewMaxDesc])
	}

	var parts []string
	if rig != "" {
		parts = append(parts, "rig: "+rig)
	}
	if title != "" {
		parts = append(parts, title)
	}
	if desc != "" {
		parts = append(parts, desc)
	}
	query := strings.Join(parts, "\n")
	if len(query) > contextDBMaxQuery {
		query = strings.TrimSpace(query[:contextDBMaxQuery])
	}
	return strings.TrimSpace(query)
}

// dedupeContextDBHits removes duplicate concepts (same concept_id) from the hit
// list, preserving the first (highest-ranked) occurrence. The /search seam can
// return the same concept twice once the graph-expansion layer lands — a concept
// can be both a vector seed and an edge neighbor (context-db/api/app.py
// §GRAPH-EXPANSION SEAM). Deduping keeps the orientation block tight (efficiency).
// Hits with an empty concept_id are left as-is (unexpected; not collapsed).
func dedupeContextDBHits(hits []contextDBHit) []contextDBHit {
	if len(hits) <= 1 {
		return hits
	}
	seen := make(map[string]struct{}, len(hits))
	out := make([]contextDBHit, 0, len(hits))
	for _, h := range hits {
		if h.ConceptID != "" {
			if _, ok := seen[h.ConceptID]; ok {
				continue
			}
			seen[h.ConceptID] = struct{}{}
		}
		out = append(out, h)
	}
	return out
}

// logContextDBConceptIDs records the retrieved concept_ids on the bead so a
// seed is reproducible (TOUCHPOINTS §6.1). Best-effort: bounded, and any error
// is swallowed (debug-logged only). Skipped in dry-run (no side effects).
func logContextDBConceptIDs(hookedBead *beads.Issue, hits []contextDBHit) {
	if primeDryRun {
		return
	}
	ids := make([]string, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, h.ConceptID)
	}
	if len(ids) == 0 {
		return
	}
	note := fmt.Sprintf("context-db seed (gt prime): concept_ids=[%s]", strings.Join(ids, ", "))

	cmdCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, "bd", "update", hookedBead.ID, "--notes", note)
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		contextDBDebugf("failed to log concept_ids on %s: %v", hookedBead.ID, err)
	}
}
