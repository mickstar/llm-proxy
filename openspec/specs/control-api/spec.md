# control-api Specification

## Purpose

Provides the local HTTP surface that frontends — the terminal UI today, a GUI such as Wails later — use to inspect traffic and manage configuration, keeping frontend and server decoupled.

## Requirements

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

#### Scenario: Partial settings update preserves the model policy
- **WHEN** a client updates server settings without naming `model_policy`
- **THEN** the stored policy is retained while the supplied server fields are applied

### Requirement: Set the upstream credential
The control API SHALL provide an operation that sets the upstream credential and, optionally, the upstream base URL, without replacing any other setting. Both fields SHALL be optional so a key-only update preserves the stored base URL, and the change SHALL take effect without a restart and be persisted. A keychain reference SHALL be resolved before it is stored, and an unreadable or malformed reference SHALL be rejected without changing the configuration.

#### Scenario: Key-only update preserves the base URL
- **WHEN** a client sets only `api_key` on a proxy configured for `https://api.fireworks.ai/inference/v1`
- **THEN** the credential is stored and the base URL is unchanged

#### Scenario: Keychain reference accepted
- **WHEN** a client sets `api_key` to `{keychain:FireworksAI/fireworks-api-key}` and the keychain item is readable
- **THEN** the reference is stored as given — not the secret — and subsequent proxied requests use the resolved secret

#### Scenario: Unreadable reference rejected
- **WHEN** a client sets `api_key` to a reference naming a keychain item that does not exist
- **THEN** the API responds 400 identifying the item and the configuration is unchanged

### Requirement: Expose and clear the halted state
The control API SHALL report the halted state, including the filter that triggered it, when it began, and how many requests have been refused since, and SHALL provide an operation that clears it. The control API SHALL remain available while the proxy is halted.

#### Scenario: Halt is visible
- **WHEN** the proxy is halted and a client requests health
- **THEN** the response reports the halted state, the triggering filter, and the refusal count

#### Scenario: Resume through the API
- **WHEN** a client requests a resume
- **THEN** the halted state is cleared and the proxy forwards subsequent requests

### Requirement: Expose recorded redactions
The control API SHALL list every recorded redaction alias with its original value, producing rule, use count, and first-seen time.

#### Scenario: List aliases
- **WHEN** a client lists redactions after a proxied request applied one
- **THEN** the alias, original value, rule, and count are returned

### Requirement: Expose the traffic log
The control API SHALL expose traffic log entries: a list of recent entries, full detail for a single entry by id, and a live server-sent-events stream that pushes each new entry as it is recorded.

#### Scenario: List recent traffic
- **WHEN** a client lists traffic entries after several proxied requests
- **THEN** the entries are returned newest-first with their summary fields

#### Scenario: Stream live traffic
- **WHEN** a client subscribes to the traffic stream and a new proxied exchange is recorded
- **THEN** the entry is pushed to the subscriber

### Requirement: Expose usage metrics
The control API SHALL expose a metrics snapshot with all-time and per-model token and cost totals, trailing-window throughput, per-minute activity, in-flight requests, the last token-bearing request, any persistence error, and the current halt state.

#### Scenario: Read metrics
- **WHEN** a client requests the metrics endpoint after proxied usage has been recorded
- **THEN** the response includes the totals, the models' totals, completion tokens per second, and a nil-or-current halt state

### Requirement: Fetch models through the API
The control API SHALL expose an operation that fetches a model's metadata from OpenRouter and adds or updates it in the catalog, with the same behavior as the `fetch` CLI command.

#### Scenario: Fetch via API
- **WHEN** a client requests a fetch for `deepseek/deepseek-v4.1-flash` through the API and the model exists
- **THEN** the catalog gains the model and the response includes the fetched metadata

### Requirement: Test filter patterns
The control API SHALL provide a test operation that applies a supplied pattern or `pattern_file` and optional replacement against supplied sample text and returns the matches and resulting text, without saving anything.

#### Scenario: Test a pattern
- **WHEN** a client tests pattern `sk-\w+` against sample text containing `sk-live-abc123`
- **THEN** the response reports the match and, when a replacement is supplied, the resulting text

#### Scenario: Test a file-backed pattern
- **WHEN** a client tests `pattern_file` naming an existing file against sample text containing that file's contents
- **THEN** the response reports the match, and an error naming the path when the file cannot be read

#### Scenario: File-backed test saves nothing
- **WHEN** a client tests a `pattern_file` filter
- **THEN** the configuration is unchanged and the file's contents are not stored

### Requirement: Bind locally by default
The control API SHALL listen on a loopback address by default.

#### Scenario: Default binding
- **WHEN** the server starts without a control listen address override
- **THEN** the control API is reachable only on the loopback interface
