// Package catalog holds an immutable snapshot of the enabled model entries and
// serves the OpenAI-compatible model listing endpoints from it.
package catalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/mickstar/llm-proxy/internal/config"
	"github.com/mickstar/llm-proxy/internal/oai"
)

// modelsPrefix is the local path that ServeHTTP serves.
const modelsPrefix = "/v1/models"

// ModelObject is the OpenAI-compatible representation of a catalog entry.
type ModelObject struct {
	ID            string `json:"id"`
	Object        string `json:"object"`
	Created       int64  `json:"created"`
	OwnedBy       string `json:"owned_by"`
	Name          string `json:"name,omitempty"`
	ContextLength int    `json:"context_length,omitempty"`
}

// Catalog is an immutable snapshot containing only enabled model entries, in
// the configured order.
type Catalog struct {
	models []config.Model
	index  map[string]int
}

// Build creates an immutable snapshot containing only enabled entries.
func Build(models []config.Model) *Catalog {
	c := &Catalog{index: make(map[string]int, len(models))}
	for _, m := range models {
		if !m.Enabled {
			continue
		}
		m.SupportedParameters = append([]string(nil), m.SupportedParameters...)
		c.index[m.ID] = len(c.models)
		c.models = append(c.models, m)
	}
	return c
}

// Has reports whether an enabled model with id exists.
func (c *Catalog) Has(id string) bool {
	_, ok := c.index[id]
	return ok
}

// Get returns the enabled model with id.
func (c *Catalog) Get(id string) (config.Model, bool) {
	i, ok := c.index[id]
	if !ok {
		return config.Model{}, false
	}
	return c.models[i], true
}

// List returns the enabled entries in configured order.
func (c *Catalog) List() []config.Model {
	return append([]config.Model(nil), c.models...)
}

// ListObjects returns the enabled entries as OpenAI model objects.
func (c *Catalog) ListObjects() []ModelObject {
	out := make([]ModelObject, 0, len(c.models))
	for _, m := range c.models {
		out = append(out, object(m))
	}
	return out
}

// Object returns the OpenAI model object for an enabled model id.
func (c *Catalog) Object(id string) (ModelObject, bool) {
	m, ok := c.Get(id)
	if !ok {
		return ModelObject{}, false
	}
	return object(m), true
}

func object(m config.Model) ModelObject {
	return ModelObject{
		ID:            m.ID,
		Object:        "model",
		Created:       0,
		OwnedBy:       "openrouter",
		Name:          m.Name,
		ContextLength: m.ContextLength,
	}
}

type listResponse struct {
	Object string        `json:"object"`
	Data   []ModelObject `json:"data"`
}

// ServeHTTP serves GET /v1/models and GET /v1/models/{id} where {id} may contain
// slashes (e.g. "deepseek/deepseek-v4.1-flash"). Non-GET -> 405. Unknown id ->
// 404 via oai.WriteError.
func (c *Catalog) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		oai.WriteError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "only GET is supported for this endpoint")
		return
	}
	id, ok := modelIDFromPath(r)
	if !ok {
		oai.WriteError(w, http.StatusNotFound, "invalid_request_error", "model_not_found", "unknown model")
		return
	}
	if id == "" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(listResponse{Object: "list", Data: c.ListObjects()})
		return
	}
	obj, found := c.Object(id)
	if !found {
		oai.WriteError(w, http.StatusNotFound, "invalid_request_error", "model_not_found", fmt.Sprintf("model %q not found", id))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(obj)
}

// modelIDFromPath extracts the (URL-decoded) model id from the request path.
// An empty id with ok=true means the collection endpoint.
func modelIDFromPath(r *http.Request) (string, bool) {
	escaped := r.URL.EscapedPath()
	if rest, found := strings.CutPrefix(escaped, modelsPrefix); found {
		if rest == "" || rest == "/" {
			return "", true
		}
		if !strings.HasPrefix(rest, "/") {
			// e.g. /v1/modelsfoo
			return "", false
		}
		id, err := url.PathUnescape(rest[1:])
		if err != nil {
			return "", false
		}
		return id, true
	}
	// Tolerate being mounted with the prefix stripped.
	p := strings.TrimPrefix(escaped, "/")
	if p == "" {
		return "", true
	}
	id, err := url.PathUnescape(p)
	if err != nil {
		return "", false
	}
	return id, true
}

// ParseModelID splits an OpenRouter id into author and slug on the FIRST '/'.
// The slug keeps any ':variant' suffix. Missing/empty parts -> error.
func ParseModelID(id string) (author, slug string, err error) {
	author, slug, ok := strings.Cut(id, "/")
	if !ok || author == "" || slug == "" {
		return "", "", fmt.Errorf("invalid model id %q: want author/slug", id)
	}
	return author, slug, nil
}

// Upsert inserts m by ID or replaces the existing entry, preserving order and
// returning the new slice plus whether m was newly added.
func Upsert(models []config.Model, m config.Model) (out []config.Model, added bool) {
	out = append([]config.Model(nil), models...)
	for i := range out {
		if out[i].ID == m.ID {
			out[i] = m
			return out, false
		}
	}
	out = append(out, m)
	return out, true
}
