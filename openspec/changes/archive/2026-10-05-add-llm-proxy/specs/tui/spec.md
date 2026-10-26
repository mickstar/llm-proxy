# Spec Delta

## Purpose

Terminal interface for watching proxied traffic live and managing filters, models, and settings through the control API, as the first frontend in the server/frontend separation.

## ADDED Requirements

### Requirement: Live traffic view
The TUI SHALL display a live-updating list of proxied requests showing at least time, model, endpoint, status, duration, and filter outcome, and SHALL allow opening a selected entry to inspect its request and response bodies.

#### Scenario: Live updates
- **WHEN** a new exchange is recorded while the traffic view is open
- **THEN** the list updates to include the new entry without user action

#### Scenario: Inspect an entry
- **WHEN** the user opens a recorded entry
- **THEN** the filtered request body and the response body are displayed

### Requirement: Manage filters
The TUI SHALL allow creating, editing, enabling, disabling, and deleting filters, and SHALL include a test pane that applies the edited pattern and replacement against sample text and shows the matches and resulting text.

#### Scenario: Draft and test a filter
- **WHEN** the user drafts a pattern with sample text in the test pane
- **THEN** matches and the resulting text are shown before the filter is saved

#### Scenario: Disable a filter
- **WHEN** the user disables an enabled filter
- **THEN** subsequent requests are no longer affected by that filter

### Requirement: Manage models
The TUI SHALL allow listing configured models, adding a model by fetching its metadata from OpenRouter, editing entries, and removing entries.

#### Scenario: Add a model
- **WHEN** the user adds model id `deepseek/deepseek-v4.1-flash` in the TUI and the fetch succeeds
- **THEN** the model appears in the list with its fetched metadata

### Requirement: Edit settings
The TUI SHALL allow editing server settings and the OpenRouter credential, saved through the control API.

#### Scenario: Change a setting
- **WHEN** the user edits the OpenRouter API key and saves
- **THEN** the new credential is persisted and used for subsequent upstream requests

### Requirement: Server lifecycle
The default `llm-proxy` invocation SHALL start the server and open the TUI attached to it. If a server is already reachable on the configured control address, the TUI SHALL attach to it instead of starting a second one.

#### Scenario: Default start
- **WHEN** `llm-proxy` is run with no subcommand
- **THEN** the server starts and the TUI opens connected to it

#### Scenario: Attach to a running server
- **WHEN** `llm-proxy` is run while a server is already running on the configured control address
- **THEN** no second server starts and the TUI attaches to the existing one
