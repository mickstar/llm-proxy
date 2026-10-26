package pricing

import (
	"math"
	"testing"

	"github.com/mickstar/llm-proxy/internal/config"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-12 }

func TestBuildParsesRatesAndDefaultsCacheToPrompt(t *testing.T) {
	tab := Build([]config.Model{
		{ID: "a/b", Pricing: config.Pricing{Prompt: "0.0000003", Completion: "0.0000012"}},
		{ID: "c/d", Pricing: config.Pricing{Prompt: "0.0000003", Cache: "0.000000006", Completion: "0.0000012"}},
	})

	r, ok := tab.Lookup("a/b")
	if !ok {
		t.Fatal("a/b missing")
	}
	if !near(r.Prompt, 0.0000003) || !near(r.Completion, 0.0000012) {
		t.Fatalf("rates = %+v", r)
	}
	if !near(r.Cache, r.Prompt) {
		t.Fatalf("cache = %v, want prompt rate %v", r.Cache, r.Prompt)
	}

	r, _ = tab.Lookup("c/d")
	if !near(r.Cache, 0.000000006) {
		t.Fatalf("cache = %v", r.Cache)
	}
}

func TestBuildSkipsEntriesWithoutBothRates(t *testing.T) {
	tab := Build([]config.Model{
		{ID: "no-completion", Pricing: config.Pricing{Prompt: "1"}},
		{ID: "no-prompt", Pricing: config.Pricing{Completion: "1"}},
		{ID: "bad", Pricing: config.Pricing{Prompt: "x", Completion: "1"}},
		{ID: "negative", Pricing: config.Pricing{Prompt: "-1", Completion: "1"}},
		{ID: `nan`, Pricing: config.Pricing{Prompt: "NaN", Completion: "1"}},
	})
	if len(tab) != 0 {
		t.Fatalf("table = %+v, want empty", tab)
	}
}

func TestLookupPrefersExactThenLongestBoundaryPrefix(t *testing.T) {
	tab := Table{
		"accounts/fireworks/models/deepseek-v4p1-flash":         {Prompt: 1},
		"accounts/fireworks/models/deepseek-v4p1-flash-1017":    {Prompt: 2},
		"accounts/fireworks/models/deepseek-v4p1-flash-1017-us": {Prompt: 3},
	}

	if r, _ := tab.Lookup("accounts/fireworks/models/deepseek-v4p1-flash-1017"); r.Prompt != 2 {
		t.Fatalf("exact match = %v, want 2", r.Prompt)
	}
	if r, _ := tab.Lookup("accounts/fireworks/models/deepseek-v4p1-flash-1017-us-extra"); r.Prompt != 3 {
		t.Fatalf("prefix match = %v, want 3", r.Prompt)
	}
	if r, _ := tab.Lookup("accounts/fireworks/models/deepseek-v4p1-flash:free"); r.Prompt != 1 {
		t.Fatalf("colon suffix match = %v, want 1", r.Prompt)
	}
	if _, ok := tab.Lookup("accounts/fireworks/models/deepseek-v4p1-flashx"); ok {
		t.Fatal("matched across a non-boundary suffix")
	}
}

func TestCostSplitsCachedPromptTokens(t *testing.T) {
	r := Rate{Prompt: 3e-7, Cache: 6e-9, Completion: 1.2e-6}
	// 1M prompt tokens of which 250k were cached, plus 1k completion.
	got := r.Cost(1_000_000, 250_000, 1_000)
	want := 750_000*3e-7 + 250_000*6e-9 + 1_000*1.2e-6
	if !near(got, want) {
		t.Fatalf("cost = %v, want %v", got, want)
	}
}

func TestCostClampsCachedToPrompt(t *testing.T) {
	r := Rate{Prompt: 1, Cache: 2, Completion: 3}
	if got := r.Cost(10, 99, 0); !near(got, 20) {
		t.Fatalf("cost = %v, want 20 (all prompt tokens at cache rate)", got)
	}
	if got := r.Cost(10, -5, 0); !near(got, 10) {
		t.Fatalf("cost = %v, want 10 (negative cached ignored)", got)
	}
}
