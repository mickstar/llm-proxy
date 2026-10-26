package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLoadCreatesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Server.ProxyAddr != DefaultProxyAddr || c.Server.ControlAddr != DefaultControlAddr {
		t.Errorf("addresses = %q/%q, want loopback defaults", c.Server.ProxyAddr, c.Server.ControlAddr)
	}
	if c.Server.TrafficLogCapacity != DefaultTrafficLogCapacity {
		t.Errorf("capacity = %d, want %d", c.Server.TrafficLogCapacity, DefaultTrafficLogCapacity)
	}
	if len(c.Models) != 0 || len(c.Filters) != 0 {
		t.Errorf("models=%d filters=%d, want empty", len(c.Models), len(c.Filters))
	}
	if c.OpenRouter.BaseURL != DefaultBaseURL {
		t.Errorf("base url = %q", c.OpenRouter.BaseURL)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("default file not created: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("default file invalid JSON: %v", err)
	}
	if _, ok := raw["server"]; !ok {
		t.Errorf("default file missing server key: %s", data)
	}
	// Reloading the created file must round-trip.
	if _, err := Load(path); err != nil {
		t.Fatalf("reload: %v", err)
	}
}

func TestResolvePathOverride(t *testing.T) {
	got, err := ResolvePath("/tmp/custom.json")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/custom.json" {
		t.Errorf("override = %q", got)
	}
	def, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	got, err = ResolvePath("")
	if err != nil {
		t.Fatal(err)
	}
	if got != def {
		t.Errorf("default = %q, want %q", got, def)
	}
}

func TestValidateInvalidPatternNamesFileAndFilter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{
	  "filters": [{"name":"bad-regex","pattern":"[unclosed","action":"replace","replacement":"x","enabled":true}]
	}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error")
	}
	msg := err.Error()
	if !strings.Contains(msg, path) {
		t.Errorf("error does not name config file: %v", msg)
	}
	if !strings.Contains(msg, "bad-regex") {
		t.Errorf("error does not name filter: %v", msg)
	}
}

func TestValidatePatternErrorHidesSecret(t *testing.T) {
	const secret = "hf_nLCdeadbeefcafe0123456789"
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{
	  "filters": [{"name":"hf","pattern":"[` + secret + `","action":"replace","replacement":"x","enabled":true}]
	}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error")
	}
	msg := err.Error()
	if strings.Contains(msg, secret) {
		t.Errorf("error echoes the pattern text: %v", msg)
	}
	if !strings.Contains(msg, `filter "hf"`) || !strings.Contains(msg, "invalid pattern") {
		t.Errorf("error does not name filter and failure class: %v", msg)
	}
}

func TestValidateValidConfigPasses(t *testing.T) {
	c := Default()
	c.Filters = []Filter{
		{Name: "swap", Pattern: "James Brown", Action: ActionReplace, Replacement: "John Smith", Enabled: true},
		{Name: "secret", Pattern: `sk-\w+`, Action: ActionBlock, Enabled: true},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	c2 := Default()
	c2.Filters = []Filter{{Name: "r", Pattern: "x", Action: ActionReplace, Enabled: true}}
	if err := c2.Validate(); err == nil {
		t.Error("replace without replacement accepted")
	}
	c3 := Default()
	c3.Filters = []Filter{{Name: "a", Pattern: "x", Action: "nope", Replacement: "y", Enabled: true}}
	if err := c3.Validate(); err == nil {
		t.Error("invalid action accepted")
	}
	c4 := Default()
	c4.Filters = []Filter{{Name: "dup", Pattern: "x", Action: ActionBlock}, {Name: "dup", Pattern: "y", Action: ActionBlock}}
	if err := c4.Validate(); err == nil {
		t.Error("duplicate names accepted")
	}
}

func TestSaveAtomicRoundTripAndNoTempLeft(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub")
	path := filepath.Join(dir, "config.json")
	c := Default()
	c.OpenRouter.APIKey = "sk-or-test"
	c.Filters = []Filter{{Name: "k", Pattern: "abc", Action: ActionReplace, Replacement: "xyz", Enabled: true}}
	if _, err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.OpenRouter.APIKey != "sk-or-test" || len(got.Filters) != 1 || got.Filters[0].Replacement != "xyz" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	// The file may hold a literal credential; it must not be group- or
	// world-readable.
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config mode = %o, want 600", perm)
	}
	if info, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	} else if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("config dir mode = %o, want 700", perm)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".config-") {
			t.Errorf("temp file remains: %s", e.Name())
		}
	}
}

func TestConcurrentReadersSeeCompleteJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if _, err := Save(path, Default()); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	// Writer: alternate between two configs with different sizes.
	wg.Add(1)
	go func() {
		defer wg.Done()
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			c := Default()
			c.OpenRouter.APIKey = strings.Repeat("k", 10+i%50)
			i++
			if _, err := Save(path, c); err != nil {
				t.Errorf("save: %v", err)
				return
			}
		}
	}()
	// Readers: must never observe a partial file.
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 300 {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Errorf("read: %v", err)
					return
				}
				var v Config
				if err := json.Unmarshal(data, &v); err != nil {
					t.Errorf("partial read: %v", err)
					return
				}
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

func TestExternalEditAppliesLive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	m, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	edited := Default()
	edited.OpenRouter.APIKey = "sk-or-external-edit"
	if _, err := Save(path, edited); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 3*time.Second, func() bool { return m.Current().OpenRouter.APIKey == "sk-or-external-edit" }) {
		t.Fatalf("external edit not applied; current key = %q", m.Current().OpenRouter.APIKey)
	}
}

func TestSelfSaveDoesNotTriggerReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	m, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	var mu sync.Mutex
	count := 0
	m.OnChange(func(*Config) {
		mu.Lock()
		count++
		mu.Unlock()
	})

	if err := m.Update(func(c *Config) error {
		c.Filters = append(c.Filters, Filter{Name: "n", Pattern: "x", Action: ActionBlock, Enabled: true})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	after := count
	mu.Unlock()
	if after != 1 {
		t.Fatalf("notifications after update = %d, want 1", after)
	}
	time.Sleep(3 * WatchInterval)
	mu.Lock()
	after = count
	mu.Unlock()
	if after != 1 {
		t.Fatalf("self-save triggered reload: notifications = %d, want 1", after)
	}
}

func TestInvalidExternalEditKeepsLastGood(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	m, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	if err := m.Update(func(c *Config) error { c.OpenRouter.APIKey = "sk-or-good"; return nil }); err != nil {
		t.Fatal(err)
	}
	bad := `{"filters":[{"name":"bad","pattern":"[unclosed","action":"replace","replacement":"x","enabled":true}]}`
	if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 3*time.Second, func() bool { return m.WatchError() != nil }) {
		t.Fatal("invalid external edit not reported")
	}
	if got := m.Current().OpenRouter.APIKey; got != "sk-or-good" {
		t.Errorf("last-good config lost: key = %q", got)
	}
	// A subsequent valid edit recovers.
	fixed := Default()
	fixed.OpenRouter.APIKey = "sk-or-fixed"
	if _, err := Save(path, fixed); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 3*time.Second, func() bool {
		return m.WatchError() == nil && m.Current().OpenRouter.APIKey == "sk-or-fixed"
	}) {
		t.Fatalf("recovery failed: err=%v key=%q", m.WatchError(), m.Current().OpenRouter.APIKey)
	}
}

func TestManagerUpdateRejectsInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	m, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	err = m.Update(func(c *Config) error {
		c.Filters = append(c.Filters, Filter{Name: "bad", Pattern: "[unclosed", Action: ActionReplace, Replacement: "x"})
		return nil
	})
	if err == nil {
		t.Fatal("invalid update accepted")
	}
	if len(m.Current().Filters) != 0 {
		t.Error("config changed despite failed update")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatal(statErr)
	}
}

func TestCompilePatternCaseInsensitiveByDefault(t *testing.T) {
	re, err := CompilePattern("james brown")
	if err != nil {
		t.Fatal(err)
	}
	if !re.MatchString("Ask James Brown") {
		t.Error("pattern did not match case-insensitively")
	}
	re, err = CompilePattern(`(?-i)james brown`)
	if err != nil {
		t.Fatal(err)
	}
	if re.MatchString("Ask James Brown") {
		t.Error("(?-i) opt-out did not force case-sensitive matching")
	}
	if _, err := CompilePattern("[unclosed"); err == nil {
		t.Error("invalid pattern compiled without error")
	}
}

func TestModelPolicyDefaultsToPassthrough(t *testing.T) {
	if got := Default().Server.ModelPolicy; got != PolicyPassthrough {
		t.Errorf("Default policy = %q, want %q", got, PolicyPassthrough)
	}
	// A config written before model_policy existed normalizes to the default.
	c, err := decode([]byte(`{"server":{"proxy_addr":"127.0.0.1:1"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Server.ModelPolicy != PolicyPassthrough {
		t.Errorf("normalized policy = %q, want %q", c.Server.ModelPolicy, PolicyPassthrough)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("legacy config rejected: %v", err)
	}
}

func TestValidateRejectsUnknownModelPolicy(t *testing.T) {
	c := Default()
	c.Server.ModelPolicy = "gate"
	err := c.Validate()
	if err == nil {
		t.Fatal("unknown model policy accepted")
	}
	if !strings.Contains(err.Error(), "model_policy") {
		t.Errorf("error does not name the field: %v", err)
	}

	c.Server.ModelPolicy = PolicyAllowlist
	if err := c.Validate(); err != nil {
		t.Errorf("allowlist rejected: %v", err)
	}
}

func TestValidateAPIKeyReference(t *testing.T) {
	c := Default()
	c.OpenRouter.APIKey = "{keychain:FireworksAI/fireworks-api-key}"
	if err := c.Validate(); err != nil {
		t.Fatalf("valid keychain reference rejected: %v", err)
	}

	c.OpenRouter.APIKey = "fw_literal-secret"
	if err := c.Validate(); err != nil {
		t.Fatalf("literal key rejected: %v", err)
	}

	c.OpenRouter.APIKey = "{keychain:FireworksAI}"
	err := c.Validate()
	if err == nil {
		t.Fatal("malformed keychain reference accepted")
	}
	if !strings.Contains(err.Error(), "api_key") {
		t.Errorf("error does not name the field: %v", err)
	}
}

func TestValidateRedactions(t *testing.T) {
	c := Default()
	c.Redactions = []Redaction{
		{Name: "email", Pattern: `[\w.+-]+@[\w.-]+`, Template: "redacted$R@example.com", Enabled: true},
		{Name: "path", Pattern: `/Users/jane.doe/`, Template: `/Users/john.smith/`, Enabled: true},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("valid redactions rejected: %v", err)
	}

	cases := map[string]func(*Config){
		"missing name":     func(c *Config) { c.Redactions[0].Name = "" },
		"duplicate name":   func(c *Config) { c.Redactions[1].Name = c.Redactions[0].Name },
		"bad pattern":      func(c *Config) { c.Redactions[0].Pattern = "[unclosed" },
		"missing pattern":  func(c *Config) { c.Redactions[0].Pattern = "" },
		"missing template": func(c *Config) { c.Redactions[0].Template = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := Default()
			c.Redactions = []Redaction{
				{Name: "a", Pattern: "x", Template: "y", Enabled: true},
				{Name: "b", Pattern: "p", Template: "q", Enabled: true},
			}
			mutate(c)
			if err := c.Validate(); err == nil {
				t.Errorf("%s accepted", name)
			}
		})
	}
}

func TestRedactionsDefaultToEmptySlice(t *testing.T) {
	c, err := decode([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Redactions == nil || len(c.Redactions) != 0 {
		t.Fatalf("redactions = %#v", c.Redactions)
	}
}

func TestCloneCopiesRedactions(t *testing.T) {
	c := Default()
	c.Redactions = []Redaction{{Name: "a", Pattern: "x", Template: "y", Enabled: true}}
	cp := c.Clone()
	cp.Redactions[0].Name = "changed"
	if c.Redactions[0].Name != "a" {
		t.Error("Clone shared the redaction slice")
	}
}

func TestValidateModelPricing(t *testing.T) {
	base := func(p Pricing) *Config {
		c := Default()
		c.Models = []Model{{ID: "accounts/fireworks/models/x", Pricing: p, Enabled: true}}
		return c
	}

	if err := base(Pricing{Prompt: "0.0000003", Cache: "0.000000006", Completion: "0.0000012"}).Validate(); err != nil {
		t.Fatalf("valid pricing rejected: %v", err)
	}
	if err := base(Pricing{}).Validate(); err != nil {
		t.Fatalf("unset pricing rejected: %v", err)
	}

	for _, p := range []Pricing{
		{Prompt: "cheap", Completion: "1"},
		{Prompt: "1", Cache: "nope", Completion: "1"},
		{Prompt: "1", Completion: "-1"},
		{Prompt: "1", Cache: "NaN", Completion: "1"},
	} {
		err := base(p).Validate()
		if err == nil {
			t.Fatalf("invalid pricing accepted: %+v", p)
		}
		if !strings.Contains(err.Error(), "pricing") {
			t.Errorf("error does not name pricing: %v", err)
		}
	}
}

func TestPricingCacheRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	c := Default()
	c.Models = []Model{{
		ID:      "accounts/fireworks/models/deepseek-v4p1-flash-1017",
		Pricing: Pricing{Prompt: "0.0000003", Cache: "0.000000006", Completion: "0.0000012"},
		Enabled: true,
	}}
	if _, err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Models[0].Pricing != c.Models[0].Pricing {
		t.Fatalf("pricing = %+v, want %+v", got.Models[0].Pricing, c.Models[0].Pricing)
	}
}
