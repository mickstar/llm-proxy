// Package config defines the single JSON configuration file that is the source
// of truth for the proxy: server listen addresses, the OpenRouter credential and
// base URL, the model catalog, and the filter rules. It handles first-run default
// generation, validation, atomic persistence, and hot-reload of external changes.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"regexp/syntax"
	"strconv"
	"strings"

	"github.com/mickstar/llm-proxy/internal/secret"
)

// Filter action values.
const (
	ActionBlock   = "block"
	ActionReplace = "replace"
)

// Model policy values. The policy decides whether the catalog gates traffic.
const (
	// PolicyPassthrough forwards any model id the client names, and answers
	// GET /v1/models from OpenRouter. The catalog only affects the model-detail
	// endpoint and the TUI.
	PolicyPassthrough = "passthrough"
	// PolicyAllowlist forwards only models present and enabled in the catalog,
	// rejecting every other model locally before contacting OpenRouter.
	PolicyAllowlist = "allowlist"
)

// Defaults for a fresh configuration.
const (
	DefaultProxyAddr          = "127.0.0.1:8787"
	DefaultControlAddr        = "127.0.0.1:8788"
	DefaultModelPolicy        = PolicyPassthrough
	DefaultTrafficLogCapacity = 500
	DefaultBaseURL            = "https://openrouter.ai/api/v1"
)

// Config is the whole configuration file.
type Config struct {
	Server     ServerConfig     `json:"server"`
	OpenRouter OpenRouterConfig `json:"openrouter"`
	Models     []Model          `json:"models"`
	Filters    []Filter         `json:"filters"`
	Redactions []Redaction      `json:"redactions"`
}

// ServerConfig holds the two local listener addresses, the model policy, and
// log sizing.
type ServerConfig struct {
	ProxyAddr          string `json:"proxy_addr"`
	ControlAddr        string `json:"control_addr"`
	ModelPolicy        string `json:"model_policy"`
	TrafficLogCapacity int    `json:"traffic_log_capacity"`
}

// OpenRouterConfig holds the upstream endpoint and credential.
type OpenRouterConfig struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
}

// Pricing holds per-token USD prices as strings, exactly as OpenRouter's
// metadata endpoint returns them. Prompt and Completion are required for a
// price to be usable; Cache applies to prompt tokens served from the provider's
// prompt cache and defaults to the prompt rate when empty, i.e. no discount.
type Pricing struct {
	Prompt     string `json:"prompt"`
	Cache      string `json:"cache,omitempty"`
	Completion string `json:"completion"`
}

// Model is one entry of the user-configured catalog.
type Model struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	ContextLength       int      `json:"context_length"`
	Pricing             Pricing  `json:"pricing"`
	SupportedParameters []string `json:"supported_parameters"`
	Enabled             bool     `json:"enabled"`
}

// Filter is one user-defined content-filter rule. Exactly one match source is
// used: either an inline Pattern, or PatternFile, a path or glob naming files
// whose verbatim contents are matched. PatternFile stores only the path; the
// file contents are read when the filter compiles and are never persisted.
type Filter struct {
	Name        string `json:"name"`
	Pattern     string `json:"pattern"`
	PatternFile string `json:"pattern_file,omitempty"`
	Action      string `json:"action"`
	Replacement string `json:"replacement,omitempty"`
	Enabled     bool   `json:"enabled"`
}

// Redaction is one reversible substitution rule. Every match of Pattern in a
// request string value is replaced by Template, and the alias is mapped back to
// the matched text in the response. Template may use $R for a per-match token
// and $0..$9 for capture groups. The mapping lives in memory only.
type Redaction struct {
	Name     string `json:"name"`
	Pattern  string `json:"pattern"`
	Template string `json:"template"`
	Enabled  bool   `json:"enabled"`
}

// Default returns a new configuration with safe defaults: loopback listen
// addresses, empty catalog and filter list, and the public OpenRouter endpoint.
func Default() *Config {
	return &Config{
		Server: ServerConfig{
			ProxyAddr:          DefaultProxyAddr,
			ControlAddr:        DefaultControlAddr,
			ModelPolicy:        DefaultModelPolicy,
			TrafficLogCapacity: DefaultTrafficLogCapacity,
		},
		OpenRouter: OpenRouterConfig{BaseURL: DefaultBaseURL},
		Models:     []Model{},
		Filters:    []Filter{},
		Redactions: []Redaction{},
	}
}

// DefaultPath returns the default config path: llm-proxy/config.json under the
// user's config directory.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config dir: %w", err)
	}
	return filepath.Join(dir, "llm-proxy", "config.json"), nil
}

// ResolvePath returns override when non-empty, otherwise the default path.
func ResolvePath(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	return DefaultPath()
}

// Load reads, normalizes, and validates the config at path. When the file does
// not exist it is created with Default() contents.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		c := Default()
		if _, err := Save(path, c); err != nil {
			return nil, err
		}
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	c, err := decode(data)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return c, nil
}

// decode unmarshals and normalizes a config document.
func decode(data []byte) (*Config, error) {
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	c.normalize()
	return &c, nil
}

// normalize fills zero-valued fields that have defaults and guarantees non-nil
// slices so the file round-trips with `[]` rather than `null`.
func (c *Config) normalize() {
	if c.Server.ProxyAddr == "" {
		c.Server.ProxyAddr = DefaultProxyAddr
	}
	if c.Server.ControlAddr == "" {
		c.Server.ControlAddr = DefaultControlAddr
	}
	if c.Server.ModelPolicy == "" {
		c.Server.ModelPolicy = DefaultModelPolicy
	}
	if c.Server.TrafficLogCapacity <= 0 {
		c.Server.TrafficLogCapacity = DefaultTrafficLogCapacity
	}
	if c.OpenRouter.BaseURL == "" {
		c.OpenRouter.BaseURL = DefaultBaseURL
	}
	if c.Models == nil {
		c.Models = []Model{}
	}
	if c.Filters == nil {
		c.Filters = []Filter{}
	}
	if c.Redactions == nil {
		c.Redactions = []Redaction{}
	}
}

// CompilePattern compiles a filter pattern with the project's matching
// semantics: case-insensitive by default. A pattern may force case-sensitive
// matching by starting with the inline RE2 flag "(?-i)".
func CompilePattern(pattern string) (*regexp.Regexp, error) {
	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		return nil, patternError(err)
	}
	return re, nil
}

// patternError reports why a pattern failed to compile without repeating the
// pattern. A pattern can be a literal credential, and this text reaches the log
// file and API error bodies, so only the failure class survives.
func patternError(err error) error {
	var syntaxErr *syntax.Error
	if errors.As(err, &syntaxErr) {
		return fmt.Errorf("invalid pattern: %s", syntaxErr.Code)
	}
	return errors.New("invalid pattern")
}

// Validate checks the config contents: filter names are unique and non-empty,
// each filter defines exactly one match source (pattern or pattern_file) that
// compiles or resolves, actions are block/replace, and replace filters define a
// replacement. Errors name the offending filter and field.
func (c *Config) Validate() error {
	switch c.Server.ModelPolicy {
	case PolicyPassthrough, PolicyAllowlist:
	case "":
		return fmt.Errorf("server: model_policy is required")
	default:
		return fmt.Errorf("server: model_policy %q is not %q or %q", c.Server.ModelPolicy, PolicyPassthrough, PolicyAllowlist)
	}
	// The credential is either a literal secret or a keychain reference; a
	// reference that does not parse is a typo that would otherwise surface as
	// a failed lookup on the first proxied request.
	if secret.IsRef(c.OpenRouter.APIKey) {
		if _, _, err := secret.ParseRef(c.OpenRouter.APIKey); err != nil {
			return fmt.Errorf("openrouter: api_key: %w", err)
		}
	}
	seen := make(map[string]bool, len(c.Filters))
	for i := range c.Filters {
		f := &c.Filters[i]
		label := f.Name
		if label == "" {
			label = fmt.Sprintf("#%d", i+1)
		}
		if f.Name == "" {
			return fmt.Errorf("filter %s: name is required", label)
		}
		if seen[f.Name] {
			return fmt.Errorf("filter %q: duplicate filter name", f.Name)
		}
		seen[f.Name] = true
		if f.Pattern == "" && f.PatternFile == "" {
			return fmt.Errorf("filter %q: pattern or pattern_file is required", f.Name)
		}
		if f.Pattern != "" && f.PatternFile != "" {
			return fmt.Errorf("filter %q: pattern and pattern_file are mutually exclusive", f.Name)
		}
		if _, err := CompileFilter(*f); err != nil {
			return fmt.Errorf("filter %q: %w", f.Name, err)
		}
		switch f.Action {
		case ActionBlock:
			if f.Replacement != "" {
				return fmt.Errorf("filter %q: action %q must not define a replacement", f.Name, ActionBlock)
			}
		case ActionReplace:
			if f.Replacement == "" {
				return fmt.Errorf("filter %q: action %q requires a replacement", f.Name, ActionReplace)
			}
		case "":
			return fmt.Errorf("filter %q: action is required", f.Name)
		default:
			return fmt.Errorf("filter %q: action %q is not %q or %q", f.Name, f.Action, ActionBlock, ActionReplace)
		}
	}
	seenRedactions := make(map[string]bool, len(c.Redactions))
	for i := range c.Redactions {
		r := &c.Redactions[i]
		label := r.Name
		if label == "" {
			label = fmt.Sprintf("#%d", i+1)
		}
		if r.Name == "" {
			return fmt.Errorf("redaction %s: name is required", label)
		}
		if seenRedactions[r.Name] {
			return fmt.Errorf("redaction %q: duplicate redaction name", r.Name)
		}
		seenRedactions[r.Name] = true
		if r.Pattern == "" {
			return fmt.Errorf("redaction %q: pattern is required", r.Name)
		}
		if _, err := CompilePattern(r.Pattern); err != nil {
			return fmt.Errorf("redaction %q: pattern: %w", r.Name, err)
		}
		if r.Template == "" {
			return fmt.Errorf("redaction %q: template is required", r.Name)
		}
	}
	for i := range c.Models {
		m := &c.Models[i]
		if strings.TrimSpace(m.ID) == "" {
			return fmt.Errorf("model #%d: id is required", i+1)
		}
		if err := validatePrice(m.Pricing.Prompt); err != nil {
			return fmt.Errorf("model %q: pricing.prompt: %w", m.ID, err)
		}
		if err := validatePrice(m.Pricing.Cache); err != nil {
			return fmt.Errorf("model %q: pricing.cache: %w", m.ID, err)
		}
		if err := validatePrice(m.Pricing.Completion); err != nil {
			return fmt.Errorf("model %q: pricing.completion: %w", m.ID, err)
		}
		if m.ContextLength < 0 {
			return fmt.Errorf("model %q: context_length must not be negative", m.ID)
		}
	}
	return nil
}

// validatePrice rejects a price string that is not a non-negative USD amount
// per token. Empty means "unset" and is allowed: the catalog tolerates entries
// fetched without pricing.
func validatePrice(s string) error {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return fmt.Errorf("%q is not a number", s)
	}
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return fmt.Errorf("%q is not a non-negative finite amount", s)
	}
	return nil
}

// Clone returns a deep copy so snapshots handed to readers are never mutated
// by later updates.
func (c *Config) Clone() *Config {
	cp := *c
	cp.Models = make([]Model, len(c.Models))
	for i, m := range c.Models {
		m.SupportedParameters = append([]string(nil), m.SupportedParameters...)
		cp.Models[i] = m
	}
	cp.Filters = append([]Filter(nil), c.Filters...)
	cp.Redactions = append([]Redaction(nil), c.Redactions...)
	return &cp
}

// Save validates c and atomically writes it to path (temp file in the same
// directory, then rename). It returns the exact bytes written.
func Save(path string, c *Config) ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode config %s: %w", path, err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create config dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("create temp config in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("sync temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("close temp config: %w", err)
	}
	// The file can hold a literal upstream credential, so keep it owner-only.
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return nil, fmt.Errorf("chmod temp config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return nil, fmt.Errorf("replace config %s: %w", path, err)
	}
	return data, nil
}
