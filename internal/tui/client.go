package tui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mickstar/llm-proxy/internal/traffic"
)

// The types below mirror the control API's JSON shapes (snake_case). They are
// defined here rather than imported from the server packages so the TUI stays
// a pure HTTP client; only traffic.Entry is shared because the SSE stream and
// the traffic list serialize it verbatim.

// Pricing holds per-token USD prices as strings. Cache is the rate for prompt
// tokens served from the provider's prompt cache.
type Pricing struct {
	Prompt     string `json:"prompt"`
	Cache      string `json:"cache,omitempty"`
	Completion string `json:"completion"`
}

// Model is one catalog entry as returned by the control API.
type Model struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	ContextLength       int      `json:"context_length"`
	Pricing             Pricing  `json:"pricing"`
	SupportedParameters []string `json:"supported_parameters"`
	Enabled             bool     `json:"enabled"`
}

// Filter is one content-filter rule.
type Filter struct {
	Name        string `json:"name"`
	Pattern     string `json:"pattern"`
	PatternFile string `json:"pattern_file,omitempty"`
	Action      string `json:"action"`
	Replacement string `json:"replacement,omitempty"`
	Enabled     bool   `json:"enabled"`
}

// ServerConfig holds the two listener addresses, the model policy, and the log
// capacity.
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

// Config is the whole configuration returned by GET /api/config.
type Config struct {
	Server     ServerConfig     `json:"server"`
	OpenRouter OpenRouterConfig `json:"openrouter"`
	Models     []Model          `json:"models"`
	Filters    []Filter         `json:"filters"`
}

// HaltStatus mirrors the proxy breaker reported by GET /api/health.
type HaltStatus struct {
	Halted  bool      `json:"halted"`
	At      time.Time `json:"halted_at"`
	Filter  string    `json:"halted_filter"`
	Refused int       `json:"refused"`
}

// Health is the GET /api/health response.
type Health struct {
	Status      string     `json:"status"`
	ConfigError string     `json:"config_error"`
	Halt        HaltStatus `json:"halt"`
}

// RedactionInstance is one recorded alias from GET /api/redactions. The alias
// was sent upstream in place of Original.
type RedactionInstance struct {
	Alias     string    `json:"alias"`
	Original  string    `json:"original"`
	Rule      string    `json:"rule"`
	Count     int       `json:"count"`
	FirstSeen time.Time `json:"first_seen"`
	Conflict  bool      `json:"conflict,omitempty"`
}

// TestResult is the POST /api/filters/test response.
type TestResult struct {
	Matches int      `json:"matches"`
	Matched []string `json:"matched"`
	Result  string   `json:"result"`
}

// Client talks to the control API over HTTP.
type Client struct {
	base   string
	http   *http.Client
	stream *http.Client
}

// NewClient returns a client for the control API rooted at base (e.g.
// "http://127.0.0.1:8788").
func NewClient(base string) *Client {
	return &Client{
		base:   strings.TrimRight(base, "/"),
		http:   &http.Client{Timeout: 30 * time.Second},
		stream: &http.Client{}, // no timeout: the SSE stream is long-lived
	}
}

// do performs one request, decoding a successful JSON response into out (when
// non-nil) and surfacing the API's {"error":...} body as the returned error.
func (c *Client) do(method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.base+path, r)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %s", method, path, readAPIError(resp))
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil && err != io.EOF {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	return nil
}

// readAPIError extracts the {"error":"..."} message, falling back to the status.
func readAPIError(resp *http.Response) string {
	var e struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e); err == nil && e.Error != "" {
		return e.Error
	}
	return resp.Status
}

// Health probes GET /api/health.
func (c *Client) Health() (Health, error) {
	var h Health
	err := c.do(http.MethodGet, "/api/health", nil, &h)
	return h, err
}

// GetConfig reads GET /api/config.
func (c *Client) GetConfig() (Config, error) {
	var cfg Config
	err := c.do(http.MethodGet, "/api/config", nil, &cfg)
	return cfg, err
}

// PutSettings writes server and OpenRouter settings via PUT /api/settings.
func (c *Client) PutSettings(s ServerConfig, o OpenRouterConfig) (Config, error) {
	var cfg Config
	body := map[string]any{"server": s, "openrouter": o}
	err := c.do(http.MethodPut, "/api/settings", body, &cfg)
	return cfg, err
}

// ListFilters reads GET /api/filters.
func (c *Client) ListFilters() ([]Filter, error) {
	var fs []Filter
	err := c.do(http.MethodGet, "/api/filters", nil, &fs)
	return fs, err
}

// CreateFilter creates a filter via POST /api/filters.
func (c *Client) CreateFilter(f Filter) (Filter, error) {
	var out Filter
	err := c.do(http.MethodPost, "/api/filters", f, &out)
	return out, err
}

// UpdateFilter replaces the filter named name via PUT /api/filters/{name}.
func (c *Client) UpdateFilter(name string, f Filter) (Filter, error) {
	var out Filter
	err := c.do(http.MethodPut, "/api/filters/"+url.PathEscape(name), f, &out)
	return out, err
}

// DeleteFilter removes the filter named name via DELETE /api/filters/{name}.
func (c *Client) DeleteFilter(name string) error {
	return c.do(http.MethodDelete, "/api/filters/"+url.PathEscape(name), nil, nil)
}

// TestFilter runs POST /api/filters/test for an inline pattern or a
// pattern_file against sample text.
func (c *Client) TestFilter(pattern, patternFile, replacement, sample string) (TestResult, error) {
	var tr TestResult
	err := c.do(http.MethodPost, "/api/filters/test", map[string]string{
		"pattern":      pattern,
		"pattern_file": patternFile,
		"replacement":  replacement,
		"sample":       sample,
	}, &tr)
	return tr, err
}

// ListModels reads GET /api/models.
func (c *Client) ListModels() ([]Model, error) {
	var ms []Model
	err := c.do(http.MethodGet, "/api/models", nil, &ms)
	return ms, err
}

// CreateModel adds a model via POST /api/models.
func (c *Client) CreateModel(m Model) (Model, error) {
	var out Model
	err := c.do(http.MethodPost, "/api/models", m, &out)
	return out, err
}

// UpdateModel replaces the model with id via PUT /api/models/{id}. The id's
// path segments are escaped individually so the '/' separators stay literal.
func (c *Client) UpdateModel(id string, m Model) (Model, error) {
	var out Model
	err := c.do(http.MethodPut, modelPath(id), m, &out)
	return out, err
}

// DeleteModel removes the model with id via DELETE /api/models/{id}.
func (c *Client) DeleteModel(id string) error {
	return c.do(http.MethodDelete, modelPath(id), nil, nil)
}

// FetchModel adds/updates a model from OpenRouter via POST /api/models/fetch.
func (c *Client) FetchModel(id string) (Model, error) {
	var m Model
	err := c.do(http.MethodPost, "/api/models/fetch", map[string]string{"id": id}, &m)
	return m, err
}

// modelPath builds /api/models/{id} keeping '/' separators literal.
func modelPath(id string) string {
	parts := strings.Split(id, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return "/api/models/" + strings.Join(parts, "/")
}

// ListTraffic reads GET /api/traffic?limit=N (newest-first, bodies omitted).
// Resume clears the halted state and returns the resulting status.
func (c *Client) Resume() (HaltStatus, error) {
	var st HaltStatus
	err := c.do(http.MethodPost, "/api/resume", nil, &st)
	return st, err
}

func (c *Client) ListRedactions() ([]RedactionInstance, error) {
	var items []RedactionInstance
	err := c.do(http.MethodGet, "/api/redactions", nil, &items)
	return items, err
}

func (c *Client) ListTraffic(limit int) ([]traffic.Entry, error) {
	var es []traffic.Entry
	err := c.do(http.MethodGet, "/api/traffic?limit="+strconv.Itoa(limit), nil, &es)
	return es, err
}

// GetTraffic reads GET /api/traffic/{id} including request/response bodies.
func (c *Client) GetTraffic(id uint64) (traffic.Entry, error) {
	var e traffic.Entry
	err := c.do(http.MethodGet, "/api/traffic/"+strconv.FormatUint(id, 10), nil, &e)
	return e, err
}

// StreamTraffic consumes GET /api/traffic/stream, invoking fn for each entry
// until ctx is cancelled or the connection ends. It returns the terminal error
// (nil when the stream ended cleanly or via cancellation of ctx).
func (c *Client) StreamTraffic(ctx context.Context, fn func(traffic.Entry)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/traffic/stream", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.stream.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("traffic stream: %s", readAPIError(resp))
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "" {
			continue
		}
		var e traffic.Entry
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			continue // skip malformed frames rather than dropping the stream
		}
		fn(e)
	}
	return sc.Err()
}
