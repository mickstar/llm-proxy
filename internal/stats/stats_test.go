package stats

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mickstar/llm-proxy/internal/pricing"
)

func TestRecordAggregatesTotalsAndModels(t *testing.T) {
	s := New("")
	now := time.Now()
	s.Begin()
	s.Record(Event{Time: now, Model: "b/model", PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30, CostUSD: 0.25, Status: 200, RequestBytes: 100, ResponseBytes: 200, DurationMS: 1000})
	s.End()
	s.Record(Event{Time: now, Model: "a/model", PromptTokens: 5, CompletionTokens: 5, TotalTokens: 10, CostUSD: 0.5, Status: 500})
	s.Record(Event{Time: now, Model: "a/model", PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3, CostUSD: 0.125, Status: 403, Blocked: true})

	snap := s.SnapshotAt(now)
	want := Totals{Requests: 3, PromptTokens: 16, CompletionTokens: 27, TotalTokens: 43, CostUSD: 0.875, Errors: 2, Blocked: 1, RequestBytes: 100, ResponseBytes: 200}
	if snap.Totals != want {
		t.Fatalf("totals = %+v, want %+v", snap.Totals, want)
	}
	if snap.ActiveRequests != 0 {
		t.Errorf("active = %d, want 0", snap.ActiveRequests)
	}
	if len(snap.Models) != 2 || snap.Models[0].Model != "b/model" {
		t.Fatalf("models = %+v, want b/model first", snap.Models)
	}
	if got := snap.Models[1]; got.Requests != 2 || got.TotalTokens != 13 || got.Errors != 2 || got.CostUSD != 0.625 {
		t.Errorf("a/model = %+v", got)
	}
	if snap.Last == nil || snap.Last.Model != "a/model" || snap.Last.TokensPerSecond != 0 || snap.Last.CostUSD != 0.125 {
		t.Errorf("last = %+v", snap.Last)
	}
}

func TestDerivesMissingTotalsAndRates(t *testing.T) {
	s := New("")
	now := time.Now()
	s.Record(Event{Time: now, Model: "m", PromptTokens: 10, DurationMS: 2000})
	s.Record(Event{Time: now, Model: "m", CompletionTokens: 50, DurationMS: 2500, TotalTokens: 60, PromptTokens: 10})

	snap := s.SnapshotAt(now)
	if snap.Totals.TotalTokens != 70 || snap.Totals.CompletionTokens != 50 {
		t.Fatalf("totals = %+v", snap.Totals)
	}
	if snap.Last == nil || snap.Last.CompletionTokens != 50 || snap.Last.TokensPerSecond != 20 {
		t.Fatalf("last = %+v", snap.Last)
	}
}

func TestThroughputUsesTrailingWindow(t *testing.T) {
	s := New("")
	now := time.Now().Truncate(time.Second)
	s.Record(Event{Time: now.Add(-2 * time.Minute), Model: "m", CompletionTokens: 999, TotalTokens: 999})
	s.Record(Event{Time: now.Add(-10 * time.Second), Model: "m", PromptTokens: 30, CompletionTokens: 60, TotalTokens: 90})
	s.Record(Event{Time: now, Model: "m", CompletionTokens: 30, TotalTokens: 30})

	snap := s.SnapshotAt(now)
	if got := snap.Throughput.CompletionTokensPerSecond; got != 1.5 {
		t.Errorf("completion tok/s = %v, want 1.5", got)
	}
	if got := snap.Throughput.TotalTokensPerSecond; got != 2 {
		t.Errorf("total tok/s = %v, want 2", got)
	}
	if got := snap.Throughput.RequestsPerMinute; got != 2 {
		t.Errorf("req/min = %v, want 2", got)
	}
}

func TestActivityBucketsAreChronological(t *testing.T) {
	s := New("")
	now := time.Now().Truncate(time.Minute)
	s.Record(Event{Time: now.Add(-30 * time.Minute), Model: "m", TotalTokens: 7})
	s.Record(Event{Time: now, Model: "m", TotalTokens: 3})

	snap := s.SnapshotAt(now)
	if len(snap.Activity) != activityMinutes {
		t.Fatalf("activity len = %d, want %d", len(snap.Activity), activityMinutes)
	}
	for i := 1; i < len(snap.Activity); i++ {
		if !snap.Activity[i].Minute.After(snap.Activity[i-1].Minute) {
			t.Fatalf("activity not chronological at %d", i)
		}
	}
	if got := snap.Activity[len(snap.Activity)-31]; got.Requests != 1 || got.Tokens != 7 {
		t.Errorf("30m bucket = %+v", got)
	}
	if got := snap.Activity[len(snap.Activity)-1]; got.Requests != 1 || got.Tokens != 3 {
		t.Errorf("current bucket = %+v", got)
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "stats.json")
	s := New(path)
	now := time.Now()
	s.Record(Event{Time: now, Model: "one/model", PromptTokens: 11, CompletionTokens: 22, TotalTokens: 33, CostUSD: 0.125, Status: 200})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	loaded := New(path)
	t.Cleanup(func() { _ = loaded.Close() })
	snap := loaded.SnapshotAt(now)
	if snap.Totals.TotalTokens != 33 || snap.Totals.Requests != 1 || snap.Totals.CostUSD != 0.125 {
		t.Fatalf("totals after reload = %+v", snap.Totals)
	}
	if len(snap.Models) != 1 || snap.Models[0].Model != "one/model" || snap.Models[0].CompletionTokens != 22 || snap.Models[0].CostUSD != 0.125 {
		t.Fatalf("models after reload = %+v", snap.Models)
	}
	if snap.Since.IsZero() {
		t.Error("since was not persisted")
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o600 {
		t.Errorf("stats file mode = %o, want 600", info.Mode().Perm())
	}
}

func TestCorruptFileStartsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(path)
	t.Cleanup(func() { _ = s.Close() })
	s.Record(Event{Model: "m", TotalTokens: 1})
	snap := s.Snapshot()
	if snap.Totals.Requests != 1 || snap.Totals.TotalTokens != 1 {
		t.Fatalf("totals = %+v", snap.Totals)
	}
}

func TestBeginEndNeverGoesNegative(t *testing.T) {
	s := New("")
	s.End()
	s.Begin()
	s.Begin()
	if got := s.Snapshot().ActiveRequests; got != 2 {
		t.Fatalf("active = %d, want 2", got)
	}
	s.End()
	s.End()
	s.End()
	if got := s.Snapshot().ActiveRequests; got != 0 {
		t.Fatalf("active = %d, want 0", got)
	}
}

func TestBackfillCostsFromCatalogRates(t *testing.T) {
	s := New("")
	s.Record(Event{
		Model:            "deepseek/deepseek-v4.1-flash-1017",
		PromptTokens:     1_000_000,
		CompletionTokens: 1_000,
		TotalTokens:      1_001_000,
	})
	s.BackfillCosts(pricing.Table{
		"deepseek/deepseek-v4.1-flash": {Prompt: 0.000003, Cache: 0.00000003, Completion: 0.0000024},
		"other/model":                  {Prompt: 1, Completion: 2},
	})

	snap := s.Snapshot()
	want := 3.0 + 0.0024
	if diff := snap.Totals.CostUSD - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("total cost = %v, want %v", snap.Totals.CostUSD, want)
	}
	if len(snap.Models) != 1 || snap.Models[0].CostUSD != snap.Totals.CostUSD {
		t.Fatalf("model cost = %+v", snap.Models)
	}

	// A second backfill must not double-count or replace recorded costs.
	s.BackfillCosts(pricing.Table{
		"deepseek/deepseek-v4.1-flash": {Prompt: 10, Completion: 20},
	})
	if got := s.Snapshot().Totals.CostUSD; got != snap.Totals.CostUSD {
		t.Fatalf("second backfill changed cost: %v -> %v", snap.Totals.CostUSD, got)
	}
}

func TestRecordTracksCachedPromptTokens(t *testing.T) {
	s := New("")
	s.Record(Event{Model: "m", PromptTokens: 100, CachedPromptTokens: 60, CompletionTokens: 5, TotalTokens: 105})

	snap := s.Snapshot()
	if got := snap.Totals.CachedPromptTokens; got != 60 {
		t.Fatalf("cached prompt tokens = %d, want 60", got)
	}
	if got := snap.Models[0].CachedPromptTokens; got != 60 {
		t.Fatalf("model cached prompt tokens = %d, want 60", got)
	}
}

func TestBackfillCostsSkipsModelsWithRecordedCost(t *testing.T) {
	s := New("")
	s.Record(Event{Model: "m", PromptTokens: 1, TotalTokens: 1, CostUSD: 0.5})
	s.BackfillCosts(pricing.Table{"m": {Prompt: 100, Completion: 100}})
	if got := s.Snapshot().Totals.CostUSD; got != 0.5 {
		t.Fatalf("cost = %v, want 0.5", got)
	}
}
