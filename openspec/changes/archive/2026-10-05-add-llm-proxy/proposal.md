# Proposal

## Why

LLM client tools send whatever context they assemble — file paths, API keys, personal names — straight to third-party providers. There is no local checkpoint where that traffic can be inspected, redacted, or blocked before it leaves the machine, and building such controls into every client tool doesn't scale. A local proxy provides one checkpoint for all LLM traffic.

## What Changes

- New Go binary `llm-proxy` (greenfield) that terminates OpenAI-compatible API traffic locally and forwards it to OpenRouter.
- Regex-based content filters applied to every outbound request body before forwarding. Two actions: **block** (halt the request, return an error response) or **replace** (substitute each match with a user-defined replacement, e.g. `sk_xy**********ab`, `[REDACTED]`, or `James Brown` → `John Smith`). Filters are entirely user-defined in a JSON config file.
- Model catalog restricted to user-configured models; `GET /v1/models` returns only those models. `llm-proxy fetch <model-id>` pulls model metadata (context length, pricing, supported parameters) from OpenRouter and writes it into the config so users don't have to look values up manually.
- Every proxied exchange is recorded in a traffic log with the filter actions taken. Logged request bodies are the filtered versions; original matched values are never stored.
- Local HTTP control API exposing config management, traffic log reading/streaming, and model fetch — frontend-agnostic so a future GUI (e.g. Wails) can reuse the same surface.
- Terminal UI (Bubble Tea) for live traffic inspection and filter/model/settings editing, built on the control API. Server and frontend are separate components communicating over HTTP.

Non-goals for this change: response-side filtering (filters apply to outbound requests only), traffic-log persistence across restarts, multi-user authentication.

## Capabilities

### New Capabilities
- `proxying`: Forward OpenAI-compatible API traffic to OpenRouter, including streaming responses, credential injection, model allowlist enforcement, and traffic recording.
- `content-filtering`: Inspect outbound request payloads against user-defined regex rules and block or transform matches before forwarding.
- `model-catalog`: Maintain a user-configured set of models, serve `GET /v1/models` locally from that set, and fetch model metadata from OpenRouter into the config.
- `control-api`: Local HTTP API for reading and writing config, reading and streaming traffic log entries, and fetching model metadata.
- `tui`: Terminal interface for monitoring live traffic and editing filters, models, and settings via the control API.
- `configuration`: Single JSON config file as the source of truth — loading, validation, atomic persistence, and hot-reload of changes.

### Modified Capabilities

- None (greenfield project; no existing specs).

## Impact
- Greenfield Go module at the repository root (`cmd/llm-proxy`, `internal/…`); no existing code is affected.
- New external dependencies: Bubble Tea, Bubble Tea bubbles, and Lip Gloss for the TUI. HTTP serving/proxying, regex matching, and JSON handling use the Go standard library.
- Configuration: single JSON file (default under the user's config directory, overridable with `--config`) holding server settings, the OpenRouter API key and base URL, the model catalog, and filter rules.
- External system: OpenRouter API (`https://openrouter.ai/api/v1`) — chat completions and model metadata endpoints.
