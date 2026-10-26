// Package usage extracts token accounting from proxied response bodies.
//
// It reads no schema of its own beyond the usage object every OpenAI-compatible
// provider returns: a top-level `"usage"` member in a JSON response, or the
// final `data:` event of a streamed response. OpenRouter reports usage exactly
// once, in the last chunk before `[DONE]`, so the extractor keeps a bounded
// tail of the body and reads the last complete usage object from it. That makes
// memory use independent of response length and lets a stream be teed without
// delaying or rewriting a byte.
package usage

import (
	"bytes"
	"encoding/json"
	"io"
	"sync"
)

const (
	// tailLimit is how many trailing body bytes are retained for extraction.
	// Usage is small and providers emit it at the end of the body, so the tail
	// always contains it.
	tailLimit = 256 << 10
	// usageKey is the JSON member searched for, quotes included, so text such
	// as `usage:` in prose does not match.
	usageKey = `"usage"`
)

// Usage is the token accounting reported by a provider. PromptTokens and
// CompletionTokens are normalized from both the chat-completions names and the
// Responses API's input/output names. Cost is the provider-reported charge in
// USD credits, when the provider includes one.
type Usage struct {
	PromptTokens int `json:"prompt_tokens"`
	// CachedPromptTokens is the part of PromptTokens the provider served from
	// its prompt cache; it bills at the cache rate rather than the prompt rate.
	CachedPromptTokens int `json:"cached_prompt_tokens"`
	CompletionTokens   int `json:"completion_tokens"`
	TotalTokens        int `json:"total_tokens"`
	// CostUSD is the provider-reported charge. CostReported distinguishes an
	// explicit zero from an absent field, so the proxy only estimates a cost
	// when the provider stayed silent.
	CostUSD      float64 `json:"cost_usd"`
	CostReported bool    `json:"-"`
}

// Valid reports whether the usage carries any tokens or cost. A zero usage is
// treated as absent rather than recorded as a request that consumed nothing.
func (u Usage) Valid() bool {
	return u.PromptTokens > 0 || u.CompletionTokens > 0 || u.TotalTokens > 0 || u.CostUSD > 0
}

// Capture is a concurrency-safe slot for one response's usage. The response
// body is read by the reverse proxy while the request handler may already be
// unwinding, so writes and reads are synchronized.
type Capture struct {
	mu sync.Mutex
	u  Usage
	ok bool
}

// Set stores u when it carries tokens. A later report for the same response
// replaces an earlier one; providers report cumulative values, never deltas.
func (c *Capture) Set(u Usage) {
	if c == nil || !u.Valid() {
		return
	}
	c.mu.Lock()
	c.u = u
	c.ok = true
	c.mu.Unlock()
}

// Get returns the captured usage and whether any was reported.
func (c *Capture) Get() (Usage, bool) {
	if c == nil {
		return Usage{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.u, c.ok
}

// NewReader wraps rc so that, once the body reaches EOF (or is closed), the
// last usage object in its trailing bytes is stored in c. The returned reader
// passes every byte through unchanged. A nil capture returns rc untouched.
func NewReader(rc io.ReadCloser, c *Capture) io.ReadCloser {
	if rc == nil || c == nil {
		return rc
	}
	return &reader{rc: rc, capture: c}
}

type reader struct {
	rc      io.ReadCloser
	capture *Capture
	tail    []byte
	once    sync.Once
}

func (r *reader) Read(p []byte) (int, error) {
	n, err := r.rc.Read(p)
	if n > 0 {
		r.appendTail(p[:n])
	}
	if err == io.EOF {
		r.finish()
	}
	return n, err
}

func (r *reader) Close() error {
	r.finish()
	return r.rc.Close()
}

// appendTail keeps at most tailLimit trailing bytes, compacting in place so a
// long response does not allocate per read.
func (r *reader) appendTail(p []byte) {
	if len(p) >= tailLimit {
		r.tail = append(r.tail[:0], p[len(p)-tailLimit:]...)
		return
	}
	if len(r.tail)+len(p) > tailLimit {
		drop := len(r.tail) + len(p) - tailLimit
		r.tail = append(r.tail[:0], r.tail[drop:]...)
	}
	r.tail = append(r.tail, p...)
}

func (r *reader) finish() {
	r.once.Do(func() {
		if u, ok := Extract(r.tail); ok {
			r.capture.Set(u)
		}
	})
}

// Extract returns the last complete usage object in b. It searches backwards
// so a model whose own output quotes `"usage"` cannot shadow the provider's
// real accounting at the end of the body.
func Extract(b []byte) (Usage, bool) {
	for end := len(b); end > 0; {
		idx := bytes.LastIndex(b[:end], []byte(usageKey))
		if idx < 0 {
			return Usage{}, false
		}
		if u, ok := parseAt(b, idx+len(usageKey)); ok {
			return u, true
		}
		end = idx
	}
	return Usage{}, false
}

// parseAt decodes the object following the usage key at b[i:]. It returns
// false when the key is not followed by a complete JSON object, which lets the
// caller continue searching for an earlier occurrence.
func parseAt(b []byte, i int) (Usage, bool) {
	i = skipSpace(b, i)
	if i >= len(b) || b[i] != ':' {
		return Usage{}, false
	}
	i = skipSpace(b, i+1)
	if i >= len(b) || b[i] != '{' {
		return Usage{}, false
	}
	end, ok := objectEnd(b[i:])
	if !ok {
		return Usage{}, false
	}

	var raw struct {
		PromptTokens       int      `json:"prompt_tokens"`
		CompletionTokens   int      `json:"completion_tokens"`
		InputTokens        int      `json:"input_tokens"`
		OutputTokens       int      `json:"output_tokens"`
		TotalTokens        int      `json:"total_tokens"`
		Cost               *float64 `json:"cost"`
		PromptTokensDetail struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		InputTokensDetail struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"input_tokens_details"`
	}
	if err := json.Unmarshal(b[i:i+end], &raw); err != nil {
		return Usage{}, false
	}

	u := Usage{
		PromptTokens:       raw.PromptTokens,
		CachedPromptTokens: raw.PromptTokensDetail.CachedTokens,
		CompletionTokens:   raw.CompletionTokens,
		TotalTokens:        raw.TotalTokens,
	}
	if raw.Cost != nil {
		u.CostUSD = *raw.Cost
		u.CostReported = true
	}
	if u.PromptTokens == 0 {
		u.PromptTokens = raw.InputTokens
	}
	if u.CachedPromptTokens == 0 {
		u.CachedPromptTokens = raw.InputTokensDetail.CachedTokens
	}
	if u.CompletionTokens == 0 {
		u.CompletionTokens = raw.OutputTokens
	}
	if u.TotalTokens == 0 {
		u.TotalTokens = u.PromptTokens + u.CompletionTokens
	}
	// Cached tokens are a subset of the prompt; a provider that reports more
	// than that is clamped so cost estimation never goes negative.
	if u.CachedPromptTokens > u.PromptTokens {
		u.CachedPromptTokens = u.PromptTokens
	}
	return u, u.Valid()
}

// objectEnd returns the length of the JSON object at the start of b, respecting
// strings and escapes. b[0] must be '{'.
func objectEnd(b []byte) (int, bool) {
	depth := 0
	inString := false
	escaped := false
	for i, c := range b {
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i + 1, true
			}
		}
	}
	return 0, false
}

func skipSpace(b []byte, i int) int {
	for i < len(b) {
		switch b[i] {
		case ' ', '\t', '\r', '\n':
			i++
		default:
			return i
		}
	}
	return i
}
