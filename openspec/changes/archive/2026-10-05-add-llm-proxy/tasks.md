# Tasks

## 1. Module scaffolding and configuration

- [x] 1.1 Initialize the Go module (`go mod init llm-proxy`), create `cmd/llm-proxy` with a minimal main, and add the Bubble Tea/bubbles/lipgloss dependencies. Verify with `go build ./...` succeeding.
- [x] 1.2 Implement config types, load, default generation, and first-run creation in `internal/config` (design D6 shape: server, openrouter, models, filters; snake_case JSON). Verify with a unit test: missing file at a temp path creates safe defaults (loopback addresses, empty models and filters) and loads them; `--config` override resolves to the given path.
- [x] 1.3 Implement config validation (JSON parses, filter patterns compile, actions are `block`/`replace`, `replace` filters define a replacement) with errors naming the file and offending field. Verify with unit tests: a config with pattern `[unclosed` fails validation naming the config file and filter; a valid config passes.
- [x] 1.4 Implement atomic save (temp file in same directory + rename). Verify with a unit test: save-then-reload round-trips the config, no temp file remains, and a reader concurrently reloading only ever sees complete JSON.
- [x] 1.5 Implement change watching: poll mtime+size every 500ms, SHA-256 compare to skip self-saves, validate externally changed files, keep last-good config on invalid edits and expose the error. Verify with unit tests: an external edit is applied without restart; the server's own save does not trigger a reload; an invalid external edit keeps the last-good config and reports the error.

## 2. Filter engine

- [x] 2.1 Implement filter compilation into an immutable snapshot and sequential `replace` application over every string value of a parsed JSON body with capture-group support (`internal/filter`, design D4). Verify with unit tests: literal replacement (`James Brown` → `John Smith`), capture-group masking (`sk_xy1234abcd7890ab` → `sk_xy**********ab`), case-insensitive path rewrite (`(?i)/Users/james/` → `/users/john.smith/`), and chained filters applying in order.
- [x] 2.2 Implement the `block` action with short-circuit semantics: report the blocked filter name and per-filter match counts only, never matched text, and stop processing. Verify with unit tests: a request matching both a replace and a block filter is reported blocked with no replacement applied; the result struct contains none of the matched values.
- [x] 2.3 Preserve JSON structure and scan nested fields: apply filters to strings inside messages, system prompts, and tool definitions while leaving keys and non-string values untouched, parsing with `UseNumber()`. Verify with a unit test that round-trips a body with tools, temperature, unrecognized fields, and large-integer literals and asserts the output equals the input except for the matched substrings, with number literals byte-identical.
- [x] 2.4 Detect non-filterable bodies: content types or payloads that are not JSON produce an explicit not-content-filtered marker. Verify with a unit test passing a multipart body and asserting the passthrough marker.

## 3. Traffic log

- [x] 3.1 Implement the bounded ring buffer with configurable capacity and entry fields (id, timestamp, method, path, model, status, duration, request/response sizes, filter summary, bodies) in `internal/traffic`. Verify with a unit test: overflowing capacity drops the oldest entries and keeps the newest; ids are monotonic.
- [x] 3.2 Implement SSE broadcast to subscribers with drop-oldest semantics for slow subscribers and non-blocking publish. Verify with a unit test using a subscriber that reads slower than entries are published: it receives the newest entries and the publisher never blocks.
- [x] 3.3 Implement capped body capture with truncation flags (256 KiB response, 1 MiB request). Verify with a unit test feeding an oversized body: the stored body is capped and flagged truncated.

## 4. Proxy server

- [x] 4.1 Implement the reverse proxy core in `internal/proxy` (design D3): `/v1/X` → `{base_url}/X` mapping, header preservation minus hop-by-hop, `Authorization` replacement with the configured OpenRouter credential, upstream response passthrough. Verify with an integration test against an `httptest` fake OpenRouter: a non-streaming chat completion round-trips status, headers, and body; the upstream received the server's credential, never the client's; and with no filter matching, the body reaches the upstream byte-for-byte.
- [x] 4.2 Enable incremental streaming (`FlushInterval: -1`). Verify with an integration test: the fake upstream sends SSE chunks with artificial delays and the test client observes each chunk arriving before the stream completes.
- [x] 4.3 Implement the pre-proxy pipeline: parse JSON bodies, enforce the model allowlist (404 `model_not_found` before any upstream call, per spec `proxying`/Enforce the model allowlist), run filters, rewrite the request body, and mark non-JSON bodies as not content-filtered. Verify with integration tests: an unconfigured model gets a 404 without an upstream request; a blocked request gets a 403 naming the filter; a replaced request reaches the upstream with the rewritten text; a multipart body is forwarded unmodified and marked.
- [x] 4.4 Wire traffic recording into the proxy path: every exchange including blocked ones is recorded with outcome and filtered request body only. Verify with an integration test asserting log entries for forwarded, blocked, and not-content-filtered requests, and that a blocked secret appears nowhere in its entry.
- [x] 4.5 Return all proxy-generated errors in the OpenAI error JSON shape (`error.message`, `error.type`, `error.param`, `error.code`). Verify with a test asserting the shape for blocked request, unknown model, and unparseable JSON body on a model-checked endpoint.

## 5. Model catalog and OpenRouter client

- [x] 5.1 Implement catalog types and local serving of `GET /v1/models` and `GET /v1/models/{id}` from enabled entries in OpenAI response shapes (`internal/catalog`). Verify with handler tests: only configured models are listed, a configured id returns its entry, an unconfigured id returns 404.
- [x] 5.2 Implement the OpenRouter model-metadata client wrapping `GET {base_url}/model/{author}/{slug}` with tolerant decoding (`internal/openrouter`, design D7). Verify with a unit test against a fixture server: id, name, context_length, pricing strings, and supported_parameters are extracted; unknown fields are ignored.
- [x] 5.3 Implement catalog upsert with persistence and live snapshot swap: fetched models are added or updated in config and usable immediately. Verify with a test: fetch-then-upsert adds a new entry, a second fetch updates it, and the in-memory catalog serves the model without restart.
- [x] 5.4 Validate model id format `author/slug[:variant]` (split on first `/`, variant suffix preserved). Verify with unit tests: `gpt-4` is rejected, `deepseek/deepseek-v4.1-flash` parses, `openai/gpt-4:free` keeps the `:free` suffix.

## 6. Control API

- [x] 6.1 Implement `/api/health`, `GET /api/config`, and `PUT /api/settings` (`internal/control`, design D8). Verify with handler tests: health answers 200, config read returns the current JSON, a settings update is validated, persisted to the file, and live for new requests.
- [x] 6.2 Implement filters CRUD (`GET/POST /api/filters`, `PUT/DELETE /api/filters/{name}`) with validation before apply. Verify with handler tests: a created filter is active for subsequent proxied requests and present on disk; an invalid pattern returns 400 with the problem named and the config unchanged; update and delete round-trip.
- [x] 6.3 Implement models CRUD and `POST /api/models/fetch` using the shared fetch path. Verify with handler tests against a fake OpenRouter: fetch via API adds the model with metadata and returns it; delete removes it from catalog and file.
- [x] 6.4 Implement `POST /api/filters/test` applying a supplied pattern and optional replacement to sample text without saving. Verify with a handler test: matches and resulting text are returned for pattern `sk-\w+` against text containing `sk-live-abc123`, and no config change occurs.
- [x] 6.5 Implement traffic endpoints: `GET /api/traffic?limit=` newest-first, `GET /api/traffic/{id}` full detail, `GET /api/traffic/stream` SSE push. Verify with handler tests: list order after several recorded exchanges, detail includes bodies, and a new recorded entry is pushed to a connected stream subscriber.
- [x] 6.6 Bind the control API to loopback only by default. Verify with a test asserting the default config value and the listener's actual bound address.

## 7. CLI

- [x] 7.1 Implement subcommand dispatch in `cmd/llm-proxy`: `serve` runs proxy + control API headless; `--config` is a global flag. Verify by running the built binary with a temp config: `/api/health` answers on the control address and no TUI launches; `--config` against a custom path is honored.
- [x] 7.2 Implement `llm-proxy fetch <model-id>`: probe `/api/health` and route through `POST /api/models/fetch` when a server is running, otherwise load-mutate-save the config file directly (design D6). Verify with tests: fetch with no running server updates the config file and prints the added/updated model; fetch with a running server routes through the API and the file reflects it.
- [x] 7.3 Implement the default command: probe health, start the server in-process when unreachable, then launch the TUI; attach when a server already answers. Verify by smoke: default run opens the TUI with the server up; a second invocation while the first runs attaches without starting a second listener.

## 8. TUI

- [x] 8.1 Implement the TUI shell with tab navigation across Traffic, Filters, Models, Settings and the attach-or-start logic on launch (`internal/tui`, design D9). Verify by building and manually smoke-testing tab switching against a running server.
- [x] 8.2 Implement the Traffic view: live table (time, model, endpoint, status, duration, filter outcome) fed by `GET /api/traffic` backfill plus the SSE stream, and a detail viewport showing the filtered request body and response body. Verify by manual smoke: proxy a request while the view is open, watch the entry appear without a keypress, open it and see the bodies; plus a view-model unit test applying a streamed entry.
- [x] 8.3 Implement the Filters view: filter table, editor form (name, pattern, action, replacement, enabled), and a live test pane calling `POST /api/filters/test`. Verify by manual smoke: draft a pattern in the test pane and see matches while typing, save the filter, and confirm the next matching request through the proxy is affected.
- [x] 8.4 Implement the Models view: model table, add-by-id via `POST /api/models/fetch`, edit, and remove. Verify by manual smoke: add `deepseek/deepseek-v4.1-flash` (or a fake-upstream id in tests) and see it appear with fetched metadata; remove it and confirm `GET /v1/models` no longer lists it.
- [x] 8.5 Implement the Settings view over `GET /api/config` and `PUT /api/settings` (addresses, base URL, API key, log capacity). Verify by manual smoke: change the API key, confirm it is written to the config file and the next upstream request uses it.

## 9. End-to-end verification and docs

- [x] 9.1 Run the full suite and confirm `go build ./...` and `go test ./...` pass clean. Verify by the commands' output.
- [x] 9.2 Run an end-to-end smoke: start the server, fetch a model (fake OpenRouter upstream or a real key via env), send a non-streaming and a streaming completion through the proxy with a secret and a PII name in the prompt, and observe block and replace behavior plus traffic entries in the TUI. Verify by recorded observations of each outcome.
- [x] 9.3 Write the README: install, config reference with the example JSON, filter examples (masked key, PII swap, path rewrite), `fetch` usage, TUI keybindings, and the control-API note for a future GUI. Verify that every documented command runs as written.

## 10. Case-insensitive filtering

- [x] 10.1 Match filter patterns case-insensitively by default (effective pattern `(?i)` + configured pattern), with an inline `(?-i)` opt-out; apply the same effective compilation in config validation, the filter engine, and the control-API test endpoint. Verify with unit tests: pattern `james brown` rewrites `JAMES BROWN` and `James Brown`; `(?-i)james brown` does not match `James Brown`; a block filter matches different case; `[unclosed` still fails validation.
- [x] 10.2 Document the case-insensitive default and `(?-i)` opt-out in the README and the `content-filtering` spec delta. Verify the examples match observed behavior.

## 11. TUI detail rendering

- [x] 11.1 Auto-format bodies in the Traffic detail view: valid JSON is re-indented with `json.Indent` (field order and number literals preserved), SSE `data:` payloads are indented individually, and non-JSON bodies are shown verbatim. Verify with unit tests (`prettyBody`, `entryDetail`) and a live smoke of both a JSON and an SSE exchange.
