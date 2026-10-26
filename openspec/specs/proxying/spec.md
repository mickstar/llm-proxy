# proxying Specification

## Purpose

Forwards OpenAI-compatible API traffic from local clients to OpenRouter transparently, for both streaming and non-streaming exchanges, while recording every exchange in the traffic log.

## Requirements

### Requirement: Forward OpenAI-compatible requests
The proxy SHALL accept HTTP requests on its configured proxy listen address and forward them to the configured OpenRouter base URL, preserving the method, path relative to the base URL, query parameters, request body, and request headers, except for the upstream credential injection, removal of hop-by-hop headers, and dropping `Accept-Encoding` so the proxy negotiates compression and decompresses upstream bodies itself. The proxy SHALL NOT otherwise translate or reshape requests. Upstream responses SHALL be returned to the client with their status, headers, and body unmodified.

#### Scenario: Non-streaming chat completion
- **WHEN** a client sends `POST /v1/chat/completions` with a JSON body naming a configured model
- **THEN** the request is forwarded to OpenRouter and the client receives the upstream status, headers, and body

#### Scenario: Other OpenAI endpoint paths
- **WHEN** a client requests any other path under `/v1/` other than the locally served model listing (e.g. `POST /v1/embeddings`)
- **THEN** the request is forwarded to the same path at OpenRouter

#### Scenario: Untouched body forwarded byte-for-byte
- **WHEN** a request body triggers no filter changes
- **THEN** the body forwarded to OpenRouter is byte-for-byte identical to the body the client sent

#### Scenario: Client headers reach upstream unchanged
- **WHEN** a client sends a custom `User-Agent` plus attribution headers (`X-Title`, `HTTP-Referer`)
- **THEN** the upstream receives those values unchanged, and a request that omits `User-Agent` forwards none

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

### Requirement: Pass through model ids
When `server.model_policy` is `passthrough`, the proxy SHALL forward any model id named in a request body that carries a chat, completion, embedding, or responses payload, whether or not the id appears in the model catalog.

#### Scenario: Unknown model forwarded under passthrough
- **WHEN** `model_policy` is `passthrough` and a chat completion request names model `some/unknown-model` that is not in the catalog
- **THEN** the proxy forwards the request to OpenRouter and returns the upstream response

### Requirement: Enforce the allowlist policy
When `server.model_policy` is `allowlist`, a request whose body names a model that is not present and enabled in the configured catalog SHALL be rejected before any upstream request is made, with an HTTP 404 response in the OpenAI error format that names the unknown model and states how to add it. Requests without a top-level `model` field SHALL be forwarded.

#### Scenario: Unknown model rejected under allowlist
- **WHEN** `model_policy` is `allowlist` and a chat completion request names model `some/unknown-model` that is not in the catalog
- **THEN** the proxy responds 404 without contacting OpenRouter, and the error message identifies the model

#### Scenario: Configured model forwarded
- **WHEN** a request names a model present and enabled in the catalog
- **THEN** the request is forwarded to OpenRouter under either policy

### Requirement: Halt after a block match
A block-filter match SHALL put the proxy into a halted state. While halted, every request on the proxy listener SHALL be answered with HTTP 403 in the OpenAI error format with code `proxy_halted`, without being forwarded, including the model listing.

#### Scenario: Halt stops all traffic
- **WHEN** a block filter matches and a later request is innocuous
- **THEN** the later request receives 403 with code `proxy_halted` and OpenRouter is not contacted

#### Scenario: Desktop notification on macOS
- **WHEN** the proxy halts on macOS
- **THEN** a notification naming the triggering filter is posted, containing no part of the matched value

#### Scenario: Model listing closed while halted
- **WHEN** the proxy is halted and a client requests `GET /v1/models`
- **THEN** the request receives 403

### Requirement: Keep halted until explicitly resumed
The halted state SHALL persist until resumed through the control API. Further requests, configuration reloads, and repeated triggers MUST NOT clear it, and the first breach's filter and timestamp SHALL be preserved. Requests refused while halted MUST NOT be recorded in the traffic log; the halt state SHALL count them instead.

#### Scenario: Resume restores service
- **WHEN** the halt is resumed through the control API
- **THEN** subsequent requests are forwarded again

#### Scenario: Refusals are counted, not logged
- **WHEN** requests arrive while the proxy is halted
- **THEN** the traffic log is unchanged and the refused count grows

#### Scenario: First cause is preserved
- **WHEN** two different filters would block while the proxy is already halted
- **THEN** the halt keeps the filter and timestamp of the first breach

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
