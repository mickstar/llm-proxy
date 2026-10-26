package catalog_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/mickstar/llm-proxy/internal/catalog"
	"github.com/mickstar/llm-proxy/internal/config"
	"github.com/mickstar/llm-proxy/internal/openrouter"
)

// TestFetchUpsertBuildIsLive exercises the full path: fetch metadata from a
// fixture OpenRouter, upsert it through a real config.Manager, rebuild the
// catalog snapshot, and observe the model served immediately — then update it
// with a second fetch and see the new snapshot without restart.
func TestFetchUpsertBuildIsLive(t *testing.T) {
	const id = "deepseek/deepseek-v4.1-flash"
	var requests int32
	var contextLen int64 = 163840
	var name atomic.Value
	name.Store("DeepSeek V4.1 Flash")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		if r.URL.Path != "/model/deepseek/deepseek-v4.1-flash" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":%q,"name":%q,"context_length":%d,"pricing":{"prompt":"0.0000001","completion":"0.0000002"},"supported_parameters":["temperature"]}`,
			id, name.Load().(string), atomic.LoadInt64(&contextLen))
	}))
	defer srv.Close()

	mgr, err := config.Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatalf("config.Open: %v", err)
	}
	defer mgr.Close()

	client := openrouter.New(srv.URL, "test-key")
	ctx := context.Background()

	// First fetch adds the model.
	fetched, err := client.FetchModel(ctx, id)
	if err != nil {
		t.Fatalf("FetchModel: %v", err)
	}
	if err := mgr.Update(func(c *config.Config) error {
		out, added := catalog.Upsert(c.Models, *fetched)
		if !added {
			t.Errorf("first Upsert reported existing")
		}
		c.Models = out
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	snap := catalog.Build(mgr.Current().Models)
	if !snap.Has(id) {
		t.Fatal("fetched model not served by new snapshot")
	}
	got, ok := snap.Get(id)
	if !ok {
		t.Fatal("Get failed")
	}
	if got.Name != "DeepSeek V4.1 Flash" || got.ContextLength != 163840 || !got.Enabled {
		t.Fatalf("model = %+v", got)
	}

	// Second fetch updates the entry in place.
	name.Store("DeepSeek V4.1 Flash (updated)")
	atomic.StoreInt64(&contextLen, 200000)
	fetched, err = client.FetchModel(ctx, id)
	if err != nil {
		t.Fatalf("second FetchModel: %v", err)
	}
	if err := mgr.Update(func(c *config.Config) error {
		out, added := catalog.Upsert(c.Models, *fetched)
		if added {
			t.Errorf("second Upsert reported new")
		}
		c.Models = out
		return nil
	}); err != nil {
		t.Fatalf("second Update: %v", err)
	}

	snap2 := catalog.Build(mgr.Current().Models)
	if n := len(snap2.List()); n != 1 {
		t.Fatalf("catalog len = %d, want 1", n)
	}
	updated, ok := snap2.Get(id)
	if !ok {
		t.Fatal("updated model missing")
	}
	if updated.Name != "DeepSeek V4.1 Flash (updated)" || updated.ContextLength != 200000 {
		t.Fatalf("updated model = %+v", updated)
	}
	if got := atomic.LoadInt32(&requests); got != 2 {
		t.Fatalf("fixture requests = %d, want 2", got)
	}
}
