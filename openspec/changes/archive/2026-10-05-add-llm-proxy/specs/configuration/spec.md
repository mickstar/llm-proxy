# Spec Delta

## Purpose

Defines how the whole system is configured: a single JSON file as the source of truth, validated on load, written atomically, and hot-reloaded when it changes.

## ADDED Requirements

### Requirement: Single JSON config file
All configuration — server listen addresses, OpenRouter base URL and credential, model catalog, and filter rules — SHALL be loaded from a single JSON file. The default path SHALL be `llm-proxy/config.json` under the user's config directory and SHALL be overridable with `--config <path>`.

#### Scenario: Default config path
- **WHEN** `llm-proxy` starts without `--config`
- **THEN** configuration is loaded from the default path in the user's config directory

#### Scenario: Config path override
- **WHEN** the tool is run with `--config /path/to/proxy.json`
- **THEN** configuration is loaded from that path instead of the default

### Requirement: Validate configuration
Configuration SHALL be validated when loaded and when modified: the JSON must parse, filter patterns must compile, filter actions must be `block` or `replace`, and `replace` filters must define a replacement. On invalid configuration the server SHALL fail to start with an error identifying the config file and the offending field.

#### Scenario: Invalid pattern fails startup
- **WHEN** the config contains a filter with pattern `[unclosed`
- **THEN** startup fails with an error naming the config file and the filter

### Requirement: First-run default config
When no config file exists at the resolved path, the system SHALL create one containing safe defaults (loopback listen addresses, empty model catalog, empty filter list) and continue startup using it.

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
