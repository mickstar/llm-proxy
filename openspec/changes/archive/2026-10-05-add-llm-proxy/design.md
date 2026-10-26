# Design

## Context

Greenfield Go module (module path `llm-proxy`), single binary, no existing code. See `proposal.md` for motivation; the six capability specs in `specs/` define the required behavior. The one architectural constraint that shapes everything: the terminal UI is a *client* of the proxy server, because a Wails GUI is expected to replace or join it later — so all inspection and management traffic flows through the control API, never through in-process calls from the TUI.

External surfaces involved:

- OpenRouter chat completions at `https://openrouter.ai/api/v1` (OpenAI-compatible paths).
- OpenRouter model metadata: `GET /api/v1/model/{author}/{slug}` returns a single model's `id`, `name`, `context_length`, `pricing.prompt`/`pricing.completion` (strings), and `supported_parameters` (verified against the current OpenRouter OpenAPI spec).

## Goals / Non-Goals

**Goals:**

- One binary that runs proxy + control API + TUI, or any subset (`serve`, attach-to-running).
- The control API is the only frontend surface; the TUI uses it exactly like a future GUI would.
- Filtering never stores original matched values anywhere (log, error responses, memory beyond the request lifetime).
- Streaming responses stay streaming — no full-buffer detour.

**Non-Goals:**

- Response-side filtering, persistent traffic history, TLS on local listeners, multi-provider routing, config encryption, multi-user auth (proposal non-goals plus design-level cuts).
- TLS termination, plugins, or provider formats other than the OpenAI spec.

## Decisions

### D1: Runtime shape — one process, three roles, HTTP between them

`cmd/llm-proxy` dispatches subcommands:

- `llm-proxy` (default): probe `GET /api/health` on the configured control address; if unreachable, start proxy + control API in-process, then launch the TUI. If reachable, attach the TUI to the existing server.
- `llm-proxy serve`: headless server.
- `llm-proxy fetch <model-id>`: add/update a model (D7).

The TUI never imports server internals; it talks HTTP to the control address even when the server runs in the same process. Alternative rejected — calling server methods directly when in-process — because it forks the TUI into two code paths and stops dogfooding the API the GUI will rely on.

Package layout:

```
cmd/llm-proxy/        CLI dispatch
internal/config/      config types, load/validate, atomic save, change watch
internal/filter/      filter compilation and application over JSON values
internal/proxy/       reverse proxy, allowlist check, traffic recording
internal/traffic/     ring-buffer traffic log + SSE broadcast
internal/catalog/     model catalog, local /v1/models serving
internal/openrouter/  OpenRouter HTTP client (model metadata)
internal/control/     control API handlers
internal/tui/         Bubble Tea models (traffic, filters, models, settings)
```

### D2: Two listeners — proxy port and control port

The proxy listener (default `127.0.0.1:8787`) serves only the OpenAI-compatible surface: locally served `GET /v1/models[/{id}]`, everything else forwarded. The control listener (default `127.0.0.1:8788`, loopback only) serves `/api/*`. Alternative rejected — one listener with an `/api` prefix — because the proxy port must forward unknown `/v1/*` paths generically, and keeping control off the client-facing port avoids ever exposing management to whatever network the proxy binds later.

Path mapping: client base URL is `http://127.0.0.1:8787/v1`, so local path `/v1/X` maps to `{base_url}/X` with `base_url` defaulting to `https://openrouter.ai/api/v1`. The base URL is configurable.

### D3: Proxying via `httputil.ReverseProxy`

Stdlib `httputil.ReverseProxy` with:

- A director/rewrite that swaps scheme/host, strips the local `/v1` prefix, drops hop-by-hop headers, and replaces `Authorization` with the configured OpenRouter credential.
- `FlushInterval: -1` for immediate SSE chunk flushing.
- A request-body interception step *before* the proxy: read the body (chat requests are small relative to responses), apply the allowlist check and filters, and hand the proxy a rewritten body only when a filter changed something — otherwise the original bytes go through untouched. Non-JSON or unparseable bodies pass through unfiltered and are logged as not content-filtered (spec: `content-filtering` / Pass through non-JSON bodies).

The proxy is a transparent pass-through on the OpenAI wire format: no OpenAI↔OpenRouter format translation, no field mapping, no header injection. The only changes to a forwarded request are the path prefix mapping (`/v1/X` → `{base_url}/X`), the `Host` header, `Authorization` replacement, and removal of hop-by-hop headers (an HTTP standard requirement, not OpenRouter-specific). Everything else — query strings, all other headers including OpenRouter extensions such as `HTTP-Referer` and `X-Title`, and all body fields including OpenRouter-specific extensions (`provider`, `transforms`, `route`) — is forwarded as-is, and untouched bodies are forwarded byte-for-byte.

Alternative rejected — manual handler with `http.Client` round-trips — because ReverseProxy already handles hop-by-hop headers, streaming flush, and upstream error mapping correctly.

Allowlist: for body-carrying endpoints (`/v1/chat/completions`, `/v1/completions`, `/v1/embeddings`, `/v1/responses`), a top-level `model` field absent from the enabled catalog short-circuits with 404 before any upstream call. Requests without a `model` field are forwarded (OpenRouter errors).

### D4: Filtering on parsed JSON values, not raw bytes

The pipeline: unmarshal body into `any` with `json.Decoder.UseNumber()` so numeric literals survive the round-trip exactly; walk every string value recursively (message content, system prompts, tool definitions — keys are not scanned); for each string, apply enabled filters in configured order with `regexp.ReplaceAllString`; short-circuit on any `block` match; forward the client's original body bytes verbatim when no filter changed anything, and re-marshal only when a replacement occurred.

Rationale over raw-body regex: re-marshaling guarantees correct JSON escaping (a replacement containing quotes/backslashes cannot corrupt the payload — spec: `content-filtering` / Preserve JSON structure) and avoids matching JSON syntax or keys. RE2 compile happens once at config load (validation), and compiled filters are held in an immutable snapshot swapped atomically on config change — requests never walk a mutating list. Patterns are compiled case-insensitively by default (the effective pattern is `(?i)` prepended to the configured pattern); a pattern may force case-sensitive matching by starting with the inline `(?-i)` flag.

Fidelity on filtered bodies: `UseNumber` keeps number literals byte-exact (a `float64` round-trip could silently corrupt large integers), and the untyped walk preserves every field, including unknown and OpenRouter-extension fields. Cosmetic caveat: a re-marshaled body normalizes key order (Go maps marshal alphabetically) and whitespace — semantically identical JSON, invisible to any JSON consumer, and unreachable for bodies no filter touched.

Error mapping for blocked requests: HTTP 403, OpenAI error JSON, `type: "permission_error"`, `code: "content_filter"`, message names the filter but never includes the matched value. Unknown model: 404, `code: "model_not_found"`.

### D5: Traffic log — bounded ring buffer, capped body capture, no originals

`internal/traffic` keeps a mutex-protected ring buffer (default capacity 500, configurable) of entries: id, timestamp, method, path, model, status, duration, request/response sizes, filter summary (filter name, action, match count — or blocked), the **filtered** request body, and the response body. New entries broadcast to SSE subscribers over buffered channels; slow subscribers drop oldest, never block the proxy path.

Response capture tees the stream through a capped buffer (256 KiB; request bodies capped at 1 MiB) with a truncated flag, so logging a large SSE stream can't balloon memory. The blocked-request entry stores no matched value — the filter engine reports counts and filter names, not the text (spec: `proxying` / Record proxied traffic).

### D6: Config — disk is source of truth, one writer per change, hash-based reload skip

Single JSON file (snake_case fields), default `os.UserConfigDir()/llm-proxy/config.json`, overridable with `--config`. Shape:

```json
{
  "server": {"proxy_addr": "127.0.0.1:8787", "control_addr": "127.0.0.1:8788", "traffic_log_capacity": 500},
  "openrouter": {"base_url": "https://openrouter.ai/api/v1", "api_key": "sk-or-..."},
  "models": [{"id": "...", "name": "...", "context_length": 163840, "pricing": {"prompt": "0.000001", "completion": "0.000002"}, "supported_parameters": ["temperature"], "enabled": true}],
  "filters": [{"name": "redact-keys", "pattern": "sk-[A-Za-z0-9]{20,}", "action": "replace", "replacement": "[SECRET]", "enabled": true}]
}
```

- Validation at load and before every save: JSON parses, patterns compile, actions are `block`/`replace`, `replace` filters define a replacement. Startup fails with file + field named (spec: `configuration`).
- Saves are atomic: temp file in the same directory, `os.Rename`.
- External changes detected by polling mtime+size every 500ms (stdlib-only, avoids fsnotify platform edge cases), then validated; content is compared by SHA-256 against the last applied save so the server's own writes never trigger a redundant reload. An invalid external edit keeps the last-good config and surfaces the error via the control API (TUI banner).
- First run with no file: write safe defaults (loopback addresses, empty catalog and filters) and continue.

Two-writer conflict (CLI `fetch` while server runs): `fetch` probes `/api/health`; if a server answers, it routes the mutation through `POST /api/models/fetch` so there is a single writer at a time; otherwise it loads, mutates, and saves the file itself.

### D7: Model fetch — by-slug endpoint, shared by CLI and API

`internal/openrouter` wraps `GET {base_url}/model/{author}/{slug}` (note: singular `/model/`), tolerant of unknown fields. Model ids must match `author/slug[:variant]`; the id is split on the first `/`, and the remainder (including any `:free` suffix) is the slug. The result is upserted into the catalog (same code path for CLI `fetch` and `POST /api/models/fetch`), persisted, and live immediately via the config snapshot swap. Fetched pricing stays a string exactly as OpenRouter returns it.

### D8: Control API — stdlib routing, REST + SSE

Go 1.22+ `net/http` method+path patterns; no router dependency. Endpoints:

```
GET    /api/health
GET    /api/config
PUT    /api/settings
GET|POST       /api/filters
PUT|DELETE     /api/filters/{name}
POST   /api/filters/test          {pattern, replacement?, sample} -> matches + result
GET|POST       /api/models
PUT|DELETE     /api/models/{id}
POST   /api/models/fetch          {id}
GET    /api/traffic?limit=N       newest-first
GET    /api/traffic/{id}
GET    /api/traffic/stream        SSE: event "entry"
```

Mutations go through one path: validate → save to disk → swap in-memory snapshots (filters, catalog, settings). `GET /api/config` returns the API key unredacted — the TUI settings form needs it and the listener is loopback-only; noted as a risk.

### D9: TUI — Bubble Tea, four views, SSE + REST only

Bubble Tea + bubbles (table, textinput, textarea, viewport) + lipgloss. Views:

- **Traffic**: live table (time, model, endpoint, status, duration, filter outcome) fed by SSE on focus + `GET /api/traffic` backfill; `Enter` opens a viewport with filtered request and response bodies.
- **Filters**: table of filters; editor form (name, pattern, action, replacement, enabled) with a test pane calling `POST /api/filters/test` live while editing.
- **Models**: table; add-by-id triggers `POST /api/models/fetch`; edit/remove via REST.
- **Settings**: form over `GET/PUT /api/settings` (addresses, base URL, API key, log capacity).

Alternative rejected — Bubble Tea hooks calling server code directly — same reason as D1.

### D10: Dependencies

Go 1.25 toolchain. Runtime deps: `github.com/charmbracelet/bubbletea`, `github.com/charmbracelet/bubbles`, `github.com/charmbracelet/lipgloss`. Everything else — HTTP, reverse proxy, SSE, regex, JSON, config watch, ring buffer — stdlib.

## Risks / Trade-offs

- [Regex false positives silently rewrite or block legitimate traffic] → test pane fires on every keystroke in the filter editor; block errors name the filter; per-filter enable toggle for quick isolation.
- [Control API exposes the OpenRouter key to any local process] → loopback-only bind; acceptable for a personal dev tool; token auth is a contained future addition.
- [Teeing response bodies for the log costs memory per in-flight stream] → hard caps (256 KiB response / 1 MiB request) with truncation flags; ring buffer bounds total retention.
- [Two config writers race (CLI fetch vs. running server)] → fetch routes through the control API when `/api/health` answers; otherwise the server is not running and there is a single writer; hash-skip prevents reload loops.
- [OpenRouter metadata endpoint changes shape] → isolated in `internal/openrouter`; unknown fields ignored, missing fields land as zero values surfaced in fetch output rather than failing.
- [Streaming regressions (buffering proxy)] → integration test streams SSE through the proxy against a fake upstream and asserts chunk-by-chunk delivery with artificial delays.
- [Non-JSON bodies bypass filters] → spec'd as visible pass-through: entries are marked "not content-filtered" in the log and TUI so the gap is observable, not silent.
- [Re-marshaling filtered bodies normalizes key order and whitespace] → only matched substrings change semantically; number literals stay exact via `UseNumber`, all fields survive the untyped walk, and untouched bodies bypass re-marshaling entirely.

## Migration Plan

Greenfield — no existing users or data. Deploy = `go build`/`go install`; rollback = remove binary and config file. Nothing to migrate.
