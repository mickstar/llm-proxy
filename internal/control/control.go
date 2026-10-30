// Package control implements the local HTTP control API: configuration
// read/write, filter and model management, model fetching, traffic log reads,
// recorded redaction aliases, and a live SSE traffic stream. It is the only surface the TUI (and a future
// GUI) uses to talk to the server.
package control

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/mickstar/llm-proxy/internal/catalog"
	"github.com/mickstar/llm-proxy/internal/config"
	"github.com/mickstar/llm-proxy/internal/filter"
	"github.com/mickstar/llm-proxy/internal/halt"
	"github.com/mickstar/llm-proxy/internal/openrouter"
	"github.com/mickstar/llm-proxy/internal/redact"
	"github.com/mickstar/llm-proxy/internal/secret"
	"github.com/mickstar/llm-proxy/internal/stats"
	"github.com/mickstar/llm-proxy/internal/traffic"
)

// Server serves the control API.
type Server struct {
	mgr     *config.Manager
	log     *traffic.Log
	aliases *redact.Table
	preview *redact.Table // stable tokens for the filter test pane
	halt    *halt.State
	stats   *stats.Store
	secrets *secret.Resolver
}

// New builds a control API server over the given config manager, traffic log,
// alias table, breaker, usage store, and credential resolver. A nil table
// yields an empty read-only view; a nil store yields an empty metrics view; a
// nil resolver reads the macOS keychain.
func New(mgr *config.Manager, log *traffic.Log, aliases *redact.Table, breaker *halt.State, store *stats.Store, secrets *secret.Resolver) *Server {
	if aliases == nil {
		aliases = redact.NewTable(0)
	}
	if breaker == nil {
		breaker = halt.New()
	}
	if store == nil {
		store = stats.New("")
	}
	if secrets == nil {
		secrets = secret.NewResolver()
	}
	return &Server{mgr: mgr, log: log, aliases: aliases, preview: redact.NewTable(0), halt: breaker, stats: store, secrets: secrets}
}

// Handler returns the control API router.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/stats", s.getStats)
	mux.HandleFunc("GET /api/config", s.getConfig)
	mux.HandleFunc("PUT /api/settings", s.putSettings)
	mux.HandleFunc("PUT /api/key", s.putKey)

	mux.HandleFunc("GET /api/filters", s.listFilters)
	mux.HandleFunc("POST /api/filters", s.createFilter)
	mux.HandleFunc("PUT /api/filters/{name}", s.updateFilter)
	mux.HandleFunc("DELETE /api/filters/{name}", s.deleteFilter)
	mux.HandleFunc("POST /api/filters/test", s.testFilter)

	mux.HandleFunc("GET /api/models", s.listModels)
	mux.HandleFunc("POST /api/models", s.createModel)
	mux.HandleFunc("POST /api/models/fetch", s.fetchModel)
	mux.HandleFunc("PUT /api/models/{id...}", s.updateModel)
	mux.HandleFunc("DELETE /api/models/{id...}", s.deleteModel)

	mux.HandleFunc("GET /api/traffic", s.listTraffic)
	mux.HandleFunc("GET /api/traffic/stream", s.streamTraffic)
	mux.HandleFunc("GET /api/traffic/{id}", s.getTraffic)

	mux.HandleFunc("GET /api/redactions", s.listRedactions)
	mux.HandleFunc("POST /api/redactions/rules", s.createRedactionRule)
	mux.HandleFunc("PUT /api/redactions/rules/{name}", s.updateRedactionRule)
	mux.HandleFunc("DELETE /api/redactions/rules/{name}", s.deleteRedactionRule)

	mux.HandleFunc("GET /api/halt", s.getHalt)
	mux.HandleFunc("POST /api/resume", s.resume)
	return guardLocal(mux)
}

// guardLocal refuses control requests that did not address the loopback
// listener and any browser cross-origin traffic. The API returns the upstream
// credential and redaction originals, so a page that points its own hostname at
// 127.0.0.1 must not be able to read them.
func guardLocal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" {
			writeErr(w, http.StatusForbidden, "cross-origin requests are not allowed")
			return
		}
		if !isLoopbackHost(r.Host) {
			writeErr(w, http.StatusForbidden, "control API is loopback-only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isLoopbackHost reports whether a Host header names the local machine.
func isLoopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// --- health / config / settings ---

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{"status": "ok", "halt": s.halt.Status()}
	if err := s.mgr.WatchError(); err != nil {
		resp["config_error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.mgr.Current())
}

// statsResponse is the metrics snapshot plus the breaker state, so a poller
// needs one request to render both throughput and a halt.
type statsResponse struct {
	stats.Snapshot
	Halt halt.Status `json:"halt"`
}

func (s *Server) getStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, statsResponse{Snapshot: s.stats.Snapshot(), Halt: s.halt.Status()})
}

type settingsUpdate struct {
	Server     *config.ServerConfig     `json:"server"`
	OpenRouter *config.OpenRouterConfig `json:"openrouter"`
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var up settingsUpdate
	if err := json.NewDecoder(r.Body).Decode(&up); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	err := s.mgr.Update(func(c *config.Config) error {
		if up.Server != nil {
			// A caller that predates model_policy omits the field; keep the
			// stored policy rather than resetting it to the default.
			prev := c.Server.ModelPolicy
			c.Server = *up.Server
			if c.Server.ModelPolicy == "" {
				c.Server.ModelPolicy = prev
			}
		}
		if up.OpenRouter != nil {
			c.OpenRouter = *up.OpenRouter
		}
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.mgr.Current())
}

// keyUpdate sets the upstream credential and, optionally, the endpoint it is
// used against. Both fields are optional so a client that only knows the key
// cannot reset the base URL to the default.
type keyUpdate struct {
	APIKey  *string `json:"api_key"`
	BaseURL *string `json:"base_url"`
}

// putKey stores a literal secret or a keychain reference as the upstream
// credential. A reference is resolved before it is saved so a typo or a
// missing keychain item is reported now instead of as a 401 on the first
// proxied request.
func (s *Server) putKey(w http.ResponseWriter, r *http.Request) {
	var up keyUpdate
	if err := json.NewDecoder(r.Body).Decode(&up); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if up.APIKey == nil && up.BaseURL == nil {
		writeErr(w, http.StatusBadRequest, "api_key or base_url is required")
		return
	}
	var key string
	if up.APIKey != nil {
		key = strings.TrimSpace(*up.APIKey)
		if secret.IsRef(key) {
			if _, err := s.secrets.Resolve(key); err != nil {
				writeErr(w, http.StatusBadRequest, "api_key: "+err.Error())
				return
			}
		}
	}
	err := s.mgr.Update(func(c *config.Config) error {
		if up.APIKey != nil {
			c.OpenRouter.APIKey = key
		}
		if up.BaseURL != nil {
			c.OpenRouter.BaseURL = strings.TrimSpace(*up.BaseURL)
		}
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.mgr.Current())
}

// --- filters ---

func (s *Server) listFilters(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.mgr.Current().Filters)
}

func (s *Server) createFilter(w http.ResponseWriter, r *http.Request) {
	var f config.Filter
	if err := json.NewDecoder(r.Body).Decode(&f); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	err := s.mgr.Update(func(c *config.Config) error {
		for _, existing := range c.Filters {
			if existing.Name == f.Name {
				return fmt.Errorf("filter %q already exists", f.Name)
			}
		}
		c.Filters = append(c.Filters, f)
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, f)
}

func (s *Server) updateFilter(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var f config.Filter
	if err := json.NewDecoder(r.Body).Decode(&f); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if f.Name == "" {
		f.Name = name
	}
	found := false
	err := s.mgr.Update(func(c *config.Config) error {
		for i := range c.Filters {
			if c.Filters[i].Name == name {
				if f.Name != name {
					for j := range c.Filters {
						if c.Filters[j].Name == f.Name {
							return fmt.Errorf("filter %q already exists", f.Name)
						}
					}
				}
				c.Filters[i] = f
				found = true
				return nil
			}
		}
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "filter not found: "+name)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (s *Server) deleteFilter(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	found := false
	err := s.mgr.Update(func(c *config.Config) error {
		out := c.Filters[:0]
		for _, f := range c.Filters {
			if f.Name == name {
				found = true
				continue
			}
			out = append(out, f)
		}
		c.Filters = out
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "filter not found: "+name)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type testRequest struct {
	Pattern     string `json:"pattern"`
	PatternFile string `json:"pattern_file"`
	Replacement string `json:"replacement"`
	Sample      string `json:"sample"`
}

func (s *Server) testFilter(w http.ResponseWriter, r *http.Request) {
	var req testRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Pattern == "" && req.PatternFile == "" {
		writeErr(w, http.StatusBadRequest, "pattern or pattern_file is required")
		return
	}
	if req.Pattern != "" && req.PatternFile != "" {
		writeErr(w, http.StatusBadRequest, "pattern and pattern_file are mutually exclusive")
		return
	}
	rule := config.Filter{Pattern: req.Pattern, PatternFile: req.PatternFile, Replacement: req.Replacement}
	matched, result, err := filter.Test(rule, req.Sample, s.preview)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if matched == nil {
		matched = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"matches": len(matched),
		"matched": matched,
		"result":  result,
	})
}

// --- models ---

func (s *Server) listModels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.mgr.Current().Models)
}

func (s *Server) createModel(w http.ResponseWriter, r *http.Request) {
	var m config.Model
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	err := s.mgr.Update(func(c *config.Config) error {
		for _, existing := range c.Models {
			if existing.ID == m.ID {
				return fmt.Errorf("model %q already exists", m.ID)
			}
		}
		c.Models = append(c.Models, m)
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) updateModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var m config.Model
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if m.ID == "" {
		m.ID = id
	}
	found := false
	err := s.mgr.Update(func(c *config.Config) error {
		for i := range c.Models {
			if c.Models[i].ID == id {
				c.Models[i] = m
				found = true
				return nil
			}
		}
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "model not found: "+id)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) deleteModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	found := false
	err := s.mgr.Update(func(c *config.Config) error {
		out := c.Models[:0]
		for _, m := range c.Models {
			if m.ID == id {
				found = true
				continue
			}
			out = append(out, m)
		}
		c.Models = out
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "model not found: "+id)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) fetchModel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if _, _, err := catalog.ParseModelID(req.ID); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg := s.mgr.Current()
	key, err := s.secrets.Resolve(cfg.OpenRouter.APIKey)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "upstream credential unavailable: "+err.Error())
		return
	}
	client := openrouter.New(cfg.OpenRouter.BaseURL, key)
	m, err := client.FetchModel(r.Context(), req.ID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if err := s.mgr.Update(func(c *config.Config) error {
		c.Models, _ = catalog.Upsert(c.Models, *m)
		return nil
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// --- halt ---

func (s *Server) getHalt(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.halt.Status())
}

func (s *Server) resume(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.halt.Resume())
}

// --- redactions ---

func (s *Server) listRedactions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.aliases.List())
}

func (s *Server) createRedactionRule(w http.ResponseWriter, r *http.Request) {
	var rule config.Redaction
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	err := s.mgr.Update(func(c *config.Config) error {
		for _, existing := range c.Redactions {
			if existing.Name == rule.Name {
				return fmt.Errorf("redaction %q already exists", rule.Name)
			}
		}
		c.Redactions = append(c.Redactions, rule)
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, rule)
}

func (s *Server) updateRedactionRule(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var rule config.Redaction
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if rule.Name == "" {
		rule.Name = name
	}
	found := false
	err := s.mgr.Update(func(c *config.Config) error {
		for i := range c.Redactions {
			if c.Redactions[i].Name != name {
				continue
			}
			if rule.Name != name {
				for j := range c.Redactions {
					if c.Redactions[j].Name == rule.Name {
						return fmt.Errorf("redaction %q already exists", rule.Name)
					}
				}
			}
			c.Redactions[i] = rule
			found = true
			return nil
		}
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "redaction not found: "+name)
		return
	}
	writeJSON(w, http.StatusOK, rule)
}

func (s *Server) deleteRedactionRule(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	found := false
	err := s.mgr.Update(func(c *config.Config) error {
		out := c.Redactions[:0]
		for _, rule := range c.Redactions {
			if rule.Name == name {
				found = true
				continue
			}
			out = append(out, rule)
		}
		c.Redactions = out
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "redaction not found: "+name)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- traffic ---

func (s *Server) listTraffic(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	entries := s.log.List(limit)
	out := make([]traffic.Entry, 0, len(entries))
	for _, e := range entries {
		out = append(out, summaryEntry(e))
	}
	writeJSON(w, http.StatusOK, out)
}

// summaryEntry returns a copy of e without its captured bodies. Both the list
// and the live stream carry summary fields only: a streamed frame is a few
// hundred bytes instead of up to the body caps (1 MiB + 256 KiB), and clients
// fetch bodies from the detail endpoint only for the entry they inspect.
func summaryEntry(e *traffic.Entry) traffic.Entry {
	cp := *e
	cp.RequestBody = ""
	cp.ResponseBody = ""
	cp.RequestTruncated = false
	cp.ResponseTruncated = false
	return cp
}

func (s *Server) getTraffic(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid traffic id")
		return
	}
	e, ok := s.log.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "traffic entry not found")
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (s *Server) streamTraffic(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch, unsubscribe := s.log.Subscribe()
	defer unsubscribe()
	for {
		select {
		case <-r.Context().Done():
			return
		case e := <-ch:
			data, err := json.Marshal(summaryEntry(e))
			if err != nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "event: entry\ndata: %s\n\n", data)
			flusher.Flush()
		}
	}
}
