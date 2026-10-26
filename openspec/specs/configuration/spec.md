# configuration Specification

## Purpose

Defines how the whole system is configured: a single JSON file as the source of truth, validated on load, written atomically, and hot-reloaded when it changes.

## Requirements

### Requirement: Single JSON config file
All configuration — server listen addresses, OpenRouter base URL and credential, model catalog, and filter rules — SHALL be loaded from a single JSON file. The default path SHALL be `llm-proxy/config.json` under the user's config directory and SHALL be overridable with `--config <path>`.

#### Scenario: Default config path
- **WHEN** `llm-proxy` starts without `--config`
- **THEN** configuration is loaded from the default path in the user's config directory

#### Scenario: Config path override
- **WHEN** the tool is run with `--config /path/to/proxy.json`
- **THEN** configuration is loaded from that path instead of the default

### Requirement: Validate configuration
Configuration SHALL be validated when loaded and when modified: the JSON must parse, `server.model_policy` must be `passthrough` or `allowlist`, filters must define exactly one match source — an inline `pattern` or a `pattern_file` naming files whose verbatim contents are matched — inline patterns must compile, every file named by `pattern_file` must be readable, non-empty, at most 1 MiB, and matched by its glob, filter actions must be `block` or `replace`, `replace` filters must define a replacement, redaction names must be unique and non-empty, every redaction `pattern` must compile, every `template` must be non-empty, and model ids must be non-empty with non-negative context lengths and non-negative finite prices. On invalid configuration the server SHALL fail to start with an error identifying the config file and the offending field.

#### Scenario: Invalid pattern fails startup
- **WHEN** the config contains a filter with pattern `[unclosed`
- **THEN** startup fails with an error naming the config file and the filter

#### Scenario: Unreadable pattern file fails validation
- **WHEN** a filter sets `pattern_file` to a path that does not exist or a glob that matches no file
- **THEN** configuration validation fails with an error naming the filter and the path, and the file is not persisted

#### Scenario: Ambiguous match source rejected
- **WHEN** a filter defines both `pattern` and `pattern_file`, or neither
- **THEN** configuration validation fails with an error naming the filter and the conflicting fields

#### Scenario: Invalid redaction rejected
- **WHEN** the config contains a redaction with an uncompilable `pattern`, an empty `template`, or a duplicate name
- **THEN** configuration validation fails with an error naming the redaction and the offending field

#### Scenario: Invalid model price rejected
- **WHEN** a model sets `pricing.prompt`, `pricing.cache`, or `pricing.completion` to a non-numeric, negative, or infinite value
- **THEN** configuration validation fails with an error naming the model and the price field, and the file is not persisted

#### Scenario: Invalid model policy rejected
- **WHEN** the config sets `server.model_policy` to a value other than `passthrough` or `allowlist`
- **THEN** configuration validation fails with an error naming the field, and the file is not persisted

### Requirement: Credential source
`openrouter.api_key` SHALL accept either a literal secret or a macOS keychain reference of the form `{keychain:service/account}`. A reference SHALL be resolved on demand with the macOS keychain, and resolved values SHALL be cached so a read does not run on every proxied request. A resolved secret SHALL never be written to the config file. A malformed reference SHALL fail validation with an error naming `openrouter.api_key`; a reference whose item cannot be read SHALL fail at the point of use rather than being sent upstream unauthenticated. References SHALL only be read on macOS.

#### Scenario: Keychain reference stays out of the file
- **WHEN** the config sets `openrouter.api_key` to `{keychain:FireworksAI/fireworks-api-key}` and the keychain item exists
- **THEN** proxied requests carry the keychain secret in `Authorization` while the config file, `GET /api/config`, and `stats.json` contain only the reference

#### Scenario: Malformed reference rejected
- **WHEN** the config sets `openrouter.api_key` to `{keychain:FireworksAI}`
- **THEN** configuration validation fails with an error naming `api_key`

#### Scenario: Unreadable reference fails locally
- **WHEN** a proxied request arrives while the referenced keychain item cannot be read
- **THEN** the proxy answers 502 with an error naming the keychain item and does not contact the upstream

### Requirement: First-run default config
When no config file exists at the resolved path, the system SHALL create one containing safe defaults (loopback listen addresses, the `passthrough` model policy, empty model catalog, empty filter and redaction lists) and continue startup using it.

#### Scenario: First run
- **WHEN** `llm-proxy` is run on a machine with no config file
- **THEN** a default config file is created and the server starts with those defaults

### Requirement: Persist changes atomically
All writes to the config file SHALL be atomic — written to a temporary file in the same directory and renamed into place — so readers never observe a partial file.

#### Scenario: No partial reads
- **WHEN** the config file is being saved while another process reads it
- **THEN** the reader observes either the previous or the new complete file

### Requirement: Reload external changes
Changes to the config file made by anything other than the running server SHALL be detected and applied without a restart, and changes made through the control API SHALL be written back to the file.

#### Scenario: External edit applies live
- **WHEN** the config file is edited while the server runs, for example by `llm-proxy fetch`
- **THEN** the running server applies the new configuration without restart

#### Scenario: API change is persisted
- **WHEN** a filter is created through the control API
- **THEN** the config file on disk contains the new filter
