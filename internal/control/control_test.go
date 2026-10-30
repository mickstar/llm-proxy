package control_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mickstar/llm-proxy/internal/config"
	"github.com/mickstar/llm-proxy/internal/control"
	"github.com/mickstar/llm-proxy/internal/halt"
	"github.com/mickstar/llm-proxy/internal/secret"
	"github.com/mickstar/llm-proxy/internal/stats"
	"github.com/mickstar/llm-proxy/internal/traffic"
)

type fixtures struct {
	ts    *httptest.Server
	mgr   *config.Manager
	log   *traffic.Log
	stats *stats.Store
	halt  *halt.State
	upHit *int32
}

func newControl(t *testing.T, upstream http.HandlerFunc) *fixtures {
	t.Helper()
	var hits int32
	var fakeURL string
	if upstream != nil {
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&hits, 1)
			upstream(w, r)
		}))
		fakeURL = up.URL + "/api/v1"
		t.Cleanup(up.Close)
	}
	mgr, err := config.Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	if fakeURL != "" {
		if err := mgr.Update(func(c *config.Config) error { c.OpenRouter.BaseURL = fakeURL; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	log := traffic.New(50)
	breaker := halt.New()
	store := stats.New("")
	t.Cleanup(func() { _ = store.Close() })
	secrets := secret.NewResolver(secret.WithLookup(func(service, account string) (string, error) {
		if account == "missing" {
			return "", fmt.Errorf("keychain item %s/%s: The specified item could not be found in the keychain.", service, account)
		}
		return "fw_" + account, nil
	}))
	srv := httptest.NewServer(control.New(mgr, log, nil, breaker, store, secrets).Handler())
	t.Cleanup(srv.Close)
	return &fixtures{ts: srv, mgr: mgr, log: log, stats: store, halt: breaker, upHit: &hits}
}

func (f *fixtures) do(t *testing.T, method, path, body string) *http.Response {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, f.ts.URL+path, r)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func decode[T any](t *testing.T, r *http.Response) T {
	t.Helper()
	defer r.Body.Close()
	var v T
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return v
}

// doAs issues a request with an explicit Host header, the way a browser that
// resolved its own hostname to 127.0.0.1 would reach the control listener.
func (f *fixtures) doAs(t *testing.T, host, origin string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, f.ts.URL+"/api/config", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestControlAPIRejectsNonLoopbackHost(t *testing.T) {
	f := newControl(t, nil)
	resp := f.doAs(t, "attacker.example:8788", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

func TestControlAPIRejectsCrossOrigin(t *testing.T) {
	f := newControl(t, nil)
	resp := f.doAs(t, "127.0.0.1:8788", "https://attacker.example")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

func TestControlAPIAcceptsLocalhostHost(t *testing.T) {
	f := newControl(t, nil)
	resp := f.doAs(t, "localhost:8788", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestHealthAndConfig(t *testing.T) {
	f := newControl(t, nil)
	if err := f.mgr.Update(func(c *config.Config) error { c.OpenRouter.APIKey = "sk-or-abc"; return nil }); err != nil {
		t.Fatal(err)
	}

	resp := f.do(t, http.MethodGet, "/api/health", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", resp.StatusCode)
	}
	health := decode[map[string]any](t, resp)
	if health["status"] != "ok" {
		t.Errorf("health = %v", health)
	}

	resp = f.do(t, http.MethodGet, "/api/config", "")
	got := decode[config.Config](t, resp)
	if got.OpenRouter.APIKey != "sk-or-abc" {
		t.Errorf("config read key = %q", got.OpenRouter.APIKey)
	}
	if got.Server.ProxyAddr == "" {
		t.Errorf("config read missing server fields")
	}
}

func TestSettingsUpdatePersists(t *testing.T) {
	f := newControl(t, nil)
	body := `{"openrouter":{"base_url":"http://example.test/api/v1","api_key":"sk-or-new"}}`
	resp := f.do(t, http.MethodPut, "/api/settings", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("settings status = %d", resp.StatusCode)
	}
	resp.Body.Close()
	if got := f.mgr.Current().OpenRouter.APIKey; got != "sk-or-new" {
		t.Errorf("live key = %q", got)
	}
	disk, err := config.Load(f.mgr.Path())
	if err != nil {
		t.Fatal(err)
	}
	if disk.OpenRouter.APIKey != "sk-or-new" || disk.OpenRouter.BaseURL != "http://example.test/api/v1" {
		t.Errorf("persisted config = %+v", disk.OpenRouter)
	}
}

func TestKeyEndpointSetsLiteralKeyWithoutResettingBaseURL(t *testing.T) {
	f := newControl(t, nil)
	if err := f.mgr.Update(func(c *config.Config) error {
		c.OpenRouter.BaseURL = "https://api.fireworks.ai/inference/v1"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	resp := f.do(t, http.MethodPut, "/api/key", `{"api_key":"fw_literal"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("key status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	got := f.mgr.Current().OpenRouter
	if got.APIKey != "fw_literal" {
		t.Errorf("live key = %q", got.APIKey)
	}
	if got.BaseURL != "https://api.fireworks.ai/inference/v1" {
		t.Errorf("base url reset to %q; want the stored endpoint", got.BaseURL)
	}
	disk, err := config.Load(f.mgr.Path())
	if err != nil {
		t.Fatal(err)
	}
	if disk.OpenRouter.APIKey != "fw_literal" {
		t.Errorf("disk key = %q", disk.OpenRouter.APIKey)
	}
}

func TestKeyEndpointStoresKeychainReference(t *testing.T) {
	var gotAuth string
	f := newControl(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		fmt.Fprint(w, `{"id":"deepseek/deepseek-v4.1-flash","name":"DeepSeek"}`)
	})

	ref := "{keychain:FireworksAI/fireworks-api-key}"
	resp := f.do(t, http.MethodPut, "/api/key", `{"api_key":"`+ref+`"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("key status = %d", resp.StatusCode)
	}
	resp.Body.Close()
	if got := f.mgr.Current().OpenRouter.APIKey; got != ref {
		t.Fatalf("stored key = %q; want the reference", got)
	}
	disk, err := config.Load(f.mgr.Path())
	if err != nil {
		t.Fatal(err)
	}
	if disk.OpenRouter.APIKey != ref {
		t.Fatalf("disk key = %q; the secret must not be written", disk.OpenRouter.APIKey)
	}

	// The stored reference feeds the metadata client with the resolved secret.
	resp = f.do(t, http.MethodPost, "/api/models/fetch", `{"id":"deepseek/deepseek-v4.1-flash"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fetch status = %d", resp.StatusCode)
	}
	resp.Body.Close()
	if gotAuth != "Bearer fw_fireworks-api-key" {
		t.Fatalf("fetch auth = %q; want the resolved keychain secret", gotAuth)
	}
}

func TestKeyEndpointRejectsUnreadableReference(t *testing.T) {
	f := newControl(t, nil)
	resp := f.do(t, http.MethodPut, "/api/key", `{"api_key":"{keychain:FireworksAI/missing}"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", resp.StatusCode)
	}
	body := decode[map[string]string](t, resp)
	if !strings.Contains(body["error"], "could not be found") {
		t.Errorf("error = %q", body["error"])
	}
	if got := f.mgr.Current().OpenRouter.APIKey; got != "" {
		t.Errorf("key = %q; want the config unchanged", got)
	}
}

func TestKeyEndpointRejectsMalformedReference(t *testing.T) {
	f := newControl(t, nil)
	resp := f.do(t, http.MethodPut, "/api/key", `{"api_key":"{keychain:FireworksAI}"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", resp.StatusCode)
	}
	resp.Body.Close()
	if got := f.mgr.Current().OpenRouter.APIKey; got != "" {
		t.Errorf("key = %q; want the config unchanged", got)
	}
}

func TestKeyEndpointRequiresAField(t *testing.T) {
	f := newControl(t, nil)
	resp := f.do(t, http.MethodPut, "/api/key", `{}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestFiltersCRUD(t *testing.T) {
	f := newControl(t, nil)

	// invalid pattern -> 400, config unchanged
	resp := f.do(t, http.MethodPost, "/api/filters", `{"name":"bad","pattern":"[","action":"replace","replacement":"x","enabled":true}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid pattern status = %d", resp.StatusCode)
	}
	m := decode[map[string]string](t, resp)
	if !strings.Contains(m["error"], "pattern") {
		t.Errorf("error does not name problem: %v", m)
	}
	if len(f.mgr.Current().Filters) != 0 {
		t.Fatal("config changed despite invalid filter")
	}

	// replace without replacement -> 400
	resp = f.do(t, http.MethodPost, "/api/filters", `{"name":"norepl","pattern":"x","action":"replace","enabled":true}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing replacement status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// create valid
	resp = f.do(t, http.MethodPost, "/api/filters", `{"name":"pii","pattern":"James Brown","action":"replace","replacement":"John Smith","enabled":true}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// duplicate -> 400
	resp = f.do(t, http.MethodPost, "/api/filters", `{"name":"pii","pattern":"x","action":"block","enabled":true}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("duplicate status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// present in API and on disk
	var list []config.Filter
	resp = f.do(t, http.MethodGet, "/api/filters", "")
	list = decode[[]config.Filter](t, resp)
	if len(list) != 1 || list[0].Name != "pii" {
		t.Fatalf("filters = %+v", list)
	}
	disk, err := config.Load(f.mgr.Path())
	if err != nil {
		t.Fatal(err)
	}
	if len(disk.Filters) != 1 || disk.Filters[0].Name != "pii" {
		t.Errorf("filter not persisted: %+v", disk.Filters)
	}

	// update
	resp = f.do(t, http.MethodPut, "/api/filters/pii", `{"name":"pii","pattern":"James Brown","action":"replace","replacement":"Jane Doe","enabled":false}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update status = %d", resp.StatusCode)
	}
	resp.Body.Close()
	if f.mgr.Current().Filters[0].Replacement != "Jane Doe" || f.mgr.Current().Filters[0].Enabled {
		t.Errorf("update not applied: %+v", f.mgr.Current().Filters[0])
	}

	// update unknown -> 404
	resp = f.do(t, http.MethodPut, "/api/filters/missing", `{"name":"missing","pattern":"x","action":"block","enabled":true}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("update unknown status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// delete
	resp = f.do(t, http.MethodDelete, "/api/filters/pii", "")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d", resp.StatusCode)
	}
	resp.Body.Close()
	if len(f.mgr.Current().Filters) != 0 {
		t.Errorf("delete not applied")
	}

	// delete unknown -> 404
	resp = f.do(t, http.MethodDelete, "/api/filters/pii", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("delete unknown status = %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestRedactionRulesCRUD(t *testing.T) {
	f := newControl(t, nil)

	// missing template -> 400, config unchanged
	resp := f.do(t, http.MethodPost, "/api/redactions/rules", `{"name":"userpath","pattern":"/Users/jane.doe","enabled":true}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing template status = %d", resp.StatusCode)
	}
	if msg := decode[map[string]string](t, resp)["error"]; !strings.Contains(msg, "template") {
		t.Errorf("error = %q, want it to name the template", msg)
	}
	if len(f.mgr.Current().Redactions) != 0 {
		t.Fatal("config changed despite invalid redaction")
	}

	// create
	resp = f.do(t, http.MethodPost, "/api/redactions/rules",
		`{"name":"userpath","pattern":"/Users/jane.doe","template":"/Users/john.smith","enabled":true}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// duplicate -> 400
	resp = f.do(t, http.MethodPost, "/api/redactions/rules",
		`{"name":"userpath","pattern":"x","template":"y","enabled":true}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("duplicate status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// persisted and visible in the config snapshot
	disk, err := config.Load(f.mgr.Path())
	if err != nil {
		t.Fatal(err)
	}
	if len(disk.Redactions) != 1 {
		t.Fatalf("redactions = %+v", disk.Redactions)
	}
	rule := disk.Redactions[0]
	if rule.Pattern != "/Users/jane.doe" || rule.Template != "/Users/john.smith" {
		t.Errorf("redaction = %+v", rule)
	}

	// update in place
	resp = f.do(t, http.MethodPut, "/api/redactions/rules/userpath",
		`{"name":"userpath","pattern":"/home/michael","template":"/home/john.smith","enabled":false}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update status = %d", resp.StatusCode)
	}
	resp.Body.Close()
	if got := f.mgr.Current().Redactions[0]; got.Pattern != "/home/michael" || got.Enabled {
		t.Errorf("update not applied: %+v", got)
	}

	// rename
	resp = f.do(t, http.MethodPut, "/api/redactions/rules/userpath",
		`{"name":"homepath","pattern":"/home/michael","template":"/home/john.smith","enabled":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rename status = %d", resp.StatusCode)
	}
	resp.Body.Close()
	if got := f.mgr.Current().Redactions[0].Name; got != "homepath" {
		t.Errorf("name = %q, want homepath", got)
	}

	// update unknown -> 404
	resp = f.do(t, http.MethodPut, "/api/redactions/rules/missing",
		`{"name":"missing","pattern":"x","template":"y","enabled":true}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("update unknown status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// delete
	resp = f.do(t, http.MethodDelete, "/api/redactions/rules/homepath", "")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d", resp.StatusCode)
	}
	resp.Body.Close()
	if len(f.mgr.Current().Redactions) != 0 {
		t.Error("delete not applied")
	}
	resp = f.do(t, http.MethodDelete, "/api/redactions/rules/homepath", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("delete unknown status = %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestModelFetchAndCRUD(t *testing.T) {
	f := newControl(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/model/deepseek/deepseek-v4.1-flash" {
			t.Errorf("unexpected fetch path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"deepseek/deepseek-v4.1-flash","name":"DeepSeek V4.1","context_length":163840,"pricing":{"prompt":"0.0000001","completion":"0.0000002"},"supported_parameters":["temperature","tools"],"unknown_field":1}`)
	})

	resp := f.do(t, http.MethodPost, "/api/models/fetch", `{"id":"deepseek/deepseek-v4.1-flash"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fetch status = %d", resp.StatusCode)
	}
	fetched := decode[config.Model](t, resp)
	if fetched.ContextLength != 163840 || fetched.Pricing.Prompt != "0.0000001" || len(fetched.SupportedParameters) != 2 {
		t.Errorf("fetched metadata = %+v", fetched)
	}

	models := decode[[]config.Model](t, f.do(t, http.MethodGet, "/api/models", ""))
	if len(models) != 1 || models[0].ID != "deepseek/deepseek-v4.1-flash" {
		t.Fatalf("models = %+v", models)
	}
	disk, err := config.Load(f.mgr.Path())
	if err != nil {
		t.Fatal(err)
	}
	if len(disk.Models) != 1 {
		t.Errorf("fetch not persisted: %+v", disk.Models)
	}

	// second fetch updates in place (no duplicate)
	resp = f.do(t, http.MethodPost, "/api/models/fetch", `{"id":"deepseek/deepseek-v4.1-flash"}`)
	resp.Body.Close()
	if models := f.mgr.Current().Models; len(models) != 1 {
		t.Errorf("second fetch duplicated entry: %d", len(models))
	}

	// invalid id -> 400, no upstream request
	before := atomic.LoadInt32(f.upHit)
	resp = f.do(t, http.MethodPost, "/api/models/fetch", `{"id":"gpt-4"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid id status = %d", resp.StatusCode)
	}
	resp.Body.Close()
	if atomic.LoadInt32(f.upHit) != before {
		t.Errorf("invalid id contacted upstream")
	}

	// create, update, delete
	resp = f.do(t, http.MethodPost, "/api/models", `{"id":"manual/model","name":"Manual","enabled":true}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create model status = %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = f.do(t, http.MethodPut, "/api/models/manual/model", `{"id":"manual/model","name":"Renamed","enabled":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update model status = %d", resp.StatusCode)
	}
	resp.Body.Close()
	if got := f.mgr.Current().Models[1].Name; got != "Renamed" {
		t.Errorf("model update not applied: %q", got)
	}
	resp = f.do(t, http.MethodDelete, "/api/models/manual/model", "")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete model status = %d", resp.StatusCode)
	}
	resp.Body.Close()
	if len(f.mgr.Current().Models) != 1 {
		t.Errorf("model delete not applied")
	}
}

func TestFilterTestEndpoint(t *testing.T) {
	f := newControl(t, nil)
	resp := f.do(t, http.MethodPost, "/api/filters/test",
		`{"pattern":"sk-\\w+","sample":"value sk-live-abc123 end"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("test status = %d", resp.StatusCode)
	}
	out := decode[map[string]any](t, resp)
	if out["matches"].(float64) < 1 {
		t.Errorf("matches = %v", out["matches"])
	}
	if out["result"] != "value sk-live-abc123 end" {
		t.Errorf("result without replacement = %v", out["result"])
	}
	if len(f.mgr.Current().Filters) != 0 {
		t.Errorf("test endpoint mutated config")
	}

	resp = f.do(t, http.MethodPost, "/api/filters/test",
		`{"pattern":"sk-[\\w-]+","replacement":"[X]","sample":"value sk-live-abc123 end"}`)
	out = decode[map[string]any](t, resp)
	if out["result"] != "value [X] end" {
		t.Errorf("result with replacement = %v", out["result"])
	}

	// invalid pattern -> 400
	resp = f.do(t, http.MethodPost, "/api/filters/test", `{"pattern":"[","sample":"x"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid pattern status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// file-backed pattern
	key := filepath.Join(t.TempDir(), "id_rsa")
	if err := os.WriteFile(key, []byte("KEY-MATERIAL"), 0o600); err != nil {
		t.Fatal(err)
	}
	resp = f.do(t, http.MethodPost, "/api/filters/test",
		fmt.Sprintf(`{"pattern_file":%q,"sample":"has KEY-MATERIAL in it"}`, key))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pattern_file test status = %d", resp.StatusCode)
	}
	out = decode[map[string]any](t, resp)
	if out["matches"].(float64) != 1 || out["result"] != "has KEY-MATERIAL in it" {
		t.Errorf("pattern_file test = %v", out)
	}

	// unreadable file and ambiguous sources -> 400
	resp = f.do(t, http.MethodPost, "/api/filters/test",
		fmt.Sprintf(`{"pattern_file":%q,"sample":"x"}`, filepath.Join(t.TempDir(), "missing")))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing pattern_file status = %d", resp.StatusCode)
	}
	if msg := decode[map[string]string](t, resp)["error"]; !strings.Contains(msg, "pattern_file") {
		t.Errorf("error = %q, want it to name pattern_file", msg)
	}
	resp = f.do(t, http.MethodPost, "/api/filters/test", `{"pattern":"a","pattern_file":"b","sample":"x"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("both sources status = %d", resp.StatusCode)
	}
	resp = f.do(t, http.MethodPost, "/api/filters/test", `{"sample":"x"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("no source status = %d", resp.StatusCode)
	}
	resp.Body.Close()
	if len(f.mgr.Current().Filters) != 0 {
		t.Error("pattern_file test mutated config")
	}
}

func TestTrafficEndpoints(t *testing.T) {
	f := newControl(t, nil)
	for i := range 3 {
		f.log.Record(&traffic.Entry{
			Method: http.MethodPost, Path: "/v1/chat/completions", Model: "m",
			Status: 200, RequestBody: fmt.Sprintf("body-%d", i), ResponseBody: fmt.Sprintf("resp-%d", i),
		})
	}

	list := decode[[]traffic.Entry](t, f.do(t, http.MethodGet, "/api/traffic?limit=2", ""))
	if len(list) != 2 {
		t.Fatalf("list len = %d, want 2", len(list))
	}
	if list[0].ID <= list[1].ID {
		t.Errorf("not newest-first: %d then %d", list[0].ID, list[1].ID)
	}
	if list[0].RequestBody != "" || list[0].ResponseBody != "" {
		t.Errorf("list should omit bodies: %+v", list[0])
	}

	detail := decode[traffic.Entry](t, f.do(t, http.MethodGet, fmt.Sprintf("/api/traffic/%d", list[0].ID), ""))
	if detail.RequestBody != "body-2" || detail.ResponseBody != "resp-2" {
		t.Errorf("detail bodies = %q/%q", detail.RequestBody, detail.ResponseBody)
	}

	resp := f.do(t, http.MethodGet, "/api/traffic/9999", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown entry status = %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestTrafficStream(t *testing.T) {
	f := newControl(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.ts.URL+"/api/traffic/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("content type = %q", ct)
	}

	go func() {
		time.Sleep(150 * time.Millisecond)
		f.log.Record(&traffic.Entry{
			Method: http.MethodPost, Path: "/v1/chat/completions", Status: 200,
			RequestBody: "request body", ResponseBody: "response body",
		})
	}()

	rd := bufio.NewReader(resp.Body)
	deadline := time.Now().Add(3 * time.Second)
	var data string
	for time.Now().Before(deadline) {
		line, err := rd.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(line, "data: ") {
			data = strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			break
		}
	}
	if data == "" {
		t.Fatal("no SSE entry pushed")
	}
	var e traffic.Entry
	if err := json.Unmarshal([]byte(data), &e); err != nil {
		t.Fatalf("stream data not an entry: %v (%s)", err, data)
	}
	if e.ID == 0 || e.Path != "/v1/chat/completions" {
		t.Errorf("streamed entry = %+v", e)
	}
	// The stream pushes summary fields only, exactly like the list: bodies would
	// cost up to 1.25 MiB per frame for clients that never display them.
	if e.RequestBody != "" || e.ResponseBody != "" || e.RequestTruncated || e.ResponseTruncated {
		t.Errorf("stream should omit bodies: %+v", e)
	}
	if strings.Contains(data, `"request_body"`) || strings.Contains(data, `"response_body"`) {
		t.Errorf("stream frame carries body fields: %s", data)
	}
}

func TestSettingsUpdatePreservesOmittedModelPolicy(t *testing.T) {
	f := newControl(t, nil)
	resp := f.do(t, http.MethodPut, "/api/settings",
		`{"server":{"proxy_addr":"127.0.0.1:9100","control_addr":"127.0.0.1:9101","model_policy":"allowlist","traffic_log_capacity":10}}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pin policy status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// A caller that omits model_policy must not reset the stored policy.
	resp = f.do(t, http.MethodPut, "/api/settings",
		`{"server":{"proxy_addr":"127.0.0.1:9100","control_addr":"127.0.0.1:9101","traffic_log_capacity":20}}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	srv := f.mgr.Current().Server
	if srv.ModelPolicy != config.PolicyAllowlist {
		t.Errorf("policy = %q, want %q", srv.ModelPolicy, config.PolicyAllowlist)
	}
	if srv.TrafficLogCapacity != 20 {
		t.Errorf("capacity = %d, want 20", srv.TrafficLogCapacity)
	}
}

func TestHaltExposedAndResumed(t *testing.T) {
	f := newControl(t, nil)

	resp := f.do(t, http.MethodGet, "/api/halt", "")
	st := decode[halt.Status](t, resp)
	if st.Halted {
		t.Fatalf("started halted: %+v", st)
	}

	f.halt.Trigger("secret")
	f.halt.Refuse()

	resp = f.do(t, http.MethodGet, "/api/health", "")
	type healthResp struct {
		Halt halt.Status `json:"halt"`
	}
	health := decode[healthResp](t, resp)
	if !health.Halt.Halted || health.Halt.Filter != "secret" || health.Halt.Refused != 1 {
		t.Errorf("health halt = %+v", health.Halt)
	}

	resp = f.do(t, http.MethodPost, "/api/resume", "")
	st = decode[halt.Status](t, resp)
	if st.Halted {
		t.Fatalf("resume left halted: %+v", st)
	}
	if f.halt.Status().Halted {
		t.Error("breaker still halted after resume")
	}
}

func TestStatsEndpoint(t *testing.T) {
	f := newControl(t, nil)
	f.stats.Record(stats.Event{
		Model:            "deepseek/deepseek-v4.1-flash",
		PromptTokens:     10,
		CompletionTokens: 20,
		TotalTokens:      30,
		CostUSD:          0.00042,
		Status:           200,
		DurationMS:       1000,
	})
	f.stats.Begin()
	f.halt.Trigger("secret")

	resp := f.do(t, http.MethodGet, "/api/stats", "")
	type statsResp struct {
		stats.Snapshot
		Halt halt.Status `json:"halt"`
	}
	s := decode[statsResp](t, resp)

	if got := s.Totals; got.Requests != 1 || got.TotalTokens != 30 || got.CompletionTokens != 20 || got.CostUSD != 0.00042 {
		t.Errorf("totals = %+v", got)
	}
	if len(s.Models) != 1 || s.Models[0].Model != "deepseek/deepseek-v4.1-flash" || s.Models[0].TotalTokens != 30 {
		t.Errorf("models = %+v", s.Models)
	}
	if s.Last == nil || s.Last.TokensPerSecond != 20 {
		t.Errorf("last = %+v", s.Last)
	}
	if s.ActiveRequests != 1 {
		t.Errorf("active = %d, want 1", s.ActiveRequests)
	}
	if s.Throughput.CompletionTokensPerSecond <= 0 {
		t.Errorf("throughput = %+v", s.Throughput)
	}
	if len(s.Activity) != 60 {
		t.Errorf("activity buckets = %d, want 60", len(s.Activity))
	}
	if !s.Halt.Halted || s.Halt.Filter != "secret" {
		t.Errorf("halt = %+v", s.Halt)
	}
}
