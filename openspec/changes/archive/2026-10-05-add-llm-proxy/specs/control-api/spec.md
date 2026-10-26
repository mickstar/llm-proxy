# Spec Delta

## Purpose

Provides the local HTTP surface that frontends — the terminal UI today, a GUI such as Wails later — use to inspect traffic and manage configuration, keeping frontend and server decoupled.

## ADDED Requirements

### Requirement: Expose configuration
The control API SHALL expose the full configuration for reading and SHALL support creating, updating, and deleting filters and model entries, and updating settings. Every mutation SHALL be validated before being applied, SHALL take effect without a server restart, and SHALL be persisted to the config file. Invalid mutations SHALL be rejected with an error identifying the problem.

#### Scenario: Read config
- **WHEN** a client calls the config read endpoint
- **THEN** the current configuration is returned as JSON

#### Scenario: Create a filter
- **WHEN** a client creates a filter with a valid pattern through the API
- **THEN** the filter is active for subsequent proxied requests and present in the config file on disk

#### Scenario: Invalid filter rejected
- **WHEN** a client creates a filter whose pattern fails to compile
- **THEN** the API responds 400 with an error naming the pattern problem and the config is unchanged

### Requirement: Expose the traffic log
The control API SHALL expose traffic log entries: a list of recent entries, full detail for a single entry by id, and a live server-sent-events stream that pushes each new entry as it is recorded.

#### Scenario: List recent traffic
- **WHEN** a client lists traffic entries after several proxied requests
- **THEN** the entries are returned newest-first with their summary fields

#### Scenario: Stream live traffic
- **WHEN** a client subscribes to the traffic stream and a new proxied exchange is recorded
- **THEN** the entry is pushed to the subscriber

### Requirement: Fetch models through the API
The control API SHALL expose an operation that fetches a model's metadata from OpenRouter and adds or updates it in the catalog, with the same behavior as the `fetch` CLI command.

#### Scenario: Fetch via API
- **WHEN** a client requests a fetch for `deepseek/deepseek-v4.1-flash` through the API and the model exists
- **THEN** the catalog gains the model and the response includes the fetched metadata

### Requirement: Test filter patterns
The control API SHALL provide a test operation that applies a supplied pattern and optional replacement against supplied sample text and returns the matches and resulting text, without saving anything.

#### Scenario: Test a pattern
- **WHEN** a client tests pattern `sk-\w+` against sample text containing `sk-live-abc123`
- **THEN** the response reports the match and, when a replacement is supplied, the resulting text

### Requirement: Bind locally by default
The control API SHALL listen on a loopback address by default.

#### Scenario: Default binding
- **WHEN** the server starts without a control listen address override
- **THEN** the control API is reachable only on the loopback interface
