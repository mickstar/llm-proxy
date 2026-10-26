package proxy_test

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mickstar/llm-proxy/internal/config"
	"github.com/mickstar/llm-proxy/internal/halt"
	"github.com/mickstar/llm-proxy/internal/proxy"
	"github.com/mickstar/llm-proxy/internal/secret"
	"github.com/mickstar/llm-proxy/internal/stats"
	"github.com/mickstar/llm-proxy/internal/traffic"
)

type captured struct {
	mu       sync.Mutex
	count    int
	path     string
	auth     string
	header   http.Header
	body     []byte
	query    string
	lastPath string
}

func (c *captured) handler(respond func(w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.count++
		c.path = r.URL.Path
		c.query = r.URL.RawQuery
		c.auth = r.Header.Get("Authorization")
		c.header = r.Header.Clone()
		c.body = b
		c.lastPath = r.URL.Path
		c.mu.Unlock()
		respond(w, r)
	}
}

// recorder captures halt notifications without touching the OS.
type recorder struct {
	mu    sync.Mutex
	notes []string
}

func (r *recorder) add(title, body string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notes = append(r.notes, title+"|"+body)
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.notes)
}

func (r *recorder) first() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.notes) == 0 {
		return ""
	}
	return r.notes[0]
}

type harness struct {
	local    *httptest.Server
	upstream *captured
	mgr      *config.Manager
	log      *traffic.Log
	stats    *stats.Store
	halt     *halt.State
	notified *recorder
}

func newHarness(t *testing.T, respond func(w http.ResponseWriter, r *http.Request), mutate func(*config.Config)) *harness {
	return newHarnessSecrets(t, respond, mutate, nil)
}

func newHarnessSecrets(t *testing.T, respond func(w http.ResponseWriter, r *http.Request), mutate func(*config.Config), secrets *secret.Resolver) *harness {
	t.Helper()
	cap := &captured{}
	up := httptest.NewServer(cap.handler(respond))
	dir := t.TempDir()
	mgr, err := config.Open(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Update(func(c *config.Config) error {
		c.OpenRouter.BaseURL = up.URL + "/api/v1"
		c.OpenRouter.APIKey = "sk-or-server-secret"
		if mutate != nil {
			mutate(c)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	log := traffic.New(100)
	breaker := halt.New()
	store := stats.New("")
	rec := &recorder{}
	p := proxy.New(proxy.Options{Manager: mgr, Log: log, Halt: breaker, Stats: store, Secrets: secrets, Notify: rec.add})
	local := httptest.NewServer(p)
	t.Cleanup(func() {
		local.Close()
		p.Close()
		mgr.Close()
		up.Close()
	})
	return &harness{local: local, upstream: cap, mgr: mgr, log: log, stats: store, halt: breaker, notified: rec}
}

func withModel(c *config.Config) {
	c.Models = append(c.Models, config.Model{ID: "deepseek/deepseek-v4.1-flash", Name: "DeepSeek", Enabled: true})
}

// withAllowlist pins the catalog-gated policy so the allowlist tests do not
// depend on the default.
func withAllowlist(c *config.Config) {
	c.Server.ModelPolicy = config.PolicyAllowlist
}

func post(t *testing.T, url, contentType, body string, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func decodeErr(t *testing.T, r *http.Response) map[string]any {
	t.Helper()
	defer r.Body.Close()
	var m map[string]any
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return m
}

func TestProxyResolvesKeychainCredential(t *testing.T) {
	var gotService, gotAccount string
	secrets := secret.NewResolver(secret.WithLookup(func(service, account string) (string, error) {
		gotService, gotAccount = service, account
		return "fw_from_keychain", nil
	}))
	h := newHarnessSecrets(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"cmpl-1","choices":[]}`)
	}, func(c *config.Config) {
		withModel(c)
		c.OpenRouter.APIKey = "{keychain:FireworksAI/fireworks-api-key}"
	}, secrets)

	resp := post(t, h.local.URL+"/v1/chat/completions", "application/json",
		`{"model":"deepseek/deepseek-v4.1-flash","messages":[]}`, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	h.upstream.mu.Lock()
	auth := h.upstream.auth
	h.upstream.mu.Unlock()
	if auth != "Bearer fw_from_keychain" {
		t.Fatalf("upstream auth = %q, want the resolved keychain secret", auth)
	}
	if gotService != "FireworksAI" || gotAccount != "fireworks-api-key" {
		t.Fatalf("lookup used %q/%q", gotService, gotAccount)
	}
}

func TestProxyCredentialFailureIsLocal(t *testing.T) {
	secrets := secret.NewResolver(secret.WithLookup(func(string, string) (string, error) {
		return "", errors.New("keychain item FireworksAI/missing: item not found")
	}))
	h := newHarnessSecrets(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"cmpl-1","choices":[]}`)
	}, func(c *config.Config) {
		withModel(c)
		c.OpenRouter.APIKey = "{keychain:FireworksAI/missing}"
	}, secrets)

	resp := post(t, h.local.URL+"/v1/chat/completions", "application/json",
		`{"model":"deepseek/deepseek-v4.1-flash","messages":[]}`, nil)
	body := decodeErr(t, resp)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d; want 502", resp.StatusCode)
	}
	if msg, _ := body["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "item not found") {
		t.Fatalf("error body = %v", body)
	}
	h.upstream.mu.Lock()
	defer h.upstream.mu.Unlock()
	if h.upstream.count != 0 {
		t.Fatalf("upstream contacted %d times despite an unreadable credential", h.upstream.count)
	}
}

func TestProxyForwardsNonStreamingRoundTrip(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream", "yes")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"id":"cmpl-1","choices":[]}`)
	}, withModel)

	resp := post(t, h.local.URL+"/v1/chat/completions?stream=false", "application/json",
		`{"model":"deepseek/deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}]}`,
		map[string]string{"Authorization": "Bearer sk-client-123", "X-Custom": "abc"})
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if string(body) != `{"id":"cmpl-1","choices":[]}` {
		t.Errorf("body = %q", body)
	}
	if got := resp.Header.Get("X-Upstream"); got != "yes" {
		t.Errorf("upstream header not passed through: %q", got)
	}
	h.upstream.mu.Lock()
	defer h.upstream.mu.Unlock()
	if h.upstream.path != "/api/v1/chat/completions" {
		t.Errorf("upstream path = %q", h.upstream.path)
	}
	if h.upstream.query != "stream=false" {
		t.Errorf("query not preserved: %q", h.upstream.query)
	}
	if h.upstream.auth != "Bearer sk-or-server-secret" {
		t.Errorf("upstream auth = %q, want server credential", h.upstream.auth)
	}
	if strings.Contains(h.upstream.auth, "sk-client-123") {
		t.Errorf("client credential leaked upstream")
	}
	if h.upstream.header.Get("X-Custom") != "abc" {
		t.Errorf("custom header dropped: %v", h.upstream.header)
	}
	want := `{"model":"deepseek/deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}]}`
	if string(h.upstream.body) != want {
		t.Errorf("upstream body = %q, want byte-identical %q", h.upstream.body, want)
	}
}

func TestProxyPassesClientUserAgentAndAttributionHeaders(t *testing.T) {
	const ua = "nexus/0.10.1 (opencode; darwin)"
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"id":"cmpl-1","choices":[]}`)
	}, withModel)

	resp := post(t, h.local.URL+"/v1/chat/completions", "application/json",
		`{"model":"deepseek/deepseek-v4.1-flash","messages":[]}`,
		map[string]string{"User-Agent": ua, "X-Title": "OpenCode", "HTTP-Referer": "nexus/v0.10.1"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	h.upstream.mu.Lock()
	defer h.upstream.mu.Unlock()
	if got := h.upstream.header.Get("User-Agent"); got != ua {
		t.Errorf("upstream User-Agent = %q, want %q", got, ua)
	}
	if got := h.upstream.header.Get("X-Title"); got != "OpenCode" {
		t.Errorf("upstream X-Title = %q", got)
	}
	if got := h.upstream.header.Get("HTTP-Referer"); got != "nexus/v0.10.1" {
		t.Errorf("upstream HTTP-Referer = %q", got)
	}
}

func TestProxyStreamsIncrementally(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		fmt.Fprint(w, "data: one\n\n")
		f.Flush()
		time.Sleep(400 * time.Millisecond)
		fmt.Fprint(w, "data: two\n\n")
		f.Flush()
	}, withModel)

	start := time.Now()
	resp := post(t, h.local.URL+"/v1/chat/completions", "application/json",
		`{"model":"deepseek/deepseek-v4.1-flash","stream":true,"messages":[]}`, nil)
	defer resp.Body.Close()
	rd := bufio.NewReader(resp.Body)
	var t1, t2 time.Duration
	deadline := time.Now().Add(5 * time.Second)
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(line, "one") && t1 == 0 {
			t1 = time.Since(start)
		}
		if strings.Contains(line, "two") {
			t2 = time.Since(start)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for streamed chunks")
		}
	}

	if t1 == 0 {
		t.Fatal("first chunk never observed")
	}
	if t1 > 250*time.Millisecond {
		t.Errorf("first chunk arrived at %v; proxy buffered the stream", t1)
	}
	if t2-t1 < 300*time.Millisecond {
		t.Errorf("chunks not spaced by upstream delay (t1=%v t2=%v)", t1, t2)
	}
}

func TestProxyUnknownModelRejectedBeforeUpstream(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, func(c *config.Config) {
		withModel(c)
		withAllowlist(c)
	})

	resp := post(t, h.local.URL+"/v1/chat/completions", "application/json",
		`{"model":"some/unknown-model","messages":[]}`, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	m := decodeErr(t, resp)
	eo := m["error"].(map[string]any)
	if eo["code"] != "model_not_found" {
		t.Errorf("code = %v", eo["code"])
	}
	if !strings.Contains(eo["message"].(string), "some/unknown-model") {
		t.Errorf("message does not name model: %v", eo["message"])
	}
	h.upstream.mu.Lock()
	defer h.upstream.mu.Unlock()
	if h.upstream.count != 0 {
		t.Errorf("upstream contacted %d times, want 0", h.upstream.count)
	}
}

// Passthrough is the default: a model absent from the catalog is forwarded and
// recorded, not rejected.
func TestProxyPassthroughForwardsUnknownModel(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"id":"cmpl-1"}`)
	}, nil)

	resp := post(t, h.local.URL+"/v1/chat/completions", "application/json",
		`{"model":"some/unknown-model","messages":[]}`, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	h.upstream.mu.Lock()
	hits, path := h.upstream.count, h.upstream.path
	h.upstream.mu.Unlock()
	if hits != 1 || path != "/api/v1/chat/completions" {
		t.Errorf("upstream hits=%d path=%q, want 1 %q", hits, path, "/api/v1/chat/completions")
	}

	entries := h.log.List(0)
	if len(entries) != 1 || entries[0].Model != "some/unknown-model" {
		t.Fatalf("traffic log = %+v, want one entry naming the model", entries)
	}
}

// Passthrough forwards the collection listing so clients see the real upstream
// catalog rather than the local pins.
func TestProxyPassthroughProxiesModelListing(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"id":"upstream/only"}]}`)
	}, withModel)

	resp, err := http.Get(h.local.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "upstream/only") {
		t.Errorf("listing not proxied: %s", body)
	}

	h.upstream.mu.Lock()
	hits, path := h.upstream.count, h.upstream.path
	h.upstream.mu.Unlock()
	if hits != 1 || path != "/api/v1/models" {
		t.Errorf("upstream hits=%d path=%q, want 1 %q", hits, path, "/api/v1/models")
	}
}

// Allowlist mode answers the listing locally and never contacts upstream.
func TestProxyAllowlistServesModelListingLocally(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"object":"list","data":[{"id":"upstream/only"}]}`)
	}, func(c *config.Config) {
		withModel(c)
		withAllowlist(c)
	})

	resp, err := http.Get(h.local.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "deepseek/deepseek-v4.1-flash") {
		t.Errorf("configured model missing from listing: %s", body)
	}
	if strings.Contains(string(body), "upstream/only") {
		t.Errorf("listing leaked upstream models: %s", body)
	}

	h.upstream.mu.Lock()
	hits := h.upstream.count
	h.upstream.mu.Unlock()
	if hits != 0 {
		t.Errorf("upstream contacted %d times, want 0", hits)
	}
}

// Upstream has no /models/{id} route, so the detail endpoint stays local even
// in passthrough mode.
func TestProxyPassthroughKeepsModelDetailLocal(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"should-not-be-reached"}`)
	}, withModel)

	resp, err := http.Get(h.local.URL + "/v1/models/deepseek/deepseek-v4.1-flash")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "deepseek/deepseek-v4.1-flash") {
		t.Errorf("detail body = %s", body)
	}

	h.upstream.mu.Lock()
	hits := h.upstream.count
	h.upstream.mu.Unlock()
	if hits != 0 {
		t.Errorf("upstream contacted %d times, want 0", hits)
	}
}

func TestProxyBlockedRequest(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {}, func(c *config.Config) {
		withModel(c)
		c.Filters = append(c.Filters, config.Filter{Name: "secret", Pattern: `sk-live-\w+`, Action: config.ActionBlock, Enabled: true})
	})

	resp := post(t, h.local.URL+"/v1/chat/completions", "application/json",
		`{"model":"deepseek/deepseek-v4.1-flash","messages":[{"role":"user","content":"use sk-live-abc123"}]}`, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	m := decodeErr(t, resp)
	eo := m["error"].(map[string]any)
	if eo["code"] != "content_filter" || eo["type"] != "permission_error" {
		t.Errorf("error = %v", eo)
	}
	if !strings.Contains(eo["message"].(string), "secret") {
		t.Errorf("message does not name filter: %v", eo["message"])
	}
	if strings.Contains(eo["message"].(string), "sk-live-abc123") {
		t.Errorf("matched value leaked in error: %v", eo["message"])
	}
	h.upstream.mu.Lock()
	if h.upstream.count != 0 {
		t.Errorf("blocked request reached upstream")
	}
	h.upstream.mu.Unlock()

	entries := h.log.List(0)
	if len(entries) != 1 || !entries[0].Blocked {
		t.Fatalf("blocked exchange not recorded: %+v", entries)
	}
	raw, _ := json.Marshal(entries[0])
	if bytes.Contains(raw, []byte("sk-live-abc123")) {
		t.Errorf("blocked secret stored in entry: %s", raw)
	}
	if entries[0].FilterSummary == "" || !strings.Contains(entries[0].FilterSummary, "secret") {
		t.Errorf("filter summary = %q", entries[0].FilterSummary)
	}
}

func TestProxyBlockedRequestPatternFile(t *testing.T) {
	key := filepath.Join(t.TempDir(), "id_rsa")
	const secret = "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXk\n-----END OPENSSH PRIVATE KEY-----"
	if err := os.WriteFile(key, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {}, func(c *config.Config) {
		withModel(c)
		c.Filters = append(c.Filters, config.Filter{Name: "ssh-keys", PatternFile: key, Action: config.ActionBlock, Enabled: true})
	})

	escaped := strings.ReplaceAll(secret, "\n", `\n`)
	resp := post(t, h.local.URL+"/v1/chat/completions", "application/json",
		`{"model":"deepseek/deepseek-v4.1-flash","messages":[{"role":"user","content":"here: `+escaped+`"}]}`, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	eo := decodeErr(t, resp)["error"].(map[string]any)
	if !strings.Contains(eo["message"].(string), "ssh-keys") {
		t.Errorf("message does not name filter: %v", eo["message"])
	}
	h.upstream.mu.Lock()
	if h.upstream.count != 0 {
		t.Errorf("blocked request reached upstream")
	}
	h.upstream.mu.Unlock()

	entries := h.log.List(0)
	if len(entries) != 1 || !entries[0].Blocked {
		t.Fatalf("blocked exchange not recorded: %+v", entries)
	}
	raw, _ := json.Marshal(entries[0])
	if bytes.Contains(raw, []byte("b3BlbnNzaC1rZXk")) {
		t.Errorf("key material stored in entry: %s", raw)
	}
}

func TestProxyReplaceReachesUpstreamRewritten(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, func(c *config.Config) {
		withModel(c)
		c.Filters = append(c.Filters, config.Filter{Name: "pii", Pattern: "James Brown", Action: config.ActionReplace, Replacement: "John Smith", Enabled: true})
	})

	resp := post(t, h.local.URL+"/v1/chat/completions", "application/json",
		`{"model":"deepseek/deepseek-v4.1-flash","messages":[{"role":"user","content":"Ask James Brown"}]}`, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	h.upstream.mu.Lock()
	defer h.upstream.mu.Unlock()
	if !strings.Contains(string(h.upstream.body), "Ask John Smith") {
		t.Errorf("upstream body not rewritten: %s", h.upstream.body)
	}
	if strings.Contains(string(h.upstream.body), "James Brown") {
		t.Errorf("original value reached upstream: %s", h.upstream.body)
	}
}

func TestProxyMultipartPassesUnfilteredMarked(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, withModel)

	resp := post(t, h.local.URL+"/v1/audio/transcriptions", "multipart/form-data; boundary=xyz",
		"--xyz\r\nContent-Disposition: form-data; name=\"file\"\r\n\r\nraw bytes\r\n--xyz--\r\n", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	h.upstream.mu.Lock()
	got := string(h.upstream.body)
	h.upstream.mu.Unlock()
	if !strings.Contains(got, "raw bytes") {
		t.Errorf("multipart body not forwarded unmodified: %q", got)
	}
	entries := h.log.List(0)
	if len(entries) != 1 || !entries[0].NotFiltered {
		t.Fatalf("entry not marked not-content-filtered: %+v", entries)
	}
}

func TestProxyRecordsAllOutcomes(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"ok":true}`)
	}, func(c *config.Config) {
		withModel(c)
		c.Filters = append(c.Filters, config.Filter{Name: "secret", Pattern: `sk-live-\w+`, Action: config.ActionBlock, Enabled: true})
	})

	// forwarded
	post(t, h.local.URL+"/v1/chat/completions", "application/json",
		`{"model":"deepseek/deepseek-v4.1-flash","messages":[]}`, nil).Body.Close()
	// not content-filtered
	post(t, h.local.URL+"/v1/audio/transcriptions", "multipart/form-data; boundary=b",
		"--b--\r\n", nil).Body.Close()
	// blocked last: a block halts the proxy, so nothing may follow it
	post(t, h.local.URL+"/v1/chat/completions", "application/json",
		`{"model":"deepseek/deepseek-v4.1-flash","messages":[{"role":"user","content":"sk-live-zzz999"}]}`, nil).Body.Close()

	entries := h.log.List(0)
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}
	var forwarded, blocked, notFiltered bool
	for _, e := range entries {
		switch {
		case e.Blocked:
			blocked = true
		case e.NotFiltered:
			notFiltered = true
		case e.Status == 200:
			forwarded = true
		}
	}
	if !forwarded || !blocked || !notFiltered {
		t.Errorf("missing outcomes: forwarded=%v blocked=%v notFiltered=%v", forwarded, blocked, notFiltered)
	}
}

func TestProxyErrorShape(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"blocked", `{"model":"deepseek/deepseek-v4.1-flash","messages":[{"role":"user","content":"sk-live-abc"}]}`, "content_filter"},
		{"unknown model", `{"model":"nope/missing","messages":[]}`, "model_not_found"},
		{"unparseable", `{not json`, "invalid_json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A fresh harness per case: a block now halts the proxy, so sharing
			// one would make every later case answer proxy_halted.
			h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}, func(c *config.Config) {
				withModel(c)
				withAllowlist(c)
				c.Filters = append(c.Filters, config.Filter{Name: "secret", Pattern: `sk-live-\w+`, Action: config.ActionBlock, Enabled: true})
			})
			resp := post(t, h.local.URL+"/v1/chat/completions", "application/json", tc.body, nil)
			m := decodeErr(t, resp)
			eo, ok := m["error"].(map[string]any)
			if !ok {
				t.Fatalf("no error object: %v", m)
			}
			for _, k := range []string{"message", "type", "param", "code"} {
				if _, ok := eo[k]; !ok {
					t.Errorf("error missing %q: %v", k, eo)
				}
			}
			if eo["code"] != tc.want {
				t.Errorf("code = %v, want %q", eo["code"], tc.want)
			}
		})
	}
}

func TestProxyFilterCaseInsensitiveByDefault(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, func(c *config.Config) {
		withModel(c)
		c.Filters = append(c.Filters, config.Filter{Name: "pii", Pattern: "james brown", Action: config.ActionReplace, Replacement: "John Smith", Enabled: true})
	})

	resp := post(t, h.local.URL+"/v1/chat/completions", "application/json",
		`{"model":"deepseek/deepseek-v4.1-flash","messages":[{"role":"user","content":"Ask JAMES BROWN"}]}`, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	h.upstream.mu.Lock()
	defer h.upstream.mu.Unlock()
	if !strings.Contains(string(h.upstream.body), "Ask John Smith") {
		t.Errorf("lowercase pattern did not match different case through proxy: %s", h.upstream.body)
	}
}

func withFixedRedaction(c *config.Config) {
	c.Redactions = append(c.Redactions, config.Redaction{
		Name: "email", Pattern: `john\.smith@example\.com`, Template: "redacted473919@gmail.com", Enabled: true,
	})
}

// A redacted value leaves as an alias and comes back as the original, while the
// traffic log keeps only the aliased form.
func TestProxyRedactsRequestAndRestoresResponse(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"content":"your address is redacted473919@gmail.com"}`)
	}, withFixedRedaction)

	resp := post(t, h.local.URL+"/v1/chat/completions", "application/json",
		`{"model":"m","messages":[{"role":"user","content":"mine is john.smith@example.com"}]}`, nil)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if !strings.Contains(string(body), "john.smith@example.com") {
		t.Errorf("response not restored: %s", body)
	}
	if strings.Contains(string(body), "redacted473919") {
		t.Errorf("alias leaked to the client: %s", body)
	}

	h.upstream.mu.Lock()
	sent := string(h.upstream.body)
	h.upstream.mu.Unlock()
	if strings.Contains(sent, "john.smith") {
		t.Errorf("original sent upstream: %s", sent)
	}
	if !strings.Contains(sent, "redacted473919@gmail.com") {
		t.Errorf("alias missing upstream: %s", sent)
	}

	entries := h.log.List(0)
	if len(entries) != 1 {
		t.Fatalf("entries = %d", len(entries))
	}
	e := entries[0]
	if !strings.Contains(e.RequestBody, "redacted473919@gmail.com") {
		t.Errorf("logged request body = %q", e.RequestBody)
	}
	if strings.Contains(e.ResponseBody, "john.smith") {
		t.Errorf("logged response body kept the original: %q", e.ResponseBody)
	}
	if e.RedactionSummary != "email x1" {
		t.Errorf("redaction summary = %q", e.RedactionSummary)
	}
}

// An alias split across two SSE chunks is still restored.
func TestProxyRestoresAliasSplitAcrossSSEChunks(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"delta\":\"redacted4739")
		fl.Flush()
		time.Sleep(20 * time.Millisecond)
		fmt.Fprint(w, "19@gmail.com\"}\n\n")
		fl.Flush()
	}, withFixedRedaction)

	req, err := http.NewRequest(http.MethodPost, h.local.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"m","stream":true,"messages":[{"role":"user","content":"john.smith@example.com"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if !strings.Contains(string(body), `"delta":"john.smith@example.com"`) {
		t.Fatalf("split alias not restored: %s", body)
	}
	if strings.Contains(string(body), "redacted4739") {
		t.Errorf("alias fragment leaked: %s", body)
	}
}

// Providers stream a reply token by token, so an alias arrives split across the
// JSON deltas of successive SSE events. Each fragment is a separate JSON string,
// never a contiguous byte run in the wire stream.
func TestProxyRestoresAliasSplitAcrossDeltas(t *testing.T) {
	chunks := []string{"your address is reda", "cted47", "39", "19@gmail", ".com", " ok"}
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for _, c := range chunks {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", c)
			fl.Flush()
			time.Sleep(5 * time.Millisecond)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		fl.Flush()
	}, withFixedRedaction)

	req, err := http.NewRequest(http.MethodPost, h.local.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"m","stream":true,"messages":[{"role":"user","content":"john.smith@example.com"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var text strings.Builder
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, "data: ") || strings.Contains(line, "[DONE]") {
			continue
		}
		var ev struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			t.Fatalf("event is not JSON: %v: %s", err, line)
		}
		for _, c := range ev.Choices {
			text.WriteString(c.Delta.Content)
		}
	}
	if got, want := text.String(), "your address is john.smith@example.com ok"; got != want {
		t.Errorf("client text = %q, want %q", got, want)
	}
	if strings.Contains(string(body), "redacted4739") {
		t.Errorf("alias leaked: %s", body)
	}
	if !strings.Contains(string(body), "data: [DONE]") {
		t.Errorf("terminator lost: %s", body)
	}
}

// The combination that broke in the wild: a gzip-capable client and a provider
// that streams the alias across separate deltas.
func TestProxyRestoresDeltaSplitForGzipClient(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		body := []byte(`data: {"choices":[{"delta":{"content":"mail red"}}]}` + "\n\n" +
			`data: {"choices":[{"delta":{"content":"acted47"}}]}` + "\n\n" +
			`data: {"choices":[{"delta":{"content":"3919@gmail.com"}}]}` + "\n\n" +
			"data: [DONE]\n\n")
		if r.Header.Get("X-Test-Gzip") == "1" {
			var buf bytes.Buffer
			zw := gzip.NewWriter(&buf)
			_, _ = zw.Write(body)
			_ = zw.Close()
			w.Header().Set("Content-Encoding", "gzip")
			body = buf.Bytes()
		}
		_, _ = w.Write(body)
	}, withFixedRedaction)

	req, err := http.NewRequest(http.MethodPost, h.local.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"m","stream":true,"messages":[{"role":"user","content":"john.smith@example.com"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("X-Test-Gzip", "1")
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.Header.Get("Content-Encoding") == "gzip" {
		t.Fatal("response still compressed")
	}
	if !strings.Contains(string(body), `"content":"john.smith@example.com"`) {
		t.Errorf("restored value missing: %s", body)
	}
	if strings.Contains(string(body), "redacted47") || strings.Contains(string(body), "acted47") {
		t.Errorf("alias fragment leaked: %s", body)
	}
}

// Redactions never see a body the filters blocked.
func TestProxyBlockedRequestSkipsRedaction(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {}, func(c *config.Config) {
		c.Filters = append(c.Filters, config.Filter{Name: "secret", Pattern: `sk-live-\w+`, Action: config.ActionBlock, Enabled: true})
		c.Redactions = append(c.Redactions, config.Redaction{Name: "k", Pattern: `sk-live-\w+`, Template: "[R-$R]", Enabled: true})
	})

	resp := post(t, h.local.URL+"/v1/chat/completions", "application/json",
		`{"model":"m","messages":[{"role":"user","content":"sk-live-abc"}]}`, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

// A block is a breach: the proxy refuses everything until resumed.
func TestProxyHaltsAfterBlockUntilResumed(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"ok":true}`)
	}, func(c *config.Config) {
		withModel(c)
		c.Filters = append(c.Filters, config.Filter{Name: "secret", Pattern: `sk-live-\w+`, Action: config.ActionBlock, Enabled: true})
	})

	resp := post(t, h.local.URL+"/v1/chat/completions", "application/json",
		`{"model":"deepseek/deepseek-v4.1-flash","messages":[{"role":"user","content":"sk-live-abc"}]}`, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("breach status = %d, want 403", resp.StatusCode)
	}

	h.upstream.mu.Lock()
	before := h.upstream.count
	h.upstream.mu.Unlock()

	// A perfectly clean request is refused, and never reaches upstream.
	resp = post(t, h.local.URL+"/v1/chat/completions", "application/json",
		`{"model":"deepseek/deepseek-v4.1-flash","messages":[]}`, nil)
	body := decodeErr(t, resp)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("clean request status = %d, want 403", resp.StatusCode)
	}
	if eo, ok := body["error"].(map[string]any); !ok || eo["code"] != "proxy_halted" {
		t.Errorf("error = %v, want code proxy_halted", body)
	}
	h.upstream.mu.Lock()
	after := h.upstream.count
	h.upstream.mu.Unlock()
	if after != before {
		t.Fatalf("upstream contacted while halted: %d -> %d", before, after)
	}

	// Even the model listing is closed.
	mr, err := http.Get(h.local.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	mr.Body.Close()
	if mr.StatusCode != http.StatusForbidden {
		t.Errorf("model listing status = %d, want 403", mr.StatusCode)
	}

	if st := h.halt.Status(); !st.Halted || st.Filter != "secret" || st.Refused != 2 {
		t.Errorf("status = %+v, want halted by secret after 2 refusals", st)
	}

	// Halting is sticky: a second trigger keeps the first cause.
	h.halt.Trigger("other")
	if st := h.halt.Status(); st.Filter != "secret" {
		t.Errorf("trigger overwrote the cause: %+v", st)
	}

	h.halt.Resume()
	resp = post(t, h.local.URL+"/v1/chat/completions", "application/json",
		`{"model":"deepseek/deepseek-v4.1-flash","messages":[]}`, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status after resume = %d, want 200", resp.StatusCode)
	}
}

func TestProxyNotifiesOnceOnHalt(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, func(c *config.Config) {
		c.Filters = append(c.Filters, config.Filter{Name: "secret", Pattern: `sk-live-\w+`, Action: config.ActionBlock, Enabled: true})
	})

	post(t, h.local.URL+"/v1/chat/completions", "application/json",
		`{"model":"m","messages":[{"role":"user","content":"sk-live-abc"}]}`, nil).Body.Close()
	// A refused request and a second block must not notify again.
	post(t, h.local.URL+"/v1/chat/completions", "application/json",
		`{"model":"m","messages":[]}`, nil).Body.Close()
	post(t, h.local.URL+"/v1/chat/completions", "application/json",
		`{"model":"m","messages":[{"role":"user","content":"sk-live-xyz"}]}`, nil).Body.Close()

	if got := h.notified.count(); got != 1 {
		t.Fatalf("notifications = %d, want 1", got)
	}
	note := h.notified.first()
	if !strings.Contains(note, "secret") {
		t.Errorf("notification does not name the filter: %q", note)
	}
	if strings.Contains(note, "sk-live") {
		t.Errorf("notification leaked the matched value: %q", note)
	}
}

// A $R replacement in a filter is reversible: the alias leaves and the original
// comes back.
func TestProxyFilterTokenRoundTrip(t *testing.T) {
	var h *harness
	var alias string
	h = newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		h.upstream.mu.Lock()
		sent := string(h.upstream.body)
		h.upstream.mu.Unlock()
		re := regexp.MustCompile(`redacted[a-z2-7]{6}@gmail\.com`)
		alias = re.FindString(sent)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"content":"write to %s"}`, alias)
	}, func(c *config.Config) {
		c.Filters = append(c.Filters, config.Filter{
			Name: "email", Pattern: `[\w.+-]+@[\w.-]+`, Action: config.ActionReplace,
			Replacement: "redacted$R@gmail.com", Enabled: true,
		})
	})

	resp := post(t, h.local.URL+"/v1/chat/completions", "application/json",
		`{"model":"m","messages":[{"role":"user","content":"mail john@gmail.com"}]}`, nil)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if alias == "" {
		t.Fatal("no alias reached upstream")
	}
	if !strings.Contains(string(body), "write to john@gmail.com") {
		t.Errorf("response not restored: %s", body)
	}
	if strings.Contains(string(body), "redacted") {
		t.Errorf("alias leaked: %s", body)
	}
}

// A gzip-requesting client still gets aliases restored: the proxy drops
// Accept-Encoding and decompresses upstream transparently.
func TestProxyRestoresThroughGzip(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := []byte(`{"content":"write to redacted473919@gmail.com"}`)
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			var buf bytes.Buffer
			zw := gzip.NewWriter(&buf)
			_, _ = zw.Write(body)
			_ = zw.Close()
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write(buf.Bytes())
			return
		}
		_, _ = w.Write(body)
	}, withFixedRedaction)

	req, err := http.NewRequest(http.MethodPost, h.local.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"john.smith@example.com"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.Header.Get("Content-Encoding") == "gzip" {
		t.Fatalf("response still compressed; restoration would have been skipped")
	}
	if !strings.Contains(string(body), "john.smith@example.com") {
		t.Errorf("response not restored: %s", body)
	}
	// The log keeps the alias (no PII) but must be readable, not gzip bytes.
	entries := h.log.List(0)
	if len(entries) != 1 || !strings.Contains(entries[0].ResponseBody, "redacted473919") {
		t.Errorf("logged response body = %q", entries[0].ResponseBody)
	}
	if strings.Contains(entries[0].ResponseBody, "john.smith") {
		t.Errorf("log kept the original: %q", entries[0].ResponseBody)
	}
}

func TestProxyRecordsTokenUsage(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"cmpl-1","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":34,"total_tokens":46,"cost":0.000789}}`)
	}, withModel)

	resp := post(t, h.local.URL+"/v1/chat/completions", "application/json",
		`{"model":"deepseek/deepseek-v4.1-flash","messages":[]}`, nil)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	entries := h.log.List(0)
	if len(entries) != 1 {
		t.Fatalf("traffic entries = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.PromptTokens != 12 || e.CompletionTokens != 34 || e.TotalTokens != 46 || e.CostUSD != 0.000789 {
		t.Errorf("entry usage = %d/%d/%d", e.PromptTokens, e.CompletionTokens, e.TotalTokens)
	}

	snap := h.stats.Snapshot()
	if got := snap.Totals; got.Requests != 1 || got.PromptTokens != 12 || got.CompletionTokens != 34 || got.TotalTokens != 46 || got.CostUSD != 0.000789 {
		t.Errorf("stats totals = %+v", got)
	}
	if len(snap.Models) != 1 || snap.Models[0].Model != "deepseek/deepseek-v4.1-flash" || snap.Models[0].TotalTokens != 46 {
		t.Errorf("stats models = %+v", snap.Models)
	}
	if snap.Last == nil || snap.Last.CompletionTokens != 34 {
		t.Errorf("stats last = %+v", snap.Last)
	}
}

func TestProxyRecordsStreamingUsage(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		f.Flush()
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":5,\"total_tokens\":8}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		f.Flush()
	}, withModel)

	resp := post(t, h.local.URL+"/v1/chat/completions", "application/json",
		`{"model":"deepseek/deepseek-v4.1-flash","stream":true,"messages":[]}`, nil)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "[DONE]") {
		t.Fatalf("stream truncated: %s", body)
	}

	entries := h.log.List(0)
	if len(entries) != 1 {
		t.Fatalf("traffic entries = %d, want 1", len(entries))
	}
	if got := entries[0]; got.PromptTokens != 3 || got.CompletionTokens != 5 || got.TotalTokens != 8 {
		t.Errorf("stream entry usage = %d/%d/%d", got.PromptTokens, got.CompletionTokens, got.TotalTokens)
	}
	if got := h.stats.Snapshot().Totals.TotalTokens; got != 8 {
		t.Errorf("stats total tokens = %d, want 8", got)
	}
}

func TestProxyCountsBlockedInStats(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, func(c *config.Config) {
		c.Filters = []config.Filter{{Name: "secret", Pattern: "sk-live-[a-z0-9]+", Action: config.ActionBlock, Enabled: true}}
	})

	resp := post(t, h.local.URL+"/v1/chat/completions", "application/json",
		`{"model":"m","messages":[{"role":"user","content":"sk-live-abc"}]}`, nil)
	resp.Body.Close()

	snap := h.stats.Snapshot()
	if snap.Totals.Requests != 1 || snap.Totals.Blocked != 1 || snap.Totals.Errors != 1 {
		t.Fatalf("stats totals = %+v", snap.Totals)
	}
}

func withPricedModel(c *config.Config) {
	c.Models = append(c.Models, config.Model{
		ID:      "accounts/fireworks/models/deepseek-v4p1-flash-1017",
		Name:    "DeepSeek V4.1 Flash 1017",
		Enabled: true,
		Pricing: config.Pricing{Prompt: "0.0000003", Cache: "0.000000006", Completion: "0.0000012"},
	})
}

func TestProxyEstimatesCostFromCatalogRates(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Fireworks shape: no cost field, cached prompt tokens reported.
		fmt.Fprint(w, `{"id":"cmpl-1","choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":10,"total_tokens":1010,"prompt_tokens_details":{"cached_tokens":400}}}`)
	}, withPricedModel)

	resp := post(t, h.local.URL+"/v1/chat/completions", "application/json",
		`{"model":"accounts/fireworks/models/deepseek-v4p1-flash-1017","messages":[]}`, nil)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	want := 600*0.0000003 + 400*0.000000006 + 10*0.0000012
	entries := h.log.List(0)
	if len(entries) != 1 {
		t.Fatalf("traffic entries = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.CachedPromptTokens != 400 {
		t.Errorf("cached tokens = %d, want 400", e.CachedPromptTokens)
	}
	if diff := e.CostUSD - want; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("entry cost = %v, want %v", e.CostUSD, want)
	}

	snap := h.stats.Snapshot()
	if diff := snap.Totals.CostUSD - want; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("stats cost = %v, want %v", snap.Totals.CostUSD, want)
	}
	if snap.Totals.CachedPromptTokens != 400 {
		t.Errorf("stats cached tokens = %d, want 400", snap.Totals.CachedPromptTokens)
	}
}

func TestProxyPrefersProviderReportedCost(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":10,"total_tokens":1010,"cost":0.5}}`)
	}, withPricedModel)

	resp := post(t, h.local.URL+"/v1/chat/completions", "application/json",
		`{"model":"accounts/fireworks/models/deepseek-v4p1-flash-1017","messages":[]}`, nil)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if got := h.log.List(0)[0].CostUSD; got != 0.5 {
		t.Fatalf("cost = %v, want provider-reported 0.5", got)
	}
}

func TestProxyLeavesCostZeroWithoutRates(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":1,"total_tokens":11}}`)
	}, withModel)

	resp := post(t, h.local.URL+"/v1/chat/completions", "application/json",
		`{"model":"deepseek/deepseek-v4.1-flash","messages":[]}`, nil)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if got := h.log.List(0)[0].CostUSD; got != 0 {
		t.Fatalf("cost = %v, want 0 without catalog rates", got)
	}
}
