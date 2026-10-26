# console Specification

## Purpose

Native macOS console for inspecting and managing the proxy: a full-window companion to the menu-bar control center, built as a pure client of the control API.

## Requirements

### Requirement: Open the console from the panel
The menu-bar panel SHALL provide an Expand action that closes the panel and opens a resizable console window. While that window is open the app SHALL be a regular application (Dock tile and Cmd-Tab entry); closing the window SHALL return it to a menu-bar-only agent. The console SHALL close on Cmd-W and quit with the app.

#### Scenario: Expand
- **WHEN** the user activates Expand in the status-item panel
- **THEN** the panel closes, the console window opens focused, and the app becomes a regular app

#### Scenario: Return to agent mode
- **WHEN** the user closes the console window
- **THEN** the app returns to an accessory (menu-bar-only) app with no Dock tile

### Requirement: Overview
The console SHALL show a live overview with completion-token throughput, a recent-activity chart, total tokens and cost (including average cost per request), request/error/blocked counts, the last request's generation rate, per-model usage, and recent traffic. It SHALL refresh automatically while open.

#### Scenario: Live overview
- **WHEN** proxied usage is recorded while the overview is visible
- **THEN** throughput, totals, models, and recent traffic update without user action

### Requirement: Live traffic and inspector
The console SHALL list traffic newest-first from the control API and SHALL append entries from the SSE stream as they are recorded. Selecting an entry SHALL fetch its full detail and show method, path, status, duration, sizes, token split, cost, filter/redaction summaries, and request/response bodies. Body rendering SHALL bound the displayed text so a large captured body cannot stall the window, and SHALL disclose when the display is capped.

#### Scenario: Live entry appears
- **WHEN** a proxied exchange is recorded while the traffic view is open
- **THEN** it appears at the top of the table without manual refresh

#### Scenario: Inspect an entry
- **WHEN** the user selects a row
- **THEN** the inspector shows its metadata and pretty-printed bodies, with token counts and cost

#### Scenario: Large body
- **WHEN** a captured body exceeds the display limit
- **THEN** the inspector shows a bounded prefix, states that it is capped, and remains responsive

### Requirement: Manage filters natively
The console SHALL list filters and allow creating, editing, enabling, disabling, and deleting them. The editor SHALL accept a name, block/replace action, exactly one of pattern or pattern file, an optional replacement, and an enabled flag. A replace editor SHALL offer the TUI's `$R` suggestion. The console SHALL validate required and mutually exclusive fields before saving and SHALL surface server validation errors. It SHALL provide a debounced live test against sample text that reports match count, matched values, and the transformed result.

#### Scenario: Save a new filter
- **WHEN** the user fills a valid name, pattern, and action and saves
- **THEN** the filter is created through the control API and appears in the list

#### Scenario: Invalid filter
- **WHEN** both pattern and pattern file are supplied
- **THEN** saving is rejected with an inline explanation and the config is unchanged

#### Scenario: Live test
- **WHEN** the user types sample text that matches the current pattern
- **THEN** the test pane reports the matches and resulting text after a short debounce

#### Scenario: Toggle a filter
- **WHEN** the user disables an enabled filter
- **THEN** it is updated through the API and subsequent traffic is unaffected by it

### Requirement: Manage models
The console SHALL list catalog models with enabled state and per-million pricing, and SHALL allow creating a model manually, fetching metadata for an author/slug from OpenRouter, editing name, context length, prompt/cache/completion pricing, supported parameters, and enablement, and deleting an entry with confirmation.

#### Scenario: Fetch model metadata
- **WHEN** the user enters `deepseek/deepseek-v4.1-flash` and fetches
- **THEN** the model is added or updated in the catalog and selected in the editor

#### Scenario: Edit pricing
- **WHEN** the user changes the per-million prompt/completion prices and saves
- **THEN** the config stores the corresponding per-token rates and the list reflects them

### Requirement: Edit settings
The console SHALL edit proxy and control listen addresses, model policy, traffic log capacity, OpenRouter base URL, and API key through `PUT /api/settings`, with inline validation. It SHALL state that listener changes take effect after a proxy restart and that the API key is stored in plaintext.

#### Scenario: Save settings
- **WHEN** the user changes the model policy and saves
- **THEN** the control API persists it and the form reflects the stored config

### Requirement: Inspect redactions
The console SHALL list recorded redaction aliases with original value, rule, use count, and first-seen time, with search and refresh. It SHALL state that originals are held in memory only.

#### Scenario: Redaction recorded
- **WHEN** a proxied request matches a redaction rule
- **THEN** the alias and its original appear in the redactions view

### Requirement: Use only the control API
The console SHALL communicate exclusively through the control API over loopback and SHALL NOT import proxy internals, so it remains usable against a separately running server.

#### Scenario: Separate server
- **WHEN** the console runs with a control URL override
- **THEN** it reads and mutates that server through the same HTTP endpoints as the TUI
