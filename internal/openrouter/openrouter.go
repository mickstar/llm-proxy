// Package openrouter is a minimal HTTP client for the OpenRouter model metadata
// endpoint used to populate the local catalog.
package openrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mickstar/llm-proxy/internal/catalog"
	"github.com/mickstar/llm-proxy/internal/config"
)

// httpTimeout bounds every metadata request.
const httpTimeout = 30 * time.Second

// maxBody caps how much of a response body is read.
const maxBody = 1 << 20

// Client fetches model metadata from an OpenRouter-compatible base URL.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// New returns a Client for baseURL. apiKey may be empty, in which case no
// Authorization header is sent.
func New(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: httpTimeout},
	}
}

type payload struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	ContextLength       int      `json:"context_length"`
	Pricing             pricing  `json:"pricing"`
	SupportedParameters []string `json:"supported_parameters"`
}

type pricing struct {
	Prompt     string `json:"prompt"`
	Completion string `json:"completion"`
}

// FetchModel validates id via catalog.ParseModelID, GETs
// {baseURL}/model/{author}/{slug}, and returns the model with Enabled=true.
// Fetched pricing stays the exact string OpenRouter returned. Decoding is
// tolerant: unknown fields are ignored, missing fields land as zero values, and
// both a top-level model object and a {"data":{...}} envelope are accepted.
func (c *Client) FetchModel(ctx context.Context, id string) (*config.Model, error) {
	author, slug, err := catalog.ParseModelID(id)
	if err != nil {
		return nil, err
	}
	endpoint := c.baseURL + "/model/" + url.PathEscape(author) + "/" + url.PathEscape(slug)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("openrouter: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openrouter: fetch %s: %w", id, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("openrouter: read response for %s: %w", id, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openrouter: fetch %s: status %d: %s", id, resp.StatusCode, excerpt(body))
	}
	var p payload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("openrouter: decode model %s: %w", id, err)
	}
	if p.ID == "" {
		var env struct {
			Data payload `json:"data"`
		}
		if err := json.Unmarshal(body, &env); err == nil && env.Data.ID != "" {
			p = env.Data
		}
	}
	m := &config.Model{
		ID:                  p.ID,
		Name:                p.Name,
		ContextLength:       p.ContextLength,
		Pricing:             config.Pricing{Prompt: p.Pricing.Prompt, Completion: p.Pricing.Completion},
		SupportedParameters: p.SupportedParameters,
		Enabled:             true,
	}
	if m.ID == "" {
		m.ID = id
	}
	return m, nil
}

// excerpt returns a short, single-line, bounded rendering of a response body.
func excerpt(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	const max = 200
	if len(s) > max {
		s = s[:max] + "..."
	}
	return s
}
