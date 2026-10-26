# model-catalog Specification

## Purpose

Maintains the set of models the user pinned, serves the model endpoints from that set when the allowlist policy gates traffic, and fills in model metadata by fetching it from OpenRouter.

## Requirements

### Requirement: Model catalog entries
Each catalog entry SHALL contain the OpenRouter model id, display name, context length, prompt, optional cached-input, and completion pricing as per-token USD strings, supported parameters, and an enabled flag. An absent cached-input price SHALL bill cached prompt tokens at the prompt rate. The catalog SHALL contain only user-configured entries.

#### Scenario: Entry round-trips through config
- **WHEN** the config contains a model entry for `deepseek/deepseek-v4.1-flash` with context length 163840
- **THEN** the catalog exposes that model with the configured metadata

### Requirement: Serve the model listing according to policy
`GET /v1/models` SHALL answer from the enabled catalog entries when `server.model_policy` is `allowlist`, returning exactly those entries and never models that are not configured. When the policy is `passthrough`, `GET /v1/models` SHALL be forwarded to OpenRouter so the client sees the upstream catalog.

#### Scenario: Allowlist lists only configured models
- **WHEN** `model_policy` is `allowlist`, the catalog contains two enabled models, and a client calls `GET /v1/models`
- **THEN** the response contains exactly those two models and no others

#### Scenario: Passthrough proxies the upstream listing
- **WHEN** `model_policy` is `passthrough` and a client calls `GET /v1/models`
- **THEN** the request is forwarded to OpenRouter and the client receives the upstream listing unmodified

### Requirement: Serve model detail from the catalog
`GET /v1/models/{id}` SHALL answer from the local catalog under either policy, returning 404 for an id that is not configured and enabled.

#### Scenario: Model detail for unconfigured id
- **WHEN** a client calls `GET /v1/models/some/unknown-model` under either policy
- **THEN** the proxy responds 404

### Requirement: Fetch model metadata from OpenRouter
`llm-proxy fetch <model-id>` SHALL retrieve the model's metadata from OpenRouter and add it to the config if absent, or update the existing entry if present. The entry SHALL include the model id, display name, context length, prompt and completion pricing, and supported parameters; cached-input pricing is not exposed by the metadata endpoint and is set by hand.

#### Scenario: Fetch adds a new model
- **WHEN** `llm-proxy fetch deepseek/deepseek-v4.1-flash` is run and the model exists on OpenRouter
- **THEN** the config gains an entry for that model populated with context length, pricing, and supported parameters from OpenRouter

#### Scenario: Fetch updates an existing entry
- **WHEN** fetch is run for a model already in the catalog
- **THEN** the entry's metadata is refreshed and the command reports the update

#### Scenario: Unknown model id
- **WHEN** `llm-proxy fetch nonexistent/fake-model` is run
- **THEN** the command fails with a clear error and the config is unchanged

#### Scenario: Invalid model id format
- **WHEN** `llm-proxy fetch gpt-4` is run with an id not in `author/slug` form
- **THEN** the command fails with a clear error and the config is unchanged

### Requirement: Fetched changes are live
A model added or updated by fetch SHALL be usable by the proxy without a server restart.

#### Scenario: Fetch then immediately use
- **WHEN** a model is fetched while the server is running and a client then sends a chat completion naming that model
- **THEN** the request is forwarded to OpenRouter
