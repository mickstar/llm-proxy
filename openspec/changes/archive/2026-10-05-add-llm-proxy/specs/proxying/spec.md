# Spec Delta

## Purpose

Forwards OpenAI-compatible API traffic from local clients to OpenRouter transparently, for both streaming and non-streaming exchanges, while recording every exchange in the traffic log.

## ADDED Requirements

### Requirement: Forward OpenAI-compatible requests
The proxy SHALL accept HTTP requests on its configured proxy listen address and forward them to the configured OpenRouter base URL, preserving the method, path relative to the base URL, query parameters, request body, and request headers, except for the upstream credential injection and removal of hop-by-hop headers. The proxy SHALL NOT otherwise translate or reshape requests. Upstream responses SHALL be returned to the client with their status, headers, and body unmodified.

#### Scenario: Non-streaming chat completion
- **WHEN** a client sends `POST /v1/chat/completions` with a JSON body naming a configured model
- **THEN** the request is forwarded to OpenRouter and the client receives the upstream status, headers, and body

#### Scenario: Other OpenAI endpoint paths
- **WHEN** a client requests any other path under `/v1/` other than the locally served model listing (e.g. `POST /v1/embeddings`)
- **THEN** the request is forwarded to the same path at OpenRouter

#### Scenario: Untouched body forwarded byte-for-byte
- **WHEN** a request body triggers no filter changes
- **THEN** the body forwarded to OpenRouter is byte-for-byte identical to the body the client sent

### Requirement: Stream server-sent event responses incrementally
When the upstream response is a server-sent event stream, the proxy SHALL forward each chunk to the client as it arrives, without waiting for the complete response.

#### Scenario: Streaming chat completion
- **WHEN** a client sends `POST /v1/chat/completions` with `"stream": true`
- **THEN** the client receives each SSE chunk as the upstream produces it, in order, until the stream ends

### Requirement: Inject upstream credentials
The proxy SHALL replace the client-supplied `Authorization` header with the OpenRouter credential from configuration on every forwarded request. Client-supplied credentials MUST NOT be sent to OpenRouter.

#### Scenario: Client credential replaced
- **WHEN** a client sends `Authorization: Bearer sk-client-123`
- **THEN** the forwarded request carries the configured OpenRouter credential instead of `sk-client-123`

#### Scenario: No client credential required
- **WHEN** a client sends a request without an `Authorization` header
- **THEN** the request is still forwarded with the configured OpenRouter credential

### Requirement: Enforce the model allowlist
Requests whose body names a model that is not present and enabled in the configured model catalog SHALL be rejected before any upstream request is made, with an HTTP 404 response in the OpenAI error format that names the unknown model.

#### Scenario: Unknown model rejected
- **WHEN** a chat completion request names model `some/unknown-model` and that model is not in the catalog
- **THEN** the proxy responds 404 without contacting OpenRouter, and the error message identifies the model

#### Scenario: Configured model forwarded
- **WHEN** a request names a model present and enabled in the catalog
- **THEN** the request is forwarded to OpenRouter

### Requirement: Record proxied traffic
The proxy SHALL record every proxied exchange in an in-memory traffic log bounded by a configurable capacity, retaining the newest entries. Each entry SHALL include timestamp, endpoint, model, request and response sizes, upstream status, duration, and the filter outcome. Entries SHALL store the filtered request body; values matched by block or replace filters MUST NOT be stored.

#### Scenario: Successful exchange recorded
- **WHEN** a request is forwarded and OpenRouter responds
- **THEN** a traffic entry is recorded with the endpoint, model, status, duration, and filter outcome

#### Scenario: Blocked request recorded without the secret
- **WHEN** a request is blocked by a filter that matched the value `sk-live-abc123`
- **THEN** the traffic entry records the blocked outcome and the filter name, and `sk-live-abc123` appears nowhere in the entry

### Requirement: Return proxy errors in OpenAI error format
All proxy-generated error responses (blocked request, unknown model, unparseable request body) SHALL use an HTTP error status and a JSON body in the OpenAI error shape with `error.message`, `error.type`, `error.param`, and `error.code`, so standard OpenAI SDKs can decode them.

#### Scenario: Error shape is OpenAI-compatible
- **WHEN** a request is blocked by a filter
- **THEN** the response body is JSON with an `error` object containing `message`, `type`, `param`, and `code` fields

### Requirement: Run headless
The server SHALL be runnable without the terminal UI via `llm-proxy serve`.

#### Scenario: Headless start
- **WHEN** `llm-proxy serve` is run with a valid config
- **THEN** the proxy and control API start and no terminal UI is launched
