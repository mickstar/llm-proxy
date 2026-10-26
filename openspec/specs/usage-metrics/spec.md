# usage-metrics Specification

## Purpose

Accounts for token usage reported by the upstream provider, aggregates it across exchanges, persists all-time totals, and exposes a snapshot that the macOS menu bar app renders as live throughput and activity.

## Requirements

### Requirement: Extract token usage from responses
The proxy SHALL read the provider's `usage` object from every proxied response that carries one, for both non-streaming JSON bodies and server-sent event streams, without altering, delaying, or buffering the response body sent to the client. It SHALL accept both chat-completions names (`prompt_tokens`, `completion_tokens`) and Responses API names (`input_tokens`, `output_tokens`), SHALL derive a missing total from prompt plus completion, and SHALL record the provider's `cost` field in USD when present. A response with no usage object, or an all-zero usage, SHALL not be recorded as token usage.

#### Scenario: Non-streaming usage
- **WHEN** a non-streaming response body ends with `"usage":{"prompt_tokens":12,"completion_tokens":34,"total_tokens":46,"cost":0.000123}`
- **THEN** the traffic entry and the aggregate metrics record 12 prompt tokens, 34 completion tokens, and $0.000123

#### Scenario: Streaming usage
- **WHEN** a streamed response's final `data:` chunk carries a `usage` object
- **THEN** the aggregate metrics record those tokens after the stream ends, and the bytes forwarded to the client are unchanged

#### Scenario: Cached prompt tokens
- **WHEN** a response reports `"prompt_tokens":100` with `"prompt_tokens_details":{"cached_tokens":64}`
- **THEN** the entry and totals record 100 prompt tokens of which 64 were cached

#### Scenario: Cost estimated from catalog rates
- **WHEN** a provider reports usage without a `cost` field and the model has catalog prices for prompt, cache, and completion
- **THEN** the recorded cost is the non-cached prompt tokens at the prompt rate, the cached tokens at the cache rate, and the completion tokens at the completion rate

#### Scenario: Provider cost wins
- **WHEN** a provider reports an explicit `cost`
- **THEN** that value is recorded even when catalog prices exist

#### Scenario: Tokens quoted in model output
- **WHEN** the model's own content contains the text `"usage": {...}`
- **THEN** the quoted object is not mistaken for the provider's accounting

### Requirement: Aggregate usage and activity
The proxy SHALL maintain all-time totals (requests, prompt/completion/total tokens, cached prompt tokens, cost in USD — provider-reported or estimated from catalog rates — errors, blocked requests, bytes), per-model totals including cost and cached prompt tokens, in-flight request count, a trailing 60-second throughput window (completion and total tokens per second, requests per minute), and per-minute activity for the last 60 minutes. Token throughput SHALL count completion tokens; prompt tokens SHALL be reported separately.

#### Scenario: Per-model totals
- **WHEN** requests to two model ids complete with usage
- **THEN** each model id reports its own request and token totals and the all-time totals equal their sum

#### Scenario: In-flight gauge
- **WHEN** a request is being proxied
- **THEN** the snapshot reports one active request until that exchange is recorded

#### Scenario: Blocked requests counted
- **WHEN** a request is refused by a content filter
- **THEN** it increments the request and blocked totals without token usage

### Requirement: Persist all-time totals
The proxy SHALL persist all-time totals and per-model totals as JSON beside the config file, writing atomically with owner-only permissions and flushing periodically and on shutdown. It SHALL load them on startup. Monetary totals recorded before cost tracking existed SHALL be seeded once from the catalog's per-token rates when a model id matches exactly or by a dated suffix; history has no cached-token split, so seeding bills every prompt token at the prompt rate. A corrupt or unreadable metrics file MUST NOT prevent the proxy from starting; the proxy SHALL start with empty totals and report the persistence error in the snapshot when a later write fails.

#### Scenario: Totals survive restart
- **WHEN** the proxy records usage, shuts down, and starts again
- **THEN** the all-time totals and per-model totals are unchanged before new traffic arrives

#### Scenario: Corrupt metrics file
- **WHEN** the metrics file contains invalid JSON at startup
- **THEN** the proxy starts and serves traffic with empty totals

### Requirement: Expose a metrics snapshot
The control API SHALL expose a single JSON snapshot containing the totals, per-model totals (including cost), throughput window, per-minute activity, in-flight count, last token-bearing request (with its generation rate and cost), persistence error (when any), and the halt state.

#### Scenario: Poll for a menu bar
- **WHEN** a client requests the metrics endpoint
- **THEN** it receives one JSON document with all fields needed to render throughput, totals, models, and a halt without further requests

### Requirement: Menu bar client
The macOS menu bar app SHALL poll the metrics endpoint about once per second and display completion tokens per second in the status bar. Clicking the status item SHALL open a compact control-center panel, rendered with a Liquid Glass background on macOS 26, that shows a prominent throughput readout, a recent-activity chart, total and per-request token figures, total and average cost, request and error counts, per-model usage and cost, the last request's generation rate, a resume action while the proxy is halted, and an Expand action that opens the full console. The panel SHALL close on Escape, on loss of app focus, or when the status item is clicked again. The app SHALL discover the control address from an environment override, a saved default, or `server.control_addr` in the standard config file, in that order. When the control API is unreachable it SHALL show an offline state with a retry action instead of stale throughput, and a missing metrics endpoint SHALL be reported as a proxy-build problem.

#### Scenario: Live throughput
- **WHEN** proxied usage is recorded and the menu bar polls the metrics endpoint
- **THEN** the status bar title reflects the trailing-window completion tokens per second

#### Scenario: Open the control center
- **WHEN** the user clicks the status item
- **THEN** the glass panel appears below it with throughput, activity, totals, and per-model usage

#### Scenario: Resume from the panel
- **WHEN** the proxy is halted and the user chooses Resume
- **THEN** the app posts the resume operation and refreshes its state

#### Scenario: Proxy offline
- **WHEN** the control API stops answering
- **THEN** the panel shows an offline state, the control URL, and a retry action, and the status bar stops reporting throughput
