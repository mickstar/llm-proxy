// Package pricing turns the catalog's per-token price strings into rates and
// estimates the cost of an exchange when the upstream provider reports none.
// Nothing here talks to a provider: Fireworks, for one, publishes rates only as
// static tables, so the catalog is the sole source.
package pricing

import (
	"math"
	"strconv"
	"strings"

	"github.com/mickstar/llm-proxy/internal/config"
)

// Rate is a model's USD price per token.
type Rate struct {
	Prompt     float64 // a prompt token not served from cache
	Cache      float64 // a prompt token served from the provider's prompt cache
	Completion float64
}

// Cost estimates the charge for one exchange. Cached prompt tokens bill at the
// cache rate and the rest at the prompt rate; cached is clamped to [0, prompt]
// so a provider that reports more cached tokens than prompt tokens cannot
// produce a negative charge.
func (r Rate) Cost(prompt, cached, completion int) float64 {
	if cached < 0 {
		cached = 0
	}
	if cached > prompt {
		cached = prompt
	}
	return float64(prompt-cached)*r.Prompt +
		float64(cached)*r.Cache +
		float64(completion)*r.Completion
}

// Table maps model ids to per-token rates.
type Table map[string]Rate

// Build parses catalog pricing. An entry is skipped unless both prompt and
// completion prices parse as non-negative finite amounts: pricing traffic with
// a partial rate would silently under- or over-charge. An absent cache price
// bills cached tokens at the prompt rate, i.e. no cache discount.
func Build(models []config.Model) Table {
	t := make(Table, len(models))
	for _, m := range models {
		prompt, ok := parse(m.Pricing.Prompt)
		if !ok {
			continue
		}
		completion, ok := parse(m.Pricing.Completion)
		if !ok {
			continue
		}
		cache := prompt
		if c, ok := parse(m.Pricing.Cache); ok {
			cache = c
		}
		t[m.ID] = Rate{Prompt: prompt, Cache: cache, Completion: completion}
	}
	return t
}

// Lookup finds a model's rates by exact id, then by the longest id prefix that
// ends at a "-" or ":" boundary, so a dated or router variant such as
// "accounts/fireworks/models/deepseek-v4p1-flash-1017" inherits its base
// model's rates.
func (t Table) Lookup(id string) (Rate, bool) {
	if r, ok := t[id]; ok {
		return r, true
	}
	var best Rate
	longest := -1
	for key, r := range t {
		if key == "" || len(key) <= longest || !strings.HasPrefix(id, key) {
			continue
		}
		if rest := id[len(key):]; rest != "" && (rest[0] == '-' || rest[0] == ':') {
			best, longest = r, len(key)
		}
	}
	return best, longest >= 0
}

// parse accepts an empty string as unset, reported as ok=false.
func parse(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0, false
	}
	return v, true
}
