package cmd

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Rates below are typed in from Anthropic's pricing page
// (https://platform.claude.com/docs/en/about-claude/pricing), USD per million
// tokens, so the tests check calculateCost against the published prices rather
// than against the table it reads. Cache writes cost 1.25x the input price for
// the 5-minute lifetime and 2x for the 1-hour lifetime.
type publishedRate struct {
	input, output, cacheRead float64
}

func costCloseTo(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("%s: cost = %.10f, want %.10f", name, got, want)
	}
}

func TestCalculateCost_PublishedClaudePrices(t *testing.T) {
	rates := map[string]publishedRate{
		"claude-fable-5-2":          {8, 40, 0.20},
		"claude-opus-5-5":           {4, 20, 0.20},
		"claude-opus-4-6":           {5, 25, 0.50},
		"claude-opus-4-5-20251101":  {5, 25, 0.50},
		"claude-sonnet-5-5":         {2, 10, 0.10},
		"claude-sonnet-4-6":         {3, 15, 0.30},
		"claude-sonnet-4-20250514":  {3, 15, 0.30},
		"claude-haiku-5-5":          {0.10, 0.50, 0.01},
		"claude-haiku-4-5-20251001": {1, 5, 0.10},
		"claude-3-5-haiku-20241022": {0.80, 4, 0.08},
	}

	// 10K tokens of each kind keeps the prompt (40K) under every
	// long-context threshold.
	const n = 10_000
	for model, r := range rates {
		usage := &TokenUsage{
			Model:                    model,
			InputTokens:              n,
			OutputTokens:             n,
			CacheReadInputTokens:     n,
			CacheCreationInputTokens: 2 * n, // n with a 5-minute lifetime, n with 1 hour
			CacheCreation1hTokens:    n,
		}
		want := n / 1e6 * (r.input + r.output + r.cacheRead + 1.25*r.input + 2*r.input)
		costCloseTo(t, model, calculateCost(usage), want)
	}
}

func TestCalculateCost_HaikuLongContext(t *testing.T) {
	// Claude Haiku 5.5 is 0.10/0.50 up to 100K prompt tokens, 0.50/2.50 above.
	at := &TokenUsage{Model: "claude-haiku-5-5", InputTokens: 100_000, OutputTokens: 1_000}
	costCloseTo(t, "at threshold", calculateCost(at), 0.1*0.10+0.001*0.50)

	over := &TokenUsage{Model: "claude-haiku-5-5", InputTokens: 60_000, CacheReadInputTokens: 50_000, OutputTokens: 1_000}
	costCloseTo(t, "over threshold", calculateCost(over), 0.06*0.50+0.05*0.05+0.001*2.50)
}

func TestCalculateCost_BareCacheCreationIsFiveMinute(t *testing.T) {
	// Without a cache_creation breakdown every write bills at the 5-minute rate.
	usage := &TokenUsage{Model: "claude-sonnet-4-6", CacheCreationInputTokens: 1_000_000}
	costCloseTo(t, "bare cache creation", calculateCost(usage), 3*1.25)
}

func TestCalculateCost_UnknownModelUsesDefault(t *testing.T) {
	usage := &TokenUsage{
		Model:                    "some-unlisted-model",
		InputTokens:              1_000_000,
		OutputTokens:             1_000_000,
		CacheReadInputTokens:     1_000_000,
		CacheCreationInputTokens: 1_000_000,
	}
	costCloseTo(t, "unknown model", calculateCost(usage), 3+15+0.3+3.75)
	costCloseTo(t, "empty model", calculateCost(&TokenUsage{InputTokens: 1_000_000}), 3)
	costCloseTo(t, "nil usage", calculateCost(nil), 0)
}

func TestExtractCostFromWorkDir_PricesEachMessage(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)

	projectDir, err := getClaudeProjectDir("/work/pricing")
	if err != nil {
		t.Fatalf("getClaudeProjectDir: %v", err)
	}
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	lines := []string{
		`{"type":"user","message":{"role":"user","content":"hi"}}`,
		// Opus 5.5 with a 1-hour cache write, as Claude Code writes them.
		`{"type":"assistant","message":{"model":"claude-opus-5-5","role":"assistant","usage":{"input_tokens":2000,"cache_creation_input_tokens":40000,"cache_read_input_tokens":300000,"output_tokens":5000,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":40000}}}}`,
		`not json`,
		// The session switched to Haiku 5.5; this request is over 100K.
		`{"type":"assistant","message":{"model":"claude-haiku-5-5","role":"assistant","usage":{"input_tokens":150000,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":2000}}}`,
		`{"type":"assistant","message":{"model":"<synthetic>","role":"assistant","usage":{"input_tokens":0,"output_tokens":0}}}`,
	}
	path := filepath.Join(projectDir, "session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := extractCostFromWorkDir("/work/pricing")
	if err != nil {
		t.Fatalf("extractCostFromWorkDir: %v", err)
	}
	opus := 0.002*4 + 0.040*4*2 + 0.300*0.20 + 0.005*20
	haiku := 0.150*0.50 + 0.002*2.50
	costCloseTo(t, "transcript", got, opus+haiku)
}
