# Spec Delta

## Purpose

Maintains the set of models the user chose to proxy, serves the OpenAI model-listing endpoints from that set, and fills in model metadata by fetching it from OpenRouter.

## ADDED Requirements

### Requirement: Model catalog entries
Each catalog entry SHALL contain the OpenRouter model id, display name, context length, prompt and completion pricing, supported parameters, and an enabled flag. The catalog SHALL contain only user-configured entries.

#### Scenario: Entry round-trips through config
- **WHEN** the config contains a model entry for `deepseek/deepseek-v4.1-flash` with context length 163840
- **THEN** the catalog exposes that model with the configured metadata

### Requirement: Serve model listings locally
`GET /v1/models` on the proxy SHALL return exactly the enabled catalog entries in the OpenAI list-models response shape, and never models that are not configured. `GET /v1/models/{id}` SHALL return the entry for a configured model id and 404 for any other id.

#### Scenario: Only configured models listed
- **WHEN** the catalog contains two enabled models and a client calls `GET /v1/models`
- **THEN** the response contains exactly those two models and no others

#### Scenario: Model detail for unconfigured id
- **WHEN** a client calls `GET /v1/models/some/unknown-model` for a model not in the catalog
- **THEN** the proxy responds 404

### Requirement: Fetch model metadata from OpenRouter
`llm-proxy fetch <model-id>` SHALL retrieve the model's metadata from OpenRouter and add it to the config if absent, or update the existing entry if present. The entry SHALL include the model id, display name, context length, prompt and completion pricing, and supported parameters.

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
