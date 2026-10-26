// Package proxy implements the OpenAI-compatible reverse proxy: it forwards
// requests to OpenRouter, enforces the configured model allowlist, applies
// content filters to outbound JSON bodies, records every exchange in the
// traffic log, and streams SSE responses incrementally.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mickstar/llm-proxy/internal/catalog"
	"github.com/mickstar/llm-proxy/internal/config"
	"github.com/mickstar/llm-proxy/internal/filter"
	"github.com/mickstar/llm-proxy/internal/halt"
	"github.com/mickstar/llm-proxy/internal/oai"
	"github.com/mickstar/llm-proxy/internal/pricing"
	"github.com/mickstar/llm-proxy/internal/redact"
	"github.com/mickstar/llm-proxy/internal/secret"
	"github.com/mickstar/llm-proxy/internal/stats"
	"github.com/mickstar/llm-proxy/internal/traffic"
	"github.com/mickstar/llm-proxy/internal/usage"
)

// Server is the proxy HTTP handler.
type Server struct {
	mgr     *config.Manager
	log     *traffic.Log
	rp      *httputil.ReverseProxy
	cat     atomic.Pointer[catalog.Catalog]
	flts    atomic.Pointer[filter.Set]
	rdct    atomic.Pointer[redact.Set]
	prices  atomic.Pointer[pricing.Table]
	aliases *redact.Table
	halt    *halt.State
	stats   *stats.Store
	secrets *secret.Resolver
	notify  func(title, body string)
	unsub   func()
}

// Options configures a Server.
type Options struct {
	Manager   *config.Manager
	Log       *traffic.Log
	Aliases   *redact.Table            // optional; a private table is created when nil
	Halt      *halt.State              // optional; a private breaker is created when nil
	Stats     *stats.Store             // optional; usage totals are skipped when nil
	Secrets   *secret.Resolver         // optional; resolves keychain-backed credentials when nil
	Notify    func(title, body string) // optional; called once when the proxy halts
	Transport http.RoundTripper        // optional; defaults to http.DefaultTransport
}

// New builds the proxy and subscribes to config changes so filter and catalog
// snapshots stay live.
func New(opts Options) *Server {
	aliases := opts.Aliases
	if aliases == nil {
		aliases = redact.NewTable(0)
	}
	breaker := opts.Halt
	if breaker == nil {
		breaker = halt.New()
	}
	secrets := opts.Secrets
	if secrets == nil {
		secrets = secret.NewResolver()
	}
	s := &Server{mgr: opts.Manager, log: opts.Log, aliases: aliases, halt: breaker, stats: opts.Stats, secrets: secrets, notify: opts.Notify}
	s.rp = &httputil.ReverseProxy{
		Rewrite:        s.rewrite,
		FlushInterval:  -1,
		ModifyResponse: s.modifyResponse,
		ErrorHandler:   s.errorHandler,
		Transport:      opts.Transport,
	}
	s.Refresh(opts.Manager.Current())
	s.unsub = opts.Manager.OnChange(s.Refresh)
	return s
}

// Close unsubscribes from config changes.
func (s *Server) Close() {
	if s.unsub != nil {
		s.unsub()
	}
}

// Refresh rebuilds the catalog, filter, redaction, and price snapshots from cfg.
func (s *Server) Refresh(cfg *config.Config) {
	if set, err := filter.Compile(cfg.Filters, s.aliases); err == nil {
		s.flts.Store(set)
	}
	s.cat.Store(catalog.Build(cfg.Models))
	if rs, err := redact.Compile(cfg.Redactions); err == nil {
		s.rdct.Store(rs)
	}
	prices := pricing.Build(cfg.Models)
	s.prices.Store(&prices)
}

// ServeHTTP serves the model endpoints from the local catalog and forwards
// everything else to OpenRouter. In allowlist mode the collection endpoint is
// answered locally so clients only see configured models; in passthrough mode
// it is forwarded so clients see the real upstream catalog. The detail endpoint
// has no upstream equivalent and always answers from the local catalog.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// A block match is a breach: nothing is forwarded until a human resumes.
	if s.halt.Status().Halted {
		s.halt.Refuse()
		oai.WriteError(w, http.StatusForbidden, "permission_error", "proxy_halted",
			"the proxy is halted after a content-filter breach; resume it in the TUI or with POST /api/resume")
		return
	}
	if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/models/") {
		s.cat.Load().ServeHTTP(w, r)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/v1/models" && s.allowlist() {
		s.cat.Load().ServeHTTP(w, r)
		return
	}
	s.forward(w, r)
}

// allowlist reports whether the catalog gates model ids.
func (s *Server) allowlist() bool {
	return s.mgr.Current().Server.ModelPolicy == config.PolicyAllowlist
}

// notifyHalt announces the breach once. It names the filter only, never the
// matched value, so nothing sensitive reaches the notification system.
func (s *Server) notifyHalt(filter string) {
	if s.notify == nil {
		return
	}
	s.notify("llm-proxy halted", fmt.Sprintf("Filter %q matched — all requests are refused until you resume.", filter))
}

// modelEndpoint reports whether the path carries a chat/completion body whose
// model is subject to the allowlist.
func modelEndpoint(path string) bool {
	switch path {
	case "/v1/chat/completions", "/v1/completions", "/v1/embeddings", "/v1/responses":
		return true
	}
	return false
}

func isJSONContentType(ct string) bool {
	if ct == "" {
		return false
	}
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	return mt == "application/json" || strings.HasSuffix(mt, "+json")
}

type captureKey struct{}
type usageKey struct{}

// forward runs the pre-proxy pipeline (read body, allowlist, filters) and then
// the reverse proxy, recording the exchange.
func (s *Server) forward(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if s.stats != nil {
		s.stats.Begin()
		defer s.stats.End()
	}

	var body []byte
	if r.Body != nil {
		var err error
		body, err = io.ReadAll(r.Body)
		if err != nil {
			oai.WriteError(w, http.StatusBadRequest, "invalid_request_error", "invalid_body", "could not read request body")
			s.record(&traffic.Entry{Time: start, Method: r.Method, Path: r.URL.Path, Status: http.StatusBadRequest, DurationMS: time.Since(start).Milliseconds()})
			return
		}
		_ = r.Body.Close()
	}

	filtered := body
	model := ""
	notFiltered := false
	var outcome *filter.Outcome

	if len(body) > 0 {
		if json.Valid(body) {
			var top struct {
				Model string `json:"model"`
			}
			_ = json.Unmarshal(body, &top)
			model = top.Model

			if modelEndpoint(r.URL.Path) && model != "" && s.allowlist() {
				if !s.cat.Load().Has(model) {
					msg := fmt.Sprintf("model %q is not in the configured catalog; add it with `llm-proxy fetch %s` or set server.model_policy to %q", model, model, config.PolicyPassthrough)
					oai.WriteError(w, http.StatusNotFound, "invalid_request_error", "model_not_found", msg)
					s.record(&traffic.Entry{Time: start, Method: r.Method, Path: r.URL.Path, Model: model, Status: http.StatusNotFound, DurationMS: time.Since(start).Milliseconds(), RequestSize: len(body)})
					return
				}
			}

			out, err := s.flts.Load().ApplyBody(body)
			if err == nil {
				outcome = out
				if out.Changed {
					filtered = out.Body
				}
			} else {
				// Declared-valid JSON that the filter decoder rejects is malformed.
				s.record(&traffic.Entry{Time: start, Method: r.Method, Path: r.URL.Path, Model: model, Status: http.StatusBadRequest, DurationMS: time.Since(start).Milliseconds(), RequestSize: len(body)})
				oai.WriteError(w, http.StatusBadRequest, "invalid_request_error", "invalid_json", "request body is not valid JSON")
				return
			}
			if outcome != nil && outcome.Blocked {
				if _, first := s.halt.Trigger(outcome.BlockedFilter); first {
					s.notifyHalt(outcome.BlockedFilter)
				}
				oai.WriteError(w, http.StatusForbidden, "permission_error", "content_filter", fmt.Sprintf("request blocked by filter %q", outcome.BlockedFilter))
				s.record(&traffic.Entry{
					Time: start, Method: r.Method, Path: r.URL.Path, Model: model,
					Status: http.StatusForbidden, DurationMS: time.Since(start).Milliseconds(),
					RequestSize: len(body), Blocked: true, FilterSummary: "blocked: " + outcome.BlockedFilter,
				})
				return
			}
		} else if isJSONContentType(r.Header.Get("Content-Type")) {
			s.record(&traffic.Entry{Time: start, Method: r.Method, Path: r.URL.Path, Status: http.StatusBadRequest, DurationMS: time.Since(start).Milliseconds(), RequestSize: len(body)})
			oai.WriteError(w, http.StatusBadRequest, "invalid_request_error", "invalid_json", "request body is not valid JSON")
			return
		} else {
			notFiltered = true
		}
	}

	// Redactions run after the filters so a block rule still sees the original
	// text. Aliases are recorded process-wide and restored on the way back.
	redactionSummary := ""
	if !notFiltered && len(filtered) > 0 {
		if rs := s.rdct.Load(); rs != nil && rs.Len() > 0 {
			res, err := rs.ApplyBody(filtered, s.aliases)
			if err == nil {
				if res.Changed {
					filtered = res.Body
				}
				redactionSummary = res.Summary()
			}
		}
	}

	// Resolve the upstream credential before forwarding so an unreadable
	// keychain item fails locally instead of reaching upstream unauthenticated.
	key, err := s.secrets.Resolve(s.mgr.Current().OpenRouter.APIKey)
	if err != nil {
		oai.WriteError(w, http.StatusBadGateway, "api_error", "credential_unavailable", "upstream credential unavailable: "+err.Error())
		s.record(&traffic.Entry{Time: start, Method: r.Method, Path: r.URL.Path, Status: http.StatusBadGateway, DurationMS: time.Since(start).Milliseconds()})
		return
	}

	capture := traffic.NewBodyCapture(traffic.MaxResponseBody)
	uCap := &usage.Capture{}
	ctx := context.WithValue(r.Context(), captureKey{}, capture)
	ctx = context.WithValue(ctx, usageKey{}, uCap)
	ctx = context.WithValue(ctx, credentialKey{}, key)
	r = r.WithContext(ctx)
	if len(body) > 0 || r.Body != nil {
		r.Body = io.NopCloser(bytes.NewReader(filtered))
		r.ContentLength = int64(len(filtered))
	}

	rw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
	s.rp.ServeHTTP(rw, r)

	u, _ := uCap.Get()
	if t := s.prices.Load(); t != nil && !u.CostReported && u.TotalTokens > 0 {
		if rate, ok := t.Lookup(model); ok {
			u.CostUSD = rate.Cost(u.PromptTokens, u.CachedPromptTokens, u.CompletionTokens)
		}
	}
	reqBody, reqTrunc := traffic.CapString(string(filtered), traffic.MaxRequestBody)
	entry := &traffic.Entry{
		Time:               start,
		Method:             r.Method,
		Path:               r.URL.Path,
		Model:              model,
		Status:             rw.status,
		DurationMS:         time.Since(start).Milliseconds(),
		RequestSize:        len(filtered),
		ResponseSize:       capture.Size(),
		PromptTokens:       u.PromptTokens,
		CachedPromptTokens: u.CachedPromptTokens,
		CompletionTokens:   u.CompletionTokens,
		TotalTokens:        u.TotalTokens,
		CostUSD:            u.CostUSD,
		FilterSummary:      summarize(outcome, notFiltered),
		RedactionSummary:   redactionSummary,
		NotFiltered:        notFiltered,
		RequestBody:        reqBody,
		RequestTruncated:   reqTrunc,
		ResponseBody:       capture.String(),
		ResponseTruncated:  capture.Truncated(),
	}
	s.record(entry)
}

// record stores an entry in the traffic log and feeds the usage aggregator.
func (s *Server) record(e *traffic.Entry) {
	if s.log != nil {
		s.log.Record(e)
	}
	if s.stats != nil {
		s.stats.Record(stats.Event{
			Time:               e.Time,
			Model:              e.Model,
			PromptTokens:       e.PromptTokens,
			CachedPromptTokens: e.CachedPromptTokens,
			CompletionTokens:   e.CompletionTokens,
			TotalTokens:        e.TotalTokens,
			CostUSD:            e.CostUSD,
			Status:             e.Status,
			Blocked:            e.Blocked,
			RequestBytes:       e.RequestSize,
			ResponseBytes:      e.ResponseSize,
			DurationMS:         e.DurationMS,
		})
	}
}

func summarize(o *filter.Outcome, notFiltered bool) string {
	if o != nil && o.Blocked {
		return "blocked: " + o.BlockedFilter
	}
	if notFiltered {
		return "not content-filtered"
	}
	if o == nil {
		return ""
	}
	var parts []string
	for _, c := range o.Counts {
		if c.Count > 0 {
			parts = append(parts, fmt.Sprintf("%s x%d", c.Name, c.Count))
		}
	}
	return strings.Join(parts, ", ")
}

// credentialKey carries the resolved upstream credential from forward to
// rewrite.
type credentialKey struct{}

// rewrite maps the local /v1/X path onto {base_url}/X, replaces the
// Authorization header with the configured credential, and drops the local
// Host. ReverseProxy removes hop-by-hop headers itself.
func (s *Server) rewrite(pr *httputil.ProxyRequest) {
	cfg := s.mgr.Current()
	base, err := url.Parse(cfg.OpenRouter.BaseURL)
	if err != nil || base.Host == "" {
		base = &url.URL{Scheme: "https", Host: "openrouter.ai", Path: "/api/v1"}
	}
	rest := strings.TrimPrefix(pr.In.URL.Path, "/v1")
	path := strings.TrimSuffix(base.Path, "/") + rest
	pr.Out.URL.Scheme = base.Scheme
	pr.Out.URL.Host = base.Host
	pr.Out.URL.Path = path
	pr.Out.URL.RawPath = ""
	pr.Out.URL.RawQuery = pr.In.URL.RawQuery
	pr.Out.Host = base.Host
	// Drop the client's Accept-Encoding so Transport negotiates gzip itself and
	// transparently decompresses. Otherwise a compressed upstream body reaches
	// modifyResponse still encoded, alias restoration is skipped, and the traffic
	// log stores unreadable bytes.
	pr.Out.Header.Del("Accept-Encoding")
	pr.SetXForwarded()
	key, _ := pr.In.Context().Value(credentialKey{}).(string)
	if key != "" {
		pr.Out.Header.Set("Authorization", "Bearer "+key)
	} else {
		pr.Out.Header.Del("Authorization")
	}
}

// modifyResponse tees the upstream response body into the per-request capture.
func (s *Server) modifyResponse(resp *http.Response) error {
	c, ok := resp.Request.Context().Value(captureKey{}).(*traffic.BodyCapture)
	if !ok {
		return nil
	}
	// Capture first so the traffic log keeps the aliased bytes, then restore the
	// originals on the way to the client. Usage is read from the upstream bytes
	// before restoration; token counts are unaffected by the substitution.
	body := resp.Body
	if restorable(resp) {
		if u := usageCapture(resp.Request.Context()); u != nil {
			body = usage.NewReader(body, u)
		}
	}
	resp.Body = &captureReader{rc: body, w: c}
	if restorable(resp) && s.aliases.Len() > 0 {
		if isEventStream(resp) {
			resp.Body = redact.NewEventRestoreReader(resp.Body, s.aliases)
		} else {
			resp.Body = redact.NewRestoreReader(resp.Body, s.aliases)
		}
		resp.ContentLength = -1
		resp.Header.Del("Content-Length")
	}
	return nil
}

// restorable reports whether a response body is text this proxy can rewrite in
// place. Compressed and binary bodies pass through untouched.
func restorable(resp *http.Response) bool {
	if resp.Header.Get("Content-Encoding") != "" {
		return false
	}
	ct := resp.Header.Get("Content-Type")
	return strings.Contains(ct, "application/json") || strings.Contains(ct, "text/event-stream")
}

// isEventStream reports whether the response is SSE, whose aliases arrive split
// across the JSON deltas of successive events.
func isEventStream(resp *http.Response) bool {
	return strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")
}

// usageCapture returns the request's token-accounting slot, when one exists.
func usageCapture(ctx context.Context) *usage.Capture {
	c, _ := ctx.Value(usageKey{}).(*usage.Capture)
	return c
}

// errorHandler maps upstream transport failures to the OpenAI error shape.
func (s *Server) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() == context.Canceled {
		// Client went away; nothing useful to send.
		return
	}
	oai.WriteError(w, http.StatusBadGateway, "api_error", "upstream_error", "upstream request failed: "+err.Error())
}

// captureReader tees reads into a BodyCapture.
type captureReader struct {
	rc io.ReadCloser
	w  io.Writer
}

func (c *captureReader) Read(p []byte) (int, error) {
	n, err := c.rc.Read(p)
	if n > 0 {
		_, _ = c.w.Write(p[:n])
	}
	return n, err
}

func (c *captureReader) Close() error { return c.rc.Close() }

// statusWriter records the response status and exposes Unwrap/Flush so the
// reverse proxy can stream.
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status = code
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if !w.wrote {
		w.status = http.StatusOK
		w.wrote = true
	}
	return w.ResponseWriter.Write(p)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
