package app_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mickstar/llm-proxy/internal/app"
	"github.com/mickstar/llm-proxy/internal/config"
)

type upstream struct {
	mu   sync.Mutex
	auth string
	body string
	path string
	hit  int
}

func (u *upstream) set(r *http.Request, body []byte) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.hit++
	u.auth = r.Header.Get("Authorization")
	u.body = string(body)
	u.path = r.URL.Path
}

func (u *upstream) snapshot() (auth, body, path string, hit int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.auth, u.body, u.path, u.hit
}

// startApp writes a config seeded with mutate, starts the app on ephemeral
// loopback ports, and returns it plus the fake upstream.
func startApp(t *testing.T, up *upstream, mutate func(*config.Config)) (*app.App, *httptest.Server) {
	t.Helper()
	var fake *httptest.Server
	if up != nil {
		fake = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/model/") {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"deepseek/deepseek-v4.1-flash","name":"DeepSeek","context_length":163840,"pricing":{"prompt":"0.0000001","completion":"0.0000002"},"supported_parameters":["temperature"]}`)
				return
			}
			b, _ := io.ReadAll(r.Body)
			up.set(r, b)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"ok":true,"usage":{"prompt_tokens":7,"completion_tokens":9,"total_tokens":16}}`)
		}))
	}
	path := filepath.Join(t.TempDir(), "config.json")
	c := config.Default()
	c.Server.ProxyAddr = "127.0.0.1:0"
	c.Server.ControlAddr = "127.0.0.1:0"
	if fake != nil {
		c.OpenRouter.BaseURL = fake.URL + "/api/v1"
		c.OpenRouter.APIKey = "sk-or-initial"
	}
	if mutate != nil {
		mutate(c)
	}
	if _, err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	a, err := app.New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		a.Close()
		if fake != nil {
			fake.Close()
		}
	})
	return a, fake
}

func post(t *testing.T, url, body string, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestAPIFilterChangeAffectsProxy(t *testing.T) {
	up := &upstream{}
	a, _ := startApp(t, up, func(c *config.Config) {
		c.Models = append(c.Models, config.Model{ID: "deepseek/deepseek-v4.1-flash", Enabled: true})
	})

	resp := post(t, a.ControlURL()+"/api/filters",
		`{"name":"pii","pattern":"James Brown","action":"replace","replacement":"John Smith","enabled":true}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create filter status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	pr := post(t, a.ProxyURL()+"/v1/chat/completions",
		`{"model":"deepseek/deepseek-v4.1-flash","messages":[{"role":"user","content":"Ask James Brown"}]}`, nil)
	pr.Body.Close()
	if pr.StatusCode != http.StatusOK {
		t.Fatalf("proxy status = %d", pr.StatusCode)
	}
	if _, body, _, _ := up.snapshot(); !strings.Contains(body, "Ask John Smith") {
		t.Errorf("filter not live for proxied request: %s", body)
	}
}

func TestFetchViaAPIThenProxyUsesModel(t *testing.T) {
	up := &upstream{}
	a, _ := startApp(t, up, func(c *config.Config) {
		// The local listing is only served when the catalog gates traffic.
		c.Server.ModelPolicy = config.PolicyAllowlist
	})

	resp := post(t, a.ControlURL()+"/api/models/fetch", `{"id":"deepseek/deepseek-v4.1-flash"}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fetch status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Locally served model listing includes it immediately, no restart.
	mr, err := http.Get(a.ProxyURL() + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	var listing struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(mr.Body).Decode(&listing); err != nil {
		t.Fatal(err)
	}
	mr.Body.Close()
	if len(listing.Data) != 1 || listing.Data[0].ID != "deepseek/deepseek-v4.1-flash" {
		t.Fatalf("listing = %+v", listing)
	}

	pr := post(t, a.ProxyURL()+"/v1/chat/completions",
		`{"model":"deepseek/deepseek-v4.1-flash","messages":[]}`, nil)
	pr.Body.Close()
	if pr.StatusCode != http.StatusOK {
		t.Fatalf("proxied request after fetch status = %d", pr.StatusCode)
	}
	if _, _, path, hit := up.snapshot(); hit != 1 || path != "/api/v1/chat/completions" {
		t.Errorf("upstream hit=%d path=%q", hit, path)
	}
}

func TestSettingsKeyLiveForUpstream(t *testing.T) {
	up := &upstream{}
	a, _ := startApp(t, up, func(c *config.Config) {
		c.Models = append(c.Models, config.Model{ID: "deepseek/deepseek-v4.1-flash", Enabled: true})
	})

	// Read current config, then update only the key (preserving the fake base URL).
	cur, err := http.Get(a.ControlURL() + "/api/config")
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Config
	if err := json.NewDecoder(cur.Body).Decode(&cfg); err != nil {
		t.Fatal(err)
	}
	cur.Body.Close()
	body := fmt.Sprintf(`{"openrouter":{"base_url":%q,"api_key":"sk-or-updated"}}`, cfg.OpenRouter.BaseURL)
	req, _ := http.NewRequest(http.MethodPut, a.ControlURL()+"/api/settings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("settings status = %d", resp.StatusCode)
	}

	pr := post(t, a.ProxyURL()+"/v1/chat/completions",
		`{"model":"deepseek/deepseek-v4.1-flash","messages":[]}`, map[string]string{"Authorization": "Bearer sk-client"})
	pr.Body.Close()
	auth, _, _, _ := up.snapshot()
	if auth != "Bearer sk-or-updated" {
		t.Errorf("upstream auth = %q, want updated key", auth)
	}
}

func TestDefaultBindingIsLoopback(t *testing.T) {
	if got := config.Default().Server.ControlAddr; got != "127.0.0.1:8788" {
		t.Errorf("default control addr = %q", got)
	}
	a, _ := startApp(t, nil, nil)
	host, _, err := net.SplitHostPort(strings.TrimPrefix(a.ControlURL(), "http://"))
	if err != nil {
		t.Fatal(err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		t.Errorf("control bound to non-loopback address %q", host)
	}
}

func TestRedactionsListedOverControlAPI(t *testing.T) {
	up := &upstream{}
	a, _ := startApp(t, up, func(c *config.Config) {
		c.Redactions = append(c.Redactions, config.Redaction{
			Name: "email", Pattern: `john\.smith@example\.com`, Template: "redacted473919@gmail.com", Enabled: true,
		})
	})

	resp := post(t, a.ProxyURL()+"/v1/chat/completions",
		`{"model":"m","messages":[{"role":"user","content":"john.smith@example.com"}]}`, nil)
	resp.Body.Close()

	if _, body, _, _ := up.snapshot(); !strings.Contains(body, "redacted473919@gmail.com") {
		t.Fatalf("upstream body = %q", body)
	}

	lr, err := http.Get(a.ControlURL() + "/api/redactions")
	if err != nil {
		t.Fatal(err)
	}
	defer lr.Body.Close()
	var items []struct {
		Alias    string `json:"alias"`
		Original string `json:"original"`
		Rule     string `json:"rule"`
		Count    int    `json:"count"`
	}
	if err := json.NewDecoder(lr.Body).Decode(&items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}
	if items[0].Alias != "redacted473919@gmail.com" || items[0].Original != "john.smith@example.com" ||
		items[0].Rule != "email" || items[0].Count != 1 {
		t.Errorf("item = %+v", items[0])
	}
}

func TestStatsEndpointReportsProxiedUsage(t *testing.T) {
	up := &upstream{}
	a, _ := startApp(t, up, nil)

	resp := post(t, a.ProxyURL()+"/v1/chat/completions",
		`{"model":"deepseek/deepseek-v4.1-flash","messages":[]}`, nil)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	r, err := http.Get(a.ControlURL() + "/api/stats")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var snap struct {
		Totals struct {
			Requests         int64 `json:"requests"`
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
			TotalTokens      int64 `json:"total_tokens"`
		} `json:"totals"`
		Models []struct {
			Model       string `json:"model"`
			TotalTokens int64  `json:"total_tokens"`
		} `json:"models"`
	}
	if err := json.NewDecoder(r.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	if snap.Totals.Requests != 1 || snap.Totals.PromptTokens != 7 || snap.Totals.CompletionTokens != 9 || snap.Totals.TotalTokens != 16 {
		t.Errorf("totals = %+v", snap.Totals)
	}
	if len(snap.Models) != 1 || snap.Models[0].Model != "deepseek/deepseek-v4.1-flash" || snap.Models[0].TotalTokens != 16 {
		t.Errorf("models = %+v", snap.Models)
	}
}
